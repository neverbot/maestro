// Renders docs/design-system.html from the two files that own the design
// system: docs/design.md's frontmatter (the normative token layer) and
// docs/design-tokens.json (the sidecar: canonical OKLCH, shadows, motion,
// component snippets, named rules).
//
// The page is a committed artefact, and it is generated rather than
// hand-written for the reason a generated artefact usually is: a hand-kept
// copy of a token table goes stale silently, and nothing anywhere turns
// red. Change a token in docs/design.md, run this, commit both.
//
// Usage: node docs/design-system.mjs

import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");

// A deliberately small YAML reader: it understands exactly the two shapes
// design.md's frontmatter uses, `key: "value"` and one level of nesting.
// A general parser would be a dependency, and this file is not worth one.
function frontmatter(text) {
  const body = text.split(/^---$/m)[1];
  if (!body) throw new Error("design.md has no frontmatter");
  const out = {};
  let section = null;
  let entry = null;
  for (const raw of body.split("\n")) {
    if (!raw.trim() || raw.trimStart().startsWith("#")) continue;
    const indent = raw.length - raw.trimStart().length;
    const line = raw.trim();
    const m = /^([\w-]+):\s*(.*)$/.exec(line);
    if (!m) continue;
    const [, key, rest] = m;
    const value = rest.replace(/^"(.*)"$/, "$1");
    if (indent === 0) {
      section = value === "" ? (out[key] = {}) : ((out[key] = value), null);
      entry = null;
    } else if (indent === 2 && section) {
      entry = value === "" ? (section[key] = {}) : ((section[key] = value), null);
    } else if (indent === 4 && entry) {
      entry[key] = value;
    }
  }
  return out;
}

const fm = frontmatter(readFileSync(join(root, "docs/design.md"), "utf8"));
const side = JSON.parse(readFileSync(join(root, "docs/design-tokens.json"), "utf8"));
const { colorMeta, typographyMeta, shadows, motion, breakpoints, density } = side.extensions;

const esc = (s) => String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

// The chrome of this page is the design system describing itself. Every
// value below comes from the tokens, so a page that looked wrong would be
// evidence about the system rather than about the page.
const vars = (dark) =>
  Object.entries(colorMeta)
    .map(([k, meta]) =>
      k.startsWith("data-")
        ? `  --${k}: ${meta.canonical};`
        : `  --${k}: ${dark ? meta.darkHex : fm.colors[k]};`,
    )
    .join("\n");

const SAMPLES = {
  display: "Vael Crypt",
  headline: "The ash well",
  title: "Types in this game",
  body: "105 quests, 12 shown, sorted by level",
  prose:
    "The well does not give back what is thrown into it. Vael knows this, and still they go down: once a year, with the rope measured and a dead name in the mouth.",
  label: "Requires",
  mono: 'requires_ability: "mothwing_cloak"',
};

const typeRows = Object.entries(fm.typography)
  .map(([role, t]) => {
    const meta = typographyMeta[role] ?? { purpose: "" };
    const style = [
      `font-family:${t.fontFamily}`,
      `font-size:${t.fontSize}`,
      `font-weight:${t.fontWeight}`,
      `line-height:${t.lineHeight}`,
      `letter-spacing:${t.letterSpacing}`,
      role === "prose" ? "max-width:68ch" : "",
    ]
      .filter(Boolean)
      .join(";");
    return `<div class="row">
  <div class="meta"><b>${esc(role)}</b><span>${esc(meta.purpose)}</span>
    <code>${esc(t.fontFamily.split(",")[0])} ${esc(t.fontWeight)} &middot; ${esc(t.fontSize)} / ${esc(t.lineHeight)}</code></div>
  <div style="${style}">${esc(SAMPLES[role] ?? role)}</div>
</div>`;
  })
  .join("\n");

