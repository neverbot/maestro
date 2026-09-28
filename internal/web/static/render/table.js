// The `table` renderer: rows and columns, and the most-used picture in
// the product per the catalogue's own comment — so it gets the most care
// and none of the drama.

import { addressOf } from "../address.js";
import { UNSET_LABEL, labelFor } from "../palette.js";
import {
  ABSENT_TEXT,
  COLUMN_KEY,
  COLUMN_NAME,
  COLUMN_TYPE,
  absentCell,
  presentCell,
  valueCell,
} from "./twin.js";
import { CONTROL_COLUMN, CONTROL_COLUMNS, CONTROL_COUNT, CONTROL_SLOT, control } from "./controls.js";

// The name the catalogue holds, and this module's.
export const RENDERER = "table";

// The renderer_params keys, spelled as the server spells them.
import {
  PARAM_COLUMNS,
  PARAM_SORT,
  PARAM_GROUP_BY,
  PARAM_PAGE_SIZE,
} from "./params.js";
export {
  PARAM_COLUMNS,
  PARAM_SORT,
  PARAM_GROUP_BY,
  PARAM_PAGE_SIZE,
} from "./params.js";

// The three built-in columns the envelope carries, in the catalogue's
// own spellings (`envelopeBuiltins` in internal/views/renderers.go).
// They are the entity's identity and never a projection slot, which is
// why they wear the sigil: a game may declare a field called `name`, and
// `@name` is not it.
export const BUILTIN_NAME = "@name";
export const BUILTIN_KEY = "@key";
export const BUILTIN_TYPE = "@type";
export const BUILTINS = [BUILTIN_NAME, BUILTIN_KEY, BUILTIN_TYPE];

// What a built-in column is drawn in (§4.7). It is a name in the model
// rather than a colour or a font stack, for the reason every other
// decision in this layer is data: the stylesheet paints it, a test can
// read it, and the three faces are styles.css's `--serif`, `--mono` and
// `--muted` rather than three values retyped here.
export const STYLE_SERIF = "serif";
export const STYLE_MONO = "mono";
export const STYLE_MUTED = "muted";
export const STYLE_PLAIN = "plain";

// The default order the columns are drawn in when the view declares
// none: the identity first, then every slot the answer carries. It is
// the twin's own order — `label` leads because execute.go always
// provides it — so a view that names no columns and the twin beside it
// are the same table.
export const DEFAULT_BUILTINS = [BUILTIN_NAME, BUILTIN_TYPE, BUILTIN_KEY];
const LABEL_SLOT = "label";

// Which way a column is read. `sort` opens ascending, which is what a
// designer means by "sorted by level"; a reader clicking the header
// takes it round.
export const ASCENDING = "asc";
export const DESCENDING = "desc";

// Where the ordering came from, because the two are different statements
// and a surface that had to infer them would infer wrongly: the view
// opened this way, or a reader clicked.
export const SORT_VIEW = "view";
export const SORT_READER = "reader";

// The caption of the group a node with no value for `group_by` falls
// into. The palette's own word for an absence in a legend row, called
// rather than restated, so a designer meets one word for one fact.
export const UNGROUPED_CAPTION = UNSET_LABEL;

// The controls. Each tooltip says what the knob does to the **picture**,
// which is the half internal/views/renderers.go deliberately does not
// carry (render/controls.js's header has the argument).
export const CONTROLS = [
  control(
    PARAM_COLUMNS,
    CONTROL_COLUMNS,
    "The columns to draw, in this order and no other. @name, @key and " +
      "@type are the entity's own identity; anything else is a projection " +
      "slot or a field the query carried. Left unset, the table draws the " +
      "identity and every slot the answer has.",
  ),
  control(
    PARAM_SORT,
    CONTROL_COLUMN,
    "The column the rows are in when the view opens. Clicking any header " +
      "re-sorts from there, without asking the server again — the rows are " +
      "all here already. A number column sorts as numbers, and rows with " +
      "nothing in that column go last either way.",
  ),
  control(
    PARAM_GROUP_BY,
    CONTROL_SLOT,
    "Breaks the rows into groups under a sticky heading, one per value of " +
      "this slot, each with its count. Rows whose slot found nothing make " +
      "the last group rather than the first.",
  ),
  control(
    PARAM_PAGE_SIZE,
    CONTROL_COUNT,
    "How many rows to show at once. It pages the rows already on this " +
      "machine and asks the server for nothing; when the answer itself hit " +
      "its cap the pager says so, so a page count is never read as a " +
      "content count.",
  ),
];

// --- The scene -------------------------------------------------------

