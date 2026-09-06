// The layout engine: the vendored dagre, wrapped, and nothing else.
//
// Pure by construction — nodes with measured sizes and edges in,
// coordinates out. No DOM, no fetch, no state, no time. That is what
// lets the whole of it run under `node internal/web/jstest/layout_test.mjs`
// while the browser runs the identical bytes inside a module worker.
//
// **The two imports are relative, and that is not a style choice.**
// Every other module of ours writes `import … from "lit"` and lets the
// shells' import map resolve it. An import map is a property of a
// *document*: a module worker has its own module map and no map at all,
// so a bare specifier inside anything the worker imports fails to
// resolve, in the worker, at load, with an error the page sees only as a
// dead worker. engine.js is imported by worker.js, so it names the
// vendored files by path. internal/web/static_layout_test.go pins this
// in both directions, because "it works today" is how it stops working.
//
// **The determinism this module owes the rest of the product, and what
// it actually rests on.** Spec §5.3 lets `manual` mode lay out unplaced
// nodes and never write the result back, and §5.6 removes `layout_seed`,
// on one shared premise: the same graph lands in the same place on every
// load. dagre is deterministic — but only *given an insertion order*.
// Reversing the order the nodes are added in moves every node; so does
// reversing the edges. Measured, on the vendored 3.1.1, on an
// eight-node graph: both shuffles produce a different drawing.
//
// So this module **sorts**, by the entity's address, before it inserts
// anything, and that is where the guarantee comes from. It does not come
// from the envelope's node order being stable — which it happens to be,
// `ORDER BY capped.rank, capped.id` in internal/views/compile.go, but
// that is an order by *uuid*, so it is stable across loads and not
// across a re-seed, and nothing anywhere had written that dependency
// down. Sorting here removes it: the arrangement is a function of the
// entity addresses and the sizes, and of nothing else.

import { Graph } from "../vendor/graphlib.mjs";
import { layout } from "../vendor/dagre.mjs";

// addressOf is an entity's identity, everywhere in this front end: the
// `(type, key)` pair, never the id. internal/web/static/client.js writes
// positions by that pair, render/twin.js addresses its rows by it, and
// internal/views' own Position carries it instead of an id "so a caller
// can round-trip it without ever having read the game's ids".
//
// It is JSON rather than `type + "/" + key` so that a type or a key
// containing the separator cannot forge another entity's address —
// render/twin.js's row key is built the same way for the same reason,
// and internal/web/jstest/layout_test.mjs joins the two modules on it
// rather than trusting two spellings to stay equal.
//
// It lives in the engine because the engine is what turns an entity into
// a graph vertex, and a graph vertex needs one string. compose.js
// re-exports it so a caller has one import for the whole layer.
export function addressOf(node) {
  return JSON.stringify([
    typeof node.type === "string" ? node.type : "",
    typeof node.key === "string" ? node.key : "",
  ]);
}

// The ranked drawing's shape. Spec §5.2 chose a ranked engine over a
// force one because a game's content graph is overwhelmingly directional
// — `requires`, `unlocks`, `connects_to` — and these are the knobs that
// claim says are worth having. They are one object so a renderer can
// override one of them without restating the rest.
export const DEFAULT_LAYOUT_OPTIONS = {
  rankdir: "TB",
  align: undefined,
  nodesep: 40,
  edgesep: 20,
  ranksep: 60,
  marginx: 24,
  marginy: 24,
};

// A node with no measured size still has to be drawn somewhere, so it
// gets a box rather than a NaN. Task 7 measures every label in the page
// with a hidden <svg><text> and hands the sizes down; this is what a
// caller that forgot looks like, and it looks like a small box rather
// than like a layout that silently collapsed.
export const DEFAULT_NODE_WIDTH = 120;
export const DEFAULT_NODE_HEIGHT = 32;

