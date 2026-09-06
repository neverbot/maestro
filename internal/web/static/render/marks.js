// The drawing vocabulary the six renderers share: how big a node is,
// what a value's size means, and what a node, an edge, a stub and an
// enclosure look like as marks.
//
// This file exists because `graph` is the **first** of six renderers and
// almost nothing in it is only `graph`'s. `layered` draws "the same node
// vocabulary" (spec §4.4), `nested` and `map` draw boxes with labels in
// them, and every one of the six meets an edge whose endpoint the query
// did not draw. Six copies of a rounded box would be five places for the
// corner radius, the padding and the absent-value dash to drift apart,
// and the dash in particular is a *decision* (Task 1) rather than a
// number: a renderer that spelled it differently would be telling a
// designer something different with the same picture.
//
// It is a pure function of plain data — no DOM, no state, one import —
// for the reason render/scene.js and render/twin.js are: a Node harness
// reads it and a mutation turns it red. The marks it returns are the
// contract render/scene.js declares and components/mst-canvas.js emits;
// nothing here knows an SVG element exists.
//
// **Colour is not decided here.** The palette (Task 1) decides which hue
// a value wears and what an absent value looks like; this module is
// handed a fill and a dash and puts them on a box. The one paint it does
// name is the *chrome's* — a hairline outline, a muted edge, the ground
// under an edge label — because those are not data.

import {
  LAYER_EDGES,
  LAYER_IMAGE,
  MARK_DISC,
  MARK_IMAGE,
  MARK_LABEL,
  MARK_LINE,
  MARK_RECT,
  isDrawableHref,
} from "./scene.js";

// --- What a node's box measures --------------------------------------

// The label's type size, in the 11–13px band §4.3 names. Eleven is the
// bottom of it because a graph is the densest of the six pictures.
export const LABEL_SIZE = 11;

// A box is measured from its label rather than fixed, because the name
// *is* the identifying thing (§4.3's argument against circles). The
// width of a string in a proportional face cannot be computed without a
// text engine, so this is an estimate: an average advance width as a
// fraction of the type size, for the sans stack in styles.css.
//
// **The estimate being slightly wrong is harmless; two estimates would
// not be.** The same function measures the box handed to the layout
// engine and the box drawn on the canvas, so a mis-measured label makes
// one box a few pixels wide of ideal — where a renderer that measured
// once for the engine and once for the drawing would put a label outside
// the box the layout reserved for it, at every zoom, forever.
export const CHAR_ADVANCE = 0.55;
export const NODE_PADDING_X = 12;
export const NODE_PADDING_Y = 8;
export const NODE_MIN_WIDTH = 48;
export const NODE_RADIUS = 4;

// LINE_HEIGHT is the box's inner height for one line of text.
export const LINE_HEIGHT = 1.45;

// boxFor is the size of one node's rounded rectangle, before any
// size_by scaling.
export function boxFor(label, options = {}) {
  const size = number(options.size, LABEL_SIZE);
  const text = typeof label === "string" ? label : "";
  const width = Math.max(
    NODE_MIN_WIDTH,
    Math.ceil([...text].length * CHAR_ADVANCE * size) + 2 * NODE_PADDING_X,
  );
  const height = Math.ceil(size * LINE_HEIGHT) + 2 * NODE_PADDING_Y;
  return { width, height };
}

// --- What a value's size means ---------------------------------------
//
// `size_by` maps a numeric slot to a node's **area**, over a bounded
// range, and both halves of that sentence are load-bearing.
//
// *Area*, because area is what a reader compares between two boxes. A
// map that scaled the *width* by the value would make a node with four
// times the value sixteen times the ink, which reads as a difference of
// sixteen — and getting this wrong is invisible: the picture looks fine
// and every number in it is a lie. So the value chooses an area and the
// length scale is that area's square root.
//
// *Bounded*, because an unbounded map makes one node a page. The whole
// range is 1× to 3× area: the largest value in the answer is three times
// the ink of the smallest, whatever the values are — 10 and 30, or 1 and
// 10^6. That is a deliberate loss of information. The alternative is a
// picture whose scale is set by its outlier, where every other node is a
// dot; the twin carries the numbers exactly.
export const SIZE_AREA_MIN = 1;
export const SIZE_AREA_MAX = 3;

// sizeDomainFor is the numeric range the slot spans across the answer,
// or null when there is nothing to scale against.
//
// Only numbers count. A slot present with a string in it is not a size,
// and a slot absent is not a size either; both take the range's minimum,
// which is `anAbsentSizeSlotTakesTheRangeMinimum` and is the smallest
// box rather than no box.
export function sizeDomainFor(nodes, slot) {
  if (typeof slot !== "string" || slot === "") return null;
  let min = null;
  let max = null;
  for (const node of Array.isArray(nodes) ? nodes : []) {
    const value = attrOf(node, slot);
    if (typeof value !== "number" || !Number.isFinite(value)) continue;
    if (min === null || value < min) min = value;
    if (max === null || value > max) max = value;
  }
  if (min === null) return null;
  return { min, max };
}

