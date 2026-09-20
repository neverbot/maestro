// A game's own settings: what it is called, the address every URL into
// it carries, and the tokens its agents hold.
//
// **Two tabs, because the two halves answer to two different gates.**
// The name and the address are the owner's. The tokens are any member's
// to list and revoke and an editor's to mint — `api_tokens.go` refuses a
// viewer deliberately, because a token carries the project binding and a
// viewer minting one would be handing themselves a credential wider than
// their own role. One page with one gate would have to pick a side, and
// either side is wrong.
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
  STATE_REFUSED,
  TAB_AGENTS,
  expired,
  fillState,
  gameURL,
  negativeState,
  openGame,
  say,
  setBreadcrumb,
} from "./page.js";
import { goToLogin, setFormBusy } from "../app.js";
import { openDialog } from "../components/mst-dialog.js";

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
    { label: "Game settings" },
  ]);
  doc.title = opened.game.name + " · Game settings · Maestro";

  const answer = await opened.client.summary();
  if (!answer.ok) {
    if (expired(answer)) {
      goToLogin();
      return opened;
    }
    say(errorEl, answer.error.message);
    return opened;
  }

  const role = String(answer.result.role ?? "");
  // The tabs first, so the address's #agents is honoured whether or not
  // the reader may touch the half this page used to be all of.
  wireTabs(doc, globalThis.window);
  void agentsTab(opened, role);

  // Owner, and nothing less — **for this tab**. The server refuses
  // anything else; this is the same rule said where somebody can read
  // it. The Agents tab beside it has its own, wider gate, which is why
  // this page no longer refuses a reader outright.
  if (role !== "owner") {
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


// --- The Agents tab ---------------------------------------------------

// The tab that opens is decided from the address rather than from a
// default, so a person handed a link to #agents — the empty game's panel
// hands out exactly that one — lands on the half they were sent to.
// TAB_AGENTS itself is stated in page.js, with every other address.

// MCP_PATH is where this server speaks MCP, and it is one string because
// three places say it: the command, the address beside it, and the
// sentence about any other client.
export const MCP_PATH = "/mcp";

// **The origin is the browser's, not a value this product stores.** The
// person reading this page reached this instance at some address, and
// that is the address their agent will reach it at too — which is true
// on localhost, over a VPN and behind a proxy alike, and is the one
// thing a configured `PUBLIC_URL` would keep getting wrong for somebody.
export function installCommand(origin, token) {
  return (
    "claude mcp add maestro " + String(origin || "") + MCP_PATH + " \\\n" +
    "  --transport http \\\n" +
    '  --header "Authorization: Bearer ' + String(token || "") + '" \\\n' +
    "  --scope local"
  );
}

// The sentence for a client that is not Claude Code: the same three
// facts, apart, because a command is not a configuration format and
// every host has its own.
export function elsewhereSentence(origin) {
  return (
    "Any other MCP client needs the same three things: the address " +
    String(origin || "") + MCP_PATH +
    ', the header Authorization: Bearer <token>, and HTTP as the transport.'
  );
}

// **What happens next, so nobody goes looking for a manual.** The agent
// fetches Maestro's own instructions itself, with skill.install, on its
// first call; there is nothing for a designer to install, paste or
// teach.
export const TOKEN_NEXT =
  "Then tell your agent to start. Its own first call fetches Maestro's instructions — the " +
  "metamodel, the tools and three worked games — so you do not have to teach it any of that.";

// The one sentence that has to be read before the panel is closed.
export const TOKEN_ONCE =
  "Copy this now. This is the only time this token is shown; if it is lost, create another and " +
  "revoke this one.";

export const NO_TOKENS_HEADING = "No tokens yet";
export const NO_TOKENS_SENTENCE =
  "A token is how an agent reaches this game. Every agent, and every machine an agent runs on, " +
  "gets its own, so one can be revoked without stopping the rest.";

export const NO_MINT_HEADING = "Only an editor or the owner can create a token";
export const NO_MINT_SENTENCE =
  "A token carries whatever access this game grants, so it is not a viewer's to hand out. You " +
  "can still see which tokens exist and revoke one.";

export const COPY_FAILED = "Could not copy. Select the text and copy it yourself.";
export const COPIED = "Copied";
export const REVOKE = "Revoke";
export const REVOKED = "revoked";
export const REVOKE_CONFIRM = "Revoke this token?";

// A row of the token list.
//
// **It is not the shared catalogue row**, and that is deliberate: every
// catalogue in this product is a fixed six-cell grid marked up as a
// table, and this list carries a control. A row with a button in a cell
// is a table row a screen reader reads as data; a list of tokens with an
// action each is a list. What it shares with a catalogue is the
// vocabulary, not the markup.
export function tokenRow(doc, token, onRevoke) {
  const item = doc.createElement("li");
  item.className = "token";

  const label = doc.createElement("span");
  label.className = "token-label";
  label.textContent = token.label || "unnamed token";
  item.append(label);

  const hint = doc.createElement("code");
  hint.className = "token-hint";
  // The fingerprint, which is what a person matches a value they found
  // in a config against. It is not the token and cannot be used as one.
  hint.textContent = (token.token_hint || "") + "…";
  item.append(hint);

  const by = doc.createElement("span");
  by.className = "muted";
  by.textContent = token.minted_by ? "created by " + token.minted_by : "";
  item.append(by);

  // **Four cells, always.** A revoked row that simply left the control
  // out shortened itself by a column and stopped lining up with the rows
  // above it; the word goes where the button would be.
  if (token.revoked_at) {
    const state = doc.createElement("span");
    state.className = "token-state";
    state.textContent = REVOKED;
    item.append(state);
    return item;
  }

  // **Armed in the row, not in a dialog.** A modal is this product's
  // first-reach ban, and revoking is the one destructive thing on this
  // screen: the confirmation belongs where the thing being revoked is,
  // with its own name beside it.
  const revoke = doc.createElement("button");
  revoke.type = "button";
  // **Ghost, not ink.** The Ink Button is this identity's *primary*
  // action, one per screen; a row action wearing it made a list of three
  // tokens the loudest thing on the page and shouted the one word nobody
  // should press by accident. It goes danger-coloured when it is armed,
  // which is the moment it is worth looking at.
  revoke.className = "ghost";
  revoke.textContent = REVOKE;
  // `data-armed` is this product's word for a destructive control that
  // has asked once, and the colour comes with it: the sign-out in the
  // header and the armed buttons inside every shadow root already use
  // exactly this attribute.
  revoke.addEventListener("click", () => {
    if (revoke.getAttribute("data-armed") === "true") {
      void onRevoke(token.id);
      return;
    }
    revoke.setAttribute("data-armed", "true");
    revoke.textContent = REVOKE_CONFIRM;
  });
  item.append(revoke);
  return item;
}

// copyInto puts text on the clipboard and answers in the button itself,
// because a copy that silently failed is a person pasting the last thing
// they copied into their agent's configuration.
export async function copyInto(button, text) {
  const idle = button.dataset.idle || button.textContent;
  button.dataset.idle = idle;
  try {
    await globalThis.navigator.clipboard.writeText(text);
    button.textContent = COPIED;
  } catch {
    button.textContent = COPY_FAILED;
  }
}

// showTab moves the selection. The panels are `hidden` rather than
// styled away, so what is not shown is not in the tab order either.
export function showTab(doc, which) {
  const tabs = [
    { button: doc.getElementById("tab-game"), panel: doc.getElementById("panel-game"), name: "game" },
    { button: doc.getElementById("tab-agents"), panel: doc.getElementById("panel-agents"), name: "agents" },
  ];
  for (const tab of tabs) {
    const on = tab.name === which;
    if (tab.button) tab.button.setAttribute("aria-selected", on ? "true" : "false");
    if (tab.panel) tab.panel.hidden = !on;
  }
}

export function wireTabs(doc, win) {
  const tabs = [doc.getElementById("tab-game"), doc.getElementById("tab-agents")];
  for (const tab of tabs) {
    if (!tab) continue;
    tab.addEventListener("click", () => {
      // The panel a tab opens is the attribute the shell wrote beside
      // it, not the tab's own id spelled a second time here.
      const which = tab.getAttribute("data-panel") === "panel-agents" ? "agents" : "game";
      showTab(doc, which);
      // The address follows the selection, so this page can be linked to
      // and reloaded on the half the reader was looking at.
      if (win && win.history && typeof win.history.replaceState === "function") {
        win.history.replaceState(null, "", which === "agents" ? TAB_AGENTS : "#game");
      }
    });
  }
  showTab(doc, win && win.location && win.location.hash === TAB_AGENTS ? "agents" : "game");
}

// agentsTab fills the second half: who may mint, what exists, and the
// one paste.
export async function agentsTab(opened, role) {
  const doc = opened.document;
  const listEl = doc.getElementById("tokens");
  const emptyEl = doc.getElementById("tokens-empty");
  const errorEl = doc.getElementById("tokens-error");
  const form = doc.getElementById("create-token");
  const mayMint = role === "owner" || role === "editor";

  if (form) form.hidden = !mayMint;
  // Both directions, always. The shell ships this hole hidden, so the
  // refusal branch alone looked right and left the state of the screen
  // decided by the markup rather than by the role — which holds exactly
  // until something renders this tab twice.
  const refused = doc.getElementById("agents-refused");
  if (refused) {
    refused.replaceChildren(
      ...(mayMint
        ? []
        : [negativeState(doc, { kind: STATE_REFUSED, heading: NO_MINT_HEADING, sentence: NO_MINT_SENTENCE })]),
    );
    refused.hidden = mayMint;
  }

  async function refresh() {
    const answer = await opened.client.listTokens();
    if (!answer.ok) {
      if (expired(answer)) {
        goToLogin();
        return;
      }
      say(errorEl, answer.error.message);
      if (errorEl) errorEl.hidden = false;
      return;
    }
    const tokens = Array.isArray(answer.result.tokens) ? answer.result.tokens : [];
    if (listEl) {
      listEl.replaceChildren(...tokens.map((token) => tokenRow(doc, token, revoke)));
      listEl.hidden = tokens.length === 0;
    }
    // Through fillState, like every other empty-state hole in the
    // product: the shell declares the hole and one call fills it, which
    // is what static_vocabulary_test.go's own guard reads. Whether it is
    // *shown* is this listing's business and stays here.
    fillState(doc, "tokens-empty", { heading: NO_TOKENS_HEADING, sentence: NO_TOKENS_SENTENCE });
    if (emptyEl) emptyEl.hidden = tokens.length > 0;
  }

  async function revoke(id) {
    const answer = await opened.client.revokeToken(id);
    if (!answer.ok) {
      say(errorEl, answer.error.message);
      if (errorEl) errorEl.hidden = false;
      return;
    }
    await refresh();
  }

  if (form) {
    const labelEl = doc.getElementById("token-label");
    const formError = doc.getElementById("create-token-error");
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      say(formError, "");
      setFormBusy(form, true, "Creating…");
      const answer = await opened.client.createToken(labelEl ? labelEl.value.trim() : "");
      setFormBusy(form, false);
      if (!answer.ok) {
        say(formError, answer.error.message || "Could not create the token.");
        return;
      }
      if (labelEl) labelEl.value = "";
      showIssuedToken(opened, answer.result.token || "");
      await refresh();
    });
  }

  await refresh();
}

