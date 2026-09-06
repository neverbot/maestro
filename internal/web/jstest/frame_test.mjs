// The harness for internal/web/static/render/scene.js: it imports the
// real, unmodified module and asserts over the plain data it returns.
//
// What this covers that no Go test can. The frame is the one place a
// designer learns that the drawing in front of them is not the whole
// truth, and every rule that makes it honest is a property of the
// JavaScript: that three truncation flags are three sentences, that a
// clean envelope produces **no** reassurance of any kind, that ambiguity
// counts nodes rather than slots because the flag is on the node, that a
// stale view under the failing policy draws no picture at all, that a
// best-effort picture bands permanently and names what it lost in the
// server's own words, that a rename is a quiet line and never a band,
// that an unbound parameter is answered in the bar, and that an empty
// answer is a success that guesses at no cause.
//
// Run directly: `node internal/web/jstest/frame_test.mjs`.
// internal/web/static_appjs_browser_test.go shells out to it too.

import { readParams, writeParams } from "../static/client.js";
import {
  ACTION_RUN_BEST_EFFORT,
  BANNER_AMBIGUOUS,
  BANNER_DROPPED,
  BANNER_ORDER,
  BANNER_PLACED,
  BANNER_STALE,
  BANNER_TRUNCATED_DEPTH,
  BANNER_TRUNCATED_EDGES,
  BANNER_TRUNCATED_NODES,
  KIND_DIAGNOSTICS,
  KIND_EMPTY,
  KIND_PICTURE,
  KIND_UNBOUND,
  ON_STALE_BEST_EFFORT,
  bannersFor,
  frameFor,
  runAction,
} from "../static/render/scene.js";

let failures = 0;
const pending = [];

