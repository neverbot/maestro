// The text twin's model: a pure function from the run envelope to the
// two tables that *are* the accessible content of every view, the five
// graphical ones included.

import { labelFor } from "../palette.js";
// The row's identity, and the value the twin compares against to know
// which row is selected. It is the product's one address function
// (../address.js) rather than a spelling of its own: the canvas joins a
// selected row to a laid-out node on this string, and two spellings of
// it would fail that join silently.
import { addressOf } from "../address.js";

// ABSENT_TEXT is the fallback mark for a value that was not there and
// has no column to be named after. It is the text half of
// render/marks.js's absent dash; `absentTextFor` below is what a cell
// with a column says instead.
export const ABSENT_TEXT = "—";

// absentTextFor is the **word** for a value that is not there, which is
// what the Named Absence Rule asks for: "no zone", not a dash. The
// catalogue has said it in words since the rule was written and these
// two tables said it with a mark, so one product spelled one rule two
// ways.
export function absentTextFor(column) {
  const name = typeof column === "string" ? column.trim() : "";
  return name === "" ? ABSENT_TEXT : "no " + name;
}

// OUTSIDE_NOTE labels the far end of a stub.
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
      ...slots.map((slot) => valueCell(node, slot)),
    ],
  }));
  return { caption: count(rows.length, "node", "nodes"), columns, rows };
}

// slotsOf is the union of every slot any node carries, not the slots of
// the first node.
function slotsOf(nodes) {
  const seen = new Set();
  for (const node of nodes) {
    if (!isObject(node.attrs)) continue;
    for (const slot of Object.keys(node.attrs)) seen.add(slot);
  }
  const rest = [...seen].filter((slot) => slot !== LABEL_SLOT).sort();
  return seen.has(LABEL_SLOT) ? [LABEL_SLOT, ...rest] : rest;
}

// valueCell is where absent and empty stay two answers, and it is
// **exported because render/table.js draws the same cell**.
export function valueCell(node, key) {
  for (const bag of [node && node.attrs, node && node.fields]) {
    if (!isObject(bag) || !Object.prototype.hasOwnProperty.call(bag, key)) continue;
    // labelFor is the palette's own rule, called rather than restated: a
    // string shows as the game wrote it, anything else as its JSON text,
    // so the twin cell of a coloured node is character for character the
    // legend row that names its hue.
    return present(key, labelFor(JSON.stringify(bag[key])), bag[key]);
  }
  return absent(key);
}

// present and absent are the two cell shapes, exported for valueCell's
// reason: a caller building a cell of its own — a table's built-in
// column, which is the node's own identity and never a slot — builds the
// same shape, so `absent` cannot come to mean two things.
export { present as presentCell, absent as absentCell };

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
function endpointCell(column, id, byID) {
  const node = typeof id === "string" ? byID.get(id) : undefined;
  if (node) return present(column, text(node.name));
  return { column, text: text(id), value: undefined, absent: false, outside: true, note: OUTSIDE_NOTE };
}

// `value` is the cell's underlying value, carried beside its text so a
// caller that has to *order* rows can compare 9 with 10 as numbers
// rather than as strings — a table sorted by a number column as text is
// a wrong answer that looks like a right one. The twin itself never
// reads it; it is undefined for the cells that have no such thing.
function present(column, value, raw) {
  return { column, text: value, value: raw, absent: false, outside: false, note: "" };
}

function absent(column) {
  return { column, text: absentTextFor(column), value: undefined, absent: true, outside: false, note: "" };
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
