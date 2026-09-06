// The harness for the `nested` renderer:
// internal/web/static/render/nested.js, over the drawing vocabulary it
// shares with the other five (internal/web/static/render/marks.js,
// render/controls.js) and the frame's own bands (render/scene.js).
//
// What this layer covers that no Go test can, and what is this
// renderer's alone.
//
// **That a drawing depth is not a fetch boundary.** `max_depth` holds
// children back from the picture; every one of them is already in the
// envelope. A renderer that treated the bound as a fetch boundary would
// make one parameter mean two things and would put a network round trip
// behind a disclosure triangle. The chip's count is asserted, and so is
// the number of fetches an expansion makes: zero, over a stubbed global
// `fetch` that counts.
//
// **That a root and an orphan of the cap do not look alike.** "This
// thing is top-level" and "this thing's parent did not fit" are two
// statements, and both are drawn at the top level. They are in **one**
// fixture, because a test with one of each in separate fixtures passes
// for an implementation that treats them identically.
//
// **That a containment cycle terminates and says so.** A contains B
// contains A is data the metamodel permits, and the failure mode without
// a repeat check is a stack overflow rather than a wrong picture — which
// is exactly why the check has a test rather than a comment. The
// repeated box carries the glyph and the frame names both ends.
//
// **That colour tints the header and never the box**, at every level,
// because a nest of four fills has no legible text in it.
//
// **That the twin describes the answer and not the drawing**: it lists
// every node, including the ones the depth bound held back.
//
// Run directly: `node internal/web/jstest/render_nested_test.mjs`.
// internal/web/static_appjs_browser_test.go shells out to it too.

import { addressOf } from "../static/address.js";
import { UNSET_LABEL } from "../static/palette.js";
import {
  BANNER_CONTAINMENT_CYCLE,
  BANNER_TRUNCATED_NODES,
  MARK_ELEMENTS,
  MARK_ORIGINS,
  bannersFor,
  footerFor,
} from "../static/render/scene.js";
import { twinFor } from "../static/render/twin.js";
import { controlNamed } from "../static/render/controls.js";
import {
  ABSENT_DASH,
  CLASS_CHIP,
  CLASS_CHIP_LABEL,
  CLASS_CONTAINER,
  CLASS_CONTAINER_ABSENT,
  CLASS_CONTAINER_HEADER,
  CLASS_CONTAINER_LABEL,
  CLASS_CYCLE,
  CYCLE_GLYPH,
  NODE_PLAIN_FILL,
  PLATE_FILL,
  UNFILLED,
} from "../static/render/marks.js";
import {
  CONTROLS,
  PARAM_MAX_DEPTH,
  RENDERER,
  nestedScene,
} from "../static/render/nested.js";

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
  return { id: `id-${key}`, type: "zone", key, name: `Zone ${key}`, attrs, ...extra };
}

// The containment edge: `source` is contained in `target`, which is the
// direction internal/views/renderers.go's own parameter doc states.
function inside(inner, outer, type = "part_of") {
  return { id: `e-${inner}-${outer}`, type, source: `id-${inner}`, target: `id-${outer}` };
}

function envelopeOf(nodes, edges = [], extra = {}) {
  return {
    nodes,
    edges,
    stats: { nodes: nodes.length, edges: edges.length, max_depth_reached: 3, duration_ms: 5 },
    truncated: { nodes: false, edges: false, depth: false },
    stale: [],
    ...extra,
  };
}

const VIA = { contain_via: "part_of" };

function scene(envelope, params = {}, options = {}) {
  return nestedScene(envelope, { ...VIA, ...params }, options);
}

function address(key) {
  return addressOf({ type: "zone", key });
}

function marksOfClass(result, className) {
  return result.marks.filter((mark) => mark.class === className);
}

function boxOf(result, key) {
  const found = result.marks.find(
    (mark) =>
      mark.key === address(key) &&
      (mark.class === CLASS_CONTAINER || mark.class === CLASS_CONTAINER_ABSENT),
  );
  if (!found) throw new Error(`no box drawn for zone/${key}`);
  return found;
}

function headerOf(result, key) {
  const found = result.marks.find(
    (mark) => mark.key === address(key) && mark.class === CLASS_CONTAINER_HEADER,
  );
  if (!found) throw new Error(`no header drawn for zone/${key}`);
  return found;
}

