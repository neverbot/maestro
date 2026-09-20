// Administration: who is waiting to join this instance, and who runs it.
//
// The only interface to four endpoints that have never had one — list,
// create and revoke an account invitation, and name or unname an
// administrator. Before this screen, inviting the second person to a
// freshly installed Maestro meant calling the API by hand, which is a
// thing a game designer does not do.
//
// **Admin-only, and the server is what enforces that.** This page is
// served to anyone who asks for it and every call it makes is refused
// with 403 for a caller who is not an admin; what the check below does
// is say so in a sentence instead of drawing three dead forms. The menu
// that leads here is not drawn for a non-admin either, so nobody reaches
// this by accident.

import {
  fetchAPI,
  fetchMe,
  goToLogin,
  markRefused,
  postJSON,
  renderHeader,
  sendJSON,
  setFormBusy,
} from "../app.js";
import {
  STATE_REFUSED,
  countLabel,
  emptyOrRows,
  fillState,
  negativeState,
  say,
} from "./page.js";
import { headerRow, row } from "../rows.js";
import { openDialog } from "../components/mst-dialog.js";

// The empty state, in the page rather than in the shell.
export const NO_INVITES_HEADING = "Nobody is waiting";
export const NO_INVITES_SENTENCE =
  "No invitation is outstanding. One appears here from the moment you create it until the " +
  "person uses it or you revoke it.";

export const REVOKE = "Revoke";
export const REVOKE_ARMED = "Revoke — click again";

// An invitation is a row with a state, so it is the shared row: the
// address it was made for, when it expires, and the one thing that can
// be done to it.
export function inviteRow(doc, invite, onRevoke) {
  // **No id on screen.** The key track carries a thing a person reads
  // and types; an invitation has no such name, and a uuid in the column
  // where every other catalogue puts a slug is hex where a word goes.
  // The id is still what the revoke button sends.
  //
  // **A revoked invitation is still listed**, because the server's
  // "outstanding" means not yet redeemed and not yet pruned, revoked or
  // not — measured against a real instance, where revoking a row left it
  // on screen looking exactly like a live one. So the row says which it
  // is, and a dead one has nothing left to do to it.
  const dead = invite.revoked === true;
  const item = row(doc, {
    label: String(invite.email || "anyone with the link"),
    cells: [{ text: dead ? "revoked" : expiry(invite.expires_at), absent: dead }],
  });
  // **An address is not the game's voice.** The row's label is serif 600
  // because a label is usually a quest or a place; an email is a machine
  // identifier a person copies, which this system sets in the tool's
  // sans. The Two Voices Rule reads the other way round here.
  const label = item.querySelector(".catalogue-label");
  if (label) label.classList.add("plain");
  if (!dead) {
    // **Two clicks, like signing out.** Revoking is irreversible, there
    // is no undo on the server, and it was a 47x20px underlined word one
    // click away from cancelling somebody's way in. The second click is
    // asked for in the label rather than in a modal.
    const revoke = doc.createElement("button");
    revoke.type = "button";
    revoke.className = "link-button";
    revoke.textContent = REVOKE;
    let armed = null;
    revoke.addEventListener("click", () => {
      if (armed === null) {
        revoke.textContent = REVOKE_ARMED;
        revoke.dataset.armed = "true";
        armed = setTimeout(() => {
          armed = null;
          revoke.textContent = REVOKE;
          revoke.removeAttribute("data-armed");
        }, 4000);
        return;
      }
      clearTimeout(armed);
      armed = null;
      onRevoke(invite, revoke);
    });
    // Into the count track, which is where a row's own action goes: the
    // row has six children and this replaces the last of them rather
    // than adding a seventh, so the subgrid still lines up with every
    // other catalogue in the product.
    const tally = item.querySelector(".catalogue-count");
    if (tally) tally.replaceChildren(revoke);
  }
  return item;
}

// When it stops working, in the words a person would use. An invitation
// that has expired is still listed by the server until it is pruned, and
// "expired" is a different fact from "expires in three days".
export function expiry(value) {
  const when = new Date(String(value ?? ""));
  if (Number.isNaN(when.getTime())) return "no expiry recorded";
  const days = Math.round((when.getTime() - Date.now()) / 86400000);
  if (days < 0) return "expired";
  if (days === 0) return "expires today";
  return "expires in " + countLabel(days, "day", "days");
}

