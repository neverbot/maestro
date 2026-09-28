package markdown_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
)

func TestABodyWithNoFrontmatterIsReturnedUnchanged(t *testing.T) {
	body := "# Duskwood\n\nThe worgen came at dusk.\n"
	got, err := markdown.SplitContent("lore/duskwood", body)
	assert.Must(t, err == nil, "SplitContent: %v", err)
	assert.Must(t, got.Body == body, "Body = %q, want the input unchanged (%q)", got.Body, body)
	assert.Must(t, string(got.FrontmatterJSON) == "{}", "FrontmatterJSON = %s, want {}", got.FrontmatterJSON)
	assert.Must(t, got.Title == "Duskwood", "Title = %q, want the first heading %q", got.Title, "Duskwood")
	assert.Must(t, got.Summary == "The worgen came at dusk.", "Summary = %q, want the first paragraph", got.Summary)
}

func TestFrontmatterIsSplitOffAndTheBodyIsWhatWasWrittenAfterIt(t *testing.T) {
	content := "---\ntitle: The Fall of Duskwood\nsummary: How the forest went dark\nera: third\n---\n" +
		"# Ignored heading\n\nThe worgen came at dusk.\n"
	got, err := markdown.SplitContent("lore/duskwood", content)
	assert.Must(t, err == nil, "SplitContent: %v", err)
	wantBody := "# Ignored heading\n\nThe worgen came at dusk.\n"
	assert.Must(t, got.Body == wantBody, "Body = %q, want %q", got.Body, wantBody)
	assert.Must(t, got.Title == "The Fall of Duskwood", "Title = %q: frontmatter wins over the first heading", got.Title)
	assert.Must(t, got.Summary == "How the forest went dark", "Summary = %q: frontmatter wins over the first paragraph", got.Summary)
	// Stored, echoed back, never interpreted: `era` survives intact and
	// means nothing to Maestro.
	assert.Must(t, strings.Contains(string(got.FrontmatterJSON), `"era":"third"`), "FrontmatterJSON = %s, want it to carry era", got.FrontmatterJSON)
	if got.Frontmatter["era"] != "third" {
		t.Fatalf("Frontmatter[era] = %#v, want the decoded map to carry it too",
			got.Frontmatter["era"])
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
	assert.Must(t, err == nil, "SplitContent: %v", err)
	if n := len([]rune(got.Title)); n != markdown.MaxTitleLen {
		t.Fatalf("Title is %d runes, want exactly %d", n, markdown.MaxTitleLen)
	}
	assert.Must(t, got.Title == strings.Repeat("é", markdown.MaxTitleLen), "Title = %q, want the first %d runes intact", got.Title, markdown.MaxTitleLen)
	if n := len([]rune(got.Summary)); n != markdown.MaxSummaryLen {
		t.Fatalf("Summary is %d runes, want exactly %d", n, markdown.MaxSummaryLen)
	}
	assert.Must(t, got.Summary == strings.Repeat("ñ", markdown.MaxSummaryLen), "Summary = %q, want the first %d runes intact", got.Summary, markdown.MaxSummaryLen)
}

func TestAnOversizedBodyIsInvalidInputAtPathContent(t *testing.T) {
	huge := strings.Repeat("a", markdown.MaxBodyBytes+1)
	err := mustFail(t, huge)
	problem := fieldProblem(t, err)
	assert.Must(t, problem.Path == "content", "problem path = %q, want %q", problem.Path, "content")
	assert.Must(t, strings.Contains(problem.Message, "is too long"), "message = %q, want it to say the body is too long", problem.Message)
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
	assert.Must(t, problem.Path == "content", "problem path = %q, want %q", problem.Path, "content")
	assert.Must(t, strings.Contains(problem.Message, fmt.Sprintf(
		"the most one takes is %d", markdown.MaxFrontmatterBytes)), "message = %q, want it to name the frontmatter bound", problem.Message)
}

func TestAFrontmatterBlockJustUnderTheBoundIsAccepted(t *testing.T) {
	var block strings.Builder
	for i := 0; block.Len() < markdown.MaxFrontmatterBytes-1024; i++ {
		fmt.Fprintf(&block, "key%08d: value\n", i)
	}
	got, err := markdown.SplitContent("p", "---\n"+block.String()+"---\nbody\n")
	assert.Must(t, err == nil, "a block under the bound must be accepted: %v", err)
	assert.Must(t, got.Body == "body\n", "Body = %q, want %q", got.Body, "body\n")
}

func TestAControlCharacterInTheBodyIsRefusedButNewlinesAndTabsAreNot(t *testing.T) {
	if _, err := markdown.SplitContent("p", "line one\n\tindented\r\nline two\n"); err != nil {
		t.Fatalf("newline, tab and carriage return must be allowed in prose: %v", err)
	}
	err := mustFail(t, "the bell rang\a")
	problem := fieldProblem(t, err)
	assert.Must(t, problem.Path == "content", "problem path = %q, want %q", problem.Path, "content")
	assert.Must(t, strings.Contains(problem.Message, "control character"), "message = %q, want it to name the control character", problem.Message)
}

func TestMalformedFrontmatterIsTheCallersOwnProblem(t *testing.T) {
	err := mustFail(t, "---\ntitle: [unclosed\n---\nbody\n")
	problem := fieldProblem(t, err)
	assert.Must(t, problem.Path == "content", "problem path = %q, want %q", problem.Path, "content")
	assert.Must(t, strings.Contains(problem.Message, "frontmatter"), "message = %q, want it to name the frontmatter", problem.Message)
}

func TestAnUnterminatedFrontmatterFenceIsBody(t *testing.T) {
	// No closing fence: the whole thing is prose. Refusing it would make
	// a document beginning with a horizontal rule unwritable.
	content := "---\nthis is not frontmatter, there is no closing fence\n"
	got, err := markdown.SplitContent("p", content)
	assert.Must(t, err == nil, "SplitContent: %v", err)
	assert.Must(t, got.Body == content, "Body = %q, want the whole input", got.Body)
	assert.Must(t, string(got.FrontmatterJSON) == "{}", "FrontmatterJSON = %s, want {}", got.FrontmatterJSON)
}

func TestOnlyALineThatIsExactlyThreeHyphensClosesTheBlock(t *testing.T) {
	// "----" is a horizontal rule, not a closing fence. With nothing
	// else below it there is no closing fence at all, so the whole
	// thing is prose — which is the decisive observation: if "----"
	// closed the block, the title would derive from the block instead
	// of falling back to the path.
	content := "---\ntitle: Duskwood\n----\n"
	got, err := markdown.SplitContent("lore/duskwood", content)
	assert.Must(t, err == nil, "SplitContent: %v", err)
	assert.Must(t, got.Body == content, "Body = %q, want the whole input: only an exact --- line closes the block",
		got.Body)
	assert.Must(t, got.Title == "lore/duskwood", "Title = %q, want the path: nothing here is frontmatter", got.Title)
}

func TestAFenceBelowTheFirstLineDoesNotOpenAFrontmatterBlock(t *testing.T) {
	content := "Prose first.\n---\ntitle: not frontmatter\n---\n"
	got, err := markdown.SplitContent("p", content)
	assert.Must(t, err == nil, "SplitContent: %v", err)
	assert.Must(t, got.Body == content, "Body = %q, want the whole input", got.Body)
	assert.Must(t, string(got.FrontmatterJSON) == "{}", "FrontmatterJSON = %s, want {}", got.FrontmatterJSON)
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
			assert.Must(t, problem.Path == "content", "problem path = %q, want %q", problem.Path, "content")
			assert.Must(t, strings.Contains(problem.Message, "JSON"), "message = %q, want it to say the value cannot be stored",
				problem.Message)
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
	assert.Must(t, err == nil, "SplitContent: %v", err)
	assert.Must(t, strings.Contains(string(got.FrontmatterJSON), `"1":"one"`), "FrontmatterJSON = %s, want the key folded to a string", got.FrontmatterJSON)
}

func TestANonStringTitleInFrontmatterFallsThroughToTheNextRule(t *testing.T) {
	// Frontmatter is inert: a project may put anything under `title`. It
	// simply does not supply the derivation.
	got, err := markdown.SplitContent("p", "---\ntitle: 42\n---\n# Duskwood\n\nprose\n")
	assert.Must(t, err == nil, "SplitContent: %v", err)
	assert.Must(t, got.Title == "Duskwood", "Title = %q, want the heading: a non-string title does not derive", got.Title)
	assert.Must(t, strings.Contains(string(got.FrontmatterJSON), `"title":42`), "FrontmatterJSON = %s, want the 42 stored intact anyway", got.FrontmatterJSON)
}

func mustFail(t *testing.T, content string) error {
	t.Helper()
	_, err := markdown.SplitContent("p", content)
	assert.Must(t, err != nil, "SplitContent succeeded, want a refusal")
	assert.Must(t, errors.Is(err, metamodel.ErrInvalidInput), "want invalid_input, got %v", err)
	return err
}

func TestAClosingFenceIsTheEarliestOneAndNotTheFirstShapeTried(t *testing.T) {
	// Mixed line endings are what an agent assembling a JSON string
	// produces routinely: a CRLF frontmatter block above an LF body. When
	// the closing candidates were tried in list order rather than by
	// offset, the LF `---` further down the body matched first, the block
	// ran past the real fence, and yaml kept only its first document — so
	// `intro\n---\n` was deleted from the body with nothing said.
	content := "---\r\ntitle: X\r\n---\r\nintro\n---\ntrailing\n"
	got, err := markdown.SplitContent("p", content)
	assert.Must(t, err == nil, "SplitContent: %v", err)
	wantBody := "intro\n---\ntrailing\n"
	assert.Must(t, got.Body == wantBody, "Body = %q, want %q: the CRLF fence closes the block", got.Body, wantBody)
	assert.Must(t, got.Title == "X", "Title = %q, want %q", got.Title, "X")
}

func TestAFenceTerminatedByEndOfFileClosesTheBlock(t *testing.T) {
	// Every tool this convention is borrowed from — Jekyll, Hugo,
	// goldmark's own extension — accepts an EOF-terminated block, and a
	// metadata-only document written without a trailing newline is an
	// ordinary thing for an agent composing a JSON string. "An
	// unterminated fence is body" is the rule for a *missing* fence; a
	// fence terminated by EOF is not missing.
	for _, content := range []string{
		"---\ntitle: X\n---",
		"---\r\ntitle: X\r\n---",
		"---\ntitle: X\n---\r\n",
	} {
		t.Run(fmt.Sprintf("%q", content), func(t *testing.T) {
			got, err := markdown.SplitContent("p", content)
			assert.Must(t, err == nil, "SplitContent: %v", err)
			assert.Must(t, got.Title == "X", "Title = %q, want the frontmatter title %q", got.Title, "X")
			assert.Must(t, got.Body == "", "Body = %q, want it empty", got.Body)
			assert.Must(t, got.Summary == "", "Summary = %q, want it empty and never the fence", got.Summary)
		})
	}
}

func TestALeadingByteOrderMarkIsRefusedByName(t *testing.T) {
	// U+FEFF is Cf, not Cc, so the control scan passes it and the opening
	// CutPrefix then fails: the whole document including the mark becomes
	// body and the summary becomes `U+FEFF ---`, silently. Refusing names
	// the byte; reinterpreting the document does not.
	err := mustFail(t, "\ufeff---\ntitle: X\n---\nbody\n")
	problem := fieldProblem(t, err)
	assert.Must(t, problem.Path == "content", "problem path = %q, want %q", problem.Path, "content")
	assert.Must(t, strings.Contains(problem.Message, "U+FEFF"), "message = %q, want it to name U+FEFF", problem.Message)
}

func TestAByteOrderMarkInsideTheBodyIsNotRefused(t *testing.T) {
	// Only a *leading* mark defeats the fence. Mid-body it is an ordinary
	// zero-width character and refusing it would blame prose.
	if _, err := markdown.SplitContent("p", "prose\ufeffmore prose\n"); err != nil {
		t.Fatalf("a mark inside the body is not a problem: %v", err)
	}
}

func TestAFrontmatterBlockExactlyAtTheBoundIsAccepted(t *testing.T) {
	// The bound is inclusive, the same way MaxBodyBytes is, and the block
	// measured is the bytes between the fences including the newline that
	// ends the last one.
	line := "key%08d: value\n"
	var block strings.Builder
	for block.Len()+len(fmt.Sprintf(line, 0)) <= markdown.MaxFrontmatterBytes {
		fmt.Fprintf(&block, line, block.Len())
	}
	pad := markdown.MaxFrontmatterBytes - block.Len()
	if pad > 0 {
		// Pad with a comment line of exactly the remaining length.
		fmt.Fprintf(&block, "#%s\n", strings.Repeat("p", pad-2))
	}
	assert.Must(t, block.Len() == markdown.MaxFrontmatterBytes, "test built a block of %d bytes, want exactly %d",
		block.Len(), markdown.MaxFrontmatterBytes)
	if _, err := markdown.SplitContent("p", "---\n"+block.String()+"---\nbody\n"); err != nil {
		t.Fatalf("a block of exactly MaxFrontmatterBytes must be accepted: %v", err)
	}
}

func TestYAMLThatDoesNotParseIsToldApartFromYAMLThatIsNotAMapping(t *testing.T) {
	// The two refusals a caller fixes differently. Asserting only that
	// both say "frontmatter" cannot tell them apart — which is the exact
	// distinction parseFrontmatter's second decode exists to preserve.
	err := mustFail(t, "---\ntitle: [unclosed\n---\nbody\n")
	assert.Must(t, strings.Contains(fieldProblem(t, err).Message, "not valid YAML"), "message = %q, want it to name the syntax error",
		fieldProblem(t, err).Message)
	err = mustFail(t, "---\n- one\n- two\n---\nbody\n")
	message := fieldProblem(t, err).Message
	assert.Must(t, !strings.Contains(message, "not valid YAML"), "message = %q: a list parses fine, it is simply not a mapping", message)
	assert.Must(t, strings.Contains(message, "a list"), "message = %q, want it to say a list rather than a Go type name", message)
	assert.Must(t, !strings.Contains(message, "interface {}"), "message = %q: an agent must not be shown a Go type name", message)
}

func TestAScalarFrontmatterBlockIsNamedAsASingleValue(t *testing.T) {
	err := mustFail(t, "---\njust a sentence\n---\nbody\n")
	message := fieldProblem(t, err).Message
	assert.Must(t, strings.Contains(message, "a single value"), "message = %q, want it to say a single value", message)
}

func TestAControlCharacterInAFrontmatterTitleOrSummaryIsRefused(t *testing.T) {
	// A body may carry newlines; a title may not. Both derived columns
	// are rendered as one line — a page title, a listing row — which is
	// the same reason the metamodel's name check refuses a newline. Prose
	// keeps its paragraph breaks, the two single-line columns do not.
	for _, key := range []string{"title", "summary"} {
		t.Run(key, func(t *testing.T) {
			err := mustFail(t, "---\n"+key+": \"one\\ntwo\"\n---\nbody\n")
			problem := fieldProblem(t, err)
			assert.Must(t, problem.Path == "content", "problem path = %q, want %q", problem.Path, "content")
			assert.Must(t, strings.Contains(problem.Message, key), "message = %q, want it to name the %s key", problem.Message, key)
			assert.Must(t, strings.Contains(problem.Message, "one line"), "message = %q, want it to say the value is one line",
				problem.Message)
		})
	}
}

// Where a document's title and summary come from, in the order the rules
// are tried: the frontmatter, then the first heading, then the path.
func TestTheTitleAndSummaryAreDerivedInOrder(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		title, summary   string
	}{
		{"no heading, so the path is the title", "lore/duskwood/history",
			"Just prose, no heading.\n", "lore/duskwood/history", "Just prose, no heading."},
		{"a heading and nothing after it leaves no summary", "lore/duskwood",
			"# Duskwood\n\n", "Duskwood", ""},
		{"CRLF frontmatter is split like LF", "p",
			"---\r\ntitle: Duskwood\r\n---\r\nprose\r\n", "Duskwood", "prose"},
		{"a whitespace-only frontmatter title falls through to the heading", "p",
			"---\ntitle: \"   \"\n---\n# Duskwood\n\nprose\n", "Duskwood", "prose"},
		{"a heading inside a fenced block is not the title", "p",
			"```\n# not a title\n```\nreal prose\n", "p", "real prose"},
		{"a heading after a closed fence still is", "p",
			"~~~yaml\n# sample\n~~~\n\n# Duskwood\n\nprose\n", "Duskwood", "prose"},
		{"a line merely starting with a hash is a summary", "p",
			"#1 rule of Duskwood: do not go out at dusk.\n",
			"p", "#1 rule of Duskwood: do not go out at dusk."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := markdown.SplitContent(tc.path, tc.body)
			assert.NoErr(t, err, "SplitContent")
			assert.Must(t, got.Title == tc.title, "Title = %q, want %q", got.Title, tc.title)
			assert.Must(t, got.Summary == tc.summary, "Summary = %q, want %q", got.Summary, tc.summary)
		})
	}
}

