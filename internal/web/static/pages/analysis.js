// The three read-only analyses, on one page.
//
// **The rule that outranks every layout here**
// (docs/superpowers/specs/2026-09-11-analysis-screens-design.md §1): an
// empty findings list is not a verdict. The engine is built around it —
// every result carries what it walked, because an empty list means
// either "the game is clean" or "the walk followed nothing" and the two
// are otherwise the same JSON. So each report has three parts, always: a
// verdict line, the findings, and a muted line saying what the run
// looked at, present whether the list is empty or full.
//
// A run that was cut short says so **above** its findings. A reader who
// has scrolled past them has already believed them.
//
// Nothing here fetches. Every call goes through client.js, and every
// sentence a designer reads is either the server's own or one of the
// constants below, which is what makes them checkable in one place.

import {
  DESTINATION_ANALYSIS,
  analysisURL,
  countLabel,
  entityURL,
  gameURL,
  openGame,
  routesURL,
  say,
  setBreadcrumb,
  setReadOnly,
} from "./page.js";
import { row } from "../rows.js";

// --- The words -------------------------------------------------------
//
// Grouped so a reader of this file can see the whole voice of the screen
// at once, and so the vocabulary test has one place to look.

export const NOTE_ON_DEMAND =
  "Each of these walks the game when you ask it to. Nothing below has been checked yet.";

export const CYCLES_CLEAN = "Nothing depends on itself.";
export const CONTAINMENT_CLEAN = "Nothing contains itself.";
export const UNREACHABLE_CLEAN = "Everything can be reached.";
export const ORPHANS_CLEAN = "Everything is connected to something.";

export const HEADING_PREREQUISITE = "Prerequisite loops";
export const HEADING_CONTAINMENT = "Containment loops";

// The reasons, in the reader's words. The engine's own spellings are
// isolated_from_start, no_path, container_unreachable and depth_limited,
// and not one of them should reach a screen whose job is the game's
// vocabulary.
export const REASONS = {
  isolated_from_start: "Nothing leads to it",
  no_path: "Every way in is itself unreachable",
  container_unreachable: "The place it is in cannot be reached",
  depth_limited: "Further away than this run looked",
};

// depth_limited is a fact about the run and not about the game, and
// mixing it into the findings reports the tool's bound as the designer's
// defect. It gets its own line, below the rest.
export const REASON_DEPTH = "depth_limited";

// The engine's own three, in its own spellings, with the reader's words
// beside them. They are `isolated`, `sink` and `source` — not a
// direction pair somebody could guess at, and guessing wrong is how this
// screen first shipped: the server answered
// `mode: is "both"; the three are "isolated", "sink", "source"`, which is
// a refusal doing the job a picker should have done.
//
// `isolated` is the engine's default and is disjoint from the other two;
// `sink` and `source` partition the entities with exactly one direction
// of edge.
export const ORPHAN_MODES = [
  ["isolated", "Connected to nothing"],
  ["sink", "Nothing leads out of it"],
  ["source", "Nothing leads into it"],
];

export const TRUNCATED_HEAD = "This answer is not complete";
export const TRUNCATED_RESULTS = "The run stopped at its result limit, so there may be more than these.";
export const TRUNCATED_DEPTH =
  "The walk stopped at its depth bound, so something further away may exist and this run did not look.";

export const UNDECLARED_HEAD = "This game has not said what its connections do";
export const UNDECLARED_BODY =
  "An analysis walks edges by what they mean, not by what they are called. Until a relation type " +
  "declares that, there is nothing to follow — and an engine with nothing to follow would answer " +
  "that nothing is wrong, which is the one answer it must never give.";

export const RUN_LABEL = "Check";
export const RUNNING_LABEL = "Checking…";

// --- The page --------------------------------------------------------

function el(doc, id) {
  return doc.getElementById(id);
}

