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
	"html"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
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
	// Blurb is the page's own first sentence, quoted rather than
	// written: the index of the bundle is a list of names without it,
	// and a list of names does not say which page to open.
	Blurb string
	// Wide drops the reading measure for a page whose content is not
	// prose. Exactly one page is: the design system is a specimen sheet,
	// and a colour grid squeezed into a paragraph's width is a specimen
	// of nothing. It keeps the header, the sheet and the rail.
	Wide bool
	// Stylesheets are the extra sheets this page needs, relative to the
	// site root. Only the design system has one: it is a specimen sheet
	// and its own rules are its content.
	Stylesheets []string
	// Sections are the page's own h2 headings, in order, for the rail.
	// A page whose headings a reader cannot see from the top is a page
	// they have to scroll to survey, which is what 58 tools under 11
	// headings with no index was.
	Sections []section
}

// section is one h2 of a page: the text a reader sees and the id an
// anchor lands on.
type section struct {
	ID   string
	Text string
}

// headingRE is an h2 of a rendered page. The generator owns both ends of
// this: goldmark writes the tag and this reads it back, so a heading
// with inline markup in it (a <code> span, an emphasis) is matched and
// its text is taken with the tags stripped.
var headingRE = regexp.MustCompile(`<h2>(.*?)</h2>`)

// tagRE strips inline markup out of a heading before it becomes an id.
var tagRE = regexp.MustCompile(`<[^>]+>`)

// nonWord is every run of characters that is not a word in a slug.
var nonWord = regexp.MustCompile(`[^a-z0-9]+`)

// anchor gives every h2 an id and returns the list, so the rail can
// carry the page's own contents and a reader can link a section.
func anchor(body string) (string, []section) {
	var out []section
	taken := map[string]bool{}
	rendered := headingRE.ReplaceAllStringFunc(body, func(match string) string {
		inner := headingRE.FindStringSubmatch(match)[1]
		text := strings.TrimSpace(html.UnescapeString(tagRE.ReplaceAllString(inner, "")))
		id := strings.Trim(nonWord.ReplaceAllString(strings.ToLower(text), "-"), "-")
		if id == "" {
			return match
		}
		// Two sections with one name would give two elements one id, and
		// an anchor that lands on whichever the browser saw first.
		for n := 2; taken[id]; n++ {
			id = fmt.Sprintf("%s-%d", strings.Trim(nonWord.ReplaceAllString(strings.ToLower(text), "-"), "-"), n)
		}
		taken[id] = true
		out = append(out, section{ID: id, Text: text})
		return `<h2 id="` + id + `">` + inner + `</h2>`
	})
	return rendered, out
}

// withoutFrontmatter drops a bundle page's YAML header.
//
// **It was being published as a heading.** `skill.md` opens with
// `---\nname: maestro\ndescription: ...\n---`, and markdown reads a line
// of dashes under text as a setext heading: the skill page's first
// section heading, on the published site, was the words "name: maestro"
// followed by the whole description as a paragraph. The frontmatter is
// for the agent's client, which reads it off the file; a person reading
// the page is being shown the envelope.
func withoutFrontmatter(body string) string {
	if !strings.HasPrefix(body, "---\n") {
		return body
	}
	rest := body[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return body
	}
	return strings.TrimLeft(rest[end+len("\n---\n"):], "\n")
}

// blurbOf is a page's own first sentence, taken from its markdown: the
// frontmatter, the generator's comment line and the h1 are skipped, and
// what is left is the first paragraph, cut at its first full stop.
//
// **It is quoted and not written.** A description of a page written
// beside the page is the second description this whole generator exists
// to refuse; the page's own opening sentence is the page saying what it
// is.
func blurbOf(body string) string {
	lines := strings.Split(body, "\n")
	i := 0
	if i < len(lines) && strings.TrimSpace(lines[i]) == "---" {
		for i++; i < len(lines) && strings.TrimSpace(lines[i]) != "---"; i++ {
		}
		i++
	}
	var paragraph []string
	for ; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "<!--") {
			if len(paragraph) > 0 {
				break
			}
			continue
		}
		paragraph = append(paragraph, line)
	}
	text := strings.ReplaceAll(strings.Join(paragraph, " "), "**", "")
	text = strings.ReplaceAll(text, "`", "")
	if cut := strings.Index(text, ". "); cut >= 0 {
		text = text[:cut+1]
	}
	return text
}

