// The harness for internal/web/static/layout/: the real, unmodified
// engine.js, compose.js and budget.js, imported and driven over plain
// data.
//
// What this covers that no Go test can, and no browser needs to. Layout
// is where three of this sub-project's load-bearing claims are either
// true or quietly false:
//
//   - **The engine is deterministic.** `manual` mode lays out unplaced
//     nodes and never writes the result back, and the views table's seed
//     column was dropped (Task 17, migration 0012), both on that one
//     premise. Every determinism assertion below runs the same input
//     twice *and a third time with the arrays shuffled*, because dagre
//     is deterministic given an insertion order and not otherwise —
//     measured, on the vendored 3.1.1 — and it is engine.js sorting by
//     address that turns "deterministic given an order" into
//     "deterministic". Without the shuffled run these tests would pass
//     on an engine whose stability was an accident of the envelope's
//     `ORDER BY … capped.id`, which is an order by uuid and does not
//     survive a re-seed.
//   - **Pinned nodes never move.** Asserted with `===` against the
//     stored number, not within a tolerance: a designer's coordinate
//     surviving a float round trip *approximately* is a coordinate that
//     drifts one pixel per re-run.
//   - **The fit does not rotate.** A full similarity fit would rotate,
//     and would be strictly better at minimising the error it was given.
//     The fixture below is one whose best-fit rotation is 90°, so the
//     refusal is asserted rather than assumed.
//
// The three degeneracies of the fit get one test each and three
// different fixtures, because a fixture that cannot tell a translation
// from a scale of 1 proves nothing about either.
//
// Run directly: `node internal/web/jstest/layout_test.mjs`.
// internal/web/static_appjs_browser_test.go shells out to it too.

import {
  ACTION_RETRY_LAYOUT,
  BANNER_LAYOUT_BUDGET,
  LAYOUT_BUDGET_MS,
  LAYOUT_RETRY_MS,
  budgetSentence,
  nextBudgetMs,
  runWithBudget,
} from "../static/layout/budget.js";
import {
  DEFAULT_LAYOUT_OPTIONS,
  DEFAULT_NODE_HEIGHT,
  DEFAULT_NODE_WIDTH,
  layoutGraph,
} from "../static/layout/engine.js";
import {
  FIT_IDENTITY,
  FIT_SIMILARITY,
  FIT_TRANSLATION,
  MODE_AUTO,
  MODE_MANUAL,
  MODE_MIXED,
  SOURCE_COMPUTED,
  SOURCE_GRID,
  SOURCE_STORED,
  addressOf,
  compose,
  fitTransform,
  gridFallback,
  layoutView,
  rerunPlan,
  storedFrom,
  subgraphFor,
} from "../static/layout/compose.js";
import { footerFor } from "../static/render/scene.js";
import { twinFor } from "../static/render/twin.js";

let failures = 0;
const pending = [];

