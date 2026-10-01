// The plumbing every page module shares: where this page is, where the
// other pages are, and the three things a page does before it can render
// anything.

import { fetchGames, fetchMe, goToLogin, rememberGame, renderHeader } from "../app.js";
// Imported for its side effect, which is the custom element's
// definition: every screen inside a game builds a read-only notice
// through this module, so the element is defined wherever the notice can
// appear rather than by each page remembering to ask for it.
import "../components/mst-hint.js";
import { REREAD_DEBOUNCE_MS, client } from "../client.js";
// countLabel is *imported* here and not only re-exported below: a
// re-export forwards a name to this module's consumers and never binds
// it in this module's own scope, so expiry() calling it threw
// `countLabel is not defined` on every invitation ever listed — on this
// screen and on the instance's admin screen, which reads expiry from
// here too.
import { countLabel, markTables } from "../rows.js";
import { STATE_EMPTY, negativeState } from "../state.js";

// The route prefix of one game, and the segments under it. They are
// constants rather than spellings at each call site because
// internal/web/server.go registers exactly these and a page linking to a
// segment the server does not serve is a 404 nobody tested.
export const GAME_PREFIX = "/g/";
export const SEGMENT_VIEWS = "/views";
export const SEGMENT_VIEW = "/v/";
export const SEGMENT_TYPES = "/types";
export const SEGMENT_TYPE = "/t/";
export const SEGMENT_ENTITY = "/e/";
export const SEGMENT_RELATION_TYPE = "/rt/";
export const SEGMENT_DOC = "/doc";
export const SEGMENT_ASSETS = "/assets";
export const SEGMENT_ANALYSIS = "/analysis";
export const SEGMENT_SETTINGS = "/settings";
export const SEGMENT_ROUTES = "/analysis/routes";

// The three destinations, in the order the home shows them and in the
// order every page's own navigation shows them. One list, so a lane
// added to the home cannot be missing from the strip on every other
// page — which is exactly how a fourth destination would arrive
// half-built.
export const DESTINATION_VIEWS = "Overviews";
export const DESTINATION_CATALOGUE = "Content";
export const DESTINATION_PROSE = "Writing";
// Images is a destination because it is a screen. It was not one, and
// /g/{slug}/assets therefore had no place of its own to mark: it marked
// **Views**, so a reader saw the wrong tab in bold and a screen reader
// was told the wrong location (found in the 2026-09-09 audit). A page in
// the product with no entry in the bar is a page the bar has to lie
// about.
export const DESTINATION_IMAGES = "Images";
// The fifth, and it waited for its screens. The frame settled five
// destinations and shipped four, because "a destination pointing at
// nothing is worse than one that is missing"; these are the screens it
// was waiting for.
export const DESTINATION_ANALYSIS = "Checks";
export const DESTINATIONS = [DESTINATION_CATALOGUE, DESTINATION_VIEWS, DESTINATION_PROSE];

// What a game with nothing saved to look at reads. It names the thing by
// what a reader would get out of it, never by how it is stored.
export const NO_VIEWS_HEADING = "Nothing saved to look at yet";
export const NO_VIEWS_SENTENCE =
  "An overview is a saved way of seeing part of this game: how its missions unlock each other, " +
  "where its places sit, what happens in what order.";
export const NO_VIEWS_COMPOSE =
  "You can put a simple one together here. Your assistant writes the ones that ask a harder " +
  "question.";
export const NO_VIEWS_VIEWER =
  "You can read this game but not change it, so somebody with an editor's or a manager's role " +
  "saves these.";
export const SKILL_BUNDLE_LABEL = "Connect an assistant";
export const COMPOSE_LABEL = "Put one together";

// The one line in this front end that names an address outside this
// instance, and it is a **hyperlink a human may click** rather than
// anything this page loads.
export const SKILL_BUNDLE_HREF = "https://github.com/neverbot/maestro#the-skill-bundle";

// The published documentation site, and the second address in this front
// end that leaves the instance.
export const DOCUMENTATION_HREF = "https://neverbot.github.io/maestro/";
export const DESTINATION_DOCUMENTATION = "Documentation";

// slugOf reads the game's slug out of a path. One reader, for the reason
// this file exists.
export function slugOf(pathname) {
  const path = typeof pathname === "string" ? pathname : "";
  if (!path.startsWith(GAME_PREFIX)) return null;
  const rest = path.slice(GAME_PREFIX.length);
  const cut = rest.indexOf("/");
  const slug = cut === -1 ? rest : rest.slice(0, cut);
  return slug === "" ? null : decodeURIComponent(slug);
}

