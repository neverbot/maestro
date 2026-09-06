// The `timeline` renderer: nodes along one axis, optionally in lanes,
// and an axis that is a **declaration** rather than a sort.
//
// A renderer is a **pure function from an envelope to a scene** — the
// rule render/graph.js's header sets out — and like `map` and `nested`
// this one asks the layout engine for nothing: §5.1 says so outright
// ("`timeline` has an axis"), and a graph algorithm asked for a position
// the game already states would be inventing an arrangement over one
// somebody wrote down.
//
// **An enum axis is its option sequence, not its set.** A championship
// declaring `[heat, semi, final]` has an axis in that order, and
// `[final, heat, semi]` is a different axis over the same three words.
// Sorting them alphabetically would produce a picture that is wrong in a
// way nothing in it shows — which is exactly why
// internal/views/renderers.go refuses to save a view whose two ends are
// enums over different option lists, and why the options arrive here
// **from the declaration** and are never derived from the values in the
// answer. An answer contains the values a query found; the axis is what
// the game says exists, which is why a declared option with no node
// still gets a tick.
//
// **What the catalogue enforces and this renderer therefore does not
// restate.** `axis_field` has to be declared identically across every
// type in scope, and `axis_end_field` has to land on the same axis; both
// are checked at save time, so a *saved* view cannot violate either. An
// inline run can, and this module's answer to it is the same one it
// gives any value it cannot place: the node is drawn before the axis
// begins and counted. It does not refuse, it does not repeat the
// server's sentence, and it does not guess a second axis.
//
// **What it draws when the answer is not a clean one:**
//
//   a node with no value for the axis — a mark in the region **before
//     the axis begins**, captioned and counted, and never at the axis
//     origin: the origin is a value the axis means, exactly as `map`'s
//     (0,0) is a place a designer may have meant.
//   a value that is not on the axis — the same region and the same
//     count. An enum axis is its options; a value outside them is not a
//     value *of this axis*, which is the honest reading and the one that
//     needs no second sentence.
//   a span whose end is before its start — a zero-length mark with a
//     caret, and the frame names it. **The two ends are not swapped**:
//     that is a content defect a designer wants to know about, and a
//     renderer that sorted the pair would draw a plausible bar over it.
//   a span whose end is not on the axis — the start's own point mark,
//     and a count. There is no length to draw between a value and one
//     that is not on this axis.
//   more than three marks overlapping in one lane — three stacked and
//     the rest collapsed into a count chip, which expands from the
//     envelope already in hand. A drawing density is not a fetch
//     boundary, exactly as `nested`'s depth is not.
//   a truncated answer — what an untruncated one draws.

import { addressOf } from "../address.js";
import { UNSET_LABEL, labelFor } from "../palette.js";
import {
  BAND_CAPTION_GAP,
  CHIP_HEIGHT,
  POINT_RADIUS,
  bandMarks,
  caretMarks,
  chipMarks,
  pointMarks,
  spanMarks,
} from "./marks.js";
import { CONTROL_AXIS_FIELD, CONTROL_SLOT, CONTROL_TEXT, control } from "./controls.js";

// The name the catalogue holds, and this module's.
export const RENDERER = "timeline";

// The renderer_params keys, spelled as the server spells them.
export const PARAM_AXIS_FIELD = "axis_field";
export const PARAM_AXIS_END_FIELD = "axis_end_field";
export const PARAM_LANE_BY = "lane_by";
export const PARAM_AXIS_LABEL = "axis_label";

// The two kinds of axis, in the metamodel's own spellings for a declared
// field's type (internal/metamodel's FieldNumber and FieldEnum).
export const AXIS_NUMBER = "number";
export const AXIS_ENUM = "enum";

// How long the axis is drawn, in the picture's own units. The canvas
// zooms; this is the length everything is laid out against, so that two
// runs of one view are one picture whatever the numbers are.
export const AXIS_LENGTH = 720;

