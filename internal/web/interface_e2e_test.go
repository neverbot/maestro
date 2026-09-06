package web_test

import (
	"bufio"
	"bytes"
	"context"
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
	"time"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/testutil"
	"github.com/neverbot/maestro/internal/views"
	"github.com/neverbot/maestro/internal/web"
)

// The interface sub-project's definition of done, over the transports a
// browser actually uses.
//
// **It is deliberately not views_e2e_test.go again.** That file walks the
// same story over the MCP tool surface and answers "does the product
// work"; this one seeds through those same tools and then reads
// everything else the way the front end does — the shell at `/g/{slug}`,
// `GET /api/games/{slug}/summary`, `GET …/views/by-key/{key}`,
// `POST …/views/run`, `POST …/views/by-key/{key}/positions`, and the SSE
// stream at `GET …/events`. Every number the frame puts on screen and
// every rule the client obeys is a field of one of those responses, and
// until this file nothing asserted that the *routes* carry them.
//
// # What was driven in a browser, and what a Go test can say about it
//
// Task 18 is the one task of this plan whose evidence is a real browser:
// computed styles need a cascade, a frame budget needs a frame, and two
// pages talking over an event stream need two pages. That half was driven
// against a running instance (Firefox, `make dev`) and its measurements
// are recorded below so they can be read next to the code they judge. **A
// measurement recorded in a comment is not an assertion and this file
// does not pretend otherwise** — what is asserted here is the server half
// each browser step stood on, plus (at the end) three source-shape guards
// over the exact wiring lines the browser found missing.
//
// Recorded from the driven run, on the seeded game described below:
//
//	step 2 — computed styles resolve the tokens. `--paper` resolved to
//	  #faf8f5 and `body`'s computed `background-color` to
//	  rgb(250, 248, 245), the same colour; `--ink` #14140f and the
//	  computed `color` rgb(20, 20, 15). Three `section.lane` elements,
//	  headed Views, Catalogue and Prose. No Node harness can see any of
//	  this: it needs a cascade.
//
//	step 3 — `/g/{slug}/v/mage-quests?p.class_key=mage` drew 5 nodes and
//	  5 edges. The parameter bar's input carried "mage" as a *property*
//	  (Lit's `.value=`), so it is in `input.value` and in neither the
//	  attribute nor the text — which is worth writing down, because a
//	  check that read the DOM as text would have called a working binding
//	  broken. The footer carried both durations: "5 nodes, 5 edges ·
//	  depth 1 · query 9 ms · layout 23 ms". The legend did not exist at
//	  all; see the first defect below.
//
//	step 4 — the budgets, measured with `performance.now()` around a
//	  freshly opened window:
//	    game page, first contentful paint      33-56 ms   (budget 1 s)
//	    game page, three lanes populated      121-182 ms  (budget 1 s)
//	    500-node graph, opened to nodes drawn 340-540 ms  (budget 1 s)
//	      of which layout                      75-85 ms   (budget 2 s)
//	    1000-node graph, opened to drawn          502 ms
//	      of which layout                         230 ms
//	  The 500-node layout is faster than the 85-101 ms the plan recorded
//	  earlier and nowhere near the 2 s hard stop; no budget failed, so no
//	  constant was touched. Measured in a `window.open`ed page and not an
//	  iframe, because `frame-ancestors 'none'` refuses to be framed even
//	  same-origin — an iframe stays at about:blank forever and the first
//	  attempt at this measurement timed out rather than failing.
//
//	step 5 — four drags through real pointer events on the real surface:
//	  one `POST …/positions` each, counted off resource timing, and the
//	  four coordinates survived a reload exactly (each rect's `x` was the
//	  stored centre minus half its width, to the digit).
//
//	step 6 — the two-page case, and the first time anything watched a
//	  second page receive a drag. Page B, open on the same view, went
//	  from 1 to 2 `/views/run` requests — **one** coalesced re-read, not a
//	  patch from the payload — and the dragged node moved 396 to 441 to
//	  match page A. With a *local* drag in flight on page B, page A's next
//	  drag did not arrive: the run count stayed at 2 and the node page A
//	  had moved stayed where page B last saw it. On page B's drop the
//	  deferred re-read arrived, exactly one (2 to 3), page B's own dropped
//	  node kept its new coordinate and the colleague's caught up.
//
//	step 6b — the frame cost of one drag move, which is Task 14's
//	  performance claim and had never been measured on a real frame. A
//	  `MutationObserver` over the whole canvas shadow tree during one
//	  `pointermove`: **5 mutations** — one `transform` on the drag layer's
//	  group, and `x1`/`y1`/`x2`/`y2` on the one edge leaving the
//	  selection — out of 25 elements in the surface, in 3 ms. The claim
//	  holds: one transform write plus the edges of the selection, never
//	  the tree.
//
//	step 7 — twelve quests added over MCP. They do **not** arrive in an
//	  open page and that is by design, not a fault: client.js's
//	  `applyEvent` handles the four `view.*` kinds, the two renames and
//	  `resync`, and `entity.upserted` falls to the `kind.unhandled` arm,
//	  so a picture is never swapped under a designer by content arriving.
//	  The honest consequence is recorded as the fourth finding below. On
//	  the next run they were all there: 17 nodes, the four pinned ones at
//	  byte-identical coordinates, and on the map 4 solid anchors, 13
//	  hollow rings and the band "12 new nodes were placed automatically."
//
//	step 8 — the rename alone does **not** stop the view drawing, and the
//	  plan's step 8 assumed it would. A rename moves the catalogue and not
//	  the reference: the stored ref row still carries the relation type's
//	  id, so `views.run` answered 200 with a `stale[]` advisory and the
//	  full 17-node picture, and the frame showed a `<details>` in the
//	  title strip reading "This view names 1 thing the game now spells
//	  differently" over the pointer `/traverse/0/via/0` and both
//	  spellings. What produces the refusal step 8 describes is a reference
//	  the game no longer has, so the walk removed the `requires` relation
//	  type as well. Then: `views.run` refused with `query_stale`, the page
//	  drew **zero node marks**, the diagnostics panel carried both of the
//	  server's own sentences with their pointers, and one click on "Run
//	  anyway (best effort)" produced 17 nodes and 16 edges — one edge
//	  fewer, the dropped one — under a `data-code="stale"` banner naming
//	  the dropped step, whose row swatch computed to rgb(176, 168, 154),
//	  which is `--dropped` (#b0a89a).
//
//	step 9 — the keyboard path, driven alone. Focusing a twin row selects
//	  its node, and after the fix below one ArrowRight on the focused
//	  surface moved the node 287 to 288 and sent exactly one write.
//	  Enumerating every focusable element in the page, light DOM and every
//	  shadow root: none is inside an `aria-hidden="true"` subtree, and the
//	  one focusable thing in the canvas is `.surface-host`, which wraps
//	  the hidden `<svg>` rather than living in it.
//
//	step 10 — **not driven, because it does not exist.** Below tablet
//	  width the view page is meant to fall back to the twin with the
//	  writing interactions disabled rather than shrunk (design spec §9).
//	  At a 600 px viewport the canvas was still displayed at 536 px with
//	  every writing interaction live, and `styles.css` has exactly one
//	  media query that is not the dark theme — the game home's three-lane
//	  grid. No task of this plan built the fallback and Task 18 is not the
//	  place to invent a responsive mode, so the step is left unticked and
//	  recorded here rather than reported as done. It is the one item of
//	  §11 this sub-project does not deliver.
//
// # The four things the browser found that no harness had
//
//  1. **The colour key was built by four renderers and drawn by nobody.**
//     `palette.js`'s `legendFor` returns the rows, every renderer test
//     asserts over them, `scene.legend` carries them — and no component,
//     page or emitter ever read the field. A `graph` with `color_by` drew
//     eight hues with nothing anywhere on the page saying what one meant.
//     Fixed by `scene.js`'s `legendModel` and a legend strip in
//     `mst-view-frame`; confirmed on the seeded game, where the two rows
//     computed to rgb(122, 53, 140) and rgb(110, 79, 233) and the nodes
//     carried `var(--data-6)` and `var(--data-4)`, which are those two
//     colours.
//
//  2. **The map renderer was never given a composition.** `map.js`
//     promises that a node new to a saved arrangement is placed by the
//     client, drawn hollow and counted; it reads that from
//     `options.layout`, and `pages/view.js` passed none. Twelve quests
//     added to a map with four pinned rows all went to the shelf, zero
//     rings were hollow and the band could not fire. Fixed by
//     `mapComposition`; after it, 4 solid, 13 hollow, "12 new nodes were
//     placed automatically", pinned coordinates unmoved.
//
//  3. **`mst-select` had no listener.** The twin dispatches it on row
//     focus, its header says the selection travels so a canvas can follow
//     it, `twin_test.mjs` asserts it goes out "under the name a canvas
//     will listen for" — and nothing listened. The whole keyboard path of
//     §8.1 was dead: tab through the twin, move to the canvas, press an
//     arrow key, and nothing moved and nothing was written.
//
//  4. **A view says nothing when the game grows under it.** Not a defect
//     of any one line — it is what the event rules add up to. Content
//     events are unhandled by design, and `relation_type.renamed` re-reads
//     the *view row*, which a rename does not change; so an open picture
//     is silently a picture of an older game, with no band and no
//     invitation to re-run. Recorded, not fixed: changing it means
//     deciding what a designer is told and when, which is a design
//     decision and not a wiring one.
//
// All three of the wiring defects are the same defect, and it is the one
// this sub-project keeps finding: correct in the module, asserted by a
// harness that called the module directly, dead at the call site. The
// three guards at the bottom of this file are the cheapest thing that
// goes red for the fourth instance without a browser.

