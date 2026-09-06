// The harness for the text twin: internal/web/static/render/twin.js (the
// model) and internal/web/static/components/mst-twin.js (the painter),
// driven the way Task 4 drives the frame — the real, unmodified modules,
// asserted over the plain data they return.
//
// What this covers that no Go test can, and that no other harness here
// has had to cover before.
//
// **That the twin describes the answer and not the drawing.** The five
// graphical renderers shelve what they cannot place, drop what falls
// beyond a depth bound and collapse a crowd into a count chip. Every one
// of those nodes still has a row, because the twin's row source is the
// envelope. The fixture below runs a stand-in renderer that does all
// three, so the assertion is a comparison against a picture that really
// is smaller, not against a number written down twice.
//
// **That a hostile game name arrives as text.** A game's words are
// hostile input — this repository has shipped one stored cross-site
// scripting defect already — and the component renders them through Lit.
// So the harness walks the emitted template and asks *where each value
// is bound*: a value in child position becomes a Text node, a value in
// an attribute position does not, and a value inside the static markup
// would not be a value at all. Every game string must be a child
// binding, and no game string may appear anywhere in the templates' own
// HTML. That is the property; Lit's escaping is what enforces it, and
// this is the test that we hand Lit the strings in the position where it
// does.
//
// **That absent and empty stay two answers.** The views work spent real
// effort keeping "unset" and "the empty string" distinguishable from the
// jsonb up; a table that rendered both as nothing would throw it away at
// the last step, in the last place anybody would look.
//
// Run directly: `node internal/web/jstest/twin_test.mjs`.
// internal/web/static_appjs_browser_test.go shells out to it too.

// --- The DOM the components need, and no more -------------------------
//
// Lit's `html` builds a TemplateResult without touching the DOM; only
// `render()` into a container needs one. So the stub below is what
// *importing* a component module and *calling* its render() needs:
// custom-element registration, an HTMLElement to extend, and an event
// target. Nothing here parses HTML, which is deliberate — a stub that
// could turn a string into elements would be a second, worse browser,
// and the escaping question would then be a question about the stub.
//
// The elements are never connected, which is what keeps Lit from
// scheduling a real update: ReactiveElement holds its first update until
// connectedCallback enables it.
import { register } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const defined = new Map();
globalThis.window = globalThis;
globalThis.document = {
  createElement: () => ({}),
  createComment: () => ({}),
  // lit-html builds one TreeWalker at module scope and only walks it
  // when a template is *rendered* into a container, which nothing here
  // does. It has to exist for the module to load; it never runs.
  createTreeWalker: () => ({ nextNode: () => null }),
  head: {},
};
globalThis.CustomEvent = class CustomEvent {
  constructor(type, init = {}) {
    this.type = type;
    this.detail = init.detail;
    this.bubbles = init.bubbles === true;
    this.composed = init.composed === true;
  }
};
globalThis.HTMLElement = class HTMLElement {
  constructor() {
    this.dispatched = [];
  }
  attachShadow() {
    return {};
  }
  dispatchEvent(event) {
    this.dispatched.push(event);
    return true;
  }
  addEventListener() {}
  removeEventListener() {}
};
globalThis.customElements = {
  define(name, ctor) {
    defined.set(name, ctor);
  },
  get(name) {
    return defined.get(name);
  },
};

// The components import `lit` by bare specifier, as the browser's import
// map requires. Node resolves it through that same map, read out of a
// shipped shell by ./importmap_loader.mjs, so this harness loads exactly
// what the browser loads and no specifier is restated here.
register(new URL("./importmap_loader.mjs", import.meta.url), {
  parentURL: import.meta.url,
  data: { staticDir: path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "static"), shell: "game.html" },
});

const { html } = await import("lit");
const { ABSENT_TEXT, OUTSIDE_NOTE, twinFor } = await import("../static/render/twin.js");
const { frameFor } = await import("../static/render/scene.js");
const { legendFor } = await import("../static/palette.js");
const { MstTwin, SELECT_EVENT } = await import("../static/components/mst-twin.js");
const { MstViewFrame } = await import("../static/components/mst-view-frame.js");

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

