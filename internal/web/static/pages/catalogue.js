// One entity type's catalogue: every entity of it, in the order the
// listing returns them, paged over the cursor that listing already
// issues.
//
// **It says out loud that it is a catalogue and not a view**, because it
// is deliberately close in appearance to the `table` renderer and is a
// different thing: a table is a saved query answered by the view engine,
// with a projection, parameters and a renderer; this is every row of one
// declared type, in key order, answering nothing. A designer who mistook
// one for the other would think a filter had been applied when none had.
//
// It pages over the **existing** cursor and does not invent one: the
// listing issues `next_cursor` whenever a page came back full, so the
// page that reports the end is the empty one after the last row, which
// is why the button stays until the server stops sending a cursor.

import {
  countLabel,
  destinations,
  DESTINATION_CATALOGUE,
  emptyOrRows,
  entityURL,
  expired,
  openGame,
  row,
  say,
  segmentsOf,
  typesURL,
} from "./page.js";
import { goToLogin } from "../app.js";

// The line this page carries about itself. It is a constant so the
// harness asks for it by identity and never by matching prose, and so
// there is exactly one place it can be softened.
export const CATALOGUE_NOTE =
  "This is the catalogue of one declared type: every entity of it, in key order. " +
  "It is not a view — a view is a saved query with a renderer, and lives under Views.";

// **The empty state here names no action, and that is the role rule
// rather than an omission.** The home's two empty states word their
// second half from the caller's role, which GET /summary carries; this
// page does not read the summary, and telling a viewer to declare an
// entity the server will refuse is worse than telling them nothing. A
// second call for one sentence is the wrong trade, so the sentence says
// what is true for everybody and stops.

export async function cataloguePage(opened) {
  const doc = opened.document;
  const nameEl = doc.getElementById("type-name");
  const metaEl = doc.getElementById("type-meta");
  const noteEl = doc.getElementById("type-note");
  const listEl = doc.getElementById("entities");
  const emptyEl = doc.getElementById("entities-empty");
  const errorEl = doc.getElementById("entities-error");
  const moreEl = doc.getElementById("entities-more");

  if (opened.game === null) {
    say(nameEl, "Game not found");
    say(metaEl, opened.failure ?? "You may not have access to this game, or it no longer exists.");
    return opened;
  }
  const back = doc.getElementById("back-to-types");
  if (back) back.href = typesURL(opened.slug);

  // /g/{slug}/t/{typeKey}: the segment after the "t". Read through
  // segmentsOf, so this page cannot disagree with the entity page about
  // how many segments precede a key.
  const typeKey = segmentsOf(opened.location.pathname)[1] || "";
  if (!typeKey) {
    say(nameEl, "No type asked for");
    return opened;
  }

  say(noteEl, CATALOGUE_NOTE);

  const type = await opened.client.getType(typeKey);
  if (!type.ok) {
    if (expired(type)) {
      goToLogin();
      return opened;
    }
    say(nameEl, "Could not read this type");
    say(errorEl, type.error.message);
    return opened;
  }
  say(nameEl, type.result.label_plural || type.result.label || type.result.key);
  say(metaEl, type.result.key);

  let cursor = null;
  let rendered = 0;

  async function page() {
    if (moreEl) moreEl.disabled = true;
    const answer = await opened.client.listEntities(
      cursor === null ? { typeKey } : { typeKey, cursor },
    );
    if (!answer.ok) {
      if (expired(answer)) {
        goToLogin();
        return;
      }
      if (emptyEl) emptyEl.hidden = true;
      if (moreEl) moreEl.hidden = true;
      say(errorEl, answer.error.message);
      return;
    }
    say(errorEl, "");
    const body = answer.result;
    const items = Array.isArray(body.items) ? body.items : [];
    for (const entity of items) {
      listEl.append(
        row(doc, {
          label: entity.name || entity.key,
          key: entity.key,
          count: "",
          // Kept and marked, never deleted: a row a schema edit stopped
          // fitting is work a designer has to do, and hiding it would
          // hide the work.
          flag: entity.invalid === true ? "invalid" : "",
          href: entityURL(opened.slug, typeKey, entity.key),
        }),
      );
    }
    rendered += items.length;
    emptyOrRows(listEl, emptyEl, rendered);
    say(metaEl, type.result.key + " · " + countLabel(rendered, "entity", "entities"));
    cursor = typeof body.next_cursor === "string" ? body.next_cursor : null;
    if (moreEl) {
      moreEl.hidden = cursor === null;
      moreEl.disabled = false;
    }
  }

  if (moreEl) moreEl.addEventListener("click", () => page());
  await page();
  return opened;
}

if (globalThis.document && globalThis.document.getElementById("type-note")) {
  const opened = await openGame();
  if (opened !== null) {
    const doc = opened.document;
    if (opened.game !== null) doc.body.prepend(destinations(doc, opened.slug, DESTINATION_CATALOGUE));
    await cataloguePage(opened);
  }
}
