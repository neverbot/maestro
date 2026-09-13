// The picker: one control over a game's own vocabulary.
//
// **This is the reusable half of the query builder**, and it is built
// before the builder for the reason the builder's own spec gives: the
// four pickers are "the real work", and three other screens want them
// too — the route step list, the Draw clause's `color_by`, and the
// frame's "start from this room".
//
// It exists because a person choosing "available to" from a list cannot
// misspell `available_to`, and a misspelled key is the single most
// common way a hand-written query fails. So the control never takes a
// key as free text: it takes a **choice** from what the game declared,
// and the key it answers with is the one the game wrote.
//
// It is a plain custom element rather than a Lit one, deliberately:
// nothing here re-renders from a model, it is a `<details>` around a
// filtered list, and adding Lit's module graph to a control that closes
// on a click would be paying for a framework by the byte. It adopts the
// shared control stylesheet like everything else, so it looks like the
// products' other controls rather than like itself.

import { CONTROL_CSS, adoptControlStyles } from "./control-styles.js";

// The classes this component's own stylesheet paints, as constants for
// the reason every other component's are: a renamed class is one visible
// diff, and a test can ask for a part by identity.
export const CLASS_ROOT = "picker";
export const CLASS_SUMMARY = "chosen";
export const CLASS_MENU = "menu";
export const CLASS_FILTER = "filter";
export const CLASS_OPTION = "option";
export const CLASS_EMPTY = "empty";

// EMPTY_LABEL is what a picker with nothing to choose from says. A game
// that declares no relation types has none to offer, and an empty menu
// with no words in it reads as a broken control.
export const EMPTY_LABEL = "nothing to choose from";

// NONE_LABEL is the choice that means "no opinion" — a `color_by` that
// colours nothing, a `to_type` that does not narrow. It is offered only
// when the caller says the clause is optional, because a picker that
// always offers "none" teaches that every clause is.
export const NONE_LABEL = "no choice";

// CHOOSE_EVENT carries the choice out. The detail is the option itself,
// so a caller reads `key` and never parses a label back into one.
export const CHOOSE_EVENT = "mst-choose";

// FILTER_FROM is how many options a picker has to hold before it grows a
// filter box. Below it the list is the filter: a filter over four
// relation types is a control asking a person to type what they can
// already see.
export const FILTER_FROM = 8;

const PICKER_CSS = `
:host { display: inline-block; position: relative; }

.picker > summary {
  display: inline-flex;
  align-items: center;
  gap: var(--s2, 8px);
  height: var(--control-h, 32px);
  max-width: 16rem;
  padding: 0 var(--s3, 12px);
  cursor: pointer;
  list-style: none;
  border: 1px solid var(--line-strong, #8e8279);
  border-radius: var(--radius, 3px);
  background: var(--raised, #fcfaf4);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.picker > summary::-webkit-details-marker { display: none; }
.picker > summary::after { content: " \\25be"; color: var(--muted, #6e625a); }
.picker > summary:hover { background: var(--ground, #efe9dc); }

.menu {
  display: none;
  position: absolute;
  z-index: 10;
  left: 0;
  min-width: 14rem;
  max-height: 18rem;
  overflow-y: auto;
  margin: 0.4rem 0 0;
  padding: 0.35rem;
  background: var(--raised, #fcfaf4);
  border: 1px solid var(--line, #d8d2c7);
  border-radius: var(--radius, 3px);
  box-shadow: var(--shadow-2, 0 2px 9px rgba(94, 72, 55, 0.14));
}

.picker[open] .menu { display: grid; gap: 0.15rem; }

.filter { width: 100%; margin-bottom: 0.35rem; }

.option {
  display: flex;
  align-items: baseline;
  gap: var(--s2, 8px);
  width: 100%;
  min-height: var(--row-compact-h, 28px);
  padding: 0.3rem 0.45rem;
  border: 0;
  border-radius: var(--radius, 3px);
  background: none;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}

.option:hover { background: var(--ground, #efe9dc); }
.option[aria-current="true"] { font-weight: 600; }

/* The key beside the name, in the voice this product spends on a thing
   a person copies. It is shown because a query document carries the key
   and the person picking is about to read one. */
.option .key {
  font-family: var(--mono, ui-monospace, monospace);
  font-size: 0.78rem;
  color: var(--muted, #6e625a);
}

.empty {
  padding: 0.3rem 0.45rem;
  color: var(--muted, #6e625a);
  font-style: italic;
}
`;

// adoptPickerStyles puts this component's own sheet on a shadow root as
// a constructible stylesheet, beside the shared control one. A
// `<style>` element built by script is refused outright by this
// product's content security policy — `default-src 'self'` — and the
// refusal is silent, which is the whole reason every component here goes
// through adoptedStyleSheets.
export function adoptPickerStyles(shadow) {
  if (!shadow || typeof globalThis.CSSStyleSheet !== "function") return null;
  const sheet = new globalThis.CSSStyleSheet();
  sheet.replaceSync(PICKER_CSS);
  shadow.adoptedStyleSheets = [...(shadow.adoptedStyleSheets ?? []), sheet];
  return sheet;
}

// optionsFrom turns one of the three listings this product already
// fetches into options. It is exported and pure so the mapping is
// testable without a DOM: what a picker offers is a property of the
// game's vocabulary, not of a component.
//
// `label` falls back to the key, because a type declared without one is
// still a type a person has to be able to choose.
export function optionsFrom(rows, options = {}) {
  const list = Array.isArray(rows) ? rows : [];
  const mapped = list
    .filter((row) => row && typeof row.key === "string" && row.key !== "")
    .map((row) => ({
      key: row.key,
      label: String(row.label || row.label_plural || row.name || row.key),
    }));
  if (options.none === true) {
    // First, and marked by a key nothing can collide with: a game may
    // legitimately declare a type called `none`.
    return [{ key: "", label: String(options.noneLabel || NONE_LABEL) }, ...mapped];
  }
  return mapped;
}

