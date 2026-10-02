import { t } from "./i18n.js";
// The row every list in this product is made of.
export const CATALOGUE_CELLS = 3;

// The namespace an SVG element has to be created in; a createElement
// gives an HTMLUnknownElement that renders nothing.
export const SVG_NS = "http://www.w3.org/2000/svg";

// byWeight orders a catalogue by how much of the game each row is, and
// shares out the proportion. Alphabetical order hid the shape: in
// RL-Aeternum `rinde culto a` is 45 of 95 connections, nearly half the
// game's graph, and it sat ninth of twelve under the Spanish alphabet.
export function byWeight(rows, count) {
  const sorted = [...rows].sort((a, b) => Number(count(b)) - Number(count(a)));
  const total = sorted.reduce((n, r) => n + Number(count(r)), 0);
  return { sorted, total };
}


// nextCursorOf reads a listing answer's cursor, and is the one place
// this front end decides what "there is more" means.
export function nextCursorOf(body) {
  const cursor = body && typeof body.next_cursor === "string" ? body.next_cursor : "";
  return cursor === "" ? null : cursor;
}

// countLabel spells a count with the right noun, so "1 entities" never
// reaches a designer's screen.
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
export function markTable(listEl, label) {
  if (!listEl) return;
  listEl.setAttribute("role", "table");
  if (typeof label === "string" && label !== "") listEl.setAttribute("aria-label", label);
}

// markTables marks every catalogue on a page, and is what the pages
// actually call.
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

  // **The share of the whole, drawn.** A catalogue of counts answers
  // "how many" and never "how much of this game", which is the question
  // a designer opens the page with: `worships` is 45, and 45 is half of
  // RL-Aeternum's connections. The number alone does not say that and
  // the order alone does not say by how much.
  //
  // An SVG rect and not a styled div: `default-src 'self'` admits no
  // inline style attribute, so a width written as CSS would be dropped
  // by the browser without a word. A geometry attribute is not CSS.
  if (spec.share && Number(spec.share.of) > 0) {
    // The share and the bar are one cell, after the count: a percentage
    // printed before the number it is derived from reads as a different
    // measurement rather than the same one said twice.
    const cell = doc.createElement("span");
    cell.className = "catalogue-share";
    cell.setAttribute("role", "cell");

    const part = Math.max(0, Math.min(1, Number(spec.share.value) / Number(spec.share.of)));
    const pct = doc.createElement("span");
    pct.className = "share-pct";
    pct.textContent = part > 0 && part < 0.01 ? "<1%" : Math.round(part * 100) + "%";
    cell.append(pct);

    // An SVG rect and not a styled div: `default-src 'self'` admits no
    // inline style attribute, so a width written as CSS would be dropped
    // by the browser without a word. A geometry attribute is not CSS.
    const bar = doc.createElementNS(SVG_NS, "svg");
    bar.setAttribute("class", "share-bar");
    bar.setAttribute("viewBox", "0 0 100 6");
    bar.setAttribute("preserveAspectRatio", "none");
    bar.setAttribute("aria-hidden", "true");

    const track = doc.createElementNS(SVG_NS, "rect");
    track.setAttribute("class", "share-track");
    track.setAttribute("x", "0");
    track.setAttribute("y", "0");
    track.setAttribute("width", "100");
    track.setAttribute("height", "6");
    track.setAttribute("rx", "3");
    bar.append(track);

    const fill = doc.createElementNS(SVG_NS, "rect");
    fill.setAttribute("class", "share-fill");
    fill.setAttribute("x", "0");
    fill.setAttribute("y", "0");
    // A row that is one of ninety-five still draws something: a bar that
    // rounds to nothing reads as a missing value rather than a small one.
    fill.setAttribute("width", String(Math.max(part * 100, part > 0 ? 1.5 : 0)));
    fill.setAttribute("height", "6");
    fill.setAttribute("rx", "3");
    bar.append(fill);

    cell.append(bar);
    item.append(cell);
  }

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
  // The count track's own heading. It was always empty, which was right
  // while the only thing in that track was a count of something the
  // label already named — and wrong on the two listings that put a
  // *value* there: a view's renderer and, on the images list, nothing at
  // all. "graph" and "layered" sat right-aligned under a blank heading.
  const tally = doc.createElement("span");
  tally.className = "catalogue-count";
  tally.setAttribute("role", "columnheader");
  tally.textContent = spec.count ?? "";
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
      ? t("catalogue.sorted.ascending", { column: text })
      : active
        ? t("catalogue.sorted.descending", { column: text })
        : t("catalogue.sortBy", { column: text }));
  button.addEventListener("click", () => {
    if (typeof spec.onSort === "function") spec.onSort(next);
  });
  return button;
}
