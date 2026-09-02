package markdown_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
)

func TestABodyWithNoFrontmatterIsReturnedUnchanged(t *testing.T) {
	body := "# Duskwood\n\nThe worgen came at dusk.\n"
	got, err := markdown.SplitContent("lore/duskwood", body)
	if err != nil {
		t.Fatalf("SplitContent: %v", err)
	}
	if got.Body != body {
		t.Fatalf("Body = %q, want the input unchanged (%q)", got.Body, body)
	}
	if string(got.FrontmatterJSON) != "{}" {
		t.Fatalf("FrontmatterJSON = %s, want {}", got.FrontmatterJSON)
	}
	if got.Title != "Duskwood" {
		t.Fatalf("Title = %q, want the first heading %q", got.Title, "Duskwood")
	}
	if got.Summary != "The worgen came at dusk." {
		t.Fatalf("Summary = %q, want the first paragraph", got.Summary)
	}
}

func TestFrontmatterIsSplitOffAndTheBodyIsWhatWasWrittenAfterIt(t *testing.T) {
	content := "---\ntitle: The Fall of Duskwood\nsummary: How the forest went dark\nera: third\n---\n" +
		"# Ignored heading\n\nThe worgen came at dusk.\n"
	got, err := markdown.SplitContent("lore/duskwood", content)
	if err != nil {
		t.Fatalf("SplitContent: %v", err)
	}
	wantBody := "# Ignored heading\n\nThe worgen came at dusk.\n"
	if got.Body != wantBody {
		t.Fatalf("Body = %q, want %q", got.Body, wantBody)
	}
	if got.Title != "The Fall of Duskwood" {
		t.Fatalf("Title = %q: frontmatter wins over the first heading", got.Title)
	}
	if got.Summary != "How the forest went dark" {
		t.Fatalf("Summary = %q: frontmatter wins over the first paragraph", got.Summary)
	}
	// Stored, echoed back, never interpreted: `era` survives intact and
	// means nothing to Maestro.
	if !strings.Contains(string(got.FrontmatterJSON), `"era":"third"`) {
		t.Fatalf("FrontmatterJSON = %s, want it to carry era", got.FrontmatterJSON)
	}
	if got.Frontmatter["era"] != "third" {
		t.Fatalf("Frontmatter[era] = %#v, want the decoded map to carry it too",
			got.Frontmatter["era"])
	}
}

func TestTheTitleFallsBackToThePath(t *testing.T) {
	got, err := markdown.SplitContent("lore/duskwood/history", "Just prose, no heading.\n")
	if err != nil {
		t.Fatalf("SplitContent: %v", err)
	}
	if got.Title != "lore/duskwood/history" {
		t.Fatalf("Title = %q, want the path", got.Title)
	}
}

func TestTheSummaryIsEmptyWhenTheBodyOffersNoGist(t *testing.T) {
	// A heading and nothing else: inventing a summary would put words in
	// the author's mouth in a listing.
	got, err := markdown.SplitContent("lore/duskwood", "# Duskwood\n\n")
	if err != nil {
		t.Fatalf("SplitContent: %v", err)
	}
	if got.Summary != "" {
		t.Fatalf("Summary = %q, want the empty string", got.Summary)
	}
}

func TestADerivedTitleAndSummaryAreTruncatedByRune(t *testing.T) {
	// The two derived columns truncate instead of refusing: the caller
	// did not choose them, Maestro did, so a refusal would blame a
	// caller for our own derivation. Truncation is by rune, so a
	// multi-byte character can never be cut in half — an invalid UTF-8
	// sequence is exactly what Postgres refuses outright.
	longTitle := strings.Repeat("é", markdown.MaxTitleLen+50)
	longSummary := strings.Repeat("ñ", markdown.MaxSummaryLen+50)
	got, err := markdown.SplitContent("p", "---\ntitle: "+longTitle+"\nsummary: "+longSummary+"\n---\nbody\n")
	if err != nil {
		t.Fatalf("SplitContent: %v", err)
	}
	if n := len([]rune(got.Title)); n != markdown.MaxTitleLen {
		t.Fatalf("Title is %d runes, want exactly %d", n, markdown.MaxTitleLen)
	}
	if got.Title != strings.Repeat("é", markdown.MaxTitleLen) {
		t.Fatalf("Title = %q, want the first %d runes intact", got.Title, markdown.MaxTitleLen)
	}
	if n := len([]rune(got.Summary)); n != markdown.MaxSummaryLen {
		t.Fatalf("Summary is %d runes, want exactly %d", n, markdown.MaxSummaryLen)
	}
	if got.Summary != strings.Repeat("ñ", markdown.MaxSummaryLen) {
		t.Fatalf("Summary = %q, want the first %d runes intact", got.Summary, markdown.MaxSummaryLen)
	}
}

func TestAnOversizedBodyIsInvalidInputAtPathContent(t *testing.T) {
	huge := strings.Repeat("a", markdown.MaxBodyBytes+1)
	err := mustFail(t, huge)
	problem := fieldProblem(t, err)
	if problem.Path != "content" {
		t.Fatalf("problem path = %q, want %q", problem.Path, "content")
	}
	if !strings.Contains(problem.Message, "is too long") {
		t.Fatalf("message = %q, want it to say the body is too long", problem.Message)
	}
}

