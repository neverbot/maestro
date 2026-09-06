// The harness for the canvas: internal/web/static/components/mst-canvas.js
// (the emitter, the pan/zoom transform and the drag layer) and the
// emitter contract it reads out of internal/web/static/render/scene.js.
//
// What this layer covers that no Go test can.
//
// **That one mark becomes one element and nothing more.** The emitter is
// the joint the whole sub-project's verification turns on: the six
// renderers are pure functions a mutation turns red, and they are only
// worth testing that way if the thing that draws their answer adds
// nothing of its own. So the assertions here are about the *contract* —
// which attribute a field becomes, which fields are dropped, which
// element a kind is — and never about how a picture looks.
//
// **That paint order is what the layers say.** SVG paints in document
// order. The fixtures below push labels *before* their nodes on purpose,
// so the assertion is that the layering does the work rather than that
// the caller happened to order its marks well.
//
// **That a drag touches the dragged subtree and nothing else.** This is
// the one performance property in the sub-project that a Node harness
// can actually measure, because "how many elements changed" is a count
// and not a frame rate. The stub's mutation log is the instrument; the
// fixture is 200 nodes, so a full re-render is unmissable rather than a
// near miss.
//
// **That a game's words are characters.** Task 5 answered this for the
// text twin by asserting *placement*, because Lit does the escaping.
// That argument does not transfer and is not reused: there is no
// framework on this path. `createElementNS`/`setAttribute`/`textContent`
// never parse markup, so the property is that no game string reaches
// anything but a Text node, plus SVG's own three hazards, which
// internal/web/static_canvas_test.go holds by shape.
//
// Run directly: `node internal/web/jstest/canvas_test.mjs`.
// internal/web/static_appjs_browser_test.go shells out to it too.

import { install, walk, SVG_NS } from "./svg_dom.mjs";

const dom = install();

const {
  ENDPOINT_BOTH,
  ENDPOINT_SOURCE,
  ENDPOINT_TARGET,
  LAYER_DRAG,
  LAYER_EDGES,
  LAYER_IMAGE,
  LAYER_LABELS,
  LAYER_NODES,
  MARK_ATTRIBUTES,
  MARK_DISC,
  MARK_IMAGE,
  MARK_LABEL,
  MARK_LINE,
  MARK_RECT,
  isDrawableHref,
  joinEdges,
} = await import("../static/render/scene.js");

const {
  CANVAS_CSS,
  adoptCanvasStyles,
  CLASS_PANELS,
  CLASS_ROOT,
  CLASS_SURFACE,
  CLASS_SURFACE_HOST,
  DEFAULT_LABEL_SIZE,
  LABEL_SCALE_MAX,
  LABEL_SCALE_MIN,
  MstCanvas,
  emitScene,
  labelFontSize,
  labelScale,
} = await import("../static/components/mst-canvas.js");

const { ACTION_RETRY_LAYOUT, BANNER_LAYOUT_BUDGET, runWithBudget } = await import(
  "../static/layout/budget.js"
);

const { HATCH_FILL, HATCH_GROUND, HATCH_PATTERN_ID, HATCH_STROKE, fillFor } = await import(
  "../static/palette.js"
);
const { CLASS_REVERSED, REVERSAL_SIZE } = await import("../static/render/marks.js");

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

// assertSame compares identity and describes an element by what it is
// rather than by JSON, which a DOM node's parent links make circular.
function assertSame(actual, expected, message) {
  if (actual !== expected) {
    throw new Error(`${message}: got ${describe(actual)}, want ${describe(expected)}`);
  }
}

