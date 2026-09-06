// The harness for "Save as": internal/web/static/components/mst-save-as.js
// and the wiring in internal/web/static/pages/view.js that a designer's
// clicks arrive through, driven against the real
// internal/web/static/client.js over a stubbed fetch whose every request
// body is kept as **text**.
//
// What this layer covers that no Go test can.
//
// **That the copied query is the source's, byte for byte.** This is the
// whole reason the dialog was allowed to exist: a human alone cannot
// write a query in this product, there is no builder, and the moment
// this dialog edits one stage of a query it is a builder with none of a
// builder's design. The property is therefore about *the bytes that
// leave the browser*, and the instrument is the raw body string the
// stubbed fetch was handed — not a decoded object, which would forgive
// a re-serialisation that reordered or dropped something. On the server
// the column is jsonb and normalises anyway, so the only place this can
// be checked at all is here.
//
// **That an illegal key costs no request.** The evidence is a *count of
// requests*, which does not exist on the server: by the time
// internal/web sees anything, a request that should not have been made
// and one that should are the same request.
//
// **That the renderer list is the server's.** The fixture serves a
// catalogue this front end has never heard of, and the chooser offers
// it. A dialog that offered six hard-coded names would pass every check
// that read only the real catalogue, and would drift the first time the
// server's table changed.
//
// Run directly: `node internal/web/jstest/save_as_test.mjs`.
// internal/web/static_appjs_browser_test.go shells out to it too.

import { register } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));

// pages/view.js imports the canvas, which imports `lit`. The loader
// resolves that bare specifier out of the shipped shell's own import
// map, so this harness resolves modules the way the browser does rather
// than the way Node would like to.
register(new URL("./importmap_loader.mjs", import.meta.url), {
  parentURL: import.meta.url,
  data: { staticDir: path.join(HERE, "..", "static"), shell: "view.html" },
});

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
// Small, local and with no helpful defaults, for svg_dom.mjs's stated
// reason: a stub that starts a flag where the assertion wants it makes a
// test pass on its own. `getAttribute` answers null for an attribute
// nobody set, and there is no innerHTML in either direction, so anything
// found in a rendered subtree got there through textContent.
function fakeElement(tag) {
  const el = {
    tagName: tag,
    childNodes: [],
    parentNode: null,
    attributes: new Map(),
    listeners: {},
    ownText: "",
    value: "",
    appendChild(node) {
      node.parentNode = el;
      el.childNodes.push(node);
      return node;
    },
    removeChild(node) {
      const at = el.childNodes.indexOf(node);
      if (at < 0) throw new Error("stub: removeChild was handed a node that is not a child");
      el.childNodes.splice(at, 1);
      node.parentNode = null;
      return node;
    },
    replaceChildren(...nodes) {
      el.childNodes = [...nodes];
      for (const node of nodes) node.parentNode = el;
    },
    setAttribute(name, value) {
      if (typeof value !== "string") throw new Error(`stub: setAttribute(${name}) needs a string`);
      el.attributes.set(name, value);
    },
    getAttribute(name) {
      return el.attributes.has(name) ? el.attributes.get(name) : null;
    },
    addEventListener(name, handler) {
      (el.listeners[name] ??= []).push(handler);
    },
    async dispatch(name, event = {}) {
      for (const handler of el.listeners[name] ?? []) {
        await handler({ target: el, preventDefault() {}, ...event });
      }
    },
    get textContent() {
      return el.ownText + el.childNodes.map((child) => child.textContent).join("");
    },
    set textContent(value) {
      if (typeof value !== "string") throw new Error("stub: textContent is a string");
      el.childNodes = [];
      el.ownText = value;
    },
    get innerHTML() {
      throw new Error("stub: nothing in this front end may read or write innerHTML");
    },
    set innerHTML(_value) {
      throw new Error("stub: nothing in this front end may read or write innerHTML");
    },
  };
  return el;
}

