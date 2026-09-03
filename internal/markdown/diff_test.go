package markdown_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/markdown"
)

func TestADiffShowsOnlyWhatChanged(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	before := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n"
	after := "one\ntwo\nthree\nfour\nFIVE\nsix\nseven\neight\nnine\nten\n"
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: before, ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: after, ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}

	got, err := svc.Diff(ctx, game, "bible", 1, 2)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if got.Coarse {
		t.Fatal("a one-line change must not come back coarse")
	}
	if got.Path != "bible" || got.FromVersion != 1 || got.ToVersion != 2 {
		t.Fatalf("result = %+v, want it to name what it compared", got)
	}
	for _, want := range []string{"--- bible@1\n", "+++ bible@2\n", "-five", "+FIVE", "@@ "} {
		if !strings.Contains(got.Unified, want) {
			t.Fatalf("diff %q is missing %q", got.Unified, want)
		}
	}
	// Three lines of context each side, and nothing beyond it.
	if strings.Contains(got.Unified, "one") {
		t.Fatalf("diff %q reaches past three lines of context", got.Unified)
	}
	// The hunk header counts the lines it actually carries: four context
	// lines (two above, two below the edit sits between) plus the pair.
	if !strings.Contains(got.Unified, "@@ -2,7 +2,7 @@") {
		t.Fatalf("diff %q does not place its hunk at the changed line", got.Unified)
	}
}

func TestDiffingAVersionAgainstItselfIsEmpty(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	writeVersions(t, svc, game, "bible", 2)

	got, err := svc.Diff(ctx, game, "bible", 2, 2)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if strings.Contains(got.Unified, "@@") {
		t.Fatalf("diff = %q, want no hunks", got.Unified)
	}
	if got.Coarse {
		t.Fatal("an empty diff is not a coarse one")
	}
}

func TestDiffReadsInTheDirectionItWasAsked(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	writeVersions(t, svc, game, "bible", 2)

	forward, err := svc.Diff(ctx, game, "bible", 1, 2)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	backward, err := svc.Diff(ctx, game, "bible", 2, 1)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(forward.Unified, "+v2") || !strings.Contains(backward.Unified, "+v1") {
		t.Fatalf("forward = %q, backward = %q: the direction must follow the arguments",
			forward.Unified, backward.Unified)
	}
}

// TestADiffOfAnAddedAndARemovedLineCountsBothSides pins the hunk header
// arithmetic in the one case where the two sides differ in length: a
// header that counted the wrong side renders as a broken patch in every
// tool that reads unified diffs.
func TestADiffOfAnAddedAndARemovedLineCountsBothSides(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "a\nb\nc\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "a\nb\nc\nd\ne\n", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, err := svc.Diff(ctx, game, "bible", 1, 2)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(got.Unified, "@@ -1,3 +1,5 @@") {
		t.Fatalf("diff %q must count three lines before and five after", got.Unified)
	}
	if !strings.Contains(got.Unified, "+d\n+e\n") {
		t.Fatalf("diff %q must show both added lines", got.Unified)
	}
}

// TestAnAddedDocumentsDiffNumbersTheEmptySideByGNURules and
// TestADeletedDocumentsDiffNumbersTheEmptySideByGNURules pin hunkRange
// against GNU diff -U3's own output for the one case that discriminates
// it from printing the count verbatim: a completely empty side. `diff
// -U3 empty full` prints "@@ -0,0 +1,3 @@", never "@@ -1,0"; the from
// side of an empty-to-full diff has no line 1 to be "after", so it is
// numbered by the line before the range, one lower than the count of
// lines on that side would otherwise suggest.
func TestAnAddedDocumentsDiffNumbersTheEmptySideByGNURules(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "a\nb\nc\n", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, err := svc.Diff(ctx, game, "bible", 1, 2)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if got.Coarse {
		t.Fatal("a three-line body is not a coarse case")
	}
	if !strings.Contains(got.Unified, "@@ -0,0 +1,3 @@") {
		t.Fatalf("diff %q must number the empty from-side by GNU's rule (@@ -0,0 +1,3 @@)", got.Unified)
	}
}

