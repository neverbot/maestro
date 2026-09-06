// The data palette: the pure half of "every hue on screen belongs to the
// game" (interface design spec §2.5).
//
// This module is a function from a value to a slot and nothing else. It
// has no DOM, no state, no fetch and no import: a renderer, the legend,
// the text twin and a Node harness all reach it, and the only reason
// they can is that it is pure. Everything that decides *which* hue a
// value wears lives here, so it is data a test can read and a mutation
// can turn red.
//
// The tokens themselves are declared in styles.css, twice — once for the
// light ground and once for the dark one — and this module names them by
// `var(--data-N)` rather than by hex, so the theme is the stylesheet's
// business and never this file's.

// The eight categorical hues. Eight, not nine: a ninth hue is not
// distinguishable from its neighbours at 11px over a busy canvas, and a
// palette that grows with the data is a palette with no meaning at all.
// The ninth-and-beyond values go to the hatched tail (see legendFor).
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

// hueFor assigns a slot by hashing the value's JSON text, never by its
// rank in the result.
//
// Rank assignment would recolour the whole picture the day somebody adds
// a zone, which destroys the one property a saved view exists for: being
// recognisable across the months a designer lives inside it. The same
// argument the views spec makes for geometry (an unseeded force layout),
// made for colour.
//
// The cost is collisions: two values can land on one hue, and this
// function does not try to avoid it. A collision is stable — the same
// two values collide tomorrow — where a de-collided assignment would be
// stable only until the result set changed, which is the failure being
// avoided. The legend row and the node label carry the text, and colour
// is never the only carrier (spec §2.5).
//
// The hash is FNV-1a over the UTF-8 bytes of the value's JSON text. JSON
// text, so a projected number and the string of it are different values,
// matching the envelope's own type preservation; UTF-8 bytes rather than
// JS string code units, so the assignment is a property of the value and
// not of the representation the host happens to use for strings.
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
//
// Returns `{rows, byValue}`:
//
//   rows    — the legend, in the order it is drawn: the hue rows first,
//             ordered by count descending then by JSON text ascending;
//             then the hatched tail, if there is one; then the unset
//             row, if there is one. Deterministic for a given multiset
//             of values, and independent of the order the nodes arrived
//             in — the same property hueFor has, for the same reason.
//   byValue — a Map from a value's JSON text to the row that carries it,
//             which is how a node finds its own fill without the caller
//             re-deriving any of this.
//
// A row is `{kind, value, label, count, members}`:
//   kind    — "hue" | "hatch" | "unset"
//   value   — the JSON text, for "hue" rows; null otherwise
//   label   — what a reader sees
//   count   — how many nodes are in this row
//   members — the tail's JSON texts, for "hatch"; null otherwise
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
  for (const [value, count] of ordered.slice(0, DATA_SLOTS)) {
    const row = {
      kind: "hue",
      value,
      label: labelFor(value),
      count,
      index: hueFor(value),
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
function labelFor(jsonText) {
  return jsonText.startsWith('"') ? JSON.parse(jsonText) : jsonText;
}

// fillFor turns a legend row into the paint a mark wears.
//
// `css` names a custom property rather than a hex value: the light and
// dark sets are two declarations of the same token names (spec §2.3), so
// a mark painted through this function is correct in both themes without
// this module knowing either one exists.
//
// An unset mark gets no fill at all — `var(--unset)` is `transparent` in
// both themes — and is told apart by its dashed outline, which is a
// second, non-colour carrier. `--line-strong` and not `--line`: the
// dashed outline is the whole of the signal here, so it is a meaningful
// graphical object and has to reach 3:1 against the canvas, where a
// decorative hairline does not.
export function fillFor(row) {
  switch (row?.kind) {
    case "hue":
      return { kind: "hue", index: row.index, css: DATA_TOKENS[row.index] };
    case "hatch":
      return { kind: "hatch", index: null, css: "var(--line-strong)" };
    default:
      return { kind: "unset", index: null, css: "var(--unset)" };
  }
}