export async function loadInvites(doc, onRevoke) {
  const noteEl = doc.getElementById("admin-note");
  fillState(doc, "invites-empty", {
    heading: NO_INVITES_HEADING,
    sentence: NO_INVITES_SENTENCE,
  });
  const listEl = doc.getElementById("invites");
  const emptyEl = doc.getElementById("invites-empty");
  const errorEl = doc.getElementById("invites-error");
  const answer = await fetchAPI("/api/invites");
  if (!answer.ok) {
    if (answer.expired) {
      goToLogin();
      return null;
    }
    say(errorEl, answer.message);
    return null;
  }
  say(errorEl, "");
  // The key the handler writes, which is `invites`. Read from the wrong
  // one this page would render "Nobody is waiting" over a list of
  // people who are, which is exactly how the images screen spent its
  // whole life.
  const invites = Array.isArray(answer.body.invites) ? answer.body.invites : [];
  // The header names the two columns under it. A column with no header
  // is a value a reader has to guess at, and "expires in 14 days" beside
  // "revoked" is two different kinds of fact in one place.
  listEl.replaceChildren(
    headerRow(doc, { label: "Invited", cells: [{ text: "State" }], count: "" }),
    ...invites.map((invite) => inviteRow(doc, invite, onRevoke)),
  );
  emptyOrRows(listEl, emptyEl, invites.length);
  // **Every time the list is read, not only the first.** The count was
  // written once on load, so creating an invitation left "0 invitations
  // outstanding" over a row that had just appeared.
  //
  // It counts the ones somebody could still use: a revoked row is listed
  // so an admin can see what they just did, and counting it as
  // outstanding would make the sentence above the list disagree with the
  // word inside it.
  const live = invites.filter((invite) => invite.revoked !== true).length;
  // Both numbers when they differ, because the list shows revoked rows:
  // "1 invitation outstanding" over two rows is a count that argues with
  // what is under it.
  say(noteEl, live === invites.length
    ? countLabel(live, "invitation outstanding", "invitations outstanding")
    : live + " of " + invites.length + " still usable");
  return invites;
}

// One link and the button that copies it. A copy that fails leaves the
// link on screen, selectable, which is what it was there for anyway.
//
// **This is the other secret shown once, and it deliberately does not
// use components/mst-dialog.js.** The token on the Agents tab moved into
// a dialog so a credential stops sitting on a screen nobody is watching;
// an invitation link is the same shape and the opposite case. Creating a
// second invitation used to destroy the first link on screen — the only
// copy of a still-valid secret, gone, with nothing said — and the fix
// was to keep every link of this sitting and mark the earlier ones. A
// dialog is dismissed, and dismissing it is exactly that defect again,
// performed by the reader instead of by the page. So the links stay,
// and this comment is here so the next survey does not "finish the job".
export function inviteLink(doc, href) {
  const line = doc.createElement("div");
  line.className = "secret-line";

  const code = doc.createElement("code");
  code.className = "mono";
  code.textContent = href;
  line.append(code);

  const copy = doc.createElement("button");
  copy.type = "button";
  copy.className = "ghost";
  copy.textContent = "Copy the link";
  copy.addEventListener("click", async () => {
    try {
      await globalThis.navigator.clipboard.writeText(href);
      copy.textContent = "Copied";
    } catch {
      copy.textContent = "Select it and copy";
    }
  });
  line.append(copy);
  return line;
}

export function wireInviteForm(doc, reload) {
  const form = doc.getElementById("new-invite");
  if (!form) return null;
  const errorEl = doc.getElementById("new-invite-error");
  const madeEl = doc.getElementById("new-invite-made");
  const linksEl = doc.getElementById("new-invite-links");
  const emailEl = doc.getElementById("new-invite-email");

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (errorEl) errorEl.textContent = "";
    setFormBusy(form, true, "Creating…");
    const answer = await postJSON("/api/invites", { email: emailEl ? emailEl.value : "" });
    setFormBusy(form, false);
    if (!answer.ok) {
      markRefused(form, emailEl, errorEl, answer.message);
      return;
    }
    // **Shown once, and never overwritten.** The server answers with the
    // clear token and its redemption path and can never answer with
    // either again. This wrote into a single element, so creating a
    // second invitation destroyed the first link on screen: the only
    // copy of a still-valid secret, gone, with nothing said. Each one
    // now stays, and the ones before it are marked as no longer
    // reissuable rather than silently replaced.
    const path = String(answer.body.redeem_path || "");
    if (linksEl && path !== "") {
      for (const older of linksEl.querySelectorAll(".secret-line")) older.classList.add("spent");
      linksEl.append(inviteLink(doc, globalThis.location.origin + path));
    }
    if (madeEl) madeEl.hidden = false;
    form.reset();
    await reload();
  });
  return form;
}

