// The harness for the seven page modules Task 15 put on seven routes:
// internal/web/static/pages/{home,views,view,types,catalogue,entity,assets}.js
// and the plumbing they share, pages/page.js.
//
// **What it holds has no Go counterpart and could not have one.** Every
// route in this product serves a static shell and resolves nothing
// server-side, so a handler test can prove a shell is served and can
// prove nothing about what the shell then does — whether the home costs
// one call or one per type, whether a game with no views is offered a
// button that leads nowhere, whether an entity page shows the fields its
// type declares and its entity does not carry, whether a node click
// costs a designer their arrangement. All of those are properties of
// these modules and of nothing else.
//
// It replaces internal/web/jstest/game_summary_test.mjs, whose subject
// moved: the game home is `pages/home.js` now, and its checks are the
// four at the end of this file, unchanged in substance.
//
// Run directly: `node internal/web/jstest/pages_test.mjs`.
// internal/web/static_appjs_browser_test.go shells out to it too.

import { register } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ORIGIN = "http://localhost:8124";
const HERE = path.dirname(fileURLToPath(import.meta.url));

register(new URL("./importmap_loader.mjs", import.meta.url), {
  parentURL: import.meta.url,
  data: { staticDir: path.join(HERE, "..", "static"), shell: "game.html" },
});

