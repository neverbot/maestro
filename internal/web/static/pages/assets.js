// The images this game has uploaded, paged.

import {
  DESTINATION_IMAGES,
  countLabel,
  destinations,
  emptyOrRows,
  expired,
  fillState,
  gameURL,
  openGame,
  say,
  setBreadcrumb,
  setReadOnly,
} from "./page.js";
import { isDrawableHref } from "../render/scene.js";
import { locale, t } from "../i18n.js";
import { headerRow, imageRow, nextCursorOf, row } from "../rows.js";
import { goToLogin } from "../app.js";

// The empty state, in the page rather than in the shell. It names where
// an image actually comes from, because nothing on this screen uploads
// one.
export const NO_IMAGES_HEADING = t("images.none.heading");
export const NO_IMAGES_SENTENCE = t("images.none.sentence");

// Where an uploaded image is served from: **the URL the server spelled**,
// filtered through the one href rule this front end has.
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

// weigh turns a byte count into the shortest honest line about it.
//
// **The units are symbols and not words**, so they do not pass through
// the catalogue: kB and MB read the same in both languages and a
// translated "MB" would be a mistranslation waiting to happen. The
// number does go through the reader's locale, because a decimal comma
// and a decimal point are not the same number to the person reading it.
export function weigh(bytes) {
  const n = Number(bytes);
  if (!Number.isFinite(n) || n <= 0) return "";
  const units = ["B", "kB", "MB", "GB"];
  let step = 0;
  let size = n;
  while (size >= 1000 && step < units.length - 1) {
    size /= 1000;
    step += 1;
  }
  // Whole bytes and kilobytes; one decimal from a megabyte up, where it
  // is the digit that distinguishes two libraries.
  const digits = step >= 2 && size < 100 ? 1 : 0;
  return new Intl.NumberFormat(locale, {
    minimumFractionDigits: digits, maximumFractionDigits: digits,
  }).format(size) + "\u00a0" + units[step];
}

export async function assetsPage(opened) {
  const doc = opened.document;
  const noteEl = doc.getElementById("assets-note");
  fillState(doc, "assets-empty", {
    heading: NO_IMAGES_HEADING,
    sentence: NO_IMAGES_SENTENCE,
  });
  const listEl = doc.getElementById("assets");
  const emptyEl = doc.getElementById("assets-empty");
  const errorEl = doc.getElementById("assets-error");
  const moreEl = doc.getElementById("assets-more");

  if (opened.game === null) {
    say(noteEl, opened.failure ?? t("error.noAccessToGame"));
    return opened;
  }
  setBreadcrumb(doc, [
    { label: opened.game.name, href: gameURL(opened.slug) },
    { label: DESTINATION_IMAGES },
  ]);
  // The game first: a person with three games open read three tabs
  // called "Images".
  doc.title = opened.game.name + " \u00b7 " + t("nav.images") + " \u00b7 Maestro";
  const role = await opened.client.summary();
  if (role.ok) setReadOnly(doc, role.result.role, "writes.images");

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
    // (api_assets.go), so the page rendered "No images yet" over a
    // game whose map view was drawing one of these files at the time.
    // Everything below this line had therefore never run in a browser,
    // and two of its lines were separately wrong: the row it built by
    // hand put three children into a six-track subgrid, so a size landed
    // in the first content cell instead of the count, and the thumbnail
    // repeated the visible filename as its alt text, which a screen
    // reader reads twice. Both are gone with the hand-built row.
    const items = Array.isArray(body.assets) ? body.assets : [];
    // The same header the catalogue and the views list carry. This
    // listing puts the pixel size in the first content cell and the id
    // in the key track, and neither was named.
    if (rendered === 0 && items.length > 0) {
      listEl.append(headerRow(doc, { label: t("images.column.image"), key: "id", cells: [{ text: t("images.column.size") }] }));
    }
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

      // The same row the entity page draws, from the same builder: the
      // thumbnail, and the preview under the pointer. This list had the
      // first and not the second while the entity's had both, over the
      // same files, and a reader who learned the gesture on one screen
      // found it dead on the other.
      imageRow(doc, item, { url: source, width: asset.width, height: asset.height });

      listEl.append(item);
    }
    rendered += items.length;
    emptyOrRows(listEl, emptyEl, rendered);
    // **The whole library, not this page.** The count used to be the
    // rows rendered so far, which climbed as a reader pressed "show
    // more" and never said how much there was. With no cap on how many
    // images a game may hold, what took the cap's place is being able
    // to see what it is carrying, so the line is the library's own
    // count and the library's own weight.
    const held = Number(body.held);
    const line = Number.isFinite(held) && held > 0
      ? [countLabel(held, t("unit.image"), t("unit.images")), weigh(body.bytes)]
        .filter((part) => part !== "").join(" \u00b7 ")
      : "";
    say(noteEl, line);
    cursor = nextCursorOf(body);
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