// The fixture: one game, one designer's cookie, one agent's token, and a
// hub, because step 6's whole subject is what one page's write does to
// another page's stream.
type interfaceWorld struct {
	srv   *web.Server
	ts    *httptest.Server
	deps  web.MCPDeps
	hub   *realtime.Hub
	agent web.Caller
	game  uuid.UUID
	slug  string
	token string
	human *http.Cookie
}

// The seeded game, which is views_e2e_test.go's fixture on purpose: two
// zones so `color_by` is not a constant, a level-26 quest belonging to
// the *warrior* so the traversal is load-bearing, and quests either side
// of the band so a filter that did nothing would be visible.
type interfaceQuest struct {
	key   string
	name  string
	level float64
	zone  string
	class string
}

var interfaceQuests = []interfaceQuest{
	{"cooking-for-bruises", "Cooking for Bruises", 12, "elwynn", "mage"},
	{"kobold-candles", "Kobold Candles", 15, "elwynn", "mage"},
	{"wanted-hogger", "Wanted: Hogger", 22, "elwynn", "mage"},
	{"the-defias-brotherhood", "The Defias Brotherhood", 25, "westfall", "mage"},
	{"charge-of-the-mounted", "Charge of the Mounted", 26, "elwynn", "warrior"},
	{"jangolode-mine", "The Jangolode Mine", 28, "elwynn", "mage"},
	{"westfall-stew", "Westfall Stew", 30, "westfall", "mage"},
	{"the-deadmines", "The Deadmines", 35, "westfall", "mage"},
	{"the-stockade", "The Stockade", 40, "elwynn", "mage"},
}

// The four the saved view draws, and the four a designer drags.
var interfaceReachable = []string{
	"jangolode-mine", "the-defias-brotherhood", "wanted-hogger", "westfall-stew",
}

