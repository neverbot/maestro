package markdown

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Diffs are computed on read and never stored (spec §3). Snapshots are
// full, so a diff is always available and never on the critical path of
// a write; storing them would make every past version depend on every
// link of a chain being right.
//
// **No dependency.** A line diff with a fallback is a hundred lines of
// arithmetic, and "every dependency justifies its presence" (claude.md)
// is not satisfied by saving them.

// diffContext is how many unchanged lines are shown either side of a
// change, and it is also how far the common-prefix and common-suffix
// trim below backs off: trimming to the exact first and last difference
// would leave a hunk with no context at all, which is a diff a human
// cannot read and a patch no tool can apply.
const diffContext = 3

// lcsLimit bounds the dynamic-programming table one diff will build,
// per side, after the common prefix and suffix have been trimmed.
//
// **Measured, not guessed.** BenchmarkUnifiedDiff, on an Apple M1:
// 4,000 lines rewritten on both sides costs 49 ms and 66.7 MB of
// allocation for one call, which is the worst case this bound admits and
// the cost of one "compare these two versions" click on a document
// rewritten end to end. One line past the bound the coarse fallback
// costs 0.5 ms and 0.37 MB, and a one-line edit inside a 40,000-line
// document costs 1.1 ms and 1.3 MB — almost all of it splitting the two
// bodies into lines, not diffing them.
//
// 66.7 MB per concurrent call is the number that sets the bound, and it
// is the reason it is 4,000 rather than the ~20,000 lines a 1 MiB body
// holds: the table is O(n*m) cells of int32, so 20,000 per side is 25
// times the cells — 1.6 GB and over a second — which is not a thing an
// HTTP handler may do, and a handful of concurrent callers asking for it
// is an out-of-memory kill rather than a slow page. Recorded rather than
// hidden: nothing here limits how many diffs run at once, so the honest
// worst case for an instance is 66.7 MB times the number of simultaneous
// comparison requests. If that ever bites, the fix is a semaphore around
// this call and not a smaller table.
//
// The trim is what makes the bound generous rather than tight. It
// applies to the *difference*, not to the document, so a one-line edit
// in a 40,000-line script builds a table of 7 by 7
// (TestAHugeButLocalChangeIsStillComputedLineByLine). Only a document
// rewritten from top to bottom reaches the limit, and past it the answer
// is one coarse hunk saying the whole body was replaced, with
// DiffResult.Coarse saying so out loud rather than pretending the
// line-by-line answer was computed.
const lcsLimit = 4000

// DiffResult is one comparison between two versions of a document.
//
// **Coarse is not decoration.** A caller that renders Unified without
// reading it would show "the whole document was replaced" for two
// versions that differ by a word, and blame the author. It is a field of
// its own because a client cannot reconstruct from the diff itself why
// the diff looks like that. TestAHugeChangeComesBackCoarseAndSaysSo
// pins the true case and every other diff test the false one.
type DiffResult struct {
	Path        string
	FromVersion int32
	ToVersion   int32
	Unified     string
	Coarse      bool
}

// Diff compares two versions of one document and returns a unified diff.
//
// The versions may be given in either order; the diff reads from the
// first to the second, so `from: 3, to: 1` is the reverse of
// `from: 1, to: 3` and both are legitimate questions
// (TestDiffReadsInTheDirectionItWasAsked).
//
// It reads both sides through ReadVersion, so the project filter, the
// path grammar and the two not_founds are the ones that call already
// establishes; nothing here addresses a row by id.
func (s *Service) Diff(ctx context.Context, projectID uuid.UUID, path string, from, to int32) (DiffResult, error) {
	fromRow, err := s.ReadVersion(ctx, projectID, path, from)
	if err != nil {
		return DiffResult{}, versionArgument(err, "from_version")
	}
	toRow, err := s.ReadVersion(ctx, projectID, path, to)
	if err != nil {
		return DiffResult{}, versionArgument(err, "to_version")
	}
	unified, coarse := unifiedDiff(
		fmt.Sprintf("%s@%d", path, from), fmt.Sprintf("%s@%d", path, to),
		fromRow.BodyMd, toRow.BodyMd)
	return DiffResult{
		Path: path, FromVersion: from, ToVersion: to, Unified: unified, Coarse: coarse,
	}, nil
}

// versionArgument re-points a MissingError at the argument the caller
// actually sent. ReadVersion reports at `version`, which is right for
// its own tool and wrong here, where there are two of them and a caller
// that mistyped one needs to know which.
// TestDiffNamesWhichVersionArgumentWasWrong pins both.
//
// Only a miss at `version` moves. A missing *document* keeps its own
// path, because "check your version number" is the wrong instruction
// when the document itself is not there
// (TestDiffingAnotherGamesDocumentIsNotFoundAtThePath).
func versionArgument(err error, path string) error {
	var missing *MissingError
	if errors.As(err, &missing) && missing.Path == "version" {
		return &MissingError{Path: path, Message: missing.Message}
	}
	return err
}

