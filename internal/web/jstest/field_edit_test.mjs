// The harness for the second write a person can make: an entity's field
// values, edited where they are read.

import path from "node:path";
import { register } from "node:module";
import { fileURLToPath } from "node:url";

register(new URL("./importmap_loader.mjs", import.meta.url), {
  parentURL: import.meta.url,
  data: { staticDir: path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "static"), shell: "game.html" },
});

import { install } from "./svg_dom.mjs";

const dom = install();
// app.js makes two lookups at module scope and the module graph reaches
// it through pages/entity.js, exactly as the rename harness records.
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
  controlFor,
  fieldsWith,
  fieldList,
  fieldRows,
  relationGroups,
  relationList,
  formatValue,
  wireFieldEdits,
  BOOL_FALSE,
  BOOL_TRUE,
  CLEAR_HINT,
  NOT_A_NUMBER,
} = await import("../static/pages/entity.js");

const doc = dom.document;

const SCHEMA = [
  { key: "min_level", type: "number" },
  { key: "summary", type: "longtext" },
  { key: "faction", type: "enum", options: ["alliance", "horde"] },
  { key: "repeatable", type: "bool" },
  { key: "tags", type: "list<text>" },
];

const ENTITY = {
  type_key: "quest",
  key: "hogger",
  name: "Wanted: Hogger",
  version: 3,
  fields: { min_level: 9, summary: "A gnoll.", faction: "alliance", tags: ["elwynn"] },
  // The rendering the browser's own route sends beside the values. The
  // tool's answer carries no such key, which is why the page reads it by
  // key and shows text when it is not there.
  fields_html: { summary: "<p>A gnoll.<br>\nIn Elwynn.</p>" },
};

// --- The controls, on their own ---------------------------------------

check("a number takes a number control", controlFor(doc, SCHEMA[0], 9).el.type, "number");
check("a bool takes a checkbox", controlFor(doc, SCHEMA[3], true).el.type, "checkbox");
check("a longtext takes a textarea", controlFor(doc, SCHEMA[1], "x").el.tagName, "textarea");
check("a list takes a textarea", controlFor(doc, SCHEMA[4], ["a"]).el.tagName, "textarea");
check("an enum takes a select", controlFor(doc, SCHEMA[2], "horde").el.tagName, "select");

// The enum offers the declared options and a named way to clear it. A
// blank option in a list of words reads as a rendering fault.
check(
  "the enum offers exactly what the type declared, plus a named nothing",
  controlFor(doc, SCHEMA[2], "horde").el.children.map((option) => option.value),
  ["", "alliance", "horde"],
);

// **Absent and empty stay two answers.**
{
  const text = controlFor(doc, SCHEMA[1], "A gnoll.");
  text.el.value = "";
  check("clearing a text field removes the value", text.read(), { absent: true });
  text.el.value = "still here";
  check("and typing into it stores what was typed", text.read(), { value: "still here" });
}

{
  const number = controlFor(doc, SCHEMA[0], 9);
  number.el.value = "twelve";
  check("a number that is not one is refused before the round trip", number.read(), { problem: NOT_A_NUMBER });
  number.el.value = "12";
  check("and a number is stored as a number, not as its text", number.read(), { value: 12 });
  number.el.value = "";
  check("an emptied number is removed rather than stored as zero", number.read(), { absent: true });
}

{
  const list = controlFor(doc, SCHEMA[4], ["elwynn", "quest"]);
  check("a list edits as one value per line", list.el.value, "elwynn\nquest");
  list.el.value = "  elwynn  \n\n westfall \n";
  check("blank lines are not values and neither is surrounding space", list.read(), { value: ["elwynn", "westfall"] });
  list.el.value = "\n  \n";
  check("a list with nothing in it is absent, not an empty list", list.read(), { absent: true });
}

{
  const bool = controlFor(doc, SCHEMA[3], undefined);
  check("a bool a row does not carry starts unticked", bool.el.checked, false);
  bool.el.checked = true;
  check("and answers with true, not with the word", bool.read(), { value: true });
}

// --- The whole row goes back ------------------------------------------

