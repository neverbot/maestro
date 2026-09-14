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

const { headerRow, SORT_MARKS } = await import("../static/rows.js");

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

const fresh = header("name");
check("the two orderable columns carry a button and the field column does not",
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

if (failures > 0) {
  console.log(failures + " failed");
  process.exit(1);
}
console.log("all passed");
