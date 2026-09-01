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
function rememberGame(slug) {
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

// fallbackMessage covers the two cases a server response can't supply its
// own words for: the network never delivered a response at all, or the
// body wasn't the JSON error shape (error, message) every handler in
// internal/web is documented to return. Every *reachable* server error
// already carries a human-readable "message" (see internal/web/auth.go's
// writeError and its call sites) — this file always prefers that text
// over inventing its own, so a wording change on the server is never
// duplicated, and never drifts, here.
const fallbackMessage = "Could not reach the server. Please try again.";

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
// reported the same shape as a server-side error, so a caller only has
// to branch on "ok" once instead of wrapping every call in its own
// try/catch.
async function postJSON(url, payload) {
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
  if (response.ok) {
    return { ok: true, response };
  }
  return { ok: false, status: response.status, message: await parseErrorBody(response) };
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

const loginForm = document.getElementById("login");
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
      // but navigate. handleRoot (internal/web/api_projects.go) decides
      // where from here: straight into the one game the user can reach, or
      // the picker.
      window.location.href = "/";
      return;
    }
    setFormBusy(loginForm, false);
    errorEl.textContent = result.message;
  });
}

// The invite form only appears when this page was reached via an invite
// link (?invite=TOKEN) — see the picker/game logic further down, and the
// visibility toggle at the bottom of this file, for why the split is on
// that query parameter rather than a second HTML file: every "invite
// redemption" concern here is just handleLogin's sibling endpoint, POST
// /api/auth/register, called with the token from the URL.
const inviteForm = document.getElementById("invite");
if (inviteForm) {
  const errorEl = document.getElementById("invite-error");
  const inviteToken = new URLSearchParams(window.location.search).get("invite") ?? "";
  inviteForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    errorEl.textContent = "";
    const data = new FormData(inviteForm);
    setFormBusy(inviteForm, true, "Creating your account…");
    const result = await postJSON("/api/auth/register", {
      email: data.get("email"),
      display_name: data.get("display_name"),
      password: data.get("password"),
      invite_token: inviteToken,
    });
    if (result.ok) {
      // Same as the login form above: a session cookie is already set,
      // there is no token for this page to hold, just navigate onward.
      window.location.href = "/";
      return;
    }
    setFormBusy(inviteForm, false);
    errorEl.textContent = result.message;
  });
}

// Toggle which of the two forms on login.html is shown, based purely on
// whether an invite token is present in the URL — see inviteForm's own
// comment above for why this is one HTML file with two forms rather than
// a second page.
const loginView = document.getElementById("login-view");
const inviteView = document.getElementById("invite-view");
if (loginView && inviteView) {
  const hasInvite = new URLSearchParams(window.location.search).has("invite");
  loginView.hidden = hasInvite;
  inviteView.hidden = !hasInvite;
}

// fetchGames wraps GET /api/games the same defensive way postJSON wraps a
// POST: a network failure or a non-JSON body is reported the same shape
// as a mapped server error, and a 401 arriving here — a session that
// expired, or was revoked in another tab, mid-browse — is treated as
// "sign in again", not as a blank or broken page, since staying on a page
// that can no longer authenticate anything it fetches serves nobody.
async function fetchGames() {
  let response;
  try {
    response = await fetch("/api/games");
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
    const body = await response.json();
    return { ok: true, games: Array.isArray(body.games) ? body.games : [] };
  } catch {
    return { ok: false, message: fallbackMessage };
  }
}

// game.html has no #games list; index.html's picker does. Splitting on
// that, rather than the URL, keeps this one file shared by both pages
// without either needing to know which page loaded it.
const gamesList = document.getElementById("games");
const statusEl = document.getElementById("status");
if (gamesList) {
  const result = await fetchGames();
  if (!result.ok) {
    if (result.expired) {
      window.location.href = "/login";
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
    const remembered = recallGame();
    if (remembered && games.some((game) => game.slug === remembered)) {
      window.location.href = `/g/${remembered}`;
    } else if (games.length === 0) {
      if (statusEl) statusEl.textContent = "You have no games yet.";
    } else {
      if (statusEl) statusEl.hidden = true;
      gamesList.hidden = false;
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

// game.html: record the slug this page was loaded for, then resolve its
// name from the same GET /api/games the picker uses — there is no
// server-side slug resolution on this route (Task 8's Round 2 Correction
// 12 — /g/{slug} only ever serves this static shell), so the slug comes
// from the URL the browser already has, and the name comes from whichever
// row in the list matches it.
const gameNameEl = document.getElementById("game-name");
if (gameNameEl) {
  const gameSlugMatch = window.location.pathname.match(/^\/g\/([^/]+)/);
  const slug = gameSlugMatch ? decodeURIComponent(gameSlugMatch[1]) : null;
  const summaryEl = document.getElementById("game-summary");

  if (slug) {
    rememberGame(slug);
  }

  const result = await fetchGames();
  if (!result.ok) {
    if (result.expired) {
      window.location.href = "/login";
    } else {
      gameNameEl.textContent = "Could not load this game";
      if (summaryEl) summaryEl.textContent = result.message;
    }
  } else {
    const game = result.games.find((g) => g.slug === slug);
    if (game) {
      // textContent, never innerHTML: see the picker's own comment above
      // — a game name is attacker-reachable input (anyone who can create
      // a game controls it), not markup this page should ever interpret.
      gameNameEl.textContent = game.name;
    } else {
      gameNameEl.textContent = "Game not found";
      if (summaryEl) {
        summaryEl.textContent = "You may not have access to this game, or it no longer exists.";
      }
    }
  }
}