// contains is the geometric claim the whole renderer is about: this box
// is inside that one.
// contains is the geometric claim the whole renderer is about: this box
// is inside that one, with **clearance on every side**.
//
// Strictly inside, and not merely not-overflowing: a child flush against
// its container's border reads as a box that escaped, and a check
// written as `<=` passes for a renderer that drew the nest with no
// padding at all — the shape of the fixture-at-the-edge mistake Task 8
// found in its own stub test.
function contains(outer, inner) {
  return (
    inner.x > outer.x &&
    inner.y > outer.y &&
    inner.x + inner.w < outer.x + outer.w &&
    inner.y + inner.h < outer.y + outer.h
  );
}

// --- The depth bound -------------------------------------------------

check("beyondMaxDepthACountChipExpandsWithoutARerun", () => {
  // One root with twelve children, drawn one level deep: the twelve are
  // held back and the chip says how many.
  const kids = Array.from({ length: 12 }, (_, i) => node(`k${i}`));
  const envelope = envelopeOf(
    [node("root"), ...kids],
    kids.map((kid) => inside(kid.key, "root")),
  );

  // Every fetch this module could make, counted. It makes none, and the
  // assertion is over the count rather than over the absence of an
  // import, because a renderer that grew a client would still pass a
  // structural check.
  const original = globalThis.fetch;
  let fetches = 0;
  globalThis.fetch = () => {
    fetches++;
    return Promise.reject(new Error("a renderer does not fetch"));
  };
  try {
    const capped = scene(envelope, { max_depth: 1 });
    const chips = marksOfClass(capped, CLASS_CHIP_LABEL);
    assertEqual(chips.length, 1, "one chip, on the one container holding children back");
    assertEqual(chips[0].text, "+12", "and it reads +12");
    assertDeepEqual(capped.chips, [{ key: address("root"), count: 12 }], "and the scene says so too");
    assertEqual(capped.hidden.length, 12, "the twelve are held back, by name");
    assertEqual(capped.drawn.length, 1, "and only the root has a box");

    // Expanding is a redraw of the **same envelope**. The nodes were
    // always here; the bound is about the drawing.
    const opened = scene(envelope, { max_depth: 1 }, { expanded: [address("root")] });
    assertEqual(marksOfClass(opened, CLASS_CHIP).length, 0, "the chip is gone");
    assertEqual(opened.drawn.length, 13, "and all thirteen boxes are drawn");
    assertEqual(opened.hidden.length, 0, "with nothing held back");
    const outer = boxOf(opened, "root");
    for (const kid of kids) {
      assert(contains(outer, boxOf(opened, kid.key)), `zone/${kid.key} is drawn inside the root`);
    }
    assertEqual(fetches, 0, "and expanding asked the server for nothing");
  } finally {
    if (original === undefined) delete globalThis.fetch;
    else globalThis.fetch = original;
  }
});

check("aTopLevelNodeAndAnOrphanOfTheCapDoNotLookAlike", () => {
  // Both in one fixture, because two fixtures would pass for an
  // implementation that drew them identically.
  //
  //   zone/root  — a real root: no containment edge leaves it.
  //   zone/lost  — contained in something the node cap left out.
  const envelope = envelopeOf(
    [node("root"), node("lost"), node("child")],
    [inside("child", "root"), inside("lost", "gone")],
    { truncated: { nodes: true, edges: false, depth: false } },
  );
  const result = scene(envelope);

  const root = boxOf(result, "root");
  const lost = boxOf(result, "lost");
  assertEqual(lost.class, CLASS_CONTAINER_ABSENT, "the orphan of the cap is drawn as an absence");
  assertEqual(lost.dash, ABSENT_DASH, "and is dashed");
  assertEqual(root.class, CLASS_CONTAINER, "while the real root is an ordinary box");
  assertEqual(root.dash, undefined, "and is not dashed");
  assertDeepEqual(result.orphans, [address("lost")], "and only one of the two is counted");

  // Both are at the top level: the difference is the dash and the count,
  // never the position, because the picture has nowhere else to put a
  // node whose container is missing.
  assert(!contains(root, lost), "the orphan is not drawn inside the root");

  // And the count reaches the band §4.5 names, as a clause on the
  // truncation the cap produced rather than as a second band.
  const banners = bannersFor(envelope, { orphans: result.orphans.length });
  const truncation = banners.find((banner) => banner.code === BANNER_TRUNCATED_NODES);
  assert(truncation !== undefined, "the node cap is banded");
  assert(
    truncation.text.includes("1 box is drawn at the top level because the container it names did not fit"),
    `and the band counts the orphan: ${truncation.text}`,
  );

  // A picture with no orphan says nothing about them, which is
  // render/scene.js's own first rule: there is no clause for the absence
  // of a thing.
  const quiet = bannersFor(envelope, { orphans: 0 }).find(
    (banner) => banner.code === BANNER_TRUNCATED_NODES,
  );
  assert(!quiet.text.includes("top level"), `and a picture with none is silent: ${quiet.text}`);
});

