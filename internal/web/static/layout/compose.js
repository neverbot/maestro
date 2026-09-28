// The composition rule: spec §5.3, and the answer to a question this
// product has carried since positions were added.

import { addressOf, layoutGraph, DEFAULT_NODE_WIDTH, DEFAULT_NODE_HEIGHT } from "./engine.js";
// The wire spelling of a saved arrangement, and the vocabulary for where
// a coordinate came from. They were declared here while the layout was
// their only reader; `map` reads a saved arrangement without running an
// engine at all (spec §5.1), so they moved to ../positions.js, which
// costs a caller nothing, and are re-exported so every caller in this
// layer keeps its one import. See that file's header.
import {
  DEFAULT_MODE,
  MODE_AUTO,
  MODE_MANUAL,
  MODE_MIXED,
  SOURCE_COMPUTED,
  SOURCE_GRID,
  SOURCE_STORED,
  normaliseMode,
  storedFrom,
} from "../positions.js";

export {
  addressOf,
  DEFAULT_MODE,
  MODE_AUTO,
  MODE_MANUAL,
  MODE_MIXED,
  SOURCE_COMPUTED,
  SOURCE_GRID,
  SOURCE_STORED,
  normaliseMode,
  storedFrom,
};

// How the pinned nodes determined the fit. Three answers, because the
// three are three different statements about the picture and a caller
// that had to infer them from a scale of exactly 1 would be inferring
// wrongly on the day a real fit produces one.
export const FIT_IDENTITY = "identity";
export const FIT_TRANSLATION = "translation";
export const FIT_SIMILARITY = "similarity";

// The gap between cells in the fallback grid. Not a design token: the
// grid is an admission of failure, not a drawing, and the only thing
// asked of it is that two boxes do not touch.
const GRID_GAP = 24;

// --- What the engine is asked to lay out -----------------------------

// subgraphFor answers "what does the engine run over", which is the one
// question that differs between the three modes before any arithmetic.
export function subgraphFor(mode, nodes, edges, stored) {
  const all = Array.isArray(nodes) ? nodes : [];
  if (normaliseMode(mode) !== MODE_MANUAL) {
    return { nodes: all.slice(), edges: Array.isArray(edges) ? edges.slice() : [] };
  }
  const placed = new Set(rowsOf(stored).map((row) => row.key));
  const unplaced = all.filter((node) => node && !placed.has(addressOf(node)));
  const inSubgraph = new Set(unplaced.map((node) => addressOf(node)));
  const kept = (Array.isArray(edges) ? edges : []).filter((edge) => {
    if (!edge || typeof edge !== "object") return false;
    const source = endpointKey(edge.source);
    const target = endpointKey(edge.target);
    return source !== null && target !== null && inSubgraph.has(source) && inSubgraph.has(target);
  });
  return { nodes: unplaced, edges: kept };
}

function endpointKey(value) {
  if (typeof value === "string") return value === "" ? null : value;
  if (value && typeof value === "object" && typeof value.key === "string" && value.key !== "") {
    return addressOf(value);
  }
  return null;
}

// --- The fit ---------------------------------------------------------

// fitTransform is step 2 of §5.3: the similarity transform that carries
// the computed shape onto the pinned coordinates, minimising squared
// error, with **translation and uniform scale and no rotation**.
export function fitTransform(pairs) {
  const usable = (Array.isArray(pairs) ? pairs : []).filter(
    (pair) =>
      pair &&
      pair.from &&
      pair.to &&
      Number.isFinite(pair.from.x) &&
      Number.isFinite(pair.from.y) &&
      Number.isFinite(pair.to.x) &&
      Number.isFinite(pair.to.y),
  );
  if (usable.length === 0) return { scale: 1, tx: 0, ty: 0, kind: FIT_IDENTITY, pins: 0 };

  const n = usable.length;
  let cx = 0;
  let cy = 0;
  let px = 0;
  let py = 0;
  for (const pair of usable) {
    cx += pair.from.x;
    cy += pair.from.y;
    px += pair.to.x;
    py += pair.to.y;
  }
  cx /= n;
  cy /= n;
  px /= n;
  py /= n;

  let numerator = 0;
  let denominator = 0;
  for (const pair of usable) {
    const dx = pair.from.x - cx;
    const dy = pair.from.y - cy;
    numerator += dx * (pair.to.x - px) + dy * (pair.to.y - py);
    denominator += dx * dx + dy * dy;
  }

  let scale = denominator > 0 ? numerator / denominator : 1;
  let kind = denominator > 0 ? FIT_SIMILARITY : FIT_TRANSLATION;
  if (!Number.isFinite(scale) || scale <= 0) {
    scale = 1;
    kind = FIT_TRANSLATION;
  }
  return { scale, tx: px - scale * cx, ty: py - scale * cy, kind, pins: n };
}