// The region before the axis begins, where a node with no place on it is
// drawn. It is to the **left of the origin** and separated from it,
// because the origin is a value.
export const OFF_AXIS_GAP = 40;
export const OFF_AXIS_WIDTH = 160;

// A lane's height, and how deep marks stack inside one before the rest
// collapse into a chip (§4.8).
export const LANE_HEIGHT = 72;
export const STACK_GAP = 18;
export const STACK_DEPTH = 3;

// How many ticks a number axis aims for. It is a target and not a count:
// the step is chosen from 1, 2 and 5 times a power of ten so that the
// captions are numbers a reader recognises, which means the number of
// ticks lands near this rather than on it.
export const TICK_TARGET = 8;

// The caption of the lane a node with no value for `lane_by` falls into,
// and of the region before the axis. The first is the palette's own word
// for an absence, called rather than restated; the second names the
// field, because the field is the recovery.
export const UNLANED_CAPTION = UNSET_LABEL;

export function offAxisCaption(field) {
  return `no value for ${field === null || field === "" ? "the axis" : field}`;
}

// The controls. Each tooltip says what the knob does to the **picture**,
// which is the half internal/views/renderers.go deliberately does not
// carry (render/controls.js's header has the argument).
//
// `axis_field`'s is the one that matters most: the catalogue says it
// takes a number or an enum, and what a designer needs to know is that
// choosing an enum chooses its **declared order** as the axis — which is
// the whole reason two enums over different options cannot be the two
// ends of one span.
export const CONTROLS = [
  control(
    PARAM_AXIS_FIELD,
    CONTROL_AXIS_FIELD,
    "The field the axis reads. A number field gives a numeric axis; an " +
      "enum field gives one tick per declared option, in the order the type " +
      "declares them and never alphabetically — that order is the axis. A " +
      "node with no value for it is drawn before the axis begins rather " +
      "than at its start.",
  ),
  control(
    PARAM_AXIS_END_FIELD,
    CONTROL_AXIS_FIELD,
    "The field holding the end of a span, for things that occupy a range " +
      "rather than a point. Each node becomes a bar from one value to the " +
      "other. A span whose end comes before its start is drawn with a caret " +
      "and named in the frame rather than quietly turned round.",
  ),
  control(
    PARAM_LANE_BY,
    CONTROL_SLOT,
    "Splits the picture into horizontal lanes, one per value of this slot, " +
      "each with a caption and a hairline. Where more than three marks " +
      "overlap in a lane, the rest become a count chip that opens from the " +
      "answer already on screen.",
  ),
  control(
    PARAM_AXIS_LABEL,
    CONTROL_TEXT,
    "What to call the axis. It is written once, at the axis itself, and " +
      "changes nothing else about the picture.",
  ),
];

// --- The axis --------------------------------------------------------

// axisFor is the whole of "an enum axis is its option sequence".
//
// `declaration` is the field the *game* declares — `{type, options}` —
// and it arrives from the caller because it is not in the envelope: the
// answer carries the values a query found, and an axis is what the type
// says exists. That is the difference `everyDeclaredOptionGetsATickEven
// WithNoNodes` is about, and it cannot be recovered from the nodes.
//
// With no declaration there is only a number axis: a value that is not a
// number has no position without one, and inventing an order from the
// values in the answer would be exactly the sort this renderer exists to
// refuse.
export function axisFor(declaration, values) {
  const declared = declaration && typeof declaration === "object" ? declaration : {};
  if (declared.type === AXIS_ENUM) {
    const options = (Array.isArray(declared.options) ? declared.options : []).filter(
      (option) => typeof option === "string",
    );
    // The step is the axis divided by the gaps between its options, so
    // the first option is at the origin and the last at the end,
    // whatever the count. One option is an axis of one place.
    const step = options.length > 1 ? AXIS_LENGTH / (options.length - 1) : 0;
    return {
      type: AXIS_ENUM,
      options,
      // One tick per **declared** option, in declaration order, whether
      // or not any node carries it.
      ticks: options.map((option, index) => ({
        value: option,
        caption: option,
        x: index * step,
      })),
      at: (value) => {
        const index = options.indexOf(value);
        return index < 0 ? null : index * step;
      },
    };
  }

  const numbers = values.filter((value) => typeof value === "number" && Number.isFinite(value));
  const min = numbers.length === 0 ? 0 : Math.min(...numbers);
  const max = numbers.length === 0 ? 0 : Math.max(...numbers);
  const span = max - min;
  // A domain of one value is not a range: everything sits at the origin
  // rather than being spread across an axis that means nothing.
  const scale = span > 0 ? AXIS_LENGTH / span : 0;
  return {
    type: AXIS_NUMBER,
    options: null,
    ticks: niceTicks(min, max).map((value) => ({
      value,
      caption: labelFor(JSON.stringify(value)),
      x: (value - min) * scale,
    })),
    at: (value) =>
      typeof value === "number" && Number.isFinite(value) ? (value - min) * scale : null,
  };
}

