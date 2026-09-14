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
//
// **The frame below is design.md's page frame and not a second one.**
// The first version of this file set `main { max-width: 46rem }`, which
// put 688px of content in a 1440px window — the same 686px, two pixels
// off, that design.md's own page-frame section records as a defect it
// already fixed once ("the 68ch measure belongs to the prose role alone
// and never to the page"). It also printed every word of the site on
// `ground` with the chrome on `paper`, which is the Two Grounds Rule
// exactly backwards, and set every section heading in the serif, which
// is the Two Voices Rule exactly backwards. Three named rules, all three
// inverted, on a site whose reason to exist is publishing the document
// that names them.
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
  --raised: #fcfaf4;
  --ink: #2c221b;
  --muted: #6e625a;
  --line: #d8d2c7;
  --focus: #9c470d;
  --shadow-1: 0 1px 2px rgba(94, 72, 55, 0.12);
  --serif: Literata, ui-serif, Georgia, "Times New Roman", serif;
  --sans: "Fira Sans", system-ui, sans-serif;
  --mono: ui-monospace, SFMono-Regular, Menlo, monospace;
  /* The frame, from docs/design.md: 1440 max, a 280px rail, a 48px
     header, and the width below which the rail folds under the
     content. cmd/maestro-docs/site_test.go holds these against the
     document rather than trusting this comment. */
  --page: 1440px;
  --rail: 280px;
  --bar: 48px;
  --gutter: 24px;
}

@media (prefers-color-scheme: dark) {
  :root {
    --paper: #1c1913;
    --ground: #13100b;
    --raised: #26221c;
    --ink: #e7e2d9;
    --muted: #9a9289;
    --line: #36312a;
    --focus: #d78958;
    /* A warm shadow on a warm ground is depth; the same shadow on a
       near-black ground is a glow, which is the tell this palette is
       built to avoid. */
    --shadow-1: 0 1px 2px rgba(0, 0, 0, 0.4);
  }
}

*, *::before, *::after { box-sizing: border-box; }

body {
  margin: 0;
  background: var(--ground);
  color: var(--ink);
  font: 0.95rem/1.6 var(--sans);
}

/* The keyboard reader's way past the header into the text. It is the
   first focusable thing on every page and invisible until it is
   focused. */
.skip {
  position: absolute;
  left: -9999px;
}

.skip:focus {
  left: var(--gutter);
  top: 6px;
  z-index: 2;
  padding: 6px 13px;
  background: var(--paper);
  border: 1px solid var(--line);
  border-radius: 3px;
}

/* One row, 48px, sticky, exactly as the frame says. It is full bleed
   and its contents line up with the page's own gutters. */
header {
  position: sticky;
  top: 0;
  z-index: 1;
  background: var(--paper);
  border-bottom: 1px solid var(--line);
}

header .bar {
  display: flex;
  gap: 1.5rem;
  align-items: center;
  height: var(--bar);
  max-width: var(--page);
  margin: 0 auto;
  padding: 0 var(--gutter);
}

header .brand { font-family: var(--serif); font-weight: 600; margin-right: auto; }

header a { color: inherit; text-decoration: none; }
header a:hover { text-decoration: underline; text-underline-offset: 2px; }
/* Active state: weight and ink, never a filled pill. */
header a[aria-current] { font-weight: 600; text-decoration: underline; text-underline-offset: 3px; }

.page {
  max-width: var(--page);
  margin: 0 auto;
  padding: var(--gutter) var(--gutter) 4rem;
  display: grid;
  /* The column is the measure and the page is not. design.md's frame
     caps the page at 1440 and gives the prose role 68ch: a sheet the
     width of the window with a 640px paragraph hugging its left edge
     obeys the letter of both and reads as a mistake. So the reading
     column is the measure plus its own padding, the rail sits beside
     it, and the pair is centred on the desk. */
  grid-template-columns: minmax(0, 46rem) var(--rail);
  justify-content: center;
  gap: var(--gutter);
  align-items: start;
}

/* The content is on the paper and the paper is on the desk. It was the
   other way round: the bar and the code blocks were the only things
   lifted onto paper and every word of prose was printed on the ground. */
