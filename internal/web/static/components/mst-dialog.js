// The one dialog in this product, and the only one there will be.
//
// **A modal is not this product's first answer and this component does
// not make it one.** `docs/product.md` bans reaching for a modal, and
// that ban stands: a confirmation that can be armed in the row is armed
// in the row (the sign-out in the header and the token revoke both do
// it), and a form that belongs on a screen stays on the screen. What a
// modal is for is the two cases nothing else covers:
//
//   - **a secret shown once.** A token is rendered, copied and then has
//     to *go*. Left on the page it sits there while somebody walks away
//     from their desk, and it pushes the screen it was opened from
//     halfway down the window. The reader closing it is the point.
//   - **a question whose answer cannot wait and cannot be undone**, with
//     nowhere in the page to ask it.
//
// Everything else is a screen, a panel or an armed control.
//
// **It is a plain custom element**, like mst-hint and mst-canvas: every
// string it shows is either a caller's own node or a caller's own text,
// built with `createElement` and `textContent`, so nothing on this path
// parses markup. Content arrives as **nodes**, never as a string of
// HTML, which is what makes that guarantee the caller's too.
//
// **What it owns is the behaviour**, because that is the part each
// hand-rolled panel gets wrong differently: Escape closes it, a press on
// the ground outside closes it, focus moves into it when it opens and
// goes back where it came from when it closes, Tab stays inside it while
// it is open, and it says `aria-modal` so a screen reader stops reading
// the page behind it.
//
// Usage:
//
//     const dialog = openDialog(doc, {
//       title: "Token created",
//       content: [note, value, command],
//       dismissLabel: "Done",
//     });
//     await dialog.closed;   // if the caller cares

import { adoptControlStyles } from "./control-styles.js";

export const CLASS_BACKDROP = "backdrop";
export const CLASS_PANEL = "panel";
export const DISMISS_LABEL = "Done";
export const CLOSE_LABEL = "Close";

// The sheet. Every value is a token the page defines, because a custom
// property is the one thing that crosses the shadow boundary.
const DIALOG_CSS = `
:host {
  position: fixed;
  inset: 0;
  z-index: 60;
  display: flex;
  align-items: center;
  justify-content: center;
}

:host([hidden]) {
  display: none;
}

/* **The ground, not a black wash.** A neutral black over this identity's
   warm paper is what makes an interface read as a dashboard, which is
   the product's first anti-reference; the dim is tinted the same brown
   the shadows are. */
.backdrop {
  position: absolute;
  inset: 0;
  background: rgba(44, 34, 27, 0.32);
}

.panel {
  position: relative;
  width: min(42rem, calc(100vw - 2 * var(--s5)));
  max-height: calc(100vh - 2 * var(--s5));
  overflow-y: auto;
  padding: var(--s5);
  background: var(--paper);
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  box-shadow: var(--shadow-2);
  font: 400 0.875rem/1.5 var(--sans);
}

.head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--s4);
  margin-bottom: var(--s4);
}

/* The game's voice is the serif and the tool's is the sans; a dialog is
   the tool talking. */
.title {
  margin: 0;
  font: 600 1.125rem/1.2 var(--sans);
}

.foot {
  display: flex;
  justify-content: flex-end;
  margin-top: var(--s5);
}

/* --- What a dialog's body may be made of ------------------------------
 *
 * **The page's stylesheet does not reach in here**, which is the Shadow
 * Boundary Rule and which this component met the first time it was
 * opened in a browser: the token and the command arrived as bare text,
 * the command lost its line breaks, and the copy buttons sat in the
 * middle of a paragraph. Tokens cross that boundary and rules do not, so
 * the small vocabulary a dialog's body is built from is stated here, in
 * terms of the same tokens styles.css uses.
 *
 * It is a vocabulary and not a copy: .copyable lives *only* here now,
 * because the one screen that had it on the page moved it into a dialog.
 */
label {
  display: block;
  font-size: 0.75rem;
  font-weight: 600;
  letter-spacing: 0.03em;
  color: var(--muted);
  margin: var(--s4) 0 var(--s2);
}

.muted {
  color: var(--muted);
}

.note {
  margin: 0;
  padding: var(--s3);
  background: var(--ground);
  border-radius: var(--radius);
}

/* A value somebody has to copy: mono, on the raised ground, wrapping
   rather than scrolling — a command whose end a reader cannot see is a
   command they cannot check before running it. */
.copyable {
  display: flex;
  align-items: flex-start;
  gap: var(--s3);
}

.copyable code {
  flex: 1;
  min-width: 0;
  padding: var(--s3);
  background: var(--raised);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  font: 400 0.8125rem/1.5 var(--mono);
  /* The command is four lines and says so. Without this the newlines
     collapse and a reader is handed one long line to trust. */
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
`;