// segmentsOf is everything after the slug, decoded. A page asking for
// "the key in the second segment" asks this rather than splitting the
// path again with its own idea of how many segments precede it.
export function segmentsOf(pathname) {
  const path = typeof pathname === "string" ? pathname : "";
  if (!path.startsWith(GAME_PREFIX)) return [];
  const rest = path.slice(GAME_PREFIX.length);
  const cut = rest.indexOf("/");
  if (cut === -1) return [];
  return rest
    .slice(cut + 1)
    .split("/")
    .filter((part) => part !== "")
    .map((part) => decodeURIComponent(part));
}

// --- Where the other pages are ---------------------------------------

export function gameURL(slug) {
  return GAME_PREFIX + encodeURIComponent(String(slug));
}

export function viewsURL(slug) {
  return gameURL(slug) + SEGMENT_VIEWS;
}

// viewURL carries the bound parameters, because a parameterised view is
// a link: client.js's writeParams is what spells them, so a link built
// here and a URL a designer edited in the address bar mean the same
// thing.
// builderURL is the query builder, which is the one thing a person can
// now do in this destination that the product's own documentation said
// they could not.
export function builderURL(slug) {
  return viewsURL(slug) + "/new";
}

export function viewURL(slug, key, query) {
  const suffix = typeof query === "string" && query !== "" ? "?" + query : "";
  return gameURL(slug) + SEGMENT_VIEW + encodeURIComponent(String(key)) + suffix;
}

export function typesURL(slug) {
  return gameURL(slug) + SEGMENT_TYPES;
}

export function typeURL(slug, typeKey) {
  return gameURL(slug) + SEGMENT_TYPE + encodeURIComponent(String(typeKey));
}

// relationTypeURL addresses a relation type's own page. `/rt/` and not
// `/t/`: the two vocabularies are separate namespaces in the metamodel —
// a game may declare an entity type and a relation type with the same
// key — so one segment for both would be an address that means two
// things.
export function relationTypeURL(slug, key) {
  return gameURL(slug) + SEGMENT_RELATION_TYPE + encodeURIComponent(String(key ?? ""));
}

export function entityURL(slug, typeKey, key) {
  return (
    gameURL(slug) +
    SEGMENT_ENTITY +
    encodeURIComponent(String(typeKey)) +
    "/" +
    encodeURIComponent(String(key))
  );
}

// docURL puts the document's path in the query string and never in a
// segment, which is internal/web/api_docs.go's own rule for the same
// address on the API side.
export function docURL(slug, path) {
  return gameURL(slug) + SEGMENT_DOC + "?path=" + encodeURIComponent(String(path ?? ""));
}

export function assetsURL(slug) {
  return gameURL(slug) + SEGMENT_ASSETS;
}

// A game's own settings. Not a destination in the header strip: the
// frame settled five destinations and this is not a sixth place to go
// and read, it is one owner's screen reached from the game it belongs
// to.
export function settingsURL(slug) {
  return gameURL(slug) + SEGMENT_SETTINGS;
}

// TAB_AGENTS is the fragment that opens the settings page's second tab.
export const TAB_AGENTS = "#agents";

export function analysisURL(slug) {
  return gameURL(slug) + SEGMENT_ANALYSIS;
}

export function routesURL(slug) {
  return gameURL(slug) + SEGMENT_ROUTES;
}

export function routeURL(slug, key) {
  return routesURL(slug) + "/" + encodeURIComponent(String(key ?? ""));
}

// --- Starting a page -------------------------------------------------