main {
  background: var(--paper);
  border: 1px solid var(--line);
  border-radius: 3px;
  box-shadow: var(--shadow-1);
  padding: 1.75rem 2rem 2.5rem;
  min-width: 0;
}

/* The measure belongs to the prose and never to the page: tables, code
   blocks and the index below use the whole column. */
main :is(p, ul, ol, blockquote) { max-width: 68ch; }

/* The trail under the header, on every page: Body type, Sepia, with the
   page you are on in ink. It is the frame's own component and the site
   had none, on pages three levels deep. */
.crumbs {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
  align-items: center;
  min-height: 32px;
  margin: 0 0 0.5rem;
  color: var(--muted);
  font-size: 0.875rem;
}

.crumbs a { color: var(--muted); text-decoration: none; }
.crumbs a:hover { color: var(--ink); text-decoration: underline; }
.crumbs b { font-weight: 500; color: var(--ink); }
.crumbs span { color: var(--line-strong, var(--muted)); }

h1, h2, h3, h4 { line-height: 1.2; }

/* A heading that is a machine name is set in the machine's face. The
   tool surface's eleven domains are the words a client sends over the
   wire, and they were in the tool's voice beside the sentences Maestro
   writes. */
.ident { font-family: var(--mono); font-size: 1.125rem; letter-spacing: 0; }
.map .ident, .rail .ident { font-size: 0.85em; }

/* A markdown rule and a section heading are the same signal. The readme
   writes both, and the two hairlines landed 30px apart. */
hr { border: 0; border-top: 1px solid var(--line); margin: 2rem 0; }
hr:has(+ h2) { display: none; }

/* Display: the name of the thing on the page, in the game's voice,
   closed by a rule the way every screen in the product closes its page
   head. */
h1 {
  font: 600 1.75rem/1.15 var(--serif);
  letter-spacing: -0.015em;
  margin: 0 0 1rem;
  padding-bottom: 1rem;
  border-bottom: 1px solid var(--line);
}

/* Headline and Title: Maestro's own furniture, in Maestro's own voice.
   A section heading is the tool talking, however long the page is. */
h2 {
  font: 600 1.25rem/1.3 var(--sans);
  letter-spacing: -0.005em;
  margin: 2.5rem 0 0.75rem;
  padding-top: 1.25rem;
  border-top: 1px solid var(--line);
}

h3 { font: 600 1.125rem/1.35 var(--sans); margin: 1.75rem 0 0.5rem; }
h4 { font: 600 1rem/1.35 var(--sans); margin: 1.25rem 0 0.4rem; }

/* Anchors land below the sticky bar rather than under it. */
:is(h1, h2, h3, h4)[id] { scroll-margin-top: calc(var(--bar) + 1rem); }

a { color: var(--ink); text-underline-offset: 2px; }

code, pre { font-family: var(--mono); font-size: 0.85em; }

pre {
  padding: 0.75rem 1rem;
  overflow-x: auto;
  background: var(--ground);
  border: 1px solid var(--line);
  border-radius: 3px;
}

blockquote {
  margin: 1.5rem 0;
  padding: 0 0 0 1rem;
  border-left: 1px solid var(--line);
  color: var(--muted);
}

/* Wide content scrolls inside its own container, never the page. */
.scroller {
  overflow-x: auto;
  border: 1px solid var(--line);
  border-radius: 3px;
  margin: 1rem 0;
}

table { border-collapse: collapse; width: 100%; }

th, td {
  padding: 0.35rem 0.6rem;
  text-align: left;
  border-bottom: 1px solid var(--line);
  vertical-align: top;
}

tr:last-child td { border-bottom: 0; }

th { font: 600 0.75rem/1.4 var(--sans); letter-spacing: 0.03em; color: var(--muted); }

img { max-width: 100%; }

/* The rail carries what is true of the whole site: where you are in it,
   and where you are on this page. It is sticky under the header and it
   folds beneath the content below 1100px, which is the frame's own
   breakpoint. */
.rail {
  position: sticky;
  top: calc(var(--bar) + var(--gutter));
  font-size: 0.875rem;
  max-height: calc(100vh - var(--bar) - var(--gutter) * 2);
  overflow-y: auto;
}