// walkedLine is the negative half, and it is assembled from whatever
// counts the result carries rather than from a template per analysis, so
// a count the engine adds later cannot be silently left off.
export function walkedLine(result) {
  const parts = [];
  if (Number.isFinite(result.seed_total)) parts.push("started from " + countLabel(result.seed_total, "entity", "entities"));
  if (Number.isFinite(result.considered_total)) parts.push("considered " + countLabel(result.considered_total, "entity", "entities"));
  if (Number.isFinite(result.reachable_total)) parts.push("reached " + countLabel(result.reachable_total, "entity", "entities"));
  if (Number.isFinite(result.edges_walked)) parts.push("followed " + countLabel(result.edges_walked, "edge", "edges"));
  if (parts.length === 0) return "";
  return "This run " + parts.join(", ") + ".";
}

// cycleRow spells the loop in order: the names are the game's own words
// and each is a link, the relation types are the game's words between
// them, and the arrow is text. A drawing was considered and refused —
// at three or four nodes it spends a hundred square pixels to say what
// one line says better, and eleven of them is eleven coordinate systems
// to orient in.
export function cycleRow(doc, slug, cycle) {
  const item = doc.createElement("li");
  item.className = "cycle";
  const nodes = Array.isArray(cycle.entities) ? cycle.entities : [];
  const edges = Array.isArray(cycle.edges) ? cycle.edges : [];
  nodes.forEach((node, index) => {
    const link = doc.createElement("a");
    link.className = "cycle-node";
    link.href = entityURL(slug, node.entity_type ?? node.type ?? "", node.key ?? "");
    link.textContent = node.name || node.key || "";
    item.append(link);
    const edge = edges[index];
    if (edge) {
      const via = doc.createElement("span");
      via.className = "cycle-edge";
      via.textContent = "→ " + (edge.relation_type ?? edge.type ?? "") + " →";
      item.append(via);
    }
  });
  // The loop closes: the first name again, so a reader sees it is one.
  if (nodes.length > 0 && edges.length >= nodes.length) {
    const back = doc.createElement("span");
    back.className = "cycle-node closing";
    back.textContent = nodes[0].name || nodes[0].key || "";
    item.append(back);
  }
  return item;
}

function cycleList(doc, slug, heading, cycles) {
  const wrap = doc.createElement("section");
  const head = doc.createElement("h3");
  head.textContent = heading;
  wrap.append(head);
  const count = doc.createElement("p");
  count.className = "muted";
  count.textContent = countLabel(cycles.length, "loop", "loops");
  wrap.append(count);
  const list = doc.createElement("ul");
  list.className = "cycles";
  for (const cycle of cycles) list.append(cycleRow(doc, slug, cycle));
  wrap.append(list);
  return wrap;
}

// reasonGroups groups the findings by the reason a designer acts on,
// because fixing "nothing leads to it" is one kind of work and fixing
// "every way in is unreachable" is another. The type is a column.
export function reasonGroups(findings) {
  const groups = new Map();
  for (const finding of findings) {
    const reason = String(finding.reason ?? "");
    if (!groups.has(reason)) groups.set(reason, []);
    groups.get(reason).push(finding);
  }
  return groups;
}

function unreachableSection(doc, slug, reason, findings) {
  const wrap = doc.createElement("section");
  const head = doc.createElement("h3");
  head.textContent = REASONS[reason] || reason;
  wrap.append(head);
  const list = doc.createElement("ul");
  list.className = "catalogue";
  for (const finding of findings) {
    // The blockers are the only thing on this screen that is not already
    // on another one: they are what would have let this entity through.
    const blockers = Array.isArray(finding.blockers) ? finding.blockers : [];
    list.append(
      row(doc, {
        label: finding.name || finding.key,
        key: finding.key,
        cells: [
          { text: finding.entity_type ?? "" },
          blockers.length === 0
            ? { text: "no way in", absent: true }
            : { text: blockers.map((b) => b.key ?? "").join(", ") },
        ],
        count: "",
        href: entityURL(slug, finding.entity_type ?? "", finding.key ?? ""),
      }),
    );
  }
  wrap.append(list);
  return wrap;
}

