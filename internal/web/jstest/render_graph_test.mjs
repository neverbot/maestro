// The harness for the `graph` renderer:
// internal/web/static/render/graph.js and the drawing vocabulary it
// shares with the five renderers that follow it
// (internal/web/static/render/marks.js and render/controls.js).
//
// What this layer covers that no Go test can.
//
// **That the picture and the text twin describe the same answer.** Task
// 5 built the twin before any renderer precisely so that each renderer
// task could assert this, and a picture and a twin that disagree is the
// defect that ordering exists to catch. `theTwinAndTheSceneAgreeOn
// NodeCount` joins the two over one envelope.
//
// **That an absence is marked as many times as it happened.** A node
// missing its colour slot is unfilled *and* dashed; a node missing its
// size slot takes the smallest box; a node missing both gets both, and
// the check that says so is the one that would fail if some future
// "unknown" treatment collapsed two facts into one.
//
// **That a size is an area.** Getting this wrong is invisible — the
// picture looks fine and every comparison in it is wrong by a square —
// so the arithmetic is asserted numerically, in both halves: the mapping
// itself, and the boxes a fixture comes out with.
//
// **That `group_by` draws and `cluster_by` does not.** They are the pair
// a designer trips over, so "draws nothing" is asserted as *no mark
// attributable to it* and, in the layout harness, as *the arrangement
// moved* — because "draws nothing" must not decay into "does nothing".
//
// This harness needs no DOM: a scene is plain data. It is
// internal/web/jstest/canvas_test.mjs that turns marks into elements,
// and the two meet at render/scene.js's contract.
//
// Run directly: `node internal/web/jstest/render_graph_test.mjs`.
// internal/web/static_appjs_browser_test.go shells out to it too.

import { addressOf } from "../static/address.js";
import { UNSET_LABEL, legendFor } from "../static/palette.js";
import { footerFor, joinEdges } from "../static/render/scene.js";
import { twinFor } from "../static/render/twin.js";
import { layoutGraph } from "../static/layout/engine.js";
import { controlNamed } from "../static/render/controls.js";
import {
  ABSENT_DASH,
  CLASS_AMBIGUOUS,
  CLASS_ARROW,
  CLASS_EDGE,
  CLASS_EDGE_LABEL,
  CLASS_EDGE_PLATE,
  CLASS_ENCLOSURE,
  CLASS_ENCLOSURE_HEADING,
  CLASS_NODE,
  CLASS_NODE_ABSENT,
  CLASS_NODE_LABEL,
  CLASS_STUB,
  CLASS_STUB_RING,
  NODE_PLAIN_FILL,
  SIZE_AREA_MAX,
  SIZE_AREA_MIN,
  UNFILLED,
  areaScaleFor,
  boxFor,
  lengthScaleForArea,
} from "../static/render/marks.js";
import {
  CONTROLS,
  PARAM_CLUSTER_BY,
  PARAM_GROUP_BY,
  RENDERER,
  graphLayoutRequest,
  graphScene,
} from "../static/render/graph.js";

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

function assertClose(actual, expected, message) {
  if (!(Math.abs(actual - expected) < 1e-9)) {
    throw new Error(`${message}: got ${actual}, want ${expected}`);
  }
}

function assertDeepEqual(actual, expected, message) {
  const a = JSON.stringify(actual);
  const b = JSON.stringify(expected);
  if (a !== b) throw new Error(`${message}: got ${a}, want ${b}`);
}

// --- Fixtures --------------------------------------------------------

// node builds one envelope-shaped node. `attrs` is passed through
// untouched, so a fixture can leave a slot **absent** — which is the
// distinction half of this file is about — by simply not writing it.
function node(key, attrs, extra = {}) {
  return { id: `id-${key}`, type: "quest", key, name: `Quest ${key}`, attrs, ...extra };
}

function edge(source, target, extra = {}) {
  return { id: `e-${source}-${target}`, type: "requires", source, target, ...extra };
}

function envelopeOf(nodes, edges = []) {
  return {
    nodes,
    edges,
    stats: { nodes: nodes.length, edges: edges.length, max_depth_reached: 1, duration_ms: 3 },
    truncated: { nodes: false, edges: false, depth: false },
    stale: [],
  };
}

// laidOut runs the real layout engine over the renderer's own request,
// so every scene below is drawn on coordinates the product would
// actually produce. A hand-written placement list would let a fixture
// choose an arrangement that made a geometric claim true.
function laidOut(envelope, params = {}) {
  const request = graphLayoutRequest(envelope, params);
  return layoutGraph(request.nodes, request.edges);
}

function scene(envelope, params = {}) {
  return graphScene(envelope, laidOut(envelope, params), params);
}

function marksOfClass(result, className) {
  return result.marks.filter((mark) => mark.class === className);
}

function boxOf(result, key) {
  const address = addressOf({ type: "quest", key });
  const found = result.marks.find(
    (mark) => mark.key === address && (mark.class === CLASS_NODE || mark.class === CLASS_NODE_ABSENT),
  );
  if (!found) throw new Error(`no box drawn for quest/${key}`);
  return found;
}