// niceTicks is a deterministic set of round numbers across a range.
//
// Deterministic is the property that matters and the one the test names:
// the same data twice is the same ticks, because nothing here reads
// anything but the two ends of the domain. The step is 1, 2 or 5 times a
// power of ten, which is what makes a caption a number a reader
// recognises rather than 13.7142857.
export function niceTicks(min, max, target = TICK_TARGET) {
  if (!Number.isFinite(min) || !Number.isFinite(max)) return [];
  if (!(max > min)) return [min];
  const raw = (max - min) / Math.max(1, target);
  const magnitude = Math.pow(10, Math.floor(Math.log10(raw)));
  let step = magnitude;
  for (const multiple of [1, 2, 5, 10]) {
    step = multiple * magnitude;
    if ((max - min) / step <= target) break;
  }
  const ticks = [];
  const first = Math.ceil(min / step) * step;
  // The loop counts rather than accumulates, so a step that is not
  // exactly representable does not drift along the axis.
  for (let i = 0; first + i * step <= max + step / 1e6; i++) {
    ticks.push(round(first + i * step, step));
  }
  return ticks;
}

// round removes the floating-point tail a multiplication leaves, so a
// caption reads `0.3` rather than `0.30000000000000004`.
function round(value, step) {
  const places = Math.max(0, -Math.floor(Math.log10(step)) + 1);
  return Number(value.toFixed(Math.min(20, places)));
}

// --- The scene -------------------------------------------------------