func TestAContentExactlyAtTheBoundIsAccepted(t *testing.T) {
	// The bound is inclusive, so the refusal above is proved to be about
	// the bound and not about "large content" in general.
	if _, err := markdown.SplitContent("p", strings.Repeat("a", markdown.MaxBodyBytes)); err != nil {
		t.Fatalf("content of exactly MaxBodyBytes must be accepted: %v", err)
	}
}

func TestAnOversizedFrontmatterBlockIsInvalidInputAtPathContent(t *testing.T) {
	// The keys are distinct and the block is valid YAML, so the only
	// thing that can refuse it is the byte bound. An earlier version of
	// this test repeated one key, and yaml refused the duplicate — it
	// went on passing with the bound removed.
	var block strings.Builder
	for i := 0; block.Len() <= markdown.MaxFrontmatterBytes; i++ {
		fmt.Fprintf(&block, "key%08d: value\n", i)
	}
	err := mustFail(t, "---\n"+block.String()+"---\nbody\n")
	problem := fieldProblem(t, err)
	if problem.Path != "content" {
		t.Fatalf("problem path = %q, want %q", problem.Path, "content")
	}
	if !strings.Contains(problem.Message, fmt.Sprintf(
		"the most one takes is %d", markdown.MaxFrontmatterBytes)) {
		t.Fatalf("message = %q, want it to name the frontmatter bound", problem.Message)
	}
}

func TestAFrontmatterBlockJustUnderTheBoundIsAccepted(t *testing.T) {
	var block strings.Builder
	for i := 0; block.Len() < markdown.MaxFrontmatterBytes-1024; i++ {
		fmt.Fprintf(&block, "key%08d: value\n", i)
	}
	got, err := markdown.SplitContent("p", "---\n"+block.String()+"---\nbody\n")
	if err != nil {
		t.Fatalf("a block under the bound must be accepted: %v", err)
	}
	if got.Body != "body\n" {
		t.Fatalf("Body = %q, want %q", got.Body, "body\n")
	}
}

func TestAControlCharacterInTheBodyIsRefusedButNewlinesAndTabsAreNot(t *testing.T) {
	if _, err := markdown.SplitContent("p", "line one\n\tindented\r\nline two\n"); err != nil {
		t.Fatalf("newline, tab and carriage return must be allowed in prose: %v", err)
	}
	err := mustFail(t, "the bell rang\a")
	problem := fieldProblem(t, err)
	if problem.Path != "content" {
		t.Fatalf("problem path = %q, want %q", problem.Path, "content")
	}
	if !strings.Contains(problem.Message, "control character") {
		t.Fatalf("message = %q, want it to name the control character", problem.Message)
	}
}

func TestANulByteIsRefusedLikeAnyOtherControlCharacter(t *testing.T) {
	// Postgres refuses a NUL in a text value with SQLSTATE 22021, the
	// same code an invalid byte sequence raises, so it must not get past
	// here either.
	err := mustFail(t, "duskwood\x00history")
	if !strings.Contains(fieldProblem(t, err).Message, "control character") {
		t.Fatalf("message = %q, want it to name the control character",
			fieldProblem(t, err).Message)
	}
}

func TestInvalidUTF8IsRefusedBeforePostgresSeesIt(t *testing.T) {
	// A lone continuation byte: not a NUL, decodes as U+FFFD, so a
	// control-character scan alone would let it through — and Postgres
	// refuses every invalid byte sequence with SQLSTATE 22021, which
	// would reach an agent as internal_error over its own argument.
	err := mustFail(t, "duskwood \x80 history")
	if !strings.Contains(fieldProblem(t, err).Message, "valid UTF-8") {
		t.Fatalf("message = %q, want it to name the encoding", fieldProblem(t, err).Message)
	}
}

func TestMalformedFrontmatterIsTheCallersOwnProblem(t *testing.T) {
	err := mustFail(t, "---\ntitle: [unclosed\n---\nbody\n")
	problem := fieldProblem(t, err)
	if problem.Path != "content" {
		t.Fatalf("problem path = %q, want %q", problem.Path, "content")
	}
	if !strings.Contains(problem.Message, "frontmatter") {
		t.Fatalf("message = %q, want it to name the frontmatter", problem.Message)
	}
}

func TestFrontmatterThatIsNotAMappingIsRefused(t *testing.T) {
	err := mustFail(t, "---\n- one\n- two\n---\nbody\n")
	if !strings.Contains(fieldProblem(t, err).Message, "a set of key/value pairs") {
		t.Fatalf("message = %q, want it to say what frontmatter must be",
			fieldProblem(t, err).Message)
	}
}

