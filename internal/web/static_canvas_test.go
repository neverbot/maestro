package web_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

// The escaping perimeter, for the context the canvas draws in.
var scriptableSVG = map[string]bool{
	"script":           true,
	"foreignObject":    true,
	"use":              true,
	"a":                true,
	"iframe":           true,
	"object":           true,
	"embed":            true,
	"animate":          true,
	"animateTransform": true,
	"animateMotion":    true,
	"set":              true,
	"handler":          true,
	"style":            true,
}

// createElementNSRE finds a namespaced element creation and captures the
// namespace expression and the tag literal.
var createElementNSRE = regexp.MustCompile(`createElementNS\(\s*([A-Za-z_$][\w$.]*|"[^"]*")\s*,\s*("[^"]*"|[A-Za-z_$][\w$.]*)\s*\)`)

// xlinkRE finds the legacy href spelling in any form. There is no
// legitimate use of it in this front end: SVG 2 serves `href`, and a
// filter written against `href` alone would not see this one.
var xlinkRE = regexp.MustCompile(`\bxlink\b|\bxlinkHref\b`)

// foreignObjectRE finds the one element that re-enters the HTML parser.
// Unlike the set above it is unambiguous as a bare word, so it is caught
// wherever it is spelled in code and not only in a creation call.
var foreignObjectRE = regexp.MustCompile(`\bforeignObject\b`)

// scanForSVGHazards walks internal/web/static's own modules — the
// vendored subtree is upstream code pinned by hash and is not ours to
// edit — and returns what it read alongside every hit.
func scanForSVGHazards(t *testing.T) (scanned []string, offences []string) {
	t.Helper()
	err := filepath.WalkDir("static", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if filepath.ToSlash(path) == vendorDir {
				return filepath.SkipDir
			}
			return nil
		}
		if !ownModuleExtensions[filepath.Ext(path)] {
			return nil
		}
		scanned = append(scanned, filepath.ToSlash(path))
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range codeLines(string(raw)) {
			where := path + ":" + strconv.Itoa(line.number) + ": "
			if xlinkRE.MatchString(line.code) {
				offences = append(offences, where+"xlink: "+line.text)
			}
			if foreignObjectRE.MatchString(line.code) {
				offences = append(offences, where+"foreignObject: "+line.text)
			}
			for _, hit := range createElementNSRE.FindAllStringSubmatch(line.code, -1) {
				if tag, ok := literalTag(hit[2]); ok && scriptableSVG[tag] {
					offences = append(offences, where+"creates <"+tag+">: "+line.text)
				}
				// The namespace is the other half: an SVG element made
				// with the HTML namespace is an unknown element that lays
				// out as nothing, which is a bug with no error message.
				if hit[1] != "SVG_NS" {
					offences = append(offences, where+"creates an element in "+hit[1]+": "+line.text)
				}
			}
		}
		return nil
	})
	assert.Must(t, err == nil, "walk static: %v", err)
	return scanned, offences
}

func TestNoOwnModuleNamesAScriptableSVGElement(t *testing.T) {
	t.Parallel()
	scanned, offences := scanForSVGHazards(t)
	assert.Must(t, len(scanned) != 0, "scanned no module under internal/web/static: a walk that reads nothing guards nothing")
	if len(offences) > 0 {
		sort.Strings(offences)
		t.Fatalf("%d SVG hazard(s) in this front end's own modules:\n%s\n"+
			"the canvas is built with createElementNS, setAttribute and textContent, none of which parses "+
			"markup; an element that runs code, navigates or re-enters the HTML parser is how a game's own "+
			"words stop being characters",
			len(offences), strings.Join(offences, "\n"))
	}
	t.Logf("SVG hazard scan read %d own module(s)", len(scanned))
}

// TestTheEmitterContractNamesNoScriptableElement reads the map itself.
// The scan above holds what the *source* spells; this holds what the
// emitter is contractually allowed to build, which is the list a future
// renderer would grow rather than a call it would write.
func TestTheEmitterContractNamesNoScriptableElement(t *testing.T) {
	t.Parallel()
	elements := markElements(t)
	assert.Must(t, len(elements) != 0, "read no element out of render/scene.js's MARK_ELEMENTS: a contract nobody could read guards nothing")
	for _, name := range elements {
		assert.Should(t, !(scriptableSVG[name]), "MARK_ELEMENTS lets a mark become <%s>, which is not a drawing", name)
	}
	t.Logf("the emitter contract names %v", elements)
}