// --- Walking an emitted template -------------------------------------
//
// A Lit TemplateResult is `{strings, values}`: the component's own HTML
// in `strings`, everything it was handed in `values`, interleaved. The
// walk below flattens a result — recursing into nested results and into
// arrays, which is how a `.map()` over rows arrives — into one ordered
// stream of markup chunks and bound values, and tells each value where
// in that markup it sits.
//
// `position` is the whole point:
//
//   "text"      — a child binding. Lit commits it as a Text node, so the
//                 value cannot be markup however it is spelled.
//   "attribute" — inside a tag. Committed with setAttribute or as a
//                 property; safe, but not *text*, and a game string here
//                 is a game string the reader cannot read.
//   "raw"       — inside <script>, <style>, <textarea> or <title>, where
//                 a browser's own parser reads content as characters and
//                 Lit's guarantee does not hold.
//   "comment"   — inside <!-- -->.
//
// theBindingPositionScannerReadsWhatItClaimsTo is the guard on this
// guard, in all four directions, because a scanner that answered "text"
// to everything would make the escaping assertion below pass on any
// component at all.
const RAW_TEXT_ELEMENTS = new Set(["script", "style", "textarea", "title"]);

function walk(result) {
  const items = [];
  emit(result, items);
  let state = { mode: "text", quote: "", tag: "", attr: "", word: "" };
  for (const item of items) {
    if (item.kind === "markup") {
      state = advance(state, item.text);
      continue;
    }
    item.position = POSITIONS[state.mode];
    item.tag = state.tag;
    item.attribute = state.attr;
  }
  return items;
}

// The five modes the machine below can be in when a binding arrives,
// and what each means for the value bound there. Three of the five are
// the same answer — anywhere inside a tag is an attribute, property or
// event binding — and they are kept apart in the machine because the
// parsing differs and folded together here because the *question* does
// not.
const POSITIONS = {
  text: "text",
  tagname: "attribute",
  tag: "attribute",
  quoted: "attribute",
  closing: "attribute",
  raw: "raw",
  comment: "comment",
};

function emit(value, items) {
  if (isTemplateResult(value)) {
    const { strings, values } = value;
    for (let i = 0; i < strings.length; i++) {
      items.push({ kind: "markup", text: strings[i] });
      if (i < values.length) emit(values[i], items);
    }
    return;
  }
  if (Array.isArray(value)) {
    for (const item of value) emit(item, items);
    return;
  }
  items.push({ kind: "value", value });
}

function isTemplateResult(value) {
  return (
    value !== null &&
    typeof value === "object" &&
    typeof value._$litType$ !== "undefined" &&
    Array.isArray(value.strings)
  );
}

// advance runs one markup chunk through a small HTML state machine and
// returns where it left off, so the next binding can be placed. It is
// deliberately small — the markup it reads is the markup in this
// repository — and every mode it can be in is one of POSITIONS above, so
// there is no path on which it answers "text" by default.
function advance(state, chunk) {
  let { mode, quote, tag, attr, word } = state;
  for (let i = 0; i < chunk.length; i++) {
    const c = chunk[i];
    switch (mode) {
      case "text":
        if (chunk.startsWith("<!--", i)) {
          mode = "comment";
          i += 3;
        } else if (chunk.startsWith("</", i)) {
          mode = "closing";
          i += 1;
        } else if (c === "<") {
          mode = "tagname";
          tag = "";
          attr = "";
          word = "";
        }
        break;
      case "closing":
        if (c === ">") mode = "text";
        break;
      case "tagname":
        if (c === ">") mode = closesInto(tag);
        else if (/[\s/]/.test(c)) mode = "tag";
        else tag += c;
        break;
      case "tag":
        if (c === '"' || c === "'") {
          quote = c;
          mode = "quoted";
        } else if (c === ">") {
          mode = closesInto(tag);
          attr = "";
          word = "";
        } else if (c === "=") {
          attr = word;
          word = "";
        } else if (/[\s/]/.test(c)) {
          word = "";
          attr = "";
        } else {
          word += c;
        }
        break;
      case "quoted":
        if (c === quote) mode = "tag";
        break;
      case "comment":
        if (chunk.startsWith("-->", i)) {
          mode = "text";
          i += 2;
        }
        break;
      case "raw":
        if (chunk.startsWith("</", i)) {
          mode = "closing";
          i += 1;
        }
        break;
      default:
        break;
    }
  }
  return { mode, quote, tag, attr, word };
}