// timelineScene is the picture.
//
// `options`:
//   axis     — the *declared* field `axis_field` names: `{type, options}`.
//              See axisFor: it is not in the envelope and cannot be.
//   expanded — the chips a designer has clicked open, by key, which is
//              the whole of a chip's behaviour and is why it needs no
//              client.
//
// Returns:
//   marks    — the scene.
//   axis     — `{type, label, ticks}`, the ticks in axis order.
//   lanes    — `{value, caption, count, y}` per lane, or null when the
//              view lanes nothing. Null and not empty, as everywhere.
//   drawn    — the addresses that got a mark of their own.
//   collapsed — the addresses a chip is holding. Drawn plus collapsed is
//              every node the answer has, which is what the twin
//              describes.
//   chips    — `{key, count}` per chip.
//   offAxis  — `{count, field, keys}`: the nodes drawn before the axis
//              begins, whether their value was absent or simply not on
//              this axis.
//   inverted — `{key, name, from, to}` per span that ends before it
//              starts, for the frame's band.
//   openSpans — how many spans have a start on the axis and an end that
//              is not, drawn as their start's point.
export function timelineScene(envelope, params = {}, options = {}) {
  const nodes = nodesOf(envelope);
  const config = readParams(params);
  const expanded = new Set(Array.isArray(options.expanded) ? options.expanded : []);

  // The axis is built over every value either end of a span can carry,
  // so a bar that runs past the last start still has an axis under it.
  const values = [];
  for (const node of nodes) {
    values.push(valueOf(node, config.axisField));
    if (config.axisEndField !== null) values.push(valueOf(node, config.axisEndField));
  }
  const axis = axisFor(options.axis, values);

  const lanes = lanesFor(nodes, config.laneBy);
  const laneOf = new Map();
  for (const lane of lanes) for (const key of lane.keys) laneOf.set(key, lane);

  const placed = [];
  const offAxis = [];
  const inverted = [];
  let openSpans = 0;

  for (const node of nodes) {
    const key = addressOf(node);
    const lane = laneOf.get(key) || lanes[0];
    const label = labelOf(node);
    const start = axis.at(valueOf(node, config.axisField));
    if (start === null) {
      // No value, or a value this axis does not have. One region and one
      // count: a value outside an enum's options is not a value *of this
      // axis*, which is the same statement as having none.
      offAxis.push({ key, node, label, lane });
      continue;
    }
    if (config.axisEndField === null) {
      placed.push({ key, node, label, lane, x1: start, x2: start, span: false });
      continue;
    }
    const endValue = valueOf(node, config.axisEndField);
    const end = axis.at(endValue);
    if (end === null) {
      // A span with no end on this axis has no length to draw. It is the
      // start, as a point, and a count — never a bar to the origin,
      // which would claim a range the answer does not have.
      openSpans++;
      placed.push({ key, node, label, lane, x1: start, x2: start, span: false });
      continue;
    }
    if (end < start) {
      // **Not swapped.** The mark has no length and carries a caret; the
      // frame names the node and both of its values.
      inverted.push({
        key,
        name: label,
        from: valueText(node, config.axisField),
        to: valueText(node, config.axisEndField),
      });
      placed.push({ key, node, label, lane, x1: start, x2: start, span: false, inverted: true });
      continue;
    }
    placed.push({ key, node, label, lane, x1: start, x2: end, span: true });
  }

  const marks = [];

  // The axis and its ticks first: they are the ground the marks sit on,
  // and bandMarks puts a rule on the image layer for the reason an
  // enclosure goes there — chrome drawn over a mark cuts it.
  const height = Math.max(LANE_HEIGHT, lanes.length * LANE_HEIGHT);
  marks.push(
    ...bandMarks({
      x1: 0,
      y1: height,
      x2: AXIS_LENGTH,
      y2: height,
      caption: config.axisLabel === null ? "" : config.axisLabel,
      captionX: 0,
      captionY: height + BAND_CAPTION_GAP + 12,
      anchor: "start",
      baseline: "hanging",
    }),
  );
  for (const tick of axis.ticks) {
    marks.push(
      ...bandMarks({
        x1: tick.x,
        y1: 0,
        x2: tick.x,
        y2: height,
        caption: tick.caption,
        captionX: tick.x,
        captionY: height + BAND_CAPTION_GAP,
        anchor: "middle",
        baseline: "hanging",
      }),
    );
  }

  // The lanes, each with its own hairline and caption, and only when the
  // view lanes anything: a single unnamed lane has nothing to caption
  // and a rule across a picture with one row in it is chrome that says
  // nothing.
  if (config.laneBy !== null) {
    for (const lane of lanes) {
      marks.push(
        ...bandMarks({
          x1: -OFF_AXIS_GAP - OFF_AXIS_WIDTH,
          y1: lane.y - LANE_HEIGHT / 2,
          x2: AXIS_LENGTH,
          y2: lane.y - LANE_HEIGHT / 2,
          caption: lane.caption,
          captionX: -OFF_AXIS_GAP - OFF_AXIS_WIDTH,
          captionY: lane.y - LANE_HEIGHT / 2 - BAND_CAPTION_GAP,
          anchor: "start",
          baseline: "auto",
        }),
      );
    }
  }

  // The region before the axis begins, and its own caption. It is drawn
  // when there is something in it and not otherwise: there is no band,
  // and no caption, for the absence of a thing.
  if (offAxis.length > 0) {
    marks.push(
      ...bandMarks({
        x1: -OFF_AXIS_GAP,
        y1: 0,
        x2: -OFF_AXIS_GAP,
        y2: height,
        caption: offAxisCaption(config.axisField),
        captionX: -OFF_AXIS_GAP,
        captionY: height + BAND_CAPTION_GAP,
        anchor: "end",
        baseline: "hanging",
      }),
    );
  }

  const drawn = [];
  const collapsed = [];
  const chips = [];

  // The off-axis marks, stacked in the region before the origin. They
  // are laid out in their own lanes, so a designer can still see which
  // lane a valueless node belongs to.
  layOut(offAxis.map((entry) => ({ ...entry, x1: offAxisX(entry, offAxis), x2: offAxisX(entry, offAxis) })), {
    marks,
    drawn,
    collapsed,
    chips,
    expanded,
  });

  layOut(placed, { marks, drawn, collapsed, chips, expanded });

  return {
    marks,
    axis: {
      type: axis.type,
      label: config.axisLabel,
      ticks: axis.ticks.map((tick) => ({ value: tick.value, caption: tick.caption, x: tick.x })),
    },
    lanes:
      config.laneBy === null
        ? null
        : lanes.map((lane) => ({
            value: lane.value,
            caption: lane.caption,
            count: lane.keys.length,
            y: lane.y,
          })),
    drawn,
    collapsed,
    chips,
    offAxis: {
      count: offAxis.length,
      field: config.axisField,
      keys: offAxis.map((entry) => entry.key).sort(compare),
    },
    inverted,
    openSpans,
  };
}

