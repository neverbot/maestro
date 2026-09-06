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

// renderHeader prepends the one piece of chrome every authenticated page
// (the picker, a game) shares: the product name linking back to "/", and
// a sign-out button. login.html never calls this — there is nothing to
// sign out of yet, and nowhere useful for "/" to send an anonymous
// visitor that isn't back to /login. Built with createElement/textContent
// throughout, never innerHTML, the same rule every other DOM write in
// this file follows.
export function renderHeader() {
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


// --- The game home page ---
//
// What this page shows is a *catalogue with counts*, never a listing:
// the game's declared entity types and relation types, each with how
// many rows instance it, plus three totals. That is a deliberate bound
// rather than a stage on the way to showing everything — GET
// /api/games/{game}/summary answers with one row per declared type, so a
// game holding four hundred entities renders exactly as fast, and as
// small, as one holding four. Paging content belongs to the views
// sub-project, which is where a page that shows entities will get a
// cursor and a filter.

// countLabel spells a count with the right noun, so "1 entities" never
// reaches a designer's screen.
export function countLabel(count, singular, plural) {
  return `${count} ${count === 1 ? singular : plural}`;
}

// describeTotals is the one line under the game's name. An empty game
// says so in words rather than showing three zeros, which reads as a
// broken page rather than a new one.
function describeTotals(totals) {
  const entities = Number(totals.entities ?? 0);
  const relations = Number(totals.relations ?? 0);
  const invalid = Number(totals.invalid ?? 0);
  if (entities === 0 && relations === 0) {
    return "No content yet.";
  }
  const parts = [countLabel(entities, "entity", "entities"), countLabel(relations, "relation", "relations")];
  if (invalid > 0) {
    // Only when there are any: a permanent "0 no longer fit" would
    // train a designer to ignore the one number on this page that ever
    // asks them to do something.
    parts.push(`${invalid} no longer fit their type`);
  }
  return parts.join(" · ");
}

// describeWhoDeclaresTypes writes the second half of the entity-types
// empty state, which is the half that depends on who is reading.
//
// The page used to tell every reader that a type is declared over MCP or
// over the game's content routes. That is true for an editor and false
// for a viewer, whose request those same routes refuse — and a page
// telling someone to do the one thing the server will not let them do is
// worse than one that says nothing at all. GET /summary carries the
// caller's role for exactly this sentence.
//
// Anything that is not "viewer" gets the editor's sentence: viewer is
// the only role the content routes refuse, so it is the only one whose
// reader has to be told something different.
function describeWhoDeclaresTypes(role) {
  if (role === "viewer") {
    return (
      "Your role in this game is viewer, so this instance will refuse a write from you: " +
      "an editor, an admin or the owner declares them."
    );
  }
  return (
    "You declare them through this instance's API, either from an agent over MCP or " +
    "over the game's content routes. Nothing on this page creates one yet."
  );
}

// catalogueRow builds one line of a catalogue. Every string on it comes
// from the game's own content — a label a designer wrote, a key an agent
// sent — so every one goes in through textContent and never as markup,
// the same rule the picker follows for a game name.
function catalogueRow(label, key, count, invalid) {
  const item = document.createElement("li");

  const name = document.createElement("span");
  name.className = "catalogue-label";
  name.textContent = label;
  item.append(name);

  const handle = document.createElement("code");
  handle.className = "catalogue-key";
  handle.textContent = key;
  item.append(handle);

  const tally = document.createElement("span");
  tally.className = "catalogue-count";
  tally.textContent = count;
  item.append(tally);

  if (invalid > 0) {
    const flag = document.createElement("span");
    flag.className = "catalogue-invalid";
    flag.textContent = `${invalid} invalid`;
    item.append(flag);
  }
  return item;
}

// fillCatalogue renders one list and shows its empty state when there is
// nothing to render. It clears the list first (replaceChildren, not
// innerHTML) so a re-render can never double a catalogue.
function fillCatalogue(listEl, emptyEl, rows) {
  if (!listEl) {
    return;
  }
  listEl.replaceChildren();
  for (const row of rows) {
    listEl.append(row);
  }
  listEl.hidden = rows.length === 0;
  if (emptyEl) {
    emptyEl.hidden = rows.length > 0;
  }
}

// describeWhoWritesDocuments is the documents empty state's second half,
// the same shape describeWhoDeclaresTypes has and for the same reason: a
// viewer's write is refused by registerContentRoute (internal/web/
// server.go) on every prose route, so telling a viewer to write one
// would be promising an action this instance will not perform. Nothing
// on this page writes a document either way — said outright rather than
// implied by the absence of a button.
function describeWhoWritesDocuments(role) {
  if (role === "viewer") {
    return (
      "Your role in this game is viewer, so this instance will refuse a write from you: " +
      "an editor, an admin or the owner writes them."
    );
  }
  return (
    "You write them through this instance's API, either from an agent over MCP or " +
    "over the game's content routes. Nothing on this page creates one yet."
  );
}

// documentRow is catalogueRow with a link where the label goes: a
// document's title is the one thing on either catalogue that leads
// somewhere, so it is an <a> rather than a <span>, and the path it links
// to travels in the query string exactly as the API's own does (see
// internal/web/api_docs.go's header for why a document path never
// occupies a URL segment).
//
// Every string here is game content — a title a designer wrote, a path
// an agent sent — so every one goes in through textContent, the rule
// this whole file follows.
function documentRow(slug, doc) {
  const item = document.createElement("li");

  const link = document.createElement("a");
  link.className = "catalogue-label";
  link.href = `/g/${encodeURIComponent(slug)}/doc?path=${encodeURIComponent(doc.path ?? "")}`;
  link.textContent = doc.title || doc.path || "Untitled";
  item.append(link);

  const handle = document.createElement("code");
  handle.className = "catalogue-key";
  handle.textContent = doc.path ?? "";
  item.append(handle);

  const kind = document.createElement("span");
  kind.className = "catalogue-count";
  // A document need not have a kind — DocumentSummaryOutput omits an
  // empty one — and an empty cell reads better than the word "none",
  // which would look like a kind called "none".
  kind.textContent = doc.kind ?? "";
  item.append(kind);

  return item;
}

// describeDocKinds turns GET /api/games/{game}/docs/kinds into the one
// line above the documents catalogue.
//
// **A kind is free text this game invented and Maestro ships no
// vocabulary of them**, so this line is the only place a designer can
// see which kinds their own prose is using — the same job the entity-type
// catalogue above does for the types the game declared. The counts are
// beside the names for the same reason they are there: "lore 42, script
// 3" says something "lore, script" does not.
//
// The unkinded total is appended only when there is one. A permanent
// "0 with no kind" would train a reader to ignore the number, which is
// describeTotals' own rule for the invalid count.
//
// It returns the empty string when the game has no kinds at all —
// including a game with prose that none of it is filed under — and the
// caller hides the line rather than printing "no kinds", which reads as
// a fault on a page whose documents are right underneath.
function describeDocKinds(body) {
  const kinds = Array.isArray(body.kinds) ? body.kinds : [];
  if (kinds.length === 0) {
    return "";
  }
  const parts = kinds.map((k) => `${k.kind} ${Number(k.document_count ?? 0)}`);
  let line = `Kinds: ${parts.join(" · ")}`;
  const unkinded = Number(body.unkinded ?? 0);
  if (unkinded > 0) {
    line += ` · ${unkinded} with no kind`;
  }
  return line;
}

// renderDocKinds draws that line. A failed request leaves it hidden
// rather than showing a wrong vocabulary: the documents below are the
// page's subject and a missing summary above them is a smaller lie than
// a stale one.
async function renderDocKinds(gameID) {
  const el = document.getElementById("doc-kinds");
  if (!el) {
    return;
  }
  const result = await fetchAPI(`/api/games/${gameID}/docs/kinds`);
  if (!result.ok) {
    el.hidden = true;
    return;
  }
  const line = describeDocKinds(result.body ?? {});
  // textContent, like every other value on this page: a kind is game
  // content an agent wrote.
  el.textContent = line;
  el.hidden = line === "";
}

// renderDocuments fills the game page's Documents catalogue from GET
// /api/games/{game}/docs, one page at a time, and keeps the cursor the
// server issued so "Show more documents" can ask for the next.
//
// A failed request shows the server's own message and leaves both the
// list and the empty state hidden. That distinction is the point: an
// empty list and a request that never answered look identical on a page
// that renders a plausible blank, and only one of them means "this game
// has no documents".
async function renderDocuments(gameID, slug, role) {
  const listEl = document.getElementById("docs");
  const emptyEl = document.getElementById("docs-empty");
  const errorEl = document.getElementById("docs-error");
  const moreEl = document.getElementById("docs-more");
  if (!listEl) {
    return;
  }
  const actionEl = document.getElementById("docs-empty-action");
  if (actionEl) {
    actionEl.textContent = describeWhoWritesDocuments(role);
  }

  // The vocabulary first, because it describes the list below it and a
  // reader scanning down should meet it before the rows.
  await renderDocKinds(gameID);

  let cursor = null;
  let rendered = 0;

  async function loadPage() {
    if (moreEl) moreEl.disabled = true;
    const query = cursor ? `?cursor=${encodeURIComponent(cursor)}` : "";
    const result = await fetchAPI(`/api/games/${gameID}/docs${query}`);
    if (!result.ok) {
      if (result.expired) {
        goToLogin();
        return;
      }
      // Hidden, not emptied: a page that has already rendered two
      // documents and then fails to fetch the third page must keep the
      // two it has and say what went wrong beside them.
      if (emptyEl) emptyEl.hidden = true;
      if (moreEl) moreEl.hidden = true;
      if (errorEl) {
        errorEl.textContent = result.message;
        errorEl.hidden = false;
      }
      return;
    }
    if (errorEl) {
      errorEl.textContent = "";
      errorEl.hidden = true;
    }
    const body = result.body ?? {};
    const items = Array.isArray(body.items) ? body.items : [];
    for (const doc of items) {
      listEl.append(documentRow(slug, doc));
    }
    rendered += items.length;
    listEl.hidden = rendered === 0;
    if (emptyEl) emptyEl.hidden = rendered > 0;

    // The listing issues a cursor whenever a page came back full, so the
    // page that reports the end is the empty one after the last row —
    // which is why the button stays until the server stops sending a
    // cursor, rather than being hidden on a short page.
    cursor = typeof body.next_cursor === "string" ? body.next_cursor : null;
    if (moreEl) {
      moreEl.hidden = cursor === null;
      moreEl.disabled = false;
    }
  }

  if (moreEl) {
    // The handler returns loadPage's promise rather than discarding it:
    // a browser ignores the return value, and the Node harness in
    // internal/web/jstest awaits it, which is what lets a test press this
    // button and then assert what the next page rendered.
    moreEl.addEventListener("click", () => loadPage());
  }
  await loadPage();
}

// renderGameSummary draws the whole page body from one request. A
// failure leaves the game's name in place and puts the server's own
// message where the totals would have gone: a page that says what went
// wrong beats one that silently shows an empty catalogue, which is
// indistinguishable from a game with nothing in it.
async function renderGameSummary(gameID, slug) {
  const summaryEl = document.getElementById("game-summary");
  const result = await fetchAPI(`/api/games/${gameID}/summary`);
  if (!result.ok) {
    if (result.expired) {
      goToLogin();
      return;
    }
    if (summaryEl) {
      summaryEl.textContent = result.message;
    }
    // Hidden here rather than left alone. game.html ships #game-content
    // hidden and this arm never reveals it, so the two are the same
    // pixels — but "the failure path hides the catalogue" is then a
    // property of the shell and not of this function, which is exactly
    // how the harness's assertion about it came to hold whether or not
    // this code did anything. Saying it here makes the assertion an
    // assertion about the page.
    const failedContent = document.getElementById("game-content");
    if (failedContent) {
      failedContent.hidden = true;
    }
    return;
  }

  const summary = result.body ?? {};
  const entityTypes = Array.isArray(summary.entity_types) ? summary.entity_types : [];
  const relationTypes = Array.isArray(summary.relation_types) ? summary.relation_types : [];
  if (summaryEl) {
    summaryEl.textContent = describeTotals(summary.totals ?? {});
  }
  const typesAction = document.getElementById("types-empty-action");
  if (typesAction) {
    // textContent, like every other string this page writes: the role is
    // the server's own word, but the rule here is the page's and holds
    // for every value it renders.
    typesAction.textContent = describeWhoDeclaresTypes(summary.role);
  }

  fillCatalogue(
    document.getElementById("types"),
    document.getElementById("types-empty"),
    entityTypes.map((type) =>
      catalogueRow(
        type.label_plural || type.label || type.key,
        type.key,
        countLabel(Number(type.entity_count ?? 0), "entity", "entities"),
        Number(type.invalid_count ?? 0),
      ),
    ),
  );
  fillCatalogue(
    document.getElementById("relation-types"),
    document.getElementById("relation-types-empty"),
    relationTypes.map((type) =>
      catalogueRow(
        type.label || type.key,
        type.key,
        countLabel(Number(type.relation_count ?? 0), "relation", "relations"),
        // The same flag the entity catalogue shows, and for the same
        // reason: since 0009 an edge is judged against its relation
        // type's field schema too, so a relation type can hold rows a
        // designer has to go and fix. A hard-coded zero stood here while
        // an edge could not be invalid, and it would now hide half of
        // what the summary's own total counts.
        Number(type.invalid_count ?? 0),
      ),
    ),
  );

  await renderDocuments(gameID, slug, summary.role);

  const content = document.getElementById("game-content");
  if (content) {
    // Revealed only now, with both catalogues already filled, so the
    // page never flashes two empty lists on its way to the real ones.
    content.hidden = false;
  }
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
      await renderGameSummary(game.id, slug);
    } else {
      gameNameEl.textContent = "Game not found";
      if (summaryEl) {
        summaryEl.textContent = "You may not have access to this game, or it no longer exists.";
      }
    }
  }
}