// showUndeclared turns the engine's own refusal into something a person
// can act on. The payload is every relation type of the game with what
// it currently declares, which is the list the recovery needs in front
// of it — the error carries it rather than pointing at it for exactly
// this reason.
export function showUndeclared(doc, slug, error) {
  say(el(doc, "undeclared-head"), UNDECLARED_HEAD);
  say(el(doc, "undeclared-body"), UNDECLARED_BODY);
  // The advice is the engine's own, generated from the vocabulary rather
  // than written here, so it cannot promise a word the column refuses.
  const details = error && error.details ? error.details : {};
  say(el(doc, "undeclared-advice"), String(details.advice ?? ""));
  const box = el(doc, "undeclared");
  if (box) box.hidden = false;

  const list = el(doc, "undeclared-types");
  if (!list) return;
  // `relation_types`, which is what the domain's Details() writes.
  const types = Array.isArray(details.relation_types) ? details.relation_types : [];
  list.replaceChildren();
  for (const type of types) {
    const traits = Array.isArray(type.analysis_traits) ? type.analysis_traits : [];
    list.append(
      row(doc, {
        label: type.key ?? "",
        key: "",
        cells: [
          type.semantic_role ? { text: type.semantic_role } : { text: "no role", absent: true },
          traits.length > 0 ? { text: traits.join(", ") } : { text: "declares nothing", absent: true },
        ],
        count: "",
      }),
    );
  }
  list.hidden = types.length === 0;
}

function hideUndeclared(doc) {
  const box = el(doc, "undeclared");
  if (box) box.hidden = true;
  const list = el(doc, "undeclared-types");
  if (list) list.hidden = true;
}

// report wires one section: its button, its three parts, and the two
// refusals every one of them can answer with.
function report(doc, slug, name, run, paint) {
  const button = el(doc, name + "-run");
  const verdict = el(doc, name + "-verdict");
  const body = el(doc, name + "-body");
  const walked = el(doc, name + "-walked");
  const error = el(doc, name + "-error");

  async function go() {
    if (button) {
      button.disabled = true;
      button.textContent = RUNNING_LABEL;
    }
    const answer = await run();
    if (button) {
      button.disabled = false;
      button.textContent = RUN_LABEL;
    }
    if (!answer.ok) {
      if (answer.error && answer.error.code === "semantics_undeclared") {
        // Not this section's failure: it is the game's, and every one of
        // the three would answer the same way, so it is said once at the
        // foot of the page rather than three times in three sections.
        showUndeclared(doc, slug, answer.error);
        say(verdict, "");
        if (body) body.replaceChildren();
        say(walked, "");
        if (error) error.hidden = true;
        return;
      }
      if (error) {
        error.textContent = answer.error.message;
        error.hidden = false;
      }
      return;
    }
    hideUndeclared(doc);
    if (error) error.hidden = true;
    paint(answer.result, { verdict, body, walked });
    say(walked, walkedLine(answer.result));
  }

  if (button) button.addEventListener("click", () => go());
  return go;
}

function showTruncation(doc, name, result) {
  const box = el(doc, name + "-truncated");
  if (!box) return;
  const reasons = [];
  if (result.truncated === true) reasons.push(TRUNCATED_RESULTS);
  if (result.depth_limited === true) reasons.push(TRUNCATED_DEPTH);
  box.hidden = reasons.length === 0;
  say(el(doc, name + "-truncated-head"), reasons.length === 0 ? "" : TRUNCATED_HEAD);
  say(el(doc, name + "-truncated-body"), reasons.join(" "));
}

