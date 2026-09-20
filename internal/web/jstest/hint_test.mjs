// The hint: a word on screen and the sentence behind it.
//
// What is asserted here is the half a browser session cannot see
// quickly and a `title` attribute never had: that the explanation is
// reachable **without a pointer**, that Escape puts it away, and that
// the sentence arrives as text rather than as markup. The look of the
// panel is CSS and is held by internal/web/static_hint_test.go, which
// reads the sheet.
//
// Run directly: `node internal/web/jstest/hint_test.mjs`.
// internal/web/static_hint_test.go shells out to it too.

import { install } from "./svg_dom.mjs";

const dom = install();

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

const { MstHint, CLASS_PANEL, PANEL_ID } = await import("../static/components/mst-hint.js");

const SENTENCE = "An agent writes this game's content; nothing on this page does.";

// mount builds a connected hint carrying a sentence, the way page.js
// builds one.
function mount(text = SENTENCE, attributes = {}) {
  const hint = new MstHint();
  // The stub's HTMLElement has no attribute map of its own; this is the
  // same pair of methods a page uses.
  const values = new Map();
  hint.getAttribute = (name) => (values.has(name) ? values.get(name) : null);
  hint.setAttribute = (name, value) => values.set(name, String(value));
  hint.hasAttribute = (name) => values.has(name);
  hint.removeAttribute = (name) => values.delete(name);
  hint.ownerDocument = dom.document;
  if (text !== null) values.set("text", text);
  for (const [name, value] of Object.entries(attributes)) values.set(name, value);
  hint.connectedCallback();
  return hint;
}

function fire(hint, type, event = {}) {
  hint.dispatchEvent({ type, ...event });
}

check("theSentenceIsReachableFromAKeyboard", () => {
  const hint = mount();
  assertEqual(hint.open, false, "the hint opens before anybody asks for it");
  fire(hint, "focusin");
  assertEqual(hint.open, true, "the explanation cannot be reached without a pointer, which is what title did");
  fire(hint, "keydown", { key: "Escape" });
  assertEqual(hint.open, false, "Escape does not put the panel away, so a keyboard reader is stuck with it open");
});

check("aPointerOpensAndLeavingCloses", () => {
  const hint = mount();
  fire(hint, "pointerenter");
  assertEqual(hint.open, true, "hovering the word says nothing");
  fire(hint, "pointerleave");
  assertEqual(hint.open, false, "the panel stays open after the pointer has gone");
});

check("theTriggerIsFocusableAndNamesThePanel", () => {
  const hint = mount();
  assertEqual(hint.trigger.getAttribute("tabindex"), "0", "the trigger is not in the tab order");
  assertEqual(
    hint.trigger.getAttribute("aria-describedby"),
    PANEL_ID,
    "the trigger does not name the panel, so a screen reader is told nothing extra",
  );
  assertEqual(hint.panel.getAttribute("role"), "tooltip", "the panel does not say what it is");
  assertEqual(hint.panel.getAttribute("id"), PANEL_ID, "the panel carries no id for the trigger to name");
  assertEqual(hint.panel.className, CLASS_PANEL, "the panel is not the class the sheet styles");
});

// The sentence is sometimes the server's own prose. The stub throws on
// innerHTML in both directions, so a sink that parsed markup would fail
// here rather than ship.
check("theSentenceArrivesAsText", () => {
  const crafted = "<img src=x onerror=alert(1)>";
  const hint = mount(crafted);
  assertEqual(hint.panel.textContent, crafted, "the sentence did not arrive as text");
});

// A hint that was given nothing to say must not open an empty sheet
// under the word.
check("aHintWithNothingToSayStaysShut", () => {
  const hint = mount("");
  fire(hint, "focusin");
  assertEqual(hint.open, false, "an empty hint opened a panel with nothing in it");
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
