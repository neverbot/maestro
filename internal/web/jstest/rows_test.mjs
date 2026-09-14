// The harness for internal/web/static/rows.js, and specifically for the
// one part of it that is now a control rather than a label: a column
// heading the listing can be ordered by.
//
// What it holds, and why each one is here rather than left to a browser:
//
//   - **Pressing a column asks for that column**, and pressing the
//     column already sorted asks for its reverse. A header whose every
//     press asked for the same ascending order is a sort that looks
//     wired and is not.
//   - **A column the server has no order for gets no control.** The
//     declared-field columns cannot be ordered yet, and a heading that
//     looked pressable and refused would be worse than a plain one.
//   - **The arrow is never the only carrier.** The direction is in the
//     button's accessible name, in words, because an arrow is one
//     character to a screen reader and this product does not let a glyph
//     or a colour carry a value alone.
//
// Run directly: `node internal/web/jstest/rows_test.mjs`.
// internal/web/static_rows_test.go shells out to it too.

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

const { headerRow, row, SORT_MARKS } = await import("../static/rows.js");

const doc = dom.document;

function header(sorted) {
  const asked = [];
  const row = headerRow(doc, {
    label: "Quest",
    key: "key",
    cells: [{ text: "Level", numeric: true }],
    sort: { label: "name", key: "key" },
    sorted,
    onSort: (next) => asked.push(next),
  });
  const buttons = [];
  for (const cell of row.children) {
    for (const child of cell.children || []) {
      if (child.tagName === "button") buttons.push(child);
    }
  }
  return { row, buttons, asked };
}

// --- A declared field's own heading -----------------------------------
//
// The catalogue's field columns are orderable too: the server orders by
// the stored jsonb value, so a number sorts as a number. A cell heading
// that names an order is a control; one that names none stays a label,
// which is what every other listing in the product passes.
function fieldHeader(sorted) {
  const asked = [];
  const row = headerRow(doc, {
    label: "Quest",
    key: "key",
    cells: [{ text: "Level", numeric: true, order: "field:level" }, { text: "Reward" }],
    sort: { label: "name", key: "key" },
    sorted,
    onSort: (next) => asked.push(next),
  });
  const buttons = [];
  const walk = (node) => {
    for (const child of node.children || []) {
      if (child.tagName === "button") buttons.push(child);
      walk(child);
    }
  };
  walk(row);
  return { row, buttons, asked };
}

const withField = fieldHeader("name");
check("a field column with an order carries a button and one without does not",
  withField.buttons.map((b) => b.textContent), ["Quest \u2191", "key", "Level"]);

withField.buttons[2].click();
check("pressing a field column asks for that field", withField.asked, ["field:level"]);

const byField = fieldHeader("field:level");
check("the field column says it is the sorted one",
  [byField.buttons[0].textContent, byField.buttons[2].textContent],
  ["Quest", "Level \u2191"]);
byField.buttons[2].click();
check("pressing it again reverses it", byField.asked, ["-field:level"]);

const fresh = header("name");
check("a header whose field columns name no order carries two buttons",
  fresh.buttons.length, 2);

check("the sorted column says which way it is sorted, in words and with a mark",
  [fresh.buttons[0].textContent, fresh.buttons[0].getAttribute("aria-label")],
  ["Quest " + SORT_MARKS.asc, "Sorted by Quest, first to last. Sort last to first."]);

check("an unsorted column says what pressing it would do",
  [fresh.buttons[1].textContent, fresh.buttons[1].getAttribute("aria-label")],
  ["key", "Sort by key"]);

fresh.buttons[0].click();
check("pressing the sorted column reverses it", fresh.asked, ["-name"]);

fresh.buttons[1].click();
check("pressing another column starts it ascending", fresh.asked, ["-name", "key"]);

const reversed = header("-name");
reversed.buttons[0].click();
check("pressing a reversed column puts it back", reversed.asked, ["name"]);
check("a reversed column says so both ways",
  [reversed.buttons[0].textContent, reversed.buttons[0].getAttribute("aria-label")],
  ["Quest " + SORT_MARKS.desc, "Sorted by Quest, last to first. Sort first to last."]);

// A header with no sort spec is what every other listing in the product
// still passes, and it must be exactly what it was: a label.
const plain = headerRow(doc, { label: "Quest", key: "key", cells: [{ text: "Level" }] });
let controls = 0;
for (const cell of plain.children) {
  for (const child of cell.children || []) if (child.tagName === "button") controls += 1;
}
check("a header nobody said was sortable has no controls", controls, 0);
check("and it still says its columns",
  [plain.children[0].textContent, plain.children[1].textContent],
  ["Quest", "key"]);

// --- What "there is more" means ---------------------------------------
//
// Every listing answer carries `next_cursor`, and a server that has
// reached the end sends `""`. Six pages tested it with
// `typeof body.next_cursor === "string"`, which `""` passes — so the
// pager stayed on screen at the end of a listing, and pressing it
// re-fetched the first page and appended it again. Seen in a browser on
// the images list: two images became four, under a count that then said
// "4 images".

const { nextCursorOf } = await import("../static/rows.js");

check("the end of a listing is not a cursor", [
  nextCursorOf({ next_cursor: "" }),
  nextCursorOf({}),
  nextCursorOf(null),
  nextCursorOf({ next_cursor: 7 }),
  nextCursorOf({ next_cursor: "abc" }),
], [null, null, null, null, "abc"]);

// --- What a screen reader is told ------------------------------------
//
// A catalogue is a `ul` of `li`s in a subgrid: the right thing to look
// at, and ten flat strings per row to listen to, with no column ever
// named. The roles are meaningful precisely because the shape is fixed —
// every row emits the same cells in the same order — so a cell is always
// under the heading that names it.

const { markTables } = await import("../static/rows.js");

const sorted = header("-name");
check("a row is a row and every one of its cells is a cell",
  [sorted.row.getAttribute("role"), ...sorted.row.children.map((c) => c.getAttribute("role"))],
  ["row", "columnheader", "columnheader", "columnheader", "columnheader", "columnheader", "columnheader"]);

const line = row(doc, { label: "Hogger", key: "hogger", cells: [{ text: "12" }], count: "3", flag: "invalid" });
check("a content row names its cells too",
  [line.getAttribute("role"), ...line.children.map((c) => c.getAttribute("role"))],
  ["row", "cell", "cell", "cell", "cell", "cell", "cell", "cell"]);

// **aria-sort, and the button's name, are two answers to two
// questions.** The button says what pressing it would do; aria-sort says
// how the table is ordered right now, which is what a reader arriving at
// the column asks. A column that can be sorted and is not says "none",
// which is not the same statement as a column that cannot be sorted and
// carries no attribute at all.
check("the sorted column, the sortable one and the plain one say three different things",
  [
    sorted.row.children[0].getAttribute("aria-sort"),
    sorted.row.children[1].getAttribute("aria-sort"),
    sorted.row.children[2].getAttribute("aria-sort"),
  ],
  ["descending", "none", null]);

check("a field column the listing is sorted by says ascending",
  fieldHeader("field:level").row.children[2].getAttribute("aria-sort"),
  "ascending");

// The list itself, which is where the roles above become legal: a row
// role outside a table is worse than no role at all.
const list = doc.createElement("ul");
list.className = "catalogue wide";
const shell = doc.createElement("main");
shell.append(list);
markTables({ querySelectorAll: (selector) => (selector === "ul.catalogue" ? [list] : []) });
check("the catalogue itself is the table", list.getAttribute("role"), "table");

if (failures > 0) {
  console.log(failures + " failed");
  process.exit(1);
}
console.log("all passed");
