// The control vocabulary, written once and adopted by every shadow root.
//
// **This file exists because the design system stopped at the shadow
// boundary.** `styles.css` styles `button` and `input` with element
// selectors, and an element selector does not cross into a shadow root.
// The audit of 2026-09-09 measured the view screen and found five of six
// controls rendering as browser defaults: `2px outset` bevels, an
// `#e9e9ed` fill, `-apple-system` at 13.33px, and a text field filled
// `#ffffff` — the one colour this identity forbids outright. Every token
// was right and nothing anywhere was red, which is this repository's
// most repeated defect wearing CSS: correct in the module, dead at the
// call site.
//
// Custom properties *do* inherit across that boundary, so the fix is not
// to give up on tokens. It is to ship one statement of what a control
// looks like, in terms of `var(--…)`, and have every component adopt it.
//
// **It is a string first and a Lit `css` second, because this front end
// has two adoption paths and one of them is not Lit.** `mst-canvas` and
// `mst-ground` build their own `CSSStyleSheet` and hand it to
// `adoptedStyleSheets`; the Lit components spread `static styles`. Both
// need the same rules, and a second copy for the second path is the
// duplication this whole task exists to remove.
//
// **What it measured, before and after.** Read in Firefox on
// /g/{game}/v/{key}, walking every shadow root and reporting each
// control's computed background, border and font:
//
//     control            before                     after
//     Save as…           #e9e9ed, 2px outset, r0    --ink, 1px solid, r3
//     class_key field    #ffffff, 2px inset, 13.3px --raised, 1px solid, 14px
//     Unpin              #e9e9ed, 2px outset, r0    --ink, 1px solid, r3
//     Clear the position #e9e9ed, 2px outset, r0    --ink, 1px solid, r3
//     ground's file input border 0, -apple-system   the file rule below
//
// Only the page's own Sign out was ever styled, because it is the only
// one of the six outside a shadow root.
//
// **A `<style>` element is not an option here and never was.** The
// server's policy is `default-src 'self'` with no `unsafe-inline`, so a
// script-built `<style>` is refused *silently*: the element is in the
// tree, its text is intact, `querySelector` finds it, and `style.sheet`
// is null. That is how the canvas shipped with no styles at all until a
// browser session read `.sheet` and found nothing. `adoptedStyleSheets`
// is not inline style and no policy governs it.

// **This module imports nothing.** It was written with `import { unsafeCSS }
// from "lit"` for one line of convenience, and that put Lit into the module
// graph of mst-canvas and mst-ground — two plain custom elements that had
// deliberately never depended on it. Their harnesses then failed to resolve
// a bare specifier, and once that was fixed they failed again on a DOM stub
// with no createTreeWalker. A stylesheet with no dependencies has neither
// problem, and the three Lit components wrap the string themselves, in the
// one place that already imports Lit.

// Every rule below names a token and no rule spells a colour, a radius or
// a duration literally — internal/web/static_tokens_test.go reads this
// file the way it reads the stylesheet.
export const CONTROL_CSS = `
:host {
  color: var(--ink);
  font-family: var(--sans);
  font-size: 0.875rem;
  line-height: 1.5;
}

/* The focus ring is the accent's first job and it is drawn the same way
   everywhere, including here, where the page's own :focus-visible rule
   cannot reach. */
:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 2px;
}

button {
  font: inherit;
  height: var(--control-h);
  padding: 0 13px;
  cursor: pointer;
  border: 1px solid var(--ink);
  border-radius: var(--radius);
  background: var(--ink);
  color: var(--paper);
  box-shadow: var(--shadow-1);
  transition: background var(--dur-state) var(--ease);
}

/* The primary button is ink on paper and never accent on accent-ink: a
   filled accent button would spend the accent on a fourth meaning and
   make every screen carrying a form the loudest screen in the product. */
button:hover:not(:disabled) {
  background: var(--ground);
  color: var(--ink);
}

button:disabled {
  opacity: 0.6;
  cursor: default;
  box-shadow: none;
}

/* Secondary. Most controls in this product are secondary — a screen has
   one most-likely action and the rest are ghosts. */
button.ghost {
  background: transparent;
  color: var(--ink);
  border-color: var(--line-strong);
  box-shadow: none;
}

button.ghost:hover:not(:disabled) {
  background: var(--ground);
}

/* An action that reads as a sentence rather than as a target. It carries
   no box at all, because a box around three words in a paragraph is the
   thing that makes a paragraph look like a form. */
button.link {
  height: auto;
  padding: 0;
  border: 0;
  background: none;
  color: var(--ink);
  box-shadow: none;
  text-decoration: underline;
  text-underline-offset: 2px;
}

button.link:hover:not(:disabled) {
  background: none;
}

input,
select,
textarea {
  font: inherit;
  height: var(--control-h);
  padding: 0 9px;
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--raised);
}

textarea {
  height: auto;
  padding: var(--s2) 9px;
}

input::placeholder,
textarea::placeholder {
  color: var(--muted);
}

input:focus,
select:focus,
textarea:focus {
  border-color: var(--focus);
}

/* A file input renders its own button, which the rule above does not
   reach; it is named here so the one control a person uploads an image
   with is not the only platform-styled thing left on the screen. */
input[type="file"] {
  height: auto;
  padding: var(--s1) 0;
  border: 0;
  background: none;
}

/* Exactly one chip in a group is selected, and the selected chip is the
   only surface in this product the accent fills. */
button.chip {
  height: 26px;
  padding: 0 10px;
  border-radius: var(--radius-pill);
  border: 1px solid var(--line);
  background: transparent;
  color: var(--muted);
  box-shadow: none;
}

button.chip:hover:not(:disabled) {
  background: var(--ground);
  color: var(--ink);
}

button.chip[aria-pressed="true"] {
  background: var(--focus);
  border-color: var(--focus);
  color: var(--focus-ink);
  font-weight: 500;
}

a {
  color: inherit;
  text-decoration: none;
}

a:hover {
  text-decoration: underline;
  text-underline-offset: 2px;
}
`;

// For the Lit components, which wrap it themselves:
//
//     static styles = [unsafeCSS(CONTROL_CSS), css`…`];

// For the two components that build their own sheet.
//
// It returns the sheet it adopted, or null when the platform has no
// constructible stylesheets — and it adds no `<style>` fallback on that
// path, because under this server's policy such a fallback cannot work.
// A shadow root that cannot adopt is left unstyled and honest about it,
// which is the same decision adoptCanvasStyles already made and the
// reason this one is written to match rather than to improve on it.
export function adoptControlStyles(shadow) {
  if (!shadow || !Array.isArray(shadow.adoptedStyleSheets)) return null;
  if (typeof CSSStyleSheet !== "function") return null;
  let sheet;
  try {
    sheet = new CSSStyleSheet();
    sheet.replaceSync(CONTROL_CSS);
  } catch {
    return null;
  }
  shadow.adoptedStyleSheets = [...shadow.adoptedStyleSheets, sheet];
  return sheet;
}