// matches is the filter, and it reads the key as well as the label for
// the reason the catalogue's search does: the handle a designer meets in
// every error message is the one they will type.
export function matches(option, query) {
  const needle = String(query ?? "").trim().toLowerCase();
  if (needle === "") return true;
  return (
    String(option.label ?? "").toLowerCase().includes(needle) ||
    String(option.key ?? "").toLowerCase().includes(needle)
  );
}

export class MstPicker extends HTMLElement {
  constructor(init = {}) {
    super();
    this.doc = init.document || this.ownerDocument || globalThis.document;
    this.options = Array.isArray(init.options) ? init.options : [];
    this.chosen = init.chosen ?? null;
    this.placeholder = init.placeholder || "Choose";
    this.query = "";
    const shadow = this.attachShadow({ mode: "open" });
    adoptControlStyles(shadow);
    adoptPickerStyles(shadow);
    this.root = this.doc.createElement("details");
    this.root.setAttribute("class", CLASS_ROOT);
    shadow.appendChild(this.root);
    this.draw();
  }

  // setOptions replaces what there is to choose from — the listing
  // arrived, or the type changed and the fields with it — and keeps the
  // current choice only if it is still on offer.
  setOptions(options) {
    this.options = Array.isArray(options) ? options : [];
    if (this.chosen !== null && !this.options.some((option) => option.key === this.chosen)) {
      this.chosen = null;
    }
    this.draw();
    return this.options;
  }

  // choose is the one place a choice is made, so a click, a keyboard
  // activation and a caller setting it programmatically cannot drift.
  choose(key) {
    const option = this.options.find((candidate) => candidate.key === key) ?? null;
    if (option === null) return null;
    this.chosen = option.key;
    this.root.open = false;
    this.query = "";
    this.draw();
    this.dispatchEvent(new globalThis.CustomEvent(CHOOSE_EVENT, {
      bubbles: true,
      composed: true,
      detail: option,
    }));
    return option;
  }

  // label is what the control says when closed: the chosen option's own
  // words, or the caller's placeholder.
  label() {
    const option = this.options.find((candidate) => candidate.key === this.chosen);
    return option ? option.label : this.placeholder;
  }

  draw() {
    const doc = this.doc;
    const summary = doc.createElement("summary");
    summary.setAttribute("class", CLASS_SUMMARY);
    summary.textContent = this.label();

    const menu = doc.createElement("div");
    menu.setAttribute("class", CLASS_MENU);

    if (this.options.length >= FILTER_FROM) {
      const filter = doc.createElement("input");
      filter.setAttribute("type", "search");
      filter.setAttribute("class", CLASS_FILTER);
      filter.setAttribute("aria-label", "Filter the list");
      filter.value = this.query;
      filter.addEventListener("input", () => {
        this.query = filter.value;
        this.draw();
        // The box keeps the focus and the caret: redrawing under
        // somebody's hands and taking their cursor with it is the kind
        // of thing that makes a control feel broken.
        const again = this.root.querySelector("." + CLASS_FILTER);
        if (again) {
          again.focus();
          if (typeof again.setSelectionRange === "function") {
            again.setSelectionRange(again.value.length, again.value.length);
          }
        }
      });
      menu.appendChild(filter);
    }

    const showing = this.options.filter((option) => matches(option, this.query));
    if (showing.length === 0) {
      const empty = doc.createElement("p");
      empty.setAttribute("class", CLASS_EMPTY);
      // Two different absences, two different sentences: a game with no
      // relation types has nothing to offer, and a filter that matched
      // none of them is the reader's own doing.
      empty.textContent = this.options.length === 0
        ? EMPTY_LABEL
        : "nothing matches “" + this.query.trim() + "”";
      menu.appendChild(empty);
    }
    for (const option of showing) {
      const button = doc.createElement("button");
      button.setAttribute("type", "button");
      button.setAttribute("class", CLASS_OPTION);
      button.setAttribute("data-key", option.key);
      if (option.key === this.chosen) button.setAttribute("aria-current", "true");
      const label = doc.createElement("span");
      label.textContent = option.label;
      button.appendChild(label);
      // The key, when it says something the label does not. A relation
      // type labelled "available to" is written `available_to` in the
      // document the person is about to read.
      if (option.key !== "" && option.key !== option.label) {
        const key = doc.createElement("code");
        key.setAttribute("class", "key");
        key.textContent = option.key;
        button.appendChild(key);
      }
      button.addEventListener("click", () => this.choose(option.key));
      menu.appendChild(button);
    }

    this.root.replaceChildren(summary, menu);
    // Escape closes it, the way it closes every other disclosure in this
    // product: a menu a keyboard opened and cannot close is a trap.
    this.root.addEventListener("keydown", (event) => {
      if (event.key === "Escape" && this.root.open) {
        this.root.open = false;
        summary.focus();
      }
    });
    return this.root;
  }
}

// The element is defined once, and only where a browser is: a Node
// harness imports this module for `optionsFrom` and `matches` without a
// custom element registry.
if (globalThis.customElements && !globalThis.customElements.get("mst-picker")) {
  globalThis.customElements.define("mst-picker", MstPicker);
}

export { CONTROL_CSS };
