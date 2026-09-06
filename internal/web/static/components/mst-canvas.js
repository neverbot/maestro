// The canvas: pan, zoom, the SVG emitter and the drag layer.
//
// **The emitter is deliberately dumb.** One scene mark becomes one SVG
// element, with the attributes render/scene.js's MARK_ATTRIBUTES names
// and nothing else. It has no opinion about colour, about absence or
// about truncation: those were decided in palette.js (Task 1) and in
// scene.js's banner stack (Task 4), and they travel in the mark. That
// separation is what makes the six renderers testable without a browser
// and this emitter testable without a renderer, and it is what the whole
// sub-project's verification rests on. If something here wants to decide
// what a mark means, the decision belongs in the mark.
//
// Dumb is not the same as a passthrough. A passthrough is how `onload`,
// `style` and `href` arrive on an element built from data, so the
// contract is an allowlist per kind: a field the map does not name does
// not reach the DOM, and the emitter is judged by that map rather than
// by the caller's discipline.
//
// **The hostile name, in a different context.** The text twin's answer
// (Task 5) was that Lit escapes and our job is placement, asserted by a
// scanner that finds where each value is bound. That argument does not
// transfer here, and this file does not reuse it: there is no framework
// on this path at all. `createElementNS`, `setAttribute` and
// `textContent` never parse markup, so nothing is escaped because
// nothing is ever re-parsed — the property is *no parsing*, not *correct
// escaping*. What SVG adds that HTML text position did not have is three
// hazards of its own, and each is closed by construction:
//
//   1. Element and attribute **names** never come from data. Both are
//      looked up in scene.js's frozen maps by the mark's kind; a mark
//      cannot name an element and cannot invent an attribute.
//   2. `<foreignObject>` re-enters the HTML parser and `<script>`,
//      `<use>` and `<a>` bring script or navigation into a picture.
//      None is in MARK_ELEMENTS, and internal/web/static_canvas_test.go
//      fails on the day one is spelled in this file.
//   3. `href` is the one attribute a browser *resolves* rather than
//      draws. scene.js's isDrawableHref admits same-origin absolute
//      paths and nothing else, and the emitter drops what it refuses.
//
// A game's own words reach the DOM through `textContent` on a `<text>`
// element and through no other path — textContent assigns character
// data, so markup in a node's name is characters in a label. The
// harness asserts it against a node named with a script tag, and
// asserts the complement: that no attribute value anywhere in the
// emitted tree contains a `<`.
//
// **Paint order is a correctness property.** SVG paints in document
// order, so the five layers are five sibling groups in LAYER_ORDER and
// a mark's layer decides which one it lands in — not the order the
// renderer happened to push it. A label under its own node is a label
// nobody can read; a dragged node under the graph it is crossing is a
// drag a designer loses.
//
// **The drag layer is the one performance-shaped decision here** (spec
// §8.2: a drag stays at 60fps at 1000 nodes). On `beginDrag` the
// dragged nodes, their labels and the edges whose *both* endpoints are
// dragged move onto a detached layer; a move then writes **one**
// transform for the whole body, plus one endpoint pair for each edge
// that leaves the selection — an edge with one end moving cannot be
// translated, it has to be reshaped. Nothing else in the tree is
// touched, and the harness counts exactly that against a 200-node
// fixture, where a full re-render is unmissable.
//
// This component is not a LitElement, and that is deliberate: it holds
// no declarative template at all. Lit's contract is "describe the tree
// and it will be reconciled", which is precisely what the drag budget
// above forbids, and a component that re-rendered its tree to move four
// nodes would be a correct Lit component and a broken canvas.

import {
  COMMON_ATTRIBUTES,
  DEFAULT_LAYER,
  LAYER_DRAG,
  LAYER_ORDER,
  MARK_ATTRIBUTES,
  MARK_ELEMENTS,
  MARK_LABEL,
  MARK_LINE,
  MARK_ORIGINS,
  PLACEABLE_LAYERS,
  SVG_NS,
  isDrawableHref,
} from "../render/scene.js";

// --- Zoom, pan, and the label band -----------------------------------

