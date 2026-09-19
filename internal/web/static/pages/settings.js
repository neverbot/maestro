// A game's own settings: what it is called, and the address every URL
// into it carries.
//
// **This screen exists because an address could never be changed.** It
// is derived from the name when a game is created, which is right for
// the ninety-nine percent who never think about it and useless for the
// one who renamed their game six months in. `internal/projects` had
// `Create` and nothing else; now it has `Update`, and this is the only
// place in the product that calls it.
//
// Owner-only, and the refusal is a state rather than a disabled form: a
// reader who may not change these is told so, instead of being shown
// two inputs and a button the server will refuse.

import {
  expired,
  fillState,
  gameURL,
  openGame,
  say,
  setBreadcrumb,
} from "./page.js";
import { goToLogin, setFormBusy } from "../app.js";

// What the warning says, with this game's own address in it. The
// sentence names the thing that is about to stop working rather than
// describing the category of thing: "/g/ashfall" is a URL a person
// recognises, "your links" is not.
export const ADDRESS_WARNING_PREFIX = "Changing the address breaks every link into this game. ";

export function addressWarning(currentSlug, nextSlug) {
  const current = "/g/" + currentSlug;
  if (!nextSlug || nextSlug === currentSlug) {
    return (
      "This game is at " + current + ". Changing that address breaks every link into it: " +
      "bookmarks, links you have sent to somebody, and anything an agent has written down. " +
      "Nothing forwards the old one."
    );
  }
  return (
    ADDRESS_WARNING_PREFIX +
    current + " stops resolving and /g/" + nextSlug + " takes its place. Bookmarks, links you " +
    "have sent to somebody and anything an agent has written down will all have to be updated. " +
    "Nothing forwards the old address."
  );
}

// The two words for a reader who may not change any of this.
export const NOT_YOURS_HEADING = "Only an owner can change these";
export const NOT_YOURS_SENTENCE =
  "A game's name and address are the owner's to change, because changing the address breaks " +
  "every link into the game for everybody in it.";

export async function settingsPage(opened) {
  const doc = opened.document;
  const errorEl = doc.getElementById("settings-error");
  const form = doc.getElementById("game-settings");
  const nameEl = doc.getElementById("game-name");
  const slugEl = doc.getElementById("game-slug");
  const warningEl = doc.getElementById("address-warning");

  if (opened.game === null) {
    say(errorEl, opened.failure ?? "You may not have access to this game, or it no longer exists.");
    return opened;
  }
  setBreadcrumb(doc, [
    { label: opened.game.name, href: gameURL(opened.slug) },
    { label: "Settings" },
  ]);
  doc.title = opened.game.name + " · Settings · Maestro";

  const answer = await opened.client.summary();
  if (!answer.ok) {
    if (expired(answer)) {
      goToLogin();
      return opened;
    }
    say(errorEl, answer.error.message);
    return opened;
  }

  // Owner, and nothing less. The server refuses anything else; this is
  // the same rule said where somebody can read it.
  if (answer.result.role !== "owner") {
    fillState(doc, "settings-refused", {
      heading: NOT_YOURS_HEADING,
      sentence: NOT_YOURS_SENTENCE,
    });
    const refused = doc.getElementById("settings-refused");
    if (refused) refused.hidden = false;
    return opened;
  }

  if (form) form.hidden = false;
  if (nameEl) nameEl.value = opened.game.name;
  if (slugEl) slugEl.value = opened.slug;
  say(warningEl, addressWarning(opened.slug, opened.slug));

  // The warning follows what is typed, so it names the address that is
  // about to replace this one rather than a category of consequence.
  if (slugEl) {
    slugEl.addEventListener("input", () => {
      say(warningEl, addressWarning(opened.slug, slugEl.value.trim()));
    });
  }

  if (form) {
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      say(errorEl, "");
      setFormBusy(form, true, "Saving…");
      const result = await opened.client.updateGame({
        slug: slugEl ? slugEl.value.trim() : "",
        name: nameEl ? nameEl.value : "",
      });
      if (result.ok) {
        // **The page you are standing on is at the old address**, which
        // stopped resolving the moment that answered. Going to the new
        // one is the only correct next step, and the server sends it
        // back for exactly this.
        globalThis.window.location.href = gameURL(result.result.slug) + "/settings";
        return;
      }
      setFormBusy(form, false);
      say(errorEl, result.error.message || "Could not save these settings.");
    });
  }

  return opened;
}

if (globalThis.document && globalThis.document.getElementById("game-settings")) {
  openGame().then((opened) => {
    if (opened) void settingsPage(opened);
  });
}