// openGame is the first thing every page module does: it resolves the
// slug in the URL against the games this caller can actually reach, and
// hands back the game's own row and a data client bound to it.
export async function openGame(options = {}) {
  const doc = options.document || globalThis.document;
  // Every catalogue on this page is a table, said once here rather than
  // beside each list: rows.js gives every row and every cell its ARIA
  // role, and a row role outside a table is worse than none. See
  // rows.js markTables.
  markTables(doc);
  const url = options.location || globalThis.window.location;
  const slug = slugOf(url.pathname);
  // Both answers before the header, because the bar carries both: the
  // switcher is the game list and the menu on the right is the person.
  const [who, answer] = await Promise.all([fetchMe(), fetchGames()]);
  const me = who.ok ? who.body : null;
  if (!answer.ok) {
    if (answer.expired) {
      goToLogin();
      return null;
    }
    // The header still goes up on a failed list — with no switcher,
    // because there is no list to switch through — so a page that could
    // not load is still a page a person can sign out of.
    renderHeader({ me });
    return { slug, game: null, client: null, failure: answer.message, document: doc, location: url };
  }
  const game = answer.games.find((row) => row.slug === slug) || null;
  // The header is rendered **here and not before the fetch**, which is
  // the whole of what makes its switcher possible: the switcher lists
  // the caller's games and marks the current one, and this is the first
  // moment either is known. The cost is that the wordmark and the
  // sign-out button appear one round trip later than they used to; the
  // alternative was a second GET /api/games on every page in the
  // product to fill in chrome the page had already paid for.
  const nav = game === null ? null : destinations(doc, game.slug, options.destination || null);
  renderHeader({
    me,
    games: answer.games,
    current: game,
    nav,
    // The same list the strip was built from, for the widths where the
    // strip cannot be shown.
    destinations: game === null ? null : destinationTargets(game.slug),
    destination: options.destination || null,
  });
  if (game === null) {
    return { slug, game: null, client: null, failure: null, document: doc, location: url };
  }
  // Recorded only here, inside the branch that has just confirmed this
  // slug is in the server's own list — never unconditionally from the
  // URL the moment a page loads. A stray or stale link (a bookmark to a
  // deleted game, a typo, a game the caller lost access to) must not
  // overwrite a good remembered value with one that cannot be reached.
  if (slug) rememberGame(slug);
  return {
    slug: game.slug,
    game,
    client: options.client || client({ slug: game.slug }),
    failure: null,
    document: doc,
    location: url,
  };
}

// expired says a refusal was a session that is no longer one. It reads
// the status the client carried across and never a sentence: a message
// is prose the server owns and a status is a fact.
export function expired(answer) {
  return Boolean(answer) && answer.ok === false && answer.error.status === 401;
}

// --- What a page says when there is nothing to show ------------------
export function say(el, text) {
  if (!el) return null;
  el.textContent = text;
  el.hidden = text === "";
  return el;
}

// fail puts a refusal where the page's own content would have gone and
// hides the content, which is the distinction that matters: an empty
// list and a request that never answered look identical on a page that
// renders a plausible blank, and only one of them means "there is
// nothing here".
export function fail(errorEl, contentEl, message) {
  if (contentEl) contentEl.hidden = true;
  return say(errorEl, message);
}

// emptyOrRows hides the empty state when there are rows and shows it
// when there are none, and refuses to be asked about a list that failed
// — that is `fail`'s answer and never this one's.
export function emptyOrRows(listEl, emptyEl, count) {
  if (listEl) listEl.hidden = count === 0;
  if (emptyEl) emptyEl.hidden = count > 0;
  return count;
}

// --- The strip every page carries ------------------------------------

// destinationStrip is the three destinations as links, so a designer
// holds the whole map of this tool in their head from any page in it.
// The current one is marked with `aria-current` rather than removed: a
// strip that dropped the page you are on changes shape as you navigate,
// which is the one thing a persistent strip must not do.
// The four destinations as data, so the two places that show them build
// from one list rather than one of them reading the other back out of the
// DOM. The switcher's narrow-width copies were built by calling
// querySelectorAll on the rendered nav, which is an API the harness's DOM
// stub does not model — a product that reaches for a browser-only method
// is a product half its tests cannot drive, and this is the second time
// in one design pass that exact mistake landed.
export function destinationTargets(slug) {
  return [
    [DESTINATION_VIEWS, viewsURL(slug)],
    [DESTINATION_CATALOGUE, typesURL(slug)],
    // Prose has no listing page of its own: the home's third lane is the
    // whole of it, so this link is the home anchored at that lane.
    [DESTINATION_PROSE, gameURL(slug) + "#prose"],
    [DESTINATION_IMAGES, assetsURL(slug)],
    [DESTINATION_ANALYSIS, analysisURL(slug)],
  ];
}

export function destinations(doc, slug, current) {
  const nav = doc.createElement("nav");
  nav.className = "destinations";
  // Two navigations on most screens, and only one of them was named.
  nav.setAttribute("aria-label", "Sections of this game");
  // Prose has no listing page of its own: the home's third lane is the
  // whole of it, so this link is the home anchored at that lane rather
  // than a fourth shell nobody would have anything else to put on.
  for (const [label, href] of destinationTargets(slug)) {
    const link = doc.createElement("a");
    link.href = href;
    link.textContent = label;
    if (label === current) link.setAttribute("aria-current", "page");
    nav.append(link);
  }
  nav.append(documentationLink(doc));
  return nav;
}