// areaScaleFor maps one value into [1, 3].
//
// Linear in the value across the domain, so the *ordering* of the answer
// is preserved exactly and the extremes are pinned at the bounds. A
// domain of one value (every node equal, or one node) is no range at
// all: everybody takes the minimum rather than everybody taking the
// maximum, because a picture where every box is the largest it can be
// says "these are all big" about an answer that said nothing.
export function areaScaleFor(value, domain) {
  if (!domain || typeof value !== "number" || !Number.isFinite(value)) return SIZE_AREA_MIN;
  const { min, max } = domain;
  if (!(max > min)) return SIZE_AREA_MIN;
  const t = (value - min) / (max - min);
  const clamped = t < 0 ? 0 : t > 1 ? 1 : t;
  return SIZE_AREA_MIN + clamped * (SIZE_AREA_MAX - SIZE_AREA_MIN);
}

// lengthScaleForArea is the whole of "area, not diameter", in one line
// so that it can be asserted in one line: an area four times another is
// twice the width and twice the height.
export function lengthScaleForArea(area) {
  const value = typeof area === "number" && Number.isFinite(area) && area > 0 ? area : SIZE_AREA_MIN;
  return Math.sqrt(value);
}

// scaleBox applies a length scale to both sides, so a scaled box is the
// same shape and `area(scaled) / area(base)` is the area scale exactly.
export function scaleBox(box, lengthScale) {
  const k = typeof lengthScale === "number" && Number.isFinite(lengthScale) && lengthScale > 0
    ? lengthScale
    : 1;
  return { width: box.width * k, height: box.height * k };
}

// --- The chrome's own paint ------------------------------------------

// UNFILLED is "this shape has no fill", in one spelling.
//
// It is the palette's `--unset` token (`transparent` in both themes) and
// not the SVG keyword `none`, so that the *one* declaration of "nothing
// was there to paint" lives in styles.css beside the eight hues, where
// the token guard in internal/web/static_tokens_test.go can see it. A
// literal `none` here would be a second spelling of a decision Task 1
// already took, invisible to that guard and free to drift.
export const UNFILLED = "var(--unset)";

// The hairline outline every node wears, over its fill.
export const NODE_STROKE = "var(--line)";
export const NODE_STROKE_WIDTH = 1;

// What a node with no colour slot configured wears. It is `--paper` and
// deliberately not the unset treatment: "this view does not colour
// anything" and "this node's colour slot found nothing" are two
// statements, and a picture that drew them alike would report an absence
// nobody asked about.
export const NODE_PLAIN_FILL = "var(--paper)";

// The dash of a box that is missing something the picture needed.
//
// One spelling, one meaning, across the six: **something this box needs
// is not here.** In `graph` that is its colour slot; in `layered` it is
// its numeric rank, so the node goes to the trailing unranked band and
// is dashed there; in `nested` it is its container, truncated away, so
// the box sits at the top level dashed rather than looking like a root.
// A renderer that spelled the dash differently, or that used it for
// "this is unusual" rather than for "this is missing", would tell a
// designer something different with the same picture.
//
// A second, non-colour carrier for an absence, exactly as the twin's
// `absent` class is beside its em dash — and never the *only* carrier:
// each renderer above pairs it with a position, a legend row or a count.
export const ABSENT_DASH = "4 3";

export const EDGE_STROKE = "var(--muted)";
export const EDGE_STROKE_WIDTH = 1;
export const LABEL_FILL = "var(--ink)";
export const PLATE_FILL = "var(--ground)";

// The enclosure a `group_by` value draws: a hairline, never a filled
// panel, because a stub legitimately leaves it (§4.3) and a filled panel
// would make that look like an error.
export const ENCLOSURE_STROKE = "var(--line)";
export const ENCLOSURE_PADDING = 16;
export const ENCLOSURE_HEADING_GAP = 6;

// The classes a mark wears, so a stylesheet and a test name the same
// thing. They are constants for BANNER_ORDER's reason: a renamed class
// is one visible diff.
export const CLASS_NODE = "node";
export const CLASS_NODE_LABEL = "node-label";
export const CLASS_NODE_ABSENT = "node absent";
export const CLASS_AMBIGUOUS = "ambiguous";
export const CLASS_EDGE = "edge";
export const CLASS_EDGE_LABEL = "edge-label";
export const CLASS_EDGE_PLATE = "edge-plate";
export const CLASS_ARROW = "arrow";
export const CLASS_STUB = "stub";
export const CLASS_STUB_RING = "stub-ring";
export const CLASS_ENCLOSURE = "enclosure";
export const CLASS_ENCLOSURE_HEADING = "enclosure-heading";

// The arrowhead's geometry, and the stub's.
export const ARROW_LENGTH = 8;
export const ARROW_SPREAD = 0.42;
export const STUB_LENGTH = 28;
export const STUB_RING_RADIUS = 3.5;
export const AMBIGUOUS_RADIUS = 3;
export const AMBIGUOUS_INSET = 4;
export const AMBIGUOUS_FILL = "var(--line-strong)";

// --- The marks -------------------------------------------------------

