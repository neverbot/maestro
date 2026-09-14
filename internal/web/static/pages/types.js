// The Catalogue destination: what this game declared.
//
// It is the same two catalogues the home's middle lane shows, from the
// same single call, and that is deliberate rather than duplication: the
// lane is a summary a designer glances at and this page is where they
// come to read it, with room for the counts and the flags. Both are
// built from GET /summary, so a game with four hundred thousand
// entities renders exactly as fast as one with four.
//
// An entity type is a link to its own catalogue of entities; a relation
// type is a link to its own page, which says what it may join, what the
// analysis engine makes of it and what its edges carry. There is still
// no page *of edges* — an edge is read beside the entity it touches —
// and that is a different thing from the type's own declaration, which
// until now could be read nowhere.

import {
  DESTINATION_CATALOGUE,
  countLabel,
  destinations,
  expired,
  fail,
  fill,
  gameURL,
  fillState,
  openGame,
  relationTypeURL,
  row,
  say,
  setBreadcrumb,
  setReadOnly,
  typeURL,
  whoWrites,
} from "./page.js";
import {
  DECLARES_TYPES,
  NO_RELATION_TYPES_HEADING,
  NO_RELATION_TYPES_SENTENCE,
  NO_TYPES_HEADING,
  NO_TYPES_SENTENCE,
  describeTotals,
} from "./home.js";
import { goToLogin } from "../app.js";

// The two negative states this screen can be in are the home lane's own,
// stated once in pages/home.js and re-exported here: this page and that
// lane show the same two catalogues from the same call, so they are the
// same state and not two states that happen to agree.
export {
  NO_RELATION_TYPES_HEADING,
  NO_RELATION_TYPES_SENTENCE,
  NO_TYPES_HEADING,
  NO_TYPES_SENTENCE,
} from "./home.js";

export async function typesPage(opened) {
  const doc = opened.document;
  const noteEl = doc.getElementById("catalogue-note");
  const errorEl = doc.getElementById("catalogue-error");

  if (opened.game === null) {
    say(noteEl, opened.failure ?? "You may not have access to this game, or it no longer exists.");
    return opened;
  }
  setBreadcrumb(doc, [
    { label: opened.game.name, href: gameURL(opened.slug) },
    { label: DESTINATION_CATALOGUE },
  ]);
  // The game first: a person with three games open read three tabs
  // called "Catalogue".
  doc.title = opened.game.name + " \u00b7 Catalogue \u00b7 Maestro";

  const answer = await opened.client.summary();
  if (!answer.ok) {
    if (expired(answer)) {
      goToLogin();
      return opened;
    }
    say(noteEl, "");
    fail(errorEl, null, answer.error.message);
    return opened;
  }

  const summary = answer.result;
  say(noteEl, describeTotals(summary.totals));
  setReadOnly(doc, summary.role, "declares the types");
  fillState(doc, "types-empty", {
    heading: NO_TYPES_HEADING,
    sentence: NO_TYPES_SENTENCE + " " + whoWrites(summary.role, DECLARES_TYPES),
  });
  fillState(doc, "relation-types-empty", {
    heading: NO_RELATION_TYPES_HEADING,
    sentence: NO_RELATION_TYPES_SENTENCE,
  });

  fill(
    doc.getElementById("types"),
    doc.getElementById("types-empty"),
    (Array.isArray(summary.entity_types) ? summary.entity_types : []).map((type) =>
      row(doc, {
        label: type.label_plural || type.label || type.key,
        key: type.key,
        count: countLabel(Number(type.entity_count ?? 0), "entity", "entities"),
        flag: Number(type.invalid_count ?? 0) > 0 ? `${Number(type.invalid_count)} invalid` : "",
        href: typeURL(opened.slug, type.key),
      }),
    ),
  );
  fill(
    doc.getElementById("relation-types"),
    doc.getElementById("relation-types-empty"),
    (Array.isArray(summary.relation_types) ? summary.relation_types : []).map((type) =>
      row(doc, {
        label: type.label || type.key,
        key: type.key,
        count: countLabel(Number(type.relation_count ?? 0), "relation", "relations"),
        flag: Number(type.invalid_count ?? 0) > 0 ? `${Number(type.invalid_count)} invalid` : "",
        // **It goes somewhere now.** These rows hovered like links and
        // led nowhere, which is the worse half of the two ways to fix
        // it: the page a relation type never had is the one that says
        // what it may join and what a walk makes of it.
        href: relationTypeURL(opened.slug, type.key),
      }),
    ),
  );
  return opened;
}

if (globalThis.document && globalThis.document.getElementById("catalogue-note")) {
  const opened = await openGame({ destination: DESTINATION_CATALOGUE });
  if (opened !== null) {
    const doc = opened.document;
    await typesPage(opened);
  }
}
