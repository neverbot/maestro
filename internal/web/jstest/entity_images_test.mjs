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

const { entityBody, attachControl, wireImageWrites } = await import("../static/pages/entity.js");
const { weigh } = await import("../static/rows.js");
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

// **Real files, because a browser hands this control real ones.** A
// plain object would have let the queue carry something no object URL
// can be made for, and the preview is half of what this form is for.
function fileOf(name, bytes, type = "image/png") {
  return new File([new Uint8Array(bytes)], name, { type });
}

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
  // **The same builder the images page uses**, and this list says which
  // way the preview opens because it is the one that knows it is last
  // on the page.
  check("the row is the shared image row, opening upward here",
    rows[0].getAttribute("class"), "image-row preview-up");
  // Its box is stated as geometry attributes, which are not CSS: the
  // browser holds the space before the bytes land and nothing jumps.
  check("and holds its own box before it loads",
    [preview.getAttribute("width"), preview.getAttribute("height")], ["480", "300"]);

  check("the pixel size is beside the name", walk(rows[0])
    .filter((el) => (el.getAttribute && el.getAttribute("class")) === "catalogue-count")
    .map((el) => el.textContent), ["480×300"]);
}

// --- The one write a person makes --------------------------------------

// A stub for the two calls the control makes, recording what it was
// given and answering the way the server does.
function stub(options = {}) {
  const calls = { uploads: [], attaches: [], progress: [] };
  const refuse = options.refuse || {};
  const client = {
    uploadAsset: async (file, name, onProgress) => {
      calls.uploads.push(name);
      if (typeof onProgress === "function") {
        // The client reports a fraction as the bytes go. **The bar is
        // read from inside the tick**, because that is the only moment
        // it exists: the row carries a progress element while its file
        // is in flight and a word to discard it when it is not.
        onProgress(0.5);
        calls.progress.push([name, 0.5]);
        if (typeof options.onTick === "function") options.onTick(name);
      }
      if (refuse.upload && refuse.upload[name]) {
        return { ok: false, error: { message: refuse.upload[name] } };
      }
      return { ok: true, result: { id: "u-" + name } };
    },
    attachEntityImage: async (typeKey, key, id) => {
      calls.attaches.push([typeKey, key, id]);
      if (refuse.attach && refuse.attach[id]) {
        return { ok: false, error: { message: refuse.attach[id] } };
      }
      return { ok: true, result: { items: [{ ...IMAGE, id }] } };
    },
  };
  return { calls, opened: { client } };
}

const partsOf = (control) => {
  const form = control.children.find((child) => child.tagName === "form");
  const nodes = walk(form);
  return {
    form,
    open: control.children[0],
    dropNote: control.children.find(
      (child) => child.getAttribute && child.getAttribute("class") === "attach-drop-note"),
    file: nodes.find((el) => el.tagName === "input"),
    label: nodes.find((el) => el.tagName === "label"),
    bounds: nodes.find((el) => (el.getAttribute && el.getAttribute("class")) === "attach-bounds"),
    count: nodes.find((el) => (el.getAttribute && el.getAttribute("class")) === "catalogue-count"),
    queue: nodes.find((el) => (el.getAttribute && el.getAttribute("class")) === "attach-queue"),
    error: nodes.find((el) => (el.getAttribute && el.getAttribute("class")) === "error"),
  };
};

const queueRows = (parts) => parts.queue.children;
const inRow = (row, className) =>
  walk(row).find((el) => (el.getAttribute && el.getAttribute("class")) === className);

