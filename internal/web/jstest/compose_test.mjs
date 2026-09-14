// The harness for the query builder's emitter.
//
// Three properties, and each of them is the reason a line of the design
// spec exists:
//
//   - **What the sentence says is what the document says.** A clause
//     stack emits the query a designer read aloud, including the parts
//     they did not type: the linear chain's `from`, the set names, and
//     the `all` a second condition on one line becomes.
//   - **A pointer lands on a clause.** `views.validate` answers with a
//     JSON pointer, and the map returned beside the document is the only
//     thing that turns that pointer back into the line that wrote it
//     (§5). The pointer for a lone condition and for one of two is not
//     the same pointer, which is exactly the case a map built by hand
//     gets wrong.
//   - **The door closes by itself** (§4). A document the builder cannot
//     hold is refused rather than approximated, and the refusal comes
//     from the round trip rather than from a list of shapes somebody
//     remembered to write down.
//
// Run directly: `node internal/web/jstest/compose_test.mjs`.
// internal/web/static_compose_test.go shells out to it too.

let failures = 0;
function check(what, got, want) {
  if (JSON.stringify(got) === JSON.stringify(want)) {
    console.log("ok   " + what);
    return;
  }
  failures += 1;
  console.log("FAIL " + what + "\n  got  " + JSON.stringify(got) + "\n  want " + JSON.stringify(want));
}

const { compose, decompose, roundTrips, canonicalJSON, V, TOO_MUCH, CLAUSE_FROM, CLAUSE_WHERE, CLAUSE_FOLLOW, CLAUSE_EDGE_WHERE, CLAUSE_DRAW } =
  await import("../static/query/compose.js");

// --- The sentence the language was designed around --------------------
//
// "the quests a Mage can reach between level 20 and 30, coloured by zone"

const SENTENCE = [
  { kind: CLAUSE_FROM, id: "a", type: "quest" },
  { kind: CLAUSE_WHERE, id: "b", field: "level", op: "gte", value: 20 },
  { kind: CLAUSE_WHERE, id: "c", field: "level", op: "lte", value: 30 },
  { kind: CLAUSE_FOLLOW, id: "d", via: ["available_to"], direction: "in", depth: 1 },
  { kind: CLAUSE_DRAW, id: "e", colorBy: "zone" },
];

// Every document this file composes, kept for --emit below.
const EMITTED = [];
function composed(clauses) {
  const answer = compose(clauses);
  if (answer.problems.length === 0) EMITTED.push(answer.document);
  return answer;
}

const built = composed(SENTENCE);

check("the sentence emits the document", built.document, {
  v: V,
  from: [{
    type: "quest",
    as: "quest",
    where: { all: [{ field: "level", op: "gte", value: 20 }, { field: "level", op: "lte", value: 30 }] },
  }],
  traverse: [{ from: "quest", via: "available_to", as: "step1", direction: "in", depth: 1 }],
  project: { color_by: "zone" },
});

check("nothing is wrong with it", built.problems, []);

// The two conditions are one predicate and two pointers, and neither is
// "/from/0/where": a pointer that names the whole predicate cannot pick
// out which of the two lines the diagnostic is about.
check("each condition has its own pointer", [built.pointers.get("b.op"), built.pointers.get("c.value")], [
  "/from/0/where/all/0/op",
  "/from/0/where/all/1/value",
]);

check("a lone condition points at the predicate itself",
  composed([{ kind: CLAUSE_FROM, id: "a", type: "quest" }, { kind: CLAUSE_WHERE, id: "b", field: "level", op: "gte", value: 20 }])
    .pointers.get("b.field"),
  "/from/0/where/field");

check("the Draw clause's pointers name the projection",
  [built.pointers.get("e.colorBy"), built.pointers.get("d.via"), built.pointers.get("a.type")],
  ["/project/color_by", "/traverse/0/via", "/from/0/type"]);

// --- Names appear only when something reads them ----------------------