// tableScene is the table.
export function tableScene(envelope, params = {}, options = {}) {
  const nodes = nodesOf(envelope);
  const config = readParams(params);
  const columns = columnsFor(nodes, config.columns);
  const sort = sortFor(columns, config.sort, options.sort);

  const rows = nodes.map((node, index) => ({
    key: addressOf(node),
    node: { type: text(node.type), key: text(node.key) },
    // `index` is the answer's own order, kept so that the ordering below
    // is stable **by construction** rather than by the engine's sort
    // happening to be. Two rows equal on the sort column stay in the
    // order the server returned them, on every browser and for ever.
    index,
    cells: columns.map((column) => cellFor(node, column)),
  }));

  if (sort !== null) order(rows, columns, sort);

  const groups = config.groupBy === null ? null : groupsFor(rows, nodes, config.groupBy);
  if (groups !== null) regroup(rows, groups);

  return {
    columns,
    rows,
    groups,
    sort,
    page: pageFor(rows, config.pageSize, options.page, truncatedNodes(envelope)),
  };
}

// --- The columns -----------------------------------------------------

// columnsFor is the declared columns in their declared order, or the
// default table when the view names none.
function columnsFor(nodes, declared) {
  const names = declared !== null ? declared : [...DEFAULT_BUILTINS, ...slotsOf(nodes)];
  const seen = new Set();
  const columns = [];
  for (const name of names) {
    if (typeof name !== "string" || name === "" || seen.has(name)) continue;
    seen.add(name);
    columns.push({
      key: name,
      // A built-in is drawn under its bare word: `@name` is the sigil the
      // *query language* uses to tell an identity from a field, and a
      // designer reading a table has no such ambiguity to resolve.
      label: BUILTINS.includes(name) ? name.slice(1) : name,
      builtin: BUILTINS.includes(name),
      style: styleOf(name),
    });
  }
  return columns;
}

// styleOf is §4.7's defaults, and it gives them to the built-ins only:
// the name is the editorial thing on the row, a key is an identifier and
// reads as one, a type is context rather than content. A projection slot
// is the game's own value and this interface has no opinion about its
// face.
function styleOf(name) {
  switch (name) {
    case BUILTIN_NAME:
      return STYLE_SERIF;
    case BUILTIN_KEY:
      return STYLE_MONO;
    case BUILTIN_TYPE:
      return STYLE_MUTED;
    default:
      return STYLE_PLAIN;
  }
}

// slotsOf is the union of every slot any node carries, `label` first and
// the rest in JSON-text order — render/twin.js's own rule, because the
// default table and the twin are the same table and two orders would
// make them two.
function slotsOf(nodes) {
  const seen = new Set();
  for (const node of nodes) {
    if (!isObject(node.attrs)) continue;
    for (const slot of Object.keys(node.attrs)) seen.add(slot);
  }
  const rest = [...seen].filter((slot) => slot !== LABEL_SLOT).sort();
  return seen.has(LABEL_SLOT) ? [LABEL_SLOT, ...rest] : rest;
}

// cellFor is one cell, and the two arms are the two kinds of column.
function cellFor(node, column) {
  switch (column.key) {
    case BUILTIN_NAME:
      return presentCell(COLUMN_NAME, text(node.name));
    case BUILTIN_KEY:
      return presentCell(COLUMN_KEY, text(node.key));
    case BUILTIN_TYPE:
      return presentCell(COLUMN_TYPE, text(node.type));
    default:
      return valueCell(node, column.key);
  }
}

// --- The order -------------------------------------------------------

// sortFor decides which ordering is in force: the reader's click, or the
// view's own `sort`, or none.
function sortFor(columns, declared, clicked) {
  const has = (name) => columns.some((column) => column.key === name);
  if (clicked && typeof clicked === "object" && has(clicked.column)) {
    return {
      column: clicked.column,
      direction: clicked.direction === DESCENDING ? DESCENDING : ASCENDING,
      source: SORT_READER,
    };
  }
  if (declared !== null && has(declared)) {
    // Ascending on open, which is what a designer means by "sorted by
    // level". The catalogue's parameter carries no direction, so
    // inventing a descending default would be this interface deciding
    // something the document did not.
    return { column: declared, direction: ASCENDING, source: SORT_VIEW };
  }
  return null;
}

// order sorts the rows in place.
function order(rows, columns, sort) {
  const at = columns.findIndex((column) => column.key === sort.column);
  if (at < 0) return;
  const sign = sort.direction === DESCENDING ? -1 : 1;
  rows.sort((a, b) => {
    const left = a.cells[at];
    const right = b.cells[at];
    if (left.absent !== right.absent) return left.absent ? 1 : -1;
    if (!left.absent) {
      const compared = compareValues(left, right);
      if (compared !== 0) return sign * compared;
    }
    return a.index - b.index;
  });
}

