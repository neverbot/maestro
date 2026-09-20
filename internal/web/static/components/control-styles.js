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
// **What is left here is only what a page stylesheet cannot say.**
// `:host` has no meaning outside a shadow root and `:focus-visible`
// inside one is not reached by the page's own rule. Every other control
// rule moved to static/controls.css, which this module fetches once and
// every shadow root adopts — see the sheet's own header for why one
// statement replaced two.
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
// CONTROLS_HREF is the one file that says what a control looks like. It
// is fetched rather than inlined because the page reads the very same
// bytes through an @import: two readers, one statement.
export const CONTROLS_HREF = "/static/controls.css";

// The shared sheet. **One `CSSStyleSheet` for the whole document**, not
// one per shadow root: a constructed sheet may be adopted by any number
// of roots, so every component is dressed by the same object and a
// future edit cannot reach half of them.
//
// It is created empty and filled when the fetch answers. A shadow root
// that mounts in that window is unstyled for a frame and then correct,
// which is the same trade every component here already made for a sheet
// that fails to construct at all.
let controlsSheet = null;

export function sharedControlSheet(fetchImpl) {
  if (controlsSheet) return controlsSheet;
  if (typeof CSSStyleSheet !== "function") return null;
  try {
    controlsSheet = new CSSStyleSheet();
  } catch {
    return null;
  }
  const load = fetchImpl || (typeof fetch === "function" ? fetch : null);
  if (!load) return controlsSheet;
  Promise.resolve(load(CONTROLS_HREF))
    .then((answer) => (answer && typeof answer.text === "function" ? answer.text() : ""))
    .then((css) => {
      if (typeof css === "string" && css !== "") controlsSheet.replaceSync(css);
    })
    .catch(() => {});
  return controlsSheet;
}

// adoptControlStyles dresses one shadow root: the shared vocabulary
// first, then the two rules that only mean something inside a root.
//
// It returns the host sheet it built, which is what the callers that
// check for null already expect.
export function adoptControlStyles(shadow) {
  if (!shadow || !Array.isArray(shadow.adoptedStyleSheets)) return null;
  if (typeof CSSStyleSheet !== "function") return null;
  let host;
  try {
    host = new CSSStyleSheet();
    host.replaceSync(CONTROL_CSS);
  } catch {
    return null;
  }
  const shared = sharedControlSheet();
  const sheets = shared ? [shared, host] : [host];
  shadow.adoptedStyleSheets = [...shadow.adoptedStyleSheets, ...sheets];
  return host;
}