// closesInto is where a `>` leaves the machine: inside <script>,
// <style>, <textarea> or <title> a browser reads what follows as
// characters, which is the one place Lit's child bindings are not text
// nodes and where this scan must not answer "text".
function closesInto(tag) {
  return RAW_TEXT_ELEMENTS.has(tag.toLowerCase()) ? "raw" : "text";
}

function markupOf(items) {
  return items
    .filter((item) => item.kind === "markup")
    .map((item) => item.text)
    .join("");
}

function valuesOf(items) {
  return items.filter((item) => item.kind === "value");
}

function renderComponent(component) {
  return walk(component.render());
}

// --- The fixtures ----------------------------------------------------

// A game whose answer is bigger than any drawing of it. Nine nodes in
// four sets: the ones a renderer draws, the ones it shelves because it
// could not place them, the ones it drops beyond a depth bound and the
// ones it collapses into a count chip. Every one of them has a row.
const nodes = [
  node("elwynn", "zone", "Elwynn", "drawn", { label: "Elwynn", color_by: "alliance", level: 1 }),
  node("duskwood", "zone", "Duskwood", "drawn", { label: "Duskwood", color_by: "alliance", level: 10 }),
  node("barrens", "zone", "The Barrens", "drawn", { label: "The Barrens", color_by: "horde", level: 10 }),
  // `region` is carried by this node and by no earlier one: the columns
  // are the union of every node's slots, and a twin that read them off
  // the first row would lose this whole column and the answer in it.
  node("orgrimmar", "city", "Orgrimmar", "shelved", { label: "Orgrimmar", color_by: "horde", region: "kalimdor" }),
  node("hyjal", "zone", "Hyjal", "shelved", { label: "Hyjal", color_by: "neutral", level: 55 }),
  node("blackrock", "zone", "Blackrock", "beyond", { label: "Blackrock", color_by: "" , level: 55 }),
  node("deadmines", "instance", "The Deadmines", "beyond", { label: "The Deadmines" }),
  node("talent-a", "talent", "Arcane Power", "collapsed", { label: "Arcane Power", color_by: "mage" }),
  node("talent-b", "talent", "Frost Nova", "collapsed", { label: "Frost Nova", color_by: "mage" }),
];

function node(key, type, name, set, attrs) {
  return { id: "id-" + key, key, type, name, set, attrs };
}

const edges = [
  { id: "e1", type: "connects_to", source: "id-elwynn", target: "id-duskwood", label: "road" },
  { id: "e2", type: "connects_to", source: "id-duskwood", target: "id-barrens" },
  // A stub in each direction: an edge the query drew between sets it
  // chose not to draw. Neither endpoint is in `nodes`, which
  // internal/views/execute.go says is ordinary rather than exceptional.
  { id: "e3", type: "unlocks", source: "id-elwynn", target: "id-stranglethorn", label: "" },
  { id: "e4", type: "requires", source: "id-westfall", target: "id-elwynn" },
];

const answer = {
  nodes,
  edges,
  stats: { nodes: nodes.length, edges: edges.length, max_depth_reached: 2, duration_ms: 9 },
  truncated: { nodes: false, edges: false, depth: false },
};

// The stand-in renderer. It is here rather than imported because no
// renderer exists yet — this task is deliberately ahead of all six — and
// what the assertion needs is not any particular renderer but *a picture
// that is smaller than the answer for each of the three reasons the six
// will be smaller for*.
function standInScene(envelope) {
  const drawn = [];
  const shelf = [];
  let collapsed = 0;
  for (const n of envelope.nodes) {
    if (n.set === "drawn") drawn.push(n.key);
    else if (n.set === "shelved") shelf.push(n.key);
    else if (n.set === "collapsed") collapsed++;
    // "beyond" is dropped: past the depth bound, it is not in the scene
    // at all, not even on the shelf.
  }
  return { drawn, shelf, collapsed };
}