// --- The separation pass ---------------------------------------------

// separate is step 3 of §5.3, and it is a **reduction of overlap, not a
// guarantee of none**. Said here as well as at the top of the file
// because this is the function a later reader would otherwise read a
// promise into.
function separate(fixed, movers) {
  const obstacles = fixed.slice();
  for (const mover of movers) {
    for (const box of obstacles) {
      const overlapX = (mover.width + box.width) / 2 - Math.abs(mover.x - box.x);
      const overlapY = (mover.height + box.height) / 2 - Math.abs(mover.y - box.y);
      if (overlapX <= 0 || overlapY <= 0) continue;
      // A tie, and two nodes at exactly the same point, both resolve the
      // same way every time: along x, in the positive direction. An
      // arrangement that depended on which of two equal numbers a
      // comparison happened to prefer would be an arrangement that moved
      // between engine versions.
      if (overlapX <= overlapY) {
        mover.x += (mover.x >= box.x ? 1 : -1) * overlapX;
      } else {
        mover.y += (mover.y >= box.y ? 1 : -1) * overlapY;
      }
    }
    obstacles.push(mover);
  }
}

// --- The composition -------------------------------------------------

// compose applies the layout_mode contract. The server reads none of
// these values (0008_views.sql says so); this function is the reader,
// which is the answer to "what reads that column".
export function compose(mode, computed, stored) {
  const resolved = normaliseMode(mode);
  const laid = placementsOf(computed);
  const rows = rowsOf(stored);

  if (resolved === MODE_AUTO) {
    // `stored` is not read past this point and is never mutated: the
    // rows stay in the database untouched, so switching the view back to
    // `mixed` restores the arrangement a designer left behind.
    return {
      mode: resolved,
      placements: laid.map((box) => ({
        key: box.key,
        x: box.x,
        y: box.y,
        pinned: false,
        source: SOURCE_COMPUTED,
      })),
      transform: { scale: 1, tx: 0, ty: 0, kind: FIT_IDENTITY, pins: 0 },
      placedAutomatically: 0,
      draggable: false,
    };
  }

  const byKey = new Map(rows.map((row) => [row.key, row]));

  if (resolved === MODE_MANUAL) {
    const placements = [];
    for (const box of laid) {
      const row = byKey.get(box.key);
      placements.push(
        row
          ? { key: box.key, x: row.x, y: row.y, pinned: row.pinned, source: SOURCE_STORED }
          : { key: box.key, x: box.x, y: box.y, pinned: false, source: SOURCE_COMPUTED },
      );
    }
    const seen = new Set(placements.map((p) => p.key));
    for (const row of rows) {
      if (seen.has(row.key)) continue;
      placements.push({ key: row.key, x: row.x, y: row.y, pinned: row.pinned, source: SOURCE_STORED });
    }
    placements.sort(byPlacementKey);
    return {
      mode: resolved,
      placements,
      transform: { scale: 1, tx: 0, ty: 0, kind: FIT_IDENTITY, pins: 0 },
      placedAutomatically: placements.filter((p) => p.source === SOURCE_COMPUTED).length,
      draggable: true,
    };
  }

  // mixed, in the three steps §5.3 names.
  const pins = rows.filter((row) => row.pinned === true);
  if (pins.length === 0) {
    // §5.3 says two things about this case that pull apart, and this is
    // the reading. It says an unpinned node with a stored row is used as
    // its starting coordinate "in step 3", and it says that with 0
    // pinned nodes the transform is the identity and mixed "behaves as
    // auto". With nothing pinned there is no step 3 to start from —
    // there is no fit and nothing to separate against — so the second
    // sentence is the one that applies, and the result is the auto
    // answer exactly.
    // "0 is the identity and mixed behaves as auto." Which means the
    // whole of the auto answer, separation pass included — there is
    // nothing to separate *from*, and running one anyway would make
    // `mixed` and `auto` two different pictures of the same graph, which
    // is the one thing this branch exists to deny. Only `draggable`
    // differs, and it must: a drag here pins a node and is precisely how
    // a designer leaves this case.
    const auto = compose(MODE_AUTO, computed, rows);
    return { ...auto, mode: resolved, draggable: true };
  }

  const computedByKey = new Map(laid.map((box) => [box.key, box]));
  const transform = fitTransform(
    pins
      .filter((pin) => computedByKey.has(pin.key))
      .map((pin) => ({ from: computedByKey.get(pin.key), to: pin })),
  );

  // Step 2 applied to the whole shape.
  const fitted = new Map();
  for (const box of laid) {
    fitted.set(box.key, {
      key: box.key,
      x: transform.scale * box.x + transform.tx,
      y: transform.scale * box.y + transform.ty,
      width: box.width,
      height: box.height,
    });
  }

  // Step 3. Every pinned node goes to its exact stored coordinate — the
  // stored number itself, not a number computed from it, so that
  // `pinnedNodesNeverMove` can assert `===` and mean it — and only the
  // unpinned ones move.
  const pinnedBoxes = [];
  const moving = [];
  const keys = new Set([...fitted.keys(), ...rows.map((row) => row.key)]);
  for (const key of Array.from(keys).sort(compareKeys)) {
    const row = byKey.get(key);
    const box = fitted.get(key);
    // A pinned node the engine was not asked to place — every retained
    // node of an incremental re-run — has no computed box, so its size
    // comes off the stored row when the caller knows it (rerunPlan
    // carries it over from the new envelope's measurements) and off the
    // engine's own default when nobody does. A wrong size here costs a
    // nudge in the separation pass and nothing else.
    const width = size(box, row, "width", DEFAULT_NODE_WIDTH);
    const height = size(box, row, "height", DEFAULT_NODE_HEIGHT);
    if (row && row.pinned === true) {
      pinnedBoxes.push({ key, x: row.x, y: row.y, width, height, source: SOURCE_STORED });
      continue;
    }
    if (row) {
      // An unpinned node that *has* a stored row (written with
      // `pinned: false`) starts from that coordinate and may be moved by
      // the pass. That is the only difference between an unpinned stored
      // row and no row at all in this mode, and it is the difference
      // `manual` does not make.
      moving.push({ key, x: row.x, y: row.y, width, height, source: SOURCE_STORED });
      continue;
    }
    moving.push({ key, x: box.x, y: box.y, width, height, source: SOURCE_COMPUTED });
  }

  separate(pinnedBoxes, moving);

  // separate() mutates only its movers, so a pinned box still carries
  // the stored number itself rather than a copy computed from it.
  const placements = [
    ...pinnedBoxes.map((box) => ({
      key: box.key,
      x: box.x,
      y: box.y,
      pinned: true,
      source: SOURCE_STORED,
    })),
    ...moving.map((box) => ({
      key: box.key,
      x: box.x,
      y: box.y,
      pinned: false,
      source: box.source,
    })),
  ].sort(byPlacementKey);

  return {
    mode: resolved,
    placements,
    transform,
    placedAutomatically: placements.filter((p) => p.source === SOURCE_COMPUTED).length,
    draggable: true,
  };
}

