// The `graph` renderer: a node-link diagram, and the first of six.
//
// A renderer is a **pure function from an envelope to a scene**. It reads
// what the server said and the layout's answer, and it returns marks —
// plain data that components/mst-canvas.js turns into SVG elements one
// for one. It decides nothing about colour (palette.js, Task 1), nothing
// about what a negative state says (render/scene.js, Task 4) and nothing
// about where a node goes (layout/, Task 6); those travel in, and this
// module arranges them.
//
// That purity is the whole verification story of this sub-project: a
// picture is a value a Node harness can read, and every claim below —
// that an absence is marked twice, that a size is an area, that
// clustering draws nothing — is an assertion over that value rather than
// a screenshot somebody looked at.
//
// **The scene and the text twin describe the same answer.** The twin
// (render/twin.js) is built from the envelope alone and is the
// accessible content of every view; this module is built from the
// envelope and a layout. Two descriptions of one answer that disagree is
// the defect the plan's ordering exists to catch, so the harness joins
// them: every node the twin has a row for is drawn or is reported
// unplaced, and every edge row is drawn or is a stub.
//
// **What it draws when the answer is not a clean one**, which in this
// product is the ordinary case rather than the edge case:
//
//   an absent colour value — the box is unfilled and its outline is
//     dashed, and the legend carries the palette's `unset` row. Two
//     carriers, because colour is never the only one.
//   an absent size value — the range's minimum, which is the smallest
//     box and not no box. A node missing *both* gets both marks: two
//     absences are two facts, and one "unknown" treatment would collapse
//     them into one.
//   an ambiguous node — one mark at the box's corner, on the node,
//     because `Node.Ambiguous` is a flag on the node and execute.go is
//     explicit that which slot it was is not said.
//   an edge that leaves the picture — a stub: a line out of its known
//     endpoint ending in a small hollow ring, no label, counted in the
//     footer. Not an error and never `--danger` (§4.2).
//   a truncated answer — **nothing at all**, deliberately. The envelope
//     says a cap was hit; it does not say which node lost a neighbour,
//     and a mark claiming to know would be this interface inventing the
//     one thing the server declined to measure. The frame bands it
//     (Task 4) and the picture draws what it was given.
//   an empty answer — an empty scene: no marks, no legend rows. The
//     frame says *"This view matched nothing"* and says it as a success;
//     a renderer that drew an apology would be a second voice.
//   a node with no position — not drawn, and named in `unplaced`. A box
//     at the origin for a node nobody placed is the lie execute.go
//     refuses one row up, and drawing it would put an entity somewhere a
//     designer could then drag and save.

import { addressOf } from "../address.js";
import { fillFor, labelFor, legendFor } from "../palette.js";
import { joinEdges } from "./scene.js";
import {
  ENCLOSURE_PADDING,
  NODE_PLAIN_FILL,
  areaScaleFor,
  boundsOf,
  boxFor,
  edgeMarks,
  enclosureFor,
  lengthScaleForArea,
  nodeMarks,
  scaleBox,
  sizeDomainFor,
  stubMarks,
} from "./marks.js";
import { CONTROL_BOOL, CONTROL_SLOT, control } from "./controls.js";

// The name this renderer is stored under, and the one internal/views'
// catalogue holds. It is a constant so that a page choosing a renderer
// by name and this module agreeing on it is one string.
export const RENDERER = "graph";

// The renderer_params keys, spelled as the server spells them.
export const PARAM_COLOR_BY = "color_by";
export const PARAM_GROUP_BY = "group_by";
export const PARAM_SIZE_BY = "size_by";
export const PARAM_CLUSTER_BY = "cluster_by";
export const PARAM_EDGE_LABELS = "edge_labels";
export const PARAM_ARROWS = "arrows";