// The band labels are allowed to scale in (design spec §4.6). Outside
// it a label keeps the size it had at the edge of the band: at 4× zoom
// a designer wants more of the picture, not bigger text, and at 0.25×
// they want to still be able to read a name.
export const LABEL_SCALE_MIN = 0.75;
export const LABEL_SCALE_MAX = 1.5;

// The label size a mark gets when it carries none. 11px is the spec's
// number for a map label; it is the fallback and not a policy — a mark
// that says how big its text is wins.
export const DEFAULT_LABEL_SIZE = 11;

// labelScale is the *apparent* factor a label is drawn at, in screen
// terms, for a world zoomed by `zoom`. Inside the band it is the zoom
// itself, which is what "labels scale with zoom" means; outside it, the
// nearer edge.
export function labelScale(zoom) {
  const k = Number.isFinite(zoom) && zoom > 0 ? zoom : 1;
  return Math.min(LABEL_SCALE_MAX, Math.max(LABEL_SCALE_MIN, k));
}

// labelFontSize is what the attribute is set to. Labels live inside the
// world group, so the group's scale multiplies whatever is written
// here; dividing by it is how an apparent size becomes an authored one.
// At 1× it is the identity, which is the control that keeps a mutation
// clamping everything to 1 from passing.
export function labelFontSize(baseSize, zoom) {
  const k = Number.isFinite(zoom) && zoom > 0 ? zoom : 1;
  const size = Number.isFinite(baseSize) && baseSize > 0 ? baseSize : DEFAULT_LABEL_SIZE;
  return (size * labelScale(k)) / k;
}

// transformFor is the one string the whole of pan and zoom produces.
// One string, on one element, because image and nodes are one
// coordinate space and any interface where they can drift apart is a
// broken map (spec §4.6).
export function transformFor(view) {
  const x = Number.isFinite(view && view.x) ? view.x : 0;
  const y = Number.isFinite(view && view.y) ? view.y : 0;
  const k = Number.isFinite(view && view.k) && view.k > 0 ? view.k : 1;
  return `translate(${x} ${y}) scale(${k})`;
}

export function translateFor(dx, dy) {
  const x = Number.isFinite(dx) ? dx : 0;
  const y = Number.isFinite(dy) ? dy : 0;
  return `translate(${x} ${y})`;
}

// --- The classes the shell wears -------------------------------------
//
// The diagram is full-bleed and the chrome floats over it (spec §2.6):
// the 68ch measure `styles.css` sets is for reading surfaces, and a
// diagram inside a centred column of prose width is a diagram nobody can
// use. The classes are exported so the harness asks for them by identity
// rather than by matching a string it also wrote.
export const CLASS_ROOT = "canvas full-bleed";
export const CLASS_SURFACE_HOST = "surface-host";
export const CLASS_SURFACE = "surface";
export const CLASS_WORLD = "world";
export const CLASS_PANELS = "panels floating";
export const CLASS_LAYER_PREFIX = "layer layer-";

// The component's stylesheet, as a string rather than a Lit `css`
// literal, because this component holds no Lit template to attach one
// to. It names tokens and never hex values, so both themes are the
// stylesheet's business (Task 1).
export const CANVAS_CSS = `
:host { display: block; position: absolute; inset: 0; }
.canvas { position: absolute; inset: 0; overflow: hidden; background: var(--ground); }
.canvas.full-bleed { max-width: none; width: 100%; height: 100%; margin: 0; }
.surface-host { position: absolute; inset: 0; }
svg.surface { display: block; width: 100%; height: 100%; touch-action: none; }
.panels { position: absolute; inset: 0; pointer-events: none; }
.panels.floating > * {
  pointer-events: auto;
  background: var(--paper);
  border: 1px solid var(--line);
  border-radius: 4px;
}
.layer-drag { pointer-events: none; }
`;

// --- The emitter -----------------------------------------------------

