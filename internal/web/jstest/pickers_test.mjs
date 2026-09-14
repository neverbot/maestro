// The harness for the four pickers' sources: what a picker offers, and
// where it gets it.
//
// The control itself is held by picker_test.mjs. What is here is the
// half that decides *what may be chosen*, and it is worth its own file
// because the whole point of a picker in this product is that a key
// cannot be misspelled — which is a property of the mapping from a
// game's vocabulary to a list of options, not of a menu.
//
// The entity picker is the one that cannot be a list: a game in this
// instance holds a thousand entities of one type, so its source is a
// search and the properties are about *when* it asks and *what it does
// with an answer that arrived late*.
//
// Run directly: `node internal/web/jstest/pickers_test.mjs`.
// internal/web/static_pickers_test.go shells out to it too.

import { install } from "./svg_dom.mjs";

const dom = install();

let failures = 0;
function check(what, got, want) {
  if (JSON.stringify(got) === JSON.stringify(want)) {
    console.log("ok   " + what);
    return;
  }
  failures += 1;
  console.log("FAIL " + what + "\n  got  " + JSON.stringify(got) + "\n  want " + JSON.stringify(want));
}

const { entityTypeOptions, relationTypeOptions, fieldOptions, entitySource, SEARCH_PAGE } =
  await import("../static/components/pickers.js");
const { MstPicker, SEARCH_DELAY_MS, SEARCHING_LABEL, SEARCH_HINT, CLASS_FILTER, CLASS_OPTION, CLASS_EMPTY } =
  await import("../static/components/mst-picker.js");

// --- The three vocabularies -------------------------------------------

const CLIENT = {
  calls: [],
  async listTypes() {
    this.calls.push("listTypes");
    return { ok: true, result: { items: [
      { key: "quest", label: "Quest", label_plural: "Quests" },
      { key: "zone", label: "Zone" },
    ] } };
  },
  async listRelationTypes() {
    this.calls.push("listRelationTypes");
    return { ok: true, result: { items: [{ key: "available_to", label: "available to" }] } };
  },
  async getType(key) {
    this.calls.push("getType:" + key);
    return { ok: true, result: { key, field_schema: [
      { key: "min_level", label: "Minimum level", type: "number" },
      { key: "summary", type: "longtext" },
    ] } };
  },
};

check("an entity type picker offers what the game declared, by its own key",
  await entityTypeOptions(CLIENT),
  [{ key: "quest", label: "Quest" }, { key: "zone", label: "Zone" }]);

check("a relation type picker labels with the game's words and answers with its key",
  await relationTypeOptions(CLIENT),
  [{ key: "available_to", label: "available to" }]);

// The fields a type declares, **in the order it declares them**: that
// order is the game's own statement about its schema, and sorting it
// would be this control editing it.
check("a field picker offers the type's fields in the type's own order",
  await fieldOptions(CLIENT, "quest"),
  [{ key: "min_level", label: "Minimum level" }, { key: "summary", label: "summary" }]);

// The optional clause: a picker that always offered "no choice" would
// teach that every clause is optional, so it is offered only when the
// caller says so — and its key is the empty string, which no game can
// declare.
check("an optional clause can be left unanswered, and that choice has a key nothing collides with",
  (await entityTypeOptions(CLIENT, { none: true }))[0],
  { key: "", label: "no choice" });

check("a field picker with no type asks nothing at all",
  await fieldOptions(CLIENT, ""),
  []);

// A refusal is an empty list and never a thrown error inside a menu: the
// page's own error surface is where a failed fetch belongs.
check("a listing that failed offers nothing rather than breaking the control",
  await entityTypeOptions({ async listTypes() { return { ok: false, error: { message: "nope" } }; } }),
  []);

// --- The entity picker, which is a search ------------------------------

function searchingClient(answers) {
  const asked = [];
  return {
    asked,
    async searchEntities(query, typeKey, options) {
      asked.push({ query, typeKey, limit: options.limit, kind: options.kind });
      const answer = answers.shift();
      return answer ?? { ok: true, result: { items: [] } };
    },
  };
}

