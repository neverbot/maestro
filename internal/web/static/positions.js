// A saved arrangement, in the one spelling the wire uses, and where a
// coordinate came from.

import { addressOf } from "./address.js";

// The three modes 0008_views.sql accepts, and the default it applies.
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
export const SOURCE_STORED = "stored";
export const SOURCE_COMPUTED = "computed";
export const SOURCE_GRID = "grid";

// storedFrom normalises the envelope's `positions[]` into `{key, x, y,
// pinned}` rows, keyed by the product's one address.
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