{
  const bars = [];
  let parts = null;
  const { calls, opened } = stub({
    onTick: () => {
      const row = queueRows(parts)[0];
      const bar = inRow(row, "attach-progress");
      bars.push(bar ? [bar.tagName, bar.max, bar.value, bar.getAttribute("aria-label")] : null);
      check("while a file is in flight its row offers no way to discard it",
        inRow(row, "quiet attach-discard"), undefined);
    },
  });
  let handed = null;
  let keptOpen;
  const control = attachControl(doc, opened, "quest", "first-steps", (items, keep) => {
    handed = items;
    keptOpen = keep;
  });
  parts = partsOf(control);

  // **A word, not a filled button.** This page is read; the way in to
  // its most secondary act costs it a word, which is the shape the
  // comment composer beside it already uses.
  check("the way in is a quiet word", parts.open.getAttribute("class"), "quiet attach-open");
  check("and the form is closed until it is asked for", parts.form.hidden, true);

  check("the bounds are stated before anything is chosen", parts.bounds.textContent !== "", true);
  // **The sentence that names the drop is outside the form.** Inside
  // it, it would be readable only once the form is open — which is the
  // one moment a person is not about to drag a file at a closed one.
  check("and what a drop would do is said outside the form, which is shut",
    parts.dropNote.textContent, "Drop them here to attach them.");
  check("the picker admits only what the server admits",
    parts.file.accept, "image/png,image/jpeg,image/webp");
  // **Several in one gesture.** A designer arrives having chosen the
  // three maps of a zone together, in whatever folder they live in.
  check("and takes more than one file at a time", parts.file.multiple, true);
  // **The words on the picker are this product's.** A bare file input
  // wears the browser's own label, in the browser's own language, and
  // ::file-selector-button cannot rename it.
  check("the picker wears a word from the catalogue, tied to the real control",
    [parts.label.textContent, parts.label.getAttribute("for"), parts.file.id],
    ["Choose files", "attach-file", "attach-file"]);
  check("and says, in the same words, that nothing is chosen yet",
    parts.count.textContent, "No file chosen");
  // The bounds are the field's own description, not a sentence further
  // down the page.
  check("the bounds are tied to the field", parts.file.getAttribute("aria-describedby"), parts.bounds.id);
  check("a refusal here is announced, as the rename's on this page is",
    [parts.error.getAttribute("role"), parts.error.getAttribute("aria-live")], ["alert", "assertive"]);

  await parts.open.dispatch("click");
  check("the word opens the form and takes itself away",
    [parts.form.hidden, parts.open.hidden], [false, true]);

  // --- The queue -------------------------------------------------------
  parts.file.files = [fileOf("map.png", 2048), fileOf("sketch.png", 4096)];
  await parts.file.dispatch("change");
  check("every file chosen is a row of its own", queueRows(parts).length, 2);
  check("and the count is said in the reader's own words, not the browser's",
    parts.count.textContent, "2 files");
  check("a row names its file and weighs it",
    [inRow(queueRows(parts)[0], "attach-name").textContent,
      inRow(queueRows(parts)[0], "catalogue-count").textContent],
    ["map.png", weigh(2048)]);
  check("and shows it, because a form that takes an image and draws only its name hides it",
    inRow(queueRows(parts)[0], "asset-thumb attach-thumb").getAttribute("src").startsWith("blob:"), true);

  // **The input's own list cannot have one taken out of it.** Picking
  // again used to replace the lot, so a person who chose four files and
  // meant three had no way back.
  const discard = inRow(queueRows(parts)[1], "quiet attach-discard");
  check("each row carries the word that takes it back out, named for its file",
    discard.getAttribute("aria-label"), "Discard sketch.png");
  await discard.dispatch("click");
  check("discarding one leaves the other where it was",
    [queueRows(parts).length, inRow(queueRows(parts)[0], "attach-name").textContent, parts.count.textContent],
    [1, "map.png", "1 file"]);

  // A second gesture adds to the queue instead of replacing it.
  parts.file.files = [fileOf("legend.webp", 512, "image/webp")];
  await parts.file.dispatch("change");
  check("a second gesture adds to what was already chosen",
    queueRows(parts).map((row) => inRow(row, "attach-name").textContent),
    ["map.png", "legend.webp"]);

  // --- Sending ---------------------------------------------------------
  await parts.form.dispatch("submit", { preventDefault() {} });
  check("every queued file is uploaded to the library first", calls.uploads, ["map.png", "legend.webp"]);
  check("then attached to this entity by the id each upload answered with",
    calls.attaches, [["quest", "first-steps", "u-map.png"], ["quest", "first-steps", "u-legend.webp"]]);
  check("and progress is asked for, so a slow link says how far it has got",
    calls.progress, [["map.png", 0.5], ["legend.webp", 0.5]]);
  // The bar is a real `<progress>` carrying the fraction, named for the
  // file it is about: "Sending" eight times over is eight bars a screen
  // reader cannot tell apart.
  check("and the fraction is drawn, in the row of the file it is about",
    bars, [["progress", 1, 0.5, "Sending map.png"], ["progress", 1, 0.5, "Sending legend.webp"]]);
  check("the section is handed the last whole listing back, so nothing reloads",
    handed.map((item) => item.id), ["u-legend.webp"]);
  check("with nothing to keep open, everything having landed", keptOpen, undefined);
  check("the form closes itself once the images are there",
    [parts.form.hidden, parts.open.hidden, parts.count.textContent],
    [true, false, "No file chosen"]);
  // Hiding the control somebody is standing on leaves the focus
  // nowhere, and a keyboard starts again from the top of the document.
  check("and hands focus back to the word that opened it", doc.activeElement === parts.open, true);
}