// --- The fallback ----------------------------------------------------

// gridFallback is what the canvas draws when the budget ran out: a
// deterministic grid, **ordered by node type then entity key**, which is
// the order a designer can scan for the node they were looking for.
export function gridFallback(nodes, options = {}) {
  const gap = Number.isFinite(options.gap) ? options.gap : GRID_GAP;
  const boxes = (Array.isArray(nodes) ? nodes : [])
    .filter((node) => node && typeof node === "object" && typeof node.key === "string" && node.key !== "")
    .map((node) => ({
      type: typeof node.type === "string" ? node.type : "",
      entityKey: node.key,
      key: addressOf(node),
      width: Number.isFinite(node.width) && node.width > 0 ? node.width : DEFAULT_NODE_WIDTH,
      height: Number.isFinite(node.height) && node.height > 0 ? node.height : DEFAULT_NODE_HEIGHT,
    }));
  // Type then key, on the tuple rather than on the address string, so
  // the order is the order the spec names and not whatever JSON's
  // escaping does to a type holding a quote.
  boxes.sort((a, b) => compareKeys(a.type, b.type) || compareKeys(a.entityKey, b.entityKey));

  const seen = new Set();
  const unique = boxes.filter((box) => (seen.has(box.key) ? false : (seen.add(box.key), true)));
  if (unique.length === 0) return [];

  const columns = Math.max(1, Math.ceil(Math.sqrt(unique.length)));
  const cellWidth = Math.max(...unique.map((box) => box.width)) + gap;
  const cellHeight = Math.max(...unique.map((box) => box.height)) + gap;
  return unique.map((box, index) => ({
    key: box.key,
    x: (index % columns) * cellWidth + cellWidth / 2,
    y: Math.floor(index / columns) * cellHeight + cellHeight / 2,
    pinned: false,
    source: SOURCE_GRID,
  }));
}

// --- Re-runs ---------------------------------------------------------