function areaOf(mark) {
  return mark.w * mark.h;
}

// --- The two absences ------------------------------------------------

check("anAbsentColourSlotIsDashedAndUnfilled", () => {
  const envelope = envelopeOf([
    node("a", { label: "Alpha", zone: "elwynn" }),
    node("b", { label: "Beta" }),
  ]);
  const result = scene(envelope, { color_by: "zone" });

  const absent = boxOf(result, "b");
  assertEqual(absent.fill, UNFILLED, "a node whose colour slot found nothing carries no fill");
  assertEqual(absent.dash, ABSENT_DASH, "and is dashed, which is the second carrier");
  assertEqual(absent.class, CLASS_NODE_ABSENT, "and says so in its class as well as its stroke");

  // The control: the node that *has* a value is filled and solid, so a
  // renderer that dashed everything fails here.
  const filled = boxOf(result, "a");
  assert(filled.fill !== UNFILLED, "a node with a value is filled");
  assertEqual(filled.dash, undefined, "and is not dashed");

  // And the legend names the absence in words, because colour — or the
  // lack of it — is never the only carrier.
  const unset = result.legend.rows.filter((row) => row.kind === "unset");
  assertEqual(unset.length, 1, "the legend carries exactly one unset row");
  assertEqual(unset[0].label, UNSET_LABEL, "with the palette's own words");
  assertEqual(unset[0].count, 1, "counting the node that has no value");
});

check("noColourSlotIsNotAnAbsentOne", () => {
  // The third answer, and the one a two-way implementation gets wrong:
  // "this view colours nothing" is not "this node's value is missing".
  const envelope = envelopeOf([node("a", { label: "Alpha" })]);
  const result = scene(envelope, {});
  const box = boxOf(result, "a");
  assertEqual(box.fill, NODE_PLAIN_FILL, "an uncoloured view paints paper");
  assertEqual(box.dash, undefined, "and reports no absence");
  assertEqual(result.legend, null, "and has no legend at all, which is not an empty one");
});

check("anAbsentSizeSlotTakesTheRangeMinimum", () => {
  const envelope = envelopeOf([
    node("a", { label: "Same", weight: 10 }),
    node("b", { label: "Same", weight: 50 }),
    node("c", { label: "Same" }),
  ]);
  const result = scene(envelope, { size_by: "weight" });

  const smallest = boxOf(result, "a");
  const absent = boxOf(result, "c");
  // The labels are the same length on purpose: a box is measured from
  // its label, so two nodes with different names would differ in size
  // for a reason that has nothing to do with size_by.
  assertClose(areaOf(absent), areaOf(smallest), "an absent size takes the range minimum exactly");
  assert(areaOf(absent) > 0, "and is the smallest box, not no box");
});

check("twoAbsencesAreTwoMarks", () => {
  // A single "unknown" treatment would collapse two facts into one, and
  // a designer would learn nothing from either.
  const envelope = envelopeOf([
    node("a", { label: "Same", zone: "elwynn", weight: 10 }),
    node("b", { label: "Same", zone: "elwynn", weight: 90 }),
    node("c", { label: "Same" }),
  ]);
  const result = scene(envelope, { color_by: "zone", size_by: "weight" });

  const both = boxOf(result, "c");
  assertEqual(both.fill, UNFILLED, "the colour absence is unfilled");
  assertEqual(both.dash, ABSENT_DASH, "and dashed");
  assertClose(
    areaOf(both),
    areaOf(boxOf(result, "a")),
    "and the size absence is the minimum, independently",
  );
  assert(
    areaOf(boxOf(result, "b")) > areaOf(both),
    "with a node that has a size to prove the scale is running",
  );
});

// --- Size is an area -------------------------------------------------

check("sizeMapsAreaNotDiameter", () => {
  // The arithmetic, first, in the one line it lives on: an area four
  // times another is twice the width and twice the height. Getting this
  // wrong is invisible in a picture and wrong everywhere in it.
  assertClose(lengthScaleForArea(4), 2, "an area of 4x is a length of 2x");
  assertClose(lengthScaleForArea(9), 3, "and an area of 9x is a length of 3x");
  assertClose(lengthScaleForArea(SIZE_AREA_MIN), 1, "and the minimum area is the authored box");

  // And in a real scene: the node at the top of the range has three
  // times the *area* of the one at the bottom, which is sqrt(3) times
  // the width — not three times, which is what a renderer that scaled
  // the length by the value would produce.
  const envelope = envelopeOf([
    node("a", { label: "Same", weight: 1 }),
    node("b", { label: "Same", weight: 4 }),
  ]);
  const result = scene(envelope, { size_by: "weight" });
  const small = boxOf(result, "a");
  const large = boxOf(result, "b");
  assertClose(large.w / small.w, Math.sqrt(SIZE_AREA_MAX), "the width grows as the area's root");
  assertClose(large.h / small.h, Math.sqrt(SIZE_AREA_MAX), "and so does the height");
  assertClose(areaOf(large) / areaOf(small), SIZE_AREA_MAX, "so the area ratio is the mapped one");
});

