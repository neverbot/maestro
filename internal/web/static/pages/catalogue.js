// One entity type's catalogue: every entity of it, in the order the
// listing returns them, paged over the cursor that listing already
// issues.
//
// **It says out loud that it is a catalogue and not a view**, because it
// is deliberately close in appearance to the `table` renderer and is a
// different thing: a table is a saved query answered by the view engine,
// with a projection, parameters and a renderer; this is every row of one
// declared type, in whichever order its headings were last pressed,
// answering nothing. A designer who mistook one for the other would
// think a filter had been applied when none had.
//
// It pages over the **existing** cursor and does not invent one: the
// listing issues `next_cursor` whenever a page came back full, so the
// page that reports the end is the empty one after the last row, which
// is why the button stays until the server stops sending a cursor.

import {
  DESTINATION_CATALOGUE,
  STATE_REFUSED,
  countLabel,
  destinations,
  emptyOrRows,
  entityURL,
  expired,
  gameURL,
  negativeState,
  openGame,
  say,
  segmentsOf,
  setBreadcrumb,
  setReadOnly,
  typesURL,
} from "./page.js";
import { nextCursorOf } from "../rows.js";
import { headerRow, row } from "../rows.js";
import { goToLogin } from "../app.js";

// The line this page carries about itself. It is a constant so the
// harness asks for it by identity and never by matching prose, and so
// there is exactly one place it can be softened.
export const CATALOGUE_NOTE =
  "This is the catalogue of one declared type: every entity of it, in the order " +
  "you choose from the column headings. " +
  "It is not a view — a view is a saved query with a renderer, and lives under Views.";

// **The empty state here names no action, and that is the role rule
// rather than an omission.** The home's two empty states word their
// second half from the caller's role, which GET /summary carries; this
// page does not read the summary, and telling a viewer to declare an
// entity the server will refuse is worse than telling them nothing. A
// second call for one sentence is the wrong trade, so the sentence says
// what is true for everybody and stops.

// **The pages get bigger as a reader keeps going.** A thousand rows at
// fifty a press is eighteen presses, each after scrolling to a button
// that has moved further down the page — measured on the seeded
// thousand-creature type, and the reason this is not just a number.
//
// The first page stays small because most visits end there: a designer
// opening a type to check one row should not wait for five hundred. Past
// that, somebody pressing "show more" has said they are reading the
// whole thing, and the cheapest thing this product can do for them is
// stop asking. 50, then 200, then 500 — the server's own cap
// (metamodel.MaxEntityPage) — walks a thousand rows in four presses.
//
// It is not infinite scroll, and that is a decision rather than an
// omission: this product's readers "read more than they click"
// (docs/product.md), a list that loads under the scroll takes the page
// footer away from them, and a keyboard or a screen reader has nothing
// to activate.
export const PAGE_SIZES = [50, 200, 500];

export function nextPageSize(fetched) {
  if (fetched < PAGE_SIZES[0]) return PAGE_SIZES[0];
  if (fetched < PAGE_SIZES[0] + PAGE_SIZES[1]) return PAGE_SIZES[1];
  return PAGE_SIZES[2];
}

// How many matches a search asks for at a time. It is a page size now
// and not a cap: the search pages, so this is how much arrives per
// press of the same button the listing uses, and no sentence has to
// admit a wall any more.
export const SEARCH_LIMIT = 50;

