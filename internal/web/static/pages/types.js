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
// type is not, because there is no page of edges — an edge is read
// beside the entity it touches, on the entity page, where both of its
// ends are.

import {
  DESTINATION_CATALOGUE,
  countLabel,
  destinations,
  expired,
  fail,
  fill,
  gameURL,
  openGame,
  row,
  say,
  setBreadcrumb,
  typeURL,
  whoWrites,
} from "./page.js";
import { DECLARES_TYPES, describeTotals } from "./home.js";
import { goToLogin } from "../app.js";

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
  say(doc.getElementById("types-empty-action"), whoWrites(summary.role, DECLARES_TYPES));

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
