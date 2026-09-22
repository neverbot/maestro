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
  expiry,
  inviteLink,
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
export const NOT_YOURS_HEADING = "Only a game manager can change these";
export const NOT_YOURS_SENTENCE =
  "A game's name and address are its managers' to change, because changing the address breaks " +
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
  void peopleTab(opened, role);
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


// --- The People tab ---------------------------------------------------
//
// **A game could be created from the web and never shared from it.**
// Every membership route has existed since Task 8 and nothing in the
// interface called one: the instance's invitations make an account and
// grant no game, so a game had exactly one person in it, whoever made
// it. This tab is the other half.

// The three roles, in the words a person reads. `owner` is the key on
// the wire — the API, the database's CHECK constraint and the skill
// bundle all say it — and "manager" is what it is called on a screen.
// The key is never printed.
export const ROLE_WORDS = { owner: "manager", editor: "editor", viewer: "viewer" };
export const ROLE_MANAGER = "owner";

export function roleWord(role) {
  return ROLE_WORDS[String(role || "")] ?? String(role || "");
}

export const YOU = "you";
export const EDIT = "Edit";
export const REMOVE = "Remove from this game";
export const REMOVE_CONFIRM = "Remove them?";
export const REVOKE_INVITE = "Revoke";
export const REVOKE_INVITE_CONFIRM = "Revoke this invitation?";
export const MEMBER_TITLE = "What they may do";
export const SAVE = "Save";
export const CANCEL = "Cancel";
export const NO_MEMBERS_HEADING = "Only you";
export const NO_MEMBERS_SENTENCE =
  "Nobody else can open this game. An invitation above puts somebody in it, at the role you " +
  "choose.";
export const NO_INVITES_HEADING = "Nobody is waiting";
export const NO_INVITES_SENTENCE =
  "No invitation into this game is outstanding. One appears here from the moment you create it " +
  "until the person uses it or you revoke it.";
export const NOT_A_MANAGER_HEADING = "Only a game manager can change who is here";
export const NOT_A_MANAGER_SENTENCE =
  "You can see who is in this game and what they may do. Inviting somebody, changing a role or " +
  "removing a member is a manager's.";
export const LAST_MANAGER =
  "This game must keep at least one manager. Make somebody else one first.";
export const CANNOT_CHANGE_YOURSELF =
  "This is your own membership. Another manager changes it, so nobody can take their own way " +
  "into a game away by accident.";

// memberRow is one person in this game: who they are, what they may do,
// and the way in to changing it. The role is a word and not a chooser:
// this list is read far more often than it is written, and a select in
// every row is a row that changes under a stray scroll.
export function memberRow(doc, member, onEdit) {
  const item = doc.createElement("li");
  item.className = "person";

  const name = doc.createElement("span");
  name.className = "person-name";
  name.textContent = member.display_name || "";
  item.append(name);

  const role = doc.createElement("span");
  role.className = "person-email";
  role.textContent = roleWord(member.role);
  item.append(role);

  const mine = doc.createElement("span");
  mine.className = "person-standing";
  mine.textContent = member.you === true ? YOU : "";
  item.append(mine);

  if (!onEdit) {
    const nothing = doc.createElement("span");
    item.append(nothing);
    return item;
  }
  const edit = doc.createElement("button");
  edit.type = "button";
  edit.className = "ghost";
  edit.textContent = EDIT;
  edit.addEventListener("click", () => onEdit(member));
  item.append(edit);
  return item;
}