// documentationLink is the last item in the strip and the only one that
// leaves this instance, so it says so twice: `target="_blank"` opens it
// beside the work rather than over it, and the class marks it for the
// stylesheet, which draws the arrow every convention uses for a link
// that goes outside.
export function documentationLink(doc) {
  const link = doc.createElement("a");
  link.href = DOCUMENTATION_HREF;
  link.textContent = DESTINATION_DOCUMENTATION;
  link.className = "leaves";
  link.target = "_blank";
  link.setAttribute("rel", "noopener");
  return link;
}

// --- The breadcrumb ---------------------------------------------------

// The trail from the game to this page, and **the one component that
// replaced four different back links**. The 2026-09-09 audit found "Back
// to this game", "Back to the game", "Back to the catalogue" and "Back to
// this type" on five screens: two spellings of one destination, and not
// one of them saying where the reader currently was.
export function crumbNodes(doc, trail) {
  const nodes = [];
  trail.forEach((crumb, index) => {
    if (index > 0) {
      const sep = doc.createElement("span");
      sep.className = "crumb-sep";
      sep.setAttribute("aria-hidden", "true");
      sep.textContent = "/";
      nodes.push(sep);
    }
    const last = index === trail.length - 1;
    const node = doc.createElement(crumb.href && !last ? "a" : "b");
    if (crumb.href && !last) node.href = crumb.href;
    if (last) node.setAttribute("aria-current", "page");
    node.textContent = crumb.label ?? "";
    nodes.push(node);
  });
  return nodes;
}

// setBreadcrumb fills the shell's own placeholder rather than prepending
// a second one, so a page that renders twice — every page that names a
// thing it had to fetch — does not stack two trails.
export function setBreadcrumb(doc, trail) {
  const host = doc.getElementById("crumbs");
  if (!host) return null;
  host.replaceChildren(...crumbNodes(doc, trail));
  return host;
}

// --- The rows the catalogues are made of ------------------------------

// countLabel spells a count with the right noun, so "1 entities" never
// reaches a designer's screen.
// countLabel moved to ../rows.js, for the reason `row` did: app.js needs
// it and page.js imports app.js. Re-exported so its seven callers here
// are unchanged.

// row and fill live in ../rows.js now and are re-exported here so the
// seven page modules that import them from this file keep working. They
// moved because **app.js needs a row too** — the picker was building its
// own — and app.js is the module this one imports from, so importing it
// back would close a cycle. A vocabulary two modules share belongs to
// neither.
export { countLabel, markTable, markTables, row } from "../rows.js";

// fill replaces a list's rows and shows its empty state when there are
// none. replaceChildren, never innerHTML, so a re-render can neither
// double a catalogue nor interpret one.
export function fill(listEl, emptyEl, rows) {
  if (!listEl) return 0;
  listEl.replaceChildren(...rows);
  return emptyOrRows(listEl, emptyEl, rows.length);
}

// whoWrites is the second half of an empty state, and it is the half
// that depends on who is reading.
export const ROLE_VIEWER = "viewer";

export function whoWrites(role, what) {
  if (role === ROLE_VIEWER) {
    return (
      "You can read this game but not change it, so somebody with an editor's or a manager's " +
      `role ${what}.`
    );
  }
  // Who does it, not over what transport. The reader is a game designer
  // and the sentence used to send them to an API they do not have.
  return `Your assistant ${what}.`;
}

// --- A secret shown once, and when an invitation stops working --------
// copyLine is one string a reader is meant to take away, with the way to
// take it beside it. `label` names what is being copied, because "Copy"
// alone on a page with two of them says nothing.
export function copyLine(doc, text, label) {
  const line = doc.createElement("div");
  line.className = "secret-line";

  const code = doc.createElement("code");
  code.className = "mono";
  code.textContent = text;
  line.append(code);

  const copy = doc.createElement("button");
  copy.type = "button";
  copy.className = "ghost";
  copy.textContent = label;
  copy.addEventListener("click", async () => {
    try {
      await globalThis.navigator.clipboard.writeText(text);
      copy.textContent = "Copied";
    } catch {
      copy.textContent = "Select it and copy";
    }
  });
  line.append(copy);
  return line;
}

export function inviteLink(doc, href) {
  const line = doc.createElement("div");
  line.className = "secret-line";

  const code = doc.createElement("code");
  code.className = "mono";
  code.textContent = href;
  line.append(code);

  const copy = doc.createElement("button");
  copy.type = "button";
  copy.className = "ghost";
  copy.textContent = "Copy the link";
  copy.addEventListener("click", async () => {
    try {
      await globalThis.navigator.clipboard.writeText(href);
      copy.textContent = "Copied";
    } catch {
      copy.textContent = "Select it and copy";
    }
  });
  line.append(copy);
  return line;
}