// rerunPlan is spec §5.5: a re-run must not reshuffle a picture the
// designer is working in.
export function rerunPlan(previous, nodes, options = {}) {
  const held = new Map();
  for (const placement of Array.isArray(previous) ? previous : []) {
    if (!placement || typeof placement.key !== "string") continue;
    if (!Number.isFinite(placement.x) || !Number.isFinite(placement.y)) continue;
    held.set(placement.key, placement);
  }
  const all = (Array.isArray(nodes) ? nodes : []).filter(
    (node) => node && typeof node === "object" && typeof node.key === "string" && node.key !== "",
  );
  const retained = [];
  const fresh = [];
  for (const node of all) {
    const key = addressOf(node);
    const previousPlacement = held.get(key);
    if (previousPlacement) {
      retained.push({
        key,
        x: previousPlacement.x,
        y: previousPlacement.y,
        pinned: true,
        width: Number.isFinite(node.width) ? node.width : undefined,
        height: Number.isFinite(node.height) ? node.height : undefined,
      });
    } else {
      fresh.push(node);
    }
  }

  if (options.rearrange === true) {
    return { full: true, reason: "rearrange", retained: [], fresh: all.slice() };
  }
  // "Under half", strictly: a retained set that is exactly half is still
  // half a picture the designer recognises.
  if (retained.length * 2 < all.length) {
    return { full: true, reason: "mostly_new", retained: [], fresh: all.slice() };
  }
  return { full: false, reason: "incremental", retained, fresh };
}

// --- The one call the worker makes -----------------------------------

// layoutView is everything above, in order, and is what worker.js calls.
export function layoutView(request = {}, { engine = layoutGraph } = {}) {
  const mode = normaliseMode(request.mode);
  const nodes = (Array.isArray(request.nodes) ? request.nodes : []).filter(
    (node) => node && typeof node === "object" && typeof node.key === "string" && node.key !== "",
  );
  const edges = Array.isArray(request.edges) ? request.edges : [];
  const inPicture = new Set(nodes.map(addressOf));
  // `positions` is the envelope's own array, in the envelope's own
  // spelling: the worker is handed what the server said and normalises
  // it here, so there is one reader of that spelling in the product.
  const stored = storedFrom(request.positions).filter((row) => inPicture.has(row.key));

  const plan = rerunPlan(request.previous, nodes, { rearrange: request.rearrange === true });

  if (!plan.full) {
    // An incremental re-run is a `mixed` composition whose pins are the
    // retained nodes: they keep their coordinates exactly, and the fresh
    // nodes are separated against them as obstacles. The fit degenerates
    // to the identity on its own — a pin the engine was not asked to
    // place contributes no pair — so no branch is needed for it.
    const honourStored = mode !== MODE_AUTO;
    const freshKeys = new Set(plan.fresh.map(addressOf));
    const freshStored = honourStored ? stored.filter((row) => freshKeys.has(row.key)) : [];
    const subgraph = subgraphFor(MODE_MANUAL, plan.fresh, edges, freshStored);
    const computed = engine(subgraph.nodes, subgraph.edges, request.options);
    const composed = compose(MODE_MIXED, computed, [...plan.retained, ...freshStored]);
    return { ...composed, mode, draggable: honourStored, rerun: plan.reason };
  }

  const subgraph = subgraphFor(mode, nodes, edges, stored);
  const computed = engine(subgraph.nodes, subgraph.edges, request.options);
  return { ...compose(mode, computed, stored), rerun: plan.reason };
}

// --- Small shared things ---------------------------------------------

function placementsOf(computed) {
  if (Array.isArray(computed)) return computed;
  if (computed && typeof computed === "object" && Array.isArray(computed.placements)) {
    return computed.placements;
  }
  return [];
}

// rowsOf takes rows that are already normalised (`{key, x, y, pinned}`)
// and leaves them alone; storedRows additionally accepts the wire.
function rowsOf(stored) {
  const rows = [];
  for (const row of Array.isArray(stored) ? stored : []) {
    if (!row || typeof row !== "object") continue;
    if (typeof row.key !== "string" || row.key === "") continue;
    if (!Number.isFinite(row.x) || !Number.isFinite(row.y)) continue;
    rows.push({ key: row.key, x: row.x, y: row.y, pinned: row.pinned !== false });
  }
  return rows;
}

function size(box, row, field, fallback) {
  if (box && Number.isFinite(box[field]) && box[field] > 0) return box[field];
  if (row && Number.isFinite(row[field]) && row[field] > 0) return row[field];
  return fallback;
}

function byPlacementKey(a, b) {
  return compareKeys(a.key, b.key);
}

function compareKeys(a, b) {
  return a < b ? -1 : a > b ? 1 : 0;
}
