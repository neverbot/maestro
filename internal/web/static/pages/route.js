// One route: the claim it makes, and what the last check found.
//
// **Two columns because they are two things.** The steps this route
// asserts, in order, and the verdict at each — and a verdict about a
// game that has since moved says so above the claim, in the route's own
// numbers, because a reader who scrolls past it has already believed it.
//
// The one write in the whole of Analysis is here: "Check this route".

import {
  DESTINATION_ANALYSIS,
  analysisURL,
  countLabel,
  entityURL,
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
import { STATUS_WORDS } from "./routes.js";

// The four a step can come back as, in the reader's words. `ok` is not
// "true": a verdict is a sentence about the design, not a boolean.
export const STEP_VERDICTS = {
  ok: "reachable",
  missing_entity: "no longer exists",
  out_of_order: "comes before something it needs",
  unmet_prerequisite: "blocked",
};

export const STALE_HEAD = "This answer may no longer be about this game";
export const CHECK_LABEL = "Check this route";
export const CHECKING_LABEL = "Checking…";
export const NEVER_CHECKED = "This route has never been checked.";

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
  return Number.isNaN(when.getTime()) ? String(raw) : when.toLocaleString();
}

export function metaLine(route) {
  const parts = [route.key, STATUS_WORDS[route.status] || route.status];
  const checked = whenChecked(route.last_checked_at);
  if (checked !== "") {
    parts.push("last checked " + checked);
    if (Number.isFinite(route.last_checked_design_version)) {
      parts.push("against design version " + route.last_checked_design_version);
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
      ? "Every step held."
      : countLabel(check.steps_broken ?? 0, "step", "steps") + " did not hold.",
  );

  // The negative half: five `ok`s from a check that walked nothing and
  // five from one that walked four hundred edges are the same answer
  // without this line.
  const parts = [];
  if (Number.isFinite(check.edges_walked)) parts.push("followed " + countLabel(check.edges_walked, "edge", "edges"));
  if (Number.isFinite(check.steps_checked)) parts.push("over " + countLabel(check.steps_checked, "step", "steps"));
  say(walkedEl, parts.length === 0 ? "" : "This check " + parts.join(" ") + ".");
}

// paintSteps draws **one table**: the ordered claim and the verdict at
// each step, in one row per step.
//
// It was two tables, 220px apart, with the same four keys in the same
// order in both — so the one question a reader has, "did step three
// hold?", was answered by matching a key across two lists by eye. The
// position is the first column, because the order is what makes this a
// claim rather than a set.
export function paintSteps(doc, slug, route) {
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
        label: "Step",
        key: "type",
        cells: [{ text: "Verdict" }, { text: "Blocked by" }],
      }),
    );
  }
  steps.forEach((step, index) => {
    const found = byPosition.get(Number(step.position));
    const blockers = Array.isArray(found && found.blockers) ? found.blockers : [];
    listEl.append(
      row(doc, {
        label: step.key || "",
        key: step.entity_type || "",
        cells: [
          found
            ? { text: verdictWord(found.verdict), status: found.verdict === "ok" ? "checked" : "broken" }
            : { text: "not checked", absent: true },
          blockers.length === 0 ? { text: "" } : { text: blockers.map((b) => b.key ?? "").join(", ") },
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
    "It was checked against design version " +
      String(route.last_checked_design_version ?? "?") +
      "; this game is at " +
      String(route.design_version ?? "?") +
      ".",
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
    say(nameEl, "Could not read this route");
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
    { label: "Routes", href: routesURL(opened.slug) },
    { label: name },
  ]);
  say(metaEl, metaLine(route));

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
    setReadOnly(doc, role, "checks these routes");
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