// nodeMarks is one node's box, its name, and — when the envelope says
// the node is ambiguous — one mark at its corner.
//
// `placement` is the layout's answer: `{x, y}` is the box's **centre**,
// which is what engine.js reports and what compose.js composes, so the
// centring arithmetic happens here once rather than in six renderers.
//
// The ambiguity mark is on the **node** and there is exactly one of it,
// because `Node.Ambiguous` is a flag on the node: execute.go says a node
// with two related slots, one of them ambiguous, is flagged and which of
// the two is deliberately not said. A per-slot mark would be this
// interface inventing a distinction the server declined to carry.
export function nodeMarks(node) {
  const { key, label, x, y, width, height, fill, dash, ambiguous } = node;
  const marks = [
    {
      kind: MARK_RECT,
      key,
      class: dash ? CLASS_NODE_ABSENT : CLASS_NODE,
      x: x - width / 2,
      y: y - height / 2,
      w: width,
      h: height,
      radius: NODE_RADIUS,
      fill,
      stroke: dash ? "var(--line-strong)" : NODE_STROKE,
      strokeWidth: NODE_STROKE_WIDTH,
      dash: dash ? ABSENT_DASH : undefined,
    },
    {
      kind: MARK_LABEL,
      key,
      class: CLASS_NODE_LABEL,
      x,
      y,
      text: typeof label === "string" ? label : "",
      fill: LABEL_FILL,
      size: LABEL_SIZE,
      anchor: "middle",
      baseline: "middle",
    },
  ];
  if (ambiguous) {
    marks.push({
      kind: MARK_DISC,
      key,
      class: CLASS_AMBIGUOUS,
      cx: x + width / 2 - AMBIGUOUS_INSET,
      cy: y - height / 2 + AMBIGUOUS_INSET,
      r: AMBIGUOUS_RADIUS,
      fill: AMBIGUOUS_FILL,
    });
  }
  return marks;
}

// edgeMarks is one drawn edge: the line, the arrowhead when `arrows` is
// on, and the label on its plate when `edge_labels` is.
//
// The line runs between the two boxes' **borders** rather than their
// centres, so an arrowhead lands where a reader expects it and a line
// does not run under the box it points at.
//
// Every mark here carries `source` and `target`, which is how
// mst-canvas.js knows an edge from a decoration and which end of it is
// moving during a drag — except the two the drag layer cannot honestly
// reshape, see below.
export function edgeMarks(edge) {
  const { source, target, label, arrows, edgeLabels } = edge;
  const from = borderPoint(source, target);
  const to = borderPoint(target, source);
  const marks = [
    {
      kind: MARK_LINE,
      class: CLASS_EDGE,
      source: source.key,
      target: target.key,
      x1: from.x,
      y1: from.y,
      x2: to.x,
      y2: to.y,
      stroke: EDGE_STROKE,
      strokeWidth: EDGE_STROKE_WIDTH,
    },
  ];
  if (arrows) marks.push(...arrowMarks(from, to, target.key));
  if (edgeLabels && typeof label === "string" && label !== "") {
    marks.push(...edgeLabelMarks(midpoint(from, to), label));
  }
  return marks;
}

// arrowMarks is the head at the target end, and it is a two-line chevron
// rather than the filled triangle §4.3 describes.
//
// **A filled head would need a mark kind the drag layer cannot move.** A
// triangle is a `polygon` with a `points` list, and mst-canvas.js
// translates a dragged body by adding a delta to each of a kind's
// coordinate *pairs* (MARK_ORIGINS) and bakes the offset in on drop; a
// points list has no pairs to name, so a head that rode a drag would
// snap back to where it started when the drag ended. Task 7 took the
// same decision about a curved edge for the same reason and said the
// renderer that needs the kind adds the kind and the reshaping answer
// together. A chevron is two lines, which that machinery already moves
// exactly.
//
// Both of the head's endpoint keys are the **target's**, which is not a
// mistake: a mark whose two ends belong to one node is a mark that rides
// that node's transform whole, which is exactly right — the head is
// attached to the box it points at, moves with it, and is baked in with
// it on drop.
export function arrowMarks(from, to, targetKey) {
  const dx = to.x - from.x;
  const dy = to.y - from.y;
  const length = Math.hypot(dx, dy);
  if (!(length > 0)) return [];
  const ux = dx / length;
  const uy = dy / length;
  const back = { x: to.x - ux * ARROW_LENGTH, y: to.y - uy * ARROW_LENGTH };
  const nx = -uy * ARROW_LENGTH * ARROW_SPREAD;
  const ny = ux * ARROW_LENGTH * ARROW_SPREAD;
  return [
    arrowLine(to, { x: back.x + nx, y: back.y + ny }, targetKey),
    arrowLine(to, { x: back.x - nx, y: back.y - ny }, targetKey),
  ];
}

function arrowLine(tip, tail, targetKey) {
  return {
    kind: MARK_LINE,
    class: CLASS_ARROW,
    source: targetKey,
    target: targetKey,
    x1: tip.x,
    y1: tip.y,
    x2: tail.x,
    y2: tail.y,
    stroke: EDGE_STROKE,
    strokeWidth: EDGE_STROKE_WIDTH,
  };
}

