// The `table` renderer: rows and columns, and the most-used picture in
// the product per the catalogue's own comment — so it gets the most care
// and none of the drama.
//
// A renderer is a **pure function from an envelope to a scene** — the
// rule render/graph.js's header sets out — and this one departs from the
// other five in exactly one way, which is the whole of what makes it
// different: **it emits no marks.** A table is rows on paper with
// hairline rules; there is no coordinate anywhere in it, nothing to pan,
// nothing to drag, and a `marks: []` on the way out would be a mechanism
// nothing reads pretending to be a picture. What it returns is the model
// a route paints as a document (Task 15), in the same shape the text
// twin already returns, which is not a coincidence:
//
// **This renderer and the text twin are the same table, drawn twice.**
// render/twin.js is the accessible content of every view, built from the
// envelope alone; this is the *view* for a designer who asked for rows.
// So the cells come from twin.js — `valueCell`, `presentCell`,
// `absentCell` — rather than from a second implementation, because the
// em dash, the `absent` flag and the palette's own text rule are one
// decision each and a table that respelled any of them would tell a
// reader something different about the same answer.
//
// **Where the two deliberately differ**, since a difference nobody
// states is a defect nobody finds:
//
//   columns — the twin shows every slot any node carries, in its own
//     order, because it describes the answer. This shows the columns the
//     **view declares**, in the order it declares them, because a
//     designer chose them. A table that reordered them would be
//     answering a question nobody asked.
//   paging — the twin has none. `page_size` pages the rows the client
//     **already holds**: views.run has no cursor, and §4.7 is explicit
//     that the pager says so when the result is also truncated, so
//     nobody reads a page count as a content count.
//   edges — the twin has an edge table and this has none. The catalogue
//     says `table` consumes "nodes only" and deliberately does **not**
//     refuse a query that draws edges, because one envelope for every
//     renderer is what lets a saved view swap `graph` for `table`
//     without rewriting its query. The edges are still in the answer and
//     still in the twin; this renderer simply draws no relation.
//
// **`color_by` is not offered here and is not honoured here.** The
// catalogue gives this renderer no such parameter, and §4.7 says why: a
// slot another renderer would colour is a **column** in a table, which
// is the honest form of the same information. A table that tinted a row
// would be inventing a channel the catalogue refused it, and one a
// reader who cannot separate two hues could not read at all.
//
// **What it draws when the answer is not a clean one:**
//
//   an absent value — an em dash, never a blank cell: an empty cell is
//     indistinguishable from a rendering bug. The empty string is a
//     blank cell, because that is what it is.
//   zero rows — the header row stays. A table with no header is not an
//     empty table, it is a broken one, and the sentence sits under it.
//   a truncated answer — the pager says `capped` beside the count and
//     the frame bands it. The count itself is never adjusted for it:
//     the client holds what it holds.
//   a group value that is absent — its own group, captioned in the
//     palette's own words and placed **last**, exactly as `layered`'s
//     unranked band is: a group of absences first would read as the
//     leading group of the answer.

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
export const PARAM_COLUMNS = "columns";
export const PARAM_SORT = "sort";
export const PARAM_GROUP_BY = "group_by";
export const PARAM_PAGE_SIZE = "page_size";

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
//
// `page_size`'s is the one worth reading twice, for `nested`'s
// `max_depth` reason: a designer who reads it as "how much was fetched"
// has the mechanism exactly backwards, and the pager's own `capped` is
// the only thing in this interface that says how much was not.
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
//
// `options`:
//   sort — what a reader clicked: `{column, direction}`. It overrides
//          the view's `sort`, which is what "sort applied on open and
//          re-sortable by clicking a header" means, and it is where the
//          click ends: there is no fetch anywhere in this module and
//          nothing to fetch, because views.run returned every row.
//   page — which page to show, 1-based. Out of range is clamped rather
//          than empty: a page number nobody can reach is a table that
//          looks like it lost its rows.
//
// Returns:
//   columns — `{key, label, builtin, style}` in the order they are
//             drawn.
//   rows    — every row of the answer, ordered, before paging. The twin
//             has one of these per node and so does this.
//   page    — `{rows, from, to, total, size, number, pages, capped,
//             text}`. `rows` is the page's own rows.
//   groups  — `{value, caption, count, from}` per group, or null when
//             the view groups nothing. Null and not empty: "this view
//             does not group" is not "this slot had no values".
//   sort    — `{column, direction, source}`, or null.
//   marks   — deliberately absent. See the header.
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
//
// **No column is dropped and none is invented.** A declared column
// naming a slot no node carries is still a column: internal/views'
// `column` checker refuses one the query cannot produce at *save* time,
// so a client that silently dropped it would be hiding a view the server
// already judged, and a designer would be left with a table that is
// missing a column and says nothing about it. Every one of its cells is
// simply absent, which is the honest answer and the one a reader can act
// on.
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
//
// A built-in is the entity's own identity, which every node has and
// which is therefore never absent — the empty string is a legal name and
// reads as an empty cell, which is exactly what it is. Everything else
// goes through the twin's own `valueCell`, so that a slot that found
// nothing is an em dash here and an em dash there, once.
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
//
// A click on a column this table does not draw is not an ordering, and
// neither is a `sort` naming one — the catalogue refuses the second at
// save time, so meeting it means meeting a document the server would not
// have stored.
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
//
// **A number column sorts as numbers.** The cell carries its underlying
// value beside its text (render/twin.js), and comparing the text would
// put 10 before 9 — a wrong answer that looks like a right one, in the
// renderer a designer reaches for most.
//
// **A row with nothing in the sort column goes last, in both
// directions.** Reversing the direction reverses the rows that have an
// answer; it does not promote the ones that have none to the top, where
// they would read as the extreme of the column rather than as its
// absence.
//
// **Ties keep the answer's own order**, by comparing `index` last, so
// stability is a property of this function rather than of the engine's
// sort algorithm.
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
//
// The groups are ordered by value, and the absence is **last**: a group
// of "the ones we know nothing about" at the top would read as the
// leading group of the answer, which is the same mistake `layered`
// refuses when it puts its unranked band at the end.
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
//
// The rows **inside** a group keep the ordering the sort gave them:
// grouping is a second axis and not a second sort, and a designer who
// sorted by level and grouped by zone means "by zone, and by level
// within it".
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
//
// **`total` is the rows this client holds and never an estimate of the
// game.** views.run has no cursor — a page of a graph is not a graph —
// so a result too large for one response is truncated and flagged, and
// this pager pages what arrived. `capped` is that flag, said out loud
// beside the count, so nobody reads a page count as a content count.
//
// A single page of an untruncated answer says **nothing**: there is no
// sentence for the absence of a thing, which is render/scene.js's own
// first rule, and *"showing 1–7 of 7"* under a table of seven rows is a
// machine talking to itself.
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
//
// `color_by` is deliberately not among them and is not read anywhere in
// this module: the catalogue does not offer it, §4.7 says a slot another
// renderer would colour is a *column* here, and a table that honoured it
// would be drawing a channel the catalogue refused it.
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
