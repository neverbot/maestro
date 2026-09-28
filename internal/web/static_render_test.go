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
	// moduleParamImport lifts the names a module imports from params.js.
	moduleParamImport = regexp.MustCompile(`(?s)import \{([^{}]*)\} from "\./params\.js";`)
	moduleControl     = regexp.MustCompile(`(?m)^  control\(\n    (PARAM_[A-Z_]+),`)
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
func moduleParamValues(t *testing.T, source string) map[string][]string {
	t.Helper()
	strConst := map[string]string{}
	for _, match := range moduleStringConst.FindAllStringSubmatch(source, -1) {
		strConst[match[1]] = match[2]
	}
	// The parameter names come from render/params.js now, not from the
	// module that draws with them.
	for name, spelled := range paramSpellings(t, source) {
		strConst[name] = spelled
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

			// The spellings live in render/params.js, which the server
			// also reads at start-up (checkRendererParams); a module
			// imports them and declares a control for each.
			// Only the ones this module imports: params.js holds every
			// renderer's knobs, and this case is about one renderer.
			byConstant := paramSpellings(t, source)
			assert.Must(t, len(byConstant) != 0, "read no PARAM_ constant out of render/params.js: the parameter spellings moved and this guard did not")

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
		params := moduleParamValues(t, source)
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
		byConstant := paramSpellings(t, source)
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

// paramSpellings maps a module's imported PARAM_ constants to the strings
// render/params.js gives them.
func paramSpellings(t *testing.T, source string) map[string]string {
	t.Helper()
	spelling := map[string]string{}
	for _, match := range moduleParamConst.FindAllStringSubmatch(renderModule(t, "params.js"), -1) {
		spelling[match[1]] = match[2]
	}
	assert.Must(t, len(spelling) != 0, "read no PARAM_ constant out of render/params.js")
	out := map[string]string{}
	for _, match := range moduleParamImport.FindAllStringSubmatch(source, -1) {
		for _, name := range strings.Split(match[1], ",") {
			if spelled, ok := spelling[strings.TrimSpace(name)]; ok {
				out[strings.TrimSpace(name)] = spelled
			}
		}
	}
	return out
}
