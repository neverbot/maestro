// The harness for the query builder: the sentence, the document it
// writes, and the two refusals it makes for itself.
//
// The emitter has its own file (compose_test.mjs) and the pickers have
// theirs. What is here is the **screen**: that a stack reads as one
// sentence in the order a person would say it, that a diagnostic's JSON
// pointer finds the clause that wrote it, and that a save the server
// would refuse is refused here first, with the reason beside the button
// rather than after a press.
//
// Run directly: `node internal/web/jstest/builder_test.mjs`.
// internal/web/static_builder_test.go shells out to it too.

import path from "node:path";
import { register } from "node:module";
import { fileURLToPath } from "node:url";

register(new URL("./importmap_loader.mjs", import.meta.url), {
  parentURL: import.meta.url,
  data: { staticDir: path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "static"), shell: "game.html" },
});

import { install } from "./svg_dom.mjs";

const dom = install();
// app.js makes two lookups at module scope, and the module graph reaches
// it through pages/page.js.
dom.document.getElementById = () => null;
dom.document.querySelector = () => null;

let failures = 0;
function check(what, got, want) {
  if (JSON.stringify(got) === JSON.stringify(want)) {
    console.log("ok   " + what);
    return;
  }
  failures += 1;
  console.log("FAIL " + what + "\n  got  " + JSON.stringify(got) + "\n  want " + JSON.stringify(want));
}

const {
  BOUNDARY,
  DIRECTIONS,
  OPERATORS,
  VALUELESS,
  addClause,
  clauseOfPointer,
  documentText,
  nextId,
  numberOrText,
  saveProblems,
  stackOf,
} = await import("../static/pages/builder.js");
const { CLAUSE_DRAW, CLAUSE_FOLLOW, CLAUSE_FROM, CLAUSE_WHERE, compose } =
  await import("../static/query/compose.js");

// --- The stack a builder starts with ----------------------------------

// A query starts from something and a view is drawn some way, so those
// two lines are there from the first paint. Everything else is a clause
// the person adds, which is §2's rule: a clause is optional and says so
// by being absent, not by being an empty box.
check("a fresh stack is the two lines a view cannot be without",
  stackOf().map((clause) => clause.kind),
  [CLAUSE_FROM, CLAUSE_DRAW]);

// **The Draw line stays last.** It is the last thing said in the
// sentence, and it matters to the document too: the emitter attaches a
// condition to the clause above it, so a stack that let Draw sit in the
// middle would be a sentence whose meaning depends on where somebody
// pressed a button.
{
  const state = { stack: stackOf() };
  addClause(state, { kind: CLAUSE_WHERE, id: nextId(), field: "level", op: "gte", value: 20 });
  addClause(state, { kind: CLAUSE_FOLLOW, id: nextId(), via: ["available_to"] });
  check("a new clause lands before the Draw line",
    state.stack.map((clause) => clause.kind),
    [CLAUSE_FROM, CLAUSE_WHERE, CLAUSE_FOLLOW, CLAUSE_DRAW]);

  // And the condition attaches to the Start line, which is what the
  // order was protecting.
  const built = compose(state.stack);
  check("so the condition narrows what the sentence started from",
    built.document.from[0].where,
    { field: "level", op: "gte", value: 20 });
}

// --- The sentence the language was designed around ---------------------