// hiddenColumnsSentence says which declared fields the lane is not
// showing, and where they can be read.
//
// It is exported and pure so the harness can read the sentence itself:
// the three-column cap is deliberate, and what was wrong was the
// silence, so the assertion worth holding is about the words rather than
// about the element.
export function hiddenColumnsSentence(declared, shown, typeLabel) {
  const names = declared.slice(shown).map((field) => field.label || field.key).filter((name) => name !== "");
  if (names.length === 0) return "";
  // "a, b and c" rather than "a, b, c": this is a sentence a person
  // reads, not a list a machine parses.
  const last = names[names.length - 1];
  const list = names.length === 1 ? last : names.slice(0, -1).join(", ") + " and " + last;
  return (
    "Showing " + shown + " of " + countLabel(declared.length, "declared field", "declared fields") +
    ". " + list + (names.length === 1 ? " is" : " are") +
    " on each " + String(typeLabel || "").toLowerCase() + "'s own page."
  );
}

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
    // **A page that could not read its type has no catalogue on it**, so
    // it keeps neither the note describing one nor the controls that
    // narrow one. The search box stayed drawn and enabled over a refusal
    // and answered nothing when typed into, because its listener is
    // attached further down, after the fetch that just failed: a control
    // that silently does nothing is worse than one that is not there.
    say(noteEl, "");
    const tools = doc.querySelector(".tools");
    if (tools) tools.hidden = true;
    say(nameEl, "This type is not in this game");
    // The refusal in the one shape, with the way back that the address
    // bar cannot offer: the catalogue this key was supposed to be in.
    const refusal = negativeState(doc, {
      kind: STATE_REFUSED,
      heading: "Nothing here is called “" + typeKey + "”",
      sentence: type.error.message,
      action: { href: typesURL(opened.slug), label: "This game's catalogue" },
    });
    const host = doc.getElementById("entities-empty");
    if (host && host.parentNode) host.parentNode.replaceChild(refusal, host);
    else say(errorEl, type.error.message);
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
    // **The claim shrank when the entity page gained a rename**, and a
    // notice that outlives the limitation it describes is the next
    // "prose about code" defect. A name is the one thing a person can
    // change now, and it is changed one screen along.
    setReadOnly(doc, counts.result.role, "writes these entities; a name is changed on the entity's own page");
    const found = (counts.result.entity_types || []).find((entry) => entry.key === typeKey);
    if (found && Number.isFinite(found.entity_count)) total = found.entity_count;
  }

  // At most three of the type's declared fields become columns. Three,
  // because a lane of ten columns is a spreadsheet and this is a reading
  // surface; the first three declared are the ones the game's author put
  // first, which is a better order than any this page could invent.
  const declared = Array.isArray(type.result.field_schema) ? type.result.field_schema : [];
  const columns = declared.slice(0, 3);
  // **A hidden column is said out loud.** Three is the cap and the cap
  // is right; a screen that shows three of eight fields and says nothing
  // is a screen claiming the type has three. The sentence names what is
  // missing and where it can be read, because "5 more fields" on its own
  // tells a reader they are lost rather than where they are.
  const columnsEl = doc.getElementById("entities-columns");
  if (columnsEl) {
    const sentence = hiddenColumnsSentence(declared, columns.length, type.result.label || type.result.key);
    columnsEl.hidden = sentence === "";
    say(columnsEl, sentence);
  }

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
        cells: columns.map((field) => ({
          text: field.label || field.key,
          numeric: field.type === "number",
          // Every declared column is orderable: the server orders by the
          // stored jsonb value, so a number sorts as a number and a row
          // with no value for that field sorts last either way.
          order: "field:" + field.key,
        })),
        sort: { label: "name", key: "key" },
        sorted: order,
        onSort: (next) => {
          order = next;
          // Ordering the listing clears a search, the way the two
          // filters do and for the same reason: a search answers the
          // fifty best matches for a word and is not the listing, so an
          // order pressed over one would reorder something that is not
          // on screen.
          if (query !== "") {
            query = "";
            if (searchEl) searchEl.value = "";
          }
          void refilter();
        },
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
  // The two filters the listing itself can take. They are the listing's
  // and not the search's, which is what makes them pageable: search
  // answers the fifty best matches for a word and cannot be paged past
  // them, and these narrow the order the pager is already walking.
  let prefix = "";
  let onlyInvalid = false;
  // The order the column headers set. "name" is the server's default
  // and is spelled out here rather than left empty, so the header can
  // say which column the listing is in from the first paint.
  let order = "name";

  function filtering() {
    return prefix !== "" || onlyInvalid;
  }

  // What the reader asked for, in their own words, so the count above the
  // list says which set it is counting.
  function describeFilters() {
    const parts = [];
    if (prefix !== "") parts.push("starting with \u201c" + prefix + "\u201d");
    if (onlyInvalid) parts.push("no longer fitting this type");
    return parts.join(" and ");
  }

  // The bold line over an empty filtered listing. It is built per case
  // rather than from describeFilters, which reads as a clause after a
  // count ("0 entities that no longer fit this type") and as nonsense
  // after a word ("Nothing that no longer fit this type").
  function nothingFound() {
    if (prefix !== "" && onlyInvalid) {
      return "Nothing starting with \u201c" + prefix + "\u201d has stopped fitting";
    }
    if (prefix !== "") return "No name starts with \u201c" + prefix + "\u201d";
    return "Everything here still fits its type";
  }

  // Both filters restart the listing: a cursor belongs to the filter it
  // was issued for, and the server refuses one carried across — which is
  // the right answer and not one a reader should ever have to see.
  async function refilter() {
    if (missEl) missEl.hidden = true;
    listEl.replaceChildren();
    putHeader();
    rendered = 0;
    cursor = null;
    await page();
  }

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
      // **The count says what is on screen, and "so far" says there is
      // more.** It read "First 50 matches … narrow it" when fifty was
      // all a search could ever answer; the search pages now, so the
      // honest sentence is the listing's own — what has been fetched,
      // and whether the set goes on.
      say(
        scopeEl,
        countLabel(rendered, "match", "matches") +
          " for \u201c" + query + "\u201d" +
          (cursor === null ? "" : ", so far"),
      );
    } else if (filtering()) {
      // **A filtered listing is not the type.** "Showing 12 of 1000"
      // over a listing narrowed to names beginning "Th" would be a count
      // of the wrong set: the reader asked a question and the sentence
      // has to answer the one they asked. The denominator is gone
      // because the server does not count a filtered listing, and a
      // number nobody can check is worse than none.
      say(scopeEl, countLabel(rendered, "entity", "entities") + " " + describeFilters()
        + (cursor === null ? "" : ", so far"));
    } else if (total !== null && rendered < total) {
      say(scopeEl, "Showing " + rendered + " of " + total);
    } else {
      say(scopeEl, "");
    }
  }

  async function page() {
    if (moreEl) moreEl.disabled = true;
    const request = { typeKey, verbose: true, prefix, invalid: onlyInvalid, order, limit: nextPageSize(rendered) };
    if (cursor !== null) request.cursor = cursor;
    const answer = await opened.client.listEntities(request);
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
    // The cursor before the sentence: `sayScope` says "so far" when
    // there is more to fetch, and reading it a line later meant the
    // first page of a filtered listing claimed to be all of it.
    cursor = nextCursorOf(body);
    // **A filtered listing that found nothing is not an empty type.**
    // The listing's own empty state reads "Nothing of this type yet" —
    // which, under a filter, sits over a type holding a thousand rows
    // and says the opposite of the truth. A filter that matched nothing
    // is the same shape of fact as a search that did.
    if (filtering() && rendered === 0) {
      listEl.hidden = true;
      if (emptyEl) emptyEl.hidden = true;
      if (missEl) {
        missEl.hidden = false;
        say(missHeadEl, nothingFound());
        say(
          missBodyEl,
          total === null
            ? "This type has entities; none of them answers that."
            : countLabel(total, "entity", "entities") + " of this type, and none of them answers that.",
        );
      }
    } else {
      if (missEl && query === "") missEl.hidden = true;
      emptyOrRows(listEl, emptyEl, rendered);
    }
    // **The type's count, not the page's.** This said
    // countLabel(rendered, …), which is how many rows are on screen: the
    // catalogue said a type had 1000 entities and this screen, dedicated
    // to that type, said 50 — and the number grew as "Show more" was
    // pressed. Two screens described one type with two numbers and the
    // wrong one was on the screen about it. `total` comes from the game's
    // summary, which the catalogue already reads for exactly this.
    sayScope();
    if (moreEl) {
      moreEl.hidden = cursor === null || rendered === 0;
      // It says how many it will fetch. "Show more" makes a reader guess
      // whether pressing it costs them a second or a minute.
      moreEl.textContent = "Show " + nextPageSize(rendered) + " more";
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
    await searchPage();
  }

  // **A search pages, and that is what turned its cap from a wall into a
  // door.** It answered the fifty best matches and hid the pager, so a
  // query matching three hundred rows left two hundred and fifty of them
  // unreachable by any path on this screen. The server walks the whole
  // matching set now, in the ranking's own order, and this reads it the
  // way `page` reads the listing — same button, same cursor rule, same
  // sentence about what is on screen.
  async function searchPage() {
    if (moreEl) moreEl.disabled = true;
    const request = { limit: SEARCH_LIMIT, verbose: true, kind: "entity" };
    if (cursor !== null) request.cursor = cursor;
    const answer = await opened.client.searchEntities(query, typeKey, request);
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
    rendered += entities.length;
    // The cursor before the sentence, as the listing does it: `sayScope`
    // says "so far" while there is more to fetch, and reading it a line
    // later made the first page claim to be the whole answer.
    cursor = nextCursorOf(answer.result);
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
    if (moreEl) {
      moreEl.hidden = cursor === null;
      moreEl.disabled = false;
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

  const prefixEl = doc.getElementById("entities-prefix");
  if (prefixEl) {
    let timer = null;
    prefixEl.addEventListener("input", () => {
      const next = prefixEl.value.trim();
      if (timer !== null) clearTimeout(timer);
      // The same pause the search box takes, for the same reason: a
      // keystroke is not a question.
      timer = setTimeout(async () => {
        if (next === prefix) return;
        prefix = next;
        // A prefix and a search are two answers to one question, and the
        // search is the one that cannot be paged: narrowing the listing
        // clears it rather than leaving a reader with a filter that does
        // nothing to what is on screen.
        if (query !== "") {
          query = "";
          if (searchEl) searchEl.value = "";
        }
        await refilter();
      }, 200);
    });
  }

  const invalidEl = doc.getElementById("entities-invalid");
  if (invalidEl) {
    invalidEl.addEventListener("change", async () => {
      onlyInvalid = invalidEl.checked === true;
      if (query !== "") {
        query = "";
        if (searchEl) searchEl.value = "";
      }
      await refilter();
    });
  }

  // One button, two sources. Which one it continues is which one is on
  // screen: a search and the listing are never both showing, and the
  // cursor belongs to whichever answered last.
  if (moreEl) moreEl.addEventListener("click", () => (query === "" ? page() : searchPage()));
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
