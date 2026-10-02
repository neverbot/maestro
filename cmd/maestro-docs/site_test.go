package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

// buildSite builds the whole site into a temporary directory and returns
// every page it wrote, keyed by its path.
func buildSite(t *testing.T) map[string]string {
	t.Helper()
	out := t.TempDir()
	if err := build("../..", out); err != nil {
		t.Fatalf("build the site: %v", err)
	}
	pages := map[string]string{}
	err := filepath.WalkDir(out, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".html") {
			return err
		}
		body, err := os.ReadFile(p) // #nosec G304 -- a path this test just wrote
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(out, p)
		if err != nil {
			return err
		}
		pages[filepath.ToSlash(rel)] = string(body)
		return nil
	})
	assert.Must(t, err == nil, "walk the built site: %v", err)
	assert.Must(t, len(pages) >= 20, "built %d page(s): the bundle alone is nineteen, so this guard is holding nothing", len(pages))
	return pages
}

// TestEveryPageWearsTheSameFrame holds the one shape this site has.
func TestEveryPageWearsTheSameFrame(t *testing.T) {
	pages := buildSite(t)
	if _, ok := pages["design-system.html"]; !ok {
		t.Fatal("the site has no design-system.html: this guard exists for that page")
	}
	for path, body := range pages {
		for _, part := range []struct{ what, needle string }{
			{"the skip link", `class="skip"`},
			{"the header bar", `<header><div class="bar">`},
			{"the content sheet", `<main id="content">`},
			{"the rail", `<nav class="rail"`},
			{"the site stylesheet", `style.css"`},
		} {
			assert.Should(t, strings.Contains(body, part.needle), "%s carries no %s (%q)", path, part.what, part.needle)
		}
		assert.Should(t, strings.Count(body, "<main") == 1, "%s has %d <main> elements: a page has exactly one", path, strings.Count(body, "<main"))
	}
}

// TestTheHeaderPromisesAPageAndNotAnAnchor holds the fix for the link
// the owner could not place.
func TestTheHeaderPromisesAPageAndNotAnAnchor(t *testing.T) {
	pages := buildSite(t)
	index, ok := pages[agentsIndexPath]
	assert.Must(t, ok, "the site has no %s", agentsIndexPath)
	for path, body := range pages {
		assert.Should(t, !strings.Contains(body, `href="index.html#for-agents">For agents`), "%s still sends For agents at an anchor into the readme", path)
		assert.Should(t, strings.Contains(body, `For agents</a>`), "%s has no For agents link at all", path)
	}
	// Every bundle page is on it, under a group heading.
	bundle, err := bundlePages()
	assert.Must(t, err == nil, "render the bundle: %v", err)
	for _, p := range bundle {
		href := strings.TrimPrefix(p.Path, "agents/")
		assert.Should(t, strings.Contains(index, `href="`+href+`"`), "%s does not link %s", agentsIndexPath, href)
	}
	for _, label := range groupLabels {
		assert.Should(t, strings.Contains(index, ">"+label+"<"), "%s does not group anything under %q", agentsIndexPath, label)
	}
	// And the entry point is first, which is what the path order got
	// wrong: `agents/genres/...` sorts before `agents/skill.md`.
	if first := strings.Index(index, `href="skill.html"`); first < 0 {
		t.Error("the bundle's own entry point is not linked from its index")
	} else if other := strings.Index(index, `href="genres/`); other >= 0 && other < first {
		t.Error("the index still leads with the worked examples: skill is where an agent starts")
	}
}

// TestNoPageFetchesAnotherOrigin holds the CSP argument on a site that
// has no CSP.
func TestNoPageFetchesAnotherOrigin(t *testing.T) {
	for path, body := range buildSite(t) {
		for _, attr := range []string{`<img`, `<script`} {
			for _, fragment := range strings.Split(body, attr)[1:] {
				tag, _, _ := strings.Cut(fragment, ">")
				assert.Should(t, !strings.Contains(tag, `src="http`), "%s fetches %s from another origin: %s", path, attr, strings.TrimSpace(tag))
			}
		}
	}
}

// TestTheSiteAnswersAMistypedAddress is the third negative state on the
// one surface that can reach it: GitHub Pages serves /404.html for a
// path it does not have, and without one the reader leaves the design
// entirely at the moment they most need a way back.
func TestTheSiteAnswersAMistypedAddress(t *testing.T) {
	pages := buildSite(t)
	body, ok := pages["404.html"]
	assert.Must(t, ok, "the site has no 404.html")
	assert.Should(t, strings.Contains(body, `href="index.html"`), "the refusal page offers no way back")
}

