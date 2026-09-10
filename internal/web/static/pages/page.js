// The plumbing every page module shares: where this page is, where the
// other pages are, and the three things a page does before it can render
// anything.
//
// **It is a file Task 15 did not name, and the argument for it is the
// standing failure pattern this plan keeps catching.** Seven page
// modules each need to read the slug out of the URL, build a link to
// another page of the same game, and decide what to do with a 401. Seven
// copies of `/^\/g\/([^/]+)/` is seven chances to disagree about what an
// address is, and the whole product has just moved to slug addressing —
// the one decision a seventh copy would quietly break.
//
// **Every address in this front end is built here, and every one of them
// is a slug.** internal/web/static_pages_test.go's
// TestNoPageURLContainsAUUID reads the page modules for a uuid in a path
// position and finds none, and it can only mean that while the paths are
// built in one place a reader can check.
//
// It fetches nothing. Every call goes through internal/web/static/client.js,
// which is the rule internal/web/static_client_test.go holds; what this
// module owns is *where a page is*, not what a page knows.

import { fetchGames, goToLogin, rememberGame, renderHeader } from "../app.js";
import { client } from "../client.js";

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
export const SEGMENT_DOC = "/doc";
export const SEGMENT_ASSETS = "/assets";

// The three destinations, in the order the home shows them and in the
// order every page's own navigation shows them. One list, so a lane
// added to the home cannot be missing from the strip on every other
// page — which is exactly how a fourth destination would arrive
// half-built.
export const DESTINATION_VIEWS = "Views";
export const DESTINATION_CATALOGUE = "Catalogue";
export const DESTINATION_PROSE = "Prose";
// Images is a destination because it is a screen. It was not one, and
// /g/{slug}/assets therefore had no place of its own to mark: it marked
// **Views**, so a reader saw the wrong tab in bold and a screen reader
// was told the wrong location (found in the 2026-09-09 audit). A page in
// the product with no entry in the bar is a page the bar has to lie
// about.
//
// Analysis is deliberately *not* here yet, though the design settled it
// as the fifth. Its screens do not exist, and a destination pointing at
// nothing is worse than one that is missing: it lands with them.
export const DESTINATION_IMAGES = "Images";
export const DESTINATIONS = [DESTINATION_VIEWS, DESTINATION_CATALOGUE, DESTINATION_PROSE];

// The sentence a game with no views at all reads, and the one piece of
// onboarding in this product (the plan's O1).
//
// **It is a sentence and a link and never a button.** Nothing in this
// interface writes a view — a saved query is an agent's job over MCP,
// taught by the skill bundle — so a *New view* control here would lead
// nowhere, and a page offering an action it cannot perform is the defect
// this whole sub-project is written against.
// The heading is the fact, and it is short because it is read first and
// often alone. The sentence beneath it is the explanation, and it keeps
// the wording it has always had.
export const NO_VIEWS_HEADING = "No saved views yet";
export const NO_VIEWS_SENTENCE =
  "A view is a saved query plus a renderer, written by an agent over MCP.";
export const SKILL_BUNDLE_LABEL = "How an agent writes one";

// The one line in this front end that names an address outside this
// instance, and it is a **hyperlink a human may click** rather than
// anything this page loads.
//
// internal/web/static_vendor_test.go's outbound-URL scan exists so that
// a Maestro on a private network with no outbound route renders
// completely, and this line does not touch that: nothing fetches it, the
// page is whole without it, and a designer on an air-gapped instance
// simply has documentation they cannot reach — the same position they
// are in with any external reference. It is exempted there by an exact
// one-declaration rule, in Task 7's SVG-namespace shape and for the same
// reason: a blanket "any documentation URL" exemption would be a hole,
// and refusing this one outright would leave the product's single piece
// of onboarding pointing at a path this instance does not serve, which
// is the *New view* button that leads nowhere wearing a different hat.
export const SKILL_BUNDLE_HREF = "https://neverbot.github.io/maestro/agents/views";

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
//
// Every one of these takes the game's **slug** and the row's **key**,
// and none of them can be handed an id: there is no parameter for one.
// That is deliberate — an address the product could not spell wrongly is
// better than one it is merely careful with.

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

// --- Starting a page -------------------------------------------------

