package web_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

// The provenance, payload and resolution guards for the vendored half of
// internal/web/static.

const (
	vendorRoot   = "static/vendor"
	manifestPath = vendorRoot + "/manifest.json"
	licensesDir  = "licenses"

	// **Two budgets, because there are two questions.** There was one —
	// 150KB over everything vendored, described as "everything this
	// front end makes a browser download before it can draw anything" —
	// and the day the two typefaces were vendored that description
	// stopped being true of half of it. A font declared
	// `font-display: swap` is downloaded *after* the page has drawn: the
	// browser paints in the fallback and re-flows when it arrives.
	// Counting it against a first-paint budget would have meant either
	// giving up the design system's central rule or quietly redefining
	// what the number measures, and both are worse than splitting it.
	renderBlockingBudget = 150 * 1024

	// fontBudget is the latin subsets of the two voices: Literata
	// variable for the game's words and Fira Sans at 400 and 600 for the
	// tool's. 128KB is what those three files cost plus room for a
	// weight, and not a byte for a third family — Fira Mono was
	// deliberately not vendored, because monospace is the Copyable role
	// and machine text does not care whose mono it is.
	fontBudget = 128 * 1024
)

// fontsPrefix is the one path convention these budgets rest on: a
// vendored font lives under it and nothing else does.
const fontsPrefix = "fonts/"

// vendoredFile is one entry of internal/web/static/vendor/manifest.json.
// The fields this test does not read (`url_note`) are deliberately
// absent from the struct: they are prose for a human, and asserting on
// prose is how a guard becomes a spelling test.
type vendoredFile struct {
	Path        string `json:"path"`
	Package     string `json:"package"`
	Version     string `json:"version"`
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
	Bytes       int64  `json:"bytes"`
	License     string `json:"license"`
	LicensePath string `json:"license_path"`
	LicenseURL  string `json:"license_url"`
}

type vendorManifest struct {
	Files []vendoredFile `json:"files"`
}

func readVendorManifest(t *testing.T) vendorManifest {
	t.Helper()
	raw, err := os.ReadFile(manifestPath)
	assert.Must(t, err == nil, "read %s: %v", manifestPath, err)
	// The manifest carries prose fields this struct does not model, so
	// unknown fields are tolerated; what is not tolerated is malformed
	// JSON, which would otherwise leave every test below reading an
	// empty manifest and passing.
	var manifest vendorManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse %s: %v", manifestPath, err)
	}
	assert.Must(t, len(manifest.Files) != 0, "%s lists no files: every test in this file would pass vacuously", manifestPath)
	return manifest
}

// TestVendoredFilesMatchTheirManifest walks the manifest and checks each
// entry against the bytes on disk. A file replaced by a newer upstream
// release without its hash and size being updated in the same commit is
// caught here, and only here.
func TestVendoredFilesMatchTheirManifest(t *testing.T) {
	t.Parallel()
	manifest := readVendorManifest(t)

	for _, entry := range manifest.Files {
		if entry.Path == "" || entry.Package == "" || entry.Version == "" || entry.URL == "" {
			t.Errorf("%s: an entry is missing path, package, version or url: %+v", manifestPath, entry)
			continue
		}
		full := filepath.Join(vendorRoot, filepath.FromSlash(entry.Path))
		raw, err := os.ReadFile(full)
		if err != nil {
			t.Errorf("%s lists %q, which is not in the tree: %v", manifestPath, entry.Path, err)
			continue
		}
		sum := sha256.Sum256(raw)
		if got := hex.EncodeToString(sum[:]); got != entry.SHA256 {
			t.Errorf("%s: sha256 = %s, manifest says %s\n"+
				"the bytes on disk are not the bytes this repository claims to have vendored",
				entry.Path, got, entry.SHA256)
		}
		if got := int64(len(raw)); got != entry.Bytes {
			t.Errorf("%s: %d bytes on disk, manifest says %d", entry.Path, got, entry.Bytes)
		}
		assert.Should(t, strings.HasPrefix(entry.URL, "https://"), "%s: url %q is not an https URL; provenance that cannot be refetched is not provenance",
			entry.Path, entry.URL)
	}
}