check("aContainmentCycleStopsAtTheRepeat", () => {
  // A contains B contains A. Recursing over it does not terminate: the
  // failure without the repeat check is a stack overflow, which is why
  // this test exists rather than a comment.
  const envelope = envelopeOf(
    [node("a", { label: "Alpha" }), node("b", { label: "Beta" })],
    [inside("b", "a"), inside("a", "b")],
  );
  // A depth bound well past the repeat, on purpose: `max_depth` would
  // otherwise stop the recursion for a reason that has nothing to do
  // with the cycle, and the test would pass over a renderer with no
  // repeat check at all. With this, the missing check is a stack
  // overflow, which is the failure this test exists to make loud.
  const result = scene(envelope, { max_depth: 60 });

  const glyphs = marksOfClass(result, CLASS_CYCLE);
  assertEqual(glyphs.length, 1, "the repeated box carries one cycle glyph");
  assertEqual(glyphs[0].text, CYCLE_GLYPH, "and it is the glyph, not a dash");
  assertDeepEqual(
    result.cycles,
    [{ outer: "Beta", inner: "Alpha" }],
    "and both ends are recorded, by the labels the picture drew",
  );

  // The recursion really stopped: the nest is a, then b, then a once
  // more, and no further.
  const boxes = result.marks.filter(
    (mark) => mark.class === CLASS_CONTAINER || mark.class === CLASS_CONTAINER_ABSENT,
  );
  assertEqual(boxes.length, 3, "three boxes: a, b inside it, and a repeated");
  assert(contains(boxes[0], boxes[1]), "the second is inside the first");

  // And the frame names both ends. This is the one band whose rows name
  // entities, and §4.5 asks for it: a designer told only that "this
  // picture contains a cycle" is left to find two boxes out of four
  // hundred.
  const banners = bannersFor(envelope, { containmentCycles: result.cycles });
  const band = banners.find((banner) => banner.code === BANNER_CONTAINMENT_CYCLE);
  assert(band !== undefined, "the cycle is banded");
  assertEqual(band.rows.length, 1, "with one row for the one repeat");
  assert(band.rows[0].message.includes("Beta"), `the row names the outer box: ${band.rows[0].message}`);
  assert(band.rows[0].message.includes("Alpha"), `and the inner one: ${band.rows[0].message}`);

  // A picture with no cycle carries no band, for the frame's first rule.
  assertEqual(
    bannersFor(envelope, { containmentCycles: [] }).filter(
      (banner) => banner.code === BANNER_CONTAINMENT_CYCLE,
    ).length,
    0,
    "and an acyclic nest says nothing about cycles",
  );
});

check("aCyclicAnswerStillDrawsSomething", () => {
  // A ring of containers has no root at all — every member is contained
  // in another — so a renderer that only drew from roots would draw an
  // empty picture for it. That hides the defect even more thoroughly
  // than a plausible tree does, so the lowest address is promoted to an
  // entry point.
  const envelope = envelopeOf(
    [node("a"), node("b"), node("c")],
    [inside("b", "a"), inside("c", "b"), inside("a", "c")],
  );
  const result = scene(envelope, { max_depth: 60 });
  assertEqual(result.drawn.length, 3, "every node in the ring is drawn once");
  assertEqual(marksOfClass(result, CLASS_CYCLE).length, 1, "and the repeat is marked once");
  assertEqual(result.cycles.length, 1, "with one pair named for the frame");
});

// --- Colour ----------------------------------------------------------