// openGame is the first thing every page module does: it resolves the
// slug in the URL against the games this caller can actually reach, and
// hands back the game's own row and a data client bound to it.
//
// The list is still fetched and it is not fetched for an id. What it
// supplies is the game's **name** for the heading and the fact that the
// slug reaches a game at all — which is what tells "not found" apart
// from a blank page. The slug that goes into every subsequent call is
// the game's *stored* one and never the URL's own casing.
export async function openGame(options = {}) {
  const doc = options.document || globalThis.document;
  const url = options.location || globalThis.window.location;
  const slug = slugOf(url.pathname);
  const answer = await fetchGames();
  if (!answer.ok) {
    if (answer.expired) {
      goToLogin();
      return null;
    }
    // The header still goes up on a failed list — with no switcher,
    // because there is no list to switch through — so a page that could
    // not load is still a page a person can sign out of.
    renderHeader();
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
  //
  // `game` may be null — a slug that reaches nothing. The switcher then
  // says "Games" instead of naming one, which is exactly the state that
  // most needs a way out.
  // The nav is built here and handed to the header, so the bar a person
  // sees is one element in one order on every screen. `options.current`
  // is the destination this page belongs to; a page that passes none
  // gets the bar with nothing marked, which is honest, rather than the
  // bar marking a page it is not on.
  const nav = game === null ? null : destinations(doc, game.slug, options.destination || null);
  renderHeader({
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
//
// **A page does not invent a negative state.** Task 4 exists so the
// empty answer, the refusal and the reassurance are decided once, and
// `mst-view-frame` is where a *view's* three negative states live. The
// three helpers below are the same decision for the pages that are not
// views — a catalogue, a listing, an entity — and they are here for the
// same reason: one place, so seven pages cannot each style a failure
// slightly differently.
//
// The server's sentence is carried across unmodified in every one of
// them. None of these functions composes a word; the words are the
// caller's, from the game or from the refusal.

// say writes text into an element and reveals it. textContent, never
// markup: every string a page renders is a designer's or an agent's.
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
  return nav;
}

// --- The breadcrumb ---------------------------------------------------

// The trail from the game to this page, and **the one component that
// replaced four different back links**. The 2026-09-09 audit found "Back
// to this game", "Back to the game", "Back to the catalogue" and "Back to
// this type" on five screens: two spellings of one destination, and not
// one of them saying where the reader currently was.
//
// A crumb with an href is a place to go; the last crumb has none and is
// where you are. Every string goes in through textContent, and every
// address comes from the functions above, so a crumb can only ever point
// at a slug address.
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
//
// It appends the nodes rather than building a nav and moving its
// childNodes across: `childNodes` is a live NodeList the harness's DOM
// stub does not model, and a product that reaches for a DOM API only a
// browser has is a product one half of its tests cannot drive.
export function setBreadcrumb(doc, trail) {
  const host = doc.getElementById("crumbs");
  if (!host) return null;
  host.replaceChildren(...crumbNodes(doc, trail));
  return host;
}

// --- The rows the catalogues are made of ------------------------------

// countLabel spells a count with the right noun, so "1 entities" never
// reaches a designer's screen.
export function countLabel(count, singular, plural) {
  return `${count} ${count === 1 ? singular : plural}`;
}

// row builds one line of a catalogue: a label, a key in mono, a count,
// and the one flag that ever asks a designer to do something.
//
// **Every string on it goes in through textContent**, without exception:
// a label is a designer's own words, a key is what an agent sent, and
// neither is markup this product ever interprets. `href` turns the label
// into a link and is built by the address functions above, so a row can
// only ever point at a slug address.
export function row(doc, spec) {
  const item = doc.createElement("li");

  const label = doc.createElement(spec.href ? "a" : "span");
  label.className = "catalogue-label";
  if (spec.href) label.href = spec.href;
  label.textContent = spec.label ?? "";
  item.append(label);

  const handle = doc.createElement("code");
  handle.className = "catalogue-key";
  handle.textContent = spec.key ?? "";
  item.append(handle);

  const tally = doc.createElement("span");
  tally.className = "catalogue-count";
  tally.textContent = spec.count ?? "";
  item.append(tally);

  if (spec.flag) {
    const flag = doc.createElement("span");
    flag.className = "catalogue-invalid";
    flag.textContent = spec.flag;
    item.append(flag);
  }
  return item;
}

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
//
// A viewer's write is refused by internal/web/server.go's
// registerContentRoute on every content route, so telling a viewer to do
// it would be promising an action this instance will not perform — worse
// than saying nothing. Anything that is not "viewer" gets the editor's
// sentence: viewer is the only role those routes refuse, so it is the
// only one whose reader has to be told something different.
export const ROLE_VIEWER = "viewer";

export function whoWrites(role, what) {
  if (role === ROLE_VIEWER) {
    return (
      "Your role in this game is viewer, so this instance will refuse a write from you: " +
      `an editor, an admin or the owner ${what}.`
    );
  }
  return (
    "You do it through this instance's API, either from an agent over MCP or " +
    "over the game's content routes. Nothing on this page does it for you."
  );
}

// --- The three negative states, in one shape --------------------------

// A screen with nothing on it is in one of exactly three states, and the
// 2026-09-09 audit found four different shapes for the first of them on a
// single screen: a filled box with a coloured left stripe, two bare grey
// sentences, and a nine-line paragraph explaining the MCP API in a 200px
// lane. Three of the four said the same thing in a different voice.
//
// One shape, three flavours, stated once:
//
//   - **empty**    nothing is here, and here is who would put it there
//   - **loading**  the answer has not come back yet
//   - **refused**  it came back and it was a refusal
//
// The heading is the fact, in ink. The sentence beneath it is the
// explanation, muted, and there is at most one link. It never teaches an
// API: the person reading it does not have one.
export const STATE_EMPTY = "empty";
export const STATE_LOADING = "loading";
export const STATE_REFUSED = "refused";

export function negativeState(doc, spec) {
  const root = doc.createElement("div");
  root.className = spec.kind === STATE_REFUSED ? "state refused" : "state";

  const heading = doc.createElement("b");
  heading.textContent = spec.heading ?? "";
  root.append(heading);

  if (spec.sentence) {
    const sentence = doc.createElement("span");
    sentence.textContent = spec.sentence;
    root.append(sentence);
  }

  // At most one, and only where there is somewhere useful to go. A state
  // with two actions is a state that has become a form.
  if (spec.action && spec.action.href) {
    const link = doc.createElement("a");
    link.href = spec.action.href;
    link.textContent = spec.action.label ?? "";
    root.append(link);
  }

  return root;
}

// onboarding is the no-views state, and it is now the shared shape with
// the product's one piece of onboarding in it rather than a component of
// its own. It kept its name and its sentence; what it lost is the box and
// the 3px coloured left stripe, which the identity bans by name.
export function onboarding(doc) {
  return negativeState(doc, {
    kind: STATE_EMPTY,
    heading: NO_VIEWS_HEADING,
    sentence: NO_VIEWS_SENTENCE,
    action: { href: SKILL_BUNDLE_HREF, label: SKILL_BUNDLE_LABEL },
  });
}