// vendoredTreeFiles returns every path under static/vendor, slash-separated
// and relative to that directory.
func vendoredTreeFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(vendorRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(vendorRoot, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	assert.Must(t, err == nil, "walk %s: %v", vendorRoot, err)
	sort.Strings(out)
	return out
}

// TestNoVendoredFileIsUnlisted is the other direction: it walks the tree
// rather than the manifest, so a file dropped into static/vendor that
// nothing lists fails. Without it the manifest is a list of the files
// somebody remembered, and the budget is a sum over that list rather
// than over what actually ships.
func TestNoVendoredFileIsUnlisted(t *testing.T) {
	t.Parallel()
	manifest := readVendorManifest(t)

	listed := map[string]bool{"manifest.json": true}
	for _, entry := range manifest.Files {
		listed[entry.Path] = true
		if entry.LicensePath != "" {
			listed[entry.LicensePath] = true
		}
	}

	files := vendoredTreeFiles(t)
	assert.Must(t, len(files) != 0, "%s holds no files: this walk would pass whatever the manifest said", vendorRoot)
	var unlisted []string
	for _, rel := range files {
		if !listed[rel] {
			unlisted = append(unlisted, rel)
		}
	}
	assert.Must(t, len(unlisted) <= 0, "%d file(s) under %s that %s does not list:\n%s\n"+
		"a vendored file with no provenance is a file this repository cannot answer for; "+
		"add it to the manifest with its package, version, upstream URL, sha256, size and licence",
		len(unlisted), vendorRoot, manifestPath, strings.Join(unlisted, "\n"))
	t.Logf("%s holds %d file(s), all listed", vendorRoot, len(files))
}

// TestTheVendoredPayloadIsUnderBudget sums the code a browser downloads
// before this front end can draw anything, and logs the number whether
// it passes or fails. The log is the point: a budget nobody sees until
// it breaks is a budget that breaks.
func TestTheVendoredPayloadIsUnderBudget(t *testing.T) {
	t.Parallel()
	manifest := readVendorManifest(t)

	totals := map[string]int64{}
	budgets := map[string]int64{"render": renderBlockingBudget, "fonts": fontBudget}
	lines := make([]string, 0, len(manifest.Files))
	for _, entry := range manifest.Files {
		info, err := os.Stat(filepath.Join(vendorRoot, filepath.FromSlash(entry.Path)))
		assert.Must(t, err == nil, "stat %s: %v", entry.Path, err)
		kind := "render"
		if strings.HasPrefix(entry.Path, fontsPrefix) {
			kind = "fonts"
		}
		totals[kind] += info.Size()
		lines = append(lines, fmt.Sprintf("  %-34s %7d B  %-6s %s %s",
			entry.Path, info.Size(), kind, entry.Package, entry.Version))
	}

	sort.Strings(lines)
	for _, kind := range []string{"render", "fonts"} {
		t.Logf("%s: %d B of a %d B budget (%.1f%% used, %d B spare)",
			kind, totals[kind], budgets[kind],
			100*float64(totals[kind])/float64(budgets[kind]),
			budgets[kind]-totals[kind])
	}
	t.Log("\n" + strings.Join(lines, "\n"))

	for _, kind := range []string{"render", "fonts"} {
		if totals[kind] > budgets[kind] {
			t.Fatalf("the vendored %s payload is %d B, over the %d B budget by %d B",
				kind, totals[kind], budgets[kind], totals[kind]-budgets[kind])
		}
	}
	// A budget nothing spends is a budget nobody is keeping: if the
	// fonts ever leave the tree, this test should stop claiming to
	// guard them.
	if totals["fonts"] == 0 {
		t.Fatal("no vendored font is under " + fontsPrefix + ", so the font budget guards nothing")
	}
}

// TestEveryVendoredFontIsSwapped is the condition the split above rests
// on, and without it the second budget is a loophole.
func TestEveryVendoredFontIsSwapped(t *testing.T) {
	t.Parallel()
	sheet, err := os.ReadFile(filepath.Join("static", "styles.css"))
	assert.Must(t, err == nil, "read styles.css: %v", err)

	faces := regexp.MustCompile(`(?s)@font-face\s*\{(.*?)\}`).FindAllStringSubmatch(string(sheet), -1)
	declared := map[string]bool{}
	for _, face := range faces {
		body := face[1]
		src := regexp.MustCompile(`url\("([^"]+)"\)`).FindStringSubmatch(body)
		if src == nil {
			t.Errorf("a @font-face names no url:\n%s", body)
			continue
		}
		if !strings.Contains(body, "font-display: swap") {
			t.Errorf("@font-face for %s does not declare `font-display: swap`, so it blocks the "+
				"first paint and is spending the wrong budget", src[1])
		}
		declared[strings.TrimPrefix(src[1], "/static/vendor/")] = true
	}

	manifest := readVendorManifest(t)
	vendored := map[string]bool{}
	for _, entry := range manifest.Files {
		if !strings.HasPrefix(entry.Path, fontsPrefix) {
			continue
		}
		vendored[entry.Path] = true
		assert.Should(t, declared[entry.Path], "%s is vendored and no @font-face in styles.css declares it: a font nothing "+
			"points at is bytes in a public repository for nobody", entry.Path)
	}
	for path := range declared {
		assert.Should(t, vendored[path], "styles.css declares a face at %s, which the manifest does not vendor: a "+
			"@font-face this repository does not ship is a 404 or a third-party origin", path)
	}
	assert.Must(t, len(vendored) != 0, "no vendored font at all, so this guard holds nothing")
}

// TestEveryVendoredPackageHasItsLicence holds the redistribution half.
// Both licences here (BSD-3-Clause for Lit, MIT for dagre and graphlib)
// permit redistribution in a public repository *provided the notice
// travels with the code*, so the notice travelling with the code is the
// condition, and this is the test that it does.
func TestEveryVendoredPackageHasItsLicence(t *testing.T) {
	t.Parallel()
	manifest := readVendorManifest(t)

	referenced := map[string]bool{}
	for _, entry := range manifest.Files {
		if entry.License == "" || entry.LicensePath == "" {
			t.Errorf("%s: %s names no licence and no licence file", manifestPath, entry.Path)
			continue
		}
		if !strings.HasPrefix(entry.LicensePath, licensesDir+"/") {
			t.Errorf("%s: licence_path %q is not under %s/", entry.Path, entry.LicensePath, licensesDir)
			continue
		}
		referenced[entry.LicensePath] = true

		raw, err := os.ReadFile(filepath.Join(vendorRoot, filepath.FromSlash(entry.LicensePath)))
		if err != nil {
			t.Errorf("%s: licence file %q is missing: %v", entry.Path, entry.LicensePath, err)
			continue
		}
		text := string(raw)
		// A licence file is a text this project is legally obliged to
		// carry, so an empty or truncated placeholder must not satisfy
		// this test. Every permissive licence text says who holds the
		// copyright and grants a permission; both being present is a
		// cheap floor no stub clears by accident.
		assert.Should(t, strings.Contains(text, "Copyright"), "%s: licence file %q carries no copyright line", entry.Path, entry.LicensePath)
		assert.Should(t, strings.Contains(text, "permitted") || strings.Contains(text, "Permission"), "%s: licence file %q grants no permission; it does not read like a licence",
			entry.Path, entry.LicensePath)
	}

	for _, rel := range vendoredTreeFiles(t) {
		if !strings.HasPrefix(rel, licensesDir+"/") {
			continue
		}
		assert.Should(t, referenced[rel], "%s is a licence no manifest entry points at: either the package it covers "+
			"is no longer vendored, or an entry lost its licence_path", rel)
	}
}

// outboundURL matches an absolute http(s) URL. `importScripts(` is
// matched separately: it is not a URL, it is the one classic-worker
// spelling of "fetch and run this", and a worker is exactly where this
// front end's layout engine lives.
var (
	outboundURL      = regexp.MustCompile(`https?://`)
	importScriptsFn  = regexp.MustCompile(`\bimportScripts\s*\(`)
	moduleExtensions = map[string]bool{".js": true, ".mjs": true}
)

// namespaceDeclaration is the one exemption, and it is exact.
var namespaceDeclaration = regexp.MustCompile(`^export const [A-Z_]*NS = "http://www\.w3\.org/2000/svg";$`)

// documentationLink is the second exemption, and it is exact in the same
// shape and for the same kind of reason.
var documentationLink = regexp.MustCompile(
	`^export const SKILL_BUNDLE_HREF = "https://github\.com/neverbot/maestro(#[a-z][a-z0-9-]*)?";$`)

// publishedSite is the third exemption, and it is exact for the third
// time.
var publishedSite = regexp.MustCompile(
	`^export const DOCUMENTATION_HREF = "https://neverbot\.github\.io/maestro/";$`)

// networkReach is one line of one module that reaches outside the
// instance.
type networkReach struct {
	file string
	line int
	text string
}

// scanForNetworkReach reads every module under static — vendored ones
// included, because an upstream file that phones home phones home just
// the same — and returns the files it read alongside every line of code
// naming an absolute URL or importScripts. Comments are stripped by
// codeLines (static_sinks_test.go), which is why a doc comment quoting a
// URL is allowed and a string literal holding one is not.
func scanForNetworkReach(t *testing.T) (read []string, reaches []networkReach) {
	t.Helper()
	err := filepath.WalkDir("static", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !moduleExtensions[filepath.Ext(p)] {
			return nil
		}
		slashed := filepath.ToSlash(p)
		read = append(read, slashed)
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		reaches = append(reaches, networkReachesIn(slashed, string(raw))...)
		return nil
	})
	assert.Must(t, err == nil, "walk static: %v", err)
	sort.Strings(read)
	return read, reaches
}

