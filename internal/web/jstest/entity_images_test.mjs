// The images attached to a thing: the section that draws them, the one
// write a person makes that no agent can, and the line the library
// prints about its own size.

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

const { entityBody, attachControl } = await import("../static/pages/entity.js");
const { weigh } = await import("../static/pages/assets.js");
const doc = dom.document;

const MODEL = {
  entity: { id: "e1", type_key: "quest", key: "first-steps", name: "First Steps", version: 1, fields: {} },
  schema: [],
  out: [],
  in: [],
  relationLabels: new Map(),
  documents: [],
  images: [],
};

function sectionOf(model) {
  const body = entityBody(doc, "azeroth", model);
  return body.children.find((child) => child.getAttribute && child.getAttribute("class") === "entity-images");
}

function walk(node, found = []) {
  if (!node || !node.children) return found;
  for (const child of node.children) {
    found.push(child);
    walk(child, found);
  }
  return found;
}

const classesIn = (node) => walk(node).map((el) => (el.getAttribute ? el.getAttribute("class") : "") || "");

// --- The section -------------------------------------------------------

{
  const section = sectionOf(MODEL);
  check("an entity with no images says so rather than showing an empty list",
    classesIn(section).some((c) => c.includes("state")), true);
  check("and draws no rows at all", walk(section).filter((el) => el.tagName === "li").length, 0);
}

const IMAGE = {
  id: "a1",
  filename: "map.png",
  mime: "image/png",
  width: 480,
  height: 300,
  url: "/api/games/azeroth/assets/a1",
};

{
  const section = sectionOf({ ...MODEL, images: [IMAGE, { ...IMAGE, id: "a2", filename: "sketch.png" }] });
  const rows = walk(section).filter((el) => el.tagName === "li");
  check("every attached image is a row", rows.length, 2);

  const link = walk(rows[0]).find((el) => el.tagName === "a");
  check("the row is a link to the file itself", link.getAttribute("href"), IMAGE.url);
  check("and says what the file is called", link.textContent, "map.png");

  // **The thumbnail is the content.** A row of filenames hides the one
  // thing a reader came to this section to tell apart.
  const thumb = walk(rows[0]).find((el) => el.tagName === "img" && el.getAttribute("class") === "asset-thumb");
  check("a thumbnail sits in the row, from the same url", thumb.getAttribute("src"), IMAGE.url);
  check("and carries no alt text, the filename beside it being the row's name", thumb.getAttribute("alt"), "");

  // The preview is in the markup from the start, because the policy
  // admits no inline style attribute and so nothing can place it later.
  const preview = walk(rows[0]).find((el) => el.getAttribute && el.getAttribute("class") === "image-preview");
  check("the preview is drawn with the row and hidden by the stylesheet",
    [preview.getAttribute("src"), preview.getAttribute("aria-hidden")],
    [IMAGE.url, "true"]);
  // Its box is stated as geometry attributes, which are not CSS: the
  // browser holds the space before the bytes land and nothing jumps.
  check("and holds its own box before it loads",
    [preview.getAttribute("width"), preview.getAttribute("height")], ["480", "300"]);

  check("the pixel size is beside the name", walk(rows[0])
    .filter((el) => (el.getAttribute && el.getAttribute("class")) === "catalogue-count")
    .map((el) => el.textContent), ["480×300"]);
}

// --- The one write a person makes --------------------------------------

