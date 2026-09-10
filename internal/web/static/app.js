// Minimal progressive behaviour. No build step, no framework yet: the real
// interface arrives with the views sub-project.

// Where the picker remembers which game this browser last visited, so a
// user who has been going straight into one game for months lands where
// they expect the moment a second game appears and the server-side
// single-game shortcut (handleRoot, internal/web/api_projects.go) stops
// applying. See Task 15's plan entry for why this lives here, client-side,
// instead of a server column: a column would cost a write on every
// navigation for a pure convenience, and a remembered value can go stale
// the moment the user loses access to that game or it is deleted, with no
// natural place server-side to notice either has happened.
const LAST_GAME_KEY = "maestro:lastGame";

// localStorage can throw (private browsing, a full quota, a disabled
// storage API) — losing this convenience must never break the page it is
// a convenience *for*, so every access goes through these two wrappers
// rather than being called inline.
// Exported since Task 15: the page that corroborates a slug against the
// caller's own game list is `pages/page.js`'s `openGame` now, and the
// remembering belongs at that corroboration and nowhere else. The picker
// below still reads it back through `recallGame`, which is why both
// wrappers stay here rather than moving with the caller.
export function rememberGame(slug) {
  try {
    localStorage.setItem(LAST_GAME_KEY, slug);
  } catch {
    // Nothing to do: the next visit just won't have a remembered slug.
  }
}
function recallGame() {
  try {
    return localStorage.getItem(LAST_GAME_KEY);
  } catch {
    return null;
  }
}

// safeReturnPath reads ?return= off the current URL — set by fetchGames
// below when a 401 interrupts an otherwise-authenticated page — and hands
// back only a same-origin path. This is the one place user-supplied text
// becomes a navigation target rather than DOM text, so it gets its own
// guard rather than trusting the query string.
//
// A pattern match on the raw string is not that guard: a review proved a
// leading-slash-count check bypassable three ways a browser's own URL
// parser disagrees with a naive regex about — "/\evil.example" and
// "/\/evil.example" (a browser's URL parser treats a backslash as a
// path separator on a special scheme, same as a second forward slash),
// and a raw control character such as a newline between two slashes
// (which the parser strips before it ever looks at the string). Every
// one of those still resolves to a different host once actually parsed,
// which a character-counting regex has no way to know without
// re-implementing the parser's own stripping and normalization rules by
// hand — and the next variant it doesn't happen to enumerate would pass
// silently. Resolving raw through the URL constructor and comparing the
// *result's* origin to this page's own does that parsing correctly by
// construction, is robust to encoded/backslash/control-character variants
// alike, and is what this function does instead: only a resolved URL
// whose origin matches is accepted, and only its parsed path (never the
// raw string) is handed to the caller.
function safeReturnPath() {
  const raw = new URLSearchParams(window.location.search).get("return");
  if (!raw) {
    return "/";
  }
  let target;
  try {
    target = new URL(raw, window.location.origin);
  } catch {
    return "/";
  }
  if (target.origin !== window.location.origin) {
    return "/";
  }
  return target.pathname + target.search + target.hash;
}

// fallbackMessage covers the two cases a server response can't supply its
// own words for: the network never delivered a response at all, or the
// body wasn't the JSON error shape (error, message) every handler in
// internal/web is documented to return. Every *reachable* server error
// already carries a human-readable "message" (see internal/web/auth.go's
// writeError and its call sites) — this file always prefers that text
// over inventing its own, so a wording change on the server is never
// duplicated, and never drifts, here.
export const fallbackMessage = "Could not reach the server. Please try again.";

// parseErrorBody reads {"error": code, "message": text} defensively: a
// response that isn't JSON, or is JSON but not that shape, must not throw
// out of a catch handler that is itself handling a failure.
async function parseErrorBody(response) {
  try {
    const body = await response.json();
    if (body && typeof body.message === "string" && body.message) {
      return body.message;
    }
  } catch {
    // Not JSON, or empty body — fall through to the generic message.
  }
  return fallbackMessage;
}