check("colourTintsTheHeaderNotTheBox", () => {
  // Four levels, each with a value: a filled nest would be four
  // overlapping fills and no legible text, which is why the box is paper
  // at every level and the strip carries the colour.
  const envelope = envelopeOf(
    [
      node("l1", { label: "One", color_by: "alpha" }),
      node("l2", { label: "Two", color_by: "beta" }),
      node("l3", { label: "Three", color_by: "gamma" }),
      node("l4", { label: "Four", color_by: "delta" }),
    ],
    [inside("l2", "l1"), inside("l3", "l2"), inside("l4", "l3")],
  );
  const result = scene(envelope, { max_depth: 4 });

  const tints = new Set();
  for (const key of ["l1", "l2", "l3", "l4"]) {
    assertEqual(boxOf(result, key).fill, NODE_PLAIN_FILL, `zone/${key}'s box is paper`);
    const header = headerOf(result, key);
    assert(header.fill !== NODE_PLAIN_FILL, `and zone/${key}'s header carries the tint`);
    tints.add(header.fill);
  }
  assertEqual(tints.size, 4, "four values, four tints, so the tint is really the value's");
  assertEqual(result.legend.rows.length, 4, "and the legend names them");

  // A node whose colour slot found nothing: the strip takes the
  // palette's `unset`, which is transparent, and the legend names the
  // absence in words. The box is still paper — the tint's absence and
  // the box's fill are two different facts.
  const withGap = envelopeOf(
    [node("l1", { label: "One", color_by: "alpha" }), node("l2", { label: "Two" })],
    [inside("l2", "l1")],
  );
  const gapped = scene(withGap);
  assertEqual(headerOf(gapped, "l2").fill, UNFILLED, "an absent colour tints nothing");
  assertEqual(boxOf(gapped, "l2").fill, NODE_PLAIN_FILL, "and the box is paper regardless");
  const unset = gapped.legend.rows.filter((row) => row.kind === "unset");
  assertEqual(unset.length, 1, "the legend carries the absence");
  assertEqual(unset[0].label, UNSET_LABEL, "in the palette's own words");

  // And a query that colours nothing has no legend at all, which is not
  // an empty one — the third answer a two-way implementation gets wrong.
  const plain = scene(envelopeOf([node("l1", { label: "One" })], []));
  assertEqual(plain.legend, null, "an uncoloured nest has no legend");
  assertEqual(headerOf(plain, "l1").fill, PLATE_FILL, "and its header is the plate's own ground");
});

check("leafLabelFallsBackToTheName", () => {
  const envelope = envelopeOf(
    [
      node("outer", { label: "Outer" }),
      node("named", { label: "Named", short: "N" }),
      node("bare", { label: "Bare" }),
    ],
    [inside("named", "outer"), inside("bare", "outer")],
  );
  const result = scene(envelope, { leaf_label: "short" });
  const labelOf = (key) =>
    result.marks.find((mark) => mark.key === address(key) && mark.class === CLASS_CONTAINER_LABEL)
      .text;

  assertEqual(labelOf("named"), "N", "a leaf with the slot uses it");
  assertEqual(labelOf("bare"), "Bare", "and a leaf whose slot found nothing keeps its name");
  // Never blank: an empty strip is indistinguishable from a rendering
  // fault, and the slot being absent is not a reason to stop naming the
  // thing.
  assert(labelOf("bare") !== "", "rather than going blank");
  // And the slot is a *leaf* label: a container keeps its own.
  assertEqual(labelOf("outer"), "Outer", "a container is not relabelled by leaf_label");
});

// --- The twin, and the emitter's contract ----------------------------

check("theTwinListsEveryNodeIncludingThoseBeyondMaxDepth", () => {
  // The twin is a description of the **answer**, not of the drawing. A
  // node the depth bound held back is still in the envelope, still a row,
  // and a twin that agreed with the picture here would be a twin that had
  // learned to see the drawing.
  const envelope = envelopeOf(
    [node("a"), node("b"), node("c"), node("d")],
    [inside("b", "a"), inside("c", "b"), inside("d", "c")],
  );
  const result = scene(envelope, { max_depth: 2 });
  const twin = twinFor(envelope);

  // The chip first, because the arithmetic below fails for this
  // mutation too and would leave this assertion correct and never run —
  // Task 8's own lesson about an assertion sitting under a count check.
  // zone/b is holding back c *and* d: a chip reading +1 would tell a
  // designer that opening it costs one box when it costs two, and the
  // fixture is a chain precisely so the two numbers differ.
  assertDeepEqual(
    result.chips,
    [{ key: address("b"), count: 2 }],
    "the chip counts every node under the container, not its children",
  );

  assertEqual(twin.nodes.rows.length, 4, "the twin has every node");
  assert(result.hidden.length > 0, "and the drawing is holding some back");
  assertEqual(
    result.drawn.length + result.hidden.length,
    twin.nodes.rows.length,
    "every row the twin has is a box in the picture or a node the depth bound held back",
  );
  assertDeepEqual(
    [...result.drawn, ...result.hidden].sort(),
    twin.nodes.rows.map((row) => row.key).sort(),
    "and they are the same nodes, by the same address",
  );


  // The footer counts the answer's edges that leave the picture, as
  // every renderer does — a nest draws no stub, and the count is still
  // the answer's.
  const leaving = envelopeOf(
    [node("a"), node("b")],
    [inside("b", "a"), inside("a", "gone")],
  );
  const outside = scene(leaving);
  assertEqual(outside.stubs.total, 1, "the edge to a node outside the picture is counted");
  const footer = footerFor(leaving, { outside: outside.stubs.total });
  assert(
    footer.text.includes("1 edge leads outside this picture"),
    `and the strip says so: ${footer.text}`,
  );
});

