// The `layered` renderer: the same node vocabulary as `graph`, arranged
// in ranks, and honest about the edges it had to draw backwards.

import { addressOf } from "../address.js";
import { labelFor } from "../palette.js";
import { joinEdges } from "./scene.js";
import {
  BAND_CAPTION_GAP,
  NODE_PLAIN_FILL,
  bandMarks,
  boundsOf,
  boxFor,
  directionOf,
  edgeMarks,
  midpointOf,
  nodeMarks,
  reversalMarks,
  stubMarks,
} from "./marks.js";
import { CONTROL_BOOL, CONTROL_ENUM, CONTROL_RANK_BY, control } from "./controls.js";

// The name the catalogue holds, and this module's.
export const RENDERER = "layered";

// The renderer_params keys, spelled as the server spells them.
export const PARAM_RANK_DIRECTION = "rank_direction";
export const PARAM_RANK_BY = "rank_by";
export const PARAM_LAYER_LABELS = "layer_labels";
export const PARAM_ALIGN = "align";

// The admitted values, in the catalogue's own spellings.
export const DIRECTION_TB = "TB";
export const DIRECTION_LR = "LR";
export const DIRECTIONS = [DIRECTION_TB, DIRECTION_LR];
export const RANK_BY_EDGES = "edges";
export const ALIGN_START = "start";
export const ALIGN_CENTER = "center";
export const ALIGN_END = "end";
export const ALIGNMENTS = [ALIGN_START, ALIGN_CENTER, ALIGN_END];

// The caption of the band a node with no numeric rank goes to. It is
// the palette's own word for an absence in a legend row, called rather
// than restated, so the band and the row a designer meets elsewhere say
// the same thing about the same fact.
export const UNRANKED_CAPTION = "not ranked";

// The gap between two bands, and the picture's margin, taken from the
// layout engine's own defaults so a hand-banded picture and an
// engine-banded one are spaced alike.
export const BAND_SEPARATION = 60;
export const BAND_MARGIN = 24;

// The controls. Each tooltip says what the knob does to the **picture**,
// which is the half internal/views/renderers.go deliberately does not
// carry: its `Doc` describes what a query must produce, written for an
// agent (render/controls.js's own header has the argument).
export const CONTROLS = [
  control(
    PARAM_RANK_DIRECTION,
    CONTROL_ENUM,
    "Which way the ranks run: TB stacks them top to bottom, LR runs them " +
      "left to right. It transposes the whole picture and nothing else.",
    DIRECTIONS,
  ),
  control(
    PARAM_RANK_BY,
    CONTROL_RANK_BY,
    'What decides a node\'s layer. "edges" uses the longest path through ' +
      "the relations, and each band is captioned with its index. A number " +
      "field key uses that number, and each band is captioned with the " +
      "value itself — a layer labelled 22 rather than 3. A node whose " +
      "field is missing goes to a trailing unranked band, never to the top.",
  ),
  control(
    PARAM_LAYER_LABELS,
    CONTROL_BOOL,
    "Draws a muted rule between the layers with a caption on each. Off, " +
      "the layers are still there and nothing names them.",
  ),
  control(
    PARAM_ALIGN,
    CONTROL_ENUM,
    "Where a layer's nodes sit across the picture: start packs each layer " +
      "to one side, end to the other, center centres it. Left unset, the " +
      "layout keeps edges as straight as it can, which is usually what you " +
      "want and never lines the layers up.",
    ALIGNMENTS,
  ),
];

// --- What the layout is asked for ------------------------------------

// layeredLayoutRequest is the request the worker runs.
export function layeredLayoutRequest(envelope, params = {}) {
  const nodes = nodesOf(envelope);
  const options = readParams(params);
  const laid = nodes.map((node) => {
    const box = boxFor(labelOf(node));
    return { type: text(node.type), key: text(node.key), width: box.width, height: box.height };
  });
  const { drawn } = joinEdges(nodes, edgesOf(envelope));
  const edges = drawn.map(({ edge, source, target }) => ({
    type: text(edge.type),
    source: { type: text(source.type), key: text(source.key) },
    target: { type: text(target.type), key: text(target.key) },
  }));
  return { nodes: laid, edges, options: { rankdir: options.direction } };
}

// --- The ranking -----------------------------------------------------

// ranksFor is the whole of the parameter's meaning: which band each node
// is in, what each band is called, and whether the graph has a cycle.
export function ranksFor(nodes, drawn, rankBy) {
  return rankBy === RANK_BY_EDGES ? rankByEdges(nodes, drawn) : rankByField(nodes, drawn, rankBy);
}

