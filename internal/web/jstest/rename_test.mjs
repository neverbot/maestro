// The harness for the product's first write by a person: renaming an
// entity, and what the screen does when somebody else wrote first.
//
// What this layer covers that no Go test can. The server's part is a
// compare-and-set and is already tested; what is not is the **page's**
// answer to a refusal, and the whole design turns on two properties that
// are properties of the client:
//
//   - the edit is never lost — a refusal leaves what was typed where it
//     was typed, because the person's sentence is the only thing in the
//     exchange that exists nowhere else;
//   - the edit never silently wins — nothing re-reads a version and
//     writes again on its own, which is last-writer-wins with extra
//     steps and nobody told.
//
// And one defect that only this layer could have caught: the batch route
// answers **200 with a `failed` list** rather than 409, so the first
// version of this page read a refused write as a success and showed the
// typed name over a row the server had kept. A page lying about a write
// is worse than a page refusing one.
//
// Run directly: `node internal/web/jstest/rename_test.mjs`.
// internal/web/static_rename_test.go shells out to it too.

import path from "node:path";
import { register } from "node:module";
import { fileURLToPath } from "node:url";

register(new URL("./importmap_loader.mjs", import.meta.url), {
  parentURL: import.meta.url,
  data: { staticDir: path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "static"), shell: "game.html" },
});

import { install } from "./svg_dom.mjs";

// svg_dom installs the globals Lit needs at import time; the two lookups
// app.js makes at module scope are added on top of it, because the
// module graph reaches app.js through page.js.
const dom = install();
dom.document.getElementById = () => null;
dom.document.querySelector = () => null;

let failures = 0;
function check(what, got, want) {
  if (JSON.stringify(got) === JSON.stringify(want)) {
    console.log("ok   " + what);
    return;
  }
  failures += 1;
  console.log("FAIL " + what + "\n  got  " + JSON.stringify(got) + "\n  want " + JSON.stringify(want));
}

// A document stub holding exactly the elements the rename touches.
function page(name) {
  const elements = {};
  const make = (id, tag) => {
    const el = {
      id,
      tagName: tag,
      value: "",
      textContent: "",
      hidden: false,
      className: "",
      disabled: false,
      children: [],
      listeners: {},
      elements: [],
      lastElementChild: null,
      addEventListener(kind, fn) {
        this.listeners[kind] = fn;
      },
      append(...kids) {
        this.children.push(...kids);
      },
      replaceChildren(...kids) {
        this.children = kids;
      },
      querySelector: () => null,
      querySelectorAll: () => [],
      focus() {},
      select() {},
      click() {
        if (this.listeners.click) this.listeners.click({ preventDefault() {} });
      },
      async submit() {
        if (this.listeners.submit) await this.listeners.submit({ preventDefault() {} });
      },
    };
    elements[id] = el;
    return el;
  };

  for (const id of [
    "page-actions", "rename", "rename-name", "rename-error", "rename-cancel",
    "rename-conflict", "conflict-theirs", "conflict-yours", "conflict-keep", "conflict-take",
    "entity-name", "crumbs",
  ]) {
    make(id, id === "rename" ? "form" : "div");
  }
  elements["entity-name"].textContent = name;
  elements["rename"].elements = [elements["rename-name"]];
  elements["rename"].querySelector = () => null;

  return {
    title: "",
    elements,
    getElementById: (id) => elements[id] ?? null,
    createElement: (tag) => make("made-" + tag + "-" + Math.random(), tag),
  };
}

// A client whose every answer is scripted, and which counts what the
// page asked it for.
function client(script) {
  const calls = [];
  return {
    calls,
    async renameEntity(entity, name) {
      calls.push({ call: "renameEntity", name, version: entity.version });
      return script.writes.shift();
    },
    async getEntity(typeKey, key) {
      calls.push({ call: "getEntity", typeKey, key });
      return script.reads.shift();
    },
  };
}

const { wireRename } = await import("../static/pages/entity.js");

const ENTITY = { type_key: "case", key: "hogger", name: "Wanted: Hogger", version: 3, fields: { act: "one" } };

function stage(script) {
  const doc = page(ENTITY.name);
  const opened = { slug: "azeroth", client: client(script), document: doc };
  wireRename(doc, opened, { entity: { ...ENTITY } });
  return { doc, opened };
}

// --- The write that lands ---------------------------------------------

{
  const { doc, opened } = stage({ writes: [{ ok: true, result: { written: [{ version: 4 }], failed: [] } }], reads: [] });
  doc.elements["page-actions"].children[0].click();
  doc.elements["rename-name"].value = "Wanted: Hogger, alive";
  await doc.elements["rename"].submit();
  check("the name on the screen is the name that was written", doc.elements["entity-name"].textContent, "Wanted: Hogger, alive");
  check("the version it stated is the one the page read", opened.client.calls[0].version, 3);
  check("and it asked for nothing else", opened.client.calls.length, 1);
  check("the form closes", doc.elements["rename"].hidden, true);
}