const interfaceQuery = `{
  "v": 1,
  "params": [{"key": "class_key", "type": "text", "default": "mage"}],
  "from": [
    {"type": "class", "as": "cls",
     "where": {"field": "@key", "op": "eq", "value": {"param": "class_key"}}}
  ],
  "traverse": [
    {"from": "cls", "via": "available_to", "direction": "in", "to_type": "quest",
     "depth": 1, "as": "reachable",
     "where": {"all": [
       {"field": "min_level", "op": "gte", "value": 20},
       {"field": "min_level", "op": "lte", "value": 30}
     ]}}
  ],
  "nodes": [{"set": "cls", "role": "seed"}, {"set": "reachable"}],
  "edges": [
    {"from_step": "reachable"},
    {"via": "requires", "between": ["reachable", "reachable"], "direction": "out"}
  ],
  "project": {
    "label": "@name",
    "color_by": {"related": {"via": "takes_place_in", "direction": "out",
                             "type": "zone", "attr": "@name"}},
    "fields": ["min_level"]
  },
  "limits": {"max_nodes": 500}
}`

// The repaired document, written out in full rather than derived, for
// views_e2e_test.go's reason: a repair an agent makes is a document it
// writes, and asserting against a derivation is asserting against the
// derivation. Both references move — the renamed one and the removed one.
const interfaceRepairedQuery = `{
  "v": 1,
  "params": [{"key": "class_key", "type": "text", "default": "mage"}],
  "from": [
    {"type": "class", "as": "cls",
     "where": {"field": "@key", "op": "eq", "value": {"param": "class_key"}}}
  ],
  "traverse": [
    {"from": "cls", "via": "usable_by", "direction": "in", "to_type": "quest",
     "depth": 1, "as": "reachable",
     "where": {"all": [
       {"field": "min_level", "op": "gte", "value": 20},
       {"field": "min_level", "op": "lte", "value": 30}
     ]}}
  ],
  "nodes": [{"set": "cls", "role": "seed"}, {"set": "reachable"}],
  "edges": [{"from_step": "reachable"}],
  "project": {
    "label": "@name",
    "color_by": {"related": {"via": "takes_place_in", "direction": "out",
                             "type": "zone", "attr": "@name"}},
    "fields": ["min_level"]
  },
  "limits": {"max_nodes": 500}
}`