func networkReachesIn(file, src string) []networkReach {
	var out []networkReach
	for _, line := range codeLines(src) {
		if namespaceDeclaration.MatchString(line.code) || documentationLink.MatchString(line.code) ||
			publishedSite.MatchString(line.code) {
			continue
		}
		if !outboundURL.MatchString(line.code) && !importScriptsFn.MatchString(line.code) {
			continue
		}
		text := line.text
		// A minified module is one enormous line; quoting all 48KB of it
		// in a failure message helps nobody find the URL.
		if len(text) > 200 {
			text = text[:200] + "…"
		}
		out = append(out, networkReach{file: file, line: line.number, text: text})
	}
	return out
}

// TestNoModuleFetchesFromTheNetwork is the invariant a self-hosted
// instance rests on: a Maestro on a private network with no outbound
// route must work completely. A CDN import, a font fetch or a telemetry
// beacon would fail there and *only* there — every developer machine
// would keep working, which is what makes this a source guard rather
// than something a runtime test would ever catch.
func TestNoModuleFetchesFromTheNetwork(t *testing.T) {
	t.Parallel()
	read, reaches := scanForNetworkReach(t)
	assert.Must(t, len(read) != 0, "scanned no modules under internal/web/static: this test would pass on an empty tree")
	if len(reaches) > 0 {
		lines := make([]string, 0, len(reaches))
		for _, r := range reaches {
			lines = append(lines, fmt.Sprintf("%s:%d: %s", r.file, r.line, r.text))
		}
		sort.Strings(lines)
		t.Fatalf("%d module line(s) reaching outside this instance:\n%s\n"+
			"every asset this front end needs ships in the binary; an instance with no outbound "+
			"route must render completely",
			len(reaches), strings.Join(lines, "\n"))
	}
	t.Logf("read %d module(s), none reaching the network", len(read))
}

