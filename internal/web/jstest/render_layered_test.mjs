// The harness for the `layered` renderer:
// internal/web/static/render/layered.js, over the drawing vocabulary it
// shares with the other five (internal/web/static/render/marks.js,
// render/controls.js) and the frame's own sentences
// (render/scene.js).
//
// What this layer covers that no Go test can, and what is this
// renderer's alone.
//
// **That a broken edge is marked and not hidden.** This renderer's
// consumption note says "expected mostly acyclic" and a ranked drawing
// of a cyclic graph is only possible because something ran an edge
// backwards. A picture that silently reversed an arrow would show a
// prerequisite chain the wrong way round and look perfectly fine doing
// it, which is the whole class of defect this sub-project is built
// against. So the arrowhead is asserted to be at the relation's **true**
// target, the double-slash is asserted to be on the line, and the
// frame's sentence is asserted to count it.
//
// **That the frame's sentence stays a drawing report.** It does not name
// the nodes and never says "unreachable": sub-project 6 owns the real
// answer, and a renderer that guessed at it would be publishing a result
// the product has not computed.
//
// **That the two ranking policies produce two captions.** One fixture
// cannot tell them apart, so there are two, and the mutation that
// captions everything with its index turns exactly one of them red.
//
// **That the picture and the text twin describe one answer.** Task 5
// built the twin before any renderer for this.
//
// Run directly: `node internal/web/jstest/render_layered_test.mjs`.
// internal/web/static_appjs_browser_test.go shells out to it too.

import { addressOf } from "../static/address.js";
import { MARK_ELEMENTS, MARK_LABEL, MARK_ORIGINS, footerFor, joinEdges } from "../static/render/scene.js";
import { twinFor } from "../static/render/twin.js";
import { layoutGraph } from "../static/layout/engine.js";
import { controlNamed } from "../static/render/controls.js";
import {
  ABSENT_DASH,
  CLASS_ARROW,
  CLASS_BAND_CAPTION,
  CLASS_BAND_RULE,
  CLASS_EDGE,
  CLASS_NODE,
  CLASS_NODE_ABSENT,
  CLASS_NODE_LABEL,
  CLASS_REVERSED,
  CLASS_STUB,
  CLASS_STUB_RING,
  LABEL_SIZE,
  NODE_PLAIN_FILL,
  REVERSAL_SIZE,
  REVERSAL_TEXT,
  borderPoint,
  boxFor,
} from "../static/render/marks.js";
import {
  ALIGN_CENTER,
  ALIGN_END,
  ALIGN_START,
  CONTROLS,
  DIRECTION_LR,
  DIRECTION_TB,
  PARAM_ALIGN,
  PARAM_RANK_BY,
  PARAM_RANK_DIRECTION,
  RENDERER,
  UNRANKED_CAPTION,
  layeredLayoutRequest,
  layeredScene,
} from "../static/render/layered.js";

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
  if (!(Math.abs(actual - expected) < 1e-6)) {
    throw new Error(`${message}: got ${actual}, want ${expected}`);
  }
}

function assertDeepEqual(actual, expected, message) {
  const a = JSON.stringify(actual);
  const b = JSON.stringify(expected);
  if (a !== b) throw new Error(`${message}: got ${a}, want ${b}`);
}

// --- Fixtures --------------------------------------------------------

function node(key, attrs = {}, extra = {}) {
  return { id: `id-${key}`, type: "quest", key, name: `Quest ${key}`, attrs, ...extra };
}

function edge(source, target, extra = {}) {
  return { id: `e-${source}-${target}`, type: "unlocks", source, target, ...extra };
}

function envelopeOf(nodes, edges = [], extra = {}) {
  return {
    nodes,
    edges,
    stats: { nodes: nodes.length, edges: edges.length, max_depth_reached: 2, duration_ms: 4 },
    truncated: { nodes: false, edges: false, depth: false },
    stale: [],
    ...extra,
  };
}

// laidOut runs the real engine over the renderer's own request, so every
// coordinate below is one the product would actually produce. A
// hand-written placement list would let a fixture choose the arrangement
// that made a geometric claim true.
function laidOut(envelope, params = {}) {
  const request = layeredLayoutRequest(envelope, params);
  return layoutGraph(request.nodes, request.edges, request.options);
}

