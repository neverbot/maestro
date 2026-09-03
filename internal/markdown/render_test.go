package markdown_test

import (
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/markdown"
)

// This file takes no database, so nothing in it may skip: every other
// test file in this package calls testutil.NewPool, which skips when
// TEST_DATABASE_URL is unset, and a renderer test that skipped with them
// would be a security check nobody notices is not running.

func TestRenderProducesHTMLFromMarkdown(t *testing.T) {
	got, err := markdown.Render("# Duskwood\n\nThe *worgen* came at dusk.\n")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, "<h1>Duskwood</h1>") || !strings.Contains(got, "<em>worgen</em>") {
		t.Fatalf("html = %q, want a heading and emphasis", got)
	}
}

func TestRawHTMLInABodyIsNotRendered(t *testing.T) {
	got, err := markdown.Render("<script>alert(1)</script>\n\nhello\n")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(got, "<script>") {
		t.Fatalf("html = %q: raw HTML must not survive rendering", got)
	}
	// The prose around it still arrives: dropping the markup must not
	// drop the document.
	if !strings.Contains(got, "hello") {
		t.Fatalf("html = %q, want the prose beside the dropped markup", got)
	}
}

// TestAnInlineHTMLSpanIsNotRendered is the other half of the one above:
// goldmark treats a whole `<script>` line as a raw *block* and a tag
// sitting inside a paragraph as raw *inline* HTML, and the two travel
// through different code paths. A configuration that disabled only one
// of them would leave this test's `<img onerror>` — the payload a
// blocked block-level script does not need — rendering as markup.
func TestAnInlineHTMLSpanIsNotRendered(t *testing.T) {
	got, err := markdown.Render("Duskwood is <img src=x onerror=alert(1)> haunted.\n")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(got, "<img") || strings.Contains(got, "onerror") {
		t.Fatalf("html = %q: inline raw HTML must not survive rendering either", got)
	}
}

// TestADangerousLinkSchemeIsNeutralised drives the allowlist: anything
// that is not http, https, mailto or a relative reference is replaced by
// "#".
//
// Every case asserts the neutralised destination `href="#"` (or
// `src="#"`) rather than only the absence of the payload's own spelling.
// Absence alone would pass against a renderer that dropped the
// destination entirely, and — worse — against one that left a *differently*
// spelled live scheme behind; the entity cases below are exactly that
// shape, and they are why this test asserts what was written instead of
// what was not.
func TestADangerousLinkSchemeIsNeutralised(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"javascript", "[click](javascript:alert(1))\n", `href="#"`},
		{"data", "![x](data:text/html;base64,PHNjcmlwdD4=)\n", `src="#"`},
		{"vbscript", "[click](vbscript:msgbox)\n", `href="#"`},
		// Case is the first thing a payload varies: the scheme match
		// folds it.
		{"mixed case", "[click](JavaScript:alert(1))\n", `href="#"`},
		// Leading whitespace is not trimmed anywhere and does not need
		// to be: URLEscape writes a tab as %09, so " javascript:" is
		// simply not one of the three spellings the allowlist accepts.
		// The refusal is the allowlist's, not a normaliser's.
		{"leading tab", "[click](\tjavascript:alert(1))\n", `href="#"`},
		// Schemes a browser will happily follow and a game bible has no
		// business carrying. None of these is on any denylist goldmark
		// ships; they are refused because they are not on ours. A
		// denylist would have to grow an arm for each.
		{"file", "[click](file:///etc/passwd)\n", `href="#"`},
		{"ftp", "[click](ftp://example.com/a)\n", `href="#"`},
		{"tel", "[click](tel:+34600000000)\n", `href="#"`},
		{"about", "[click](about:blank)\n", `href="#"`},
		{"blob", "[click](blob:https://example.com/uuid)\n", `href="#"`},
		{"custom app scheme", "[click](maestro-agent://run?cmd=rm)\n", `href="#"`},
	} {
		got, err := markdown.Render(tc.body)
		if err != nil {
			t.Fatalf("Render(%q): %v", tc.body, err)
		}
		if !strings.Contains(got, tc.want) {
			t.Fatalf("%s: Render(%q) = %q, want the destination neutralised to %s",
				tc.name, tc.body, got, tc.want)
		}
		for _, forbidden := range []string{
			"javascript:", "JavaScript:", "data:text/html", "vbscript:",
			"file:", "ftp:", "tel:", "about:", "blob:", "maestro-agent:",
		} {
			if strings.Contains(got, forbidden) {
				t.Fatalf("%s: Render(%q) = %q, which still carries %q",
					tc.name, tc.body, got, forbidden)
			}
		}
	}
}

