// The routes listing: every saved claim about a path, and its health.
//
// **Three states, never two.** A route is `never_checked`, `stale` or
// `checked`, and the screen must not collapse them: a tick-or-cross would
// turn "we do not know" into "it is fine", which is the one thing this
// listing exists to prevent. `stale` is not a failure either — it means
// the answer may no longer be about this game — and it is the one place
// on these screens `--danger` is earned.

import {
  DESTINATION_ANALYSIS,
  analysisURL,
  countLabel,
  gameURL,
  openGame,
  routeURL,
  say,
  setBreadcrumb,
  setReadOnly,
} from "./page.js";
import { row } from "../rows.js";

export const NOTE_ROUTES =
  "A route is a claim that one thing can be reached from another, saved so it can be checked " +
  "again when the design moves.";

// The three, in the reader's words, keyed by the engine's own spellings.
export const STATUS_WORDS = {
  never_checked: "Never checked",
  stale: "Checked against an older design",
  checked: "Checked",
};

export const STATUS_CLASSES = {
  never_checked: "never",
  stale: "stale",
  checked: "checked",
};

// statusCell is the status as a row's cell: the word, in the treatment
// its meaning earns. It is a cell and not an icon, because three states
// do not fit in a glyph a reader has to learn.
export function statusCell(status) {
  const key = String(status ?? "");
  return {
    text: STATUS_WORDS[key] || key,
    status: STATUS_CLASSES[key] || "never",
  };
}

export async function routesPage(opened) {
  const doc = opened.document;
  if (opened.game === null) return opened;

  setBreadcrumb(doc, [
    { label: opened.game.name, href: gameURL(opened.slug) },
    { label: DESTINATION_ANALYSIS, href: analysisURL(opened.slug) },
    { label: "Routes" },
  ]);
  doc.title = opened.game.name + " · Routes · Maestro";
  say(doc.getElementById("routes-note"), NOTE_ROUTES);

  const summary = await opened.client.summary();
  if (summary.ok) setReadOnly(doc, summary.result.role, "writes these routes");

  const listEl = doc.getElementById("routes");
  const emptyEl = doc.getElementById("routes-empty");
  const errorEl = doc.getElementById("routes-error");
  const moreEl = doc.getElementById("routes-more");

  let cursor = null;
  let shown = 0;

  async function page() {
    if (moreEl) moreEl.disabled = true;
    const answer = await opened.client.listRoutes(cursor === null ? {} : { cursor });
    if (!answer.ok) {
      if (errorEl) {
        errorEl.textContent = answer.error.message;
        errorEl.hidden = false;
      }
      if (moreEl) moreEl.hidden = true;
      return;
    }
    if (errorEl) errorEl.hidden = true;
    // **`routes`, not `items`.** The listing routes answer under their own
    // name, unlike the entity and view listings, and reading the wrong
    // key gave an empty list on a game that has one — an empty state
    // claiming there are no routes, which is the sharpest form of a page
    // saying something false. Found by opening it.
    const items = Array.isArray(answer.result.routes) ? answer.result.routes : [];
    for (const route of items) {
      const status = statusCell(route.status);
      listEl.append(
        row(doc, {
          label: route.name || route.key,
          key: route.key,
          cells: [
            { text: countLabel(route.step_count ?? 0, "step", "steps") },
            { text: status.text, status: status.status },
          ],
          count: "",
          href: routeURL(opened.slug, route.key),
        }),
      );
    }
    shown += items.length;
    listEl.hidden = shown === 0;
    if (emptyEl) emptyEl.hidden = shown > 0;
    cursor = typeof answer.result.next_cursor === "string" && answer.result.next_cursor !== ""
      ? answer.result.next_cursor
      : null;
    if (moreEl) {
      moreEl.hidden = cursor === null || shown === 0;
      moreEl.disabled = false;
    }
  }

  if (moreEl) moreEl.addEventListener("click", () => page());
  await page();
  return opened;
}

if (globalThis.document && globalThis.document.getElementById("routes-note")) {
  const opened = await openGame({ destination: DESTINATION_ANALYSIS });
  if (opened !== null) await routesPage(opened);
}
