// The harness for the `timeline` renderer:
// internal/web/static/render/timeline.js, over the drawing vocabulary it
// shares with the other five (internal/web/static/render/marks.js,
// render/controls.js) and the frame's own bands (render/scene.js).
//
// What this layer covers that no Go test can, and what is this
// renderer's alone.
//
// **That an enum axis is its option sequence and not its set.** A
// championship declaring `[heat, semi, final]` has an axis in that
// order; sorting those three words alphabetically produces a picture
// that is wrong in a way nothing in it shows. The fixture's declaration
// order is deliberately not its alphabetical order, or the test proves
// nothing — and the options come from the **declaration**, so a declared
// option no node carries still gets a tick, which is the half no answer
// can supply.
//
// **That a span whose end is before its start is drawn and named.** It
// is a content defect a designer wants to know about, and a renderer
// that sorted the two ends would draw a perfectly plausible bar over it.
// The test asserts both halves: the mark has no length and carries a
// caret, and the two values reach the frame **unswapped**.
//
// **That a node with no place on the axis is never at the origin.** The
// origin is a value — the axis minimum, or the first declared option —
// and the fixture has a node sitting on it, so "before the axis begins"
// is asserted against a picture where the origin is occupied by
// something that means it.
//
// **That the fourth overlapping mark in a lane collapses without a
// re-run**, over a stubbed global `fetch` that counts, exactly as
// `nested`'s depth chip does: a drawing density is not a fetch boundary.
//
// Run directly: `node internal/web/jstest/render_timeline_test.mjs`.
// internal/web/static_appjs_browser_test.go shells out to it too.

import { addressOf } from "../static/address.js";
import { UNSET_LABEL } from "../static/palette.js";
import {
  BANNER_INVERTED_SPAN,
  BANNER_OFF_AXIS,
  MARK_ELEMENTS,
  MARK_ORIGINS,
  bannersFor,
} from "../static/render/scene.js";
import { twinFor } from "../static/render/twin.js";
import { controlNamed } from "../static/render/controls.js";
import {
  CARET_GLYPH,
  CLASS_BAND_CAPTION,
  CLASS_CARET,
  CLASS_CHIP_LABEL,
  CLASS_POINT,
  CLASS_SPAN,
} from "../static/render/marks.js";
import {
  AXIS_ENUM,
  AXIS_NUMBER,
  CONTROLS,
  PARAM_AXIS_END_FIELD,
  PARAM_AXIS_FIELD,
  PARAM_AXIS_LABEL,
  PARAM_LANE_BY,
  RENDERER,
  niceTicks,
  offAxisCaption,
  timelineScene,
} from "../static/render/timeline.js";

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

function node(key, attrs = {}, extra = {}) {
  return { id: `id-${key}`, type: "race", key, name: `Race ${key}`, attrs, ...extra };
}

function envelopeOf(nodes, edges = [], extra = {}) {
  return {
    nodes,
    edges,
    stats: { nodes: nodes.length, edges: edges.length, max_depth_reached: 1, duration_ms: 2 },
    truncated: { nodes: false, edges: false, depth: false },
    stale: [],
    ...extra,
  };
}

function address(key) {
  return addressOf({ type: "race", key });
}

// A championship's stages, declared in the order they are run. Their
// alphabetical order is [final, heat, semi], which is a different axis
// over the same three words — which is the whole of the first test.
const STAGES = ["heat", "semi", "final"];
const STAGE_AXIS = { type: AXIS_ENUM, options: STAGES };

function marksOfClass(result, className) {
  return result.marks.filter((mark) => mark.class === className);
}

function pointOf(result, key) {
  const found = marksOfClass(result, CLASS_POINT).find((mark) => mark.key === address(key));
  if (!found) throw new Error(`no point drawn for race/${key}`);
  return found;
}

// keyedMarks is every mark that belongs to a node, which is what the
// origin assertion is about: a tick's rule legitimately starts at the
// origin, because the origin is where the axis starts.
function keyedMarks(result) {
  return result.marks.filter((mark) => typeof mark.key === "string" && mark.key !== "");
}

function xOf(mark) {
  return mark.cx !== undefined ? mark.cx : mark.x !== undefined ? mark.x : mark.x1;
}

// --- The axis --------------------------------------------------------