// --- The answer, not the drawing -------------------------------------

check("everyNodeInTheEnvelopeHasARow", () => {
  const scene = standInScene(answer);
  // The fixture has to be able to tell the two outcomes apart, so the
  // picture must really be smaller — in all three ways.
  assert(scene.drawn.length < answer.nodes.length, "the stand-in picture draws fewer nodes than the answer holds");
  assert(scene.shelf.length > 0, "and shelves some");
  assert(scene.collapsed > 0, "and collapses some into a chip");
  const dropped = answer.nodes.filter((n) => n.set === "beyond").map((n) => n.key);
  assert(dropped.length > 0, "and drops some beyond a depth bound");

  const twin = twinFor(answer);
  assertEqual(twin.nodes.rows.length, answer.nodes.length, "the twin has one row per node in the envelope");

  const rowKeys = twin.nodes.rows.map((row) => row.node.key).sort();
  assertDeepEqual(rowKeys, answer.nodes.map((n) => n.key).sort(), "and they are the envelope's nodes");

  // Named individually, so a count that happened to match cannot pass.
  for (const key of [...scene.shelf, ...dropped, "talent-a", "talent-b"]) {
    const row = twin.nodes.rows.find((r) => r.node.key === key);
    assert(row, `${key} is not in the picture and must still have a row`);
    const name = answer.nodes.find((n) => n.key === key).name;
    assertEqual(row.cells[0].text, name, `and the row carries ${key}'s name`);
  }
});

check("theTwinCannotBeToldWhatTheRendererDrew", () => {
  // The structural half of the property above: there is no parameter
  // through which a scene could reach the model, so a future renderer
  // cannot quietly start filtering the twin.
  assertEqual(twinFor.length, 1, "twinFor takes the envelope and nothing else");
  const withScene = twinFor(answer, standInScene(answer));
  assertDeepEqual(withScene, twinFor(answer), "and a second argument changes nothing");
});

check("everyEdgeHasARowIncludingStubs", () => {
  const twin = twinFor(answer);
  assertEqual(twin.edges.rows.length, edges.length, "one row per edge in the envelope");

  const drawnRow = twin.edges.rows[0];
  assertDeepEqual(
    drawnRow.cells.map((cell) => cell.text),
    ["Elwynn", "connects_to", "Duskwood", "road"],
    "an edge with both ends in the picture names them",
  );
  assert(
    drawnRow.cells.every((cell) => cell.outside === false),
    "and nothing about it is outside",
  );

  const outgoing = twin.edges.rows[2];
  assertEqual(outgoing.cells[2].text, "id-stranglethorn", "a stub's far end is the id, which is all the envelope has");
  assertEqual(outgoing.cells[2].outside, true, "and it is marked as outside the picture");
  assertEqual(outgoing.cells[2].note, OUTSIDE_NOTE, "and it says so in words");
  assertEqual(outgoing.cells[0].outside, false, "while the near end is an ordinary cell");

  const incoming = twin.edges.rows[3];
  assertEqual(incoming.cells[0].text, "id-westfall", "a stub in the other direction is a stub too");
  assertEqual(incoming.cells[0].outside, true, "and is marked");

  // The label column, where absent and empty are the same distinction
  // the projection slots make: e3 carries "" and e4 carries none.
  assertEqual(outgoing.cells[3].text, "", "an edge labelled with the empty string reads as empty");
  assertEqual(outgoing.cells[3].absent, false, "and is not absent");
  assertEqual(incoming.cells[3].text, ABSENT_TEXT, "an edge with no label at all is absent");
  assertEqual(incoming.cells[3].absent, true, "and says so");
});

// --- Absent, and empty -----------------------------------------------