// postJSON posts a JSON body and reports the outcome without ever
// throwing: a network failure (offline, DNS, a dropped connection) is
// reported the same shape as a server-side error, so a caller only has to
// branch on "ok" once instead of wrapping every call in its own
// try/catch. On success it also hands back the parsed body, defensively —
// a handler that returns 201 with a created resource (POST /api/games) has
// something a caller needs; one that returns 204 (nothing) or 200 with a
// body a caller doesn't care about (login) simply gets an empty object.
export async function postJSON(url, payload) {
  let response;
  try {
    response = await fetch(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
  } catch {
    return { ok: false, message: fallbackMessage };
  }
  if (!response.ok) {
    return { ok: false, status: response.status, message: await parseErrorBody(response) };
  }
  let body = {};
  try {
    body = await response.json();
  } catch {
    // A 204, or any other body-less success — nothing to parse.
  }
  return { ok: true, status: response.status, body };
}

// setFormBusy disables every field and the submit button while a request
// is in flight, and swaps the button's label, so a slow connection (or a
// rate limiter's minute-long wait) can never be mistaken for a form that
// silently ate the click — a double submission on a slow link is exactly
// what this exists to prevent.
function setFormBusy(form, busy, busyLabel) {
  const button = form.querySelector("button[type=submit]");
  for (const field of form.elements) {
    field.disabled = busy;
  }
  if (button) {
    if (busy) {
      button.dataset.idleLabel = button.dataset.idleLabel ?? button.textContent;
      button.textContent = busyLabel;
    } else if (button.dataset.idleLabel) {
      button.textContent = button.dataset.idleLabel;
    }
  }
}

// GAMES_PATH is the picker's own address, and the reason it exists is
// the bug this switcher was written for: "/" is a *shortcut* (handleRoot
// sends a caller with one game straight into it, and the picker below
// sends a caller with a remembered game straight back into that one), so
// "/" is not a way out of a game — it is the way back in. /games serves
// the same shell and takes neither shortcut, so there is exactly one
// address in this product that always means "all of my games".
export const GAMES_PATH = "/games";

// gameSwitcher is the door handle on the inside of a game.
//
// Until it existed, signing in landed a designer in the remembered game
// and nothing in the product listed another one: the wordmark pointed at
// "/", "/" redirected back, and an account in three games could reach
// exactly one of them. The exit is put *here*, in the chrome beside the
// wordmark, rather than by pointing the wordmark somewhere else, for two
// reasons. It is where a person looks for "which of my things am I
// in" — and so it answers that question too: the summary is the current
// game's name, which is the only line outside the home page's own <h1>
// that says which game these pages belong to. And it is on every page of
// a game, not only the home, so the way out does not depend on first
// navigating back to a particular page of the game you are trying to
// leave.
//
// Every game the caller can reach is listed, the current one included
// and marked with aria-current rather than dropped — the same rule
// `destinations` (pages/page.js) follows, and for the same reason: a
// list that changes shape as you move through it is a list nobody learns
// the shape of.
//
// It is <details>/<summary> and not a scripted menu: it opens on click
// and on Enter, closes on Escape, is reachable by keyboard and readable
// by a screen reader without a line of JavaScript, and cannot get stuck
// open in a state this file forgot to close. Built with
// createElement/textContent throughout, never innerHTML — a game's name
// is chosen by whoever created the game.
function gameSwitcher(games, current, destinations, destination) {
  const details = document.createElement("details");
  details.className = "game-switcher";

  const summary = document.createElement("summary");
  summary.textContent = current ? current.name : "Games";
  details.append(summary);

  const list = document.createElement("ul");
  for (const game of games) {
    const item = document.createElement("li");
    const link = document.createElement("a");
    link.href = `/g/${encodeURIComponent(game.slug)}`;
    link.textContent = game.name;
    if (current && game.slug === current.slug) {
      link.setAttribute("aria-current", "page");
    }
    item.append(link);
    list.append(item);
  }

  // The last row is the picker, which is where a *new* game is made: the
  // create form used to be reachable only from the empty state, so an
  // account with one game had no way to make a second one either. Both
  // halves of that are fixed in one place — this link, and index.html's
  // form no longer living inside #empty-state.
  const all = document.createElement("li");
  all.className = "game-switcher-all";
  const allLink = document.createElement("a");
  allLink.href = GAMES_PATH;
  allLink.textContent = "All games";
  all.append(allLink);
  list.append(all);

  // **The destinations, for the widths where the bar cannot hold them.**
  // Below 780px the bar has no room and CSS hides the strip; hiding
  // navigation with nothing in its place leaves a phone with no way to
  // change section, which the frame calls out by name. They are copied
  // into this menu — copied and not moved, because the strip is the way
  // in at every width above that, and one of the two is always the one
  // showing. `.narrow-only` is what decides which, in exactly one rule.
  if (Array.isArray(destinations)) {
    const rule = document.createElement("li");
    rule.className = "game-switcher-sep narrow-only";
    list.append(rule);
    for (const [label, href] of destinations) {
      const item = document.createElement("li");
      item.className = "narrow-only";
      const link = document.createElement("a");
      link.href = href;
      link.textContent = label;
      if (label === destination) link.setAttribute("aria-current", "page");
      item.append(link);
      list.append(item);
    }
  }

  details.append(list);
  return details;
}

// renderHeader prepends the one piece of chrome every authenticated page
// (the picker, a game) shares: the product name linking back to "/", the
// game switcher, and a sign-out button. login.html never calls this —
// there is nothing to sign out of yet, and nowhere useful for "/" to
// send an anonymous visitor that isn't back to /login. Built with
// createElement/textContent throughout, never innerHTML, the same rule
// every other DOM write in this file follows.
//
// `options.games` is the caller's own game list and `options.current` is
// the game this page is inside, or null. They are **passed in and never
// fetched here**: every page that renders this header has already asked
// GET /api/games (page.js's openGame, doc.js) because it needs that list
// to resolve its own slug, so a fetch inside the header would be a
// second identical request on every page in the product. A caller with
// no list — the picker itself, which *is* the list — passes nothing and
// gets no switcher.
export function renderHeader(options = {}) {
  // Two elements, not one: the bar spans the window so its bottom rule
  // crosses the whole page, and the row inside it is capped at the page
  // width so the wordmark lines up with the content below. Built as one
  // element, the bar inherited the page's cap and drew a paper band with
  // 120px of ground either side of it on a 1680px monitor.
  const header = document.createElement("header");
  header.className = "site-header";

  const inner = document.createElement("div");
  inner.className = "header-inner";
  header.append(inner);

  const brand = document.createElement("a");
  brand.className = "brand";
  brand.href = "/";
  brand.textContent = "Maestro";
  inner.append(brand);

  const games = Array.isArray(options.games) ? options.games : [];
  if (games.length > 0) {
    inner.append(gameSwitcher(games, options.current || null, options.destinations, options.destination));
  }

  // **The destinations belong in the bar, not above it.** Every page but
  // the home used to prepend its own strip to the body *after* this
  // header was already there, which drew the three least permanent
  // things on the screen above the product's name and the way out of it.
  // The 2026-09-09 audit measured three different chromes inside one
  // game for the same reason. The caller passes the nav it has already
  // built, because this module cannot import pages/page.js without a
  // cycle.
  if (options.nav) inner.append(options.nav);

  // Everything above is identity and navigation; everything after this
  // is the account. The spacer is what makes the order an order rather
  // than a coincidence of widths.
  const spacer = document.createElement("span");
  spacer.className = "header-spacer";
  inner.append(spacer);

  const signOut = document.createElement("button");
  signOut.type = "button";
  signOut.className = "sign-out";
  signOut.textContent = "Sign out";
  signOut.addEventListener("click", async () => {
    signOut.disabled = true;
    try {
      await fetch("/api/auth/logout", { method: "POST" });
    } catch {
      // Best effort: even a failed request here still means the browser
      // is about to navigate to /login, which is the only thing that
      // actually matters to the person who just clicked this.
    }
    window.location.href = "/login";
  });
  inner.append(signOut);

  document.body.prepend(header);
}

const loginForm = document.getElementById("login");
const inviteForm = document.getElementById("invite");

// Module-scope, not declared inside the block below: the invite form's
// own submit handler (further down this file) is a separate top-level
// `if` block and needs to read the same value the block below captures
// on load — a `const` declared inside that block would go out of scope
// the moment it ends, which is exactly the bug a review caught here: the
// submit handler re-read window.location.hash instead, which
// history.replaceState had already cleared by then, so it always sent an
// empty invite_token. inviteToken is the one true reading of the token
// this page ever takes; everything downstream uses this variable, never
// the URL, which by the time a form is even submitted has already been
// scrubbed.
let inviteToken = "";

if (loginForm || inviteForm) {
  // login.html only: figure out which of the two forms to show, and with
  // what copy, before either is usable.
  //
  // The invite token travels in the URL *fragment* (#invite=…), never the
  // query string: a fragment is a browser-only construct that is never
  // sent in an HTTP request at all, so it cannot leak through Referer on
  // the very next same-origin request (this page's own subresources,
  // including this script) the way a query parameter demonstrably did in
  // review. history.replaceState below additionally scrubs it from the
  // visible URL and from browser history the moment it's read, and the
  // no-referrer <meta> on this page is a second, independent layer for
  // anything this page still sends elsewhere.
  const hashParams = new URLSearchParams(window.location.hash.replace(/^#/, ""));
  inviteToken = (hashParams.get("invite") ?? "").trim();
  if (window.location.hash) {
    history.replaceState(null, "", window.location.pathname + window.location.search);
  }

  const loginView = document.getElementById("login-view");
  const inviteView = document.getElementById("invite-view");
  const inviteCopy = document.getElementById("invite-copy");
  const modeNotice = document.getElementById("mode-notice");
  const registerToggleWrap = document.getElementById("register-toggle-wrap");
  const registerToggle = document.getElementById("register-toggle");
  const backToLogin = document.getElementById("back-to-login");

  // showInviteView is shared by two paths: an actual invite link, and the
  // self-service "Create an account" toggle below. The only difference
  // between them is the copy and whether an invite token is attached to
  // the eventual POST /api/auth/register — the form and its handler
  // (further down) do not need to know which one got them here.
  function showInviteView(copy) {
    if (!loginView || !inviteView) return;
    loginView.hidden = true;
    inviteView.hidden = false;
    if (inviteCopy) inviteCopy.textContent = copy;
    document.title = "Create your account · Maestro";
  }
  function showLoginView() {
    if (!loginView || !inviteView) return;
    loginView.hidden = false;
    inviteView.hidden = true;
    document.title = "Sign in · Maestro";
  }

  if (backToLogin) {
    backToLogin.addEventListener("click", showLoginView);
  }
  if (registerToggle) {
    registerToggle.addEventListener("click", () => {
      showInviteView("Create an account to get started.");
    });
  }

  if (inviteToken) {
    showInviteView("You have been invited to Maestro. Create your account to continue.");
  } else {
    // No invite token: tell the visitor what this instance actually
    // admits, driven by GET /api/config (a single-field, unauthenticated
    // endpoint — see internal/web/api_config.go) rather than leaving a
    // bare password box with no explanation, or leaving self-service
    // registration reachable only by someone who happens to know to edit
    // the URL by hand.
    fetch("/api/config")
      .then((response) => (response.ok ? response.json() : null))
      .then((config) => {
        if (!config) return;
        if (config.registration_mode === "domain_open") {
          if (modeNotice) {
            modeNotice.textContent = "This instance is open to anyone with an allowed email address.";
            modeNotice.hidden = false;
          }
          if (registerToggleWrap) registerToggleWrap.hidden = false;
        } else if (config.registration_mode === "invite_only") {
          if (modeNotice) {
            modeNotice.textContent = "This instance only admits invited users.";
            modeNotice.hidden = false;
          }
        }
      })
      .catch(() => {
        // The login form itself needs no server round trip to be usable,
        // so a failed config fetch just means no mode-specific copy —
        // never a broken sign-in page.
      });
  }
}

if (loginForm) {
  const errorEl = document.getElementById("login-error");
  loginForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    errorEl.textContent = "";
    const data = new FormData(loginForm);
    setFormBusy(loginForm, true, "Signing in…");
    const result = await postJSON("/api/auth/login", {
      email: data.get("email"),
      password: data.get("password"),
    });
    if (result.ok) {
      // Login sets an httpOnly session cookie server-side; this page never
      // sees or stores a token itself, so there is nothing left to do here
      // but navigate — to wherever a mid-session 401 sent this visitor to
      // sign back in from (safeReturnPath), or "/" otherwise. handleRoot
      // (internal/web/api_projects.go) decides the rest: straight into the
      // one game the user can reach, or the picker.
      window.location.href = safeReturnPath();
      return;
    }
    setFormBusy(loginForm, false);
    errorEl.textContent = result.message;
  });
}

if (inviteForm) {
  const errorEl = document.getElementById("invite-error");
  inviteForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    errorEl.textContent = "";
    const data = new FormData(inviteForm);
    // The token was already read out of the URL fragment and scrubbed
    // above; it is closed over here rather than re-read from the URL,
    // which by this point history.replaceState has already cleared it —
    // re-reading here would always see an empty fragment and silently
    // send an empty invite_token on every redemption. An empty token
    // (the self-service "Create an account" path) is exactly what tells
    // POST /api/auth/register to take its domain_open branch instead of
    // trying to redeem an invite (internal/web/api_auth.go).
    setFormBusy(inviteForm, true, "Creating your account…");
    const result = await postJSON("/api/auth/register", {
      email: data.get("email"),
      display_name: data.get("display_name"),
      password: data.get("password"),
      invite_token: inviteToken,
    });
    if (result.ok) {
      window.location.href = safeReturnPath();
      return;
    }
    setFormBusy(inviteForm, false);
    errorEl.textContent = result.message;
  });
}

