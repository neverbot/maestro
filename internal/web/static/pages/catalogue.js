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
  DESTINATION_CATALOGUE,
  countLabel,
  destinations,
  emptyOrRows,
  entityURL,
  expired,
  gameURL,
  openGame,
  say,
  segmentsOf,
  setBreadcrumb,
  setReadOnly,
  typesURL,
} from "./page.js";
import { headerRow, row } from "../rows.js";
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

// How many a search asks for. It is named because two places read it:
// the call, and the sentence that admits the cap.
export const SEARCH_LIMIT = 50;

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
  // The type's own label is not known until its fetch lands, so the trail
  // goes up now with the slug in the last crumb and is rewritten below
  // once the type has a name. A crumb that waits is a page with no way
  // back while it loads.
  setBreadcrumb(doc, [
    { label: opened.game.name, href: gameURL(opened.slug) },
    { label: DESTINATION_CATALOGUE, href: typesURL(opened.slug) },
  ]);

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
  const typeName = type.result.label_plural || type.result.label || type.result.key;
  doc.title = typeName + " \u00b7 Maestro";
  say(nameEl, typeName);
  // The last crumb, now that the type has a name. Until this line it
  // read the key, which is what the address says and what a reader who
  // arrived by link already knows.
  setBreadcrumb(doc, [
    { label: opened.game.name, href: gameURL(opened.slug) },
    { label: DESTINATION_CATALOGUE, href: typesURL(opened.slug) },
    { label: typeName },
  ]);
  say(metaEl, type.result.key);

  // How many entities this type *has*, which is a different number from
  // how many are on screen. The summary is one call and the catalogue
  // page already makes it for the same fact.
  let total = null;
  const counts = await opened.client.summary();
  if (counts.ok) {
    // The same call carries the caller's role, which is what decides
    // whether this screen says "an agent writes these" or "this instance
    // will refuse a write from you".
    setReadOnly(doc, counts.result.role, "writes these entities");
    const found = (counts.result.entity_types || []).find((entry) => entry.key === typeKey);
    if (found && Number.isFinite(found.entity_count)) total = found.entity_count;
  }

  // At most three of the type's declared fields become columns. Three,
  // because a lane of ten columns is a spreadsheet and this is a reading
  // surface; the first three declared are the ones the game's author put
  // first, which is a better order than any this page could invent.
  const columns = (Array.isArray(type.result.field_schema) ? type.result.field_schema : []).slice(0, 3);

  // A value the entity does not carry is named, never blank: a blank cell
  // cannot be told from a value that failed to load.
  function cellsFor(entity) {
    const values = entity.fields && typeof entity.fields === "object" ? entity.fields : {};
    return columns.map((field) => {
      const value = values[field.key];
      const name = field.label || field.key;
      // **Three arms, not two.** design.md's Named Absence Rule is that
      // absent and empty are different facts that never look alike, and
      // this folded the empty string into the absent arm: an entity with
      // `faction: ""` rendered pixel-identically to one with no faction
      // at all. The rule is stated in this repository and the module next
      // door already honours it.
      if (value === undefined || value === null) {
        // The field's *label* and not its key: the key is the model's
        // spelling, and a field keyed `min_level` with a label "Minimum
        // level" was reading "no min_level" on the screen whose whole job
        // is the game's vocabulary.
        return { text: "no " + name, absent: true };
      }
      if (value === "") return { text: "empty", absent: true };
      return { text: String(value), numeric: field.type === "number" };
    });
  }

  // The header, rebuilt with every listing so a search's results carry the
  // same columns the full listing does.
  function putHeader() {
    if (columns.length === 0) return;
    listEl.append(
      headerRow(doc, {
        label: type.result.label || type.result.key,
        key: "key",
        cells: columns.map((field) => ({ text: field.label || field.key, numeric: field.type === "number" })),
      }),
    );
  }

  const searchEl = doc.getElementById("entities-search");
  const missEl = doc.getElementById("entities-miss");
  const missHeadEl = doc.getElementById("entities-miss-head");
  const missBodyEl = doc.getElementById("entities-miss-body");
  const scopeEl = doc.getElementById("entities-scope");

  let cursor = null;
  let rendered = 0;
  let query = "";

  // The line under the title: the key, what the type holds, and — when a
  // search has narrowed it — what is being shown instead.
  function sayScope() {
    const parts = [type.result.key];
    if (total !== null) parts.push(countLabel(total, "entity", "entities"));
    say(metaEl, parts.join(" \u00b7 "));
    if (!scopeEl) return;
    if (query !== "") {
      // **"First 50" and not "50 matches".** The search asks for fifty and
      // reports what came back, so a query matching a thousand rows said
      // "50 matches" — the same defect this pass exists to fix, a count
      // naming the page rather than the thing, re-introduced in the new
      // code path. The honest sentence is the one that admits the cap.
      const capped = rendered >= SEARCH_LIMIT;
      say(
        scopeEl,
        (capped ? "First " + rendered + " matches" : countLabel(rendered, "match", "matches")) +
          " for \u201c" + query + "\u201d",
      );
    } else if (total !== null && rendered < total) {
      say(scopeEl, "Showing " + rendered + " of " + total);
    } else {
      say(scopeEl, "");
    }
  }

  async function page() {
    if (moreEl) moreEl.disabled = true;
    const answer = await opened.client.listEntities(
      cursor === null ? { typeKey, verbose: true } : { typeKey, cursor, verbose: true },
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
          cells: cellsFor(entity),
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
    // **The type's count, not the page's.** This said
    // countLabel(rendered, …), which is how many rows are on screen: the
    // catalogue said a type had 1000 entities and this screen, dedicated
    // to that type, said 50 — and the number grew as "Show more" was
    // pressed. Two screens described one type with two numbers and the
    // wrong one was on the screen about it. `total` comes from the game's
    // summary, which the catalogue already reads for exactly this.
    sayScope();
    cursor = typeof body.next_cursor === "string" ? body.next_cursor : null;
    if (moreEl) {
      moreEl.hidden = cursor === null || rendered === 0;
      // It says how many it will fetch. "Show more" makes a reader guess
      // whether pressing it costs them a second or a minute.
      moreEl.textContent = "Show 50 more";
      moreEl.disabled = false;
    }
  }

  // A search replaces the listing rather than filtering it in the page:
  // the server holds a thousand rows and the browser holds fifty, so a
  // filter over what is on screen would answer from the wrong set.
  async function runSearch() {
    listEl.replaceChildren();
    putHeader();
    rendered = 0;
    cursor = null;
    if (moreEl) moreEl.hidden = true;
    const answer = await opened.client.searchEntities(query, typeKey, { limit: SEARCH_LIMIT, verbose: true });
    if (!answer.ok) {
      if (expired(answer)) {
        goToLogin();
        return;
      }
      if (emptyEl) emptyEl.hidden = true;
      say(errorEl, answer.error.message);
      return;
    }
    say(errorEl, "");
    // **A search hit is not an entity.** The route answers
    // `{kind, rank, name_match, entity}` — one envelope per hit with the
    // row nested inside it — and reading `hit.name` off the envelope
    // rendered a list of empty rows that still counted correctly, which
    // is the worst shape a bug can have. Found by searching for a key in
    // a browser.
    const hits = Array.isArray(answer.result.items) ? answer.result.items : [];
    const entities = hits.map((hit) => hit.entity).filter((entity) => entity && entity.key);
    for (const entity of entities) {
      listEl.append(
        row(doc, {
          label: entity.name || entity.key,
          key: entity.key,
          // The found row is the one a reader most wants to compare, and
          // it was the only row in the product rendered under headers for
          // columns it did not draw.
          cells: cellsFor(entity),
          count: "",
          flag: entity.invalid === true ? "invalid" : "",
          href: entityURL(opened.slug, entity.type_key || typeKey, entity.key),
        }),
      );
    }
    rendered = entities.length;
    // **A miss is not an empty type.** emptyOrRows shows the listing's
    // own empty state, which reads "Nothing of this type yet" — three
    // lines under a heading that says the type has 105 entities. A search
    // that found nothing is a third negative state and it says so.
    if (missEl) {
      const missed = query !== "" && rendered === 0;
      missEl.hidden = !missed;
      if (missed) {
        say(missHeadEl, "No match for \u201c" + query + "\u201d");
        say(
          missBodyEl,
          total === null
            ? "Nothing of this type matches that name or key."
            : countLabel(total, "entity", "entities") + " of this type, and none of them matches that name or key.",
        );
      }
    }
    if (query !== "") {
      listEl.hidden = rendered === 0;
      if (emptyEl) emptyEl.hidden = true;
    } else {
      emptyOrRows(listEl, emptyEl, rendered);
    }
    sayScope();
  }

  if (searchEl) {
    let timer = null;
    searchEl.addEventListener("input", () => {
      const next = searchEl.value.trim();
      if (timer !== null) clearTimeout(timer);
      // A keystroke is not a question. The pause is what turns typing
      // into one query instead of one per character.
      timer = setTimeout(async () => {
        if (next === query) return;
        query = next;
        if (query === "") {
          if (missEl) missEl.hidden = true;
          listEl.replaceChildren();
          putHeader();
          rendered = 0;
          cursor = null;
          await page();
          return;
        }
        await runSearch();
      }, 200);
    });
  }

  if (moreEl) moreEl.addEventListener("click", () => page());
  putHeader();
  await page();
  return opened;
}

if (globalThis.document && globalThis.document.getElementById("type-note")) {
  const opened = await openGame({ destination: DESTINATION_CATALOGUE });
  if (opened !== null) {
    const doc = opened.document;
    await cataloguePage(opened);
  }
}