// TestAnEntityEncodedSchemeIsResolvedBeforeItIsJudged is the case that
// found the bug safeDestination's comment describes, and the reason it
// calls util.URLEscape before reading the scheme.
//
// `javascript&#58;alert(1)` carries **no colon at all** in the bytes
// goldmark stores on the node. A check that cut the raw destination at
// ":" therefore found no scheme, concluded "relative reference", and
// passed it through — while goldmark's own renderLink writes
// util.EscapeHTML(util.URLEscape(dest, true)), and URLEscape's
// resolveReference arm runs ResolveNumericReferences and
// ResolveEntityNames, turning those bytes into a live
// `href="javascript:alert(1)"`.
//
// **This was verified, not reasoned about**: rendering these three
// bodies through plain goldmark.New() — whose own destination handling
// refuses a literal `javascript:` — produces
// `<a href="javascript:alert(1)">` for every one of them, and so does a
// safeDestination that judges dest instead of URLEscape(dest, true).
// Three spellings of the same colon get past both. That is what makes
// these the cases worth pinning: they are not a variation on a payload
// the layer below already stops, they are the payload it does not.
//
// The last case is the mirror image and is here so a "fix" that
// resolved *harder* than the renderer does cannot pass: `%6a` is not a
// scheme byte to any browser, so `%6aavascript:` is refused for having
// an unknown scheme, and never by decoding it into `javascript:`.
func TestAnEntityEncodedSchemeIsResolvedBeforeItIsJudged(t *testing.T) {
	for _, body := range []string{
		"[click](javascript&#58;alert(1))\n",   // decimal reference
		"[click](javascript&#x3a;alert(1))\n",  // hexadecimal reference
		"[click](javascript&colon;alert(1))\n", // named entity
		"[click](<javascript&#58;alert(1)>)\n", // and inside pointy brackets
		"![x](javascript&#58;alert(1))\n",      // and on an image
		"[click](%6aavascript:alert(1))\n",     // percent-encoded, still refused
	} {
		got, err := markdown.Render(body)
		if err != nil {
			t.Fatalf("Render(%q): %v", body, err)
		}
		if strings.Contains(got, "javascript:") {
			t.Fatalf("Render(%q) = %q: the entity resolves to a live javascript: href",
				body, got)
		}
		if !strings.Contains(got, `href="#"`) && !strings.Contains(got, `src="#"`) {
			t.Fatalf("Render(%q) = %q, want the destination neutralised to #", body, got)
		}
	}
}

func TestOrdinaryLinksSurvive(t *testing.T) {
	got, err := markdown.Render("[a](https://example.com) [b](lore/duskwood) [c](#top) [d](mailto:a@b.c)\n")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{"https://example.com", "lore/duskwood", "#top", "mailto:a@b.c"} {
		if !strings.Contains(got, want) {
			t.Fatalf("html = %q dropped %q", got, want)
		}
	}
}

// TestAnImageDestinationIsJudgedTheSameWayALinksIs pins that the
// allowlist reaches both node kinds. A rewrite that only walked
// *ast.Link would leave `![x](javascript:…)` intact, which is the same
// payload one element along.
func TestAnImageDestinationIsJudgedTheSameWayALinksIs(t *testing.T) {
	got, err := markdown.Render("![art](https://example.com/a.png) ![bad](javascript:alert(1))\n")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(got, "javascript:") {
		t.Fatalf("html = %q: an image destination is judged like a link's", got)
	}
	if !strings.Contains(got, "https://example.com/a.png") {
		t.Fatalf("html = %q dropped an ordinary image", got)
	}
}

func TestARelativePathWithAColonIsNotReadAsAScheme(t *testing.T) {
	got, err := markdown.Render("[a](docs/act:two)\n")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, "docs/act:two") {
		t.Fatalf("html = %q, want the relative reference intact", got)
	}
}

