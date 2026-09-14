// The row every list in this product is made of.
//
// **It lives here, above both of its callers, because it has two.** It
// was a function in pages/page.js, which the seven page modules import;
// then the game picker in app.js needed the same row and hand-built its
// own instead — a second implementation of one shape, which design.md
// asks to be reported as a defect rather than tolerated. app.js cannot
// import pages/page.js (that module imports app.js), so the shape moved
// to a module with no imports of its own and both sides take it from
// here. pages/page.js re-exports it, so no page module changed.
//
// **Every string goes in through textContent**, without exception: a
// label is a designer's own words, a key is what an agent sent, and
// neither is markup this product ever interprets. `href` turns the label
// into a link and is built by the address functions in pages/page.js, so
// a row can only ever point at a slug address.

// row builds one line of a catalogue: a label, a key in mono, a count,
// and the one flag that ever asks a designer to do something.
//
// **Every string on it goes in through textContent**, without exception:
// a label is a designer's own words, a key is what an agent sent, and
// neither is markup this product ever interprets. `href` turns the label
// into a link and is built by the address functions above, so a row can
// only ever point at a slug address.
// **Every row emits the same number of cells, always.** A row that
// appended a span only when it had something to put in it produced
// sibling grids with different track counts, and sibling grids share
// nothing: the column header sat 18px to the right of the values it
// named, two rows with different key lengths disagreed with each other,
// and the `faction` header's box overlapped the `x` column's. A header
// over the wrong column is worse than no header, because it is
// confidently wrong.
//
// The fix is a fixed shape — name, key, three content cells, count —
// and `subgrid` on the row so every one of them takes its widths from
// the list rather than from itself. Three because that is what the
// catalogue shows; an empty cell costs an `auto` track that collapses to
// nothing.
export const CATALOGUE_CELLS = 3;

// nextCursorOf reads a listing answer's cursor, and is the one place
// this front end decides what "there is more" means.
//
// **An empty string is not a cursor.** Six pages wrote
// `typeof body.next_cursor === "string" ? body.next_cursor : null`, and
// every server listing answers `next_cursor: ""` when the page is the
// last one — so `""` passed the test, the pager stayed on screen, and
// pressing it re-fetched the first page and appended it again: two
// images became four, under a count that then said "4 images". Seen in
// a browser on the images list, which is the one listing that had never
// been rendered with a row in it.
//
// It lives here, beside `row`, for the reason `row` does: every module
// that draws a listing already imports this one, and `pages/page.js`
// cannot be imported by `app.js` or by `doc.js`.
export function nextCursorOf(body) {
  const cursor = body && typeof body.next_cursor === "string" ? body.next_cursor : "";
  return cursor === "" ? null : cursor;
}

// countLabel spells a count with the right noun, so "1 entities" never
// reaches a designer's screen.
//
// It lives here rather than in pages/page.js for the reason `row` does:
// **app.js needs it too** — the picker counts games — and page.js
// imports app.js, so importing it back would close a cycle. page.js
// re-exports it, so the seven page modules are unaffected.
export function countLabel(count, singular, plural) {
  return `${count} ${count === 1 ? singular : plural}`;
}

// **The list is a table and says so.** A catalogue is a `ul` of `li`s
// carrying a fixed six cells in a subgrid, which is the right thing
// visually and, to a screen reader, ten flat strings per row with no
// column ever named. The four ARIA roles below cost four attributes and
// are meaningful precisely because the shape is fixed: every row emits
// the same cells in the same order, so a cell always sits under the
// heading that names it.
//
// `markTable` is what a page calls on the list element itself, because
// the list is in the shell's HTML and these functions only build its
// children. A `ul` with `role="table"` must hold rows and nothing else,
// which is already true of every catalogue in this product.
export function markTable(listEl, label) {
  if (!listEl) return;
  listEl.setAttribute("role", "table");
  if (typeof label === "string" && label !== "") listEl.setAttribute("aria-label", label);
}