// The globals a component's module needs at *definition* time. The page
// modules import the canvas, the ground panel, the frame and the table
// painter, and each of those calls customElements.define at module
// scope; without these the first import throws before a single check
// runs. Nothing here parses markup, deliberately.
const defined = new Map();
globalThis.CustomEvent = class CustomEvent {
  constructor(type, init = {}) {
    this.type = type;
    this.detail = init.detail;
    this.bubbles = init.bubbles === true;
    this.composed = init.composed === true;
  }
};
globalThis.HTMLElement = class HTMLElement {
  constructor() {
    this.dispatched = [];
  }
  attachShadow() {
    return { appendChild() {} };
  }
  dispatchEvent(event) {
    this.dispatched.push(event);
    return true;
  }
  addEventListener() {}
  removeEventListener() {}
};
globalThis.customElements = {
  define(name, ctor) {
    defined.set(name, ctor);
  },
  get(name) {
    return defined.get(name);
  },
};

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
// It is the stub the game-summary harness used, plus what a page needs
// that a catalogue did not: listeners a test can fire, attributes an
// address can be read off, and a parent chain the address walk climbs.
//
// **Nothing in it can interpret a string as markup.** A stub with an
// innerHTML setter would let a regression through by emulating one, so
// anything found in a rendered subtree got there through textContent.
function fakeElement(tag = "div") {
  const el = {
    tagName: tag,
    className: "",
    textContent: "",
    href: "",
    children: [],
    parentNode: null,
    attributes: new Map(),
    listeners: {},
    disabled: false,
    files: null,
    append(...nodes) {
      for (const node of nodes) {
        if (node && typeof node === "object") node.parentNode = el;
      }
      this.children.push(...nodes);
    },
    prepend(...nodes) {
      for (const node of nodes) {
        if (node && typeof node === "object") node.parentNode = el;
      }
      this.children.unshift(...nodes);
    },
    replaceChildren(...nodes) {
      for (const node of nodes) {
        if (node && typeof node === "object") node.parentNode = el;
      }
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
    async dispatch(name, event = {}) {
      for (const handler of this.listeners[name] ?? []) {
        await handler({ target: el, preventDefault() {}, ...event });
      }
    },
    async click() {
      await this.dispatch("click");
    },
    querySelector() {
      return null;
    },
  };
  el._hidden = false;
  el.hiddenWrites = 0;
  Object.defineProperty(el, "hidden", {
    enumerable: true,
    get() {
      return this._hidden;
    },
    set(value) {
      this._hidden = value;
      this.hiddenWrites++;
    },
  });
  return el;
}

// text flattens a rendered subtree the way a reader sees it.
function text(node) {
  if (!node) return "";
  const own = node.textContent ?? "";
  return own + node.children.map(text).join(" ");
}

// links collects every anchor in a subtree, so an assertion about
// addressing reads what a designer would click.
function links(node, found = []) {
  if (!node) return found;
  if (node.tagName === "a") found.push(node);
  for (const child of node.children) links(child, found);
  return found;
}

// The ids every shell in this product declares, so one stub serves them
// all and a page that looks up an id no shell has gets null — which is
// what a browser would hand it.
const SHELL_IDS = [
  "game-name",
  "game-summary",
  "home",
  "views",
  "views-error",
  "views-onboarding",
  "views-more",
  "view-list-note",
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
  "catalogue-note",
  "catalogue-error",
  "type-name",
  "type-meta",
  "type-note",
  "entities",
  "entities-empty",
  "entities-error",
  "entities-more",
  "entity-name",
  "entity-address",
  "entity-error",
  "entity-content",
  "assets-note",
  "assets",
  "assets-empty",
  "assets-error",
  "assets-more",
  "view-root",
  "view-error",
  "view-narrow",
  "entity-panel",
  // The trail that replaced four differently-worded back links. Every
  // shell inside a game declares it; the pages fill it through
  // pages/page.js setBreadcrumb.
  "crumbs",
];

const GAME = { id: "1e9d6b0c-4f7a-4a9e-8a5b-2c1d3e4f5a6b", slug: "azeroth", name: "Azeroth" };

// mount installs a document holding only the ids the shell under test
// declares, and a fetch that answers from a routing table.
//
// `routes` is a list of [predicate, answer] pairs, tried in order. An
// unmatched URL is a 404 whose message names it, so a page that asked
// for something nobody expected fails loudly rather than rendering a
// plausible blank.
function mount({ ids, pathname, search = "", routes = [], games = [GAME] }) {
  const elements = {};
  for (const id of SHELL_IDS) elements[id] = ids.includes(id) ? fakeElement() : null;
  // Every shell ships its content hidden; the stub copies that and then
  // zeroes the write, so every visibility write an assertion can see is
  // the page's.
  for (const id of ["home", "entity-content", "entity-panel", "views", "assets", "entities"]) {
    if (elements[id]) elements[id].hidden = true;
  }
  for (const el of Object.values(elements)) {
    if (el) el.hiddenWrites = 0;
  }

  const body = fakeElement("body");
  const requested = [];
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
  const location = { hash: "", pathname, search, origin: ORIGIN, href: ORIGIN + pathname };
  const navigations = [];
  Object.defineProperty(location, "href", {
    enumerable: true,
    get() {
      return ORIGIN + pathname;
    },
    set(value) {
      navigations.push(value);
    },
  });
  globalThis.window = { location };
  globalThis.history = { replaceState() {} };
  globalThis.localStorage = {
    _values: {},
    getItem(key) {
      return Object.prototype.hasOwnProperty.call(this._values, key) ? this._values[key] : null;
    },
    setItem(key, value) {
      this._values[key] = value;
    },
  };
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
  return { elements, requested, navigations, body };
}

// load imports one page module fresh. The query string is what makes it
// fresh: a module is evaluated once per URL, and every case here needs
// its own top-level run.
let serial = 0;
async function load(name) {
  serial += 1;
  return import(`../static/pages/${name}.js?case=${serial}`);
}

const base = "/api/games/" + GAME.slug;
const events = [(url) => url === base + "/events", { stream: true }];
const noViews = [(url) => url.startsWith(base + "/views"), { body: { items: [] } }];
const noDocs = [(url) => url.startsWith(base + "/docs"), { body: { items: [] } }];
const noKinds = [(url) => url === base + "/docs/kinds", { body: { kinds: [], unkinded: 0 } }];

function summaryOf(overrides = {}) {
  return {
    entity_types: [
      { id: "a", key: "quest", label: "Quest", label_plural: "Quests", entity_count: 400, invalid_count: 3 },
      { id: "b", key: "zone", label: "Zone", label_plural: "Zones", entity_count: 1, invalid_count: 0 },
    ],
    relation_types: [
      { id: "c", key: "takes_place_in", label: "takes place in", relation_count: 12, invalid_count: 2 },
    ],
    totals: { entities: 401, relations: 12, invalid: 5 },
    role: "editor",
    ...overrides,
  };
}

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

// --- The home ---------------------------------------------------------

// **One call for the counts.** The obvious future edit — a count per
// type, fetched per type — turns this red the moment it lands, which is
// the whole reason the number is asserted rather than the rendering.
check("theHomeMakesOneSummaryCall", async () => {
  const dom = mount({
    ids: HOME_IDS,
    pathname: "/g/azeroth",
    routes: [[(url) => url === base + "/summary", { body: summaryOf() }], noKinds, noDocs, noViews, events],
  });
  await load("home");
  const counts = dom.requested.filter((url) => url.endsWith("/summary"));
  assertEqual(counts.length, 1, "the home asked for the counts more than once");
  for (const url of dom.requested) {
    assert(
      !url.includes("/entities") && !url.includes("/relations"),
      `the home enumerated content to build a catalogue: ${url}`,
    );
  }
});

check("theHomeLanesAreViewsCatalogueProseInThatOrder", async () => {
  const { DESTINATIONS, DESTINATION_VIEWS, DESTINATION_CATALOGUE, DESTINATION_PROSE } = await load("page");
  assertEqual(
    DESTINATIONS.join("|"),
    [DESTINATION_VIEWS, DESTINATION_CATALOGUE, DESTINATION_PROSE].join("|"),
    "the three destinations are not Views, Catalogue and Prose",
  );
  // And the shell puts its lanes in that order, which is the half a
  // constant cannot hold: the order a reader meets them in is the order
  // of the sections in game.html.
  const { readFileSync } = await import("node:fs");
  const shell = readFileSync(path.join(HERE, "..", "static", "game.html"), "utf8");
  const order = ["lane-views", "lane-catalogue", "prose"].map((id) => shell.indexOf(`id="${id}"`));
  for (const at of order) assert(at > 0, "game.html is missing one of the three lanes");
  assert(order[0] < order[1] && order[1] < order[2], `the lanes are not in destination order: ${order}`);
});

// **A sentence and a link, and no button.** Nothing in this interface
// writes a view, so a create control here would lead nowhere — the
// plan's O1, and the one piece of onboarding this product ships.
check("aGameWithNoViewsGetsTheAgentSentenceAndNoCreateButton", async () => {
  const dom = mount({
    ids: HOME_IDS,
    pathname: "/g/azeroth",
    routes: [[(url) => url === base + "/summary", { body: summaryOf() }], noKinds, noDocs, noViews, events],
  });
  const { NO_VIEWS_HEADING, NO_VIEWS_SENTENCE, SKILL_BUNDLE_HREF, SKILL_BUNDLE_LABEL } = await load("page");
  await load("home");
  const lane = dom.elements["views-onboarding"];
  const rendered = text(lane);
  assert(rendered.includes(NO_VIEWS_HEADING), `the onboarding heading is missing: ${JSON.stringify(rendered)}`);
  assert(rendered.includes(NO_VIEWS_SENTENCE), `the onboarding sentence is missing: ${JSON.stringify(rendered)}`);
  const anchors = links(lane);
  assertEqual(anchors.length, 1, "the onboarding is not exactly one link");
  assertEqual(anchors[0].href, SKILL_BUNDLE_HREF, "the link does not point at the skill bundle's documentation");
  assertEqual(anchors[0].textContent, SKILL_BUNDLE_LABEL, "the link is not labelled");
  assert(!lane.hidden, "the onboarding is hidden on a game with no views");

  // And nothing anywhere on the page offers to create one. A verb is
  // what a button that leads nowhere would be spelled with.
  const everything = [text(lane), text(dom.elements.views)].join(" ").toLowerCase();
  for (const verb of ["new view", "create", "add view", "save as"]) {
    assert(!everything.includes(verb), `the views lane offers "${verb}" on a page that cannot create a view`);
  }
});

check("aGameWithViewsGetsNoOnboarding", async () => {
  const dom = mount({
    ids: HOME_IDS,
    pathname: "/g/azeroth",
    routes: [
      [(url) => url === base + "/summary", { body: summaryOf() }],
      noKinds,
      noDocs,
      [
        (url) => url.startsWith(base + "/views"),
        { body: { items: [{ key: "world", name: "The world", renderer: "graph", stale: false }] } },
      ],
      events,
    ],
  });
  await load("home");
  assert(dom.elements["views-onboarding"].hidden, "the onboarding shows on a game that has views");
  const anchors = links(dom.elements.views);
  assertEqual(anchors.length, 1, "the view is not a link");
  assertEqual(anchors[0].href, "/g/azeroth/v/world", "a view is not addressed by its key");
});

// **A viewer is never told to do what the server will refuse.** Two
// fixtures and two sentences, because one fixture cannot tell a branch
// on a role from a constant.
check("emptyStateActionsFollowTheRole", async () => {
  const sentences = {};
  for (const role of ["viewer", "editor"]) {
    const dom = mount({
      ids: HOME_IDS,
      pathname: "/g/azeroth",
      routes: [
        [
          (url) => url === base + "/summary",
          { body: summaryOf({ role, entity_types: [], relation_types: [], totals: { entities: 0, relations: 0, invalid: 0 } }) },
        ],
        noKinds,
        noDocs,
        noViews,
        events,
      ],
    });
    await load("home");
    sentences[role] = {
      types: dom.elements["types-empty-action"].textContent,
      docs: dom.elements["docs-empty-action"].textContent,
    };
    assert(!dom.elements["types-empty"].hidden, `the ${role} did not get the entity-types empty state`);
    assert(!dom.elements["docs-empty"].hidden, `the ${role} did not get the documents empty state`);
  }
  assert(
    sentences.viewer.types !== sentences.editor.types,
    "a viewer and an editor read the same sentence about declaring a type",
  );
  assert(
    sentences.viewer.docs !== sentences.editor.docs,
    "a viewer and an editor read the same sentence about writing a document",
  );
  for (const what of ["types", "docs"]) {
    assert(
      sentences.viewer[what].includes("will refuse a write from you"),
      `the viewer's ${what} sentence does not say the instance will refuse the write`,
    );
  }
});

check("theProseLaneNamesTheGamesDocumentKinds", async () => {
  const dom = mount({
    ids: HOME_IDS,
    pathname: "/g/azeroth",
    routes: [
      [(url) => url === base + "/summary", { body: summaryOf() }],
      [
        (url) => url === base + "/docs/kinds",
        {
          body: {
            kinds: [
              { kind: "lore", document_count: 42 },
              { kind: "<img src=x onerror=alert(1)>script", document_count: 3 },
            ],
            unkinded: 1,
          },
        },
      ],
      noDocs,
      noViews,
      events,
    ],
  });
  await load("home");
  const line = dom.elements["doc-kinds"].textContent;
  assert(line.includes("lore 42"), `the kind line does not name lore: ${JSON.stringify(line)}`);
  assert(line.includes("1 with no kind"), `the kind line drops the unkinded total: ${JSON.stringify(line)}`);
  assert(!dom.elements["doc-kinds"].hidden, "the kind line is hidden on a game that has kinds");
  // The crafted kind is on screen as characters, because a kind is an
  // agent's own text and this page never interprets one.
  assert(line.includes("<img src=x onerror=alert(1)>script"), "a crafted kind did not reach the page as text");
});

// **A path is a value that changes and not an identity to cache.**
// `document.moved` names both spellings, and a lane that kept the old
// one would link a designer at a document that is no longer there.
check("aMovedDocumentIsFollowedNotCached", async () => {
  let asked = 0;
  const paths = ["lore/duskwood", "lore/duskwood-forest"];
  const dom = mount({
    // #game-name is deliberately absent, so the module's own top-level
    // run does not fire and this check drives `home` itself — which is
    // the only way to reach the listener the page hands the client.
    ids: HOME_IDS.filter((id) => id !== "game-name"),
    pathname: "/g/azeroth",
    routes: [
      [(url) => url === base + "/summary", { body: summaryOf() }],
      noKinds,
      [
        (url) => url.startsWith(base + "/docs"),
        () => {
          const at = Math.min(asked, paths.length - 1);
          asked += 1;
          return { body: { items: [{ id: "d1", path: paths[at], title: "Duskwood" }] } };
        },
      ],
      noViews,
      events,
    ],
  });
  const { home } = await load("home");
  const { openGame } = await import("../static/pages/page.js");
  const { applyEvent, REREAD, TARGET_PROSE } = await import("../static/client.js");

  const surface = await home(await openGame());
  const before = text(dom.elements.docs);
  assert(before.includes("lore/duskwood"), `the lane did not render the document: ${JSON.stringify(before)}`);

  // The decision is the client's own, from the real reducer over the
  // real event, so this check cannot pass against a verdict this file
  // invented.
  const verdict = applyEvent({ views: {} }, {
    kind: "document.moved",
    data: { from: "lore/duskwood", to: "lore/duskwood-forest" },
  });
  assertEqual(verdict.decision, REREAD, "a moved document is not a re-read");
  assertEqual(verdict.target, TARGET_PROSE, "a moved document does not re-read the prose surface");

  assert(typeof surface.onEvent === "function", "the home registered no stream listener");
  await surface.onEvent(verdict);
  const after = text(dom.elements.docs);
  assert(
    after.includes("lore/duskwood-forest"),
    `the lane kept the old path after a move: ${JSON.stringify(after)}`,
  );
  assert(!after.includes("lore/duskwood "), "the lane rendered the old path beside the new one");
});

// --- The catalogue ----------------------------------------------------

const CATALOGUE_IDS = [
  "type-name",
  "type-meta",
  "type-note",
  "entities",
  "entities-empty",
  "entities-error",
  "entities-more",
  "crumbs",
];

check("theCatalogueSaysItIsACatalogueAndNotAView", async () => {
  const dom = mount({
    ids: CATALOGUE_IDS,
    pathname: "/g/azeroth/t/quest",
    routes: [
      [(url) => url === base + "/types/by-key/quest", { body: { key: "quest", label: "Quest", label_plural: "Quests", field_schema: [] } }],
      [(url) => url.startsWith(base + "/entities"), { body: { items: [] } }],
      events,
    ],
  });
  const { CATALOGUE_NOTE } = await load("catalogue");
  assertEqual(
    dom.elements["type-note"].textContent,
    CATALOGUE_NOTE,
    "the catalogue does not say what it is",
  );
  assert(
    CATALOGUE_NOTE.includes("not a view"),
    "the catalogue's own line does not distinguish it from a view, which it is deliberately close to in appearance",
  );
});

// The trail replaced four differently-worded back links, and the thing
// those links never did is the thing this asserts: the last crumb says
// where the reader *is*. It is checked on the deepest trail the
// catalogue has, and after the type's own fetch has landed — the page
// puts a trail up before it knows the type's name and rewrites the last
// crumb when it does, so a check that only saw the first one would pass
// on a page still showing a key.
check("theTrailNamesTheGameTheSectionAndWhereYouAre", async () => {
  const dom = mount({
    ids: CATALOGUE_IDS,
    pathname: "/g/azeroth/t/quest",
    routes: [
      [(url) => url === base + "/types/by-key/quest", { body: { key: "quest", label: "Quest", label_plural: "Quests", field_schema: [] } }],
      [(url) => url.startsWith(base + "/entities"), { body: { items: [] } }],
      events,
    ],
  });
  await load("catalogue");
  const tag = (node) => String(node.tagName).toUpperCase();
  const crumbs = dom.elements["crumbs"].children.filter((node) => tag(node) !== "SPAN");
  assertEqual(
    crumbs.map((node) => node.textContent).join(" / "),
    "Azeroth / Catalogue / Quests",
    "the trail does not name the game, the section and this page",
  );
  assertEqual(tag(crumbs[0]), "A", "the game is not a link back to the game");
  assertEqual(tag(crumbs[1]), "A", "the section is not a link back to the section");
  assertEqual(
    tag(crumbs[2]),
    "B",
    "the last crumb is a link: a trail whose end is a link to the page you are on is a back link with extra steps",
  );
  assertEqual(
    crumbs[2].getAttribute("aria-current"),
    "page",
    "the last crumb does not tell a screen reader it is the current page",
  );

  // The component's own contract, asserted against the component rather
  // than through a page: **the last crumb is never a link, even when a
  // caller hands it an href.** No caller does today, which is exactly why
  // this is checked here — a guard only the call sites keep is a guard
  // the seventh call site breaks.
  const { crumbNodes } = await load("page");
  const forced = crumbNodes(globalThis.document, [
    { label: "Azeroth", href: "/g/azeroth" },
    { label: "Quests", href: "/g/azeroth/t/quest" },
  ]);
  const lastForced = forced[forced.length - 1];
  assertEqual(
    tag(lastForced),
    "B",
    "a last crumb given an href became a link to the page the reader is already on",
  );
  assertEqual(lastForced.href, "", "the last crumb kept an address");
});

check("theCataloguePagesOverTheExistingCursor", async () => {
  const pages = [
    { items: [{ key: "a", name: "A" }, { key: "b", name: "B" }], next_cursor: "1" },
    { items: [{ key: "c", name: "C" }] },
  ];
  const dom = mount({
    ids: CATALOGUE_IDS,
    pathname: "/g/azeroth/t/quest",
    routes: [
      [(url) => url === base + "/types/by-key/quest", { body: { key: "quest", label_plural: "Quests", field_schema: [] } }],
      [
        (url) => url.startsWith(base + "/entities"),
        (url) => {
          const cursor = new URL(ORIGIN + url).searchParams.get("cursor");
          return { body: pages[cursor === null ? 0 : Number(cursor)] };
        },
      ],
      events,
    ],
  });
  await load("catalogue");
  assert(text(dom.elements.entities).includes("A"), "the first page did not render");
  assert(!dom.elements["entities-more"].hidden, "the button is hidden while the server is still issuing a cursor");
  await dom.elements["entities-more"].click();
  const rendered = text(dom.elements.entities);
  assert(rendered.includes("C"), `the second page did not render: ${JSON.stringify(rendered)}`);
  const asked = dom.requested.filter((url) => url.includes("/entities"));
  assertEqual(asked.length, 2, "the catalogue did not page over the cursor");
  assert(asked[1].includes("cursor=1"), `the second page did not carry the server's cursor: ${asked[1]}`);
  assert(dom.elements["entities-more"].hidden, "the button is still offered after the last page");
});

// --- The entity -------------------------------------------------------

const ENTITY_IDS = ["entity-name", "entity-address", "entity-error", "entity-content", "crumbs"];

const QUEST_SCHEMA = [
  { key: "level", label: "Level", type: "number" },
  { key: "repeatable", label: "Repeatable", type: "bool" },
  { key: "summary", label: "Summary", type: "longtext" },
];

function entityRoutes(entity, out = [], into = [], documents = []) {
  return [
    [(url) => url.startsWith(base + "/entities/by-key/"), { body: entity }],
    [(url) => url === base + "/types/by-key/quest", { body: { key: "quest", label: "Quest", field_schema: QUEST_SCHEMA } }],
    [
      (url) => url.startsWith(base + "/relations") && url.includes("source_key="),
      { body: { items: out } },
    ],
    [
      (url) => url.startsWith(base + "/relations") && url.includes("target_key="),
      { body: { items: into } },
    ],
    [(url) => url.startsWith(base + "/docs/links"), { body: { documents, entities: [] } }],
    events,
  ];
}

// **What could be filled in here** is the question a designer is usually
// asking, so a declared field the entity does not carry is a row and not
// an omission.
check("theEntityShowsDeclaredButUnsetFields", async () => {
  const dom = mount({
    ids: ENTITY_IDS,
    pathname: "/g/azeroth/e/quest/hogger",
    routes: entityRoutes({ type_key: "quest", key: "hogger", name: "Wanted: Hogger", fields: { level: 11 } }),
  });
  const { ABSENT_TEXT } = await import("../static/render/twin.js");
  await load("entity");
  const rendered = text(dom.elements["entity-content"]);
  for (const field of QUEST_SCHEMA) {
    assert(rendered.includes(field.label), `the declared field ${field.key} is missing from the page`);
  }
  assert(rendered.includes("11"), "the field the entity carries is missing its value");
  // Two absences, both wearing the twin's own mark, because the entity
  // carries one of three declared fields.
  const dashes = rendered.split(ABSENT_TEXT).length - 1;
  assertEqual(dashes, 2, "a declared-and-unset field is not marked absent");
});

check("theEntityShowsDeclaredFieldsInDeclaredOrder", async () => {
  const { fieldRows } = await load("entity");
  const rows = fieldRows(QUEST_SCHEMA, { fields: { summary: "x", level: 3 } });
  assertEqual(
    rows.map((row) => row.key).join("|"),
    "level|repeatable|summary",
    "the fields are not in the order the type declares them",
  );
});

// **The exact shape that was write-only for a whole sub-project.** A
// relation type may declare a field schema and nothing showed the
// values until this page.
check("relationRowsCarryTheRelationsOwnFields", async () => {
  const dom = mount({
    ids: ENTITY_IDS,
    pathname: "/g/azeroth/e/quest/hogger",
    routes: entityRoutes(
      { type_key: "quest", key: "hogger", name: "Hogger", fields: {} },
      [
        {
          type_key: "rewards",
          target: { type_key: "item", key: "cudgel", name: "Hogger's Cudgel" },
          fields: { quantity: 2, bind: "on_pickup" },
        },
      ],
    ),
  });
  await load("entity");
  const rendered = text(dom.elements["entity-content"]);
  assert(rendered.includes("rewards"), "the relation type does not head its group");
  assert(rendered.includes("Hogger's Cudgel"), "the far end of the relation is missing");
  assert(rendered.includes("quantity 2"), `the relation's own field is missing: ${JSON.stringify(rendered)}`);
  assert(rendered.includes("bind on_pickup"), "the relation's second field is missing");
});

check("bothDirectionsOfARelationAreListedSeparately", async () => {
  const dom = mount({
    ids: ENTITY_IDS,
    pathname: "/g/azeroth/e/quest/hogger",
    routes: entityRoutes(
      { type_key: "quest", key: "hogger", name: "Hogger", fields: {} },
      [{ type_key: "rewards", target: { type_key: "item", key: "cudgel", name: "Cudgel" }, fields: {} }],
      [{ type_key: "requires", source: { type_key: "quest", key: "wanted", name: "Wanted" }, fields: {} }],
    ),
  });
  await load("entity");
  const rendered = text(dom.elements["entity-content"]);
  // The two headings are the game's direction in plain words now, not the
  // model's: "Relations out" and "Relations in" are the shape of the
  // query, and a designer reads which way an edge points.
  const out = rendered.indexOf("Leading out of this");
  const into = rendered.indexOf("Pointing at this");
  assert(out > -1 && into > out, "the two directions are not two lists in order");
  assert(rendered.indexOf("Cudgel") > out && rendered.indexOf("Cudgel") < into, "the outgoing edge is in the wrong list");
  assert(rendered.indexOf("Wanted") > into, "the incoming edge is in the wrong list");
  // Two calls, one per direction, each naming the endpoint it filters on.
  const asked = dom.requested.filter((url) => url.startsWith(base + "/relations"));
  assertEqual(asked.length, 2, "an entity's edges were asked for in one call and sorted afterwards");
  assert(asked.some((url) => url.includes("source_key=hogger")), "no call filtered on this entity as a source");
  assert(asked.some((url) => url.includes("target_key=hogger")), "no call filtered on this entity as a target");
});

check("anEntityPageAddressesEveryLinkBySlugAndKey", async () => {
  const dom = mount({
    ids: ENTITY_IDS,
    pathname: "/g/azeroth/e/quest/hogger",
    routes: entityRoutes(
      { type_key: "quest", key: "hogger", name: "Hogger", fields: {} },
      [{ type_key: "rewards", target: { type_key: "item", key: "cudgel", name: "Cudgel" }, fields: {} }],
      [],
      [{ id: "d1", path: "lore/hogger", title: "Hogger", kind: "lore", role: "describes" }],
    ),
  });
  await load("entity");
  const hrefs = links(dom.elements["entity-content"]).map((a) => a.href);
  assert(hrefs.includes("/g/azeroth/e/item/cudgel"), `the far end is not addressed by key: ${hrefs}`);
  assert(hrefs.includes("/g/azeroth/doc?path=lore%2Fhogger"), `the document is not addressed by path: ${hrefs}`);
  for (const href of hrefs) {
    assert(
      !/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/.test(href),
      `a link on the entity page carries a uuid: ${href}`,
    );
  }
});

// --- The entity panel over a canvas -----------------------------------

// **O5's deliberate absence.** The panel reads an entity; it never runs
// a view. A panel that ran a query to show a field would make reading a
// field cost a diagram.
check("theEntityPanelRunsNoViews", async () => {
  const dom = mount({
    ids: ["entity-panel"],
    pathname: "/g/azeroth/v/world",
    routes: entityRoutes({ type_key: "quest", key: "hogger", name: "Hogger", fields: { level: 11 } }),
  });
  const { client } = await import("../static/client.js");
  const { openPanel } = await load("view");
  const panel = dom.elements["entity-panel"];
  await openPanel(globalThis.document, panel, GAME.slug, client({ slug: GAME.slug }), JSON.stringify(["quest", "hogger"]));
  const runs = dom.requested.filter((url) => url.includes("/views/run"));
  assertEqual(runs.length, 0, `the entity panel ran ${runs.length} view(s)`);
  assert(!panel.hidden, "the panel did not open");
  assert(text(panel).includes("Hogger"), "the panel is empty");
  assert(
    links(panel).some((a) => a.href === "/g/azeroth/e/quest/hogger"),
    "the full page is not one click further",
  );
});

// **A node click opens the panel and the canvas stays mounted.** Losing
// an arrangement to read a field would make reading fields expensive.
check("aNodeClickOpensThePanelAndDoesNotNavigate", async () => {
  const dom = mount({
    // No #view-root: this check drives `wire` directly, and letting the
    // module's own top-level run fire as well would mix its calls into
    // the ones being counted.
    ids: ["entity-panel"],
    pathname: "/g/azeroth/v/world",
    routes: entityRoutes({ type_key: "quest", key: "hogger", name: "Hogger", fields: {} }),
  });
  const { client } = await import("../static/client.js");
  const { wire } = await load("view");

  // A canvas stand-in: what `wire` actually touches, and a node element
  // carrying the emitter's own addressing attribute.
  const surface = fakeElement("g");
  const node = fakeElement("rect");
  node.setAttribute("data-key", JSON.stringify(["quest", "hogger"]));
  surface.append(node);
  const panels = fakeElement("div");
  const selected = [];
  const canvas = {
    shell: { surfaceHost: surface, panels },
    view: { k: 1 },
    showArrangement() {},
  };
  const root = fakeElement("div");
  root.append(surface);
  const state = {
    canvas,
    arrangement: {
      select(address) {
        selected.push(address);
        return [address];
      },
      pointerDown() {
        return null;
      },
    },
    ground: { root: fakeElement("div"), placing: null },
    frame: fakeElement("mst-view-frame"),
  };
  wire(globalThis.document, dom.elements["entity-panel"], GAME.slug, client({ slug: GAME.slug }), state);

  await surface.dispatch("pointerdown", { target: node, clientX: 10, clientY: 10 });
  await surface.dispatch("pointerup", { target: node, clientX: 10, clientY: 10 });

  assertEqual(selected.length, 1, "the click selected no node");
  assert(!dom.elements["entity-panel"].hidden, "the click did not open the panel");
  assertEqual(dom.navigations.length, 0, `a node click navigated: ${dom.navigations}`);
  assert(root.children.includes(surface), "the canvas was unmounted by a node click");
});

// **The twin's selection reaches the arrangement, and it goes through
// `wire`.**
//
// twin_test.mjs asserts mst-twin dispatches `mst-select` "under the name
// a canvas will listen for", and for four tasks nothing listened for it:
// a reader could tab through the twin, watch the rows mark themselves,
// move to the canvas and find the arrow keys moved nothing and wrote
// nothing. Measured in a browser on the seeded game before the fix —
// focusing the *Wanted: Hogger* row set `mst-twin.selected`, one
// ArrowRight left the node's x at 287 and sent zero writes — which is the
// whole keyboard path of spec §8.1, gone.
//
// So this check drives `wire` and not the listener, for save_as_test's
// reason: a check that called the handler directly would stay green with
// the one binding line deleted, which is the exact defect being closed.
check("theTwinsSelectionReachesTheArrangementAtItsCallSite", async () => {
  const dom = mount({ ids: ["entity-panel"], pathname: "/g/azeroth/v/world", routes: [] });
  const { wire } = await load("view");
  const { SELECT_EVENT } = await import("../static/components/mst-twin.js");

  const selected = [];
  const shown = [];
  const surface = fakeElement("g");
  const frame = fakeElement("mst-view-frame");
  const state = {
    canvas: {
      shell: { surfaceHost: surface, panels: fakeElement("div") },
      view: { k: 1 },
      showArrangement(arrangement) {
        shown.push(arrangement);
      },
    },
    arrangement: {
      select(address) {
        selected.push(address);
        return [address];
      },
      pointerDown() {
        return null;
      },
    },
    ground: { root: fakeElement("div"), placing: null },
    frame,
  };
  wire(globalThis.document, dom.elements["entity-panel"], GAME.slug, null, state);

  await frame.dispatch(SELECT_EVENT, { detail: { type: "quest", key: "hogger" } });
  assertEqual(selected.length, 1, "focusing a twin row selected no node on the canvas");
  assertEqual(selected[0], JSON.stringify(["quest", "hogger"]), "the twin's node arrived under the wrong address");
  assertEqual(shown.length, 1, "the canvas was never told to redraw its selection");

  // An event carrying no node is not a selection of nothing. The twin
  // never sends one, and a listener that treated it as an address would
  // clear a designer's selection on any stray event of the same name.
  await frame.dispatch(SELECT_EVENT, { detail: null });
  assertEqual(selected.length, 1, "an event with no node still reached the arrangement");
});

// **Below tablet width the drawing goes and the writing path goes with
// it — and this check drives the page, not the controller.**
//
// The plan's Step 10 was left unbuilt for a reason worth keeping: hiding
// the canvas with CSS alone leaves `Arrangement` believing it may write,
// so a keyboard reader who tabs the twin — which is *still on screen*,
// because it is now the whole view — can nudge a node on a drawing
// nobody can see. writes_test.mjs holds the controller's half; this is
// the call site, and the two halves together are the property.
//
// It asserts the conjunction on purpose: **the canvas is hidden AND the
// keydown wrote nothing.** A CSS-only fallback satisfies the first and
// fails the second; a `setDrawn` that nothing calls satisfies the second
// only because the first never happened. Deleting either the
// `applyWidth` call from `draw`/`watchWidth` or the `setDrawn` line
// inside `applyWidth` turns this red.
check("aNarrowWindowHidesTheDrawingAndDisarmsTheKeyboard", async () => {
  const dom = mount({
    ids: ["entity-panel", "view-narrow"],
    pathname: "/g/azeroth/v/world",
    routes: [],
  });
  const { wire, watchWidth, NOTICE_TOO_NARROW } = await load("view");
  const { Arrangement } = await import("../static/components/mst-canvas.js");
  const { client } = await import("../static/client.js");
  const { MODE_MANUAL } = await import("../static/positions.js");

  // Every request this page could make, counted. It is the instrument
  // for the half that matters: "the keyboard wrote nothing" is a count
  // of requests and not a state of a flag.
  const wrote = [];
  const c = client({
    slug: GAME.slug,
    fetchImpl: async (url) => {
      wrote.push(String(url));
      return { ok: true, status: 200, json: async () => ({ positions: [] }) };
    },
  });

  const surface = fakeElement("g");
  const canvas = {
    hidden: false,
    shell: { surfaceHost: surface, panels: fakeElement("div") },
    view: { k: 1 },
    shown: 0,
    showArrangement() {
      this.shown += 1;
    },
    nudgeNodes() {},
    beginDrag() {
      return null;
    },
    endDrag() {
      return null;
    },
  };
  const arrangement = new Arrangement({
    canvas,
    client: c,
    viewKey: "world",
    row: { key: "world", layout_mode: MODE_MANUAL, version: 3 },
    mode: MODE_MANUAL,
    role: "designer",
    nodes: [{ type: "quest", key: "hogger", x: 10, y: 20 }],
  });
  const ground = {
    root: fakeElement("div"),
    placing: null,
    hidden: false,
    cancelled: 0,
    cancel() {
      this.cancelled += 1;
      return null;
    },
  };
  const state = {
    canvas,
    arrangement,
    ground,
    frame: fakeElement("mst-view-frame"),
    narrowEl: dom.elements["view-narrow"],
    pictured: true,
    narrow: false,
    client: c,
  };
  wire(globalThis.document, dom.elements["entity-panel"], GAME.slug, c, state);
  arrangement.select(JSON.stringify(["quest", "hogger"]));

  // The precondition, and it is the point: at a width that draws, this
  // very keystroke on this very wiring does write.
  await surface.dispatch("keydown", { key: "ArrowRight" });
  assert(wrote.length > 0, "the keyboard wrote nothing even at full width; the check proves nothing");
  const atFullWidth = wrote.length;

  // A media query list the harness owns, because a fallback that could
  // only be driven by resizing a real window is a fallback with no test.
  const listeners = [];
  const media = {
    matches: true,
    addEventListener(name, handler) {
      if (name === "change") listeners.push(handler);
    },
    fire() {
      for (const handler of listeners) handler({ matches: this.matches });
    },
  };
  watchWidth(state, { media });

  assertEqual(canvas.hidden, true, "the drawing is still on screen below tablet width");
  assertEqual(ground.hidden, true, "the ground panel outlived the drawing it aligns against");
  assertEqual(ground.cancelled, 1, "a placement in flight was left in flight");
  assertEqual(
    dom.elements["view-narrow"].textContent,
    NOTICE_TOO_NARROW,
    "the picture vanished without a word, which reads as a view that answered nothing",
  );
  assertEqual(dom.elements["view-narrow"].hidden, false, "and the sentence is hidden");

  await surface.dispatch("keydown", { key: "ArrowRight" });
  assertEqual(
    wrote.length,
    atFullWidth,
    "the canvas is hidden and the arrow keys still wrote a position to it",
  );
  await surface.dispatch("keydown", { key: "z", ctrlKey: true });
  assertEqual(wrote.length, atFullWidth, "and undo wrote to it too");

  // Widening the window gives the drawing and the writes back, which is
  // the half that says this is a fallback and not a mode a designer is
  // stuck in.
  media.matches = false;
  media.fire();
  assertEqual(canvas.hidden, false, "the drawing did not come back when the window did");
  assertEqual(ground.hidden, false, "nor did the ground panel");
  assertEqual(dom.elements["view-narrow"].textContent, "", "nor did the sentence go");
  await surface.dispatch("keydown", { key: "ArrowRight" });
  assert(wrote.length > atFullWidth, "a widened window still refuses the keyboard");
});

// --- What the game-summary harness held --------------------------------

check("aCraftedLabelReachesTheHomeAsText", async () => {
  const dom = mount({
    ids: HOME_IDS,
    pathname: "/g/azeroth",
    routes: [
      [
        (url) => url === base + "/summary",
        {
          body: summaryOf({
            entity_types: [
              {
                id: "a",
                key: "quest",
                label: "Quest",
                label_plural: "<img src=x onerror=alert(1)>Quests",
                entity_count: 400,
                invalid_count: 3,
              },
            ],
          }),
        },
      ],
      noKinds,
      noDocs,
      noViews,
      events,
    ],
  });
  await load("home");
  const rendered = text(dom.elements.types);
  assert(
    rendered.includes("<img src=x onerror=alert(1)>Quests"),
    "a crafted label did not reach the page as characters",
  );
  assert(rendered.includes("400 entities"), "the row lost its count");
  assert(rendered.includes("3 invalid"), "the row lost its invalid flag");
});

check("aCountOfOneIsSpelledInTheSingular", async () => {
  const dom = mount({
    ids: HOME_IDS,
    pathname: "/g/azeroth",
    routes: [[(url) => url === base + "/summary", { body: summaryOf() }], noKinds, noDocs, noViews, events],
  });
  await load("home");
  const rendered = text(dom.elements.types);
  assert(rendered.includes("1 entity") && !rendered.includes("1 entities"), `got: ${JSON.stringify(rendered)}`);
});

check("aFailedSummaryLeavesTheServersMessageAndNoCatalogue", async () => {
  const dom = mount({
    ids: HOME_IDS,
    pathname: "/g/azeroth",
    routes: [
      [
        (url) => url === base + "/summary",
        { status: 500, body: { error: "internal_error", message: "the game could not be counted" } },
      ],
      noKinds,
      noDocs,
      noViews,
      events,
    ],
  });
  await load("home");
  assertEqual(
    dom.elements["game-summary"].textContent,
    "the game could not be counted",
    "the server's own message is not where the totals would have gone",
  );
  const lanes = dom.elements.home;
  assert(lanes.hiddenWrites > 0, "nothing under test wrote .hidden, so this would be an assertion on the stub");
  assert(lanes.hidden, "a failed summary revealed an empty catalogue, which reads like a game with no content");
});

check("anEmptyGameGetsItsEmptyStatesAndNotTwoBlankLists", async () => {
  const dom = mount({
    ids: HOME_IDS,
    pathname: "/g/azeroth",
    routes: [
      [
        (url) => url === base + "/summary",
        { body: summaryOf({ entity_types: [], relation_types: [], totals: { entities: 0, relations: 0, invalid: 0 } }) },
      ],
      noKinds,
      noDocs,
      noViews,
      events,
    ],
  });
  await load("home");
  assertEqual(dom.elements["game-summary"].textContent, "No content yet.", "an empty game shows three zeros");
  assert(!dom.elements["types-empty"].hidden, "the entity-types empty state is hidden on an empty game");
  assert(!dom.elements["relation-types-empty"].hidden, "the relation-types empty state is hidden on an empty game");
  assert(dom.elements["doc-kinds"].hidden, "the kind line shows on a game with no kinds");
});

// --- The first sight of a picture -------------------------------------
//
// A view opens fitted, and the reason these checks exist is that the
// first version of the fit *looked* correct and did nothing. It measured
// the canvas in the same turn that put the frame on the page, got a
// 0x0 box because the frame had not laid out yet, computed no fit, and
// then recorded the view as fitted — so every view in this product
// opened at the origin at 1x and no test anywhere disagreed. Found by
// mounting a view in a browser and reading `canvas.view` afterwards.

// fakeCanvas is a canvas that measures nothing until it is allowed to.
// `sizes` is the sequence getBoundingClientRect answers with, which is
// how a frame that has not laid out yet is spelled.
function fakeCanvas(sizes) {
  const remaining = sizes.slice();
  let last = remaining[remaining.length - 1];
  return {
    view: { x: 0, y: 0, k: 1 },
    measured: 0,
    getBoundingClientRect() {
      this.measured += 1;
      const next = remaining.length > 1 ? remaining.shift() : last;
      return { width: next.width, height: next.height };
    },
    setView(view) {
      this.view = { ...this.view, ...view };
      return this.view;
    },
  };
}

const FIT_MARKS = [
  { x: 0, y: 0, w: 100, h: 40 },
  { x: 300, y: 200, w: 100, h: 40 },
];

check("aViewOpensFittedEvenWhenTheFrameHasNotLaidOutYet", async () => {
  const view = await load("view");
  const canvas = fakeCanvas([{ width: 0, height: 0 }, { width: 800, height: 600 }]);
  let settled = 0;
  const fitted = await view.fitOnce(canvas, FIT_MARKS, { settle: async () => { settled += 1; } });
  assertEqual(fitted, true, "the fit reports that it happened");
  assertEqual(settled, 1, "it waited for the frame exactly once");
  assert(canvas.view.k > 0 && canvas.view.k <= view.FIT_MAX_ZOOM, "it zoomed within the cap");
  assert(canvas.view.x !== 0 || canvas.view.y !== 0, "and it panned off the origin");
});

check("aFitThatCouldNotMeasureSaysSoRatherThanRecordingItself", async () => {
  const view = await load("view");
  const canvas = fakeCanvas([{ width: 0, height: 0 }]);
  const fitted = await view.fitOnce(canvas, FIT_MARKS, { settle: async () => {} });
  assertEqual(fitted, false, "a canvas that never has a size is not reported as fitted");
  assertEqual(canvas.view.x, 0, "and nothing was written to the view");
  assertEqual(canvas.view.k, 1, "including the zoom");
});

check("aFitCentresTheWholePictureAndNeverZoomsPastTheCap", async () => {
  const view = await load("view");
  const canvas = fakeCanvas([{ width: 800, height: 600 }]);
  await view.fitOnce(canvas, FIT_MARKS, { settle: async () => {} });
  const bounds = view.boundsOf(FIT_MARKS);
  assertEqual(bounds.x, 0, "the bounds start at the leftmost mark");
  assertEqual(bounds.width, 400, "and span to the far edge of the rightmost one");
  const centreX = canvas.view.x + canvas.view.k * (bounds.x + bounds.width / 2);
  const centreY = canvas.view.y + canvas.view.k * (bounds.y + bounds.height / 2);
  assert(Math.abs(centreX - 400) < 0.001, "the picture's centre lands on the canvas's centre in x");
  assert(Math.abs(centreY - 300) < 0.001, "and in y");
  assertEqual(canvas.view.k, view.FIT_MAX_ZOOM, "a small answer in a big window stops at the cap");
});

check("aSceneWithNoPlaceableMarksIsNotFitted", async () => {
  const view = await load("view");
  const canvas = fakeCanvas([{ width: 800, height: 600 }]);
  assertEqual(view.boundsOf([]), null, "an empty scene has no bounds");
  assertEqual(await view.fitOnce(canvas, [], { settle: async () => {} }), false, "and is not reported as fitted");
  assertEqual(canvas.measured, 0, "a canvas with nothing to fit is not even measured");
});

// --- The ground a map draws over --------------------------------------
//
// A view's `background_asset_id` is written by views.set_background and
// read by the map renderer, and between them sits this page, which is
// the only thing that mounts a renderer. It passed no asset at all: the
// scene got an entry with an empty href, which is how this page spells
// "the image is gone", so a correctly placed background drew nothing and
// banded nothing. Found by opening a map view over an uploaded image.

const ASSET = { id: "a1", url: "/api/games/azeroth/view-assets/a1", width: 1200, height: 800 };

function assetClient(pages) {
  let calls = 0;
  return {
    calls: () => calls,
    listAssets: async (options) => {
      calls += 1;
      const cursor = options && options.cursor ? options.cursor : "";
      const page = pages[cursor];
      return { ok: true, result: page };
    },
  };
}

check("aViewThatNamesAGroundIsGivenTheAssetThatCarriesItsURL", async () => {
  const view = await load("view");
  const client = assetClient({ "": { assets: [ASSET], next_cursor: "" } });
  const asset = await view.backgroundAssetFor(client, { background_asset_id: "a1" }, null);
  assertEqual(asset && asset.url, ASSET.url, "the asset's own URL reaches the page");
  const ground = view.backgroundOf({ background_asset_id: "a1", background_scale: 1 }, asset);
  assertEqual(ground.href, ASSET.url, "and the scene is given an href it can draw");
  assertEqual(ground.width, 1200, "with the pixel width only the asset knows");
});

check("aGroundIsFoundOnALaterPageOfTheListing", async () => {
  const view = await load("view");
  const client = assetClient({
    "": { assets: [{ id: "other" }], next_cursor: "c1" },
    c1: { assets: [ASSET], next_cursor: "" },
  });
  const asset = await view.backgroundAssetFor(client, { background_asset_id: "a1" }, null);
  assertEqual(asset && asset.id, "a1", "the walk follows the cursor");
  assertEqual(client.calls(), 2, "and stops as soon as it finds it");
});

check("aRedrawOfAnUnchangedGroundCostsNoCall", async () => {
  const view = await load("view");
  const client = assetClient({ "": { assets: [ASSET], next_cursor: "" } });
  const again = await view.backgroundAssetFor(client, { background_asset_id: "a1" }, ASSET);
  assertEqual(again, ASSET, "the asset already held is the answer");
  assertEqual(client.calls(), 0, "and the listing is not walked again");
});

check("aViewWithNoGroundAsksForNothing", async () => {
  const view = await load("view");
  const client = assetClient({ "": { assets: [ASSET], next_cursor: "" } });
  assertEqual(await view.backgroundAssetFor(client, {}, null), null, "no reference, no asset");
  assertEqual(await view.backgroundAssetFor(client, { background_asset_id: "" }, null), null, "an empty reference is no reference");
  assertEqual(client.calls(), 0, "and neither costs a call");
  assertEqual(view.backgroundOf({}, null), null, "and the scene is told there is no ground at all");
});

check("aGroundThatIsGoneIsAnHrefTheRendererCanBand", async () => {
  const view = await load("view");
  const client = assetClient({ "": { assets: [{ id: "other" }], next_cursor: "" } });
  const asset = await view.backgroundAssetFor(client, { background_asset_id: "a1" }, null);
  assertEqual(asset, null, "an asset that is no longer listed resolves to nothing");
  const ground = view.backgroundOf({ background_asset_id: "a1" }, asset);
  assert(ground !== null, "the view still says it names a ground");
  assertEqual(ground.href, "", "and the empty href is what says the image is gone");
});

// --- The axis a timeline lays out against ------------------------------
//
// The same shape of hole as the ground above, in the other direction: an
// axis is a *declaration* the game holds, the envelope carries values,
// and render/timeline.js says so in its own comment — with no
// declaration it has only a number axis, and on a number axis every enum
// value is off-axis. So a championship over six declared stages drew one
// tick reading "0" and piled all eighty-two of its events into the "no
// value" region, which is a picture the renderer draws on purpose and
// therefore says nothing about. Found by opening one.

const TIMELINE_ROW = {
  renderer: "timeline",
  query: {
    from: [{ type: "event", as: "all" }],
    traverse: [{ to_type: "heat" }],
  },
};

function typeClient(schemas) {
  const asked = [];
  return {
    asked,
    getType: async (key) => {
      asked.push(key);
      const schema = schemas[key];
      if (!schema) return { ok: false, error: { error: "not_found" } };
      return { ok: true, result: { key, field_schema: schema } };
    },
  };
}

const STAGES = ["Prologue", "Round 1", "Finale"];

check("aTimelineIsGivenTheDeclaredOptionsItsAxisIsMadeOf", async () => {
  const view = await load("view");
  const client = typeClient({ event: [{ key: "stage", type: "enum", options: STAGES }] });
  const axis = await view.axisDeclarationFor(client, TIMELINE_ROW, { axis_field: "stage" }, null);
  assertEqual(axis && axis.type, "enum", "the axis knows it is an enum");
  assertEqual(axis.options.join("|"), STAGES.join("|"), "in the order the type declares, not the answer's");
});

check("aDeclarationIsFoundOnATypeTheQueryTraversesTo", async () => {
  const view = await load("view");
  const client = typeClient({ heat: [{ key: "stage", type: "enum", options: STAGES }] });
  const axis = await view.axisDeclarationFor(client, TIMELINE_ROW, { axis_field: "stage" }, null);
  assertEqual(axis && axis.options.length, 3, "a traversed type is in scope too");
  assertEqual(view.typeKeysOf(TIMELINE_ROW).join("|"), "event|heat", "and both types are what scope means");
});

check("aRedrawOfAnUnchangedAxisCostsNoCall", async () => {
  const view = await load("view");
  const client = typeClient({ event: [{ key: "stage", type: "enum", options: STAGES }] });
  const held = { key: "stage", type: "enum", options: STAGES };
  assertEqual(await view.axisDeclarationFor(client, TIMELINE_ROW, { axis_field: "stage" }, held), held, "the axis already held is the answer");
  assertEqual(client.asked.length, 0, "and no type is read again");
});

check("onlyATimelineAsksForAnAxis", async () => {
  const view = await load("view");
  const client = typeClient({ event: [{ key: "stage", type: "enum", options: STAGES }] });
  const graphRow = { ...TIMELINE_ROW, renderer: "graph" };
  assertEqual(await view.axisDeclarationFor(client, graphRow, { axis_field: "stage" }, null), null, "a graph has no axis");
  assertEqual(await view.axisDeclarationFor(client, TIMELINE_ROW, {}, null), null, "and neither has a timeline with no axis_field");
  assertEqual(client.asked.length, 0, "neither costs a call");
});

// --- The addresses ----------------------------------------------------

check("everyAddressIsBuiltFromASlugAndAKey", async () => {
  const page = await load("page");
  assertEqual(page.gameURL("azeroth"), "/g/azeroth", "the game address");
  assertEqual(page.viewsURL("azeroth"), "/g/azeroth/views", "the views address");
  assertEqual(page.viewURL("azeroth", "world", ""), "/g/azeroth/v/world", "the view address");
  assertEqual(page.viewURL("azeroth", "world", "p.class=mage"), "/g/azeroth/v/world?p.class=mage", "a bound view");
  assertEqual(page.typesURL("azeroth"), "/g/azeroth/types", "the catalogue address");
  assertEqual(page.typeURL("azeroth", "quest"), "/g/azeroth/t/quest", "a type's address");
  assertEqual(page.entityURL("azeroth", "quest", "hogger"), "/g/azeroth/e/quest/hogger", "an entity's address");
  assertEqual(page.assetsURL("azeroth"), "/g/azeroth/assets", "the images address");
  // A key with a slash in it is one segment, encoded, and never two.
  assertEqual(page.entityURL("azeroth", "quest", "a/b"), "/g/azeroth/e/quest/a%2Fb", "a key containing a slash");
  assertEqual(page.slugOf("/g/azeroth/e/quest/hogger"), "azeroth", "reading the slug back");
  assertEqual(page.segmentsOf("/g/azeroth/e/quest/hogger").join("|"), "e|quest|hogger", "reading the segments back");
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