check("anEnumAxisFollowsDeclarationOrderNotSortOrder", () => {
  const envelope = envelopeOf([
    node("a", { label: "A", stage: "final" }),
    node("b", { label: "B", stage: "heat" }),
    node("c", { label: "C", stage: "semi" }),
  ]);
  const result = timelineScene(envelope, { [PARAM_AXIS_FIELD]: "stage" }, { axis: STAGE_AXIS });

  assertEqual(result.axis.type, AXIS_ENUM, "an enum field gives an enum axis");
  assertDeepEqual(
    result.axis.ticks.map((tick) => tick.caption),
    STAGES,
    "the ticks are the declared options, in the order the type declares them",
  );
  // And the fixture really can tell the two apart: alphabetically this
  // is [final, heat, semi], so a sorted axis is a different list.
  assertDeepEqual(
    [...STAGES].sort(),
    ["final", "heat", "semi"],
    "the declaration order is deliberately not the alphabetical one",
  );

  // The marks follow the axis, which is what makes the tick order more
  // than a caption: a heat is before a semi is before a final.
  assert(pointOf(result, "b").cx < pointOf(result, "c").cx, "the heat is before the semi");
  assert(pointOf(result, "c").cx < pointOf(result, "a").cx, "and the semi before the final");
  // The first declared option is the origin, which is the fact the
  // unplaced-lane test leans on.
  assertEqual(pointOf(result, "b").cx, 0, "and the first declared option is the axis origin");
});

check("everyDeclaredOptionGetsATickEvenWithNoNodes", () => {
  // The axis is what the *game* declares and the answer is what a query
  // found. A championship with no race yet in the final still has a
  // final, and an axis derived from the answer could not know it — which
  // is why the declaration reaches this renderer from the caller and is
  // never recovered from the nodes.
  const some = timelineScene(
    envelopeOf([node("b", { label: "B", stage: "heat" })]),
    { [PARAM_AXIS_FIELD]: "stage" },
    { axis: STAGE_AXIS },
  );
  assertDeepEqual(
    some.axis.ticks.map((tick) => tick.caption),
    STAGES,
    "one node carrying one option still draws all three ticks",
  );

  const none = timelineScene(envelopeOf([]), { [PARAM_AXIS_FIELD]: "stage" }, { axis: STAGE_AXIS });
  assertDeepEqual(
    none.axis.ticks.map((tick) => tick.caption),
    STAGES,
    "and an answer with no nodes at all still draws the axis",
  );
  assertEqual(none.drawn.length, 0, "with nothing on it");
  assert(none.marks.length > 0, "which is a picture and not an empty scene");

  // The captions are on the canvas and not only in the model.
  const captions = marksOfClass(none, CLASS_BAND_CAPTION).map((mark) => mark.text);
  for (const option of STAGES) {
    assert(captions.includes(option), `the axis draws a caption for ${option}`);
  }
});

check("aNumberAxisPicksTicksDeterministically", () => {
  const nodes = [
    node("a", { label: "A", level: 1 }),
    node("b", { label: "B", level: 37 }),
    node("c", { label: "C", level: 60 }),
  ];
  const params = { [PARAM_AXIS_FIELD]: "level" };
  const first = timelineScene(envelopeOf(nodes), params, { axis: { type: AXIS_NUMBER } });
  const second = timelineScene(envelopeOf(nodes), params, { axis: { type: AXIS_NUMBER } });

  assertEqual(first.axis.type, AXIS_NUMBER, "a number field gives a number axis");
  assertDeepEqual(
    second.axis.ticks,
    first.axis.ticks,
    "the same data twice is the same ticks, to the coordinate",
  );

  // And the same data in another order is still the same ticks: the
  // ticks are a property of the range, not of the arrival order, which
  // is the same rule the layout engine holds itself to.
  const shuffled = timelineScene(envelopeOf([nodes[2], nodes[0], nodes[1]]), params, {
    axis: { type: AXIS_NUMBER },
  });
  assertDeepEqual(shuffled.axis.ticks, first.axis.ticks, "and a shuffled answer is the same axis");

  // The captions are round numbers a reader recognises rather than the
  // range divided by eight.
  assertDeepEqual(niceTicks(1, 60), [10, 20, 30, 40, 50, 60], "1 to 60 ticks in tens");
  assertDeepEqual(niceTicks(0, 1), [0, 0.2, 0.4, 0.6, 0.8, 1], "0 to 1 ticks in fifths");
  for (const tick of first.axis.ticks) {
    assert(
      !String(tick.caption).includes("000000"),
      `the caption ${tick.caption} is a number somebody would write`,
    );
  }
  // A domain of one value is not a range: one tick, and everything on
  // it, rather than an axis whose ends are the same number.
  assertDeepEqual(niceTicks(5, 5), [5], "one value is one tick");
});