function check(name, fn) {
  pending.push([name, fn]);
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function assertEqual(actual, expected, message) {
  if (actual !== expected) {
    throw new Error(`${message}: got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);
  }
}

function assertDeepEqual(actual, expected, message) {
  const a = JSON.stringify(actual);
  const b = JSON.stringify(expected);
  if (a !== b) throw new Error(`${message}: got ${a}, want ${b}`);
}

// --- Fixtures --------------------------------------------------------

// node builds one envelope-shaped node with a measured size. The sizes
// are uneven on purpose: a fixture where every box is the same shape
// cannot tell an overlap resolved along x from one resolved along y.
function node(type, key, width = 100, height = 30) {
  return { type, key, name: `${type} ${key}`, width, height };
}

function edge(source, target, relation = "requires") {
  return { source, target, type: relation };
}

// A small directional graph — the shape spec §5.2 says a game's content
// actually has — with two ranks below the root and a join, so a layout
// that ran can be told from one that silently did nothing.
function questGraph() {
  const nodes = [
    node("quest", "hogger"),
    node("quest", "defias"),
    node("quest", "westfall"),
    node("zone", "elwynn"),
    node("zone", "duskwood"),
    node("zone", "redridge"),
  ];
  const edges = [
    edge({ type: "quest", key: "hogger" }, { type: "quest", key: "defias" }),
    edge({ type: "quest", key: "defias" }, { type: "quest", key: "westfall" }),
    edge({ type: "quest", key: "hogger" }, { type: "zone", key: "elwynn" }),
    edge({ type: "zone", key: "elwynn" }, { type: "zone", key: "duskwood" }),
    edge({ type: "zone", key: "duskwood" }, { type: "zone", key: "redridge" }),
    edge({ type: "quest", key: "westfall" }, { type: "zone", key: "redridge" }),
  ];
  return { nodes, edges };
}

// shuffled is a deterministic permutation — a seeded rotate-and-swap
// rather than Math.random — so a failure is reproducible and a green run
// is not one lucky ordering. It is applied to node arrays and edge
// arrays alike, because dagre is sensitive to both.
function shuffled(items) {
  const out = items.slice();
  for (let i = out.length - 1; i > 0; i--) {
    const j = (i * 7 + 3) % (i + 1);
    const swap = out[i];
    out[i] = out[j];
    out[j] = swap;
  }
  return out;
}

function coordinatesOf(result) {
  const placements = Array.isArray(result) ? result : result.placements;
  return JSON.stringify(placements.map((p) => [p.key, p.x, p.y]));
}

function keyed(placements) {
  return new Map(placements.map((p) => [p.key, p]));
}

function at(result, type, key) {
  const found = keyed(result.placements).get(addressOf({ type, key }));
  if (!found) throw new Error(`no placement for ${type}/${key}`);
  return found;
}

// A frozen fixture. Every mode is handed one of these, so a composer
// that wrote a placement back into the stored rows — or reordered them
// in place to sort them — throws instead of passing.
function frozen(rows) {
  for (const row of rows) Object.freeze(row);
  return Object.freeze(rows);
}

function stored(type, key, x, y, pinned = true) {
  return { key: addressOf({ type, key }), x, y, pinned };
}

// --- The engine ------------------------------------------------------

check("theEngineIsDeterministic", () => {
  const { nodes, edges } = questGraph();
  const first = layoutGraph(nodes, edges);
  const second = layoutGraph(nodes, edges);
  assertEqual(coordinatesOf(second), coordinatesOf(first), "the same input twice");

  // The third run is the one that matters. dagre's own output depends on
  // the order nodes and edges are inserted in — reverse either and every
  // node moves — so this is the assertion that says the stability the
  // no-write-back rule rests on is a property of *this* module and not
  // of the order the server happened to return its rows in.
  const third = layoutGraph(shuffled(nodes), shuffled(edges));
  assertEqual(coordinatesOf(third), coordinatesOf(first), "the same input, shuffled");

  // And a layout really ran: every coordinate is finite and the graph is
  // not a single point, or the two assertions above would hold on an
  // engine that returned zeroes.
  const xs = new Set(first.placements.map((p) => p.x));
  const ys = new Set(first.placements.map((p) => p.y));
  assert(first.placements.every((p) => Number.isFinite(p.x) && Number.isFinite(p.y)), "finite");
  assert(xs.size > 1 && ys.size > 1, "the drawing has two dimensions");
  assert(first.placements.length === nodes.length, "every node was placed");
});

// clusterGraph is six nodes in two interleaved clusters with one edge,
// arranged so that clustering has somewhere to move things *to*: the
// three `a` nodes and the three `b` nodes alternate in address order, so
// an engine that ignored the clustering leaves them interleaved and one
// that honoured it does not.
function clusterGraph(clustered) {
  const nodes = ["a1", "b1", "a2", "b2", "a3", "b3"].map((key) => ({
    ...node("quest", key),
    ...(clustered ? { cluster: key.startsWith("a") ? "alliance" : "horde" } : {}),
  }));
  return { nodes, edges: [edge({ type: "quest", key: "a1" }, { type: "quest", key: "a2" })] };
}

check("clusteringMovesTheLayoutAndAddsNoNode", () => {
  // `cluster_by` draws nothing at all — no enclosure, no heading, no
  // legend row — so the only way it can be anything but a lie is here,
  // in the arrangement. This is the assertion that keeps "draws nothing"
  // from decaying into "does nothing".
  const plain = layoutGraph(clusterGraph(false).nodes, clusterGraph(false).edges);
  const clustered = layoutGraph(clusterGraph(true).nodes, clusterGraph(true).edges);
  assert(
    coordinatesOf(clustered) !== coordinatesOf(plain),
    "clustering changed no coordinate; the parameter reached nothing",
  );

  // And it added nothing to the picture: a cluster is a vertex in the
  // engine's graph, and a renderer handed one would draw a box for a
  // *value*.
  assertEqual(clustered.placements.length, 6, "a cluster is not a node");
  assertDeepEqual(
    clustered.placements.map((p) => p.key).filter((key) => key.includes("cluster")),
    [],
    "and no cluster vertex reaches the placements",
  );

  // Deterministic with clustering on, including the shuffle: the
  // compound path inserts parents of its own, and inserting them in the
  // order the caller's nodes happened to arrive in would be the coupling
  // the engine's sort exists to remove.
  const again = layoutGraph(shuffled(clusterGraph(true).nodes), clusterGraph(true).edges);
  assertEqual(coordinatesOf(again), coordinatesOf(clustered), "the same clustered input, shuffled");

  // The two `b` nodes with no edge between them are adjacent, which is
  // what clustering is *for* and what tells this apart from a layout
  // that merely differs.
  const at = new Map(clustered.placements.map((p) => [p.key, p]));
  const b = ["b1", "b2", "b3"].map((key) => at.get(addressOf({ type: "quest", key })));
  assert(
    b.every((p) => Math.abs(p.y - b[0].y) < 1e-9),
    "the cluster's members share a rank",
  );
});

check("anEmptyClusterValueIsNotACluster", () => {
  // A node whose `cluster_by` slot found nothing carries the empty
  // string. Putting every such node in one parent would invent a group
  // out of an absence — the same distinction the palette makes with its
  // `unset` legend row rather than a ninth hue.
  const { nodes, edges } = clusterGraph(false);
  const empty = nodes.map((n) => ({ ...n, cluster: "" }));
  assertEqual(
    coordinatesOf(layoutGraph(empty, edges)),
    coordinatesOf(layoutGraph(nodes, edges)),
    "an empty cluster name laid out differently from no cluster name",
  );
});

check("anUntypedEdgeSurvivesAClusteredLayout", () => {
  // The regression the clustering work found: dagre's compound layout
  // throws on an edge whose *name* is the empty string, which is what
  // every edge carrying no relation type used to get. A `graph` view
  // with cluster_by on and one untyped edge would have taken the whole
  // picture down with "Cannot set properties of undefined".
  const nodes = [
    { ...node("quest", "a"), cluster: "alliance" },
    { ...node("quest", "b"), cluster: "alliance" },
  ];
  const result = layoutGraph(nodes, [
    { source: { type: "quest", key: "a" }, target: { type: "quest", key: "b" } },
  ]);
  assertEqual(result.placements.length, 2, "an untyped edge in a cluster laid out");
  assert(
    result.placements.every((p) => Number.isFinite(p.x) && Number.isFinite(p.y)),
    "with finite coordinates",
  );
});

check("theEngineRanksAlongTheEdges", () => {
  // The one behavioural claim §5.2 makes about choosing a ranked engine:
  // a directional game graph draws as a hierarchy. Without this the
  // determinism assertions above would be satisfied by a wrapper that
  // returned the same wrong answer every time.
  const { nodes, edges } = questGraph();
  const result = layoutGraph(nodes, edges);
  const byKey = keyed(result.placements);
  const hogger = byKey.get(addressOf({ type: "quest", key: "hogger" }));
  const defias = byKey.get(addressOf({ type: "quest", key: "defias" }));
  assert(defias.y > hogger.y, `defias should rank below hogger: ${hogger.y} → ${defias.y}`);
});

check("anEdgeLeavingThePictureInventsNoNode", () => {
  // Spec §4.2: an edge whose endpoint is not in `nodes` is normal, not
  // an error. dagre's setEdge creates a node for an unknown endpoint, so
  // without the filter this picture would grow an empty seventh box and
  // rearrange itself around an entity the query chose not to draw.
  const { nodes, edges } = questGraph();
  const withStub = [...edges, edge({ type: "quest", key: "hogger" }, { type: "quest", key: "offscreen" })];
  const result = layoutGraph(nodes, withStub);
  assertEqual(result.placements.length, nodes.length, "no node was invented");
  // The second line is the one that catches it. `placements` is built
  // from the nodes that were handed in, so an invented seventh box never
  // appears there — it appears as a rank the real nodes had to make room
  // for, which is a coordinate shift and nothing else.
  assertEqual(coordinatesOf(result), coordinatesOf(layoutGraph(nodes, edges)), "and nothing moved");
});

check("aNodeWithNoMeasuredSizeStillGetsABox", () => {
  const result = layoutGraph([{ type: "quest", key: "hogger" }], []);
  assertEqual(result.placements[0].width, DEFAULT_NODE_WIDTH, "the default width");
  assertEqual(result.placements[0].height, DEFAULT_NODE_HEIGHT, "the default height");
  assert(Number.isFinite(result.placements[0].x), "and a finite coordinate rather than a NaN");
});

check("theEngineAddressesANodeExactlyAsTheTwinAddressesItsRow", () => {
  // The seam between two modules that must agree and have no other
  // reason to. render/twin.js builds a row key out of (type, key); the
  // engine builds a graph vertex out of the same pair. A canvas joining
  // a selected row to a laid-out node joins them on this string, so two
  // spellings of it is a defect nobody would see until a selection
  // silently highlighted nothing.
  const nodes = [node("quest", "hogger"), node("zone", "elwynn")];
  const twin = twinFor({ nodes, edges: [] });
  const laid = layoutGraph(nodes, []);
  assertDeepEqual(
    laid.placements.map((p) => p.key).sort(),
    twin.nodes.rows.map((r) => r.key).sort(),
    "the engine's vertex names are the twin's row keys",
  );
});

check("theLayoutOptionsAreOverridableWithoutRestatingTheRest", () => {
  const { nodes, edges } = questGraph();
  const wide = layoutGraph(nodes, edges, { rankdir: "LR" });
  const tall = layoutGraph(nodes, edges);
  assert(coordinatesOf(wide) !== coordinatesOf(tall), "rankdir reached the engine");
  assertEqual(DEFAULT_LAYOUT_OPTIONS.rankdir, "TB", "and the default is the ranked one §5.2 chose");
});

// --- auto ------------------------------------------------------------

check("autoIgnoresStoredPositionsAndDeletesNothing", () => {
  const { nodes, edges } = questGraph();
  const computed = layoutGraph(nodes, edges);
  const rows = frozen([stored("quest", "hogger", 900, 900), stored("zone", "elwynn", -50, -50)]);
  const before = JSON.stringify(rows);

  const result = compose(MODE_AUTO, computed, rows);
  assertEqual(JSON.stringify(rows), before, "the stored rows are untouched");
  assertDeepEqual(
    result.placements,
    compose(MODE_AUTO, computed, []).placements,
    "and unused: the answer is the answer with no stored rows at all",
  );
  assert(
    !JSON.stringify(result).includes("900"),
    "no stored coordinate reached the arrangement",
  );
  assertEqual(result.placedAutomatically, 0, "and auto counts no node as newly placed");
});

check("draggingIsDisabledInAutoAndNowhereElse", () => {
  // The other half of "what reads layout_mode". A drag in auto writes a
  // view_positions row that nothing will ever read back, so the mode
  // that ignores stored positions is the mode that refuses the write.
  const { nodes, edges } = questGraph();
  const computed = layoutGraph(nodes, edges);
  const rows = [stored("quest", "hogger", 10, 10)];
  assertEqual(compose(MODE_AUTO, computed, rows).draggable, false, "auto");
  assertEqual(compose(MODE_MANUAL, computed, rows).draggable, true, "manual");
  assertEqual(compose(MODE_MIXED, computed, rows).draggable, true, "mixed");
});

// --- manual ----------------------------------------------------------

check("manualDoesNotWriteBackPlacements", () => {
  const { nodes, edges } = questGraph();
  const rows = frozen([stored("quest", "hogger", 400, 10), stored("quest", "defias", 400, 120)]);
  const subgraph = subgraphFor(MODE_MANUAL, nodes, edges, rows);
  assertEqual(subgraph.nodes.length, 4, "the engine is asked for the unplaced sub-graph only");

  const result = compose(MODE_MANUAL, layoutGraph(subgraph.nodes, subgraph.edges), rows);
  // A composer that persisted its automatic placements would have to put
  // them somewhere, and the two somewheres are the stored rows it was
  // handed — frozen, so a push throws — and a write intent in its own
  // answer. Neither exists.
  assertEqual(rows.length, 2, "nothing was appended to the stored rows");
  for (const name of Object.keys(result)) {
    assert(
      !/write|persist|save|commit|dirty/i.test(name),
      `the composer returned a write intent named ${name}; the only writer is a human dragging a node`,
    );
  }
  assertEqual(
    result.placements.filter((p) => p.source === SOURCE_COMPUTED).length,
    4,
    "the four automatic placements are marked as computed rather than as stored",
  );
  assertEqual(result.placedAutomatically, 4, "and counted for the frame's own sentence");
});

check("manualHonoursEveryStoredPositionPinnedOrNot", () => {
  const { nodes, edges } = questGraph();
  const rows = frozen([
    stored("quest", "hogger", 400, 10, true),
    stored("zone", "elwynn", -70.5, 33.25, false),
  ]);
  const subgraph = subgraphFor(MODE_MANUAL, nodes, edges, rows);
  const result = compose(MODE_MANUAL, layoutGraph(subgraph.nodes, subgraph.edges), rows);
  assertEqual(at(result, "quest", "hogger").x, 400, "a pinned row is drawn where it says");
  assertEqual(at(result, "zone", "elwynn").x, -70.5, "and so is an unpinned one: manual honours both");
  assertEqual(at(result, "zone", "elwynn").pinned, false, "with its flag carried through");
  assertEqual(at(result, "zone", "elwynn").source, SOURCE_STORED, "and its provenance");
});

check("unplacedNodesLandInTheSameSpotOnEveryLoad", () => {
  // The property that lets an automatic placement not be persisted. Two
  // loads of the same saved view, the second with the envelope's arrays
  // in a different order, because that is the difference between "the
  // engine is stable" and "the server happened to answer in the same
  // order twice".
  const { nodes, edges } = questGraph();
  const rows = [stored("quest", "hogger", 400, 10)];
  const load = (ns, es) => {
    const subgraph = subgraphFor(MODE_MANUAL, ns, es, rows);
    return compose(MODE_MANUAL, layoutGraph(subgraph.nodes, subgraph.edges), rows);
  };
  const first = load(nodes, edges);
  assertEqual(coordinatesOf(load(nodes, edges)), coordinatesOf(first), "a second load");
  assertEqual(
    coordinatesOf(load(shuffled(nodes), shuffled(edges))),
    coordinatesOf(first),
    "and a load whose envelope arrived in another order",
  );
});

// --- mixed: the fit --------------------------------------------------

check("pinnedNodesNeverMove", () => {
  const { nodes, edges } = questGraph();
  const computed = layoutGraph(nodes, edges);
  // Three pins, deliberately far apart and at coordinates no layout
  // would produce, including a fractional one: a pin restored "within a
  // tolerance" is a pin that drifts, and 33.3 is where that shows.
  const rows = frozen([
    stored("quest", "hogger", -412.5, 88),
    stored("quest", "westfall", 733, 33.3),
    stored("zone", "duskwood", 120, 640),
  ]);
  const result = compose(MODE_MIXED, computed, rows);
  assertEqual(at(result, "quest", "hogger").x, -412.5, "hogger x, exactly");
  assertEqual(at(result, "quest", "hogger").y, 88, "hogger y, exactly");
  assertEqual(at(result, "quest", "westfall").x, 733, "westfall x, exactly");
  assertEqual(at(result, "quest", "westfall").y, 33.3, "westfall y, exactly");
  assertEqual(at(result, "zone", "duskwood").x, 120, "duskwood x, exactly");
  assertEqual(at(result, "zone", "duskwood").y, 640, "duskwood y, exactly");
  for (const type of ["hogger", "westfall"]) {
    assertEqual(at(result, "quest", type).pinned, true, `${type} is still pinned`);
  }
  assertEqual(result.transform.kind, FIT_SIMILARITY, "three spread pins determine a scale");
  assert(result.transform.scale !== 1, "and it is not the identity dressed as one");
});

check("mixedWithNoPinnedNodesIsAuto", () => {
  const { nodes, edges } = questGraph();
  const computed = layoutGraph(nodes, edges);
  const auto = compose(MODE_AUTO, computed, []);
  const mixed = compose(MODE_MIXED, computed, []);
  assertDeepEqual(mixed.placements, auto.placements, "the same arrangement, node for node");
  assertEqual(mixed.transform.kind, FIT_IDENTITY, "the transform is the identity");
  assertEqual(mixed.transform.pins, 0, "because there was nothing to fit to");
  // The one thing that differs, and must: a drag is how a designer
  // leaves this case, so mixed stays draggable where auto does not.
  assertEqual(mixed.draggable, true, "mixed is still draggable");
  assertEqual(auto.draggable, false, "auto is not");

  // An unpinned stored row does not change it. §5.3 gives that row a
  // meaning "in step 3", and with nothing pinned there is no step 3.
  const loose = compose(MODE_MIXED, computed, [stored("quest", "hogger", 900, 900, false)]);
  assertDeepEqual(loose.placements, auto.placements, "an unpinned row alone is still the auto answer");
});

check("mixedWithOnePinnedNodeTranslatesOnly", () => {
  // Widely spaced boxes, so the separation pass has nothing to do and
  // the distances this test measures are the fit's and only the fit's.
  const nodes = [
    node("quest", "a"),
    node("quest", "b"),
    node("quest", "c"),
    node("quest", "d"),
  ];
  const edges = [
    edge({ type: "quest", key: "a" }, { type: "quest", key: "b" }),
    edge({ type: "quest", key: "b" }, { type: "quest", key: "c" }),
    edge({ type: "quest", key: "c" }, { type: "quest", key: "d" }),
  ];
  const computed = layoutGraph(nodes, edges, { nodesep: 200, ranksep: 200 });
  const result = compose(MODE_MIXED, computed, [stored("quest", "b", 1000, -500)]);
  assertEqual(result.transform.kind, FIT_TRANSLATION, "one pin determines a translation");
  assertEqual(result.transform.scale, 1, "and no scale at all");
  assertEqual(result.transform.pins, 1, "from one pin");

  const before = keyed(computed.placements);
  const after = keyed(result.placements);
  const keys = [...after.keys()].sort();
  for (let i = 0; i < keys.length; i++) {
    for (let j = i + 1; j < keys.length; j++) {
      const d0 = distance(before.get(keys[i]), before.get(keys[j]));
      const d1 = distance(after.get(keys[i]), after.get(keys[j]));
      assert(
        Math.abs(d0 - d1) < 1e-9,
        `${keys[i]}–${keys[j]} was ${d0} and is ${d1}: a translation preserves every distance`,
      );
    }
  }
  assertEqual(at(result, "quest", "b").x, 1000, "and the pin is exactly where it was dragged");
  assertEqual(at(result, "quest", "b").y, -500, "in both coordinates");
});

check("twoPinsAtOneComputedPointAreATranslationAndNotADivisionByZero", () => {
  // The other way to reach a zero denominator, and the one a tolerance
  // would paper over: two pins whose *computed* coordinates coincide.
  // The spread of the shape they are fitted from is zero, so there is no
  // scale to recover from them, and the fit says so rather than
  // returning a NaN and painting an empty canvas.
  const pairs = [
    { from: { x: 40, y: 40 }, to: { x: 0, y: 0 } },
    { from: { x: 40, y: 40 }, to: { x: 300, y: 300 } },
  ];
  const fit = fitTransform(pairs);
  assertEqual(fit.kind, FIT_TRANSLATION, "a translation");
  assertEqual(fit.scale, 1, "at unit scale");
  assert(Number.isFinite(fit.tx) && Number.isFinite(fit.ty), "with finite offsets, not NaNs");
  assertEqual(fit.pins, 2, "counted honestly as two pins even so");
});

check("collinearPinsStillDetermineAScale", () => {
  // The case a rotational fit could not answer and this one can: three
  // pins in a row is what a designer who has tidied a column produces,
  // and it is the common arrangement rather than a corner of one.
  const fit = fitTransform([
    { from: { x: 0, y: 0 }, to: { x: 0, y: 0 } },
    { from: { x: 0, y: 10 }, to: { x: 0, y: 20 } },
    { from: { x: 0, y: 20 }, to: { x: 0, y: 40 } },
  ]);
  assertEqual(fit.kind, FIT_SIMILARITY, "a similarity");
  assert(Math.abs(fit.scale - 2) < 1e-9, `the scale is 2: got ${fit.scale}`);
});

check("theFitDoesNotRotate", () => {
  // Pins whose best fit is a 90° rotation: the computed shape runs along
  // x and the designer's arrangement of the same three nodes runs along
  // y. A full similarity fit (Umeyama) would find that rotation and
  // would minimise the squared error better than anything below does —
  // which is exactly the point. A saved view is recognised by its shape,
  // and a diagram silently turned on its side is not the diagram anyone
  // saved.
  const computed = [
    { key: addressOf({ type: "q", key: "a" }), x: 0, y: 0, width: 10, height: 10 },
    { key: addressOf({ type: "q", key: "b" }), x: 100, y: 0, width: 10, height: 10 },
    { key: addressOf({ type: "q", key: "c" }), x: 200, y: 0, width: 10, height: 10 },
    // The witness: a node off the pinned axis, whose output says which
    // transform was applied. The three pins alone cannot, because they
    // are restored to their stored coordinates under every transform.
    { key: addressOf({ type: "q", key: "w" }), x: 100, y: 400, width: 10, height: 10 },
  ];
  const rows = [
    stored("q", "a", 0, 0),
    stored("q", "b", 0, 100),
    stored("q", "c", 0, 200),
  ];
  const result = compose(MODE_MIXED, computed, rows);
  const witness = at(result, "q", "w");

  // Where a 90° rotation would have put it, worked out rather than
  // guessed: the pins' computed centroid is (100, 0) and their stored
  // centroid is (0, 100), the rotation carrying (1, 0) to (0, 1) maps
  // the witness's offset (0, 400) to (-400, 0), and the witness lands at
  // (-400, 100). Worth stating because the first version of this
  // fixture named (-300, 100) — a point no transform produces — and so
  // held nothing at all: the assertion below it was doing all the work.
  assert(
    !(Math.abs(witness.x + 400) < 1 && Math.abs(witness.y - 100) < 1),
    `the witness landed at the rotated position (${witness.x}, ${witness.y})`,
  );
  // And it went where a translation puts it: the shape kept its
  // orientation, so the witness stayed below the pinned axis.
  assert(witness.y > 100, `the shape kept its orientation: witness y is ${witness.y}`);
  assertEqual(result.transform.kind, FIT_TRANSLATION, "the fit degenerated rather than rotating");
});

check("theFitNeverFlipsTheShape", () => {
  // The back door into the same defect. A *negative* uniform scale is a
  // 180° rotation composed with a reflection, so a fit that admitted one
  // would do by arithmetic exactly what "no rotation" forbids by rule —
  // and these pins ask for it: the designer's arrangement runs the
  // opposite way along the axis the computed shape does.
  const fit = fitTransform([
    { from: { x: 0, y: 0 }, to: { x: 0, y: 0 } },
    { from: { x: 100, y: 0 }, to: { x: -100, y: 0 } },
  ]);
  assertEqual(fit.scale, 1, "the scale is clamped to 1 rather than fitted to -1");
  assertEqual(fit.kind, FIT_TRANSLATION, "and it is reported as the translation it now is");
});

// --- mixed: the separation pass --------------------------------------

check("theSeparationPassRunsInEntityKeyOrder", () => {
  // Two unpinned nodes at the same point, plus one pin so that the mode
  // is really `mixed` and not its behaves-as-auto branch. The pass
  // resolves movers in address order against the boxes already fixed, so
  // the *first* by address keeps its coordinate and the second is the
  // one that moves. Run twice with the input in two different orders,
  // because a pass ordered by the caller's array would answer
  // differently the second time — and would still look correct once.
  const boxes = () => [
    { key: addressOf({ type: "quest", key: "aardvark" }), x: 0, y: 0, width: 100, height: 30 },
    { key: addressOf({ type: "quest", key: "zebra" }), x: 0, y: 0, width: 100, height: 30 },
    { key: addressOf({ type: "zone", key: "far" }), x: 900, y: 900, width: 100, height: 30 },
  ];
  for (const [order, computed] of [
    ["given", boxes()],
    ["reversed", boxes().reverse()],
    ["shuffled", shuffled(boxes())],
  ]) {
    const result = compose(MODE_MIXED, computed, [stored("zone", "far", 900, 900)]);
    const first = at(result, "quest", "aardvark");
    const second = at(result, "quest", "zebra");
    assertEqual(first.x, 0, `${order}: the first by address did not move`);
    assertEqual(first.y, 0, `${order}: in either coordinate`);
    // The axis of least displacement: these boxes are 100 wide and 30
    // tall, so clearing them costs 100 sideways and 30 downwards, and
    // the pass takes the cheap one. A pass that always pushed along x
    // would throw a node three times as far for the same result.
    assertEqual(second.x, 0, `${order}: the second by address moved on the cheap axis only`);
    assertEqual(second.y, 30, `${order}: by exactly the overlap`);
  }
});

check("theSeparationPassMovesNoPinnedNode", () => {
  // A pin sitting exactly where an unpinned node wants to be. The pass
  // is over the unpinned nodes *only*, so the pin is an obstacle and
  // never a mover — which is the whole of "pinned nodes never move" in
  // the one case where honouring it costs something.
  const computed = [
    { key: addressOf({ type: "quest", key: "mover" }), x: 0, y: 0, width: 100, height: 30 },
    { key: addressOf({ type: "quest", key: "pin" }), x: 0, y: 0, width: 100, height: 30 },
  ];
  const result = compose(MODE_MIXED, computed, [stored("quest", "pin", 0, 0)]);
  assertEqual(at(result, "quest", "pin").x, 0, "the pin stayed");
  assertEqual(at(result, "quest", "pin").y, 0, "in both coordinates");
  assertEqual(at(result, "quest", "mover").y, 30, "and the unpinned node is what moved");
});

check("theSeparationPassIsAReductionAndNotAGuarantee", async () => {
  // Stated in the code and asserted here, so that nobody reads a promise
  // into it later. Two pins with a narrow gap between them and one
  // unpinned node in that gap: clearing the first pin pushes the node
  // into the second, clearing the second pushes it straight back into
  // the first, and the pass — one pass, obstacles in order, no going
  // back — does not return to the pin it has already passed.
  //
  // If this fixture ever stops overlapping, the pass has changed shape,
  // and what must be re-read is compose.js's sentence rather than this
  // test: a settling loop is a simulation, which §5.2 refused when it
  // refused a force layout.
  const tall = (key, x) => ({ key: addressOf({ type: "q", key }), x, y: 0, width: 30, height: 100 });
  const result = compose(MODE_MIXED, [tall("left", 0), tall("mover", 10), tall("right", 40)], [
    stored("q", "left", 0, 0),
    stored("q", "right", 40, 0),
  ]);
  const mover = at(result, "q", "mover");
  const left = at(result, "q", "left");
  assert(
    Math.abs(mover.x - left.x) < 30 && Math.abs(mover.y - left.y) < 100,
    `the mover was expected to still overlap the left pin: it is at (${mover.x}, ${mover.y})`,
  );
  assertEqual(left.x, 0, "and both pins are exactly where they were dragged");
  assertEqual(at(result, "q", "right").x, 40, "both of them");
});

check("anUnpinnedStoredRowIsAStartingCoordinateInMixed", () => {
  // The one difference between an unpinned stored row and no row at all
  // in this mode, and the difference `manual` does not make.
  const computed = [
    { key: addressOf({ type: "q", key: "loose" }), x: 0, y: 0, width: 10, height: 10 },
    { key: addressOf({ type: "q", key: "pin" }), x: 500, y: 0, width: 10, height: 10 },
  ];
  const rows = [stored("q", "pin", 500, 0), stored("q", "loose", -777, 42, false)];
  const result = compose(MODE_MIXED, computed, rows);
  assertEqual(at(result, "q", "loose").x, -777, "it started from its stored coordinate");
  assertEqual(at(result, "q", "loose").y, 42, "in both coordinates");
  assertEqual(at(result, "q", "loose").pinned, false, "and it is still not pinned");
});

check("mixedIsDeterministicOnShuffledInput", () => {
  const { nodes, edges } = questGraph();
  const rows = [stored("quest", "hogger", -412.5, 88), stored("quest", "westfall", 733, 33.3)];
  const run = (ns, es, rs) => compose(MODE_MIXED, layoutGraph(ns, es), rs);
  const first = run(nodes, edges, rows);
  assertEqual(coordinatesOf(run(nodes, edges, rows)), coordinatesOf(first), "twice");
  assertEqual(
    coordinatesOf(run(shuffled(nodes), shuffled(edges), shuffled(rows))),
    coordinatesOf(first),
    "and with every array shuffled, stored rows included",
  );
});

// --- Reading the wire ------------------------------------------------

check("aStoredPositionIsReadInTheSpellingTheServerWrites", () => {
  // One spelling crosses the wire: internal/views.Position carries json
  // tags, so a run marshals what views.set_positions takes and
  // internal/web/static/client.js sends. This fixture is the write's
  // spelling and the run's because they are the same spelling;
  // internal/web/static_layout_test.go pins that against the real struct
  // rather than against this fixture, which cannot know what Go emits.
  const fromRun = storedFrom([
    { entity_type: "quest", entity_key: "hogger", x: 12.5, y: -40.25, pinned: true, updated_at: "2026-09-06T00:00:00Z" },
  ]);
  assertDeepEqual(
    fromRun,
    [{ key: addressOf({ type: "quest", key: "hogger" }), x: 12.5, y: -40.25, pinned: true }],
    "the envelope's spelling",
  );
  // And the Go field names the envelope used before the tags landed are
  // read by nothing now: a row in the old spelling is not half-read into
  // a position at the origin, it is not a position at all.
  assertEqual(
    storedFrom([{ EntityType: "quest", EntityKey: "hogger", X: 12.5, Y: -40.25, Pinned: true }]).length,
    0,
    "and the spelling that is gone reads as nothing",
  );

  // The default that matters: 0008_views.sql defaults `pinned` to true,
  // so a row that omits it is a node a human put somewhere and must not
  // become a node the engine may move.
  assertEqual(
    storedFrom([{ entity_type: "q", entity_key: "k", x: 0, y: 0 }])[0].pinned,
    true,
    "a row with no flag is pinned",
  );
  assertEqual(
    storedFrom([{ entity_type: "q", entity_key: "k", x: 0, y: 0, pinned: false }])[0].pinned,
    false,
    "and one written false is not",
  );
  // (0, 0) is a coordinate a designer may deliberately have chosen —
  // execute.go refuses to return unplaced as the origin for that exact
  // reason — so a row at the origin is a row.
  assertEqual(storedFrom([{ entity_type: "q", entity_key: "k", x: 0, y: 0 }]).length, 1, "the origin is a place");
  assertEqual(
    storedFrom([{ entity_type: "q", entity_key: "k", x: null, y: 0 }]).length,
    0,
    "and a non-finite coordinate is not",
  );
});

check("aStoredRowForAnEntityOutsideThePictureIsNotDrawn", () => {
  const { nodes, edges } = questGraph();
  const result = layoutView({
    mode: MODE_MIXED,
    nodes,
    edges,
    positions: [
      { entity_type: "quest", entity_key: "hogger", x: 400, y: 10 },
      { entity_type: "quest", entity_key: "deleted-last-week", x: 1, y: 1 },
    ],
  });
  assertEqual(result.placements.length, nodes.length, "one placement per node in the answer");
  assert(
    !JSON.stringify(result.placements).includes("deleted-last-week"),
    "and nothing on the canvas for an entity the query did not return",
  );
});

// --- The fallback grid -----------------------------------------------

check("theGridIsOrderedByTypeThenKey", () => {
  const nodes = shuffled([
    node("zone", "elwynn"),
    node("quest", "westfall"),
    node("quest", "defias"),
    node("zone", "duskwood"),
    node("quest", "hogger"),
  ]);
  const grid = gridFallback(nodes);
  assertDeepEqual(
    grid.map((p) => JSON.parse(p.key)),
    [
      ["quest", "defias"],
      ["quest", "hogger"],
      ["quest", "westfall"],
      ["zone", "duskwood"],
      ["zone", "elwynn"],
    ],
    "type then key, and the caller's order ignored",
  );
  // A grid, not a pile: the first row runs along x and the next row is
  // below it.
  assert(grid[1].x > grid[0].x, "the second cell is to the right of the first");
  assert(grid[3].y > grid[0].y, "and the second row is below the first");
  assertEqual(grid[0].source, SOURCE_GRID, "every cell says what it is");
  assertEqual(grid[0].pinned, false, "and none of them is a designer's coordinate");
  assertDeepEqual(gridFallback(shuffled(nodes)), grid, "and the grid is deterministic");
});

// --- The budget ------------------------------------------------------

// A clock whose only job is to make two seconds cost nothing. Same shape
// as internal/web/jstest/client_test.mjs's, and injected the same way.
function fakeClock() {
  let at = 0;
  let next = 1;
  const timers = new Map();
  return {
    now: () => at,
    setTimer: (fn, ms) => {
      const id = next++;
      timers.set(id, { at: at + ms, fn });
      return id;
    },
    clearTimer: (id) => timers.delete(id),
    pendingCount: () => timers.size,
    async advance(ms) {
      const target = at + ms;
      for (;;) {
        let due = null;
        for (const [id, timer] of timers) {
          if (timer.at <= target && (due === null || timer.at < due.timer.at)) due = { id, timer };
        }
        if (due === null) break;
        timers.delete(due.id);
        at = due.timer.at;
        due.timer.fn();
        await Promise.resolve();
      }
      at = target;
      await Promise.resolve();
    },
  };
}

check("theBudgetTerminatesAndTheGridIsAnnounced", async () => {
  const { nodes } = questGraph();
  const clock = fakeClock();
  let terminated = 0;
  const attempt = runWithBudget({
    // An engine that never answers. Not a slow one: this is the case
    // where terminating is the only thing that ends it.
    run: new Promise(() => {}),
    budgetMs: LAYOUT_BUDGET_MS,
    fallback: () => gridFallback(nodes),
    cancel: () => {
      terminated++;
    },
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
    now: clock.now,
  });
  await clock.advance(LAYOUT_BUDGET_MS);
  const result = await attempt;

  assertEqual(result.ok, false, "the attempt gave up");
  assertEqual(terminated, 1, "and the worker was terminated exactly once");
  assertEqual(result.banner.code, BANNER_LAYOUT_BUDGET, "the band names itself");
  // Computed from the constant, never quoted. This is the assertion the
  // plan asks to break by hard-coding the sentence: with the text
  // written out, changing LAYOUT_BUDGET_MS leaves the prose behind and
  // this line stays green, which is the drift the generated sentence
  // exists to make impossible.
  assertEqual(result.banner.text, budgetSentence(LAYOUT_BUDGET_MS), "the sentence is the number's");
  // Spelled from the constant, not from a reading of it: hard-coding
  // "2 seconds" *and* retuning the constant would leave the equality
  // above green, because both sides would be the same frozen string.
  assert(
    result.banner.text.includes(`${LAYOUT_BUDGET_MS / 1000} seconds`),
    "and it names the budget it actually enforced",
  );
  assert(
    result.banner.text.includes("grid"),
    "a grid presented without explanation reads as the graph having no structure",
  );
  assertDeepEqual(result.layout, gridFallback(nodes), "and the arrangement is that grid");
  assertEqual(clock.pendingCount(), 0, "no timer was left behind");
});

check("theRetryUsesTheRetryConstantAndDoesNotEscalateTwice", async () => {
  const clock = fakeClock();
  const give_up = async (budgetMs) => {
    const attempt = runWithBudget({
      run: new Promise(() => {}),
      budgetMs,
      fallback: () => [],
      setTimer: clock.setTimer,
      clearTimer: clock.clearTimer,
      now: clock.now,
    });
    await clock.advance(budgetMs);
    return attempt;
  };

  const first = await give_up(LAYOUT_BUDGET_MS);
  assertEqual(first.retry.code, ACTION_RETRY_LAYOUT, "the first failure offers a retry");
  assertEqual(first.retry.budgetMs, LAYOUT_RETRY_MS, "at the retry constant, not at a multiple");
  assertEqual(first.banner.text, budgetSentence(LAYOUT_BUDGET_MS), "and says the budget that failed");

  const second = await give_up(LAYOUT_RETRY_MS);
  assertEqual(second.retry, null, "the second failure offers nothing: there is no third rung");
  assertEqual(second.banner.text, budgetSentence(LAYOUT_RETRY_MS), "and its sentence names ten seconds");
  assertEqual(nextBudgetMs(LAYOUT_RETRY_MS), null, "the rule itself, stated once");
  assertEqual(nextBudgetMs(LAYOUT_BUDGET_MS), LAYOUT_RETRY_MS, "in both directions");
});

check("aLayoutInsideTheBudgetKeepsItsAnswerAndCancelsNothing", async () => {
  const { nodes, edges } = questGraph();
  const clock = fakeClock();
  let terminated = 0;
  const result = await runWithBudget({
    run: () => layoutView({ mode: MODE_AUTO, nodes, edges }),
    budgetMs: LAYOUT_BUDGET_MS,
    fallback: () => gridFallback(nodes),
    cancel: () => {
      terminated++;
    },
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
    now: clock.now,
  });
  assertEqual(result.ok, true, "it finished");
  assertEqual(terminated, 0, "nothing was terminated");
  assertEqual(result.banner, null, "and nothing was announced");
  assertEqual(result.layout.placements.length, nodes.length, "the real arrangement came back");
  assertEqual(clock.pendingCount(), 0, "and the deadline timer was cleared, not merely ignored");
});

check("layoutElapsedIsReportedToTheFrame", async () => {
  // Spec §5.4: the engine's elapsed time sits in the footer beside
  // `duration_ms`, so "the query is slow" and "the picture is slow" are
  // two answers. This is the join between the module that measures it
  // and the model that says it.
  const clock = fakeClock();
  const attempt = runWithBudget({
    run: new Promise((resolve) => clock.setTimer(() => resolve([]), 640)),
    budgetMs: LAYOUT_BUDGET_MS,
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
    now: clock.now,
  });
  await clock.advance(640);
  const result = await attempt;
  assertEqual(result.ok, true, "it finished inside the budget");
  assertEqual(result.elapsedMs, 640, "and it measured how long it took");

  const footer = footerFor({ stats: { nodes: 6, edges: 6, max_depth_reached: 2, duration_ms: 31 } }, {
    layoutMs: result.elapsedMs,
  });
  assertEqual(footer.layoutMs, 640, "the frame's footer carries it");
  assert(footer.text.includes("query 31 ms"), "beside the query's own number");
  assert(footer.text.includes("layout 640 ms"), "and not instead of it");
});

// --- Re-runs ---------------------------------------------------------

check("aRerunKeepsRetainedCoordinates", () => {
  const previous = [];
  const nodes = [];
  for (let i = 0; i < 40; i++) {
    const n = node("quest", `q${String(i).padStart(3, "0")}`);
    nodes.push(n);
    previous.push({ key: addressOf(n), x: i * 130, y: (i % 5) * 90, pinned: false, source: SOURCE_COMPUTED });
  }
  const grown = nodes.slice();
  for (let i = 0; i < 12; i++) grown.push(node("quest", `new${String(i).padStart(3, "0")}`));

  const plan = rerunPlan(previous, grown);
  assertEqual(plan.full, false, "40 of 52 retained is not a re-arrange");
  assertEqual(plan.reason, "incremental", "and it says so");
  assertEqual(plan.retained.length, 40, "the forty are retained");
  assertEqual(plan.fresh.length, 12, "and the twelve are fresh");

  const result = layoutView({ mode: MODE_MIXED, nodes: grown, edges: [], previous });
  const after = keyed(result.placements);
  for (const before of previous) {
    const now = after.get(before.key);
    assert(now, `${before.key} is still drawn`);
    assertEqual(now.x, before.x, `${before.key} kept its x exactly`);
    assertEqual(now.y, before.y, `${before.key} kept its y exactly`);
  }
  assertEqual(result.placements.length, 52, "and the twelve arrived");
  assertEqual(result.placedAutomatically, 12, "counted as the twelve the frame will announce");
  assertEqual(result.rerun, "incremental", "the plan is reported with the answer");
  assertEqual(result.mode, MODE_MIXED, "and the view's own mode survives the re-run path");
});

check("aMostlyNewResultRelaysOutFully", () => {
  const previous = [];
  const kept = [];
  for (let i = 0; i < 10; i++) {
    const n = node("quest", `q${i}`);
    kept.push(n);
    previous.push({ key: addressOf(n), x: i * 130, y: 0, pinned: false, source: SOURCE_COMPUTED });
  }
  const grown = kept.slice();
  for (let i = 0; i < 30; i++) grown.push(node("quest", `new${i}`));

  const plan = rerunPlan(previous, grown);
  assertEqual(plan.full, true, "10 retained of 40 is under half");
  assertEqual(plan.reason, "mostly_new", "and it says why");
  assertEqual(plan.retained.length, 0, "a full re-layout retains nothing");

  // Exactly half is not under half: the boundary is a rule, not an
  // accident of a comparison.
  const half = rerunPlan(previous, kept.concat(Array.from({ length: 10 }, (_, i) => node("quest", `half${i}`))));
  assertEqual(half.full, false, "10 retained of 20 is still incremental");

  // And an explicit re-arrange overrides all of it.
  assertEqual(rerunPlan(previous, grown.slice(0, 12), { rearrange: true }).full, true, "re-arrange");
  assertEqual(rerunPlan(previous, grown.slice(0, 12), { rearrange: true }).reason, "rearrange", "by name");

  const result = layoutView({ mode: MODE_AUTO, nodes: grown, edges: [], previous });
  const after = keyed(result.placements);
  const moved = previous.filter((before) => {
    const now = after.get(before.key);
    return !now || now.x !== before.x || now.y !== before.y;
  });
  assert(moved.length > 0, "a full re-layout really re-laid the retained nodes out");
  assertEqual(result.rerun, "mostly_new", "and reported it");
});

check("aRerunInAutoStaysUndraggable", () => {
  // The rule carried one step along: the incremental path is a `mixed`
  // composition internally, and a view in `auto` must not come back from
  // it draggable.
  const nodes = [node("quest", "a"), node("quest", "b")];
  const previous = nodes.map((n, i) => ({ key: addressOf(n), x: i * 300, y: 0 }));
  const result = layoutView({ mode: MODE_AUTO, nodes: [...nodes, node("quest", "c")], edges: [], previous });
  assertEqual(result.rerun, "incremental", "it took the incremental path");
  assertEqual(result.mode, MODE_AUTO, "the mode is the view's");
  assertEqual(result.draggable, false, "and a drag is still refused");
});

check("aRerunHonoursAStoredPositionForANodeThatIsNewToThePicture", () => {
  // A node that was dragged, filtered out by a parameter change, and has
  // now come back. It is "fresh" to the re-run and stored to the view,
  // and the arrangement it is drawn in must be the designer's.
  const nodes = [node("quest", "a"), node("quest", "b")];
  const previous = nodes.map((n, i) => ({ key: addressOf(n), x: i * 300, y: 0 }));
  const result = layoutView({
    mode: MODE_MIXED,
    nodes: [...nodes, node("quest", "returning")],
    edges: [],
    previous,
    positions: [{ entity_type: "quest", entity_key: "returning", x: -900, y: 250 }],
  });
  assertEqual(at(result, "quest", "returning").x, -900, "its stored x");
  assertEqual(at(result, "quest", "returning").y, 250, "its stored y");
  assertEqual(at(result, "quest", "returning").source, SOURCE_STORED, "and it is not an automatic placement");
  assertEqual(result.placedAutomatically, 0, "so nothing is announced as newly placed");
});

check("layoutViewIsDeterministicEndToEnd", () => {
  const { nodes, edges } = questGraph();
  const request = {
    mode: MODE_MIXED,
    nodes,
    edges,
    positions: [{ entity_type: "quest", entity_key: "hogger", x: 400, y: 10 }],
  };
  const first = layoutView(request);
  assertEqual(coordinatesOf(layoutView(request)), coordinatesOf(first), "twice");
  assertEqual(
    coordinatesOf(layoutView({ ...request, nodes: shuffled(nodes), edges: shuffled(edges) })),
    coordinatesOf(first),
    "and with the envelope's arrays in another order",
  );
});

// --- Run -------------------------------------------------------------

function distance(a, b) {
  return Math.hypot(a.x - b.x, a.y - b.y);
}

for (const [name, fn] of pending) {
  try {
    await fn();
    console.log("ok   " + name);
  } catch (err) {
    failures++;
    console.error("FAIL " + name + ": " + (err && err.message ? err.message : err));
  }
}

if (failures > 0) {
  console.error(`${failures} layout check(s) failed`);
  process.exit(1);
}
console.log("layout: all checks passed");
