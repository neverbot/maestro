package web_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/views"
)

// The seam between a renderer module and the server's renderer
// catalogue.

var (
	// A renderer's section opens with "- name (consumes …)" and its
	// parameters are the indented list under it.
	rendererSection = regexp.MustCompile(`(?m)^- ([a-z_]+) \(consumes `)
	rendererParam   = regexp.MustCompile(`(?m)^  - ([a-z_]+): `)
	// The module spells each parameter key once, as an exported
	// constant, and names the constant in its control list.
	moduleParamConst = regexp.MustCompile(`(?m)^export const (PARAM_[A-Z_]+) = "([a-z_]+)";$`)
	moduleControl    = regexp.MustCompile(`(?m)^  control\(\n    (PARAM_[A-Z_]+),`)
	// The constants a module spells its admitted values with, and the
	// list that gathers them.
	moduleStringConst = regexp.MustCompile(`(?m)^export const ([A-Z][A-Z_]*) = "([^"]*)";$`)
	moduleListConst   = regexp.MustCompile(`(?m)^export const ([A-Z][A-Z_]*) = \[([A-Z_, ]+)\];$`)
	// One whole control call, and the identifier argument that carries
	// its values — the only bare capital identifier in the call that is
	// neither the parameter nor the kind.
	moduleControlCall      = regexp.MustCompile(`(?s)\n  control\(\n    (PARAM_[A-Z_]+),\n(.*?)\n  \),`)
	moduleControlValuesArg = regexp.MustCompile(`(?m)^    ([A-Z][A-Z_]*),$`)
	// A control's kind: the second argument of the call, which is one of
	// render/controls.js's own constants.
	moduleControlKind = regexp.MustCompile(`(?m)^  control\(\n    (PARAM_[A-Z_]+),\n    (CONTROL_[A-Z_]+),`)
)

// rendererSectionOf is one renderer's block of the generated
// description, from its own heading to the next renderer's.
func rendererSectionOf(description, renderer string) (string, bool) {
	sections := rendererSection.FindAllStringSubmatchIndex(description, -1)
	for i, section := range sections {
		if description[section[2]:section[3]] != renderer {
			continue
		}
		end := len(description)
		if i+1 < len(sections) {
			end = sections[i+1][0]
		}
		return description[section[1]:end], true
	}
	return "", false
}

// catalogueParams is the parameter names one renderer declares, read out
// of the generated description.
func catalogueParams(description, renderer string) []string {
	section, ok := rendererSectionOf(description, renderer)
	if !ok {
		return nil
	}
	var names []string
	for _, match := range rendererParam.FindAllStringSubmatch(section, -1) {
		names = append(names, match[1])
	}
	sort.Strings(names)
	return names
}

// catalogueEnumValues is the spellings one enum parameter admits, in the
// order the catalogue prints them — which is the order it declares them,
// and which a control offering them has no business reordering.
func catalogueEnumValues(description, renderer, param string) ([]string, bool) {
	section, ok := rendererSectionOf(description, renderer)
	if !ok {
		return nil, false
	}
	match := regexp.MustCompile(`(?m)^  - ` + regexp.QuoteMeta(param) + `: one of "(.*)"\.`).FindStringSubmatch(section)
	if match == nil {
		return nil, false
	}
	return strings.Split(match[1], `", "`), true
}

// moduleParamValues is every enum control a renderer module declares,
// resolved from the constants the module spells them with.
func moduleParamValues(source string) map[string][]string {
	strConst := map[string]string{}
	for _, match := range moduleStringConst.FindAllStringSubmatch(source, -1) {
		strConst[match[1]] = match[2]
	}
	listConst := map[string][]string{}
	for _, match := range moduleListConst.FindAllStringSubmatch(source, -1) {
		var values []string
		for _, name := range strings.Split(match[2], ",") {
			value, ok := strConst[strings.TrimSpace(name)]
			if !ok {
				values = nil
				break
			}
			values = append(values, value)
		}
		if values != nil {
			listConst[match[1]] = values
		}
	}
	out := map[string][]string{}
	for _, call := range moduleControlCall.FindAllStringSubmatch(source, -1) {
		param, ok := strConst[call[1]]
		if !ok {
			continue
		}
		for _, arg := range moduleControlValuesArg.FindAllStringSubmatch(call[2], -1) {
			if values, ok := listConst[arg[1]]; ok {
				out[param] = values
			}
		}
	}
	return out
}

// controlKinds is render/controls.js's vocabulary, resolved: the
// constant a module names against the wire spelling the server uses.
func controlKinds(t *testing.T) map[string]string {
	t.Helper()
	source := renderModule(t, "controls.js")
	out := map[string]string{}
	for _, match := range moduleStringConst.FindAllStringSubmatch(source, -1) {
		if strings.HasPrefix(match[1], "CONTROL_") {
			out[match[1]] = match[2]
		}
	}
	assert.Must(t, len(out) != 0, "read no CONTROL_ constant out of render/controls.js; the module's shape moved and this guard did not")
	return out
}

