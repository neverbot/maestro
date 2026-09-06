// The composition rule: spec §5.3, and the answer to a question this
// product has carried since positions were added.
//
// `views.layout_mode` is stored, validated and returned by the server,
// and **read by no server code at all** — 0008_views.sql says so in as
// many words: it is a contract with the client. This module is that
// client. `compose` is the only reader of the column anywhere in the
// product, which is what makes the column something other than a knob
// that lies, and it is also why `auto` disables dragging: a drag in
// `auto` would write a `view_positions` row that nothing would ever
// read back, which is this project's oldest recurring defect wearing a
// mouse.
//
// Everything here is pure: plain data in, plain data out, no DOM, no
// time, no randomness. The engine is injected into `layoutView` so a
// stub that never answers can be driven through the budget, and so this
// whole file runs under `node internal/web/jstest/layout_test.mjs`.
//
// **Two honesties, stated here rather than only in a report.**
//
// 1. The separation pass is a **reduction** of overlap and not a
//    guarantee of none. One pass, each unpinned node resolved once
//    against the boxes already fixed, in address order. Pushing one node
//    clear of a box can push it into another, and the pass does not go
//    back. Anything built on "the layout does not overlap" is built on
//    something this code does not provide, and `separate`'s own comment
//    says it again where a reader would otherwise assume it.
// 2. `manual` lays out the unplaced sub-graph and **never writes it
//    back** — the nodes stay unpinned and absent from `view_positions`
//    until a human drags one. That is only safe because the same
//    unplaced node lands in the same spot on every load, and *that*
//    rests on engine.js sorting its input by address before it inserts
//    it. It does not rest on the envelope's node order, which is an
//    order by uuid and not stable across a re-seed. See engine.js's
//    header for the measurement.

import { addressOf, layoutGraph, DEFAULT_NODE_WIDTH, DEFAULT_NODE_HEIGHT } from "./engine.js";

export { addressOf };

// The three modes 0008_views.sql accepts, and the default it applies.
// Exported as constants so a mode is asked for by identity rather than
// by matching a string at eleven call sites.
export const MODE_AUTO = "auto";
export const MODE_MANUAL = "manual";
export const MODE_MIXED = "mixed";
export const DEFAULT_MODE = MODE_MIXED;

// Where a coordinate came from. Read by the canvas (an unpinned node is
// drawn with a hollow anchor dot rather than a solid one, spec §4.2) and
// by the count below.
export const SOURCE_STORED = "stored";
export const SOURCE_COMPUTED = "computed";
export const SOURCE_GRID = "grid";

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

// --- Reading the wire ------------------------------------------------

// storedFrom normalises the envelope's `positions[]` into the rows
// `compose` takes.
//
// **The envelope and the write speak different spellings, and this is
// the shipped server rather than a guess.** `internal/views.Position`
// carries no struct tags, so a run marshals a stored position as
// `{"EntityType","EntityKey","X","Y","Pinned","UpdatedAt"}` — Go field
// names, which internal/web/mcp_views_test.go reads back by those exact
// strings. `views.set_positions` takes the snake_case spelling, which is
// what internal/web/static/client.js sends. Both are accepted here, the
// Go one first, because both really do occur; internal/web/
// static_layout_test.go pins the one the server writes by marshalling
// the struct rather than by quoting it, so a future set of json tags
// fails here loudly instead of leaving every saved arrangement silently
// unread.
//
// `pinned` defaults to **true**, which is 0008_views.sql's own default
// and views.set_positions': a row written without the flag is a node a
// human put somewhere.
export function storedFrom(positions) {
  const rows = [];
  for (const row of Array.isArray(positions) ? positions : []) {
    if (!row || typeof row !== "object") continue;
    const type = pick(row, "EntityType", "entity_type");
    const key = pick(row, "EntityKey", "entity_key");
    if (typeof key !== "string" || key === "") continue;
    const x = pick(row, "X", "x");
    const y = pick(row, "Y", "y");
    if (!Number.isFinite(x) || !Number.isFinite(y)) continue;
    const pinned = pick(row, "Pinned", "pinned");
    rows.push({
      key: addressOf({ type, key }),
      x,
      y,
      pinned: pinned === undefined ? true : pinned === true,
    });
  }
  return rows;
}

function pick(row, first, second) {
  if (Object.prototype.hasOwnProperty.call(row, first)) return row[first];
  if (Object.prototype.hasOwnProperty.call(row, second)) return row[second];
  return undefined;
}

// --- What the engine is asked to lay out -----------------------------