function scene(envelope, params = {}) {
  return layeredScene(envelope, laidOut(envelope, params), params);
}

function marksOfClass(result, className) {
  return result.marks.filter((mark) => mark.class === className);
}

function address(key) {
  return addressOf({ type: "quest", key });
}

function boxOf(result, key) {
  const found = result.marks.find(
    (mark) =>
      mark.key === address(key) && (mark.class === CLASS_NODE || mark.class === CLASS_NODE_ABSENT),
  );
  if (!found) throw new Error(`no box drawn for quest/${key}`);
  return found;
}

// centreOf is a drawn box's centre, which is what the scene's own
// arithmetic works in and what a mark reports the corner of.
function centreOf(result, key) {
  const box = boxOf(result, key);
  return { x: box.x + box.w / 2, y: box.y + box.h / 2, width: box.w, height: box.h };
}

function bandOf(result, key) {
  const centre = centreOf(result, key);
  for (const band of result.bands) {
    if (Math.abs(band.centre - centre.y) < 1e-6 || Math.abs(band.centre - centre.x) < 1e-6) {
      return band;
    }
  }
  throw new Error(`quest/${key} sits in no band`);
}

// --- The edge the ranking had to break --------------------------------

// The three-node cycle every test below leans on: a unlocks b unlocks c
// unlocks a. Ranked by the edges, the walk breaks c -> a, so that edge
// runs backwards up the picture and the other two run down it.
function cycleOfThree() {
  return envelopeOf(
    [node("a", { label: "Alpha" }), node("b", { label: "Beta" }), node("c", { label: "Gamma" })],
    [edge("id-a", "id-b"), edge("id-b", "id-c"), edge("id-c", "id-a")],
  );
}

check("aReversedEdgeKeepsItsTrueArrowhead", () => {
  const result = scene(cycleOfThree(), {});

  // The ranking is the longest path with the cycle broken, so the
  // backwards edge is c -> a.
  assertEqual(result.against, 1, "one edge runs against the ranking");
  assert(result.cyclic, "and the walk that found it says the graph has a cycle");

  const a = centreOf(result, "a");
  const c = centreOf(result, "c");
  assert(bandOf(result, "c").index > bandOf(result, "a").index, "c is ranked below a");

  // The head is at quest/a — the relation's real target — and not at c,
  // which is where it would be if the endpoints had been sorted to make
  // the line run downwards.
  const heads = marksOfClass(result, CLASS_ARROW).filter((mark) => mark.source === address("a"));
  assertEqual(heads.length, 2, "the chevron at quest/a is two strokes");
  const tip = borderPoint(a, c);
  for (const head of heads) {
    assertClose(head.x1, tip.x, "the head's tip is on quest/a's border, facing quest/c");
    assertClose(head.y1, tip.y, "and on the same border point");
  }
  // And it points **up** the picture, which is the whole of "reversed".
  // An implementation that sorted the endpoints to make every line run
  // downwards would draw this head at quest/c facing down, and the
  // chevron's strokes trail away from the tip in the direction the line
  // came from.
  for (const head of heads) {
    assert(head.y2 > head.y1, "and it faces up the picture, back towards its true source");
  }
  // Every drawn edge keeps its head, so nothing was dropped or doubled
  // in the reversing: three edges, two strokes each.
  assertEqual(marksOfClass(result, CLASS_ARROW).length, 6, "all three edges are still headed");

  // And the line itself carries the break: one double-slash, on the one
  // edge that runs against the ranking, at its middle.
  //
  // **It is a glyph and not a pair of strokes, which is a correction
  // made in a browser**: two nine-unit lines inside the zoomed world
  // render at 0.43 px on the picture this mark exists for. See
  // render/marks.js's reversalMarks, and the check below, which is the
  // one that would have caught it.
  const slashes = marksOfClass(result, CLASS_REVERSED);
  assertEqual(slashes.length, 1, "the double-slash is one mark and there is one of it");
  assertEqual(slashes[0].text, REVERSAL_TEXT, "and it is a double slash");
  const tail = borderPoint(c, a);
  const mid = { x: (tail.x + tip.x) / 2, y: (tail.y + tip.y) / 2 };
  assertClose(slashes[0].x, mid.x, "it sits at the broken line's middle");
  assertClose(slashes[0].y, mid.y, "in both axes");
  assertEqual(slashes[0].anchor, "middle", "centred on it horizontally");
  assertEqual(slashes[0].baseline, "middle", "and vertically, so the middle is the glyph's and not its corner's");
});