func renderModule(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("static", "render", name))
	assert.Must(t, err == nil, "read static/render/%s: %v", name, err)
	return string(raw)
}

// The renderers whose module is written, and the catalogue name each of
// them claims. It is a table and not one test per renderer because the
// join is one rule: six copies of it would be five places for it to be
// weakened, which is the same argument render/marks.js is built on.
var rendererModules = []struct {
	module   string
	renderer string
}{
	{"graph.js", views.RendererGraph},
	{"layered.js", views.RendererLayered},
	{"nested.js", views.RendererNested},
	{"map.js", views.RendererMap},
	{"table.js", views.RendererTable},
	{"timeline.js", views.RendererTimeline},
}

// TestEveryRendererInTheCatalogueHasAModule closes the table above.
func TestEveryRendererInTheCatalogueHasAModule(t *testing.T) {
	t.Parallel()
	withModule := map[string]bool{}
	for _, entry := range rendererModules {
		withModule[entry.renderer] = true
	}
	for _, name := range views.RendererNames() {
		assert.Should(t, withModule[name], "the catalogue offers %q and no module in this table draws it: an agent can save that view and a designer meets a blank canvas", name)
	}
	inCatalogue := map[string]bool{}
	for _, name := range views.RendererNames() {
		inCatalogue[name] = true
	}
	for _, entry := range rendererModules {
		assert.Should(t, inCatalogue[entry.renderer], "render/%s claims to draw %q and the catalogue has no such renderer", entry.module, entry.renderer)
	}
	assert.Should(t, len(rendererModules) == len(views.RendererNames()), "%d modules and %d renderers: the two lists have to be the same list", len(rendererModules), len(views.RendererNames()))
}

// TestARenderersControlsAreTheCataloguesParameters is the join.
func TestARenderersControlsAreTheCataloguesParameters(t *testing.T) {
	t.Parallel()
	for _, entry := range rendererModules {
		t.Run(entry.renderer, func(t *testing.T) {
			source := renderModule(t, entry.module)

			byConstant := map[string]string{}
			for _, match := range moduleParamConst.FindAllStringSubmatch(source, -1) {
				byConstant[match[1]] = match[2]
			}
			assert.Must(t, len(byConstant) != 0, "read no PARAM_ constant out of render/%s: the module's parameter spellings moved and this guard did not", entry.module)

			var controlled []string
			for _, match := range moduleControl.FindAllStringSubmatch(source, -1) {
				name, ok := byConstant[match[1]]
				if !ok {
					t.Errorf("render/%s declares a control for %s, which is not a parameter constant", entry.module, match[1])
					continue
				}
				controlled = append(controlled, name)
			}
			sort.Strings(controlled)

			// Every constant is used by a control: a parameter spelled in
			// the module and offered nowhere is the same
			// knob-nobody-can-reach as a parameter the module never heard
			// of.
			var declared []string
			for _, name := range byConstant {
				declared = append(declared, name)
			}
			sort.Strings(declared)
			assert.Should(t, strings.Join(controlled, ",") == strings.Join(declared, ","), "render/%s's controls are %v and its parameter constants are %v; every parameter it knows needs a control", entry.module, controlled, declared)

			want := catalogueParams(views.RendererDescription(), entry.renderer)
			assert.Must(t, len(want) != 0, "read no parameter for %q out of the renderer catalogue; this guard is reading the wrong text", entry.renderer)
			assert.Should(t, strings.Join(controlled, ",") == strings.Join(want, ","), "render/%s offers %v and the catalogue declares %v: a control with no parameter composes a document "+
				"the server refuses, and a parameter with no control is a knob only an agent can set", entry.module, controlled, want)

			// And the renderer this module says it is, is one the
			// catalogue has.
			assert.Should(t, strings.Contains(source, `export const RENDERER = "`+entry.renderer+`";`), "render/%s does not name %q as its renderer", entry.module, entry.renderer)
			found := false
			for _, name := range views.RendererNames() {
				if name == entry.renderer {
					found = true
				}
			}
			assert.Should(t, found, "the catalogue has no renderer named %q", entry.renderer)
		})
	}
}

// TestAnEnumControlOffersTheSpellingsTheCatalogueAdmits is the second
// half of the same seam, and it arrived with `layered`, the first
// renderer to have an enum at all.
func TestAnEnumControlOffersTheSpellingsTheCatalogueAdmits(t *testing.T) {
	t.Parallel()
	description := views.RendererDescription()
	checked := 0
	for _, entry := range rendererModules {
		source := renderModule(t, entry.module)
		params := moduleParamValues(source)
		for name, values := range params {
			want, ok := catalogueEnumValues(description, entry.renderer, name)
			if !ok {
				t.Errorf("render/%s offers values for %q and the catalogue does not declare it as an enum", entry.module, name)
				continue
			}
			assert.Should(t, strings.Join(values, ",") == strings.Join(want, ","), "render/%s offers %v for %q and the catalogue admits %v", entry.module, values, name, want)
			checked++
		}
	}
	// The guard on the guard: a reader that resolved nothing would pass
	// over every module ever written.
	assert.Must(t, checked != 0, "read no enum control out of any renderer module; the modules' shape moved and this guard did not")
}