// TestTheNetworkScanFlagsAFabricatedFetch is the other half of the same
// worry: the scan may be reading every file and still be blind, because
// its pattern is wrong or because the comment strip it depends on eats
// the code as well. So it is run over sources written to be caught, and
// over one written not to be.
func TestTheNetworkScanFlagsAFabricatedFetch(t *testing.T) {
	t.Parallel()
	caught := []struct {
		name string
		src  string
	}{
		{"a bare import from a CDN", `import { html } from "https://cdn.example/lit.js";`},
		{"a dynamic import", "const m = await import('http://cdn.example/x.js');"},
		{"a fetch", `fetch("https://telemetry.example/beacon", {method: "POST"});`},
		{"importScripts in a worker", `importScripts("/static/vendor/dagre.mjs");`},
		{"a URL after code on a line that also carries a comment", `const u = "https://cdn.example/x.js"; // vendored`},
	}
	for _, c := range caught {
		if got := networkReachesIn("fabricated.js", c.src); len(got) == 0 {
			t.Errorf("the network scan did not flag %s: %s", c.name, c.src)
		}
	}

	ignored := []struct {
		name string
		src  string
	}{
		{"a line comment", `// fetched from https://unpkg.com/@dagrejs/dagre@3.1.1/dist/dagre.esm.js`},
		{"a block comment", "/*\n * see https://lit.dev for the runtime this vendors\n */"},
		{"a relative source map", "//# sourceMappingURL=dagre.esm.js.map"},
	}
	for _, c := range ignored {
		if got := networkReachesIn("fabricated.js", c.src); len(got) > 0 {
			t.Errorf("the network scan flagged %s, which is prose, not a fetch: %+v", c.name, got)
		}
	}
}