// TestEverySectionCanBeLinked: a page whose headings have no ids is a
// page whose rail cannot point at them and whose sections cannot be
// sent to anybody.
func TestEverySectionCanBeLinked(t *testing.T) {
	for path, body := range buildSite(t) {
		assert.Should(t, !strings.Contains(body, "<h2>") || strings.Contains(body, `<nav class="rail"`), "%s has an h2 with no id", path)
		// The rail's own headings are the exception: they are furniture,
		// not sections of the page.
		content := body
		if from := strings.Index(body, `<nav class="rail"`); from >= 0 {
			content = body[:from]
		}
		assert.Should(t, !strings.Contains(content, "<h2>"), "%s carries an h2 with no id, so nothing can link to it", path)
	}
}

// TestTheRailsGroupsAreTheBundleAndNothingElse.
func TestTheRailsGroupsAreTheBundleAndNothingElse(t *testing.T) {
	pages, err := collect("../..")
	assert.Must(t, err == nil, "collect the pages: %v", err)
	for _, group := range grouped(pages) {
		for _, p := range group {
			if !strings.HasPrefix(p.Path, "agents/") {
				t.Errorf("%s is not a page of the bundle and the rail groups it under %q",
					p.Path, groupLabels[groupOf(group[0])])
			}
		}
	}
}

// TestEveryPageSaysWhereItIs holds the breadcrumb the frame specifies
// and the site shipped without, on pages three levels deep.
func TestEveryPageSaysWhereItIs(t *testing.T) {
	pages := buildSite(t)
	for path, body := range pages {
		// The home page is the exception and it is the only one: the
		// site root is not inside anything, and a trail of one crumb
		// printed "Maestro" above an h1 reading "Maestro" under a
		// wordmark reading "Maestro".
		if path == "index.html" {
			assert.Should(t, !strings.Contains(body, `<nav class="crumbs"`), "the home page carries a breadcrumb of one crumb")
			continue
		}
		assert.Should(t, strings.Contains(body, `<nav class="crumbs"`), "%s has no breadcrumb", path)
		assert.Should(t, strings.Contains(body, `<b aria-current="page">`), "%s has a breadcrumb that does not mark the page you are on", path)
	}
	deep := pages["agents/reference/tools.html"]
	for _, crumb := range []string{">Maestro<", ">For agents<", ">Reference<", ">The tool surface<"} {
		assert.Should(t, strings.Contains(deep, crumb), "the tool surface's breadcrumb has no %s", crumb)
	}
	// And the trail is climbable: every crumb above the last one is a
	// link, and each one is relative to a page two directories deep.
	for _, link := range []string{
		`href="../../index.html">Maestro<`,
		`href="../../agents/index.html">For agents<`,
		`href="../../agents/index.html#reference">Reference<`,
	} {
		assert.Should(t, strings.Contains(deep, link), "the tool surface's breadcrumb does not climb: no %s", link)
	}
}

// TestTheSiteCarriesItsOwnMap is the site's answer to "where is the
// thing called X" on a site that ships no JavaScript: one page holding
// every page and every section, which the browser's own find searches.
func TestTheSiteCarriesItsOwnMap(t *testing.T) {
	pages := buildSite(t)
	body, ok := pages[mapPath]
	assert.Must(t, ok, "the site has no %s", mapPath)
	for path := range pages {
		if path == mapPath || path == "404.html" {
			continue
		}
		assert.Should(t, strings.Contains(body, `"`+path), "%s is on the site and not on its map", path)
	}
	// Every section of the longest page is on it, by anchor.
	for _, want := range []string{
		"agents/reference/tools.html#analysis",
		"agents/reference/tools.html#relation_types",
		"running.html#known-limitations",
	} {
		assert.Should(t, strings.Contains(body, want), "the map does not carry %s", want)
	}
	// Every entry opens the page it names, and none of them does it in
	// a second shape.
	assert.Should(t, !strings.Contains(body, "Open the page"), "the map still special-cases a page with no sections with its own link shape")
	assert.Should(t, strings.Contains(body, `<summary><b><a href="index.html">Maestro</a></b>`), "a page's title on the map is not a link to it")
	// It discloses progressively and still answers a find: a folded page
	// whose words the browser cannot see would defeat the one thing this
	// page is for.
	assert.Should(t, strings.Count(body, "<details open>") >= 20, "the map has %d open sections: they ship open so find-in-page reads them",
		strings.Count(body, "<details open>"))
}