check("sizeIsBoundedAtThreeTimes", () => {
  // An unbounded map makes one boss the size of the canvas. A million
  // is three times the smallest box and not a million times it.
  const envelope = envelopeOf([
    node("a", { label: "Same", weight: 1 }),
    node("b", { label: "Same", weight: 1000000 }),
  ]);
  const result = scene(envelope, { size_by: "weight" });
  assertClose(
    areaOf(boxOf(result, "b")) / areaOf(boxOf(result, "a")),
    SIZE_AREA_MAX,
    "the area ratio is exactly the bound",
  );

  // The mapping itself, at both ends and outside them, because a clamp
  // that only worked on the way up would pass the assertion above.
  const domain = { min: 1, max: 1000000 };
  assertClose(areaScaleFor(1, domain), SIZE_AREA_MIN, "the smallest value is the minimum");
  assertClose(areaScaleFor(1000000, domain), SIZE_AREA_MAX, "the largest is the maximum");
  assertClose(areaScaleFor(-5, domain), SIZE_AREA_MIN, "and a value under the domain clamps");
  assertClose(areaScaleFor(1e12, domain), SIZE_AREA_MAX, "as does one over it");
});

check("oneValueIsNoRangeAndEverybodyTakesTheMinimum", () => {
  // Every node carrying the same number is an answer that said nothing
  // about size. Drawing every box at the top of the range would say
  // "these are all big" about it.
  const envelope = envelopeOf([
    node("a", { label: "Same", weight: 7 }),
    node("b", { label: "Same", weight: 7 }),
  ]);
  const result = scene(envelope, { size_by: "weight" });
  const authored = boxFor("Same");
  assertClose(areaOf(boxOf(result, "a")), authored.width * authored.height, "the box is the authored one, unscaled");
  assertClose(areaOf(boxOf(result, "b")), authored.width * authored.height, "and so is the other");
});

check("aNonNumericSizeValueIsNotASize", () => {
  // A slot holding text is not a size and takes the minimum, the same
  // answer an absent slot gets — the fact being reported is "no number
  // here" in both cases.
  const envelope = envelopeOf([
    node("a", { label: "Same", weight: 1 }),
    node("b", { label: "Same", weight: 9 }),
    node("c", { label: "Same", weight: "heavy" }),
  ]);
  const result = scene(envelope, { size_by: "weight" });
  assertClose(
    areaOf(boxOf(result, "c")),
    areaOf(boxOf(result, "a")),
    "a string in a size slot takes the minimum",
  );
});

// --- Ambiguity -------------------------------------------------------

check("anAmbiguousNodeCarriesItsMarkAtTheCorner", () => {
  const envelope = envelopeOf([
    node("a", { label: "Alpha", zone: "elwynn" }, { ambiguous: true }),
    node("b", { label: "Beta", zone: "elwynn" }),
  ]);
  const result = scene(envelope, { color_by: "zone" });

  const marks = marksOfClass(result, CLASS_AMBIGUOUS);
  assertEqual(marks.length, 1, "one mark for one ambiguous node");
  assertEqual(marks[0].key, addressOf({ type: "quest", key: "a" }), "on the node that is flagged");

  // At the corner of that node's box, which is what makes it readable as
  // a mark *on* the node rather than as a decoration nearby.
  const box = boxOf(result, "a");
  assert(
    marks[0].cx > box.x + box.w / 2 - 10 && marks[0].cx <= box.x + box.w,
    "at the box's right edge",
  );
  assert(marks[0].cy >= box.y && marks[0].cy < box.y + 10, "and its top edge");

  // And nowhere is there a mark naming a *slot*: execute.go says the
  // flag is on the node and that which slot was ambiguous is
  // deliberately not carried, so a per-slot mark would be this interface
  // inventing a distinction the server declined to make.
  const perSlot = result.marks.filter(
    (mark) => typeof mark.slot === "string" || (typeof mark.class === "string" && mark.class.includes("slot")),
  );
  assertDeepEqual(perSlot, [], "no mark is attributed to a slot");
});

// --- Grouping draws; clustering does not -----------------------------

