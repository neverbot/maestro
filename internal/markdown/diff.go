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
// **What the bound costs at its own limit.** The table is (n+1)*(m+1)
// cells of int32, so a diff at the limit on both sides holds
// 1001*1001*4 bytes = 4.0 MB, and BenchmarkUnifiedDiff on an Apple M1
// measures the whole call at **4.3 ms and 4.37 MB of allocation** for
// 1,000 lines rewritten on both sides. That is the worst case this
// bound admits: one "compare these two versions" click on a document
// rewritten end to end. One line past the bound the coarse fallback
// costs 0.14 ms and 88 KB, and a one-line edit inside a 40,000-line
// document costs 1.1 ms and 1.3 MB -- almost all of it splitting the
// two bodies into lines, not diffing them.
//
// **Why 1,000 and not more.** The bound is a memory bound, and the
// figure that matters is not one call but several: nothing here limits
// how many diffs run at once, and Diff is mounted for agents and for
// browsers, the two consumers that fan out. The table is quadratic, so
// the bound is the only thing standing between a handful of concurrent
// comparisons and an out-of-memory kill on the small self-hosted box
// this project targets. At 1,000 that worst case is 4.37 MB per call,
// so ten at once is 44 MB -- an expense. The previous bound of 4,000
// was 66.7 MB per call (measured, same benchmark: 67 ms), so ten at
// once was 670 MB -- a killed process. The quadratic is why the
// numbers are so far apart for a 4x change in the constant, and it is
// why raising this constant is never a small decision: doubling it
// quadruples the memory.
//
// **What is lost is nothing a caller could have used.** Past the bound
// the answer is one coarse hunk saying the whole body was replaced,
// with DiffResult.Coarse saying so out loud. A document that still
// differs by more than 1,000 lines *after* its common prefix and suffix
// are trimmed has been rewritten, not edited, and "this was rewritten"
// is a true answer rather than a degraded one -- nobody reads a
// 2,000-line unified diff line by line either.
//
// The trim is what makes the bound generous rather than tight. It
// applies to the *difference*, not to the document, so a one-line edit
// in a 40,000-line script builds a table of 7 by 7
// (TestAHugeButLocalChangeIsStillComputedLineByLine). Only a document
// rewritten from top to bottom reaches the limit.
const lcsLimit = 1000

// DiffResult is one comparison between two versions of a document.
//
// **Coarse is not decoration.** A caller that renders Unified without
// reading it would show "the whole document was replaced" for two
// versions that differ by a word, and blame the author. It is a field of
// its own because a client cannot reconstruct from the diff itself why
// the diff looks like that. TestAHugeChangeComesBackCoarseAndSaysSo
// pins the true case and every other diff test the false one.
//
// **FromDeleted and ToDeleted are not decoration either**, and they were
// added by the sub-project's end-to-end run rather than designed in. A
// delete appends a tombstone version carrying the document exactly as it
// stood, so the last live version and the tombstone hold the same body
// and the diff between them is empty — the same answer, byte for byte,
// as diffing a version against itself. Without these two bools nothing
// in this result tells a caller that a deletion is what it is looking
// at, and the reading view turned that empty diff into the sentence
// "These two versions are identical", which is false.
// TestADiffAcrossATombstoneSaysWhichSideIsDeleted pins all three cases:
// onto a tombstone, back off one, and between two live versions.
//
// They are two fields rather than one "spans a delete" flag because the
// comparison reads in whichever direction it was asked, and a deletion
// and a resurrection are different events to show.
type DiffResult struct {
	Path        string
	FromVersion int32
	ToVersion   int32
	Unified     string
	Coarse      bool
	FromDeleted bool
	ToDeleted   bool
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
		FromDeleted: fromRow.Deleted, ToDeleted: toRow.Deleted,
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

	// The coarse fallback below prints its whole range as changed, so it
	// is checked and rendered against the *raw* trim — head and tail as
	// they stand, common lines excluded. Backing them off by diffContext
	// first (as the fine path needs, below) would hand the fallback up
	// to three genuinely identical lines at each end and it would print
	// them as both removed and added: arithmetically consistent with a
	// header that counts what it prints, but a line no one changed has
	// no business under a '-' or a '+'. A coarse hunk already says "the
	// whole document was replaced"; it does not need its own context.
	if len(from)-head-tail > lcsLimit || len(to)-head-tail > lcsLimit {
		coarseFrom, coarseTo := from[head:len(from)-tail], to[head:len(to)-tail]
		fmt.Fprintf(&b, "@@ -%s +%s @@\n",
			hunkRange(head+1, len(coarseFrom)), hunkRange(head+1, len(coarseTo)))
		for _, line := range coarseFrom {
			fmt.Fprintf(&b, "-%s\n", line)
		}
		for _, line := range coarseTo {
			fmt.Fprintf(&b, "+%s\n", line)
		}
		return b.String(), true
	}

	// Back the trim off by the context width, so the lines either side
	// of the change survive into the hunk. Without this the trim eats
	// exactly the context the format exists to show, and every hunk is
	// a bare pair of changed lines. Only the fine path below needs this:
	// diffOps/writeHunks tell changed lines from context themselves, so
	// backed-off common lines are printed as context (' '), never as
	// '-'/'+' the way the coarse path above would have printed them.
	head = max(0, head-diffContext)
	tail = max(0, tail-diffContext)
	midFrom := from[head : len(from)-tail]
	midTo := to[head : len(to)-tail]

	writeHunks(&b, diffOps(midFrom, midTo), head)
	return b.String(), false
}

// hunkRange renders one half of a unified hunk header (the "1,3" or
// "0,0" in "@@ -1,3 +0,0 @@").
//
// **A zero-length range is numbered by the line before it, not the line
// after.** That is the unified format's own rule — GNU diff writes
// "@@ -0,0 +1,3 @@" for a file created from nothing, never "@@ -1,0" —
// and start is always the first line the *non-empty* side would have
// occupied, one past where a caller trimming context already stands.
// Printing start verbatim when count is 0 claims the empty range
// follows a line that does not exist on an empty side (line 1 of a body
// with no line 1), and anything positioning by that number — patch, a
// renderer jumping to a hunk — lands one line high.
// TestAnAddedDocumentsDiffNumbersTheEmptySideByGNURules and
// TestADeletedDocumentsDiffNumbersTheEmptySideByGNURules pin both
// directions against GNU diff's own output.
func hunkRange(start, count int) string {
	if count == 0 {
		return fmt.Sprintf("%d,0", start-1)
	}
	return fmt.Sprintf("%d,%d", start, count)
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
		fmt.Fprintf(b, "@@ -%s +%s @@\n", hunkRange(startFrom, fromCount), hunkRange(startTo, toCount))
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

// MaxDiffLines is lcsLimit under a name internal/web can quote, so
// docs.diff's tool description states the comparison bound rather than
// repeating the number — a description promising a bound the code has
// moved off is the defect this project has produced most often, and one
// constant with two names cannot drift. It says the same thing lcsLimit
// does, from the outside: past this many lines per side, after the
// common prefix and suffix are trimmed, the answer is one coarse hunk
// with DiffResult.Coarse set.
const MaxDiffLines = lcsLimit