// rankByEdges is the longest path, over the graph with its cycles
// broken.
function rankByEdges(nodes, drawn) {
  const keys = nodes.map(addressOf).sort(compare);
  const out = new Map(keys.map((key) => [key, []]));
  for (const { source, target } of drawn) {
    const from = addressOf(source);
    const to = addressOf(target);
    // A relation to itself is a cycle of one that no ranking reverses
    // and no line can draw; it is counted as a loop by the scene and
    // left out of the walk.
    if (from === to || !out.has(from) || !out.has(to)) continue;
    out.get(from).push(to);
  }
  for (const list of out.values()) list.sort(compare);

  // The depth-first walk: `state` is 0 unseen, 1 on the stack, 2 done.
  // A back edge is one to a node still on the stack.
  const state = new Map(keys.map((key) => [key, 0]));
  const back = new Set();
  const order = [];
  let cyclic = false;
  for (const root of keys) {
    if (state.get(root) !== 0) continue;
    const stack = [{ key: root, at: 0 }];
    state.set(root, 1);
    while (stack.length > 0) {
      const frame = stack[stack.length - 1];
      const next = out.get(frame.key)[frame.at];
      if (next === undefined) {
        state.set(frame.key, 2);
        order.push(frame.key);
        stack.pop();
        continue;
      }
      frame.at++;
      if (state.get(next) === 1) {
        cyclic = true;
        back.add(edgeKey(frame.key, next));
        continue;
      }
      if (state.get(next) === 0) {
        state.set(next, 1);
        stack.push({ key: next, at: 0 });
      }
    }
  }

  // The longest path, over the edges the walk did not break. `order` is
  // a reverse topological order of that DAG, so one pass from its end
  // gives every node a rank one past its deepest predecessor.
  const rank = new Map(keys.map((key) => [key, 0]));
  for (let i = order.length - 1; i >= 0; i--) {
    const key = order[i];
    for (const next of out.get(key)) {
      if (back.has(edgeKey(key, next))) continue;
      const candidate = rank.get(key) + 1;
      if (candidate > rank.get(next)) rank.set(next, candidate);
    }
  }

  const depth = keys.length === 0 ? 0 : Math.max(...keys.map((key) => rank.get(key))) + 1;
  const bands = [];
  for (let i = 0; i < depth; i++) {
    // The index, and nothing else: there is no value to name.
    bands.push({ index: i, caption: String(i), unranked: false, value: null });
  }
  return { bandOf: rank, bands, cyclic };
}

// rankByField is one band per distinct value of a declared number field,
// ascending, with the **value** as the caption.
function rankByField(nodes, drawn, field) {
  const values = new Map();
  for (const node of nodes) {
    const value = numberOf(node, field);
    if (value === null) continue;
    values.set(value, true);
  }
  const sorted = [...values.keys()].sort((a, b) => a - b);
  const bands = sorted.map((value, index) => ({
    index,
    caption: labelFor(JSON.stringify(value)),
    unranked: false,
    value,
  }));

  const bandOf = new Map();
  let unrankedBand = null;
  for (const node of nodes) {
    const key = addressOf(node);
    const value = numberOf(node, field);
    if (value === null) {
      if (unrankedBand === null) {
        unrankedBand = bands.length;
        bands.push({ index: unrankedBand, caption: UNRANKED_CAPTION, unranked: true, value: null });
      }
      bandOf.set(key, unrankedBand);
      continue;
    }
    bandOf.set(key, sorted.indexOf(value));
  }
  return { bandOf, bands, cyclic: rankByEdges(nodes, drawn).cyclic };
}

// --- The scene -------------------------------------------------------

