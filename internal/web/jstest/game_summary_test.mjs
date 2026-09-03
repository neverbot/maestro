// Regression harness for the game home page: it drives the real,
// unmodified internal/web/static/app.js in a minimal DOM stub, the same
// way the two harnesses beside it do, and asserts what the page actually
// renders from GET /api/games/{game}/summary.
//
// A Go test cannot cover this. The summary endpoint has its own tests in
// internal/web/api_metamodel_test.go; what those cannot see is whether
// the page puts a game's own strings on screen as text or as markup,
// whether an empty game gets its empty state instead of two blank lists,
// and whether the counts a designer reads are the counts the server
// sent. Every one of those is a property of this file's consumer, not of
// the API.
//
// Run directly: `node internal/web/jstest/game_summary_test.mjs`.
// internal/web/static_appjs_browser_test.go shells out to it too.

const ORIGIN = "http://localhost:8124";

function fail(message) {
  console.error("FAIL: " + message);
  process.exit(1);
}

// fakeElement is the smallest thing app.js's DOM writes need: text,
// children, a hidden flag and a class. Nothing here can interpret a
// string as markup, which is exactly the point — a harness with an
// innerHTML setter would let a regression through by emulating one.
function fakeElement(tag = "div") {
  return {
    tagName: tag,
    hidden: false,
    className: "",
    textContent: "",
    children: [],
    append(...nodes) {
      this.children.push(...nodes);
    },
    prepend(...nodes) {
      this.children.unshift(...nodes);
    },
    replaceChildren(...nodes) {
      this.children = [...nodes];
    },
    disabled: false,
    listeners: {},
    addEventListener(name, handler) {
      (this.listeners[name] ??= []).push(handler);
    },
    // click drives the real handler app.js registered, so a paging
    // button is tested by pressing it rather than by asserting it was
    // wired.
    async click() {
      for (const handler of this.listeners.click ?? []) {
        await handler();
      }
    },
    querySelector() {
      return null;
    },
  };
}

// text flattens a rendered subtree the way a reader sees it.
function text(node) {
  const own = node.textContent ?? "";
  return own + node.children.map(text).join(" ");
}

async function runCase({ summary, summaryStatus = 200, docPages = [{ items: [] }], docsStatus = 200 }) {
  const elements = {
    "game-name": fakeElement("h1"),
    "game-summary": fakeElement("p"),
    "game-content": fakeElement("section"),
    types: fakeElement("ul"),
    "types-empty": fakeElement("p"),
    "types-empty-action": fakeElement("span"),
    "relation-types": fakeElement("ul"),
    "relation-types-empty": fakeElement("p"),
    docs: fakeElement("ul"),
    "docs-empty": fakeElement("p"),
    "docs-empty-action": fakeElement("span"),
    "docs-error": fakeElement("p"),
    "docs-more": fakeElement("button"),
    // The picker's own ids, absent on this page.
    games: null,
    status: null,
    "empty-state": null,
    "create-game": null,
    "create-game-error": null,
    login: null,
    invite: null,
  };
  elements["game-content"].hidden = true;

  const body = fakeElement("body");
  globalThis.document = {
    title: "",
    body,
    createElement: (tag) => fakeElement(tag),
    getElementById(id) {
      return Object.prototype.hasOwnProperty.call(elements, id) ? elements[id] : null;
    },
  };

  const location = { hash: "", pathname: "/g/azeroth", search: "", origin: ORIGIN, href: "" };
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

  const game = { id: "1e9d6b0c-4f7a-4a9e-8a5b-2c1d3e4f5a6b", slug: "azeroth", name: "Azeroth" };
  const requested = [];
  globalThis.fetch = async (url) => {
    requested.push(url);
    if (url === "/api/games") {
      return { ok: true, status: 200, json: async () => ({ games: [game] }) };
    }
    if (url === `/api/games/${game.id}/summary`) {
      return {
        ok: summaryStatus === 200,
        status: summaryStatus,
        json: async () => summary,
      };
    }
    if (url.startsWith(`/api/games/${game.id}/docs`)) {
      if (docsStatus !== 200) {
        return {
          ok: false,
          status: docsStatus,
          json: async () => ({ error: "internal_error", message: "the documents could not be listed" }),
        };
      }
      // The cursor decides which page answers, exactly as the keyset
      // listing does: no cursor is the first page, and the cursor the
      // previous page issued is the next one.
      const cursor = new URLSearchParams(url.slice(url.indexOf("?") + 1)).get("cursor");
      const index = cursor ? Number(cursor) : 0;
      const page = docPages[index] ?? { items: [] };
      return { ok: true, status: 200, json: async () => page };
    }
    return { ok: false, status: 404, json: async () => ({ error: "not_found", message: "unexpected fetch: " + url }) };
  };

  await import(`../static/app.js?case=${encodeURIComponent(JSON.stringify(summary).slice(0, 40))}${Math.random()}`);
  return { elements, requested, game };
}

