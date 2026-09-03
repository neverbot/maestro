// Regression harness for the reading view: it drives the real,
// unmodified internal/web/static/doc.js in a minimal DOM stub, the same
// way the harnesses beside it drive app.js, and asserts what the page
// actually renders from GET /docs/rendered, /members, /summary,
// /docs/history and /docs/comparison, and what it actually sends to
// /docs/revert.
//
// A Go test cannot cover any of it. The routes have their own tests in
// internal/web/api_docs_test.go; what those cannot see is whether the
// rendered HTML reaches the page as markup while every *other* string
// reaches it as text, whether a history row shows a person's name
// instead of a uuid, whether a revert sends the document's current
// version as expected_version (the compare-and-set that stops it
// overwriting somebody else's save) and whether a conflict is shown in
// the server's own words.
//
// Run directly: `node internal/web/jstest/document_page_test.mjs`.
// internal/web/static_appjs_browser_test.go shells out to it too.

const ORIGIN = "http://localhost:8124";
const GAME = { id: "1e9d6b0c-4f7a-4a9e-8a5b-2c1d3e4f5a6b", slug: "azeroth", name: "Azeroth" };
const ANA = "3f0a1b2c-4d5e-6f70-8192-a3b4c5d6e7f8";

function fail(message) {
  console.error("FAIL: " + message);
  process.exit(1);
}

// fakeElement carries the one thing the game-page harness deliberately
// refuses to model — an innerHTML setter — because this page is the one
// that legitimately writes markup and the assertions below are about
// *which* strings get to. It is recorded, never parsed: the stub still
// cannot turn a string into elements, so a title that appeared in
// innerHTML rather than textContent shows up here as a failure and not
// as a rendered tag.
function fakeElement(tag = "div") {
  return {
    tagName: tag,
    hidden: false,
    className: "",
    textContent: "",
    innerHTML: "",
    value: "",
    href: "",
    disabled: false,
    selectedIndex: -1,
    children: [],
    get options() {
      return this.children;
    },
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
    addEventListener(name, handler) {
      (this.listeners[name] ??= []).push(handler);
    },
    async fire(name, event = {}) {
      for (const handler of this.listeners[name] ?? []) {
        await handler({ preventDefault() {}, ...event });
      }
    },
    async click() {
      await this.fire("click");
    },
    querySelector() {
      return null;
    },
  };
}

function text(node) {
  const own = node.textContent ?? "";
  return own + node.children.map(text).join(" ");
}

// find walks a rendered subtree for the first node satisfying a
// predicate, so an assertion can name the button it wants to press
// instead of indexing into children by position.
function find(node, predicate) {
  if (predicate(node)) {
    return node;
  }
  for (const child of node.children) {
    const hit = find(child, predicate);
    if (hit) {
      return hit;
    }
  }
  return null;
}

const IDS = [
  "back-to-game",
  "doc-title",
  "doc-meta",
  "doc-error",
  "doc-body",
  "doc-content",
  "doc-entities",
  "doc-entities-empty",
  "doc-history",
  "doc-history-error",
  "doc-history-more",
  "doc-revert-note",
  "compare-form",
  "compare-from",
  "compare-to",
  "compare-error",
  "comparison",
];