// **The mark a reader can actually find**, which is the whole of this
// renderer's negative half: a layered picture that silently reverses an
// arrow is the wrong picture that looks right.
//
// A hand check on a hundred-step progression fitted the drawing at
// k = 0.058 and measured the two strokes at 0.43 × 0.43 CSS pixels — at
// the zoom where the progression reads as a progression, the mark was
// invisible. The property that fixes it is not "the mark is bigger": it
// is that the mark is drawn by the one mechanism in this front end that
// holds a size in *screen* terms while the world scales, which is the
// label band (mst-canvas.js's labelScale / labelFontSize). So the
// assertion is that the mark is a label at all — a line, of any length,
// scales with the picture and disappears again.
check("theReversalMarkKeepsItsSizeWhileThePictureShrinks", () => {
  const result = scene(cycleOfThree(), {});
  const slashes = marksOfClass(result, CLASS_REVERSED);
  assertEqual(slashes.length, 1, "one edge runs against the ranking");
  const slash = slashes[0];
  assertEqual(slash.kind, MARK_LABEL, "the mark is a label, which is what the zoom band re-sizes");
  assert(slash.size > 0, "and it declares the size the band re-derives from");
  assert(slash.size > LABEL_SIZE, "larger than a name: it is not a label *on* anything");

  // What the canvas then does with it — the actual screen size at the
  // zoom the hand check measured — is asserted in
  // internal/web/jstest/canvas_test.mjs, which owns the label band and
  // can import it without dragging a DOM into this harness.

  // And it is legible over the edges it sits on, which is what a halo is
  // for: the mark's whole job is to be found among four hundred lines.
  assert(typeof slash.halo === "string" && slash.halo !== "", "the glyph carries a halo");
  assert(slash.haloWidth > 0, "of a real width");

  // **And nothing is painted over it.** Scene order is document order is
  // paint order, and every node label in this picture goes into the same
  // layer as this mark. Emitted where it is computed, it landed under a
  // hundred names — on the hundred-step progression the browser check
  // used, `elementFromPoint` at the mark's own centre answered a
  // `node-label`, not the mark. So it is the last mark in the scene.
  const at = result.marks.indexOf(slash);
  assertEqual(at, result.marks.length - 1, "the reversal mark is the last mark in the scene");
  const labelsAfter = result.marks.slice(at + 1).filter((mark) => mark.kind === MARK_LABEL);
  assertEqual(labelsAfter.length, 0, "so no label is painted over it");
});

check("aReversalMarkStandsStillDuringADrag", () => {
  // The same decision render/marks.js took for an edge label, carried
  // one step along rather than re-argued: an edge whose one end moved
  // has a new middle, which no translation of a pair of strokes can
  // produce. So the mark names no endpoint, is not carried by the drag
  // layer, and is redrawn with the picture on the drop that writes. A
  // mark that named one end would ride half a drag and land wrong.
  const result = scene(cycleOfThree(), {});
  const found = marksOfClass(result, CLASS_REVERSED);
  assert(found.length > 0, "there is a mark to check");
  for (const slash of found) {
    assertEqual(slash.source, undefined, "a double-slash belongs to no node");
    assertEqual(slash.target, undefined, "at either end");
    assertEqual(slash.key, undefined, "and to no entity, so the drag layer never carries it");
  }
});

