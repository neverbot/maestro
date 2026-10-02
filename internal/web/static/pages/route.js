// One route: the claim it makes, and what the last check found.

import {
  DESTINATION_ANALYSIS,
  analysisURL,
  countLabel,
  entityURL,
  fillState,
  gameURL,
  openGame,
  routesURL,
  say,
  ROLE_VIEWER,
  segmentsOf,
  setReadOnly,
  setBreadcrumb,
} from "./page.js";
import { headerRow, row } from "../rows.js";
import { locale, t } from "../i18n.js";
import { STATUS_WORDS } from "./routes.js";

// The empty state, in the page rather than in the shell.
export const NO_STEPS_HEADING = t("route.noSteps.heading");
export const NO_STEPS_SENTENCE = t("route.noSteps.sentence");

// The four a step can come back as, in the reader's words. `ok` is not
// "true": a verdict is a sentence about the design, not a boolean.
export const STEP_VERDICTS = {
  ok: t("route.verdict.ok"),
  missing_entity: t("route.verdict.missing"),
  out_of_order: t("route.verdict.outOfOrder"),
  unmet_prerequisite: t("route.verdict.blocked"),
};

export const STALE_HEAD = t("route.stale");
export const CHECK_LABEL = t("route.check");
export const CHECKING_LABEL = t("route.checking");
export const NEVER_CHECKED = t("route.neverChecked");

export function routeKeyOf(pathname) {
  // /g/{slug}/analysis/routes/{key} — the segment after "routes".
  const parts = segmentsOf(pathname);
  return parts[parts.length - 1] || "";
}

function verdictWord(verdict) {
  const key = String(verdict ?? "");
  return STEP_VERDICTS[key] || key;
}

// **A verdict has an age, and that is what makes stale meaningful
// rather than alarming.** The status alone says "this may no longer be
// about your game"; when it was taken and against which version of the
// game is what lets a reader decide whether to care. Shown always, not
// only when the route has gone stale — a fresh verdict with no date is
// a claim with no standing either.
export function whenChecked(raw) {
  if (!raw) return "";
  const when = new Date(raw);
  return Number.isNaN(when.getTime()) ? String(raw) : when.toLocaleString(locale);
}

export function metaLine(route) {
  const parts = [route.key, STATUS_WORDS[route.status] || route.status];
  const checked = whenChecked(route.last_checked_at);
  if (checked !== "") {
    parts.push(t("route.lastCheckedAt", { when: checked }));
    if (Number.isFinite(route.last_checked_design_version)) {
      parts.push(t("route.againstVersion", { version: route.last_checked_design_version }));
    }
  }
  return parts.join(" \u00b7 ");
}

export function paintVerdict(doc, slug, route) {
  const verdictEl = doc.getElementById("route-verdict");
  const walkedEl = doc.getElementById("route-walked");
  const check = route.last_check && typeof route.last_check === "object" ? route.last_check : null;

  if (check === null) {
    say(verdictEl, NEVER_CHECKED);
    say(walkedEl, "");
    return;
  }

  say(
    verdictEl,
    check.holds === true
      ? t("route.everyStepHeld")
      : t("route.stepsBroken", { steps: countLabel(check.steps_broken ?? 0, t("unit.step"), t("unit.steps")) }),
  );

  // The negative half: five `ok`s from a check that walked nothing and
  // five from one that walked four hundred edges are the same answer
  // without this line.
  const parts = [];
  if (Number.isFinite(check.edges_walked)) parts.push(t("route.followed", { edges: countLabel(check.edges_walked, t("unit.edge"), t("unit.edges")) }));
  if (Number.isFinite(check.steps_checked)) parts.push(t("route.over", { steps: countLabel(check.steps_checked, t("unit.step"), t("unit.steps")) }));
  const cut = [];
  if (check.depth_limited === true) cut.push(t("route.cut.depth"));
  if (check.truncated === true) cut.push(t("route.cut.rows"));
  const sentence = parts.length === 0 ? "" : t("route.thisCheck", { what: parts.join(" ") });
  const caveat = cut.length === 0
    ? ""
    : t("route.caveat", { what: cut.join(t("list.and")) });
  say(walkedEl, sentence === "" && caveat === "" ? "" : (sentence + caveat).trim());
}

