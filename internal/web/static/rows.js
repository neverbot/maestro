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