.rail h2 {
  font: 600 0.75rem/1.2 var(--sans);
  letter-spacing: 0.03em;
  color: var(--muted);
  margin: 0 0 0.5rem;
  padding: 0;
  border: 0;
}

.rail section + section { margin-top: 1.5rem; }

.rail ul { list-style: none; margin: 0; padding: 0; max-width: none; }

.rail li { margin: 0 0 0.35rem; }

.rail a { color: var(--muted); text-decoration: none; display: block; }
.rail a:hover { color: var(--ink); text-decoration: underline; }
.rail a[aria-current] { color: var(--ink); font-weight: 600; }

/* The bundle's pages on their own page, and on the home page's pointer
   into it: the column count comes from the width, so the list uses the
   sheet instead of running down one side of it. */
/* Five groups of one, eight, three, four and three: a grid of equal
   columns leaves a hole under the short one, so they flow in columns
   instead and a group is never broken across them. */
.index { columns: 2; column-gap: 2rem; }
.index section { break-inside: avoid; margin: 0 0 1.5rem; }
.index h2 { font: 600 1.125rem/1.35 var(--sans); margin: 0 0 0.6rem; padding: 0; border: 0; }
.index ul { list-style: none; margin: 0; padding: 0; max-width: none; }
.index li { margin: 0 0 0.75rem; }
.index a { text-decoration: none; }
.index a:hover { text-decoration: underline; }
.index b { font-weight: 600; }
.index span { display: block; color: var(--muted); }

/* The whole site on one page, and the one place this site discloses
   progressively: a page folds away and find-in-page still reads it,
   which is why every one of them ships open. */
.map { display: grid; gap: 0.5rem; }
.map details { border-bottom: 1px solid var(--line); padding: 0 0 0.6rem; }
.map details:last-child { border-bottom: 0; }
.map summary { cursor: pointer; padding: 0.4rem 0; }
.map summary b { font-weight: 600; }
.map summary span { display: block; color: var(--muted); font-size: 0.875rem; }
.map ul { list-style: none; margin: 0.2rem 0 0 1.25rem; padding: 0; max-width: none;
  columns: 2; column-gap: 2rem; }
.map li { margin: 0 0 0.3rem; break-inside: avoid; }
.map a { color: var(--muted); text-decoration: none; }
.map a:hover { color: var(--ink); text-decoration: underline; }

footer {
  max-width: var(--page);
  margin: 0 auto;
  padding: 0 var(--gutter) 3rem;
  color: var(--muted);
  font-size: 0.875rem;
}

/* The one page whose content is not prose. Its content *is* panels, and
   panels do not nest: a bordered paper panel inside a bordered paper
   sheet is the defect design.md names by that word, so here the sheet
   steps back and the panels are what sits on the desk. */
.page.wide { grid-template-columns: minmax(0, 1fr) var(--rail); }
.page.wide main { background: none; border: 0; box-shadow: none; padding: 0; }

@media (max-width: 1100px) {
  .page { grid-template-columns: minmax(0, 1fr); }
  /* **Above the article, not under it.** Folded beneath the content the
     rail is thirty links a reader meets after everything they came for,
     which is the same as not being there. It goes first and lies flat:
     the groups sit side by side and the whole thing is a band rather
     than a column. */
  .rail {
    position: static;
    order: -1;
    max-height: none;
    padding: 0 0 1rem;
    border-bottom: 1px solid var(--line);
    display: flex;
    flex-wrap: wrap;
    gap: 1.5rem 2rem;
  }
  /* Each group on its own line, its label inline with it, so the band
     is three rows and not three columns of stacked links. */
  .rail { display: block; }
  .rail section { display: flex; flex-wrap: wrap; align-items: baseline; gap: 0.25rem 0.9rem; }
  .rail section + section { margin-top: 0.5rem; }
  .rail h2 { margin: 0; }
  .rail ul { display: flex; flex-wrap: wrap; gap: 0.25rem 0.9rem; }
  .map ul { columns: 1; }
}

@media (max-width: 780px) {
  :root { --gutter: 16px; }
  .index { columns: 1; }
  main { padding: 1.25rem 1.25rem 2rem; }
  header .bar { gap: 1rem; overflow-x: auto; }
}

:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
`
