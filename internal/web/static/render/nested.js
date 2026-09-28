// The `nested` renderer: boxes inside boxes, and a refusal to draw a
// tree over a graph that is not one.

import { addressOf } from "../address.js";
import { fillFor, labelFor, legendFor } from "../palette.js";
import { joinEdges } from "./scene.js";
import {
  CHIP_HEIGHT,
  CONTAINER_PADDING,
  HEADER_HEIGHT,
  boxFor,
  chipMarks,
  chipWidth,
  containerMarks,
  cycleGlyphMarks,
} from "./marks.js";
import { CONTROL_COUNT, CONTROL_RELATION_TYPE, CONTROL_SLOT, control } from "./controls.js";

// The name the catalogue holds, and this module's.
export const RENDERER = "nested";

// The renderer_params keys, spelled as the server spells them.
export const PARAM_CONTAIN_VIA = "contain_via";
export const PARAM_MAX_DEPTH = "max_depth";
export const PARAM_LEAF_LABEL = "leaf_label";

// The projection slot the tint reads. It is the conventional name the
// query language already has for "the value this view colours by", not a
// renderer parameter — see the header.
export const COLOUR_SLOT = "color_by";

// How deep the nesting goes when the view does not say.
export const DEFAULT_MAX_DEPTH = 4;

// The gap between two siblings inside a container.
export const SIBLING_GAP = 10;

// The controls. Each tooltip says what the knob does to the **picture**,
// which is the half internal/views/renderers.go deliberately does not
// carry (render/controls.js's header has the argument).
export const CONTROLS = [
  control(
    PARAM_CONTAIN_VIA,
    CONTROL_RELATION_TYPE,
    "The relation read as containment: the source of each edge is drawn " +
      "inside its target. It is the nesting, so the picture is one flat row " +
      "of boxes without it.",
  ),
  control(
    PARAM_MAX_DEPTH,
    CONTROL_COUNT,
    "How many levels of boxes to draw. A container with children past the " +
      "last level shows a count chip instead; clicking it opens them from " +
      "the answer already on screen, without asking the server again.",
  ),
  control(
    PARAM_LEAF_LABEL,
    CONTROL_SLOT,
    "Labels a box that contains nothing with this slot's value instead of " +
      "its name. A box whose slot found nothing keeps its name rather than " +
      "going blank.",
  ),
];

// --- The scene -------------------------------------------------------

// nestedScene is the picture.
export function nestedScene(envelope, params = {}, options = {}) {
  const nodes = nodesOf(envelope);
  const config = readParams(params);
  const expanded = new Set(Array.isArray(options.expanded) ? options.expanded : []);
  const byAddress = new Map(nodes.map((node) => [addressOf(node), node]));

  // `legendFor` always answers — a slot no node carries produces a
  // legend of one `unset` row — so "this query colours nothing" is read
  // as "no node has a value", not as "the legend is empty". A view that
  // colours nothing has **no legend at all**, which is not an empty one,
  // and its headers take the plate's own ground.
  const legend = legendFor(nodes, COLOUR_SLOT);
  const coloured = legend.rows.some((row) => row.kind !== "unset") ? legend : null;

  const { drawn: joined, stubs } = joinEdges(nodes, edgesOf(envelope));

  // The containment tree. Only edges of `contain_via` build it: a
  // `nested` view may legitimately carry other relations in its answer,
  // and reading them as containment would nest a quest inside the zone
  // it merely mentions.
  const children = new Map(nodes.map((node) => [addressOf(node), []]));
  const parentOf = new Map();
  for (const { edge, source, target } of joined) {
    if (!isContainment(edge, config.containVia)) continue;
    const inner = addressOf(source);
    const outer = addressOf(target);
    // A thing inside itself is not a nesting anybody can draw, and it is
    // its own repeat: the cycle machinery below would catch it, but the
    // tree must not carry it or the first node would be its own parent.
    if (inner === outer) continue;
    // **One container per box**, and the one with the lowest address
    // wins. The metamodel permits a thing to be contained in two — there
    // is no constraint against it — and a box cannot be drawn in two
    // places: a renderer that pushed the child into both lists would
    // draw it twice, count it twice, and disagree with its own twin
    // about how many things the answer has. The second containment is
    // not lost: it is still an edge in the answer and still a row in the
    // twin, which is where an answer this vocabulary cannot draw belongs.
    const held = parentOf.get(inner);
    if (held === undefined || outer < held) parentOf.set(inner, outer);
  }
  for (const [inner, outer] of parentOf) children.get(outer).push(inner);
  for (const list of children.values()) list.sort(compare);

  // An orphan of the cap: this node has a containment edge whose target
  // is not in the picture. It is a root **and** an absence, and the two
  // are drawn differently on purpose.
  const orphans = new Set();
  for (const stub of stubs) {
    if (!isContainment(stub.edge, config.containVia)) continue;
    if (stub.source === null) continue;
    orphans.add(addressOf(stub.source));
  }

  // The roots: everything no drawn containment edge points out of.
  // Address order, so one envelope is one picture however it arrived.
  const roots = [...byAddress.keys()].filter((key) => !parentOf.has(key)).sort(compare);
  // And the entry points a cycle needs. A ring of containers has no root
  // at all — every member has a parent — so the lowest address of each
  // unreached group is promoted. Without this a cyclic answer would draw
  // nothing whatever, which is a picture that hides the defect even more
  // thoroughly than a plausible tree does.
  const reached = new Set();
  for (const root of roots) walk(root, children, reached);
  for (const key of [...byAddress.keys()].sort(compare)) {
    if (reached.has(key)) continue;
    roots.push(key);
    walk(key, children, reached);
  }

  const context = {
    byAddress,
    children,
    config,
    expanded,
    legend: coloured,
    orphans,
    drawn: [],
    hidden: [],
    chips: [],
    cycles: [],
  };

  const trees = roots.map((key) => measure(key, 1, [], context));
  const packed = pack(trees, 0, 0);

  const marks = [];
  for (const tree of trees) emit(tree, marks, context);

  return {
    marks,
    legend: coloured,
    width: packed.width,
    height: packed.height,
    drawn: context.drawn,
    hidden: context.hidden,
    chips: context.chips,
    orphans: [...orphans].filter((key) => byAddress.has(key)).sort(compare),
    cycles: context.cycles,
    // Counted, never drawn. Every renderer reports the answer's edges
    // that leave the picture, because the footer's sentence is about the
    // answer (Task 8's rule); a nest has no margin to run a line out
    // into, and the orphan's dashed box already says the same thing in
    // the place a reader is looking.
    stubs: { total: stubs.length, drawn: 0, anchorless: stubs.length },
  };
}

