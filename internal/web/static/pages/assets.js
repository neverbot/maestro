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
    const items = Array.isArray(body.items) ? body.items : [];
    for (const asset of items) {
      const item = doc.createElement("li");

      const source = assetURL(asset);
      const thumb = doc.createElement("img");
      thumb.className = "asset-thumb";
      if (source !== "") thumb.src = source;
      // The filename is prose a designer typed and is stored for them to
      // read; it is never consulted for the format, and it is the only
      // honest alt text this page has.
      thumb.alt = String(asset.filename ?? "");
      item.append(thumb);

      const name = doc.createElement("span");
      name.className = "catalogue-label";
      name.textContent = String(asset.filename ?? "");
      item.append(name);

      const meta = doc.createElement("span");
      meta.className = "catalogue-count";
      meta.textContent = describeAsset(asset);
      item.append(meta);

      listEl.append(item);
    }
    rendered += items.length;
    emptyOrRows(listEl, emptyEl, rendered);
    say(noteEl, rendered === 0 ? "" : countLabel(rendered, "image", "images"));
    cursor = typeof body.next_cursor === "string" ? body.next_cursor : null;
    if (moreEl) {
      moreEl.hidden = cursor === null;
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
