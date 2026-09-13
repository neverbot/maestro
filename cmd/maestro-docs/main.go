// Command maestro-docs builds this repository's documentation site.
//
// The site is generated from the files that already exist and are
// already true — `readme.md`, the skill bundle's own pages, the
// generated design system — and it invents no prose of its own. That is
// the whole design decision here: a documentation site written by hand
// beside a product is a second description of it, and this repository
// has spent the year learning what a second description costs.
//
// What it publishes:
//
//   - the readme, as the home page;
//   - every page of the skill bundle, which is what an agent is handed
//     and what the product's own onboarding link points at;
//   - the generated design system page, copied whole.
//
// No CSS framework, no fonts fetched from anywhere, no JavaScript: the
// same discipline the product's own front end keeps, for the same
// reason.
//
//	go run ./cmd/maestro-docs -o site
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/skill"
)

func main() {
	out := flag.String("o", "site", "directory to write the site into")
	root := flag.String("root", ".", "repository root to read the sources from")
	check := flag.Bool("check", false, "after building, fail if any internal link points at a page the site does not have")
	flag.Parse()

	if err := build(*root, *out); err != nil {
		fmt.Fprintf(os.Stderr, "maestro-docs: %v\n", err)
		os.Exit(1)
	}
	if !*check {
		return
	}
	broken, err := checkLinks(*out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "maestro-docs: %v\n", err)
		os.Exit(1)
	}
	if len(broken) > 0 {
		fmt.Fprintf(os.Stderr, "maestro-docs: %d broken internal link(s):\n", len(broken))
		for _, link := range broken {
			fmt.Fprintf(os.Stderr, "  %s\n", link)
		}
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "maestro-docs: every internal link resolves")
}

// page is one rendered page of the site.
type page struct {
	// Path is where it is written, relative to the output directory.
	Path string
	// Title is what the tab says and what the index links it by.
	Title string
	// Body is the rendered HTML of the page's own content.
	Body string
}

func build(root, out string) error {
	pages, err := collect(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o750); err != nil {
		return fmt.Errorf("make %s: %w", out, err)
	}
	for _, p := range pages {
		full := filepath.Join(out, filepath.FromSlash(p.Path))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			return fmt.Errorf("make %s: %w", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(shell(p, depthOf(p.Path))), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", full, err)
		}
	}
	// The design system is already a generated page and is copied whole:
	// rendering it again from its own sources here would be a second
	// generator for one artefact.
	if body, err := os.ReadFile(filepath.Join(root, "docs", "design-system.html")); err == nil {
		if err := os.WriteFile(filepath.Join(out, "design-system.html"), body, 0o600); err != nil {
			return fmt.Errorf("copy the design system: %w", err)
		}
	}
	if err := os.WriteFile(filepath.Join(out, "style.css"), []byte(siteCSS), 0o600); err != nil {
		return fmt.Errorf("write the stylesheet: %w", err)
	}
	// GitHub Pages runs Jekyll over an artefact unless told not to, and
	// Jekyll drops every directory beginning with an underscore. Nothing
	// here starts with one today; the file costs a byte and removes a
	// whole class of surprise.
	if err := os.WriteFile(filepath.Join(out, ".nojekyll"), nil, 0o600); err != nil {
		return fmt.Errorf("write .nojekyll: %w", err)
	}
	fmt.Fprintf(os.Stderr, "maestro-docs: wrote %d page(s) into %s\n", len(pages), out)
	return nil
}

// collect renders every page the site is made of.
func collect(root string) ([]page, error) {
	var pages []page

	readme, err := os.ReadFile(filepath.Join(root, "readme.md"))
	if err != nil {
		return nil, fmt.Errorf("read readme.md: %w", err)
	}
	home, err := markdown.RenderDoc(rewriteLinks(string(readme)))
	if err != nil {
		return nil, err
	}
	pages = append(pages, page{Path: "index.html", Title: "", Body: home + agentsIndex()})

	// The licence, because the readme links to it and a link that
	// downloads a file instead of opening a page is a broken link with
	// an excuse.
	licence, err := os.ReadFile(filepath.Join(root, "license.md"))
	if err != nil {
		return nil, fmt.Errorf("read license.md: %w", err)
	}
	licenceBody, err := markdown.RenderDoc(string(licence))
	if err != nil {
		return nil, err
	}
	pages = append(pages, page{Path: "license.html", Title: "Licence", Body: licenceBody})

	bundle, err := bundlePages()
	if err != nil {
		return nil, err
	}
	pages = append(pages, bundle...)
	return pages, nil
}