// --- The accounts on this instance ------------------------------------

export const ADMINISTRATOR = "administrator";
export const YOU = "you";
export const EDIT = "Edit";
export const NO_USERS_HEADING = "Nobody but you";
export const NO_USERS_SENTENCE =
  "This instance has one account, yours. An invitation above is how a second one is made.";
export const EDIT_TITLE = "Edit this account";
export const SAVE = "Save";
export const CANCEL = "Cancel";
export const LAST_ADMIN =
  "This instance must keep at least one administrator. Make somebody else one first.";
export const CANNOT_DEMOTE_YOURSELF =
  "You are signed in as an administrator. Taking it away here would lock you out of this page, so " +
  "another administrator does it for you.";

// personRow is one account: who they are, how they sign in, what they
// may do, and the way in to changing it.
//
// **The standing is a word and not a tick.** A checkbox in a row reads
// as something a stray click changes; this list is read far more often
// than it is written, so the row states the fact and the editing happens
// where a person went to edit.
export function personRow(doc, user, onEdit) {
  const item = doc.createElement("li");
  item.className = "person";

  const name = doc.createElement("span");
  name.className = "person-name";
  name.textContent = user.display_name || "";
  item.append(name);

  const email = doc.createElement("code");
  email.className = "person-email";
  email.textContent = user.email || "";
  item.append(email);

  const standing = doc.createElement("span");
  standing.className = "person-standing";
  // Two facts, and both are worth a word: who administers the instance,
  // and which row is the reader's own — which is the one row where a
  // change can lock them out of the page they are standing on.
  standing.textContent = [user.is_admin ? ADMINISTRATOR : "", user.you ? YOU : ""]
    .filter((word) => word !== "")
    .join(" · ");
  item.append(standing);

  const edit = doc.createElement("button");
  edit.type = "button";
  edit.className = "ghost";
  edit.textContent = EDIT;
  edit.addEventListener("click", () => onEdit(user));
  item.append(edit);
  return item;
}

// editAccount opens the one form that changes somebody else's account.
//
// **A dialog, and this is the third case the component admits.** It is
// not a secret shown once and not a destructive question; it is a form
// that belongs to *one row of a list* rather than to the screen. An
// inline editor would push twenty rows down the page to change one, and
// a screen with a form per row is a screen of forms. components/
// mst-dialog.js's own header records the three.
export function editAccount(doc, user, onSave) {
  const form = doc.createElement("form");
  form.setAttribute("id", "edit-account");

  const nameLabel = doc.createElement("label");
  nameLabel.setAttribute("for", "edit-name");
  nameLabel.textContent = "Name";
  const name = doc.createElement("input");
  // `setAttribute` and not `.id = `: the property reflects to the
  // attribute in a browser and not in this project's DOM stub, and a
  // field the harness cannot find is a field nothing asserts about.
  name.setAttribute("id", "edit-name");
  name.setAttribute("type", "text");
  name.value = user.display_name || "";

  const emailLabel = doc.createElement("label");
  emailLabel.setAttribute("for", "edit-email");
  emailLabel.textContent = "Email";
  const email = doc.createElement("input");
  email.setAttribute("id", "edit-email");
  email.setAttribute("type", "email");
  email.value = user.email || "";

  const standing = doc.createElement("label");
  standing.className = "choice";
  const admin = doc.createElement("input");
  admin.setAttribute("id", "edit-admin");
  admin.setAttribute("type", "checkbox");
  admin.checked = user.is_admin === true;
  // **Not your own standing, on your own screen.** Taking the flag off
  // yourself here closes the page you are on, and the server's own
  // last-administrator rule would not catch it while another admin
  // exists. Somebody else does it for you.
  if (user.you === true) admin.disabled = true;
  standing.append(admin, doc.createTextNode(" Administers this instance"));

  const note = doc.createElement("p");
  note.className = "muted";
  if (user.you === true) note.textContent = CANNOT_DEMOTE_YOURSELF;

  const error = doc.createElement("p");
  error.className = "error";
  error.setAttribute("role", "alert");

  const save = doc.createElement("button");
  save.type = "submit";
  save.textContent = SAVE;
  // It lives in the dialog's footer, beside Cancel, and stays this
  // form's submit: they share a shadow root, so the association holds
  // and Enter in a field still saves.
  save.setAttribute("form", "edit-account");

  form.append(nameLabel, name, emailLabel, email, standing, note, error);
  form.addEventListener("submit", async (event) => {
    if (event && typeof event.preventDefault === "function") event.preventDefault();
    error.textContent = "";
    setFormBusy(form, true, "Saving…");
    const answer = await onSave({
      display_name: name.value,
      email: email.value,
      is_admin: admin.checked,
    });
    setFormBusy(form, false);
    if (answer && answer.ok === false) {
      error.textContent = answer.status === 409 && answer.code === "last_admin"
        ? LAST_ADMIN
        : answer.message;
      return;
    }
    const dialog = doc._mstDialog;
    if (dialog && typeof dialog.close === "function") dialog.close();
  });

  return openDialog(doc, { title: EDIT_TITLE, content: [form], actions: [save], dismissLabel: CANCEL });
}

