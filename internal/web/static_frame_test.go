package web_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The view frame's one property with no runtime signature: **the
// component invents no words.**
//
// Every sentence a designer reads in the frame is built in
// internal/web/static/render/scene.js, which is a pure function a Node
// harness drives and a mutation turns red. The component is supposed to
// be a painter: it decides where each string sits and what it is painted
// with, and never what it says. Nothing at runtime can see the
// difference — a component that hard-coded "complete picture" into its
// footer would render perfectly, pass every check in
// internal/web/jstest/frame_test.mjs (which never loads it) and be
// exactly the drift the whole task exists to prevent, since the next
// five renderers would each grow their own copy.
//
// So it is held by shape, the way internal/web/static_client_test.go
// holds "this module composes no sentence": **every text node in the
// component's templates is either whitespace or an interpolation.** A
// word typed between two tags is a failing test.
//
// The one deliberate exception is the label of the single action the
// diagnostics panel offers, which is a constant at the top of the file
// and reaches the template as `${RUN_ANYWAY_LABEL}` — an interpolation
// like any other, and named there rather than in the model because it
// names a *control* and not a state of the answer.

// textNodeRE finds a candidate text node: a `>` that closes a tag —
// preceded by something that is neither whitespace nor `=`, which is
// what tells `</span>` from a JavaScript `a > b` and from an arrow
// `() =>` — followed by whatever runs until the region ends.
//
// A region ends at `<` (the next tag), at `$` (an interpolation) or at a
// backtick (the end of the template). Anything non-blank before one of
// those is a word the component is saying in its own voice.
var textNodeRE = regexp.MustCompile("[^\\s=]>([^<$`]*)")

// scanTemplateText returns every text node the component speaks in its
// own voice. It strips comments first, then the `css` block — a
// stylesheet is not prose and contains `>` in its selectors — and reads
// what is left.
func scanTemplateText(src string) []string {
	body := stripCSSBlock(stripComments(src))
	var offences []string
	for _, hit := range textNodeRE.FindAllStringSubmatch(body, -1) {
		if strings.TrimSpace(hit[1]) == "" {
			continue
		}
		offences = append(offences, strings.TrimSpace(hit[1]))
	}
	return offences
}

// stripCSSBlock removes every css`…` tagged template. Lit stylesheets
// carry no prose and do carry `>` (a child combinator), so leaving them
// in would make the scan above report selectors as sentences.
func stripCSSBlock(src string) string {
	var out strings.Builder
	for {
		at := strings.Index(src, "css`")
		if at < 0 {
			out.WriteString(src)
			return out.String()
		}
		out.WriteString(src[:at])
		rest := src[at+len("css`"):]
		end := strings.IndexByte(rest, '`')
		if end < 0 {
			return out.String()
		}
		src = rest[end+1:]
	}
}

func TestTheViewFrameSpeaksOnlyTheModelsWords(t *testing.T) {
	raw, err := os.ReadFile("static/components/mst-view-frame.js")
	if err != nil {
		t.Fatalf("read mst-view-frame.js: %v", err)
	}
	if offences := scanTemplateText(string(raw)); len(offences) > 0 {
		t.Errorf("mst-view-frame.js writes its own words into %d text node(s): %q\n"+
			"every sentence in the frame belongs to static/render/scene.js, which is where the "+
			"server's own sentences are carried verbatim and where a harness can read them",
			len(offences), offences)
	}
}

// TestTheTemplateTextScanReadsWhatItClaimsTo is the guard on the guard,
// in both directions. A scanner that found nothing because it matched
// nothing would pass the test above on any file at all, which is the
// failure mode this repository has hit twice: a guard that audits
// itself.
func TestTheTemplateTextScanReadsWhatItClaimsTo(t *testing.T) {
	speaks := "const label = html`<div class=\"footer\">complete picture</div>`;"
	if got := scanTemplateText(speaks); len(got) != 1 || got[0] != "complete picture" {
		t.Errorf("the scan missed a hard-coded sentence: %q", got)
	}

	silent := "const label = html`<div class=\"footer\">${footer.text}</div>`;"
	if got := scanTemplateText(silent); len(got) != 0 {
		t.Errorf("the scan reported an interpolation as prose: %q", got)
	}

	// The three spellings that must not be mistaken for a text node: a
	// comparison, an arrow function, and a sentence inside a comment.
	for name, src := range map[string]string{
		"a comparison": "if (rows.length > 0) return rows;",
		"an arrow":     "rows.map((row) => row.pointer);",
		"a comment":    "// this view is a complete picture of the game\nconst a = 1;",
	} {
		if got := scanTemplateText(src); len(got) != 0 {
			t.Errorf("the scan read %s as prose: %q", name, got)
		}
	}

	// And the css block really is skipped, rather than the scan happening
	// to find nothing in it.
	styled := "static styles = css`.a > .b { color: red; }`;\nconst h = html`<p>a word</p>`;"
	if got := scanTemplateText(styled); len(got) != 1 || got[0] != "a word" {
		t.Errorf("the css skip ate the templates after it, or the selector was read as prose: %q", got)
	}
}