// TestAControlDeclaresTheKindTheCatalogueDeclares is the third arm of
// the same seam, and it arrived with `map`, the first renderer whose
// parameters are neither values nor slots.
func TestAControlDeclaresTheKindTheCatalogueDeclares(t *testing.T) {
	t.Parallel()
	kinds := controlKinds(t)
	checked := 0
	for _, entry := range rendererModules {
		source := renderModule(t, entry.module)
		byConstant := map[string]string{}
		for _, match := range moduleParamConst.FindAllStringSubmatch(source, -1) {
			byConstant[match[1]] = match[2]
		}
		for _, match := range moduleControlKind.FindAllStringSubmatch(source, -1) {
			param, ok := byConstant[match[1]]
			if !ok {
				t.Errorf("render/%s declares a control for %s, which is not a parameter constant", entry.module, match[1])
				continue
			}
			kind, ok := kinds[match[2]]
			if !ok {
				t.Errorf("render/%s's control for %q names %s, which render/controls.js does not declare", entry.module, param, match[2])
				continue
			}
			want, ok := views.RendererParamKind(entry.renderer, param)
			if !ok {
				t.Errorf("render/%s offers %q and the catalogue's %s has no such parameter", entry.module, param, entry.renderer)
				continue
			}
			assert.Should(t, kind == want, "render/%s's control for %q is a %q and the catalogue declares a %q: a control of the wrong kind offers a designer an editor that composes a document the server refuses",
				entry.module, param, kind, want)
			checked++
		}
	}
	// The guard on the guard, for the reason
	// TestAnEnumControlOffersTheSpellingsTheCatalogueAdmits has one: a
	// reader that resolved nothing would pass over every module ever
	// written.
	assert.Must(t, checked >= len(rendererModules), "read %d control kinds out of %d renderer modules; the modules' shape moved and this guard did not", checked, len(rendererModules))
}

// TestTheCatalogueParameterScanReadsWhatItClaimsTo is the guard on the
// guard, in both directions: a scan that read nothing, or that read
// every renderer's parameters into one list, would make the join above
// pass on any module ever written.
func TestTheCatalogueParameterScanReadsWhatItClaimsTo(t *testing.T) {
	t.Parallel()
	fixture := strings.Join([]string{
		"The renderer catalogue. Preamble that names nothing.",
		"",
		"- alpha (consumes nodes): draws.",
		"  requires: nothing.",
		"  - first: true or false. Doc.",
		"  - second: a number. Doc.",
		`  - third: one of "up", "down". Doc.`,
		"",
		"- beta (consumes nodes, edges): draws.",
		`  - third: one of "left". Doc.`,
		"",
	}, "\n")

	if got := catalogueParams(fixture, "alpha"); strings.Join(got, ",") != "first,second,third" {
		t.Errorf("alpha's parameters = %v, want [first second third]: the scan does not stop at the next renderer", got)
	}
	if got := catalogueParams(fixture, "beta"); strings.Join(got, ",") != "third" {
		t.Errorf("beta's parameters = %v, want [third]", got)
	}
	if got := catalogueParams(fixture, "gamma"); got != nil {
		t.Errorf("gamma's parameters = %v, want none: a renderer the catalogue does not have has no parameters", got)
	}

	// The enum reader is scoped to one renderer for the same reason: two
	// renderers declaring a parameter of the same name with different
	// spellings is exactly what an unscoped read would conflate.
	if got, ok := catalogueEnumValues(fixture, "alpha", "third"); !ok || strings.Join(got, ",") != "up,down" {
		t.Errorf(`alpha's "third" = %v (%v), want [up down]`, got, ok)
	}
	if got, ok := catalogueEnumValues(fixture, "beta", "third"); !ok || strings.Join(got, ",") != "left" {
		t.Errorf(`beta's "third" = %v (%v), want [left]`, got, ok)
	}
	if _, ok := catalogueEnumValues(fixture, "alpha", "first"); ok {
		t.Error(`alpha's "first" is not an enum and must not read as one`)
	}

	// And the module reader really reads a module: `layered` has two
	// enums, and a reader that resolved neither would make the join above
	// vacuous.
	values := moduleParamValues(renderModule(t, "layered.js"))
	assert.Should(t, len(values) == 2, "read %d enum controls out of render/layered.js, want 2: %v", len(values), values)
	if got := values["rank_direction"]; strings.Join(got, ",") != "TB,LR" {
		t.Errorf(`rank_direction's module values = %v, want [TB LR]`, got)
	}

	// And it really reads the shipped text: the real catalogue's `graph`
	// section has parameters, and every one of them is a name the module
	// could plausibly spell.
	real := catalogueParams(views.RendererDescription(), views.RendererGraph)
	assert.Must(t, len(real) >= 2, "the real catalogue gave %v for `graph`; the description's shape changed", real)
	for _, name := range real {
		assert.Should(t, strings.TrimSpace(name) == name && name != "", "parameter %q read out of the description is not a bare name", name)
	}
}