// markTables marks every catalogue on a page, and is what the pages
// actually call.
//
// It is one sweep over the shell rather than a call beside each list
// because the roles below are **not optional once a row carries one**:
// `role="row"` is only meaningful inside a table, and a row role in a
// plain list is worse than no role at all. Every catalogue in this
// product is the same fixed-shape grid, so every one of them is a table
// — and a sweep cannot be the thing somebody forgets when they add the
// next screen.
export function markTables(doc) {
  const lists = doc && typeof doc.querySelectorAll === "function"
    ? doc.querySelectorAll("ul.catalogue")
    : [];
  for (const list of lists) markTable(list, "");
}

export function row(doc, spec) {
  const item = doc.createElement("li");
  item.setAttribute("role", "row");

  // **The whole row is the target, or the hover is a lie.** The row
  // highlights on hover and the comment beside that rule says "the row
  // itself is the affordance" — but only the label was a link, so a
  // promise made over 100% of a row was kept on 32% of it and a click
  // forty pixels from the right edge did nothing. The link stretches
  // over the row through a pseudo-element, which keeps one anchor and
  // one accessible name rather than wrapping every cell.
  const label = doc.createElement(spec.href ? "a" : "span");
  label.className = spec.href ? "catalogue-label stretched" : "catalogue-label";
  if (spec.href) label.href = spec.href;
  label.textContent = spec.label ?? "";
  // The anchor carries the cell role rather than being wrapped in one:
  // an extra element between the row and the link would break the
  // subgrid the whole row's alignment rests on.
  label.setAttribute("role", "cell");
  item.append(label);

  const handle = doc.createElement("code");
  handle.className = "catalogue-key";
  handle.setAttribute("role", "cell");
  handle.textContent = spec.key ?? "";
  item.append(handle);

  // The columns this row's own content has, between the key and the
  // count. A catalogue of a thousand entities showing a name and a key
  // shows a reader the two things they already knew; what they came to
  // compare is the fields the type declares. The row is **widened**
  // rather than copied, which is what design.md asks for when a second
  // screen needs something the first has.
  const cells = Array.isArray(spec.cells) ? spec.cells : [];
  for (let i = 0; i < CATALOGUE_CELLS; i += 1) {
    const cell = cells[i];
    const value = doc.createElement("span");
    value.className = cell && cell.absent ? "catalogue-cell absent" : "catalogue-cell";
    if (cell && cell.numeric) value.classList.add("numeric");
    // A cell whose meaning has a treatment of its own — a route's three
    // states are the only one today. It is a class and not a colour at
    // the call site, so the stylesheet stays the one place a meaning is
    // spelled.
    if (cell && cell.status) value.classList.add("status", cell.status);
    value.setAttribute("role", "cell");
    value.textContent = cell ? (cell.text ?? "") : "";
    item.append(value);
  }

  const tally = doc.createElement("span");
  tally.className = "catalogue-count";
  tally.setAttribute("role", "cell");
  tally.textContent = spec.count ?? "";
  item.append(tally);

  if (spec.flag) {
    const flag = doc.createElement("span");
    flag.className = "catalogue-invalid";
    // The flag shares the count's cell rather than adding a seventh
    // column: it is a mark *on* a row, and a column that is empty on
    // every row but one is a column that says nothing six times.
    flag.setAttribute("role", "cell");
    flag.textContent = spec.flag;
    item.append(flag);
  }
  return item;
}