// edgeLabelMarks is the label on its `--ground` plate at the middle of
// the edge.
//
// It carries **no** endpoint keys, so the drag layer neither carries it
// nor reshapes it: a plate has one position and an edge whose one end
// moved has a new middle, which no translation of the plate can produce.
// It therefore stands still during a drag and is redrawn where it
// belongs when the picture is (spec §6.1: the drop is the write, and the
// write re-runs the view). The arrowhead is the exception above because
// its position is a property of one node and a translation *is* exact.
export function edgeLabelMarks(at, label) {
  const box = boxFor(label, { size: LABEL_SIZE });
  const width = box.width - NODE_PADDING_X;
  const height = LABEL_SIZE + NODE_PADDING_Y;
  return [
    {
      kind: MARK_RECT,
      class: CLASS_EDGE_PLATE,
      layer: LAYER_EDGES,
      x: at.x - width / 2,
      y: at.y - height / 2,
      w: width,
      h: height,
      radius: 2,
      fill: PLATE_FILL,
    },
    {
      kind: MARK_LABEL,
      class: CLASS_EDGE_LABEL,
      x: at.x,
      y: at.y,
      text: label,
      fill: LABEL_FILL,
      size: LABEL_SIZE,
      anchor: "middle",
      baseline: "middle",
    },
  ];
}

// stubMarks is an edge that leaves the picture: it starts at its known
// endpoint's border and ends in a small hollow ring, with no label.
//
// **The direction is outward from the node's enclosure**, and the
// terminus is placed beyond that enclosure's edge, which is what makes
// `stubsLeaveTheirGroupEnclosure` a property of the drawing rather than
// of a lucky fixture. A stub that stopped inside its group's box would
// read as a relation to something in the group, which is the one thing
// it is not. When the node is in no group the enclosure is the picture's
// own bounds, so a stub still points away from the drawing.
//
// It carries the known endpoint's key at **both** ends, for arrowMarks's
// reason: the whole stub belongs to one node and rides its transform.
export function stubMarks(stub) {
  const { node, enclosure } = stub;
  const direction = outward(node, enclosure);
  const start = borderPointTowards(node, direction);
  const exit = exitPoint(enclosure, node, direction);
  const distance = Math.max(
    STUB_LENGTH,
    Math.hypot(exit.x - start.x, exit.y - start.y) + STUB_LENGTH,
  );
  const end = { x: start.x + direction.x * distance, y: start.y + direction.y * distance };
  return [
    {
      kind: MARK_LINE,
      class: CLASS_STUB,
      source: node.key,
      target: node.key,
      x1: start.x,
      y1: start.y,
      x2: end.x,
      y2: end.y,
      stroke: EDGE_STROKE,
      strokeWidth: EDGE_STROKE_WIDTH,
    },
    {
      kind: MARK_DISC,
      key: node.key,
      class: CLASS_STUB_RING,
      cx: end.x,
      cy: end.y,
      r: STUB_RING_RADIUS,
      fill: UNFILLED,
      stroke: EDGE_STROKE,
      strokeWidth: EDGE_STROKE_WIDTH,
    },
  ];
}

// enclosureFor is a `group_by` value's hairline box and its heading.
//
// The box goes on the **image** layer, under the edges: an enclosure
// drawn over the picture would cut every edge that crosses it, and the
// one thing an enclosure must not do is look like a relation.
export function enclosureFor(members, heading) {
  const bounds = boundsOf(members);
  if (!bounds) return [];
  const x = bounds.minX - ENCLOSURE_PADDING;
  const y = bounds.minY - ENCLOSURE_PADDING;
  const w = bounds.maxX - bounds.minX + 2 * ENCLOSURE_PADDING;
  const h = bounds.maxY - bounds.minY + 2 * ENCLOSURE_PADDING;
  return [
    {
      kind: MARK_RECT,
      class: CLASS_ENCLOSURE,
      layer: LAYER_IMAGE,
      x,
      y,
      w,
      h,
      radius: NODE_RADIUS,
      fill: UNFILLED,
      stroke: ENCLOSURE_STROKE,
      strokeWidth: NODE_STROKE_WIDTH,
    },
    {
      kind: MARK_LABEL,
      class: CLASS_ENCLOSURE_HEADING,
      x,
      y: y - ENCLOSURE_HEADING_GAP,
      text: typeof heading === "string" ? heading : "",
      fill: "var(--muted)",
      size: LABEL_SIZE,
      anchor: "start",
      baseline: "auto",
    },
  ];
}

// --- Geometry --------------------------------------------------------

// boundsOf is the rectangle a set of placed boxes occupies, or null for
// an empty set — null rather than a zero rectangle at the origin, for
// execute.go's own reason about an unplaced node: a box at (0,0) is a
// claim, and there is nothing here to claim it about.
export function boundsOf(boxes) {
  let bounds = null;
  for (const box of Array.isArray(boxes) ? boxes : []) {
    if (!box || !Number.isFinite(box.x) || !Number.isFinite(box.y)) continue;
    const halfW = (box.width || 0) / 2;
    const halfH = (box.height || 0) / 2;
    const next = {
      minX: box.x - halfW,
      minY: box.y - halfH,
      maxX: box.x + halfW,
      maxY: box.y + halfH,
    };
    if (!bounds) {
      bounds = next;
      continue;
    }
    bounds.minX = Math.min(bounds.minX, next.minX);
    bounds.minY = Math.min(bounds.minY, next.minY);
    bounds.maxX = Math.max(bounds.maxX, next.maxX);
    bounds.maxY = Math.max(bounds.maxY, next.maxY);
  }
  return bounds;
}

// borderPoint is where the segment between two box centres crosses the
// first box's border.
export function borderPoint(box, towards) {
  const dx = towards.x - box.x;
  const dy = towards.y - box.y;
  return borderPointTowards(box, unit(dx, dy));
}

