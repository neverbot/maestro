// The images this game has uploaded, paged.
//
// A background belongs to a *view* — a `map` renderer is pointed at one
// from the view itself — so nothing on this page places an image or
// removes one. What it answers is the question a designer has once they
// have uploaded three maps and cannot remember which is which: what is
// in this game, how big it is, and what it looks like.
//
// The thumbnail is served by this instance, at the same route the canvas
// draws from, so a page with no outbound route shows every image on it.

import {
  DESTINATION_IMAGES,
  countLabel,
  destinations,
  emptyOrRows,
  expired,
  gameURL,
  openGame,
  say,
  setBreadcrumb,
  setReadOnly,
} from "./page.js";
import { isDrawableHref } from "../render/scene.js";
import { row } from "../rows.js";
import { goToLogin } from "../app.js";

// Where an uploaded image is served from: **the URL the server spelled**,
// filtered through the one href rule this front end has.
//
// internal/web/api_view_assets.go says outright that the URL is the
// server's to spell "rather than assembled by every client that wants to
// draw one" — a token caller and a session caller must be told the same
// address for one image — so this page reads it rather than rebuilding
// it. What it does not do is trust it: `isDrawableHref`
// (render/scene.js) is the same judge `map.js` asks before it draws a
// background, and an href it refuses is an image this page leaves out
// rather than an `<img src>` pointing anywhere at all.
export function assetURL(asset) {
  const url = asset && typeof asset.url === "string" ? asset.url : "";
  return isDrawableHref(url) ? url : "";
}

// describeAsset is the one line beside a thumbnail: what it is and how
// big. The listing carries no byte count — it would otherwise be
// megabytes a caller throws away — so the size said here is the pixel
// size, which is also the number a placement is scaled against.
export function describeAsset(asset) {
  const from = asset && typeof asset === "object" ? asset : {};
  const parts = [];
  if (Number.isFinite(from.width) && Number.isFinite(from.height)) {
    parts.push(`${from.width}×${from.height}`);
  }
  if (typeof from.mime === "string" && from.mime !== "") parts.push(from.mime);
  return parts.join(" · ");
}

export async function assetsPage(opened) {
  const doc = opened.document;
  const noteEl = doc.getElementById("assets-note");
  const listEl = doc.getElementById("assets");
  const emptyEl = doc.getElementById("assets-empty");
  const errorEl = doc.getElementById("assets-error");
  const moreEl = doc.getElementById("assets-more");

  if (opened.game === null) {
    say(noteEl, opened.failure ?? "You may not have access to this game, or it no longer exists.");
    return opened;
  }
  setBreadcrumb(doc, [
    { label: opened.game.name, href: gameURL(opened.slug) },
    { label: DESTINATION_IMAGES },
  ]);
  // The game first: a person with three games open read three tabs
  // called "Images".
  doc.title = opened.game.name + " \u00b7 Images \u00b7 Maestro";
  const role = await opened.client.summary();
  if (role.ok) setReadOnly(doc, role.result.role, "uploads these images");

  let cursor = null;
  let rendered = 0;

  async function page() {
    if (moreEl) moreEl.disabled = true;
    const answer = await opened.client.listAssets(cursor === null ? {} : { cursor });
    if (!answer.ok) {
      if (expired(answer)) {
        goToLogin();
        return;
      }
      if (emptyEl) emptyEl.hidden = true;
      if (moreEl) moreEl.hidden = true;
      say(errorEl, answer.error.message);
      return;
    }
    say(errorEl, "");
    const body = answer.result;
    // **`assets`, which is what the server writes.** This read was
    // `body.items` and the handler has always answered under `assets`
    // (api_view_assets.go), so the page rendered "No images yet" over a
    // game whose map view was drawing one of these files at the time.
    // Everything below this line had therefore never run in a browser,
    // and two of its lines were separately wrong: the row it built by
    // hand put three children into a six-track subgrid, so a size landed
    // in the first content cell instead of the count, and the thumbnail
    // repeated the visible filename as its alt text, which a screen
    // reader reads twice. Both are gone with the hand-built row.
    const items = Array.isArray(body.assets) ? body.assets : [];
    for (const asset of items) {
      const source = assetURL(asset);
      const item = row(doc, {
        label: String(asset.filename ?? ""),
        key: String(asset.id ?? ""),
        cells: [{ text: describeAsset(asset) }],
        // The image itself is the one thing this page can send a reader
        // to, and it is the same URL the canvas draws from.
        href: source === "" ? "" : source,
      });

      const thumb = doc.createElement("img");
      thumb.className = "asset-thumb";
      if (source !== "") thumb.src = source;
      // **Empty, on purpose.** The filename sits beside it in the label
      // and is the row's accessible name; alt text repeating it makes a
      // screen reader say the same words twice, and the picture carries
      // nothing the name does not.
      thumb.alt = "";
      item.firstChild.prepend(thumb);

      listEl.append(item);
    }
    rendered += items.length;
    emptyOrRows(listEl, emptyEl, rendered);
    say(noteEl, rendered === 0 ? "" : countLabel(rendered, "image", "images"));
    cursor = typeof body.next_cursor === "string" ? body.next_cursor : null;
    if (moreEl) {
      // **A pager on an empty list is a control with nothing to fetch.**
      // The condition was the cursor alone, and an empty first page that
      // still carried one left "Show more images" sitting under a state
      // that had just said there are none.
      moreEl.hidden = cursor === null || rendered === 0;
      moreEl.disabled = false;
    }
  }

  if (moreEl) moreEl.addEventListener("click", () => page());
  await page();
  return opened;
}

if (globalThis.document && globalThis.document.getElementById("assets-note")) {
  const opened = await openGame({ destination: DESTINATION_IMAGES });
  if (opened !== null) {
    const doc = opened.document;
    await assetsPage(opened);
  }
}