check("a stack with no Follow line names no set",
  composed([{ kind: CLAUSE_FROM, id: "a", type: "quest" }]).document.from,
  [{ type: "quest" }]);

check("a stack with a Follow line names the set it starts from",
  composed([
    { kind: CLAUSE_FROM, id: "a", type: "quest" },
    { kind: CLAUSE_FOLLOW, id: "b", via: ["available_to"] },
  ]).document,
  { v: V, from: [{ type: "quest", as: "quest" }], traverse: [{ from: "quest", via: "available_to", as: "step1" }] });

// The chain is linear by construction: the second Follow starts from the
// first, and there is no control through which it could start elsewhere.
check("a second Follow starts from the first",
  composed([
    { kind: CLAUSE_FROM, id: "a", type: "zone" },
    { kind: CLAUSE_FOLLOW, id: "b", via: ["connects_to"] },
    { kind: CLAUSE_FOLLOW, id: "c", via: ["takes_place_in"], direction: "in" },
  ]).document.traverse.map((step) => [step.from, step.as]),
  [["zone", "step1"], ["step1", "step2"]]);

check("two relation types stay an array",
  composed([{ kind: CLAUSE_FROM, id: "a", type: "quest" }, { kind: CLAUSE_FOLLOW, id: "b", via: ["a", "b"] }])
    .document.traverse[0].via,
  ["a", "b"]);

// --- What a picker cannot prevent, said before a round trip -----------

check("a condition with nothing above it is a problem, not a document",
  composed([{ kind: CLAUSE_WHERE, id: "b", field: "level", op: "gte", value: 20 }]).problems.map((p) => p.clause),
  ["b", ""]);

check("a connection's condition needs a Follow line",
  composed([
    { kind: CLAUSE_FROM, id: "a", type: "quest" },
    { kind: CLAUSE_EDGE_WHERE, id: "b", field: "weight", op: "gt", value: 1 },
  ]).problems.map((p) => p.clause),
  ["b"]);

check("a connection's condition lands on the step that walked it",
  composed([
    { kind: CLAUSE_FROM, id: "a", type: "quest" },
    { kind: CLAUSE_FOLLOW, id: "b", via: ["available_to"] },
    { kind: CLAUSE_EDGE_WHERE, id: "c", field: "weight", op: "gt", value: 1 },
  ]).document.traverse[0].edge_where,
  { field: "weight", op: "gt", value: 1 });

// --- It generates; it does not edit -----------------------------------

const OPENABLE = JSON.stringify({
  v: 1,
  from: [{ type: "quest", as: "quest", where: { field: "level", op: "gte", value: 20 } }],
  traverse: [{ from: "quest", via: "available_to", as: "step1", direction: "in", depth: 1 }],
  project: { color_by: "zone", label: "name" },
});

check("a document the builder wrote reopens", roundTrips(OPENABLE), true);

check("reopening it gives back the sentence", decompose(JSON.parse(OPENABLE)).map((c) => c.kind), [
  CLAUSE_FROM, CLAUSE_WHERE, CLAUSE_FOLLOW, CLAUSE_DRAW,
]);

// Whitespace and key order are not things the language means, so neither
// closes the door. Everything below this line is.
check("formatting is not a difference", roundTrips(JSON.stringify(JSON.parse(OPENABLE), null, 2)), true);

