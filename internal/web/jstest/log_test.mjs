// The band beside a thing's content: the log, its one write, and the
// phrase a reader actually wants out of a timestamp.

import path from "node:path";
import { register } from "node:module";
import { fileURLToPath } from "node:url";

register(new URL("./importmap_loader.mjs", import.meta.url), {
  parentURL: import.meta.url,
  data: { staticDir: path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "static"), shell: "entity.html" },
});

import { install } from "./svg_dom.mjs";

const dom = install();
dom.document.getElementById = () => null;
dom.document.querySelector = () => null;

let failures = 0;
function check(what, got, want) {
  if (JSON.stringify(got) === JSON.stringify(want)) {
    console.log("ok   " + what);
    return;
  }
  failures += 1;
  console.log("FAIL " + what + "\n  got  " + JSON.stringify(got) + "\n  want " + JSON.stringify(want));
}

const { logBand, when } = await import("../static/log.js");
const doc = dom.document;

// --- How long ago ------------------------------------------------------

// A log answers "when" with how long ago, not with a date a reader has
// to subtract from today.
const now = Date.parse("2026-10-07T12:00:00Z");
check("a log says how long ago, in the four steps a log needs",
  [
    when(now, "2026-10-07T11:59:40Z"),
    when(now, "2026-10-07T11:20:00Z"),
    when(now, "2026-10-07T04:00:00Z"),
    when(now, "2026-10-01T12:00:00Z"),
  ],
  ["just now", "40 min ago", "8 h ago", "6 days ago"]);
check("and says nothing at all about a stamp it cannot read", when(now, "not a time"), "");

// --- The band ----------------------------------------------------------

const ENTRY = {
  id: "c1",
  on: "entity",
  author: "Administrator",
  created_at: "2026-10-07T11:00:00Z",
  body_html: "<p>Imported from the 1998 build.</p>",
};

function textOf(node) {
  if (!node || !node.children) return String(node && node.textContent ? node.textContent : "");
  return [node.textContent || "", ...node.children.map(textOf)].join(" ");
}

{
  const band = logBand(doc, { role: "editor", comments: [ENTRY], write: async () => ({ ok: true }), remove: () => {}, now });
  const list = band.children.find((child) => child.tagName === "ol");
  check("a log is a list of entries", list.children.length, 1);
  const entry = list.children[0];
  const meta = entry.children[0];
  check("an entry says who and how long ago, and offers to take itself out",
    meta.children.map((part) => part.textContent),
    ["AD", "Administrator", "1 h ago", "Remove"]);
  // **The mark is drawn, not fetched**: a third-party avatar is blocked
  // by `default-src 'self'` and would tell a stranger the hash of a
  // designer's email on every page view.
  check("the author's mark is two letters on a hue of the data palette",
    meta.children[0].getAttribute("class"), "log-chip hue-3");

  // **The tool's voice, not the game's.** A comment is what somebody
  // thought about the game and would not survive it shipping, so it is
  // the prose vocabulary in the sans.
  const body = entry.children[1].children[0];
  check("the note is rendered prose in the tool's voice", body.getAttribute("class"), "prose tool");
  check("and it is the rendering the server sent", body.innerHTML, "<p>Imported from the 1998 build.</p>");
}

// **An agent is said to be one, in a word.** The token's label is a word
// somebody chose — "rl-aeternum" reads as a person — so the kind of
// author is written out and the hue says whose it is, which is the one
// thing a mark can carry without a legend.
{
  const byAgent = { ...ENTRY, author: "rl-aeternum", by_agent: true, author_of: "Administrator" };
  const band = logBand(doc, { role: "editor", comments: [byAgent], now });
  const meta = band.children.find((child) => child.tagName === "ol").children[0].children[0];
  check("an agent's entry says so and says whose agent it is",
    meta.children.map((part) => part.textContent),
    ["RA", "rl-aeternum", "Administrator's agent", "1 h ago"]);
  // The hue is the owner's, so a person and their agent share one.
  const mine = logBand(doc, { role: "editor", comments: [{ ...ENTRY, author: "Administrator", author_of: "Administrator" }], now });
  const hueOf = (b) => b.children.find((c) => c.tagName === "ol").children[0].children[0].children[0].getAttribute("class");
  check("and wears the hue of the person it belongs to", hueOf(band), hueOf(mine));
}

// A log is read oldest first, whatever order the server answered in, and
// nothing is left out: a cap would hide what a reader does not know is
// there.
{
  const older = { ...ENTRY, id: "c0", body_html: "<p>first</p>", created_at: "2026-10-06T11:00:00Z" };
  const newer = { ...ENTRY, id: "c1", body_html: "<p>second</p>", created_at: "2026-10-07T11:00:00Z" };
  const band = logBand(doc, { role: "editor", comments: [newer, older], now, write: async () => ({ ok: true }) });
  const list = band.children.find((child) => child.tagName === "ol");
  check("the newest the server sent first is drawn last",
    list.children.map((item) => item.children[1].children[0].innerHTML),
    ["<p>first</p>", "<p>second</p>"]);
  // And the one write sits at the end of what it writes into.
  check("the box is the last thing in the band", band.children.at(-1).getAttribute("class"), "log-composer");
}

// A log nobody has written in says so, and says who would.
{
  const band = logBand(doc, { role: "editor", comments: [], write: async () => ({ ok: true }), now });
  const list = band.children.find((child) => child.tagName === "ol");
  check("an empty log draws no list", list.hidden, true);
  check("and says what the band is for", textOf(band).includes("Nothing has been written here yet"), true);
}

// A reader who may not write gets no box at all, rather than one that
// cannot succeed.
{
  const band = logBand(doc, { role: "viewer", comments: [ENTRY], now });
  check("a reader is offered no way to write", band.children.some((child) => child.tagName === "form"), false);
  const list = band.children.find((child) => child.tagName === "ol");
  check("and the entry offers no way to remove itself",
    list.children[0].children[0].children.map((part) => part.textContent),
    ["AD", "Administrator", "1 h ago"]);
}

// The one write: an empty box is refused here rather than at the server,
// which is a round trip for a mistake the page can see.
{
  const sent = [];
  const band = logBand(doc, { role: "editor", comments: [], now, write: async (body) => { sent.push(body); return { ok: true }; } });
  const form = band.children.find((child) => child.tagName === "form");
  const box = form.children[0];
  box.value = "   ";
  await form.dispatch("submit", { preventDefault() {} });
  check("an empty comment never reaches the server", sent, []);
  box.value = "  Imported from the 1998 build.  ";
  await form.dispatch("submit", { preventDefault() {} });
  check("and a written one arrives trimmed", sent, ["Imported from the 1998 build."]);
  check("and the box is cleared for the next one", box.value, "");
}

if (failures > 0) {
  console.log(failures + " failed");
  process.exit(1);
}
console.log("all passed");
