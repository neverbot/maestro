// The game home: three bands a reader goes down, **what this game is
// made of · saved to look at · writing**.

import {
  REREAD,
  TARGET_CONTENT,
  TARGET_EVERYTHING,
  TARGET_PROSE,
  TARGET_VIEW,
} from "../client.js";
import {
  STATE_EMPTY,
  TAB_AGENTS,
  assetsURL,
  countLabel,
  docURL,
  emptyOrRows,
  expired,
  fill,
  fillState,
  negativeState,
  isEmptyGame,
  coalesce,
  copyLine,
  onboarding,
  openGame,
  row,
  say,
  setReadOnly,
  settingsURL,
  relationTypeURL,
  typeURL,
  typesURL,
  viewURL,
  viewsURL,
  whoWrites,
} from "./page.js";
import { byWeight, nextCursorOf } from "../rows.js";
import { goToLogin } from "../app.js";
import { t } from "../i18n.js";

// What the two role-dependent empty states are about, in the words the
// sentence needs. They are arguments to one function rather than two
// functions, because "who may do this" is one rule with two subjects.
// The key of a whole sentence, not a fragment to splice: see whoWrites.
export const DECLARES_TYPES = "writes.types";
export const WRITES_DOCUMENTS = "writes.documents";

// The three negative states the home's lanes can be in. They live here
// rather than in game.html, and pages/types.js reads the first two from
// here rather than keeping a second wording of the same state: the
// catalogue destination shows the same two catalogues this lane does,
// and two wordings of one state is one of them going stale.
export const NO_TYPES_HEADING = t("content.empty.heading");
export const NO_TYPES_SENTENCE = t("content.empty.sentence");
  "and circuits in another. Yours has named none yet.";
export const NO_RELATION_TYPES_HEADING = t("connections.empty.heading");
export const NO_RELATION_TYPES_SENTENCE = t("connections.empty.sentence");
  "needs another one first, a reward unlocks a class.";
export const NO_PROSE_HEADING = t("writing.empty.heading");
export const NO_PROSE_SENTENCE = t("writing.empty.sentence");
  "character says. Every piece keeps its older versions.";

// describeTotals is the one line under the game's name. An empty game
// says so in words rather than showing three zeros, which reads as a
// broken page rather than a new one.
export function describeTotals(totals, written) {
  const counts = totals && typeof totals === "object" ? totals : {};
  const entities = Number(counts.entities ?? 0);
  const relations = Number(counts.relations ?? 0);
  const invalid = Number(counts.invalid ?? 0);
  const documents = Number(written ?? 0);
  if (entities === 0 && relations === 0 && documents === 0) return t("home.totals.empty");
  const parts = [];
  // **A game that is only writing is still a game.** This counted the
  // metamodel and nothing else, so a game holding a document and no
  // types read "Nothing in this game yet" with the document on screen
  // underneath it.
  if (entities > 0 || relations > 0) {
    parts.push(
      countLabel(entities, t("home.totals.thing"), t("home.totals.things")),
      countLabel(relations, t("home.totals.connection"), t("home.totals.connections")),
    );
  }
  if (documents > 0) parts.push(countLabel(documents, t("home.totals.writing"), t("home.totals.writings")));
  // Only when there are any: a permanent "0 no longer fit" would train a
  // designer to ignore the one number on this page that ever asks them
  // to do something.
  if (invalid > 0) parts.push(t("home.totals.misfit", { count: invalid }));
  return parts.join(" · ");
}

// describeDocKinds turns GET /docs/kinds into the one line above the
// documents. A kind is free text this game invented and Maestro ships no
// vocabulary of them, so this line is the only place a designer sees
// which kinds their own prose is using. It returns the empty string for
// a game with none, and the caller hides the line rather than printing
// "no kinds", which reads as a fault on a page whose documents are
// underneath it.
// countDocuments is the whole of this game's writing, from the same
// answer the kinds line is built from. The listing itself is paged and
// carries no total, so this is the only exact number the page has.
export function countDocuments(body) {
  const from = body && typeof body === "object" ? body : {};
  const kinds = Array.isArray(from.kinds) ? from.kinds : [];
  let total = Number(from.unkinded ?? 0);
  for (const k of kinds) total += Number(k.document_count ?? 0);
  return total;
}