// TestTheInterfaceEndToEnd is the walk, over the routes the front end
// calls, in the order §11 names them.
//
// It is one test and not ten, because the steps are a sequence: step 5
// drags what step 3 drew, step 7 asserts those coordinates did not move,
// and step 8 breaks the query step 1 saved. Splitting it would either
// re-seed nine times or share state between tests that claim to be
// independent.
func TestTheInterfaceEndToEnd(t *testing.T) {
	w := newInterfaceWorld(t)

	// --- Step 2: the game page's own data, by slug ---------------------
	//
	// The shell resolves nothing server-side, so what the browser gets at
	// /g/{slug} is bytes, and what fills the three lanes is the summary.
	shell := w.get(t, w.human, "/g/"+w.slug)
	if shell.Code != http.StatusOK {
		t.Fatalf("GET /g/%s = %d, want 200", w.slug, shell.Code)
	}
	if !strings.Contains(shell.Body.String(), "<!doctype html") &&
		!strings.Contains(shell.Body.String(), "<!DOCTYPE html") {
		t.Fatalf("GET /g/%s did not answer with a shell:\n%s", w.slug, first(shell.Body.String(), 200))
	}

	var summary struct {
		EntityTypes []struct {
			Key         string `json:"key"`
			LabelPlural string `json:"label_plural"`
			EntityCount int    `json:"entity_count"`
		} `json:"entity_types"`
		RelationTypes []struct {
			Key string `json:"key"`
		} `json:"relation_types"`
	}
	w.decode(t, w.get(t, w.human, w.api("/summary")), &summary)
	// Three types and three relation types: the middle lane's two lists,
	// asserted by content and not by count alone, because a summary that
	// answered the same list twice would satisfy a count.
	if got := keysOfTypes(summary.EntityTypes); !equalStrings(got, []string{"class", "quest", "zone"}) {
		t.Errorf("the catalogue lane's types are %v", got)
	}
	if got := keysOfRelationTypes(summary.RelationTypes); !equalStrings(got,
		[]string{"available_to", "requires", "takes_place_in"}) {
		t.Errorf("the catalogue lane's relation types are %v", got)
	}

	// The views lane, and the prose lane, over the two routes that fill
	// them. A game page with an empty views lane is the failure mode Task
	// 15 shipped a guard for; here it is asserted over the real route.
	var listing struct {
		Items []struct {
			Key      string `json:"key"`
			Renderer string `json:"renderer"`
		} `json:"items"`
	}
	w.decode(t, w.get(t, w.human, w.api("/views")), &listing)
	if len(listing.Items) != 1 || listing.Items[0].Key != "mage-quests" || listing.Items[0].Renderer != "graph" {
		t.Fatalf("the views lane holds %+v", listing.Items)
	}
	var docs struct {
		Items []struct {
			Path string `json:"path"`
		} `json:"items"`
	}
	w.decode(t, w.get(t, w.human, w.api("/docs")), &docs)
	if len(docs.Items) != 1 || docs.Items[0].Path != "lore/westfall.md" {
		t.Fatalf("the prose lane holds %+v", docs.Items)
	}

	// --- Step 3: the view, its parameter, and the two durations --------
	//
	// The page reads the row first (it needs the renderer and its
	// parameters before it can draw), then runs it with the binding the
	// URL carried.
	var row struct {
		Key            string          `json:"key"`
		Renderer       string          `json:"renderer"`
		RendererParams map[string]any  `json:"renderer_params"`
		Query          json.RawMessage `json:"query"`
		Version        int             `json:"version"`
	}
	w.decode(t, w.get(t, w.human, w.api("/views/by-key/mage-quests")), &row)
	if row.Renderer != "graph" {
		t.Fatalf("the row the page reads says renderer %q", row.Renderer)
	}

	envelope := w.run(t, map[string]any{"key": "mage-quests", "params": map[string]any{"class_key": "mage"}})
	if got := nodeKeys(envelope); !equalStrings(got, sortedCopy(append([]string{"mage"}, interfaceReachable...))) {
		t.Fatalf("the picture holds %v", got)
	}
	// The footer's two numbers. `duration_ms` is the server's; the layout
	// duration is the client's own and cannot be asserted here, which is
	// why the driven run's value is recorded in this file's header
	// instead of asserted.
	if envelope.Stats.DurationMs < 0 {
		t.Errorf("stats.duration_ms = %d", envelope.Stats.DurationMs)
	}
	// The colour slot every legend row is built from. Two distinct
	// values over the four quests, and *absent* on the seed class — which
	// is the row the legend calls "not set" and the drawing dashes.
	zones := map[string]int{}
	unset := 0
	for _, node := range envelope.Nodes {
		value, ok := node.Attrs["color_by"]
		if !ok {
			unset++
			continue
		}
		zones[fmt.Sprint(value)]++
	}
	if len(zones) != 2 || unset != 1 {
		t.Errorf("color_by came back with %d values and %d unset nodes; the legend needs two rows and an unset row",
			len(zones), unset)
	}

	// --- Steps 5 and 6: the write, and what a second page sees ---------
	//
	// One subscriber, opened *before* the write, because the whole claim
	// is about what the stream delivers to a page that was already there.
	stream, closeStream := w.subscribe(t)
	defer closeStream()

	drags := make([]web.ViewsPositionInput, 0, len(interfaceReachable))
	for i, key := range interfaceReachable {
		drags = append(drags, web.ViewsPositionInput{
			EntityType: "quest", EntityKey: key,
			X: float64(120 + 90*i), Y: float64(240 - 40*i), Pinned: boolPtr(true),
		})
	}
	// Four drags are four drops, so four writes: a browser sends one per
	// `pointerup` and Task 14's claim is that it sends no more than one.
	for _, drag := range drags {
		rec := w.post(t, w.human, w.api("/views/by-key/mage-quests/positions"),
			map[string]any{"positions": []web.ViewsPositionInput{drag}})
		if rec.Code != http.StatusOK {
			t.Fatalf("POST positions for %s = %d: %s", drag.EntityKey, rec.Code, rec.Body.String())
		}
	}

	// **The event carries identity and no coordinates**, which is the one
	// property the reducer's refusal to patch from a payload rests on: a
	// client that *could* patch from it would, and would then be showing
	// a coordinate nothing re-read.
	positions := waitForEvents(t, stream, "view.positions", len(drags))
	for _, event := range positions {
		if event.Data["key"] != "mage-quests" {
			t.Errorf("view.positions named view %v", event.Data["key"])
		}
		for _, forbidden := range []string{"x", "y", "positions", "pinned"} {
			if _, ok := event.Data[forbidden]; ok {
				t.Errorf("view.positions carries %q; a client could patch from it instead of re-reading", forbidden)
			}
		}
	}

	// The reload: the four coordinates come back exactly, and the fifth
	// node still has none. Byte equality and not a tolerance — a
	// coordinate that survived a round trip approximately is a
	// coordinate that moved.
	reloaded := w.run(t, map[string]any{"key": "mage-quests", "params": map[string]any{"class_key": "mage"}})
	stored := positionsByKey(reloaded)
	for _, drag := range drags {
		got, ok := stored[drag.EntityKey]
		if !ok {
			t.Fatalf("%s lost its position across the reload", drag.EntityKey)
		}
		if got.X != drag.X || got.Y != drag.Y || !got.Pinned {
			t.Errorf("%s came back at (%v, %v) pinned=%v, want (%v, %v) pinned", drag.EntityKey, got.X, got.Y, got.Pinned, drag.X, drag.Y)
		}
	}
	if _, ok := stored["mage"]; ok {
		t.Errorf("the seed class came back with a position; nobody placed it")
	}

	// --- Step 7: twelve more quests, and four that did not move --------
	added := make([]web.EntityItemInput, 0, 12)
	edges := make([]web.RelationItemInput, 0, 24)
	for i := range 12 {
		key := fmt.Sprintf("side-quest-%02d", i)
		added = append(added, web.EntityItemInput{
			TypeKey: "quest", Key: key, Name: fmt.Sprintf("Side Quest %d", i+1),
			Fields: map[string]any{"min_level": float64(20 + i%8)},
		})
		zone := "westfall"
		if i%2 == 1 {
			zone = "elwynn"
		}
		edges = append(edges,
			web.RelationItemInput{TypeKey: "available_to",
				Source: web.RefInput{TypeKey: "quest", Key: key},
				Target: web.RefInput{TypeKey: "class", Key: "mage"}},
			web.RelationItemInput{TypeKey: "takes_place_in",
				Source: web.RefInput{TypeKey: "quest", Key: key},
				Target: web.RefInput{TypeKey: "zone", Key: zone}})
	}
	if _, err := web.MCPEntitiesUpsert(context.Background(), w.deps, w.agent, w.game,
		web.EntitiesUpsertInput{Items: added}); err != nil {
		t.Fatalf("the twelve quests: %v", err)
	}
	if _, err := web.MCPRelationsUpsert(context.Background(), w.deps, w.agent, w.game,
		web.RelationsUpsertInput{Items: edges}); err != nil {
		t.Fatalf("the twelve quests' edges: %v", err)
	}

	widened := w.run(t, map[string]any{"key": "mage-quests", "params": map[string]any{"class_key": "mage"}})
	if len(widened.Nodes) != len(envelope.Nodes)+12 {
		t.Fatalf("the picture went from %d nodes to %d; twelve arrived", len(envelope.Nodes), len(widened.Nodes))
	}
	// **Coordinate equality, not by eye.** The twelve arriving must not
	// move the four a designer placed, and `mixed` layout is exactly the
	// mode in which they could.
	after := positionsByKey(widened)
	for _, drag := range drags {
		got := after[drag.EntityKey]
		if got.X != drag.X || got.Y != drag.Y {
			t.Errorf("%s moved from (%v, %v) to (%v, %v) when twelve quests arrived",
				drag.EntityKey, drag.X, drag.Y, got.X, got.Y)
		}
	}
	// And the twelve arrive with no row of their own, which is what makes
	// them the ones a client places and counts.
	for _, item := range added {
		if _, ok := after[item.Key]; ok {
			t.Errorf("%s arrived with a stored position; nobody placed it", item.Key)
		}
	}

	// --- Step 8: the vocabulary moves, and the view says so ------------
	//
	// Two moves, because they are two different failures and the plan's
	// step 8 assumed the first was the second. A *rename* leaves the
	// stored reference resolvable — the ref row carries the id — so the
	// view runs and reports; a *removal* leaves nothing to resolve, and
	// that is what refuses.
	renameVersion := int32(w.relationTypeVersion(t, "available_to"))
	if _, err := web.MCPRelationTypesRename(context.Background(), w.deps, w.agent, w.game,
		web.RelationTypesRenameInput{From: "available_to", To: "usable_by", ExpectedVersion: &renameVersion}); err != nil {
		t.Fatalf("relation_types.rename: %v", err)
	}
	renamed := w.run(t, map[string]any{"key": "mage-quests"})
	if len(renamed.Nodes) == 0 {
		t.Fatalf("the rename stopped the view drawing; the reference is by id and should still resolve")
	}
	if len(renamed.Stale) != 1 || renamed.Stale[0].Pointer != "/traverse/0/via/0" ||
		renamed.Stale[0].Was != "available_to" || renamed.Stale[0].Now != "usable_by" {
		t.Fatalf("the advisory the title strip renders is %+v", renamed.Stale)
	}

	if _, err := web.MCPRelationTypesRemove(context.Background(), w.deps, w.agent, w.game,
		web.RelationTypesRemoveInput{Key: "requires", Cascade: true}); err != nil {
		t.Fatalf("relation_types.remove: %v", err)
	}

	// **No picture at all**, and the pointers the panel puts on screen.
	refusal := w.postRaw(t, w.human, w.api("/views/run"), map[string]any{"key": "mage-quests"})
	if refusal.Code != http.StatusConflict && refusal.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a stale view ran with status %d: %s", refusal.Code, refusal.Body.String())
	}
	var problem struct {
		Error   string `json:"error"`
		Details struct {
			Fields []struct {
				Path    string `json:"path"`
				Message string `json:"message"`
			} `json:"fields"`
		} `json:"details"`
	}
	if err := json.Unmarshal(refusal.Body.Bytes(), &problem); err != nil {
		t.Fatalf("the refusal is not JSON: %s", refusal.Body.String())
	}
	if problem.Error != "query_stale" {
		t.Fatalf("the refusal is %q, want query_stale: %s", problem.Error, refusal.Body.String())
	}
	pointers := map[string]string{}
	for _, field := range problem.Details.Fields {
		pointers[field.Path] = field.Message
	}
	for _, want := range []string{"/traverse/0/via/0", "/edges/1/via/0"} {
		message, ok := pointers[want]
		if !ok {
			t.Fatalf("the panel has no row for %s; it has %v", want, pointers)
		}
		if strings.TrimSpace(message) == "" {
			t.Errorf("the row at %s carries no sentence; the panel renders the server's own words", want)
		}
	}
	// The one sentence the panel must carry verbatim names the missing
	// key, because it is the only thing a designer can act on.
	if !strings.Contains(pointers["/edges/1/via/0"], "requires") {
		t.Errorf("the refusal at /edges/1/via/0 does not name the missing relation type: %q",
			pointers["/edges/1/via/0"])
	}

	// "Run anyway" is one more run with one more member, and it draws the
	// reduced picture: every node, one edge fewer.
	best := w.run(t, map[string]any{"key": "mage-quests", "on_stale": "best_effort"})
	if len(best.Nodes) != len(widened.Nodes) {
		t.Errorf("best effort drew %d nodes, want %d", len(best.Nodes), len(widened.Nodes))
	}
	if len(best.Edges) != len(widened.Edges)-1 {
		t.Errorf("best effort drew %d edges, want %d — the dropped step is one edge",
			len(best.Edges), len(widened.Edges)-1)
	}
	// The banner names the dropped step, so the reduced picture cannot be
	// mistaken for a whole one.
	dropped := false
	for _, entry := range best.Stale {
		if entry.Pointer == "/edges/1/via/0" {
			dropped = true
		}
	}
	if !dropped {
		t.Errorf("the best-effort picture does not name the step it dropped: %+v", best.Stale)
	}

	// The repair, which is an edit of the document and not of the
	// placement: after it the view is clean and the four coordinates are
	// still exactly where the designer left them.
	if _, err := web.MCPViewsUpsert(context.Background(), w.deps, w.agent, w.game, web.ViewsUpsertInput{
		Key: "mage-quests", Name: "Mage quests 20-30",
		Query: json.RawMessage(interfaceRepairedQuery), Renderer: "graph",
		RendererParams:  map[string]any{"color_by": "color_by"},
		ExpectedVersion: int32Of(int32(row.Version)),
	}); err != nil {
		t.Fatalf("the repair: %v", err)
	}
	repaired := w.run(t, map[string]any{"key": "mage-quests"})
	if len(repaired.Stale) != 0 {
		t.Errorf("the repaired view still reports %+v", repaired.Stale)
	}
	final := positionsByKey(repaired)
	for _, drag := range drags {
		got := final[drag.EntityKey]
		if got.X != drag.X || got.Y != drag.Y {
			t.Errorf("%s moved during the repair, from (%v, %v) to (%v, %v)",
				drag.EntityKey, drag.X, drag.Y, got.X, got.Y)
		}
	}
}