// TestAMachineNameKeepsItsOwnVoice holds the Copyable Is Mono Rule on
// the eleven headings that are wire names rather than sentences.
func TestAMachineNameKeepsItsOwnVoice(t *testing.T) {
	pages := buildSite(t)
	tools := pages["agents/reference/tools.html"]
	for _, name := range []string{"analysis", "relation_types", "entities"} {
		assert.Should(t, strings.Contains(tools, `id="`+name+`" class="ident"`), "the heading %q is not marked as a machine name", name)
	}
	// And a sentence is not: the rule is a shape, so it has to refuse as
	// well as admit.
	home := pages["index.html"]
	assert.Should(t, !strings.Contains(home, `id="known-limitations" class="ident"`), `"Known limitations" is a sentence and is marked as a machine name`)
	assert.Should(t, strings.Contains(siteCSS, ".ident { font-family: var(--mono)"), "nothing sets the machine-name voice, so the class is a mechanism nothing reads")
}

// TestNothingOnTheSiteScrollsSideways is the specificity bug that
// shipped: `.page.wide` is two classes and the responsive override was
// one, so the design-system page kept its two columns on a phone.
func TestNothingOnTheSiteScrollsSideways(t *testing.T) {
	if !strings.Contains(siteCSS, ".page, .page.wide { grid-template-columns: minmax(0, 1fr); }") {
		t.Error("the narrow layout does not name .page.wide, so the one page that opts out of the " +
			"reading measure also opts out of folding its rail")
	}
}

// TestTheStylesheetHasNoDeadOrMissingTokens holds two halves of the same
// rule: a token nothing declares and a rule nothing renders are both
// "a mechanism nothing reads".
func TestTheStylesheetHasNoDeadOrMissingTokens(t *testing.T) {
	used := regexp.MustCompile(`var\(\s*(--[a-z0-9-]+)`).FindAllStringSubmatch(siteCSS, -1)
	for _, m := range used {
		if !strings.Contains(siteCSS, m[1]+":") {
			t.Errorf("%s is read and never declared, so every rule that uses it silently does nothing", m[1])
		}
	}
	// And every element the stylesheet dresses is an element some page
	// emits. `footer` was styled for a foot no page had.
	pages := buildSite(t)
	for _, tag := range []string{"footer", "main", "summary"} {
		if !strings.Contains(siteCSS, tag+" ") && !strings.Contains(siteCSS, tag+" {") {
			continue
		}
		found := false
		for _, body := range pages {
			if strings.Contains(body, "<"+tag) {
				found = true
				break
			}
		}
		assert.Should(t, found, "the stylesheet dresses <%s> and no page emits one", tag)
	}
}

// --- The screenshots, and the index that dates them -------------------

// imageDir and imageIndex are the directory the site's screenshots live
// in and the file that says what each one is.
const (
	imageDir   = "../../docs/images"
	imageIndex = "../../docs/images/readme.md"
)

// TestEveryImageIsInTheIndex is the guard the images exist behind.
func TestEveryImageIsInTheIndex(t *testing.T) {
	entries, err := os.ReadDir(imageDir)
	assert.Must(t, err == nil, "read %s: %v", imageDir, err)
	index, err := os.ReadFile(imageIndex)
	assert.Must(t, err == nil, "read %s: %v", imageIndex, err)
	listed := string(index)

	var files []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".png") {
			continue
		}
		files = append(files, entry.Name())
		assert.Should(t, strings.Contains(listed, "`"+entry.Name()+"`"), "%s is published and has no row in docs/images/readme.md: nothing says what it "+
			"shows, where it was taken or when it stopped being true", entry.Name())
	}
	assert.Must(t, len(files) != 0, "no image found under docs/images: this guard is holding nothing")

	// And the other direction: a row for an image somebody deleted.
	for _, row := range regexp.MustCompile("`([\\w-]+\\.png)`").FindAllStringSubmatch(listed, -1) {
		if _, err := os.Stat(filepath.Join(imageDir, row[1])); err != nil {
			t.Errorf("docs/images/readme.md has a row for %s and there is no such file", row[1])
		}
	}
}

// TestEveryPublishedImageIsCarriedAndUsed holds the two halves that make
// an image reach a reader: it is copied into the site, and some page
// points at it. An image nobody shows is weight in a repository.
func TestEveryPublishedImageIsCarriedAndUsed(t *testing.T) {
	pages := buildSite(t)
	entries, err := os.ReadDir(imageDir)
	assert.Must(t, err == nil, "read %s: %v", imageDir, err)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".png") {
			continue
		}
		shown := false
		for _, body := range pages {
			if strings.Contains(body, "images/"+entry.Name()) {
				shown = true
				break
			}
		}
		assert.Should(t, shown, "%s is committed and no page shows it", entry.Name())
	}
}

