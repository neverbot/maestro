// The way out of a game.
//
// **The bug this file was written against.** Signing in landed a
// designer in the game this browser last visited and there was no way to
// reach another one. The account was a member of three games and the
// server said so on every page; nothing in the product listed them. Two
// halves closed the loop: the picker (app.js) redirected straight to the
// remembered game the moment it had one, and the only link out of a game
// was the wordmark, which pointed at "/" — the picker, which redirected
// straight back in. The picker's list worked perfectly and was
// unreachable.
//
// What is asserted here is the exit, and it is asserted **from inside a
// game**: a check that only proved the picker renders a list when
// nothing is remembered would have passed on the broken product, because
// that was already true. So the first check below loads the real game
// home at /g/{slug}, with a remembered game in localStorage, and reads
// the addresses a person could actually click off the rendered chrome.
//
// The remembered-game shortcut is *kept*, and the last two checks are
// what keeps it honest: it still fires at "/" (a designer with one game
// must not meet a one-row picker every morning) and it never fires at
// /games (which is why /games exists). Both properties in one file,
// because either one alone is satisfiable by deleting the other.
//
// Run directly: `node internal/web/jstest/game_switcher_test.mjs`.
// internal/web/static_appjs_browser_test.go shells out to it too.

const ORIGIN = "http://localhost:8125";

const AZEROTH = { id: "a1", slug: "azeroth", name: "Azeroth" };
const LE_MANS = { id: "b2", slug: "le-mans", name: "Le Mans" };
const HYRULE = { id: "c3", slug: "hyrule", name: "Hyrule" };
const ALL_GAMES = [AZEROTH, LE_MANS, HYRULE];

let failures = 0;
const pending = [];

