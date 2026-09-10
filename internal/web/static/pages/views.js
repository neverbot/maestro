// The Views destination: every saved view of this game, paged.
//
// A row is a link, and it is a link **by slug** — a view is addressed by
// its key on every surface of this product, and a URL a designer can
// send a colleague is the whole reason Task 3 put parameter binding in
// the query string.
//
// A game with no views at all gets the product's one piece of
// onboarding, from `pages/page.js` so that this page and the home lane
// cannot come to say two different things: a sentence saying a view is
// written by an agent over MCP, a link to the documentation that teaches
// it, and **no create control of any kind**.

import {
  destinations,
  DESTINATION_VIEWS,
  countLabel,
  emptyOrRows,
  expired,
  gameURL,
  onboarding,
  openGame,
  row,
  say,
  viewURL,
} from "./page.js";
import { goToLogin } from "../app.js";

export async function viewsPage(opened) {
  const doc = opened.document;
  const noteEl = doc.getElementById("view-list-note");
  const listEl = doc.getElementById("views");
  const errorEl = doc.getElementById("views-error");
  const onboardingEl = doc.getElementById("views-onboarding");
  const moreEl = doc.getElementById("views-more");

  if (opened.game === null) {
    say(noteEl, opened.failure ?? "You may not have access to this game, or it no longer exists.");
    return opened;
  }
  const back = doc.getElementById("back-to-game");
  if (back) back.href = gameURL(opened.slug);

  let cursor = null;
  let rendered = 0;

  async function page() {
    if (moreEl) moreEl.disabled = true;
    const answer = await opened.client.listViews(cursor === null ? {} : { cursor });
    if (!answer.ok) {
      if (expired(answer)) {
        goToLogin();
        return;
      }
      // The rows already on screen stay: a page that has rendered ten
      // views and then fails to fetch the eleventh must keep the ten and
      // say what went wrong beside them.
      if (moreEl) moreEl.hidden = true;
      say(errorEl, answer.error.message);
      return;
    }
    say(errorEl, "");
    const body = answer.result;
    const items = Array.isArray(body.items) ? body.items : [];
    for (const view of items) {
      listEl.append(
        row(doc, {
          label: view.name || view.key,
          key: view.key,
          count: String(view.renderer ?? ""),
          // The server's own flag, and the only thing on this page that
          // ever asks a designer to do something: the game moved under
          // this view's saved query.
          flag: view.stale === true ? "stale" : "",
          href: viewURL(opened.slug, view.key, ""),
        }),
      );
    }
    rendered += items.length;
    emptyOrRows(listEl, null, rendered);
    if (onboardingEl) {
      onboardingEl.replaceChildren(...(rendered === 0 ? [onboarding(doc)] : []));
      onboardingEl.hidden = rendered > 0;
    }
    say(noteEl, rendered === 0 ? "" : countLabel(rendered, "view", "views"));
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

if (globalThis.document && globalThis.document.getElementById("view-list-note")) {
  const opened = await openGame({ destination: DESTINATION_VIEWS });
  if (opened !== null) {
    const doc = opened.document;
    await viewsPage(opened);
  }
}