// --- What the form refuses on its own ----------------------------------

{
  // **The bound is the form's, not only the server's.** The sentence
  // above the field promises 8 MB; sending the bytes anyway makes a
  // person wait out an upload to be told what the form already knew.
  // And it is **per file**: three good maps and one stray should attach
  // three maps.
  const { calls, opened } = stub();
  const control = attachControl(doc, opened, "quest", "first-steps", () => {});
  const parts = partsOf(control);
  await parts.open.dispatch("click");
  parts.file.files = [fileOf("huge.png", 9 * 1024 * 1024), fileOf("notes.txt", 10, "text/plain"), fileOf("map.png", 64)];
  await parts.file.dispatch("change");
  check("a file over the bound is refused in its own row",
    inRow(queueRows(parts)[0], "attach-note").textContent, "That file is over 8 MB. Nothing was sent.");
  check("and so is something that is not an image at all",
    inRow(queueRows(parts)[1], "attach-note").textContent,
    "This game takes PNG, JPEG and WebP. Nothing was sent.");
  check("a refused row says so with a class and not only a colour",
    queueRows(parts)[0].getAttribute("class"), "attach-item attach-refused");
  check("and carries no thumbnail, nothing having been made for it",
    inRow(queueRows(parts)[0], "asset-thumb attach-thumb"), undefined);

  await parts.form.dispatch("submit", { preventDefault() {} });
  check("only what this game takes is sent", calls.uploads, ["map.png"]);
  check("the two refusals stay on screen with the form open",
    [queueRows(parts).length, parts.form.hidden], [2, false]);
  check("and the line above them says how the gesture went",
    parts.error.textContent, "1 of 1 attached. The rest are still here, with what the server said about each.");
}

{
  // A refusal from the server is its own sentence, in the row it is
  // about, and the ones that landed leave the queue.
  const { opened } = stub({ refuse: { upload: { "notes.png": "that is not an image this game admits" } } });
  let handed = null;
  let keptOpen = null;
  const control = attachControl(doc, opened, "quest", "first-steps", (items, keep) => {
    handed = items;
    keptOpen = keep;
  });
  const parts = partsOf(control);
  await parts.open.dispatch("click");
  parts.file.files = [fileOf("map.png", 64), fileOf("notes.png", 64)];
  await parts.file.dispatch("change");
  await parts.form.dispatch("submit", { preventDefault() {} });
  check("the one the server refused keeps its row and the server's words",
    [queueRows(parts).length, inRow(queueRows(parts)[0], "attach-note").textContent],
    [1, "that is not an image this game admits"]);
  check("the one that landed is gone from the queue and in the listing handed up",
    handed.map((item) => item.id), ["u-map.png"]);
  // **The control is handed up with the listing.** Redrawing the
  // section replaces it, and a person reading a refusal cannot read it
  // in a form that was torn out from under them.
  check("and the control itself goes up too, to be moved rather than replaced",
    keptOpen === control, true);
  check("the form stays open on the file that caused it", parts.form.hidden, false);
}