// fontFiles are the faces the site sets its two voices in: the same
// files the product serves, copied rather than re-fetched.
//
// **A documentation site that names a typeface it does not carry is the
// same defect as a design document that names one the product does not
// serve**, which is what the typography section of docs/design.md said
// about itself until the fonts were vendored. The design system page in
// particular renders specimens of both voices; without these it renders
// them in whatever the reader's machine has, under a heading that says
// which face it is.
var fontFiles = []string{
	"literata-var-latin.woff2",
	"fira-sans-400-latin.woff2",
	"fira-sans-600-latin.woff2",
}

// copyFonts puts them beside the stylesheet that asks for them.
func copyFonts(root, out string) error {
	dir := filepath.Join(out, "fonts")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("make %s: %w", dir, err)
	}
	for _, name := range fontFiles {
		//nolint:gosec // Both paths are the caller's own flags; see the copy above.
		body, err := os.ReadFile(filepath.Join(root, "internal", "web", "static", "vendor", "fonts", name))
		if err != nil {
			return fmt.Errorf("read the vendored %s: %w", name, err)
		}
		// gosec reads `name` as tainted because it comes from a package
		// variable; it is one of three constants in `fontFiles` above,
		// and the destination is the caller's own -o flag, exactly as
		// every other write in this file.
		//nolint:gosec // G703: the names are this file's own constants, not input.
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
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
		if err := os.WriteFile(full, []byte(shell(p, depthOf(p.Path), pages)), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", full, err)
		}
	}
	if err := copyFile(filepath.Join(root, "docs", "design-system.css"), filepath.Join(out, "design-system.css")); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "style.css"), []byte(siteCSS), 0o600); err != nil {
		return fmt.Errorf("write the stylesheet: %w", err)
	}
	if err := copyFonts(root, out); err != nil {
		return err
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

	//nolint:gosec // -root is the caller's own flag; this reads this repository.
	readme, err := os.ReadFile(filepath.Join(root, "readme.md"))
	if err != nil {
		return nil, fmt.Errorf("read readme.md: %w", err)
	}
	home, err := markdown.RenderDoc(rewriteLinks(string(readme)))
	if err != nil {
		return nil, err
	}
	home, homeSections := anchor(scrollers(stripRemoteImages(home)) + agentsPointer())
	pages = append(pages, page{Path: "index.html", Title: "", Body: home, Sections: homeSections})

	// The licence, because the readme links to it and a link that
	// downloads a file instead of opening a page is a broken link with
	// an excuse.
	//
	// Spelled `license` throughout, after the file: the American
	// spelling of the noun is a *racing* concept in this product's own
	// genre templates, and `TestNoGenreVocabularyInServerCode` — rightly
	// — refuses a genre word in a server identifier. It caught this one.
	//nolint:gosec // -root is the caller's own flag; this reads this repository.
	terms, err := os.ReadFile(filepath.Join(root, "license.md"))
	if err != nil {
		return nil, fmt.Errorf("read license.md: %w", err)
	}
	termsBody, err := markdown.RenderDoc(string(terms))
	if err != nil {
		return nil, err
	}
	termsBody, termsSections := anchor(scrollers(termsBody))
	pages = append(pages, page{Path: "license.html", Title: "License", Body: termsBody, Sections: termsSections})

	system, err := designSystemPage(root)
	if err != nil {
		return nil, err
	}
	pages = append(pages, system)

	bundle, err := bundlePages()
	if err != nil {
		return nil, err
	}
	pages = append(pages, agentsIndexPage(bundle))
	pages = append(pages, bundle...)
	pages = append(pages, notFoundPage())
	return pages, nil
}

// The two markers docs/design-system.mjs writes around the part of its
// page that is content rather than chrome. They are a contract between
// the two generators and a build that cannot find them fails: the design
// system was copied whole once, which is how the site came to have one
// page with its own header, its own wordmark, twice everything else's
// width and no link back to anywhere.
const (
	systemBodyStart = "<!-- site:body -->"
	systemBodyEnd   = "<!-- /site:body -->"
)

