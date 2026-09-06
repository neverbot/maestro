// The `map` renderer: the one picture with a ground under it, and the
// one whose coordinates are *required* rather than computed.
//
// A renderer is a **pure function from an envelope to a scene** — the
// rule render/graph.js's header sets out, and everything in it applies
// here: no DOM, no fetch, no colour decision, no SVG. What is new is
// where the geometry comes from.
//
// **It asks the layout engine for nothing**, and the spec is explicit
// about it: §5.1 says "map's unplaced nodes go to the shelf, not through
// the engine". A map's coordinates are either two declared number
// fields — the game's own statement of where a thing is — or the
// positions designers dragged and saved. Running a graph layout for
// either would be inventing an arrangement over one somebody already
// stated. That is why the composed arrangement arrives in `options` and
// not as a positional `layout` argument: a signature that looked like
// `graph`'s would read as a renderer that lays out.
//
// **The two coordinate modes, and what each one's absence looks like.**
//
//   "fields" — `x_field` and `y_field` name declared number fields, and
//     `project.fields` has to carry them. A node whose x *or* y is
//     missing cannot be placed at all: it goes to the **shelf**, never
//     to the origin. `(0,0)` is a place a designer may deliberately have
//     used, and internal/views/execute.go refuses to return "unplaced"
//     as a coordinate for exactly that reason; the shelf is the visual
//     form of the same refusal. The values are used **as they stand** —
//     no normalisation, no fitting to the viewport — because the game's
//     numbers are the map's coordinate space, and a renderer that
//     rescaled them would move everything on the day one node's
//     coordinate changed.
//   "manual" — the coordinates designers dragged. A node with no saved
//     row is on the shelf too, with one exception that is the whole of
//     §4.2's second bullet: when the view *has* a saved arrangement,
//     a node new to it is placed by the client, drawn with a **hollow**
//     anchor, and counted — *"12 new nodes were placed automatically"* —
//     which is what tells a `manual`-mode designer there is arranging to
//     do.
//
// **The first run, which is this product's most impressive feature
// meeting a designer for the first time.** A `manual` map where *no*
// node has a saved position is not the empty state and must not use it:
// the answer is full, the query matched, and the only thing missing is
// the designer's own work. So the background is drawn, every node is on
// the shelf, and the frame says *"Nothing has been placed yet. Drag a
// node from the shelf onto the map."* Routing that through the generic
// *"This view matched nothing"* would tell a designer their query is
// broken at the exact moment it is not.
//
// **Where the sentence goes.** `firstRun` comes back in the scene and is
// not a mark. A sentence drawn into the picture would have a coordinate,
// and a coordinate pans away from the reader on the first drag; the
// frame owns prose (Task 4) and this is prose.
//
// **What it draws when the answer is not a clean one:**
//
//   a node with no coordinate — a labelled chip on the shelf, dashed
//     (render/marks.js's one spelling for "something this box needs is
//     not here"), counted in a band, and **no mark anywhere at (0,0)**.
//   a background that is gone — the plain ground, every coordinate
//     untouched, and one line in the frame. `background_asset_id` is
//     ON DELETE SET NULL, so this is an ordinary transition between two
//     runs rather than a fault.
//   an edge that leaves the picture — a stub, exactly as `graph` draws
//     one, counted in the footer.
//   a relation from an entity to itself — counted, not drawn.
//   a truncated answer — what an untruncated one draws.

import { addressOf } from "../address.js";
import { labelFor } from "../palette.js";
import { SOURCE_STORED, storedFrom } from "../positions.js";
import { joinEdges } from "./scene.js";
import {
  BAND_CAPTION_GAP,
  CHIP_HEIGHT,
  POINT_RADIUS,
  backgroundMarks,
  bandMarks,
  boundsOf,
  chipMarks,
  chipWidth,
  edgeMarks,
  gridMarks,
  pointMarks,
  stubMarks,
} from "./marks.js";
import { CONTROL_ENUM, CONTROL_NUMBER, CONTROL_NUMBER_FIELD, control } from "./controls.js";

// The name the catalogue holds, and this module's.
export const RENDERER = "map";

// The renderer_params keys, spelled as the server spells them.
export const PARAM_COORDINATE_SOURCE = "coordinate_source";
export const PARAM_X_FIELD = "x_field";
export const PARAM_Y_FIELD = "y_field";
export const PARAM_SNAP = "snap";