check("absentSlotsRenderAsAnEmDashNotBlank", () => {
  const twin = twinFor(answer);
  const columns = twin.nodes.columns.map((column) => column.key);
  assertDeepEqual(columns, ["name", "type", "key", "label", "color_by", "level", "region"],
    "the slot columns are the union of every node's slots, with label first");
  // Named, because the assertion above would also pass on a twin that
  // read its columns off the first row if every slot happened to be
  // there: `region` is on the fourth node and on no earlier one.
  assert(!Object.prototype.hasOwnProperty.call(answer.nodes[0].attrs, "region"),
    "the fixture's first node does not carry every slot, or this test cannot tell the two readings apart");
  const region = twin.nodes.rows.find((r) => r.node.key === "elwynn").cells[columns.indexOf("region")];
  assertEqual(region.absent, true, "and a node without the late-arriving slot is absent in that column");

  const cellOf = (key, column) => {
    const row = twin.nodes.rows.find((r) => r.node.key === key);
    const index = columns.indexOf(column);
    return row.cells[index];
  };

  // Orgrimmar has no `level`; Blackrock's `color_by` is the empty
  // string. Two different answers, two different cells.
  const missing = cellOf("orgrimmar", "level");
  assertEqual(missing.absent, true, "a slot the projection did not find is absent");
  assertEqual(missing.text, ABSENT_TEXT, "and is a mark on the screen rather than a blank");
  assert(missing.text.trim() !== "", "a blank cell is what the empty string looks like, and this is not that");

  const emptyString = cellOf("blackrock", "color_by");
  assertEqual(emptyString.absent, false, "a slot holding the empty string is present");
  assertEqual(emptyString.text, "", "and reads as nothing, which is what it is");

  const both = cellOf("deadmines", "color_by");
  assertEqual(both.absent, true, "and a node missing the same slot is still absent beside it");
  assert(
    missing.text !== emptyString.text && missing.absent !== emptyString.absent,
    "the two answers differ in the cell's text and in its flag, so neither carries the distinction alone",
  );
});

check("aValueThatLooksLikeTheAbsentMarkIsStillNotAbsent", () => {
  // A game may write an em dash. The mark is then not enough on its own,
  // which is why the cell also says `absent` and why the component
  // paints that as a class rather than trusting the character.
  const twin = twinFor({
    nodes: [node("dash", "zone", "Dash", "drawn", { label: "Dash", color_by: ABSENT_TEXT })],
    edges: [],
  });
  const cell = twin.nodes.rows[0].cells[4];
  assertEqual(cell.text, ABSENT_TEXT, "the game's own em dash reaches the cell");
  assertEqual(cell.absent, false, "and the cell is not absent, which is the half that tells them apart");
});

// --- Colour is never the only carrier --------------------------------

check("theTwinCarriesTheValueTextForEveryColouredNode", () => {
  const twin = twinFor(answer);
  const columns = twin.nodes.columns.map((column) => column.key);
  const colorColumn = columns.indexOf("color_by");
  assert(colorColumn >= 0, "the coloured slot is a column");

  const { byValue } = legendFor(answer.nodes, "color_by");
  let coloured = 0;
  for (const n of answer.nodes) {
    if (!Object.prototype.hasOwnProperty.call(n.attrs, "color_by")) continue;
    const row = byValue.get(JSON.stringify(n.attrs.color_by));
    assert(row, `${n.key} wears a hue the legend does not know`);
    coloured++;
    const cell = twin.nodes.rows.find((r) => r.node.key === n.key).cells[colorColumn];
    // The legend's own label, character for character: the twin and the
    // hue are two carriers of one value, not two spellings of it.
    const expected = row.kind === "hue" ? row.label : n.attrs.color_by;
    assertEqual(cell.text, expected, `${n.key}'s value text is in its row`);
  }
  assert(coloured >= 4, "and the fixture has enough coloured nodes to mean something");
});

// --- The frame the twin lives in -------------------------------------

check("everyAnswerHasATwinAndNoRefusalDoes", () => {
  const view = { key: "world", name: "The world", renderer: "graph" };
  const picture = frameFor({ view, envelope: answer });
  assert(picture.twin !== null, "a picture has a twin");
  assertEqual(picture.twin.nodes.rows.length, answer.nodes.length, "and it is the whole answer");

  const emptyEnvelope = {
    nodes: [],
    edges: [],
    stats: { nodes: 0, edges: 0, max_depth_reached: 0, duration_ms: 4 },
    truncated: { nodes: false, edges: false, depth: false },
  };
  const empty = frameFor({ view, envelope: emptyEnvelope });
  assert(empty.twin !== null, "an empty answer has one too, because it is an answer");
  assertEqual(empty.twin.nodes.rows.length, 0, "with no rows in it");

  const refusal = frameFor({
    view,
    error: { code: "query_stale", details: { fields: [{ path: "/from/1/type", message: "gone" }] } },
  });
  assertEqual(refusal.twin, null, "a refusal has none: there is no answer to describe");
});

