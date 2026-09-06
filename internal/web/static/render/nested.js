// The `nested` renderer: boxes inside boxes, and a refusal to draw a
// tree over a graph that is not one.
//
// A renderer is a **pure function from an envelope to a scene** — the
// rule render/graph.js's header sets out — and this one is the purest of
// the six: it asks the layout engine for nothing at all. Its geometry is
// determined by the containment tree itself. See "Why no engine" below,
// because §5.1 names `nested` as one of the engine's three consumers and
// this is a deliberate departure from it.
//
// **One relation type, read as containment.** `contain_via` names it and
// the catalogue refuses a view whose query draws no edges of it, so the
// nesting is always a relation somebody chose. `source` is contained in
// `target` (internal/views/renderers.go says so in the parameter's own
// doc), which is the direction a reader has to get right once and never
// again.
//
// **Colour tints the header strip and never the box.** A nest four
// levels deep with a fill at every level is four overlapping fills and
// no legible text, so the box is `--paper` at every level and the tint
// goes on the strip that carries the name. That is §4.5's decision and
// `colourTintsTheHeaderNotTheBox` is what holds it.
//
// **Where the colour comes from, since this renderer has no `color_by`
// parameter.** The catalogue gives `nested` three knobs and none of them
// is a colour; §4.5 nonetheless says "colour from `color_by`". Those are
// reconciled the way `graph` already reconciles the label: `color_by` is
// a **projection slot** in the query language (the views spec lists
// `color_by`, `group_by`, `size_by`, `sort_by` as the conventional slot
// names), so a query that declares `project.color_by` puts a value on
// every node's `attrs` and this renderer tints from it, exactly as
// render/graph.js reads `attrs.label` with no parameter naming it. No
// knob is invented, no catalogue entry is changed, and nothing here is a
// mechanism nothing can reach: the query author turns it on.
//
// **The negative half, all three cases real.**
//
//   beyond `max_depth` — the container shows a count chip (`+12`) and
//     expanding it re-draws from the **envelope already in hand**. A
//     drawing depth is not a fetch boundary, and treating it as one
//     would make one parameter mean two things.
//   a node with no container — two different things that must not look
//     alike. A node no containment edge points out of is a **root**,
//     which is ordinary. A node whose containment edge names a target
//     that is not in this picture is an **orphan of the cap**: it is
//     drawn at the top level too, dashed, and counted in the truncation
//     band. "This thing is top-level" and "this thing's parent did not
//     fit" are two statements.
//   a containment cycle — A contains B contains A is data the metamodel
//     permits and recursion over it does not terminate. The nesting
//     stops at the repeat, the repeated box carries the cycle glyph, and
//     the frame names **both** ends. Silently stopping would draw a
//     plausible tree over a graph that is not one, which is the whole
//     class of defect this sub-project exists against.
//
// **Why no engine.** Spec §5.1 lists `nested` among dagre's three
// consumers, on the strength of dagre's compound graphs. What a compound
// layout is *for* is arranging nodes that also have edges between them;
// `nested` consumes "nodes, edges of one containment relation type" and
// draws no other relation, so there is nothing left for a ranking to
// decide. The arrangement of a container's children is determined by the
// tree and by their measured sizes, and asking a graph algorithm for it
// would be asking for an arrangement the data already states — while
// putting 48 kB of vendored dagre on the path of every nest. The packing
// is bottom-up and deterministic: children in address order, in a grid,
// with the shared vocabulary's own padding.

import { addressOf } from "../address.js";
import { fillFor, labelFor, legendFor } from "../palette.js";
import { joinEdges } from "./scene.js";
import {
  CHIP_HEIGHT,
  CONTAINER_PADDING,
  HEADER_HEIGHT,
  boxFor,
  chipMarks,
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
//
// Four, because that is the depth §4.5's own hand check asks about and
// because a fifth level of boxes inside boxes is a strip of text inside
// a strip of text. A view that wants more says so; the chip says what
// was held back, and expanding it costs nothing.
export const DEFAULT_MAX_DEPTH = 4;

// The gap between two siblings inside a container.
export const SIBLING_GAP = 10;

// The controls. Each tooltip says what the knob does to the **picture**,
// which is the half internal/views/renderers.go deliberately does not
// carry (render/controls.js's header has the argument).
//
// `max_depth`'s is the one worth reading twice: a designer who thinks it
// controls how much was *fetched* will raise it to see more content,
// and the honest sentence is that everything is already here.
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
//
// There is no `layout` argument, and its absence is the point: see the
// header. `options.expanded` is the set of container addresses a
// designer has clicked open, which is the whole of the chip's behaviour
// and is why it needs no client.
//
// Returns:
//   marks     — the scene.
//   legend    — the palette's rows for the `color_by` slot, or null when
//               no node carries one. Null and not empty: "this query
//               colours nothing" is not "this slot had no values".
//   drawn     — the addresses that got a box of their own.
//   hidden    — the addresses held back by the depth bound. Drawn plus
//               hidden is every node the answer has, which is what the
//               twin describes.
//   chips     — `{key, count}` per container that is holding children
//               back, so a test and a click handler read one answer.
//   orphans   — the addresses drawn at the top level because the
//               container they name is not in this picture.
//   cycles    — `{outer, inner}` per repeat, by label, for the frame.
//   stubs     — as every renderer reports them, for the footer.
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
//
// `path` is the chain of containers this node is being drawn inside. A
// node already on it is a **repeat**: the recursion stops there, the box
// is drawn once more with the cycle glyph, and the pair is recorded for
// the frame. That check is what makes a containment cycle terminate, and
// the test for it exists because the failure without it is a stack
// overflow rather than a wrong picture.
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
    const width = Math.max(boxFor(label).width, boxFor("+" + held.length).width + 2 * CONTAINER_PADDING);
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
//
// A grid rather than a row because a container with forty children in a
// line is a picture nobody can read at any zoom; the column count is the
// square root, so a nest stays roughly as wide as it is tall.
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
//
// A view that colours nothing gets the plate's own ground, and a node
// whose slot found nothing gets the palette's `unset` — which is
// transparent, so the strip shows the paper under it and the legend's
// own `unset` row names the absence in words. The box is never asked.
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
//
// A leaf whose `leaf_label` slot found nothing keeps its ordinary label
// rather than going blank: an empty strip is indistinguishable from a
// rendering fault, and the slot being absent is not a reason to stop
// naming the thing.
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
