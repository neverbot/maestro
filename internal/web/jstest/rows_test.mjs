// The harness for internal/web/static/rows.js, and specifically for the
// one part of it that is now a control rather than a label: a column
// heading the listing can be ordered by.

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

const { nextCursorOf } = await import("../static/rows.js");

check("the end of a listing is not a cursor", [
  nextCursorOf({ next_cursor: "" }),
  nextCursorOf({}),
  nextCursorOf(null),
  nextCursorOf({ next_cursor: 7 }),
  nextCursorOf({ next_cursor: "abc" }),
], [null, null, null, null, "abc"]);

// --- What a screen reader is told ------------------------------------

const { markTables } = await import("../static/rows.js");

const sorted = header("-name");
check("a row is a row and every one of its cells is a cell",
  [sorted.row.getAttribute("role"), ...sorted.row.children.map((c) => c.getAttribute("role"))],
  ["row", "columnheader", "columnheader", "columnheader", "columnheader", "columnheader", "columnheader"]);

const line = row(doc, { label: "Hogger", key: "hogger", cells: [{ text: "12" }], count: "3", flag: "invalid" });
check("a content row names its cells too",
  [line.getAttribute("role"), ...line.children.map((c) => c.getAttribute("role"))],
  ["row", "cell", "cell", "cell", "cell", "cell", "cell", "cell"]);

// A value too long for a row is clipped to one line by a class, because
// the three content tracks size to what is in them: a six-hundred
// character longtext made the column that wide and the row five lines
// tall.
{
  const prose = row(doc, { label: "Hogger", key: "hogger", cells: [{ text: "A gnoll.", oneline: true }], count: "" });
  check("a cell that cannot hold its value says so", prose.children[2].getAttribute("class"), "catalogue-cell oneline");
  check("and an ordinary cell does not", line.children[2].getAttribute("class"), "catalogue-cell");
}

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

// --- What a heading does with the tracks it does not name -------------

// **An empty heading leaves its cell empty, so the track collapses.**
// `.catalogue-cell:not(:empty)` is what gives a cell its minimum, and
// the header put an empty span in every track the caller did not name —
// so a cell holding no word was still a cell holding an element, and the
// home's two bands carried three tracks of 28px and their gaps between
// the name and the count: 120px of nothing, on a screen whose complaint
// was that its tables looked wrong.
const sparse = headerRow(doc, { label: "Quest", key: "", count: "" });
check("a heading names a column or leaves it empty",
  [...sparse.children].map((cell) => (cell.children || []).length),
  [1, 0, 0, 0, 0, 0]);

// **The share heading stands over the bars.** The cell holds a
// right-aligned percentage and a bar that starts at a fixed x, and a
// heading at the cell's own left edge began where the percentage begins:
// 24px from the heading before it, with the numbers under its first
// letters. The same empty span the rows use for the percentage holds the
// place, so the word starts where every bar under it does.
const shared = headerRow(doc, { label: "Quest", count: "Count", share: "Of the total" });
const shareCell = [...shared.children].find((cell) => cell.className === "catalogue-share");
check("the share heading is placed, not left-aligned",
  shareCell ? [...shareCell.children].map((child) => child.className || child.textContent) : null,
  ["share-pct", "Of the total"]);

if (failures > 0) {
  console.log(failures + " failed");
  process.exit(1);
}
console.log("all passed");
