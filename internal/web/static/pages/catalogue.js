// One entity type's catalogue: every entity of it, in the order the
// listing returns them, paged over the cursor that listing already
// issues.

import {
  DESTINATION_CATALOGUE,
  STATE_REFUSED,
  countLabel,
  destinations,
  emptyOrRows,
  entityURL,
  expired,
  fillState,
  gameURL,
  negativeState,
  openGame,
  say,
  segmentsOf,
  setBreadcrumb,
  setReadOnly,
  typesURL,
} from "./page.js";
import { ABSENT_MARK, nextCursorOf } from "../rows.js";
import { t } from "../i18n.js";
import { headerRow, row } from "../rows.js";
import { formatValue } from "./entity.js";
import { goToLogin } from "../app.js";


// **The empty state here names no action, and that is the role rule
// rather than an omission.** The home's two empty states word their
// second half from the caller's role, which GET /summary carries; this
// page does not read the summary, and telling a viewer to declare an
// entity the server will refuse is worse than telling them nothing. A
// second call for one sentence is the wrong trade, so the sentence says
// what is true for everybody and stops.
export const NOTHING_OF_THIS_TYPE_HEADING = t("catalogue.none.heading");
export const NOTHING_OF_THIS_TYPE_SENTENCE = t("catalogue.none.sentence");

// **The pages get bigger as a reader keeps going.** A thousand rows at
// fifty a press is eighteen presses, each after scrolling to a button
// that has moved further down the page — measured on the seeded
// thousand-creature type, and the reason this is not just a number.
export const PAGE_SIZES = [50, 200, 500];

export function nextPageSize(fetched) {
  if (fetched < PAGE_SIZES[0]) return PAGE_SIZES[0];
  if (fetched < PAGE_SIZES[0] + PAGE_SIZES[1]) return PAGE_SIZES[1];
  return PAGE_SIZES[2];
}