export function describeDocKinds(body) {
  const from = body && typeof body === "object" ? body : {};
  const kinds = Array.isArray(from.kinds) ? from.kinds : [];
  if (kinds.length === 0) return "";
  const parts = kinds.map((k) => `${k.kind} ${Number(k.document_count ?? 0)}`);
  let line = t("writing.kinds", { kinds: parts.join(" · ") });
  const unkinded = Number(from.unkinded ?? 0);
  if (unkinded > 0) line += " · " + t("writing.kinds.unkinded", { count: unkinded });
  return line;
}

// describeView is the second column of a views row: what it draws and
// whether the game has moved under it. `stale` is the server's own flag
// and is the only thing on this lane that ever asks for attention.
// DRAWN_AS turns a renderer's name into what a reader will actually see.
// A designer has no idea what "layered" or "nested" mean, and does not
// need to: the row says how the thing is drawn, in the words they would
// use for it themselves.
export const DRAWN_AS = {
  graph: t("overviews.drawn.graph"),
  layered: t("overviews.drawn.layered"),
  nested: t("overviews.drawn.nested"),
  map: t("overviews.drawn.map"),
  timeline: t("overviews.drawn.timeline"),
  table: t("overviews.drawn.table"),
};

export function describeView(view) {
  const from = view && typeof view === "object" ? view : {};
  const renderer = String(from.renderer ?? "");
  return DRAWN_AS[renderer] ?? renderer;
}


// --- The page --------------------------------------------------------