func TestADeletedDocumentsDiffNumbersTheEmptySideByGNURules(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "a\nb\nc\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, err := svc.Diff(ctx, game, "bible", 1, 2)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if got.Coarse {
		t.Fatal("a three-line body is not a coarse case")
	}
	if !strings.Contains(got.Unified, "@@ -1,3 +0,0 @@") {
		t.Fatalf("diff %q must number the empty to-side by GNU's rule (@@ -1,3 +0,0 @@)", got.Unified)
	}
}

// TestADocumentThatDoesNotEndInANewlineDiffsAgainstOneThatDoes pins
// splitLines' trailing-newline rule: without it the two bodies differ by
// a phantom empty line and every such pair reads as a two-line change.
func TestADocumentThatDoesNotEndInANewlineDiffsAgainstOneThatDoes(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "only\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "only", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, err := svc.Diff(ctx, game, "bible", 1, 2)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if strings.Contains(got.Unified, "@@") {
		t.Fatalf("diff = %q, want no hunks: only the trailing newline moved", got.Unified)
	}
}

func TestAHugeChangeComesBackCoarseAndSaysSo(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	before := strings.Repeat("a\n", 5000)
	after := strings.Repeat("b\n", 5000)
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: before, ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: after, ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}

	got, err := svc.Diff(ctx, game, "bible", 1, 2)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !got.Coarse {
		t.Fatal("a 5,000-line rewrite must report itself coarse rather than pretending")
	}
	// A coarse answer is still an answer: every line of both sides is
	// there, under one hunk.
	if strings.Count(got.Unified, "\n-a") != 5000 || strings.Count(got.Unified, "\n+b") != 5000 {
		t.Fatal("a coarse diff still carries both whole sides")
	}
}

// TestACoarseDiffDoesNotReprintItsCommonHeadAndTailAsChanged pins that
// the coarse fallback does not carry the up-to-three lines of context
// the fine path's trim backs off. Those lines are genuinely identical on
// both sides; printing them under both '-' and '+' is arithmetically
// consistent with a header that counts what it prints, but it is not a
// change and a coarse hunk does not need context of its own.
func TestACoarseDiffDoesNotReprintItsCommonHeadAndTailAsChanged(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	commonHead := "ctx1\nctx2\nctx3\n"
	commonTail := "ctx4\nctx5\nctx6\n"
	before := commonHead + strings.Repeat("a\n", 5000) + commonTail
	after := commonHead + strings.Repeat("b\n", 5000) + commonTail
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: before, ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: after, ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}

	got, err := svc.Diff(ctx, game, "bible", 1, 2)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !got.Coarse {
		t.Fatal("a 5,000-line rewrite must report itself coarse rather than pretending")
	}
	if strings.Count(got.Unified, "\n-a") != 5000 || strings.Count(got.Unified, "\n+b") != 5000 {
		t.Fatal("a coarse diff still carries both whole sides")
	}
	for _, line := range []string{"ctx1", "ctx2", "ctx3", "ctx4", "ctx5", "ctx6"} {
		if strings.Contains(got.Unified, "-"+line) || strings.Contains(got.Unified, "+"+line) {
			t.Fatalf("diff %q reprints the common line %q as changed", got.Unified, line)
		}
	}
}

// TestAHugeButLocalChangeIsStillComputedLineByLine is the other half of
// the bound: the table is built over what survives the common prefix and
// suffix, so a one-line edit in the middle of a very long document is
// fine and only a genuinely large *difference* falls back.
func TestAHugeButLocalChangeIsStillComputedLineByLine(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	head := strings.Repeat("a\n", 20000)
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: head + "before\n" + head, ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: head + "after\n" + head, ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, err := svc.Diff(ctx, game, "bible", 1, 2)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if got.Coarse {
		t.Fatal("a one-line edit in a 40,000-line document is not a coarse case")
	}
	if !strings.Contains(got.Unified, "-before") || !strings.Contains(got.Unified, "+after") {
		t.Fatalf("diff %q does not show the one line that moved", got.Unified)
	}
	if strings.Count(got.Unified, "\n") > 20 {
		t.Fatalf("diff carries %d lines for a one-line edit", strings.Count(got.Unified, "\n"))
	}
}