// The controls, in the order the catalogue prints them, each with the
// sentence a designer reads beside it.
//
// **`group_by` and `cluster_by` are the pair these tooltips exist for.**
// They take the same kind of value, sit beside each other in the same
// panel, and do entirely different things: one draws a box around the
// nodes sharing a value, the other draws nothing whatever and only tells
// the layout to keep them together. Nowhere else in this interface can
// that be learned — the catalogue's own text describes what a *query*
// must produce, not what appears on the canvas — so these two sentences
// are the whole of the teaching, and the harness asserts them rather
// than admiring them.
export const CONTROLS = [
  control(
    PARAM_COLOR_BY,
    CONTROL_SLOT,
    "Fills each node with a colour from this slot's value. Eight colours; " +
      "everything past the eighth most common value is hatched, and a node " +
      "whose slot found nothing is left unfilled with a dashed outline.",
  ),
  control(
    PARAM_GROUP_BY,
    CONTROL_SLOT,
    "Draws a hairline enclosure around the nodes that share this slot's " +
      "value, with the value as its heading. It changes what is drawn, not " +
      "where nodes go.",
  ),
  control(
    PARAM_SIZE_BY,
    CONTROL_SLOT,
    "Sizes each node by this slot's number, mapped to the box's area over " +
      "a 1x-3x range: the largest value in the answer has three times the " +
      "area of the smallest, whatever the numbers are. A node whose slot " +
      "found nothing takes the smallest size.",
  ),
  control(
    PARAM_CLUSTER_BY,
    CONTROL_SLOT,
    "Draws nothing. It tells the layout to keep the nodes that share this " +
      "slot's value near each other, so it changes where nodes go and not " +
      "what is drawn.",
  ),
  control(
    PARAM_EDGE_LABELS,
    CONTROL_BOOL,
    "Draws each edge's label on a plate at the middle of the line. The " +
      "query has to ask for one with label_from.",
  ),
  control(
    PARAM_ARROWS,
    CONTROL_BOOL,
    "Draws a head at the target end of every edge, so the direction of a " +
      "relation is readable without opening the query.",
  ),
];

// --- What the layout is asked for ------------------------------------

// graphLayoutRequest is the `nodes` and `edges` half of the request the
// worker runs, in the layout layer's own vocabulary: an entity is a
// `(type, key)` pair with a measured box, and an edge is a pair of them.
//
// **The sizes are measured here and nowhere else.** The box a node is
// laid out in and the box it is drawn in come from one call to
// `boxFor`, so a `size_by` value that makes a node three times the area
// makes the layout reserve three times the area; a renderer that
// measured for the drawing only would draw big boxes into small holes.
// `theLayoutIsAskedForTheBoxThatIsDrawn` is that join.
//
// `cluster_by` reaches the layout here, as each node's `cluster`, and
// this is the only thing it ever does. `group_by` is deliberately absent
// from this function: an enclosure is drawn around wherever the nodes
// ended up and never asks for them to be moved.
export function graphLayoutRequest(envelope, params = {}) {
  const nodes = nodesOf(envelope);
  const options = readParams(params);
  const domain = sizeDomainFor(nodes, options.sizeBy);
  const clusterBy = options.clusterBy;

  const laid = nodes.map((node) => {
    const box = boxOf(node, options, domain);
    const cluster = clusterBy === null ? null : valueText(node, clusterBy);
    return {
      type: text(node.type),
      key: text(node.key),
      width: box.width,
      height: box.height,
      // A node whose cluster slot found nothing is in no cluster, not in
      // a cluster of absences. The engine reads an empty string as no
      // cluster for the same reason.
      cluster: cluster === null ? "" : cluster,
    };
  });

  // Only the edges whose two endpoints are both in the picture reach the
  // engine: dagre's setEdge *creates* a node for an unknown endpoint, so
  // a stub handed to it would invent an empty box for an entity the
  // query chose not to draw. joinEdges is the one place that rule lives.
  const { drawn } = joinEdges(nodes, edgesOf(envelope));
  const edges = drawn.map(({ edge, source, target }) => ({
    type: text(edge.type),
    source: { type: text(source.type), key: text(source.key) },
    target: { type: text(target.type), key: text(target.key) },
  }));

  return { nodes: laid, edges };
}

// --- The scene -------------------------------------------------------

