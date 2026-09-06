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
  const el = {
    tagName: tag,
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
  // `hidden` counts its writes, so assertHidden/assertVisible below can
  // tell "app.js set this" apart from "the stub started it here".
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

// assertHidden and assertVisible are the only way this file asks about
// visibility, and they refuse to pass on the stub's initial state.
//
// A visibility assertion the code under test never wrote is an assertion
// about this stub — it goes green whether or not the page does anything,
// and deleting the line that should have set the flag changes nothing.
// One of those was live in this file: "the page revealed an empty
// catalogue after a failed summary" held only because the stub had
// hidden #game-content itself. Counting writes makes that a loud
// failure instead of a tick.
function assertHidden(el, id, why) {
  if (el.hiddenWrites === 0) {
    fail(`#${id}: nothing under test ever wrote .hidden, so "${why}" would be an assertion on the stub's own initialisation`);
  }
  if (!el.hidden) {
    fail(why);
  }
}

function assertVisible(el, id, why) {
  if (el.hiddenWrites === 0) {
    fail(`#${id}: nothing under test ever wrote .hidden, so "${why}" would be an assertion on the stub's own initialisation`);
  }
  if (el.hidden) {
    fail(why);
  }
}

// text flattens a rendered subtree the way a reader sees it.
function text(node) {
  const own = node.textContent ?? "";
  return own + node.children.map(text).join(" ");
}

async function runCase({
  summary,
  summaryStatus = 200,
  docPages = [{ items: [] }],
  docsStatus = 200,
  kinds = { kinds: [], documents: 0, unkinded: 0 },
  kindsStatus = 200,
}) {
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
    "doc-kinds": fakeElement("p"),
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
  // game.html ships the body hidden; the stub copies that and then
  // zeroes the write, so every write the assertions can see is app.js's.
  elements["game-content"].hidden = true;
  // game.html ships the kind line hidden too, for the same reason.
  elements["doc-kinds"].hidden = true;
  for (const el of Object.values(elements)) {
    if (el) el.hiddenWrites = 0;
  }

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
    // Before the /docs prefix below, which would otherwise swallow it.
    if (url === `/api/games/${game.id}/docs/kinds`) {
      if (kindsStatus !== 200) {
        return {
          ok: false,
          status: kindsStatus,
          json: async () => ({ error: "internal_error", message: "the kinds could not be counted" }),
        };
      }
      return { ok: true, status: 200, json: async () => kinds };
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
    kinds: {
      kinds: [
        { kind: "lore", document_count: 42 },
        { kind: "<img src=x onerror=alert(1)>script", document_count: 3 },
      ],
      documents: 46,
      unkinded: 1,
    },
    summary: {
      entity_types: [
        { id: "a", key: "quest", label: "Quest", label_plural: "<img src=x onerror=alert(1)>Quests", entity_count: 400, invalid_count: 3 },
        { id: "b", key: "zone", label: "Zone", label_plural: "Zones", entity_count: 1, invalid_count: 0 },
      ],
      relation_types: [{ id: "c", key: "takes_place_in", label: "takes place in", relation_count: 12, invalid_count: 2 }],
      totals: { entities: 401, relations: 12, invalid: 5 },
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
  if (rendered.includes("2 invalid")) {
    fail(`the entity catalogue is showing the relation types' invalid count: ${JSON.stringify(rendered)}`);
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
  // Since 0009 an edge can stop fitting its relation type's field schema
  // too, so this row carries the same flag the entity rows do. It read a
  // hard-coded zero while an edge could not be invalid, and leaving it
  // there would hide half of what the totals line below counts.
  if (!text(elements["relation-types"]).includes("2 invalid")) {
    fail(`the relation type row does not flag its invalid edges: ${JSON.stringify(text(elements["relation-types"]))}`);
  }
  const totals = elements["game-summary"].textContent;
  if (!totals.includes("401 entities") || !totals.includes("12 relations") || !totals.includes("5 no longer fit")) {
    fail(`the totals line reads ${JSON.stringify(totals)}`);
  }
  assertVisible(elements["game-content"], "game-content", "the page body is still hidden after a successful summary");
  assertHidden(elements["types-empty"], "types-empty", "an empty state is showing on a game that has content");
  assertHidden(elements["relation-types-empty"], "relation-types-empty", "an empty state is showing on a game that has content");
  // The whole page is four requests — the game list, the summary, the
  // document-kind catalogue and the first page of documents — and none
  // of them is an entity or relation listing: that is the property that
  // keeps a game with four hundred entities rendering like a game with
  // four. The documents *are* a listing, deliberately and boundedly: it
  // is a keyset page with a cursor, not the whole game. The kind
  // catalogue is a grouped count whose size is the number of kinds, not
  // the number of documents, which is why it can sit beside them here.
  if (requested.length !== 4 || requested.some((url) => url.includes("/entities") || url.includes("/relations"))) {
    fail(`the page fetched ${JSON.stringify(requested)}, want the game list, the summary, the kind catalogue and one page of documents`);
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
  assertHidden(elements["docs-empty"], "docs-empty", "the documents empty state is showing on a game that has documents");
  assertHidden(elements["docs-error"], "docs-error", "the documents error line is showing after a successful listing");
  assertHidden(elements["docs-more"], "docs-more", "the paging button is offered when the server issued no cursor");

  // The kind vocabulary above the list. It is the only place on this
  // page a designer can learn which kinds their own prose uses, because
  // Maestro ships none — so a missing line here is a missing feature,
  // not a missing decoration. The counts are beside the names, the
  // unkinded remainder is named, and a crafted kind reaches the page as
  // text like every other value a game supplies.
  const kindLine = elements["doc-kinds"].textContent;
  if (!kindLine.includes("lore 42") || !kindLine.includes("1 with no kind")) {
    fail(`the kind line reads ${JSON.stringify(kindLine)}`);
  }
  if (!kindLine.includes("<img src=x onerror=alert(1)>script 3")) {
    fail(`a game's kind did not reach the page as text: ${JSON.stringify(kindLine)}`);
  }
  assertVisible(elements["doc-kinds"], "doc-kinds", "the kind line is hidden on a game that has kinds");
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

  assertVisible(elements["docs-more"], "docs-more", "the server issued a cursor and the page offered no way to ask for the next page");
  await elements["docs-more"].click();
  if (!text(elements.docs).includes("B")) {
    fail(`the second page was not appended: ${JSON.stringify(text(elements.docs))}`);
  }
  if (!text(elements.docs).includes("A")) {
    fail("the second page replaced the first instead of appending to it");
  }
  await elements["docs-more"].click();
  assertHidden(elements["docs-more"], "docs-more", "the empty page after the last row did not retire the paging button");
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

  assertVisible(elements["docs-error"], "docs-error", "a failed documents listing said nothing");
  if (elements["docs-error"].textContent !== "the documents could not be listed") {
    fail(`the failure message reads ${JSON.stringify(elements["docs-error"].textContent)}`);
  }
  assertHidden(
    elements["docs-empty"],
    "docs-empty",
    "a failed documents listing rendered the empty state, which claims the game has no documents",
  );
  assertHidden(elements["docs-more"], "docs-more", "a failed documents listing still offers to fetch more");
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

  assertVisible(elements["types-empty"], "types-empty", "a game with no types is missing its empty state");
  assertVisible(elements["relation-types-empty"], "relation-types-empty", "a game with no types is missing its empty state");
  assertHidden(elements.types, "types", "an empty catalogue list is still showing");
  assertHidden(elements["relation-types"], "relation-types", "an empty catalogue list is still showing");
  if (elements["game-summary"].textContent !== "No content yet.") {
    fail(`the totals line for an empty game reads ${JSON.stringify(elements["game-summary"].textContent)}`);
  }
  assertVisible(
    elements["game-content"],
    "game-content",
    "the page body is hidden on an empty game, so its empty states are invisible",
  );
  // A game with no kinds gets no line at all, rather than the words "no
  // kinds": the documents empty state right below already says this game
  // has no prose, and a second sentence saying it again in a vocabulary
  // nobody has yet reads as a fault.
  assertHidden(elements["doc-kinds"], "doc-kinds", "the kind line is showing on a game with no kinds");
  if (elements["doc-kinds"].textContent !== "") {
    fail(`the kind line on an empty game reads ${JSON.stringify(elements["doc-kinds"].textContent)}`);
  }
}

// Case 2b: prose that is all unfiled. Every document has a kind of "",
// which is not a kind: the catalogue is empty and there is nothing to
// show, even though the game has documents. A line reading "Kinds:" with
// nothing after it would be worse than none.
{
  const { elements } = await runCase({
    docPages: [{ items: [{ id: "d1", path: "notes/a", title: "A note", version: 1 }] }],
    kinds: { kinds: [], documents: 1, unkinded: 1 },
    summary: {
      entity_types: [],
      relation_types: [],
      totals: { entities: 0, relations: 0, invalid: 0 },
      role: "owner",
    },
  });

  assertHidden(elements["doc-kinds"], "doc-kinds", "the kind line is showing for a game whose prose is all unfiled");
  assertHidden(elements["docs-empty"], "docs-empty", "the documents empty state is showing on a game with a document");
}

// Case 2c: the kind catalogue fails while the documents list succeeds.
// The line stays hidden rather than showing a wrong vocabulary, and the
// documents below still render: a missing summary above them is a
// smaller lie than a stale one, and it must not take the list with it.
{
  const { elements } = await runCase({
    docPages: [{ items: [{ id: "d1", path: "lore/a", title: "Some lore", kind: "lore", version: 1 }] }],
    kindsStatus: 500,
    summary: {
      entity_types: [],
      relation_types: [],
      totals: { entities: 0, relations: 0, invalid: 0 },
      role: "owner",
    },
  });

  assertHidden(elements["doc-kinds"], "doc-kinds", "the kind line is showing after its request failed");
  if (!text(elements.docs).includes("lore/a")) {
    fail(`a failed kind catalogue took the documents list with it: ${JSON.stringify(text(elements.docs))}`);
  }
  assertHidden(elements["docs-error"], "docs-error", "a failed kind catalogue was reported as a documents failure");
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
  assertHidden(elements["game-content"], "game-content", "the page revealed an empty catalogue after a failed summary");
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
  assertVisible(editor.elements["docs-empty"], "docs-empty", "a game with no documents is missing its empty state");
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