// --- Nothing to place ------------------------------------------------

check("aNodeWithNoAxisValueGoesToAPinnedUnplacedLane", () => {
  // `race/b` is at the first declared option, which is the axis origin,
  // and `race/gap` has no stage at all. Both are in one fixture, because
  // the point is that the origin is a value somebody means and the
  // valueless node must not be there.
  const envelope = envelopeOf([
    node("b", { label: "B", stage: "heat" }),
    node("gap", { label: "Gap" }),
  ]);
  const result = timelineScene(envelope, { [PARAM_AXIS_FIELD]: "stage" }, { axis: STAGE_AXIS });

  // **The origin scan first**, for the reason `map`'s is first: the
  // mutation this test exists against — a valueless node placed at the
  // start of the axis — fails the count below as well, and an assertion
  // that never runs is not an assertion.
  for (const mark of keyedMarks(result)) {
    if (mark.key !== address("gap")) continue;
    assert(
      xOf(mark) < 0,
      `every mark of the valueless node is before the axis begins: a ${mark.kind} is at ${xOf(mark)}`,
    );
  }
  assertEqual(pointOf(result, "b").cx, 0, "while the node at the first option is at the origin");

  assertDeepEqual(result.offAxis.keys, [address("gap")], "the valueless node is counted");
  assertEqual(result.offAxis.count, 1, "once");
  assertEqual(result.offAxis.field, "stage", "and the count names the field, which is the recovery");

  // The region is captioned, in §4.8's own words.
  const captions = marksOfClass(result, CLASS_BAND_CAPTION).map((mark) => mark.text);
  assert(captions.includes("no value for stage"), `the region says what is missing: ${captions}`);
  assertEqual(offAxisCaption("stage"), "no value for stage", "in the sentence the spec names");

  // And the frame counts it, naming the field.
  const band = bannersFor(envelope, { offAxis: result.offAxis }).find(
    (banner) => banner.code === BANNER_OFF_AXIS,
  );
  assert(band !== undefined, "the frame bands it");
  assert(band.text.includes("1 node has no value"), `in the singular: ${band.text}`);
  assert(band.text.includes("stage"), `and names the field: ${band.text}`);
  // A picture with nothing off the axis says nothing about it.
  assertEqual(
    bannersFor(envelope, { offAxis: { count: 0, field: "stage" } }).filter(
      (banner) => banner.code === BANNER_OFF_AXIS,
    ).length,
    0,
    "and an answer with none is silent",
  );
});

check("aValueOutsideTheDeclaredOptionsIsNotOnTheAxis", () => {
  // The catalogue refuses, at save time, a view whose axis field is
  // declared differently across the types in scope — so a *saved* view
  // cannot produce this. An inline run can, and the answer is the one
  // this renderer gives any value it cannot place: an enum axis **is**
  // its options, so a value outside them is not a value of this axis.
  // It is emphatically not a fourth tick invented from the answer.
  const envelope = envelopeOf([
    node("a", { label: "A", stage: "heat" }),
    node("odd", { label: "Odd", stage: "bronze" }),
  ]);
  const result = timelineScene(envelope, { [PARAM_AXIS_FIELD]: "stage" }, { axis: STAGE_AXIS });

  assertDeepEqual(
    result.axis.ticks.map((tick) => tick.caption),
    STAGES,
    "the axis is still the three declared options",
  );
  assertDeepEqual(result.offAxis.keys, [address("odd")], "and the stray value is off the axis");
  for (const mark of keyedMarks(result)) {
    if (mark.key !== address("odd")) continue;
    assert(xOf(mark) < 0, "drawn before the axis begins, with the valueless ones");
  }
  // It is still drawn and still counted: a node dropped from the picture
  // would be a scene disagreeing with its own twin.
  assert(result.drawn.includes(address("odd")), "it is drawn");
  assertEqual(result.drawn.length + result.collapsed.length, 2, "and both nodes are accounted for");
});

// --- Spans -----------------------------------------------------------

