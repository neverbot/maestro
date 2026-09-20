// The game's settings, both halves of it.
//
// **What this file is written against.** The three token routes have
// existed since Task 12 and nothing in the interface called them, so a
// designer who cannot write a line of a game's content by hand also had
// no way to hand an agent the token that would. The Agents tab is the
// crossing, and the properties that matter are not visual:
//
//   - the command carries this instance's own origin and the token, and
//     the token is in a header and never in a URL;
//   - a viewer is not offered a mint the server refuses, and is still
//     shown the list they may revoke from;
//   - the clear token is rendered once, by the one function that ever
//     receives it;
//   - revoking asks first, in the row, with the token's own name beside
//     it.
//
// Run directly: `node internal/web/jstest/settings_test.mjs`.
// internal/web/static_settings_test.go shells out to it too.

import { install } from "./svg_dom.mjs";

const dom = install();

// pages/page.js defines the read-only notice's hint component when it
// loads, and a custom element's class needs these two globals to exist
// before it is declared.
globalThis.HTMLElement ??= class {};
globalThis.customElements ??= { define() {}, get: () => undefined };

let failures = 0;
const pending = [];

function setClipboard(clipboard) {
  Object.defineProperty(globalThis, "navigator", {
    configurable: true,
    writable: true,
    value: { clipboard },
  });
}

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

const ORIGIN = "https://maestro.example.test";
const TOKEN = "mst_a1b2c3d4e5f6";

const IDS = [
  "tab-game",
  "tab-agents",
  "panel-game",
  "panel-agents",
  "create-token",
  "token-label",
  "create-token-error",
  "agents-refused",
  "token-issued",
  "token-once",
  "token-value",
  "token-snippet",
  "token-elsewhere",
  "token-next",
  "copy-token",
  "copy-snippet",
  "tokens",
  "tokens-empty",
  "tokens-error",
];

// mount builds the shell's holes and a client that records what it was
// asked for. The document is the strict stub: it cannot parse markup and
// it refuses innerHTML in both directions.
function mount({ tokens = [], hash = "" } = {}) {
  const elements = {};
  for (const id of IDS) {
    elements[id] = dom.document.createElement("div");
    elements[id].setAttribute("id", id);
  }
  // The shell writes which panel each tab opens; settings.js reads that
  // rather than the tab's id, so the harness has to carry it too.
  elements["tab-game"].setAttribute("data-panel", "panel-game");
  elements["tab-agents"].setAttribute("data-panel", "panel-agents");
  const doc = {
    createElement: (tag) => dom.document.createElement(tag),
    getElementById: (id) => (Object.prototype.hasOwnProperty.call(elements, id) ? elements[id] : null),
  };
  const calls = [];
  const client = {
    async listTokens() {
      calls.push(["listTokens"]);
      return { ok: true, result: { tokens } };
    },
    async createToken(label) {
      calls.push(["createToken", label]);
      return { ok: true, result: { token: TOKEN, label, token_hint: "a1b2" } };
    },
    async revokeToken(id) {
      calls.push(["revokeToken", id]);
      return { ok: true, result: {} };
    },
  };
  const copied = [];
  // `navigator` is a getter-only global in Node, so the clipboard is
  // installed by defining the property rather than by assigning it.
  setClipboard({ async writeText(text) { copied.push(text); } });
  const replaced = [];
  const win = {
    location: { hash, origin: ORIGIN },
    history: { replaceState: (_s, _t, url) => replaced.push(url) },
  };
  globalThis.window = win;
  return {
    opened: { document: doc, client, origin: ORIGIN, slug: "ashfall" },
    elements,
    calls,
    copied,
    replaced,
    win,
  };
}

// The module graph reaches app.js, which looks for the login form and
// the header the moment it loads. A global document that finds nothing
// is what a page that is none of those looks like; every assertion below
// uses its own document, built by mount.
globalThis.document = {
  title: "",
  body: dom.document.createElement("body"),
  head: dom.document.createElement("head"),
  hidden: false,
  createElement: (tag) => dom.document.createElement(tag),
  createComment: () => ({}),
  createTreeWalker: () => ({ nextNode: () => null }),
  addEventListener() {},
  querySelectorAll: () => [],
  getElementById: () => null,
};
globalThis.window = { location: { pathname: "/", search: "", hash: "", origin: ORIGIN }, addEventListener() {}, removeEventListener() {} };

