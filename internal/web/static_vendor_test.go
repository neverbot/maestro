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
)

// The provenance, payload and resolution guards for the vendored half of
// internal/web/static.
//
// Three files this project did not write are shipped inside its binary:
// Lit, dagre and graphlib. A vendored file in a public repository is a
// file this project answers for, and the four questions that answer
// consists of are all mechanical:
//
//   - **Where did it come from?** manifest.json records the package, the
//     exact version, the URL the bytes were fetched from and their
//     SHA-256, and this file walks the manifest and the tree in both
//     directions so neither can drift from the other in silence. A
//     re-vendored file that skipped the manifest fails here, which is
//     the only way anyone notices.
//   - **What does it cost?** The sizes are summed against a stated
//     budget and the number is logged on every run, so the margin is
//     visible long before it is spent rather than only at the failure.
//   - **May we redistribute it?** Every package's licence text is
//     committed next to the code it covers, and every entry names one.
//   - **Will it load?** The import map is what makes `import … from
//     "lit"` resolve with no build step. It has to be the same map in
//     every shell, it has to point at files that exist and that this
//     server actually serves as JavaScript, and no module — ours or
//     theirs — may reach for the network, because a self-hosted instance
//     on a private network has no outbound route and a CDN import would
//     fail there and nowhere else.
//
// **What this file cannot answer.** Whether the upstream bytes are
// themselves trustworthy: a SHA-256 pins *which* file was vendored, not
// that the file is good. Nor does a `https?://` scan see a URL a module
// assembles at runtime from fragments — the same blind spot
// static_sinks_test.go names for markup sinks, and the same answer:
// catching that needs a parser and a dataflow.

const (
	vendorRoot   = "static/vendor"
	manifestPath = vendorRoot + "/manifest.json"
	licensesDir  = "licenses"

	// vendoredPayloadBudget is the ceiling on everything this front end
	// makes a browser download before it can draw anything: 150KB, the
	// figure the interface plan commits to.
	//
	// It lives here rather than in manifest.json on purpose. A budget a
	// contributor can raise by editing the same file it is checked
	// against is not a budget; raising this one is a diff to a test,
	// which is a conversation.
	vendoredPayloadBudget = 150 * 1024
)

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
	if err != nil {
		t.Fatalf("read %s: %v", manifestPath, err)
	}
	// The manifest carries prose fields this struct does not model, so
	// unknown fields are tolerated; what is not tolerated is malformed
	// JSON, which would otherwise leave every test below reading an
	// empty manifest and passing.
	var manifest vendorManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse %s: %v", manifestPath, err)
	}
	if len(manifest.Files) == 0 {
		t.Fatalf("%s lists no files: every test in this file would pass vacuously", manifestPath)
	}
	return manifest
}

// TestVendoredFilesMatchTheirManifest walks the manifest and checks each
// entry against the bytes on disk. A file replaced by a newer upstream
// release without its hash and size being updated in the same commit is
// caught here, and only here.
func TestVendoredFilesMatchTheirManifest(t *testing.T) {
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
		if !strings.HasPrefix(entry.URL, "https://") {
			t.Errorf("%s: url %q is not an https URL; provenance that cannot be refetched is not provenance",
				entry.Path, entry.URL)
		}
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
	if err != nil {
		t.Fatalf("walk %s: %v", vendorRoot, err)
	}
	sort.Strings(out)
	return out
}

// TestNoVendoredFileIsUnlisted is the other direction: it walks the tree
// rather than the manifest, so a file dropped into static/vendor that
// nothing lists fails. Without it the manifest is a list of the files
// somebody remembered, and the budget is a sum over that list rather
// than over what actually ships.
func TestNoVendoredFileIsUnlisted(t *testing.T) {
	manifest := readVendorManifest(t)

	listed := map[string]bool{"manifest.json": true}
	for _, entry := range manifest.Files {
		listed[entry.Path] = true
		if entry.LicensePath != "" {
			listed[entry.LicensePath] = true
		}
	}

	files := vendoredTreeFiles(t)
	if len(files) == 0 {
		t.Fatalf("%s holds no files: this walk would pass whatever the manifest said", vendorRoot)
	}
	var unlisted []string
	for _, rel := range files {
		if !listed[rel] {
			unlisted = append(unlisted, rel)
		}
	}
	if len(unlisted) > 0 {
		t.Fatalf("%d file(s) under %s that %s does not list:\n%s\n"+
			"a vendored file with no provenance is a file this repository cannot answer for; "+
			"add it to the manifest with its package, version, upstream URL, sha256, size and licence",
			len(unlisted), vendorRoot, manifestPath, strings.Join(unlisted, "\n"))
	}
	t.Logf("%s holds %d file(s), all listed", vendorRoot, len(files))
}

