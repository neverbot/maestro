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
  LAYER_IMAGE,
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
:host {
  display: block;
  /* **The drawing's own enclosure.** Everything below is
     position:absolute inset:0 and therefore needs a positioned box with
     a real height to fill; a host with no height renders six hundred
     kilobytes of correct SVG that nobody can see. It lives here, on the
     component that needs it, and not on the frame that slots it in: the
     frame slots the *table* renderer into the same place, and a table
     forced into a 70vh box loses the sticky headers §4.7 asks it for and
     spills a thousand rows past its own frame. Both halves found by
     mounting a view and a table (Task 15). Viewport-relative because a
     diagram is a thing you look *at*; the floor is what keeps it usable
     in a short window. */
  position: relative;
  height: 70vh;
  min-height: 22rem;
}
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
/* A haloed label — render/marks.js's haloLabelMarks, which is how a name
   stays legible over a designer's own image on a \`map\` (spec §4.6) —
   carries the halo as a stroke. Painted in the default order a stroke
   goes *over* the glyphs and thickens them into a blur; \`paint-order\`
   puts it under, which is the whole difference between a halo and a
   smear. It is declared for every label rather than for the map's,
   because a label with no stroke is unaffected by it and a second class
   would be a second place for this to be forgotten. */
svg.surface text { paint-order: stroke; }
/* The arrangement menu: the three things about saving a position that
   are otherwise invisible, beside the controls they are about. */
.menu { max-width: 40ch; margin: 0.75rem; padding: 0.75rem; color: var(--ink); font-family: var(--sans); }
.menu button { font: inherit; margin-right: 0.4rem; }
.menu .note { margin: 0.5rem 0 0; color: var(--muted); font-size: 0.85em; }
.menu .band { margin: 0.5rem 0 0; color: var(--danger); font-size: 0.9em; }
/* In flight. A reduced-opacity treatment and not a spinner: what is
   waiting for the server here is the arrangement a designer is still
   looking at, and a spinner is a thing to look at instead of it. It is
   declared once and worn by anything with a write outstanding. */
.pending { opacity: 0.55; }
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

  const surfaceHost = doc.createElement("div");
  surfaceHost.setAttribute("class", CLASS_SURFACE_HOST);
  root.appendChild(surfaceHost);

  const panels = doc.createElement("div");
  panels.setAttribute("class", CLASS_PANELS);
  panels.appendChild(doc.createElement("slot"));
  root.appendChild(panels);

  return { root, surfaceHost, panels };
}