function compareValues(left, right) {
  if (typeof left.value === "number" && typeof right.value === "number") {
    return left.value < right.value ? -1 : left.value > right.value ? 1 : 0;
  }
  return left.text < right.text ? -1 : left.text > right.text ? 1 : 0;
}

// --- The groups ------------------------------------------------------

// groupsFor is one entry per value of the `group_by` slot, each with the
// count §4.7's sticky sub-header carries.
function groupsFor(rows, nodes, groupBy) {
  const byAddress = new Map(nodes.map((node) => [addressOf(node), node]));
  const groups = new Map();
  for (const row of rows) {
    const json = valueJSON(byAddress.get(row.key), groupBy);
    const value = json === null ? null : json;
    const entry = groups.get(value) || {
      value,
      caption: value === null ? UNGROUPED_CAPTION : labelFor(value),
      rows: [],
    };
    entry.rows.push(row);
    groups.set(value, entry);
  }
  const present = [...groups.values()]
    .filter((group) => group.value !== null)
    .sort((a, b) => (a.value < b.value ? -1 : a.value > b.value ? 1 : 0));
  const missing = groups.get(null);
  return [...present, ...(missing ? [missing] : [])];
}

// regroup rewrites the row order so that the groups are contiguous, and
// records where each one starts.
function regroup(rows, groups) {
  let at = 0;
  const ordered = [];
  for (const group of groups) {
    group.from = at;
    group.count = group.rows.length;
    for (const row of group.rows) ordered.push(row);
    at += group.rows.length;
    delete group.rows;
  }
  rows.length = 0;
  for (const row of ordered) rows.push(row);
}

// --- The pager -------------------------------------------------------

// pageFor is which rows are on screen and the sentence beside them.
function pageFor(rows, size, requested, capped) {
  const total = rows.length;
  const perPage = size === null || size >= total ? total : size;
  const pages = perPage > 0 ? Math.max(1, Math.ceil(total / perPage)) : 1;
  const number = clamp(Number.isFinite(requested) ? Math.trunc(requested) : 1, 1, pages);
  const from = total === 0 ? 0 : (number - 1) * perPage + 1;
  const to = total === 0 ? 0 : Math.min(number * perPage, total);
  const paged = pages > 1;
  return {
    rows: total === 0 ? [] : rows.slice(from - 1, to),
    from,
    to,
    total,
    size: perPage,
    number,
    pages,
    capped,
    // An en dash between two numbers of a range, which is what a range
    // is written with.
    text: paged || capped ? `showing ${from}–${to} of ${total}` + (capped ? ", capped" : "") : "",
  };
}

function clamp(value, low, high) {
  return value < low ? low : value > high ? high : value;
}

// --- Reading the parameters ------------------------------------------

// readParams is the one reader of renderer_params.
function readParams(params) {
  const p = params && typeof params === "object" ? params : {};
  const columns = Array.isArray(p[PARAM_COLUMNS])
    ? p[PARAM_COLUMNS].filter((name) => typeof name === "string" && name !== "")
    : null;
  const size = p[PARAM_PAGE_SIZE];
  return {
    // An empty list is not a table with no columns — the server refuses
    // one — so it reads as "the view named none", which is the default
    // table rather than a header with nothing under it.
    columns: columns === null || columns.length === 0 ? null : columns,
    sort: slot(p[PARAM_SORT]),
    groupBy: slot(p[PARAM_GROUP_BY]),
    pageSize:
      typeof size === "number" && Number.isFinite(size) && size >= 1 ? Math.trunc(size) : null,
  };
}

function slot(value) {
  return typeof value === "string" && value !== "" ? value : null;
}

// --- Reading the envelope --------------------------------------------

function nodesOf(envelope) {
  const env = envelope && typeof envelope === "object" ? envelope : {};
  return Array.isArray(env.nodes) ? env.nodes.filter(isObject) : [];
}

function truncatedNodes(envelope) {
  const env = envelope && typeof envelope === "object" ? envelope : {};
  const truncated = isObject(env.truncated) ? env.truncated : {};
  return truncated.nodes === true;
}

function valueJSON(node, key) {
  const attrs = isObject(node) && isObject(node.attrs) ? node.attrs : null;
  if (!attrs || !Object.prototype.hasOwnProperty.call(attrs, key)) return null;
  return JSON.stringify(attrs[key]);
}

function isObject(value) {
  return value !== null && typeof value === "object";
}

function text(value) {
  return typeof value === "string" ? value : "";
}

// ABSENT_TEXT is re-exported so a caller that has this module has the
// mark too, without a second import of the twin — and, more to the
// point, without a second spelling of it.
export { ABSENT_TEXT };