const shell = new Map();
globalThis.window = globalThis;
globalThis.document = {
  createElement: (tag) => fakeElement(tag),
  getElementById: (id) => shell.get(id) ?? null,
  // Lit reads these three at module scope. They answer nothing useful,
  // deliberately: nothing in this harness renders a Lit template, and a
  // stub that pretended to would be a second, worse browser.
  createComment: () => ({}),
  createTreeWalker: () => ({ nextNode: () => null }),
  head: {},
  addEventListener() {},
};
globalThis.HTMLElement = class HTMLElement {
  attachShadow() {
    return fakeElement("shadow-root");
  }
  dispatchEvent() {
    return true;
  }
  addEventListener() {}
  removeEventListener() {}
};
const defined = new Map();
globalThis.customElements = {
  define(name, ctor) {
    defined.set(name, ctor);
  },
  get(name) {
    return defined.get(name);
  },
};

const {
  KEY_MAX,
  KEY_REQUIRED,
  KEY_SHAPE,
  MstSaveAs,
  NOTE_ASK_AN_AGENT,
  NOTE_DEFAULTS_ARE_A_BINDING,
  NOTE_QUERY_COPIED,
  valueOf,
} = await import("../static/components/mst-save-as.js");
const { client } = await import("../static/client.js");
const { mountSaveAs, wire, wireSaveAs } = await import("../static/pages/view.js");

// --- The fixture ------------------------------------------------------

const SLUG = "kestrel";

// A query document with something of every shape a re-serialisation
// could quietly change: a nested object, two arrays, a number, a
// boolean, an empty string and a declared parameter carrying a default.
// The key order is deliberately not alphabetical, because
// JSON.stringify preserves insertion order and a rebuilt document would
// not.
const SOURCE_QUERY = {
  from: [{ type: "quest", as: "q", where: { field: "act", op: "eq", value: 2 } }],
  traverse: [{ via: "takes_place_in", to_type: "zone", as: "z", depth: 2 }],
  params: [
    { key: "act", type: "number", default: 2 },
    { key: "faction", type: "text", default: "" },
  ],
  project: { fields: ["level"], color_by: "faction", label: "name" },
  limits: { nodes: 500, edges: 2000 },
  include_invalid: false,
};

const SOURCE = {
  key: "quests-by-zone",
  name: "Quests by zone",
  description: "Every quest, in the zone it happens in.",
  query: SOURCE_QUERY,
  renderer: "graph",
  renderer_params: { color_by: "faction", arrows: true },
  layout_mode: "mixed",
  version: 7,
};

// The catalogue the *server* serves. Deliberately not the six renderers
// this front end ships modules for: `spiral` exists nowhere in
// internal/web/static, so a chooser that offers it can only have read
// it, and a chooser that hard-codes its own six cannot.
const CATALOGUE = {
  renderers: [
    {
      name: "graph",
      consumes: "nodes, edges",
      doc: "A node-link diagram.",
      requires: "",
      reads_background: false,
      params: [
        { name: "color_by", kind: "slot", required: false, doc: "a projection slot." },
        { name: "arrows", kind: "bool", required: false, doc: "true or false." },
      ],
    },
    {
      name: "spiral",
      consumes: "nodes",
      doc: "A renderer this front end has never heard of.",
      requires: "nothing at all.",
      reads_background: false,
      params: [
        { name: "turns", kind: "count", required: true, doc: "a whole number of at least 1." },
        {
          name: "winding",
          kind: "enum",
          required: false,
          values: ["in", "out"],
          doc: 'one of "in", "out".',
        },
      ],
    },
  ],
};

// The server's own refusal for a key that is already taken, exactly as
// internal/web writes it: the code, the message and the pointer.
const TAKEN_MESSAGE =
  'a view is already saved at key "quests-by-zone-wide": expected_version 0 ' +
  "means the view must not exist yet, and it is at version 3";

function jsonResponse(status, body) {
  return { ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) };
}