const settings = await import("../static/pages/settings.js");

// --- The one paste ----------------------------------------------------

check("theCommandCarriesThisInstanceAndPutsTheTokenInAHeader", () => {
  const command = settings.installCommand(ORIGIN, TOKEN);
  assert(command.includes(ORIGIN + "/mcp"), `the command does not name this instance's MCP address: ${command}`);
  assert(command.includes("--transport http"), `the command does not say the transport: ${command}`);
  assert(
    command.includes('--header "Authorization: Bearer ' + TOKEN + '"'),
    `the token does not travel in the Authorization header: ${command}`,
  );
  // The one that matters: a credential in a URL is a credential in a
  // shell history, a proxy log and a server access log.
  const address = ORIGIN + "/mcp";
  const afterAddress = command.slice(command.indexOf(address) + address.length);
  assert(
    !command.slice(0, command.indexOf(address) + address.length).includes(TOKEN),
    `the token is inside the address: ${command}`,
  );
  assert(afterAddress.includes(TOKEN), "the command does not carry the token at all");
});

check("anyOtherClientIsToldTheSameThreeFactsAndNotTheToken", () => {
  const sentence = settings.elsewhereSentence(ORIGIN);
  assert(sentence.includes(ORIGIN + "/mcp"), `no address: ${sentence}`);
  assert(sentence.includes("Authorization: Bearer"), `no header: ${sentence}`);
  assert(!sentence.includes(TOKEN), "a sentence about any client carries this token");
});

// --- Who may mint -----------------------------------------------------

check("aViewerIsNotOfferedAMintTheServerWouldRefuse", async () => {
  const world = mount();
  await settings.agentsTab(world.opened, "viewer");
  assertEqual(world.elements["create-token"].hidden, true, "a viewer is shown the mint form");
  assertEqual(world.elements["agents-refused"].hidden, false, "a viewer is told nothing about why");
  assert(
    world.elements["agents-refused"].textContent.includes(settings.NO_MINT_HEADING),
    `the refusal does not carry its heading: ${JSON.stringify(world.elements["agents-refused"].textContent)}`,
  );
  // And still sees the list: revoking is any member's.
  assert(world.calls.some((c) => c[0] === "listTokens"), "a viewer was not shown the tokens they may revoke");
});

check("anEditorMayMint", async () => {
  const world = mount();
  await settings.agentsTab(world.opened, "editor");
  assertEqual(world.elements["create-token"].hidden, false, "an editor is refused a mint the server allows");
  assertEqual(world.elements["agents-refused"].hidden, true, "an editor is told they may not mint");
});

// --- The token itself -------------------------------------------------

check("theTokenIsRenderedOnceWithItsCommand", () => {
  const world = mount();
  settings.showIssuedToken(world.opened, TOKEN);
  assertEqual(world.elements["token-issued"].hidden, false, "the token was minted and never shown");
  assertEqual(world.elements["token-value"].textContent, TOKEN, "the token is not on the page");
  assertEqual(
    world.elements["token-snippet"].textContent,
    settings.installCommand(ORIGIN, TOKEN),
    "the command on the page is not the one this module builds",
  );
  assert(
    world.elements["token-once"].textContent.includes("only time"),
    "nothing says this is the only time the token is shown",
  );
  assert(
    world.elements["token-next"].textContent.includes("fetches Maestro's instructions"),
    "nothing says the agent fetches its own instructions",
  );
});

check("copyingPutsTheTokenAndTheCommandOnTheClipboard", async () => {
  const world = mount();
  settings.showIssuedToken(world.opened, TOKEN);
  await world.elements["copy-token"].listeners; // no-op: onclick, not a listener
  await world.elements["copy-token"].onclick();
  await world.elements["copy-snippet"].onclick();
  assertEqual(world.copied[0], TOKEN, "the token did not reach the clipboard");
  assertEqual(
    world.copied[1],
    settings.installCommand(ORIGIN, TOKEN),
    "the command did not reach the clipboard",
  );
  assertEqual(world.elements["copy-token"].textContent, settings.COPIED, "the button said nothing back");
});