// layoutGraph runs one layout.
//
// `nodes` are `{type, key, width, height}` and `edges` are
// `{source, target}` with each endpoint a `{type, key}` pair — the
// entity address, which is the vocabulary every other surface in this
// product uses (internal/web/static/client.js writes positions by it,
// render/twin.js addresses its rows by it) and never an id.
//
// Returns `{placements, width, height}`. `placements` are
// `{key, x, y, width, height}` where `key` is the address string, sorted
// by it, and `x`/`y` are the box's **centre**, which is what dagre
// reports and what an SVG `<rect>` is positioned from once by the
// emitter rather than once per renderer.
export function layoutGraph(nodes, edges, options = {}) {
  const boxes = normaliseNodes(nodes);
  const graph = new Graph({ compound: false, directed: true, multigraph: true });
  graph.setGraph({ ...DEFAULT_LAYOUT_OPTIONS, ...options });
  graph.setDefaultEdgeLabel(() => ({}));

  // Sorted, for the reason in this file's header. `boxes` is already in
  // address order and `links` is sorted the same way.
  for (const box of boxes) {
    graph.setNode(box.key, { width: box.width, height: box.height });
  }
  for (const link of normaliseEdges(edges, boxes)) {
    // A name on the edge, so two relations between the same pair are two
    // edges rather than one silently overwriting the other — the game
    // model allows a quest to both `requires` and `unlocks` a thing.
    graph.setEdge(link.source, link.target, {}, link.name);
  }

  if (boxes.length > 0) layout(graph);

  const placements = boxes.map((box) => {
    const laid = graph.node(box.key) || {};
    return {
      key: box.key,
      x: finite(laid.x, 0),
      y: finite(laid.y, 0),
      width: box.width,
      height: box.height,
    };
  });
  const size = graph.graph() || {};
  return {
    placements,
    width: finite(size.width, 0),
    height: finite(size.height, 0),
  };
}

// normaliseNodes drops what cannot be laid out and orders what is left.
//
// A duplicate address keeps the **first** occurrence rather than the
// last, so that a caller that concatenated two overlapping node lists
// gets a stable answer instead of one that depends on which list it put
// second.
function normaliseNodes(nodes) {
  const seen = new Set();
  const boxes = [];
  for (const node of Array.isArray(nodes) ? nodes : []) {
    if (!node || typeof node !== "object") continue;
    if (typeof node.key !== "string" || node.key === "") continue;
    const key = addressOf(node);
    if (seen.has(key)) continue;
    seen.add(key);
    boxes.push({
      key,
      width: positive(node.width, DEFAULT_NODE_WIDTH),
      height: positive(node.height, DEFAULT_NODE_HEIGHT),
    });
  }
  boxes.sort(byKey);
  return boxes;
}

// normaliseEdges drops every edge with an endpoint outside `boxes`, and
// this is load-bearing rather than defensive.
//
// Spec §4.2: an edge whose endpoint is not in `nodes` is **normal** —
// the node and edge caps are independent, and an `edges: [{between: …}]`
// entry legitimately draws relations between sets the query chose not to
// draw. dagre's `setEdge` creates a node for an unknown endpoint, so
// passing those through would invent an empty box for every entity
// outside the picture, give it a rank, and push the real drawing around
// to make room for entities the designer asked not to see. They are
// drawn as stubs by the renderer (Task 7's `joinEdges`), which is a
// decoration on a known endpoint and not a node the engine ever sees.
function normaliseEdges(edges, boxes) {
  const known = new Set(boxes.map((box) => box.key));
  const links = [];
  for (const edge of Array.isArray(edges) ? edges : []) {
    if (!edge || typeof edge !== "object") continue;
    const source = endpoint(edge.source);
    const target = endpoint(edge.target);
    if (source === null || target === null) continue;
    if (!known.has(source) || !known.has(target)) continue;
    links.push({ source, target, name: typeof edge.type === "string" ? edge.type : "" });
  }
  links.sort(
    (a, b) =>
      compare(a.source, b.source) || compare(a.target, b.target) || compare(a.name, b.name),
  );
  return links;
}

function endpoint(value) {
  if (typeof value === "string") return value === "" ? null : value;
  if (value && typeof value === "object" && typeof value.key === "string" && value.key !== "") {
    return addressOf(value);
  }
  return null;
}

function byKey(a, b) {
  return compare(a.key, b.key);
}

function compare(a, b) {
  return a < b ? -1 : a > b ? 1 : 0;
}

function finite(value, fallback) {
  return Number.isFinite(value) ? value : fallback;
}

function positive(value, fallback) {
  return Number.isFinite(value) && value > 0 ? value : fallback;
}
