package main

// siteCSS is the site's whole stylesheet: the product's own tokens, the
// warm paper ground and the two voices, in the one place a static site
// needs them.
//
// It is a copy of a handful of values from docs/design.md rather than a
// second design: the site is not the product, it has no components, and
// wiring the product's 1,800-line stylesheet into a static page would
// bring six shadow-root components' worth of rules with it. The tokens
// are the part that matters, and `docs/design-system.html` — published
// beside this — is where they are stated normatively.
const siteCSS = `
/* The two voices, from the same files the product serves — see
   copyFonts. Every face swaps rather than blocks: the site draws in the
   reader's own faces and re-flows, which keeps a documentation page
   readable on a slow connection and is the same rule the product's own
   payload budget rests on. */
@font-face {
  font-family: Literata;
  font-style: normal;
  font-weight: 400 600;
  font-display: swap;
  src: url("fonts/literata-var-latin.woff2") format("woff2");
}

@font-face {
  font-family: "Fira Sans";
  font-style: normal;
  font-weight: 400;
  font-display: swap;
  src: url("fonts/fira-sans-400-latin.woff2") format("woff2");
}

@font-face {
  font-family: "Fira Sans";
  font-style: normal;
  font-weight: 600;
  font-display: swap;
  src: url("fonts/fira-sans-600-latin.woff2") format("woff2");
}

:root {
  color-scheme: light dark;
  --paper: #f7f3e9;
  --ground: #efe9dc;
  --ink: #2c221b;
  --muted: #6e625a;
  --line: #d8d2c7;
  --accent: #9c470d;
  --serif: Literata, ui-serif, Georgia, "Times New Roman", serif;
  --sans: "Fira Sans", system-ui, sans-serif;
  --mono: ui-monospace, SFMono-Regular, Menlo, monospace;
}

@media (prefers-color-scheme: dark) {
  :root {
    --paper: #1c1913;
    --ground: #13100b;
    --ink: #e7e2d9;
    --muted: #9a9289;
    --line: #36312a;
    --accent: #d78958;
  }
}

*, *::before, *::after { box-sizing: border-box; }

body {
  margin: 0;
  background: var(--ground);
  color: var(--ink);
  font: 0.95rem/1.6 var(--sans);
}

header {
  display: flex;
  gap: 1.5rem;
  align-items: center;
  height: 48px;
  padding: 0 1.5rem;
  background: var(--paper);
  border-bottom: 1px solid var(--line);
}

header .brand { font-family: var(--serif); font-weight: 600; margin-right: auto; }

header a { color: inherit; text-decoration: none; }
header a:hover { text-decoration: underline; text-underline-offset: 2px; }

main {
  max-width: 46rem;
  margin: 0 auto;
  padding: 2rem 1.5rem 4rem;
}

h1, h2, h3 { font-family: var(--serif); line-height: 1.2; }
h1 { font-size: 1.75rem; }
h2 { font-size: 1.4rem; margin-top: 2.5rem; }
h3 { font-size: 1.15rem; }

a { color: var(--ink); text-underline-offset: 2px; }

code, pre { font-family: var(--mono); font-size: 0.85em; }

pre {
  padding: 0.75rem 1rem;
  overflow-x: auto;
  background: var(--paper);
  border: 1px solid var(--line);
  border-radius: 3px;
}

blockquote {
  margin: 1.5rem 0;
  padding: 0 0 0 1rem;
  border-left: 1px solid var(--line);
  color: var(--muted);
}

table { border-collapse: collapse; width: 100%; margin: 1rem 0; }

th, td {
  padding: 0.35rem 0.6rem;
  text-align: left;
  border-bottom: 1px solid var(--line);
  vertical-align: top;
}

th { font: 600 0.75rem/1.4 var(--sans); letter-spacing: 0.03em; color: var(--muted); }

img { max-width: 100%; }

:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
`
