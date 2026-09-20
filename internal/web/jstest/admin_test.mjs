// The administration screen's account list.
//
// **What this file is written against.** The screen used to say, in its
// own prose, that Maestro could not tell you who was on the instance —
// there was no endpoint — and offered a field to type an address into
// instead. What matters now is not that a list renders: it is that the
// row says what a person needs before they change somebody's standing,
// and that the one change which would lock the reader out of the page
// they are standing on is not offered.
//
// Run directly: `node internal/web/jstest/admin_test.mjs`.
// internal/web/static_admin_test.go shells out to it too.

import { install } from "./svg_dom.mjs";

const dom = install();
globalThis.HTMLElement ??= class {};
globalThis.customElements ??= { define() {}, get: () => undefined };

let failures = 0;
const pending = [];
const check = (name, fn) => pending.push([name, fn]);
const assert = (condition, message) => {
  if (!condition) throw new Error(message);
};
const assertEqual = (actual, expected, message) => {
  if (actual !== expected) throw new Error(`${message}: got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);
};

globalThis.document = {
  title: "",
  body: dom.document.createElement("body"),
  head: dom.document.createElement("head"),
  createElement: (tag) => dom.document.createElement(tag),
  createComment: () => ({}),
  createTreeWalker: () => ({ nextNode: () => null }),
  addEventListener() {},
  querySelectorAll: () => [],
  getElementById: () => null,
};
globalThis.window = { location: { pathname: "/admin", search: "", hash: "", origin: "http://x" }, addEventListener() {}, removeEventListener() {} };

const admin = await import("../static/pages/admin.js");

const ADMIN = { id: "u1", email: "admin@example.test", display_name: "Admin", is_admin: true, you: true };
const OTHER = { id: "u2", email: "other@example.test", display_name: "Other", is_admin: false, you: false };

function papers() {
  const body = { children: [], append(...nodes) { this.children.push(...nodes); } };
  const doc = {
    body,
    activeElement: null,
    createTextNode: (text) => {
      const node = dom.document.createElement("span");
      node.textContent = text;
      return node;
    },
    createElement: (tag) => {
      if (typeof tag === "string" && tag.includes("-")) {
        const Ctor = globalThis.customElements.get(tag);
        if (Ctor) {
          const made = new Ctor();
          made.ownerDocument = doc;
          made.hidden = false;
          return made;
        }
      }
      return dom.document.createElement(tag);
    },
    getElementById: () => null,
  };
  return doc;
}

check("aRowSaysWhoAdministersAndWhichOneIsYou", () => {
  const doc = papers();
  const mine = admin.personRow(doc, ADMIN, () => {});
  assert(mine.textContent.includes(admin.ADMINISTRATOR), "an administrator's row does not say so");
  assert(mine.textContent.includes(admin.YOU), "the reader's own row is not marked");
  assert(mine.textContent.includes("admin@example.test"), "the row does not carry the address they sign in with");

  const theirs = admin.personRow(doc, OTHER, () => {});
  assert(!theirs.textContent.includes(admin.ADMINISTRATOR), "a plain account is described as an administrator");
  assert(!theirs.textContent.includes(admin.YOU), "somebody else's row is marked as the reader's own");
});

check("everyRowOffersTheWayIn", async () => {
  const doc = papers();
  const opened = [];
  const row = admin.personRow(doc, OTHER, (who) => opened.push(who.id));
  const button = row.childNodes.find((child) => child.tagName === "button");
  assert(button, "a row carries no way to edit the account");
  assertEqual(button.textContent, admin.EDIT, "the control does not say what it opens");
  await button.click();
  assertEqual(opened.join(""), "u2", "pressing it opened the wrong account, or none");
});

// **The one control a person must not be offered.** Taking your own
// administrator flag away closes the page you are standing on, and the
// server's last-administrator rule does not catch it while somebody else
// still has the flag. The checkbox is disabled and the reason is said.
check("youCannotTakeYourOwnStandingAwayHere", () => {
  const doc = papers();
  admin.editAccount(doc, ADMIN, async () => ({ ok: true }));
  const dialog = doc._mstDialog;
  assert(dialog, "editing opened no dialog");
  const inputs = dialog.focusable().filter((stop) => stop.tagName === "input");
  const flag = inputs.find((input) => input.getAttribute("id") === "edit-admin");
  assertEqual(flag, undefined, "the reader's own administrator flag is reachable and changeable");
  assert(
    dialog.bodyEl.textContent.includes("lock you out"),
    `nothing says why it cannot be changed here: ${JSON.stringify(dialog.bodyEl.textContent.slice(0, 80))}`,
  );
});

check("somebodyElsesStandingIsChangeable", () => {
  const doc = papers();
  admin.editAccount(doc, OTHER, async () => ({ ok: true }));
  const dialog = doc._mstDialog;
  const flag = dialog
    .focusable()
    .find((stop) => stop.getAttribute && stop.getAttribute("id") === "edit-admin");
  assert(flag, "another account's administrator flag is not offered at all");
  // `disabled` is only ever *set*, never cleared: an element that was
  // never disabled has no such property, which is what a browser's
  // `false` means here.
  assertEqual(flag.disabled === true, false, "another account's flag is disabled");
});

// The save button belongs to the dialog's footer beside Cancel, and it
// is still this form's submit.
check("theDialogsOwnActionsSitBesideTheWayOut", () => {
  const doc = papers();
  admin.editAccount(doc, OTHER, async () => ({ ok: true }));
  const dialog = doc._mstDialog;
  const labels = dialog.footEl.children.map((child) => child.textContent);
  assertEqual(labels.join(","), admin.SAVE + "," + admin.CANCEL, "the footer does not hold Save then Cancel");
  const save = dialog.footEl.children[0];
  assertEqual(save.getAttribute("form"), "edit-account", "the footer's Save is not the form's submit");
});

check("whatIsTypedIsWhatIsSent", async () => {
  const doc = papers();
  const sent = [];
  admin.editAccount(doc, OTHER, async (changes) => {
    sent.push(changes);
    return { ok: true };
  });
  const dialog = doc._mstDialog;
  const form = dialog.bodyEl.children.find((child) => child.tagName === "form");
  const field = (id) => {
    const found = [];
    const visit = (node) => {
      if (node.getAttribute && node.getAttribute("id") === id) found.push(node);
      for (const child of node.children ?? []) visit(child);
    };
    visit(form);
    return found[0];
  };
  field("edit-name").value = "New Name";
  field("edit-email").value = "new@example.test";
  await form.dispatch("submit", { preventDefault() {} });
  assertEqual(sent.length, 1, "submitting the form sent nothing");
  assertEqual(sent[0].display_name, "New Name", "the name was not sent as typed");
  assertEqual(sent[0].email, "new@example.test", "the address was not sent as typed");
  assertEqual(dialog.hidden, true, "a saved edit left the dialog open");
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