// offAxisX spreads the valueless marks across the region before the
// axis, so that two of them in one lane are two marks a reader can tell
// apart rather than one on top of another.
function offAxisX(entry, all) {
  const index = all.indexOf(entry);
  const step = all.length > 1 ? OFF_AXIS_WIDTH / all.length : 0;
  return -OFF_AXIS_GAP - OFF_AXIS_WIDTH + index * step + step / 2;
}

// --- Stacking --------------------------------------------------------

// layOut places a run of marks in their lanes, stacking the ones that
// overlap and collapsing what will not fit.
//
// The stack is a greedy colouring of the overlap intervals in x order:
// each mark takes the first row whose last mark ends before this one
// starts. A row past the third is not drawn — §4.8 stacks three deep —
// and the marks that would have been there are counted into one chip,
// which expands from the envelope already in hand.
function layOut(entries, sink) {
  const byLane = new Map();
  for (const entry of entries) {
    const list = byLane.get(entry.lane) || [];
    list.push(entry);
    byLane.set(entry.lane, list);
  }
  for (const [lane, list] of byLane) {
    // In x order, and by address within it, so one answer is one picture
    // whatever order the envelope arrived in.
    list.sort((a, b) => a.x1 - b.x1 || compare(a.key, b.key));
    const rowEnds = [];
    const overflow = [];
    for (const entry of list) {
      const width = Math.max(entry.x2 - entry.x1, 2 * POINT_RADIUS);
      let row = rowEnds.findIndex((end) => end <= entry.x1);
      if (row < 0) {
        row = rowEnds.length;
        rowEnds.push(entry.x1);
      }
      rowEnds[row] = entry.x1 + width;
      if (row >= STACK_DEPTH) {
        overflow.push(entry);
        continue;
      }
      emit(entry, lane, row, sink);
    }
    if (overflow.length > 0) {
      // The chip is addressed by the first collapsed node, which is a
      // real address a click can carry and a test can name.
      const key = overflow[0].key;
      if (sink.expanded.has(key)) {
        overflow.forEach((entry, index) => emit(entry, lane, STACK_DEPTH + index, sink));
      } else {
        for (const entry of overflow) sink.collapsed.push(entry.key);
        sink.chips.push({ key, count: overflow.length });
        sink.marks.push(
          ...chipMarks(
            { x: overflow[0].x1, y: rowY(lane, STACK_DEPTH) + CHIP_HEIGHT / 2 },
            overflow.length,
            key,
          ),
        );
      }
    }
  }
}