check("groupByDrawsAHairlineEnclosureWithAHeading", () => {
  const envelope = envelopeOf([
    node("a", { label: "Alpha", zone: "elwynn" }),
    node("b", { label: "Beta", zone: "elwynn" }),
    node("c", { label: "Gamma", zone: "duskwood" }),
  ]);
  const result = scene(envelope, { group_by: "zone" });

  const boxes = marksOfClass(result, CLASS_ENCLOSURE);
  assertEqual(boxes.length, 2, "one enclosure per value");
  assertEqual(boxes[0].fill, UNFILLED, "a hairline enclosure is not a filled panel");
  assert(boxes[0].strokeWidth <= 1, "and its rule is a hairline");

  const headings = marksOfClass(result, CLASS_ENCLOSURE_HEADING).map((mark) => mark.text).sort();
  assertDeepEqual(headings, ["duskwood", "elwynn"], "each enclosure is captioned with its value");

  // The enclosure contains its members: a box drawn around two nodes
  // that does not contain them is a box about nothing.
  const elwynn = boxes.find((mark) => mark.x < 1e9 && mark.w > 0 && mark.h > 0);
  assert(elwynn !== undefined, "the enclosure has a rectangle");
  for (const key of ["a", "b"]) {
    const member = boxOf(result, key);
    const inside = boxes.some(
      (encl) =>
        member.x >= encl.x &&
        member.y >= encl.y &&
        member.x + member.w <= encl.x + encl.w &&
        member.y + member.h <= encl.y + encl.h,
    );
    assert(inside, `quest/${key} is inside an enclosure`);
  }

  assertDeepEqual(
    result.groups.map((group) => [group.heading, group.count]),
    [["duskwood", 1], ["elwynn", 2]],
    "and the scene reports the groups it drew",
  );
});

check("aNodeWithNoGroupValueIsInNoEnclosure", () => {
  // An enclosure around "the ones we know nothing about" is a claim they
  // belong together, which is exactly what an absence does not say.
  const envelope = envelopeOf([
    node("a", { label: "Alpha", zone: "elwynn" }),
    node("b", { label: "Beta" }),
  ]);
  const result = scene(envelope, { group_by: "zone" });
  assertEqual(marksOfClass(result, CLASS_ENCLOSURE).length, 1, "one enclosure, for the one value");
  assertDeepEqual(
    marksOfClass(result, CLASS_ENCLOSURE_HEADING).map((mark) => mark.text),
    ["elwynn"],
    "and no enclosure named for an absence",
  );
});

check("clusterByDrawsNothing", () => {
  const nodes = [
    node("a", { label: "Alpha", zone: "elwynn" }),
    node("b", { label: "Beta", zone: "elwynn" }),
    node("c", { label: "Gamma", zone: "duskwood" }),
  ];
  const envelope = envelopeOf(nodes);

  const clustered = scene(envelope, { cluster_by: "zone" });
  const plain = scene(envelope, {});

  // No mark is attributable to it: not an enclosure, not a heading, not
  // a legend row, and not a class of its own.
  assertEqual(marksOfClass(clustered, CLASS_ENCLOSURE).length, 0, "clustering draws no enclosure");
  assertEqual(marksOfClass(clustered, CLASS_ENCLOSURE_HEADING).length, 0, "and no heading");
  assertEqual(clustered.legend, null, "and no legend");
  assertDeepEqual(clustered.groups, [], "and reports no groups");
  assertEqual(
    clustered.marks.length,
    plain.marks.length,
    "and the scene has exactly the marks an unclustered one has",
  );

  // But it is not nothing: the layout request carries it, which is where
  // its whole effect is. Without this assertion "draws nothing" would
  // decay into "does nothing" and nobody would notice.
  const request = graphLayoutRequest(envelope, { cluster_by: "zone" });
  assertDeepEqual(
    request.nodes.map((entry) => entry.cluster),
    ['"elwynn"', '"elwynn"', '"duskwood"'],
    "the layout is told which nodes cluster together",
  );

  // And `group_by` does the opposite, in the same two places: it draws,
  // and it reaches the layout with nothing.
  const grouped = graphLayoutRequest(envelope, { group_by: "zone" });
  assertDeepEqual(
    grouped.nodes.map((entry) => entry.cluster),
    ["", "", ""],
    "grouping tells the layout nothing",
  );
});

check("aNodeWithNoClusterValueIsInNoCluster", () => {
  const envelope = envelopeOf([
    node("a", { label: "Alpha", zone: "elwynn" }),
    node("b", { label: "Beta" }),
  ]);
  const request = graphLayoutRequest(envelope, { cluster_by: "zone" });
  assertDeepEqual(
    request.nodes.map((entry) => entry.cluster),
    ['"elwynn"', ""],
    "an absent cluster value is no cluster, not a cluster of absences",
  );
});

check("theTooltipsTeachTheDifferenceBetweenGroupingAndClustering", () => {
  // These two sentences are the only place in the product a designer
  // learns why two controls that take the same kind of value do
  // different things. They are load-bearing, so they are asserted.
  const group = controlNamed(CONTROLS, PARAM_GROUP_BY);
  const cluster = controlNamed(CONTROLS, PARAM_CLUSTER_BY);
  assert(group !== null && cluster !== null, "both controls exist");

  assert(group.tooltip.includes("enclosure"), "grouping's tooltip says it draws an enclosure");
  assert(
    group.tooltip.includes("not") && group.tooltip.includes("where nodes go"),
    "and that it does not move anything",
  );
  assert(cluster.tooltip.startsWith("Draws nothing."), "clustering's tooltip opens by saying so");
  assert(
    cluster.tooltip.includes("where nodes go") && cluster.tooltip.includes("not"),
    "and says the layout is the whole of what it changes",
  );
  assert(
    group.tooltip !== cluster.tooltip,
    "and the two controls do not share one sentence",
  );

  // Every control has one, because a knob with no tooltip is a knob a
  // designer has to guess at, and the sixth one added later is the one
  // that would go without.
  assertEqual(CONTROLS.length, 6, "the catalogue's six parameters are six controls");
  for (const entry of CONTROLS) {
    assert(entry.tooltip.length > 20, `${entry.name} has a sentence`);
    assert(entry.tooltip.trim().endsWith("."), `${entry.name}'s tooltip is a sentence`);
  }
  assertEqual(RENDERER, "graph", "and this is the renderer the catalogue names");
});

