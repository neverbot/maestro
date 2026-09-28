// The data palette: the pure half of "every hue on screen belongs to the
// game" (interface design spec §2.5).
export const DATA_SLOTS = 8;

// The eight tokens, written out rather than assembled from an index.
// Two reasons, and the second is the load-bearing one: a constructed
// name like `var(--data-${i})` is invisible to the source-shape guard in
// internal/web/static_tokens_test.go, which reads every asset for
// `var(--…)` and holds that the declared set and the referenced set are
// the same set in both directions. A palette that grew a ninth slot
// without a ninth token would pass a guard that could not see it.
const DATA_TOKENS = [
  "var(--data-1)",
  "var(--data-2)",
  "var(--data-3)",
  "var(--data-4)",
  "var(--data-5)",
  "var(--data-6)",
  "var(--data-7)",
  "var(--data-8)",
];

// **What a label printed on one of these hues is set in.**
const HUE_LABEL_FILL = "var(--paper)";
const PLAIN_LABEL_FILL = "var(--ink)";

export function labelOn(fill) {
  return DATA_TOKENS.includes(fill) ? HUE_LABEL_FILL : PLAIN_LABEL_FILL;
}

// hueFor assigns a slot by hashing the value's JSON text, never by its
// rank in the result.
const FNV_OFFSET = 2166136261;
const FNV_PRIME = 16777619;

export function hueFor(jsonText) {
  const bytes = new TextEncoder().encode(String(jsonText));
  let hash = FNV_OFFSET;
  for (const byte of bytes) {
    hash ^= byte;
    hash = Math.imul(hash, FNV_PRIME) >>> 0;
  }
  return hash % DATA_SLOTS;
}

// ROW_UNSET_LABEL is what a slot that found nothing is called on screen.
// The envelope guarantees "a slot that found nothing is absent, not
// empty" (internal/views/execute.go), so *not set* and *the empty
// string* are two different answers and get two different rows. Folding
// them together would throw away a guarantee the server went out of its
// way to provide.
export const UNSET_LABEL = "not set";

// legendFor turns the nodes' values for one slot into the legend.
export function legendFor(nodes, slot) {
  const counts = new Map();
  let unsetCount = 0;

  for (const node of nodes ?? []) {
    const attrs = node?.attrs;
    // `slot in attrs` and not `attrs[slot] === undefined`: the envelope's
    // distinction is presence, and a slot present with a null value is a
    // value the game means.
    if (!attrs || !Object.prototype.hasOwnProperty.call(attrs, slot)) {
      unsetCount++;
      continue;
    }
    const text = JSON.stringify(attrs[slot]);
    counts.set(text, (counts.get(text) ?? 0) + 1);
  }

  const ordered = [...counts.entries()].sort((a, b) => {
    if (b[1] !== a[1]) return b[1] - a[1];
    return a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0;
  });

  const rows = [];
  const byValue = new Map();

  // The eight most frequent values take the eight hues. Frequency, not
  // name: the tail is the long thin part of the distribution, and
  // choosing it alphabetically would hatch a zone with forty quests in
  // it because it happens to start with a Z.
  // **A hue is a value's own, unless another value in this picture wants
  // it.** `hueFor` hashes a value to one of eight slots, which is what
  // makes a zone the same colour in every view that mentions it — and
  // two values whose hashes land on the same slot got the same colour in
  // one picture. Seen on the demo game: `neutral` and `alliance`, two
  // legend rows with one swatch between them, in a legend whose whole
  // job is telling values apart.
  const taken = new Set();
  for (const [value, count] of ordered.slice(0, DATA_SLOTS)) {
    let index = hueFor(value);
    for (let step = 0; taken.has(index) && step < DATA_SLOTS; step += 1) {
      index = (index + 1) % DATA_SLOTS;
    }
    taken.add(index);
    const row = {
      kind: "hue",
      value,
      label: labelFor(value),
      count,
      index,
      members: null,
    };
    rows.push(row);
    byValue.set(value, row);
  }

  const tail = ordered.slice(DATA_SLOTS);
  if (tail.length > 0) {
    const row = {
      kind: "hatch",
      value: null,
      label: `other (${tail.length} values)`,
      count: tail.reduce((sum, entry) => sum + entry[1], 0),
      index: null,
      members: tail.map((entry) => entry[0]),
    };
    rows.push(row);
    for (const [value] of tail) byValue.set(value, row);
  }

  if (unsetCount > 0) {
    rows.push({
      kind: "unset",
      value: null,
      label: UNSET_LABEL,
      count: unsetCount,
      index: null,
      members: null,
    });
  }

  return { rows, byValue };
}

// labelFor is what a value is called on screen. A string is shown as the
// game wrote it; anything else is shown as its JSON text, so a number,
// a boolean and null are visibly not strings — which is the same
// distinction that gives `20` and `"20"` two rows.
export function labelFor(jsonText) {
  return jsonText.startsWith('"') ? JSON.parse(jsonText) : jsonText;
}

// The tail's paint, and **the whole of why it is a paint server and not
// a colour.**
export const HATCH_PATTERN_ID = "mst-hatch";
export const HATCH_FILL = `url(#${HATCH_PATTERN_ID})`;
// The token the hatch's own strokes are drawn in, and the ground behind
// them. Named here rather than in the emitter so that "what colour is
// the tail" has one answer, in the module that owns every other answer
// to that question.
export const HATCH_STROKE = "var(--line-strong)";
export const HATCH_GROUND = "var(--paper)";
// The hatch's geometry, in the drawing's own coordinates: a stripe every
// PITCH units, WIDTH units thick, at 45°. The pitch is a fraction of a
// node's height (render/marks.js's NODE_H is 44), so a tail node carries
// several stripes rather than one, and a node too small to show a stripe
// is a node too small to show a hue either.
export const HATCH_PITCH = 7;
export const HATCH_WIDTH = 2.5;

// fillFor turns a legend row into the paint a mark wears.
export function fillFor(row) {
  switch (row?.kind) {
    case "hue":
      return { kind: "hue", index: row.index, css: DATA_TOKENS[row.index] };
    case "hatch":
      return { kind: "hatch", index: null, css: HATCH_FILL };
    default:
      return { kind: "unset", index: null, css: "var(--unset)" };
  }
}