func TestAnUnterminatedFrontmatterFenceIsBody(t *testing.T) {
	// No closing fence: the whole thing is prose. Refusing it would make
	// a document beginning with a horizontal rule unwritable.
	content := "---\nthis is not frontmatter, there is no closing fence\n"
	got, err := markdown.SplitContent("p", content)
	if err != nil {
		t.Fatalf("SplitContent: %v", err)
	}
	if got.Body != content {
		t.Fatalf("Body = %q, want the whole input", got.Body)
	}
	if string(got.FrontmatterJSON) != "{}" {
		t.Fatalf("FrontmatterJSON = %s, want {}", got.FrontmatterJSON)
	}
}

func TestAnEmptyFrontmatterBlockIsAnEmptyObjectAndNotAFailure(t *testing.T) {
	got, err := markdown.SplitContent("p", "---\n---\nbody\n")
	if err != nil {
		t.Fatalf("SplitContent: %v", err)
	}
	if string(got.FrontmatterJSON) != "{}" {
		t.Fatalf("FrontmatterJSON = %s, want {}: never null", got.FrontmatterJSON)
	}
	if got.Body != "body\n" {
		t.Fatalf("Body = %q, want %q", got.Body, "body\n")
	}
}

func TestOnlyALineThatIsExactlyThreeHyphensClosesTheBlock(t *testing.T) {
	// "----" is a horizontal rule, not a closing fence. With nothing
	// else below it there is no closing fence at all, so the whole
	// thing is prose — which is the decisive observation: if "----"
	// closed the block, the title would derive from the block instead
	// of falling back to the path.
	content := "---\ntitle: Duskwood\n----\n"
	got, err := markdown.SplitContent("lore/duskwood", content)
	if err != nil {
		t.Fatalf("SplitContent: %v", err)
	}
	if got.Body != content {
		t.Fatalf("Body = %q, want the whole input: only an exact --- line closes the block",
			got.Body)
	}
	if got.Title != "lore/duskwood" {
		t.Fatalf("Title = %q, want the path: nothing here is frontmatter", got.Title)
	}
}

func TestAFenceBelowTheFirstLineDoesNotOpenAFrontmatterBlock(t *testing.T) {
	content := "Prose first.\n---\ntitle: not frontmatter\n---\n"
	got, err := markdown.SplitContent("p", content)
	if err != nil {
		t.Fatalf("SplitContent: %v", err)
	}
	if got.Body != content {
		t.Fatalf("Body = %q, want the whole input", got.Body)
	}
	if string(got.FrontmatterJSON) != "{}" {
		t.Fatalf("FrontmatterJSON = %s, want {}", got.FrontmatterJSON)
	}
}

func TestFrontmatterHoldingAValueJSONCannotCarryIsRefused(t *testing.T) {
	// YAML's `.nan` and `.inf` decode to float64 NaN and +Inf. jsonb
	// cannot carry either, and they are precisely the two values a
	// numeric bound compares false against without noticing — so the
	// refusal belongs here rather than as a database fault the caller
	// cannot read.
	for _, literal := range []string{".nan", ".inf", "-.inf"} {
		t.Run(literal, func(t *testing.T) {
			err := mustFail(t, "---\nratio: "+literal+"\n---\nbody\n")
			problem := fieldProblem(t, err)
			if problem.Path != "content" {
				t.Fatalf("problem path = %q, want %q", problem.Path, "content")
			}
			if !strings.Contains(problem.Message, "JSON") {
				t.Fatalf("message = %q, want it to say the value cannot be stored",
					problem.Message)
			}
		})
	}
}

func TestANonStringScalarKeyIsFoldedToAStringRatherThanRefused(t *testing.T) {
	// `1: one` is a legal YAML mapping and jsonb keys are strings, so
	// the key folds. Pinned because the alternative — refusing it — was
	// the shape this task was first written with, and a caller would
	// have been refused over a document it could not have fixed without
	// being told which key.
	got, err := markdown.SplitContent("p", "---\n1: one\n---\nbody\n")
	if err != nil {
		t.Fatalf("SplitContent: %v", err)
	}
	if !strings.Contains(string(got.FrontmatterJSON), `"1":"one"`) {
		t.Fatalf("FrontmatterJSON = %s, want the key folded to a string", got.FrontmatterJSON)
	}
}

func TestANonStringTitleInFrontmatterFallsThroughToTheNextRule(t *testing.T) {
	// Frontmatter is inert: a project may put anything under `title`. It
	// simply does not supply the derivation.
	got, err := markdown.SplitContent("p", "---\ntitle: 42\n---\n# Duskwood\n\nprose\n")
	if err != nil {
		t.Fatalf("SplitContent: %v", err)
	}
	if got.Title != "Duskwood" {
		t.Fatalf("Title = %q, want the heading: a non-string title does not derive", got.Title)
	}
	if !strings.Contains(string(got.FrontmatterJSON), `"title":42`) {
		t.Fatalf("FrontmatterJSON = %s, want the 42 stored intact anyway", got.FrontmatterJSON)
	}
}

func mustFail(t *testing.T, content string) error {
	t.Helper()
	_, err := markdown.SplitContent("p", content)
	if err == nil {
		t.Fatal("SplitContent succeeded, want a refusal")
	}
	if !errors.Is(err, metamodel.ErrInvalidInput) {
		t.Fatalf("want invalid_input, got %v", err)
	}
	return err
}
