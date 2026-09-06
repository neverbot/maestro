// The text twin's model: a pure function from the run envelope to the
// two tables that *are* the accessible content of every view, the five
// graphical ones included.
//
// One property governs this file and every test of it:
//
// **The twin describes the answer, not the drawing.** Its row source is
// the envelope and nothing else. A node the graph renderer shelved
// because the layout could not place it, a node dropped beyond a depth
// bound, a node collapsed into a `+12 more` count chip — each still has
// a row here, with its name, its type, its key and every projected slot.
// The moment the twin is derived from a scene it stops being the ground
// truth the renderers are checked against and becomes a second, poorer
// rendering of the same picture; the six renderer tasks that follow
// assert their scene and this model describe the same answer, which is
// only a real assertion while the two are computed from the envelope
// independently. That is why this module imports no renderer, takes no
// scene, and has no parameter through which one could reach it.
//
// The second property is the one that turns the identity's central claim
// into something a test can fail: **colour is never the only carrier.**
// Every value that earns a hue on the canvas is written out here as
// text, through the same `labelFor` the legend uses, so a designer who
// cannot separate two hues — or cannot see them at all — reads the same
// answer off the twin.
//
// The third is the distinction internal/views/execute.go goes out of its
// way to preserve and which a table is the last place it could be
// thrown away: **a slot that found nothing is absent, and absent is not
// the empty string.** An absent cell carries ABSENT_TEXT and is flagged
// `absent`; a slot holding "" carries the empty string and is not. Two
// different cells, in the model and on the screen.
//
// It writes its own words — a caption, the em dash, the note on an edge
// endpoint the query chose not to draw — because they are its own
// statements about the answer and there is no server sentence for any of
// them. The component that paints it writes none, exactly as
// mst-view-frame.js writes none of scene.js's; that rule is held by
// TestEveryComponentSpeaksOnlyItsModelsWords in
// internal/web/static_frame_test.go.

import { labelFor } from "../palette.js";

// ABSENT_TEXT is what a projection slot that found nothing looks like.
//
// An em dash, and never a blank cell: a blank cell is what the *empty
// string* looks like, and folding the two together at the last step
// would discard end to end what the envelope, the palette's `unset`
// legend row and this table all keep apart. A reader sees a mark where
// there is no answer and nothing where the answer is nothing.
//
// Because a game may legitimately hold an em dash as a value, the mark
// is never the only carrier either: the cell also says `absent`, which
// the component paints as a class and which
// aValueThatLooksLikeTheAbsentMarkIsStillNotAbsent asserts is what tells
// the two apart.
export const ABSENT_TEXT = "—";

// OUTSIDE_NOTE labels the far end of a stub.
//
// An edge's endpoints are not guaranteed to be among the nodes, and this
// is ordinary rather than exceptional (internal/views/execute.go): an
// `edges: [{between: …}]` entry draws relations between sets the query
// chose not to draw. On the canvas that edge ends in mid-air; here its
// far end is the only address the envelope carries for it — the entity's
// id — said plainly, with this note, so a reader is told the row is
// complete and the picture is not.
export const OUTSIDE_NOTE = "outside this picture";

// The three columns every node row has before its projection slots. They
// are constants rather than bare strings at the call sites so that a
// renamed column is one visible diff, and so a test can ask for a column
// by identity rather than by matching a word.
export const COLUMN_NAME = "name";
export const COLUMN_TYPE = "type";
export const COLUMN_KEY = "key";

// And the four an edge row has. `label` is a column even when no edge
// carries one, because an absent label is an answer.
export const COLUMN_SOURCE = "source";
export const COLUMN_TARGET = "target";
export const COLUMN_LABEL = "label";

// LABEL_SLOT is the projection slot every node has (execute.go: "`label`
// is always there, because it defaults to the entity's name"). It leads
// the slot columns for that reason; the rest follow in JSON-text order,
// so the column order is a property of the query's projection and not of
// the order the nodes happened to arrive in.
const LABEL_SLOT = "label";

// twinFor builds both tables from one envelope.
//
// It takes the envelope alone. See the note at the top of this file for
// why there is no second parameter: a twin that could see the drawing is
// a twin that would eventually describe it.
export function twinFor(envelope) {
  const env = envelope && typeof envelope === "object" ? envelope : {};
  const nodes = Array.isArray(env.nodes) ? env.nodes.filter(isObject) : [];
  const edges = Array.isArray(env.edges) ? env.edges.filter(isObject) : [];

  const byID = new Map();
  for (const node of nodes) {
    if (typeof node.id === "string" && !byID.has(node.id)) byID.set(node.id, node);
  }

  return {
    nodes: nodeTable(nodes),
    edges: edgeTable(edges, byID),
  };
}

