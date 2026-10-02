// The Views destination: every saved view of this game, paged.

import {
  DESTINATION_VIEWS,
  coalesce,
  isEmptyGame,
  countLabel,
  destinations,
  emptyOrRows,
  expired,
  gameURL,
  onboarding,
  openGame,
  row,
  say,
  setBreadcrumb,
  ROLE_VIEWER,
  builderURL,
  setReadOnly,
  viewURL,
} from "./page.js";
import { headerRow, nextCursorOf } from "../rows.js";
import { t } from "../i18n.js";
import { REREAD, TARGET_EVERYTHING, TARGET_VIEW } from "../client.js";
import { goToLogin } from "../app.js";

export async function viewsPage(opened) {
  const doc = opened.document;
  const noteEl = doc.getElementById("view-list-note");
  const listEl = doc.getElementById("views");
  const errorEl = doc.getElementById("views-error");
  const onboardingEl = doc.getElementById("views-onboarding");
  const moreEl = doc.getElementById("views-more");

  if (opened.game === null) {
    say(noteEl, opened.failure ?? t("error.noAccessToGame"));
    return opened;
  }
  setBreadcrumb(doc, [
    { label: opened.game.name, href: gameURL(opened.slug) },
    { label: DESTINATION_VIEWS },
  ]);
  // The game first: a person with three games open read three tabs
  // called "Views".
  doc.title = opened.game.name + " \u00b7 " + t("nav.overviews") + " \u00b7 Maestro";
  const role = await opened.client.summary();
  // **The way in to the builder, where the read-only notice used to be
  // the whole of what this screen could say.** A viewer still gets the
  // notice: the server would refuse the save, and a control that cannot
  // succeed is worse than a sentence saying so.
  // Whether this game holds anything to draw yet, which decides what the
  // empty state offers: an assistant to connect, or the way to compose
  // the first overview.
  const hasContent = role.ok && !isEmptyGame(role.result);
  const actions = doc.getElementById("page-actions");
  if (role.ok && role.result.role !== ROLE_VIEWER && actions) {
    const compose = doc.createElement("a");
    compose.className = "button";
    compose.href = builderURL(opened.slug);
    compose.textContent = t("overviews.new");
    actions.replaceChildren(compose);
  } else if (role.ok) {
    setReadOnly(doc, role.result.role, "writes.views");
  }

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
    // **The header, once, above the first page.** The catalogue has one
    // and this list did not, so the renderer's name sat right-aligned in
    // the count track with nothing saying what that word was.
    if (rendered === 0 && items.length > 0) {
      listEl.append(headerRow(doc, { label: t("overviews.column.view"), key: t("column.key"), count: t("overviews.column.renderer") }));
    }
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
      onboardingEl.replaceChildren(...(rendered === 0 ? [onboarding(doc, role.ok ? role.result.role : "", opened.slug, hasContent)] : []));
      onboardingEl.hidden = rendered > 0;
    }
    say(noteEl, rendered === 0 ? "" : countLabel(rendered, t("unit.overview"), t("unit.overviews")));
    cursor = nextCursorOf(body);
    if (moreEl) {
      moreEl.hidden = cursor === null || rendered === 0;
      moreEl.disabled = false;
    }
  }

  if (moreEl) moreEl.addEventListener("click", () => page());
  await page();

  // **This list listened to nothing.** An assistant saving an overview
  // left the page showing the ones it found on load, and a designer
  // watching it work saw nothing happen.
  const reread = coalesce(async () => {
    cursor = null;
    rendered = 0;
    if (listEl) listEl.replaceChildren();
    await page();
  });
  const onEvent = (verdict) => {
    if (verdict.decision !== REREAD) return null;
    if (verdict.target === TARGET_VIEW || verdict.target === TARGET_EVERYTHING) return reread();
    return null;
  };
  opened.client.connect(onEvent);
  return { ...opened, onEvent };
}

if (globalThis.document && globalThis.document.getElementById("view-list-note")) {
  const opened = await openGame({ destination: DESTINATION_VIEWS });
  if (opened !== null) {
    const doc = opened.document;
    await viewsPage(opened);
  }
}