const swatches = Object.entries(colorMeta)
  .filter(([k]) => !k.startsWith("data-"))
  .map(
    ([k, meta]) => `<div class="sw"><i style="background:${fm.colors[k]}"></i>
  <b>${esc(meta.displayName)}</b><code>${esc(k)}</code><code class="dim">${esc(fm.colors[k])}</code>
  <code class="dim">${esc(meta.canonical)}</code><code class="dim">dark ${esc(meta.darkHex)}</code></div>`,
  )
  .join("\n");

const dataSwatches = Object.entries(colorMeta)
  .filter(([k]) => k.startsWith("data-"))
  .map(([k, meta]) => `<i style="background:${meta.canonical}" title="${esc(k)}"></i>`)
  .join("");

// **The chip wears the token and not the literal.** It rendered
// `shadows[i].value`, which is the light theme's warm brown — so on the
// dark ground the specimen of "Resting" was a rust glow, which is the
// one thing this palette's shadow rule exists to avoid, drawn by the
// page that states the rule. The code line below it still prints the
// light value, because that is what the token resolves to on paper.
const shadowCards = shadows
  .map(
    (s, i) => `<div class="sh"><div class="chip" style="box-shadow:var(--shadow-${i + 1})"></div>
  <b>${esc(s.name)}</b><span>${esc(s.purpose)}</span><code>${esc(s.value)}</code></div>`,
  )
  .join("\n");

// A component the size of a page (the header, a catalogue table) is
// unreadable in a third of a row: it wraps and overlaps and says nothing
// true about itself. Those carry `full: true` and get a row of their own,
// with a scroller so a wide one is scrolled rather than squeezed.
const componentCard = (c) => `<section class="comp${c.full ? " full" : ""}">
  <h3>${esc(c.name)}</h3><p>${esc(c.description)}</p>
  <div class="stage${c.full ? " wide" : ""}">${c.html}</div>
  <style>${c.css}</style>
</section>`;

const components = side.components.filter((c) => !c.full).map(componentCard).join("\n");
const componentsFull = side.components.filter((c) => c.full).map(componentCard).join("\n");

const list = (items, f) => items.map(f).join("");
const scales = (obj) =>
  Object.entries(obj)
    .map(([k, v]) => `<li><code>${esc(k)}</code> <b>${esc(v)}</b></li>`)
    .join("");

// **The page is a fragment and a shell, because it is one more page of
// the site.** It used to be a whole document with its own header, its
// own wordmark, its own width and no link back anywhere: the only page
// on the published site that used the window, at double its siblings'
// width, with zero outbound links — a second product under one
// navigation. What it keeps is its body, which is a specimen sheet and
// should look like one; what it loses is the chrome, which is the site's
// and is stated once, in cmd/maestro-docs.
//
// The two markers are the contract between this generator and that one.
// cmd/maestro-docs reads between them and refuses to build without them,
// so a rename here fails a build rather than publishing a page with the
// wrong frame.
const BODY_START = "<!-- site:body -->";
const BODY_END = "<!-- /site:body -->";

