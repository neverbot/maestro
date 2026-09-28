package web_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