// emitScene turns a scene into an SVG tree, and returns the index the
// canvas navigates it by.
//
//   root   — the `<svg>`.
//   world  — the one group pan and zoom transform.
//   layers — Map from a layer name to its group, in LAYER_ORDER.
//   nodes  — Map from a mark's key to `{shapes, labels}`; a node's box
//            and its label share the key, which is the entity's address.
//   edges  — every mark carrying a `source` and a `target`, with its
//            element, so the drag layer can find what is incident
//            without ever querying the DOM.
//   labels — every label element with the base size it was authored at,
//            which is what the zoom band re-derives from.
//   skipped — marks of a kind the contract does not name. Returned
//            rather than thrown or silently dropped: a renderer that
//            invented a kind should see the number.
export function emitScene(scene, options = {}) {
  const doc = options.document || globalThis.document;
  const marks = Array.isArray(scene) ? scene : Array.isArray(scene && scene.marks) ? scene.marks : [];

  const root = doc.createElementNS(SVG_NS, "svg");
  root.setAttribute("class", CLASS_SURFACE);
  const world = doc.createElementNS(SVG_NS, "g");
  world.setAttribute("class", CLASS_WORLD);
  root.appendChild(world);

  const layers = new Map();
  for (const name of LAYER_ORDER) {
    const group = doc.createElementNS(SVG_NS, "g");
    group.setAttribute("class", CLASS_LAYER_PREFIX + name);
    world.appendChild(group);
    layers.set(name, group);
  }

  const nodes = new Map();
  const edges = [];
  const edgesByNode = new Map();
  const labels = [];
  const skipped = [];

  for (const mark of marks) {
    const kind = mark && typeof mark === "object" ? mark.kind : null;
    const tag = MARK_ELEMENTS[kind];
    if (!tag) {
      skipped.push(mark);
      continue;
    }
    const element = doc.createElementNS(SVG_NS, tag);
    applyMark(element, mark, kind);
    const layer = PLACEABLE_LAYERS.includes(mark.layer) ? mark.layer : DEFAULT_LAYER[kind];
    layers.get(layer).appendChild(element);

    const record = { mark, kind, element, layer };
    if (kind === MARK_LABEL) {
      labels.push({ element, size: Number.isFinite(mark.size) ? mark.size : DEFAULT_LABEL_SIZE });
    }
    // An edge is a **line** carrying two endpoint keys, and only that.
    // The definition is narrow on purpose: the drag layer reshapes an
    // edge whose one end moved by rewriting that end's coordinate pair,
    // which is a rule `d` or `cx` cannot express, so a kind that cannot
    // be reshaped is a kind that cannot be an edge. A renderer wanting a
    // curved edge adds the kind and that answer in one diff.
    if (kind === MARK_LINE && typeof mark.source === "string" && typeof mark.target === "string") {
      record.source = mark.source;
      record.target = mark.target;
      edges.push(record);
      index(edgesByNode, mark.source, record);
      if (mark.target !== mark.source) index(edgesByNode, mark.target, record);
      continue;
    }
    if (typeof mark.key === "string" && mark.key !== "") {
      const entry = nodes.get(mark.key) || { key: mark.key, shapes: [], labels: [] };
      (kind === MARK_LABEL ? entry.labels : entry.shapes).push(record);
      nodes.set(mark.key, entry);
    }
  }

  return { root, world, layers, nodes, edges, edgesByNode, labels, skipped };
}

function index(map, key, value) {
  const list = map.get(key);
  if (list) list.push(value);
  else map.set(key, [value]);
}

// applyMark is the whole of "one mark becomes one element". Every
// attribute it writes is named by the contract; every field the contract
// does not name is dropped, `href` included unless it is a path this
// instance can serve.
function applyMark(element, mark, kind) {
  const fields = { ...COMMON_ATTRIBUTES, ...MARK_ATTRIBUTES[kind] };
  for (const [field, attribute] of Object.entries(fields)) {
    const value = mark[field];
    if (value === undefined || value === null || value === "") continue;
    if (typeof value === "object" || typeof value === "function") continue;
    if (attribute === "href" && !isDrawableHref(value)) continue;
    element.setAttribute(attribute, String(value));
  }
  // The game's words, as character data. Labels only: a mark of any
  // other kind carrying text is a mark whose text has no place to go,
  // and putting it somewhere would be the emitter having an opinion.
  if (kind === MARK_LABEL) element.textContent = typeof mark.text === "string" ? mark.text : "";
}

// --- The shell -------------------------------------------------------

