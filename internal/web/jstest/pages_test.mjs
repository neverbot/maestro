// The harness for the seven page modules Task 15 put on seven routes:
// internal/web/static/pages/{home,views,view,types,catalogue,entity,assets}.js
// and the plumbing they share, pages/page.js.

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
function fakeElement(tag = "div") {
  const el = {
    tagName: tag,
    className: "",
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
    // The entity page builds an empty section for its log and swaps the
    // filled band into it once the server has answered, so the band's
    // place in the page is the builder's decision and not a race.
    replaceWith(...nodes) {
      const parent = this.parentNode;
      if (!parent) return;
      const at = parent.children.indexOf(this);
      if (at < 0) return;
      for (const node of nodes) {
        if (node && typeof node === "object") node.parentNode = parent;
      }
      parent.children.splice(at, 1, ...nodes);
    },
    setAttribute(name, value) {
      this.attributes.set(name, String(value));
    },
    // classList, because the shared row writes a class conditionally —
    // a numeric column, a sorted heading — after setting `className`
    // wholesale. A stub with only the second turns every such write into
    // "Cannot read properties of undefined", which is a harness crash
    // wearing the costume of a page defect.
    get classList() {
      const owner = this;
      const names = () => (owner.className === "" ? [] : owner.className.split(" "));
      return {
        add(...wanted) {
          const has = names();
          for (const name of wanted) if (!has.includes(name)) has.push(name);
          owner.className = has.join(" ");
        },
        remove(...unwanted) {
          owner.className = names().filter((name) => !unwanted.includes(name)).join(" ");
        },
        contains(name) {
          return names().includes(name);
        },
        toggle(name, on) {
          const wanted = on === undefined ? !names().includes(name) : Boolean(on);
          if (wanted) this.add(name);
          else this.remove(name);
          return wanted;
        },
      };
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
  // **Setting `textContent` clears the children**, as the real DOM does.
  // Without it a stub element kept the elements a page had just replaced
  // with a sentence, and "no control is offered any more" passed over a
  // control that was still in the tree — in the stub only.
  el._text = "";
  Object.defineProperty(el, "textContent", {
    enumerable: true,
    get() {
      if (this._text !== "") return this._text;
      return this.children.map((child) => child.textContent ?? "").join("");
    },
    set(value) {
      this._text = String(value);
      this.children = [];
    },
  });

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
  "connect-agent",
  "views",
  "views-error",
  "views-onboarding",
  "views-more",
  "view-list-note",
  "types",
  "types-empty",
  "relation-types",
  "relation-types-empty",
  "docs",
  "doc-kinds",
  "docs-empty",
  "docs-error",
  "docs-more",
  "catalogue-note",
  "catalogue-error",
  "type-name",
  "type-meta",
  "relation-type-name",
  "relation-type-meta",
  "relation-type-about",
  "relation-type-endpoints",
  "relation-type-role",
  "relation-type-fields",
  "relation-type-fields-empty",
  "relation-type-error",
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
  "view-builder",
  "entity-panel",
  // The trail that replaced four differently-worded back links. Every
  // shell inside a game declares it; the pages fill it through
  // pages/page.js setBreadcrumb.
  "crumbs",
];

const GAME = { id: "1e9d6b0c-4f7a-4a9e-8a5b-2c1d3e4f5a6b", slug: "azeroth", name: "Azeroth" };

// mount installs a document holding only the ids the shell under test
// declares, and a fetch that answers from a routing table.
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
    // A text node is an element with only text as far as this stub is
    // concerned: what an assertion reads is textContent either way.
    // An SVG element is created in its namespace; the stub records the
    // tag and the attributes, which is what the catalogue's share bar is
    // asserted on.
    createElementNS: (_ns, tag) => fakeElement(tag),
    createTextNode: (text) => {
      const node = fakeElement("#text");
      node.textContent = String(text);
      return node;
    },
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
  "connect-agent",
  "views",
  "views-error",
  "views-onboarding",
  "views-more",
  "types",
  "types-empty",
  "relation-types",
  "relation-types-empty",
  "docs",
  "doc-kinds",
  "docs-empty",
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

check("theHomeBandsAreContentOverviewsWritingInThatOrder", async () => {
  const { DESTINATIONS, DESTINATION_VIEWS, DESTINATION_CATALOGUE, DESTINATION_PROSE } = await load("page");
  assertEqual(
    DESTINATIONS.join("|"),
    [DESTINATION_CATALOGUE, DESTINATION_VIEWS, DESTINATION_PROSE].join("|"),
    "the three destinations are not Content, Overviews and Writing",
  );
  // And the shell puts its lanes in that order, which is the half a
  // constant cannot hold: the order a reader meets them in is the order
  // of the sections in game.html.
  const { readFileSync } = await import("node:fs");
  const shell = readFileSync(path.join(HERE, "..", "static", "game.html"), "utf8");
  const order = ["lane-catalogue", "lane-connections", "lane-views", "prose"].map((id) => shell.indexOf(`id="${id}"`));
  for (const at of order) assert(at > 0, "game.html is missing one of the four bands");
  assert(
    order[0] < order[1] && order[1] < order[2] && order[2] < order[3],
    `the bands are not in destination order: ${order}`,
  );
});

// **The pair stands side by side only when both halves have rows.** The
// three columns this page used to have failed empty by tripling one
// piece of bad news and burying the one action above them; the pairing
// is the same shape of decision in reverse, and it is made here rather
// than by the stylesheet, which only knows the window's width.
// **The connections band says what each verb joins, in the game's own
// words.** Twelve verbs with their counts and nothing else is a list a
// designer cannot judge: `odia a 11` is right or wrong depending on
// whether it joins races or zones. The endpoints arrive as keys, which
// is what an agent writes, and this screen speaks the game's.
check("aConnectionNamesWhatItJoinsInTheGamesOwnWords", async () => {
  const dom = mount({
    ids: HOME_IDS,
    pathname: "/g/azeroth",
    routes: [
      [
        (url) => url === base + "/summary",
        {
          body: summaryOf({
            relation_types: [
              {
                id: "c", key: "takes_place_in", label: "takes place in",
                relation_count: 12, invalid_count: 0,
                source_type_keys: ["quest"], target_type_keys: ["zone"],
              },
              // Neither end declared: "anything", which is a fact and is
              // written as the word rather than left blank.
              { id: "d", key: "mentions", label: "mentions", relation_count: 1, invalid_count: 0,
                source_type_keys: [], target_type_keys: [] },
            ],
          }),
        },
      ],
      noKinds, noDocs, noViews, events,
    ],
  });
  await load("home");

  const shown = text(dom.elements["relation-types"]);
  assert(shown.includes("Quests \u2192 Zones"), `the band does not name what the verb joins: ${JSON.stringify(shown)}`);
  assert(!shown.includes("quest \u2192 zone"), "the band names the endpoints by the key an agent writes");

  const undeclared = dom.elements["relation-types"].children.at(-1);
  const cell = undeclared.children.find((c) => (c.className || "").startsWith("catalogue-cell"));
  assert(cell.className.includes("absent"), `a type that declares neither end is not marked absent: ${cell.className}`);
  assert(cell.textContent.includes("anything"), `an undeclared end reads ${JSON.stringify(cell.textContent)}`);
});

check("aGameWithBothCataloguesPairsTheTwoHalvesOfItsMetamodel", async () => {
  const dom = mount({
    ids: HOME_IDS,
    pathname: "/g/azeroth",
    routes: [[(url) => url === base + "/summary", { body: summaryOf() }], noKinds, noDocs, noViews, events],
  });
  await load("home");
  assert(
    dom.elements.home.className.split(" ").includes("pair"),
    `a game with both catalogues does not pair: ${JSON.stringify(dom.elements.home.className)}`,
  );
});

check("aGameMissingEitherCatalogueStatesItsAbsenceOnceAndDoesNotPair", async () => {
  const dom = mount({
    ids: HOME_IDS,
    pathname: "/g/azeroth",
    routes: [
      [
        (url) => url === base + "/summary",
        // Types declared and nothing connecting them, which is where a
        // game that has just started is: the connections band is an
        // empty state, and an empty state in half the window beside a
        // table is the layout this pairing exists not to produce.
        { body: summaryOf({ relation_types: [], totals: { entities: 401, relations: 0, invalid: 3 } }) },
      ],
      noKinds, noDocs, noViews, events,
    ],
  });
  await load("home");
  assert(
    !dom.elements.home.className.split(" ").includes("pair"),
    `a game with one empty catalogue still pairs: ${JSON.stringify(dom.elements.home.className)}`,
  );
});

// **A sentence and a link, and no button.** Nothing in this interface
// writes a view, so a create control here would lead nowhere — the
// plan's O1, and the one piece of onboarding this product ships.
check("aGameWithNoViewsIsToldWhatAViewIsAndGetsNoCreateButtonInTheLane", async () => {
  const dom = mount({
    ids: HOME_IDS,
    pathname: "/g/azeroth",
    routes: [[(url) => url === base + "/summary", { body: summaryOf() }], noKinds, noDocs, noViews, events],
  });
  const { NO_VIEWS_HEADING, NO_VIEWS_SENTENCE, COMPOSE_LABEL } = await load("page");
  await load("home");
  const lane = dom.elements["views-onboarding"];
  const rendered = text(lane);
  assert(rendered.includes(NO_VIEWS_HEADING), `the onboarding heading is missing: ${JSON.stringify(rendered)}`);
  assert(rendered.includes(NO_VIEWS_SENTENCE), `the onboarding sentence is missing: ${JSON.stringify(rendered)}`);
  const anchors = links(lane);
  assertEqual(anchors.length, 1, "the onboarding is not exactly one link");
  // **The way out answers what this game already has.** This game holds
  // four hundred quests, so what it needs is the way to compose the
  // first overview, not an agent it already has writing into it.
  assertEqual(anchors[0].href, "/g/azeroth/views/new", "a game full of content is told to go and connect an agent");
  assertEqual(anchors[0].textContent, COMPOSE_LABEL, "the link is not labelled");
  assert(!lane.hidden, "the onboarding is hidden on a game with no views");

  // And nothing anywhere on the page offers to create one. A verb is
  // what a button that leads nowhere would be spelled with.
  const everything = [text(lane), text(dom.elements.views)].join(" ").toLowerCase();
  for (const verb of ["new view", "create", "add view", "save as"]) {
    assert(!everything.includes(verb), `the views lane offers "${verb}" on a page that cannot create a view`);
  }
});

// The other arm: nothing in the game yet, so the way out is the
// agent, and it stays inside this instance. It pointed at a README
// on GitHub, which is where the one person this product is for stops
// reading.
check("aGameWithNothingInItIsSentToConnectAnAssistant", async () => {
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
  const anchors = links(dom.elements["views-onboarding"]);
  assertEqual(anchors.length, 1, "the empty state offers no way out");
  assertEqual(anchors[0].href, "/g/azeroth/settings#agents", "the way in leaves the product");
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
      // The whole state's text: the role-dependent half is the last
      // sentence of the sentence the state carries, and the shell no
      // longer has a span of its own for it to be written into.
      types: dom.elements["types-empty"].textContent,
      docs: dom.elements["docs-empty"].textContent,
      // The views state branches on the role for the same reason, and
      // it started to the day the builder shipped: before that it said
      // only an agent could write a view, to everybody, which was a
      // sentence about a product that no longer existed.
      views: text(dom.elements["views-onboarding"]),
    };
    assert(!dom.elements["types-empty"].hidden, `the ${role} did not get the entity-types empty state`);
    assert(!dom.elements["docs-empty"].hidden, `the ${role} did not get the documents empty state`);
    assert(!dom.elements["views-onboarding"].hidden, `the ${role} did not get the views empty state`);
  }
  assert(
    sentences.viewer.types !== sentences.editor.types,
    "a viewer and an editor read the same sentence about declaring a type",
  );
  assert(
    sentences.viewer.docs !== sentences.editor.docs,
    "a viewer and an editor read the same sentence about writing a document",
  );
  assert(
    sentences.viewer.views !== sentences.editor.views,
    "a viewer and an editor read the same sentence about composing a view",
  );
  assert(
    sentences.editor.views.includes("put a simple one together here"),
    `the editor's sentence does not say they can make one: ${JSON.stringify(sentences.editor.views)}`,
  );
  for (const what of ["types", "docs", "views"]) {
    assert(
      sentences.viewer[what].includes("not change it"),
      `the viewer's ${what} sentence does not say they cannot change the game`,
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
  const { openGame, setRereadWindow } = await import("../static/pages/page.js");
  // A burst of events is coalesced into one re-read. The window is the
  // product's; a harness cannot wait three quarters of a second per
  // event and still drives the same path.
  setRereadWindow(0);
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
  "entities",
  "entities-empty",
  "entities-error",
  "entities-more",
  "crumbs",
];

// **A thousand rows at fifty a press is eighteen presses.** The pages
// grow instead: small first, because most visits end on the first page,
// then bigger for a reader who has said they are reading the whole
// thing. Four presses walk a thousand rows.
check("thePagesGrowSoAThousandRowsIsFourPressesNotEighteen", async () => {
  const { nextPageSize, PAGE_SIZES } = await load("catalogue");
  assertEqual(nextPageSize(0), 50, "the first page is small");
  assertEqual(nextPageSize(50), 200, "the second is bigger");
  assertEqual(nextPageSize(250), 500, "and the rest are the server's own cap");
  assertEqual(nextPageSize(750), 500, "which is where it stops");
  assertEqual(
    PAGE_SIZES[0] + PAGE_SIZES[1] + PAGE_SIZES[2],
    750,
    "three presses reach 750 of a thousand, so the fourth finishes it",
  );
});

// **One value, one spelling, on both screens that show it.** A quest
// with `repeatable: false` read "false" in the catalogue and "no" on its
// own page, and a list of tags read "elwynn,quest" here and
// "elwynn, quest" there — two spellings of one value, on two screens a
// designer moves between by clicking a row. The catalogue calls the
// entity page's own formatter now.
check("theCatalogueSpellsAValueTheWayTheEntityPageDoes", async () => {
  const dom = mount({
    ids: CATALOGUE_IDS,
    pathname: "/g/azeroth/t/quest",
    routes: [
      [(url) => url === base + "/types/by-key/quest", {
        body: {
          key: "quest", label: "Quest", label_plural: "Quests",
          field_schema: [
            { key: "repeatable", type: "bool" },
            { key: "tags", type: "list<text>" },
            { key: "min_level", type: "number" },
          ],
        },
      }],
      [(url) => url.startsWith(base + "/entities"), {
        body: {
          items: [{
            type_key: "quest", key: "hogger", name: "Wanted: Hogger",
            fields: { repeatable: false, tags: ["elwynn", "wanted"], min_level: 9 },
          }],
        },
      }],
      events,
    ],
  });
  await load("catalogue");
  const row = dom.elements["entities"].children.find((child) => child.className !== "catalogue-head");
  const cells = row.children.filter((child) => child.className.includes("catalogue-cell"));
  // The boolean is drawn on both screens and spelled on neither, which
  // is what this check is named for: one value, one spelling.
  assertEqual(
    JSON.stringify(cells.slice(0, 3).map((cell) => cell.textContent)),
    JSON.stringify(["", "elwynn, wanted", "9"]),
    "the catalogue's cells",
  );
  assertEqual(cells[0].getAttribute("aria-label"), "no", "what the drawn boolean is called");
});

check("aLongtextColumnIsAOneLineLeadInAndNotTheText", async () => {
  const dom = mount({
    ids: CATALOGUE_IDS,
    pathname: "/g/azeroth/t/quest",
    routes: [
      [(url) => url === base + "/types/by-key/quest", {
        body: {
          key: "quest", label: "Quest", label_plural: "Quests",
          field_schema: [
            { key: "summary", type: "longtext" },
            { key: "min_level", type: "number" },
          ],
        },
      }],
      [(url) => url.startsWith(base + "/entities"), {
        body: {
          items: [{
            type_key: "quest", key: "hogger", name: "Wanted: Hogger",
            fields: { summary: "A gnoll.\nIn Elwynn.", min_level: 9 },
          }],
        },
      }],
      events,
    ],
  });
  await load("catalogue");
  const row = dom.elements["entities"].children.find((child) => child.className !== "catalogue-head");
  const cells = row.children.filter((child) => child.className.includes("catalogue-cell"));
  // The row keeps its one line: a six-hundred character value in an
  // `auto` track made the column that wide and the row five lines tall,
  // and the whole of a value this long is read on the entity's own page.
  assert(
    cells[0].className.includes("oneline"),
    `a longtext column is not clipped: ${JSON.stringify(cells[0].className)}`,
  );
  assert(
    !cells[1].className.includes("oneline"),
    `a number column is clipped too: ${JSON.stringify(cells[1].className)}`,
  );
});

check("aBooleanColumnIsDrawnAndStillNamed", async () => {
  const dom = mount({
    ids: CATALOGUE_IDS,
    pathname: "/g/azeroth/t/quest",
    routes: [
      [(url) => url === base + "/types/by-key/quest", {
        body: {
          key: "quest", label: "Quest", label_plural: "Quests",
          field_schema: [
            { key: "repeatable", type: "bool" },
            { key: "min_level", type: "number" },
          ],
        },
      }],
      [(url) => url.startsWith(base + "/entities"), {
        body: {
          items: [
            { type_key: "quest", key: "hogger", name: "Hogger", fields: { repeatable: true, min_level: 9 } },
            { type_key: "quest", key: "kobold", name: "Kobold", fields: { repeatable: false, min_level: 3 } },
            { type_key: "quest", key: "unknown", name: "Unknown", fields: { min_level: 1 } },
          ],
        },
      }],
      events,
    ],
  });
  await load("catalogue");
  const rows = dom.elements["entities"].children.filter((child) => child.className !== "catalogue-head");
  const boolCell = (row) => row.children.filter((c) => (c.className || "").includes("catalogue-cell"))[0];
  assertEqual(
    JSON.stringify(rows.map((row) => boolCell(row).textContent)),
    JSON.stringify(["", "", "\u2014"]),
    "a boolean column spells no words, and a row with no value keeps the dash",
  );
  assertEqual(
    JSON.stringify(rows.slice(0, 2).map((row) => boolCell(row).getAttribute("aria-label"))),
    JSON.stringify(["yes", "no"]),
    "what the two marks are called",
  );
});

// --- The relation type's own page -------------------------------------

check("aRelationTypeSaysWhatItJoinsAndWhatAWalkMakesOfIt", async () => {
  const { endpointSentence, roleSentence, ANY_TYPE, NO_ROLE, NO_TRAITS } = await load("relation-type");

  assertEqual(
    endpointSentence(["quest"], ["zone"]),
    "From quest to zone.",
    "the endpoint sentence",
  );
  // **An empty endpoint list is a permission, not an omission.** The
  // server means "no restriction" by it, and a screen drawing it as a
  // blank would read as a missing answer.
  assertEqual(
    endpointSentence([], ["zone"]),
    `From ${ANY_TYPE} to zone.`,
    "an unrestricted source end",
  );
  assertEqual(
    endpointSentence(["quest", "class"], []),
    `From quest or class to ${ANY_TYPE}.`,
    "two allowed source types, and an unrestricted target end",
  );

  assertEqual(
    roleSentence("prerequisite", ["prerequisite_of", "acyclic"]),
    "Its role is prerequisite. A walk reads it as prerequisite_of, acyclic.",
    "a type the analysis engine has something to say about",
  );
  // Two statements and not one: a type can declare a role and no traits,
  // or traits and no role, and an undeclared trait list is not an empty
  // one — the server keeps that distinction and this page keeps it too.
  assertEqual(
    roleSentence("", []),
    `${NO_ROLE}. ${NO_TRAITS}.`,
    "a type that declares neither",
  );
  assertEqual(
    roleSentence("containment", []),
    `Its role is containment. ${NO_TRAITS}.`,
    "a role with no traits",
  );
});

check("theRelationTypePageDrawsTheDeclarationThroughThePage", async () => {
  const dom = mount({
    ids: SHELL_IDS,
    pathname: "/g/azeroth/rt/requires",
    routes: [
      [(url) => url === base + "/relation-types/by-key/requires", {
        body: {
          key: "requires", label: "Requires", description: "What must be done first.",
          source_type_keys: ["quest"], target_type_keys: ["quest"],
          semantic_role: "prerequisite", analysis_traits: ["prerequisite_of"],
          field_schema: [
            { key: "weight", type: "number", required: true },
            // A default of `false` is a declared default, and reading the
            // value for truth would draw "no default" over it — the very
            // defect internal/metamodel/schema.go grew HasDefault to
            // prevent, one layer up.
            { key: "hard", type: "bool", default: false },
          ],
        },
      }],
      [(url) => url.endsWith("/summary"), { body: { role: "editor", relation_types: [{ key: "requires", relation_count: 21 }] } }],
      events,
    ],
  });
  await load("relation-type");

  assertEqual(dom.elements["relation-type-name"].textContent, "Requires", "the type's label");
  assertEqual(dom.elements["relation-type-meta"].textContent, "requires · 21 relations", "the key and the count");
  assertEqual(
    dom.elements["relation-type-about"].textContent,
    "What must be done first.",
    "the description the server has always carried",
  );
  assertEqual(
    dom.elements["relation-type-endpoints"].children.map((child) => child.textContent).join(""),
    "From quest to quest.",
    "the endpoints, drawn through the page",
  );
  assert(
    dom.elements["relation-type-endpoints"].children.some((child) => child.href === "/g/azeroth/t/quest"),
    "each named type is a link to its own catalogue",
  );
  assertEqual(
    dom.elements["relation-type-role"].textContent,
    "Its role is prerequisite. A walk reads it as prerequisite_of.",
    "what a walk makes of it",
  );
  assertEqual(dom.elements["relation-type-fields"].hidden, false, "the field table stayed hidden");
  assertEqual(dom.elements["relation-type-fields-empty"].hidden, true, "the empty state showed over two fields");
  const cells = dom.elements["relation-type-fields"].children
    .slice(1)
    .map((line) => line.children.map((cell) => cell.textContent));
  assertEqual(
    JSON.stringify(cells),
    JSON.stringify([
      ["weight", "weight", "number", "required", "no default", ""],
      ["hard", "hard", "bool", "optional", "false", ""],
    ]),
    "the declared fields",
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
    "Azeroth / Content / Quests",
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
  const { absentTextFor } = await import("../static/render/twin.js");
  await load("entity");
  const rendered = text(dom.elements["entity-content"]);
  for (const field of QUEST_SCHEMA) {
    assert(rendered.includes(field.label), `the declared field ${field.key} is missing from the page`);
  }
  assert(rendered.includes("11"), "the field the entity carries is missing its value");
  // Two absences, each **named** — "no repeatable", "no summary" — because
  // the entity carries one of three declared fields. The mark used to be
  // an em dash for all of them, which is the one spelling of the Named
  // Absence Rule this product had that was not a word.
  for (const field of QUEST_SCHEMA) {
    if (field.key === "level") continue;
    assert(
      rendered.includes(absentTextFor(field.key)),
      `the declared-and-unset field ${field.key} is not named as absent`,
    );
  }
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
  // **The field key heads a column; the cell holds the value alone.** It
  // was `quantity 2` in a nameless cell, which is a header spelled once
  // per row and a column that could hold a different field on the row
  // under it.
  const head = rendered.indexOf("Nameidbindquantity");
  assert(head > -1, `the columns are not named: ${JSON.stringify(rendered)}`);
  assert(rendered.includes("item/cudgelon_pickup2"), `a cell repeats its own column's name: ${JSON.stringify(rendered)}`);
  // And the count is above the table rather than a row of it.
  const count = rendered.indexOf("1 relation");
  assert(count > -1 && count < head, "the count is inside the table it counts");
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
  // One band, two halves, and each half says which way the arrow runs
  // from where the reader is standing. **Incoming first**: what points at
  // a thing is what a designer opens its page to find.
  const band = rendered.indexOf("Related entities");
  const into = rendered.indexOf("Pointing here");
  const out = rendered.indexOf("Pointed at from here");
  assert(band > -1 && into > band && out > into, "the two halves are not inside one band in order");
  assert(rendered.indexOf("Wanted") > into && rendered.indexOf("Wanted") < out, "the incoming edge is in the wrong half");
  assert(rendered.indexOf("Cudgel") > out, "the outgoing edge is in the wrong half");
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
check("aViewOpensInTheBuilderOnlyWhenItsQueryRoundTrips", async () => {
  const dom = mount({ ids: ["view-builder"], pathname: "/g/azeroth/v/world", routes: [] });
  const { sayBuilderDoor, OPEN_IN_BUILDER } = await load("view");
  const { TOO_MUCH } = await import("../static/query/compose.js");
  const slot = dom.elements["view-builder"];

  sayBuilderDoor(globalThis.document, "azeroth", {
    key: "quests",
    renderer: "graph",
    query: { v: 1, from: [{ type: "quest" }], project: { color_by: "zone" } },
  });
  assertEqual(slot.hidden, false, "a view the builder can hold offered no way in");
  assertEqual(slot.children[0].textContent, OPEN_IN_BUILDER, "the way in");
  assertEqual(
    slot.children[0].href,
    "/g/azeroth/views/new?from=quests",
    "the door carries the view's key and not its document",
  );

  // A parameter is the first thing §3 says the builder does not hold.
  sayBuilderDoor(globalThis.document, "azeroth", {
    key: "mage-quests",
    renderer: "graph",
    query: { v: 1, params: [{ key: "class_key", type: "text" }], from: [{ type: "quest" }] },
  });
  assertEqual(slot.hidden, false, "a view the builder cannot hold said nothing at all");
  assertEqual(slot.textContent, TOO_MUCH, "the reason, in the spike's own words");
  assert(
    slot.children.length === 0,
    "a query the builder cannot hold still offered a control, which is the one thing §4 forbids",
  );
});

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
  assert(rendered.includes("400"), "the row lost its count");
  assert(rendered.includes("3 invalid"), "the row lost its invalid flag");
});

// **A count carries no unit.** The row beside it already says what these
// are, in the game's own word: "Deidades 27". A unit here could only be
// the model's ("27 entities"), which is the vocabulary this interface is
// getting rid of.
// **A catalogue says how much of the game a row is, not only how many.**
// Ordered alphabetically, RL-Aeternum's heaviest connection (45 of 95,
// nearly half the graph) sat ninth of twelve, and the key spent a whole
// column on the word an agent uses.
check("aCatalogueIsOrderedByWeightAndDrawsTheShare", async () => {
  const dom = mount({
    ids: HOME_IDS,
    pathname: "/g/azeroth",
    routes: [
      [
        (url) => url === base + "/summary",
        // Deliberately the wrong way round in the payload: the server
        // sends what it sends, and the order on screen is this page's.
        {
          body: summaryOf({
            entity_types: [
              { id: "b", key: "zone", label: "Zone", label_plural: "Zones", entity_count: 1, invalid_count: 0 },
              { id: "a", key: "quest", label: "Quest", label_plural: "Quests", entity_count: 400, invalid_count: 3 },
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

  // **The first child is the header, and it names every column.** The
  // band drew `Deidades · 27 · 43% · bar` with nothing over any of it:
  // 27 of what, 43% of what. rows.js grew headerRow for exactly that
  // and this page did not call it.
  const all = dom.elements.types.children;
  const head = all[0];
  assertEqual(head.className, "catalogue-head", "the catalogue has no header row");
  const headings = text(head);
  for (const column of ["Type", "Count", "Of the total"]) {
    assert(headings.includes(column), `the header does not name ${column}: ${JSON.stringify(headings)}`);
  }

  const rows = all.slice(1);
  const names = rows.map((li) => li.children[0].textContent);
  assertEqual(names[0], "Quests", `the heaviest kind is not first: ${JSON.stringify(names)}`);

  const shown = text(dom.elements.types);
  assert(shown.includes("100%"), `the share is not written: ${JSON.stringify(shown)}`);
  // The key is gone: a designer reads the game's own word here and meets
  // the key on the thing's own page.
  assert(!shown.includes("quest"), "the catalogue still spends a column on the key");

  // The bar is SVG because `default-src 'self'` drops an inline style,
  // and its width is a geometry attribute rather than CSS.
  const bar = rows[0].children.flatMap((c) => c.children || []).find((c) => c.tagName === "svg");
  assert(bar, "the row draws no share bar");
  const fill = (bar.children || []).find((r) => r.getAttribute("class") === "share-fill");
  assert(fill, "the bar has no fill");
  assert(Number(fill.getAttribute("width")) > 0, "the fill has no width");
});

check("aCountOnACatalogueRowIsANumberAndNotAModelWord", async () => {
  const dom = mount({
    ids: HOME_IDS,
    pathname: "/g/azeroth",
    routes: [[(url) => url === base + "/summary", { body: summaryOf() }], noKinds, noDocs, noViews, events],
  });
  await load("home");
  const rendered = text(dom.elements.types);
  assert(rendered.includes("400"), `the row lost its count: ${JSON.stringify(rendered)}`);
  for (const word of ["entity", "entities", "relation", "relations"]) {
    assert(!rendered.includes(word), `the catalogue says "${word}" to a game designer`);
  }
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

// **The crossing this page never made.** A designer opens a new game,
// pastes the command, and the agent starts writing. Every one of those
// writes was ignored here: the page went on showing the three steps and
// the bands stayed hidden until somebody reloaded, which is the moment a
// person decides the thing does not work.
check("aGameThatFillsUpLeavesItsEmptyStateOnItsOwn", async () => {
  let asked = 0;
  const dom = mount({
    ids: HOME_IDS.filter((id) => id !== "game-name"),
    pathname: "/g/azeroth",
    routes: [
      [
        (url) => url === base + "/summary",
        () => {
          asked += 1;
          // Empty on the first read, and holding a type on the second:
          // the agent wrote between them.
          return {
            body: asked === 1
              ? summaryOf({ entity_types: [], relation_types: [], totals: { entities: 0, relations: 0, invalid: 0 } })
              : summaryOf(),
          };
        },
      ],
      noKinds,
      noDocs,
      noViews,
      events,
    ],
  });
  const { home } = await load("home");
  const { openGame, setRereadWindow } = await import("../static/pages/page.js");
  const { applyEvent, REREAD, TARGET_CONTENT } = await import("../static/client.js");
  setRereadWindow(0);

  const surface = await home(await openGame());
  assert(dom.elements.home.hidden, "the bands show on a game with nothing in it");
  assert(!dom.elements["connect-agent"].hidden, "the steps are hidden on a game with nothing in it");

  // The decision is the real reducer's over the real event.
  const verdict = applyEvent({ views: {} }, { kind: "entity.upserted", data: { id: "e1" } });
  assertEqual(verdict.decision, REREAD, "a written entity is not a re-read");
  assertEqual(verdict.target, TARGET_CONTENT, "a written entity does not re-read the content");

  await surface.onEvent(verdict);
  assert(!dom.elements.home.hidden, "the bands stayed hidden after the game filled up");
  assert(dom.elements["connect-agent"].hidden, "the three steps stayed on a game that now has content");
  assert(
    text(dom.elements.types).includes("Quests"),
    `the catalogue did not re-read: ${JSON.stringify(text(dom.elements.types))}`,
  );
});

// **A game that is only writing is still a game.** "Empty" was a fact
// about the metamodel alone, so a game holding one document and no types
// hid every band, showed the three steps for connecting an agent,
// and said "Nothing in this game yet" with the document rendered inside
// the hidden band. Reported from a real instance.
check("aGameWithOnlyWritingIsNotAnEmptyGame", async () => {
  const dom = mount({
    ids: HOME_IDS,
    pathname: "/g/azeroth",
    routes: [
      [
        (url) => url === base + "/summary",
        { body: summaryOf({ entity_types: [], relation_types: [], totals: { entities: 0, relations: 0, invalid: 0 } }) },
      ],
      [(url) => url === base + "/docs/kinds", { body: { kinds: [{ kind: "lore", document_count: 1 }], unkinded: 0 } }],
      [(url) => url.startsWith(base + "/docs"), { body: { items: [{ id: "d1", path: "general.md", title: "General" }] } }],
      noViews,
      events,
    ],
  });
  await load("home");
  assert(!dom.elements.home.hidden, "the bands are hidden on a game that holds a document");
  assert(dom.elements["connect-agent"].hidden, "a game with writing in it was told to connect an agent");
  assert(
    text(dom.elements.docs).includes("general.md"),
    `the document is not on the page: ${JSON.stringify(text(dom.elements.docs))}`,
  );
  assert(
    dom.elements["game-summary"].textContent.includes("1 piece of writing"),
    `the line above the bands does not count the writing: ${JSON.stringify(dom.elements["game-summary"].textContent)}`,
  );
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
  assertEqual(dom.elements["game-summary"].textContent, "", "the totals line repeats what the band below says");
  assert(dom.elements["game-summary"].hidden, "the empty totals line is left occupying a line");
  assert(!dom.elements["types-empty"].hidden, "the entity-types empty state is hidden on an empty game");
  assert(!dom.elements["relation-types-empty"].hidden, "the relation-types empty state is hidden on an empty game");
  assert(dom.elements["doc-kinds"].hidden, "the kind line shows on a game with no kinds");
});

// --- The first sight of a picture -------------------------------------
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

// **The fit has to happen when the canvas has a size, and the canvas has
// no size until the window's width has decided there is a picture.**
check("theFitWaitsForTheWidthToDecideThereIsAPicture", async () => {
  const view = await load("view");
  const canvas = fakeCanvas([{ width: 800, height: 600 }]);
  canvas.outside = () => ({ total: 2, hidden: 0 });
  const outsideEl = fakeElement("p");
  const state = {
    canvas,
    scene: { marks: FIT_MARKS },
    pictured: true,
    narrow: false,
    outsideEl,
  };
  await view.fitAfterLayout(state, { settle: async () => {} });
  assertEqual(state.fitted, true, "a drawn picture in a wide window was not fitted");
  assert(canvas.view.k > 0 && canvas.view.k <= view.FIT_MAX_ZOOM, "it zoomed within the cap");
  assertEqual(outsideEl.textContent, "", "a fitted view claimed something was outside it");
});

check("aNarrowWindowFitsNothingAndDoesNotRecordItself", async () => {
  const view = await load("view");
  const canvas = fakeCanvas([{ width: 800, height: 600 }]);
  const narrow = { canvas, scene: { marks: FIT_MARKS }, pictured: true, narrow: true };
  await view.fitAfterLayout(narrow, { settle: async () => {} });
  assertEqual(narrow.fitted, undefined, "a window with no drawing in it recorded a fit");
  assertEqual(canvas.measured, 0, "and measured a canvas that is not on screen");

  // The same for a renderer that drew a table rather than a picture.
  const tabular = { canvas, scene: { marks: FIT_MARKS }, pictured: false, narrow: false };
  await view.fitAfterLayout(tabular, { settle: async () => {} });
  assertEqual(tabular.fitted, undefined, "a table view recorded a fit it never needed");
});

// **A picture that does not contain its answer says so.** Fitting is the
// fix; this is the admission, for the designer who has zoomed in on
// purpose and for the view that could not be fitted at all.
check("aViewThatIsNotShowingEverythingSaysHowMuch", async () => {
  const view = await load("view");
  const outsideEl = fakeElement("p");
  const said = view.sayOutside({
    canvas: { outside: () => ({ total: 105, hidden: 48 }) },
    outsideEl,
  });
  assertEqual(said, "48 of 105 nodes are outside the view.", "the sentence did not name both numbers");
  assertEqual(outsideEl.hidden, false, "and it was not shown");

  const quiet = fakeElement("p");
  view.sayOutside({ canvas: { outside: () => ({ total: 105, hidden: 0 }) }, outsideEl: quiet });
  assertEqual(quiet.textContent, "", "a view showing everything still said something");
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
    // JSTEST_STACK=1 adds the stack. A failure here is usually a module
    // throwing several frames down, and the message alone sends whoever
    // reads it hunting through four files for a `.map`.
    console.error(
      "FAIL " + name + ": " + (error && error.message ? error.message : error) +
        (process.env.JSTEST_STACK ? "\n" + error.stack : ""),
    );
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