// fetchAPI wraps a GET the same defensive way postJSON wraps a POST: a
// network failure or a non-JSON body is reported the same shape as a
// mapped server error, and a 401 arriving here — a session that expired,
// or was revoked in another tab, mid-browse — is treated as "sign in
// again", not as a blank or broken page, since staying on a page that
// can no longer authenticate anything it fetches serves nobody.
export async function fetchAPI(path) {
  let response;
  try {
    response = await fetch(path);
  } catch {
    return { ok: false, message: fallbackMessage };
  }
  if (response.status === 401) {
    return { ok: false, expired: true };
  }
  if (!response.ok) {
    return { ok: false, message: await parseErrorBody(response) };
  }
  try {
    return { ok: true, body: await response.json() };
  } catch {
    return { ok: false, message: fallbackMessage };
  }
}

// fetchGames is fetchAPI over GET /api/games, with the one shape check
// every caller of it would otherwise repeat: a body whose "games" is not
// an array is treated as no games rather than crashing the page that is
// about to iterate it.
export async function fetchGames() {
  const result = await fetchAPI("/api/games");
  if (!result.ok) {
    return result;
  }
  return { ok: true, games: Array.isArray(result.body.games) ? result.body.games : [] };
}

// goToLogin sends the browser to sign in again, carrying the page it was
// on so a successful sign-in (safeReturnPath, above) can send it right
// back instead of stranding it on "/" regardless of where the session
// actually expired.
export function goToLogin() {
  const here = window.location.pathname + window.location.search;
  window.location.href = "/login?return=" + encodeURIComponent(here);
}