// buildShell is the chrome around the drawing: the full-bleed root, the
// host the SVG is swapped into, and the floating panel slot, in that
// order — the panels are last so they paint over the picture, which is
// what "floats over the canvas edges" means in a document.
export function buildShell(doc) {
  const root = doc.createElement("div");
  root.setAttribute("class", CLASS_ROOT);

  const style = doc.createElement("style");
  style.textContent = CANVAS_CSS;
  root.appendChild(style);

  const surfaceHost = doc.createElement("div");
  surfaceHost.setAttribute("class", CLASS_SURFACE_HOST);
  root.appendChild(surfaceHost);

  const panels = doc.createElement("div");
  panels.setAttribute("class", CLASS_PANELS);
  panels.appendChild(doc.createElement("slot"));
  root.appendChild(panels);

  return { root, style, surfaceHost, panels };
}

// --- The component ---------------------------------------------------

export class MstCanvas extends HTMLElement {
  constructor(options = {}) {
    super();
    this.doc = options.document || this.ownerDocument || globalThis.document;
    this.view = { x: 0, y: 0, k: 1 };
    this.tree = null;
    this.drag = null;
    // The layout budget's band and its retry action are declared in
    // layout/budget.js in scene.js's vocabulary and are deliberately not
    // in BANNER_ORDER: that stack is built from the envelope, and this
    // band is a statement about *this browser's* last two seconds, which
    // a second designer looking at the same view may never see. Task 6
    // left them without a reader and said the canvas places them; this
    // is the canvas placing them.
    this.banners = [];
    this.retry = null;
    this.shell = buildShell(this.doc);
    const shadow = this.attachShadow({ mode: "open" });
    if (shadow && typeof shadow.appendChild === "function") shadow.appendChild(this.shell.root);
  }

  // draw replaces the picture. It is the whole-tree path, and it is the
  // path a drag deliberately does not take.
  draw(scene) {
    this.tree = emitScene(scene, { document: this.doc });
    const host = this.shell.surfaceHost;
    while (host.childNodes.length > 0) host.removeChild(host.childNodes[host.childNodes.length - 1]);
    host.appendChild(this.tree.root);
    this.applyView();
    return this.tree;
  }

  // setLayoutResult carries budget.js's answer onto the surface that
  // shows it. A result that timed out brings a band and, unless it was
  // already the retry attempt, one action; a result that finished brings
  // neither, and clears whatever the previous attempt left.
  setLayoutResult(result) {
    const answer = result && typeof result === "object" ? result : {};
    this.banners = answer.banner ? [answer.banner] : [];
    this.retry = answer.retry || null;
    return { banners: this.banners, retry: this.retry };
  }

  setView(view) {
    const next = view && typeof view === "object" ? view : {};
    this.view = {
      x: Number.isFinite(next.x) ? next.x : this.view.x,
      y: Number.isFinite(next.y) ? next.y : this.view.y,
      k: Number.isFinite(next.k) && next.k > 0 ? next.k : this.view.k,
    };
    this.applyView();
    return this.view;
  }

  panBy(dx, dy) {
    return this.setView({ x: this.view.x + (dx || 0), y: this.view.y + (dy || 0) });
  }

  zoomTo(k) {
    return this.setView({ k });
  }

  // applyView writes the one transform, and then the label sizes — the
  // only thing in the tree that does *not* simply ride the world group,
  // because a label that scaled with a 4× zoom would be four times too
  // big rather than four times more legible.
  applyView() {
    if (!this.tree) return;
    this.tree.world.setAttribute("transform", transformFor(this.view));
    for (const label of this.tree.labels) {
      label.element.setAttribute("font-size", String(labelFontSize(label.size, this.view.k)));
    }
  }