function describe(node) {
  if (node === null || node === undefined) return String(node);
  if (typeof node !== "object" || typeof node.tagName !== "string") return JSON.stringify(node);
  const className = node.getAttribute("class");
  return `<${node.tagName}${className === null ? "" : ` class=${JSON.stringify(className)}`}>`;
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

function newCanvas() {
  return new MstCanvas({ document: dom.document });
}

function classOf(element) {
  return element.getAttribute("class");
}

// --- The instrument itself -------------------------------------------

check("theDOMStubStartsWhereTheAssertionsBegin", () => {
  // A stub that starts a flag where the assertion wants it makes a test
  // pass on its own, which this repository has shipped twice. Every
  // default below is the *absence* of an answer.
  const element = dom.document.createElementNS(SVG_NS, "rect");
  assertEqual(element.getAttribute("class"), null, "an attribute nobody set reads as null and not as empty");
  assertEqual(element.getAttribute("stroke-dasharray"), null, "and so does every other one");
  assertEqual(element.textContent, "", "a fresh element carries no text");
  assertEqual(element.childNodes.length, 0, "and no children");
  assertEqual(element.parentNode, null, "and no parent");
  assertEqual(element.namespaceURI, SVG_NS, "and it remembers the namespace it was made in");

  // Creating and reading record nothing: the log is a log of writes, so
  // a drag measured against it cannot be inflated by the measurement.
  dom.reset();
  dom.document.createElementNS(SVG_NS, "g");
  element.getAttribute("class");
  element.hasAttribute("class");
  assertEqual(dom.log.length, 0, "creating an element and reading it record no mutation");

  element.setAttribute("class", "node");
  assertEqual(dom.log.length, 1, "and a write records exactly one");
  assertEqual(element.getAttribute("class"), "node", "which reads back as what was written");

  // Per-element storage: one emitter's write must not look like every
  // other element's.
  const other = dom.document.createElementNS(SVG_NS, "rect");
  assertEqual(other.getAttribute("class"), null, "a second element shares no attribute with the first");

  // A DOM attribute is a string. Accepting a number here would let an
  // emitter that never called String() pass a comparison a browser fails.
  let threw = null;
  try {
    element.setAttribute("x", 4);
  } catch (err) {
    threw = err;
  }
  assert(threw !== null, "setAttribute refuses a non-string value");

  // Both directions of the one sink this component exists to avoid.
  for (const direction of ["read", "write"]) {
    let caught = null;
    try {
      if (direction === "read") void element.innerHTML;
      else element.innerHTML = "<b>x</b>";
    } catch (err) {
      caught = err;
    }
    assert(caught !== null, `innerHTML throws on ${direction}`);
  }

  // And a namespace is required, so "this landed in the SVG namespace"
  // cannot be satisfied by a default.
  let missing = null;
  try {
    dom.document.createElementNS(undefined, "rect");
  } catch (err) {
    missing = err;
  }
  assert(missing !== null, "createElementNS refuses a missing namespace");
});

// --- joinEdges -------------------------------------------------------

const alpha = { id: "id-a", type: "zone", key: "elwynn", name: "Elwynn" };
const beta = { id: "id-b", type: "zone", key: "duskwood", name: "Duskwood" };

check("joinEdgesSeparatesStubs", () => {
  const nodes = [alpha, beta];
  const edges = [
    { id: "e-drawn", type: "connects_to", source: "id-a", target: "id-b" },
    { id: "e-out", type: "connects_to", source: "id-a", target: "id-outside" },
    { id: "e-in", type: "connects_to", source: "id-outside", target: "id-b" },
  ];
  const { drawn, stubs } = joinEdges(nodes, edges);

  // The positive control lives in this test rather than beside it: a
  // join that put *everything* in stubs would satisfy the negative half
  // alone, and this repository has shipped a guard that only ever saw
  // one outcome.
  assertEqual(drawn.length, 1, "the edge whose two ends are in the picture is drawn");
  assertEqual(drawn[0].edge.id, "e-drawn", "and it is the one with both ends");
  assertSame(drawn[0].source, alpha, "with its endpoints resolved to the node objects");
  assertSame(drawn[0].target, beta, "both of them");

  assertEqual(stubs.length, 2, "and the two that leave the picture are stubs");
  assertDeepEqual(
    stubs.map((stub) => [stub.edge.id, stub.missing]),
    [["e-out", ENDPOINT_TARGET], ["e-in", ENDPOINT_SOURCE]],
    "each saying which end it lost",
  );
  assert(
    !stubs.some((stub) => stub.edge.id === "e-drawn"),
    "and the drawn edge is in neither half twice",
  );
  assertSame(stubs[0].source, alpha, "a stub still resolves the end it has");
  assertSame(stubs[0].target, null, "and answers null for the one it does not");

  // A node with no usable id is not an endpoint anybody can join to. If
  // it were indexed under `undefined` it would silently resolve every
  // edge that names no source, which is the picture quietly gaining
  // edges nobody drew.
  const ghosted = joinEdges([{ type: "zone", key: "nameless" }, alpha], [
    { id: "e-nowhere", source: "id-a", target: undefined },
  ]);
  assertEqual(ghosted.drawn.length, 0, "an edge whose target is nothing is not drawn");
  assertEqual(ghosted.stubs[0].missing, ENDPOINT_TARGET, "it is a stub, missing the end it never had");
});

check("bothEndpointsMissingIsAlsoAStub", () => {
  // The case an implementation written around "one end is outside" gets
  // wrong: an `||` answers "source" for it and hides half the truth, and
  // a check requiring exactly one missing end drops the edge entirely.
  // The fixture can only produce it — both ids name entities the query
  // did not draw — and it carries a drawn edge and a one-ended stub
  // beside it, so the answer has to be *this* one and not a default.
  const { drawn, stubs } = joinEdges(
    [alpha, beta],
    [
      { id: "e-drawn", source: "id-a", target: "id-b" },
      { id: "e-half", source: "id-a", target: "id-far" },
      { id: "e-both", source: "id-far", target: "id-further" },
    ],
  );
  assertEqual(drawn.length, 1, "the joined edge is still joined");
  assertEqual(stubs.length, 2, "and both of the others are stubs");
  const both = stubs.find((stub) => stub.edge.id === "e-both");
  assert(both !== undefined, "an edge with neither end in the picture is a stub, not a dropped edge");
  assertEqual(both.missing, ENDPOINT_BOTH, "and it says so, rather than naming one end");
  assertEqual(both.source, null, "with no source resolved");
  assertEqual(both.target, null, "and no target");
  assertEqual(
    stubs.find((stub) => stub.edge.id === "e-half").missing,
    ENDPOINT_TARGET,
    "while the one-ended stub still names its one end",
  );
});

// --- The emitter -----------------------------------------------------

const HOSTILE = '<script>alert(1)</script>';

check("theEmitterWritesEveryGameStringAsText", () => {
  const tree = emitScene(
    {
      marks: [
        { kind: MARK_RECT, key: "zone/elwynn", x: 0, y: 0, w: 80, h: 24, fill: "var(--data-1)" },
        { kind: MARK_LABEL, key: "zone/elwynn", x: 4, y: 16, text: HOSTILE, size: 11 },
      ],
    },
    { document: dom.document },
  );

  const labels = walk(tree.root).filter((element) => element.tagName === "text");
  assertEqual(labels.length, 1, "one label mark became one <text>");
  assertEqual(labels[0].namespaceURI, SVG_NS, "in the SVG namespace, where it lays out");
  assertEqual(labels[0].textContent, HOSTILE, "carrying the game's word as character data, unchanged");

  // The complement, which is the half a component that quietly dropped
  // the name would also pass: no attribute anywhere in the tree carries
  // markup, and the hostile string reaches no attribute at all.
  for (const element of walk(tree.root)) {
    for (const [name, value] of element.attributes) {
      assert(!value.includes("<"), `${element.tagName}'s ${name} carries markup: ${value}`);
      assert(!value.includes(HOSTILE), `${element.tagName}'s ${name} carries the game's word`);
    }
  }
});

check("everyTextNodeInTheTreeCameFromAMark", () => {
  // The canvas invents no words. There is no Lit template here for
  // internal/web/static_frame_test.go's template-text scan to read, so
  // the property is asserted at runtime instead: every character in the
  // emitted tree is a mark's text, and the multiset matches exactly.
  const texts = ["Elwynn", "Duskwood"];
  const tree = emitScene(
    {
      marks: [
        { kind: MARK_RECT, key: "a", x: 0, y: 0, w: 10, h: 10 },
        { kind: MARK_LABEL, key: "a", x: 0, y: 0, text: texts[0] },
        { kind: MARK_LINE, key: "e", source: "a", target: "b", x1: 0, y1: 0, x2: 5, y2: 5 },
        { kind: MARK_LABEL, key: "b", x: 0, y: 0, text: texts[1] },
      ],
    },
    { document: dom.document },
  );
  const spoken = walk(tree.root)
    .map((element) => element.ownText)
    .filter((text) => text !== "");
  assertDeepEqual(spoken.sort(), texts.slice().sort(), "the tree says exactly what the marks said");

  // And the stylesheet says nothing either: `content:` is the one CSS
  // property that puts words on a screen.
  assert(!CANVAS_CSS.includes("content:"), "the canvas stylesheet generates no content of its own");
});

check("theEmitterWritesOnlyTheAttributesTheContractNames", () => {
  const tree = emitScene(
    {
      marks: [
        {
          kind: MARK_RECT,
          key: "zone/elwynn",
          class: "node",
          x: 4,
          y: 8,
          w: 80,
          h: 24,
          dash: "4 2",
          fill: "var(--data-3)",
          // Four fields the contract does not name. Each is a real way a
          // passthrough emitter grows a hole.
          onload: "alert(1)",
          href: "/api/games/g/views/assets/1.png",
          style: "position:fixed",
          text: "not a label",
        },
      ],
    },
    { document: dom.document },
  );
  const rect = tree.layers.get(LAYER_NODES).childNodes[0];
  assertEqual(rect.tagName, "rect", "a rect mark is a <rect>");
  assertEqual(rect.getAttribute("x"), "4", "the named fields are written");
  assertEqual(rect.getAttribute("width"), "80", "under the SVG spelling the contract gives them");
  assertEqual(rect.getAttribute("stroke-dasharray"), "4 2", "including the dash, which is how absence is drawn");
  assertEqual(rect.getAttribute("data-key"), "zone/elwynn", "and the address the mark carries");
  assertEqual(rect.getAttribute("class"), "node", "and its class");
  for (const dropped of ["onload", "href", "style"]) {
    assertEqual(rect.getAttribute(dropped), null, `${dropped} is not in the contract and does not reach the DOM`);
  }
  assertEqual(rect.textContent, "", "and a non-label mark's text goes nowhere rather than somewhere");

  // The contract itself: no kind may map `text` to an attribute, because
  // a game's word in an attribute is a word the reader cannot read and
  // the one place this file's no-parsing argument would not cover.
  for (const [kind, fields] of Object.entries(MARK_ATTRIBUTES)) {
    assert(!Object.prototype.hasOwnProperty.call(fields, "text"), `${kind} maps text to an attribute`);
  }

  // A kind nobody declared is reported rather than drawn or thrown.
  const odd = emitScene({ marks: [{ kind: "hexagon", x: 0, y: 0 }] }, { document: dom.document });
  assertEqual(odd.skipped.length, 1, "a mark of an unknown kind is skipped");
  assertEqual(
    walk(odd.root).length,
    11,
    "and nothing was emitted for it: the svg, the hatch definition (defs, pattern, its ground and its " +
      "stripe), the world and five layers",
  );
});

check("anHrefTheInstanceCannotServeIsRefused", () => {
  // SVG's one attribute a browser *resolves* rather than draws. The twin
  // never had this question: an HTML text node fetches nothing.
  assert(isDrawableHref("/api/games/g/views/assets/1.png"), "a same-origin absolute path is drawable");
  for (const refused of [
    "javascript:alert(1)",
    "//evil.example/x.png",
    "https://evil.example/x.png",
    "data:image/svg+xml,<svg onload=alert(1)>",
    "../../etc/passwd",
    "/path with space.png",
    "/",
    "",
  ]) {
    assert(!isDrawableHref(refused), `refused: ${JSON.stringify(refused)}`);
  }

  const tree = emitScene(
    {
      marks: [
        { kind: MARK_IMAGE, key: "bg-bad", x: 0, y: 0, w: 10, h: 10, href: "javascript:alert(1)" },
        { kind: MARK_IMAGE, key: "bg-ok", x: 0, y: 0, w: 10, h: 10, href: "/api/games/g/views/assets/1.png" },
      ],
    },
    { document: dom.document },
  );
  const images = tree.layers.get(LAYER_IMAGE).childNodes;
  assertEqual(images.length, 2, "both image marks are drawn");
  assertEqual(images[0].getAttribute("href"), null, "the one whose href is not a path this instance serves has none");
  assertEqual(
    images[1].getAttribute("href"),
    "/api/games/g/views/assets/1.png",
    "and the legitimate one keeps it, which is the control",
  );
});

// --- Paint order -----------------------------------------------------

check("sceneOrderIsPaintOrder", () => {
  // Labels are pushed **before** their nodes and the edge is pushed
  // last, so what is asserted below is that the layers do the ordering
  // and not that the fixture happened to be well ordered.
  const tree = emitScene(
    {
      marks: [
        { kind: MARK_LABEL, key: "zone/elwynn", x: 0, y: 0, text: "Elwynn" },
        { kind: MARK_RECT, key: "zone/elwynn", x: 0, y: 0, w: 10, h: 10 },
        { kind: MARK_IMAGE, key: "bg", x: 0, y: 0, w: 10, h: 10 },
        { kind: MARK_LINE, key: "e", source: "zone/elwynn", target: "zone/duskwood", x1: 0, y1: 0, x2: 1, y2: 1 },
      ],
    },
    { document: dom.document },
  );

  // The five groups, in the order a browser paints them, written out
  // here rather than compared against LAYER_ORDER: a test that imports
  // the list it is checking is a test that agrees with any reordering.
  assertDeepEqual(
    tree.world.childNodes.map((group) => group.getAttribute("class")),
    ["layer layer-image", "layer layer-edges", "layer layer-nodes", "layer layer-labels", "layer layer-drag"],
    "the layers are five sibling groups in paint order",
  );
  assertSame(
    tree.world.childNodes[tree.world.childNodes.length - 1],
    tree.layers.get(LAYER_DRAG),
    "and the drag layer is last, so a dragged body paints over everything it crosses",
  );

  const order = walk(tree.root);
  const at = (element) => order.indexOf(element);
  const rect = tree.layers.get(LAYER_NODES).childNodes[0];
  const label = tree.layers.get(LAYER_LABELS).childNodes[0];
  const image = tree.layers.get(LAYER_IMAGE).childNodes[0];
  const line = tree.layers.get(LAYER_EDGES).childNodes[0];
  assert(at(label) > at(rect), "a label paints after the node it names, whatever order the marks arrived in");
  assert(at(rect) > at(line), "a node paints after the edges under it");
  assert(at(line) > at(image), "and the edges paint after the ground");
});

check("aMarkMayChooseALayerButNotTheDragLayer", () => {
  const tree = emitScene(
    {
      marks: [
        { kind: MARK_DISC, key: "halo", cx: 0, cy: 0, r: 4, layer: LAYER_EDGES },
        { kind: MARK_DISC, key: "pin", cx: 0, cy: 0, r: 4, layer: LAYER_DRAG },
        { kind: MARK_DISC, key: "plain", cx: 0, cy: 0, r: 4 },
      ],
    },
    { document: dom.document },
  );
  assertEqual(tree.layers.get(LAYER_EDGES).childNodes.length, 1, "a mark may name a layer other than its default");
  assertEqual(
    tree.layers.get(LAYER_DRAG).childNodes.length,
    0,
    "and may not name the drag layer, which the canvas empties on every drop",
  );
  assertEqual(tree.layers.get(LAYER_NODES).childNodes.length, 2, "so the refused one falls to its kind's default");
});

// --- Pan, zoom and the one transform ---------------------------------

function mapScene() {
  return {
    marks: [
      { kind: MARK_IMAGE, key: "bg", x: 0, y: 0, w: 400, h: 300, href: "/api/games/g/views/assets/1.png" },
      { kind: MARK_DISC, key: "zone/elwynn", cx: 40, cy: 60, r: 7, fill: "var(--data-1)" },
      { kind: MARK_LABEL, key: "zone/elwynn", x: 48, y: 56, text: "Elwynn", size: 11 },
    ],
  };
}

check("zoomAndPanMoveOneTransform", () => {
  const canvas = newCanvas();
  const tree = canvas.draw(mapScene());
  canvas.panBy(10, 20);
  canvas.zoomTo(2);

  const transformed = walk(tree.root).filter((element) => element.hasAttribute("transform"));
  assertEqual(transformed.length, 1, "pan and zoom are one transform on one element, and never two that can drift");
  assertSame(transformed[0], tree.world, "and it is the world group");
  assertEqual(
    tree.world.getAttribute("transform"),
    "translate(10 20) scale(2)",
    "carrying both the pan and the zoom",
  );

  // The property that makes a map honest: the ground and the pins are
  // one coordinate space. Asserted by ancestry rather than by two equal
  // strings, because two equal strings is exactly what a drifting
  // implementation has until the moment it does not.
  const ancestors = (element) => {
    const chain = [];
    for (let node = element.parentNode; node !== null; node = node.parentNode) chain.push(node);
    return chain;
  };
  const image = tree.layers.get(LAYER_IMAGE).childNodes[0];
  const disc = tree.layers.get(LAYER_NODES).childNodes[0];
  assert(ancestors(image).includes(tree.world), "the background rides the transform");
  assert(ancestors(disc).includes(tree.world), "and so does every node, through the same element");
  assert(ancestors(tree.layers.get(LAYER_DRAG)).includes(tree.world), "and so does a body being dragged");
});

check("labelsStopScalingOutsideTheBand", () => {
  assertEqual(labelScale(1), 1, "inside the band a label scales with the zoom");
  assertClose(labelScale(1.2), 1.2, "all the way to the top of it");
  assertEqual(labelScale(4), LABEL_SCALE_MAX, "at 4x it stops at the ceiling: more detail, not bigger text");
  assertEqual(labelScale(0.25), LABEL_SCALE_MIN, "and zoomed out it stops at the floor, so a name stays readable");

  const canvas = newCanvas();
  const tree = canvas.draw(mapScene());
  const label = tree.layers.get(LAYER_LABELS).childNodes[0];
  const apparent = () => Number.parseFloat(label.getAttribute("font-size")) * canvas.view.k;

  canvas.zoomTo(4);
  assertClose(Number.parseFloat(label.getAttribute("font-size")), (11 * 1.5) / 4, "the authored size is divided down");
  assertClose(apparent(), 11 * LABEL_SCALE_MAX, "so at 4x the label is drawn at 1.5x and not at 4x");

  canvas.zoomTo(0.25);
  assertClose(apparent(), 11 * LABEL_SCALE_MIN, "and at 0.25x it is drawn at 0.75x and not at 0.25x");

  // The control, without which a mutation that clamped every label to 1x
  // would pass both assertions above: inside the band the label really
  // does scale.
  canvas.zoomTo(1.2);
  assertClose(apparent(), 11 * 1.2, "inside the band the label scales with the zoom");
  assertClose(labelFontSize(11, 1.2), 11, "which is the authored size, unchanged");
  assertEqual(labelFontSize(undefined, 1), DEFAULT_LABEL_SIZE, "a mark with no size gets the default");
});

// --- What a reader can actually see ----------------------------------
//
// Three properties found by opening this front end in a browser for the
// first time (Task 15's hand checks). Each was a case where the scene
// was right and the screen was not, and none of them could have failed a
// test that only read the scene.

// **The tail is a texture, and the texture exists.**
//
// palette.js paints the ninth-and-beyond values with a paint server so
// they are visibly not one of the eight hues. A fill naming a pattern
// nobody defined is the worst of both: SVG resolves it to *nothing*, so
// the node is drawn invisible and no error is raised anywhere. So the
// two halves are joined here — the paint the palette names, and the
// definition the emitter emits.
check("theHatchTheTailIsPaintedWithIsReallyEmitted", () => {
  const tree = emitScene({ marks: [{ kind: MARK_RECT, x: 0, y: 0, w: 10, h: 10, fill: HATCH_FILL }] }, {
    document: dom.document,
  });
  const patterns = walk(tree.root).filter((el) => el.tagName === "pattern");
  assertEqual(patterns.length, 1, "the drawing defines exactly one hatch");
  assertEqual(patterns[0].getAttribute("id"), HATCH_PATTERN_ID, "under the id the palette's fill names");
  assertSame(patterns[0].parentNode.tagName === "defs" ? patterns[0] : null, patterns[0], "inside a <defs>");

  // The fill on the mark and the id of the pattern are the same string,
  // read from the two files rather than typed here twice.
  const painted = tree.layers.get(LAYER_NODES).childNodes[0];
  assertEqual(painted.getAttribute("fill"), HATCH_FILL, "and the tail's mark wears it");
  assert(HATCH_FILL.includes(HATCH_PATTERN_ID), "the fill names the pattern");
  assertEqual(fillFor({ kind: "hatch" }).css, HATCH_FILL, "which is what the palette paints a tail with");

  // Both themes: the hatch is drawn in tokens, like everything else.
  const stripe = walk(patterns[0]).find((el) => el.tagName === "line");
  const ground = walk(patterns[0]).find((el) => el.tagName === "rect");
  assertEqual(stripe.getAttribute("stroke"), HATCH_STROKE, "the stripes are a token");
  assertEqual(ground.getAttribute("fill"), HATCH_GROUND, "and so is what they are drawn on");
  assert(Number.parseFloat(stripe.getAttribute("stroke-width")) > 0, "and a stripe has a width");

  // The definition is emitted for every drawing, not only one with a
  // tail in it: a definition emitted on a condition is a fill that
  // resolves to nothing the first time the condition is wrong.
  const plain = emitScene({ marks: [] }, { document: dom.document });
  assertEqual(
    walk(plain.root).filter((el) => el.getAttribute("id") === HATCH_PATTERN_ID).length,
    1,
    "a drawing with no tail still defines the hatch",
  );
});

// **The cycle mark survives the zoom the picture is read at.**
//
// The mark that says a ranked drawing had to reverse an edge was two 1px
// strokes nine units long, drawn inside the world group. On a
// hundred-step progression the drawing fits at k = 0.058, where those
// strokes measured 0.43 CSS pixels in Firefox — the whole of this
// renderer's negative half, invisible. It is a label now, so the label
// band keeps it at a constant apparent size, and this is the assertion
// that a line would fail.
check("theCycleMarkIsStillThereWhenTheWholePictureFits", () => {
  const canvas = newCanvas();
  canvas.draw({
    marks: [
      { kind: MARK_LINE, x1: 0, y1: 0, x2: 4000, y2: 6000, stroke: "var(--muted)" },
      {
        kind: MARK_LABEL,
        class: CLASS_REVERSED,
        x: 2000,
        y: 3000,
        text: "//",
        size: REVERSAL_SIZE,
        anchor: "middle",
        baseline: "middle",
      },
    ],
  });
  const mark = walk(canvas.tree.root).find((el) => el.getAttribute("class") === CLASS_REVERSED);
  assert(mark !== undefined, "the mark is in the drawing");

  // The zoom the hand check measured, on the picture this mark exists
  // for. The old mark was 0.43 px across; a number below is what it
  // would be again.
  const fitted = 0.058;
  canvas.zoomTo(fitted);
  const drawn = Number.parseFloat(mark.getAttribute("font-size")) * fitted;
  assert(drawn > 8, `the cycle mark is ${drawn.toFixed(2)} px at the zoom a hundred-step progression fits at`);

  // And it is not simply enormous when the picture is read close up:
  // the band holds it at the top as well as at the bottom.
  canvas.zoomTo(4);
  const close = Number.parseFloat(mark.getAttribute("font-size")) * 4;
  assert(close < REVERSAL_SIZE * LABEL_SCALE_MAX + 1, `and ${close.toFixed(2)} px at 4x, not four times bigger`);
});

// **No control a keyboard can reach is hidden from a screen reader.**
//
// The drawing is aria-hidden because the twin is the accessible content
// of an answer. For one round the attribute sat on the box the canvas is
// *slotted into*, which also holds the arrangement menu, the ground
// panel and the table's sort headers — so every one of those buttons was
// reachable by tab and announced to nobody, which is worse than either
// alone. Found by reading the frame's shadow tree in Firefox.
//
// The assertion is the general rule rather than the two buttons that
// were found: anything focusable, anywhere under the canvas, with an
// aria-hidden ancestor.
check("noControlIsBothReachableByKeyboardAndHiddenFromAScreenReader", () => {
  const canvas = newCanvas();
  canvas.draw(mapScene());
  canvas.showArrangement({
    pending: false,
    band: null,
    menu: () => ({
      actions: [
        { id: "unpin", label: "Unpin" },
        { id: "clear", label: "Clear the saved position" },
      ],
      notes: [],
    }),
  });

  const hiddenUnder = (element) => {
    for (let at = element; at; at = at.parentNode) {
      if (at.getAttribute && at.getAttribute("aria-hidden") === "true") return at;
    }
    return null;
  };

  const focusable = walk(canvas.shell.root).filter(
    (el) => el.tagName === "button" || el.tagName === "a" || el.getAttribute("tabindex") !== null,
  );
  assert(focusable.length > 0, "the fixture put controls on the canvas: a scan with nothing in it guards nothing");
  for (const control of focusable) {
    const hidden = hiddenUnder(control);
    assertEqual(
      hidden,
      null,
      `a ${control.tagName} a keyboard can reach ("${control.textContent}") is inside an aria-hidden element`,
    );
  }

  // And the drawing itself still says it is decoration, which is the
  // half that must not be lost while fixing the other one.
  assertEqual(
    canvas.tree.root.getAttribute("aria-hidden"),
    "true",
    "the svg is hidden from assistive technology; the twin is the answer",
  );
  assertEqual(
    canvas.shell.surfaceHost.getAttribute("aria-hidden"),
    null,
    "and the box that takes the keyboard is not, because it is a control",
  );
  assert(
    typeof canvas.shell.surfaceHost.getAttribute("aria-label") === "string",
    "and it has a name: a focusable element with none is announced as nothing at all",
  );
});

// --- The drag layer --------------------------------------------------

// Two hundred nodes in a chain, each with a label, and 199 edges. Two
// hundred because a full re-render has to be unmissable rather than a
// near miss: the numbers below are 1 and 3, and the wrong answer is in
// the hundreds.
function chainScene(count) {
  const marks = [];
  for (let i = 0; i < count; i++) {
    marks.push({ kind: MARK_RECT, key: `zone/n${i}`, x: i * 100, y: 0, w: 80, h: 24 });
    marks.push({ kind: MARK_LABEL, key: `zone/n${i}`, x: i * 100 + 4, y: 16, text: `n${i}`, size: 11 });
    if (i > 0) {
      marks.push({
        kind: MARK_LINE,
        key: `e${i - 1}`,
        source: `zone/n${i - 1}`,
        target: `zone/n${i}`,
        x1: (i - 1) * 100 + 80,
        y1: 12,
        x2: i * 100,
        y2: 12,
      });
    }
  }
  return { marks };
}

check("aDragTouchesOnlyTheDraggedSubtree", () => {
  const canvas = newCanvas();
  const tree = canvas.draw(chainScene(200));
  assertEqual(tree.nodes.size, 200, "the fixture really is 200 nodes");
  assertEqual(tree.edges.length, 199, "and 199 edges");

  dom.reset();
  const { carried, reshaped, missing } = canvas.beginDrag(["zone/n5", "zone/n6"]);
  assertEqual(missing.length, 0, "both dragged keys are in the picture");
  // Two boxes, two labels and the edge between them: five elements move
  // onto the drag layer, and moving is not writing.
  assertEqual(carried.length, 5, "the body is two boxes, two labels and the edge inside the selection");
  assertEqual(dom.log.length, 0, "detaching the body writes no attribute at all");
  assertEqual(reshaped.length, 2, "and the two edges that leave the selection cannot ride a transform");
  assertDeepEqual(
    reshaped.map((entry) => entry.record.mark.key).sort(),
    ["e4", "e6"].sort(),
    "they are the edges into and out of the pair",
  );

  dom.reset();
  canvas.dragBy(12, -4);
  const touched = dom.touched();
  assertEqual(
    touched.length,
    3,
    `a move writes the drag layer's transform and the two edges that leave it, and nothing else; ` +
      `${touched.length} elements changed in a 200-node picture`,
  );
  assert(touched.includes(tree.layers.get(LAYER_DRAG)), "one of them is the drag layer");
  const draggedRect = tree.nodes.get("zone/n5").shapes[0].element;
  assert(!touched.includes(draggedRect), "and none of them is a dragged node: the whole body rides one transform");
  assertEqual(
    tree.layers.get(LAYER_DRAG).getAttribute("transform"),
    "translate(12 -4)",
    "which is where the body now is",
  );

  // The moving end of each reshaped edge follows; the standing end does
  // not. An edge that translated whole would leave its far end behind.
  const into = tree.edges.find((edge) => edge.mark.key === "e4").element;
  assertEqual(into.getAttribute("x2"), "512", "the end attached to the dragged node moves");
  assertEqual(into.getAttribute("x1"), "480", "and the end attached to a node standing still does not");

  // A second move is the same three elements, not three more.
  dom.reset();
  canvas.dragBy(1, 1);
  assertEqual(dom.touched().length, 3, "and every subsequent frame costs the same three writes");

  const drop = canvas.endDrag();
  assertDeepEqual(drop.keys.sort(), ["zone/n5", "zone/n6"], "the drop names what moved");
  assertEqual(drop.dx, 13, "with the offset it accumulated");
  assertEqual(drop.dy, -3, "in both axes");
  assertEqual(tree.layers.get(LAYER_DRAG).childNodes.length, 0, "the drag layer is emptied");
  assertSame(draggedRect.parentNode, tree.layers.get(LAYER_NODES), "and every element goes back to its own layer");
  assertEqual(draggedRect.getAttribute("x"), "513", "with the offset baked into the coordinate it rode on");
  assertEqual(
    tree.nodes.get("zone/n7").shapes[0].element.getAttribute("x"),
    "700",
    "while a node nobody dragged is where it always was",
  );
});

check("anEdgeInsideTheSelectionRidesTheTransformAndOneLeavingItDoesNot", () => {
  // The distinction the count above rests on, asserted directly so that
  // a beginDrag which reshaped everything incident would fail here
  // rather than only in an arithmetic comparison.
  const canvas = newCanvas();
  const tree = canvas.draw(chainScene(4));
  const { carried, reshaped } = canvas.beginDrag(["zone/n1", "zone/n2"]);
  const inside = tree.edges.find((edge) => edge.mark.key === "e1");
  assert(
    carried.some((record) => record.element === inside.element),
    "an edge with both ends in the selection is carried, because a translation is exactly right for it",
  );
  assert(
    !reshaped.some((entry) => entry.record === inside),
    "and is not reshaped, which would write two endpoint pairs a transform already moved",
  );
  assertDeepEqual(
    reshaped.map((entry) => entry.ends),
    [[["x2", "y2"]], [["x1", "y1"]]],
    "each leaving edge moves the one end that is in the selection",
  );
});

// --- The shell -------------------------------------------------------

check("theCanvasStylesheetIsAdoptedAndNeverAnElement", () => {
  // The rule, and the defect it closes: this component used to give
  // itself CANVAS_CSS by appending a `<style>` element to its shadow
  // root, and under `default-src 'self'` a browser refuses to apply it —
  // silently. The element stays in the tree with its text intact,
  // `querySelector` finds it, and `style.sheet` is null, so the canvas
  // shipped from Task 7 with none of its own layout: the host laid out
  // `static` instead of `absolute` and the surface drew at an SVG's
  // default 300x150. Found by mounting the first view (Task 15).
  //
  // `adoptedStyleSheets` is not inline style and no policy governs it.
  const adopted = [];
  const shadow = { adoptedStyleSheets: [] };
  const previous = globalThis.CSSStyleSheet;
  globalThis.CSSStyleSheet = class {
    replaceSync(text) {
      this.cssText = text;
      adopted.push(text);
    }
  };
  try {
    const sheet = adoptCanvasStyles(shadow);
    assert(sheet !== null, "a shadow root that supports constructible sheets gets one");
    assertEqual(shadow.adoptedStyleSheets.length, 1, "adopted onto the shadow root");
    assertEqual(adopted[0], CANVAS_CSS, "and it carries the component's own stylesheet");
  } finally {
    globalThis.CSSStyleSheet = previous;
  }
});

check("aShadowRootWithoutConstructibleSheetsGetsNoStyleElementFallback", () => {
  // There is deliberately no fallback. A `<style>` appended when
  // constructible sheets are missing could not work under this policy
  // anyway, and a mechanism nothing reads is worse than an absence.
  const shadow = { appendChild: () => assert(false, "nothing is appended as a fallback") };
  assertEqual(adoptCanvasStyles(shadow), null, "an unsupported shadow root is left unstyled");
  assertEqual(adoptCanvasStyles(null), null, "and so is no shadow root at all");
});

check("theCanvasIsFullBleedAndThePanelsFloat", () => {
  const canvas = newCanvas();
  canvas.draw(mapScene());
  const root = canvas.shell.root;
  assertEqual(classOf(root), CLASS_ROOT, "the canvas root is full-bleed");
  assert(CLASS_ROOT.includes("full-bleed"), "which is what that class is called");

  const classes = root.childNodes.map((child) => classOf(child));
  assertDeepEqual(
    classes,
    [CLASS_SURFACE_HOST, CLASS_PANELS],
    "the shell is the drawing and then the panels, in that order",
  );
  // And no stylesheet element among them: a `<style>` built in script is
  // inline style to a Content-Security-Policy, and this product's policy
  // refuses inline style without saying so. See
  // theCanvasStylesheetIsAdoptedAndNeverAnElement below.
  assert(
    !root.childNodes.some((child) => child.tagName === "style"),
    "the shell builds no style element",
  );
  assert(CLASS_PANELS.includes("floating"), "the panels float rather than taking space from the drawing");
  assertEqual(
    classOf(canvas.shell.surfaceHost.childNodes[0]),
    CLASS_SURFACE,
    "and the drawing is inside the host, under the panels in document order",
  );

  // The 68ch measure styles.css sets is for reading surfaces. A diagram
  // inside a centred column of prose width is a diagram nobody can use,
  // so the class has to mean something: the rule that carries it is
  // asserted, not just the name.
  assert(!CANVAS_CSS.includes("68ch"), "the canvas does not wear the reading measure");
  assert(/\.canvas\.full-bleed\s*{[^}]*max-width:\s*none/.test(CANVAS_CSS), "full-bleed removes the measure");
  assert(/\.canvas\.full-bleed\s*{[^}]*width:\s*100%/.test(CANVAS_CSS), "and fills its parent");
  assert(/\.panels\s*{[^}]*position:\s*absolute/.test(CANVAS_CSS), "and the panels are positioned over it");
});

// --- The layout budget's band ----------------------------------------

check("theCanvasPlacesTheLayoutBudgetBand", async () => {
  // Task 6 declared BANNER_LAYOUT_BUDGET and ACTION_RETRY_LAYOUT in
  // scene.js's vocabulary, kept them out of BANNER_ORDER — that stack is
  // built from the envelope and this band is a statement about *this
  // browser's* last two seconds — and said the canvas places them. This
  // is the canvas placing them; without it they are two constants with
  // no reader, which this plan calls a lie.
  const canvas = newCanvas();
  const timedOut = await runWithBudget({
    run: () => new Promise(() => {}),
    fallback: () => [],
    setTimer: (fn) => {
      fn();
      return 1;
    },
    clearTimer: () => {},
    now: () => 0,
  });
  assertEqual(timedOut.ok, false, "the fixture really did miss the deadline");
  const placed = canvas.setLayoutResult(timedOut);
  assertEqual(placed.banners.length, 1, "the canvas carries the band");
  assertEqual(placed.banners[0].code, BANNER_LAYOUT_BUDGET, "under the code budget.js declares");
  assertEqual(placed.retry.code, ACTION_RETRY_LAYOUT, "with the one escalation it allows");

  const finished = await runWithBudget({
    run: () => Promise.resolve([]),
    setTimer: () => 2,
    clearTimer: () => {},
    now: () => 0,
  });
  const cleared = canvas.setLayoutResult(finished);
  assertEqual(cleared.banners.length, 0, "and a layout that finished clears what the last attempt left");
  assertEqual(cleared.retry, null, "including its retry");
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
  console.error(`${failures} canvas check(s) failed`);
  process.exit(1);
}
console.log("mst-canvas.js: all checks passed");
