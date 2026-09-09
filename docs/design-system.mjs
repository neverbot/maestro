// Renders docs/design-system.html from the two files that own the design
// system: design.md's frontmatter (the normative token layer) and
// .impeccable/design.json (the sidecar: canonical OKLCH, shadows, motion,
// component snippets, named rules).
//
// The page is a committed artefact, and it is generated rather than
// hand-written for the reason a generated artefact usually is: a hand-kept
// copy of a token table goes stale silently, and nothing anywhere turns
// red. Change a token in design.md, run this, commit both.
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

const fm = frontmatter(readFileSync(join(root, "design.md"), "utf8"));
const side = JSON.parse(readFileSync(join(root, ".impeccable/design.json"), "utf8"));
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

const shadowCards = shadows
  .map(
    (s) => `<div class="sh"><div class="chip" style="box-shadow:${s.value}"></div>
  <b>${esc(s.name)}</b><span>${esc(s.purpose)}</span><code>${esc(s.value)}</code></div>`,
  )
  .join("\n");

const components = side.components
  .map(
    (c) => `<section class="comp">
  <h3>${esc(c.name)}</h3><p>${esc(c.description)}</p>
  <div class="stage">${c.html}</div>
  <style>${c.css}</style>
</section>`,
  )
  .join("\n");

const list = (items, f) => items.map(f).join("");
const scales = (obj) =>
  Object.entries(obj)
    .map(([k, v]) => `<li><code>${esc(k)}</code> <b>${esc(v)}</b></li>`)
    .join("");

const html = `<!doctype html>
<!-- Generated by docs/design-system.mjs. Do not edit by hand: edit
     design.md or .impeccable/design.json and regenerate. -->
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${esc(side.title)}</title>
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Fira+Sans:wght@400;500;600&family=Fira+Mono&family=Literata:opsz,wght@7..72,400;7..72,500;7..72,600&display=swap" rel="stylesheet">
<style>
:root {
${vars(false)}
${shadows.map((s, i) => `  --shadow-${i + 1}: ${s.value};`).join("\n")}
}
@media (prefers-color-scheme: dark) { :root:not([data-theme="light"]) {
${vars(true)}
  --shadow-1: 0 1px 2px rgba(0,0,0,.4); --shadow-2: 0 2px 9px rgba(0,0,0,.45); --shadow-3: 0 10px 30px rgba(0,0,0,.5);
} }
*,*::before,*::after { box-sizing: border-box; }
body { margin:0; background: var(--ground); color: var(--ink); font:400 ${fm.typography.body.fontSize}/${fm.typography.body.lineHeight} ${fm.typography.body.fontFamily}; }
header { padding: 2rem 2rem 1rem; }
header h1 { font:${fm.typography.display.fontWeight} ${fm.typography.display.fontSize}/${fm.typography.display.lineHeight} ${fm.typography.display.fontFamily}; margin:0 0 .3rem; letter-spacing:${fm.typography.display.letterSpacing}; }
header p { margin:0; color: var(--muted); max-width: 68ch; }
main { padding: 0 2rem 4rem; display:grid; gap:1.25rem; }
h2 { font:${fm.typography.headline.fontWeight} ${fm.typography.headline.fontSize}/${fm.typography.headline.lineHeight} ${fm.typography.headline.fontFamily}; margin:1.5rem 0 0; letter-spacing:${fm.typography.headline.letterSpacing}; }
.panel { background: var(--paper); border:1px solid var(--line); border-radius:${fm.rounded.sm}; box-shadow: var(--shadow-1); padding: ${fm.spacing.lg}; }
code { font:400 ${fm.typography.mono.fontSize}/${fm.typography.mono.lineHeight} ${fm.typography.mono.fontFamily}; color: var(--muted); }
code.dim { opacity:.75; }
.swatches { display:grid; grid-template-columns:repeat(auto-fill,minmax(170px,1fr)); gap:.7rem; }
.sw { display:grid; gap:1px; } .sw b { font-weight:600; }
.sw i { height:46px; border:1px solid var(--line); border-radius:${fm.rounded.sm}; margin-bottom:.3rem; }
.data { display:flex; gap:.3rem; margin-top:.5rem; }
.data i { height:30px; flex:1; border-radius:${fm.rounded.sm}; }
.row { display:grid; grid-template-columns:250px minmax(0,1fr); gap:1.25rem; padding:.85rem 0; border-bottom:1px solid var(--line); align-items:baseline; }
.row:last-child { border-bottom:0; }
.meta { display:grid; gap:.1rem; }
.meta b { font:${fm.typography.label.fontWeight} ${fm.typography.label.fontSize}/${fm.typography.label.lineHeight} ${fm.typography.label.fontFamily}; letter-spacing:${fm.typography.label.letterSpacing}; }
.meta span { color:var(--muted); font-size:.8rem; }
.shadows { display:grid; grid-template-columns:repeat(auto-fit,minmax(230px,1fr)); gap:1rem; }
.sh { display:grid; gap:.25rem; } .sh b { font-weight:600; } .sh span { color:var(--muted); font-size:.82rem; }
.sh .chip { height:52px; background:var(--raised); border:1px solid var(--line); border-radius:${fm.rounded.sm}; margin-bottom:.4rem; }
.comps { display:grid; gap:1rem; grid-template-columns:repeat(auto-fit,minmax(320px,1fr)); }
.comp h3 { font:${fm.typography.title.fontWeight} ${fm.typography.title.fontSize}/${fm.typography.title.lineHeight} ${fm.typography.title.fontFamily}; margin:0 0 .2rem; }
.comp p { margin:0 0 .7rem; color:var(--muted); }
.comp .stage { background:var(--ground); border:1px solid var(--line); border-radius:${fm.rounded.sm}; padding:1rem; }
ul.rules { margin:0; padding-left:1.1rem; display:grid; gap:.45rem; max-width:80ch; }
ul.plain { list-style:none; margin:0; padding:0; display:grid; gap:.3rem; }
.cols { display:grid; grid-template-columns:repeat(auto-fit,minmax(260px,1fr)); gap:1rem; }
.cols h3, .dd h3 { font:${fm.typography.label.fontWeight} ${fm.typography.label.fontSize}/${fm.typography.label.lineHeight} ${fm.typography.label.fontFamily}; letter-spacing:${fm.typography.label.letterSpacing}; color:var(--muted); margin:0 0 .4rem; }
.dd { display:grid; grid-template-columns:repeat(auto-fit,minmax(320px,1fr)); gap:1rem; }
.dd ul { margin:.3rem 0 0; padding-left:1.1rem; display:grid; gap:.3rem; }
footer { padding:0 2rem 3rem; color:var(--muted); }
</style></head><body>
<header>
  <h1>${esc(fm.name)}</h1>
  <p>${esc(side.narrative.northStar)}. ${esc(fm.description)} Every token, every named rule and every shared component, rendered by the system itself.</p>
</header>
<main>
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
</main>
<footer><code>Generated from design.md and .impeccable/design.json by docs/design-system.mjs</code></footer>
</body></html>
`;

writeFileSync(join(root, "docs/design-system.html"), html);
console.log(
  `docs/design-system.html: ${Object.keys(colorMeta).length} colours, ` +
    `${Object.keys(fm.typography).length} type roles, ${side.components.length} components, ` +
    `${side.narrative.rules.length} named rules`,
);