// What SplitContent refuses, and the words it refuses with. The message
// reaches a designer, and the order matters where a body is wrong twice:
// the encoding is reported before the control character.
func TestSplitContentRefusesABodyAndSaysWhy(t *testing.T) {
	for _, tc := range []struct{ name, body, says string }{
		{"a NUL byte", "duskwood\x00history", "control character"},
		{"invalid UTF-8", "duskwood \x80 history", "valid UTF-8"},
		{"frontmatter that is a list", "---\n- one\n- two\n---\nbody\n", "a set of key/value pairs"},
		{"invalid UTF-8 before a control character", "duskwood \x80 \a history", "valid UTF-8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := mustFail(t, tc.body)
			got := fieldProblem(t, err).Message
			assert.Must(t, strings.Contains(got, tc.says), "message = %q, want it to say %q", got, tc.says)
		})
	}
}

// An empty frontmatter block is an empty object and never JSON null: the
// column is jsonb and a null there reads back as "no frontmatter at all",
// which is a different fact from "a block with nothing in it".
func TestAnEmptyFrontmatterBlockIsAnEmptyObject(t *testing.T) {
	for _, tc := range []struct{ name, body, wantBody string }{
		{"an empty block", "---\n---\nbody\n", "body\n"},
		{"an empty block closed by end of file", "---\n---", ""},
		{"a block of only blank lines", "---\n\n   \n\n---\nbody\n", "body\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := markdown.SplitContent("p", tc.body)
			assert.NoErr(t, err, "SplitContent")
			assert.Must(t, string(got.FrontmatterJSON) == "{}", "FrontmatterJSON = %s, want {}", got.FrontmatterJSON)
			assert.Must(t, got.Frontmatter != nil, "Frontmatter is nil, want an empty map")
			assert.Must(t, got.Body == tc.wantBody, "Body = %q, want %q", got.Body, tc.wantBody)
		})
	}
}