// markElements pulls the values out of scene.js's MARK_ELEMENTS block.
func markElements(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("static", "render", "scene.js"))
	assert.Must(t, err == nil, "read scene.js: %v", err)
	const marker = "export const MARK_ELEMENTS = {"
	at := strings.Index(string(raw), marker)
	assert.Must(t, at >= 0, "render/scene.js declares no MARK_ELEMENTS: the emitter contract moved and this guard did not")
	rest := string(raw)[at+len(marker):]
	end := strings.Index(rest, "}")
	assert.Must(t, end >= 0, "render/scene.js's MARK_ELEMENTS block does not close")
	var found []string
	for _, hit := range regexp.MustCompile(`:\s*"([^"]*)"`).FindAllStringSubmatch(rest[:end], -1) {
		found = append(found, hit[1])
	}
	sort.Strings(found)
	return found
}

// TestTheSVGHazardScanReadsWhatItClaimsTo is the guard on the guard, in
// both directions. A scan that matched nothing would pass the test above
// on any file at all, which is the failure this repository has hit
// twice: a guard that audits itself.
func TestTheSVGHazardScanReadsWhatItClaimsTo(t *testing.T) {
	t.Parallel()
	for name, planted := range map[string]string{
		"a foreign object":    `const box = doc.createElementNS(SVG_NS, "foreignObject");`,
		"a script element":    `const s = doc.createElementNS(SVG_NS, "script");`,
		"a link":              `const a = doc.createElementNS(SVG_NS, "a");`,
		"the legacy href":     `element.setAttributeNS(XLINK, "xlink:href", mark.href);`,
		"the wrong namespace": `const rect = doc.createElementNS(HTML_NS, "rect");`,
	} {
		assert.Should(t, hazardous(planted), "the scan missed %s: %q", name, planted)
	}

	for name, benign := range map[string]string{
		"an ordinary shape": `const rect = doc.createElementNS(SVG_NS, "rect");`,
		"a text element":    `const label = doc.createElementNS(SVG_NS, "text");`,
		"a group":           `const group = doc.createElementNS(SVG_NS, "g");`,
		"an image":          `const image = doc.createElementNS(SVG_NS, "image");`,
	} {
		assert.Should(t, !hazardous(benign), "the scan read %s as a hazard: %q", name, benign)
	}

	// The comment strip is what keeps this file and mst-canvas.js from
	// failing on their own prose: both name every element they refuse.
	commented := "// <foreignObject> re-enters the HTML parser and is never emitted\nconst a = 1;\n"
	for _, line := range codeLines(commented) {
		assert.Should(t, !hazardous(line.code), "the scan read a comment as a call: %q", line.text)
	}

	// And it really did open the canvas, rather than passing because the
	// walk never reached the directory the components live in.
	scanned, _ := scanForSVGHazards(t)
	want := "static/components/mst-canvas.js"
	found := false
	for _, path := range scanned {
		if path == want {
			found = true
		}
	}
	assert.Should(t, found, "the SVG hazard scan never read %s; it read %v", want, scanned)
}

// literalTag unwraps a quoted tag argument. A tag held in a variable —
// which is how the emitter itself creates elements, from the contract's
// own map — is not a name this scan can read, and is guarded instead by
// TestTheEmitterContractNamesNoScriptableElement, which reads the map.
func literalTag(argument string) (string, bool) {
	if len(argument) >= 2 && strings.HasPrefix(argument, `"`) && strings.HasSuffix(argument, `"`) {
		return argument[1 : len(argument)-1], true
	}
	return "", false
}

// hazardous is the scan's judgement of one line, factored out so the
// guard on the guard exercises the same three matchers the walk does
// rather than a restatement of them.
func hazardous(code string) bool {
	if xlinkRE.MatchString(code) || foreignObjectRE.MatchString(code) {
		return true
	}
	for _, hit := range createElementNSRE.FindAllStringSubmatch(code, -1) {
		if hit[1] != "SVG_NS" {
			return true
		}
		if tag, ok := literalTag(hit[2]); ok && scriptableSVG[tag] {
			return true
		}
	}
	return false
}