{
  // An upload that landed and an attach that did not is the one state a
  // reader cannot deduce from the screen.
  const { opened } = stub({ refuse: { attach: { "u-map.png": "that image is attached already" } } });
  const control = attachControl(doc, opened, "quest", "first-steps", () => {});
  const parts = partsOf(control);
  await parts.open.dispatch("click");
  parts.file.files = [fileOf("map.png", 64)];
  await parts.file.dispatch("change");
  await parts.form.dispatch("submit", { preventDefault() {} });
  check("the row says both halves: what failed and what happened anyway",
    inRow(queueRows(parts)[0], "attach-note").textContent,
    "that image is attached already The file is in this game's images even so.");
}

// --- The drop ----------------------------------------------------------

{
  const { opened } = stub();
  const zone = doc.createElement("section");
  zone.className = "entity-images";
  const control = attachControl(doc, opened, "quest", "first-steps", () => {}, zone);
  const parts = partsOf(control);

  const carrying = (files) => ({
    preventDefault() {},
    dataTransfer: { types: ["Files"], files },
  });

  await zone.dispatch("dragenter", carrying([]));
  check("the whole section lights up while files are over it",
    zone.getAttribute("class"), "entity-images dropping");
  // dragleave fires on every child crossing, so the zone counts enters
  // and leaves rather than unlighting on the first one.
  await zone.dispatch("dragenter", carrying([]));
  await zone.dispatch("dragleave", {});
  check("and stays lit while the pointer is still somewhere inside it",
    zone.getAttribute("class"), "entity-images dropping");
  await zone.dispatch("dragleave", {});
  check("going out again puts it back", zone.getAttribute("class"), "entity-images");

  // A drag carrying anything else is not this control's business: a
  // zone that swallowed a dragged link would break the page's own
  // selection.
  await zone.dispatch("dragenter", { preventDefault() {}, dataTransfer: { types: ["text/plain"] } });
  check("a drag carrying no files is left alone", zone.getAttribute("class"), "entity-images");

  // **The default on a dragover is navigation.** A page that opens a
  // dropped PNG in place of itself is what a browser does unless every
  // dragover refuses it, and the refusal is not visible anywhere else:
  // the zone would light up, the drop would never arrive, and the page
  // would be gone.
  {
    let refused = 0;
    const effect = { types: ["Files"], dropEffect: "" };
    await zone.dispatch("dragover", { preventDefault() { refused += 1; }, dataTransfer: effect });
    check("a dragover refuses the browser's own default, which is to navigate to the file",
      [refused, effect.dropEffect], [1, "copy"]);
    let ignored = 0;
    await zone.dispatch("dragover", { preventDefault() { ignored += 1; }, dataTransfer: { types: ["text/plain"] } });
    check("and leaves a drag carrying something else to the page", ignored, 0);
  }

  await zone.dispatch("drop", carrying([fileOf("dropped.png", 128)]));
  check("a drop opens the form, which was never pressed open",
    [parts.form.hidden, parts.open.hidden], [false, true]);
  check("and queues what was dropped",
    queueRows(parts).map((row) => inRow(row, "attach-name").textContent), ["dropped.png"]);
  check("the zone is dark again once the files are in the form",
    zone.getAttribute("class"), "entity-images");

  // **The section is replaced on every redraw.** Listeners left on the
  // old element are listeners on a node nothing can drop onto, so the
  // gesture would work once and then go quiet.
  const fresh = doc.createElement("section");
  fresh.className = "entity-images";
  control.dropOn(fresh);
  // **Asserted on the old node and not on the new one.** Watching the
  // zone that was thrown away light up proves nothing: the handlers
  // light whichever zone the control is pointed at, so a stale listener
  // on the discarded section would have lit the visible one and this
  // check would have passed with the listeners still attached.
  check("the section the redraw threw away is not listening at all",
    Object.entries(zone.listeners).map(([kind, handlers]) => [kind, handlers.length]),
    [["dragenter", 0], ["dragover", 0], ["dragleave", 0], ["drop", 0]]);
  await zone.dispatch("drop", carrying([fileOf("stale.png", 128)]));
  check("so a drag over it does nothing to the form",
    queueRows(parts).map((row) => inRow(row, "attach-name").textContent), ["dropped.png"]);
  await fresh.dispatch("drop", carrying([fileOf("second.png", 128)]));
  check("and the one a person can see does",
    queueRows(parts).map((row) => inRow(row, "attach-name").textContent), ["dropped.png", "second.png"]);
}