check("aCycleIsCountedInTheFrame", () => {
  // Three edges running against the ranking, which is §4.4's own
  // sentence made true: a chain a -> b -> c -> d with three relations
  // pointing back up it.
  const envelope = envelopeOf(
    [node("a"), node("b"), node("c"), node("d")],
    [
      edge("id-a", "id-b"),
      edge("id-b", "id-c"),
      edge("id-c", "id-d"),
      edge("id-d", "id-a"),
      edge("id-d", "id-b"),
      edge("id-c", "id-a"),
    ],
  );
  const result = scene(envelope, {});
  assertEqual(result.against, 3, "three edges run backwards through the ranking");
  assert(result.cyclic, "and the graph really has a cycle");
  assertEqual(marksOfClass(result, CLASS_REVERSED).length, 3, "each of the three carries a slash");

  const footer = footerFor(envelope, { against: result.against, cyclic: result.cyclic });
  assert(
    footer.text.includes("3 edges run against the ranking; this graph has a cycle"),
    `the strip says so in the frame's own words: ${footer.text}`,
  );

  // The count and the flag are two facts, not one, and the frame says
  // the second only when it holds. An edge that runs backwards through a
  // *numeric* ranking — a relation from level 5 to level 2 — is a
  // drawing fact about a graph that may be perfectly acyclic, and a
  // footer that appended "this graph has a cycle" to it would be
  // reporting an analysis nobody ran.
  const acyclic = footerFor(envelope, { against: 3, cyclic: false });
  assert(
    acyclic.text.includes("3 edges run against the ranking") &&
      !acyclic.text.includes("cycle"),
    `an acyclic picture with backwards edges says only the first half: ${acyclic.text}`,
  );

  // And a clean picture says nothing at all: there is no clause for the
  // absence of a thing, which is render/scene.js's own first rule.
  const clean = scene(envelopeOf([node("a"), node("b")], [edge("id-a", "id-b")]), {});
  assertEqual(clean.against, 0, "nothing runs backwards");
  assertEqual(clean.cyclic, false, "and there is no cycle");
  const quiet = footerFor(envelope, { against: clean.against, cyclic: clean.cyclic });
  assert(!quiet.text.includes("ranking"), `and the strip is silent: ${quiet.text}`);
});

check("theCycleSentenceIsNotAnAnalysisClaim", () => {
  // This is a drawing artefact honestly reported. Sub-project 6 owns the
  // real answer — which nodes, which cycle, what is unreachable — and a
  // renderer that guessed would publish a result the product has not
  // computed, in the one place a designer would believe it.
  const envelope = cycleOfThree();
  const result = scene(envelope, {});
  const footer = footerFor(envelope, { against: result.against, cyclic: result.cyclic });

  for (const entity of envelope.nodes) {
    assert(!footer.text.includes(entity.name), `the sentence does not name ${entity.name}`);
    assert(
      !footer.text.includes(entity.attrs.label),
      `nor its label ${entity.attrs.label}`,
    );
    assert(!footer.text.includes(`"${entity.key}"`), `nor its key ${entity.key}`);
  }
  assert(!footer.text.includes("unreachable"), "and it never says unreachable");
  assert(!footer.text.includes("dead"), "nor calls anything dead");
  assert(footer.text.includes("cycle"), "while still saying the thing it does know");
});

// --- The two ranking policies ----------------------------------------

// The chain three fixtures below share: a -> b -> c, with a declared
// number field on each. Ranked by the edges it is three bands captioned
// 0, 1, 2; ranked by `level` it is three bands captioned 10, 20, 30.
function chainWithLevels() {
  return envelopeOf(
    [
      node("a", { label: "Alpha", level: 10 }),
      node("b", { label: "Beta", level: 20 }),
      node("c", { label: "Gamma", level: 30 }),
    ],
    [edge("id-a", "id-b"), edge("id-b", "id-c")],
  );
}

check("layerCaptionsAreTheIndexForRankByEdges", () => {
  const result = scene(chainWithLevels(), { rank_by: "edges", layer_labels: true });
  assertDeepEqual(
    result.bands.map((band) => band.caption),
    ["0", "1", "2"],
    "ranking by the edges captions each band with its index",
  );
  assertDeepEqual(
    marksOfClass(result, CLASS_BAND_CAPTION).map((mark) => mark.text),
    ["0", "1", "2"],
    "and the captions reach the picture",
  );
  assertEqual(marksOfClass(result, CLASS_BAND_RULE).length, 3, "with a muted rule each");
});