{
  const client = searchingClient([{ ok: true, result: { items: [
    { kind: "entity", entity: { type_key: "quest", key: "hogger", name: "Wanted: Hogger" } },
    { kind: "entity", entity: { type_key: "quest", key: "kobolds", name: "" } },
  ] } }]);
  const source = entitySource(client, "quest");

  check("an empty box is not a search", await source("   "), []);
  check("and nothing was asked", client.asked.length, 0);

  check("a search answers with the key the game wrote, labelled by the entity's name",
    await source("hog"),
    [{ key: "hogger", label: "Wanted: Hogger" }, { key: "kobolds", label: "kobolds" }]);
  check("it asks the product's own search, narrowed to the type, for one menu's worth",
    client.asked[0],
    { query: "hog", typeKey: "quest", limit: SEARCH_PAGE, kind: "entity" });
}

// --- The control over a source ----------------------------------------

// A picker with a source is driven with injected timers, so the debounce
// is a millisecond of test time rather than a sleep.
function clock() {
  const timers = new Map();
  let seq = 0;
  return {
    setTimer(fn, ms) {
      const id = ++seq;
      timers.set(id, { fn, ms });
      return id;
    },
    clearTimer(id) {
      timers.delete(id);
    },
    pending: () => timers.size,
    async run() {
      const due = [...timers.entries()];
      timers.clear();
      for (const [, timer] of due) await timer.fn();
    },
    // start fires the due timers **without waiting for them to finish**,
    // which is the only way to hold a search in flight: the timer's own
    // body awaits the source, so a test that awaited it could never
    // arrange for a slow answer to land late.
    start() {
      const due = [...timers.entries()];
      timers.clear();
      for (const [, timer] of due) void timer.fn();
    },
  };
}

// labelsOf reads the words a menu is showing. An option carries the
// label **and** the key it will answer with — that pairing is the whole
// point of the control — so a test that read the button's whole text
// would be asserting "Wanted: Hoggerhogger".
function labelsOf(picker) {
  return picker.root
    .querySelectorAll("." + CLASS_OPTION)
    .map((button) => button.children[0].textContent);
}

function searchingPicker(source) {
  const timers = clock();
  const picker = new MstPicker({
    document: dom.document,
    placeholder: "Choose an entity",
    source,
    setTimer: timers.setTimer,
    clearTimer: timers.clearTimer,
  });
  return { picker, timers };
}

{
  const { picker, timers } = searchingPicker(async () => [{ key: "hogger", label: "Wanted: Hogger" }]);
  // The box is there from the first draw: it is the only way to reach
  // what the control does not hold yet.
  check("a searching picker always has its box", !!picker.root.querySelector("." + CLASS_FILTER), true);
  check("and says what to do with it", picker.root.querySelector("." + CLASS_EMPTY).textContent, SEARCH_HINT);

  picker.query = "hog";
  picker.ask();
  check("a keystroke is not a question", timers.pending(), 1);
  await timers.run();
  const shown = labelsOf(picker);
  check("the answer becomes the menu", shown, ["Wanted: Hogger"]);
}

// **A slow answer to an old question is dropped.** Typing "gno" then
// "gnoll" starts two searches, and the first one landing last would fill
// the menu with matches for a word the person has finished typing.
{
  let served = 0;
  const { picker, timers } = searchingPicker(async (query) => {
    served += 1;
    return [{ key: "for-" + query, label: "for " + query }];
  });
  picker.query = "gno";
  picker.ask();
  picker.query = "gnoll";
  picker.ask();
  // The second `ask` cancelled the first: one timer, one call.
  check("only the last question is asked", timers.pending(), 1);
  await timers.run();
  check("and it is the one on screen", served, 1);
  check("the menu answers the word that is in the box",
    labelsOf(picker),
    ["for gnoll"]);
}