// importMapRE lifts the JSON out of a shell's one import map.
var importMapRE = regexp.MustCompile(`(?s)<script\s+type="importmap"\s*>(.*?)</script>`)

// moduleScriptRE finds a module script tag, whose position relative to
// the map matters: a browser that has already started fetching a module
// graph ignores an import map that arrives afterwards, and errors.
var moduleScriptRE = regexp.MustCompile(`<script[^>]*\btype="module"`)

type shellMap struct {
	shell   string
	imports map[string]string
	at      int // byte offset of the map in the shell
}

// shellImportMaps parses the import map out of every HTML shell.
func shellImportMaps(t *testing.T) []shellMap {
	t.Helper()
	shells, err := filepath.Glob(filepath.Join("static", "*.html"))
	assert.Must(t, err == nil, "glob shells: %v", err)
	assert.Must(t, len(shells) != 0, "found no HTML shell under internal/web/static")
	sort.Strings(shells)

	out := make([]shellMap, 0, len(shells))
	for _, shell := range shells {
		raw, err := os.ReadFile(shell)
		assert.Must(t, err == nil, "read %s: %v", shell, err)
		src := string(raw)
		found := importMapRE.FindAllStringSubmatchIndex(src, -1)
		assert.Must(t, len(found) != 0, "%s carries no import map: every shell needs one, or the first bare "+
			"specifier that page imports fails to resolve", shell)
		assert.Must(t, len(found) <= 1, "%s carries %d import maps: a browser honours the first and errors on the rest",
			shell, len(found))
		var parsed struct {
			Imports map[string]string `json:"imports"`
		}
		if err := json.Unmarshal([]byte(src[found[0][2]:found[0][3]]), &parsed); err != nil {
			t.Fatalf("%s: the import map is not valid JSON, so a browser ignores it entirely: %v", shell, err)
		}
		assert.Must(t, len(parsed.Imports) != 0, "%s: the import map declares no imports", shell)
		out = append(out, shellMap{shell: filepath.ToSlash(shell), imports: parsed.Imports, at: found[0][0]})
	}
	return out
}