function check(name, fn) {
  pending.push([name, fn]);
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function assertEqual(actual, expected, message) {
  if (actual !== expected) {
    throw new Error(`${message}: got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);
  }
}

// --- The DOM stub -----------------------------------------------------
//
// The same stub the page harness uses, cut to what these three modules
// touch. **Nothing in it can interpret a string as markup**: an element
// with an innerHTML setter would let a page render a game's name as
// markup and still pass here.
function fakeElement(tag = "div") {
  const el = {
    tagName: tag,
    className: "",
    textContent: "",
    href: "",
    open: false,
    hidden: false,
    children: [],
    attributes: new Map(),
    listeners: {},
    append(...nodes) {
      this.children.push(...nodes);
    },
    prepend(...nodes) {
      this.children.unshift(...nodes);
    },
    replaceChildren(...nodes) {
      this.children = [...nodes];
    },
    setAttribute(name, value) {
      this.attributes.set(name, String(value));
    },
    getAttribute(name) {
      return this.attributes.has(name) ? this.attributes.get(name) : null;
    },
    addEventListener(name, handler) {
      (this.listeners[name] ??= []).push(handler);
    },
    removeEventListener() {},
    querySelector() {
      return null;
    },
  };
  return el;
}

// links collects every anchor in a subtree, so an assertion about
// addressing reads what a person would click rather than what a function
// returned.
function links(node, found = []) {
  if (!node) return found;
  if (node.tagName === "a") found.push(node);
  for (const child of node.children ?? []) links(child, found);
  return found;
}

function findByClass(node, className, found = []) {
  if (!node) return found;
  if (node.className === className) found.push(node);
  for (const child of node.children ?? []) findByClass(child, className, found);
  return found;
}

// mount installs a document holding the ids named, a location at the
// path named, a localStorage that already remembers a game, and a fetch
// that answers from a routing table.
function mount({ ids, pathname, search = "", games = ALL_GAMES, routes = [], remembered = null }) {
  const elements = {};
  for (const id of ids) elements[id] = fakeElement();
  for (const id of ["games", "empty-state", "new-game", "home"]) {
    if (elements[id]) elements[id].hidden = true;
  }

  const body = fakeElement("body");
  globalThis.document = {
    title: "",
    body,
    hidden: false,
    createElement: (tag) => fakeElement(tag),
    createComment: () => ({}),
    createTreeWalker: () => ({ nextNode: () => null }),
    head: {},
    addEventListener() {},
    getElementById(id) {
      return Object.prototype.hasOwnProperty.call(elements, id) ? elements[id] : null;
    },
  };

  const navigations = [];
  const location = { hash: "", pathname, search, origin: ORIGIN };
  Object.defineProperty(location, "href", {
    enumerable: true,
    get() {
      return ORIGIN + pathname + search;
    },
    set(value) {
      navigations.push(value);
    },
  });
  globalThis.window = { location, addEventListener() {}, removeEventListener() {} };
  globalThis.history = { replaceState() {} };
  const stored = remembered === null ? {} : { "maestro:lastGame": remembered };
  globalThis.localStorage = {
    getItem(key) {
      return Object.prototype.hasOwnProperty.call(stored, key) ? stored[key] : null;
    },
    setItem(key, value) {
      stored[key] = value;
    },
  };

  const requested = [];
  globalThis.fetch = async (url, init) => {
    requested.push(String(url));
    if (url === "/api/games") {
      return { ok: true, status: 200, json: async () => ({ games }) };
    }
    for (const [match, answer] of routes) {
      if (!match(String(url))) continue;
      const value = typeof answer === "function" ? answer(String(url), init) : answer;
      if (value.stream) return { ok: true, status: 200 };
      return {
        ok: value.status === undefined || value.status === 200,
        status: value.status ?? 200,
        json: async () => value.body,
      };
    }
    return {
      ok: false,
      status: 404,
      json: async () => ({ error: "not_found", message: "unexpected fetch: " + url }),
    };
  };

  return { elements, requested, navigations, body, stored };
}

// load imports a module fresh: a module is evaluated once per URL, and
// every case here needs its own top-level run.
let serial = 0;
async function load(specifier) {
  serial += 1;
  return import(`${specifier}?case=${serial}`);
}

// --- Inside a game ----------------------------------------------------

const HOME_IDS = [
  "game-name",
  "game-summary",
  "home",
  "views",
  "views-error",
  "views-onboarding",
  "views-more",
  "types",
  "types-empty",
  "types-empty-action",
  "relation-types",
  "relation-types-empty",
  "docs",
  "doc-kinds",
  "docs-empty",
  "docs-empty-action",
  "docs-error",
  "docs-more",
];

const base = "/api/games/" + AZEROTH.slug;
const homeRoutes = [
  [(url) => url === base + "/summary", { body: { entity_types: [], relation_types: [], totals: {}, role: "editor" } }],
  [(url) => url === base + "/docs/kinds", { body: { kinds: [], unkinded: 0 } }],
  [(url) => url.startsWith(base + "/docs"), { body: { items: [] } }],
  [(url) => url.startsWith(base + "/views"), { body: { items: [] } }],
  [(url) => url === base + "/events", { stream: true }],
];

// **The check this file exists for.** A person inside one game can reach
// every other game they are a member of — and the remembered-game
// shortcut is still in place while they do it, which is the half that
// makes it a regression test for the real bug rather than for a
// simplification of it.
check("everyOtherGameIsReachableFromInsideOne", async () => {
  const dom = mount({
    ids: HOME_IDS,
    pathname: "/g/" + AZEROTH.slug,
    routes: homeRoutes,
    remembered: AZEROTH.slug,
  });
  await load("../static/pages/home.js");

  const reachable = new Set(links(dom.body).map((a) => a.href));
  for (const game of ALL_GAMES) {
    if (game.slug === AZEROTH.slug) continue;
    assert(
      reachable.has("/g/" + game.slug),
      `no address on this page reaches ${game.slug}, so a person inside ${AZEROTH.slug} cannot leave it: ` +
        JSON.stringify([...reachable]),
    );
  }
  // And the shortcut is untouched: the page still recorded the game it
  // is in, so the next visit to "/" still goes straight here.
  assertEqual(
    dom.stored["maestro:lastGame"],
    AZEROTH.slug,
    "the page stopped remembering the game it is in, which is the convenience this fix had to keep",
  );
});

// The chrome says which game you are in. Before the switcher, nothing
// did until the home page's own <h1> had loaded — and no other page of a
// game said it at all.
check("theChromeNamesTheGameYouAreIn", async () => {
  const dom = mount({
    ids: HOME_IDS,
    pathname: "/g/" + AZEROTH.slug,
    routes: homeRoutes,
    remembered: AZEROTH.slug,
  });
  await load("../static/pages/home.js");

  const switchers = findByClass(dom.body, "game-switcher");
  assertEqual(switchers.length, 1, "the header carries no game switcher");
  const summary = switchers[0].children.find((child) => child.tagName === "summary");
  assert(summary, "the switcher has no summary to read the current game's name off");
  assertEqual(summary.textContent, AZEROTH.name, "the switcher does not name the game this page is inside");

  const current = links(switchers[0]).filter((a) => a.getAttribute("aria-current") === "page");
  assertEqual(current.length, 1, "the switcher marks no single entry as the current game");
  assertEqual(current[0].href, "/g/" + AZEROTH.slug, "the entry marked current is not this game");

  // Every game is listed, the current one included: a list that changed
  // shape as you moved through it is a list nobody learns.
  const listed = links(switchers[0]).map((a) => a.href);
  for (const game of ALL_GAMES) {
    assert(listed.includes("/g/" + game.slug), `the switcher omits ${game.slug}`);
  }
  assert(listed.includes("/games"), "the switcher offers no way to the picker, where a second game is made");
});

// A game name is a designer's own text and never markup — asserted on
// the switcher because it is the newest place a name reaches the DOM.
check("aGameNameReachesTheSwitcherAsText", async () => {
  const crafted = { id: "d4", slug: "crafted", name: "<img src=x onerror=alert(1)>" };
  const dom = mount({
    ids: HOME_IDS,
    pathname: "/g/" + AZEROTH.slug,
    games: [AZEROTH, crafted],
    routes: homeRoutes,
  });
  await load("../static/pages/home.js");
  const found = links(dom.body).filter((a) => a.href === "/g/crafted");
  assertEqual(found.length, 1, "the crafted game is not listed at all");
  assertEqual(found[0].textContent, crafted.name, "the crafted name did not arrive as text");
});

// --- The shortcut that had to survive ---------------------------------

const PICKER_IDS = ["games", "status", "empty-state", "new-game", "create-game"];

check("theRememberedGameStillShortCircuitsTheRoot", async () => {
  const dom = mount({ ids: PICKER_IDS, pathname: "/", remembered: LE_MANS.slug });
  await load("../static/app.js");
  assertEqual(
    dom.navigations.join("|"),
    "/g/" + LE_MANS.slug,
    'the remembered-game shortcut no longer fires at "/", which is the convenience this fix had to keep',
  );
});

check("thePickerAtGamesNeverRedirects", async () => {
  const dom = mount({ ids: PICKER_IDS, pathname: "/games", remembered: LE_MANS.slug });
  await load("../static/app.js");
  assertEqual(
    dom.navigations.length,
    0,
    `/games sent the visitor somewhere instead of listing their games: ${JSON.stringify(dom.navigations)}`,
  );
  const listed = links(dom.elements.games).map((a) => a.href);
  for (const game of ALL_GAMES) {
    assert(listed.includes("/g/" + game.slug), `/games does not list ${game.slug}: ${JSON.stringify(listed)}`);
  }
});

// The second half of the same defect: creating a game used to be offered
// only to an account that had none.
check("theGamesPageOffersASecondGame", async () => {
  const dom = mount({ ids: PICKER_IDS, pathname: "/games" });
  await load("../static/app.js");
  assertEqual(
    dom.elements["new-game"].hidden,
    false,
    "an account that already has a game is still not offered a second one",
  );
});

// --- Run --------------------------------------------------------------

for (const [name, fn] of pending) {
  try {
    await fn();
    console.log("ok   " + name);
  } catch (error) {
    failures += 1;
    console.error("FAIL " + name + ": " + (error && error.message ? error.message : error));
  }
}

if (failures > 0) {
  console.error(`${failures} check(s) failed`);
  process.exit(1);
}
console.log(`ok: ${pending.length} check(s)`);
// The data client keeps an event stream open and reconnects on a timer,
// which is right in a browser and would hold this process open for ever.
process.exit(0);