async function runCase({
  path = "lore/duskwood",
  rendered,
  renderedStatus = 200,
  history = { items: [] },
  members = { members: [] },
  role = "editor",
  revert = null,
  revertStatus = 200,
  comparison = null,
  startVisible = false,
} = {}) {
  const elements = {};
  for (const id of IDS) {
    elements[id] = fakeElement(id.startsWith("compare-f") || id.startsWith("compare-t") ? "select" : "div");
  }
  // Every flag starts at the *opposite* of what the case that follows
  // asserts, so an assertion can only pass because doc.js actually set
  // it. document.html ships these elements hidden, which is what a
  // success case must therefore reverse (startVisible false, below); a
  // failure case starts them visible instead, because "still hidden" is
  // the shell's own initial state and would prove nothing about the
  // failure path — that is exactly how a mutation deleting showFailure's
  // two hides went unnoticed until it was run.
  // TestTheDocumentPageShipsItsSectionsHidden (internal/web/
  // static_docjs_test.go) pins the real shell's initial state, which
  // this stub is deliberately free to contradict.
  for (const id of ["doc-body", "doc-content", "doc-error", "doc-entities-empty",
                    "doc-history-error", "doc-history-more", "doc-revert-note", "comparison"]) {
    elements[id].hidden = !startVisible;
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

  const location = {
    hash: "",
    pathname: "/g/azeroth/doc",
    search: path === null ? "" : "?path=" + encodeURIComponent(path),
    origin: ORIGIN,
    href: "",
    reloaded: false,
    reload() {
      this.reloaded = true;
    },
  };
  globalThis.window = { location };
  globalThis.history = { replaceState() {} };
  globalThis.localStorage = {
    _values: {},
    getItem() {
      return null;
    },
    setItem() {},
  };

  const requested = [];
  const posted = [];
  globalThis.fetch = async (url, init) => {
    requested.push(url);
    if (init && init.method === "POST") {
      posted.push({ url, body: JSON.parse(init.body) });
      return {
        ok: revertStatus === 200,
        status: revertStatus,
        json: async () => revert ?? {},
      };
    }
    if (url === "/api/games") {
      return { ok: true, status: 200, json: async () => ({ games: [GAME] }) };
    }
    const base = `/api/games/${GAME.id}`;
    if (url.startsWith(`${base}/docs/rendered`)) {
      return { ok: renderedStatus === 200, status: renderedStatus, json: async () => rendered };
    }
    if (url.startsWith(`${base}/docs/history`)) {
      return { ok: true, status: 200, json: async () => history };
    }
    if (url.startsWith(`${base}/docs/comparison`)) {
      return { ok: comparison !== null, status: comparison === null ? 400 : 200, json: async () => comparison ?? { error: "invalid_input", message: "from_version is required" } };
    }
    if (url === `${base}/members`) {
      return { ok: true, status: 200, json: async () => members };
    }
    if (url === `${base}/summary`) {
      return { ok: true, status: 200, json: async () => ({ role }) };
    }
    return { ok: false, status: 404, json: async () => ({ error: "not_found", message: "unexpected fetch: " + url }) };
  };

  await import(`../static/doc.js?case=${Math.random()}`);
  return { elements, requested, posted, location };
}

// Case 1: a document that reads. The rendered HTML goes in as markup and
// every other string — the title, the path, an entity's name — goes in
// as text, which is the whole security property this page rests on.
{
  const { elements } = await runCase({
    rendered: {
      path: "lore/duskwood",
      kind: "lore",
      title: "<img src=x onerror=alert(1)>Duskwood",
      version: 3,
      html: "<h1>Duskwood</h1>\n<p>Dark.</p>\n",
      links: [{ entity_type_key: "zone", entity_key: "duskwood", name: "<b>Duskwood</b>", role: "setting" }],
    },
    history: {
      items: [
        { version: 3, message: "darker", author_kind: "user", author_id: ANA, created_at: "2026-09-02T10:00:00Z" },
        { version: 2, message: "", author_kind: "token", author_id: "irrelevant", created_at: "2026-09-01T10:00:00Z" },
        { version: 1, message: "first draft", author_kind: "user", author_id: "9999", created_at: "2026-08-31T10:00:00Z" },
      ],
    },
    members: { members: [{ id: ANA, display_name: "Ana", role: "editor" }] },
  });

  if (elements["doc-body"].innerHTML !== "<h1>Duskwood</h1>\n<p>Dark.</p>\n") {
    fail(`the rendered body did not reach the page as markup: ${JSON.stringify(elements["doc-body"].innerHTML)}`);
  }
  if (elements["doc-body"].hidden) {
    fail("the rendered body is still hidden after a successful read");
  }
  // The title went through textContent, so the crafted string survives
  // verbatim — and it is nowhere in the one field that would have
  // interpreted it.
  if (elements["doc-title"].textContent !== "<img src=x onerror=alert(1)>Duskwood") {
    fail(`the title reads ${JSON.stringify(elements["doc-title"].textContent)}`);
  }
  if (elements["doc-title"].innerHTML !== "") {
    fail("the title was written as markup");
  }
  const meta = elements["doc-meta"].textContent;
  if (!meta.includes("lore/duskwood") || !meta.includes("lore") || !meta.includes("version 3")) {
    fail(`the meta line reads ${JSON.stringify(meta)}`);
  }
  if (text(elements["doc-entities"]) === "" || !text(elements["doc-entities"]).includes("<b>Duskwood</b>")) {
    fail(`an attached entity's name did not reach the page as text: ${JSON.stringify(text(elements["doc-entities"]))}`);
  }
  if (!elements["doc-entities-empty"].hidden) {
    fail("the 'attached to no entity' state is showing on a document that is attached to one");
  }

  // The history names people, never uuids: a user is resolved against
  // the member list, a token is "an agent", and a user id that is no
  // longer a member is a sentence rather than a hex string.
  const history = text(elements["doc-history"]);
  if (!history.includes("Ana")) {
    fail(`the history does not name the author: ${JSON.stringify(history)}`);
  }
  if (!history.includes("an agent")) {
    fail(`a token's version does not say an agent wrote it: ${JSON.stringify(history)}`);
  }
  if (!history.includes("a former member")) {
    fail(`a departed author is not described: ${JSON.stringify(history)}`);
  }
  if (history.includes(ANA) || history.includes("9999")) {
    fail(`a raw id reached the screen: ${JSON.stringify(history)}`);
  }
  if (elements["doc-content"].hidden) {
    fail("the page body is still hidden after a successful read");
  }
}

// Case 2: revert. The button appears on a past version and not on the
// current one, and it sends the document's *current* version as
// expected_version — the compare-and-set that turns somebody else's save
// into a conflict instead of a silent overwrite.
{
  const { elements, posted, location } = await runCase({
    rendered: { path: "lore/duskwood", title: "Duskwood", version: 3, html: "<p>x</p>", links: [] },
    history: {
      items: [
        { version: 3, message: "c", author_kind: "user", author_id: ANA, created_at: "2026-09-02T10:00:00Z" },
        { version: 1, message: "a", author_kind: "user", author_id: ANA, created_at: "2026-08-31T10:00:00Z" },
      ],
    },
    members: { members: [{ id: ANA, display_name: "Ana", role: "editor" }] },
    revert: { path: "lore/duskwood", version: 4 },
  });

  const buttons = [];
  find(elements["doc-history"], (node) => {
    if (node.tagName === "button") buttons.push(node);
    return false;
  });
  if (buttons.length !== 1) {
    fail(`the history offers ${buttons.length} restore buttons; want one, on the version that is not current`);
  }
  if (!buttons[0].textContent.includes("version 1")) {
    fail(`the restore button reads ${JSON.stringify(buttons[0].textContent)}`);
  }
  await buttons[0].click();
  if (posted.length !== 1 || !posted[0].url.endsWith("/docs/revert")) {
    fail(`the restore button posted ${JSON.stringify(posted)}`);
  }
  if (posted[0].body.to_version !== 1) {
    fail(`the revert restores version ${posted[0].body.to_version}, want 1`);
  }
  if (posted[0].body.expected_version !== 3) {
    fail(
      `the revert sent expected_version ${posted[0].body.expected_version}; it must be the document's current ` +
        "version (3), or a save somebody else landed meanwhile is silently overwritten",
    );
  }
  if (posted[0].body.path !== "lore/duskwood") {
    fail(`the revert named ${JSON.stringify(posted[0].body.path)}`);
  }
  if (!location.reloaded) {
    fail("a successful revert did not re-read the page, so the reader is left looking at the old version");
  }
}

// Case 3: a revert that lost the race. The server's own words show —
// version_conflict names the version the document is on now, which is
// the sentence the reader needs and not one this page could write — and
// the page does not reload, so nothing about it claims the revert
// happened.
{
  const { elements, location } = await runCase({
    rendered: { path: "lore/duskwood", title: "Duskwood", version: 3, html: "<p>x</p>", links: [] },
    history: {
      items: [
        { version: 3, message: "c", author_kind: "user", author_id: ANA, created_at: "2026-09-02T10:00:00Z" },
        { version: 1, message: "a", author_kind: "user", author_id: ANA, created_at: "2026-08-31T10:00:00Z" },
      ],
    },
    revertStatus: 409,
    revert: {
      error: "version_conflict",
      message: 'the document at "lore/duskwood" is at version 5, not 3',
    },
  });

  const button = find(elements["doc-history"], (node) => node.tagName === "button");
  await button.click();
  if (elements["doc-history-error"].hidden) {
    fail("a refused revert said nothing");
  }
  if (!elements["doc-history-error"].textContent.includes("version 5")) {
    fail(`the conflict reads ${JSON.stringify(elements["doc-history-error"].textContent)}`);
  }
  if (location.reloaded) {
    fail("a refused revert reloaded the page as if it had worked");
  }
  if (button.disabled) {
    fail("the restore button stayed disabled after a refusal, so the reader cannot try again");
  }
}

// Case 4: a viewer. No restore button is offered — and the page says why
// rather than simply not showing one, because a missing button explains
// nothing. The server refuses the write regardless; this is only about
// not offering an action that will be refused.
{
  const { elements } = await runCase({
    role: "viewer",
    rendered: { path: "lore/duskwood", title: "Duskwood", version: 3, html: "<p>x</p>", links: [] },
    history: {
      items: [
        { version: 3, message: "c", author_kind: "user", author_id: ANA, created_at: "2026-09-02T10:00:00Z" },
        { version: 1, message: "a", author_kind: "user", author_id: ANA, created_at: "2026-08-31T10:00:00Z" },
      ],
    },
  });

  if (find(elements["doc-history"], (node) => node.tagName === "button")) {
    fail("a viewer is offered a restore button the server will refuse");
  }
  if (elements["doc-revert-note"].hidden) {
    fail("a viewer is shown no buttons and told nothing about why");
  }
  if (!elements["doc-revert-note"].textContent.includes("viewer")) {
    fail(`the viewer's note reads ${JSON.stringify(elements["doc-revert-note"].textContent)}`);
  }
}

// Case 5: the reading view fails. The server's own message shows and
// nothing else does — a document that could not be read must not look
// like a document that happens to be empty.
{
  const { elements } = await runCase({
    startVisible: true,
    renderedStatus: 404,
    rendered: { error: "not_found", message: 'this game has no document at "lore/duskwood"' },
  });

  if (elements["doc-error"].hidden) {
    fail("a failed read said nothing");
  }
  if (!elements["doc-error"].textContent.includes("no document at")) {
    fail(`the failure reads ${JSON.stringify(elements["doc-error"].textContent)}`);
  }
  if (!elements["doc-body"].hidden || !elements["doc-content"].hidden) {
    fail("a failed read left an empty article and an empty history on screen");
  }
  if (elements["doc-body"].innerHTML !== "") {
    fail("a failed read wrote markup anyway");
  }
}

// Case 6: an address that names no document at all. The page says so
// instead of asking the server for the empty path and rendering
// whatever comes back.
{
  const { elements, requested } = await runCase({ path: null, startVisible: true });

  if (elements["doc-error"].hidden) {
    fail("an address with no path rendered as if it named a document");
  }
  if (requested.some((url) => url.includes("/docs/"))) {
    fail(`the page asked for a document anyway: ${JSON.stringify(requested)}`);
  }
  if (!elements["doc-body"].hidden || !elements["doc-content"].hidden) {
    fail("an address with no path left an empty article and an empty history on screen");
  }
}

// Case 7: the comparison. Its html is the second and last thing that
// goes in as markup, and a coarse diff says so — otherwise a whole
// document replaced reads as a change nobody made.
{
  const { elements } = await runCase({
    rendered: { path: "lore/duskwood", title: "Duskwood", version: 2, html: "<p>x</p>", links: [] },
    history: {
      items: [
        { version: 2, message: "b", author_kind: "user", author_id: ANA, created_at: "2026-09-02T10:00:00Z" },
        { version: 1, message: "a", author_kind: "user", author_id: ANA, created_at: "2026-08-31T10:00:00Z" },
      ],
    },
    comparison: {
      path: "lore/duskwood",
      from_version: 1,
      to_version: 2,
      unified: "@@ -1 +1 @@\n-# old\n+# new\n",
      coarse: true,
      html: '<div class="diff"><div class="diff-removed">-# old</div></div>',
    },
  });

  // Both pickers were filled from the history, and they default to the
  // last change: the previous version on the left, the current on the
  // right.
  if (elements["compare-from"].options.length !== 2 || elements["compare-to"].options.length !== 2) {
    fail("the compare pickers were not filled from the history");
  }
  if (elements["compare-from"].selectedIndex !== 1 || elements["compare-to"].selectedIndex !== 0) {
    fail("the compare pickers do not default to the last change");
  }

  await elements["compare-form"].fire("submit");
  if (elements.comparison.hidden) {
    fail("the comparison stayed hidden after a successful compare");
  }
  if (!elements.comparison.innerHTML.includes("diff-removed")) {
    fail(`the comparison did not reach the page as markup: ${JSON.stringify(elements.comparison.innerHTML)}`);
  }
  if (!elements["compare-error"].textContent.includes("too large to compare")) {
    fail(`a coarse diff was not explained: ${JSON.stringify(elements["compare-error"].textContent)}`);
  }
}

console.log(
  "ok: the reading view renders markup only from a rendered view, names authors, " +
    "compare-and-sets its reverts, refuses to guess on a failure and offers a viewer nothing it cannot do",
);