{
  const stack = [
    { kind: CLAUSE_FROM, id: "a", type: "quest" },
    { kind: CLAUSE_WHERE, id: "b", field: "min_level", op: "gte", value: 20 },
    { kind: CLAUSE_FOLLOW, id: "c", via: ["available_to"], direction: "in", depth: 1 },
    { kind: CLAUSE_DRAW, id: "d", renderer: "graph", colorBy: "zone" },
  ];
  check("the sentence writes the document it says",
    JSON.parse(documentText(stack)),
    {
      v: 1,
      from: [{ type: "quest", as: "quest", where: { field: "min_level", op: "gte", value: 20 } }],
      traverse: [{ from: "quest", via: "available_to", as: "step1", direction: "in", depth: 1 }],
      project: { color_by: "zone" },
    });

  // **A pointer finds the line that wrote it** (§5). The longest
  // matching prefix wins, so a diagnostic deep inside a predicate lands
  // on the condition rather than on the Start clause it sits under.
  const { pointers } = compose(stack);
  check("a diagnostic about the type lands on the Start line",
    clauseOfPointer(pointers, "/from/0/type"), "a");
  check("one about the condition lands on the condition",
    clauseOfPointer(pointers, "/from/0/where/value"), "b");
  check("one about the traversal lands on the Follow line",
    clauseOfPointer(pointers, "/traverse/0/via"), "c");
  check("one about the drawing lands on the Draw line",
    clauseOfPointer(pointers, "/project/color_by"), "d");
  check("and a pointer at nothing lands nowhere rather than on the first line",
    clauseOfPointer(pointers, "/limits/max_depth"), "");

  // **The deepest line wins.** A server diagnostic can be about a place
  // no control wrote directly — an element of a value, a half of a depth
  // — so the rule is the longest control pointer that is a prefix of the
  // diagnostic's. Given a map where two clauses are both prefixes, the
  // shallower one is the wrong answer: it is the line the other sits
  // inside.
  // The shallow entry is **last** on purpose: a version of this that
  // simply kept the last match would pass with them the other way round
  // and pick the outer line here.
  const nested = new Map([
    ["inner.value", "/from/0/where/value"],
    ["outer.type", "/from/0"],
  ]);
  check("a diagnostic inside a clause finds that clause, not the one it sits in",
    clauseOfPointer(nested, "/from/0/where/value/2"), "inner");
  check("and one outside it still finds the outer line",
    clauseOfPointer(nested, "/from/0/type"), "outer");
}

// --- What this page refuses for itself --------------------------------

// A stored view needs three things that are not part of the query
// document at all. The first version of this screen let a save go out
// with no renderer and showed the server's answer, which was a correct
// sentence about a field the person had never been asked for.
{
  const stack = stackOf();
  check("a view with no renderer, name or address says the first of those",
    saveProblems(stack, "", ""),
    ["Draw needs a way to draw it.", "A view needs a name.", "A view needs an address."]);
  stack[1].renderer = "graph";
  check("and one with a renderer asks for the name",
    saveProblems(stack, "", "")[0],
    "A view needs a name.");
  check("a complete one has nothing to say",
    saveProblems(stack, "Quests", "quests"),
    []);
  // Whitespace is not a name: a view called " " is a row nobody can
  // find.
  check("nor is a name of spaces", saveProblems(stack, "   ", "quests").length, 1);
}

// --- The words between the controls -----------------------------------

// They are the product's own, never the model's: "inwards" rather than
// `"direction": "in"`, and the key is what reaches the document.
check("the directions read as words and answer with the language's keys",
  DIRECTIONS,
  [{ key: "out", label: "outwards" }, { key: "in", label: "inwards" }, { key: "both", label: "either way" }]);

check("the operators read as a clause",
  OPERATORS.slice(0, 3).map((operator) => operator.label),
  ["is", "is not", "is at least"]);

// A clause reading "level is set" with an empty box beside it is a
// control asking for something it will throw away.
check("the two operators that take no value are known to be valueless",
  [VALUELESS.has("exists"), VALUELESS.has("empty"), VALUELESS.has("eq")],
  [true, true, false]);

// **The boundary is on screen, once** (§3): a builder that covered the
// simple half of the language and hid the other half would be worse than
// one that covers it and says so.
check("the boundary names what is written as a document instead",
  [BOUNDARY.includes("two branches"), BOUNDARY.includes("depth range"), BOUNDARY.includes("parameter")],
  [true, true, true]);

// --- The one guess this page makes ------------------------------------

// `20` typed into a level comparison is a number and `Elwynn` is text.
// The server judges it against the field's declared type either way, and
// says so at a pointer this page can turn back into a line.
check("a typed value is a number when it is one",
  [numberOrText("20"), numberOrText("Elwynn"), numberOrText(""), numberOrText("20 quests"), numberOrText("007")],
  [20, "Elwynn", "", "20 quests", "007"]);