// harness builds a dialog over a real client whose every request is
// recorded as **text**, which is the instrument the byte-for-byte check
// needs.
function harness(options = {}) {
  const requests = [];
  const fetchImpl = async (url, init) => {
    const request = { url: String(url), method: (init && init.method) || "GET", body: init ? init.body : null };
    requests.push(request);
    if (request.method === "GET" && request.url.endsWith("/views/renderers")) {
      return jsonResponse(200, options.catalogue ?? CATALOGUE);
    }
    if (request.method === "POST" && request.url.endsWith("/views")) {
      if (options.refuse) {
        return jsonResponse(409, {
          error: "version_conflict",
          message: TAKEN_MESSAGE,
          details: { fields: [{ path: "/expected_version", message: TAKEN_MESSAGE }] },
        });
      }
      return jsonResponse(200, { key: "copy", version: 1 });
    }
    throw new Error(`stub: nothing serves ${request.method} ${request.url}`);
  };
  const data = client({ slug: SLUG, fetchImpl });
  const dialog = new MstSaveAs({
    document: globalThis.document,
    client: data,
    slug: SLUG,
    row: SOURCE,
    params: options.params ?? { act: "3" },
  });
  return { dialog, requests, writes: () => requests.filter((entry) => entry.method === "POST") };
}

// text flattens a rendered subtree the way a reader meets it.
function text(node) {
  return node ? node.textContent : "";
}

// controls collects every element carrying a `data-field`, which is how
// the page's wiring finds a control and therefore how a test should.
function controls(node, found = []) {
  if (!node) return found;
  if (typeof node.getAttribute === "function" && node.getAttribute("data-field") !== null) found.push(node);
  for (const child of node.childNodes ?? []) controls(child, found);
  return found;
}

function buttons(node, found = []) {
  if (!node) return found;
  if (node.tagName === "button") found.push(node);
  for (const child of node.childNodes ?? []) buttons(child, found);
  return found;
}

function optionsOf(select) {
  return (select.childNodes ?? []).map((option) => option.getAttribute("value"));
}

// --- The checks -------------------------------------------------------

// The one this whole task turns on.
//
// The assertion is on the request's **text**: the body must carry
// `"query":` followed by the source document's own serialisation,
// character for character. A decoded comparison would forgive a
// document that had been taken apart and put back together — which is
// exactly what a query builder does, and exactly what this dialog is
// forbidden to be.
check("theQueryDocumentIsCopiedByteForByte", async () => {
  const { dialog, writes } = harness();
  await dialog.show();
  dialog.setKey("quests-by-zone-wide");
  dialog.chooseRenderer("spiral");
  dialog.setParam("turns", "3");
  const answer = await dialog.save();
  assert(answer !== null && answer.ok, "the fixture's save was refused");

  const sent = writes();
  assertEqual(sent.length, 1, "a save is one request");
  const raw = sent[0].body;
  assertEqual(typeof raw, "string", "the request body reaching fetch is text");

  const wanted = '"query":' + JSON.stringify(SOURCE_QUERY);
  assert(
    raw.includes(wanted),
    "the copied query is not the source's document byte for byte.\n" +
      `  want the body to contain: ${wanted}\n` +
      `  body was:                 ${raw}`,
  );
  // And the source object was not edited on the way past: a dialog that
  // mutated the row it is a copy of would change the picture behind it.
  assertEqual(
    JSON.stringify(SOURCE.query),
    JSON.stringify(SOURCE_QUERY),
    "the dialog edited the source view's own query document",
  );
});