// adoptCanvasStyles puts CANVAS_CSS on a shadow root as a **constructible
// stylesheet**, and there is no `<style>` element fallback on purpose.
//
// A `<style>` element built in script is *inline style* to a
// Content-Security-Policy, and this product's policy is `default-src
// 'self'` with no style hash and no 'unsafe-inline'. A browser therefore
// refuses to apply it — `style.sheet` comes back null — and refuses in
// the same silent way it refuses an unhashed import map: the element is
// in the shadow tree, its textContent is intact, `querySelector` finds
// it, and the only symptom is that none of the rules are in effect. That
// is what this component shipped until Task 15 mounted a view and read
// `shadowRoot.querySelector("style").sheet` in a real browser: null, the
// host laid out `static` rather than `absolute`, and the surface drawn
// at an SVG's default 300x150 instead of filling the frame.
//
// `adoptedStyleSheets` is not inline style and no policy governs it,
// which is also why every Lit component beside this one was unaffected —
// `static styles` takes exactly this path. A fallback that appended a
// `<style>` when constructible sheets are missing would be a mechanism
// nothing reads: under this policy it cannot work, so a shadow root
// without `adoptedStyleSheets` is left unstyled and honest about it.
export function adoptCanvasStyles(shadow) {
  if (!shadow || !Array.isArray(shadow.adoptedStyleSheets)) return null;
  if (typeof CSSStyleSheet !== "function") return null;
  let sheet;
  try {
    sheet = new CSSStyleSheet();
    sheet.replaceSync(CANVAS_CSS);
  } catch {
    return null;
  }
  shadow.adoptedStyleSheets = [...shadow.adoptedStyleSheets, sheet];
  return sheet;
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
    // The arrangement menu's element, when one is showing. Held so a
    // redraw replaces it rather than stacking a second copy of the same
    // three sentences over the first.
    this.menuElement = null;
    this.shell = buildShell(this.doc);
    const shadow = this.attachShadow({ mode: "open" });
    this.sheet = adoptCanvasStyles(shadow);
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
    for (const { record, ends } of this.drag.reshaped) shiftBy(record.element, ends, dx, dy);
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
      shiftBy(record.element, MARK_ORIGINS[record.kind] || [], dx, dy);
      this.tree.layers.get(record.layer).appendChild(record.element);
    }
    this.drag = null;
    return { keys: [...dragged], dx, dy };
  }

  // nudgeNodes moves nodes with no pointer involved: the keyboard path,
  // the residual a snap adds after a drop, and the revert a refused write
  // performs. It writes coordinates in place rather than going through the
  // drag layer, because there is no gesture here to detach a body for and
  // a round trip through beginDrag/endDrag would reorder the tree to move
  // a node one pixel.
  nudgeNodes(keys, dx, dy) {
    if (!this.tree) return null;
    const moving = new Set(Array.isArray(keys) ? keys : [keys]);
    const touched = [];
    const missing = [];
    for (const key of moving) {
      const entry = this.tree.nodes.get(key);
      if (!entry) {
        missing.push(key);
        continue;
      }
      for (const record of [...entry.shapes, ...entry.labels]) {
        shiftBy(record.element, MARK_ORIGINS[record.kind] || [], dx, dy);
        touched.push(record);
      }
    }
    const seen = new Set();
    for (const key of moving) {
      for (const record of this.tree.edgesByNode.get(key) || []) {
        if (seen.has(record)) continue;
        seen.add(record);
        shiftBy(record.element, endsOf(record, moving), dx, dy);
        touched.push(record);
      }
    }
    return { keys: [...moving], dx, dy, touched: touched.length, missing };
  }

  // adjustGround moves and scales the background alone.
  //
  // The nodes hold still, which is the whole of *adjust ground* being a
  // mode: a designer aligning an image to a graph is moving one of the
  // two things, and an interface that moved both would be asking them to
  // do it by feel. It reaches the image layer directly rather than
  // through the drag layer, because the ground is not a body of marks
  // with incident edges — it is one element, and there is nothing to
  // detach it from.
  adjustGround(change) {
    if (!this.tree) return null;
    const move = change && typeof change === "object" ? change : {};
    const dx = Number.isFinite(move.dx) ? move.dx : 0;
    const dy = Number.isFinite(move.dy) ? move.dy : 0;
    const factor = Number.isFinite(move.factor) && move.factor > 0 ? move.factor : 1;
    const images = [...this.tree.layers.get(LAYER_IMAGE).childNodes];
    for (const element of images) {
      element.setAttribute("x", String(numberOf(element, "x") + dx));
      element.setAttribute("y", String(numberOf(element, "y") + dy));
      if (factor === 1) continue;
      element.setAttribute("width", String(numberOf(element, "width") * factor));
      element.setAttribute("height", String(numberOf(element, "height") * factor));
    }
    return { touched: images.length, dx, dy, factor };
  }

  // showArrangement puts the menu into the floating panel slot: the notes
  // as text, the actions as buttons, and the server's own sentence when
  // there is one. It writes no words of its own — every string comes from
  // arrangementMenu above or from the refusal the client carried across.
  showArrangement(arrangement) {
    const panels = this.shell.panels;
    if (this.menuElement) {
      panels.removeChild(this.menuElement);
      this.menuElement = null;
    }
    if (!arrangement) return null;
    const menu = arrangement.menu();
    const root = this.doc.createElement("div");
    root.setAttribute("class", arrangement.pending ? CLASS_MENU + " " + CLASS_PENDING : CLASS_MENU);
    for (const action of menu.actions) {
      const button = this.doc.createElement("button");
      button.setAttribute("type", "button");
      button.setAttribute("data-action", action.id);
      button.textContent = action.label;
      root.appendChild(button);
    }
    for (const note of menu.notes) {
      const paragraph = this.doc.createElement("p");
      paragraph.setAttribute("class", CLASS_NOTE);
      paragraph.textContent = note;
      root.appendChild(paragraph);
    }
    if (arrangement.band) {
      const band = this.doc.createElement("p");
      band.setAttribute("class", CLASS_BAND);
      band.textContent = arrangement.band;
      root.appendChild(band);
    }
    panels.appendChild(root);
    this.menuElement = root;
    return root;
  }
}

