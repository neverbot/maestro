package markdown

import (
	"bytes"
	"fmt"
	"html"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Rendering is a REST-only affordance for the browser. **MCP always gets
// the raw body** (spec §7): an agent asked to rewrite a script needs the
// markdown it will edit, and handing it HTML would mean it rewrote the
// rendering. TestTheReadingViewRendersAndTheRawBodyIsWhatMCPGets
// (internal/web/api_docs_test.go) reads one document through both
// surfaces and pins that split.
//
// **goldmark is the one dependency this sub-project adds for the UI, and
// it is configured so that no sanitiser is needed.** Without
// html.WithUnsafe, goldmark does not emit raw HTML at all — a `<script>`
// in a body is dropped and replaced by an HTML comment, and a tag inside
// a paragraph is escaped — so the class of bug a sanitiser exists to
// catch cannot arise. That is why bluemonday is not here: a second
// dependency to strip what the first never produces.
// TestRawHTMLInABodyIsNotRendered and TestAnInlineHTMLSpanIsNotRendered
// pin both halves, block and inline.
//
// What goldmark does *not* do on its own is check link destinations, so
// safeLinks below does. A `[click](javascript:…)` is a stored-XSS
// payload a designer could be tricked into pasting, and the CSP on this
// response (default-src 'self', internal/web's securityHeaders) is a
// second line and not the first.

// renderer is built once: goldmark.Markdown is safe for concurrent use
// and rebuilding it per request would parse the extension set on every
// page view.
var renderer = goldmark.New(
	goldmark.WithParserOptions(parser.WithASTTransformers(
		util.Prioritized(safeLinks{}, 100),
	)),
)

// safeLinks rewrites any link, image or autolink destination whose
// scheme is not one of the three a design document has any business
// using.
//
// The allowed set is deliberately tiny and deliberately an *allowlist*: a
// denylist of "javascript:" and "data:" is one novel scheme away from
// being wrong, and the schemes a game bible needs are http, https,
// mailto and a relative reference to another document.
//
// **The autolink is the kind that most needs this, not the one that
// needs it least.** An earlier version of this transformer skipped
// *ast.AutoLink on the claim that goldmark only produces http, https
// and mailto ones; that was false twice over. util.FindURLIndex — the
// parser behind `<scheme:rest>` — accepts *any* scheme of two to
// thirty-three URL-safe bytes, and goldmark's renderAutoLink is the one
// destination writer in the package with no IsDangerousURL check at all
// (renderLink and renderImage both have one). So `<javascript:alert(1)>`
// in a document body rendered as a live href, stored and cross-user, on
// the same page doc.js feeds into innerHTML. The CSP was carrying that
// case alone.
type safeLinks struct{}

func (safeLinks) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	source := reader.Source()
	// Autolinks are collected first and replaced after the walk.
	// Replacing a node mid-walk unlinks it from its siblings, and
	// ast.Walk reads NextSibling off the node it has just visited — so
	// an in-place rewrite judges the first autolink in a paragraph and
	// never sees the ones behind it. That is invisible in any body with
	// one autolink in it; TestEveryAutolinkInAParagraphIsJudged puts the
	// dangerous spelling last, behind two harmless ones, so it is not.
	var autolinks []*ast.AutoLink
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node := n.(type) {
		case *ast.Link:
			node.Destination = safeDestination(node.Destination)
		case *ast.Image:
			node.Destination = safeDestination(node.Destination)
		case *ast.AutoLink:
			autolinks = append(autolinks, node)
		}
		return ast.WalkContinue, nil
	})
	for _, node := range autolinks {
		if parent := node.Parent(); parent != nil {
			parent.ReplaceChild(parent, node, linkFromAutoLink(node, source))
		}
	}
}

// linkFromAutoLink turns an autolink into the ordinary link it renders
// as, so that its destination is judged by safeDestination and written
// by renderLink — the same one judge and the same one writer the
// bracketed spelling already goes through.
//
// Rewriting the node rather than registering a rival renderAutoLink is
// what makes the two impossible to drift apart: there is no second copy
// of goldmark's escaping to keep in step, and a change to safeDestination
// reaches every destination on the page by construction.
//
// The label is carried across as a *raw* ast.String, which is what
// goldmark's own renderAutoLink writes (util.EscapeHTML(label), which
// resolves nothing). Non-raw would send it through the ordinary text
// writer, unescaping backslash escapes and resolving character
// references — showing the reader `?x=&y` for a URL whose href says
// `?x=&amp;y`, a label and a destination telling different stories.
//
// One deliberate difference from renderAutoLink survives the rewrite:
// the href is now escaped by renderLink, with reference resolution on,
// where renderAutoLink had it off. That is the *stricter* of the two —
// it is the resolution safeDestination already judges against — and
// taking goldmark's link path whole is the point of doing it this way.
func linkFromAutoLink(node *ast.AutoLink, source []byte) *ast.Link {
	url := node.URL(source)
	// renderAutoLink prepends the scheme a bare address is missing;
	// without it `<a@b.c>` would become a relative link to a file named
	// after somebody's inbox.
	if node.AutoLinkType == ast.AutoLinkEmail &&
		!bytes.HasPrefix(bytes.ToLower(url), []byte("mailto:")) {
		url = append([]byte("mailto:"), url...)
	}
	link := ast.NewLink()
	link.Destination = safeDestination(url)
	label := ast.NewString(node.Label(source))
	label.SetRaw(true)
	link.AppendChild(link, label)
	return link
}

