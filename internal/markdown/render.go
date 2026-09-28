package markdown

import (
	"bytes"
	"fmt"
	"html"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
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
var renderer = goldmark.New(
	goldmark.WithParserOptions(parser.WithASTTransformers(
		util.Prioritized(safeLinks{}, 100),
	)),
)

// docRenderer is the same renderer with tables, for the documentation
// site (cmd/maestro-docs).
var docRenderer = goldmark.New(
	goldmark.WithExtensions(extension.Table),
	goldmark.WithParserOptions(parser.WithASTTransformers(
		util.Prioritized(safeLinks{}, 100),
	)),
)

// RenderDoc turns one of this repository's own markdown pages into HTML
// for the documentation site. See docRenderer for what it admits that
// Render does not.
func RenderDoc(body string) (string, error) {
	var out bytes.Buffer
	if err := docRenderer.Convert([]byte(body), &out); err != nil {
		return "", fmt.Errorf("render markdown: %w", err)
	}
	return out.String(), nil
}

// safeLinks rewrites any link, image or autolink destination whose
// scheme is not one of the three a design document has any business
// using.
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