// Every rule this page owns is scoped under `.ds`, because it is now
// rendered inside a page that has its own. The tokens are not: they are
// the same values the site's own stylesheet declares, from the same
// source, and a token is meant to be shared.
const css = `
@font-face { font-family: Literata; font-style: normal; font-weight: 400 600; font-display: swap;
  src: url("fonts/literata-var-latin.woff2") format("woff2"); }
@font-face { font-family: "Fira Sans"; font-style: normal; font-weight: 400; font-display: swap;
  src: url("fonts/fira-sans-400-latin.woff2") format("woff2"); }
@font-face { font-family: "Fira Sans"; font-style: normal; font-weight: 600; font-display: swap;
  src: url("fonts/fira-sans-600-latin.woff2") format("woff2"); }
:root {
${vars(false)}
${shadows.map((s, i) => `  --shadow-${i + 1}: ${s.value};`).join("\n")}
}
@media (prefers-color-scheme: dark) { :root:not([data-theme="light"]) {
${vars(true)}
  --shadow-1: 0 1px 2px rgba(0,0,0,.4); --shadow-2: 0 2px 9px rgba(0,0,0,.45); --shadow-3: 0 10px 30px rgba(0,0,0,.5);
} }
.ds { color: var(--ink); font:400 ${fm.typography.body.fontSize}/${fm.typography.body.lineHeight} ${fm.typography.body.fontFamily}; }
.ds *,.ds *::before,.ds *::after { box-sizing: border-box; }
.ds .ds-lede { margin:0 0 1.5rem; color: var(--muted); max-width: 68ch; }
.ds .ds-main { display:grid; gap:1.25rem; }
.ds h2 { font:${fm.typography.headline.fontWeight} ${fm.typography.headline.fontSize}/${fm.typography.headline.lineHeight} ${fm.typography.headline.fontFamily}; margin:1.5rem 0 0; letter-spacing:${fm.typography.headline.letterSpacing}; padding:0; border:0; }
.ds .panel { background: var(--paper); border:1px solid var(--line); border-radius:${fm.rounded.sm}; box-shadow: var(--shadow-1); padding: ${fm.spacing.lg}; }
.ds code { font:400 ${fm.typography.mono.fontSize}/${fm.typography.mono.lineHeight} ${fm.typography.mono.fontFamily}; color: var(--muted); }
.ds code.dim { opacity:.75; }
.ds .swatches { display:grid; grid-template-columns:repeat(auto-fill,minmax(170px,1fr)); gap:.7rem; }
.ds .sw { display:grid; gap:1px; } .ds .sw b { font-weight:600; }
.ds .sw i { height:46px; border:1px solid var(--line); border-radius:${fm.rounded.sm}; margin-bottom:.3rem; }
.ds .data { display:flex; gap:.3rem; margin-top:.5rem; }
.ds .data i { height:30px; flex:1; border-radius:${fm.rounded.sm}; }
.ds .row { display:grid; grid-template-columns:250px minmax(0,1fr); gap:1.25rem; padding:.85rem 0; border-bottom:1px solid var(--line); align-items:baseline; }
.ds .row:last-child { border-bottom:0; }
.ds .meta { display:grid; gap:.1rem; }
.ds .meta b { font:${fm.typography.label.fontWeight} ${fm.typography.label.fontSize}/${fm.typography.label.lineHeight} ${fm.typography.label.fontFamily}; letter-spacing:${fm.typography.label.letterSpacing}; }
.ds .meta span { color:var(--muted); font-size:.8rem; }
.ds .shadows { display:grid; grid-template-columns:repeat(auto-fit,minmax(230px,1fr)); gap:1rem; }
.ds .sh { display:grid; gap:.25rem; } .ds .sh b { font-weight:600; } .ds .sh span { color:var(--muted); font-size:.82rem; }
.ds .sh .chip { height:52px; background:var(--raised); border:1px solid var(--line); border-radius:${fm.rounded.sm}; margin-bottom:.4rem; }
.ds .comps { display:grid; gap:1rem; grid-template-columns:repeat(auto-fit,minmax(320px,1fr)); }
.ds .comp h3 { font:${fm.typography.title.fontWeight} ${fm.typography.title.fontSize}/${fm.typography.title.lineHeight} ${fm.typography.title.fontFamily}; margin:0 0 .2rem; }
.ds .comp p { margin:0 0 .7rem; color:var(--muted); max-width:none; }
.ds .comp .stage { background:var(--ground); border:1px solid var(--line); border-radius:${fm.rounded.sm}; padding:1rem; }
.ds .comp .stage.wide { overflow-x:auto; padding:1rem; }
.ds .comp.full { grid-column:1 / -1; }
.ds ul.rules { margin:0; padding-left:1.1rem; display:grid; gap:.45rem; max-width:80ch; }
.ds ul.plain { list-style:none; margin:0; padding:0; display:grid; gap:.3rem; max-width:none; }
.ds .cols { display:grid; grid-template-columns:repeat(auto-fit,minmax(260px,1fr)); gap:1rem; }
.ds .cols h3, .ds .dd h3 { font:${fm.typography.label.fontWeight} ${fm.typography.label.fontSize}/${fm.typography.label.lineHeight} ${fm.typography.label.fontFamily}; letter-spacing:${fm.typography.label.letterSpacing}; color:var(--muted); margin:0 0 .4rem; }
.ds .dd { display:grid; grid-template-columns:repeat(auto-fit,minmax(320px,1fr)); gap:1rem; }
.ds .dd ul { margin:.3rem 0 0; padding-left:1.1rem; display:grid; gap:.3rem; max-width:none; }
.ds .ds-foot { margin:2rem 0 0; color:var(--muted); }
`;

