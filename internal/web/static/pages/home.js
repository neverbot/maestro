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
  assetsURL,
  countLabel,
  docURL,
  emptyOrRows,
  expired,
  fill,
  onboarding,
  openGame,
  row,
  say,
  setReadOnly,
  typeURL,
  typesURL,
  viewURL,
  viewsURL,
  whoWrites,
} from "./page.js";
import { goToLogin } from "../app.js";

// What the two role-dependent empty states are about, in the words the
// sentence needs. They are arguments to one function rather than two
// functions, because "who may do this" is one rule with two subjects.
export const DECLARES_TYPES = "declares them";
export const WRITES_DOCUMENTS = "writes them";

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
  await viewsLane(doc, slug, client);
  setReadOnly(doc, summary.role, "writes this game's content");
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
  say(doc.getElementById("types-empty-action"), whoWrites(summary.role, DECLARES_TYPES));

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
      }),
    ),
  );
  return { role: String(summary.role ?? ""), ok: true };
}

// viewsLane lists the saved views, and answers a game that has none with
// the product's one piece of onboarding.
async function viewsLane(doc, slug, client) {
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
    onboardingEl.replaceChildren(...(items.length === 0 ? [onboarding(doc)] : []));
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
  const actionEl = doc.getElementById("docs-empty-action");

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
    cursor = typeof body.next_cursor === "string" ? body.next_cursor : null;
    if (moreEl) {
      moreEl.hidden = cursor === null;
      moreEl.disabled = false;
    }
  }

  async function load(role) {
    say(actionEl, whoWrites(role, WRITES_DOCUMENTS));
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