// layeredScene is the picture.
export function layeredScene(envelope, layout, params = {}) {
  const nodes = nodesOf(envelope);
  const options = readParams(params);
  const placements = placementsOf(layout);
  const { drawn, stubs } = joinEdges(nodes, edgesOf(envelope));
  const { bandOf, bands, cyclic } = ranksFor(nodes, drawn, options.rankBy);

  const along = options.direction === DIRECTION_LR ? "x" : "y";
  const cross = options.direction === DIRECTION_LR ? "y" : "x";
  const alongSize = along === "y" ? "height" : "width";
  const crossSize = cross === "y" ? "height" : "width";

  const boxes = new Map();
  const unplaced = [];
  const members = bands.map(() => []);
  for (const node of nodes) {
    const key = addressOf(node);
    const placement = placements.get(key);
    if (!placement) {
      // A box at the origin is a position nobody chose. Named, not
      // drawn, exactly as `graph` does it.
      unplaced.push(key);
      continue;
    }
    const { width, height } = boxFor(labelOf(node));
    const band = bandOf.has(key) ? bandOf.get(key) : 0;
    const box = { key, node, band, width, height, x: 0, y: 0 };
    box[cross] = placement[cross];
    boxes.set(key, box);
    if (members[band]) members[band].push(box);
  }

  // The bands are stacked along the rank axis, each thick enough for its
  // own boxes, and every box is centred in its band. The engine's
  // along-axis answer is discarded here and its cross-axis answer is
  // kept: see layeredLayoutRequest for why the split is where it is.
  let cursor = BAND_MARGIN;
  bands.forEach((band, index) => {
    const list = members[index] || [];
    const thickness = list.length === 0 ? 0 : Math.max(...list.map((box) => box[alongSize]));
    band.start = cursor;
    band.thickness = thickness;
    band.centre = cursor + thickness / 2;
    for (const box of list) box[along] = band.centre;
    cursor += thickness + BAND_SEPARATION;
  });

  alignBands(bands, members, options.align, cross, crossSize);

  const marks = [];

  // The rules and captions first: they are the ground the picture sits
  // on, and bandMarks puts them on the image layer for the reason an
  // enclosure goes there — chrome drawn over an edge cuts it.
  if (options.layerLabels) {
    const extent = crossExtentOf([...boxes.values()], cross, crossSize);
    for (const band of bands) {
      if ((members[band.index] || []).length === 0) continue;
      marks.push(...ruleFor(band, extent, along, cross));
    }
  }

  let against = 0;
  let loops = 0;
  // See the push below: these are added to the scene last, over
  // everything they would otherwise hide under.
  const reversals = [];
  for (const { source, target } of drawn) {
    const from = boxes.get(addressOf(source));
    const to = boxes.get(addressOf(target));
    if (from && to && from === to) {
      loops++;
      continue;
    }
    if (!from || !to) continue;
    // **The arrowhead is at the true target, always.** A ranked picture
    // of a cyclic graph has edges that run backwards up it, and sorting
    // the endpoints to make them run forwards is the one thing this
    // renderer must not do.
    marks.push(...edgeMarks({ source: from, target: to, arrows: true, edgeLabels: false }));
    if (from.band > to.band) {
      against++;
      // Held back and pushed **last**, which is a paint-order decision
      // and not tidiness. Scene order is document order is paint order
      // (render/scene.js), and every node label in this picture is
      // emitted after this loop into the same layer — so a reversal mark
      // pushed here is drawn under a hundred names. On the picture this
      // mark exists for, a hundred-step progression, that is exactly
      // what happened: the mark was there, at a readable size, with a
      // node label painted over its middle. Measured in Firefox with
      // elementFromPoint. It is the last mark in the scene because it is
      // the one thing in a ranked drawing that a reader must not miss.
      reversals.push(...reversalMarks(midpointOf(from, to), directionOf(from, to)));
    }
  }

  const pictureBounds = boundsOf([...boxes.values()]);
  let anchorless = 0;
  for (const stub of stubs) {
    const anchor = stub.source ?? stub.target;
    const box = anchor ? boxes.get(addressOf(anchor)) : null;
    if (!box) {
      anchorless++;
      continue;
    }
    // The enclosure a stub has to leave is the whole picture: `layered`
    // has no `group_by` and therefore no smaller box to escape from.
    // `pictureBounds` cannot be null here — `box` is one of the boxes it
    // was measured over — so there is no fallback to be wrong about.
    marks.push(...stubMarks({ node: box, enclosure: pictureBounds }));
  }

  let unranked = 0;
  for (const box of boxes.values()) {
    const band = bands[box.band];
    const absent = band !== undefined && band.unranked === true;
    if (absent) unranked++;
    marks.push(
      ...nodeMarks({
        key: box.key,
        label: labelOf(box.node),
        x: box.x,
        y: box.y,
        width: box.width,
        height: box.height,
        // This renderer has no colour parameter — the catalogue gives it
        // none — so every box is paper. The dash below is therefore
        // unambiguous here: the only thing a `layered` box can be
        // missing is its rank.
        fill: NODE_PLAIN_FILL,
        dash: absent,
        ambiguous: box.node.ambiguous === true,
      }),
    );
  }

  // Last, over the node labels: see the reversals array above.
  marks.push(...reversals);

  return {
    marks,
    legend: null,
    bands: bands.map((band) => ({
      index: band.index,
      caption: band.caption,
      unranked: band.unranked,
      value: band.value,
      centre: band.centre,
      count: (members[band.index] || []).length,
    })),
    against,
    cyclic,
    unranked,
    stubs: { total: stubs.length, drawn: stubs.length - anchorless, anchorless },
    loops,
    unplaced,
  };
}