check(
  "one value changes and every other one survives",
  fieldsWith({ a: 1, b: "two", c: [3] }, "b", { value: "three" }),
  { a: 1, b: "three", c: [3] },
);
check(
  "and clearing one removes that key alone",
  fieldsWith({ a: 1, b: "two" }, "b", { absent: true }),
  { a: 1 },
);
// The object handed in is not the one written: a page that mutated the
// entity it is holding would report a write that had not happened yet.
{
  const held = { a: 1 };
  fieldsWith(held, "b", { value: 2 });
  check("the row the page holds is not edited in place", held, { a: 1 });
}

// --- Through the page --------------------------------------------------

function client(script) {
  const calls = [];
  return {
    calls,
    async writeEntity(entity, changes) {
      calls.push({ call: "writeEntity", version: entity.version, fields: changes.fields });
      return script.writes.shift();
    },
    async getEntity(typeKey, key) {
      calls.push({ call: "getEntity", typeKey, key });
      return script.reads.shift();
    },
  };
}

function stage(script) {
  const rows = fieldRows(SCHEMA, ENTITY);
  const list = fieldList(doc, rows);
  const opened = { slug: "azeroth", client: client(script), document: doc };
  const wired = wireFieldEdits(doc, opened, {
    entity: { ...ENTITY, fields: { ...ENTITY.fields } },
    fieldsList: list,
    rows,
  }, SCHEMA);
  return { list, rows, wired, opened };
}

// --- An edge's own fields, in a row -----------------------------------

// The same rule the catalogue applies by declared type, applied here by
// length: this page reads the slim relation type listing, which carries
// no field schema, so what it knows about a value is how long it is.
{
  const long = "notes ".padEnd(130, "x");
  const groups = relationGroups([
    { type_key: "grants", source: { type_key: "guild", key: "zulk" }, target: { type_key: "ability", key: "invocar" },
      fields: { notes: long, tier: "one" } },
  ], "in", new Map([["grants", "otorga"]]));
  const list = relationList(doc, "azeroth", groups);
  const edge = list.children.find((child) => child.getAttribute("class") !== "catalogue-head");
  const cells = edge.children.filter((child) => (child.getAttribute("class") || "").includes("catalogue-cell"));
  const clipped = cells.filter((cell) => (cell.getAttribute("class") || "").includes("oneline"));
  check("an edge field too long for a row is clipped, and a short one is not", clipped.length, 1);
}

// A value whose declared type this page was never told — an edge's
// field, because the relation type listing carries no schema — is shown
// as its JSON text, and `true` is a word in one language on a screen in
// another.
check("an undeclared boolean is still a word a reader knows",
  [formatValue("", true), formatValue("", false), formatValue("", 12)],
  [BOOL_TRUE, BOOL_FALSE, "12"]);

// --- A boolean, drawn on this screen too ------------------------------

// repeatable is the fourth declared field, so its value cell is index 3.
{
  const entity = { ...ENTITY, fields: { ...ENTITY.fields, repeatable: false } };
  const rows = fieldRows(SCHEMA, entity);
  const list = fieldList(doc, rows);
  const cell = cellOf(list, 3);
  check("a boolean field draws its value", [cell.textContent, cell.children.length], ["", 1]);
  check("and says what the drawing is", cell.getAttribute("aria-label"), "no");

  // The regression editable() caused for the prose, asserted for the
  // mark: this function replaces the cell's children with its own.
  const opened = { slug: "azeroth", client: client({ writes: [], reads: [] }), document: doc };
  wireFieldEdits(doc, opened, { entity, fieldsList: list, rows }, SCHEMA);
  const kept = cell.children[0] && cell.children[0].children ? cell.children[0].children[0] : null;
  check("the drawing survives the writing being wired", kept ? kept.tagName : "", "svg");
  check("and the cell is still named", cell.getAttribute("aria-label"), "no");
}

// --- The prose a longtext is ------------------------------------------

