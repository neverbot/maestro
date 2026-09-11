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
import { countLabel, emptyOrRows, say, setBreadcrumb } from "./page.js";
import { row } from "../rows.js";

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
  if (!dead) {
    const revoke = doc.createElement("button");
    revoke.type = "button";
    revoke.className = "link-button";
    revoke.textContent = "Revoke";
    revoke.addEventListener("click", () => onRevoke(invite, revoke));
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
  listEl.replaceChildren(...invites.map((invite) => inviteRow(doc, invite, onRevoke)));
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
  say(noteEl, countLabel(live, "invitation outstanding", "invitations outstanding"));
  return invites;
}

export function wireInviteForm(doc, reload) {
  const form = doc.getElementById("new-invite");
  if (!form) return null;
  const errorEl = doc.getElementById("new-invite-error");
  const madeEl = doc.getElementById("new-invite-made");
  const linkEl = doc.getElementById("new-invite-link");
  const copyEl = doc.getElementById("new-invite-copy");
  const emailEl = doc.getElementById("new-invite-email");

  if (copyEl) {
    copyEl.addEventListener("click", async () => {
      const text = linkEl ? linkEl.textContent : "";
      try {
        await globalThis.navigator.clipboard.writeText(text);
        copyEl.textContent = "Copied";
      } catch {
        // A browser that refuses the clipboard leaves the link on
        // screen, selectable, which is what it was there for anyway.
        copyEl.textContent = "Select it and copy";
      }
    });
  }

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
    // **Shown once, and said so.** The server answers with the clear
    // token and its redemption path and can never answer with either
    // again; a screen that quietly listed the invitation without its
    // link would have made an invitation nobody could use.
    const path = String(answer.body.redeem_path || "");
    if (linkEl) linkEl.textContent = path === "" ? "" : globalThis.location.origin + path;
    if (copyEl) copyEl.textContent = "Copy the link";
    if (madeEl) madeEl.hidden = false;
    form.reset();
    await reload();
  });
  return form;
}

export function wireAdminsForm(doc) {
  const form = doc.getElementById("admins");
  if (!form) return null;
  const errorEl = doc.getElementById("admins-error");
  const doneEl = doc.getElementById("admins-done");
  const emailEl = doc.getElementById("admins-email");

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (errorEl) errorEl.textContent = "";
    if (doneEl) doneEl.hidden = true;
    const grant = form.querySelector("input[name=is_admin]:checked");
    const isAdmin = grant ? grant.value === "true" : true;
    const email = emailEl ? emailEl.value : "";
    setFormBusy(form, true, "Applying…");
    const answer = await sendJSON("PATCH", "/api/admins", { email, is_admin: isAdmin });
    setFormBusy(form, false);
    if (!answer.ok) {
      markRefused(form, emailEl, errorEl, answer.message);
      return;
    }
    if (doneEl) {
      // The sentence names the person and what changed, because this
      // screen cannot show a list that would say it instead.
      doneEl.textContent = isAdmin
        ? email + " administers this instance."
        : email + " no longer administers this instance.";
      doneEl.hidden = false;
    }
    form.reset();
  });
  return form;
}

if (globalThis.document && globalThis.document.getElementById("new-invite")) {
  const doc = globalThis.document;
  const who = await fetchMe();
  if (!who.ok && who.expired) {
    goToLogin();
  } else {
    const me = who.ok ? who.body : null;
    renderHeader({ me });
    setBreadcrumb(doc, [{ label: "Administration" }]);
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
      wireAdminsForm(doc);
      await reload();
    } else {
      // Not an admin: the forms are removed rather than disabled, and
      // the page says which account it is talking about, because the
      // commonest cause is being signed in as somebody else.
      say(noteEl, "");
      const main = doc.querySelector("main");
      if (main) {
        main.replaceChildren(...doc.querySelectorAll("nav.crumbs"));
        const state = doc.createElement("div");
        state.className = "state refused";
        const heading = doc.createElement("b");
        heading.textContent = "This instance is not yours to administer";
        const sentence = doc.createElement("span");
        sentence.textContent = me && me.email
          ? "You are signed in as " + me.email + ", which is not an administrator of this instance. An administrator can make you one."
          : "This account does not administer this instance.";
        state.append(heading, sentence);
        main.append(state);
      }
    }
  }
}
