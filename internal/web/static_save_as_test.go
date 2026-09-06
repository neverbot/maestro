package web_test

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/views"
	"github.com/neverbot/maestro/internal/web"
)

// The guards over "Save as" — the one view a human alone can make.
//
// internal/web/jstest/save_as_test.mjs drives the dialog and asserts the
// properties that are about *a gesture and the bytes it produces*: that
// the copied query is the source's document byte for byte, that an
// illegal key costs no request, that the chooser offers the list the
// server served. None of those exists on the server, and none of these
// exists in the browser.
//
// What lives here is the join. The dialog states a key rule before it
// sends anything and claims a version that means "this row must not
// exist yet", and both of those are second copies of rules that live in
// Go. A second copy of a rule is what this repository's standing failure
// list calls drift, so both are pinned, in both directions, to the
// source that enforces them.

const saveAsModule = "static/components/mst-save-as.js"

// readModule is the source of one shipped module, with a length floor so
// a guard cannot pass over an empty or moved file.
func readModule(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(raw) < 500 {
		t.Fatalf("%s is %d bytes: this test would pass on an empty file", path, len(raw))
	}
	return string(raw)
}

// TestTheSaveAsDialogStatesTheKeyRuleTheServerWillApply pins the three
// sentences and the pattern the dialog refuses a key with to
// internal/metamodel/keys.go's rowKeyProblems, which is what will judge
// the key when the request arrives.
//
// The dialog checks the key itself so that a designer who typed a space
// is not charged a round trip for it — and, more to the point, so that
// the one refusal it can prevent entirely is prevented. That is only
// worth doing while the words and the rule are the server's. A dialog
// promising a rule the server does not apply is worse than one that
// promises nothing: the sentence looks authoritative, and the designer
// who obeys it is still refused.
//
// The Go side is read out of the source rather than called, because
// rowKeyProblems, rowKeyPattern and maxRowKeyLen are all unexported —
// deliberately, since the key policy is internal/metamodel's alone. A
// source read is the only join available and it is a real one: it fails
// on a rule changed there and left alone here.
func TestTheSaveAsDialogStatesTheKeyRuleTheServerWillApply(t *testing.T) {
	module := readModule(t, saveAsModule)
	keys := readModule(t, "../metamodel/keys.go")

	// The pattern, character for character.
	goPattern := regexp.MustCompile("rowKeyPattern = regexp.MustCompile\\(`([^`]+)`\\)").FindStringSubmatch(keys)
	if goPattern == nil {
		t.Fatal("read no rowKeyPattern out of internal/metamodel/keys.go; the rule moved and this guard did not")
	}
	jsPattern := regexp.MustCompile(`export const KEY_PATTERN = /(.+)/;`).FindStringSubmatch(module)
	if jsPattern == nil {
		t.Fatalf("read no KEY_PATTERN out of %s", saveAsModule)
	}
	if jsPattern[1] != goPattern[1] {
		t.Errorf("the dialog refuses keys by %s and internal/metamodel admits %s: a designer obeying the "+
			"dialog would still be refused, or would be told a legal key is illegal", jsPattern[1], goPattern[1])
	}

	// The length cap, as a number and not as a digit that happens to
	// appear in the file.
	goMax := regexp.MustCompile(`maxRowKeyLen = (\d+)`).FindStringSubmatch(keys)
	if goMax == nil {
		t.Fatal("read no maxRowKeyLen out of internal/metamodel/keys.go")
	}
	jsMax := regexp.MustCompile(`export const KEY_MAX = (\d+);`).FindStringSubmatch(module)
	if jsMax == nil {
		t.Fatalf("read no KEY_MAX out of %s", saveAsModule)
	}
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
		if !strings.Contains(keys, sentence) {
			t.Errorf("internal/metamodel/keys.go no longer says %q; the dialog is repeating a sentence "+
				"the server has stopped using", sentence)
		}
		if !strings.Contains(module, sentence) {
			t.Errorf("%s never says %q, which is what rowKeyProblems answers: a refusal a designer reads "+
				"here and a refusal they read from the server must be the same refusal",
				saveAsModule, sentence)
		}
	}
}

// TestTheSaveAsDialogClaimsTheKeyIsFree pins client.js's
// CREATE_EXPECTED_VERSION to internal/views' createExpectedVersion.
//
// This one is not cosmetic. `expected_version` is the whole of what
// stops a copy overwriting a view somebody else is using: 0 means "this
// view must not exist yet" and every other value is a claim about a row.
// A copy that sent the *source's* version would be a write that
// destroys whatever is at the key it was given, which is the one thing
// no write in this front end may do — and a copy that sent nothing at
// all would be refused as a conflict with a sentence about versions that
// a designer who is creating a view cannot act on.
func TestTheSaveAsDialogClaimsTheKeyIsFree(t *testing.T) {
	client := readModule(t, "static/client.js")
	viewsSource := readModule(t, "../views/views.go")

	goCreate := regexp.MustCompile(`createExpectedVersion int32 = (\d+)`).FindStringSubmatch(viewsSource)
	if goCreate == nil {
		t.Fatal("read no createExpectedVersion out of internal/views/views.go")
	}
	jsCreate := regexp.MustCompile(`export const CREATE_EXPECTED_VERSION = (\d+);`).FindStringSubmatch(client)
	if jsCreate == nil {
		t.Fatal("read no CREATE_EXPECTED_VERSION out of internal/web/static/client.js")
	}
	if jsCreate[1] != goCreate[1] {
		t.Errorf("the browser spells \"this view must not exist yet\" as %s and internal/views spells it "+
			"as %s: a copy would overwrite a view nobody asked it to touch", jsCreate[1], goCreate[1])
	}
	// And the constant is what saveViewAs actually sends, rather than a
	// constant beside a literal.
	body := regexp.MustCompile(`(?s)async function saveViewAs\(.*?\n  \}`).FindString(client)
	if body == "" {
		t.Fatal("read no saveViewAs out of internal/web/static/client.js")
	}
	if !strings.Contains(body, "expected_version: CREATE_EXPECTED_VERSION") {
		t.Error("saveViewAs does not send CREATE_EXPECTED_VERSION: the constant above is then a value nothing reads")
	}
	if !strings.Contains(body, "query: from.query") {
		t.Error("saveViewAs no longer hands the source's own query object to the encoder. That is the whole " +
			"of \"the copy is the source's query\": a document rebuilt on the way out is a query builder, " +
			"and this product deliberately has none")
	}
}