export async function home(opened) {
  const doc = opened.document;
  const nameEl = doc.getElementById("game-name");
  const summaryEl = doc.getElementById("game-summary");

  if (opened.game === null) {
    if (opened.failure !== null) {
      say(nameEl, "Could not load this game");
      say(summaryEl, opened.failure);
      return { ...opened, onEvent: null };
    }
    say(nameEl, "Game not found");
    say(summaryEl, "You may not have access to this game, or it no longer exists.");
    return { ...opened, onEvent: null };
  }

  // **No trail here, deliberately.** The home is the root of a game and a
  // one-item trail is not a trail: it printed the game's name in muted
  // 0.8rem directly above an <h1> holding the same name. Every screen
  // *under* the home has one, and the first crumb on all of them is this
  // page.
  // The tab's own name. Every page in the product was titled "Maestro",
  // so a designer with the engine and three games open read three
  // identical tabs.
  doc.title = opened.game.name + " · Maestro";

  const { slug, client } = opened;
  // textContent, never markup: a game name is chosen by whoever created
  // the game, so it is untrusted input as far as this page is concerned.
  say(nameEl, opened.game.name);

  // The three lanes head to the three destinations. The links are set
  // here rather than written into the shell because only this module
  // knows the slug, and a hard-coded href in the shell would be an
  // address the address functions could not check.
  // The lane headings are headings, not links. They were two links and
  // one plain heading, and both links pointed at destinations the header
  // already carries: three routes to two pages from one screen.
  // The Images link that used to sit here, alone below the three lanes
  // and outside their grid, is gone: Images is a destination in the
  // header now, and a second route to the same page from the same
  // screen is the duplication design.md asks to be reported as a defect.

  const prose = proseLane(doc, slug, client);

  // In lane order. The catalogue's call is also the one that carries the
  // caller's role, which two of the three lanes word an empty state
  // from, so it is awaited before the sentences that need it.
  const summary = await catalogueLane(doc, slug, client);
  // A summary that never answered is the page's failure and not one
  // lane's: the totals line carries the server's sentence and the lanes
  // stay hidden, because two empty catalogues under a message read as a
  // game with nothing in it.
  if (!summary.ok) return { ...opened, onEvent: null };
  const viewCount = await viewsLane(doc, slug, client, summary.role, !summary.empty);
  setReadOnly(doc, summary.role, "writes.content");
  // **The way into the one screen that changes a game rather than its
  // content**, and it is here because the frame's five destinations are
  // places to go and read: settings is one person's screen, reached
  // from the game it belongs to. Only an owner sees it, because only an
  // owner can save anything there.
  offerSettings(doc, slug, summary.role);
  const written = await prose.load(summary.role);

  // The stream, last: the page has just read everything, so the first
  // connection schedules nothing and only a later event asks for a
  // re-read.
  // **Every band re-reads, and the page leaves the empty state on its
  // own.** This listened for one target and reloaded one band, so an
  // agent declaring a game's first type wrote three hundred rows into a
  // screen still showing "nothing in this game yet".
  //
  // `resync` is the server saying a subscription's buffer overflowed and
  // events were lost. It arrives as TARGET_EVERYTHING and was dropped
  // here, which made the one signal about the stream's own gaps the one
  // signal this page ignored.
  const role = summary.role;
  // What each band last reported, so "is this game empty" is a fact
  // about the whole page.
  const held = {
    catalogue: summary.empty,
    views: viewCount,
    documents: written,
    totals: summary.summary.totals,
    raw: summary.summary,
  };
  const settle = () => {
    const nothing = emptyPage(held);
    say(summaryEl, nothing ? "" : describeTotals(held.totals, held.documents));
    if (summaryEl) summaryEl.hidden = nothing;
    offerToConnectAnAgent(doc, slug, held.raw, opened.game.name, nothing);
    showBands(doc, nothing);
  };

  const rereadContent = coalesce(async () => {
    const again = await catalogueLane(doc, slug, client);
    // The crossing this page never made: a game that was empty when it
    // loaded and is not any more stops showing the three steps and
    // starts showing what it holds, without a reload.
    if (!again.ok) return;
    held.catalogue = again.empty;
    held.totals = again.summary.totals;
    held.raw = again.summary;
    settle();
  });
  const rereadViews = coalesce(async () => {
    held.views = await viewsLane(doc, slug, client, role, !held.catalogue);
    settle();
  });
  const rereadProse = coalesce(async () => {
    held.documents = await prose.load(role);
    settle();
  });
  const rereadAll = coalesce(async () => {
    const again = await catalogueLane(doc, slug, client);
    if (!again.ok) return;
    held.catalogue = again.empty;
    held.totals = again.summary.totals;
    held.raw = again.summary;
    held.views = await viewsLane(doc, slug, client, again.role, !again.empty);
    held.documents = await prose.load(again.role);
    settle();
  });

  const onEvent = (verdict) => {
    if (verdict.decision !== REREAD) return null;
    switch (verdict.target) {
      case TARGET_EVERYTHING:
        return rereadAll();
      case TARGET_CONTENT:
        return rereadContent();
      case TARGET_VIEW:
        return rereadViews();
      case TARGET_PROSE:
        return rereadProse();
      default:
        return null;
    }
  };
  client.connect(onEvent);

  // Revealed only now, with every band already filled, so the page never
  // flashes three empty lists on its way to the real ones. A game with
  // nothing in it keeps them hidden: the band above has just said what
  // this game will hold and what to do about it, and three more "nothing
  // yet" underneath say the same thing worse.
  settle();
  // The listener is handed back as well as registered, because "the
  // prose lane re-reads on a moved document" is a property of *this*
  // function and a harness that could only reach it through a live
  // stream would be testing the stream.
  return { ...opened, onEvent };
}

