// The page a mistyped address lands on.
//
// **It exists because the alternative was Go's own 404.** Any path the
// mux does not know — `/g/interface-e2e/images`, where the destination
// is called Images and the route is `/assets`; a game slug with a typo;
// a link from a document somebody moved — answered `404 page not found`
// in the browser's default serif on a transparent body, with no header,
// no game switcher and no way back. It did not look like Maestro at all,
// and the product's first design principle is that a screen says where
// you are.
//
// What it adds to the shell is the header (so there is always a way to
// another game) and one sentence naming the address that missed, with
// the one action the negative state allows.

import { fetchGames, fetchMe, goToLogin, renderHeader } from "../app.js";
import {
  DESTINATION_VIEWS,
  STATE_REFUSED,
  destinations,
  gameURL,
  negativeState,
  slugOf,
} from "./page.js";

// slugOfPath answers the game a missed address was inside, if it names
// one. `/g/le-mans/nowhere` is a wrong address *in a game*, and the way
// out of it is that game rather than the list of all of them.
export function slugOfPath(pathname) {
  return slugOf(String(pathname ?? "")) ?? "";
}

// whereTo is the one action: back into the game the address named, or to
// the games if it named none, or none at all if the game is not one this
// account can reach — an action that leads to a second refusal is worse
// than no action.
export function whereTo(pathname, games) {
  const slug = slugOfPath(pathname);
  const known = Array.isArray(games) ? games : [];
  const game = known.find((row) => row && row.slug === slug) || null;
  if (game !== null) return { href: gameURL(slug), label: game.name };
  if (known.length === 0) return null;
  return { href: "/games", label: "Your games" };
}

export function sentenceFor(pathname, games) {
  const slug = slugOfPath(pathname);
  const known = Array.isArray(games) ? games : [];
  if (slug !== "" && !known.some((row) => row && row.slug === slug)) {
    return "There is no game called “" + slug + "” that you can reach, so nothing under it has an address.";
  }
  return "Maestro has no page at " + String(pathname ?? "") + ". It may have been a typo, or a link to something that has since moved.";
}

if (globalThis.document && globalThis.document.getElementById("not-found")) {
  const doc = globalThis.document;
  const pathname = globalThis.window.location.pathname;
  const [who, answer] = await Promise.all([fetchMe(), fetchGames()]);
  if (!answer.ok && answer.expired) {
    goToLogin();
  } else {
    const games = answer.ok ? answer.games : [];
    const slug = slugOfPath(pathname);
    const game = games.find((row) => row && row.slug === slug) || null;
    // The switcher needs the list, and the destinations strip needs a
    // game: a wrong address inside a game still belongs to it, and the
    // way on is the bar.
    renderHeader({
      me: who.ok ? who.body : null,
      games,
      current: game,
      nav: game === null ? null : destinations(doc, game.slug, DESTINATION_VIEWS),
    });
    doc.getElementById("not-found").replaceChildren(
      negativeState(doc, {
        kind: STATE_REFUSED,
        heading: "There is nothing at this address",
        sentence: sentenceFor(pathname, games),
        action: whereTo(pathname, games) ?? undefined,
      }),
    );
  }
}
