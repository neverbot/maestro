// A saved arrangement, in the one spelling the wire uses, and where a
// coordinate came from.
//
// This is address.js's argument one step along, for the same reason and
// with the same shape. `storedFrom` and the three `source` constants
// were layout/compose.js's, which was right while the layout was their
// only reader — but compose.js imports layout/engine.js, which imports
// the vendored dagre, and `map` reads a saved arrangement *without ever
// running an engine* (design spec §5.1 is explicit: "map's unplaced
// nodes go to the shelf, not through the engine"). A renderer that had
// to load 48 kB of graph algorithm to learn that a designer dragged a
// node would be paying for a mechanism it never calls.
//
// So the reader and the vocabulary live here, with no dependency but
// address.js, and compose.js re-exports them: every caller in the layout
// layer keeps its one import and Task 6's guards are untouched, while
// render/ imports the module that costs nothing.
//
// **One spelling, and it is the documented one.** `internal/views.Position`
// carries json tags, so a run marshals a stored position as
// `{"entity_type","entity_key","x","y","pinned","updated_at"}` — the same
// snake_case `views.set_positions` takes and internal/web/static/client.js
// sends. internal/web/static_layout_test.go pins that spelling by
// marshalling the struct rather than by quoting it.

import { addressOf } from "./address.js";

// The three modes 0008_views.sql accepts, and the default it applies.
//
// They lived in layout/compose.js while the layout was their only
// reader, and they moved here for exactly the reason `storedFrom` did:
// the canvas has to know whether a drag may be written *before* it knows
// whether there is anything to lay out, and compose.js imports
// layout/engine.js, which imports 48 kB of vendored dagre. A drag layer
// that had to load a graph algorithm to learn that this view lays itself
// out automatically would be paying for a mechanism it never calls.
// compose.js re-exports them, so every caller in the layout layer keeps
// its one import.
export const MODE_AUTO = "auto";
export const MODE_MANUAL = "manual";
export const MODE_MIXED = "mixed";
export const DEFAULT_MODE = MODE_MIXED;

// normaliseMode is the one place an unknown or absent spelling becomes
// the column's own default. It was compose.js's private helper and is
// exported here because the canvas asks the same question of the same
// value and a second normalisation is a second answer.
export function normaliseMode(mode) {
  return mode === MODE_AUTO || mode === MODE_MANUAL || mode === MODE_MIXED ? mode : DEFAULT_MODE;
}

// readsPositions answers whether a saved arrangement is read back in
// this mode, which is the whole of "may a drag be written here".
//
// `auto` ignores stored rows entirely (compose.js's own first branch),
// so a drag there would write a row nothing would ever read — this
// project's oldest recurring defect wearing a mouse. `manual` and
// `mixed` both read them.
export function readsPositions(mode) {
  return normaliseMode(mode) !== MODE_AUTO;
}

// snapsPositions is narrower than readsPositions and deliberately so.
// The grid is `map`'s `snap` knob, whose own tooltip says it is read in
// `manual` mode only — `mixed` re-fits the whole picture through a
// similarity transform, so a coordinate landed on the grid at write time
// is not on the grid when it is drawn back, and a grid that lies about
// where a node will sit is worse than no grid.
export function snapsPositions(mode) {
  return normaliseMode(mode) === MODE_MANUAL;
}

// Where a coordinate came from. Read by the canvas (an unpinned node is
// drawn with a hollow anchor rather than a solid one, spec §4.2), by
// `compose`'s automatic-placement count, and by render/map.js, which
// tells a coordinate a designer chose from one this client computed.
//
// Three answers rather than a boolean, because the three are three
// different statements about a picture and a caller that had to infer
// them from a scale of exactly 1, or from a grid that happens to look
// tidy, would be inferring wrongly on the day a real fit produces one.
export const SOURCE_STORED = "stored";
export const SOURCE_COMPUTED = "computed";
export const SOURCE_GRID = "grid";

// storedFrom normalises the envelope's `positions[]` into `{key, x, y,
// pinned}` rows, keyed by the product's one address.
//
// `pinned` defaults to **true**, which is 0008_views.sql's own default
// and views.set_positions': a row written without the flag is a node a
// human put somewhere.
//
// A row whose coordinates are not both finite is not a position and is
// dropped. `(0, 0)` is emphatically *not* such a row: the origin is a
// place a designer may deliberately have used, which is the whole reason
// internal/views/execute.go refuses to return "unplaced" as a
// coordinate.
export function storedFrom(positions) {
  const rows = [];
  for (const row of Array.isArray(positions) ? positions : []) {
    if (!row || typeof row !== "object") continue;
    const key = row.entity_key;
    if (typeof key !== "string" || key === "") continue;
    const x = row.x;
    const y = row.y;
    if (!Number.isFinite(x) || !Number.isFinite(y)) continue;
    const pinned = row.pinned;
    rows.push({
      key: addressOf({ type: row.entity_type, key }),
      x,
      y,
      pinned: pinned === undefined ? true : pinned === true,
    });
  }
  return rows;
}