// catalogueLane is the whole of the summary: the totals line, the two
// type catalogues, and the role the other two lanes word their empty
// states from. **One call**, and the test that says so is the reason
// this function takes no per-type argument to fetch with.
async function catalogueLane(doc, slug, client) {
  const answer = await client.summary();
  if (!answer.ok) {
    if (expired(answer)) {
      goToLogin();
      return { role: "", ok: false };
    }
    // The server's own message where the totals would have gone, and the
    // catalogue hidden: a page that says what went wrong beats one that
    // silently shows an empty catalogue, which is indistinguishable from
    // a game with nothing in it.
    say(doc.getElementById("game-summary"), answer.error.message);
    // Hidden here rather than left alone. game.html ships #home hidden
    // and this arm never reveals it, so the two are the same pixels —
    // but "the failure path hides the lanes" is then a property of the
    // shell and not of this function, which is exactly how an assertion
    // about it comes to hold whether or not this code does anything.
    const lanes = doc.getElementById("home");
    if (lanes) lanes.hidden = true;
    return { role: "", ok: false };
  }

  const summary = answer.result;
  const entityTypes = Array.isArray(summary.entity_types) ? summary.entity_types : [];
  const relationTypes = Array.isArray(summary.relation_types) ? summary.relation_types : [];
  const empty = isEmptyGame(summary);

  fillState(doc, "types-empty", {
    heading: NO_TYPES_HEADING,
    sentence: NO_TYPES_SENTENCE + " " + whoWrites(summary.role, DECLARES_TYPES),
  });
  fillState(doc, "relation-types-empty", {
    heading: NO_RELATION_TYPES_HEADING,
    sentence: NO_RELATION_TYPES_SENTENCE,
  });

  // **No key column.** `Deidades  deity  27` spent a whole column, on
  // every row, on the word an assistant uses. A designer reads the
  // game's own word here and meets the key on the thing's own page,
  // where they would copy it.
  const things = byWeight(entityTypes, (t) => Number(t.entity_count ?? 0));
  fill(
    doc.getElementById("types"),
    doc.getElementById("types-empty"),
    things.sorted.map((type) =>
      row(doc, {
        label: type.label_plural || type.label || type.key,
        count: String(Number(type.entity_count ?? 0)),
        share: { value: Number(type.entity_count ?? 0), of: things.total },
        flag: Number(type.invalid_count ?? 0) > 0 ? `${Number(type.invalid_count)} invalid` : "",
        href: typeURL(slug, type.key),
      }),
    ),
  );
  const links = byWeight(relationTypes, (t) => Number(t.relation_count ?? 0));
  fill(
    doc.getElementById("relation-types"),
    doc.getElementById("relation-types-empty"),
    links.sorted.map((type) =>
      row(doc, {
        label: type.label || type.key,
        count: String(Number(type.relation_count ?? 0)),
        share: { value: Number(type.relation_count ?? 0), of: links.total },
        // Since migration 0009 an edge is judged against its relation
        // type's field schema too, so a relation type can hold rows a
        // designer has to go and fix.
        flag: Number(type.invalid_count ?? 0) > 0 ? `${Number(type.invalid_count)} invalid` : "",
        // The same destination the Catalogue's own list points at: a row
        // that hovers like a link and goes nowhere is the state this
        // lane and that one were both in.
        href: relationTypeURL(slug, type.key),
      }),
    ),
  );
  return { role: String(summary.role ?? ""), ok: true, empty, summary };
}

// showBands is the one place that decides whether this page is the empty
// one or the full one.
// held is what the three bands last reported. "Empty" is a fact about
// the whole page: a game with one document and no types is not empty,
// and hiding its band is how that document became invisible.
export function emptyPage(held) {
  return Boolean(held.catalogue) && Number(held.views ?? 0) === 0 && Number(held.documents ?? 0) === 0;
}

export function showBands(doc, empty) {
  const bands = doc.getElementById("home");
  if (bands) bands.hidden = Boolean(empty);
  return bands;
}