// TestTheImportMapIsIdenticalInEveryShell holds the rule the standing
// failure pattern of this project keeps breaking: a rule established in
// one file and not carried one step along. A shell with a stale map is a
// page that 404s one module and renders three quarters of itself, which
// looks like a rendering bug rather than a missing line of HTML.
func TestTheImportMapIsIdenticalInEveryShell(t *testing.T) {
	t.Parallel()
	maps := shellImportMaps(t)
	assert.Must(t, len(maps) >= 2, "found %d shell(s): this comparison needs at least two to mean anything", len(maps))

	reference := maps[0]
	for _, m := range maps[1:] {
		assert.Should(t, len(m.imports) == len(reference.imports), "%s maps %d specifier(s), %s maps %d",
			m.shell, len(m.imports), reference.shell, len(reference.imports))
		for specifier, target := range reference.imports {
			got, ok := m.imports[specifier]
			switch {
			case !ok:
				t.Errorf("%s does not map %q, which %s maps to %q", m.shell, specifier, reference.shell, target)
			case got != target:
				t.Errorf("%s maps %q to %q; %s maps it to %q", m.shell, specifier, got, reference.shell, target)
			}
		}
		for specifier := range m.imports {
			if _, ok := reference.imports[specifier]; !ok {
				t.Errorf("%s maps %q, which %s does not", m.shell, specifier, reference.shell)
			}
		}
	}
	t.Logf("%d shell(s) agree on %d specifier(s)", len(maps), len(reference.imports))
}

// TestTheImportMapPrecedesEveryModuleScript pins the ordering rule the
// HTML spec imposes and nothing else in this repository states: an
// import map must be parsed before the first module script starts
// resolving, or the browser reports "An import map is added after module
// script load was triggered" and every bare specifier on the page fails.
// The shells put their scripts at the bottom today, so this passes by
// habit; a shell that grows a module script in its head tomorrow is
// exactly the case habit does not cover.
func TestTheImportMapPrecedesEveryModuleScript(t *testing.T) {
	t.Parallel()
	for _, m := range shellImportMaps(t) {
		raw, err := os.ReadFile(m.shell)
		assert.Must(t, err == nil, "read %s: %v", m.shell, err)
		for _, loc := range moduleScriptRE.FindAllStringIndex(string(raw), -1) {
			if loc[0] < m.at {
				t.Errorf("%s: a module script at byte %d precedes the import map at byte %d; "+
					"the map arrives too late and every bare specifier fails", m.shell, loc[0], m.at)
			}
		}
	}
}

// TestEveryImportMapTargetIsAVendoredFile closes the loop between the
// two halves of this task. The map is a promise about paths; the
// manifest is the record of what those paths are. Nothing else compares
// them, so a map entry pointing at a file that was never vendored — or
// at one vendored and later removed — would be a 404 discovered by a
// designer rather than by a test.
func TestEveryImportMapTargetIsAVendoredFile(t *testing.T) {
	t.Parallel()
	manifest := readVendorManifest(t)
	vendored := map[string]bool{}
	for _, entry := range manifest.Files {
		vendored["/static/vendor/"+entry.Path] = true
	}

	// Every shell's map, not just the first: leaning on
	// TestTheImportMapIsIdenticalInEveryShell to make one shell stand
	// for four would make this test's reach a consequence of that test
	// passing, and a mutation in a shell this one never opened stays
	// green here — which is exactly what happened the first time it was
	// written against maps[0] alone.
	mapped := map[string]bool{}
	for _, m := range shellImportMaps(t) {
		for specifier, target := range m.imports {
			mapped[target] = true
			assert.Should(t, vendored[target], "%s sends %q to %q, which %s does not list",
				m.shell, specifier, target, manifestPath)
			if _, err := os.Stat(filepath.Join("static", strings.TrimPrefix(target, "/static/"))); err != nil {
				t.Errorf("%s sends %q to %q, which is not in the tree: %v", m.shell, specifier, target, err)
			}
		}
	}

	// The other direction is a warning rather than a failure only if it
	// is stated as one, so it is stated as a failure: a module vendored
	// and reachable by no specifier is either dead weight in the budget
	// or a missing map entry, and both want a human.
	for _, entry := range manifest.Files {
		// Spelled out rather than read from moduleExtensions: a shared
		// list lets one narrowing silence two checks.
		if !strings.HasSuffix(entry.Path, ".js") && !strings.HasSuffix(entry.Path, ".mjs") {
			continue
		}
		assert.Should(t, mapped["/static/vendor/"+entry.Path], "%s is vendored and costs its bytes, but no import map specifier reaches it", entry.Path)
	}
}