// The admitted values, in the catalogue's own spellings, and its own
// default: `coordinate_source` unset is "manual", which is what
// internal/views/renderers.go's Requires assumes when it refuses
// `x_field` in that mode.
export const COORDINATES_MANUAL = "manual";
export const COORDINATES_FIELDS = "fields";
export const COORDINATE_SOURCES = [COORDINATES_MANUAL, COORDINATES_FIELDS];

// The sentence a fresh manual map says. It is the product's first-run
// experience for its best feature, so it is a constant a test can assert
// by identity rather than a string composed at a call site.
export const FIRST_RUN_TEXT =
  "Nothing has been placed yet. Drag a node from the shelf onto the map.";

// The grid `snap` draws appears at 1× zoom and above (§4.6). Below it
// the lines are closer together than they are wide, which is a texture
// rather than a grid — and a designer who has zoomed out is looking at
// the whole map, not aiming at a cell.
export const GRID_MIN_ZOOM = 1;

// The shelf: a strip along the bottom edge, below everything drawn.
export const SHELF_GAP = 28;
export const SHELF_CHIP_GAP = 8;

// shelfCaption names the strip and counts it, which is §4.2's own
// instruction ("with a count and a banner"). The band says it in a
// sentence; this says it where the chips are.
export function shelfCaption(count) {
  return `not placed (${count})`;
}

// The controls. Each tooltip says what the knob does to the **picture**,
// which is the half internal/views/renderers.go deliberately does not
// carry (render/controls.js's header has the argument).
//
// `coordinate_source`'s is the one that matters most here, because the
// two modes are two different products: one reads what the *game*
// declares and cannot be dragged, the other reads what a *designer*
// dragged and is the reason this renderer has a shelf at all.
export const CONTROLS = [
  control(
    PARAM_COORDINATE_SOURCE,
    CONTROL_ENUM,
    "Where each node's position comes from. \"manual\" reads the coordinates " +
      "designers dragged and saved, and a node nobody has placed waits on " +
      "the shelf. \"fields\" reads two numbers the game itself declares, and " +
      "nothing on the map can be dragged.",
    COORDINATE_SOURCES,
  ),
  control(
    PARAM_X_FIELD,
    CONTROL_NUMBER_FIELD,
    "The declared number field holding each node's horizontal coordinate, " +
      "in \"fields\" mode. A node missing it — or missing the vertical one — " +
      "goes to the shelf rather than to the origin, because (0,0) is a place " +
      "somebody may have meant.",
  ),
  control(
    PARAM_Y_FIELD,
    CONTROL_NUMBER_FIELD,
    "The declared number field holding each node's vertical coordinate, in " +
      "\"fields\" mode. The two fields are read as the game wrote them and " +
      "are never rescaled to fit the window.",
  ),
  control(
    PARAM_SNAP,
    CONTROL_NUMBER,
    "The grid a dragged node lands on, drawn under the background at 1x zoom " +
      "and above. 0 draws no grid. It is read in \"manual\" mode only, where " +
      "there is something to drag.",
  ),
];

// --- The scene -------------------------------------------------------