// graphScene is the picture.
//
// `layout` is the layout layer's answer — `{placements: [{key, x, y,
// width, height}]}` where `key` is the entity's address and `x`/`y` are
// the box's centre.
//
// Returns:
//   marks    — the scene, in the order it is built: enclosures, edges,
//              stubs, then nodes. Paint order is the *layers'* (Task 7),
//              not this array's; the order here is only so that two runs
//              over one envelope produce the same array.
//   legend   — palette.js's rows for `color_by`, or null when the view
//              colours nothing. Null and not an empty list: "this view
//              has no colour slot" is not "this slot had no values".
//   stubs    — `{total, drawn, anchorless}`. `total` is the footer's
//              *"n edges lead outside this picture"*; `anchorless` is the
//              part of it nothing can be drawn for, because **both**
//              endpoints are outside and there is no known end to leave.
//   unplaced — the addresses of nodes the layout placed nowhere.
//   groups   — one entry per `group_by` value that has an enclosure.
export function graphScene(envelope, layout, params = {}) {
  const nodes = nodesOf(envelope);
  const options = readParams(params);
  const domain = sizeDomainFor(nodes, options.sizeBy);
  const legend = options.colorBy === null ? null : legendFor(nodes, options.colorBy);
  const placements = placementsOf(layout);

  const marks = [];
  const unplaced = [];
  const boxes = new Map();

  for (const node of nodes) {
    const key = addressOf(node);
    const placement = placements.get(key);
    if (!placement) {
      unplaced.push(key);
      continue;
    }
    // The box is **measured here**, not read back off the placement.
    // The engine echoes the size it was handed, so reading it back would
    // make this function agree with the layout by construction and hide
    // exactly the mistake that matters: a renderer that measured one box
    // for the engine and another for the drawing, which draws a
    // three-times-area node into a hole reserved for a one-times one.
    // theLayoutIsAskedForTheBoxThatIsDrawn is the join, and it can only
    // fail if the two measurements are two calls.
    const { width, height } = boxOf(node, options, domain);
    boxes.set(key, { key, x: placement.x, y: placement.y, width, height, node });
  }

  // The enclosures first, because they are the ground the rest sits on
  // and because a stub has to know which one it is leaving.
  const groups = groupsOf(nodes, boxes, options.groupBy);
  for (const group of groups) marks.push(...enclosureFor(group.members, group.heading));

  const pictureBounds = boundsOf([...boxes.values()]);

  const { drawn, stubs } = joinEdges(nodes, edgesOf(envelope));
  for (const { edge, source, target } of drawn) {
    const from = boxes.get(addressOf(source));
    const to = boxes.get(addressOf(target));
    // An edge between two nodes of the answer where one of them was
    // never placed. It is not an edge leaving the picture and is not
    // counted as one: the far end is in the answer, and the reason
    // nothing is drawn is the unplaced node, which is reported by name.
    if (!from || !to || from === to) continue;
    marks.push(
      ...edgeMarks({
        source: from,
        target: to,
        label: typeof edge.label === "string" ? edge.label : "",
        arrows: options.arrows,
        edgeLabels: options.edgeLabels,
      }),
    );
  }

  let anchorless = 0;
  for (const stub of stubs) {
    const anchor = stub.source ?? stub.target;
    const box = anchor ? boxes.get(addressOf(anchor)) : null;
    // Both endpoints outside the picture — or the one endpoint that is
    // in it was never placed. There is no known end for the line to
    // leave, so nothing is drawn; the edge is still counted, because the
    // footer's sentence is about the answer and not about the drawing.
    if (!box) {
      anchorless++;
      continue;
    }
    marks.push(...stubMarks({ node: box, enclosure: enclosureAround(box, groups, pictureBounds) }));
  }

  for (const box of boxes.values()) {
    const paint = paintFor(box.node, options, legend);
    marks.push(
      ...nodeMarks({
        key: box.key,
        label: labelOf(box.node),
        x: box.x,
        y: box.y,
        width: box.width,
        height: box.height,
        fill: paint.fill,
        dash: paint.dash,
        ambiguous: box.node.ambiguous === true,
      }),
    );
  }

  return {
    marks,
    legend,
    stubs: { total: stubs.length, drawn: stubs.length - anchorless, anchorless },
    unplaced,
    groups: groups.map((group) => ({ value: group.value, heading: group.heading, count: group.members.length })),
  };
}

// --- Reading the parameters ------------------------------------------

// readParams is the one reader of renderer_params, so a knob spelled
// wrongly is absent everywhere rather than working in one half of the
// module. A slot name that is not a non-empty string is no slot: the
// server refuses one at save time, and a client that half-honoured a
// broken value would draw a picture the server would not have saved.
function readParams(params) {
  const p = params && typeof params === "object" ? params : {};
  return {
    colorBy: slot(p[PARAM_COLOR_BY]),
    groupBy: slot(p[PARAM_GROUP_BY]),
    sizeBy: slot(p[PARAM_SIZE_BY]),
    clusterBy: slot(p[PARAM_CLUSTER_BY]),
    edgeLabels: p[PARAM_EDGE_LABELS] === true,
    arrows: p[PARAM_ARROWS] === true,
  };
}

function slot(value) {
  return typeof value === "string" && value !== "" ? value : null;
}

// --- Paint, size, grouping -------------------------------------------

// paintFor is the fill and the dash, and it is the one place the two
// absences are told apart.
//
// Three answers, not two:
//
//   no colour slot at all — `--paper` and no dash. "This view colours
//     nothing" is not "this node's value is missing", and a picture that
//     drew them alike would report an absence nobody asked about.
//   a value — the palette's hue, or its hatch for the ninth-and-beyond.
//   the slot absent from this node — no fill and a dashed outline, and
//     the legend already carries the `unset` row that names it.
function paintFor(node, options, legend) {
  if (options.colorBy === null) return { fill: NODE_PLAIN_FILL, dash: false };
  const json = valueJSON(node, options.colorBy);
  if (json === null) return { fill: fillFor({ kind: "unset" }).css, dash: true };
  const row = legend ? legend.byValue.get(json) : null;
  return { fill: fillFor(row).css, dash: false };
}