func TestADiffRendersAsClassedLinesWithItsSourceEscaped(t *testing.T) {
	got := markdown.RenderDiff("@@ -1,1 +1,1 @@\n-# old\n+# <b>new</b>\n")
	if !strings.Contains(got, `class="diff-removed"`) || !strings.Contains(got, `class="diff-added"`) {
		t.Fatalf("html = %q, want classed lines", got)
	}
	if !strings.Contains(got, `class="diff-hunk"`) {
		t.Fatalf("html = %q, want the hunk header classed as one", got)
	}
	if strings.Contains(got, "<b>") {
		t.Fatalf("html = %q: a diff's lines are source and must be escaped, not rendered", got)
	}
	if !strings.Contains(got, "&lt;b&gt;") {
		t.Fatalf("html = %q, want the source escaped and visible", got)
	}
}

// TestADiffsFileAndContextLinesAreClassedApart pins the two classes the
// test above cannot see. `---` is both a removal marker and the start of
// a file header, and reading it as the first would colour the header
// red; a context line carries no marker at all and must not be coloured
// either way.
func TestADiffsFileAndContextLinesAreClassedApart(t *testing.T) {
	got := markdown.RenderDiff("--- a\n+++ b\n@@ -1,2 +1,2 @@\n unchanged\n-gone\n")
	for _, want := range []string{`class="diff-file"`, `class="diff-context"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("html = %q, want %s", got, want)
		}
	}
	// And the file header is not read as a removal, which is the
	// distinction this test exists for.
	if strings.Contains(got, `class="diff-removed">--- a`) {
		t.Fatalf("html = %q: a file header must not be classed as a removed line", got)
	}
}

// TestAnAutolinkDestinationIsJudgedTheSameWayALinksIs is the third node
// kind that carries a destination, and the one goldmark does *not*
// defend on its own: renderLink and renderImage both consult
// IsDangerousURL, renderAutoLink consults nothing, and
// util.FindURLIndex — the parser that recognises `<scheme:rest>` —
// accepts any scheme of two to thirty-three URL-safe bytes. So
// `<javascript:alert(1)>` rendered as a live href until safeLinks
// started rewriting this kind too.
//
// The cases mirror TestADangerousLinkSchemeIsNeutralised one for one:
// the same payload written in autolink syntax must end up at the same
// `href="#"`, because a reader pasting a game bible has no idea which
// of the two spellings the renderer treats as a different code path.
func TestAnAutolinkDestinationIsJudgedTheSameWayALinksIs(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"javascript", "Read <javascript:alert(document.domain)>.\n"},
		{"vbscript", "Read <vbscript:msgbox>.\n"},
		{"data", "Read <data:text/html;base64,PHNjcmlwdD4=>.\n"},
		{"file", "Read <file:///etc/passwd>.\n"},
		{"ftp", "Read <ftp://example.com/a>.\n"},
		{"tel", "Read <tel:+34600000000>.\n"},
		{"about", "Read <about:blank>.\n"},
		{"blob", "Read <blob:https://example.com/uuid>.\n"},
		{"custom app scheme", "Read <maestro-agent://run?cmd=rm>.\n"},
		// Case-folded, exactly as the link allowlist folds it.
		{"mixed case", "Read <JavaScript:alert(1)>.\n"},
	} {
		got, err := markdown.Render(tc.body)
		if err != nil {
			t.Fatalf("Render(%q): %v", tc.body, err)
		}
		if !strings.Contains(got, `href="#"`) {
			t.Fatalf("%s: Render(%q) = %q, want the destination neutralised to #",
				tc.name, tc.body, got)
		}
		for _, forbidden := range []string{
			`href="javascript:`, `href="JavaScript:`, `href="vbscript:`,
			`href="data:`, `href="file:`, `href="ftp:`, `href="tel:`,
			`href="about:`, `href="blob:`, `href="maestro-agent:`,
		} {
			if strings.Contains(got, forbidden) {
				t.Fatalf("%s: Render(%q) = %q, which still carries %q",
					tc.name, tc.body, got, forbidden)
			}
		}
	}
}