check("layerCaptionsAreTheFieldValueForANumericRankBy", () => {
  // The pair is what makes the mutation visible: an implementation that
  // captions every band with its index passes the test above and fails
  // this one, which is the whole reason `rank_by` takes a field at all.
  const result = scene(chainWithLevels(), { rank_by: "level", layer_labels: true });
  assertDeepEqual(
    result.bands.map((band) => band.caption),
    ["10", "20", "30"],
    "a numeric rank_by captions each band with the field's value",
  );
  assertDeepEqual(
    marksOfClass(result, CLASS_BAND_CAPTION).map((mark) => mark.text),
    ["10", "20", "30"],
    "and those are the words on the picture",
  );
  assertDeepEqual(
    result.bands.map((band) => band.value),
    [10, 20, 30],
    "and the band knows the number it was made from",
  );
});

check("layerLabelsOffDrawsNoRuleAndNoCaption", () => {
  // The bands are still there — a layered picture is bands — and nothing
  // names them. Without this the parameter would be a knob that changed
  // nothing, which is the failure `cluster_by` gets its own test for.
  const result = scene(chainWithLevels(), { rank_by: "level" });
  assertEqual(result.bands.length, 3, "the ranking still happened");
  assertEqual(marksOfClass(result, CLASS_BAND_RULE).length, 0, "and no rule is drawn");
  assertEqual(marksOfClass(result, CLASS_BAND_CAPTION).length, 0, "and nothing is captioned");
});

check("anAbsentNumericRankGoesToATrailingUnrankedBand", () => {
  // A node whose rank field is missing is not a starting point. At rank
  // zero it would read as one — the top of a progression is a claim
  // about content, and this is a missing value.
  const envelope = envelopeOf(
    [
      node("a", { label: "Alpha", level: 10 }),
      node("b", { label: "Beta", level: 20 }),
      node("c", { label: "Gamma" }),
    ],
    [edge("id-a", "id-b"), edge("id-b", "id-c")],
  );
  const result = scene(envelope, { rank_by: "level", layer_labels: true });

  const bands = result.bands;
  assertEqual(bands.length, 3, "two ranked bands and one for the absence");
  const trailing = bands[bands.length - 1];
  assert(trailing.unranked, "the last band is the unranked one");
  assertEqual(trailing.caption, UNRANKED_CAPTION, "and it is captioned, not left blank");
  assertEqual(trailing.count, 1, "carrying the one node with no value");
  assertEqual(
    bands.filter((band) => band.unranked).length,
    1,
    "and there is exactly one such band",
  );

  // It is last and it is not first, asserted separately, because "last"
  // is true of a one-band picture that put it at zero.
  assertEqual(bandOf(result, "c").index, bands.length - 1, "quest/c is in the trailing band");
  assert(bandOf(result, "c").index > 0, "and never at rank zero");
  assertEqual(bandOf(result, "a").index, 0, "while the lowest value is rank zero");
  assert(
    bandOf(result, "c").centre > bandOf(result, "a").centre,
    "and the trailing band is drawn after every ranked one",
  );

  // The absence carries a second mark, which is render/marks.js's one
  // spelling for it: this box is missing something the picture needed.
  assertEqual(boxOf(result, "c").dash, ABSENT_DASH, "the unranked box is dashed");
  assertEqual(boxOf(result, "c").class, CLASS_NODE_ABSENT, "and says so in its class");
  assertEqual(boxOf(result, "a").dash, undefined, "while a ranked box is not");
  assertEqual(boxOf(result, "a").fill, NODE_PLAIN_FILL, "and every box is paper: this renderer has no colour");
  assertEqual(result.unranked, 1, "and the absence is counted as well as drawn");

  // No band is captioned "0" by accident of the ranking: the captions
  // are the values, so a reader cannot mistake the trailing band for one.
  assert(
    !bands.some((band) => band.caption === "0"),
    "and no band claims to be rank zero in a picture ranked by a field",
  );
});

// --- The two axes and the alignment ----------------------------------