// SETTINGS_LABEL is the link, and every member of the game gets it.
export const SETTINGS_LABEL = t("frame.gameSettings");
const OWNER = "owner";
const EDITOR = "editor";

export function offerSettings(doc, slug, role) {
  if (role === "") return null;
  const host = doc.getElementById("page-actions");
  if (!host) return null;
  const link = doc.createElement("a");
  link.href = settingsURL(slug);
  link.textContent = SETTINGS_LABEL;
  host.append(link);
  return link;
}

// --- The empty game ----------------------------------------------------

export const CONNECT_HEADING = t("home.empty.heading");
export const CONNECT_SENTENCE = t("home.empty.sentence");
  "it in here. Your AI assistant writes it, and you read it, judge it and correct it.";
export const CONNECT_LABEL = t("home.empty.key");
export const CONNECT_STEPS = [
  t("home.empty.step1"),
  t("home.empty.step2"),
  t("home.empty.step3"),
];
// The sentence a designer copies. It is the one thing on this page that
// turns understanding into a game with something in it, and it names
// this game rather than a placeholder, because a line somebody has to
// edit before using is a line they get wrong.
export function startingPrompt(name) {
  return t("home.empty.prompt", { game: name });
}

// offerToConnectAnAgent is the crossing, and it is offered **only to
// somebody who can make it**: minting is an editor's or the owner's, so
// telling a viewer to connect an agent would be sending them to a form
// the server refuses. A viewer of an empty game keeps the three lanes'
// own sentences, which are true for them.
export function offerToConnectAnAgent(doc, slug, summary, name, empty) {
  const host = doc.getElementById("connect-agent");
  if (!host) return null;
  const role = String(summary?.role ?? "");
  // **Empty is the whole page's fact**, handed in: a game holding one
  // document and no types is not a game to show three steps to, and it
  // used to get them with the document on screen underneath.
  const nothing = empty === undefined ? isEmptyGame(summary) : Boolean(empty);
  const show = nothing && (role === OWNER || role === EDITOR);
  if (!show) {
    host.replaceChildren();
    host.hidden = true;
    return null;
  }
  // No action of its own: the way in is step one, where a reader is
  // already looking. It was both, and a page that offers the same link
  // twice in four lines reads as two different things to do.
  const state = negativeState(doc, {
    kind: STATE_EMPTY,
    heading: CONNECT_HEADING,
    sentence: CONNECT_SENTENCE,
  });

  // **The steps, and the sentence to copy.** design.md used to forbid
  // this ("never a tutorial: a person reading it has no API"), which was
  // true when nothing here could hand a person a key. Game settings has
  // an Agents tab now, so the rule's premise is gone and what it left
  // behind was a page telling a new designer their situation was
  // hopeless.
  const steps = doc.createElement("ol");
  steps.className = "steps";
  CONNECT_STEPS.forEach((text, at) => {
    const step = doc.createElement("li");
    if (at === 0) {
      const way = doc.createElement("a");
      way.href = settingsURL(slug) + TAB_AGENTS;
      way.textContent = CONNECT_LABEL;
      step.append(way);
    }
    step.append(doc.createTextNode(text));
    steps.append(step);
  });
  state.append(steps);
  state.append(copyLine(doc, startingPrompt(name), t("home.empty.copy")));

  host.replaceChildren(state);
  host.hidden = false;
  return state;
}

// viewsLane lists the saved views, and answers a game that has none with
// the product's one piece of onboarding.
async function viewsLane(doc, slug, client, role, hasContent) {
  const listEl = doc.getElementById("views");
  const errorEl = doc.getElementById("views-error");
  const onboardingEl = doc.getElementById("views-onboarding");
  const answer = await client.listViews({});
  if (!answer.ok) {
    if (expired(answer)) {
      goToLogin();
      return;
    }
    fail(errorEl, listEl, answer.error.message);
    return;
  }
  const items = Array.isArray(answer.result.items) ? answer.result.items : [];
  fill(
    listEl,
    null,
    items.map((view) =>
      row(doc, {
        label: view.name || view.key,
        key: view.key,
        count: describeView(view),
        flag: view.stale === true ? "stale" : "",
        href: viewURL(slug, view.key, ""),
      }),
    ),
  );
  if (onboardingEl) {
    onboardingEl.replaceChildren(...(items.length === 0 ? [onboarding(doc, role, slug, hasContent)] : []));
    onboardingEl.hidden = items.length > 0;
  }
  return items.length;
}