check("aSpanEndingBeforeItsStartIsDrawnAndNamed", () => {
  // A quest declared from level 40 to level 10 is a content defect a
  // designer wants to know about. A renderer that sorted the two ends
  // would draw a perfectly plausible bar over it — which is the whole
  // class of wrong-picture-that-looks-right this sub-project exists
  // against.
  const envelope = envelopeOf([
    node("ok", { label: "Ok", from: 10, to: 40 }),
    node("back", { label: "Back", from: 40, to: 10 }),
  ]);
  const result = timelineScene(
    envelope,
    { [PARAM_AXIS_FIELD]: "from", [PARAM_AXIS_END_FIELD]: "to" },
    { axis: { type: AXIS_NUMBER } },
  );

  // The inverted one has **no length**: there is no honest length to
  // draw between two values in the wrong order.
  const bars = marksOfClass(result, CLASS_SPAN);
  assertEqual(bars.length, 1, "only the well-formed span is a bar");
  assertEqual(bars[0].key, address("ok"), "and it is the well-formed one");
  assert(bars[0].w > 0, "with a length");
  const carets = marksOfClass(result, CLASS_CARET);
  assertEqual(carets.length, 1, "the inverted one carries a caret");
  assertEqual(carets[0].key, address("back"), "on the node that has it");
  assertEqual(carets[0].text, CARET_GLYPH, "and it is the glyph, not a dash");

  // **The two ends were not swapped**, which is what the frame says: the
  // band names the node and reports the values in the order the game
  // wrote them.
  assertDeepEqual(
    result.inverted,
    [{ key: address("back"), name: "Back", from: "40", to: "10" }],
    "the frame is told from 40 to 10, in that order",
  );
  const band = bannersFor(envelope, { invertedSpans: result.inverted }).find(
    (banner) => banner.code === BANNER_INVERTED_SPAN,
  );
  assert(band !== undefined, "the frame bands it");
  assertEqual(band.rows.length, 1, "with one row for the one span");
  assert(band.rows[0].message.includes("from 40 to 10"), `unswapped: ${band.rows[0].message}`);
  assert(band.text.includes("were not swapped"), `and says so: ${band.text}`);

  // A picture with no inverted span says nothing about them.
  assertEqual(
    bannersFor(envelope, { invertedSpans: [] }).filter(
      (banner) => banner.code === BANNER_INVERTED_SPAN,
    ).length,
    0,
    "and a well-formed answer is silent",
  );

  // A span whose end is not on the axis has no length either, and is its
  // own count: the catalogue refuses that pairing at save time, and an
  // inline run meets it. Never a bar back to the origin, which would
  // claim a range the answer does not have.
  const open = timelineScene(
    envelopeOf([node("a", { label: "A", stage: "semi", ends: "bronze" })]),
    { [PARAM_AXIS_FIELD]: "stage", [PARAM_AXIS_END_FIELD]: "ends" },
    { axis: STAGE_AXIS },
  );
  assertEqual(open.openSpans, 1, "a span with no end on this axis is counted");
  assertEqual(marksOfClass(open, CLASS_SPAN).length, 0, "and draws no bar");
  assertEqual(pointOf(open, "a").cx > 0, true, "the start is where its own value is");
});

// --- Lanes and stacking ----------------------------------------------

check("laneCaptionsCarryTheirValue", () => {
  const envelope = envelopeOf([
    node("a", { label: "A", stage: "heat", series: "gt3" }),
    node("b", { label: "B", stage: "semi", series: "gt3" }),
    node("c", { label: "C", stage: "final", series: "lmp" }),
    node("d", { label: "D", stage: "heat" }),
  ]);
  const result = timelineScene(
    envelope,
    { [PARAM_AXIS_FIELD]: "stage", [PARAM_LANE_BY]: "series" },
    { axis: STAGE_AXIS },
  );

  assertDeepEqual(
    result.lanes.map((lane) => [lane.caption, lane.count]),
    [
      ["gt3", 2],
      ["lmp", 1],
      [UNSET_LABEL, 1],
    ],
    "one lane per value, with its count, and the absence last",
  );
  // Last for `layered`'s reason: a lane of "the ones we know nothing
  // about" at the top would read as the first lane of the answer.
  assertEqual(result.lanes[result.lanes.length - 1].caption, UNSET_LABEL, "the absence is last");

  // The captions are drawn, and the lanes really are separate rows.
  const captions = marksOfClass(result, CLASS_BAND_CAPTION).map((mark) => mark.text);
  assert(captions.includes("gt3") && captions.includes("lmp"), `both values are drawn: ${captions}`);
  assert(pointOf(result, "a").cy !== pointOf(result, "c").cy, "two lanes are two rows");
  assertEqual(pointOf(result, "a").cy, pointOf(result, "b").cy, "and one lane is one row");

  // A view that lanes nothing has **no** lanes, which is not an empty
  // list of them, and draws no lane rule to name.
  const plain = timelineScene(envelope, { [PARAM_AXIS_FIELD]: "stage" }, { axis: STAGE_AXIS });
  assertEqual(plain.lanes, null, "an unlaned timeline has no lanes");
  const plainCaptions = marksOfClass(plain, CLASS_BAND_CAPTION).map((mark) => mark.text);
  assert(!plainCaptions.includes("gt3"), "and names no lane");

  // The axis label is written once, at the axis.
  const labelled = timelineScene(
    envelope,
    { [PARAM_AXIS_FIELD]: "stage", [PARAM_AXIS_LABEL]: "Championship stage" },
    { axis: STAGE_AXIS },
  );
  assertEqual(
    marksOfClass(labelled, CLASS_BAND_CAPTION).filter((mark) => mark.text === "Championship stage")
      .length,
    1,
    "the axis label is drawn once",
  );
  assertEqual(labelled.axis.label, "Championship stage", "and the model carries it");
});

