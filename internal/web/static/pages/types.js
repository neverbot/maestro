// The Catalogue destination: what this game declared.

import {
  DESTINATION_CATALOGUE,
  coalesce,
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
import { byWeight, headerRow, withHeader } from "../rows.js";
import { t } from "../i18n.js";
import { goToLogin } from "../app.js";
import { REREAD, TARGET_CONTENT, TARGET_EVERYTHING } from "../client.js";

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
    say(noteEl, opened.failure ?? t("error.noAccessToGame"));
    return opened;
  }
  setBreadcrumb(doc, [
    { label: opened.game.name, href: gameURL(opened.slug) },
    { label: DESTINATION_CATALOGUE },
  ]);
  // The game first: a person with three games open read three tabs
  // called "Catalogue".
  doc.title = opened.game.name + " \u00b7 " + t("nav.content") + " \u00b7 Maestro";

  // One read, in a function, so an event can run it again. This page
  // fetched once on load and listened to nothing: a game being written
  // by an agent showed the catalogue it had when the tab opened.
  async function load() {
    const answer = await opened.client.summary();
    if (!answer.ok) {
      if (expired(answer)) {
        goToLogin();
        return;
      }
      say(noteEl, "");
      fail(errorEl, null, answer.error.message);
      return;
    }

    const summary = answer.result;
    say(noteEl, describeTotals(summary.totals));
    setReadOnly(doc, summary.role, "writes.declaresTypes");
    fillState(doc, "types-empty", {
      heading: NO_TYPES_HEADING,
      sentence: NO_TYPES_SENTENCE + " " + whoWrites(summary.role, DECLARES_TYPES),
    });
    fillState(doc, "relation-types-empty", {
      heading: NO_RELATION_TYPES_HEADING,
      sentence: NO_RELATION_TYPES_SENTENCE,
    });

    // Ordered by how much of the game each one is, and without the key:
    // the same two decisions the home's bands carry, because two
    // spellings of one catalogue is one of them going stale.
    const things = byWeight(
      Array.isArray(summary.entity_types) ? summary.entity_types : [],
      (t) => Number(t.entity_count ?? 0),
    );
    // **The same columns as the home, named the same way.** This screen
    // and the game's first page draw the same two catalogues from the
    // same call, and only one of them said what its numbers were: 27 and
    // 43% under nothing at all. The rule was carried one step along.
    fill(
      doc.getElementById("types"),
      doc.getElementById("types-empty"),
      withHeader(things.sorted.length > 0 && headerRow(doc, {
        label: t("home.column.kind"),
        count: t("home.column.count"),
        share: t("home.column.ofTotal"),
      }), things.sorted.map((type) =>
        row(doc, {
          label: type.label_plural || type.label || type.key,
          count: String(Number(type.entity_count ?? 0)),
          share: { value: Number(type.entity_count ?? 0), of: things.total },
          flag: Number(type.invalid_count ?? 0) > 0 ? `${Number(type.invalid_count)} invalid` : "",
          href: typeURL(opened.slug, type.key),
        }),
      )),
    );
    const links = byWeight(
      Array.isArray(summary.relation_types) ? summary.relation_types : [],
      (t) => Number(t.relation_count ?? 0),
    );
    fill(
      doc.getElementById("relation-types"),
      doc.getElementById("relation-types-empty"),
      withHeader(links.sorted.length > 0 && headerRow(doc, {
        label: t("home.column.connection"),
        count: t("home.column.count"),
        share: t("home.column.ofTotal"),
      }), links.sorted.map((type) =>
        row(doc, {
          label: type.label || type.key,
          count: String(Number(type.relation_count ?? 0)),
          share: { value: Number(type.relation_count ?? 0), of: links.total },
          flag: Number(type.invalid_count ?? 0) > 0 ? `${Number(type.invalid_count)} invalid` : "",
          // **It goes somewhere now.** These rows hovered like links and
          // led nowhere, which is the worse half of the two ways to fix
          // it: the page a relation type never had is the one that says
          // what it may join and what a walk makes of it.
          href: relationTypeURL(opened.slug, type.key),
        }),
      )),
    );
  }

  await load();
  const reread = coalesce(load);
  const onEvent = (verdict) => {
    if (verdict.decision !== REREAD) return null;
    if (verdict.target === TARGET_CONTENT || verdict.target === TARGET_EVERYTHING) return reread();
    return null;
  };
  opened.client.connect(onEvent);
  return { ...opened, onEvent };
}

if (globalThis.document && globalThis.document.getElementById("catalogue-note")) {
  const opened = await openGame({ destination: DESTINATION_CATALOGUE });
  if (opened !== null) {
    const doc = opened.document;
    await typesPage(opened);
  }
}
