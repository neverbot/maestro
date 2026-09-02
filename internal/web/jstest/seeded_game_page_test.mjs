// Renders the game home page over a summary a *real* server produced.
//
// game_summary_test.mjs beside this one proves the page renders *a*
// summary, and invents the numbers it renders. This one is handed the
// JSON that GET /api/games/{game}/summary actually answered for the
// seeded game in seed_e2e_test.go, so the two halves of the claim — the
// server counted the game correctly, and the page shows what the server
// counted — are checked against the same numbers for once. A page that
// dropped a type, mis-summed a total or rendered a count as "[object
// Object]" would pass every fixture-driven check and fail here.
//
// Usage: `node internal/web/jstest/seeded_game_page_test.mjs <summary.json>`.
// internal/web/seed_e2e_test.go writes the file and shells out to it.

import { readFileSync } from "node:fs";

const ORIGIN = "http://localhost:8124";

function fail(message) {
  console.error("FAIL: " + message);
  process.exit(1);
}

const path = process.argv[2];
if (!path) {
  fail("no summary file given");
}
const summary = JSON.parse(readFileSync(path, "utf8"));

// The DOM stub is the one the harness beside this uses: nothing in it can
// interpret a string as markup, so anything found in the rendered text
// got there through textContent.
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
    addEventListener() {},
    querySelector() {
      return null;
    },
  };
}

function text(node) {
  const own = node.textContent ?? "";
  return own + node.children.map(text).join(" ");
}

const elements = {
  "game-name": fakeElement("h1"),
  "game-summary": fakeElement("p"),
  "game-content": fakeElement("section"),
  types: fakeElement("ul"),
  "types-empty": fakeElement("p"),
  "types-empty-action": fakeElement("span"),
  "relation-types": fakeElement("ul"),
  "relation-types-empty": fakeElement("p"),
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
globalThis.window = { location: { hash: "", pathname: "/g/le-mans", search: "", origin: ORIGIN, href: "" } };
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

const game = { id: "1e9d6b0c-4f7a-4a9e-8a5b-2c1d3e4f5a6b", slug: "le-mans", name: "Le Mans" };
const requested = [];
globalThis.fetch = async (url) => {
  requested.push(url);
  if (url === "/api/games") {
    return { ok: true, status: 200, json: async () => ({ games: [game] }) };
  }
  if (url === `/api/games/${game.id}/summary`) {
    return { ok: true, status: 200, json: async () => summary };
  }
  return { ok: false, status: 404, json: async () => ({ error: "not_found", message: "unexpected fetch: " + url }) };
};

await import("../static/app.js");

// Every declared type reaches the page, under its own plural label and
// with the count the server sent. A type the game has and the page does
// not show is the failure this whole file exists to catch.
const rendered = text(elements.types);
for (const row of summary.entity_types) {
  if (!rendered.includes(row.label_plural)) {
    fail(`the type ${row.key} is missing from the page`);
  }
  const noun = row.entity_count === 1 ? "entity" : "entities";
  if (!rendered.includes(`${row.entity_count} ${noun}`)) {
    fail(`the ${row.key} row does not carry its count of ${row.entity_count}: ${JSON.stringify(rendered)}`);
  }
}

const relations = text(elements["relation-types"]);
for (const row of summary.relation_types) {
  if (!relations.includes(row.label)) {
    fail(`the relation type ${row.key} is missing from the page`);
  }
  const noun = row.relation_count === 1 ? "relation" : "relations";
  if (!relations.includes(`${row.relation_count} ${noun}`)) {
    fail(`the ${row.key} row does not carry its count of ${row.relation_count}: ${JSON.stringify(relations)}`);
  }
}

const totals = elements["game-summary"].textContent;
if (!totals.includes(`${summary.totals.entities} entities`)) {
  fail(`the totals line does not carry the entity total: ${JSON.stringify(totals)}`);
}
if (!totals.includes(`${summary.totals.relations} relations`)) {
  fail(`the totals line does not carry the relation total: ${JSON.stringify(totals)}`);
}
if (elements["game-content"].hidden) {
  fail("the page body is still hidden after a real summary");
}
if (!elements["types-empty"].hidden || !elements["relation-types-empty"].hidden) {
  fail("an empty state is showing on a game that has hundreds of entities in it");
}
if (elements.types.hidden || elements["relation-types"].hidden) {
  fail("the catalogue lists are hidden on a game that has content");
}
if (rendered.includes("[object Object]") || relations.includes("[object Object]")) {
  fail("a value reached the page unrendered");
}
if (requested.length !== 2) {
  fail(`the page fetched ${JSON.stringify(requested)}; a summary must stay two requests however big the game is`);
}

console.log(
  `ok: the page rendered ${summary.entity_types.length} entity types, ` +
    `${summary.relation_types.length} relation types, ` +
    `${summary.totals.entities} entities and ${summary.totals.relations} relations`,
);