// TestOrdinaryAutolinksSurvive is the other half: neutralising the
// dangerous kinds must not cost the three an autolink is normally
// written for. The email case pins the one thing rewriting the node
// could plausibly lose — goldmark's renderAutoLink prepends `mailto:`
// to a bare address, and a rewrite that forgot to would emit a relative
// link to a file named after somebody's inbox.
func TestOrdinaryAutolinksSurvive(t *testing.T) {
	for _, tc := range []struct{ body, wantHref, wantText string }{
		{"See <https://example.com/a?b=c&d=e>.\n", `href="https://example.com/a?b=c&amp;d=e"`, ">https://example.com/a?b=c&amp;d=e<"},
		{"See <http://example.com>.\n", `href="http://example.com"`, ">http://example.com<"},
		{"See <mailto:a@b.c>.\n", `href="mailto:a@b.c"`, ">mailto:a@b.c<"},
		{"See <a@b.c>.\n", `href="mailto:a@b.c"`, ">a@b.c<"},
	} {
		got, err := markdown.Render(tc.body)
		if err != nil {
			t.Fatalf("Render(%q): %v", tc.body, err)
		}
		if !strings.Contains(got, tc.wantHref) {
			t.Fatalf("Render(%q) = %q, want %s", tc.body, got, tc.wantHref)
		}
		if !strings.Contains(got, tc.wantText) {
			t.Fatalf("Render(%q) = %q, want the label %s intact", tc.body, got, tc.wantText)
		}
	}
}

// TestAnAutolinkLabelIsEscapedAndNotReRendered pins that the rewrite
// keeps the label as *raw text*. An autolink's label is its own source
// bytes, and a rewrite that fed them back through goldmark's ordinary
// text writer would unescape backslash escapes and resolve character
// references in it — so a URL a designer wrote as
// `https://example.com/?x=&amp;y` would be shown to the next reader as
// `?x=&y`, which is a different URL from the one on the page's own
// href. Raw is what goldmark's own renderAutoLink does
// (util.EscapeHTML(label), which resolves nothing), and keeping it is
// what makes the label and the destination tell the same story.
func TestAnAutolinkLabelIsEscapedAndNotReRendered(t *testing.T) {
	for _, tc := range []struct{ body, want, notWant string }{
		// A character reference in the source stays visible as one.
		{"See <https://example.com/?x=&amp;y> end.\n", "?x=&amp;amp;y", "?x=&amp;y<"},
		// So does a backslash escape.
		{"See <https://example.com/a\\_b> end.\n", `a\_b</a>`, "a_b</a>"},
		// And a `"` in the label is escaped, never left to close an
		// attribute or open a tag.
		{"See <https://example.com/?a=1&b=%22x%22> end.\n", "&amp;b=", `&b="x"`},
	} {
		got, err := markdown.Render(tc.body)
		if err != nil {
			t.Fatalf("Render(%q): %v", tc.body, err)
		}
		if !strings.Contains(got, tc.want) {
			t.Fatalf("Render(%q) = %q, want the label to carry %q", tc.body, got, tc.want)
		}
		if strings.Contains(got, tc.notWant) {
			t.Fatalf("Render(%q) = %q: the label was re-resolved into %q",
				tc.body, got, tc.notWant)
		}
	}
}

// TestEveryAutolinkInAParagraphIsJudged is the case that pins *when*
// the rewrite happens, not what it produces.
//
// Replacing a node in the middle of ast.Walk unlinks it from its
// siblings, and Walk reads NextSibling off the node it has just
// visited — so an in-place rewrite judges the first autolink in a
// paragraph and never sees the rest. That failure is invisible in any
// body with one autolink in it, and every other case in this file has
// one. Here the dangerous spelling is deliberately last, behind two
// harmless ones whose output is identical either way: only a
// transformer that collects first and replaces afterwards reaches it.
func TestEveryAutolinkInAParagraphIsJudged(t *testing.T) {
	body := "a <https://example.com/1> b <https://example.com/2> c <javascript:alert(1)> d\n"
	got, err := markdown.Render(body)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(got, `href="javascript:`) {
		t.Fatalf("html = %q: the third autolink was never judged", got)
	}
	if !strings.Contains(got, `href="#"`) {
		t.Fatalf("html = %q, want the last destination neutralised", got)
	}
	// And the two ahead of it, and the prose between them, all survive.
	for _, want := range []string{
		`href="https://example.com/1"`, `href="https://example.com/2"`, "c ", " d",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("html = %q dropped %q", got, want)
		}
	}
}