export async function analysisPage(opened) {
  const doc = opened.document;
  if (opened.game === null) return opened;

  setBreadcrumb(doc, [
    { label: opened.game.name, href: gameURL(opened.slug) },
    { label: DESTINATION_ANALYSIS },
  ]);
  doc.title = opened.game.name + " · Analysis · Maestro";
  say(el(doc, "analysis-note"), NOTE_ON_DEMAND);

  // The fourth analysis, which is the one with a lifecycle and therefore
  // its own screens rather than a section here.
  const routesLink = el(doc, "routes-link");
  if (routesLink) routesLink.href = routesURL(opened.slug);

  const summary = await opened.client.summary();
  if (summary.ok) setReadOnly(doc, summary.result.role, "runs these checks");

  report(doc, opened.slug, "cycles", () => opened.client.analysisCycles({}), (result, parts) => {
    showTruncation(doc, "cycles", result);
    const loops = Array.isArray(result.cycles) ? result.cycles : [];
    const contains = Array.isArray(result.containment_cycles) ? result.containment_cycles : [];
    const lines = [];
    if (loops.length === 0) lines.push(CYCLES_CLEAN);
    if (contains.length === 0) lines.push(CONTAINMENT_CLEAN);
    say(parts.verdict, lines.join(" "));
    parts.body.replaceChildren();
    // Two lists and not one: a containment loop is a hierarchy that is
    // not one, a prerequisite loop is a gate nobody can open. Different
    // sentence, different fix.
    if (loops.length > 0) parts.body.append(cycleList(doc, opened.slug, HEADING_PREREQUISITE, loops));
    if (contains.length > 0) parts.body.append(cycleList(doc, opened.slug, HEADING_CONTAINMENT, contains));
  });

  report(doc, opened.slug, "unreachable", () => opened.client.analysisUnreachable({}), (result, parts) => {
    showTruncation(doc, "unreachable", result);
    const findings = Array.isArray(result.unreachable) ? result.unreachable : [];
    say(
      parts.verdict,
      findings.length === 0
        ? UNREACHABLE_CLEAN
        : countLabel(findings.length, "entity", "entities") + " cannot be reached.",
    );
    parts.body.replaceChildren();
    const groups = reasonGroups(findings);
    for (const [reason, rows] of groups) {
      if (reason === REASON_DEPTH) continue;
      parts.body.append(unreachableSection(doc, opened.slug, reason, rows));
    }
    const beyond = groups.get(REASON_DEPTH);
    if (beyond && beyond.length > 0) {
      const note = doc.createElement("p");
      note.className = "muted";
      note.textContent =
        countLabel(beyond.length, "entity", "entities") + " were further away than this run looked.";
      parts.body.append(note);
    }
  });

  // The mode is this screen's one control, and exactly one is selected.
  let mode = ORPHAN_MODES[0][0];
  const modesEl = el(doc, "orphans-modes");
  const runOrphans = report(doc, opened.slug, "orphans", () => opened.client.analysisOrphans({ mode }), (result, parts) => {
    const findings = Array.isArray(result.orphans) ? result.orphans : [];
    say(
      parts.verdict,
      findings.length === 0 ? ORPHANS_CLEAN : countLabel(findings.length, "entity", "entities") + " stand alone.",
    );
    parts.body.replaceChildren();
    const list = doc.createElement("ul");
    list.className = "catalogue";
    for (const orphan of findings) {
      list.append(
        row(doc, {
          label: orphan.name || orphan.key,
          key: orphan.key,
          cells: [
            { text: orphan.entity_type ?? "" },
            { text: String(orphan.in_degree ?? 0), numeric: true },
            { text: String(orphan.out_degree ?? 0), numeric: true },
          ],
          count: "",
          href: entityURL(opened.slug, orphan.entity_type ?? "", orphan.key ?? ""),
        }),
      );
    }
    parts.body.append(list);
    const excluded = Array.isArray(result.excluded_relation_types) ? result.excluded_relation_types : [];
    if (excluded.length > 0) {
      const note = doc.createElement("p");
      note.className = "muted";
      // An orphan report that silently ignored a relation type would be
      // wrong in a way nobody could see.
      note.textContent = "Edges of these kinds were not counted: " + excluded.join(", ") + ".";
      parts.body.append(note);
    }
  });

  if (modesEl) {
    for (const [value, label] of ORPHAN_MODES) {
      const chip = doc.createElement("button");
      chip.type = "button";
      chip.className = "chip";
      chip.textContent = label;
      chip.setAttribute("aria-pressed", String(value === mode));
      chip.addEventListener("click", () => {
        mode = value;
        for (const other of modesEl.querySelectorAll("button")) {
          other.setAttribute("aria-pressed", String(other === chip));
        }
        runOrphans();
      });
      modesEl.append(chip);
    }
  }

  return opened;
}

if (globalThis.document && globalThis.document.getElementById("analysis-note")) {
  const opened = await openGame({ destination: DESTINATION_ANALYSIS });
  if (opened !== null) await analysisPage(opened);
}