// TestEveryImportMapTargetIsServedAsJavaScript is the one test here that
// starts a server, and it is worth the cost: a browser refuses a module
// whose response carries anything but a JavaScript MIME type, and the
// vendored files are the first assets this repository ships with an
// extension (`.mjs`) that no earlier test ever asked the file server to
// name. A host whose MIME database disagrees would serve dagre as
// `application/octet-stream` and the page would fail with a console
// error naming neither the map nor the file.
func TestEveryImportMapTargetIsServedAsJavaScript(t *testing.T) {
	t.Parallel()
	srv, _, _ := newTestServer(t)

	// Every target of every shell, deduplicated: same reason as above.
	targets := map[string]string{}
	for _, m := range shellImportMaps(t) {
		for specifier, target := range m.imports {
			targets[target] = specifier
		}
	}

	for target, specifier := range targets {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("GET %s (mapped from %q) = %d, want 200", target, specifier, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") &&
			!strings.HasPrefix(ct, "application/javascript") {
			t.Errorf("GET %s served as %q; a browser refuses a module that is not a JavaScript MIME type",
				target, ct)
		}
		assert.Should(t, rec.Body.Len() != 0, "GET %s served an empty body", target)
	}
}

// TestTheNamespaceExemptionIsExactlyOneDeclaration is the guard on the
// one hole deliberately left in the scan above. An exemption nobody
// bounds is where every future outbound URL would hide, so this pins
// what it admits and — the half that matters — what it still refuses.
func TestTheNamespaceExemptionIsExactlyOneDeclaration(t *testing.T) {
	t.Parallel()
	admitted := `export const SVG_NS = "http://www.w3.org/2000/svg";`
	assert.Should(t, namespaceDeclaration.MatchString(admitted), "the exemption does not admit the declaration it exists for: %q", admitted)
	for name, refused := range map[string]string{
		"a fetch of the same string": `const r = await fetch("http://www.w3.org/2000/svg");`,
		"an https spelling":          `export const SVG_NS = "https://www.w3.org/2000/svg";`,
		"the xlink namespace":        `export const XLINK_NS = "http://www.w3.org/1999/xlink";`,
		"a declaration with a tail":  `export const SVG_NS = "http://www.w3.org/2000/svg"; fetch(SVG_NS);`,
		"any other host":             `export const CDN_NS = "http://cdn.example.com/2000/svg";`,
	} {
		assert.Should(t, !namespaceDeclaration.MatchString(refused), "the exemption admits %s: %q", name, refused)
		assert.Should(t, len(networkReachesIn("static/x.js", refused+"\n")) != 0, "the scan does not report %s: %q", name, refused)
	}

	// And the real module really is the one line that uses it, so this
	// exemption cannot quietly acquire a second beneficiary.
	raw, err := os.ReadFile(filepath.Join("static", "render", "scene.js"))
	assert.Must(t, err == nil, "read scene.js: %v", err)
	found := 0
	for _, line := range codeLines(string(raw)) {
		if namespaceDeclaration.MatchString(line.code) {
			found++
		}
	}
	assert.Should(t, found == 1, "the namespace declaration appears %d time(s) in render/scene.js, want exactly 1", found)
}

// TestTheDocumentationExemptionIsExactlyOneDeclaration is the guard on
// the second hole, written to the same standard as the first: what it
// admits, what it still refuses, and that the front end contains exactly
// one line it applies to.
func TestTheDocumentationExemptionIsExactlyOneDeclaration(t *testing.T) {
	t.Parallel()
	admitted := `export const SKILL_BUNDLE_HREF = "https://github.com/neverbot/maestro#the-skill-bundle";`
	assert.Should(t, documentationLink.MatchString(admitted), "the exemption does not admit the declaration it exists for: %q", admitted)
	for name, refused := range map[string]string{
		"a fetch of the same URL":   `const r = await fetch("https://github.com/neverbot/maestro#the-skill-bundle");`,
		"an http spelling":          `export const SKILL_BUNDLE_HREF = "http://github.com/neverbot/maestro#the-skill-bundle";`,
		"another host":              `export const SKILL_BUNDLE_HREF = "https://cdn.example.com/neverbot/maestro";`,
		"another constant":          `export const ANALYTICS_HREF = "https://github.com/neverbot/maestro#the-skill-bundle";`,
		"a declaration with a tail": `export const SKILL_BUNDLE_HREF = "https://github.com/neverbot/maestro"; fetch(SKILL_BUNDLE_HREF);`,
	} {
		assert.Should(t, !documentationLink.MatchString(refused), "the exemption admits %s: %q", name, refused)
		assert.Should(t, len(networkReachesIn("static/x.js", refused+"\n")) != 0, "the scan does not report %s: %q", name, refused)
	}

	// And exactly one line in the whole front end benefits from it, so a
	// second external link cannot arrive under this argument without
	// somebody writing a new one.
	found := 0
	err := filepath.WalkDir("static", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !moduleExtensions[filepath.Ext(path)] {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, line := range codeLines(string(raw)) {
			if documentationLink.MatchString(line.code) {
				found++
			}
		}
		return nil
	})
	assert.Must(t, err == nil, "walk static: %v", err)
	assert.Should(t, found == 1, "the documentation link appears %d time(s) under internal/web/static, want exactly 1", found)
}

// TestThePublishedSiteExemptionIsExactlyOneDeclaration is the guard on
// the third hole, written to the standard the first two set: what it
// admits, what it still refuses, and that exactly one line in the front
// end benefits from it.
func TestThePublishedSiteExemptionIsExactlyOneDeclaration(t *testing.T) {
	t.Parallel()
	admitted := `export const DOCUMENTATION_HREF = "https://neverbot.github.io/maestro/";`
	assert.Should(t, publishedSite.MatchString(admitted), "the exemption does not admit the declaration it exists for: %q", admitted)
	for name, refused := range map[string]string{
		"a fetch of the same URL":   `const r = await fetch("https://neverbot.github.io/maestro/");`,
		"an http spelling":          `export const DOCUMENTATION_HREF = "http://neverbot.github.io/maestro/";`,
		"another host":              `export const DOCUMENTATION_HREF = "https://docs.example.com/maestro/";`,
		"another constant":          `export const ANALYTICS_HREF = "https://neverbot.github.io/maestro/";`,
		"a declaration with a tail": `export const DOCUMENTATION_HREF = "https://neverbot.github.io/maestro/"; fetch(DOCUMENTATION_HREF);`,
	} {
		assert.Should(t, !publishedSite.MatchString(refused), "the exemption admits %s: %q", name, refused)
		assert.Should(t, len(networkReachesIn("static/x.js", refused+"\n")) != 0, "the scan does not report %s: %q", name, refused)
	}

	found := 0
	err := filepath.WalkDir("static", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !moduleExtensions[filepath.Ext(path)] {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, line := range codeLines(string(raw)) {
			if publishedSite.MatchString(line.code) {
				found++
			}
		}
		return nil
	})
	assert.Must(t, err == nil, "walk static: %v", err)
	assert.Should(t, found == 1, "the published site's address appears %d time(s) under internal/web/static, want exactly 1", found)
}