// designSystemPage is the generated design system, as one more page of
// this site. Its body is its own — it is a specimen sheet and it should
// look like one — and its frame is the site's, stated once in shell.
func designSystemPage(root string) (page, error) {
	//nolint:gosec // -root is the caller's own flag; this reads this repository.
	raw, err := os.ReadFile(filepath.Join(root, "docs", "design-system.html"))
	if err != nil {
		return page{}, fmt.Errorf("read the generated design system: %w", err)
	}
	src := string(raw)
	from := strings.Index(src, systemBodyStart)
	to := strings.Index(src, systemBodyEnd)
	if from < 0 || to < from {
		return page{}, fmt.Errorf(
			"docs/design-system.html carries no %s ... %s markers: docs/design-system.mjs writes them "+
				"and this generator reads between them, so the page can wear the site's own frame "+
				"instead of being copied whole with a second one",
			systemBodyStart, systemBodyEnd)
	}
	body, sections := anchor(src[from+len(systemBodyStart) : to])
	return page{
		Path:        "design-system.html",
		Title:       "Design system",
		Wide:        true,
		Body:        body,
		Sections:    sections,
		Stylesheets: []string{"design-system.css"},
	}, nil
}

// copyFile puts one generated file beside the pages that ask for it.
func copyFile(from, to string) error {
	//nolint:gosec // Both paths are built from the caller's own flags.
	body, err := os.ReadFile(from)
	if err != nil {
		return fmt.Errorf("read %s: %w", from, err)
	}
	//nolint:gosec // The output directory is the caller's own -o flag.
	if err := os.WriteFile(to, body, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", to, err)
	}
	return nil
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
		rendered, err := markdown.RenderDoc(rewriteLinks(withoutFrontmatter(string(body))))
		if err != nil {
			return nil, err
		}
		rendered, sections := anchor(scrollers(stripRemoteImages(rendered)))
		pages = append(pages, page{
			Path:     path.Join("agents", strings.TrimSuffix(name, ".md")+".html"),
			Title:    titleOf(string(body), name),
			Body:     rendered,
			Blurb:    blurbOf(string(body)),
			Sections: sections,
		})
	}
	return pages, nil
}

// --- The bundle, as a destination ------------------------------------

// AGENTS_INDEX is where the header's "For agents" link goes.
//
// **It was an anchor, and the anchor was the problem.** The link pointed
// at `index.html#for-agents`: an h2 at 5,951 pixels down a 6,621-pixel
// page, so a reader who clicked it landed with the whole navigation
// scrolled off the top, looking at nineteen bullets sorted by
// repository path — `agents/genres/...` first and `skill`, the bundle's
// own entry point, last. A label that promises a destination gets a
// destination.
const agentsIndexPath = "agents/index.html"

// groupLabels name the bundle's four directories in the reader's words.
// The directory is the grouping the bundle already has; this only says
// it out loud, in the order a person reads them rather than the order
// the filesystem sorts them.
var groupOrder = []string{"", "reference", "modelling", "recipes", "genres"}

var groupLabels = map[string]string{
	"":          "Start here",
	"reference": "Reference",
	"modelling": "Modelling",
	"recipes":   "Recipes",
	"genres":    "Worked examples",
}

// groupOf is the directory a bundle page sits in, under agents/. A page
// that is not in the bundle is in no group, and says so with a value no
// directory can have: the first version returned "" for both "the
// bundle's root" and "not the bundle", which put the licence, the design
// system and the refusal page in the rail's "Start here".
const notInBundle = "-"

func groupOf(p page) string {
	if !strings.HasPrefix(p.Path, "agents/") {
		return notInBundle
	}
	rest := strings.TrimPrefix(p.Path, "agents/")
	if dir := path.Dir(rest); dir != "." {
		return dir
	}
	return ""
}

// grouped splits the bundle into the four groups, in reading order.
func grouped(bundle []page) [][]page {
	out := make([][]page, 0, len(groupOrder))
	for _, key := range groupOrder {
		var group []page
		for _, p := range bundle {
			if groupOf(p) == key && p.Path != agentsIndexPath {
				group = append(group, p)
			}
		}
		if len(group) > 0 {
			out = append(out, group)
		}
	}
	return out
}

