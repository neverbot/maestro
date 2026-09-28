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
const diffContext = 3

// lcsLimit bounds the dynamic-programming table one diff will build,
// per side, after the common prefix and suffix have been trimmed.
const lcsLimit = 1000

// DiffResult is one comparison between two versions of a document.
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
// actually sent. ReadVersion reports at `version`, which is right for its
// own tool and wrong here, where there are two of them and a caller that
// mistyped one needs to know which. TestDiffArea's "diff names which
// version argument was wrong" case pins both.
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
// trailing newline produces so that a body ending in "\n" and one that does
// not are not reported as differing by a phantom line. TestDiffArea's "a
// document that does not end in a newline diffs against one that does" case
// pins it.
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