// The other half of the same rule: what *may* differ, and that nothing
// else does.
check("onlyTheThreeEditableFieldsDiffer", async () => {
  const { dialog, writes } = harness();
  await dialog.show();
  dialog.setKey("quests-by-zone-wide");
  dialog.chooseRenderer("spiral");
  dialog.setParam("turns", "3");
  dialog.setParam("winding", "out");
  await dialog.save();

  const body = JSON.parse(writes()[0].body);
  assertEqual(
    Object.keys(body).sort().join(","),
    "description,expected_version,key,layout_mode,name,query,renderer,renderer_params",
    "the copy sends a different set of fields than the upsert takes",
  );
  // The three a designer changed.
  assertEqual(body.key, "quests-by-zone-wide", "the new key is the designer's");
  assertEqual(body.renderer, "spiral", "the renderer is the designer's");
  assertEqual(
    JSON.stringify(body.renderer_params),
    JSON.stringify({ turns: 3, winding: "out" }),
    "the renderer parameters are the designer's, in the kinds the catalogue declares",
  );
  // Everything else is the source's, unchanged.
  assertEqual(body.name, SOURCE.name, "the name is copied");
  assertEqual(body.description, SOURCE.description, "the description is copied");
  assertEqual(body.layout_mode, SOURCE.layout_mode, "the layout mode is copied");
  // And the claim about the server: this row must not exist yet.
  assertEqual(body.expected_version, 0, "a copy claims the key is free, and 0 is how that is spelled");
});

// A key that is already taken is the server's refusal and this dialog
// composes none of it.
check("aTakenKeyIsRefusedInTheServersWords", async () => {
  const { dialog, writes } = harness({ refuse: true });
  await dialog.show();
  dialog.setKey("quests-by-zone-wide");
  const answer = await dialog.save();
  assert(answer !== null && !answer.ok, "the fixture refused the save and the dialog reported success");
  assertEqual(answer.error.code, "version_conflict", "the code is carried across unchanged");
  assertEqual(dialog.band, TAKEN_MESSAGE, "the band is the server's sentence, verbatim");
  assert(text(dialog.root).includes(TAKEN_MESSAGE), "the server's sentence reaches the panel");
  assertEqual(writes().length, 1, "a refused save is one request and not a retry");
});

// An illegal key never leaves the browser, and the sentence a designer
// reads is the one internal/metamodel will use.
check("theKeyFieldRefusesAnIllegalKeyBeforeSending", async () => {
  for (const [key, message] of [
    ["", KEY_REQUIRED],
    ["a".repeat(KEY_MAX + 1), `must be at most ${KEY_MAX} characters`],
    ["quests by zone", KEY_SHAPE],
    ["-leading-hyphen", KEY_SHAPE],
  ]) {
    const { dialog, writes } = harness();
    await dialog.show();
    dialog.setKey(key);
    const answer = await dialog.save();
    assertEqual(answer, null, `${JSON.stringify(key)} was sent to the server`);
    assertEqual(writes().length, 0, `${JSON.stringify(key)} cost a request`);
    assertEqual(dialog.band, message, `${JSON.stringify(key)} was refused in the wrong words`);
    assert(text(dialog.root).includes(message), "the refusal reaches the panel");
  }
  // And a legal key is sent: a check that refused everything would pass
  // the four above and mean nothing.
  const { dialog, writes } = harness();
  await dialog.show();
  dialog.setKey("quests-by-zone-wide");
  await dialog.save();
  assertEqual(writes().length, 1, "a legal key is sent");
});

// The chooser offers the server's catalogue and nothing of its own.
check("theRendererChoiceComesFromTheServersCatalogue", async () => {
  const { dialog, requests } = harness();
  await dialog.show();
  assert(
    requests.some((entry) => entry.method === "GET" && entry.url.endsWith("/views/renderers")),
    "the dialog never asked the server what it could be drawn with",
  );
  assertEqual(
    dialog.rendererNames().join(","),
    "graph,spiral",
    "the options are not the ones the server served",
  );
  const chooser = controls(dialog.root).find((el) => el.getAttribute("data-field") === "renderer");
  assert(chooser !== undefined, "no renderer chooser reached the panel");
  assertEqual(optionsOf(chooser).join(","), "graph,spiral", "the chooser's options are not the server's");

  // The knobs are the catalogue's too, per renderer, and an enum offers
  // exactly the spellings the server admits — plus the empty one, which
  // is a real state and not a value.
  dialog.chooseRenderer("spiral");
  const painted = controls(dialog.root)
    .filter((el) => el.getAttribute("data-field") === "param")
    .map((el) => el.getAttribute("data-param"));
  assertEqual(painted.join(","), "turns,winding", "the knobs offered are not the catalogue's");
  const winding = controls(dialog.root).find((el) => el.getAttribute("data-param") === "winding");
  assertEqual(optionsOf(winding).join(","), ",in,out", "an enum offers spellings the server never declared");

  // A catalogue that did not arrive is said rather than invented.
  const empty = harness({ catalogue: { renderers: null } });
  await empty.dialog.show();
  assertEqual(empty.dialog.rendererNames().length, 0, "a catalogue that did not arrive was guessed at");
});

