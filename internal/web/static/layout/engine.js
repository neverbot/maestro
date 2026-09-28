// The layout engine: the vendored dagre, wrapped, and nothing else.

import { Graph } from "../vendor/graphlib.mjs";
import { layout } from "../vendor/dagre.mjs";

// addressOf is an entity's identity, everywhere in this front end: the
// `(type, key)` pair, never the id.
import { addressOf } from "../address.js";
export { addressOf };

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
export function layoutGraph(nodes, edges, options = {}) {
  const boxes = normaliseNodes(nodes);
  const clustered = boxes.some((box) => box.cluster !== null);
  const graph = new Graph({ compound: clustered, directed: true, multigraph: true });
  graph.setGraph({ ...DEFAULT_LAYOUT_OPTIONS, ...options });
  graph.setDefaultEdgeLabel(() => ({}));

  // Sorted, for the reason in this file's header. `boxes` is already in
  // address order and `links` is sorted the same way.
  for (const box of boxes) {
    graph.setNode(box.key, { width: box.width, height: box.height });
  }
  // The cluster parents, in the order their names sort, and each node
  // attached to its own. `clusterVertex` prefixes the value so a cluster
  // called `["quest","boss"]` cannot collide with the entity of that
  // address.
  if (clustered) {
    const names = [...new Set(boxes.filter((box) => box.cluster !== null).map((box) => box.cluster))];
    names.sort(compare);
    for (const name of names) graph.setNode(clusterVertex(name), {});
    for (const box of boxes) {
      if (box.cluster !== null) graph.setParent(box.key, clusterVertex(box.cluster));
    }
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
      // An empty cluster name is not a cluster: it is what a node whose
      // `cluster_by` slot found nothing has, and putting every such node
      // in one parent would invent a group out of an absence.
      cluster: typeof node.cluster === "string" && node.cluster !== "" ? node.cluster : null,
    });
  }
  boxes.sort(byKey);
  return boxes;
}

// normaliseEdges drops every edge with an endpoint outside `boxes`, and
// this is load-bearing rather than defensive.
function normaliseEdges(edges, boxes) {
  const known = new Set(boxes.map((box) => box.key));
  const links = [];
  for (const edge of Array.isArray(edges) ? edges : []) {
    if (!edge || typeof edge !== "object") continue;
    const source = endpoint(edge.source);
    const target = endpoint(edge.target);
    if (source === null || target === null) continue;
    if (!known.has(source) || !known.has(target)) continue;
    links.push({ source, target, name: edgeName(edge.type) });
  }
  links.sort(
    (a, b) =>
      compare(a.source, b.source) || compare(a.target, b.target) || compare(a.name, b.name),
  );
  return links;
}

// edgeName is the name a relation gets as a dagre edge, and it is never
// the empty string.
const UNTYPED_EDGE = ":untyped";

function edgeName(type) {
  return typeof type === "string" && type !== "" ? type : UNTYPED_EDGE;
}

function endpoint(value) {
  if (typeof value === "string") return value === "" ? null : value;
  if (value && typeof value === "object" && typeof value.key === "string" && value.key !== "") {
    return addressOf(value);
  }
  return null;
}

// clusterVertex is a cluster's name as a graph vertex. The prefix is
// what keeps the namespace of cluster values and the namespace of entity
// addresses apart: both are strings in one dagre graph, and a cluster
// whose text happened to be an address would silently become that node's
// parent — or, worse, itself.
function clusterVertex(name) {
  return "cluster:" + name;
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