// agentsIndexPage is the page the header's second link opens: every page
// of the bundle, in its own groups, each with its own first sentence.
func agentsIndexPage(bundle []page) page {
	var out strings.Builder
	out.WriteString("<h1>What an agent is told</h1>\n")
	out.WriteString("<p>These are the pages this instance serves to an agent, published so a " +
		"person can read what their agent was handed. They are the bundle itself, not a " +
		"description of it: the same files, rendered.</p>\n")
	out.WriteString(`<div class="index">` + "\n")
	for _, group := range grouped(bundle) {
		out.WriteString("<section>\n<h3>" + escape(groupLabels[groupOf(group[0])]) + "</h3>\n<ul>\n")
		for _, p := range group {
			href := strings.TrimPrefix(p.Path, "agents/")
			out.WriteString(`<li><a href="` + href + `"><b>` + escape(p.Title) + "</b></a>")
			if p.Blurb != "" {
				out.WriteString("<span>" + escape(p.Blurb) + "</span>")
			}
			out.WriteString("</li>\n")
		}
		out.WriteString("</ul>\n</section>\n")
	}
	out.WriteString("</div>\n")
	return page{Path: agentsIndexPath, Title: "What an agent is told", Body: out.String()}
}

// agentsPointer is what the home page keeps where the list used to be: a
// sentence and the way in. The `for-agents` id stays, because the link
// that pointed at it is in a published readme and in this product's own
// onboarding, and an id removed is a link broken.
func agentsPointer() string {
	return "\n<h2 id=\"for-agents\">What an agent is told</h2>\n" +
		"<p>This instance serves its agents a skill bundle: how to declare a game's own " +
		"vocabulary, how to fill it, and a worked example per genre. It is published here, so a " +
		"person can read what their agent was handed. " +
		"<a href=\"" + agentsIndexPath + "\">Read the bundle</a>.</p>\n"
}

// notFoundPage is the third negative state, on the one surface that can
// reach it. GitHub Pages serves /404.html for a path it does not have,
// and without this the reader leaves the design entirely at the exact
// moment they most need a way back.
func notFoundPage() page {
	return page{
		Path:  "404.html",
		Title: "Nothing here",
		Body: "<h1>There is nothing at this address</h1>\n" +
			"<p>This site has no page at the address you asked for. It may have been a typo, or a " +
			"link to a page that has since been renamed.</p>\n" +
			"<p><a href=\"index.html\">Back to the readme</a></p>\n",
	}
}

// stripRemoteImages removes every image the site would fetch from
// another origin.
//
// The readme carries two shields.io badges, which on this site are the
// only chromatic pixels above the fold — a saturated blue and an orange
// that belong to neither of this palette's two chromatic exemptions —
// and they are GitHub's chrome verbatim, which docs/product.md names as
// an anti-reference. They are also two requests to a third-party origin
// from a product whose own CSP forbids exactly that: the design-system
// page's stylesheet comment records making this mistake once already.
//
// They stay in readme.md, where they are read on GitHub and are the
// right furniture. They do not come here.
var remoteImageRE = regexp.MustCompile(`<img[^>]+src="https?://[^"]*"[^>]*>`)