// --- The three call sites a browser had to find ------------------------
//
// These are source-shape guards and they are the weakest kind of check in
// this repository. They are here because the property each one holds has
// no runtime signature any harness in this package can reach: the value
// travels from a pure function, through one line of `pages/view.js`, into
// a Lit template that only a browser renders. Every one of the three
// defects they close was live for four or more tasks with every check
// green, and the alternative to a weak guard is no guard.
//
// Each reads the shipped file off disk and each fails on an empty read,
// so none can pass vacuously.

func shippedAsset(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("static", filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if len(raw) == 0 {
		t.Fatalf("%s is empty", name)
	}
	return string(raw)
}

// TestTheRenderersLegendReachesTheFrame closes the first defect: four
// renderers built a legend and the product drew none of it.
func TestTheRenderersLegendReachesTheFrame(t *testing.T) {
	page := shippedAsset(t, "pages/view.js")
	if !regexp.MustCompile(`(?m)^\s*legend: scene && scene\.legend`).MatchString(page) {
		t.Error("pages/view.js does not hand the scene's legend to frameFor: " +
			"every renderer builds one, and until Task 18 nothing on screen read it, " +
			"so a view coloured by a slot said nowhere what any of its hues meant")
	}
	scene := shippedAsset(t, "render/scene.js")
	if !strings.Contains(scene, "export function legendModel(") {
		t.Error("render/scene.js no longer builds the frame's legend model")
	}
	if !regexp.MustCompile(`(?m)^\s*legend: empty \?`).MatchString(scene) {
		t.Error("frameFor no longer carries a legend on the frame it returns")
	}
	frame := shippedAsset(t, "components/mst-view-frame.js")
	if !strings.Contains(frame, "this.legend(frame.legend)") {
		t.Error("mst-view-frame no longer renders frame.legend: the model would be built and dropped, " +
			"which is the state this guard exists to refuse")
	}
	// The swatches are classes and not a style attribute, because a style
	// attribute is inline style and the policy refuses it with no error.
	// static_sinks_test.go's own inline-style guard covers the whole tree;
	// this names the reason at the one place that most wants one.
	for i := 1; i <= 8; i++ {
		rule := fmt.Sprintf(".legend .hue-%d { background: var(--data-%d); }", i, i)
		if !strings.Contains(frame, rule) {
			t.Errorf("mst-view-frame has no rule painting hue %d; a swatch painted from the model "+
				"instead would be a style attribute, which this instance's policy silently drops", i)
		}
	}
}

// TestTheMapRendererIsGivenAComposition closes the second: the branch
// that auto-places a node new to a saved arrangement read
// `options.layout`, and the call site passed none.
func TestTheMapRendererIsGivenAComposition(t *testing.T) {
	page := shippedAsset(t, "pages/view.js")
	if !strings.Contains(page, "layout: mapComposition(envelope, options.row, params)") {
		t.Error("the SCENES entry for `map` no longer passes a composition: without one, " +
			"render/map.js's placementsOf(options.layout) is empty on every real page, every node " +
			"new to a saved arrangement goes to the shelf instead of being placed and counted, " +
			"and the band \"n new nodes were placed automatically\" can never fire")
	}
	if !strings.Contains(page, "export function mapComposition(") {
		t.Error("pages/view.js no longer builds the map's composition")
	}
	if !strings.Contains(page, "row: state.row,") {
		t.Error("the scene options no longer carry the view row, which is where the map's layout mode comes from")
	}
	m := shippedAsset(t, "render/map.js")
	if !strings.Contains(m, "placementsOf(options.layout)") {
		t.Error("render/map.js no longer reads a composition; if the reader moved, this guard is naming the wrong line")
	}
}

// TestTheTwinsSelectionHasAListener closes the third. The behavioural
// half is jstest/pages_test.mjs's
// theTwinsSelectionReachesTheArrangementAtItsCallSite, which drives
// `wire` and goes red when the binding is removed; this holds the shape
// the harness cannot see — that the listener is on the frame, which
// survives a redraw, and not on the twin, which does not.
func TestTheTwinsSelectionHasAListener(t *testing.T) {
	page := shippedAsset(t, "pages/view.js")
	if !strings.Contains(page, "state.frame.addEventListener(SELECT_EVENT,") {
		t.Error("nothing listens for the twin's mst-select event: a keyboard reader can move through " +
			"the twin and the arrow keys on the canvas then move nothing and write nothing, " +
			"which is the whole keyboard path of the accessibility spine")
	}
	twin := shippedAsset(t, "components/mst-twin.js")
	if !strings.Contains(twin, `export const SELECT_EVENT = "mst-select";`) {
		t.Error("mst-twin no longer names the event the page binds; the two spellings have come apart")
	}
}

// --- The world ---------------------------------------------------------

func newInterfaceWorld(t *testing.T) *interfaceWorld {
	t.Helper()
	ctx := context.Background()
	pool := testutil.NewPool(t)
	cfg := config.Config{
		SessionTTL: testConfig().SessionTTL,
		InviteTTL:  testConfig().InviteTTL,
		Argon2:     testConfig().Argon2,
	}
	ids := identity.New(pool, cfg)
	projSvc := projects.New(pool)
	hub := realtime.NewHub()
	mm := metamodel.New(pool, hub)
	vs := views.New(pool, hub)
	md := markdown.New(pool, hub)
	srv := web.NewServer(web.Options{
		Version: "test", Config: cfg, Identity: ids, Projects: projSvc,
		Metamodel: mm, Views: vs, Markdown: md, Hub: hub,
		SSEMaxLifetime: time.Minute, SSEHeartbeatInterval: time.Minute,
	})
	owner, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	game, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create game: %v", err)
	}
	secret, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: game.ID, UserID: owner.ID, Label: "content agent",
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	agent, err := web.CallerForToken(ctx, ids, secret)
	if err != nil {
		t.Fatalf("CallerForToken: %v", err)
	}
	w := &interfaceWorld{
		srv:  srv,
		ts:   httptest.NewServer(srv),
		deps: web.MCPDeps{Identity: ids, Projects: projSvc, Metamodel: mm, Views: vs, Markdown: md},
		hub:  hub, agent: agent, game: game.ID, slug: game.Slug, token: secret,
		human: loginAs(t, srv, "designer@studio.com"),
	}
	t.Cleanup(w.ts.Close)
	w.seed(t)
	return w
}

