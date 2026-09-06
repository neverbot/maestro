// The scene-layer harness for internal/web/static/palette.js: it imports
// the real, unmodified module and asserts over the plain data it
// returns. No DOM stub is needed here and none is provided — palette.js
// is pure, and this file is the first place that purity is spent.
//
// What this covers that no Go test can: the identity's central mechanical
// rule (a hue belongs to a value's text, not to its rank in the result),
// and the envelope guarantee the legend has to carry forward (a slot that
// found nothing is absent, not empty, so *not set* and *the empty string*
// are two rows).
//
// Run directly: `node internal/web/jstest/palette_test.mjs`.
// internal/web/static_appjs_browser_test.go shells out to it too.

import {
  DATA_SLOTS,
  HATCH_FILL,
  HATCH_GROUND,
  HATCH_PATTERN_ID,
  HATCH_STROKE,
  UNSET_LABEL,
  fillFor,
  hueFor,
  legendFor,
} from "../static/palette.js";

let failures = 0;

function check(name, fn) {
  try {
    fn();
    console.log("ok   " + name);
  } catch (err) {
    failures++;
    console.error("FAIL " + name + ": " + (err && err.message ? err.message : err));
  }
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function assertEqual(actual, expected, message) {
  if (actual !== expected) {
    throw new Error(`${message}: got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);
  }
}

// A deterministic shuffle, so a failure here is reproducible rather than
// a flake somebody reruns until it passes.
function shuffled(items, seed) {
  const out = [...items];
  let state = seed;
  for (let i = out.length - 1; i > 0; i--) {
    state = (state * 1103515245 + 12345) % 2147483648;
    const j = state % (i + 1);
    [out[i], out[j]] = [out[j], out[i]];
  }
  return out;
}

function node(key, attrs) {
  return { key, attrs };
}

function zones(...names) {
  return names.map((name, i) => node("q" + i, { zone: name }));
}

function rowFor(result, label) {
  return result.rows.find((row) => row.label === label);
}

// hueIsStableAcrossResultOrder is the mechanical heart of the identity:
// the slot is a property of the value's text alone. Rank assignment would
// pass every other test in this file and still recolour the whole picture
// the day somebody adds a zone.
check("hueIsStableAcrossResultOrder", () => {
  const names = ["Duskwood", "Elwynn", "Westfall", "Redridge", "Stranglethorn"];
  const nodes = zones(...names);
  const baseline = legendFor(nodes, "zone");
  const byValue = new Map(baseline.rows.map((row) => [row.value, row.index]));

  for (const seed of [1, 7, 99, 12345]) {
    const other = legendFor(shuffled(nodes, seed), "zone");
    for (const row of other.rows) {
      assertEqual(row.index, byValue.get(row.value), `slot for ${row.value} under seed ${seed}`);
    }
  }

  // And the slot is what hueFor says it is, for the text, with nothing
  // else in the room: a legend that quietly renumbered would be caught
  // here even if it renumbered consistently.
  for (const row of baseline.rows) {
    assertEqual(row.index, hueFor(row.value), `slot for ${row.value} is hueFor(text)`);
  }

  // Adding a value must not move the ones already there. This is the
  // property a saved view exists for, asserted directly.
  const grown = legendFor([...nodes, node("q9", { zone: "Duskwood-2" })], "zone");
  for (const row of grown.rows) {
    if (byValue.has(row.value)) {
      assertEqual(row.index, byValue.get(row.value), `slot for ${row.value} after a zone was added`);
    }
  }
});

check("absentAndEmptyStringAreTwoRows", () => {
  const nodes = [
    node("a", {}),
    node("b", { zone: "" }),
    node("c", { zone: "" }),
  ];
  const { rows } = legendFor(nodes, "zone");

  assertEqual(rows.length, 2, "rows");
  const empty = rows.find((row) => row.kind === "hue");
  const unset = rows.find((row) => row.kind === "unset");
  assert(empty && unset, "one hue row for the empty string and one unset row");
  assertEqual(empty.value, '""', "the empty string keeps its own JSON text");
  assertEqual(empty.count, 2, "the empty string's count");
  assertEqual(unset.count, 1, "the unset count");
  assertEqual(unset.label, UNSET_LABEL, "the unset label");
  assert(empty.count !== unset.count, "the two rows carry two counts, not one folded count");

  // A slot present with an explicit null is a value the game means, not
  // an absence: presence is the test, never truthiness.
  const withNull = legendFor([node("a", { zone: null }), node("b", {})], "zone");
  assertEqual(withNull.rows.length, 2, "an explicit null and an absent slot are two rows");
  assertEqual(rowFor(withNull, "null").kind, "hue", "an explicit null is a value");
});

check("theNinthValueGoesToTheHatchedTail", () => {
  // Eleven zones: eight frequent ones and three that appear once each.
  const nodes = [];
  const frequent = ["a", "b", "c", "d", "e", "f", "g", "h"];
  frequent.forEach((name, i) => {
    for (let n = 0; n < 10 - i; n++) nodes.push(node(`${name}${n}`, { zone: name }));
  });
  for (const rare of ["x", "y", "z"]) nodes.push(node(rare, { zone: rare }));

  const { rows, byValue } = legendFor(nodes, "zone");
  const hues = rows.filter((row) => row.kind === "hue");
  const hatch = rows.filter((row) => row.kind === "hatch");

  assertEqual(hues.length, DATA_SLOTS, "hue rows");
  assertEqual(hatch.length, 1, "hatched rows");
  assertEqual(hatch[0].members.length, 3, "tail members");
  assertEqual(hatch[0].label, "other (3 values)", "tail label");
  assertEqual(hatch[0].count, 3, "tail count is nodes, not values");
  assert(hatch[0].members.includes('"x"'), "the tail carries its member list");

  // Every tail value resolves to the one hatched row, so a node in the
  // tail can find its paint without the caller re-deriving any of this.
  for (const rare of ['"x"', '"y"', '"z"']) {
    assertEqual(byValue.get(rare), hatch[0], `byValue for ${rare}`);
  }
  assertEqual(rows[rows.length - 1], hatch[0], "the tail is drawn after the hues");
});

check("theTailIsChosenByFrequencyNotByName", () => {
  const nodes = [];
  // "aardvark" is alphabetically first and appears once; the other nine
  // are common. Frequency puts it in the tail; a sort by name would not.
  nodes.push(node("rare", { zone: "aardvark" }));
  for (const name of ["b", "c", "d", "e", "f", "g", "h", "i", "j"]) {
    for (let n = 0; n < 5; n++) nodes.push(node(`${name}${n}`, { zone: name }));
  }

  const { rows, byValue } = legendFor(nodes, "zone");
  const hatch = rows.find((row) => row.kind === "hatch");
  assert(hatch, "a tail exists");
  assert(hatch.members.includes('"aardvark"'), "the rarest value is in the tail");
  assertEqual(byValue.get('"aardvark"').kind, "hatch", "the rarest value paints hatched");
  assert(
    !rows.some((row) => row.kind === "hue" && row.value === '"aardvark"'),
    "the rarest value did not take a hue",
  );
});

check("aNumberAndItsStringAreDifferentValues", () => {
  const { rows } = legendFor([node("a", { level: 20 }), node("b", { level: "20" })], "level");
  assertEqual(rows.length, 2, "rows");
  const values = rows.map((row) => row.value).sort();
  assertEqual(values.join("|"), '"20"|20', "the two JSON texts are kept apart");
  assertEqual(hueFor("20"), hueFor("20"), "hueFor is a function of its argument");
  // They may collide onto one hue — that is the stated price of hashing —
  // but they are never one row.
  assert(rows[0].value !== rows[1].value, "two rows, two values");
});

check("legendRowsAreOrderedByCountThenValue", () => {
  const nodes = [
    ...zones("beta", "beta", "beta"),
    ...zones("alpha", "alpha", "alpha"),
    ...zones("gamma"),
    node("u1", {}),
    node("u2", {}),
  ];

  for (const seed of [3, 4242]) {
    const { rows } = legendFor(shuffled(nodes, seed), "zone");
    const shape = rows.map((row) => `${row.kind}:${row.label}:${row.count}`).join(",");
    assertEqual(
      shape,
      `hue:alpha:3,hue:beta:3,hue:gamma:1,unset:${UNSET_LABEL}:2`,
      `ordering under seed ${seed}`,
    );
  }
});

// fillFor is the only place a slot becomes paint. It names tokens, never
// hex: the light and dark sets are two declarations of the same names,
// and a mark that reached for a hex value would be right in one theme.
check("fillForNamesATokenAndNeverAHexValue", () => {
  const { rows } = legendFor(
    [
      ...Array.from({ length: DATA_SLOTS + 1 }, (_, i) =>
        node("n" + i, { zone: "zone-" + i }),
      ),
      node("absent", {}),
    ],
    "zone",
  );

  const seen = new Set();
  for (const row of rows) {
    const fill = fillFor(row);
    seen.add(fill.kind);
    // The tail is the one paint that is not a colour, and that is the
    // whole of it: a flat fill among eight flat fills is a ninth colour.
    // It names a paint server, and the pattern that server resolves to
    // is drawn in tokens — asserted below and in canvas_test.mjs, which
    // is where the pattern is emitted.
    if (row.kind === "hatch") {
      assertEqual(fill.css, HATCH_FILL, `${row.label} paints through the hatch`);
      assert(fill.css.includes(HATCH_PATTERN_ID), "and names the pattern the emitter defines");
    } else {
      assert(fill.css.startsWith("var(--"), `${row.label} paints through a token: ${fill.css}`);
    }
    if (row.kind === "hue") {
      assertEqual(fill.css, `var(--data-${row.index + 1})`, `token for ${row.label}`);
      assert(row.index >= 0 && row.index < DATA_SLOTS, "the slot is within the palette");
    }
  }
  assertEqual([...seen].sort().join(","), "hatch,hue,unset", "all three kinds were exercised");
  assertEqual(fillFor(null).css, "var(--unset)", "an unknown row paints as unset, not as data");

  // **The tail is not one of the eight, and the assertion is that it is
  // not *any* of them.** This is the check the browser round added: the
  // tail was `var(--line-strong)`, which passes every "names a token"
  // test ever written and reads as a ninth colour on screen.
  const hatch = fillFor({ kind: "hatch" });
  for (let i = 1; i <= DATA_SLOTS; i += 1) {
    assert(hatch.css !== `var(--data-${i})`, "the tail wears one of the eight hues");
  }
  assert(!hatch.css.startsWith("var(--"), "the tail is a flat colour, which is a ninth category to a reader");
  assert(hatch.css.startsWith("url(#"), "the tail names a paint server, which is what makes it a texture");
  // The hatch's own colours are tokens, so both themes stay the
  // stylesheet's business.
  for (const token of [HATCH_STROKE, HATCH_GROUND]) {
    assert(token.startsWith("var(--"), `the hatch paints through a token: ${token}`);
  }
});

if (failures > 0) {
  console.error(`${failures} palette check(s) failed`);
  process.exit(1);
}
console.log("palette.js: all checks passed");