// --- Measuring -------------------------------------------------------

// measure sizes one subtree, bottom-up.
function measure(key, depth, path, context) {
  const node = context.byAddress.get(key);
  const label = labelOf(node, context.config.leafLabel, context.children.get(key).length === 0);

  if (path.includes(key)) {
    const outer = path[path.length - 1];
    context.cycles.push({
      outer: labelOf(context.byAddress.get(outer), context.config.leafLabel, false),
      inner: label,
    });
    const box = boxFor(label);
    return { key, label, node, repeat: true, children: [], chip: 0, width: box.width, height: HEADER_HEIGHT };
  }

  context.drawn.push(key);
  const kids = context.children.get(key) || [];
  const capped = depth >= context.config.maxDepth && !context.expanded.has(key);

  if (kids.length === 0) {
    // A leaf: one strip, its name in it. It wears the same mark as a
    // container with the body left off, so the tint lands on the same
    // surface at every level and the dash means the same thing.
    const box = boxFor(label);
    return { key, label, node, repeat: false, children: [], chip: 0, width: box.width, height: HEADER_HEIGHT };
  }

  if (capped) {
    // Held back, not absent. Everything under here is already in the
    // envelope; the chip says how much and expanding it draws it.
    const held = descendants(key, context.children, new Set());
    for (const hidden of held) context.hidden.push(hidden);
    context.chips.push({ key, count: held.length });
    // The chip's width comes from the function that draws it, not from a
    // second measurement of the same string: two estimates of one plate
    // is boxFor's own argument, and it is why chipWidth exists.
    const width = Math.max(boxFor(label).width, chipWidth(held.length) + 2 * CONTAINER_PADDING);
    return {
      key,
      label,
      node,
      repeat: false,
      children: [],
      chip: held.length,
      width,
      height: HEADER_HEIGHT + CONTAINER_PADDING + CHIP_HEIGHT + CONTAINER_PADDING,
    };
  }

  const inner = kids.map((child) => measure(child, depth + 1, [...path, key], context));
  const packed = pack(inner, 0, 0);
  return {
    key,
    label,
    node,
    repeat: false,
    children: inner,
    chip: 0,
    width: Math.max(boxFor(label).width, packed.width + 2 * CONTAINER_PADDING),
    height: HEADER_HEIGHT + CONTAINER_PADDING + packed.height + CONTAINER_PADDING,
  };
}

// pack lays a row of measured boxes into a grid and returns its size,
// writing each box's offset from the grid's top-left corner.
function pack(boxes, originX, originY) {
  if (boxes.length === 0) return { width: 0, height: 0 };
  const columns = Math.max(1, Math.ceil(Math.sqrt(boxes.length)));
  let x = originX;
  let y = originY;
  let rowHeight = 0;
  let width = 0;
  for (let i = 0; i < boxes.length; i++) {
    if (i > 0 && i % columns === 0) {
      y += rowHeight + SIBLING_GAP;
      x = originX;
      rowHeight = 0;
    }
    boxes[i].offsetX = x;
    boxes[i].offsetY = y;
    x += boxes[i].width + SIBLING_GAP;
    width = Math.max(width, x - SIBLING_GAP - originX);
    rowHeight = Math.max(rowHeight, boxes[i].height);
  }
  return { width, height: y + rowHeight - originY };
}