// The dialog says what it is: a duplicate that is also wrong is a
// duplicate, not a repair.
check("theDialogSaysTheQueryIsUnchanged", async () => {
  const { dialog } = harness();
  const closed = text(dialog.root);
  assert(!closed.includes(NOTE_QUERY_COPIED), "the note is on a panel nobody has opened");
  await dialog.show();
  const open = text(dialog.root);
  for (const note of [NOTE_QUERY_COPIED, NOTE_ASK_AN_AGENT, NOTE_DEFAULTS_ARE_A_BINDING]) {
    assert(open.includes(note), `the panel never says: ${note}`);
  }
  // The sentence points at the agent workflow by name, because "ask an
  // agent" with no tool named is advice a designer cannot act on.
  assert(NOTE_ASK_AN_AGENT.includes("views.upsert"), "the note names no way to change a query");
});

// The opening binding is an address and never a stage of the query.
//
// This is the correction the module's header records: a declared
// parameter's default lives *inside* the query document, so editing one
// is editing the document — which the check above forbids. What a
// designer can be given is the binding the copy opens with, and that
// travels in the URL exactly as it does for every other view in this
// product.
check("theOpeningBindingIsCarriedInTheAddressAndNotInTheQuery", async () => {
  const { dialog, writes } = harness();
  await dialog.show();
  dialog.setKey("quests-by-zone-wide");
  dialog.setBinding("act", "4");
  dialog.setBinding("faction", "vanguard");
  await dialog.save();

  const body = JSON.parse(writes()[0].body);
  assertEqual(
    JSON.stringify(body.query.params),
    JSON.stringify(SOURCE_QUERY.params),
    "the binding was written into the query's declarations, which makes this a query editor",
  );
  assertEqual(
    dialog.saved.href,
    "/g/kestrel/v/quests-by-zone-wide?p.act=4&p.faction=vanguard",
    "the copy's address does not carry the binding a designer chose",
  );
});

// The wiring: a designer's clicks and keystrokes reach the dialog.
//
// Task 15's finding was that a controller nothing drove is a controller
// nobody has, and this is the same seam one task later.
check("aDesignersClicksAndKeystrokesReachTheDialog", async () => {
  const { dialog, writes } = harness();
  wireSaveAs(dialog);

  const open = buttons(dialog.root).find((button) => button.getAttribute("data-action") === "save-as-open");
  assert(open !== undefined, "the closed panel offers no way to open it");
  await dialog.root.dispatch("click", { target: open });
  assert(dialog.open, "a click on the opening button did not open the dialog");

  const keyField = controls(dialog.root).find((el) => el.getAttribute("data-field") === "key");
  keyField.value = "quests-by-zone-wide";
  await dialog.root.dispatch("input", { target: keyField });
  assertEqual(dialog.key, "quests-by-zone-wide", "typing a key did not reach the dialog");

  const chooser = controls(dialog.root).find((el) => el.getAttribute("data-field") === "renderer");
  chooser.value = "spiral";
  await dialog.root.dispatch("change", { target: chooser });
  assertEqual(dialog.renderer, "spiral", "choosing a renderer did not reach the dialog");

  const save = buttons(dialog.root).find((button) => button.getAttribute("data-action") === "save-as-save");
  await dialog.root.dispatch("click", { target: save });
  assertEqual(writes().length, 1, "a click on the saving button wrote nothing");

  const cancel = buttons(dialog.root).find((button) => button.getAttribute("data-action") === "save-as-cancel");
  await dialog.root.dispatch("click", { target: cancel });
  assert(!dialog.open, "a click on cancel did not close the dialog");
});

