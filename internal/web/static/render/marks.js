// The drawing vocabulary the six renderers share: how big a node is,
// what a value's size means, and what a node, an edge, a stub and an
// enclosure look like as marks.

import { labelOn } from "../palette.js";
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
export const SIZE_AREA_MIN = 1;
export const SIZE_AREA_MAX = 3;

// sizeDomainFor is the numeric range the slot spans across the answer,
// or null when there is nothing to scale against.
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
export const ABSENT_DASH = "4 3";

export const EDGE_STROKE = "var(--muted)";
export const EDGE_STROKE_WIDTH = 1;
// The default, for a label on a plate, on paper or on nothing. A label
// printed *on a hue* asks palette.js's labelOn instead: see its comment
// for the eight contrast measurements that made this a decision rather
// than a constant.
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
      // The box's own fill decides this: a hue takes the paper tone, and
      // anything else takes ink.
      fill: labelOn(fill),
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
export const CLASS_BAND_RULE = "band-rule";
export const CLASS_BAND_CAPTION = "band-caption";
export const BAND_RULE_STROKE = "var(--muted)";
export const BAND_CAPTION_FILL = "var(--muted)";
export const BAND_CAPTION_GAP = 6;

// bandMarks is one band's rule and its caption.
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
export const CLASS_REVERSED = "reversed";
export const REVERSAL_TEXT = "//";
// Larger than a name. A mark that means "the drawing had to break a
// cycle to exist" is not a label on something, and at the same size as
// every node's name it reads as one more name.
export const REVERSAL_SIZE = LABEL_SIZE * 1.6;

// reversalMarks carries **no endpoint keys**, for edgeLabelMarks's
// reason and not by omission: it sits at the middle of a line, and an
// edge whose one end moved has a new middle, which no translation of a
// mark at that middle can produce. It stands still during a drag and is
// redrawn with the picture on the drop that writes.
export function reversalMarks(at, _direction) {
  return [
    {
      kind: MARK_LABEL,
      class: CLASS_REVERSED,
      // No layer: a label's default is the labels layer, which is over
      // the edges it decorates, and naming what the contract already
      // says is a second place for it to disagree.
      x: at.x,
      y: at.y,
      text: REVERSAL_TEXT,
      fill: EDGE_STROKE,
      size: REVERSAL_SIZE,
      anchor: "middle",
      baseline: "middle",
      halo: LABEL_HALO,
      haloWidth: LABEL_HALO_WIDTH,
    },
  ];
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
export const CLASS_CONTAINER = "container";
export const CLASS_CONTAINER_ABSENT = "container absent";
export const CLASS_CONTAINER_HEADER = "container-header";
export const CLASS_CONTAINER_LABEL = "container-label";
export const CONTAINER_PADDING = 14;
export const HEADER_HEIGHT = Math.ceil(LABEL_SIZE * LINE_HEIGHT) + 2 * NODE_PADDING_Y;

// containerMarks is one container: the box, its header strip, its name.
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
      // The header is the one surface colour touches in this renderer,
      // so the heading printed on it answers to the header's fill.
      fill: labelOn(typeof headerFill === "string" ? headerFill : ""),
      size: LABEL_SIZE,
      anchor: "start",
      baseline: "middle",
    },
  ];
}

// --- The count chip --------------------------------------------------
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
export function chipWidth(label) {
  return boxFor(chipLabel(label), { size: LABEL_SIZE }).width - NODE_PADDING_X;
}

// chipMarks is the plate and its text, centred on `at`.
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
export const CLASS_POINT = "point";
export const CLASS_POINT_UNPLACED = "point unplaced";
export const CLASS_POINT_LABEL = "point-label";
export const POINT_RADIUS = 3.5;
export const POINT_LABEL_GAP = 5;

// What a pin is painted with, and it is **chrome rather than data**.
export const POINT_FILL = "var(--ink)";

// The halo that lets an 11px label survive over a designer's own image.
// It is the ground colour, painted as a stroke *under* the glyphs, which
// is what `paint-order: stroke` in styles.css arranges; the mark carries
// the two attributes and nothing about painting order, which is the
// stylesheet's.
export const LABEL_HALO = "var(--ground)";
export const LABEL_HALO_WIDTH = 2;

// pointMarks is one node on the map: its disc, its label up and to the
// right, and the ambiguity mark when the envelope flagged the node.
export function pointMarks(pin) {
  const { key, label, x, y, fill, placed, ambiguous } = pin;
  const marks = [
    {
      kind: MARK_DISC,
      key,
      class: placed ? CLASS_POINT : CLASS_POINT_UNPLACED,
      cx: x,
      cy: y,
      r: POINT_RADIUS,
      fill: placed ? (typeof fill === "string" && fill !== "" ? fill : POINT_FILL) : UNFILLED,
      stroke: placed ? NODE_STROKE : "var(--line-strong)",
      strokeWidth: NODE_STROKE_WIDTH,
    },
    ...haloLabelMarks(
      { x: x + POINT_RADIUS + POINT_LABEL_GAP, y: y - POINT_RADIUS - POINT_LABEL_GAP },
      label,
      { key, class: CLASS_POINT_LABEL, anchor: "start", baseline: "auto" },
    ),
  ];
  if (ambiguous) {
    marks.push({
      kind: MARK_DISC,
      key,
      class: CLASS_AMBIGUOUS,
      cx: x + POINT_RADIUS,
      cy: y - POINT_RADIUS,
      r: AMBIGUOUS_RADIUS,
      fill: AMBIGUOUS_FILL,
    });
  }
  return marks;
}

// haloLabelMarks is a label that has to be legible over something this
// interface did not choose.
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

// --- A mark that occupies a range ------------------------------------

export const CLASS_SPAN = "span";
export const CLASS_SPAN_LABEL = "span-label";
export const SPAN_HEIGHT = 2 * POINT_RADIUS;
export const SPAN_LABEL_GAP = 5;

// spanMarks is one bar, from `x1` to `x2`, centred on `y`.
export function spanMarks(span) {
  const { key, label, x1, x2, y, fill } = span;
  const left = Math.min(x1, x2);
  const width = Math.abs(x2 - x1);
  return [
    {
      kind: MARK_RECT,
      key,
      class: CLASS_SPAN,
      x: left,
      y: y - SPAN_HEIGHT / 2,
      w: width,
      h: SPAN_HEIGHT,
      radius: 1,
      fill: typeof fill === "string" && fill !== "" ? fill : POINT_FILL,
    },
    ...haloLabelMarks(
      { x: left + width + SPAN_LABEL_GAP, y },
      label,
      { key, class: CLASS_SPAN_LABEL, anchor: "start", baseline: "middle" },
    ),
  ];
}

// The caret a span whose end is before its start wears.
export const CLASS_CARET = "caret";
export const CARET_GLYPH = "><";

export function caretMarks(at, key) {
  return [
    {
      kind: MARK_LABEL,
      key,
      class: CLASS_CARET,
      x: at.x,
      y: at.y,
      text: CARET_GLYPH,
      fill: "var(--line-strong)",
      size: LABEL_SIZE,
      anchor: "middle",
      baseline: "middle",
    },
  ];
}
