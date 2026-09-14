// The negative states live here, one module up from the pages, because
// index.html's own module (app.js) needs them too and app.js is what
// pages/page.js imports: putting them in page.js would make that cycle
// and hang the one screen a brand-new account sees.

// A screen with nothing on it is in one of exactly three states, and the
// 2026-09-09 audit found four different shapes for the first of them on a
// single screen: a filled box with a coloured left stripe, two bare grey
// sentences, and a nine-line paragraph explaining the MCP API in a 200px
// lane. Three of the four said the same thing in a different voice.
//
// One shape, three flavours, stated once:
//
//   - **empty**    nothing is here, and here is who would put it there
//   - **loading**  the answer has not come back yet
//   - **refused**  it came back and it was a refusal
//
// The heading is the fact, in ink. The sentence beneath it is the
// explanation, muted, and there is at most one link. It never teaches an
// API: the person reading it does not have one.
export const STATE_EMPTY = "empty";
export const STATE_LOADING = "loading";
export const STATE_REFUSED = "refused";

export function negativeState(doc, spec) {
  const root = doc.createElement("div");
  root.className = spec.kind === STATE_REFUSED ? "state refused" : "state";

  const heading = doc.createElement("b");
  heading.textContent = spec.heading ?? "";
  root.append(heading);

  if (spec.sentence) {
    const sentence = doc.createElement("span");
    sentence.textContent = spec.sentence;
    root.append(sentence);
  }

  // At most one, and only where there is somewhere useful to go. A state
  // with two actions is a state that has become a form.
  if (spec.action && spec.action.href) {
    const link = doc.createElement("a");
    link.href = spec.action.href;
    link.textContent = spec.action.label ?? "";
    root.append(link);
  }

  return root;
}

// fillState puts a negative state into the hole a shell leaves for it.
//
// **The words live with the page, not in the markup.** Every shell used
// to carry its own `<div class="state"><b>…</b><span>…</span></div>`,
// which is the shared *class* and not the shared component: eight copies
// of one shape, each free to drift, and one of them — the entity page's
// — never rendered at all, because that page replaces its content
// wholesale and nobody noticed the markup was dead. A hole with an id
// and a call here cannot have that problem: if the call is missing the
// state is empty on screen, which is visible, rather than being present
// in the HTML and wrong.
//
// The hole keeps the id and the `hidden` flag, because that is what
// `fill` and `emptyOrRows` toggle; what it loses is the `state` class,
// which now arrives with the element this builds.
export function fillState(doc, id, spec) {
  const host = doc.getElementById(id);
  if (!host) return null;
  host.replaceChildren(negativeState(doc, { kind: STATE_EMPTY, ...spec }));
  return host;
}
