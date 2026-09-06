package web_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/views"
)

// The seam between a renderer module and the server's renderer
// catalogue.
//
// internal/web/jstest/render_graph_test.mjs reads the module and asserts
// what it draws; internal/views/renderers_test.go reads the catalogue
// and asserts what it refuses. Neither can see the other, and the
// mistake that lives in between them is silent in both: a control naming
// a parameter the catalogue does not have composes a document
// views.upsert refuses with a pointer nobody expected, and a parameter
// the catalogue has and the module has no control for is a knob a
// designer can never reach and an agent can — a view saved by an agent
// that the interface then draws as if the knob were off.
//
// So this file joins them, in both directions, over the *generated*
// catalogue description rather than over a list retyped here: that text
// is produced from the table and from nothing else, and
// internal/views' own
// TestEveryRendererDeclaresItsParametersAndTheDescriptionIsGeneratedFromThem
// holds it against the table in both directions. Reading it is reading
// the catalogue.

var (
	// A renderer's section opens with "- name (consumes …)" and its
	// parameters are the indented list under it.
	rendererSection = regexp.MustCompile(`(?m)^- ([a-z_]+) \(consumes `)
	rendererParam   = regexp.MustCompile(`(?m)^  - ([a-z_]+): `)
	// The module spells each parameter key once, as an exported
	// constant, and names the constant in its control list.
	moduleParamConst = regexp.MustCompile(`(?m)^export const (PARAM_[A-Z_]+) = "([a-z_]+)";$`)
	moduleControl    = regexp.MustCompile(`(?m)^  control\(\n    (PARAM_[A-Z_]+),`)
)

// catalogueParams is the parameter names one renderer declares, read out
// of the generated description.
func catalogueParams(description, renderer string) []string {
	sections := rendererSection.FindAllStringSubmatchIndex(description, -1)
	for i, section := range sections {
		if description[section[2]:section[3]] != renderer {
			continue
		}
		end := len(description)
		if i+1 < len(sections) {
			end = sections[i+1][0]
		}
		var names []string
		for _, match := range rendererParam.FindAllStringSubmatch(description[section[1]:end], -1) {
			names = append(names, match[1])
		}
		sort.Strings(names)
		return names
	}
	return nil
}

func renderModule(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("static", "render", name))
	if err != nil {
		t.Fatalf("read static/render/%s: %v", name, err)
	}
	return string(raw)
}

// TestTheGraphRenderersControlsAreTheCataloguesParameters is the join.
//
// Both directions, because the two failures are different accidents: a
// control with no parameter is a document the server will refuse, and a
// parameter with no control is a knob the interface silently ignores.
func TestTheGraphRenderersControlsAreTheCataloguesParameters(t *testing.T) {
	source := renderModule(t, "graph.js")

	byConstant := map[string]string{}
	for _, match := range moduleParamConst.FindAllStringSubmatch(source, -1) {
		byConstant[match[1]] = match[2]
	}
	if len(byConstant) == 0 {
		t.Fatal("read no PARAM_ constant out of render/graph.js: the module's parameter spellings moved and this guard did not")
	}

	var controlled []string
	for _, match := range moduleControl.FindAllStringSubmatch(source, -1) {
		name, ok := byConstant[match[1]]
		if !ok {
			t.Errorf("render/graph.js declares a control for %s, which is not a parameter constant", match[1])
			continue
		}
		controlled = append(controlled, name)
	}
	sort.Strings(controlled)

	// Every constant is used by a control: a parameter spelled in the
	// module and offered nowhere is the same knob-nobody-can-reach as a
	// parameter the module never heard of.
	var declared []string
	for _, name := range byConstant {
		declared = append(declared, name)
	}
	sort.Strings(declared)
	if strings.Join(controlled, ",") != strings.Join(declared, ",") {
		t.Errorf("render/graph.js's controls are %v and its parameter constants are %v; every parameter it knows needs a control", controlled, declared)
	}

	want := catalogueParams(views.RendererDescription(), views.RendererGraph)
	if len(want) == 0 {
		t.Fatal("read no parameter for `graph` out of the renderer catalogue; this guard is reading the wrong text")
	}
	if strings.Join(controlled, ",") != strings.Join(want, ",") {
		t.Errorf("render/graph.js offers %v and the catalogue declares %v: a control with no parameter composes a document "+
			"the server refuses, and a parameter with no control is a knob only an agent can set", controlled, want)
	}

	// And the renderer this module says it is, is one the catalogue has.
	if !strings.Contains(source, `export const RENDERER = "`+views.RendererGraph+`";`) {
		t.Errorf("render/graph.js does not name %q as its renderer", views.RendererGraph)
	}
	found := false
	for _, name := range views.RendererNames() {
		if name == views.RendererGraph {
			found = true
		}
	}
	if !found {
		t.Errorf("the catalogue has no renderer named %q", views.RendererGraph)
	}
}

// TestTheCatalogueParameterScanReadsWhatItClaimsTo is the guard on the
// guard, in both directions: a scan that read nothing, or that read
// every renderer's parameters into one list, would make the join above
// pass on any module ever written.
func TestTheCatalogueParameterScanReadsWhatItClaimsTo(t *testing.T) {
	fixture := strings.Join([]string{
		"The renderer catalogue. Preamble that names nothing.",
		"",
		"- alpha (consumes nodes): draws.",
		"  requires: nothing.",
		"  - first: true or false. Doc.",
		"  - second: a number. Doc.",
		"",
		"- beta (consumes nodes, edges): draws.",
		"  - third: a number. Doc.",
		"",
	}, "\n")

	if got := catalogueParams(fixture, "alpha"); strings.Join(got, ",") != "first,second" {
		t.Errorf("alpha's parameters = %v, want [first second]: the scan does not stop at the next renderer", got)
	}
	if got := catalogueParams(fixture, "beta"); strings.Join(got, ",") != "third" {
		t.Errorf("beta's parameters = %v, want [third]", got)
	}
	if got := catalogueParams(fixture, "gamma"); got != nil {
		t.Errorf("gamma's parameters = %v, want none: a renderer the catalogue does not have has no parameters", got)
	}

	// And it really reads the shipped text: the real catalogue's `graph`
	// section has parameters, and every one of them is a name the module
	// could plausibly spell.
	real := catalogueParams(views.RendererDescription(), views.RendererGraph)
	if len(real) < 2 {
		t.Fatalf("the real catalogue gave %v for `graph`; the description's shape changed", real)
	}
	for _, name := range real {
		if strings.TrimSpace(name) != name || name == "" {
			t.Errorf("parameter %q read out of the description is not a bare name", name)
		}
	}
}