// TestTheVendoredPayloadIsUnderBudget sums the code a browser downloads
// before this front end can draw anything, and logs the number whether
// it passes or fails. The log is the point: a budget nobody sees until
// it breaks is a budget that breaks.
//
// It sums the bytes on disk rather than the manifest's numbers, so a
// manifest understating a file's size cannot buy headroom — that
// disagreement is TestVendoredFilesMatchTheirManifest's to report, and
// this test stays true regardless of it.
func TestTheVendoredPayloadIsUnderBudget(t *testing.T) {
	manifest := readVendorManifest(t)

	var total int64
	lines := make([]string, 0, len(manifest.Files))
	for _, entry := range manifest.Files {
		info, err := os.Stat(filepath.Join(vendorRoot, filepath.FromSlash(entry.Path)))
		if err != nil {
			t.Fatalf("stat %s: %v", entry.Path, err)
		}
		total += info.Size()
		lines = append(lines, fmt.Sprintf("  %-20s %7d B  %s %s", entry.Path, info.Size(), entry.Package, entry.Version))
	}

	sort.Strings(lines)
	t.Logf("vendored payload: %d B of a %d B budget (%.1f%% used, %d B spare)\n%s",
		total, vendoredPayloadBudget,
		100*float64(total)/float64(vendoredPayloadBudget),
		vendoredPayloadBudget-total,
		strings.Join(lines, "\n"))

	if total > vendoredPayloadBudget {
		t.Fatalf("the vendored payload is %d B, over the %d B budget by %d B",
			total, vendoredPayloadBudget, total-vendoredPayloadBudget)
	}
}

// TestEveryVendoredPackageHasItsLicence holds the redistribution half.
// Both licences here (BSD-3-Clause for Lit, MIT for dagre and graphlib)
// permit redistribution in a public repository *provided the notice
// travels with the code*, so the notice travelling with the code is the
// condition, and this is the test that it does.
//
// It checks in both directions, like the manifest walk: an entry with no
// licence file fails, and a licence file no entry points at fails too —
// the second because a stale licence for a package that is no longer
// vendored is a claim about code that is not here.
func TestEveryVendoredPackageHasItsLicence(t *testing.T) {
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
		if !strings.Contains(text, "Copyright") {
			t.Errorf("%s: licence file %q carries no copyright line", entry.Path, entry.LicensePath)
		}
		if !strings.Contains(text, "permitted") && !strings.Contains(text, "Permission") {
			t.Errorf("%s: licence file %q grants no permission; it does not read like a licence",
				entry.Path, entry.LicensePath)
		}
	}

	for _, rel := range vendoredTreeFiles(t) {
		if !strings.HasPrefix(rel, licensesDir+"/") {
			continue
		}
		if !referenced[rel] {
			t.Errorf("%s is a licence no manifest entry points at: either the package it covers "+
				"is no longer vendored, or an entry lost its licence_path", rel)
		}
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
//
// `http://www.w3.org/2000/svg` is an **XML namespace name**, not a
// resource: `createElementNS` compares it as a string and no browser has
// ever fetched it. Refusing it would mean an SVG element built with
// `createElement` instead, which is an unknown HTML element that lays
// out as nothing — a bug with no error message, bought to satisfy a
// guard about outbound traffic that this line does not cause.
//
// The exemption is deliberately not "any w3.org URL" and not "any line
// containing the namespace". It is a whole line declaring a constant
// whose name ends in `NS`, so `fetch("http://www.w3.org/2000/svg")` is
// still reported, an https spelling is still reported, and the xlink
// namespace — which this front end has no business naming at all, see
// internal/web/static_canvas_test.go — is still reported.
// TestTheNamespaceExemptionIsExactlyOneDeclaration holds all four.
var namespaceDeclaration = regexp.MustCompile(`^export const [A-Z_]*NS = "http://www\.w3\.org/2000/svg";$`)

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
	if err != nil {
		t.Fatalf("walk static: %v", err)
	}
	sort.Strings(read)
	return read, reaches
}