// paintSteps draws **one table**: the ordered claim and the verdict at
// each step, in one row per step.
export function paintSteps(doc, slug, route) {
  fillState(doc, "route-steps-empty", {
    heading: NO_STEPS_HEADING,
    sentence: NO_STEPS_SENTENCE,
  });

  const listEl = doc.getElementById("route-steps");
  const emptyEl = doc.getElementById("route-steps-empty");
  const steps = Array.isArray(route.steps) ? route.steps : [];
  const check = route.last_check && typeof route.last_check === "object" ? route.last_check : null;
  const byPosition = new Map();
  for (const found of Array.isArray(check && check.steps) ? check.steps : []) {
    byPosition.set(Number(found.position), found);
  }

  listEl.replaceChildren();
  if (steps.length > 0) {
    listEl.append(
      headerRow(doc, {
        label: t("route.step"),
        key: t("column.key"),
        cells: [{ text: t("route.verdict") }, { text: t("route.blockedBy") }],
      }),
    );
  }
  steps.forEach((step, index) => {
    const found = byPosition.get(Number(step.position));
    const blockers = Array.isArray(found && found.blockers) ? found.blockers : [];
    listEl.append(
      row(doc, {
        // The entity's own name, which the engine now sends with the
        // step (`analysis.RouteStep.Name`). This page listed
        // `body_on_the_rocks` in the serif the game's words wear, with
        // the type in the mono slot a person copies from: the two
        // columns swapped, on the one screen that names four entities in
        // a row. A step whose entity is gone has no name, and the key is
        // then the only true thing to show.
        label: step.name || step.key || "",
        key: step.key || "",
        cells: [
          found
            ? { text: verdictWord(found.verdict), status: found.verdict === "ok" ? "checked" : "broken" }
            : { text: t("route.notChecked"), absent: true },
          // **A word, not a blank.** Four rows under "Blocked by" were
          // empty cells, which cannot be told from a value that failed
          // to load — the rule this file's own unreachable section
          // already follows with "no way in". A step nothing blocks is
          // the good case and says so.
          blockers.length === 0
            ? { text: t("route.nothingBlocks"), absent: true }
            : { text: blockers.map((b) => b.key ?? "").join(", ") },
        ],
        // One-based, because a designer reading their own claim counts
        // from one. The model's index started at zero and reached the
        // screen.
        count: String(index + 1),
        href: entityURL(slug, step.entity_type ?? "", step.key ?? ""),
      }),
    );
  });
  listEl.hidden = steps.length === 0;
  if (emptyEl) emptyEl.hidden = steps.length > 0;
}

// paintStale is called from both the first render and the re-check, so a
// route that was checked on load and comes back stale gets a box with
// something in it. It was populated on load only and merely unhidden
// after, which is an empty 78px box.
export function paintStale(doc, route) {
  const staleEl = doc.getElementById("route-stale");
  if (!staleEl) return;
  const stale = route.status === "stale";
  staleEl.hidden = !stale;
  if (!stale) return;
  say(doc.getElementById("route-stale-head"), STALE_HEAD);
  say(
    doc.getElementById("route-stale-body"),
    t("route.staleBody", {
      checked: String(route.last_checked_design_version ?? "?"),
      now: String(route.design_version ?? "?"),
    }),
  );
}

export async function routePage(opened) {
  const doc = opened.document;
  if (opened.game === null) return opened;

  const key = routeKeyOf(opened.location.pathname);
  const nameEl = doc.getElementById("route-name");
  const metaEl = doc.getElementById("route-meta");
  const errorEl = doc.getElementById("route-error");

  const answer = await opened.client.getRoute(key);
  if (!answer.ok) {
    say(nameEl, t("route.unreadable"));
    if (errorEl) {
      errorEl.textContent = answer.error.message;
      errorEl.hidden = false;
    }
    return opened;
  }

  const route = answer.result;
  const name = route.name || route.key;
  say(nameEl, name);
  doc.title = name + " · Maestro";
  setBreadcrumb(doc, [
    { label: opened.game.name, href: gameURL(opened.slug) },
    { label: DESTINATION_ANALYSIS, href: analysisURL(opened.slug) },
    { label: t("checks.routes"), href: routesURL(opened.slug) },
    { label: name },
  ]);
  say(metaEl, metaLine(route));
  // The sentence saying what this claim is about, which the server has
  // always carried and no screen ever drew.
  say(doc.getElementById("route-about"), String(route.description ?? ""));

  // Above the claim, not below it: a reader who scrolls past a caveat
  // has already believed what it qualifies.
  paintStale(doc, route);
  paintSteps(doc, opened.slug, route);

  paintVerdict(doc, opened.slug, route);

  // The one write. It is the primary button on the page because it is
  // the only thing a person came here to do that changes anything.
  // **The notice belongs on this screen and not on the report page**,
  // because this is the one that carries a write. A viewer is told what
  // will happen instead of being handed a button the server refuses.
  const summary = await opened.client.summary();
  const role = summary.ok ? String(summary.result.role ?? "") : "";
  const actions = doc.getElementById("page-actions");
  if (actions && role === ROLE_VIEWER) {
    setReadOnly(doc, role, "writes.route");
  } else if (actions) {
    const button = doc.createElement("button");
    button.type = "button";
    button.textContent = CHECK_LABEL;
    button.addEventListener("click", async () => {
      button.disabled = true;
      button.textContent = CHECKING_LABEL;
      const checked = await opened.client.checkRoute(key);
      button.disabled = false;
      button.textContent = CHECK_LABEL;
      if (!checked.ok) {
        if (errorEl) {
          errorEl.textContent = checked.error.message;
          errorEl.hidden = false;
        }
        return;
      }
      if (errorEl) errorEl.hidden = true;
      // Re-read rather than patch the page from the check's own answer:
      // the status and the two design versions are decided by the
      // server, and a page that computed them here would be a second
      // implementation of a three-state rule.
      const again = await opened.client.getRoute(key);
      if (again.ok) {
        say(metaEl, metaLine(again.result));
        paintStale(doc, again.result);
        paintSteps(doc, opened.slug, again.result);
        paintVerdict(doc, opened.slug, again.result);
      }
    });
    actions.replaceChildren(button);
  }

  return opened;
}

if (globalThis.document && globalThis.document.getElementById("route-name")) {
  const opened = await openGame({ destination: DESTINATION_ANALYSIS });
  if (opened !== null) await routePage(opened);
}