// editMember is the one form that changes what somebody may do here,
// and the one place they are removed from the game.
//
// **Not your own membership.** A manager demoting themselves walks out
// of the door they are standing in: the server would allow it while
// another manager exists, and the reader would lose the screen they are
// on with no way back. Another manager does it.
export function editMember(doc, member, actions) {
  const form = doc.createElement("form");
  form.setAttribute("id", "edit-member");

  const label = doc.createElement("label");
  label.setAttribute("for", "member-role");
  label.textContent = "Role";
  const select = doc.createElement("select");
  select.setAttribute("id", "member-role");
  for (const [key, word] of Object.entries(ROLE_WORDS)) {
    const option = doc.createElement("option");
    option.setAttribute("value", key);
    option.textContent = word;
    if (key === member.role) option.selected = true;
    select.append(option);
  }
  if (member.you === true) select.disabled = true;

  const note = doc.createElement("p");
  note.className = "muted";
  note.textContent = member.you === true ? CANNOT_CHANGE_YOURSELF : "";

  const error = doc.createElement("p");
  error.className = "error";
  error.setAttribute("role", "alert");

  form.append(label, select, note, error);

  const save = doc.createElement("button");
  save.type = "submit";
  save.textContent = SAVE;
  save.setAttribute("form", "edit-member");
  // **Nothing on your own row is offered.** The select is already
  // disabled, and a Save beside it is a control whose only outcome is
  // the server's refusal — the shape this repository calls a knob with
  // no reader, drawn where a person can press it.
  if (member.you === true) save.disabled = true;

  const remove = doc.createElement("button");
  remove.type = "button";
  remove.className = "ghost";
  remove.textContent = REMOVE;
  if (member.you === true) remove.disabled = true;
  remove.addEventListener("click", async () => {
    // Armed in place, like every other destructive control here: a
    // second dialog on top of a dialog is a question nobody reads.
    if (remove.getAttribute("data-armed") !== "true") {
      remove.setAttribute("data-armed", "true");
      remove.textContent = REMOVE_CONFIRM;
      return;
    }
    const answer = await actions.remove(member);
    if (answer && answer.ok === false) {
      error.textContent = answer.code === "last_owner" ? LAST_MANAGER : answer.message;
      remove.removeAttribute("data-armed");
      remove.textContent = REMOVE;
      return;
    }
    closeDialog(doc);
  });

  form.addEventListener("submit", async (event) => {
    if (event && typeof event.preventDefault === "function") event.preventDefault();
    error.textContent = "";
    setFormBusy(form, true, "Saving…");
    const answer = await actions.setRole(member, select.value);
    setFormBusy(form, false);
    if (answer && answer.ok === false) {
      error.textContent = answer.code === "last_owner" ? LAST_MANAGER : answer.message;
      return;
    }
    closeDialog(doc);
  });

  // **The dialog carries the name.** "What they may do" over a role
  // select says nothing about whose game standing is about to change,
  // and the row it came from is behind a scrim by then.
  const title = member.display_name ? MEMBER_TITLE + ": " + member.display_name : MEMBER_TITLE;
  return openDialog(doc, { title, content: [form], actions: [save, remove], dismissLabel: CANCEL });
}

function closeDialog(doc) {
  const dialog = doc._mstDialog;
  if (dialog && typeof dialog.close === "function") dialog.close();
}

// inviteRow is one outstanding invitation into this game: who it is for,
// what it grants, when it stops working, and the way to take it back.
export function inviteRow(doc, invite, onRevoke) {
  const item = doc.createElement("li");
  item.className = "person";

  const who = doc.createElement("span");
  who.className = "person-name";
  who.textContent = invite.email || "anyone with the link";
  item.append(who);

  const role = doc.createElement("span");
  role.className = "person-email";
  role.textContent = roleWord(invite.role);
  item.append(role);

  const when = doc.createElement("span");
  when.className = "person-standing";
  when.textContent = expiry(invite.expires_at);
  item.append(when);

  if (!onRevoke) {
    item.append(doc.createElement("span"));
    return item;
  }
  const revoke = doc.createElement("button");
  revoke.type = "button";
  revoke.className = "ghost";
  revoke.textContent = REVOKE_INVITE;
  revoke.addEventListener("click", () => {
    if (revoke.getAttribute("data-armed") === "true") {
      void onRevoke(invite.id);
      return;
    }
    revoke.setAttribute("data-armed", "true");
    revoke.textContent = REVOKE_INVITE_CONFIRM;
  });
  item.append(revoke);
  return item;
}