// When it stops working, in the words a person would use. An invitation
// that has expired is still listed by the server until it is pruned, and
// "expired" is a different fact from "expires in three days".
export function expiry(value) {
  const when = new Date(String(value ?? ""));
  if (Number.isNaN(when.getTime())) return "no expiry recorded";
  const days = Math.round((when.getTime() - Date.now()) / 86400000);
  if (days < 0) return "expired";
  if (days === 0) return "expires today";
  return "expires in " + countLabel(days, "day", "days");
}
// --- The read-only notice ---------------------------------------------

// **What the product cannot do, written where somebody would look for
// the button.** The identity has specified this notice since the design
// pass and no screen carried it: the only place the product admitted it
// is read-only was inside an empty state, so the fuller a game was, the
// less its screens said about what they will not let you do. Two
// separate reviews found it, the second exactly that way round.
export const READ_ONLY_LABEL = "Read-only";

export function readOnlyNotice(doc, role, what) {
  const note = doc.createElement("span");
  note.className = "read-only";
  note.textContent = READ_ONLY_LABEL;
  // The detail is a hint rather than a second line: the head is a row a
  // page title shares, and a sentence there would push the content down
  // on every screen to say a thing that is true of all of them.
  const hint = doc.createElement("mst-hint");
  hint.setAttribute("text", whoWrites(role, what));
  // The notice sits at the right edge of the page head, so the panel
  // hangs from its right edge rather than off the page.
  hint.setAttribute("align", "end");
  hint.append(note);
  return hint;
}

// setReadOnly puts the notice in the shell's page head, which every
// screen inside a game has. A shell with no head element gets nothing
// rather than a notice prepended somewhere arbitrary.
export function setReadOnly(doc, role, what) {
  const host = doc.getElementById("page-actions");
  if (!host) return null;
  host.replaceChildren(readOnlyNotice(doc, role, what));
  return host;
}

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

// --- Re-reading on the stream -----------------------------------------

// coalesce turns a burst of events into one re-read. A bulk write emits
// one event per row, and three hundred of them would otherwise be three
// hundred listings fetched. The window is the client's own
// REREAD_DEBOUNCE_MS, so the whole product waits the same amount.
let rereadWindow = REREAD_DEBOUNCE_MS;

// setRereadWindow exists for a harness, which cannot wait three quarters
// of a second per event and must still drive the real path.
export function setRereadWindow(ms) {
  rereadWindow = Number(ms);
}

export function coalesce(run) {
  let timer = null;
  let pending = null;
  return () => {
    if (pending === null) {
      let settle = () => {};
      const promise = new Promise((resolve) => {
        settle = resolve;
      });
      pending = { promise, settle };
    }
    const current = pending;
    if (timer !== null) clearTimeout(timer);
    timer = setTimeout(async () => {
      timer = null;
      pending = null;
      try {
        await run();
      } finally {
        current.settle();
      }
    }, rereadWindow);
    // Returned so a caller can await the re-read this burst produces. A
    // browser discards it; a harness awaits it.
    return current.promise;
  };
}

// --- The three negative states, in one shape --------------------------

// Stated in state.js, which imports nothing, and re-exported here so
// every page keeps importing its negative states from the page module.
export { STATE_EMPTY, STATE_LOADING, STATE_REFUSED, negativeState, fillState } from "../state.js";

// onboarding is the no-views state, and it is now the shared shape with
// the product's one piece of onboarding in it rather than a component of
// its own. It kept its name; what it lost is the box and the 3px
// coloured left stripe, which the identity bans by name.
// `hasContent` is whether this game holds anything to look at yet. It
// decides the way out: a game with nothing in it needs an assistant
// connected, and a game already full of missions needs the way to make
// the first overview. Offering the first to somebody whose assistant is
// already writing is the product telling them to do what they have
// done.
export function onboarding(doc, role, slug, hasContent) {
  const viewer = role === ROLE_VIEWER;
  const way = hasContent
    ? { href: builderURL(slug), label: COMPOSE_LABEL }
    // Inside the product, never out to a code forge. This pointed at a
    // README on GitHub, which is where the one person this product is
    // for stops reading.
    : { href: settingsURL(slug) + TAB_AGENTS, label: SKILL_BUNDLE_LABEL };
  return negativeState(doc, {
    kind: STATE_EMPTY,
    heading: NO_VIEWS_HEADING,
    sentence: NO_VIEWS_SENTENCE + " " + (viewer ? NO_VIEWS_VIEWER : NO_VIEWS_COMPOSE),
    action: viewer || !slug ? null : way,
  });
}