// --- Where a diagnostic is put ----------------------------------------
//
// **`markClause` walks the clause lines and nothing else.** The row of
// "and then follow…" buttons is a child of the same root, and a version
// that walked every child cleared that row's last child: the second add
// button rendered as a 28px empty box every time a diagnostic was
// placed. Found on screen with five clauses.
{
  const doc = dom.document;
  const root = doc.createElement("div");
  const clause = doc.createElement("div");
  clause.setAttribute("data-clause", "c1");
  const controls = doc.createElement("span");
  const note = doc.createElement("span");
  clause.append(controls, note);
  const adders = doc.createElement("div");
  const second = doc.createElement("button");
  second.textContent = "and then follow…";
  adders.append(doc.createElement("button"), second);
  root.append(clause, adders);
  doc.getElementById = (id) => (id === "clauses" ? root : null);

  const { markClause } = await import("../static/pages/builder.js");
  markClause(doc, "c1", "level is not a number.");
  check("the diagnostic lands on the clause", note.textContent, "level is not a number.");
  check("and the row of add buttons keeps its words", second.textContent, "and then follow…");

  markClause(doc, "", "");
  check("and a clean run clears the clause", note.textContent, "");
  check("without clearing anything else", second.textContent, "and then follow…");
  doc.getElementById = () => null;
}

// --- Opening a stored view --------------------------------------------
//
// §4: the builder **generates and never edits**. It opens a stored query
// only when that document round-trips through it unchanged, and what it
// then composes is a *new* view — so the page says so, and the save is a
// create.

{
  const { openedFrom, startedFrom } = await import("../static/pages/builder.js");

  const held = {
    key: "quests",
    name: "Quests by level",
    renderer: "graph",
    query: { v: 1, from: [{ type: "quest" }], project: { color_by: "min_level" } },
  };
  const stack = openedFrom(held);
  check("a query the builder can hold comes back as its own sentence",
    stack.map((clause) => clause.kind), [CLAUSE_FROM, CLAUSE_DRAW]);
  check("the type it started from is chosen", stack[0].type, "quest");
  check("the drawing carries the view's renderer, which is not in the query at all",
    [stack[1].renderer, stack[1].colorBy], ["graph", "min_level"]);
  // Round trip: what it re-emits is what was stored, or it would not
  // have opened.
  check("and re-emitting it writes the document that was stored",
    compose(stack).document, held.query);

  // The four §3 names, each refused at the door rather than opened and
  // quietly simplified.
  for (const [what, query] of [
    ["a parameter", { v: 1, params: [{ key: "who", type: "text" }], from: [{ type: "quest" }] }],
    ["a branch", {
      v: 1,
      from: [{ type: "quest", as: "quest" }, { type: "zone", as: "zone" }],
      traverse: [{ from: "zone", via: "connects_to", as: "step1" }],
    }],
    ["an explicit node set", { v: 1, from: [{ type: "quest" }], nodes: [{ set: "quest" }] }],
    ["a key from a later language version", { v: 1, from: [{ type: "quest" }], unknown: true }],
    // **The case the round trip catches and the reader does not.**
    // `decompose` accepts an empty `keys` list — it is a list of strings
    // — and the emitter omits an empty one, so what would be re-emitted
    // is a different document from what is stored. Without the round
    // trip this opens, and a saved copy quietly loses a key nobody meant
    // to drop the day the builder learns to write them.
    ["a selector carrying an empty keys list", { v: 1, from: [{ type: "quest", keys: [] }] }],
    ["a projection carrying an empty fields list", {
      v: 1,
      from: [{ type: "quest" }],
      project: { fields: [] },
    }],
  ]) {
    check(what + " does not open", openedFrom({ key: "x", renderer: "graph", query }), null);
  }
  check("and a row with no query at all does not open", openedFrom({ key: "x" }), null);

  // The sentence that stops this from looking like an edit. The builder
  // never modifies a stored query, so the person has to be told that the
  // thing they opened is not the thing they will save.
  const said = startedFrom("Quests by level");
  check("the page says what it started from and what saving does",
    [said.includes("starts from Quests by level"), said.includes("writes a new view"), said.includes("not changed")],
    [true, true, true]);
}

if (failures > 0) {
  console.log(failures + " failed");
  process.exit(1);
}
console.log("all passed");