// --- The write the server refused -------------------------------------

// **A batch answers 200 with a `failed` list.** Read as a success — which
// is what the first version did — the page showed the typed name over a
// row the server had kept.
{
  const { doc, opened } = stage({
    writes: [{ ok: true, result: { written: [], failed: [{ code: "version_conflict", message: "current version is 4" }] } }],
    reads: [{ ok: true, result: { ...ENTITY, name: "Theirs", version: 4 } }],
  });
  doc.elements["page-actions"].children[0].click();
  doc.elements["rename-name"].value = "Mine";
  await doc.elements["rename"].submit();

  check("the screen does not claim the write landed", doc.elements["entity-name"].textContent, ENTITY.name);
  check("the conflict is shown", doc.elements["rename-conflict"].hidden, false);
  check("with what it says now", doc.elements["conflict-theirs"].textContent, "Theirs");
  check("and with what was typed", doc.elements["conflict-yours"].textContent, "Mine");
  check("the edit is still in the field", doc.elements["rename-name"].value, "Mine");
  // One write, then one read. **No second write**: a page that re-read
  // the version and wrote again would be last-writer-wins with nobody
  // told, which is the failure this design exists to prevent.
  check(
    "it read the current row and wrote nothing on its own",
    opened.client.calls.map((call) => call.call),
    ["renameEntity", "getEntity"],
  );
}

// --- The two answers, both the person's ------------------------------

{
  const { doc, opened } = stage({
    writes: [
      { ok: true, result: { written: [], failed: [{ code: "version_conflict" }] } },
      { ok: true, result: { written: [{ version: 5 }], failed: [] } },
    ],
    reads: [{ ok: true, result: { ...ENTITY, name: "Theirs", version: 4 } }],
  });
  doc.elements["page-actions"].children[0].click();
  doc.elements["rename-name"].value = "Mine";
  await doc.elements["rename"].submit();
  await doc.elements["conflict-keep"].listeners.click?.();
  doc.elements["conflict-keep"].onclick && (await doc.elements["conflict-keep"].onclick());

  check("keeping mine writes it", doc.elements["entity-name"].textContent, "Mine");
  // **The only write in this product that states a version the person
  // did not read on a page** — and it states the one just shown to them,
  // in the same gesture they authorised.
  check("against the version it just showed them", opened.client.calls[2].version, 4);
}

{
  const { doc } = stage({
    writes: [{ ok: true, result: { written: [], failed: [{ code: "version_conflict" }] } }],
    reads: [{ ok: true, result: { ...ENTITY, name: "Theirs", version: 4 } }],
  });
  doc.elements["page-actions"].children[0].click();
  doc.elements["rename-name"].value = "Mine";
  await doc.elements["rename"].submit();
  doc.elements["conflict-take"].onclick && (await doc.elements["conflict-take"].onclick());
  check("taking theirs shows theirs", doc.elements["entity-name"].textContent, "Theirs");
  check("and closes the conflict", doc.elements["rename-conflict"].hidden, true);
}

// --- The conflict with no consequence ---------------------------------

// The other writer wrote the same words. The person's intent already
// holds, and a refusal with nothing to decide is noise.
{
  const { doc } = stage({
    writes: [{ ok: true, result: { written: [], failed: [{ code: "version_conflict" }] } }],
    reads: [{ ok: true, result: { ...ENTITY, name: "Same words", version: 4 } }],
  });
  doc.elements["page-actions"].children[0].click();
  doc.elements["rename-name"].value = "Same words";
  await doc.elements["rename"].submit();
  check("no conflict is shown when both wrote the same thing", doc.elements["rename-conflict"].hidden, true);
  check("and the screen shows it", doc.elements["entity-name"].textContent, "Same words");
}

// --- A refusal that is not a conflict ---------------------------------

{
  const { doc } = stage({
    writes: [{ ok: true, result: { written: [], failed: [{ code: "schema_violation", message: "act is required" }] } }],
    reads: [],
  });
  doc.elements["page-actions"].children[0].click();
  doc.elements["rename-name"].value = "Mine";
  await doc.elements["rename"].submit();
  check("the domain's own sentence is shown", doc.elements["rename-error"].textContent, "act is required");
  check("and the form stays open over it", doc.elements["rename"].hidden, false);
  check("with the edit still in it", doc.elements["rename-name"].value, "Mine");
}

// An empty name is refused before it costs a round trip.
{
  const { doc, opened } = stage({ writes: [], reads: [] });
  doc.elements["page-actions"].children[0].click();
  doc.elements["rename-name"].value = "   ";
  await doc.elements["rename"].submit();
  check("an empty name is refused here", opened.client.calls.length, 0);
  check("and says why", doc.elements["rename-error"].textContent.length > 0, true);
}

if (failures > 0) {
  console.error(failures + " assertion(s) failed");
  process.exit(1);
}
console.log("the first human write: all assertions passed");