func networkReachesIn(file, src string) []networkReach {
	var out []networkReach
	for _, line := range codeLines(src) {
		if namespaceDeclaration.MatchString(line.code) {
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
	read, reaches := scanForNetworkReach(t)
	if len(read) == 0 {
		t.Fatal("scanned no modules under internal/web/static: this test would pass on an empty tree")
	}
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

// TestTheNetworkScanReadsEveryModuleIncludingTheVendoredOnes is the
// guard on the guard, and it exists because that scan reports nothing in
// two indistinguishable cases: the modules are clean, or the walk never
// opened them. The specific mistake it pins is an extension filter that
// says `.js` — which is what the interface plan's own sentence says, and
// which would silently exempt dagre.mjs and graphlib.mjs, i.e. two
// thirds of the code this project did not write.
//
// **It spells the module extensions out itself rather than reusing
// moduleExtensions.** Sharing that map is what made the first version of
// this test a tautology: narrowing the map to `.js` narrowed the scan
// and this test's own expectation in the same stroke, and the mutation
// that was supposed to turn it red left it green. Two spellings of the
// same list is the price of one of them being able to judge the other.
func TestTheNetworkScanReadsEveryModuleIncludingTheVendoredOnes(t *testing.T) {
	isModule := func(p string) bool {
		return strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".mjs")
	}

	read, _ := scanForNetworkReach(t)
	seen := map[string]bool{}
	for _, p := range read {
		seen[p] = true
	}

	manifest := readVendorManifest(t)
	vendoredModules := 0
	for _, entry := range manifest.Files {
		if !isModule(entry.Path) {
			continue
		}
		vendoredModules++
		want := vendorRoot + "/" + entry.Path
		if !seen[want] {
			t.Errorf("the network scan never read %s; it is a module this instance ships", want)
		}
	}
	if vendoredModules == 0 {
		t.Error("the manifest lists no module at all, so the loop above asserted nothing")
	}

	// And the same for our own modules, by an independent walk.
	own := 0
	err := filepath.WalkDir("static", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		slashed := filepath.ToSlash(p)
		if d.IsDir() || !isModule(slashed) {
			return nil
		}
		own++
		if !seen[slashed] {
			t.Errorf("the network scan never read %s", slashed)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk static: %v", err)
	}
	if own == 0 {
		t.Error("the walk found no module under static: this test would pass on an empty tree")
	}
	t.Logf("network scan read %d module(s) of %d found by an independent walk: %s",
		len(read), own, strings.Join(read, ", "))
}

// TestTheNetworkScanFlagsAFabricatedFetch is the other half of the same
// worry: the scan may be reading every file and still be blind, because
// its pattern is wrong or because the comment strip it depends on eats
// the code as well. So it is run over sources written to be caught, and
// over one written not to be.
func TestTheNetworkScanFlagsAFabricatedFetch(t *testing.T) {
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
	if err != nil {
		t.Fatalf("glob shells: %v", err)
	}
	if len(shells) == 0 {
		t.Fatal("found no HTML shell under internal/web/static")
	}
	sort.Strings(shells)

	out := make([]shellMap, 0, len(shells))
	for _, shell := range shells {
		raw, err := os.ReadFile(shell)
		if err != nil {
			t.Fatalf("read %s: %v", shell, err)
		}
		src := string(raw)
		found := importMapRE.FindAllStringSubmatchIndex(src, -1)
		if len(found) == 0 {
			t.Fatalf("%s carries no import map: every shell needs one, or the first bare "+
				"specifier that page imports fails to resolve", shell)
		}
		if len(found) > 1 {
			t.Fatalf("%s carries %d import maps: a browser honours the first and errors on the rest",
				shell, len(found))
		}
		var parsed struct {
			Imports map[string]string `json:"imports"`
		}
		if err := json.Unmarshal([]byte(src[found[0][2]:found[0][3]]), &parsed); err != nil {
			t.Fatalf("%s: the import map is not valid JSON, so a browser ignores it entirely: %v", shell, err)
		}
		if len(parsed.Imports) == 0 {
			t.Fatalf("%s: the import map declares no imports", shell)
		}
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
	maps := shellImportMaps(t)
	if len(maps) < 2 {
		t.Fatalf("found %d shell(s): this comparison needs at least two to mean anything", len(maps))
	}

	reference := maps[0]
	for _, m := range maps[1:] {
		if len(m.imports) != len(reference.imports) {
			t.Errorf("%s maps %d specifier(s), %s maps %d",
				m.shell, len(m.imports), reference.shell, len(reference.imports))
		}
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
	for _, m := range shellImportMaps(t) {
		raw, err := os.ReadFile(m.shell)
		if err != nil {
			t.Fatalf("read %s: %v", m.shell, err)
		}
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
			if !vendored[target] {
				t.Errorf("%s sends %q to %q, which %s does not list",
					m.shell, specifier, target, manifestPath)
			}
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
		// Spelled out rather than read from moduleExtensions for the
		// reason TestTheNetworkScanReadsEveryModuleIncludingTheVendoredOnes
		// gives: a shared list lets one narrowing silence two checks.
		if !strings.HasSuffix(entry.Path, ".js") && !strings.HasSuffix(entry.Path, ".mjs") {
			continue
		}
		if !mapped["/static/vendor/"+entry.Path] {
			t.Errorf("%s is vendored and costs its bytes, but no import map specifier reaches it", entry.Path)
		}
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
		if rec.Body.Len() == 0 {
			t.Errorf("GET %s served an empty body", target)
		}
	}
}

// TestTheNamespaceExemptionIsExactlyOneDeclaration is the guard on the
// one hole deliberately left in the scan above. An exemption nobody
// bounds is where every future outbound URL would hide, so this pins
// what it admits and — the half that matters — what it still refuses.
func TestTheNamespaceExemptionIsExactlyOneDeclaration(t *testing.T) {
	admitted := `export const SVG_NS = "http://www.w3.org/2000/svg";`
	if !namespaceDeclaration.MatchString(admitted) {
		t.Errorf("the exemption does not admit the declaration it exists for: %q", admitted)
	}
	for name, refused := range map[string]string{
		"a fetch of the same string": `const r = await fetch("http://www.w3.org/2000/svg");`,
		"an https spelling":          `export const SVG_NS = "https://www.w3.org/2000/svg";`,
		"the xlink namespace":        `export const XLINK_NS = "http://www.w3.org/1999/xlink";`,
		"a declaration with a tail":  `export const SVG_NS = "http://www.w3.org/2000/svg"; fetch(SVG_NS);`,
		"any other host":             `export const CDN_NS = "http://cdn.example.com/2000/svg";`,
	} {
		if namespaceDeclaration.MatchString(refused) {
			t.Errorf("the exemption admits %s: %q", name, refused)
		}
		if len(networkReachesIn("static/x.js", refused+"\n")) == 0 {
			t.Errorf("the scan does not report %s: %q", name, refused)
		}
	}

	// And the real module really is the one line that uses it, so this
	// exemption cannot quietly acquire a second beneficiary.
	raw, err := os.ReadFile(filepath.Join("static", "render", "scene.js"))
	if err != nil {
		t.Fatalf("read scene.js: %v", err)
	}
	found := 0
	for _, line := range codeLines(string(raw)) {
		if namespaceDeclaration.MatchString(line.code) {
			found++
		}
	}
	if found != 1 {
		t.Errorf("the namespace declaration appears %d time(s) in render/scene.js, want exactly 1", found)
	}
}