// --- Edges, arrows, labels, stubs ------------------------------------

check("arrowsDrawAHeadAtTheTargetAndRideIt", () => {
  const envelope = envelopeOf(
    [node("a", { label: "Alpha" }), node("b", { label: "Beta" })],
    [edge("id-a", "id-b")],
  );
  const withArrows = scene(envelope, { arrows: true });
  const without = scene(envelope, {});

  assertEqual(marksOfClass(without, CLASS_ARROW).length, 0, "no arrows unless asked for");
  const heads = marksOfClass(withArrows, CLASS_ARROW);
  assertEqual(heads.length, 2, "a head is two lines");

  const target = addressOf({ type: "quest", key: "b" });
  for (const head of heads) {
    assertEqual(head.source, target, "both of the head's endpoint keys are the target's");
    assertEqual(head.target, target, "so it rides that node's transform whole during a drag");
  }

  // The head's tip is at the target end of the line, not the source's.
  const line = marksOfClass(withArrows, CLASS_EDGE)[0];
  for (const head of heads) {
    assertClose(head.x1, line.x2, "the tip sits on the line's target end");
    assertClose(head.y1, line.y2, "in both coordinates");
  }
});

check("edgeLabelsDrawOnAPlateAndOnlyWhenAsked", () => {
  const envelope = envelopeOf(
    [node("a", { label: "Alpha" }), node("b", { label: "Beta" })],
    [edge("id-a", "id-b", { label: "requires" })],
  );
  const labelled = scene(envelope, { edge_labels: true });
  assertEqual(marksOfClass(labelled, CLASS_EDGE_LABEL).length, 1, "one label per labelled edge");
  assertEqual(marksOfClass(labelled, CLASS_EDGE_PLATE).length, 1, "on one plate");
  assertEqual(marksOfClass(labelled, CLASS_EDGE_LABEL)[0].text, "requires", "carrying the edge's own text");

  const plain = scene(envelope, {});
  assertEqual(marksOfClass(plain, CLASS_EDGE_LABEL).length, 0, "and nothing when the knob is off");
});

check("edgeLabelsWithoutLabelledEdgesCannotHappen", () => {
  // internal/views/renderers.go refuses `edge_labels: true` over a query
  // whose edges[] declares no label_from, at save time, with a pointer —
  // so this client never meets the combination. It is asserted anyway,
  // because "the server refuses it" is a claim about another process:
  // the renderer draws the edge and no label, rather than an empty plate
  // or a crash.
  const envelope = envelopeOf(
    [node("a", { label: "Alpha" }), node("b", { label: "Beta" })],
    [edge("id-a", "id-b")],
  );
  const result = scene(envelope, { edge_labels: true });
  assertEqual(marksOfClass(result, CLASS_EDGE).length, 1, "the edge is still drawn");
  assertEqual(marksOfClass(result, CLASS_EDGE_LABEL).length, 0, "with no label");
  assertEqual(marksOfClass(result, CLASS_EDGE_PLATE).length, 0, "and no empty plate under one");

  // An edge whose label is the empty string is the same case: the wire's
  // `label` is omitempty, so an edge nobody asked a label of and an edge
  // labelled "" arrive alike and neither gets a plate.
  const empty = envelopeOf(
    [node("a", { label: "Alpha" }), node("b", { label: "Beta" })],
    [edge("id-a", "id-b", { label: "" })],
  );
  assertEqual(marksOfClass(scene(empty, { edge_labels: true }), CLASS_EDGE_PLATE).length, 0, "nor does an empty label");
});