// unifiedDiff renders a line diff in unified format with diffContext
// lines of context, and reports whether it had to fall back to a coarse
// answer.
func unifiedDiff(fromName, toName, fromBody, toBody string) (string, bool) {
	from := splitLines(fromBody)
	to := splitLines(toBody)

	// Trim the common head and tail first: two versions of a long
	// document usually differ in one place, so this is what keeps the
	// table small enough to build at all.
	head := 0
	for head < len(from) && head < len(to) && from[head] == to[head] {
		head++
	}
	tail := 0
	for tail < len(from)-head && tail < len(to)-head &&
		from[len(from)-1-tail] == to[len(to)-1-tail] {
		tail++
	}

	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n+++ %s\n", fromName, toName)
	if head == len(from) && head == len(to) {
		// Identical, including the two-versions-of-one-body case a
		// revert produces. No hunks and not coarse.
		return b.String(), false
	}

	// Back the trim off by the context width, so the lines either side
	// of the change survive into the hunk. Without this the trim eats
	// exactly the context the format exists to show, and every hunk is
	// a bare pair of changed lines.
	head = max(0, head-diffContext)
	tail = max(0, tail-diffContext)
	midFrom := from[head : len(from)-tail]
	midTo := to[head : len(to)-tail]

	if len(midFrom) > lcsLimit || len(midTo) > lcsLimit {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", head+1, len(midFrom), head+1, len(midTo))
		for _, line := range midFrom {
			fmt.Fprintf(&b, "-%s\n", line)
		}
		for _, line := range midTo {
			fmt.Fprintf(&b, "+%s\n", line)
		}
		return b.String(), true
	}

	writeHunks(&b, diffOps(midFrom, midTo), head)
	return b.String(), false
}

// op is one line of the diff: ' ' kept, '-' removed, '+' added.
type op struct {
	kind byte
	line string
}

// diffOps is the classic LCS table, walked back into a line script.
func diffOps(from, to []string) []op {
	n, m := len(from), len(to)
	table := make([][]int32, n+1)
	for i := range table {
		table[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if from[i] == to[j] {
				table[i][j] = table[i+1][j+1] + 1
			} else if table[i+1][j] >= table[i][j+1] {
				table[i][j] = table[i+1][j]
			} else {
				table[i][j] = table[i][j+1]
			}
		}
	}
	var ops []op
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case from[i] == to[j]:
			ops = append(ops, op{' ', from[i]})
			i, j = i+1, j+1
		case table[i+1][j] >= table[i][j+1]:
			ops = append(ops, op{'-', from[i]})
			i++
		default:
			ops = append(ops, op{'+', to[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, op{'-', from[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, op{'+', to[j]})
	}
	return ops
}

// writeHunks groups a line script into unified hunks with diffContext
// lines of context, offset by the common head the caller trimmed.
func writeHunks(b *strings.Builder, ops []op, offset int) {
	keep := make([]bool, len(ops))
	for i, o := range ops {
		if o.kind == ' ' {
			continue
		}
		for j := max(0, i-diffContext); j <= min(len(ops)-1, i+diffContext); j++ {
			keep[j] = true
		}
	}
	fromLine, toLine := offset+1, offset+1
	for i := 0; i < len(ops); {
		if !keep[i] {
			if ops[i].kind != '+' {
				fromLine++
			}
			if ops[i].kind != '-' {
				toLine++
			}
			i++
			continue
		}
		start := i
		startFrom, startTo := fromLine, toLine
		var fromCount, toCount int
		for i < len(ops) && keep[i] {
			if ops[i].kind != '+' {
				fromLine++
				fromCount++
			}
			if ops[i].kind != '-' {
				toLine++
				toCount++
			}
			i++
		}
		fmt.Fprintf(b, "@@ -%d,%d +%d,%d @@\n", startFrom, fromCount, startTo, toCount)
		for _, o := range ops[start:i] {
			fmt.Fprintf(b, "%c%s\n", o.kind, o.line)
		}
	}
}

// splitLines splits a body into lines, dropping the empty element a
// trailing newline produces so that a body ending in "\n" and one that
// does not are not reported as differing by a phantom line.
// TestADocumentThatDoesNotEndInANewlineDiffsAgainstOneThatDoes pins it.
//
// The consequence, stated rather than hidden: this diff cannot show a
// change that is *only* the presence or absence of a trailing newline.
// Git spells that "\ No newline at end of file"; here the two bodies
// compare equal, and the document itself still stores them byte for
// byte, so nothing is lost — only unshown.
func splitLines(body string) []string {
	if body == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(body, "\n"), "\n")
}