{
  const uploads = [];
  const attaches = [];
  const opened = {
    client: {
      uploadAsset: async (file, name) => {
        uploads.push(name);
        return { ok: true, result: { id: "a9" } };
      },
      attachEntityImage: async (typeKey, key, id) => {
        attaches.push([typeKey, key, id]);
        return { ok: true, result: { items: [{ ...IMAGE, id }] } };
      },
    },
  };
  let handed = null;
  const control = attachControl(doc, opened, "quest", "first-steps", (items) => { handed = items; });

  // **A word, not a filled button.** This page is read; the way in to
  // its most secondary act costs it a word, which is the shape the
  // comment composer beside it already uses.
  const open = control.children[0];
  check("the way in is a quiet word", open.getAttribute("class"), "quiet attach-open");
  const form = control.children[1];
  check("and the form is closed until it is asked for", form.hidden, true);

  const bounds = walk(form).find((el) => el.getAttribute && el.getAttribute("class") === "attach-bounds");
  check("the bounds are stated before anything is chosen", bounds.textContent !== "", true);

  const file = walk(form).find((el) => el.tagName === "input");
  check("the picker admits only what the server admits",
    file.accept, "image/png,image/jpeg,image/webp");
  // **The words on the picker are this product's.** A bare file input
  // wears the browser's own label, in the browser's own language, and
  // ::file-selector-button cannot rename it.
  const label = walk(form).find((el) => el.tagName === "label");
  check("the picker wears a word from the catalogue, tied to the real control",
    [label.textContent, label.getAttribute("for"), file.id],
    ["Choose a file", "attach-file", "attach-file"]);
  check("and says, in the same words, that nothing is chosen yet",
    walk(form).find((el) => (el.getAttribute && el.getAttribute("class")) === "catalogue-count").textContent,
    "No file chosen");
  // The bounds are the field's own description, not a sentence further
  // down the page.
  check("the bounds are tied to the field", file.getAttribute("aria-describedby"), bounds.id);

  const error = walk(form).find((el) => (el.getAttribute && el.getAttribute("class")) === "error");
  check("a refusal here is announced, as the rename's on this page is",
    [error.getAttribute("role"), error.getAttribute("aria-live")], ["alert", "assertive"]);

  await open.dispatch("click");
  check("the word opens the form and takes itself away", [form.hidden, open.hidden], [false, true]);

  // **The bound is the form's, not only the server's.** The sentence
  // above the field promises 8 MB; sending the bytes anyway makes a
  // person wait out an upload to be told what the form already knew.
  file.files = [{ name: "huge.png", size: 9 * 1024 * 1024 }];
  await form.dispatch("submit", { preventDefault() {} });
  check("a file over the bound is refused without being sent",
    [uploads.length, error.textContent],
    [0, "That file is over 8 MB. Nothing was sent."]);

  // **Two calls in one order**: the file becomes an image in the game's
  // library, then the image is hung on this entity.
  file.files = [{ name: "map.png", size: 2048 }];
  await form.dispatch("submit", { preventDefault() {} });
  check("the file is uploaded to the library first", uploads, ["map.png"]);
  check("then attached to this entity by the id the upload answered with",
    attaches, [["quest", "first-steps", "a9"]]);
  check("and the section is handed the whole listing back, so nothing reloads",
    handed.map((item) => item.id), ["a9"]);
  check("the form closes itself once the image is there", [form.hidden, open.hidden], [true, false]);
  // Hiding the control somebody is standing on leaves the focus
  // nowhere, and a keyboard starts again from the top of the document.
  check("and hands focus back to the word that opened it", doc.activeElement === open, true);
}

{
  // A refusal is shown where the control is, and the form stays open on
  // the file that caused it.
  const opened = {
    client: {
      uploadAsset: async () => ({ ok: false, error: { message: "that is not an image this game admits" } }),
      attachEntityImage: async () => ({ ok: true, result: { items: [] } }),
    },
  };
  const control = attachControl(doc, opened, "quest", "first-steps", () => {});
  const form = control.children[1];
  const file = walk(form).find((el) => el.tagName === "input");
  await control.children[0].dispatch("click");
  file.files = [{ name: "notes.txt", size: 10 }];
  await form.dispatch("submit", { preventDefault() {} });
  const error = walk(form).find((el) => (el.getAttribute && el.getAttribute("class")) === "error");
  check("a refusal is the server's own sentence, under the control",
    error.textContent, "that is not an image this game admits");
  check("and the form stays open on the file that caused it", form.hidden, false);
}

// --- What the library weighs -------------------------------------------

// **The units are symbols and not words.** kB and MB read the same in
// both languages; the number goes through the reader's locale because a
// decimal comma and a decimal point are not the same number.
check("a library's weight is the shortest honest line about it",
  ["", weigh(0), weigh(900), weigh(33_000), weigh(12_300_000), weigh(4_100_000_000)].slice(1),
  ["", "900 B", "33 kB", "12.3 MB", "4.1 GB"]);
check("and nothing at all is said about a count that is not one", weigh("many"), "");

// --- Taking one off ----------------------------------------------------

// **The client has had a detach since the day this shipped and nothing
// called it.** A person who attached the wrong map could not remove it
// and neither could an agent, these writes having no MCP mirror on
// purpose: a method nobody calls is the plainest form of a mechanism
// nothing reads.
{
  const section = sectionOf({ ...MODEL, images: [IMAGE] });
  const rowNoWrite = walk(section).filter((el) => el.tagName === "li")[0];
  check("a reader who may not write is offered no way to take one off",
    walk(rowNoWrite).filter((el) => el.tagName === "button").length, 0);
}

process.exit(failures === 0 ? 0 : 1);