check("stubsLeaveTheirGroupEnclosure", () => {
  // An edge whose far end the query did not draw is ordinary (§4.2). It
  // leaves its known endpoint and ends in a hollow ring outside the
  // enclosure of that node's group — which is *why* an enclosure is a
  // hairline and not a filled panel, and which is asserted here as a
  // property of the drawing rather than of a lucky arrangement.
  const envelope = envelopeOf(
    [
      node("a", { label: "Alpha", zone: "elwynn" }),
      node("b", { label: "Beta", zone: "elwynn" }),
      node("c", { label: "Gamma", zone: "duskwood" }),
    ],
    [edge("id-a", "id-outside")],
  );
  const result = scene(envelope, { group_by: "zone" });

  const stub = marksOfClass(result, CLASS_STUB);
  const ring = marksOfClass(result, CLASS_STUB_RING);
  assertEqual(stub.length, 1, "one stub for one edge that leaves");
  assertEqual(ring.length, 1, "ending in one hollow ring");
  assertEqual(ring[0].fill, UNFILLED, "hollow, so it is not a node");

  const enclosures = marksOfClass(result, CLASS_ENCLOSURE);
  const home = enclosures.find(
    (encl) =>
      stub[0].x1 >= encl.x &&
      stub[0].x1 <= encl.x + encl.w &&
      stub[0].y1 >= encl.y &&
      stub[0].y1 <= encl.y + encl.h,
  );
  assert(home !== undefined, "the stub starts inside its node's enclosure");
  const outside =
    ring[0].cx < home.x || ring[0].cx > home.x + home.w || ring[0].cy < home.y || ring[0].cy > home.y + home.h;
  assert(
    outside,
    `the stub's terminus is outside the enclosure: ring at ${ring[0].cx},${ring[0].cy} in ` +
      `${home.x},${home.y} ${home.w}x${home.h}`,
  );

  assertEqual(result.stubs.total, 1, "and the scene counts it");
  assertEqual(result.stubs.anchorless, 0, "with a known end to leave from");
});

check("bothEndpointsOutsideIsCountedAndDrawsNothing", () => {
  // The case an implementation written around "one end is outside"
  // answers wrongly. There is no known end for a line to leave, so
  // nothing is drawn — and the count is still the answer's, because the
  // footer's sentence is about the edges the query returned.
  const envelope = envelopeOf(
    [node("a", { label: "Alpha" })],
    [edge("id-far", "id-farther"), edge("id-a", "id-outside")],
  );
  const result = scene(envelope, {});
  assertEqual(result.stubs.total, 2, "both edges lead outside this picture");
  assertEqual(result.stubs.anchorless, 1, "and one of them has no end to leave from");
  assertEqual(marksOfClass(result, CLASS_STUB).length, 1, "so one stub is drawn");
  assertEqual(marksOfClass(result, CLASS_STUB_RING).length, 1, "with one ring");
});

check("theStubCountReachesTheFooterAndAZeroSaysNothing", () => {
  // The renderer's count has a reader: render/scene.js's footer, which
  // is where §4.2 puts the sentence. A count nothing rendered would be a
  // mechanism nothing reads.
  const envelope = envelopeOf(
    [node("a", { label: "Alpha" })],
    [edge("id-a", "id-outside"), edge("id-a", "id-other")],
  );
  const result = scene(envelope, {});
  const footer = footerFor(envelope, { outside: result.stubs.total });
  assert(footer.text.includes("2 edges lead outside this picture"), `footer says so: ${footer.text}`);

  // And a picture with no stubs says nothing about them, for the frame's
  // own first rule: there is no clause for the absence of a thing.
  const clean = footerFor(envelope, { outside: 0 });
  assert(!clean.text.includes("outside"), `a zero is silent: ${clean.text}`);
  assertEqual(footerFor(envelope, {}).outside, null, "and a caller who counted nothing reports nothing");
});

// --- The negative halves the frame does not own ----------------------

check("anEmptyAnswerIsAnEmptyScene", () => {
  const result = scene(envelopeOf([], []), { color_by: "zone", group_by: "zone", arrows: true });
  assertDeepEqual(result.marks, [], "nothing is drawn");
  assertDeepEqual(result.legend.rows, [], "and the legend has no rows");
  assertDeepEqual(result.groups, [], "and there are no groups");
  assertEqual(result.stubs.total, 0, "and nothing leads outside");
  // The frame is what says "This view matched nothing", once, as a
  // success. A renderer that drew an apology would be a second voice
  // saying it differently.
});

check("aTruncatedAnswerDrawsWhatItWasGivenAndMarksNothing", () => {
  // The envelope says a cap was hit; it does not say which node lost a
  // neighbour. A mark claiming to know would invent the one thing the
  // server declined to measure, so the picture is the same picture and
  // the frame carries the band.
  const nodes = [node("a", { label: "Alpha" }), node("b", { label: "Beta" })];
  const clean = envelopeOf(nodes, [edge("id-a", "id-b")]);
  const truncated = { ...clean, truncated: { nodes: true, edges: true, depth: false } };
  assertDeepEqual(
    scene(truncated, {}).marks,
    scene(clean, {}).marks,
    "a truncated answer draws exactly what an untruncated one draws",
  );
});

