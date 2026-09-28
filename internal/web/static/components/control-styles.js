// The control vocabulary, written once and adopted by every shadow root.
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
export const CONTROLS_HREF = "/static/controls.css";

// The shared sheet. **One `CSSStyleSheet` for the whole document**, not
// one per shadow root: a constructed sheet may be adopted by any number
// of roots, so every component is dressed by the same object and a
// future edit cannot reach half of them.
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
