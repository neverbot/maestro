// Your account: who you are, how this looks, and your password.
//
// The first screen in Maestro that belongs to a person rather than to a
// game, and the only interface to two things the server has always been
// able to do and nothing could reach: PATCH /api/me/password, and — with
// the three fields GET /api/me gained for this page — saying your name
// instead of your user id.
//
// **It is not inside a game**, so it does not go through openGame: there
// is no slug to resolve, no switcher to fill and no read-only notice to
// draw. What it shares with every other screen is the header and the
// breadcrumb, which is why those two come from here directly.

import { fetchMe, markRefused, renderHeader, sendJSON, setFormBusy } from "../app.js";
import { goToLogin } from "../app.js";
import { say, setBreadcrumb } from "./page.js";

// The date a person reads, not the one a machine sorts by. The product
// renders `06/09/2026, 16:30:11` elsewhere and it is ambiguous in half
// the world and precise to a second nobody asked for; a join date is a
// month and a year.
export function since(value) {
  const when = new Date(String(value ?? ""));
  if (Number.isNaN(when.getTime())) return "";
  return when.toLocaleDateString(undefined, { year: "numeric", month: "long", day: "numeric" });
}

// The theme radios read and write through the same three functions the
// head script hung off `window`, so there is exactly one statement of
// what a stored theme is and what applying it means. A module that
// duplicated that logic would be a second implementation of a rule that
// has to agree with itself before the first paint.
export function wireTheme(doc, themeAPI) {
  const group = doc.getElementById("theme");
  if (!group || !themeAPI) return null;
  const chosen = themeAPI.read();
  for (const input of group.querySelectorAll("input[type=radio]")) {
    input.checked = input.value === chosen;
    input.addEventListener("change", () => {
      if (input.checked) themeAPI.write(input.value);
    });
  }
  return group;
}

export function showWho(doc, me) {
  const name = doc.getElementById("who-name");
  const email = doc.getElementById("who-email");
  const created = doc.getElementById("who-since");
  const admin = doc.getElementById("who-admin");

  // Each of the three says what is missing rather than rendering empty:
  // a blank beside "Email" cannot be told from a value that failed to
  // load, which is the rule this product states by name.
  if (name) {
    name.textContent = me.display_name || "no name on this account";
    name.className = me.display_name ? "" : "muted";
  }
  if (email) {
    email.textContent = me.email || "no address on this account";
    email.className = me.email ? "" : "muted";
  }
  if (created) {
    const when = since(me.created_at);
    created.textContent = when || "not recorded";
    created.className = when ? "" : "muted";
  }
  if (admin) admin.hidden = me.is_admin !== true;
}

export function wirePassword(doc) {
  const form = doc.getElementById("password");
  if (!form) return null;
  const errorEl = doc.getElementById("password-error");
  const doneEl = doc.getElementById("password-done");
  const currentEl = doc.getElementById("password-current");
  const newEl = doc.getElementById("password-new");

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (errorEl) errorEl.textContent = "";
    if (doneEl) doneEl.hidden = true;
    setFormBusy(form, true, "Changing…");
    const answer = await sendJSON("PATCH", "/api/me/password", {
      current_password: currentEl ? currentEl.value : "",
      new_password: newEl ? newEl.value : "",
    });
    setFormBusy(form, false);
    if (answer.ok) {
      form.reset();
      if (doneEl) doneEl.hidden = false;
      return;
    }
    if (answer.expired || answer.status === 401) {
      // 401 here is the current password being wrong, not the session
      // being gone: this endpoint answers 401 for a failed guess. The
      // field is marked, the focus moves to it and its text is selected,
      // which is what the sign-in screen already does for the same fact.
      markRefused(form, currentEl, errorEl, answer.message || "That is not your current password.");
      return;
    }
    say(errorEl, answer.message);
  });
  return form;
}

if (globalThis.document && globalThis.document.getElementById("who")) {
  const doc = globalThis.document;
  const who = await fetchMe();
  if (!who.ok && who.expired) {
    goToLogin();
  } else {
    const me = who.ok ? who.body : null;
    renderHeader({ me });
    setBreadcrumb(doc, [{ label: "Your account" }]);
    if (me) showWho(doc, me);
    else say(doc.getElementById("who-error"), who.message);
    wireTheme(doc, globalThis.window ? globalThis.window.maestroTheme : null);
    wirePassword(doc);
  }
}
