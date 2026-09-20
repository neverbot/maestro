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

// pages/page.js defines the read-only notice's hint component when it
// loads, and a custom element's class needs these two globals to exist
// before it is declared. Neither is what this file checks; they are here
// so importing a page does not fail on the platform being absent.
globalThis.HTMLElement ??= class {};
globalThis.customElements ??= { define() {}, get: () => undefined };

globalThis.window = globalThis;
globalThis.document = {
  title: "",
  getElementById: () => null,
  querySelector: () => null,
  querySelectorAll: () => [],
  addEventListener() {},
  createElement: () => ({ append() {}, setAttribute() {}, addEventListener() {}, classList: { add() {} } }),
};

const {
  cyclesVerdict,
  walkedLine,
  gateLine,
  adviceForDesigner,
  ORPHAN_MODES,
  perTypeLine,
  routesVerdict,
  ROUTES_NONE,
  CYCLES_CLEAN,
  CONTAINMENT_CLEAN,
} = await import("../static/pages/analysis.js");

let failures = 0;
function check(what, got, want) {
  if (got === want) {
    console.log("ok   " + what);
    return;
  }
  failures += 1;
  console.log("FAIL " + what + "\n  got  " + JSON.stringify(got) + "\n  want " + JSON.stringify(want));
}

// **The verb agrees with the count.** `countLabel` gets the noun right
// ("1 entity") and every one of these sentences then went on to say
// "are": a game with exactly one isolated entity — the ordinary case,
// because a designer fixes them as they appear — read "1 entity are
// connected to nothing." Seen on a seeded game built to make these
// findings render at all.
check("one isolated entity", ORPHAN_MODES[0].found(1), "1 entity is connected to nothing.");
check("several", ORPHAN_MODES[0].found(4), "4 entities are connected to nothing.");
check("one that leads nowhere", ORPHAN_MODES[1].found(1), "1 entity leads nowhere.");
check("several that lead nowhere", ORPHAN_MODES[1].found(3), "3 entities lead nowhere.");
check("one with no way in", ORPHAN_MODES[2].found(1), "1 entity has nothing leading to it.");
check("several with no way in", ORPHAN_MODES[2].found(2), "2 entities have nothing leading to them.");

// **The per-type breakdown has been on the wire the whole time and on
// no screen.** "4 entities cannot be reached" over a game where every
// one of them is a quest and every zone is fine is the difference
// between a modelling mistake and a missing edge.
check("only the types with something out of reach are named",
  perTypeLine({ per_type: [
    { entity_type: "quest", reachable: 3, unreachable: 4 },
    { entity_type: "zone", reachable: 1, unreachable: 0 },
  ] }),
  "Out of reach by type — quest: 4.");
check("two types that both have something",
  perTypeLine({ per_type: [
    { entity_type: "quest", reachable: 3, unreachable: 4 },
    { entity_type: "zone", reachable: 1, unreachable: 2 },
  ] }),
  "Out of reach by type — quest: 4, zone: 2.");
// A clean game says nothing rather than listing every healthy type with
// a zero beside it.
check("a game with nothing out of reach says nothing",
  perTypeLine({ per_type: [{ entity_type: "quest", reachable: 3, unreachable: 0 }] }),
  "");
check("and a result with no breakdown at all says nothing", perTypeLine({}), "");

// **Routes are a section now, not a footnote**, and what it says is the
// one thing the listing already knows: how many claims there are and
// how many the game has since contradicted.
check("no routes", routesVerdict([]), ROUTES_NONE);
check("one route, checked and holding",
  routesVerdict([{ status: "holds" }]),
  "1 route is written.");
check("stale is the number worth leading with",
  routesVerdict([{ status: "holds" }, { status: "stale" }, { status: "unchecked" }]),
  "3 routes are written; 1 is about a game that has since moved; 1 has never been checked.");
check("and two of each agree in the plural",
  routesVerdict([{ status: "stale" }, { status: "stale" }, { status: "unchecked" }, { status: "unchecked" }]),
  "4 routes are written; 2 are about a game that has since moved; 2 have never been checked.");

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