check("rankDirectionLRTransposesTheScene", () => {
  const envelope = chainWithLevels();
  const tb = scene(envelope, { rank_direction: DIRECTION_TB });
  const lr = scene(envelope, { rank_direction: DIRECTION_LR });

  const tbCentres = ["a", "b", "c"].map((key) => centreOf(tb, key));
  const lrCentres = ["a", "b", "c"].map((key) => centreOf(lr, key));

  // Top to bottom: the rank axis is y, and it advances.
  assert(tbCentres[0].y < tbCentres[1].y && tbCentres[1].y < tbCentres[2].y, "TB ranks downwards");
  // Left to right: the rank axis is x, and the y coordinates no longer
  // separate the ranks at all.
  assert(lrCentres[0].x < lrCentres[1].x && lrCentres[1].x < lrCentres[2].x, "LR ranks rightwards");
  assertClose(lrCentres[0].y, lrCentres[1].y, "and LR puts a one-node rank at the same height");
  assertClose(tbCentres[0].x, tbCentres[1].x, "as TB does across");

  // And the bands themselves are on the other axis, which is the claim a
  // renderer that only changed dagre's rankdir would fail: the band
  // centres have to be read against x in LR and y in TB.
  for (const band of tb.bands) {
    const members = tbCentres.filter((centre) => Math.abs(centre.y - band.centre) < 1e-6);
    assertEqual(members.length, 1, "each TB band's centre is a y coordinate");
  }
  for (const band of lr.bands) {
    const members = lrCentres.filter((centre) => Math.abs(centre.x - band.centre) < 1e-6);
    assertEqual(members.length, 1, "each LR band's centre is an x coordinate");
  }
});

check("alignPositionsWithinTheBand", () => {
  // Three values, all asserted: two of the three would pass a test
  // written for one, since a picture whose bands happen to be the same
  // width answers all three the same way. So the fixture is deliberately
  // lopsided — one node over three.
  const envelope = envelopeOf(
    [node("a", { label: "Alpha" }), node("b"), node("c"), node("d")],
    [edge("id-a", "id-b"), edge("id-a", "id-c"), edge("id-a", "id-d")],
  );
  const extentOf = (result, keys) => {
    let min = Infinity;
    let max = -Infinity;
    for (const key of keys) {
      const centre = centreOf(result, key);
      min = Math.min(min, centre.x - centre.width / 2);
      max = Math.max(max, centre.x + centre.width / 2);
    }
    return { min, max };
  };

  const wide = ["b", "c", "d"];
  for (const align of [ALIGN_START, ALIGN_CENTER, ALIGN_END]) {
    const result = scene(envelope, { align });
    const top = extentOf(result, ["a"]);
    const bottom = extentOf(result, wide);
    if (align === ALIGN_START) {
      assertClose(top.min, bottom.min, "start puts the lone node flush with the picture's near side");
    } else if (align === ALIGN_END) {
      assertClose(top.max, bottom.max, "end puts it flush with the far side");
    } else {
      assertClose(
        (top.min + top.max) / 2,
        (bottom.min + bottom.max) / 2,
        "center centres it in the picture",
      );
    }
  }

  // The three really are three: a renderer that honoured one spelling
  // and ignored the other two would satisfy its own assertion above.
  const at = (align) => centreOf(scene(envelope, { align }), "a").x;
  assert(at(ALIGN_START) < at(ALIGN_CENTER), "start is nearer than center");
  assert(at(ALIGN_CENTER) < at(ALIGN_END), "and center is nearer than end");
});

// --- The rest of the negative half -----------------------------------

check("anEdgeThatLeavesThePictureIsAStub", () => {
  const envelope = envelopeOf(
    [node("a"), node("b")],
    [edge("id-a", "id-b"), edge("id-b", "id-outside"), edge("id-outside", "id-elsewhere")],
  );
  const result = scene(envelope, {});
  assertEqual(result.stubs.total, 2, "both edges leaving the picture are counted");
  assertEqual(result.stubs.drawn, 1, "one has a known end to leave from");
  assertEqual(result.stubs.anchorless, 1, "and one has neither end in the picture");
  assertEqual(marksOfClass(result, CLASS_STUB).length, 1, "so one stub line is drawn");
  assertEqual(marksOfClass(result, CLASS_STUB_RING).length, 1, "with its hollow ring");

  const footer = footerFor(envelope, { outside: result.stubs.total });
  assert(
    footer.text.includes("2 edges lead outside this picture"),
    `and the strip counts the answer's edges and not the drawing's: ${footer.text}`,
  );
});