check("threeOverlappingMarksStackAndTheFourthCollapses", () => {
  // Five races in one lane at the same stage: three stack, and the rest
  // become one chip. Everything under it is already in the envelope, so
  // opening it is a redraw and not a fetch — `nested`'s rule about a
  // drawing bound, applied to a drawing density.
  const nodes = ["a", "b", "c", "d", "e"].map((key) =>
    node(key, { label: key.toUpperCase(), stage: "heat" }),
  );
  const envelope = envelopeOf(nodes);
  const params = { [PARAM_AXIS_FIELD]: "stage" };

  const original = globalThis.fetch;
  let fetches = 0;
  globalThis.fetch = () => {
    fetches++;
    return Promise.reject(new Error("a renderer does not fetch"));
  };
  try {
    const stacked = timelineScene(envelope, params, { axis: STAGE_AXIS });
    assertEqual(marksOfClass(stacked, CLASS_POINT).length, 3, "three marks stack");
    assertEqual(stacked.drawn.length, 3, "and the scene says three are drawn");
    assertEqual(stacked.collapsed.length, 2, "the rest are held by a chip");
    assertDeepEqual(stacked.chips, [{ key: address("d"), count: 2 }], "one chip, counting two");
    const chip = marksOfClass(stacked, CLASS_CHIP_LABEL);
    assertEqual(chip.length, 1, "drawn once");
    assertEqual(chip[0].text, "+2", "and reading +2, never a bare number");

    // The three that stack are three rows, not three marks on top of
    // each other.
    const rows = new Set(marksOfClass(stacked, CLASS_POINT).map((mark) => mark.cy));
    assertEqual(rows.size, 3, "the stack is three rows deep");

    const opened = timelineScene(envelope, params, {
      axis: STAGE_AXIS,
      expanded: [address("d")],
    });
    assertEqual(marksOfClass(opened, CLASS_POINT).length, 5, "opening draws all five");
    assertEqual(opened.collapsed.length, 0, "with nothing held back");
    assertEqual(marksOfClass(opened, CLASS_CHIP_LABEL).length, 0, "and the chip is gone");
    assertEqual(fetches, 0, "and opening asked the server for nothing");
  } finally {
    if (original === undefined) delete globalThis.fetch;
    else globalThis.fetch = original;
  }

  // Marks that do **not** overlap do not stack: five races across five
  // levels are five marks on one row, and a renderer that stacked by
  // arrival order would collapse a perfectly readable picture.
  const spread = timelineScene(
    envelopeOf(["a", "b", "c", "d", "e"].map((key, i) => node(key, { label: key, level: i * 10 }))),
    { [PARAM_AXIS_FIELD]: "level" },
    { axis: { type: AXIS_NUMBER } },
  );
  assertEqual(marksOfClass(spread, CLASS_POINT).length, 5, "five marks");
  assertDeepEqual(spread.chips, [], "and no chip, because nothing overlaps");
  assertEqual(new Set(marksOfClass(spread, CLASS_POINT).map((mark) => mark.cy)).size, 1, "on one row");
});

// --- The twin, and the emitter's contract ----------------------------