// **An answer to a question nobody is asking any more is dropped.** The
// cancelled timer above is only half of it: a search that has already
// gone out cannot be recalled, so a slow answer can land after the box
// has moved on. Here the first search is left in flight, the word
// changes, and the old answer resolves last — the case that fills a menu
// with matches for a word the person finished typing.
{
  const resolvers = [];
  const { picker, timers } = searchingPicker((query) =>
    new Promise((resolve) => resolvers.push(() => resolve([{ key: query, label: "for " + query }]))));

  picker.query = "gno";
  picker.ask();
  timers.start();              // the call goes out and is still in flight
  picker.query = "gnoll";
  picker.ask();
  timers.start();              // the second call goes out too
  await new Promise((resolve) => setImmediate(resolve));
  resolvers[1]();              // the new answer lands
  await new Promise((resolve) => setImmediate(resolve));
  resolvers[0]();              // and then the old one does
  await new Promise((resolve) => setImmediate(resolve));

  check("the late answer to the old question is thrown away", labelsOf(picker), ["for gnoll"]);
}

// A choice made in a searching picker survives a new *list* as well as a
// new search: `setOptions` is how a caller replaces what there is to
// choose from, and for a vocabulary picker dropping a choice the new
// list does not offer is right — a `color_by` naming a field that is
// gone is worse than none — while for a search it would un-choose an
// entity nobody un-chose.
{
  const { picker } = searchingPicker(async () => []);
  picker.setOptions([{ key: "hogger", label: "Wanted: Hogger" }]);
  picker.choose("hogger");
  picker.setOptions([{ key: "kobolds", label: "Kobolds" }]);
  check("the entity stays chosen", picker.chosen, "hogger");
  check("and the control still says which one", picker.label(), "Wanted: Hogger");
}

// The other half of that rule, on the control this file's sibling
// harness covers: a vocabulary picker *does* drop a choice its new list
// no longer offers.
{
  const picker = new MstPicker({
    document: dom.document,
    options: [{ key: "min_level", label: "Minimum level" }],
    placeholder: "Choose a field",
  });
  picker.choose("min_level");
  picker.setOptions([{ key: "summary", label: "summary" }]);
  check("a field that is gone is not still chosen", picker.chosen, null);
  check("and the control asks again", picker.label(), "Choose a field");
}

// A source that throws is an empty menu with a sentence, not a crash
// inside a control.
{
  const { picker, timers } = searchingPicker(async () => {
    throw new Error("the network went away");
  });
  picker.query = "hog";
  picker.ask();
  await timers.run();
  check("a failed search leaves a sentence rather than a broken menu",
    picker.root.querySelector("." + CLASS_EMPTY).textContent,
    "nothing matches “hog”");
}

// **A searching picker does not filter its own answer.** The server
// found these rows for reasons the client cannot see — a word in a
// field, a key, a title — and filtering them again by the typed word
// would hide exactly the rows this product's search exists to find.
{
  const { picker, timers } = searchingPicker(async () => [
    { key: "kobolds", label: "Kobolds of Elwynn" },
  ]);
  picker.query = "hogger";
  picker.ask();
  await timers.run();
  check("a row the server matched on something invisible is still offered",
    labelsOf(picker),
    ["Kobolds of Elwynn"]);
}

// The choice survives the next search: a list is one answer to one
// question, and an entity somebody chose is not un-chosen by typing
// another word.
{
  const { picker, timers } = searchingPicker(async (query) => [{ key: query, label: "row " + query }]);
  picker.query = "a";
  picker.ask();
  await timers.run();
  picker.choose("a");
  check("the control says what was chosen", picker.label(), "row a");
  picker.query = "b";
  picker.ask();
  await timers.run();
  check("and still says it after another search replaced the list", picker.label(), "row a");
  check("the delay is the one the catalogue's own search uses", SEARCH_DELAY_MS, 200);
  check("while it is looking it says so", SEARCHING_LABEL, "looking…");
}

if (failures > 0) {
  console.log(failures + " failed");
  process.exit(1);
}
console.log("all passed");