check("aNodeWithNoPositionIsNotDrawnAtTheOrigin", () => {
  const envelope = envelopeOf([node("a", { label: "Alpha" }), node("b", { label: "Beta" })]);
  const full = laidOut(envelope, {});
  const partial = { placements: full.placements.filter((p) => p.key !== addressOf({ type: "quest", key: "b" })) };
  const result = graphScene(envelope, partial, {});

  assertDeepEqual(result.unplaced, [addressOf({ type: "quest", key: "b" })], "the node is named");
  assertEqual(
    result.marks.filter((mark) => mark.key === addressOf({ type: "quest", key: "b" })).length,
    0,
    "and nothing is drawn for it: a box at the origin is a position nobody chose",
  );
  // The other node is drawn, so this is not a scene that gave up.
  assert(boxOf(result, "a") !== null, "the placed node is still drawn");
});

check("anEdgeToAnUnplacedNodeIsNotAStub", () => {
  // It is not an edge leaving the picture: the far end is in the answer.
  // Reporting it as a stub would tell a designer the query did not draw
  // something it did draw.
  const envelope = envelopeOf(
    [node("a", { label: "Alpha" }), node("b", { label: "Beta" })],
    [edge("id-a", "id-b")],
  );
  const full = laidOut(envelope, {});
  const partial = { placements: full.placements.filter((p) => p.key !== addressOf({ type: "quest", key: "b" })) };
  const result = graphScene(envelope, partial, {});
  assertEqual(result.stubs.total, 0, "nothing led outside the picture");
  assertEqual(marksOfClass(result, CLASS_EDGE).length, 0, "and the edge is simply not drawn");
});

// --- The joins -------------------------------------------------------

check("theTwinAndTheSceneAgreeOnNodeCount", () => {
  // Task 5 built the twin before the renderers so that this could be
  // asserted. A picture and a twin that disagree is a view telling two
  // stories about one answer, and it is the defect the plan's ordering
  // exists to catch.
  const envelope = envelopeOf(
    [
      node("a", { label: "Alpha", zone: "elwynn", weight: 3 }),
      node("b", { label: "Beta", zone: "elwynn" }, { ambiguous: true }),
      node("c", { label: "Gamma", zone: "duskwood", weight: 40 }),
      node("d", { label: "Delta" }),
    ],
    [edge("id-a", "id-b"), edge("id-b", "id-c"), edge("id-c", "id-outside")],
  );
  const params = { color_by: "zone", size_by: "weight", group_by: "zone", arrows: true };
  const result = scene(envelope, params);
  const twin = twinFor(envelope);

  const drawn = result.marks.filter(
    (mark) => mark.class === CLASS_NODE || mark.class === CLASS_NODE_ABSENT,
  );
  assertEqual(
    drawn.length + result.unplaced.length,
    twin.nodes.rows.length,
    "every row the twin has is a box in the picture or a node the layout placed nowhere",
  );
  assertDeepEqual(
    drawn.map((mark) => mark.key).sort(),
    twin.nodes.rows.map((row) => row.key).sort(),
    "and they are the same nodes, by the same address",
  );

  // The edges as well, in both halves: an edge the twin has a row for is
  // drawn or leads outside.
  assertEqual(
    marksOfClass(result, CLASS_EDGE).length + result.stubs.total,
    twin.edges.rows.length,
    "every edge row is a line in the picture or an edge that leaves it",
  );

  // And the words agree: a node's label in the picture is the text the
  // twin's `label` cell carries, character for character, because both
  // read the palette's own labelFor.
  const labelCell = twin.nodes.columns.findIndex((column) => column.key === "label");
  for (const row of twin.nodes.rows) {
    const label = result.marks.find(
      (mark) => mark.key === row.key && mark.class === CLASS_NODE_LABEL,
    );
    if (!label) continue;
    assertEqual(label.text, row.cells[labelCell].text, `the box and the row agree on ${row.key}`);
  }
});

check("theLayoutIsAskedForTheBoxThatIsDrawn", () => {
  // One measurement, used twice. A renderer that measured for the
  // drawing only would draw a three-times-area box into a hole reserved
  // for a one-times one, at every zoom, forever — and no test of either
  // half alone can see it.
  const envelope = envelopeOf([
    node("a", { label: "Alpha", weight: 1 }),
    node("b", { label: "A much longer name", weight: 100 }),
  ]);
  const params = { size_by: "weight" };
  const request = graphLayoutRequest(envelope, params);
  const result = scene(envelope, params);

  for (const entry of request.nodes) {
    const drawn = boxOf(result, entry.key);
    assertClose(drawn.w, entry.width, `the width laid out for quest/${entry.key} is the one drawn`);
    assertClose(drawn.h, entry.height, "and so is the height");
  }
  // The fixture really exercises both inputs to a box: a longer label
  // and a larger value both made one bigger.
  assert(request.nodes[1].width > request.nodes[0].width, "the fixture has two different boxes");
});

check("theRendererAddressesANodeAsEveryOtherSurfaceDoes", () => {
  // The address is the join between a twin row, a laid-out node, a
  // drawn box and a position write. Two spellings of it fail silently,
  // which is why there is one function and why this asserts it rather
  // than trusting it.
  const envelope = envelopeOf([node("a", { label: "Alpha" })]);
  const result = scene(envelope, {});
  const address = addressOf({ type: "quest", key: "a" });
  assertEqual(boxOf(result, "a").key, address, "the box is keyed by the address");
  assertEqual(twinFor(envelope).nodes.rows[0].key, address, "and so is the twin's row");
  assertEqual(laidOut(envelope, {}).placements[0].key, address, "and so is the placement");
});