// peopleTab fills the third half of this screen: who is here, who is
// invited, and the one form that adds somebody.
export async function peopleTab(opened, role) {
  const doc = opened.document;
  const manages = role === ROLE_MANAGER;
  const membersEl = doc.getElementById("members");
  const membersEmpty = doc.getElementById("members-empty");
  const errorEl = doc.getElementById("members-error");
  const invitesEl = doc.getElementById("game-invites");
  const invitesEmpty = doc.getElementById("game-invites-empty");
  const form = doc.getElementById("invite-member");

  if (form) form.hidden = !manages;
  const refused = doc.getElementById("people-refused");
  if (refused) {
    refused.replaceChildren(
      ...(manages
        ? []
        : [negativeState(doc, {
            kind: STATE_REFUSED,
            heading: NOT_A_MANAGER_HEADING,
            sentence: NOT_A_MANAGER_SENTENCE,
          })]),
    );
    refused.hidden = manages;
  }

  const actions = {
    async setRole(member, next) {
      const answer = await opened.client.setRole(member.id, next);
      if (answer.ok) await refresh();
      return answer;
    },
    async remove(member) {
      const answer = await opened.client.removeMember(member.id);
      if (answer.ok) await refresh();
      return answer;
    },
  };

  async function refresh() {
    const answer = await opened.client.listMembers();
    if (!answer.ok) {
      if (expired(answer)) {
        goToLogin();
        return;
      }
      say(errorEl, answer.error.message);
      if (errorEl) errorEl.hidden = false;
      return;
    }
    const members = Array.isArray(answer.result.members) ? answer.result.members : [];
    if (membersEl) {
      membersEl.replaceChildren(
        ...members.map((member) =>
          memberRow(doc, member, manages ? (who) => editMember(doc, who, actions) : null),
        ),
      );
      membersEl.hidden = members.length === 0;
    }
    fillState(doc, "members-empty", { heading: NO_MEMBERS_HEADING, sentence: NO_MEMBERS_SENTENCE });
    // "Only you" is the state worth a sentence: a game with one member
    // is a game nobody else can open, and that is the thing this tab
    // exists to fix. An empty list is impossible — a game always has a
    // manager — so it is not a state at all.
    if (membersEmpty) membersEmpty.hidden = members.length > 1;
    await refreshInvites();
  }

  async function refreshInvites() {
    if (!invitesEl) return;
    const answer = await opened.client.listGameInvites();
    if (!answer.ok) {
      // A viewer may read the members and not the invitations; that is
      // the server's rule and not a fault to report on this screen.
      invitesEl.hidden = true;
      if (invitesEmpty) invitesEmpty.hidden = true;
      return;
    }
    // **Revoked ones are gone from here.** The server keeps them — the
    // instance's admin screen lists them struck through, which is the
    // right answer for an audit — but this heading says *outstanding*
    // and the empty state below it promises the row leaves "when the
    // person uses it or you revoke it". Without this filter a revoke
    // answered 204 and changed nothing a person could see, which is
    // how it shipped the first time it was opened in a browser.
    const all = Array.isArray(answer.result.invites) ? answer.result.invites : [];
    const invites = all.filter((invite) => invite.revoked !== true);
    invitesEl.replaceChildren(
      ...invites.map((invite) =>
        inviteRow(doc, invite, manages ? async (id) => {
          const gone = await opened.client.revokeGameInvite(id);
          if (gone.ok) await refreshInvites();
        } : null),
      ),
    );
    invitesEl.hidden = invites.length === 0;
    fillState(doc, "game-invites-empty", { heading: NO_INVITES_HEADING, sentence: NO_INVITES_SENTENCE });
    if (invitesEmpty) invitesEmpty.hidden = invites.length > 0;
  }

  if (form && manages) {
    const emailEl = doc.getElementById("invite-email");
    const roleEl = doc.getElementById("invite-role");
    const formError = doc.getElementById("invite-member-error");
    const madeEl = doc.getElementById("invite-made");
    const linksEl = doc.getElementById("invite-links");
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      say(formError, "");
      setFormBusy(form, true, "Creating…");
      const answer = await opened.client.inviteToGame(
        emailEl ? emailEl.value : "",
        roleEl ? roleEl.value : "editor",
      );
      setFormBusy(form, false);
      if (!answer.ok) {
        say(formError, answer.error.message || "Could not create that invitation.");
        return;
      }
      // **Shown once, and never overwritten.** A second invitation must
      // not destroy the only copy of the first link on screen; the
      // earlier ones are marked instead, which is the decision the
      // instance's invitations already record.
      const path = String(answer.result.redeem_path || "");
      if (linksEl && path !== "") {
        for (const older of linksEl.children) older.classList.add("spent");
        linksEl.append(inviteLink(doc, globalThis.window.location.origin + path));
      }
      if (madeEl) madeEl.hidden = false;
      if (emailEl) emailEl.value = "";
      await refreshInvites();
    });
  }

  await refresh();
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