// --- Drawing ---------------------------------------------------------

// emit turns one measured subtree into marks, top-down, resolving each
// box's offsets into absolute coordinates.
function emit(tree, marks, context, left = null, top = null) {
  const x = (left === null ? tree.offsetX : left) + tree.width / 2;
  const y = (top === null ? tree.offsetY : top) + tree.height / 2;

  marks.push(
    ...containerMarks({
      key: tree.key,
      label: tree.label,
      x,
      y,
      width: tree.width,
      height: tree.height,
      // The tint, on the header and never on the box.
      headerFill: tintFor(tree.node, context.legend),
      // The dash: this box's container is not in the picture. One
      // spelling, one meaning, across the six (render/marks.js).
      dash: context.orphans.has(tree.key),
    }),
  );

  if (tree.repeat) {
    marks.push(...cycleGlyphMarks({ x, y, width: tree.width, height: tree.height }, tree.key));
    return;
  }

  if (tree.chip > 0) {
    marks.push(
      ...chipMarks(
        { x, y: y + tree.height / 2 - CONTAINER_PADDING - CHIP_HEIGHT / 2 },
        tree.chip,
        tree.key,
      ),
    );
  }

  const originX = x - tree.width / 2 + CONTAINER_PADDING;
  const originY = y - tree.height / 2 + HEADER_HEIGHT + CONTAINER_PADDING;
  for (const child of tree.children) {
    emit(child, marks, context, originX + child.offsetX, originY + child.offsetY);
  }
}

// tintFor is the header's fill: the palette's hue for this node's
// `color_by` value.
function tintFor(node, legend) {
  if (legend === null) return undefined;
  const json = valueJSON(node, COLOUR_SLOT);
  if (json === null) return fillFor({ kind: "unset" }).css;
  return fillFor(legend.byValue.get(json)).css;
}

// --- Reading the parameters ------------------------------------------

function readParams(params) {
  const p = params && typeof params === "object" ? params : {};
  const depth = p[PARAM_MAX_DEPTH];
  return {
    containVia: slot(p[PARAM_CONTAIN_VIA]),
    // A bound below 1 is not a drawing: the server refuses one, and a
    // client that honoured it would draw an empty picture for a view
    // that cannot exist.
    maxDepth:
      typeof depth === "number" && Number.isFinite(depth) && depth >= 1
        ? Math.trunc(depth)
        : DEFAULT_MAX_DEPTH,
    leafLabel: slot(p[PARAM_LEAF_LABEL]),
  };
}

function slot(value) {
  return typeof value === "string" && value !== "" ? value : null;
}

// isContainment is the one place the relation type is compared, so a
// view that names no `contain_via` nests nothing rather than nesting
// everything. The catalogue makes the parameter required, so this is the
// shape of a document the server would have refused.
function isContainment(edge, containVia) {
  if (containVia === null) return false;
  return typeof edge.type === "string" && edge.type === containVia;
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

// labelOf is what a box says: `leaf_label`'s slot on a box that contains
// nothing, and the projection's `label` otherwise.
function labelOf(node, leafLabel, isLeaf) {
  if (!isObject(node)) return "";
  if (isLeaf && leafLabel !== null) {
    const json = valueJSON(node, leafLabel);
    if (json !== null) return labelFor(json);
  }
  const label = valueJSON(node, "label");
  if (label !== null) return labelFor(label);
  return text(node.name);
}

function valueJSON(node, slotName) {
  const attrs = isObject(node) && isObject(node.attrs) ? node.attrs : null;
  if (!attrs || !Object.prototype.hasOwnProperty.call(attrs, slotName)) return null;
  return JSON.stringify(attrs[slotName]);
}

// descendants is everything under a node, once each. `seen` is what
// keeps a containment cycle from making this list infinite, for
// measure's reason.
function descendants(key, children, seen) {
  const out = [];
  for (const child of children.get(key) || []) {
    if (seen.has(child)) continue;
    seen.add(child);
    out.push(child, ...descendants(child, children, seen));
  }
  return out;
}

function walk(key, children, seen) {
  if (seen.has(key)) return;
  seen.add(key);
  for (const child of children.get(key) || []) walk(child, children, seen);
}

function compare(a, b) {
  return a < b ? -1 : a > b ? 1 : 0;
}

function isObject(value) {
  return value !== null && typeof value === "object";
}

function text(value) {
  return typeof value === "string" ? value : "";
}