// TestTheSaveAsDialogIsMountedOutsideTheDrawingsHiddenWrapper is the
// fourth of this round's screen findings, applied to the surface this
// task adds rather than only to the ones that already had it.
//
// mst-view-frame wraps the drawing in an `aria-hidden` div because the
// text twin is the accessible content of the answer. Anything appended
// to the frame is slotted into that wrapper, so a focusable control put
// there is one a keyboard can reach and a screen reader will never
// announce — which is worse than either alone. The dialog is full of
// focusable controls, so it gets a hole of its own in the shell.
//
// The harness asserts the mount really mounts; what it cannot see is
// *where the hole is*, because a stub has no shadow DOM and no slot.
// That is this test's half.
func TestTheSaveAsDialogIsMountedOutsideTheDrawingsHiddenWrapper(t *testing.T) {
	shell := readModule(t, "static/view.html")
	page := readModule(t, "static/pages/view.js")

	if !strings.Contains(shell, `id="save-as-root"`) {
		t.Error("static/view.html declares no save-as-root hole: the dialog would have nowhere to go but the frame")
	}
	if !strings.Contains(page, `doc.getElementById("save-as-root")`) {
		t.Error("pages/view.js never looks the save-as-root hole up")
	}
	if !strings.Contains(page, "mountSaveAs(doc, saveAs)") {
		t.Error("pages/view.js never mounts the dialog: a dialog nobody mounted is a mechanism nothing reads")
	}
	if !strings.Contains(page, "wireSaveAs(state.saveAs)") {
		t.Error("pages/view.js never wires the dialog: a controller no gesture reaches is a controller nobody has")
	}
	for _, forbidden := range []string{"frame.append(saveAs", "frame.appendChild(saveAs"} {
		if strings.Contains(page, forbidden) {
			t.Errorf("pages/view.js appends the dialog to the frame (%q), which slots it into the "+
				"aria-hidden drawing wrapper: every button in it becomes reachable by keyboard and "+
				"invisible to a screen reader", forbidden)
		}
	}
}

// TestTheRendererCatalogueRouteServesTheWholeTable drives the route the
// dialog's chooser reads.
//
// It is asserted against internal/views.RendererCatalogue rather than
// against a list written here, in both directions, for the reason the
// generated description exists at all: a route that served five of six
// renderers would leave one unreachable from the interface for as long
// as nobody counted, and a route serving a name the catalogue does not
// hold would offer a designer a renderer views.upsert refuses.
func TestTheRendererCatalogueRouteServesTheWholeTable(t *testing.T) {
	f := newViewsRESTFixture(t)
	rec := f.call(t, f.cookie, http.MethodGet, f.path("/views/renderers"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /views/renderers = %d: %s", rec.Code, rec.Body.String())
	}
	var out web.RenderersOutput
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}

	catalogue := views.RendererCatalogue()
	if len(out.Renderers) != len(catalogue) {
		t.Fatalf("the route served %d renderers and the catalogue holds %d", len(out.Renderers), len(catalogue))
	}
	for i, want := range catalogue {
		got := out.Renderers[i]
		if got.Name != want.Name {
			t.Errorf("renderer %d is %q on the wire and %q in the catalogue, in declared order",
				i, got.Name, want.Name)
			continue
		}
		if got.Consumes != want.Consumes || got.Doc != want.Doc || got.Requires != want.RequiresDoc {
			t.Errorf("%s's prose does not survive the route", want.Name)
		}
		if got.ReadsBackground != want.ReadsBackground {
			t.Errorf("%s reads a background: %v on the wire, %v in the catalogue",
				want.Name, got.ReadsBackground, want.ReadsBackground)
		}
		if len(got.Params) != len(want.Params) {
			t.Errorf("%s has %d knobs on the wire and %d in the catalogue: a knob that does not "+
				"arrive is one no designer can reach and an agent can",
				want.Name, len(got.Params), len(want.Params))
			continue
		}
		for j, wantParam := range want.Params {
			gotParam := got.Params[j]
			if gotParam.Name != wantParam.Name || gotParam.Kind != string(wantParam.Kind) ||
				gotParam.Required != wantParam.Required || gotParam.Doc != wantParam.Doc {
				t.Errorf("%s's %q does not survive the route: got %+v", want.Name, wantParam.Name, gotParam)
			}
			if strings.Join(gotParam.Values, ",") != strings.Join(wantParam.Values, ",") {
				t.Errorf("%s's %q admits %v on the wire and %v in the catalogue: a chooser offering a "+
					"spelling the server refuses composes a document views.upsert will not take",
					want.Name, wantParam.Name, gotParam.Values, wantParam.Values)
			}
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