// game.html has no #games list; index.html's picker does. Splitting on
// that, rather than the URL, keeps this one file shared by both pages
// without either needing to know which page loaded it.
const gamesList = document.getElementById("games");
const statusEl = document.getElementById("status");
const emptyState = document.getElementById("empty-state");
// The create-game form's own disclosure. It ships hidden and is revealed
// by the picker below whether or not the caller has games, which is the
// second half of the same defect the switcher fixes: the form used to
// live *inside* #empty-state, so the one moment this product offered to
// create a game was the moment you had none — an account with one game
// could no more make a second than it could reach a third.
const newGame = document.getElementById("new-game");
const createGameForm = document.getElementById("create-game");
if (gamesList) {
  renderHeader();
  const result = await fetchGames();
  if (!result.ok) {
    if (result.expired) {
      goToLogin();
    } else if (statusEl) {
      statusEl.textContent = result.message;
    }
  } else {
    const games = result.games;

    // Redirect to the remembered game only once the server's own response
    // has just confirmed the user can still reach it — never off the
    // stored value alone. A slug that is no longer in this list (the game
    // was deleted, or membership was lost) is silently ignored and the
    // ordinary picker renders instead, rather than sending the user
    // toward a game that no longer answers for them.
    // **Only at "/".** The remembering is right for the common case — a
    // designer with one game should not meet a one-row picker every
    // morning — but a shortcut that fires on every rendering of this
    // shell is a shortcut with no way past it, which is exactly how an
    // account in three games came to be able to reach one. /games serves
    // this same shell and is the address that never redirects, so the
    // convenience keeps the door it was written for and stops being the
    // lock on it.
    const remembered = window.location.pathname === "/" ? recallGame() : null;
    if (remembered && games.some((game) => game.slug === remembered)) {
      window.location.href = `/g/${remembered}`;
    } else if (games.length === 0) {
      // A brand new account, or one just removed from its last game, has
      // nowhere to click at all — handleRoot's own comment names this as
      // the SPA's job, so the empty state offers the one thing that gets
      // someone unstuck: creating a game (POST /api/games already exists
      // and already accepts a session caller).
      if (statusEl) statusEl.hidden = true;
      if (emptyState) emptyState.hidden = false;
      if (newGame) {
        newGame.hidden = false;
        // Opened, not merely shown: an account with nothing has exactly
        // one useful action here, and making them click a disclosure to
        // find it would be a step for its own sake.
        newGame.open = true;
      }
    } else {
      if (statusEl) statusEl.hidden = true;
      gamesList.hidden = false;
      if (newGame) newGame.hidden = false;
      for (const game of games) {
        const item = document.createElement("li");
        const link = document.createElement("a");
        link.href = `/g/${game.slug}`;
        // textContent, never innerHTML: a game name is chosen by whoever
        // created the game, so it is untrusted input as far as this page
        // is concerned and must never be interpreted as markup.
        link.textContent = game.name;
        item.append(link);
        gamesList.append(item);
      }
    }
  }
}