// seed is step 1, and it goes through the tools an agent calls: three
// entity types, three relation types, the classes, the zones, the nine
// quests, the edges, one document and one saved view.
func (w *interfaceWorld) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	for _, spec := range []web.TypesUpsertInput{
		{Key: "class", Label: "Class", LabelPlural: "Classes"},
		{Key: "quest", Label: "Quest", LabelPlural: "Quests", Schema: []web.FieldInput{
			{Key: "min_level", Type: "number", Min: ptrFloat(1), Max: ptrFloat(60)},
		}},
		{Key: "zone", Label: "Zone", LabelPlural: "Zones"},
	} {
		if _, err := web.MCPTypesUpsert(ctx, w.deps, w.agent, w.game, spec); err != nil {
			t.Fatalf("types.upsert %s: %v", spec.Key, err)
		}
	}
	for _, spec := range []web.RelationTypesUpsertInput{
		{Key: "available_to", Label: "Available to",
			SourceTypeKeys: []string{"quest"}, TargetTypeKeys: []string{"class"}},
		{Key: "requires", Label: "Requires",
			SourceTypeKeys: []string{"quest"}, TargetTypeKeys: []string{"quest"}},
		{Key: "takes_place_in", Label: "Takes place in",
			SourceTypeKeys: []string{"quest"}, TargetTypeKeys: []string{"zone"}},
	} {
		if _, err := web.MCPRelationTypesUpsert(ctx, w.deps, w.agent, w.game, spec); err != nil {
			t.Fatalf("relation_types.upsert %s: %v", spec.Key, err)
		}
	}

	items := []web.EntityItemInput{
		{TypeKey: "class", Key: "mage", Name: "Mage"},
		{TypeKey: "class", Key: "warrior", Name: "Warrior"},
		{TypeKey: "zone", Key: "elwynn", Name: "Elwynn Forest"},
		{TypeKey: "zone", Key: "westfall", Name: "Westfall"},
	}
	for _, q := range interfaceQuests {
		items = append(items, web.EntityItemInput{
			TypeKey: "quest", Key: q.key, Name: q.name,
			Fields: map[string]any{"min_level": q.level},
		})
	}
	if _, err := web.MCPEntitiesUpsert(ctx, w.deps, w.agent, w.game,
		web.EntitiesUpsertInput{Items: items}); err != nil {
		t.Fatalf("entities.upsert: %v", err)
	}

	edges := make([]web.RelationItemInput, 0, len(interfaceQuests)*2+2)
	for _, q := range interfaceQuests {
		edges = append(edges,
			web.RelationItemInput{TypeKey: "available_to",
				Source: web.RefInput{TypeKey: "quest", Key: q.key},
				Target: web.RefInput{TypeKey: "class", Key: q.class}},
			web.RelationItemInput{TypeKey: "takes_place_in",
				Source: web.RefInput{TypeKey: "quest", Key: q.key},
				Target: web.RefInput{TypeKey: "zone", Key: q.zone}})
	}
	edges = append(edges,
		web.RelationItemInput{TypeKey: "requires",
			Source: web.RefInput{TypeKey: "quest", Key: "the-defias-brotherhood"},
			Target: web.RefInput{TypeKey: "quest", Key: "wanted-hogger"}},
		web.RelationItemInput{TypeKey: "requires",
			Source: web.RefInput{TypeKey: "quest", Key: "jangolode-mine"},
			Target: web.RefInput{TypeKey: "quest", Key: "kobold-candles"}})
	if _, err := web.MCPRelationsUpsert(ctx, w.deps, w.agent, w.game,
		web.RelationsUpsertInput{Items: edges}); err != nil {
		t.Fatalf("relations.upsert: %v", err)
	}

	if _, err := web.MCPDocsWrite(ctx, w.deps, w.agent, w.game, web.DocsWriteInput{
		Path: "lore/westfall.md", Kind: strPtr("lore"), ExpectedVersion: int32Of(0),
		Message: "seed the westfall lore",
		Content: "# Westfall\n\nThe fields west of Elwynn, and what the Defias left in them.\n",
	}); err != nil {
		t.Fatalf("docs.write: %v", err)
	}

	if _, err := web.MCPViewsUpsert(ctx, w.deps, w.agent, w.game, web.ViewsUpsertInput{
		Key: "mage-quests", Name: "Mage quests 20-30",
		Description: "Every quest a mage can take between level 20 and 30, coloured by zone.",
		Query:       json.RawMessage(interfaceQuery), Renderer: "graph",
		// `color_by` names a projection *slot*, and the slots are the
		// five the document may write — label, color_by, group_by,
		// size_by, sort_by. The plan's own step 1 says `color_by:
		// "zone"`, which the catalogue refuses: "zone" is the entity type
		// the slot hops to and not the slot's name. Worth writing down,
		// because it is the second time this sub-project has found a plan
		// step written against the spec rather than against the code.
		RendererParams:  map[string]any{"color_by": "color_by"},
		ExpectedVersion: int32Of(0),
	}); err != nil {
		t.Fatalf("views.upsert: %v", err)
	}
}