// mapScene is the picture.
//
// `options`:
//   layout     — a composition from layout/compose.js, when the page
//                made one: `{placements: [{key, x, y, pinned, source}]}`.
//                Only `manual` mode reads it, and only for the nodes a
//                saved arrangement does not cover. Absent, the saved
//                arrangement is read straight off the envelope's
//                `positions[]`, which is the whole of what this renderer
//                needs and costs no engine.
//   background — the ground this view names: `{href, scale, offset,
//                width, height}`. **Its presence is the caller's
//                statement that the view names a ground**; an href that
//                cannot be drawn is then a background that is gone, and
//                is banded. Only the caller can tell those two apart:
//                internal/views' RemoveAsset resets the scale and the
//                offset along with the id, precisely so that no knob is
//                left placing an image that does not exist, which leaves
//                a removed background and one that was never set
//                identical in the database.
//   zoom       — the canvas's current scale, read only to decide whether
//                the grid is drawn.
//
// Returns:
//   marks     — the scene.
//   legend    — null, always: this renderer paints no data. See
//               render/marks.js's POINT_FILL for the argument.
//   placed    — the addresses that got a pin.
//   shelf     — `{key, label}` per node with no coordinate, in address
//               order, which is also the order the chips are drawn in.
//   automatic — how many pins are at a coordinate this client chose
//               rather than a designer, for the frame's *"n new nodes
//               were placed automatically"*.
//   firstRun  — the sentence, or null. Not a mark; see the header.
//   background — `{drawn, missing}`.
//   grid      — `{spacing, drawn}`, so a test and the canvas read one
//               answer about a thing that is sometimes not there.
//   stubs, loops — as every renderer reports them, for the footer.
//   offMap    — edges the answer has whose two ends are both in it and
//               at least one of which is on the shelf. Drawn as nothing
//               and counted, so that lines + loops + offMap + stubs is
//               exactly the answer's edge count.
export function mapScene(envelope, params = {}, options = {}) {
  const nodes = nodesOf(envelope);
  const config = readParams(params);
  const zoom = Number.isFinite(options.zoom) ? options.zoom : 1;

  const coordinates = coordinatesFor(nodes, envelope, config, options);

  const marks = [];
  const pins = new Map();
  const shelf = [];
  let automatic = 0;

  for (const node of nodes) {
    const key = addressOf(node);
    const at = coordinates.get(key);
    if (!at) {
      // Never at the origin, and never invisible either: the chip is
      // drawn below, once the picture's own extent is known.
      shelf.push({ key, label: labelOf(node) });
      continue;
    }
    if (!at.placed) automatic++;
    pins.set(key, {
      key,
      node,
      label: labelOf(node),
      x: at.x,
      y: at.y,
      placed: at.placed,
      // A pin is a disc; the drawing vocabulary joins edges between
      // *boxes*, so a pin's box is the disc's own bounding square. The
      // approximation is exact at the four compass points and a
      // half-pixel out at the diagonals, which is a line that starts
      // half a pixel inside a 7px disc.
      width: 2 * POINT_RADIUS,
      height: 2 * POINT_RADIUS,
    });
  }

  const pictureBounds = boundsOf([...pins.values()]);
  const ground = groundFor(options.background);
  const bounds = unionOf(pictureBounds, ground ? rectOf(ground) : null);

  // The grid first and the background over it: §4.6 puts the grid
  // *under* the image, so it shows exactly where the image does not and
  // never competes with a designer's own file.
  const gridDrawn = config.snap > 0 && config.mode === COORDINATES_MANUAL && zoom >= GRID_MIN_ZOOM;
  if (gridDrawn) marks.push(...gridMarks(bounds, config.snap));
  if (ground) marks.push(...backgroundMarks(ground));

  const { drawn, stubs } = joinEdges(nodes, edgesOf(envelope));
  let loops = 0;
  let offMap = 0;
  for (const { source, target } of drawn) {
    const from = pins.get(addressOf(source));
    const to = pins.get(addressOf(target));
    // A relation from an entity to itself: counted rather than dropped,
    // for render/graph.js's reason — a picture and a twin disagreeing
    // about an answer they both received is the defect this whole
    // sub-project is measured against.
    if (from && to && from === to) {
      loops++;
      continue;
    }
    // One end is on the shelf: nothing to draw, and it is not an edge
    // leaving the picture either — the far end is in the answer, and the
    // shelf chip already says why it is not on the map. **Counted**, for
    // the reason a loop is: every edge of the answer is a line, a loop,
    // a stub or this, and a renderer whose four numbers do not add up to
    // the twin's row count has quietly dropped one.
    if (!from || !to) {
      offMap++;
      continue;
    }
    // Thin lines, no heads, no labels: the catalogue gives `map` neither
    // knob, and a map whose relations shouted would be a graph drawn on
    // an image.
    marks.push(...edgeMarks({ source: from, target: to, arrows: false, edgeLabels: false }));
  }

  let anchorless = 0;
  for (const stub of stubs) {
    const anchor = stub.source ?? stub.target;
    const pin = anchor ? pins.get(addressOf(anchor)) : null;
    if (!pin) {
      anchorless++;
      continue;
    }
    marks.push(...stubMarks({ node: pin, enclosure: bounds || rectAround(pin) }));
  }

  for (const pin of pins.values()) {
    marks.push(
      ...pointMarks({
        key: pin.key,
        label: pin.label,
        x: pin.x,
        y: pin.y,
        placed: pin.placed,
        ambiguous: pin.node.ambiguous === true,
      }),
    );
  }

  marks.push(...shelfMarks(shelf, bounds));

  return {
    marks,
    legend: null,
    placed: [...pins.keys()].sort(compare),
    shelf,
    automatic,
    // The sentence, in the one case §4.6 names: a `manual` map with no
    // saved arrangement at all. A map in "fields" mode with nothing
    // placed is a different thing — the game declares the coordinates
    // and no amount of dragging would help — and telling a designer to
    // drag would send them somewhere the picture cannot go.
    firstRun:
      config.mode === COORDINATES_MANUAL && coordinates.size === 0 && nodes.length > 0
        ? { text: FIRST_RUN_TEXT }
        : null,
    background: { drawn: ground !== null, missing: backgroundMissing(options.background) },
    grid: { spacing: config.snap, drawn: gridDrawn },
    stubs: { total: stubs.length, drawn: stubs.length - anchorless, anchorless },
    loops,
    offMap,
  };
}