  // beginDrag detaches the body being moved.
  //
  // Three sets come out of it, and the third is the one that costs
  // anything per frame:
  //
  //   carried  — the dragged nodes' shapes and labels, plus every edge
  //              whose two endpoints are both in the selection. All of
  //              them ride one transform, so a move writes nothing on
  //              any of them.
  //   reshaped — the edges with exactly one endpoint in the selection.
  //              These cannot be translated: one end is standing still.
  //              Each gets its moving endpoint pair rewritten per move.
  //   missing  — keys the scene has no element for, returned rather than
  //              ignored so a caller dragging a shelved node finds out.
  beginDrag(keys) {
    if (!this.tree) return { carried: [], reshaped: [], missing: [] };
    const dragged = new Set(Array.isArray(keys) ? keys : [keys]);
    const carried = [];
    const missing = [];
    const shapes = [];
    const labels = [];
    for (const key of dragged) {
      const entry = this.tree.nodes.get(key);
      if (!entry) {
        missing.push(key);
        continue;
      }
      for (const record of entry.shapes) shapes.push(record);
      for (const record of entry.labels) labels.push(record);
    }

    const seen = new Set();
    const reshaped = [];
    const carriedEdges = [];
    for (const key of dragged) {
      for (const record of this.tree.edgesByNode.get(key) || []) {
        if (seen.has(record)) continue;
        seen.add(record);
        if (dragged.has(record.source) && dragged.has(record.target)) carriedEdges.push(record);
        else reshaped.push({ record, ends: endsOf(record, dragged) });
      }
    }

    // Into the drag layer in paint order: edges under boxes under
    // labels, the same order the layers themselves are in, so a detached
    // body looks like the picture it came out of.
    const layer = this.tree.layers.get(LAYER_DRAG);
    for (const record of carriedEdges) {
      carried.push(record);
      layer.appendChild(record.element);
    }
    for (const record of shapes) {
      carried.push(record);
      layer.appendChild(record.element);
    }
    for (const record of labels) {
      carried.push(record);
      layer.appendChild(record.element);
    }

    this.drag = { dragged, carried, reshaped, missing, dx: 0, dy: 0, layer };
    return { carried, reshaped, missing };
  }

  // dragBy moves the body. `dx`/`dy` are **world** units — the caller
  // divides a pointer delta by the zoom, because the drag layer is
  // inside the world group and shares its scale.
  //
  // What this writes, per move: one transform, and one endpoint pair per
  // edge that leaves the selection. Nothing else.
  dragBy(dx, dy) {
    if (!this.drag) return null;
    this.drag.dx += Number.isFinite(dx) ? dx : 0;
    this.drag.dy += Number.isFinite(dy) ? dy : 0;
    this.drag.layer.setAttribute("transform", translateFor(this.drag.dx, this.drag.dy));
    for (const { record, ends } of this.drag.reshaped) {
      for (const [ax, ay] of ends) {
        record.element.setAttribute(ax, String(numberOf(record.element, ax) + (Number.isFinite(dx) ? dx : 0)));
        record.element.setAttribute(ay, String(numberOf(record.element, ay) + (Number.isFinite(dy) ? dy : 0)));
      }
    }
    return { dx: this.drag.dx, dy: this.drag.dy };
  }

  // endDrag puts the body back where it belongs in the tree and bakes
  // the offset into the coordinates it rode on. It is O(the dragged
  // subtree) and it happens once, on drop — which is also where the
  // *write* happens (spec §6.1), and that write is Task 14's.
  endDrag() {
    if (!this.drag) return null;
    const { dragged, carried, dx, dy, layer } = this.drag;
    layer.removeAttribute("transform");
    for (const record of carried) {
      for (const [ax, ay] of MARK_ORIGINS[record.kind] || []) {
        record.element.setAttribute(ax, String(numberOf(record.element, ax) + dx));
        record.element.setAttribute(ay, String(numberOf(record.element, ay) + dy));
      }
      this.tree.layers.get(record.layer).appendChild(record.element);
    }
    this.drag = null;
    return { keys: [...dragged], dx, dy };
  }
}

// endsOf is which endpoint pair of an edge is moving. An edge with both
// ends in the selection never reaches here — it rides the transform —
// and an edge with neither is not incident and is not in the list.
function endsOf(record, dragged) {
  const ends = [];
  if (dragged.has(record.source)) ends.push(["x1", "y1"]);
  if (dragged.has(record.target)) ends.push(["x2", "y2"]);
  return ends;
}

function numberOf(element, attribute) {
  const raw = element.getAttribute(attribute);
  const value = raw === null ? NaN : Number.parseFloat(raw);
  return Number.isFinite(value) ? value : 0;
}

customElements.define("mst-canvas", MstCanvas);