check("theTwinAndTheSceneAgreeOnEveryNode", () => {
  // The twin is built from the envelope alone and the scene from the
  // envelope and a field declaration; two descriptions of one answer
  // that disagree is the defect this ordering exists to catch.
  //
  // The fixture carries every way this renderer can meet a node: a
  // point, a span, an inverted span, a value off the axis, a node with
  // no value at all, and four in one lane so a chip is holding one.
  const nodes = [
    node("p", { label: "P", stage: "semi" }),
    node("gap", { label: "Gap" }),
    node("odd", { label: "Odd", stage: "bronze" }),
    ...["s1", "s2", "s3", "s4"].map((key) => node(key, { label: key, stage: "final" })),
  ];
  const envelope = envelopeOf(nodes);
  const result = timelineScene(envelope, { [PARAM_AXIS_FIELD]: "stage" }, { axis: STAGE_AXIS });
  const twin = twinFor(envelope);

  assertEqual(twin.nodes.rows.length, 7, "the twin has every node");
  assertEqual(
    result.drawn.length + result.collapsed.length,
    twin.nodes.rows.length,
    "every row the twin has is a mark in the picture or a node a chip is holding",
  );
  assertDeepEqual(
    [...result.drawn, ...result.collapsed].sort(),
    twin.nodes.rows.map((row) => row.key).sort(),
    "and they are the same nodes, by the same address",
  );
  assert(result.collapsed.length > 0, "and the fixture really is holding one back");

  // The label a mark wears and the cell its row carries are the same
  // characters, because both go through the palette's own labelFor.
  const row = twin.nodes.rows.find((r) => r.key === address("p"));
  const labels = result.marks.filter((mark) => mark.key === address("p") && mark.text !== undefined);
  assertEqual(
    labels[0].text,
    row.cells.find((cell) => cell.column === "label").text,
    "a mark's label and its twin row's cell are one string",
  );

  // The twin describes the answer and this describes the drawing: it
  // lists the node a chip is holding, exactly as it lists a node
  // `nested`'s depth bound held back.
  for (const key of result.collapsed) {
    assert(
      twin.nodes.rows.some((r) => r.key === key),
      "a node a chip is holding is still a row in the twin",
    );
  }
});

check("everyMarkIsAKindTheContractNamesAndCanBeDragged", () => {
  const nodes = [
    node("a", { label: "A", from: 1, to: 5, series: "gt3" }),
    node("b", { label: "B", from: 9, to: 3, series: "gt3" }),
    node("c", { label: "C", from: 4, to: 4, series: "lmp" }),
    node("gap", { label: "Gap", series: "lmp" }),
  ];
  const result = timelineScene(
    envelopeOf(nodes),
    { [PARAM_AXIS_FIELD]: "from", [PARAM_AXIS_END_FIELD]: "to", [PARAM_LANE_BY]: "series", [PARAM_AXIS_LABEL]: "Level" },
    { axis: { type: AXIS_NUMBER } },
  );
  assert(result.marks.length > 10, "the fixture draws every kind of thing this renderer has");
  for (const mark of result.marks) {
    assert(
      Object.prototype.hasOwnProperty.call(MARK_ELEMENTS, mark.kind),
      `mark kind ${JSON.stringify(mark.kind)} is one the emitter knows; anything else is dropped silently`,
    );
    assert(
      Array.isArray(MARK_ORIGINS[mark.kind]) && MARK_ORIGINS[mark.kind].length > 0,
      `mark kind ${JSON.stringify(mark.kind)} has coordinate pairs the drag layer can translate`,
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

check("theControlsTeachWhatTheCatalogueDoesNot", () => {
  assertEqual(RENDERER, "timeline", "the module names the renderer it is");
  const axis = controlNamed(CONTROLS, PARAM_AXIS_FIELD);
  assert(axis !== null, "axis_field has a control");
  // The sentence a designer needs is that an enum's declared order *is*
  // the axis: that is the fact behind every other rule in this renderer,
  // and it is nowhere else in the interface.
  assert(axis.tooltip.includes("declares them"), "whose sentence names the declaration order");
  assert(axis.tooltip.includes("never alphabetically"), "and refuses the alternative out loud");
  const end = controlNamed(CONTROLS, PARAM_AXIS_END_FIELD);
  assert(end.tooltip.includes("caret"), "and axis_end_field says what an inverted span looks like");
  assert(end.tooltip.includes("turned round"), "and that it is not silently corrected");
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
  console.error(`${failures} timeline renderer check(s) failed`);
  process.exit(1);
}
console.log("timeline.js: all checks passed");