check("theCanvasIsAriaHiddenAndTheTwinIsNot", () => {
  const frame = new MstViewFrame();
  frame.frame = frameFor({ view: { key: "world", name: "The world", renderer: "graph" }, envelope: answer });
  const markup = markupOf(renderComponent(frame));

  assert(markup.includes("<slot></slot>"), "the drawing is slotted into the frame");
  const hidden = markup.indexOf('aria-hidden="true"');
  assert(hidden >= 0, "and the element it is slotted into is hidden from assistive technology");
  const slot = markup.indexOf("<slot>");
  const close = markup.indexOf("</div>", slot);
  assert(hidden < slot && slot < close, "the slot is inside the hidden element, not beside it");

  const twinAt = markup.indexOf("<mst-twin");
  assert(twinAt > close, "and the twin is outside it");
  assertEqual(markup.indexOf('aria-hidden', twinAt), -1, "the twin is hidden from nobody");

  const twin = new MstTwin();
  twin.twin = twinFor(answer);
  const twinMarkup = markupOf(renderComponent(twin));
  assertEqual(twinMarkup.includes("aria-hidden"), false, "and hides nothing of its own");
  assert(twinMarkup.includes("<table"), "it is a table a screen reader can navigate");
});

// --- The painter -----------------------------------------------------

check("theComponentPaintsEveryRowAndEveryCell", () => {
  const model = twinFor(answer);
  const twin = new MstTwin();
  twin.twin = model;
  const items = renderComponent(twin);
  const bound = valuesOf(items).map((item) => item.value);

  const cells = [...model.nodes.rows, ...model.edges.rows].flatMap((row) => row.cells);
  for (const cell of cells) {
    assert(bound.includes(cell.text), `the component dropped a cell: ${JSON.stringify(cell)}`);
  }
  for (const column of [...model.nodes.columns, ...model.edges.columns]) {
    assert(bound.includes(column.label), `the component dropped the ${column.key} heading`);
  }
  assert(bound.includes(model.nodes.caption), "and it paints the captions the model wrote");
  assert(bound.includes(model.edges.caption), "both of them");
});

check("focusingARowSelectsItsNode", () => {
  const model = twinFor(answer);
  const twin = new MstTwin();
  twin.twin = model;
  const items = renderComponent(twin);

  // The handler the template really binds, taken out of the template
  // rather than called by name: a row wired to nothing would pass a test
  // that called `twin.select(row)` directly.
  const handlers = valuesOf(items).filter((item) => item.attribute === "@focus");
  assertEqual(handlers.length, model.nodes.rows.length, "every node row has a focus handler and no other row does");

  const third = model.nodes.rows[2];
  handlers[2].value();
  assertEqual(twin.dispatched.length, 1, "focusing a row announces one selection");
  const event = twin.dispatched[0];
  assertEqual(event.type, SELECT_EVENT, "under the name a canvas will listen for");
  assertDeepEqual(event.detail, { type: third.node.type, key: third.node.key },
    "carrying the node's two keys, which is how a node is addressed");
  assert(!JSON.stringify(event.detail).includes("id-"), "and never its id, which is not an address");
  assert(event.bubbles && event.composed, "and it leaves the shadow root, so a page can hear it");
  assertEqual(twin.selected, third.key, "and the row marks itself, which is the half that works with no canvas");
});

// --- Escaping --------------------------------------------------------

