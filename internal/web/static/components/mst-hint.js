// A word on screen and the sentence behind it.
//
// **Why this exists at all.** This product puts short, load-bearing
// words in places where a whole sentence will not fit: "Read-only" in a
// page head, a verdict on an analysis row, a renderer's knob. Until now
// the sentence behind such a word went into a `title` attribute, which
// is three separate failures at once: the browser decides when to show
// it (about a second, and never on a keyboard), it renders in the
// operating system's own chrome rather than in this identity, and a
// reader on a touch screen never sees it. The product already had the
// words; what it had nowhere to put was the explanation.
//
// **It is not a Lit element**, for mst-canvas.js's and mst-save-as.js's
// reason: everything it renders is a sentence written in this repository
// or a server's own prose, and building with `createElement` and
// `textContent` means nothing on this path can parse markup. There is no
// template, so there is nothing for Lit to be good at here.
//
// **Hover is the convenience; focus is the contract.** A hint that only
// answers a pointer is a hint a keyboard reader never gets, so the
// trigger is focusable, the panel opens on focus, and Escape closes it
// while the focus stays where the reader put it. The panel carries
// `role="tooltip"` and the trigger `aria-describedby`, which is why both
// live inside this shadow root: an IDREF does not cross the boundary, so
// a panel rendered in light DOM beside the trigger could not be named by
// it.
//
// **The panel is a surface, not a box drawn on the page.** Paper over
// the page's ground, a hairline, the small radius and `--shadow-2` —
// the same chrome every floating thing in this product uses, stated
// once here so two of them never disagree about how a sheet on the desk
// looks.
//
// Usage, and the trigger is whatever is slotted in:
//
//     const hint = doc.createElement("mst-hint");
//     hint.setAttribute("text", "An agent writes this; nothing here does.");
//     hint.append(badge);
//
// `align="end"` puts the panel's right edge against the trigger's, which
// is what a hint anchored near the right edge of the page needs.

import { adoptControlStyles } from "./control-styles.js";

export const CLASS_TRIGGER = "trigger";
export const CLASS_PANEL = "panel";
export const PANEL_ID = "hint";

// The sheet, read by the shadow root rather than by the page: styles.css
// cannot reach in here (the Shadow Boundary Rule), and custom properties
// can, so every value below is a token the page already defines.
const HINT_CSS = `
:host {
  position: relative;
  display: inline-flex;
  align-items: center;
}

.trigger {
  display: inline-flex;
  align-items: center;
  cursor: help;
  border-radius: var(--radius);
}

/* The focus ring is the page's own accent, and it is here because a
   trigger nobody can see the focus on is a trigger a keyboard reader
   loses. */
.trigger:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
}

.panel {
  position: absolute;
  top: calc(100% + var(--s2));
  left: 0;
  z-index: 30;
  width: max-content;
  max-width: 22rem;
  padding: var(--s3);
  background: var(--paper);
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  box-shadow: var(--shadow-2);
  font: 400 0.8125rem/1.45 var(--sans);
  /* The page head puts its actions at the right, and the panel inherited
     that alignment: a sentence ragged down its left edge, which is not
     how anything else in this product sets prose. */
  text-align: left;
  /* Opacity and visibility only: a panel that animates its own position
     or size animates layout, which is the one thing the identity's
     motion section forbids outright. */
  opacity: 0;
  visibility: hidden;
  transition: opacity var(--dur-state) var(--ease), visibility var(--dur-state);
}

:host([align="end"]) .panel {
  left: auto;
  right: 0;
}

.panel[data-open] {
  opacity: 1;
  visibility: visible;
}

@media (prefers-reduced-motion: reduce) {
  .panel { transition: none; }
}
`;

function adoptHintStyles(shadow) {
  if (!shadow || !Array.isArray(shadow.adoptedStyleSheets)) return null;
  if (typeof CSSStyleSheet !== "function") return null;
  let sheet;
  try {
    sheet = new CSSStyleSheet();
    sheet.replaceSync(HINT_CSS);
  } catch {
    return null;
  }
  shadow.adoptedStyleSheets = [...shadow.adoptedStyleSheets, sheet];
  return sheet;
}

export class MstHint extends HTMLElement {
  static get observedAttributes() {
    return ["text"];
  }

  connectedCallback() {
    if (this.shadowRoot) {
      this.#say();
      return;
    }
    const shadow = this.attachShadow({ mode: "open" });
    adoptControlStyles(shadow);
    adoptHintStyles(shadow);

    const doc = this.ownerDocument ?? document;
    const trigger = doc.createElement("span");
    trigger.className = CLASS_TRIGGER;
    // Focusable, so the explanation is reachable without a pointer.
    trigger.setAttribute("tabindex", "0");
    trigger.setAttribute("aria-describedby", PANEL_ID);
    trigger.append(doc.createElement("slot"));

    const panel = doc.createElement("span");
    panel.className = CLASS_PANEL;
    panel.setAttribute("id", PANEL_ID);
    panel.setAttribute("role", "tooltip");

    shadow.append(trigger, panel);
    this.trigger = trigger;
    this.panel = panel;
    this.#say();

    // Pointer and keyboard both open it; either one closing it closes
    // the same state, so there is one flag and not two.
    this.addEventListener("pointerenter", () => this.show());
    this.addEventListener("pointerleave", () => this.hide());
    this.addEventListener("focusin", () => this.show());
    this.addEventListener("focusout", () => this.hide());
    this.addEventListener("keydown", (event) => {
      if (event?.key === "Escape") this.hide();
    });
  }

  attributeChangedCallback() {
    this.#say();
  }

  // **The sentence arrives as text.** A hint explains a limitation and
  // some of those sentences are the server's own prose; a sink that
  // parsed markup would make every one of them a question about
  // escaping.
  #say() {
    if (!this.panel) return;
    this.panel.textContent = this.getAttribute("text") ?? "";
  }

  show() {
    if (!this.panel) return;
    // A hint with nothing to say does not open an empty sheet.
    if ((this.getAttribute("text") ?? "") === "") return;
    this.panel.setAttribute("data-open", "");
  }

  hide() {
    if (!this.panel) return;
    this.panel.removeAttribute("data-open");
  }

  // open is what a test reads, so an assertion about the hint being
  // shown reads the same state the CSS does rather than a field of its
  // own.
  get open() {
    return this.panel ? this.panel.hasAttribute("data-open") : false;
  }
}

if (typeof customElements !== "undefined" && !customElements.get("mst-hint")) {
  customElements.define("mst-hint", MstHint);
}