// The call site.
//
// **This is the check the first round of mutation was missing.** Every
// check above drives `wireSaveAs` directly, so commenting out its one
// call in `wire` — the whole difference between a dialog a designer can
// open and a dialog nobody can — left them all green. That is this
// repository's most repeated defect, named at the call site: correct in
// the file, dead on the wire, nothing in between. So this one goes
// through `wire` itself, which is what the view page really calls.
check("theViewPageWiresTheDialogAtItsCallSite", async () => {
  const { dialog } = harness();
  const surface = fakeElement("div");
  const state = {
    canvas: { shell: { surfaceHost: surface, panels: fakeElement("div") } },
    ground: { root: fakeElement("div"), placing: null },
    // wire() binds the twin's selection event on the frame; this check is
    // about the dialog and only needs somewhere for that to land.
    frame: fakeElement("mst-view-frame"),
    saveAs: dialog,
  };
  wire(globalThis.document, null, SLUG, null, state);

  const open = buttons(dialog.root).find((button) => button.getAttribute("data-action") === "save-as-open");
  await dialog.root.dispatch("click", { target: open });
  assert(dialog.open, "the page never wired the dialog: a click on its own button did nothing");
});

// Where the dialog is mounted, which is the whole of check 4 of this
// round's screen findings carried one step along: the frame's drawing
// wrapper is aria-hidden, so a control slotted into it is one a keyboard
// reaches and a screen reader does not announce. The dialog goes in the
// shell's own hole instead.
check("theDialogIsMountedOutsideTheDrawingsHiddenWrapper", async () => {
  const { dialog } = harness();
  const hole = fakeElement("div");
  shell.set("save-as-root", hole);
  try {
    const mounted = mountSaveAs(globalThis.document, dialog);
    assert(mounted === hole, "the dialog was not mounted into the shell's own hole");
    // The element and not its panel: the panel lives in the dialog's
    // shadow root, which is where its adopted stylesheet is. Mounting
    // the panel alone puts an unstyled tree in the page — the shape of
    // Task 15's own finding about the canvas's stylesheet, one component
    // later.
    assert(hole.childNodes.includes(dialog), "the dialog element is not in the hole");
    assert(!hole.childNodes.includes(dialog.root), "the dialog's panel was mounted instead of the element it lives in");
  } finally {
    shell.delete("save-as-root");
  }
});

// valueOf is the one piece of arithmetic in the file, and an empty
// control is *unset* rather than a value of the wrong type.
check("anEmptyKnobIsUnsetRatherThanAValue", async () => {
  assertEqual(valueOf("count", ""), null, "an empty count is unset");
  assertEqual(valueOf("number", "2.5"), 2.5, "a number arrives as a number");
  assertEqual(valueOf("count", "3"), 3, "a count arrives as a number");
  assertEqual(valueOf("bool", "true"), true, "a bool arrives as a bool");
  assertEqual(valueOf("bool", ""), null, "an unchecked box is unset");
  assertEqual(JSON.stringify(valueOf("columns", "name, level")), '["name","level"]', "columns are a list");
  assertEqual(valueOf("slot", "faction"), "faction", "a slot is its name");

  const { dialog, writes } = harness();
  await dialog.show();
  dialog.setKey("quests-by-zone-wide");
  dialog.chooseRenderer("spiral");
  dialog.setParam("turns", "3");
  dialog.setParam("turns", "");
  await dialog.save();
  const body = JSON.parse(writes()[0].body);
  assertEqual(
    JSON.stringify(body.renderer_params),
    "{}",
    "a knob a designer emptied was still sent, as a value of the wrong type",
  );
});

for (const [name, fn] of pending) {
  try {
    await fn();
    console.log(`ok  ${name}`);
  } catch (error) {
    failures += 1;
    console.error(`FAIL ${name}: ${error.message}`);
  }
}

if (failures > 0) {
  console.error(`${failures} check(s) failed`);
  process.exit(1);
}
console.log("save as: all checks passed");