// bundlePages renders the skill bundle: the pages an agent is handed,
// published so a person can read what their agent was told.
func bundlePages() ([]page, error) {
	var names []string
	err := fs.WalkDir(skill.Files(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		names = append(names, p)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk the bundle: %w", err)
	}
	sort.Strings(names)

	pages := make([]page, 0, len(names))
	for _, name := range names {
		body, err := fs.ReadFile(skill.Files(), name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		rendered, err := markdown.RenderDoc(rewriteLinks(string(body)))
		if err != nil {
			return nil, err
		}
		pages = append(pages, page{
			Path:  path.Join("agents", strings.TrimSuffix(name, ".md")+".html"),
			Title: titleOf(string(body), name),
			Body:  rendered,
		})
	}
	return pages, nil
}

// agentsIndex is the one piece of navigation the site adds: the bundle's
// pages, linked from the home page. It is built from what was rendered
// rather than written out, so a page added to the bundle appears here
// without anybody remembering to add it.
func agentsIndex() string {
	pages, err := bundlePages()
	if err != nil {
		return ""
	}
	var out strings.Builder
	out.WriteString("\n<h2 id=\"for-agents\">What an agent is told</h2>\n")
	out.WriteString("<p>These are the pages this instance serves to an agent, published so a " +
		"person can read what their agent was handed. They are the bundle itself, not a " +
		"description of it.</p>\n<ul>\n")
	for _, p := range pages {
		out.WriteString("<li><a href=\"" + p.Path + "\">" + escape(p.Title) + "</a></li>\n")
	}
	out.WriteString("</ul>\n")
	return out.String()
}

// titleOf is a page's first heading, or its path when it has none.
func titleOf(body, fallback string) string {
	for _, line := range strings.Split(body, "\n") {
		if after, ok := strings.CutPrefix(line, "# "); ok {
			return strings.TrimSpace(after)
		}
	}
	return fallback
}

// rewriteLinks points a markdown link at a neighbouring `.md` file at
// the page this generator wrote for it. A link that still said `.md`
// would download a file instead of opening a page.
func rewriteLinks(body string) string {
	return strings.ReplaceAll(body, ".md)", ".html)")
}

// depthOf is how many directories deep a page sits, so its links to the
// stylesheet and the home page are relative and the site works from any
// prefix — a project page on GitHub Pages is served under /maestro/, and
// an absolute path would be wrong there and right nowhere else.
func depthOf(p string) int {
	return strings.Count(p, "/")
}

func shell(p page, depth int) string {
	up := strings.Repeat("../", depth)
	var out strings.Builder
	out.WriteString("<!doctype html>\n<html lang=\"en\">\n<meta charset=\"utf-8\">\n")
	out.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	// "Maestro · Maestro" is what a page whose own title is the product's
	// name produced before this.
	title := "Maestro"
	if p.Title != "" && p.Title != "Maestro" {
		title = escape(p.Title) + " · Maestro"
	}
	out.WriteString("<title>" + title + "</title>\n")
	out.WriteString("<link rel=\"stylesheet\" href=\"" + up + "style.css\">\n")
	out.WriteString("<header><a class=\"brand\" href=\"" + up + "index.html\">Maestro</a>")
	out.WriteString("<a href=\"" + up + "index.html#for-agents\">For agents</a>")
	out.WriteString("<a href=\"" + up + "design-system.html\">Design system</a>")
	out.WriteString("<a href=\"https://github.com/neverbot/maestro\">Source</a></header>\n")
	out.WriteString("<main>\n")
	out.WriteString(p.Body)
	out.WriteString("\n</main>\n")
	return out.String()
}

func escape(text string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return replacer.Replace(text)
}