// boxOf is a node's box: measured from its label, then scaled by
// `size_by` over the bounded area range.
function boxOf(node, options, domain) {
  const base = boxFor(labelOf(node));
  if (options.sizeBy === null) return base;
  const value = valueOf(node, options.sizeBy);
  return scaleBox(base, lengthScaleForArea(areaScaleFor(value, domain)));
}

// groupsOf is one entry per `group_by` value, with the placed boxes that
// carry it.
//
// A node whose group slot found nothing is in **no** enclosure rather
// than in an enclosure of absences: a box drawn around "the ones we know
// nothing about" is a claim that they belong together, which is exactly
// what an absence does not say. The palette makes the same choice with
// its `unset` legend row, which names the absence without giving it a
// hue.
//
// Ordered by heading, so the scene is the same array for one envelope
// however the nodes arrived.
function groupsOf(nodes, boxes, groupBy) {
  if (groupBy === null) return [];
  const byValue = new Map();
  for (const node of nodes) {
    const box = boxes.get(addressOf(node));
    if (!box) continue;
    const json = valueJSON(node, groupBy);
    if (json === null) continue;
    const entry = byValue.get(json) || { value: json, heading: labelFor(json), members: [] };
    entry.members.push(box);
    byValue.set(json, entry);
  }
  return [...byValue.values()].sort((a, b) => (a.value < b.value ? -1 : a.value > b.value ? 1 : 0));
}

// enclosureAround is the rectangle a stub has to leave: the enclosure of
// the node's own group, as drawn, or the picture's bounds when the node
// is in no group.
//
// This is what makes "a stub leaves its group's box" a property of the
// drawing rather than of a lucky arrangement — the terminus is put
// beyond this rectangle by construction. §4.3 names it as the reason
// enclosures are hairlines rather than filled panels: the stub crossing
// the line is correct, and a filled panel would make it look like a
// mistake.
function enclosureAround(box, groups, pictureBounds) {
  for (const group of groups) {
    if (!group.members.includes(box)) continue;
    const bounds = boundsOf(group.members);
    if (!bounds) continue;
    return {
      minX: bounds.minX - ENCLOSURE_PADDING,
      minY: bounds.minY - ENCLOSURE_PADDING,
      maxX: bounds.maxX + ENCLOSURE_PADDING,
      maxY: bounds.maxY + ENCLOSURE_PADDING,
    };
  }
  return pictureBounds || { minX: box.x, minY: box.y, maxX: box.x, maxY: box.y };
}

// --- Reading the envelope --------------------------------------------

function nodesOf(envelope) {
  const env = envelope && typeof envelope === "object" ? envelope : {};
  return Array.isArray(env.nodes) ? env.nodes.filter(isObject) : [];
}

function edgesOf(envelope) {
  const env = envelope && typeof envelope === "object" ? envelope : {};
  return Array.isArray(env.edges) ? env.edges.filter(isObject) : [];
}

function placementsOf(layout) {
  const rows = layout && Array.isArray(layout.placements) ? layout.placements : [];
  const byKey = new Map();
  for (const row of rows) {
    if (!isObject(row) || typeof row.key !== "string") continue;
    if (!Number.isFinite(row.x) || !Number.isFinite(row.y)) continue;
    if (!byKey.has(row.key)) byKey.set(row.key, row);
  }
  return byKey;
}

// labelOf is what the box says. `attrs.label` is always there — the
// projection's `label` defaults to the entity's name (execute.go) — and
// the node's own name is the fallback for an envelope that carried no
// projection at all.
function labelOf(node) {
  const json = valueJSON(node, "label");
  if (json !== null) return labelFor(json);
  return text(node.name);
}

// valueJSON is a slot's value as JSON text, or null when the slot is
// **absent**. `hasOwnProperty` and not `=== undefined`, the same test
// palette.js and render/twin.js make: the envelope's distinction is
// presence, and a slot present with a null value is a value the game
// means.
function valueJSON(node, slotName) {
  const attrs = isObject(node.attrs) ? node.attrs : null;
  if (!attrs || !Object.prototype.hasOwnProperty.call(attrs, slotName)) return null;
  return JSON.stringify(attrs[slotName]);
}

function valueOf(node, slotName) {
  const attrs = isObject(node.attrs) ? node.attrs : null;
  if (!attrs || !Object.prototype.hasOwnProperty.call(attrs, slotName)) return undefined;
  return attrs[slotName];
}

// valueText is a slot's value as the text the legend would show, which
// is what a cluster is named by: `20` and `"20"` are two values and
// cluster apart, exactly as they take two legend rows.
function valueText(node, slotName) {
  const json = valueJSON(node, slotName);
  return json === null ? null : json;
}

function isObject(value) {
  return value !== null && typeof value === "object";
}

function text(value) {
  return typeof value === "string" ? value : "";
}