const body = `${BODY_START}
<div class="ds">
<h1>${esc(side.title)}</h1>
<p class="ds-lede">${esc(side.narrative.northStar)}. ${esc(fm.description)} Every token, every named rule and every shared component, rendered by the system itself.</p>
<div class="ds-main">
  <h2>Colours</h2>
  <div class="panel"><div class="swatches">${swatches}</div>
    <p style="margin:1rem 0 .3rem"><code>data-1 &hellip; data-8 &middot; inside diagrams and legends only, never in the chrome</code></p>
    <div class="data">${dataSwatches}</div>
  </div>

  <h2>Type scale</h2>
  <div class="panel">${typeRows}</div>

  <h2>Elevation</h2>
  <div class="panel"><div class="shadows">${shadowCards}</div></div>

  <h2>Shared components</h2>
  <div class="comps">${components}</div>
  <div class="comps">${componentsFull}</div>

  <h2>Scales</h2>
  <div class="panel cols">
    <div><h3>Spacing</h3><ul class="plain">${scales(fm.spacing)}</ul></div>
    <div><h3>Radius</h3><ul class="plain">${scales(fm.rounded)}</ul></div>
    <div><h3>Density</h3><ul class="plain">${list(density, (d) => `<li><code>${esc(d.name)}</code> <b>${esc(d.value)}</b></li>`)}</ul></div>
    <div><h3>Motion</h3><ul class="plain">${list(motion, (m) => `<li><code>${esc(m.name)}</code> <b>${esc(m.value)}</b></li>`)}</ul></div>
    <div><h3>Breakpoints</h3><ul class="plain">${list(breakpoints, (b) => `<li><code>${esc(b.name)}</code> <b>${esc(b.value)}</b></li>`)}</ul></div>
  </div>

  <h2>Named rules</h2>
  <div class="panel"><ul class="rules">${list(side.narrative.rules, (r) => `<li><b>${esc(r.name)}.</b> ${esc(r.body)}</li>`)}</ul></div>

  <h2>Do and don't</h2>
  <div class="panel dd">
    <div><h3>Do</h3><ul>${list(side.narrative.dos, (x) => `<li>${esc(x)}</li>`)}</ul></div>
    <div><h3>Don't</h3><ul>${list(side.narrative.donts, (x) => `<li>${esc(x)}</li>`)}</ul></div>
  </div>
</div>
<p class="ds-foot"><code>Generated from docs/design.md and docs/design-tokens.json by docs/design-system.mjs</code></p>
</div>
${BODY_END}`;

// The standalone page, for a reader who opens this file out of the
// repository rather than off the site. It is the same body in a bare
// shell: the site's own chrome lives in cmd/maestro-docs and is not
// copied here.
const html = `<!doctype html>
<!-- Generated by docs/design-system.mjs. Do not edit by hand: edit
     docs/design.md or docs/design-tokens.json and regenerate. -->
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${esc(side.title)}</title>
<link rel="stylesheet" href="design-system.css">
<style>body { margin:0; padding:2rem; background: var(--ground); }</style>
</head><body>
${body}
</body></html>
`;

writeFileSync(join(root, "docs/design-system.css"), css.trimStart());
writeFileSync(join(root, "docs/design-system.html"), html);
console.log(
  `docs/design-system.html: ${Object.keys(colorMeta).length} colours, ` +
    `${Object.keys(fm.typography).length} type roles, ${side.components.length} components, ` +
    `${side.narrative.rules.length} named rules`,
);