// headerRow names the columns a catalogue is showing. **A column with no
// header is a number a reader has to guess at**: the quests catalogue
// shipped `ash 210 350` per row with nothing saying which was the region,
// which the reward and which the requirement.
//
// It is the same grid as a row, so the two line up without either knowing
// the other's widths, and it carries no link: a heading is not a
// destination.
//
// **A heading is not a destination and may still be a control.** A
// column the listing can be ordered by carries a button — `spec.sort`
// names the order it sets, `spec.sorted` the order the listing is
// currently in, and `spec.onSort` is called with the order to ask for.
// The button is a button and not a link because it changes what is being
// shown rather than where the reader is, and because a link would put a
// sort in the browser's history between a reader and the page they came
// from.
//
// A column with no `sort` renders exactly what it did before: the only
// orders that exist are the ones the server has a statement for, and a
// header that looked sortable and was not would be worse than a plain
// one. `fields` cannot be ordered yet, and their headings say nothing
// about it rather than offering a control that refuses.
export function headerRow(doc, spec) {
  const item = doc.createElement("li");
  item.className = "catalogue-head";
  item.setAttribute("role", "row");

  item.append(headCell(doc, spec.label ?? "", "", spec.sort && spec.sort.label, spec));

  item.append(headCell(doc, spec.key ?? "", "catalogue-key", spec.sort && spec.sort.key, spec));

  const heads = Array.isArray(spec.cells) ? spec.cells : [];
  for (let i = 0; i < CATALOGUE_CELLS; i += 1) {
    const head = doc.createElement("span");
    head.className = "catalogue-cell";
    head.setAttribute("role", "columnheader");
    markSort(head, spec, heads[i] && typeof heads[i] === "object" ? heads[i].order : "");
    if (heads[i] && heads[i].numeric) head.classList.add("numeric");
    // A cell heading naming an order is a control like the two named
    // ones; a cell that names none stays a label. Which columns can be
    // ordered is the caller's knowledge, not this module's.
    const text = heads[i] ? heads[i].text ?? heads[i] : "";
    const order = heads[i] && typeof heads[i] === "object" ? heads[i].order : "";
    head.append(headContent(doc, text, order, spec));
    item.append(head);
  }
  // The count track, empty. It exists so the header spans the same six
  // tracks a row does; without it the header is one track short and
  // everything after the name drifts.
  const tally = doc.createElement("span");
  tally.className = "catalogue-count";
  tally.setAttribute("role", "columnheader");
  item.append(tally);
  return item;
}

// SORT_MARKS is what a sorted column says beside its name. The arrow is
// never the only carrier: the button's own accessible name says which
// way the next press would order the listing, in words, because an arrow
// is a glyph a screen reader reads as a character and a colour is a
// carrier this product does not let stand alone.
export const SORT_MARKS = { asc: "\u2191", desc: "\u2193" };

// headCell writes one heading, as a plain span or as the button that
// reorders the listing by that column.
function headCell(doc, text, className, order, spec) {
  const cell = doc.createElement("span");
  if (className !== "") cell.className = className;
  cell.setAttribute("role", "columnheader");
  markSort(cell, spec, order);
  cell.append(headContent(doc, text, order, spec));
  return cell;
}

// markSort states, on the heading itself, which way the listing is
// sorted.
//
// **`aria-sort` and not only the button's name.** The button says what
// pressing it would do, which is the right thing for a control and the
// wrong thing for a reader asking "how is this table ordered?" — a
// screen reader announces `aria-sort` when the column is reached, so the
// two answers are both available and neither depends on seeing an arrow.
// A column that can be sorted and is not says "none", which is a
// different statement from a column that cannot be sorted at all and
// carries no attribute.
function markSort(cell, spec, order) {
  if (!order) return;
  const sorted = typeof spec.sorted === "string" ? spec.sorted : "";
  if (sorted === order) {
    cell.setAttribute("aria-sort", "ascending");
    return;
  }
  if (sorted === "-" + order) {
    cell.setAttribute("aria-sort", "descending");
    return;
  }
  cell.setAttribute("aria-sort", "none");
}

// headContent is the heading itself: a text node's worth of words, or
// the button that reorders the listing by this column.
function headContent(doc, text, order, spec) {
  if (!order) {
    const plain = doc.createElement("span");
    plain.textContent = text;
    return plain;
  }
  const sorted = typeof spec.sorted === "string" ? spec.sorted : "";
  const active = sorted === order || sorted === "-" + order;
  const descending = sorted === "-" + order;
  // Pressing the column the listing is already sorted by reverses it;
  // pressing another starts that column ascending, which is the reading
  // order a person expects of a column they have not touched.
  const next = active && !descending ? "-" + order : order;

  const button = doc.createElement("button");
  button.type = "button";
  button.className = "catalogue-sort";
  if (active) button.classList.add("sorted");
  button.textContent = text + (active ? " " + (descending ? SORT_MARKS.desc : SORT_MARKS.asc) : "");
  button.setAttribute("aria-label",
    active && !descending
      ? "Sorted by " + text + ", first to last. Sort last to first."
      : active
        ? "Sorted by " + text + ", last to first. Sort first to last."
        : "Sort by " + text);
  button.addEventListener("click", () => {
    if (typeof spec.onSort === "function") spec.onSort(next);
  });
  return button;
}