// TestTheFrontPageIsNotTheReadme.
func TestTheFrontPageIsNotTheReadme(t *testing.T) {
	pages := buildSite(t)
	front, ok := pages["index.html"]
	assert.Must(t, ok, "the site has no index.html")
	for _, operator := range []string{"docker compose", "FIRST_ADMIN_PASSWORD", "TEST_DATABASE_URL"} {
		assert.Should(t, !strings.Contains(front, operator), "the front page carries %q: that belongs on running.html", operator)
	}
	if _, ok := pages["running.html"]; !ok {
		t.Error("the readme is not published anywhere: an operator still needs it")
	}
	// It shows the product rather than only describing it.
	assert.Should(t, strings.Contains(front, "<figure>"), "the front page shows no screenshot of the product it describes")
}

// TestEveryWrittenPageIsAMarkdownFile is the guard on where this site's
// prose lives.
func TestEveryWrittenPageIsAMarkdownFile(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "..", siteDir))
	assert.Must(t, err == nil, "read %s: %v", siteDir, err)
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			names = append(names, strings.TrimSuffix(entry.Name(), ".md"))
		}
	}
	assert.Must(t, len(names) >= 4, "%s holds %d page(s): the front page, the bundle's index, the map and the "+
		"refusal page are all written here", siteDir, len(names))

	// Every one of them renders, and none is left asking for a part this
	// generator does not build.
	for _, name := range names {
		body, _, _, err := sitePage(filepath.Join("..", ".."), name, map[string]string{
			"pages": "<p>pages</p>", "map": "<p>map</p>",
		})
		if err != nil {
			t.Errorf("%s.md: %v", name, err)
			continue
		}
		assert.Should(t, !strings.Contains(body, "{{"), "%s.md leaves an unexpanded part in its output", name)
	}

	// And nothing in the built site still carries a marker.
	for path, body := range buildSite(t) {
		assert.Should(t, !strings.Contains(body, "{{"), "%s ships an unexpanded {{part}}", path)
	}
}

// TestAPictureOnItsOwnLineIsAFigure holds the one shape markdown cannot
// express and this site needs: a picture, its caption, and the size read
// off the file so the page does not jump while it loads.
func TestAPictureOnItsOwnLineIsAFigure(t *testing.T) {
	front := buildSite(t)["index.html"]
	assert.Should(t, !strings.Contains(front, "<p><img"), "an image is still a bare paragraph: the figure transform did not run")
	for _, want := range []string{"<figure><img ", "<figcaption>", `width="1440" height="815"`} {
		assert.Should(t, strings.Contains(front, want), "the front page's figures carry no %s", want)
	}
	// The size is read and not written down: a picture of another size
	// gets its own.
	w, h, ok := pngSize(filepath.Join("..", "..", "docs", "images", "quest-chain.png"))
	assert.Should(t, ok && w == 1440 && h == 815, "pngSize read %dx%d (ok=%v) from a 1440x815 screenshot", w, h, ok)
}

// **The design system is three files that move together, and nothing
// made them.** docs/design.md argues each named rule, and
// docs/design-tokens.json carries the sentence the generated page
// renders — so a rule written in one and not the other is a rule the
// design system states and the design system's own page does not have.
// Found by writing one: the Band Rule landed in the document and the
// page kept rendering eleven rules with nothing red anywhere.
func TestEveryNamedRuleIsInBothHalvesOfTheDesignSystem(t *testing.T) {
	t.Parallel()
	document, err := os.ReadFile(filepath.Join("..", "..", "docs", "design.md"))
	assert.NoErr(t, err, "read design.md")
	sidecar, err := os.ReadFile(filepath.Join("..", "..", "docs", "design-tokens.json"))
	assert.NoErr(t, err, "read design-tokens.json")

	// `**The Something Rule.**` at the start of a line is how the
	// document declares one.
	declared := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\*\*(The [^*.]+ Rule)\.\*\*`).FindAllStringSubmatch(string(document), -1) {
		declared[m[1]] = true
	}
	assert.Must(t, len(declared) > 8, "found %d named rules in design.md; this guard is reading the wrong place", len(declared))

	var side struct {
		Narrative struct {
			Rules []struct {
				Name string `json:"name"`
			} `json:"rules"`
		} `json:"narrative"`
	}
	assert.NoErr(t, json.Unmarshal(sidecar, &side), "parse design-tokens.json")

	carried := map[string]bool{}
	for _, rule := range side.Narrative.Rules {
		carried[rule.Name] = true
	}

	var missing, extra []string
	for name := range declared {
		if !carried[name] {
			missing = append(missing, name)
		}
	}
	for name := range carried {
		if !declared[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	assert.Must(t, len(missing) == 0, "design.md argues %s and design-tokens.json carries neither the sentence nor the page", strings.Join(missing, ", "))
	assert.Must(t, len(extra) == 0, "design-tokens.json carries %s, which design.md does not argue", strings.Join(extra, ", "))
}