// Case 1: a game with content. The counts on screen are the counts the
// server sent, a crafted label reaches the DOM as text, and the page
// never asks for a listing to build a summary.
{
  const { elements, requested } = await runCase({
    docPages: [
      {
        items: [
          { id: "d1", path: "lore/duskwood", title: "<img src=x onerror=alert(1)>Duskwood", kind: "lore", version: 3 },
          { id: "d2", path: "scripts/wanted-hogger", title: "Wanted: Hogger", version: 1 },
        ],
      },
    ],
    summary: {
      entity_types: [
        { id: "a", key: "quest", label: "Quest", label_plural: "<img src=x onerror=alert(1)>Quests", entity_count: 400, invalid_count: 3 },
        { id: "b", key: "zone", label: "Zone", label_plural: "Zones", entity_count: 1, invalid_count: 0 },
      ],
      relation_types: [{ id: "c", key: "takes_place_in", label: "takes place in", relation_count: 12 }],
      totals: { entities: 401, relations: 12, invalid: 3 },
    },
  });

  const rendered = text(elements.types);
  if (!rendered.includes("400 entities")) {
    fail(`the quest row does not carry its count: ${JSON.stringify(rendered)}`);
  }
  if (!rendered.includes("1 entity") || rendered.includes("1 entities")) {
    fail(`a count of one is not spelled in the singular: ${JSON.stringify(rendered)}`);
  }
  if (!rendered.includes("3 invalid")) {
    fail(`the invalid rows are not flagged: ${JSON.stringify(rendered)}`);
  }
  // The crafted label survives verbatim as *text*: the stub has no way
  // to interpret markup, so finding the raw string proves it went
  // through textContent rather than being parsed into elements.
  if (!rendered.includes("<img src=x onerror=alert(1)>Quests")) {
    fail(`a game's label did not reach the page as text: ${JSON.stringify(rendered)}`);
  }
  if (!text(elements["relation-types"]).includes("12 relations")) {
    fail(`the relation type row does not carry its count: ${JSON.stringify(text(elements["relation-types"]))}`);
  }
  const totals = elements["game-summary"].textContent;
  if (!totals.includes("401 entities") || !totals.includes("12 relations") || !totals.includes("3 no longer fit")) {
    fail(`the totals line reads ${JSON.stringify(totals)}`);
  }
  if (elements["game-content"].hidden) {
    fail("the page body is still hidden after a successful summary");
  }
  if (!elements["types-empty"].hidden || !elements["relation-types-empty"].hidden) {
    fail("an empty state is showing on a game that has content");
  }
  // The whole page is three requests — the game list, the summary and
  // the first page of documents — and none of them is an entity or
  // relation listing: that is the property that keeps a game with four
  // hundred entities rendering like a game with four. The documents
  // *are* a listing, deliberately and boundedly: it is a keyset page
  // with a cursor, not the whole game.
  if (requested.length !== 3 || requested.some((url) => url.includes("/entities") || url.includes("/relations"))) {
    fail(`the page fetched ${JSON.stringify(requested)}, want the game list, the summary and one page of documents`);
  }

  // The documents catalogue: a title reaches the page as text (the same
  // proof the entity-type labels get — the stub cannot parse markup, so
  // finding the raw string means it went through textContent), the path
  // is beside it, and a document with no kind renders without inventing
  // one.
  const docs = text(elements.docs);
  if (!docs.includes("<img src=x onerror=alert(1)>Duskwood")) {
    fail(`a document title did not reach the page as text: ${JSON.stringify(docs)}`);
  }
  if (!docs.includes("lore/duskwood") || !docs.includes("scripts/wanted-hogger")) {
    fail(`the documents catalogue is missing a path: ${JSON.stringify(docs)}`);
  }
  if (docs.includes("undefined") || docs.includes("null")) {
    fail(`a document with no kind rendered a placeholder: ${JSON.stringify(docs)}`);
  }
  // Each row links into the reading view, with the document's path in
  // the query string and never in a URL segment — the same shape the API
  // uses, for the same reason.
  const first = elements.docs.children[0].children[0];
  if (first.href !== "/g/azeroth/doc?path=lore%2Fduskwood") {
    fail(`the first document links to ${JSON.stringify(first.href)}`);
  }
  if (!elements["docs-empty"].hidden) {
    fail("the documents empty state is showing on a game that has documents");
  }
  if (!elements["docs-error"].hidden) {
    fail("the documents error line is showing after a successful listing");
  }
  if (!elements["docs-more"].hidden) {
    fail("the paging button is offered when the server issued no cursor");
  }
}