func stripRemoteImages(body string) string {
	return remoteImageRE.ReplaceAllString(body, "")
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

// selfNamingLink is a markdown link whose label is the file it points
// at: `[license.md](license.md)`.
var selfNamingLink = regexp.MustCompile(`\[([^\]]+)\.md\]\(([^)]+)\.md\)`)

// rewriteLinks points a markdown link at a neighbouring `.md` file at
// the page this generator wrote for it. A link that still said `.md`
// would download a file instead of opening a page.
//
// **The label is rewritten too when the label *is* the filename.** The
// home page read "MIT. See license.md." over a link to `license.html`:
// the href was right and the words named a file this site does not
// serve. Only a label that is exactly its own target is touched; a
// sentence that happens to mention a filename is prose and stays as it
// was written.
func rewriteLinks(body string) string {
	body = selfNamingLink.ReplaceAllString(body, "[$1]($2.html)")
	return strings.ReplaceAll(body, ".md)", ".html)")
}

// scrollers put every table in its own horizontal scroller, which is the
// frame's rule for wide content: a table scrolls inside its container
// and never wraps its rows into two lines.
func scrollers(body string) string {
	body = strings.ReplaceAll(body, "<table>", `<div class="scroller"><table>`)
	return strings.ReplaceAll(body, "</table>", "</table></div>")
}

// depthOf is how many directories deep a page sits, so its links to the
// stylesheet and the home page are relative and the site works from any
// prefix — a project page on GitHub Pages is served under /maestro/, and
// an absolute path would be wrong there and right nowhere else.
func depthOf(p string) int {
	return strings.Count(p, "/")
}

// railFor is the navigation beside the content: where this page sits in
// the site, what else sits beside it, and what is on it.
//
// The frame gives the rail 280px, sticky under the header, folding
// beneath the content below 1100px. What it carries here is what is true
// of the whole site — its pages — plus this page's own headings, which
// is the difference between surveying 58 tools and scrubbing 3,848
// pixels for them.
func railFor(p page, bundle []page, up string) string {
	var out strings.Builder
	out.WriteString(`<nav class="rail" aria-label="Site">` + "\n")

	out.WriteString("<section>\n<h2>Maestro</h2>\n<ul>\n")
	for _, entry := range []struct{ href, label string }{
		{"index.html", "Readme"},
		{agentsIndexPath, "What an agent is told"},
		{"design-system.html", "Design system"},
		{"license.html", "License"},
	} {
		out.WriteString(railLink(up+entry.href, entry.label, p.Path == entry.href))
	}
	out.WriteString("</ul>\n</section>\n")

	// Inside the bundle, the pages beside this one. A reader in the
	// middle of `reference/queries` wants the rest of the reference, not
	// the whole bundle.
	if strings.HasPrefix(p.Path, "agents/") && p.Path != agentsIndexPath {
		key := groupOf(p)
		for _, group := range grouped(bundle) {
			if groupOf(group[0]) != key {
				continue
			}
			out.WriteString("<section>\n<h2>" + escape(groupLabels[key]) + "</h2>\n<ul>\n")
			for _, sibling := range group {
				out.WriteString(railLink(up+sibling.Path, sibling.Title, sibling.Path == p.Path))
			}
			out.WriteString("</ul>\n</section>\n")
		}
	}

	// Two headings are a page's shape already visible from the top; the
	// list earns its space from three.
	if len(p.Sections) >= 3 {
		out.WriteString("<section>\n<h2>On this page</h2>\n<ul>\n")
		for _, s := range p.Sections {
			out.WriteString(railLink("#"+s.ID, s.Text, false))
		}
		out.WriteString("</ul>\n</section>\n")
	}

	out.WriteString("</nav>\n")
	return out.String()
}

func railLink(href, label string, current bool) string {
	mark := ""
	if current {
		mark = ` aria-current="page"`
	}
	return `<li><a href="` + href + `"` + mark + ">" + escape(label) + "</a></li>\n"
}

func shell(p page, depth int, bundle []page) string {
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
	for _, sheet := range p.Stylesheets {
		out.WriteString("<link rel=\"stylesheet\" href=\"" + up + sheet + "\">\n")
	}
	out.WriteString("<a class=\"skip\" href=\"#content\">Skip to the content</a>\n")
	out.WriteString("<header><div class=\"bar\">")
	out.WriteString("<a class=\"brand\" href=\"" + up + "index.html\">Maestro</a>")
	out.WriteString(headerLink(up+agentsIndexPath, "For agents", strings.HasPrefix(p.Path, "agents/")))
	out.WriteString(headerLink(up+"design-system.html", "Design system", p.Path == "design-system.html"))
	out.WriteString("<a href=\"https://github.com/neverbot/maestro\">Source</a>")
	out.WriteString("</div></header>\n")
	frame := "page"
	if p.Wide {
		frame = "page wide"
	}
	out.WriteString("<div class=\"" + frame + "\">\n<main id=\"content\">\n")
	out.WriteString(p.Body)
	out.WriteString("\n</main>\n")
	out.WriteString(railFor(p, bundle, up))
	out.WriteString("</div>\n")
	return out.String()
}

func headerLink(href, label string, current bool) string {
	mark := ""
	if current {
		mark = ` aria-current="page"`
	}
	return `<a href="` + href + `"` + mark + ">" + escape(label) + "</a>"
}

func escape(text string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return replacer.Replace(text)
}