function adoptDialogStyles(shadow) {
  if (!shadow || !Array.isArray(shadow.adoptedStyleSheets)) return null;
  if (typeof CSSStyleSheet !== "function") return null;
  let sheet;
  try {
    sheet = new CSSStyleSheet();
    sheet.replaceSync(DIALOG_CSS);
  } catch {
    return null;
  }
  shadow.adoptedStyleSheets = [...shadow.adoptedStyleSheets, sheet];
  return sheet;
}

export class MstDialog extends HTMLElement {
  connectedCallback() {
    if (this.shadowRoot) return;
    const shadow = this.attachShadow({ mode: "open" });
    adoptControlStyles(shadow);
    adoptDialogStyles(shadow);

    const doc = this.ownerDocument ?? document;
    const backdrop = doc.createElement("div");
    backdrop.className = CLASS_BACKDROP;
    // A press on the ground is a close. It is `mousedown` rather than
    // `click` for the reason every anchored surface uses: a click whose
    // press began inside the panel and ended outside it is a drag over a
    // selection, not a dismissal.
    backdrop.addEventListener("mousedown", () => this.close());

    const panel = doc.createElement("div");
    panel.className = CLASS_PANEL;
    panel.setAttribute("role", "dialog");
    panel.setAttribute("aria-modal", "true");

    const head = doc.createElement("div");
    head.className = "head";
    const title = doc.createElement("h2");
    title.className = "title";
    head.append(title);

    const body = doc.createElement("div");
    body.className = "body";

    const foot = doc.createElement("div");
    foot.className = "foot";
    const dismiss = doc.createElement("button");
    dismiss.type = "button";
    dismiss.textContent = DISMISS_LABEL;
    dismiss.addEventListener("click", () => this.close());
    foot.append(dismiss);

    panel.append(head, body, foot);
    shadow.append(backdrop, panel);

    this.panel = panel;
    this.titleEl = title;
    this.bodyEl = body;
    this.dismissEl = dismiss;

    this.addEventListener("keydown", (event) => this.#onKey(event));
  }

  // open renders one dialog's worth of content and shows it.
  //
  // The content is nodes the caller built. A caller handing text builds
  // a paragraph; this element never turns a string into elements.
  open(spec = {}) {
    if (!this.shadowRoot) this.connectedCallback();
    const doc = this.ownerDocument ?? document;
    this.titleEl.textContent = String(spec.title ?? "");
    this.dismissEl.textContent = String(spec.dismissLabel ?? DISMISS_LABEL);
    const content = Array.isArray(spec.content) ? spec.content : spec.content ? [spec.content] : [];
    this.bodyEl.replaceChildren(...content);

    // **The title names the dialog**, so a screen reader announces what
    // opened rather than "dialog".
    const titleID = "dialog-title";
    this.titleEl.setAttribute("id", titleID);
    this.panel.setAttribute("aria-labelledby", titleID);

    // Where focus came from, so it can be given back. A dialog that
    // dropped focus on the body sends a keyboard reader back to the top
    // of the page for every token they mint.
    this.opener = doc.activeElement ?? null;
    this.hidden = false;
    this.focusFirst();
    // Resolved when this dialog closes, for a caller that has something
    // to do afterwards.
    this.closed = new Promise((resolve) => {
      this.resolveClosed = resolve;
    });
    return this;
  }

  close() {
    if (this.hidden) return this;
    this.hidden = true;
    this.bodyEl.replaceChildren();
    // **The content goes with it.** A dialog that showed a secret and
    // kept it in a hidden node is a secret still in the page, one
    // inspector away from a reader who thought they had dismissed it.
    if (this.opener && typeof this.opener.focus === "function") this.opener.focus();
    this.opener = null;
    if (this.resolveClosed) {
      this.resolveClosed();
      this.resolveClosed = null;
    }
    return this;
  }

  // focusable lists what a reader can reach inside the panel, in order.
  //
  // It walks the tree rather than asking for a selector: this product's
  // own DOM stub speaks `.class` and nothing else, deliberately, and a
  // focus trap that only works where a full selector engine exists is a
  // focus trap no harness can assert.
  focusable() {
    const stops = [];
    const visit = (node) => {
      if (!node) return;
      const tag = String(node.tagName ?? "").toLowerCase();
      const disabled = node.disabled === true;
      const tabindex = typeof node.getAttribute === "function" ? node.getAttribute("tabindex") : null;
      const reachable =
        !disabled &&
        tabindex !== "-1" &&
        (tag === "button" || tag === "input" || tag === "select" || tag === "textarea" ||
          (tag === "a" && node.href) || (tabindex !== null && tabindex !== ""));
      if (reachable) stops.push(node);
      for (const child of node.children ?? node.childNodes ?? []) visit(child);
    };
    for (const child of this.panel?.children ?? this.panel?.childNodes ?? []) visit(child);
    return stops;
  }

  focusFirst() {
    const first = this.focusable()[0] ?? this.dismissEl;
    if (first && typeof first.focus === "function") first.focus();
    return first;
  }

  // **Tab stays inside.** Without this the next Tab out of the last
  // control lands on the page behind, which is a reader editing a screen
  // they were told was blocked.
  #onKey(event) {
    if (this.hidden) return;
    if (event.key === "Escape") {
      this.close();
      return;
    }
    if (event.key !== "Tab") return;
    const stops = this.focusable();
    if (stops.length === 0) return;
    const doc = this.ownerDocument ?? document;
    const active = this.shadowRoot?.activeElement ?? doc.activeElement;
    const first = stops[0];
    const last = stops[stops.length - 1];
    if (event.shiftKey && active === first) {
      if (typeof event.preventDefault === "function") event.preventDefault();
      last.focus();
      return;
    }
    if (!event.shiftKey && active === last) {
      if (typeof event.preventDefault === "function") event.preventDefault();
      first.focus();
    }
  }
}

if (typeof customElements !== "undefined" && !customElements.get("mst-dialog")) {
  customElements.define("mst-dialog", MstDialog);
}

// **One element per document, reused.** A dialog created per call leaves
// one dead element in the body for every token ever minted, and two of
// them open at once is a state no screen in this product has.
export function dialogFor(doc) {
  const host = doc && doc.body ? doc.body : null;
  if (!host) return null;
  if (doc._mstDialog && doc._mstDialog.isConnected !== false) return doc._mstDialog;
  const element = doc.createElement("mst-dialog");
  element.hidden = true;
  host.append(element);
  // A document created by a test harness does not run
  // connectedCallback, so the element is asked to build itself.
  if (typeof element.connectedCallback === "function" && !element.shadowRoot) element.connectedCallback();
  doc._mstDialog = element;
  return element;
}

export function openDialog(doc, spec) {
  const element = dialogFor(doc);
  if (!element) return null;
  return element.open(spec);
}
