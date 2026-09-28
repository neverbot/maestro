package web_test

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/views"
	"github.com/neverbot/maestro/internal/web"
)

// The guards over "Save as" — the one view a human alone can make.

const saveAsModule = "static/components/mst-save-as.js"

// readModule is the source of one shipped module, with a length floor so
// a guard cannot pass over an empty or moved file.
func readModule(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	assert.Must(t, err == nil, "read %s: %v", path, err)
	assert.Must(t, len(raw) >= 500, "%s is %d bytes: this test would pass on an empty file", path, len(raw))
	return string(raw)
}

// TestTheSaveAsDialogStatesTheKeyRuleTheServerWillApply pins the three
// sentences and the pattern the dialog refuses a key with to
// internal/metamodel/keys.go's rowKeyProblems, which is what will judge
// the key when the request arrives.
func TestTheSaveAsDialogStatesTheKeyRuleTheServerWillApply(t *testing.T) {
	t.Parallel()
	module := readModule(t, saveAsModule)
	keys := readModule(t, "../metamodel/keys.go")

	// The pattern, character for character.
	goPattern := regexp.MustCompile("rowKeyPattern = regexp.MustCompile\\(`([^`]+)`\\)").FindStringSubmatch(keys)
	assert.Must(t, goPattern != nil, "read no rowKeyPattern out of internal/metamodel/keys.go; the rule moved and this guard did not")
	jsPattern := regexp.MustCompile(`export const KEY_PATTERN = /(.+)/;`).FindStringSubmatch(module)
	assert.Must(t, jsPattern != nil, "read no KEY_PATTERN out of %s", saveAsModule)
	if jsPattern[1] != goPattern[1] {
		t.Errorf("the dialog refuses keys by %s and internal/metamodel admits %s: a designer obeying the "+
			"dialog would still be refused, or would be told a legal key is illegal", jsPattern[1], goPattern[1])
	}

	// The length cap, as a number and not as a digit that happens to
	// appear in the file.
	goMax := regexp.MustCompile(`maxRowKeyLen = (\d+)`).FindStringSubmatch(keys)
	assert.Must(t, goMax != nil, "read no maxRowKeyLen out of internal/metamodel/keys.go")
	jsMax := regexp.MustCompile(`export const KEY_MAX = (\d+);`).FindStringSubmatch(module)
	assert.Must(t, jsMax != nil, "read no KEY_MAX out of %s", saveAsModule)
	if jsMax[1] != goMax[1] {
		t.Errorf("the dialog caps a key at %s characters and internal/metamodel caps it at %s", jsMax[1], goMax[1])
	}
	if _, err := strconv.Atoi(jsMax[1]); err != nil {
		t.Errorf("KEY_MAX is %q, which is not a number", jsMax[1])
	}

	// And the three sentences, which are what a designer actually reads.
	// They are rowKeyProblems' own, in its own order.
	for _, sentence := range []string{
		"is required",
		"must be at most ",
		"must be letters, digits, underscores or hyphens, starting with a letter or a digit",
	} {
		assert.Should(t, strings.Contains(keys, sentence), "internal/metamodel/keys.go no longer says %q; the dialog is repeating a sentence "+
			"the server has stopped using", sentence)
		assert.Should(t, strings.Contains(module, sentence), "%s never says %q, which is what rowKeyProblems answers: a refusal a designer reads "+
			"here and a refusal they read from the server must be the same refusal",
			saveAsModule, sentence)
	}
}

// TestTheSaveAsDialogClaimsTheKeyIsFree pins client.js's
// CREATE_EXPECTED_VERSION to internal/views' createExpectedVersion.
func TestTheSaveAsDialogClaimsTheKeyIsFree(t *testing.T) {
	t.Parallel()
	client := readModule(t, "static/client.js")
	viewsSource := readModule(t, "../views/views.go")

	goCreate := regexp.MustCompile(`createExpectedVersion int32 = (\d+)`).FindStringSubmatch(viewsSource)
	assert.Must(t, goCreate != nil, "read no createExpectedVersion out of internal/views/views.go")
	jsCreate := regexp.MustCompile(`export const CREATE_EXPECTED_VERSION = (\d+);`).FindStringSubmatch(client)
	assert.Must(t, jsCreate != nil, "read no CREATE_EXPECTED_VERSION out of internal/web/static/client.js")
	if jsCreate[1] != goCreate[1] {
		t.Errorf("the browser spells \"this view must not exist yet\" as %s and internal/views spells it "+
			"as %s: a copy would overwrite a view nobody asked it to touch", jsCreate[1], goCreate[1])
	}
	// And the constant is what saveViewAs actually sends, rather than a
	// constant beside a literal.
	body := regexp.MustCompile(`(?s)async function saveViewAs\(.*?\n  \}`).FindString(client)
	assert.Must(t, body != "", "read no saveViewAs out of internal/web/static/client.js")
	assert.Should(t, strings.Contains(body, "expected_version: CREATE_EXPECTED_VERSION"), "saveViewAs does not send CREATE_EXPECTED_VERSION: the constant above is then a value nothing reads")
	if !strings.Contains(body, "query: from.query") {
		t.Error("saveViewAs no longer hands the source's own query object to the encoder. That is the whole " +
			"of \"the copy is the source's query\": a document rebuilt on the way out is a query builder, " +
			"and this product deliberately has none")
	}
}