check("theSceneIsTheSameForOneEnvelopeHoweverItsRowsArrived", () => {
  // A picture that depended on the order the server happened to return
  // its rows in is a picture that rearranges itself for no reason a
  // designer can see. The layout's own determinism is Task 6's; this is
  // the renderer's half of it.
  const nodes = [
    node("a", { label: "Alpha", zone: "elwynn" }),
    node("b", { label: "Beta", zone: "duskwood" }),
    node("c", { label: "Gamma", zone: "elwynn" }),
  ];
  const edges = [edge("id-a", "id-b"), edge("id-b", "id-c")];
  const params = { color_by: "zone", group_by: "zone", arrows: true };
  const forwards = scene(envelopeOf(nodes, edges), params);
  const backwards = scene(envelopeOf([...nodes].reverse(), [...edges].reverse()), params);

  const key = (mark) => JSON.stringify([mark.class, mark.key ?? "", mark.text ?? "", mark.x, mark.y]);
  assertDeepEqual(
    forwards.marks.map(key).sort(),
    backwards.marks.map(key).sort(),
    "the same envelope in another order is the same picture",
  );
});

check("theRendererJoinsEdgesThroughTheOnePlaceThatRuleLives", () => {
  // joinEdges is render/scene.js's, and it is the one implementation of
  // "an endpoint may not be among the nodes" — six of them would be five
  // bugs. This is the assertion that the renderer really goes through
  // it rather than having grown its own.
  const nodes = [node("a", { label: "Alpha" }), node("b", { label: "Beta" })];
  const edges = [edge("id-a", "id-b"), edge("id-a", "id-outside"), edge("id-x", "id-y")];
  const joined = joinEdges(nodes, edges);
  const result = scene(envelopeOf(nodes, edges), {});
  assertEqual(marksOfClass(result, CLASS_EDGE).length, joined.drawn.length, "drawn edges agree");
  assertEqual(result.stubs.total, joined.stubs.length, "and so do the ones that leave");
});

check("theLegendIsThePalettesAndTheRendererDoesNotSecondGuessIt", () => {
  // Every hue in the picture is one palette.js chose, so a legend row
  // and the box it explains are the same colour by construction rather
  // than by two functions agreeing.
  const nodes = [];
  for (let i = 0; i < 12; i++) {
    nodes.push(node(`n${i}`, { label: `N${i}`, zone: `zone-${i}` }));
  }
  nodes.push(node("unset", { label: "Unset" }));
  const envelope = envelopeOf(nodes);
  const result = scene(envelope, { color_by: "zone" });
  assertDeepEqual(
    result.legend.rows.map((row) => [row.kind, row.count]),
    legendFor(nodes, "zone").rows.map((row) => [row.kind, row.count]),
    "the legend is the palette's own",
  );
  const kinds = result.legend.rows.map((row) => row.kind);
  assertEqual(kinds.filter((kind) => kind === "hue").length, 8, "eight hues");
  assertEqual(kinds.filter((kind) => kind === "hatch").length, 1, "one hatched tail");
  assertEqual(kinds.filter((kind) => kind === "unset").length, 1, "and one unset row");
});

check("everyMarkIsAKindTheContractNamesAndCanBeDragged", () => {
  // The emitter drops a mark of a kind it does not know, silently as far
  // as the picture is concerned, and the drag layer cannot move a kind
  // with no coordinate pairs. Both are properties of the *renderer's*
  // output, so this is where they can be checked before a browser is
  // involved.
  const envelope = envelopeOf(
    [
      node("a", { label: "Alpha", zone: "elwynn", weight: 2 }, { ambiguous: true }),
      node("b", { label: "Beta", zone: "duskwood", weight: 9 }),
    ],
    [edge("id-a", "id-b", { label: "requires" }), edge("id-a", "id-outside")],
  );
  const result = scene(envelope, {
    color_by: "zone",
    size_by: "weight",
    group_by: "zone",
    arrows: true,
    edge_labels: true,
  });
  assert(result.marks.length > 10, "the fixture draws every kind of thing this renderer has");
  for (const mark of result.marks) {
    assert(
      ["rect", "disc", "line", "label", "image"].includes(mark.kind),
      `mark kind ${JSON.stringify(mark.kind)} is one the emitter knows`,
    );
    for (const [field, value] of Object.entries(mark)) {
      if (field === "kind" || field === "layer") continue;
      assert(
        value === undefined || typeof value !== "object",
        `${field} on a ${mark.kind} is a scalar the emitter can write`,
      );
    }
  }
});

// --- Run -------------------------------------------------------------

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
  console.error(`${failures} graph renderer check(s) failed`);
  process.exit(1);
}
console.log("graph.js: all checks passed");