// summary is the second declared field, so its value cell is index 1.
{
  const rows = fieldRows(SCHEMA, ENTITY);
  const list = fieldList(doc, rows);
  const cell = cellOf(list, 1);
  check("a longtext cell says it is prose", cell.getAttribute("class"), "field-value field-prose");
  check("and holds the rendering rather than the source", cell.children[0].innerHTML, "<p>A gnoll.<br>\nIn Elwynn.</p>");
  check("a field with no rendering is still text", cellOf(list, 0).textContent, "9");

  // **The regression this is really here for.** editable() replaces the
  // cell's children with its own, so a page a member can edit flattened
  // the block back to one line of source the moment the writing was
  // wired — and every read-only harness passed.
  const opened = { slug: "azeroth", client: client({ writes: [], reads: [] }), document: doc };
  wireFieldEdits(doc, opened, { entity: { ...ENTITY }, fieldsList: list, rows }, SCHEMA);
  // Read defensively, so a cell flattened back to text fails with the
  // comparison this is about rather than with a TypeError two lines down.
  const kept = cell.children[0] && cell.children[0].children ? cell.children[0].children[0] : null;
  check("the rendering survives the writing being wired", kept ? kept.innerHTML : "", "<p>A gnoll.<br>\nIn Elwynn.</p>");
  // A block, not a span: paragraphs inside an inline element are invalid
  // nesting and lay out as one line whatever the stylesheet says.
  check("and the holder it sits in is a block", cell.children[0].tagName, "div");
  check("and the cell still says it is prose", cell.getAttribute("class"), "field-value field-prose");
}

// A row is an element now, and its four cells are found by position —
// label, value, type, the way to change it — so a change to that shape is
// caught here rather than by a page that quietly wires nothing.
function cellOf(list, index) {
  return list.children[index].children[1];
}

function actionOf(list, index) {
  return list.children[index].children[3];
}

{
  const { list, wired, opened } = stage({
    writes: [{ ok: true, result: { written: [{ version: 4 }], failed: [] } }],
    reads: [],
  });
  check("every declared field is wired", wired.length, SCHEMA.length);
  // **The way to change a value is in the row's own column**, not inside
  // the value: the value cell holds what there is to read, and a control
  // sitting in it is a control that cannot be revealed by the row without
  // revealing the value's own layout with it.
  check("a row is four cells", list.children[0].children.length, 4);
  const inAction = actionOf(list, 0).children[0] || null;
  check("and the control is in the last one",
    [actionOf(list, 0).children.length, inAction ? inAction.getAttribute("class") : ""],
    [1, "field-edit"]);
  check("the value cell holds no control", cellOf(list, 0).children.length, 3);

  const editor = wired[0];
  editor.open.click();
  const control = editor.form.children[0];
  check("the control carries the value the row holds", control.value, "9");
  control.value = "12";
  await editor.form.dispatch("submit", { preventDefault() {} });

  check("the write states the version the page read", opened.client.calls[0].version, 3);
  check(
    "and sends the whole row with one value changed",
    opened.client.calls[0].fields,
    { min_level: 12, summary: "A gnoll.", faction: "alliance", tags: ["elwynn"] },
  );
  check("the value on the screen is the one that was written", cellOf(list, 0).children[0].textContent, "12");
  check("and the control closes", editor.form.hidden, true);
}

// A second edit states the version the **first** one produced: a page
// that kept stating the version it loaded with would refuse its own
// second write.
{
  const { wired, opened } = stage({
    writes: [
      { ok: true, result: { written: [{ version: 4 }], failed: [] } },
      { ok: true, result: { written: [{ version: 5 }], failed: [] } },
    ],
    reads: [{ ok: true, result: { fields_html: { summary: "<p>A gnoll, still.</p>" } } }],
  });
  wired[0].open.click();
  wired[0].form.children[0].value = "12";
  await wired[0].form.dispatch("submit", { preventDefault() {} });
  wired[1].open.click();
  wired[1].form.children[0].value = "A gnoll, still.";
  await wired[1].form.dispatch("submit", { preventDefault() {} });
  check("the second write states the version the first produced", opened.client.calls[1].version, 4);
  // **A saved longtext is read back for its rendering.** The write
  // answers with a batch report and this browser cannot render markdown,
  // so without the re-read a field read as prose until somebody edited it
  // and as source for the rest of the session.
  check("a saved longtext is read back", opened.client.calls[2], { call: "getEntity", typeKey: "quest", key: "hogger" });
  check(
    "and carries the first write's value with it",
    opened.client.calls[1].fields.min_level,
    12,
  );
}