if (createGameForm) {
  const errorEl = document.getElementById("create-game-error");
  createGameForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    errorEl.textContent = "";
    const data = new FormData(createGameForm);
    setFormBusy(createGameForm, true, "Creating…");
    const result = await postJSON("/api/games", {
      slug: data.get("slug"),
      name: data.get("name"),
    });
    if (result.ok && result.body && result.body.slug) {
      window.location.href = `/g/${result.body.slug}`;
      return;
    }
    setFormBusy(createGameForm, false);
    errorEl.textContent = result.ok ? fallbackMessage : result.message;
  });
}
// --- Where the game home page went -----------------------------------
//
// It is `internal/web/static/pages/home.js` now, and this file no longer
// draws a game.
//
// Task 15 made /g/{slug} the three-lane home — Views, Catalogue, Prose —
// and every call it makes goes through internal/web/static/client.js,
// which is the rule internal/web/static_client_test.go holds for
// everything this sub-project has built since Task 3. Leaving the
// catalogue here would have meant one page fetching through `fetchAPI`
// and six fetching through the client, which is exactly the drift that
// guard exists to prevent.
//
// What stays here is what the two shells this file still drives need —
// the picker (index.html), the sign-in and invite forms (login.html) —
// plus the four helpers `doc.js` and the page modules import: one fetch
// wrapper, one POST wrapper, one 401 policy and one header.