check("aCopyThatFailedSaysSo", async () => {
  const world = mount();
  setClipboard({ async writeText() { throw new Error("denied"); } });
  settings.showIssuedToken(world.opened, TOKEN);
  await world.elements["copy-token"].onclick();
  assertEqual(
    world.elements["copy-token"].textContent,
    settings.COPY_FAILED,
    "a copy that failed left the button saying it had worked",
  );
});

// --- The list ---------------------------------------------------------

check("aTokenRowAsksBeforeItRevokes", async () => {
  const revoked = [];
  const row = settings.tokenRow(
    dom.document,
    { id: "t1", label: "my laptop", token_hint: "a1b2", minted_by: "Designer" },
    async (id) => revoked.push(id),
  );
  const button = row.childNodes.find((child) => child.tagName === "button");
  assert(button, "a live token's row carries no way to revoke it");
  await button.click();
  assertEqual(revoked.length, 0, "the first press revoked the token without asking");
  assertEqual(button.getAttribute("data-armed"), "true", "the control does not say it is waiting for an answer");
  assertEqual(button.textContent, settings.REVOKE_CONFIRM, "the armed control does not say what the next press does");
  await button.click();
  assertEqual(revoked.join(""), "t1", "the second press did not revoke the token");
});

check("aRevokedTokenOffersNoButton", () => {
  const row = settings.tokenRow(
    dom.document,
    { id: "t2", label: "old runner", token_hint: "c3d4", revoked_at: "2026-09-01T00:00:00Z" },
    async () => {},
  );
  assert(
    !row.childNodes.some((child) => child.tagName === "button"),
    "a token already revoked is offered a revoke",
  );
  assert(row.textContent.includes(settings.REVOKED), "a revoked token does not say so");
});

check("theListIsTheServersAndTheEmptyStateTeaches", async () => {
  const empty = mount();
  await settings.agentsTab(empty.opened, "owner");
  assertEqual(empty.elements.tokens.hidden, true, "an empty list is shown as an empty list");
  assert(
    empty.elements["tokens-empty"].textContent.includes(settings.NO_TOKENS_HEADING),
    "a game with no tokens is told nothing",
  );

  const world = mount({ tokens: [{ id: "t1", label: "my laptop", token_hint: "a1b2" }] });
  await settings.agentsTab(world.opened, "owner");
  assertEqual(world.elements.tokens.hidden, false, "a game with a token shows no list");
  assert(
    world.elements.tokens.textContent.includes("my laptop"),
    `the list does not carry the token's name: ${JSON.stringify(world.elements.tokens.textContent)}`,
  );
});

check("mintingAsksTheServerAndShowsWhatItAnswered", async () => {
  const world = mount();
  await settings.agentsTab(world.opened, "owner");
  world.elements["token-label"].value = "  my laptop  ";
  await world.elements["create-token"].dispatch("submit", { preventDefault() {} });
  assert(
    world.calls.some((c) => c[0] === "createToken" && c[1] === "my laptop"),
    `the label was not sent as typed: ${JSON.stringify(world.calls)}`,
  );
  assertEqual(world.elements["token-value"].textContent, TOKEN, "what the server minted was not shown");
  assertEqual(world.elements["token-label"].value, "", "the field kept the label of a token already minted");
});

// --- The tabs ---------------------------------------------------------

check("theAddressChoosesTheTab", () => {
  const world = mount({ hash: "#agents" });
  settings.wireTabs(world.opened.document, world.win);
  assertEqual(world.elements["panel-agents"].hidden, false, "a link to #agents did not open the Agents tab");
  assertEqual(world.elements["panel-game"].hidden, true, "both tabs are open at once");
  assertEqual(world.elements["tab-agents"].getAttribute("aria-selected"), "true", "the tab is not marked selected");
});

check("theTabsDefaultToTheGameAndFollowAPress", async () => {
  const world = mount();
  settings.wireTabs(world.opened.document, world.win);
  assertEqual(world.elements["panel-game"].hidden, false, "the page opened on neither tab");
  await world.elements["tab-agents"].dispatch("click");
  assertEqual(world.elements["panel-agents"].hidden, false, "pressing the tab did not open it");
  assertEqual(world.elements["panel-game"].hidden, true, "the other tab stayed open");
  assertEqual(world.replaced.pop(), "#agents", "the address did not follow the selection");
});

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