// showIssuedToken is the one place a clear token is ever rendered, and
// it renders it **in a dialog the reader closes**.
//
// It was a panel on the page, and a panel is the wrong shape for this
// twice over: a credential nobody can dismiss stays on screen while its
// owner walks away from the desk, and it pushed the list of tokens half
// a window down, so the screen said two things at once. Closing the
// dialog empties it, so the token is not left in a hidden node either.
export function showIssuedToken(opened, token) {
  const doc = opened.document;
  const origin = opened.origin || (globalThis.window && globalThis.window.location
    ? globalThis.window.location.origin
    : "");
  const command = installCommand(origin, token);

  const note = doc.createElement("p");
  note.className = "note";
  note.textContent = TOKEN_ONCE;

  const content = [
    note,
    copyable(doc, "The token", token, "token-value"),
    copyable(doc, "In Claude Code", command, "token-snippet"),
    line(doc, elsewhereSentence(origin)),
    line(doc, TOKEN_NEXT),
  ];
  return openDialog(doc, { title: TOKEN_ISSUED_TITLE, content, dismissLabel: DONE_LABEL });
}

export const TOKEN_ISSUED_TITLE = "Token created";
export const DONE_LABEL = "Done";
export const COPY_LABEL = "Copy";

// A labelled value and the button that copies it. Two of them, and they
// are built rather than written into the shell because the dialog they
// live in does not exist until a token does.
export function copyable(doc, label, value, id) {
  const wrap = doc.createElement("div");

  const name = doc.createElement("label");
  name.setAttribute("for", id);
  name.textContent = label;
  wrap.append(name);

  const row = doc.createElement("div");
  row.className = "copyable";

  const code = doc.createElement("code");
  code.setAttribute("id", id);
  code.textContent = value;
  row.append(code);

  const copy = doc.createElement("button");
  copy.type = "button";
  copy.className = "ghost";
  copy.textContent = COPY_LABEL;
  copy.addEventListener("click", () => void copyInto(copy, value));
  row.append(copy);

  wrap.append(row);
  return wrap;
}

function line(doc, text) {
  const p = doc.createElement("p");
  p.className = "muted";
  p.textContent = text;
  return p;
}

if (globalThis.document && globalThis.document.getElementById("game-settings")) {
  openGame().then((opened) => {
    if (opened) void settingsPage(opened);
  });
}