export function borderPointTowards(box, direction) {
  const halfW = box.width / 2;
  const halfH = box.height / 2;
  const { x: ux, y: uy } = direction;
  const tx = ux === 0 ? Infinity : halfW / Math.abs(ux);
  const ty = uy === 0 ? Infinity : halfH / Math.abs(uy);
  const t = Math.min(tx, ty);
  if (!Number.isFinite(t)) return { x: box.x, y: box.y };
  return { x: box.x + ux * t, y: box.y + uy * t };
}

// exitPoint is where a ray from a box's centre leaves a rectangle.
export function exitPoint(rect, from, direction) {
  const { x: ux, y: uy } = direction;
  const tx = ux === 0 ? Infinity : (ux > 0 ? rect.maxX - from.x : rect.minX - from.x) / ux;
  const ty = uy === 0 ? Infinity : (uy > 0 ? rect.maxY - from.y : rect.minY - from.y) / uy;
  const t = Math.max(0, Math.min(tx, ty));
  if (!Number.isFinite(t)) return { x: from.x, y: from.y };
  return { x: from.x + ux * t, y: from.y + uy * t };
}

// outward is the direction from an enclosure's centre through the node.
// A node exactly at the centre — a group of one — has no such direction
// and takes a fixed one rather than a NaN.
export function outward(node, enclosure) {
  const cx = (enclosure.minX + enclosure.maxX) / 2;
  const cy = (enclosure.minY + enclosure.maxY) / 2;
  const dx = node.x - cx;
  const dy = node.y - cy;
  if (dx === 0 && dy === 0) return { x: 1, y: 0 };
  return unit(dx, dy);
}

function unit(dx, dy) {
  const length = Math.hypot(dx, dy);
  if (!(length > 0)) return { x: 1, y: 0 };
  return { x: dx / length, y: dy / length };
}

function midpoint(a, b) {
  return { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 };
}

function attrOf(node, slot) {
  const attrs = node && typeof node.attrs === "object" && node.attrs !== null ? node.attrs : null;
  if (!attrs || !Object.prototype.hasOwnProperty.call(attrs, slot)) return undefined;
  return attrs[slot];
}

