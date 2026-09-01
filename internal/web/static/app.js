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

// safeReturnPath reads ?return= off the current URL — set by fetchGames
// below when a 401 interrupts an otherwise-authenticated page — and hands
// back only a value that is unambiguously a path on this same origin: it
// must start with exactly one leading slash, never two ("//evil.example"
// is parsed by a browser as a scheme-relative URL to a different host,
// not a path) and never contain a scheme of its own. Anything else,
// including a missing parameter, falls back to "/". This is the one place
// user-supplied text becomes a navigation target rather than DOM text, so
// it gets its own guard rather than trusting the query string.
function safeReturnPath() {
  const raw = new URLSearchParams(window.location.search).get("return");
  if (raw && /^\/(?!\/)/.test(raw)) {
    return raw;
  }
  return "/";
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
// reported the same shape as a server-side error, so a caller only has to
// branch on "ok" once instead of wrapping every call in its own
// try/catch. On success it also hands back the parsed body, defensively —
// a handler that returns 201 with a created resource (POST /api/games) has
// something a caller needs; one that returns 204 (nothing) or 200 with a
// body a caller doesn't care about (login) simply gets an empty object.
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

// renderHeader prepends the one piece of chrome every authenticated page
// (the picker, a game) shares: the product name linking back to "/", and
// a sign-out button. login.html never calls this — there is nothing to
// sign out of yet, and nowhere useful for "/" to send an anonymous
// visitor that isn't back to /login. Built with createElement/textContent
// throughout, never innerHTML, the same rule every other DOM write in
// this file follows.
function renderHeader() {
  const header = document.createElement("header");
  header.className = "site-header";

  const brand = document.createElement("a");
  brand.className = "brand";
  brand.href = "/";
  brand.textContent = "Maestro";
  header.append(brand);

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
  header.append(signOut);

  document.body.prepend(header);
}

const loginForm = document.getElementById("login");
const inviteForm = document.getElementById("invite");

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
  const inviteToken = (hashParams.get("invite") ?? "").trim();
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
    // which by this point history.replaceState has already cleared. An
    // empty token (the self-service "Create an account" path) is exactly
    // what tells POST /api/auth/register to take its domain_open branch
    // instead of trying to redeem an invite (internal/web/api_auth.go).
    const hashParams = new URLSearchParams(window.location.hash.replace(/^#/, ""));
    setFormBusy(inviteForm, true, "Creating your account…");
    const result = await postJSON("/api/auth/register", {
      email: data.get("email"),
      display_name: data.get("display_name"),
      password: data.get("password"),
      invite_token: hashParams.get("invite") ?? "",
    });
    if (result.ok) {
      window.location.href = safeReturnPath();
      return;
    }
    setFormBusy(inviteForm, false);
    errorEl.textContent = result.message;
  });
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

// goToLogin sends the browser to sign in again, carrying the page it was
// on so a successful sign-in (safeReturnPath, above) can send it right
// back instead of stranding it on "/" regardless of where the session
// actually expired.
function goToLogin() {
  const here = window.location.pathname + window.location.search;
  window.location.href = "/login?return=" + encodeURIComponent(here);
}

// game.html has no #games list; index.html's picker does. Splitting on
// that, rather than the URL, keeps this one file shared by both pages
// without either needing to know which page loaded it.
const gamesList = document.getElementById("games");
const statusEl = document.getElementById("status");
const emptyState = document.getElementById("empty-state");
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
    const remembered = recallGame();
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

// game.html: resolve this page's own name from GET /api/games — there is
// no server-side slug resolution on this route (Task 8's Round 2
// Correction 12 — /g/{slug} only ever serves this static shell), so the
// slug comes from the URL the browser already has, and the name comes
// from whichever row in the list matches it.
const gameNameEl = document.getElementById("game-name");
if (gameNameEl) {
  renderHeader();
  const gameSlugMatch = window.location.pathname.match(/^\/g\/([^/]+)/);
  const slug = gameSlugMatch ? decodeURIComponent(gameSlugMatch[1]) : null;
  const summaryEl = document.getElementById("game-summary");

  const result = await fetchGames();
  if (!result.ok) {
    if (result.expired) {
      goToLogin();
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
      // Recorded only now, inside the branch that just confirmed this
      // slug is actually in the server's own list — not unconditionally
      // from the URL the moment the page loads. A stray or stale link
      // (a bookmark to a deleted game, a typo, a game the user lost
      // access to) must never overwrite a good remembered value with one
      // that cannot be reached; see LAST_GAME_KEY's own comment above for
      // why a wrong remembered value is never merely harmless.
      if (slug) rememberGame(slug);
    } else {
      gameNameEl.textContent = "Game not found";
      if (summaryEl) {
        summaryEl.textContent = "You may not have access to this game, or it no longer exists.";
      }
    }
  }
}
