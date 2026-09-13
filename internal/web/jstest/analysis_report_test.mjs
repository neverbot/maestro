// The harness for the analysis reports' three sentences: the verdict, the
// run line and the gate line, in internal/web/static/pages/analysis.js.
//
// What this layer covers that no Go test can. All three are pure
// functions of the engine's own envelope, and every defect they have
// shipped was a *reading* defect: a field that is not on the wire, a
// count labelled with the wrong noun, a verdict assembled so that the
// half saying "nothing is wrong" was the only half that could survive.
// The server was right every time. Driving them directly, with the
// envelope the engine really sends, is the only place those can go red.
//
// Run directly: `node internal/web/jstest/analysis_report_test.mjs`.
// internal/web/static_analysis_report_test.go shells out to it too.

// The module graph reaches app.js, which binds the sign-in shell's own
// form at module scope, so the host needs a document that answers
// getElementById before the import runs. It needs nothing else: the
// three functions under test are pure, and a stub that could do more
// would be a second, worse browser.
globalThis.window = globalThis;
globalThis.document = {
  title: "",
  getElementById: () => null,
  querySelector: () => null,
  querySelectorAll: () => [],
  addEventListener() {},
  createElement: () => ({ append() {}, setAttribute() {}, addEventListener() {}, classList: { add() {} } }),
};

const { cyclesVerdict, walkedLine, gateLine, adviceForDesigner, CYCLES_CLEAN, CONTAINMENT_CLEAN } =
  await import("../static/pages/analysis.js");

let failures = 0;
function check(what, got, want) {
  if (got === want) {
    console.log("ok   " + what);
    return;
  }
  failures += 1;
  console.log("FAIL " + what + "\n  got  " + JSON.stringify(got) + "\n  want " + JSON.stringify(want));
}

// **A report that finds something still has a verdict.** The verdict was
// built by pushing a sentence for each half that came back clean, so a
// game with prerequisite loops said only "Nothing contains itself." over
// its evidence, and a game with both kinds of loop said nothing at all.
check("clean in both halves", cyclesVerdict(0, 0), CYCLES_CLEAN + " " + CONTAINMENT_CLEAN);
check(
  "prerequisite loops, no containment",
  cyclesVerdict(3, 0),
  "3 things depend on themselves. " + CONTAINMENT_CLEAN,
);
check(
  "containment loops, no prerequisites",
  cyclesVerdict(0, 1),
  CYCLES_CLEAN + " 1 thing contains itself.",
);
check(
  "both halves find something",
  cyclesVerdict(2, 1),
  "2 things depend on themselves. 1 thing contains itself.",
);

// **The run line's nouns are the engine's shapes.** `seed_total` counts
// a seed row and the cycles walk seeds every entity once per kind of
// loop, so on a game of 28 entities it is 56 — printed as "entities" it
// contradicted the two reports beside it on the same screen.
check(
  "seed_total is starting points, not entities",
  walkedLine({ seed_total: 56, edges_walked: 48 }),
  "This run started from 56 starting points, followed 48 edges.",
);
check(
  "a reachability run names its denominator",
  walkedLine({ seeds: { total: 16 }, reachable_total: 28, unreachable_total: 0, edges_walked: 48 }),
  "This run started from 16 starting points, reached 28 of 28 entities, followed 48 edges.",
);
check(
  "an invalid edge is counted where it was followed",
  walkedLine({ considered_total: 28, edges_walked: 48, invalid_edges_followed: 2 }),
  "This run considered 28 entities, followed 48 edges, 2 of them invalid.",
);
check("an envelope with no counts says nothing", walkedLine({}), "");

// **The gate line reads the field the engine sends.** It tested
// `entry.derived_from_role`, which is not on the wire: analysis.Source is
// `source`, and its own doc comment says it exists so a designer can see
// the engine treated a type as a gate because of a role they set months
// ago. Reading the wrong name made every type read as declared.
check(
  "a role-derived gate says so",
  gateLine({
    semantics_source: [
      { key: "requires", analysis_traits: ["prerequisite_of"], source: "declared" },
      { key: "set_in", analysis_traits: ["containment"], source: "derived_from_role" },
    ],
  }),
  "It followed requires: prerequisite_of; set_in: containment (from its role).",
);
check(
  "a caller-supplied gate says so",
  gateLine({ semantics_source: [{ key: "leads_to", analysis_traits: ["gate"], source: "caller_supplied" }] }),
  "It followed leads_to: gate (because you asked for it).",
);
check("no semantics, no sentence", gateLine({}), "");

// **The refusal's advice is written for an agent.** It names
// `relation_types.upsert` and reads as a call to make; a designer has no
// API, which the negative state's own spec says in as many words. What
// survives is the vocabulary, which the engine generates from the same
// columns that validate a write.
check(
  "the vocabulary survives and the tool call does not",
  adviceForDesigner(
    'Declare analysis_traits on the relation types that gate progression — relation_types.upsert takes ' +
      'them, from "prerequisite_of", "unlocks", "containment" — or set a semantic_role of "prerequisite", ' +
      '"unlock", which this engine translates into traits.',
  ),
  "A relation type says what it means to this analysis by declaring what it does — " +
    "prerequisite_of, unlocks, containment, prerequisite, unlock — and nothing on this page declares one. " +
    "An agent does, over MCP.",
);
check("no vocabulary, no sentence", adviceForDesigner(""), "");
check("advice with nothing quoted says nothing", adviceForDesigner("Declare some traits."), "");

if (failures > 0) {
  console.error(failures + " assertion(s) failed");
  process.exit(1);
}
console.log("analysis report sentences: all assertions passed");