// A game named by somebody hostile. Every one of these is a string a
// designer or an agent can write into a game today.
const HOSTILE = '<img src=x onerror="alert(1)">';
const hostileEnvelope = {
  nodes: [
    {
      id: "h1",
      key: HOSTILE + "-key",
      type: HOSTILE + "-type",
      name: HOSTILE,
      set: "s",
      attrs: { label: HOSTILE, [HOSTILE + "-slot"]: HOSTILE + "-value" },
    },
  ],
  edges: [{ id: "he", type: HOSTILE + "-rel", source: "h1", target: "h2", label: HOSTILE + "-label" }],
  stats: { nodes: 1, edges: 1, max_depth_reached: 0, duration_ms: 1 },
  truncated: { nodes: false, edges: false, depth: false },
};

check("theTwinRendersEveryStringAsText", () => {
  const twin = new MstTwin();
  twin.twin = twinFor(hostileEnvelope);
  const items = renderComponent(twin);
  const markup = markupOf(items);

  // Half one: the component's own HTML never contains the game's words.
  // If it did, they would not be values at all and no escaping could
  // reach them.
  assert(!markup.includes("<img"), `a game string reached the component's static markup: ${markup}`);
  assert(!markup.includes("onerror"), "nor any part of one");

  // Half two: every game string is bound where Lit commits a Text node.
  const hostileValues = valuesOf(items).filter(
    (item) => typeof item.value === "string" && item.value.includes(HOSTILE),
  );
  assert(hostileValues.length >= 6, `every hostile string is bound: found ${hostileValues.length}`);
  for (const item of hostileValues) {
    assertEqual(
      item.position,
      "text",
      `a game string is bound in ${item.position} position (attribute ${item.attribute || "none"}), ` +
        "where it is not a text node: " + JSON.stringify(item.value),
    );
  }

  // And the names, the keys, the types, the slot heading, the value and
  // the edge label are all there — a twin that quietly dropped the
  // hostile column would pass the two halves above by rendering nothing.
  const bound = hostileValues.map((item) => item.value);
  for (const expected of [
    HOSTILE,
    HOSTILE + "-key",
    HOSTILE + "-type",
    HOSTILE + "-slot",
    HOSTILE + "-value",
    HOSTILE + "-label",
    HOSTILE + "-rel",
  ]) {
    assert(bound.includes(expected), `the twin dropped ${JSON.stringify(expected)} rather than rendering it`);
  }
});

check("theBindingPositionScannerReadsWhatItClaimsTo", () => {
  // The guard on the guard. A scanner that answered "text" to everything
  // would make the assertion above pass for any component at all, and
  // this repository has shipped a guard that audited itself twice.
  const value = "V";
  assertEqual(walk(html`<p>${value}</p>`).find((i) => i.kind === "value").position, "text",
    "a child binding is text");
  assertEqual(walk(html`<p title=${value}>x</p>`).find((i) => i.kind === "value").position, "attribute",
    "an unquoted attribute binding is not");
  assertEqual(walk(html`<p title="a ${value}">x</p>`).find((i) => i.kind === "value").position, "attribute",
    "nor is one inside quotes");
  assertEqual(walk(html`<script>${value}</script>`).find((i) => i.kind === "value").position, "raw",
    "a binding in a raw-text element is not a text node");
  assertEqual(walk(html`<!-- ${value} -->`).find((i) => i.kind === "value").position, "comment",
    "and a binding in a comment is not either");

  // It reads the attribute's name, which is what focusingARowSelectsItsNode
  // uses to find the handler the template really binds.
  const listener = walk(html`<tr @focus=${() => {}}>x</tr>`).find((i) => i.kind === "value");
  assertEqual(listener.attribute, "@focus", "an event binding is found by its name");

  // It recurses into nested results and into arrays, which is how a
  // table's rows arrive; a walk that stopped at the first level would
  // see none of the cells.
  const nested = walk(html`<table>
    ${[1, 2].map((n) => html`<tr>
      <td>${n}</td>
    </tr>`)}
  </table>`);
  const seen = valuesOf(nested);
  assertEqual(seen.length, 2, "both nested rows are walked");
  assert(
    seen.every((item) => item.position === "text"),
    "and their bindings are placed against the markup around them",
  );
  assert(markupOf(nested).includes("<td>"), "with the nested markup spliced into the stream");
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
  console.error(`${failures} twin check(s) failed`);
  process.exit(1);
}
console.log("twin.js: all checks passed");