check("aSelfRelationIsCountedNotDrawn", () => {
  // A straight line from a box to the same box has no length, and this
  // vocabulary has no curved mark: Task 7's rule is that the renderer
  // adding one adds the drag layer's reshaping answer with it. Dropping
  // it silently would be a picture and a twin disagreeing about an
  // answer they both received.
  const envelope = envelopeOf(
    [node("a"), node("b")],
    [edge("id-a", "id-b"), edge("id-a", "id-a")],
  );
  const result = scene(envelope, {});
  assertEqual(result.loops, 1, "the self-relation is counted");
  assertEqual(marksOfClass(result, CLASS_EDGE).length, 1, "and only the drawable edge is a line");
  assertEqual(result.against, 0, "a relation to itself runs against no ranking");
});

check("aNodeWithNoPositionIsNamedRatherThanDrawnAtTheOrigin", () => {
  const envelope = envelopeOf([node("a"), node("b")], [edge("id-a", "id-b")]);
  const layout = laidOut(envelope, {});
  // The layout answers for one node only, which is what a budget
  // fallback or a partial re-run looks like from here.
  const partial = { placements: layout.placements.filter((row) => row.key === address("a")) };
  const result = layeredScene(envelope, partial, {});
  assertDeepEqual(result.unplaced, [address("b")], "the unplaced node is named");
  assertEqual(
    result.marks.filter((mark) => mark.class === CLASS_NODE || mark.class === CLASS_NODE_ABSENT)
      .length,
    1,
    "a box at the origin is a position nobody chose",
  );
  assertEqual(marksOfClass(result, CLASS_EDGE).length, 0, "and its edge is not drawn either");
  assertEqual(result.stubs.total, 0, "nor called an edge that leaves the picture, because it is not");
});

check("aTruncatedAnswerDrawsWhatAnUntruncatedOneDraws", () => {
  // The envelope says a cap was hit; it does not say which node lost a
  // neighbour. A mark claiming to know would invent the one thing the
  // server declined to measure, and the frame owns the band.
  const nodes = [node("a"), node("b")];
  const edges = [edge("id-a", "id-b")];
  const plain = scene(envelopeOf(nodes, edges), {});
  const capped = scene(
    envelopeOf(nodes, edges, { truncated: { nodes: true, edges: true, depth: false } }),
    {},
  );
  assertDeepEqual(capped.marks, plain.marks, "a truncated answer draws exactly what a clean one does");
});

check("anEmptyAnswerIsAnEmptyScene", () => {
  const result = scene(envelopeOf([], []), {});
  assertDeepEqual(result.marks, [], "no marks");
  assertDeepEqual(result.bands, [], "no bands");
  assertEqual(result.legend, null, "and no legend, which is not an empty one");
  assertEqual(result.cyclic, false, "and nothing to say about cycles");
});

// --- The joins -------------------------------------------------------

check("theLayoutIsAskedForTheBoxThatIsDrawn", () => {
  // One measurement, used twice. A renderer that measured for the
  // drawing only would draw a box into a hole reserved for a different
  // one, at every zoom, forever — and no test of either half alone can
  // see it.
  const envelope = envelopeOf(
    [node("a", { label: "Alpha" }), node("b", { label: "A very much longer name indeed" })],
    [edge("id-a", "id-b")],
  );
  const request = layeredLayoutRequest(envelope, {});
  const result = layeredScene(envelope, layoutGraph(request.nodes, request.edges, request.options), {});
  for (const key of ["a", "b"]) {
    const asked = request.nodes.find((entry) => entry.key === key);
    const drawn = boxOf(result, key);
    assertEqual(drawn.w, asked.width, `the width laid out for quest/${key} is the one drawn`);
    assertEqual(drawn.h, asked.height, `and so is the height`);
  }
  // And the measurement really varies with the label, so the assertion
  // above is not two constants agreeing.
  assert(
    boxOf(result, "b").w > boxOf(result, "a").w,
    "a longer name is a wider box, so the join is over a real measurement",
  );
  assertEqual(boxOf(result, "a").w, boxFor("Alpha").width, "and it is the shared vocabulary's own");
});