// --- The knot the whole section hangs from ------------------------------
//
// **Driven through the function the page calls, not through the control
// alone.** The control hands the focus back to the word that opened its
// form and the page then *replaces* that word, so a test that only ever
// drives the control sees a focus that the product loses — which is what
// happened, and what a browser showed.

{
  const attached = [];
  const detached = [];
  let listing = [IMAGE];
  const opened = {
    client: {
      uploadAsset: async () => ({ ok: true, result: { id: "a9" } }),
      attachEntityImage: async (typeKey, key, id) => {
        attached.push(id);
        listing = [...listing, { ...IMAGE, id, filename: "second.png" }];
        return { ok: true, result: { items: listing } };
      },
      detachEntityImage: async (typeKey, key, id) => {
        detached.push(id);
        listing = listing.filter((item) => item.id !== id);
        return { ok: true, result: { items: listing } };
      },
    },
  };
  const model = { ...MODEL, images: listing };
  const body = entityBody(doc, "azeroth", model);
  const errors = doc.createElement("p");
  doc.activeElement = null;

  const section = wireImageWrites(doc, body, opened, model, errors);
  check("the writes are wired onto the section the page already drew",
    [section.getAttribute("class"), !!section.children.find((c) => (c.getAttribute && c.getAttribute("class")) === "attach")],
    ["entity-images", true]);
  check("and nothing is focused by a page merely loading", doc.activeElement, null);

  // The section is the drop zone, which is the whole reason the control
  // is told which element it is in.
  const control = section.children.find((c) => (c.getAttribute && c.getAttribute("class")) === "attach");
  await section.dispatch("dragenter", { preventDefault() {}, dataTransfer: { types: ["Files"] } });
  check("the section the page drew is the thing a file is dropped on",
    section.getAttribute("class"), "entity-images dropping");
  await section.dispatch("dragleave", {});

  // --- An attach that lands --------------------------------------------
  const parts = partsOf(control);
  await parts.open.dispatch("click");
  parts.file.files = [fileOf("second.png", 64)];
  await parts.file.dispatch("change");
  await parts.form.dispatch("submit", { preventDefault() {} });

  const after = body.querySelector(".entity-images");
  check("the image that landed is a row in the redrawn section",
    walk(after).filter((el) => el.tagName === "li").length, 2);
  const freshControl = after.children.find((c) => (c.getAttribute && c.getAttribute("class")) === "attach");
  check("the redrawn section carries a control again", !!freshControl, true);
  // **The control that had the focus was thrown away by this redraw.**
  // Handing it back is the page's job, because the page is what threw
  // it away.
  check("and the focus is on its word, not lost with the one that was replaced",
    doc.activeElement === freshControl.children[0], true);
  check("which is the word that opens the form", freshControl.children[0].textContent, "Attach an image");
  // The gesture follows the section that replaced the one it was on.
  await after.dispatch("dragenter", { preventDefault() {}, dataTransfer: { types: ["Files"] } });
  check("and a drop still works on the section a person can now see",
    after.getAttribute("class"), "entity-images dropping");
  await after.dispatch("dragleave", {});

  // --- A detach ---------------------------------------------------------
  const off = walk(after).find((el) => (el.getAttribute && el.getAttribute("class")) === "quiet image-detach");
  await off.dispatch("click");
  check("the row a person took off is gone", detached, ["a1"]);
  const last = body.querySelector(".entity-images");
  check("and the focus did not go with the button that vanished with its row",
    doc.activeElement === last.children.find((c) => (c.getAttribute && c.getAttribute("class")) === "attach").children[0],
    true);
}

// --- What the library weighs -------------------------------------------

// **The units are symbols and not words.** kB and MB read the same in
// both languages; the number goes through the reader's locale because a
// decimal comma and a decimal point are not the same number.
check("a library's weight is the shortest honest line about it",
  ["", weigh(0), weigh(900), weigh(33_000), weigh(12_300_000), weigh(4_100_000_000)].slice(1),
  ["", "900\u00a0B", "33\u00a0kB", "12.3\u00a0MB", "4.1\u00a0GB"]);
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
