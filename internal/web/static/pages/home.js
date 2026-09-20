// The game home: three lanes, **Views · Catalogue · Prose**, in that
// order.
//
// The metamodel has four primitives and the product has three surfaces
// over them — the pictures a designer reads, the vocabulary the game
// declared, and the prose beside it — so this page is the map of the
// tool and not a dashboard. A designer who has seen it once knows where
// everything in Maestro lives.
//
// **The catalogue lane costs one call.** GET /summary answers with one
// row per declared type and never a row of content, so a game holding
// four hundred thousand entities renders exactly as fast, and as small,
// as one holding four. `theHomeMakesOneSummaryCall` is what stops the
// obvious future edit — a count per type, fetched per type — from
// arriving unnoticed.
//
// **A game with no views gets a sentence and a link, and no button.**
// Nothing in this interface writes a view; a saved query is an agent's
// job over MCP. A *New view* control here would lead nowhere, which is
// the plan's O1 and the one piece of onboarding this product ships.
//
// It fetches nothing itself: every call goes through client.js.

import {
  REREAD,
  TARGET_PROSE,
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
import { nextCursorOf } from "../rows.js";
import { goToLogin } from "../app.js";

// What the two role-dependent empty states are about, in the words the
// sentence needs. They are arguments to one function rather than two
// functions, because "who may do this" is one rule with two subjects.
export const DECLARES_TYPES = "declares them";
export const WRITES_DOCUMENTS = "writes them";

// The three negative states the home's lanes can be in. They live here
// rather than in game.html, and pages/types.js reads the first two from
// here rather than keeping a second wording of the same state: the
// catalogue destination shows the same two catalogues this lane does,
// and two wordings of one state is one of them going stale.
//
// The first and the third end with whoWrites at the call site, because
// their last sentence depends on who is reading and a viewer must not be
// told to do what the server will refuse.
export const NO_TYPES_HEADING = "No types yet";
export const NO_TYPES_SENTENCE =
  "A game declares its own — Quest, Zone and Class for one game, Driver, Car and Circuit for " +
  "another.";
export const NO_RELATION_TYPES_HEADING = "No connections yet";
export const NO_RELATION_TYPES_SENTENCE =
  "A relation type is a kind of edge between entities — takes_place_in, requires, unlocks — and " +
  "is declared the same way an entity type is.";
export const NO_PROSE_HEADING = "No prose yet";
export const NO_PROSE_SENTENCE =
  "A document is writing addressed by a path inside this game, such as lore/duskwood, kept " +
  "version by version.";

// describeTotals is the one line under the game's name. An empty game
// says so in words rather than showing three zeros, which reads as a
// broken page rather than a new one.
export function describeTotals(totals) {
  const counts = totals && typeof totals === "object" ? totals : {};
  const entities = Number(counts.entities ?? 0);
  const relations = Number(counts.relations ?? 0);
  const invalid = Number(counts.invalid ?? 0);
  if (entities === 0 && relations === 0) return "No content yet.";
  const parts = [countLabel(entities, "entity", "entities"), countLabel(relations, "relation", "relations")];
  // Only when there are any: a permanent "0 no longer fit" would train a
  // designer to ignore the one number on this page that ever asks them
  // to do something.
  if (invalid > 0) parts.push(`${invalid} no longer fit their type`);
  return parts.join(" · ");
}

// describeDocKinds turns GET /docs/kinds into the one line above the
// documents. A kind is free text this game invented and Maestro ships no
// vocabulary of them, so this line is the only place a designer sees
// which kinds their own prose is using. It returns the empty string for
// a game with none, and the caller hides the line rather than printing
// "no kinds", which reads as a fault on a page whose documents are
// underneath it.
export function describeDocKinds(body) {
  const from = body && typeof body === "object" ? body : {};
  const kinds = Array.isArray(from.kinds) ? from.kinds : [];
  if (kinds.length === 0) return "";
  const parts = kinds.map((k) => `${k.kind} ${Number(k.document_count ?? 0)}`);
  let line = `Kinds: ${parts.join(" · ")}`;
  const unkinded = Number(from.unkinded ?? 0);
  if (unkinded > 0) line += ` · ${unkinded} with no kind`;
  return line;
}

// describeView is the second column of a views row: what it draws and
// whether the game has moved under it. `stale` is the server's own flag
// and is the only thing on this lane that ever asks for attention.
export function describeView(view) {
  const from = view && typeof view === "object" ? view : {};
  return String(from.renderer ?? "");
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
  const summary = await catalogueLane(doc, slug, client, summaryEl);
  // A summary that never answered is the page's failure and not one
  // lane's: the totals line carries the server's sentence and the lanes
  // stay hidden, because two empty catalogues under a message read as a
  // game with nothing in it.
  if (!summary.ok) return { ...opened, onEvent: null };
  await viewsLane(doc, slug, client, summary.role);
  setReadOnly(doc, summary.role, "writes this game's content");
  // **The way into the one screen that changes a game rather than its
  // content**, and it is here because the frame's five destinations are
  // places to go and read: settings is one person's screen, reached
  // from the game it belongs to. Only an owner sees it, because only an
  // owner can save anything there.
  offerSettings(doc, slug, summary.role);
  await prose.load(summary.role);

  // The stream, last: the page has just read everything, so the first
  // connection schedules nothing and only a later event asks for a
  // re-read.
  //
  // **A moved document is followed and not cached.** `document.moved`
  // carries both spellings of the path and the client answers it with a
  // re-read of the prose surface; a lane that kept the old path would
  // link a designer at a document that is no longer there.
  const onEvent = (verdict) => {
    if (verdict.decision === REREAD && verdict.target === TARGET_PROSE) {
      return prose.load(summary.role);
    }
    return null;
  };
  client.connect(onEvent);

  const content = doc.getElementById("home");
  // Revealed only now, with every lane already filled, so the page never
  // flashes three empty lists on its way to the real ones.
  if (content) content.hidden = false;
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
async function catalogueLane(doc, slug, client, summaryEl) {
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
    say(summaryEl, answer.error.message);
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
  say(summaryEl, describeTotals(summary.totals));
  offerToConnectAnAgent(doc, slug, summary);
  fillState(doc, "types-empty", {
    heading: NO_TYPES_HEADING,
    sentence: NO_TYPES_SENTENCE + " " + whoWrites(summary.role, DECLARES_TYPES),
  });
  fillState(doc, "relation-types-empty", {
    heading: NO_RELATION_TYPES_HEADING,
    sentence: NO_RELATION_TYPES_SENTENCE,
  });

  fill(
    doc.getElementById("types"),
    doc.getElementById("types-empty"),
    entityTypes.map((type) =>
      row(doc, {
        label: type.label_plural || type.label || type.key,
        key: type.key,
        count: countLabel(Number(type.entity_count ?? 0), "entity", "entities"),
        flag: Number(type.invalid_count ?? 0) > 0 ? `${Number(type.invalid_count)} invalid` : "",
        href: typeURL(slug, type.key),
      }),
    ),
  );
  fill(
    doc.getElementById("relation-types"),
    doc.getElementById("relation-types-empty"),
    relationTypes.map((type) =>
      row(doc, {
        label: type.label || type.key,
        key: type.key,
        count: countLabel(Number(type.relation_count ?? 0), "relation", "relations"),
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
  return { role: String(summary.role ?? ""), ok: true };
}

// SETTINGS_LABEL is the link, and every member of the game gets it.
//
// **"Game settings", not "Settings".** The link sits in a game's page
// head, one word from the game's own name, and a designer reading it
// there took it for the application's settings — the account, the
// instance, the theme. What it opens is this game's own screen, and
// nothing else in the product is called settings, so the word is free to
// say which.
//
// **It was the owner's alone until that screen gained a second tab.**
// The name and the address still are; the tokens beside them are any
// member's to see and to revoke, and an editor's to mint. A link shown
// only to owners would have hidden a credential an editor is allowed to
// create, which is the server's rule contradicted by the one thing that
// decides whether anybody ever finds it.
export const SETTINGS_LABEL = "Game settings";
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

export const CONNECT_HEADING = "Nothing here yet, and nothing on this page will change that";
export const CONNECT_SENTENCE =
  "A game's content is written by agents, over MCP. Give one a token for this game and it can " +
  "start declaring what this game is made of.";
export const CONNECT_LABEL = "Connect an agent";

// isEmptyGame reads the same totals the line under the title is built
// from, rather than counting the lanes: a game can declare types and
// hold no entities, and that is not an empty game — somebody has already
// been here.
export function isEmptyGame(summary) {
  const totals = summary && typeof summary.totals === "object" && summary.totals !== null
    ? summary.totals
    : {};
  const types = Array.isArray(summary?.entity_types) ? summary.entity_types.length : 0;
  const relationTypes = Array.isArray(summary?.relation_types) ? summary.relation_types.length : 0;
  return (
    Number(totals.entities ?? 0) === 0 &&
    Number(totals.relations ?? 0) === 0 &&
    types === 0 &&
    relationTypes === 0
  );
}

// offerToConnectAnAgent is the crossing, and it is offered **only to
// somebody who can make it**: minting is an editor's or the owner's, so
// telling a viewer to connect an agent would be sending them to a form
// the server refuses. A viewer of an empty game keeps the three lanes'
// own sentences, which are true for them.
export function offerToConnectAnAgent(doc, slug, summary) {
  const host = doc.getElementById("connect-agent");
  if (!host) return null;
  const role = String(summary?.role ?? "");
  const show = isEmptyGame(summary) && (role === OWNER || role === EDITOR);
  if (!show) {
    host.replaceChildren();
    host.hidden = true;
    return null;
  }
  const state = negativeState(doc, {
    kind: STATE_EMPTY,
    heading: CONNECT_HEADING,
    sentence: CONNECT_SENTENCE,
    action: { href: settingsURL(slug) + TAB_AGENTS, label: CONNECT_LABEL },
  });
  host.replaceChildren(state);
  host.hidden = false;
  return state;
}

// viewsLane lists the saved views, and answers a game that has none with
// the product's one piece of onboarding.
async function viewsLane(doc, slug, client, role) {
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
    onboardingEl.replaceChildren(...(items.length === 0 ? [onboarding(doc, role)] : []));
    onboardingEl.hidden = items.length > 0;
  }
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