function nodeTable(nodes) {
  const slots = slotsOf(nodes);
  const columns = [
    { key: COLUMN_NAME, label: COLUMN_NAME },
    { key: COLUMN_TYPE, label: COLUMN_TYPE },
    { key: COLUMN_KEY, label: COLUMN_KEY },
    ...slots.map((slot) => ({ key: slot, label: slot })),
  ];
  const rows = nodes.map((node) => ({
    key: addressOf(node),
    // The two keys that address an entity, and never its id: this is
    // what the selection carries to the canvas, and
    // internal/web/static/client.js writes positions by the same pair for
    // the same reason — an id is not an address a re-seed keeps.
    node: { type: text(node.type), key: text(node.key) },
    cells: [
      present(COLUMN_NAME, text(node.name)),
      present(COLUMN_TYPE, text(node.type)),
      present(COLUMN_KEY, text(node.key)),
      ...slots.map((slot) => slotCell(node, slot)),
    ],
  }));
  return { caption: count(rows.length, "node", "nodes"), columns, rows };
}

// slotsOf is the union of every slot any node carries, not the slots of
// the first node.
//
// The projection is per node — a related attribute that resolved for one
// quest and found nothing for the next is a slot present on one and
// absent on the other — so reading the columns off one row would drop
// whole columns for every other row, and the node that had the answer
// would be the node whose answer vanished.
function slotsOf(nodes) {
  const seen = new Set();
  for (const node of nodes) {
    if (!isObject(node.attrs)) continue;
    for (const slot of Object.keys(node.attrs)) seen.add(slot);
  }
  const rest = [...seen].filter((slot) => slot !== LABEL_SLOT).sort();
  return seen.has(LABEL_SLOT) ? [LABEL_SLOT, ...rest] : rest;
}

// slotCell is where absent and empty stay two answers.
//
// `hasOwnProperty` and not `attrs[slot] === undefined`, the same test
// palette.js's legend makes, because the envelope's distinction is
// presence: a slot present with a null value is a value the game means,
// and it reads as `null` rather than as a blank.
function slotCell(node, slot) {
  const attrs = isObject(node.attrs) ? node.attrs : {};
  if (!Object.prototype.hasOwnProperty.call(attrs, slot)) return absent(slot);
  // labelFor is the palette's own rule, called rather than restated: a
  // string shows as the game wrote it, anything else as its JSON text,
  // so the twin cell of a coloured node is character for character the
  // legend row that names its hue.
  return present(slot, labelFor(JSON.stringify(attrs[slot])));
}

function edgeTable(edges, byID) {
  const columns = [
    { key: COLUMN_SOURCE, label: COLUMN_SOURCE },
    { key: COLUMN_TYPE, label: COLUMN_TYPE },
    { key: COLUMN_TARGET, label: COLUMN_TARGET },
    { key: COLUMN_LABEL, label: COLUMN_LABEL },
  ];
  const rows = edges.map((edge, index) => ({
    // The row's identity is its position in the answer, not `edge.id`:
    // execute.go is explicit that a relation id is not an address and
    // does not survive a re-seed, so nothing here stores one.
    key: String(index),
    cells: [
      endpointCell(COLUMN_SOURCE, edge.source, byID),
      present(COLUMN_TYPE, text(edge.type)),
      endpointCell(COLUMN_TARGET, edge.target, byID),
      // `label` is omitempty on the wire, so a label nobody asked for is
      // absent rather than empty, and reads as absent.
      Object.prototype.hasOwnProperty.call(edge, COLUMN_LABEL)
        ? present(COLUMN_LABEL, text(edge.label))
        : absent(COLUMN_LABEL),
    ],
  }));
  return { caption: count(rows.length, "edge", "edges"), columns, rows };
}

// endpointCell names the node an edge reaches, or says it is not here.
//
// A stub is not a defect and is not reported as one: it is an edge whose
// far end the query did not draw. The cell carries the id, because that
// is the whole of what the envelope says about that end, and the note
// that says so.
function endpointCell(column, id, byID) {
  const node = typeof id === "string" ? byID.get(id) : undefined;
  if (node) return present(column, text(node.name));
  return { column, text: text(id), absent: false, outside: true, note: OUTSIDE_NOTE };
}

function present(column, value) {
  return { column, text: value, absent: false, outside: false, note: "" };
}

function absent(column) {
  return { column, text: ABSENT_TEXT, absent: true, outside: false, note: "" };
}

// addressOf is the row's identity, and the value the twin compares
// against to know which row is selected. JSON so that a type or a key
// carrying the separator cannot forge another row's address.
function addressOf(node) {
  return JSON.stringify([text(node.type), text(node.key)]);
}

function isObject(value) {
  return value !== null && typeof value === "object";
}

function text(value) {
  return typeof value === "string" ? value : "";
}

// count writes a number beside the noun it counts, in the right number,
// as scene.js's own does. Two copies of three lines, deliberately: the
// alternative is one of these two modules importing the other, and the
// whole reason both are testable is that neither imports anything but
// the palette.
function count(n, one, many) {
  return `${n} ${n === 1 ? one : many}`;
}