// --- Where a coordinate comes from -----------------------------------

// coordinatesFor is the whole of `coordinate_source`, and it is the one
// place a node either has a place on this map or does not.
//
// Returns a Map from address to `{x, y, placed}`, where `placed` says a
// *person or the game* chose this coordinate — a solid pin — as against
// a coordinate this client computed for a node new to a saved
// arrangement, which is drawn hollow and counted.
function coordinatesFor(nodes, envelope, config, options) {
  const out = new Map();
  if (config.mode === COORDINATES_FIELDS) {
    // The game states the position. Both halves or neither: a node with
    // an x and no y is not half-placed, it is unplaceable, and a
    // renderer that used the origin for the missing half would put it on
    // an axis nobody chose.
    for (const node of nodes) {
      const x = numberOf(node, config.xField);
      const y = numberOf(node, config.yField);
      if (x === null || y === null) continue;
      out.set(addressOf(node), { x, y, placed: true });
    }
    return out;
  }

  // manual. The saved arrangement is the envelope's own `positions[]`;
  // a composition, when the page made one, additionally carries the
  // coordinates it computed for nodes the arrangement has no row for.
  const inPicture = new Set(nodes.map(addressOf));
  const saved = new Map();
  for (const row of storedFrom(envelope && envelope.positions)) {
    if (!inPicture.has(row.key)) continue;
    saved.set(row.key, row);
  }
  for (const [key, row] of saved) out.set(key, { x: row.x, y: row.y, placed: true });

  // **A fresh map is every node on the shelf**, whatever a composition
  // computed for it. §4.6 asks for exactly that, and it is also the only
  // reading under which the first-run sentence is true: a map where the
  // client has already scattered every node is not one where "nothing
  // has been placed yet", and a designer told to drag from the shelf
  // would find it empty.
  if (saved.size === 0) return out;

  for (const placement of placementsOf(options.layout)) {
    if (!inPicture.has(placement.key) || out.has(placement.key)) continue;
    // New to a saved arrangement: placed by the client, hollow, counted.
    out.set(placement.key, {
      x: placement.x,
      y: placement.y,
      placed: placement.source === SOURCE_STORED,
    });
  }
  return out;
}

// --- The shelf -------------------------------------------------------

// shelfMarks is the strip along the bottom: a rule with the count on it,
// and one dashed chip per node that has nowhere to go.
//
// The chips are laid out with the same `chipWidth` that draws them, so
// the strip cannot overlap its own plates — boxFor's argument about two
// measurements, one step along.
function shelfMarks(shelf, bounds) {
  if (shelf.length === 0) return [];
  const left = bounds ? bounds.minX : 0;
  const top = (bounds ? bounds.maxY : 0) + SHELF_GAP;
  const right = bounds ? Math.max(bounds.maxX, left + 1) : left + 1;
  const marks = bandMarks({
    x1: left,
    y1: top,
    x2: right,
    y2: top,
    caption: shelfCaption(shelf.length),
    captionX: left,
    captionY: top - BAND_CAPTION_GAP,
    anchor: "start",
    baseline: "auto",
  });
  let x = left;
  for (const entry of shelf) {
    const width = chipWidth(entry.label);
    marks.push(
      // Dashed, because a shelved node is a node whose coordinate is not
      // here, which is the one thing that dash means across the six.
      ...chipMarks(
        { x: x + width / 2, y: top + SHELF_CHIP_GAP + CHIP_HEIGHT / 2 },
        entry.label,
        entry.key,
        { dash: true },
      ),
    );
    x += width + SHELF_CHIP_GAP;
  }
  return marks;
}

// --- The ground ------------------------------------------------------