// **Whose keys these are, said above them.** The same listing answers
// two questions — your own, or every key in this game because you own it
// — and a list that silently means two different things is a list
// somebody revokes the wrong row from. The server decides which of the
// two it answered and says so; this only spells it.
export const SCOPE_OWN = "own";
export const SCOPE_GAME = "game";
export const YOUR_KEYS =
  "These are your own tokens. Everybody in this game has their own, and only the person who " +
  "created one can retire it.";
export const EVERY_KEY =
  "You manage this game, so this is every token in it, whoever created it — and you can retire any " +
  "of them.";

export function whoseTokens(scope) {
  return scope === SCOPE_GAME ? EVERY_KEY : YOUR_KEYS;
}

export const NO_TOKENS_HEADING = "No tokens yet";
export const NO_TOKENS_SENTENCE =
  "A token is how an agent reaches this game. Every agent, and every machine an agent runs on, " +
  "gets its own, so one can be revoked without stopping the rest.";

export const NO_MINT_HEADING = "Only an editor or a game manager can create a token";
export const NO_MINT_SENTENCE =
  "A token carries whatever access this game grants, so it is not a viewer's to hand out. You " +
  "can still see which tokens exist and revoke one.";

export const COPY_FAILED = "Could not copy. Select the text and copy it yourself.";
export const COPIED = "Copied";
export const REVOKE = "Revoke";
export const REVOKED = "revoked";
export const REVOKE_CONFIRM = "Revoke this token?";
// What a row says where its control would be when the key is somebody
// else's. It is not an apology and not an error: it is who holds it.
export const NOT_YOURS = "theirs";

// A row of the token list.
//
// **It is not the shared catalogue row**, and that is deliberate: every
// catalogue in this product is a fixed six-cell grid marked up as a
// table, and this list carries a control. A row with a button in a cell
// is a table row a screen reader reads as data; a list of tokens with an
// action each is a list. What it shares with a catalogue is the
// vocabulary, not the markup.
export function tokenRow(doc, token, onRevoke, mayRevoke) {
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

  // **Four cells, always.** A row with no control — revoked, or
  // somebody else's on a screen that lists the whole game — shortened
  // itself by a column and stopped lining up with the rows above it; the
  // word goes where the button would be.
  if (token.revoked_at || mayRevoke === false) {
    const state = doc.createElement("span");
    state.className = "token-state";
    state.textContent = token.revoked_at ? REVOKED : NOT_YOURS;
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
export const TABS = ["game", "people", "agents"];

export function showTab(doc, which) {
  const tabs = TABS.map((name) => ({
    name,
    button: doc.getElementById("tab-" + name),
    panel: doc.getElementById("panel-" + name),
  }));
  for (const tab of tabs) {
    const on = tab.name === which;
    if (tab.button) tab.button.setAttribute("aria-selected", on ? "true" : "false");
    if (tab.panel) tab.panel.hidden = !on;
  }
}

export function wireTabs(doc, win) {
  const tabs = TABS.map((name) => doc.getElementById("tab-" + name));
  for (const tab of tabs) {
    if (!tab) continue;
    tab.addEventListener("click", () => {
      // The panel a tab opens is the attribute the shell wrote beside
      // it, not the tab's own id spelled a second time here.
      const panel = String(tab.getAttribute("data-panel") || "");
      const which = TABS.includes(panel.replace("panel-", "")) ? panel.replace("panel-", "") : "game";
      showTab(doc, which);
      // The address follows the selection, so this page can be linked to
      // and reloaded on the half the reader was looking at.
      if (win && win.history && typeof win.history.replaceState === "function") {
        win.history.replaceState(null, "", which === "agents" ? TAB_AGENTS : "#game");
      }
    });
  }
  const hash = win && win.location ? String(win.location.hash || "") : "";
  const asked = hash.replace("#", "");
  showTab(doc, TABS.includes(asked) ? asked : "game");
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
    const scope = String(answer.result.scope ?? SCOPE_OWN);
    say(doc.getElementById("tokens-whose"), whoseTokens(scope));
    if (listEl) {
      // **Who may retire which is the server's answer, not a rule this
      // screen re-derives.** A row carries `mine`; the whole-game
      // listing belongs to somebody who may retire any of them.
      listEl.replaceChildren(
        ...tokens.map((token) => tokenRow(doc, token, revoke, scope === SCOPE_GAME || token.mine === true)),
      );
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