check("theTwinAndTheSceneAgreeOnNodeCount", () => {
  // A picture and a twin that disagree is a view telling two stories
  // about one answer, and it is the defect the plan's ordering exists to
  // catch. Task 5 built the twin before any renderer for this.
  const envelope = envelopeOf(
    [
      node("a", { label: "Alpha", level: 10 }),
      node("b", { label: "Beta", level: 20 }),
      node("c", { label: "Gamma" }),
    ],
    [
      edge("id-a", "id-b"),
      edge("id-b", "id-c"),
      edge("id-c", "id-a"),
      edge("id-a", "id-a"),
      edge("id-b", "id-outside"),
    ],
  );
  const result = scene(envelope, { rank_by: "level" });
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
  assertEqual(
    marksOfClass(result, CLASS_EDGE).length + result.stubs.total + result.loops,
    twin.edges.rows.length,
    "every edge row is a line in the picture, an edge that leaves it, or a loop this vocabulary cannot draw",
  );

  // The words agree too: a box's label is character for character the
  // twin's `label` cell, because both call the palette's own labelFor.
  const labelCell = twin.nodes.columns.findIndex((column) => column.key === "label");
  for (const row of twin.nodes.rows) {
    const label = result.marks.find(
      (mark) => mark.key === row.key && mark.class === CLASS_NODE_LABEL,
    );
    if (!label) continue;
    assertEqual(label.text, row.cells[labelCell].text, `the box and the row agree on ${row.key}`);
  }

  // And the reversal marks are not nodes: a scene that counted its own
  // decorations as boxes would agree with the twin by accident.
  assert(marksOfClass(result, CLASS_REVERSED).length > 0, "the fixture really has a broken edge");
});

check("everyMarkIsAKindTheContractNamesAndCanBeDragged", () => {
  const envelope = envelopeOf(
    [node("a", { label: "Alpha", level: 10 }), node("b", { label: "Beta" }), node("c", {}, { ambiguous: true })],
    [edge("id-a", "id-b"), edge("id-b", "id-c"), edge("id-c", "id-a"), edge("id-a", "id-outside")],
  );
  const result = scene(envelope, { rank_by: "level", layer_labels: true, align: ALIGN_CENTER });
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
  // The catalogue's own text says what a *query* must produce; a control
  // says what the knob does to the picture. For `rank_by` that is the
  // caption on every band, which is the entire reason a designer would
  // reach for the field form — and there is nowhere else to learn it.
  assertEqual(RENDERER, "layered", "the module names the renderer it is");
  const rankBy = controlNamed(CONTROLS, PARAM_RANK_BY);
  assert(rankBy !== null, "rank_by has a control");
  assert(rankBy.tooltip.includes("caption"), "whose sentence is about the captions");
  assert(rankBy.tooltip.includes("unranked"), "and about where a missing value goes");

  const direction = controlNamed(CONTROLS, PARAM_RANK_DIRECTION);
  assertDeepEqual(direction.values, [DIRECTION_TB, DIRECTION_LR], "the enum offers the catalogue's spellings");
  const align = controlNamed(CONTROLS, PARAM_ALIGN);
  assertDeepEqual(align.values, [ALIGN_START, ALIGN_CENTER, ALIGN_END], "and so does align");

  // A control is a description, and a surface that could edit one is a
  // surface that could teach a designer something the renderer does not
  // do.
  const before = align.tooltip;
  try {
    align.tooltip = "anything else";
  } catch {
    // A frozen object in strict mode throws, which is the same answer.
  }
  assertEqual(align.tooltip, before, "and it is frozen");
});

check("joinEdgesIsTheOnlyPlaceTheEndpointRuleLives", () => {
  // The rule that an edge's endpoints are not guaranteed to be among the
  // nodes belongs to render/scene.js, once, for all six. This asserts
  // the renderer consumes both halves of its answer rather than
  // reimplementing the join — a second implementation is the drift the
  // shared module exists to prevent.
  const envelope = envelopeOf(
    [node("a"), node("b")],
    [edge("id-a", "id-b"), edge("id-a", "id-outside")],
  );
  const { drawn, stubs } = joinEdges(envelope.nodes, envelope.edges);
  const result = scene(envelope, {});
  assertEqual(marksOfClass(result, CLASS_EDGE).length, drawn.length, "the drawn edges are the join's");
  assertEqual(result.stubs.total, stubs.length, "and so are the ones that leave");
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
  console.error(`${failures} layered renderer check(s) failed`);
  process.exit(1);
}
console.log("layered.js: all checks passed");