// proseLane is the documents and the vocabulary above them, and it is
// built as a closure because it is the one lane that reloads: a
// `document.moved` re-reads it rather than editing a path in place.
function proseLane(doc, slug, client) {
  const listEl = doc.getElementById("docs");
  const emptyEl = doc.getElementById("docs-empty");
  const errorEl = doc.getElementById("docs-error");
  const moreEl = doc.getElementById("docs-more");
  const kindsEl = doc.getElementById("doc-kinds");

  let cursor = null;
  let rendered = 0;
  let wired = false;

  async function page() {
    if (moreEl) moreEl.disabled = true;
    const answer = await client.listDocs(cursor === null ? {} : { cursor });
    if (!answer.ok) {
      if (expired(answer)) {
        goToLogin();
        return;
      }
      // Hidden, not emptied: a lane that has already rendered two
      // documents and then fails to fetch the third page must keep the
      // two it has and say what went wrong beside them.
      if (emptyEl) emptyEl.hidden = true;
      if (moreEl) moreEl.hidden = true;
      say(errorEl, answer.error.message);
      return;
    }
    say(errorEl, "");
    const body = answer.result;
    const items = Array.isArray(body.items) ? body.items : [];
    for (const document of items) {
      listEl.append(
        row(doc, {
          label: document.title || document.path || "Untitled",
          key: document.path ?? "",
          // A document need not have a kind, and an empty cell reads
          // better than the word "none", which would look like a kind
          // called "none".
          count: document.kind ?? "",
          href: docURL(slug, document.path ?? ""),
        }),
      );
    }
    rendered += items.length;
    emptyOrRows(listEl, emptyEl, rendered);
    // The listing issues a cursor whenever a page came back full, so the
    // page that reports the end is the empty one after the last row.
    cursor = nextCursorOf(body);
    if (moreEl) {
      moreEl.hidden = cursor === null || rendered === 0;
      moreEl.disabled = false;
    }
  }

  async function load(role) {
    fillState(doc, "docs-empty", {
      heading: NO_PROSE_HEADING,
      sentence: NO_PROSE_SENTENCE + " " + whoWrites(role, WRITES_DOCUMENTS),
    });
    // The vocabulary first, because it describes the list below it and a
    // reader scanning down should meet it before the rows. A failed
    // request leaves the line hidden rather than showing a wrong
    // vocabulary: the documents are the lane's subject and a missing
    // summary above them is a smaller lie than a stale one.
    const kinds = await client.docKinds();
    say(kindsEl, kinds.ok ? describeDocKinds(kinds.result) : "");
    const written = kinds.ok ? countDocuments(kinds.result) : 0;
    cursor = null;
    rendered = 0;
    if (listEl) listEl.replaceChildren();
    if (moreEl && !wired) {
      // The handler returns the promise rather than discarding it: a
      // browser ignores the return value, and the Node harness awaits
      // it, which is what lets a test press this button and then assert
      // what the next page rendered.
      moreEl.addEventListener("click", () => page());
      wired = true;
    }
    await page();
    return written;
  }

  return { load };
}

// The shell this module drives, and the one it does not: game.html has a
// #game-name and index.html does not, which is how one module can be
// imported by a harness without driving a page it was not given.
if (globalThis.document && globalThis.document.getElementById("game-name")) {
  const opened = await openGame();
  if (opened !== null) {
    await home(opened);
  }
}