func TestDiffNamesWhichVersionArgumentWasWrong(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	writeVersions(t, svc, game, "bible", 2)

	_, err := svc.Diff(ctx, game, "bible", 1, 9)
	requireMissing(t, err, "to_version", `the document at "bible" has no version 9`)

	_, err = svc.Diff(ctx, game, "bible", 9, 1)
	requireMissing(t, err, "from_version", `the document at "bible" has no version 9`)
}

func TestDiffingAnotherGamesDocumentIsNotFoundAtThePath(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	azeroth := newGame(t, pool, "azeroth")
	outland := newGame(t, pool, "outland")
	writeVersions(t, svc, azeroth, "bible", 2)

	_, err := svc.Diff(ctx, outland, "bible", 1, 2)
	if !errors.Is(err, markdown.ErrNotFound) {
		t.Fatalf("want not_found, got %v", err)
	}
	// A missing *document* stays reported at `path`: versionArgument
	// re-points only the miss ReadVersion raises at `version`, and
	// sending a caller to check its version number when the document
	// itself is not there would be the wrong instruction.
	requireMissing(t, err, "path", `this game has no document at "bible"`)
}

// TestADiffAcrossATombstoneSaysWhichSideIsDeleted pins the defect the
// prose sub-project's end-to-end run found, and it is the same class of
// defect that run exists for: a state change that is stored, is readable
// through one call, and is invisible through the one a caller actually
// uses to ask "what changed between these two versions".
//
// A delete appends a tombstone version carrying the document exactly as
// it stood (Delete's own comment says so, and
// TestATombstonesBodyIsStillReadableAsAVersion pins it). So the body of
// the last live version and the body of the tombstone are identical, and
// a diff between them is empty — byte for byte the same answer as
// diffing a version against itself. Nothing on DiffResult told the two
// apart, and the reading view's own describeComparison turned that empty
// diff into the sentence "These two versions are identical", which is a
// false statement about a comparison that spans a deletion.
//
// FromDeleted and ToDeleted are the two bools that make the difference
// visible, and they are separate fields rather than one "spans a delete"
// flag because a comparison can run in either direction: reverting a
// deletion and deleting are different events and a client renders them
// differently.
func TestADiffAcrossATombstoneSaysWhichSideIsDeleted(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "notes/scrapped", Content: "the kobold arc\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "notes/scrapped", Message: "cut", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	across, err := svc.Diff(ctx, game, "notes/scrapped", 1, 2)
	if err != nil {
		t.Fatalf("Diff across the tombstone: %v", err)
	}
	// The premise: the two bodies really are identical, so the unified
	// text cannot be what tells a caller a deletion happened.
	for _, line := range strings.Split(across.Unified, "\n") {
		if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") {
			continue // the two file headers, which every diff carries
		}
		if strings.HasPrefix(line, "-") || strings.HasPrefix(line, "+") {
			t.Fatalf("the tombstone changed the body after all; this test's premise is gone:\n%s", across.Unified)
		}
	}
	if across.FromDeleted {
		t.Fatal("version 1 is a live version and the diff says it is deleted")
	}
	if !across.ToDeleted {
		t.Fatal("a diff onto a tombstone does not say the document was deleted; " +
			"an empty unified diff is all the caller gets, and it reads as 'nothing changed'")
	}

	// And the opposite direction, which is a resurrection rather than a
	// deletion and must not be reported as the same thing.
	back, err := svc.Diff(ctx, game, "notes/scrapped", 2, 1)
	if err != nil {
		t.Fatalf("Diff back across the tombstone: %v", err)
	}
	if !back.FromDeleted || back.ToDeleted {
		t.Fatalf("reading the comparison backwards = (%v, %v), want (true, false)",
			back.FromDeleted, back.ToDeleted)
	}

	// A comparison between two live versions says nothing about
	// deletion, so the flags cannot be read as decoration.
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "notes/scrapped", Content: "back, with gnolls\n", ExpectedVersion: ptrInt32(2),
	}); err != nil {
		t.Fatalf("resurrect: %v", err)
	}
	live, err := svc.Diff(ctx, game, "notes/scrapped", 1, 3)
	if err != nil {
		t.Fatalf("Diff between two live versions: %v", err)
	}
	if live.FromDeleted || live.ToDeleted {
		t.Fatalf("two live versions reported (%v, %v)", live.FromDeleted, live.ToDeleted)
	}
}