// subgraphFor answers "what does the engine run over", which is the one
// question that differs between the three modes before any arithmetic.
//
//   auto   — the whole graph. Stored positions are ignored entirely and
//            **not deleted**; nothing here reads them and nothing here
//            writes them.
//   manual — the *unplaced* sub-graph only, with the edges whose two
//            endpoints are both unplaced. Laying the whole graph out and
//            then discarding the placed nodes' coordinates would give a
//            different answer, and a worse one: adding a single stored
//            position would move every other unplaced node.
//   mixed  — the whole graph, ignoring stored positions, because step 1
//            of §5.3 wants a *shape* to fit to the pins and a shape
//            fitted to itself is not one.
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
//
// `pairs` are `[{from: {x, y}, to: {x, y}}]` — a pinned node's computed
// coordinate and the coordinate a designer dragged it to.
//
// The arithmetic, and why it is this and not the textbook similarity
// fit. Writing p = s·c + t and minimising Σ|p − (s·c + t)|² over a
// scalar s gives, with c̄ and p̄ the centroids,
//
//     s = Σ (cᵢ − c̄)·(pᵢ − p̄) / Σ |cᵢ − c̄|²          t = p̄ − s·c̄
//
// The textbook fit (Umeyama) solves for a rotation matrix as well, and
// it is *strictly better* at minimising the error — which is exactly why
// it is refused. A designer recognises a saved view by its shape; a
// diagram silently rotated 60° because two pins happened to lie that way
// is unrecognisable, and the error it minimised is not a quantity anyone
// on the other side of the glass is measuring.
//
// Three degeneracies, three different answers, each of them a real
// arrangement rather than a defensive branch:
//
//   0 pins            — the identity. `mixed` then behaves as `auto`,
//                       which §5.3 states outright.
//   every pin at one  — a translation. The denominator is zero: one pin
//   computed point      is the ordinary way to reach this, and two pins
//                       whose *computed* coordinates coincide is the
//                       other, which is a division by zero rather than a
//                       small number and so is answered by a branch and
//                       not by a tolerance.
//   a negative s      — a translation. A negative uniform scale is a
//                       180° rotation composed with a flip, so admitting
//                       it would let the fit do by the back door exactly
//                       what "no rotation" forbids at the front.
//
// Collinear pins are **not** degenerate here, and that is the one place
// refusing rotation pays for itself: a rotational fit needs the pins to
// span two dimensions, and three pins in a row down the left margin —
// which is what a designer who has tidied a column produces — is the
// common case, not a corner one.
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
//
// One pass. Each mover, in address order, is resolved once against every
// box already fixed — the pinned nodes first, then the movers already
// placed — and is pushed along the axis of least displacement, which is
// what keeps a nudge a nudge instead of throwing a node across the
// diagram. Pushing a node clear of one box can push it into another, and
// the pass does not come back for it: a settling loop is a simulation,
// which §5.2 refused when it refused a force layout, and it would make
// the arrangement depend on an iteration count nobody could reason
// about.
//
// Movers are mutated in place; `fixed` is not.
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
//
// `computed` is the engine's answer — `{placements}` or a bare array of
// `{key, x, y, width, height}` — for whatever `subgraphFor` said to lay
// out. `stored` is `[{key, x, y, pinned}]`, already filtered to the
// nodes in this picture (`layoutView` does the filtering: a stored row
// for an entity the query no longer returns is not a node, and drawing
// one would put a box on the canvas for something that is not in the
// answer).
//
// The result covers exactly the union of the two, which in every mode is
// the picture's node set. It carries:
//
//   placements  — `{key, x, y, pinned, source}`, in address order.
//                 Coordinates are the box's centre. **No sizes**: the
//                 engine takes measured sizes and returns coordinates,
//                 and the canvas that measured them does not need them
//                 handed back.
//   transform   — what the fit did, so the canvas can say so and a test
//                 can tell a translation from a scale of exactly 1.
//   placedAutomatically — nodes the engine placed because the saved
//                 arrangement has no row for them. This is
//                 render/scene.js's `placedAutomatically` option and the
//                 count in *"12 new nodes were placed automatically"*
//                 (spec §4.2), which is what tells a designer in
//                 `manual` mode that there is arranging to do. It is
//                 **0 in `auto`**, where every node is placed
//                 automatically and the sentence would be noise about a
//                 saved arrangement that is not being honoured anyway.
//   draggable   — false in `auto`, and only there. See the file header.
//
// Nothing here writes, asks for a write, or returns a write intent: the
// only writer in this sub-project is a human dragging a node (Task 14).
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
//
// It is deterministic and it is meant to look like what it is. A
// fallback that looked like a layout would be worse than one that
// obviously is not — a grid is unmistakably not a drawing of a graph,
// which is exactly what makes budget.js's banner a sentence a designer
// believes rather than one they argue with. Nothing here is tuned to
// look good.
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
//
// Layout is **incremental by identity**. A node present in both the old
// and the new envelope keeps its coordinate; the engine runs over the
// new nodes only, and the retained ones are obstacles. A full re-layout
// happens in exactly two cases, and both are stated as rules rather than
// left to an implementation: the designer asked for one (*re-arrange*),
// or the retained set is **under half** the new one, at which point the
// old arrangement is not a picture anybody recognises anyway.
//
// `previous` is the last composition's placements; `nodes` is the new
// envelope's node list. The answer is a plan and not a layout, because
// what to do with it differs: a full re-layout runs the mode's own
// composition, and an incremental one runs `mixed` over the fresh
// sub-graph with the retained nodes as pins — which needs no special
// case anywhere, because a pin the engine was not asked to place
// contributes nothing to the fit, so the transform degenerates to the
// identity and the pass separates the fresh nodes against the old ones.
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
//
// The worker holds no decisions — it is the one part of this layer no
// Node harness can drive, so it receives a request, calls this, and
// posts the answer back. Everything a test needs to reach is on this
// side of that seam, `engine` included: it is injected so a stub that
// never answers can be driven through budget.js's supervisor, and it
// defaults to the real dagre wrapper.
//
// `stored` is filtered to the picture here and nowhere else: a stored
// row for an entity this run did not return is not drawn, because it is
// not in the answer, and a box on the canvas for something outside the
// answer is the picture lying about what the query said.
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
    //
    // `auto` honours no stored row, here as everywhere, so its fresh
    // nodes all go through the engine; what it retains is the previous
    // *computed* arrangement, which is the picture in front of the
    // designer and is what §5.5 is about.
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

// normaliseMode falls back to `mixed`, which is 0008_views.sql's own
// default: a view row that somehow carries a mode this client does not
// know still draws, in the mode the database would have given it.
function normaliseMode(mode) {
  return mode === MODE_AUTO || mode === MODE_MANUAL || mode === MODE_MIXED ? mode : DEFAULT_MODE;
}

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