export async function loadUsers(doc, cursor) {
  const listEl = doc.getElementById("users");
  const emptyEl = doc.getElementById("users-empty");
  const errorEl = doc.getElementById("users-error");
  const moreEl = doc.getElementById("users-more");

  const answer = await fetchAPI("/api/users" + (cursor ? "?cursor=" + encodeURIComponent(cursor) : ""));
  if (!answer.ok) {
    if (answer.expired) {
      goToLogin();
      return null;
    }
    say(errorEl, answer.message);
    if (errorEl) errorEl.hidden = false;
    return null;
  }
  const users = Array.isArray(answer.body.users) ? answer.body.users : [];
  const save = (user) => async (changes) => {
    const result = await sendJSON("PATCH", "/api/users/" + user.id, changes);
    if (result.ok) await loadUsers(doc);
    return result;
  };
  const rows = users.map((user) => personRow(doc, user, (who) => editAccount(doc, who, save(who))));
  if (listEl) {
    // A cursor means "add to what is there"; no cursor means this is the
    // first page and replaces it.
    if (cursor) listEl.append(...rows);
    else listEl.replaceChildren(...rows);
    listEl.hidden = listEl.children.length === 0;
  }
  fillState(doc, "users-empty", { heading: NO_USERS_HEADING, sentence: NO_USERS_SENTENCE });
  if (emptyEl) emptyEl.hidden = users.length > 0 || Boolean(cursor);
  const next = typeof answer.body.next_cursor === "string" ? answer.body.next_cursor : "";
  if (moreEl) {
    moreEl.hidden = next === "";
    moreEl.onclick = () => void loadUsers(doc, next);
  }
  return users;
}
// **The by-email form is gone, and so is the sentence that apologised
// for it.** This screen asked for an address typed from memory because
// the server could not list accounts; it can now, so the standing is
// changed on the row of the person it belongs to. `PATCH /api/admins`
// itself stays for the case that has no screen at all: an operator with
// nothing but an address, before anybody has opened this page.

if (globalThis.document && globalThis.document.getElementById("new-invite")) {
  const doc = globalThis.document;
  const who = await fetchMe();
  if (!who.ok && who.expired) {
    goToLogin();
  } else {
    const me = who.ok ? who.body : null;
    renderHeader({ me });
    const noteEl = doc.getElementById("admin-note");

    if (me && me.is_admin === true) {
      const reload = () => loadInvites(doc, revoke);
      const revoke = async (invite, button) => {
        button.disabled = true;
        const answer = await sendJSON("DELETE", "/api/invites/" + invite.id);
        if (!answer.ok) {
          button.disabled = false;
          say(doc.getElementById("invites-error"), answer.message);
          return;
        }
        await reload();
      };
      wireInviteForm(doc, reload);
      void loadUsers(doc);
      await reload();
    } else {
      // Not an admin: the forms are removed rather than disabled, and
      // the page says which account it is talking about, because the
      // commonest cause is being signed in as somebody else.
      say(noteEl, "");
      // **The head stays.** This replaced the whole of `main`, which
      // deleted the `h1` and the rule under it: the one screen where a
      // person is most likely to be lost lost its own name. Only what
      // follows the head is replaced, and the refusal carries the one
      // action design.md gives it.
      const main = doc.querySelector("main");
      const head = doc.querySelector(".page-head");
      if (main && head) {
        main.replaceChildren(head);
        main.append(negativeState(doc, {
          kind: STATE_REFUSED,
          heading: "This instance is not yours to administer",
          sentence: me && me.email
            ? "You are signed in as " + me.email + ", which is not an administrator here. An administrator can make you one."
            : "This account does not administer this instance.",
          action: { href: "/account", label: "Your account" },
        }));
      }
    }
  }
}
