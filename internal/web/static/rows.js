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

export function row(doc, spec) {
  const item = doc.createElement("li");

  const label = doc.createElement(spec.href ? "a" : "span");
  label.className = "catalogue-label";
  if (spec.href) label.href = spec.href;
  label.textContent = spec.label ?? "";
  item.append(label);

  const handle = doc.createElement("code");
  handle.className = "catalogue-key";
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
    value.textContent = cell ? (cell.text ?? "") : "";
    item.append(value);
  }

  const tally = doc.createElement("span");
  tally.className = "catalogue-count";
  tally.textContent = spec.count ?? "";
  item.append(tally);

  if (spec.flag) {
    const flag = doc.createElement("span");
    flag.className = "catalogue-invalid";
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
// the other's widths, and it carries no link: a heading is not a target.
export function headerRow(doc, spec) {
  const item = doc.createElement("li");
  item.className = "catalogue-head";

  const label = doc.createElement("span");
  label.textContent = spec.label ?? "";
  item.append(label);

  const key = doc.createElement("span");
  key.className = "catalogue-key";
  key.textContent = spec.key ?? "";
  item.append(key);

  const heads = Array.isArray(spec.cells) ? spec.cells : [];
  for (let i = 0; i < CATALOGUE_CELLS; i += 1) {
    const head = doc.createElement("span");
    head.className = "catalogue-cell";
    if (heads[i] && heads[i].numeric) head.classList.add("numeric");
    head.textContent = heads[i] ? heads[i].text ?? heads[i] : "";
    item.append(head);
  }
  // The count track, empty. It exists so the header spans the same six
  // tracks a row does; without it the header is one track short and
  // everything after the name drifts.
  const tally = doc.createElement("span");
  tally.className = "catalogue-count";
  item.append(tally);
  return item;
}