function number(value, fallback) {
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

// --- The rank band ---------------------------------------------------
//
// `layered` draws a muted rule and a caption per rank when `layer_labels`
// is on, and `timeline` draws a hairline and a caption per lane (§4.8).
// They are the same two marks — a rule across the picture and a word at
// the end of it — so they are one function here rather than two that
// agree today.
//
// The rule is `--muted` and 1px: it is chrome, and a band boundary that
// competed with an edge would read as a relation.
export const CLASS_BAND_RULE = "band-rule";
export const CLASS_BAND_CAPTION = "band-caption";
export const BAND_RULE_STROKE = "var(--muted)";
export const BAND_CAPTION_FILL = "var(--muted)";
export const BAND_CAPTION_GAP = 6;

// bandMarks is one band's rule and its caption.
//
// The caption's position and anchor are the caller's, because a band's
// caption sits above a horizontal rule and beside a vertical one, and a
// function that guessed which from the geometry would guess wrong the
// first time a picture was square.
//
// A band with no caption draws its rule alone: an empty string is not a
// caption, and a label with no text is an element a reader cannot see
// and a test can.
export function bandMarks(band) {
  const { x1, y1, x2, y2, caption, captionX, captionY, anchor, baseline } = band;
  const marks = [
    {
      kind: MARK_LINE,
      class: CLASS_BAND_RULE,
      layer: LAYER_IMAGE,
      x1,
      y1,
      x2,
      y2,
      stroke: BAND_RULE_STROKE,
      strokeWidth: EDGE_STROKE_WIDTH,
    },
  ];
  if (typeof caption === "string" && caption !== "") {
    marks.push({
      kind: MARK_LABEL,
      class: CLASS_BAND_CAPTION,
      x: Number.isFinite(captionX) ? captionX : x1,
      y: Number.isFinite(captionY) ? captionY : y1 - BAND_CAPTION_GAP,
      text: caption,
      fill: BAND_CAPTION_FILL,
      size: LABEL_SIZE,
      anchor: typeof anchor === "string" ? anchor : "start",
      baseline: typeof baseline === "string" ? baseline : "auto",
    });
  }
  return marks;
}

// --- The edge the ranking had to break --------------------------------
//
// A ranked drawing of a graph with a cycle is only possible because the
// engine reverses an edge, and §4.4 refuses to let the interface hide
// that: the arrowhead stays in the relation's **true** direction and the
// line carries a small double-slash, the map-maker's mark for a break.
//
// Two short parallel strokes, at 45° to the line so they are legible
// whatever direction it runs, drawn at its midpoint.
export const CLASS_REVERSED = "reversed";
export const REVERSAL_LENGTH = 9;
export const REVERSAL_GAP = 5;

// reversalMarks carries **no endpoint keys**, for edgeLabelMarks's
// reason and not by omission: it sits at the middle of a line, and an
// edge whose one end moved has a new middle, which no translation of a
// pair of strokes can produce. It stands still during a drag and is
// redrawn with the picture on the drop that writes.
export function reversalMarks(at, direction) {
  const { x: ux, y: uy } = direction;
  // The stroke direction: the edge's own, turned by 45°.
  const c = Math.SQRT1_2;
  const sx = (ux - uy) * c;
  const sy = (ux + uy) * c;
  const half = REVERSAL_LENGTH / 2;
  const offset = REVERSAL_GAP / 2;
  return [-offset, offset].map((along) => ({
    kind: MARK_LINE,
    class: CLASS_REVERSED,
    layer: LAYER_EDGES,
    x1: at.x + ux * along - sx * half,
    y1: at.y + uy * along - sy * half,
    x2: at.x + ux * along + sx * half,
    y2: at.y + uy * along + sy * half,
    stroke: EDGE_STROKE,
    strokeWidth: EDGE_STROKE_WIDTH,
  }));
}

// midpointOf is the middle of a drawn edge, so a caller that has to
// decorate one does not restate the arithmetic edgeMarks uses.
export function midpointOf(source, target) {
  return midpoint(borderPoint(source, target), borderPoint(target, source));
}

// directionOf is the unit vector from one box's centre to another's.
export function directionOf(source, target) {
  return unit(target.x - source.x, target.y - source.y);
}
// --- Boxes that contain boxes ----------------------------------------
//
// `nested` draws a container as a `--paper` rectangle with a hairline
// and its name on the top-left, and tints the **header strip** rather
// than the box: a nest of four filled levels is four overlapping fills
// and no legible text (§4.5). So the fill and the tint are two fields
// here, and the box's is not a caller's choice.
export const CLASS_CONTAINER = "container";
export const CLASS_CONTAINER_ABSENT = "container absent";
export const CLASS_CONTAINER_HEADER = "container-header";
export const CLASS_CONTAINER_LABEL = "container-label";
export const CONTAINER_PADDING = 14;
export const HEADER_HEIGHT = Math.ceil(LABEL_SIZE * LINE_HEIGHT) + 2 * NODE_PADDING_Y;

// containerMarks is one container: the box, its header strip, its name.
//
// `x`/`y` are the box's **centre**, as everywhere else in this module,
// so a caller never has to remember which of the two conventions this
// mark uses.
//
// `headerFill` is where colour lands. A container with no colour to
// carry gets the ground under its name, which is a strip a reader can
// see the shape of; `UNFILLED` would make the header invisible and the
// tint's absence indistinguishable from a renderer that forgot it.
export function containerMarks(container) {
  const { key, label, x, y, width, height, headerFill, dash } = container;
  const left = x - width / 2;
  const top = y - height / 2;
  const header = Math.min(HEADER_HEIGHT, height);
  return [
    {
      kind: MARK_RECT,
      key,
      class: dash ? CLASS_CONTAINER_ABSENT : CLASS_CONTAINER,
      x: left,
      y: top,
      w: width,
      h: height,
      radius: NODE_RADIUS,
      // Never the tint: the box is paper at every level, and the header
      // is the one surface colour is allowed to touch.
      fill: NODE_PLAIN_FILL,
      stroke: dash ? "var(--line-strong)" : NODE_STROKE,
      strokeWidth: NODE_STROKE_WIDTH,
      dash: dash ? ABSENT_DASH : undefined,
    },
    {
      kind: MARK_RECT,
      key,
      class: CLASS_CONTAINER_HEADER,
      x: left,
      y: top,
      w: width,
      h: header,
      radius: NODE_RADIUS,
      fill: typeof headerFill === "string" && headerFill !== "" ? headerFill : PLATE_FILL,
    },
    {
      kind: MARK_LABEL,
      key,
      class: CLASS_CONTAINER_LABEL,
      x: left + NODE_PADDING_X,
      y: top + header / 2,
      text: typeof label === "string" ? label : "",
      fill: LABEL_FILL,
      size: LABEL_SIZE,
      anchor: "start",
      baseline: "middle",
    },
  ];
}

// --- The count chip --------------------------------------------------
//
// What a drawing shows when it is deliberately not drawing something it
// holds: `nested`'s children beyond `max_depth` (§4.5) and `timeline`'s
// marks past the third in one lane (§4.8) are the same statement — *"and
// twelve more, already here"* — so they are one mark.
//
// It reads `+12` and never `12`: a bare number beside a box reads as a
// property of the box.
//
// **It also carries a name, which is `map`'s shelf.** A shelved node is
// a labelled plate in a strip along the bottom edge (§4.2), and that is
// this plate with a word in it instead of a count — same height, same
// ground, same hairline, so a designer meets one shape rather than two.
// The two callers differ in exactly one thing, which is what `label`
// takes: a number is written `+12`, a string is written as it stands.
export const CLASS_CHIP = "chip";
export const CLASS_CHIP_ABSENT = "chip absent";
export const CLASS_CHIP_LABEL = "chip-label";
export const CHIP_HEIGHT = LABEL_SIZE + NODE_PADDING_Y;

export function chipText(count) {
  return "+" + Math.max(0, Math.trunc(count));
}

// chipLabel is what a chip says: a count is written `+12`, a name is
// written as it stands.
export function chipLabel(label) {
  return typeof label === "number" ? chipText(label) : String(label ?? "");
}

// chipWidth is how wide that plate is, **measured once**.
//
// A caller that has to lay chips out in a row — `map`'s shelf — needs
// the width before the marks exist, and boxFor's own argument applies to
// the second measurement exactly as it applies to a node's: a shelf
// spaced by one estimate and drawn by another overlaps its own plates.
// So chipMarks calls this too, and there is one number.
export function chipWidth(label) {
  return boxFor(chipLabel(label), { size: LABEL_SIZE }).width - NODE_PADDING_X;
}

// chipMarks is the plate and its text, centred on `at`.
//
// `label` is a number — the count of what is not being drawn — or the
// text of a plate that names something. `options.dash` marks the plate
// the way ABSENT_DASH marks a box, and means the same thing: something
// this plate needs is not here. That is what a shelf chip is.
export function chipMarks(at, label, key, options = {}) {
  const text = chipLabel(label);
  const dash = options.dash === true;
  const width = chipWidth(label);
  return [
    {
      kind: MARK_RECT,
      key,
      class: dash ? CLASS_CHIP_ABSENT : CLASS_CHIP,
      x: at.x - width / 2,
      y: at.y - CHIP_HEIGHT / 2,
      w: width,
      h: CHIP_HEIGHT,
      radius: 2,
      fill: PLATE_FILL,
      stroke: dash ? "var(--line-strong)" : NODE_STROKE,
      strokeWidth: NODE_STROKE_WIDTH,
      dash: dash ? ABSENT_DASH : undefined,
    },
    {
      kind: MARK_LABEL,
      key,
      class: CLASS_CHIP_LABEL,
      x: at.x,
      y: at.y,
      text,
      fill: LABEL_FILL,
      size: LABEL_SIZE,
      anchor: "middle",
      baseline: "middle",
    },
  ];
}

// --- The repeat ------------------------------------------------------
//
// A containment cycle — A contains B contains A — is data the metamodel
// permits, and a nest that drew it would recurse forever. The recursion
// stops at the repeat and the repeated box says so, because silently
// stopping would draw a plausible tree over a graph that is not one
// (§4.5).
//
// A glyph and not only a dash: the dash already means "something this
// box needs is not in the picture", and a repeat is the opposite — the
// thing is here, and here again.
export const CLASS_CYCLE = "cycle";
export const CYCLE_GLYPH = "↻";

export function cycleGlyphMarks(box, key) {
  return [
    {
      kind: MARK_LABEL,
      key,
      class: CLASS_CYCLE,
      x: box.x + box.width / 2 - NODE_PADDING_X,
      y: box.y - box.height / 2 + HEADER_HEIGHT / 2,
      text: CYCLE_GLYPH,
      fill: "var(--line-strong)",
      size: LABEL_SIZE,
      anchor: "end",
      baseline: "middle",
    },
  ];
}

// --- A ground, and marks on it ---------------------------------------
//
// `map` is the one renderer with an image under it (§4.6), and the four
// marks below are what a map is made of. They live here rather than in
// render/map.js for this file's own reason: the pin's hollow ring is the
// stub's ring, the halo's ground is the edge plate's ground, and the
// grid's hairline is the enclosure's hairline. Three of those are
// decisions Task 1 took about what a reader is being told, and a
// renderer that respelled any of them would say something slightly
// different with the same picture.
//
// It is also where the *second* map-shaped renderer would come looking,
// which is the test render/controls.js's header sets for a shared shape.

// A node on a map is a 7px disc and not a labelled box: a map with two
// hundred boxes on it is not a map (§4.6). The label sits beside it.
export const CLASS_PIN = "pin";
export const CLASS_PIN_UNPLACED = "pin unplaced";
export const CLASS_PIN_LABEL = "pin-label";
export const PIN_RADIUS = 3.5;
export const PIN_LABEL_GAP = 5;

// What a pin is painted with, and it is **chrome rather than data**.
//
// `map` does not tint its pins, which departs from what a reader coming
// from `graph` would expect, and the reason is the hollow ring above:
// §4.2 already spends "an outline with nothing in it" on *this
// coordinate is one nobody chose*, and Task 1 spends the same treatment
// — unfilled, dashed — on *this node's colour slot found nothing*. Two
// facts cannot share one mark. The catalogue gives `map` no colour knob
// either, so nothing is being withheld: the hue on a map belongs to the
// designer's own image, which is §2's rule about where colour comes from
// read in the one picture that has a ground.
export const PIN_FILL = "var(--ink)";

// The halo that lets an 11px label survive over a designer's own image.
// It is the ground colour, painted as a stroke *under* the glyphs, which
// is what `paint-order: stroke` in styles.css arranges; the mark carries
// the two attributes and nothing about painting order, which is the
// stylesheet's.
export const LABEL_HALO = "var(--ground)";
export const LABEL_HALO_WIDTH = 2;

// pinMarks is one node on the map: its disc, its label up and to the
// right, and the ambiguity mark when the envelope flagged the node.
//
// **A hollow disc is a coordinate nobody chose.** §4.2 asks for a hollow
// anchor rather than a solid one for a node the client placed
// automatically, and it is the same hollow the stub's terminus uses for
// the same reason: an outline with nothing in it is this vocabulary's
// one spelling of "there is less here than there looks". A designer who
// has dragged nothing sees a map of rings and knows the arrangement is
// not theirs yet.
export function pinMarks(pin) {
  const { key, label, x, y, fill, placed, ambiguous } = pin;
  const marks = [
    {
      kind: MARK_DISC,
      key,
      class: placed ? CLASS_PIN : CLASS_PIN_UNPLACED,
      cx: x,
      cy: y,
      r: PIN_RADIUS,
      fill: placed ? (typeof fill === "string" && fill !== "" ? fill : PIN_FILL) : UNFILLED,
      stroke: placed ? NODE_STROKE : "var(--line-strong)",
      strokeWidth: NODE_STROKE_WIDTH,
    },
    ...haloLabelMarks(
      { x: x + PIN_RADIUS + PIN_LABEL_GAP, y: y - PIN_RADIUS - PIN_LABEL_GAP },
      label,
      { key, class: CLASS_PIN_LABEL, anchor: "start", baseline: "auto" },
    ),
  ];
  if (ambiguous) {
    marks.push({
      kind: MARK_DISC,
      key,
      class: CLASS_AMBIGUOUS,
      cx: x + PIN_RADIUS,
      cy: y - PIN_RADIUS,
      r: AMBIGUOUS_RADIUS,
      fill: AMBIGUOUS_FILL,
    });
  }
  return marks;
}

// haloLabelMarks is a label that has to be legible over something this
// interface did not choose.
//
// One mark and not two: a plate behind a name on a map would hide the
// map, which is the thing the designer uploaded. The halo is the
// alternative §4.6 names, and it is a property of the label rather than
// a second mark so that a label and its halo cannot be separated by a
// caller who forgot one.
export function haloLabelMarks(at, label, options = {}) {
  return [
    {
      kind: MARK_LABEL,
      key: options.key,
      class: typeof options.class === "string" ? options.class : CLASS_NODE_LABEL,
      x: at.x,
      y: at.y,
      text: typeof label === "string" ? label : "",
      fill: LABEL_FILL,
      size: LABEL_SIZE,
      anchor: typeof options.anchor === "string" ? options.anchor : "start",
      baseline: typeof options.baseline === "string" ? options.baseline : "auto",
      halo: LABEL_HALO,
      haloWidth: LABEL_HALO_WIDTH,
    },
  ];
}

// The grid `snap` draws, under the background: hairline, `--line`, and
// never `--muted`, which is the colour of something a reader is meant to
// read.
export const CLASS_GRID = "grid";

// gridMarks is the lines of a grid of `spacing` over `bounds`.
//
// It draws nothing for a spacing that is not a positive number, which is
// what `snap: 0` means — the catalogue's own doc says "0 for no grid" —
// and nothing for bounds it cannot measure. Both are "there is no grid
// here" rather than a grid of one line at the origin.
//
// The lines are on the **image** layer, under everything including the
// background, which is §4.6's own instruction: a grid over a designer's
// image competes with it, and a grid under it is visible exactly where
// the image is not.
export function gridMarks(bounds, spacing) {
  if (!bounds || !Number.isFinite(spacing) || spacing <= 0) return [];
  const marks = [];
  const firstX = Math.ceil(bounds.minX / spacing) * spacing;
  const firstY = Math.ceil(bounds.minY / spacing) * spacing;
  for (let x = firstX; x <= bounds.maxX; x += spacing) {
    marks.push(gridLine(x, bounds.minY, x, bounds.maxY));
  }
  for (let y = firstY; y <= bounds.maxY; y += spacing) {
    marks.push(gridLine(bounds.minX, y, bounds.maxX, y));
  }
  return marks;
}

function gridLine(x1, y1, x2, y2) {
  return {
    kind: MARK_LINE,
    class: CLASS_GRID,
    layer: LAYER_IMAGE,
    x1,
    y1,
    x2,
    y2,
    stroke: ENCLOSURE_STROKE,
    strokeWidth: NODE_STROKE_WIDTH,
  };
}

// The background image itself.
export const CLASS_BACKGROUND = "background";

// backgroundMarks is the designer's own image, at full opacity, at the
// scale and offset the *view* carries — those are columns of the view
// and not renderer parameters, which internal/views/renderers.go refuses
// them as being, because only a foreign key can keep a reference to a
// stored asset honest.
//
// **An href this interface would not fetch draws nothing**, through
// render/scene.js's own isDrawableHref rather than through a second
// rule: `<image>` is the one mark field a browser resolves rather than
// draws, and the whole legitimate set is this instance's own asset
// paths. A background that does not draw is not silent, either — the
// renderer bands it, exactly as a removed one is banded, because a
// ground that is simply missing looks like a ground that was never set.
//
// `preserveAspectRatio: "none"` is deliberate and is the only honest
// answer: `background_scale` is one number, the width and the height are
// both derived from it, so the image is drawn at its own aspect ratio
// and the attribute is what says the emitter must not add a second
// opinion.
export function backgroundMarks(background) {
  if (!background || !isDrawableHref(background.href)) return [];
  const { href, x, y, width, height } = background;
  if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) return [];
  return [
    {
      kind: MARK_IMAGE,
      class: CLASS_BACKGROUND,
      layer: LAYER_IMAGE,
      x,
      y,
      w: width,
      h: height,
      href,
      // §4.6: "at 100% opacity". A background dimmed to make the nodes
      // read would be this interface editing the designer's own file.
      opacity: 1,
      fit: "none",
    },
  ];
}
