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
  segmentsOf,
  setBreadcrumb,
} from "./page.js";
import { row } from "../rows.js";
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

export function paintVerdict(doc, slug, route) {
  const verdictEl = doc.getElementById("route-verdict");
  const findingsEl = doc.getElementById("route-findings");
  const walkedEl = doc.getElementById("route-walked");
  findingsEl.replaceChildren();

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

  const steps = Array.isArray(check.steps) ? check.steps : [];
  const list = doc.createElement("ul");
  list.className = "catalogue";
  for (const step of steps) {
    const blockers = Array.isArray(step.blockers) ? step.blockers : [];
    list.append(
      row(doc, {
        label: step.key || "",
        key: step.entity_type || "",
        cells: [
          { text: verdictWord(step.verdict), status: step.verdict === "ok" ? "checked" : "stale" },
          blockers.length === 0
            ? { text: "", absent: false }
            : { text: "blocked by " + blockers.map((b) => b.key ?? "").join(", ") },
        ],
        count: "",
        href: entityURL(slug, step.entity_type ?? "", step.key ?? ""),
      }),
    );
  }
  findingsEl.append(list);

  // The negative half: five `ok`s from a check that walked nothing and
  // five from one that walked four hundred edges are the same answer
  // without this line.
  const parts = [];
  if (Number.isFinite(check.edges_walked)) parts.push("followed " + countLabel(check.edges_walked, "edge", "edges"));
  if (Number.isFinite(check.steps_checked)) parts.push("over " + countLabel(check.steps_checked, "step", "steps"));
  say(walkedEl, parts.length === 0 ? "" : "This check " + parts.join(" ") + ".");
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
  say(metaEl, route.key + " · " + (STATUS_WORDS[route.status] || route.status));

  // **Above the claim, not below it.** The two numbers are the route's
  // own: what it was checked against, and where the game is now.
  const staleEl = doc.getElementById("route-stale");
  if (staleEl) {
    const stale = route.status === "stale";
    staleEl.hidden = !stale;
    if (stale) {
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
  }

  const stepsEl = doc.getElementById("route-steps");
  const stepsEmptyEl = doc.getElementById("route-steps-empty");
  const steps = Array.isArray(route.steps) ? route.steps : [];
  stepsEl.replaceChildren();
  for (const step of steps) {
    stepsEl.append(
      row(doc, {
        label: step.key || "",
        key: step.entity_type || "",
        cells: [step.note ? { text: step.note } : { text: "" }],
        count: String(step.position ?? ""),
        href: entityURL(opened.slug, step.entity_type ?? "", step.key ?? ""),
      }),
    );
  }
  stepsEl.hidden = steps.length === 0;
  if (stepsEmptyEl) stepsEmptyEl.hidden = steps.length > 0;

  paintVerdict(doc, opened.slug, route);

  // The one write. It is the primary button on the page because it is
  // the only thing a person came here to do that changes anything.
  const actions = doc.getElementById("page-actions");
  if (actions) {
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
        say(metaEl, again.result.key + " · " + (STATUS_WORDS[again.result.status] || again.result.status));
        if (staleEl) staleEl.hidden = again.result.status !== "stale";
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