// shiftBy moves one element by a delta over the coordinate pairs it was
// given — a mark's own origins, or an edge's moving endpoints.
function shiftBy(element, pairs, dx, dy) {
  for (const [ax, ay] of pairs) {
    element.setAttribute(ax, String(numberOf(element, ax) + (Number.isFinite(dx) ? dx : 0)));
    element.setAttribute(ay, String(numberOf(element, ay) + (Number.isFinite(dy) ? dy : 0)));
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

// --- The write on drop -----------------------------------------------
//
// The first of Task 14's two writes, and the reason this component
// gained anything after the drag layer above.
//
// **A drag is one intention, so it is one write.** Nothing is sent while
// the pointer is down: a write per frame is sixty calls, sixty rows and
// sixty events fanned out to every other browser looking at this view,
// for one gesture whose only durable fact is where the node ended up. A
// multi-selection is written by the same rule — one call carrying every
// node — and the arrow keys are the keyboard path *to that same call*
// rather than a second write path, because two write paths are two
// chances to differ.
//
// **A dragged node is written pinned.** That is what a drag means: a
// human put it there. Unpinning is a separate, explicit act, and in
// `manual` mode it has no visible effect until the position is cleared
// too — so the menu offers the clear beside it and says so, because a
// control that appears to do nothing is worse than one that is absent.
//
// **Coordinates are the game's, not the screen's.** A pointer delta is
// divided by the zoom in `worldDelta` before it reaches anything, and
// the model this writes from is the composition's own coordinates —
// which is what the layout composed against. A canvas that wrote screen
// pixels would drift every saved arrangement by whatever viewport the
// designer happened to have.
//
// **A refused write reverts.** The nodes go back to where they were and
// the server's own sentence is banded. The alternative is the one
// outcome a shared design tool may not produce: a screen that disagrees
// with the database indefinitely, with nothing on it saying so.
//
// **Concurrency is last-writer-wins, and the menu says so.**
// `set_positions` carries no `expected_version` and does not advance the
// view's version, which is the right trade for a coordinate and the one
// place in this product where a write silently loses. The one write here
// that *is* version-checked is the structural one — switching a view out
// of `auto` — because that changes what everybody else sees.

import { addressOf } from "../address.js";
import { MODE_MIXED, normaliseMode, readsPositions, snapsPositions } from "../positions.js";

// The role that may look and not write. It is the server's own spelling
// (internal/web/api_projects.go), and it is read here for the page's
// words only: every refusal is still the server's, made again on the
// next request. A page that offered a viewer the one button the server
// will not honour is worse than one that explains.
export const ROLE_VIEWER = "viewer";

// The nudge an arrow key applies when there is no grid: one pixel of the
// game's own coordinate space (spec §6.1). With a grid it is one cell,
// which is what makes the keyboard the same gesture as the mouse.
export const NUDGE_DEFAULT = 1;

// The class an element in flight wears. A reduced-opacity treatment and
// deliberately not a spinner: a spinner is a thing to look at instead of
// the work, and what is in flight here is the arrangement a designer is
// still looking at.
export const CLASS_PENDING = "pending";
export const CLASS_MENU = "menu";
export const CLASS_NOTE = "note";
export const CLASS_BAND = "band";

// The identifiers of the menu's actions, so a caller asks for one by
// identity rather than by matching a label it also renders.
export const ACTION_SWITCH_MODE = "switch-mode";
export const ACTION_UNPIN = "unpin";
export const ACTION_CLEAR = "clear";
export const ACTION_UNDO = "undo";

// The words. They name *controls* and state what a control does to the
// database, which is mst-view-frame.js's own division: that component
// speaks only the model's words because every sentence there describes
// the answer, and RUN_ANYWAY_LABEL sits in the component beside it
// because it names a button. Everything below is the same kind of thing
// — what this control will do, and what it will not.
//
// Three of them exist because the behaviour they describe is invisible.
// Unpinning in `manual` mode changes no pixel until the position is
// cleared; undo has one level and no server behind it; a position write
// loses silently to a concurrent one. Each is a thing a designer would
// otherwise learn by watching nothing happen.
export const REASON_AUTO_NO_DRAG =
  "This view lays itself out automatically, so nothing on it can be dragged: " +
  "a position saved here would never be read back.";
export const LABEL_SWITCH_MODE = "Switch this view to mixed layout";
export const LABEL_UNPIN = "Unpin";
export const NOTE_UNPIN_NEEDS_CLEAR =
  "Unpinning on its own changes nothing you can see in manual layout: the saved " +
  "position is still read. Clear the position too.";
export const LABEL_CLEAR = "Clear the saved position";
export const LABEL_UNDO = "Undo the last move";
export const NOTE_UNDO_BOUND =
  "Undo goes back one move, in this browser tab only: saved positions keep no " +
  "history and there is no undo on the server.";
export const NOTE_LAST_WRITER_WINS =
  "Positions are last-writer-wins. If somebody else moves the same node while " +
  "you are moving it, the later drop wins and nothing warns either of you.";

// snapTo lands one coordinate on the grid. A snap of 0 — the catalogue's
// own "draw no grid" — is the identity, so "there is no grid" and "the
// grid is one unit" stay two different answers.
export function snapTo(value, snap) {
  if (!Number.isFinite(value)) return 0;
  if (!Number.isFinite(snap) || snap <= 0) return value;
  return Math.round(value / snap) * snap;
}

// worldDelta turns a pointer delta into a game delta.
//
// **This function is what stops a screen coordinate ever being written.**
// A drag ends inside a pan-and-zoom transform; the number of pixels the
// pointer travelled is a fact about this browser's viewport and about
// nothing else. Divided by the zoom it becomes a distance in the space
// the layout composed against, which is the space `view_positions`
// holds. The pan does not appear here at all, and correctly so: a
// translation cancels out of a difference.
export function worldDelta(dx, dy, zoom) {
  const k = Number.isFinite(zoom) && zoom > 0 ? zoom : 1;
  return {
    dx: (Number.isFinite(dx) ? dx : 0) / k,
    dy: (Number.isFinite(dy) ? dy : 0) / k,
  };
}

// arrangementMenu is the menu as data: what it says and what it offers.
//
// Pure, so the whole of "a viewer gets the sentence without the button"
// is one assertion over a returned object rather than a walk of a DOM
// that might merely have failed to render.
export function arrangementMenu({ mode, role, canUndo } = {}) {
  const mayWrite = role !== ROLE_VIEWER;
  if (!readsPositions(mode)) {
    return {
      notes: [REASON_AUTO_NO_DRAG],
      actions: mayWrite ? [{ id: ACTION_SWITCH_MODE, label: LABEL_SWITCH_MODE }] : [],
    };
  }
  const notes = [NOTE_UNPIN_NEEDS_CLEAR, NOTE_UNDO_BOUND, NOTE_LAST_WRITER_WINS];
  if (!mayWrite) return { notes, actions: [] };
  const actions = [
    { id: ACTION_UNPIN, label: LABEL_UNPIN },
    { id: ACTION_CLEAR, label: LABEL_CLEAR },
  ];
  if (canUndo) actions.push({ id: ACTION_UNDO, label: LABEL_UNDO });
  return { notes, actions };
}

// Arrangement is the drag, the selection, the keyboard and the write.
//
// It holds the *model* — one record per node, in game coordinates — and
// the canvas holds the drawing. Every path through it ends in `commit`,
// which is the single place a position is sent, so "one drag is one
// write" and "the arrow keys write the same shape" are properties of the
// construction rather than of two implementations agreeing.
export class Arrangement {
  constructor({ canvas, client, viewKey, row, mode, snap, role, nodes } = {}) {
    this.canvas = canvas;
    this.client = client;
    this.viewKey = viewKey;
    // The saved row, for the one version-checked write: switching out of
    // `auto` resends the row it read, carrying the version it read it at.
    this.row = row || null;
    this.mode = normaliseMode(mode !== undefined ? mode : row && row.layout_mode);
    this.snap = Number.isFinite(snap) && snap > 0 ? snap : 0;
    this.role = typeof role === "string" ? role : "";
    this.nodes = new Map();
    for (const node of Array.isArray(nodes) ? nodes : []) {
      const address = typeof node.address === "string" ? node.address : addressOf(node);
      this.nodes.set(address, {
        address,
        type: String(node.type),
        key: String(node.key),
        x: Number.isFinite(node.x) ? node.x : 0,
        y: Number.isFinite(node.y) ? node.y : 0,
      });
    }
    this.selection = new Set();
    // One level, per tab, and it is deliberately not a stack: there is no
    // history behind `view_positions` and no server-side undo to reach
    // for, so a deep stack here would be a promise this product cannot
    // keep past a reload. NOTE_UNDO_BOUND says so where a designer reads.
    this.undoStep = null;
    this.band = null;
    this.pending = false;
    this.drag = null;
  }

  // draggable is compose.js's `draggable` asked of the mode directly,
  // because the canvas has to answer it before there is a composition.
  get draggable() {
    return readsPositions(this.mode);
  }

  get mayWrite() {
    return this.role !== ROLE_VIEWER;
  }

  // gridStep is the snap that actually applies. `mixed` re-fits the whole
  // picture, so a coordinate landed on the grid at write time is not on
  // the grid when it is drawn back — see positions.js's snapsPositions.
  get gridStep() {
    return snapsPositions(this.mode) && this.snap > 0 ? this.snap : 0;
  }

  menu() {
    return arrangementMenu({ mode: this.mode, role: this.role, canUndo: this.undoStep !== null });
  }

  positionOf(address) {
    const node = this.nodes.get(address);
    return node ? { x: node.x, y: node.y } : null;
  }

  // select replaces the selection, or adds to it under shift.
  select(address, options = {}) {
    if (!this.nodes.has(address)) return this.selected();
    if (!options.shift) this.selection.clear();
    else if (this.selection.has(address)) {
      this.selection.delete(address);
      return this.selected();
    }
    this.selection.add(address);
    return this.selected();
  }

  // marquee selects every node inside a rectangle. The rectangle is in
  // **world** units, for worldDelta's reason: a marquee drawn on the
  // screen is converted once, by the caller that owns the transform, and
  // nothing downstream of that ever sees a pixel.
  marquee(rect, options = {}) {
    if (!options.shift) this.selection.clear();
    const left = Math.min(rect.x1, rect.x2);
    const right = Math.max(rect.x1, rect.x2);
    const top = Math.min(rect.y1, rect.y2);
    const bottom = Math.max(rect.y1, rect.y2);
    for (const node of this.nodes.values()) {
      if (node.x < left || node.x > right || node.y < top || node.y > bottom) continue;
      this.selection.add(node.address);
    }
    return this.selected();
  }

  selected() {
    return [...this.selection];
  }

  // pointerDown starts a drag, or refuses one.
  //
  // In `auto` it returns null and writes nothing, which is the whole of
  // "dragging is disabled there": the menu carries the sentence saying
  // why, because a silently inert canvas is the write-a-row-nothing-reads
  // defect wearing a mouse.
  pointerDown(address, point) {
    if (!this.draggable || !this.mayWrite) return null;
    if (!this.selection.has(address)) this.select(address);
    this.drag = { at: { x: point.x, y: point.y }, dx: 0, dy: 0 };
    // The client defers a re-read while this is true: an event arriving
    // mid-drag must not swap the coordinates under the pointer.
    this.client.setDragging(true);
    return this.canvas.beginDrag(this.selected());
  }

  // pointerMove moves the picture and writes nothing at all.
  pointerMove(point) {
    if (!this.drag) return null;
    const moved = worldDelta(point.x - this.drag.at.x, point.y - this.drag.at.y, this.canvas.view.k);
    this.drag.at = { x: point.x, y: point.y };
    this.drag.dx += moved.dx;
    this.drag.dy += moved.dy;
    return this.canvas.dragBy(moved.dx, moved.dy);
  }

  // pointerUp is the drop, and the drop is the write.
  async pointerUp() {
    if (!this.drag) return null;
    const { dx, dy } = this.drag;
    this.drag = null;
    const drop = this.canvas.endDrag();
    const keys = drop ? drop.keys : this.selected();
    const targets = this.snapped(keys.map((address) => {
      const node = this.nodes.get(address);
      return { address, x: node.x + dx, y: node.y + dy };
    }));
    try {
      return await this.commit(targets, { drawn: { dx, dy } });
    } finally {
      this.client.setDragging(false);
    }
  }

  // nudge is the keyboard path, and it goes through the same commit.
  // One grid unit, or one pixel where there is no grid.
  async nudge(ux, uy) {
    if (!this.draggable) return null;
    const step = this.gridStep > 0 ? this.gridStep : NUDGE_DEFAULT;
    const targets = this.snapped(this.selected().map((address) => {
      const node = this.nodes.get(address);
      return { address, x: node.x + ux * step, y: node.y + uy * step };
    }));
    return this.commit(targets);
  }

  // undo puts the pre-drag coordinates back, once. It commits without
  // remembering, so the second Ctrl-Z has nothing to do — which is the
  // bound NOTE_UNDO_BOUND states rather than leaving to be discovered.
  async undo() {
    const step = this.undoStep;
    if (!step) return null;
    this.undoStep = null;
    return this.commit(step, { remember: false });
  }

  // unpin releases the selection without moving it. A separate explicit
  // act, and the only write in this file that does not send `true`.
  async unpin() {
    const rows = this.selected().map((address) => {
      const node = this.nodes.get(address);
      return { type: node.type, key: node.key, x: node.x, y: node.y, pinned: false };
    });
    if (rows.length === 0 || !this.mayWrite) return null;
    return this.settle(() => this.client.writePositions(this.viewKey, rows));
  }

  // clearPositions drops the selection's saved coordinates. It always
  // names them: the client refuses an empty list, because an unnamed
  // clear on that route means the whole view.
  async clearPositions() {
    const rows = this.selected().map((address) => this.nodes.get(address));
    if (rows.length === 0 || !this.mayWrite) return null;
    return this.settle(() => this.client.clearPositions(this.viewKey, rows));
  }

  // switchToMixed is the one structural write here, and the one that
  // carries a version: it changes what every other browser sees, so it
  // refuses rather than overwrites a change made in between.
  async switchToMixed() {
    if (!this.row || !this.mayWrite) return null;
    const answer = await this.settle(() => this.client.upsertView(this.row, { layoutMode: MODE_MIXED }));
    if (answer && answer.ok) this.mode = MODE_MIXED;
    return answer;
  }

  // snapped lands each target on the grid, in the one mode that reads
  // one. Per node rather than per gesture: the grid is where a node
  // *lands*, and snapping the delta instead would leave a node that was
  // never on the grid permanently off it.
  snapped(targets) {
    const step = this.gridStep;
    if (step <= 0) return targets;
    return targets.map((t) => ({ address: t.address, x: snapTo(t.x, step), y: snapTo(t.y, step) }));
  }

  // commit is the only place a position is written.
  //
  // `drawn` is what the canvas has *already* moved — the drag layer bakes
  // its offset into the coordinates on drop — so `place` writes the
  // residual and never the whole delta twice. On every other path it is
  // zero, which is what lets the keyboard and the mouse share this.
  async commit(targets, options = {}) {
    if (!this.draggable || !this.mayWrite || targets.length === 0) return null;
    const before = targets.map((t) => {
      const node = this.nodes.get(t.address);
      return { address: t.address, x: node.x, y: node.y };
    });
    this.place(targets, options.drawn);
    if (options.remember !== false) this.undoStep = before;
    const rows = targets.map((t) => {
      const node = this.nodes.get(t.address);
      // pinned: true, on every entry, because that is what a drag means.
      return { type: node.type, key: node.key, x: node.x, y: node.y, pinned: true };
    });
    const answer = await this.settle(() => this.client.writePositions(this.viewKey, rows));
    if (!answer.ok) {
      // The nodes go back. A screen that disagrees with the database and
      // says nothing is the one outcome this design may not produce.
      this.place(before);
      this.undoStep = null;
    }
    return answer;
  }

  // settle runs one call with the in-flight treatment around it and the
  // server's own sentence out of it. It composes no words: `message` is
  // carried across from the refusal exactly as the client received it.
  async settle(call) {
    this.band = null;
    this.pending = true;
    let answer;
    try {
      answer = await call();
    } finally {
      this.pending = false;
    }
    if (answer && !answer.ok) this.band = answer.error.message;
    return answer;
  }

  // place moves the drawing to where the model says, then moves the
  // model. Nothing else in this file touches a coordinate.
  place(targets, drawn) {
    const dx = drawn && Number.isFinite(drawn.dx) ? drawn.dx : 0;
    const dy = drawn && Number.isFinite(drawn.dy) ? drawn.dy : 0;
    for (const target of targets) {
      const node = this.nodes.get(target.address);
      if (!node) continue;
      const rx = target.x - node.x - dx;
      const ry = target.y - node.y - dy;
      if (rx !== 0 || ry !== 0) this.canvas.nudgeNodes([target.address], rx, ry);
      node.x = target.x;
      node.y = target.y;
    }
  }
}