function check(name, fn) {
  pending.push([name, fn]);
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function assertEqual(actual, expected, message) {
  if (actual !== expected) {
    throw new Error(`${message}: got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);
  }
}

function assertDeepEqual(actual, expected, message) {
  const a = JSON.stringify(actual);
  const b = JSON.stringify(expected);
  if (a !== b) throw new Error(`${message}: got ${a}, want ${b}`);
}

// textOf collects every string anywhere in the emitted tree, which is
// what a designer would read once the component has painted it. The
// assertions about words the frame must *not* say are made over this,
// because a forbidden word is forbidden wherever it is put.
function textOf(value, out = []) {
  if (typeof value === "string") out.push(value);
  else if (Array.isArray(value)) for (const item of value) textOf(item, out);
  else if (value && typeof value === "object") for (const key of Object.keys(value)) textOf(value[key], out);
  return out;
}

function joinedText(frame) {
  return textOf(frame).join("   ").toLowerCase();
}

function codesOf(banners) {
  return banners.map((banner) => banner.code);
}

// --- The fixtures ----------------------------------------------------
//
// Each of these is built so that it can produce **one** of the frame's
// states and no other; everyThingHasItsOwnState below asserts exactly
// that over the whole set, because a fixture too small to tell two
// outcomes apart is a test that passes for the wrong reason.

const view = { key: "world", name: "The world", renderer: "graph" };

function envelope(over = {}) {
  return {
    nodes: [
      { id: "n1", key: "elwynn", type: "zone", name: "Elwynn", set: "zones", attrs: { label: "Elwynn" } },
      { id: "n2", key: "duskwood", type: "zone", name: "Duskwood", set: "zones", attrs: { label: "Duskwood" } },
    ],
    edges: [{ id: "e1", type: "connects_to", source: "n1", target: "n2" }],
    stats: { nodes: 2, edges: 1, max_depth_reached: 1, duration_ms: 7 },
    truncated: { nodes: false, edges: false, depth: false },
    ...over,
  };
}

// A clean envelope: nothing flagged, nothing stale, nothing ambiguous.
const clean = envelope();

// An envelope whose every truncation flag is set. Only this fixture can
// produce three truncation bands.
const truncatedEverywhere = envelope({ truncated: { nodes: true, edges: true, depth: true } });

// One flag, and deliberately not the first: a builder that emitted the
// stack in flag order regardless of which flags were set would pass a
// nodes-only fixture.
const truncatedEdgesOnly = envelope({ truncated: { nodes: false, edges: true, depth: false } });

// Two ambiguous nodes with two projected slots each. The count the band
// reports is 2 and not 4: `node.ambiguous` is a flag on the node, and
// which slot it was is a distinction internal/views/execute.go
// explicitly declines to carry.
const ambiguousEnvelope = envelope({
  nodes: [
    {
      id: "n1",
      key: "elwynn",
      type: "zone",
      name: "Elwynn",
      set: "zones",
      attrs: { label: "Elwynn", color_by: "alliance", size_by: 3 },
      ambiguous: true,
    },
    {
      id: "n2",
      key: "duskwood",
      type: "zone",
      name: "Duskwood",
      set: "zones",
      attrs: { label: "Duskwood", color_by: "alliance", size_by: 4 },
      ambiguous: true,
    },
    {
      id: "n3",
      key: "westfall",
      type: "zone",
      name: "Westfall",
      set: "zones",
      attrs: { label: "Westfall", color_by: "alliance", size_by: 2 },
    },
  ],
  stats: { nodes: 3, edges: 1, max_depth_reached: 1, duration_ms: 7 },
});

// A run that matched nothing, with nothing else to say about it.
const emptyEnvelope = {
  nodes: [],
  edges: [],
  stats: { nodes: 0, edges: 0, max_depth_reached: 0, duration_ms: 4 },
  truncated: { nodes: false, edges: false, depth: false },
};

// A full and correct picture beside a rename: the reference resolved by
// id, so nothing is missing and nothing is a band.
const renamedEnvelope = envelope({
  stale: [{ code: "entity_type_renamed", pointer: "/from/0/type", was: "zone", now: "region" }],
});

// A best-effort picture: the envelope carries the diagnostics, and the
// caller carries what the refusal it overrode said, sentences included.
const bestEffortEnvelope = envelope({
  stale: [{ code: "entity_type_missing", pointer: "/from/1/type", was: "faction" }],
});

const missingSentence =
  'this view names the entity type "faction" and this game no longer has it: declare it ' +
  'again with types.upsert, point this reference at another type, or run with on_stale ' +
  '"best_effort" to draw what is left';

const droppedRefs = [
  {
    code: "entity_type_missing",
    pointer: "/from/1/type",
    was: "faction",
    message: missingSentence,
  },
];

// The refusal itself, as internal/web puts it on the wire: the sentences
// under details.fields, the machine-readable half under details.stale,
// joined on the pointer.
const staleRefusal = {
  code: "query_stale",
  message: "query_stale: " + missingSentence,
  pointer: "/from/1/type",
  status: 409,
  details: {
    fields: [{ path: "/from/1/type", message: missingSentence }],
    stale: [{ code: "entity_type_missing", pointer: "/from/1/type", was: "faction" }],
  },
};

// A refusal that is not staleness. Best effort cannot answer it, so no
// action is offered — which is what tells this state apart from the one
// above in everyThingHasItsOwnState.
const invalidRefusal = {
  code: "query_invalid",
  message: "query_invalid: the depth bound must be a positive integer",
  pointer: "/traverse/0/depth",
  status: 400,
  details: { fields: [{ path: "/traverse/0/depth", message: "the depth bound must be a positive integer" }] },
};

const unboundSentence =
  'the parameter "class" has no value for this run: give it a default in params, or supply ' +
  "it when running the view";

const unboundRefusal = {
  code: "query_stale",
  message: "query_stale: " + unboundSentence,
  pointer: "/from/0/where/value",
  status: 409,
  details: {
    fields: [{ path: "/from/0/where/value", message: unboundSentence }],
    stale: [{ code: "param_unbound", pointer: "/from/0/where/value", was: "class" }],
  },
};

const declarations = [
  { key: "class", type: "text" },
  { key: "depth", type: "number" },
];

// --- The truncation flags --------------------------------------------

check("threeTruncationFlagsAreThreeSentences", () => {
  const banners = bannersFor(truncatedEverywhere, {});
  assertDeepEqual(
    codesOf(banners),
    [BANNER_TRUNCATED_NODES, BANNER_TRUNCATED_EDGES, BANNER_TRUNCATED_DEPTH],
    "three flags are three bands",
  );
  const texts = new Set(banners.map((banner) => banner.text));
  assertEqual(texts.size, 3, "and three different sentences, never one summary");
});

check("oneTruncationFlagIsOneSentence", () => {
  const banners = bannersFor(truncatedEdgesOnly, {});
  assertDeepEqual(codesOf(banners), [BANNER_TRUNCATED_EDGES], "only the flag that is set is spoken");
});

check("aCleanEnvelopeProducesNoCompletenessClaim", () => {
  const banners = bannersFor(clean, {});
  assertDeepEqual(codesOf(banners), [], "a clean envelope bands nothing at all");
  const frame = frameFor({ view, envelope: clean, declarations, params: { class: "mage" } });
  assertEqual(frame.kind, KIND_PICTURE, "a clean envelope is a picture");
  const text = joinedText(frame);
  assert(!text.includes("complete"), `the frame claimed completeness: ${text}`);
  assert(!text.includes("all"), `the frame spoke for the whole answer: ${text}`);
  assertEqual(frame.message, "", "and it says nothing in the canvas either");
});

// --- Ambiguity -------------------------------------------------------

check("ambiguityCountsNodesNotSlots", () => {
  const banners = bannersFor(ambiguousEnvelope, {});
  assertDeepEqual(codesOf(banners), [BANNER_AMBIGUOUS], "one band");
  assert(banners[0].text.startsWith("2 nodes "), `the count is nodes, not slots: ${banners[0].text}`);
  assert(banners[0].text.includes("nodes"), "and the sentence names nodes");
  assert(!banners[0].text.includes("slot"), "and never a slot, because the flag is not on one");
});

// --- The stale refusal -----------------------------------------------

check("aStaleFailureDrawsNoScene", () => {
  // The envelope of the previous, successful run is deliberately handed
  // in beside the refusal: a refusal draws no picture even when there is
  // a picture lying around to draw.
  const frame = frameFor({ view, envelope: clean, error: staleRefusal, declarations });
  assertEqual(frame.kind, KIND_DIAGNOSTICS, "no scene, a panel");
  assertDeepEqual(frame.banners, [], "and no bands: the panel is the whole statement");
  const text = joinedText(frame);
  assert(!text.includes("elwynn"), "no node from the stale envelope reached the frame");
  assert(!text.includes("duskwood"), "nor the second one");
  assertEqual(frame.diagnostics.rows.length, 1, "one row per diagnostic");
  const row = frame.diagnostics.rows[0];
  assertEqual(row.message, missingSentence, "the server's sentence, character for character");
  assertEqual(row.pointer, "/from/1/type", "and the pointer it addressed it with");
  assertEqual(row.was, "faction", "and what the document says");
  assertDeepEqual(
    frame.actions,
    [{ code: ACTION_RUN_BEST_EFFORT, onStale: ON_STALE_BEST_EFFORT }],
    "one explicit action, and one only",
  );
});

check("theBestEffortActionRerunsWithTheFlag", async () => {
  const calls = [];
  const stub = {
    runView(key, params, options) {
      calls.push({ key, params, options });
      return Promise.resolve({ ok: true, result: bestEffortEnvelope });
    },
  };
  const frame = frameFor({ view, error: staleRefusal, declarations, params: { class: "mage" } });
  await runAction(frame.actions[0], stub, { key: frame.title.key, params: { class: "mage" } });
  assertEqual(calls.length, 1, "the action ran the view");
  assertEqual(calls[0].key, "world", "by key");
  assertDeepEqual(calls[0].params, { class: "mage" }, "with the same question");
  assertEqual(calls[0].options.onStale, "best_effort", "and with the flag the designer chose");
});

check("anInvalidRefusalOffersNoBestEffortAction", () => {
  const frame = frameFor({ view, error: invalidRefusal, declarations });
  assertEqual(frame.kind, KIND_DIAGNOSTICS, "a refusal is a panel");
  assertDeepEqual(frame.actions, [], "and best effort cannot answer a document that is simply wrong");
  assertEqual(frame.diagnostics.rows[0].message, invalidRefusal.details.fields[0].message, "verbatim");
  assertEqual(frame.diagnostics.rows[0].code, "", "with no staleness code invented for it");
});

// --- The best-effort picture -----------------------------------------

check("aBestEffortPictureBandsPermanentlyAndGreysTheDropped", () => {
  const banners = bannersFor(bestEffortEnvelope, { droppedRefs });
  assertDeepEqual(codesOf(banners), [BANNER_DROPPED], "the picture is banded");
  const band = banners[0];
  assertEqual(band.css, "var(--dropped)", "in the token that means what best effort removed");
  assertEqual(band.rows.length, 1, "naming what was dropped");
  assertEqual(band.rows[0].message, missingSentence, "in the server's own sentence");
  assertEqual(band.rows[0].pointer, "/from/1/type", "at the pointer it named");
  assertEqual(band.rows[0].css, "var(--dropped)", "and the dropped row is greyed");
  assert(!joinedText({ band }).includes("--danger"), "a best-effort picture is not a refusal");
});

check("theStaleBandAndTheDroppedBandAreNeverBothSaid", () => {
  const withRefusal = codesOf(bannersFor(bestEffortEnvelope, { droppedRefs }));
  const withoutRefusal = codesOf(bannersFor(bestEffortEnvelope, {}));
  assertDeepEqual(withRefusal, [BANNER_DROPPED], "the refusal's own words win when the caller has them");
  assertDeepEqual(withoutRefusal, [BANNER_STALE], "and the envelope answers alone when it does not");
  assert(
    !withRefusal.includes(BANNER_STALE) && !withoutRefusal.includes(BANNER_DROPPED),
    "one fact is stated once",
  );
});

// --- The rename ------------------------------------------------------

check("aRenamedDiagnosticIsNotABannerAndOffersNoRepair", () => {
  const frame = frameFor({ view, envelope: renamedEnvelope, declarations });
  assertDeepEqual(frame.banners, [], "a rename is never a band: the picture is full and correct");
  assert(frame.title.renamed !== null, "it is a line in the title strip");
  assertEqual(frame.title.renamed.count, 1, "counting what moved");
  assertEqual(frame.title.renamed.rows[0].pointer, "/from/0/type", "and expanding to the pointer");
  assertEqual(frame.title.renamed.rows[0].now, "region", "with the spelling the game uses now");
  assertDeepEqual(frame.actions, [], "and no action at all");
  const text = joinedText(frame);
  for (const verb of ["repair", "fix", "rewrite", "update", "apply"]) {
    assert(!text.includes(verb), `the frame offered to ${verb} the document`);
  }
});

// --- The unbound parameter -------------------------------------------

check("anUnboundParameterHighlightsItsControl", () => {
  const frame = frameFor({ view, error: unboundRefusal, declarations, params: {} });
  assertEqual(frame.kind, KIND_UNBOUND, "the bar answers it, not a pointer");
  assertEqual(frame.diagnostics, null, "so the diagnostics panel is not shown");
  assert(frame.bar.present, "the bar is");
  const marked = frame.bar.controls.filter((control) => control.marked).map((control) => control.key);
  assertDeepEqual(marked, ["class"], "and it marks the control that has no value");
  const control = frame.bar.controls.find((c) => c.key === "class");
  assertEqual(control.message, unboundSentence, "carrying the server's own sentence");
  assertDeepEqual(frame.actions, [], "and offering no run-anyway, because a value is one field away");
});

// --- The empty answer ------------------------------------------------

check("anEmptyAnswerIsASuccess", () => {
  const frame = frameFor({
    view,
    envelope: emptyEnvelope,
    declarations,
    params: { class: "mage" },
    sets: ["zones"],
  });
  assertEqual(frame.kind, KIND_EMPTY, "an empty answer is its own state and it is a success");
  const text = joinedText(frame);
  assert(!text.includes("--danger"), "it borrows no error styling");
  assert(frame.footer.text.startsWith("0 nodes, 0 edges"), `the footer counts it: ${frame.footer.text}`);
  assertEqual(frame.message, "This view matched nothing.", "one sentence");
  assertEqual(frame.ran.renderer, "graph", "and what ran: the renderer");
  assertDeepEqual(frame.ran.sets, ["zones"], "the sets");
  assertDeepEqual(frame.ran.params, [{ key: "class", value: "mage" }], "and the parameter values in force");
});

check("anEmptyAnswerGuessesNoCause", () => {
  const frame = frameFor({
    view,
    envelope: emptyEnvelope,
    declarations,
    params: { class: "mage" },
    sets: ["zones"],
  });
  const text = joinedText(frame);
  for (const word of ["try", "filter", "of"]) {
    assert(!text.includes(word), `the empty state guessed at a cause with ${word}: ${text}`);
  }
});

// --- The footer ------------------------------------------------------

check("theFooterKeepsTheQueryAndTheLayoutApart", () => {
  const withoutLayout = frameFor({ view, envelope: clean }).footer;
  assertEqual(withoutLayout.layoutMs, null, "an unmeasured layout is absent, never zero");
  assert(!withoutLayout.text.includes("layout"), "and is not spoken for");
  const withLayout = frameFor({ view, envelope: clean, layoutMs: 31 }).footer;
  assertEqual(withLayout.durationMs, 7, "the query's own milliseconds");
  assertEqual(withLayout.layoutMs, 31, "and the picture's, separately");
  assert(withLayout.text.includes("query 7 ms"), `the footer says which is which: ${withLayout.text}`);
  assert(withLayout.text.includes("layout 31 ms"), `and both: ${withLayout.text}`);
  assertEqual(withLayout.maxDepth, 1, "and reports the depth that was reached, not one that was asked for");
});

check("aRefusalCountsNothingBecauseItMeasuredNothing", () => {
  const refused = frameFor({ view, error: staleRefusal, declarations });
  assertEqual(refused.footer.nodes, null, "a refused run counted no nodes; it did not count zero");
  assertEqual(refused.footer.edges, null, "nor zero edges");
  assertEqual(refused.footer.durationMs, null, "and it timed nothing");
  assertEqual(refused.footer.text, "", "so the strip is present and says nothing");
  // The control: a run that really did match nothing says so, and the
  // two states must not be spelled the same way.
  const matchedNothing = frameFor({ view, envelope: emptyEnvelope, declarations, sets: ["zones"] });
  assertEqual(matchedNothing.footer.nodes, 0, "a run that matched nothing counted zero");
  assert(matchedNothing.footer.text.startsWith("0 nodes, 0 edges"), "and says so");
});

// --- The stack -------------------------------------------------------

check("bannersKeepTheirFixedOrder", () => {
  const everything = envelope({
    nodes: [
      { id: "n1", key: "elwynn", type: "zone", name: "Elwynn", set: "zones", ambiguous: true },
      { id: "n2", key: "duskwood", type: "zone", name: "Duskwood", set: "zones" },
    ],
    truncated: { nodes: true, edges: false, depth: true },
    stale: [{ code: "entity_type_missing", pointer: "/from/1/type", was: "faction" }],
  });
  const banners = bannersFor(everything, { droppedRefs, placedAutomatically: 12 });
  assertDeepEqual(
    codesOf(banners),
    [BANNER_DROPPED, BANNER_TRUNCATED_NODES, BANNER_TRUNCATED_DEPTH, BANNER_PLACED, BANNER_AMBIGUOUS],
    "five bands, in the declared order",
  );
  // The order is the declared one and not merely the order this builder
  // happens to append in: the codes must be a subsequence of BANNER_ORDER.
  let at = -1;
  for (const code of codesOf(banners)) {
    const next = BANNER_ORDER.indexOf(code);
    assert(next > at, `${code} is out of the declared order`);
    at = next;
  }
});

check("theStaleSlotSitsAheadOfEverythingElse", () => {
  // The rename beside the missing field is the control on the rule that
  // renames are never a band: it would ride into this band's rows
  // without changing a single code, so the rows are asserted too.
  const stale = envelope({
    truncated: { nodes: true, edges: false, depth: false },
    stale: [
      { code: "field_missing", pointer: "/project/color_by", was: "faction" },
      { code: "entity_type_renamed", pointer: "/from/0/type", was: "zone", now: "region" },
    ],
  });
  const banners = bannersFor(stale, {});
  assertDeepEqual(
    codesOf(banners),
    [BANNER_STALE, BANNER_TRUNCATED_NODES],
    "the staleness slot is the first one",
  );
  assertDeepEqual(
    banners[0].rows.map((row) => row.pointer),
    ["/project/color_by"],
    "and it carries what is gone, never what was merely renamed",
  );
});

check("everyThingHasItsOwnState", () => {
  const frames = {
    clean: frameFor({ view, envelope: clean, declarations }),
    truncated: frameFor({ view, envelope: truncatedEverywhere, declarations }),
    ambiguous: frameFor({ view, envelope: ambiguousEnvelope, declarations }),
    renamed: frameFor({ view, envelope: renamedEnvelope, declarations }),
    bestEffort: frameFor({ view, envelope: bestEffortEnvelope, declarations, droppedRefs }),
    empty: frameFor({ view, envelope: emptyEnvelope, declarations, sets: ["zones"] }),
    staleRefusal: frameFor({ view, error: staleRefusal, declarations }),
    invalidRefusal: frameFor({ view, error: invalidRefusal, declarations }),
    unbound: frameFor({ view, error: unboundRefusal, declarations }),
  };
  // The signature of a state is its kind, its bands, whether it has a
  // panel, whether it has a rename line and what it offers to do. No two
  // fixtures may share one, or a test that claims to distinguish them is
  // asserting over a fixture too small to tell them apart.
  const seen = new Map();
  for (const [name, frame] of Object.entries(frames)) {
    const signature = JSON.stringify([
      frame.kind,
      codesOf(frame.banners),
      frame.diagnostics !== null,
      frame.title.renamed !== null,
      frame.actions.map((action) => action.code),
      frame.bar.controls.filter((control) => control.marked).length,
    ]);
    assert(!seen.has(signature), `${name} is indistinguishable from ${seen.get(signature)}: ${signature}`);
    seen.set(signature, name);
  }
});

// --- The URL ---------------------------------------------------------

check("parametersRoundTripThroughTheURL", () => {
  const bound = { "clase-ñ": "a&b=c", depth: "2" };
  const search = writeParams(bound, "?tab=graph");
  const back = readParams("?" + search);
  assertDeepEqual(back, bound, "two parameters survive a round trip through a URL");
  assert(search.includes("tab=graph"), "and the page's own keys survive with them");
});

// --- Run -------------------------------------------------------------

for (const [name, fn] of pending) {
  try {
    await fn();
    console.log("ok   " + name);
  } catch (err) {
    failures++;
    console.error("FAIL " + name + ": " + (err && err.message ? err.message : err));
  }
}

if (failures > 0) {
  console.error(`${failures} frame check(s) failed`);
  process.exit(1);
}
console.log("scene.js: all checks passed");
