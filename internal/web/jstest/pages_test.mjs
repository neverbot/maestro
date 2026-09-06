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
  "views-link",
  "types-link",
  "assets-link",
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
  "entity-panel",
  "back-to-game",
  "back-to-views",
  "back-to-types",
  "back-to-type",
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
  "views-link",
  "types-link",
  "assets-link",
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
  const { NO_VIEWS_SENTENCE, SKILL_BUNDLE_HREF, SKILL_BUNDLE_LABEL } = await load("page");
  await load("home");
  const lane = dom.elements["views-onboarding"];
  const rendered = text(lane);
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
  "back-to-types",
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

const ENTITY_IDS = ["entity-name", "entity-address", "entity-error", "entity-content", "back-to-type"];

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
  const out = rendered.indexOf("Relations out");
  const into = rendered.indexOf("Relations in");
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
  };
  wire(globalThis.document, dom.elements["entity-panel"], GAME.slug, client({ slug: GAME.slug }), state);

  await surface.dispatch("pointerdown", { target: node, clientX: 10, clientY: 10 });
  await surface.dispatch("pointerup", { target: node, clientX: 10, clientY: 10 });

  assertEqual(selected.length, 1, "the click selected no node");
  assert(!dom.elements["entity-panel"].hidden, "the click did not open the panel");
  assertEqual(dom.navigations.length, 0, `a node click navigated: ${dom.navigations}`);
  assert(root.children.includes(surface), "the canvas was unmounted by a node click");
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