// TestTheSaveAsDialogIsMountedOutsideTheDrawingsHiddenWrapper is the
// fourth of this round's screen findings, applied to the surface this
// task adds rather than only to the ones that already had it.
func TestTheSaveAsDialogIsMountedOutsideTheDrawingsHiddenWrapper(t *testing.T) {
	t.Parallel()
	shell := readModule(t, "static/view.html")
	page := readModule(t, "static/pages/view.js")

	assert.Should(t, strings.Contains(shell, `id="save-as-root"`), "static/view.html declares no save-as-root hole: the dialog would have nowhere to go but the frame")
	assert.Should(t, strings.Contains(page, `doc.getElementById("save-as-root")`), "pages/view.js never looks the save-as-root hole up")
	assert.Should(t, strings.Contains(page, "mountSaveAs(doc, saveAs)"), "pages/view.js never mounts the dialog: a dialog nobody mounted is a mechanism nothing reads")
	assert.Should(t, strings.Contains(page, "wireSaveAs(state.saveAs)"), "pages/view.js never wires the dialog: a controller no gesture reaches is a controller nobody has")
	for _, forbidden := range []string{"frame.append(saveAs", "frame.appendChild(saveAs"} {
		assert.Should(t, !strings.Contains(page, forbidden), "pages/view.js appends the dialog to the frame (%q), which slots it into the "+
			"aria-hidden drawing wrapper: every button in it becomes reachable by keyboard and "+
			"invisible to a screen reader", forbidden)
	}
}

// TestTheRendererCatalogueRouteServesTheWholeTable drives the route the
// dialog's chooser reads.
func TestTheRendererCatalogueRouteServesTheWholeTable(t *testing.T) {
	t.Parallel()
	f := newViewsRESTFixture(t)
	rec := f.call(t, f.cookie, http.MethodGet, f.path("/views/renderers"), nil)
	assert.Must(t, rec.Code == http.StatusOK, "GET /views/renderers = %d: %s", rec.Code, rec.Body.String())
	var out web.RenderersOutput
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}

	catalogue := views.RendererCatalogue()
	assert.Must(t, len(out.Renderers) == len(catalogue), "the route served %d renderers and the catalogue holds %d", len(out.Renderers), len(catalogue))
	for i, want := range catalogue {
		got := out.Renderers[i]
		if got.Name != want.Name {
			t.Errorf("renderer %d is %q on the wire and %q in the catalogue, in declared order",
				i, got.Name, want.Name)
			continue
		}
		assert.Should(t, got.Consumes == want.Consumes && got.Doc == want.Doc && got.Requires == want.RequiresDoc, "%s's prose does not survive the route", want.Name)
		assert.Should(t, got.ReadsBackground == want.ReadsBackground, "%s reads a background: %v on the wire, %v in the catalogue",
			want.Name, got.ReadsBackground, want.ReadsBackground)
		if len(got.Params) != len(want.Params) {
			t.Errorf("%s has %d knobs on the wire and %d in the catalogue: a knob that does not "+
				"arrive is one no designer can reach and an agent can",
				want.Name, len(got.Params), len(want.Params))
			continue
		}
		for j, wantParam := range want.Params {
			gotParam := got.Params[j]
			assert.Should(t, gotParam.Name == wantParam.Name && gotParam.Kind == string(wantParam.Kind) && gotParam.Required == wantParam.Required && gotParam.Doc == wantParam.Doc, "%s's %q does not survive the route: got %+v", want.Name, wantParam.Name, gotParam)
			assert.Should(t, strings.Join(gotParam.Values, ",") == strings.Join(wantParam.Values, ","), "%s's %q admits %v on the wire and %v in the catalogue: a chooser offering a "+
				"spelling the server refuses composes a document views.upsert will not take",
				want.Name, wantParam.Name, gotParam.Values, wantParam.Values)
		}
	}

	// An enum really is carried, so this test cannot pass over a route
	// that dropped every `values` list.
	enums := 0
	for _, r := range out.Renderers {
		for _, p := range r.Params {
			if len(p.Values) > 0 {
				enums++
			}
		}
	}
	if enums == 0 {
		t.Error("no renderer on the wire declares any admitted spelling; the catalogue has several, " +
			"and this test would pass over a route that served none")
	}
}