// Case 1b: paging. The listing issues a cursor whenever a page came back
// full, so the page that reports the end of a listing is the empty one
// after the last row — pressing the button is what proves this page
// follows that rule rather than hiding the button on a short page.
{
  const { elements, requested } = await runCase({
    docPages: [
      { items: [{ id: "d1", path: "a", title: "A" }], next_cursor: "1" },
      { items: [{ id: "d2", path: "b", title: "B" }], next_cursor: "2" },
      { items: [] },
    ],
    summary: {
      entity_types: [],
      relation_types: [],
      totals: { entities: 0, relations: 0, invalid: 0 },
      role: "editor",
    },
  });

  if (elements["docs-more"].hidden) {
    fail("the server issued a cursor and the page offered no way to ask for the next page");
  }
  await elements["docs-more"].click();
  if (!text(elements.docs).includes("B")) {
    fail(`the second page was not appended: ${JSON.stringify(text(elements.docs))}`);
  }
  if (!text(elements.docs).includes("A")) {
    fail("the second page replaced the first instead of appending to it");
  }
  await elements["docs-more"].click();
  if (!elements["docs-more"].hidden) {
    fail("the empty page after the last row did not retire the paging button");
  }
  if (!requested.some((url) => url.includes("cursor=2"))) {
    fail(`the page never followed the second cursor: ${JSON.stringify(requested)}`);
  }
}

// Case 1c: the documents listing fails. The server's own message shows,
// and neither the list nor the empty state does — an empty catalogue and
// a request that never answered look identical otherwise, and only one
// of them means "this game has no documents".
{
  const { elements } = await runCase({
    docsStatus: 500,
    summary: {
      entity_types: [],
      relation_types: [],
      totals: { entities: 0, relations: 0, invalid: 0 },
      role: "editor",
    },
  });

  if (elements["docs-error"].hidden) {
    fail("a failed documents listing said nothing");
  }
  if (elements["docs-error"].textContent !== "the documents could not be listed") {
    fail(`the failure message reads ${JSON.stringify(elements["docs-error"].textContent)}`);
  }
  if (!elements["docs-empty"].hidden) {
    fail("a failed documents listing rendered the empty state, which claims the game has no documents");
  }
  if (!elements["docs-more"].hidden) {
    fail("a failed documents listing still offers to fetch more");
  }
}