const CLOSED = {
  "a parameter": { v: 1, params: [{ key: "who", type: "text" }], from: [{ type: "quest" }] },
  "a branch": {
    v: 1,
    from: [{ type: "quest", as: "quest" }, { type: "zone", as: "zone" }],
    traverse: [{ from: "zone", via: "connects_to", as: "step1" }],
  },
  "a depth range": {
    v: 1,
    from: [{ type: "quest", as: "quest" }],
    traverse: [{ from: "quest", via: "requires", as: "step1", depth: { min: 1, max: 4 } }],
  },
  "an explicit node set": { v: 1, from: [{ type: "quest" }], nodes: [{ set: "quest" }] },
  "a limit": { v: 1, from: [{ type: "quest" }], limits: { max_nodes: 50 } },
  "invalid content asked for": { v: 1, from: [{ type: "quest" }], include_invalid: true },
  "an any predicate": { v: 1, from: [{ type: "quest", where: { any: [{ field: "a", op: "eq", value: 1 }] } }] },
  "a negated predicate": { v: 1, from: [{ type: "quest", where: { not: { field: "a", op: "eq", value: 1 } } }] },
  "a related attribute in the drawing": { v: 1, from: [{ type: "quest" }], project: { color_by: { related: { via: "takes_place_in", attr: "name" } } } },
  "a condition with a sibling the stack has no line for": {
    v: 1,
    from: [{ type: "quest", where: { all: [{ field: "a", op: "eq", value: 1 }], any: [{ field: "b", op: "eq", value: 2 }] } }],
  },
  "a condition carrying a key the builder does not write": {
    v: 1,
    from: [{ type: "quest", where: { field: "a", op: "eq", value: 1, case_sensitive: true } }],
  },
  // One relation type has two spellings in the language and the builder
  // writes one of them. Rewriting the other into it would be editing a
  // stored document, which is the one thing §4 forbids, so the
  // one-element array is a door the builder closes rather than a
  // difference it smooths over.
  "the other spelling of a single relation type": {
    v: 1,
    from: [{ type: "quest", as: "quest" }],
    traverse: [{ from: "quest", via: ["available_to"], as: "step1" }],
  },
  "a key from a later language version": { v: 1, from: [{ type: "quest" }], unknown_key: true },
  "another language version": { v: 2, from: [{ type: "quest" }] },
};

for (const [what, document] of Object.entries(CLOSED)) {
  check(what + " closes the door", roundTrips(JSON.stringify(document)), false);
  // And it closes it **at decompose**, not at the comparison that
  // follows. The two are not the same guard: a shape the reader accepts
  // and the emitter then drops fails the round trip too, so the
  // comparison alone would keep every one of these red while `decompose`
  // handed a caller a stack with a clause silently missing from it. The
  // reader is the door; the comparison is the lock behind it.
  check(what + " is refused by the reader", decompose(document), null);
}

// A name the builder would not have chosen is still a name it can hold:
// it is carried on the clause and written back unchanged, and the guard
// is about what a document says rather than about how the builder would
// have said it.
check("a set name of its own reopens unchanged",
  roundTrips(JSON.stringify({ v: 1, from: [{ type: "quest", as: "seeds" }] })),
  true);

check("text that is not a document closes the door", roundTrips("{"), false);

// The refusal is a sentence a person can act on, not a shrug.
check("the refusal names both ways forward",
  [TOO_MUCH.includes("Copy it"), TOO_MUCH.includes("ask an agent")],
  [true, true]);

// --- The canonical text -----------------------------------------------

check("canonical text sorts keys deep and keeps array order",
  canonicalJSON({ b: 1, a: [{ d: 2, c: 3 }] }),
  '{"a":[{"c":3,"d":2}],"b":1}');

// A value difference is a difference, which is the whole point: the
// guard is an assertion about content, not about shape.
check("a changed value is not a round trip",
  canonicalJSON({ a: 1 }) === canonicalJSON({ a: 2 }),
  false);

// --- What the real parser says about what it emits ---------------------
//
// Every property above is about this module in isolation, and this
// module's whole output is a document a Go parser reads. `--emit` prints
// the documents composed above, one per line, and
// internal/web/static_compose_test.go feeds each of them to
// views.ParseQuery: a builder whose sentence emits a document the server
// refuses is the "correct in the module, dead at the call site" defect
// with a JSON document in the middle.
if (process.argv.includes("--emit")) {
  for (const document of EMITTED) console.log(JSON.stringify(document));
  process.exit(failures > 0 ? 1 : 0);
}

if (failures > 0) {
  console.log(failures + " failed");
  process.exit(1);
}
console.log("all passed");
