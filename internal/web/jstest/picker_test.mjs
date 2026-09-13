// The harness for the picker: the control the query builder rests on,
// built before the builder because three other screens want it too.
//
// What this layer covers. Two halves are pure and are driven directly —
// what a game's vocabulary becomes as options, and what a filter
// matches — and the component half is driven through the DOM stub the
// other components use, because the properties that matter are about
// what it emits and what it keeps:
//
//   - a choice answers with the **key the game wrote**, never a label,
//     because a misspelled key is the commonest way a query fails and
//     the whole point of a picker is that it cannot be misspelled;
//   - options that go away take an impossible choice with them;
//   - an empty list and an empty filter are two different sentences.
//
// Run directly: `node internal/web/jstest/picker_test.mjs`.
// internal/web/static_picker_test.go shells out to it too.

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

const { optionsFrom, matches, MstPicker, CHOOSE_EVENT, FILTER_FROM, EMPTY_LABEL, NONE_LABEL } =
  await import("../static/components/mst-picker.js");

// --- What a game's vocabulary becomes ---------------------------------

const TYPES = [
  { key: "quest", label: "Quest", label_plural: "Quests" },
  { key: "zone", label: "Zone" },
  // A type declared without a label is still a type a person must be
  // able to choose.
  { key: "talent" },
];

check("a listing becomes options, labelled by the game's own words", optionsFrom(TYPES), [
  { key: "quest", label: "Quest" },
  { key: "zone", label: "Zone" },
  { key: "talent", label: "talent" },
]);

check("a row with no key is not an option", optionsFrom([{ label: "Nameless" }, ...TYPES]).length, 3);

// "No choice" is offered only when the clause is optional, because a
// picker that always offers it teaches that every clause is.
check("the optional clause offers no choice first", optionsFrom(TYPES, { none: true })[0], {
  key: "",
  label: NONE_LABEL,
});
check("and the others follow it", optionsFrom(TYPES, { none: true }).length, 4);

// --- What a filter matches --------------------------------------------

check("the label matches", matches({ key: "available_to", label: "Available to" }, "avail"), true);
// The key matters as much: the handle a designer meets in every error
// message is the one they will type.
check("and so does the key", matches({ key: "available_to", label: "Available to" }, "able_to"), true);
check("case is not the question", matches({ key: "quest", label: "Quest" }, "QUE"), true);
check("an empty filter matches everything", matches({ key: "quest", label: "Quest" }, "   "), true);
check("and something else matches nothing", matches({ key: "quest", label: "Quest" }, "zone"), false);

// --- The control ------------------------------------------------------

function picker(options, init = {}) {
  return new MstPicker({ document: dom.document, options, ...init });
}

function optionsOf(control) {
  const menu = control.root.childNodes[1];
  return menu.childNodes
    .filter((node) => node.attributes && node.attributes.get("class") === "option")
    .map((node) => node.attributes.get("data-key"));
}

{
  const control = picker(optionsFrom(TYPES));
  check("a picker with no choice says what it is for", control.root.childNodes[0].textContent, "Choose");
  check("and offers everything", optionsOf(control), ["quest", "zone", "talent"]);

  const heard = [];
  control.addEventListener(CHOOSE_EVENT, (event) => heard.push(event.detail));
  control.choose("zone");
  // **The key the game wrote, never the label.** A builder that read the
  // label back would be one misspelling away from the failure a picker
  // exists to prevent.
  check("choosing answers with the option itself", heard, [{ key: "zone", label: "Zone" }]);
  check("and the control says the chosen words", control.root.childNodes[0].textContent, "Zone");
  check("and closes", control.root.open, false);

  // A choice that is not on offer is not a choice.
  check("an unknown key chooses nothing", control.choose("dragon"), null);
  check("and the choice stands", control.chosen, "zone");
}

// Options that go away take an impossible choice with them: the field
// picker's list changes the moment the type does, and a `color_by`
// naming a field the new type has no column for would be a document the
// server refuses.
{
  const control = picker(optionsFrom(TYPES), { chosen: "zone" });
  control.setOptions(optionsFrom([{ key: "quest", label: "Quest" }]));
  check("a choice the new list does not contain is dropped", control.chosen, null);
  check("and the control asks again", control.root.childNodes[0].textContent, "Choose");
}

// Two absences, two sentences.
{
  const empty = picker([]);
  const menu = empty.root.childNodes[1];
  check("a game with nothing to offer says so", menu.childNodes[0].textContent, EMPTY_LABEL);

  const many = picker(optionsFrom(TYPES));
  many.query = "zzz";
  many.draw();
  const missed = many.root.childNodes[1].childNodes[0];
  check("a filter that matched nothing says that instead", missed.textContent, "nothing matches “zzz”");
}

// The filter appears only when the list is long enough to need one: a
// filter over four relation types asks a person to type what they can
// already see.
{
  const few = picker(optionsFrom(TYPES));
  const hasFilter = (control) =>
    control.root.childNodes[1].childNodes.some((node) => node.attributes?.get("class") === "filter");
  check("a short list is its own filter", hasFilter(few), false);

  const rows = [];
  for (let i = 0; i < FILTER_FROM; i += 1) rows.push({ key: "k" + i, label: "Option " + i });
  check("a long one grows a box", hasFilter(picker(optionsFrom(rows))), true);
}

if (failures > 0) {
  console.error(failures + " assertion(s) failed");
  process.exit(1);
}
console.log("the picker: all assertions passed");