func (w *interfaceWorld) api(suffix string) string { return "/api/games/" + w.slug + suffix }

func (w *interfaceWorld) get(t *testing.T, cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	w.srv.ServeHTTP(rec, req)
	return rec
}

func (w *interfaceWorld) postRaw(t *testing.T, cookie *http.Cookie, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	w.srv.ServeHTTP(rec, req)
	return rec
}

func (w *interfaceWorld) post(t *testing.T, cookie *http.Cookie, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return w.postRaw(t, cookie, path, body)
}

func (w *interfaceWorld) decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode: %v\n%s", err, first(rec.Body.String(), 400))
	}
}

// interfaceEnvelope is the run envelope, in the spelling the front end
// reads it in — which is the point of decoding it here rather than
// reaching for the domain's own struct.
type interfaceEnvelope struct {
	Nodes []struct {
		Type  string         `json:"type"`
		Key   string         `json:"key"`
		Attrs map[string]any `json:"attrs"`
	} `json:"nodes"`
	Edges     []json.RawMessage `json:"edges"`
	Positions []struct {
		EntityType string  `json:"entity_type"`
		EntityKey  string  `json:"entity_key"`
		X          float64 `json:"x"`
		Y          float64 `json:"y"`
		Pinned     bool    `json:"pinned"`
	} `json:"positions"`
	Stale []struct {
		Code    string `json:"code"`
		Pointer string `json:"pointer"`
		Was     string `json:"was"`
		Now     string `json:"now"`
	} `json:"stale"`
	Stats struct {
		Nodes      int `json:"nodes"`
		Edges      int `json:"edges"`
		DurationMs int `json:"duration_ms"`
	} `json:"stats"`
}