// groundFor is the background rectangle, or null when there is nothing
// drawable to draw.
//
// The scale multiplies the asset's own pixel size and the offset places
// its top-left corner, which is what `views.set_background` writes and
// what internal/views/assets.go defaults to 1 and (0,0).
function groundFor(background) {
  if (!background || typeof background !== "object") return null;
  const scale = Number.isFinite(background.scale) && background.scale > 0 ? background.scale : 1;
  const offset = background.offset && typeof background.offset === "object" ? background.offset : {};
  const width = Number.isFinite(background.width) ? background.width * scale : 0;
  const height = Number.isFinite(background.height) ? background.height * scale : 0;
  if (!(width > 0) || !(height > 0)) return null;
  const marks = backgroundMarks({
    href: background.href,
    x: Number.isFinite(offset.x) ? offset.x : 0,
    y: Number.isFinite(offset.y) ? offset.y : 0,
    width,
    height,
  });
  // backgroundMarks is the one judge of an href this instance would
  // fetch (render/scene.js's isDrawableHref). Asking it, rather than
  // asking the same question again here, is what keeps one answer.
  if (marks.length === 0) return null;
  return {
    href: background.href,
    x: Number.isFinite(offset.x) ? offset.x : 0,
    y: Number.isFinite(offset.y) ? offset.y : 0,
    width,
    height,
  };
}

// backgroundMissing is "this view names a ground and there is none".
//
// A view that names no background says nothing — render/scene.js's own
// first rule, that there is no band for the absence of a thing — so an
// absent descriptor is silence and a descriptor that cannot be drawn is
// the band.
function backgroundMissing(background) {
  if (!background || typeof background !== "object") return false;
  return groundFor(background) === null;
}

// --- Reading the parameters ------------------------------------------

// readParams is the one reader of renderer_params.
//
// `snap` is read in `manual` mode only, which is the catalogue's own
// rule: internal/views/renderers.go refuses the parameter in `fields`
// mode outright, saying that a node's coordinates come off its declared
// fields there, nothing is dragged, and a grid size changes no picture.
// A client that drew the grid anyway would be drawing a knob the server
// would not have stored.
function readParams(params) {
  const p = params && typeof params === "object" ? params : {};
  const snap = p[PARAM_SNAP];
  return {
    mode: COORDINATE_SOURCES.includes(p[PARAM_COORDINATE_SOURCE])
      ? p[PARAM_COORDINATE_SOURCE]
      : COORDINATES_MANUAL,
    xField: field(p[PARAM_X_FIELD]),
    yField: field(p[PARAM_Y_FIELD]),
    snap: typeof snap === "number" && Number.isFinite(snap) && snap > 0 ? snap : 0,
  };
}

function field(value) {
  return typeof value === "string" && value !== "" ? value : null;
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
  const out = [];
  for (const row of rows) {
    if (!isObject(row) || typeof row.key !== "string") continue;
    if (!Number.isFinite(row.x) || !Number.isFinite(row.y)) continue;
    out.push(row);
  }
  return out;
}

function labelOf(node) {
  const attrs = isObject(node.attrs) ? node.attrs : null;
  if (attrs && Object.prototype.hasOwnProperty.call(attrs, "label")) {
    return labelFor(JSON.stringify(attrs.label));
  }
  return text(node.name);
}

// numberOf is a node's value for a declared number field, or null.
//
// It reads `fields` as well as `attrs`, and that is not a convenience:
// `x_field` names a **declared field key**, which reaches the envelope
// through `project.fields` (internal/views/renderers.go's kindNumberField
// requires exactly that), and a run with `include_fields` carries it in
// `fields` instead. Both are the game's own number for this node.
//
// `hasOwnProperty` and a finiteness test rather than `|| 0`: a field
// absent and a field carrying 0 are two answers, and the second is a
// coordinate the game means.
function numberOf(node, key) {
  if (key === null) return null;
  for (const bag of [node.attrs, node.fields]) {
    if (!isObject(bag) || !Object.prototype.hasOwnProperty.call(bag, key)) continue;
    const value = bag[key];
    if (typeof value === "number" && Number.isFinite(value)) return value;
    return null;
  }
  return null;
}

// --- Geometry --------------------------------------------------------

function rectOf(ground) {
  return {
    minX: ground.x,
    minY: ground.y,
    maxX: ground.x + ground.width,
    maxY: ground.y + ground.height,
  };
}

function rectAround(pin) {
  return { minX: pin.x, minY: pin.y, maxX: pin.x, maxY: pin.y };
}

function unionOf(a, b) {
  if (!a) return b;
  if (!b) return a;
  return {
    minX: Math.min(a.minX, b.minX),
    minY: Math.min(a.minY, b.minY),
    maxX: Math.max(a.maxX, b.maxX),
    maxY: Math.max(a.maxY, b.maxY),
  };
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