check("everyMarkIsAKindTheContractNamesAndCanBeDragged", () => {
  const envelope = envelopeOf(
    [
      node("a", { label: "Alpha", color_by: "one" }),
      node("b", { label: "Beta", color_by: "two" }),
      node("c", { label: "Gamma" }),
      node("d", { label: "Delta" }),
      node("lost", { label: "Lost" }),
    ],
    [
      inside("b", "a"),
      inside("c", "b"),
      inside("d", "c"),
      inside("a", "d"),
      inside("lost", "gone"),
    ],
  );
  const result = scene(envelope, { max_depth: 3 });
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

check("anEmptyAnswerIsAnEmptyScene", () => {
  const result = scene(envelopeOf([], []));
  assertDeepEqual(result.marks, [], "no marks");
  assertEqual(result.legend, null, "no legend, which is not an empty one");
  assertDeepEqual(result.cycles, [], "and nothing to say about cycles");
});

check("aThingContainedInTwoPlacesIsDrawnInOne", () => {
  // The metamodel carries no constraint against it, so a `nested` view
  // can meet it: `child` is part of both `left` and `right`. A box
  // cannot be drawn in two places, and a renderer that put it in both
  // lists would draw it twice, count it twice, and disagree with its own
  // twin about how many things the answer has.
  const envelope = envelopeOf(
    [node("left"), node("right"), node("child")],
    [inside("child", "right"), inside("child", "left")],
  );
  const result = scene(envelope);
  assertEqual(result.drawn.length, 3, "three nodes, three boxes");
  assertEqual(
    result.drawn.filter((key) => key === address("child")).length,
    1,
    "and the twice-contained one is drawn once",
  );
  assertEqual(
    result.marks.filter(
      (mark) => mark.key === address("child") && mark.class === CLASS_CONTAINER,
    ).length,
    1,
    "with one box on the canvas",
  );
  // The lowest address wins, so the choice is the answer's and not the
  // envelope's arrival order: the edges above are written right-then-left
  // on purpose.
  assert(contains(boxOf(result, "left"), boxOf(result, "child")), "inside the first by address");

  // And the twin still carries both relations, which is where an answer
  // this vocabulary cannot draw belongs.
  assertEqual(twinFor(envelope).edges.rows.length, 2, "the twin keeps both containments");
});

check("aQueryWithNoContainmentDrawsOneFlatRow", () => {
  // The catalogue refuses a view whose query draws no edges of
  // `contain_via`, so this is the shape of a document the server would
  // not have saved. It draws every node at the top level rather than
  // nesting everything or nothing, which is what an implementation that
  // read *every* edge as containment would do.
  const envelope = envelopeOf(
    [node("a"), node("b")],
    [{ id: "e1", type: "connects_to", source: "id-a", target: "id-b" }],
  );
  const result = scene(envelope);
  assertEqual(result.drawn.length, 2, "both nodes are drawn");
  assert(!contains(boxOf(result, "b"), boxOf(result, "a")), "and neither is inside the other");
  assertDeepEqual(result.orphans, [], "a relation that is not containment leaves no orphan");
});

check("theControlsTeachWhatTheCatalogueDoesNot", () => {
  assertEqual(RENDERER, "nested", "the module names the renderer it is");
  const depth = controlNamed(CONTROLS, PARAM_MAX_DEPTH);
  assert(depth !== null, "max_depth has a control");
  // The sentence a designer needs is that the bound is about the
  // drawing: somebody who reads it as "how much was fetched" will raise
  // it expecting more content and learn nothing from the picture.
  assert(depth.tooltip.includes("chip"), "whose sentence names the chip");
  assert(
    depth.tooltip.includes("without asking the server again"),
    "and says the expansion costs no round trip",
  );
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
  console.error(`${failures} nested renderer check(s) failed`);
  process.exit(1);
}
console.log("nested.js: all checks passed");