// Clearing a value: the row loses the key, and the screen says the word
// rather than going blank.
{
  const { list, wired, opened } = stage({
    writes: [{ ok: true, result: { written: [{ version: 4 }], failed: [] } }],
    reads: [],
  });
  wired[1].open.click();
  wired[1].form.children[0].value = "";
  await wired[1].form.dispatch("submit", { preventDefault() {} });
  check("the key is gone from the row", Object.keys(opened.client.calls[0].fields).sort(), ["faction", "min_level", "tags"]);
  check("and the cell says the absence in words", cellOf(list, 1).children[0].textContent, "no summary");
  check("wearing the class that carries it beside the word", cellOf(list, 1).className, "field-value absent");
}

// A refusal about one value belongs under that value.
{
  const { list, wired } = stage({
    writes: [{
      ok: true,
      result: { written: [], failed: [{ code: "schema_violation", message: "fields.min_level: must be at least 1" }] },
    }],
    reads: [],
  });
  wired[0].open.click();
  wired[0].form.children[0].value = "0";
  await wired[0].form.dispatch("submit", { preventDefault() {} });
  // The value cell holds the value, the form and the sentence; the way
  // to change it has a column of its own.
  const error = cellOf(list, 0).children[2];
  check("the server's sentence lands under the control it is about", error.textContent, "fields.min_level: must be at least 1");
  check("the edit is still there", wired[0].form.children[0].value, "0");
  check("and the page does not claim the value changed", cellOf(list, 0).children[0].textContent, "9");
}

// A number that is not one never reaches the server.
{
  const { list, wired, opened } = stage({ writes: [], reads: [] });
  wired[0].open.click();
  wired[0].form.children[0].value = "twelve";
  await wired[0].form.dispatch("submit", { preventDefault() {} });
  check("nothing was sent", opened.client.calls.length, 0);
  check("and the reason is under the control", cellOf(list, 0).children[2].textContent, NOT_A_NUMBER);
}

// The conflict, about a value rather than a name.
{
  const { list, wired, opened } = stage({
    writes: [{ ok: true, result: { written: [], failed: [{ code: "version_conflict" }] } }],
    reads: [{ ok: true, result: { ...ENTITY, version: 4, fields: { ...ENTITY.fields, min_level: 20 } } }],
  });
  wired[0].open.click();
  wired[0].form.children[0].value = "12";
  await wired[0].form.dispatch("submit", { preventDefault() {} });

  check("it read the row to find out what the other writer put there", opened.client.calls[1].call, "getEntity");
  // One write, then one read. **No second write**: a page that re-read
  // the version and wrote again would be last-writer-wins with nobody
  // told.
  check("and it did not write again on its own", opened.client.calls.length, 2);
  check("the page does not claim the value changed", cellOf(list, 0).children[0].textContent, "9");
}

// The conflict with nothing to decide: the other writer wrote the same
// value, so the person's intent already holds.
{
  const { list, wired, opened } = stage({
    writes: [{ ok: true, result: { written: [], failed: [{ code: "version_conflict" }] } }],
    reads: [{ ok: true, result: { ...ENTITY, version: 4, fields: { ...ENTITY.fields, min_level: 12 } } }],
  });
  wired[0].open.click();
  wired[0].form.children[0].value = "12";
  await wired[0].form.dispatch("submit", { preventDefault() {} });
  check("the value settles at what both of them wrote", cellOf(list, 0).children[0].textContent, "12");
  check("with no box to dismiss", opened.client.calls.length, 2);
}

// The one piece of copy the control carries, and the one type it does
// not carry it for: a bool has two answers and neither is "no answer".
{
  const { wired } = stage({ writes: [], reads: [] });
  wired[1].open.click();
  check("a text control says how to remove the value", wired[1].form.children[3].textContent, CLEAR_HINT);
  wired[3].open.click();
  check("a checkbox does not", wired[3].form.children[3].textContent, "");
  check("and the formatted bool is still the product's word", BOOL_TRUE, "yes");
}

if (failures > 0) {
  console.log(failures + " failed");
  process.exit(1);
}
console.log("all passed");