// safeDestination answers dest unchanged when it is safe to render, and
// "#" — an anchor to the page the reader is already on, which navigates
// nowhere — when it is not.
//
// Neutralised rather than dropped: a destination this refuses is still a
// visible link with its own text, so a reader sees that the author wrote
// one and that following it was refused, instead of a sentence quietly
// missing a word.
//
// **The scheme is read off the destination goldmark will actually
// write, not off the bytes stored in the node.** The two differ:
// renderLink emits util.EscapeHTML(util.URLEscape(dest, true)), and
// URLEscape's reference resolution turns `javascript&#58;alert(1)` — a
// destination carrying no colon at all — into a live `javascript:` href.
// Judging the raw bytes therefore passed that payload through as an
// ordinary relative reference; TestAnEntityEncodedSchemeIsResolvedBeforeItIsJudged
// is the case that found it. Calling the renderer's own helper here is
// what keeps "what was judged" and "what is written" one string rather
// than two that agree until an escaping rule changes.
//
// There is no whitespace normalisation, and there does not need to be:
// an allowlist refuses anything that is not exactly one of three
// spellings, so " javascript:" (which URLEscape writes as
// "%20javascript:") fails the match on its own. The cost is that a
// destination written as `< https://example.com>` is neutralised too —
// which changes nothing a browser would have done with it, since the
// leading space is percent-encoded into the scheme and the URL is
// relative either way. TestADangerousLinkSchemeIsNeutralised covers the
// whitespace-prefixed spelling.
func safeDestination(dest []byte) []byte {
	value := string(util.URLEscape(dest, true))
	scheme, _, found := strings.Cut(value, ":")
	if !found {
		return dest // relative: another document, or an anchor.
	}
	// A colon inside a path segment before any slash is not a scheme —
	// "docs/a:b" is a relative reference. A scheme cannot contain a
	// slash. TestARelativePathWithAColonIsNotReadAsAScheme pins it.
	if strings.Contains(scheme, "/") {
		return dest
	}
	switch strings.ToLower(scheme) {
	case "http", "https", "mailto":
		return dest
	}
	return []byte("#")
}

// Render turns a document body into HTML for the reading view.
//
// It renders the body only: frontmatter has already been split off by
// SplitContent, and it is metadata Maestro never interprets, so putting
// it on the page would be showing a reader the plumbing.
func Render(body string) (string, error) {
	var out bytes.Buffer
	if err := renderer.Convert([]byte(body), &out); err != nil {
		return "", fmt.Errorf("render markdown: %w", err)
	}
	return out.String(), nil
}

// RenderDiff turns a unified diff into HTML for the comparison view: one
// <div> per line, classed by what the line is, so styles.css can colour
// it without the client parsing the diff.
//
// It escapes rather than renders: a diff's lines are markdown *source*,
// and rendering them would make a line that adds a heading disappear
// into a heading. TestADiffRendersAsClassedLinesWithItsSourceEscaped
// pins both halves — the classes, and the escaping.
//
// The file-header test runs before the removal one on purpose: `---` is
// a legal removal marker *and* the start of a `--- a/…` header, and
// judging in the other order would colour every header red.
// TestADiffsFileAndContextLinesAreClassedApart pins that order.
func RenderDiff(unified string) string {
	var out strings.Builder
	out.WriteString(`<div class="diff">`)
	for _, line := range strings.Split(unified, "\n") {
		class := "diff-context"
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			class = "diff-file"
		case strings.HasPrefix(line, "@@"):
			class = "diff-hunk"
		case strings.HasPrefix(line, "+"):
			class = "diff-added"
		case strings.HasPrefix(line, "-"):
			class = "diff-removed"
		}
		fmt.Fprintf(&out, `<div class="%s">%s</div>`, class, html.EscapeString(line))
	}
	out.WriteString(`</div>`)
	return out.String()
}