export async function cataloguePage(opened) {
  const doc = opened.document;
  const nameEl = doc.getElementById("type-name");
  const metaEl = doc.getElementById("type-meta");
  fillState(doc, "entities-empty", {
    heading: NOTHING_OF_THIS_TYPE_HEADING,
    sentence: NOTHING_OF_THIS_TYPE_SENTENCE,
  });
  const listEl = doc.getElementById("entities");
  const emptyEl = doc.getElementById("entities-empty");
  const errorEl = doc.getElementById("entities-error");
  const moreEl = doc.getElementById("entities-more");

  if (opened.game === null) {
    say(nameEl, t("error.gameNotFound"));
    say(metaEl, opened.failure ?? t("error.noAccessToGame"));
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
    say(nameEl, t("catalogue.noneAsked"));
    return opened;
  }

  const type = await opened.client.getType(typeKey);
  if (!type.ok) {
    if (expired(type)) {
      goToLogin();
      return opened;
    }
    // **A page that could not read its entity has no catalogue on it**,
    // so it keeps none of the controls that narrow one. The search box
    // stayed drawn and enabled over a refusal and answered nothing when
    // typed into, because its listener is attached further down, after
    // the fetch that just failed: a control that silently does nothing
    // is worse than one that is not there.
    const tools = doc.querySelector(".tools");
    if (tools) tools.hidden = true;
    say(nameEl, t("catalogue.notInGame"));
    // The refusal in the one shape, with the way back that the address
    // bar cannot offer: the catalogue this key was supposed to be in.
    const refusal = negativeState(doc, {
      kind: STATE_REFUSED,
      heading: t("catalogue.noSuchType", { key: typeKey }),
      sentence: type.error.message,
      action: { href: typesURL(opened.slug), label: t("catalogue.thisGames") },
    });
    const host = doc.getElementById("entities-empty");
    if (host) {
      host.replaceChildren(refusal);
      host.hidden = false;
    } else say(errorEl, type.error.message);
    return opened;
  }
  const typeName = type.result.label_plural || type.result.label || type.result.key;
  // The game's own words for one of these and for many, which is what
  // the line under the title counts in.
  const singular = type.result.label || type.result.label_plural || type.result.key;
  const plural = typeName;
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
    setReadOnly(doc, counts.result.role, "writes.entities");
    const found = (counts.result.entity_types || []).find((entry) => entry.key === typeKey);
    if (found && Number.isFinite(found.entity_count)) total = found.entity_count;
    // **A filter whose answer is always "none" teaches a reader to
    // ignore the toolbar.** The misfit filter is the one control on this
    // screen that answers a question most games never have, so it is
    // drawn on the games that have one: the same number the catalogue
    // already read decides it.
    const misfits = found && Number.isFinite(found.invalid_count) ? found.invalid_count : 0;
    const invalidLabel = doc.getElementById("entities-invalid-label");
    if (invalidLabel) invalidLabel.hidden = misfits === 0;
  }

  // At most three of the type's declared fields become columns. Three,
  // because a lane of ten columns is a spreadsheet and this is a reading
  // surface; the first three declared are the ones the game's author put
  // first, which is a better order than any this page could invent.
  const declared = Array.isArray(type.result.field_schema) ? type.result.field_schema : [];
  const columns = declared.slice(0, 3);

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
        // **A dash, under a heading that already names the field.** It
        // read "no Offerings it accepts" on every row that had none: the
        // column heading said again twenty times, in the one place a
        // reader is scanning for the value. The words stay as the cell's
        // accessible name, and they use the field's *label* and not its
        // key — a field keyed `min_level` labelled "Minimum level" read
        // "no min_level" on the screen whose whole job is the game's
        // vocabulary.
        return { text: ABSENT_MARK, absent: true, name: t("value.absent", { field: name }) };
      }
      // Empty and absent are different facts and never look alike:
      // design.md's Named Absence Rule, and the reason this has three
      // arms rather than two. An entity with `faction: ""` said nothing
      // and one with no faction at all said nothing, identically.
      if (value === "") return { text: t("value.empty"), absent: true, name: t("value.empty.name", { field: name }) };
      // **The same words the entity page uses**, which is the whole
      // reason this calls a shared function rather than `String(value)`.
      // A quest with `repeatable: false` read "false" in this table and
      // "no" on its own page, and a list of tags read "elwynn,quest"
      // here and "elwynn, quest" there: two spellings of one value, on
      // two screens a designer moves between by clicking a row.
      return { text: formatValue(field.type, value), numeric: field.type === "number" };
    });
  }

  // The header, rebuilt with every listing so a search's results carry the
  // same columns the full listing does.
  function putHeader() {
    if (columns.length === 0) return;
    listEl.append(
      headerRow(doc, {
        label: type.result.label || type.result.key,
        key: t("column.key"),
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
          // The filter survives an order: both are the listing's now, so
          // reordering the rows that hold "portal" is a question somebody
          // can ask. It used to clear the search box, because a search
          // was a different answer that an order could not reach.
          void refilter();
        },
      }),
    );
  }

  const searchEl = doc.getElementById("entities-search");
  const missEl = doc.getElementById("entities-miss");

  // The third negative state, built where the other two are rather than
  // being two empty slots in the shell waiting to be written into: a
  // hole with an id cannot drift from the shape, and a `<b>` and a
  // `<span>` sitting in the markup can.
  function sayMiss(heading, sentence) {
    fillState(doc, "entities-miss", { heading, sentence });
    if (missEl) missEl.hidden = false;
  }
  const scopeEl = doc.getElementById("entities-scope");

  let cursor = null;
  let rendered = 0;
  // **One text filter, and it belongs to the listing.** This page kept
  // two — a full-text query and a prefix — because the search matches
  // whole words and answers nothing while somebody is still typing. The
  // listing's own filter matches anywhere in a name or a key, so typing
  // narrows from the first letter and every row it finds is reachable by
  // the pager underneath.
  let filter = "";
  let onlyInvalid = false;
  // The order the column headers set. "name" is the server's default
  // and is spelled out here rather than left empty, so the header can
  // say which column the listing is in from the first paint.
  let order = "name";

  function filtering() {
    return filter !== "" || onlyInvalid;
  }

  // What the reader asked for, in their own words, so the count above the
  // list says which set it is counting.
  function describeFilters() {
    const parts = [];
    if (filter !== "") parts.push(t("catalogue.filter.matching", { text: filter }));
    if (onlyInvalid) parts.push(t("catalogue.filter.notFitting"));
    return parts.join(t("list.and"));
  }

  // The bold line over an empty filtered listing. It is built per case
  // rather than from describeFilters, which reads as a clause after a
  // count ("0 entities that no longer fit this type") and as nonsense
  // after a word ("Nothing that no longer fit this type").
  function nothingFound() {
    if (filter !== "" && onlyInvalid) {
      return t("catalogue.none.matchingAndInvalid", { text: filter });
    }
    if (filter !== "") return t("catalogue.none.matching", { text: filter });
    return t("catalogue.none.allFit");
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
    // **How many of the game's own thing there are**, which is the one
    // fact this line carries: "27 Deidades" and not "deity · 27
    // entities", because the key is the handle an agent uses and the
    // noun is the game's, not the metamodel's.
    say(metaEl, total === null ? "" : countLabel(total, singular, plural));
    if (!scopeEl) return;
    // A filter that found nothing is said once, by the state under the
    // list: "0 entities holding zzz" over "Nothing here holds zzz" is
    // the same sentence twice, and the count is the less useful half.
    if (filtering() && rendered === 0) {
      say(scopeEl, "");
    } else if (filtering()) {
      // **A filtered listing is not the entity's own count.** "Showing 12
      // of 1000" over a listing narrowed to the rows holding "portal"
      // would be a count of the wrong set: the reader asked a question
      // and the sentence has to answer the one they asked. There is no
      // denominator because the server does not count a filtered
      // listing, and a number nobody can check is worse than none; "so
      // far" is what says the set goes on.
      say(scopeEl, countLabel(rendered, t("unit.entity"), t("unit.entities")) + " " + describeFilters()
        + (cursor === null ? "" : t("list.soFar")));
    } else if (total !== null && rendered < total) {
      say(scopeEl, t("catalogue.showingOf", { shown: rendered, all: total }));
    } else {
      say(scopeEl, "");
    }
  }

  async function page() {
    if (moreEl) moreEl.disabled = true;
    const request = { typeKey, verbose: true, contains: filter, invalid: onlyInvalid, order, limit: nextPageSize(rendered) };
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
      sayMiss(
        nothingFound(),
        total === null
          ? t("catalogue.miss.filter.unknownTotal")
          : t("catalogue.miss.filter", { all: countLabel(total, t("unit.entity"), t("unit.entities")) }),
      );
    } else {
      if (missEl) missEl.hidden = true;
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
      moreEl.textContent = t("catalogue.showMore", { count: nextPageSize(rendered) });
      moreEl.disabled = false;
    }
  }

  if (searchEl) {
    let timer = null;
    searchEl.addEventListener("input", () => {
      const next = searchEl.value.trim();
      if (timer !== null) clearTimeout(timer);
      // A keystroke is not a question. The pause is what turns typing
      // into one request instead of one per character.
      timer = setTimeout(async () => {
        if (next === filter) return;
        filter = next;
        await refilter();
      }, 200);
    });
  }

  const invalidEl = doc.getElementById("entities-invalid");
  if (invalidEl) {
    invalidEl.addEventListener("change", async () => {
      onlyInvalid = invalidEl.checked === true;
      await refilter();
    });
  }

  if (moreEl) moreEl.addEventListener("click", () => page());
  putHeader();
  await page();
  return opened;
}

if (globalThis.document && globalThis.document.getElementById("entities")) {
  const opened = await openGame({ destination: DESTINATION_CATALOGUE });
  if (opened !== null) {
    const doc = opened.document;
    await cataloguePage(opened);
  }
}