// Case 2: a brand new game. Both empty states show, the lists are
// hidden, and the totals line says so in words rather than showing
// zeros.
{
  const { elements } = await runCase({
    summary: {
      entity_types: [],
      relation_types: [],
      totals: { entities: 0, relations: 0, invalid: 0 },
      role: "owner",
    },
  });

  if (elements["types-empty"].hidden || elements["relation-types-empty"].hidden) {
    fail("a game with no types is missing its empty state");
  }
  if (!elements.types.hidden || !elements["relation-types"].hidden) {
    fail("an empty catalogue list is still showing");
  }
  if (elements["game-summary"].textContent !== "No content yet.") {
    fail(`the totals line for an empty game reads ${JSON.stringify(elements["game-summary"].textContent)}`);
  }
  if (elements["game-content"].hidden) {
    fail("the page body is hidden on an empty game, so its empty states are invisible");
  }
}

// Case 3: the summary fails. The game's name stays, and the server's own
// message replaces the totals — never a silently empty catalogue, which
// reads exactly like a game with nothing in it.
{
  const { elements } = await runCase({
    summaryStatus: 500,
    summary: { error: "internal_error", message: "the server could not complete the request" },
  });

  if (elements["game-name"].textContent !== "Azeroth") {
    fail(`the game name was lost on a failed summary: ${JSON.stringify(elements["game-name"].textContent)}`);
  }
  if (elements["game-summary"].textContent !== "the server could not complete the request") {
    fail(`the failure message reads ${JSON.stringify(elements["game-summary"].textContent)}`);
  }
  if (!elements["game-content"].hidden) {
    fail("the page revealed an empty catalogue after a failed summary");
  }
}

// Case 4: the empty state tells its reader what that reader can do.
// An editor is told how to declare the first type; a viewer, whose
// request those same routes refuse, is told to ask someone who can
// rather than to go and do it. The page used to show the editor's
// sentence to everyone.
{
  const empty = { entity_types: [], relation_types: [], totals: { entities: 0, relations: 0, invalid: 0 } };

  const editor = await runCase({ summary: { ...empty, role: "editor" } });
  const editorAction = editor.elements["types-empty-action"].textContent;
  if (!editorAction.includes("MCP")) {
    fail(`an editor is not told how to declare a type: ${JSON.stringify(editorAction)}`);
  }

  const viewer = await runCase({ summary: { ...empty, role: "viewer" } });
  const viewerAction = viewer.elements["types-empty-action"].textContent;
  if (viewerAction === editorAction) {
    fail("a viewer is shown the editor's sentence, which names two things a viewer cannot do");
  }
  if (!viewerAction.includes("viewer")) {
    fail(`a viewer is not told why they cannot: ${JSON.stringify(viewerAction)}`);
  }
  if (viewerAction.includes("You declare")) {
    fail(`a viewer is still told to declare a type: ${JSON.stringify(viewerAction)}`);
  }

  // The documents empty state follows the same rule, and it is a
  // separate sentence written by a separate function, so it needs its
  // own assertion rather than being assumed from the one above.
  const editorDocs = editor.elements["docs-empty-action"].textContent;
  const viewerDocs = viewer.elements["docs-empty-action"].textContent;
  if (editor.elements["docs-empty"].hidden) {
    fail("a game with no documents is missing its empty state");
  }
  if (!editorDocs.includes("MCP")) {
    fail(`an editor is not told how a document gets written: ${JSON.stringify(editorDocs)}`);
  }
  if (viewerDocs === editorDocs) {
    fail("a viewer is shown the editor's sentence about writing documents, which the server refuses them");
  }
  if (!viewerDocs.includes("viewer") || !viewerDocs.includes("refuse")) {
    fail(`a viewer is not told the write would be refused: ${JSON.stringify(viewerDocs)}`);
  }
  if (viewerDocs.includes("You write")) {
    fail(`a viewer is still told to write a document: ${JSON.stringify(viewerDocs)}`);
  }
}

console.log("ok: game page renders counts, documents, empty states, roles, paging and failures");