func (w *interfaceWorld) run(t *testing.T, body map[string]any) interfaceEnvelope {
	t.Helper()
	var out interfaceEnvelope
	w.decode(t, w.postRaw(t, w.human, w.api("/views/run"), body), &out)
	return out
}

func (w *interfaceWorld) relationTypeVersion(t *testing.T, key string) int {
	t.Helper()
	var row struct {
		Version int `json:"version"`
	}
	w.decode(t, w.get(t, w.human, w.api("/relation-types/by-key/"+key)), &row)
	return row.Version
}

// --- The stream --------------------------------------------------------

type streamEvent struct {
	Kind string         `json:"kind"`
	Data map[string]any `json:"data"`
}

// subscribe opens the endpoint a second browser tab subscribes to, over a
// real socket: a ResponseRecorder runs a streaming handler to completion,
// which for this one means never.
func (w *interfaceWorld) subscribe(t *testing.T) (chan streamEvent, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.ts.URL+w.api("/events"), nil)
	if err != nil {
		cancel()
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	req.AddCookie(w.human)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("subscribe: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("subscribe status = %d", resp.StatusCode)
	}
	events := make(chan streamEvent, 64)
	go func() {
		defer close(events)
		defer func() { _ = resp.Body.Close() }()
		// The wire shape is the endpoint's own and not a JSON object
		// with a "kind" in it: `event: <kind>`, then `id:`, then
		// `data: <payload>`. So the kind is carried on one line and the
		// payload on another, and this pairs them.
		reader := bufio.NewReader(resp.Body)
		kind := ""
		for {
			line, err := reader.ReadString('\n')
			trimmed := strings.TrimRight(line, "\r\n")
			switch {
			case strings.HasPrefix(trimmed, "event:"):
				kind = strings.TrimSpace(trimmed[len("event:"):])
			case strings.HasPrefix(trimmed, "data:"):
				event := streamEvent{Kind: kind}
				if json.Unmarshal([]byte(strings.TrimSpace(trimmed[len("data:"):])), &event.Data) == nil {
					select {
					case events <- event:
					default:
					}
				}
				kind = ""
			}
			if err != nil {
				return
			}
		}
	}()
	// The subscription must exist before anything is published, or the
	// test is racing its own fixture rather than testing the stream.
	deadline := time.Now().Add(2 * time.Second)
	for w.hub.SubscriberCount(w.game) == 0 {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("the stream never registered a subscriber")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return events, cancel
}

func waitForEvents(t *testing.T, stream chan streamEvent, kind string, want int) []streamEvent {
	t.Helper()
	got := make([]streamEvent, 0, want)
	timeout := time.After(5 * time.Second)
	for len(got) < want {
		select {
		case event, ok := <-stream:
			if !ok {
				t.Fatalf("the stream closed after %d %s event(s), want %d", len(got), kind, want)
			}
			if event.Kind == kind {
				got = append(got, event)
			}
		case <-timeout:
			t.Fatalf("only %d %s event(s) arrived, want %d", len(got), kind, want)
		}
	}
	return got
}

// --- Small shared readings ---------------------------------------------

func nodeKeys(envelope interfaceEnvelope) []string {
	out := make([]string, 0, len(envelope.Nodes))
	for _, node := range envelope.Nodes {
		out = append(out, node.Key)
	}
	return sortedCopy(out)
}

type storedPosition struct {
	X, Y   float64
	Pinned bool
}

func positionsByKey(envelope interfaceEnvelope) map[string]storedPosition {
	out := make(map[string]storedPosition, len(envelope.Positions))
	for _, row := range envelope.Positions {
		out[row.EntityKey] = storedPosition{X: row.X, Y: row.Y, Pinned: row.Pinned}
	}
	return out
}

func keysOfTypes(types []struct {
	Key         string `json:"key"`
	LabelPlural string `json:"label_plural"`
	EntityCount int    `json:"entity_count"`
},
) []string {
	out := make([]string, 0, len(types))
	for _, entry := range types {
		out = append(out, entry.Key)
	}
	return sortedCopy(out)
}

func keysOfRelationTypes(types []struct {
	Key string `json:"key"`
},
) []string {
	out := make([]string, 0, len(types))
	for _, entry := range types {
		out = append(out, entry.Key)
	}
	return sortedCopy(out)
}

func strPtr(v string) *string { return &v }

// equalStrings and sortedCopy: two lists compared by content, because
// every listing this file reads is a set and a comparison that depended
// on the server's ordering would be asserting the ordering.
func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func first(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