function rowY(lane, row) {
  return lane.y - LANE_HEIGHT / 2 + STACK_GAP * (row + 1);
}

function emit(entry, lane, row, sink) {
  const y = rowY(lane, row);
  sink.drawn.push(entry.key);
  if (entry.span) {
    sink.marks.push(
      ...spanMarks({ key: entry.key, label: entry.label, x1: entry.x1, x2: entry.x2, y }),
    );
    return;
  }
  sink.marks.push(
    ...pointMarks({
      key: entry.key,
      label: entry.label,
      x: entry.x1,
      y,
      placed: true,
      ambiguous: entry.node.ambiguous === true,
    }),
  );
  if (entry.inverted === true) sink.marks.push(...caretMarks({ x: entry.x1, y }, entry.key));
}

// --- The lanes -------------------------------------------------------

// lanesFor is one lane per value of the `lane_by` slot, in value order,
// with the absence **last**.
//
// Last for `layered`'s reason: a lane of "the ones we know nothing
// about" at the top would read as the first lane of the answer, which is
// a claim about content made out of a missing value.
function lanesFor(nodes, laneBy) {
  if (laneBy === null) {
    return [{ value: null, caption: "", keys: nodes.map(addressOf), y: LANE_HEIGHT / 2 }];
  }
  const byValue = new Map();
  for (const node of nodes) {
    const json = valueJSON(node, laneBy);
    const entry = byValue.get(json) || {
      value: json,
      caption: json === null ? UNLANED_CAPTION : labelFor(json),
      keys: [],
    };
    entry.keys.push(addressOf(node));
    byValue.set(json, entry);
  }
  const present = [...byValue.values()]
    .filter((lane) => lane.value !== null)
    .sort((a, b) => compare(a.value, b.value));
  const missing = byValue.get(null);
  const ordered = [...present, ...(missing ? [missing] : [])];
  if (ordered.length === 0) {
    ordered.push({ value: null, caption: "", keys: [], y: 0 });
  }
  ordered.forEach((lane, index) => {
    lane.y = index * LANE_HEIGHT + LANE_HEIGHT / 2;
  });
  return ordered;
}

// --- Reading the parameters ------------------------------------------

function readParams(params) {
  const p = params && typeof params === "object" ? params : {};
  const label = p[PARAM_AXIS_LABEL];
  return {
    axisField: field(p[PARAM_AXIS_FIELD]),
    axisEndField: field(p[PARAM_AXIS_END_FIELD]),
    laneBy: field(p[PARAM_LANE_BY]),
    // An axis with no label is an axis with no label, and not one
    // labelled with the empty string: bandMarks draws no caption for it,
    // because a label element with no text is something a reader cannot
    // see and a test can.
    axisLabel: typeof label === "string" && label !== "" ? label : null,
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

function labelOf(node) {
  const json = valueJSON(node, "label");
  return json === null ? text(node.name) : labelFor(json);
}

// valueOf is a node's value for a declared field key, from `attrs` or
// from `fields`.
//
// `axis_field` names a **declared field key**, which reaches the
// envelope through `project.fields` (internal/views/renderers.go's
// kindAxisField requires exactly that) and through `include_fields` on a
// run that asked for one. Both are the game's own value for this node.
//
// `hasOwnProperty` and not a falsiness test: a field absent and a field
// carrying 0 — or the first option of an enum — are two answers.
function valueOf(node, key) {
  if (key === null) return undefined;
  for (const bag of [node.attrs, node.fields]) {
    if (!isObject(bag) || !Object.prototype.hasOwnProperty.call(bag, key)) continue;
    return bag[key];
  }
  return undefined;
}

// valueText is a value as the frame would write it, through the
// palette's own rule, so a band and a caption say the same characters
// about the same value.
function valueText(node, key) {
  const value = valueOf(node, key);
  return value === undefined ? "" : labelFor(JSON.stringify(value));
}

function valueJSON(node, key) {
  const value = valueOf(node, key);
  return value === undefined ? null : JSON.stringify(value);
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