// alignBands positions each band's run of boxes across the picture.
function alignBands(bands, members, align, cross, crossSize) {
  if (align === null) return;
  const all = [];
  for (const list of members) for (const box of list) all.push(box);
  const picture = crossExtentOf(all, cross, crossSize);
  if (!picture) return;
  for (const band of bands) {
    const list = members[band.index] || [];
    const extent = crossExtentOf(list, cross, crossSize);
    if (!extent) continue;
    const delta =
      align === ALIGN_START
        ? picture.min - extent.min
        : align === ALIGN_END
          ? picture.max - extent.max
          : (picture.min + picture.max) / 2 - (extent.min + extent.max) / 2;
    for (const box of list) box[cross] += delta;
  }
}

function crossExtentOf(boxes, cross, crossSize) {
  let min = null;
  let max = null;
  for (const box of boxes) {
    const low = box[cross] - box[crossSize] / 2;
    const high = box[cross] + box[crossSize] / 2;
    if (min === null || low < min) min = low;
    if (max === null || high > max) max = high;
  }
  return min === null ? null : { min, max };
}

// ruleFor is one band's muted rule and its caption, in the axis the
// direction chose. The rule runs across the picture at the band's
// leading edge and the caption sits at its start, outside the run of
// boxes, where it cannot land on one.
function ruleFor(band, extent, along, cross) {
  const at = band.start - BAND_SEPARATION / 2;
  const from = extent ? extent.min - BAND_MARGIN : 0;
  const to = extent ? extent.max + BAND_MARGIN : 0;
  const geometry =
    along === "y"
      ? {
          x1: from,
          y1: at,
          x2: to,
          y2: at,
          captionX: from,
          captionY: at - BAND_CAPTION_GAP,
          anchor: "start",
          baseline: "auto",
        }
      : {
          x1: at,
          y1: from,
          x2: at,
          y2: to,
          captionX: at + BAND_CAPTION_GAP,
          captionY: from,
          anchor: "start",
          baseline: "hanging",
        };
  return bandMarks({ ...geometry, caption: band.caption });
}

// --- Reading the parameters ------------------------------------------

// readParams is the one reader, so a knob spelled wrongly is absent
// everywhere rather than honoured in half the module.
function readParams(params) {
  const p = params && typeof params === "object" ? params : {};
  return {
    direction: DIRECTIONS.includes(p[PARAM_RANK_DIRECTION]) ? p[PARAM_RANK_DIRECTION] : DIRECTION_TB,
    // Unset ranks by the edges, which is what the catalogue's own
    // requirement — a query that draws none is refused — assumes.
    rankBy: slot(p[PARAM_RANK_BY]) === null ? RANK_BY_EDGES : p[PARAM_RANK_BY],
    layerLabels: p[PARAM_LAYER_LABELS] === true,
    align: ALIGNMENTS.includes(p[PARAM_ALIGN]) ? p[PARAM_ALIGN] : null,
  };
}

function slot(value) {
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
  const byKey = new Map();
  for (const row of rows) {
    if (!isObject(row) || typeof row.key !== "string") continue;
    if (!Number.isFinite(row.x) || !Number.isFinite(row.y)) continue;
    if (!byKey.has(row.key)) byKey.set(row.key, row);
  }
  return byKey;
}

function labelOf(node) {
  const attrs = isObject(node.attrs) ? node.attrs : null;
  if (attrs && Object.prototype.hasOwnProperty.call(attrs, "label")) {
    return labelFor(JSON.stringify(attrs.label));
  }
  return text(node.name);
}

// numberOf is a node's value for a declared number field, or null when
// there is none.
function numberOf(node, field) {
  const attrs = isObject(node.attrs) ? node.attrs : null;
  if (!attrs || !Object.prototype.hasOwnProperty.call(attrs, field)) return null;
  const value = attrs[field];
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function edgeKey(from, to) {
  return from + " " + to;
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
