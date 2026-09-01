// Regression harness for the bug a live-browser review found: the invite
// form's submit handler used to re-read window.location.hash for the
// invite token, but history.replaceState (login.html's own script, run
// on page load to scrub the token out of the visible URL — see app.js's
// own comment on that call) had already cleared it by the time anyone
// could submit the form, so every redemption silently sent an empty
// invite_token and failed with "this instance only admits invited
// users". An API-level test (POST /api/auth/register directly) could
// not have caught this: the backend was never the problem, the page's
// own script was. This script instead loads the real, unmodified
// internal/web/static/app.js as an ES module, drives its actual invite
// submit handler the way a browser would, and asserts the token it
// sends is the one that was in the URL — not a reimplementation of the
// logic that could make, and hide, the same mistake again.
//
// Run directly: `node internal/web/jstest/invite_redemption_test.mjs`
// (from the repository root, or any other directory — the import below
// is relative to this file). Exits 0 and prints "OK" on success; exits
// 1 with a diagnostic on failure. internal/web/static_appjs_invite_test.go
// shells out to this file so `go test ./internal/web/...` exercises it
// too, skipping cleanly if `node` is not on PATH.

const TOKEN = "test-invite-token-abc123";
const EMAIL = "designer@example.com";
const DISPLAY_NAME = "Designer";
const PASSWORD = "correcthorsebatterystaple12";

function fail(message) {
  console.error("FAIL: " + message);
  process.exit(1);
}

// A minimal FormData standing in for the browser's HTMLFormElement-backed
// constructor (which Node's own built-in FormData does not implement):
// it reads straight off the plain "_values" map this script attaches to
// its fake <form>, since what is under test is app.js's own handling of
// the values a form gives it, not the browser's form-serialization
// algorithm.
class FakeFormData {
  constructor(form) {
    this._values = (form && form._values) || {};
  }
  get(name) {
    return Object.prototype.hasOwnProperty.call(this._values, name) ? this._values[name] : null;
  }
}
globalThis.FormData = FakeFormData;

function fakeElement(overrides = {}) {
  return Object.assign(
    {
      hidden: false,
      textContent: "",
      addEventListener() {},
    },
    overrides,
  );
}

let submitHandler = null;
const inviteFormEl = {
  _values: { email: EMAIL, display_name: DISPLAY_NAME, password: PASSWORD },
  elements: [],
  addEventListener(type, handler) {
    if (type === "submit") submitHandler = handler;
  },
  querySelector() {
    return fakeElement({ textContent: "Create account", dataset: {} });
  },
};

const elements = {
  login: null,
  invite: inviteFormEl,
  "login-view": fakeElement(),
  "invite-view": fakeElement({ hidden: true }),
  "invite-copy": fakeElement(),
  "mode-notice": fakeElement({ hidden: true }),
  "register-toggle-wrap": fakeElement({ hidden: true }),
  "register-toggle": fakeElement(),
  "back-to-login": fakeElement(),
  "invite-error": fakeElement(),
  games: null,
  status: null,
  "empty-state": null,
  "create-game": null,
  "create-game-error": null,
  "game-name": null,
  "game-summary": null,
};

globalThis.document = {
  title: "",
  getElementById(id) {
    return Object.prototype.hasOwnProperty.call(elements, id) ? elements[id] : null;
  },
};

// The token starts out in the fragment, exactly as a real invite link
// delivers it (see app.js's own comment on why it is the fragment and
// not the query string). history.replaceState below is expected to
// clear it, mirroring login.html's script on page load.
const location = {
  hash: "#invite=" + TOKEN,
  pathname: "/login",
  search: "",
  origin: "http://localhost",
  href: "",
};
globalThis.window = { location };
globalThis.history = {
  replaceState(_state, _title, url) {
    location.hash = "";
    location.href = url;
  },
};

const calls = [];
globalThis.fetch = async (url, opts) => {
  calls.push({ url, opts });
  if (url === "/api/auth/register") {
    return { ok: true, status: 201, json: async () => ({ user_id: "11111111-1111-1111-1111-111111111111" }) };
  }
  return { ok: false, status: 404, json: async () => ({ error: "not_found", message: "unexpected fetch in test: " + url }) };
};

await import("../static/app.js");

// The fragment must be gone from the visible URL by now — app.js is
// supposed to have called history.replaceState on load, exactly the
// behaviour that broke redemption when the submit handler re-read it
// afterward instead of using the value it had already captured.
if (location.hash !== "") {
  fail("window.location.hash was not cleared on load: " + JSON.stringify(location.hash));
}

if (typeof submitHandler !== "function") {
  fail("app.js never registered a submit handler on the #invite form");
}

await submitHandler({ preventDefault() {} });

const registerCalls = calls.filter((c) => c.url === "/api/auth/register");
if (registerCalls.length !== 1) {
  fail(`expected exactly 1 POST to /api/auth/register, got ${registerCalls.length}`);
}

const body = JSON.parse(registerCalls[0].opts.body);
if (!body.invite_token) {
  fail("submitted invite_token is empty — this is the exact regression a review found");
}
if (body.invite_token !== TOKEN) {
  fail(`submitted invite_token = ${JSON.stringify(body.invite_token)}, want ${JSON.stringify(TOKEN)}`);
}
if (body.email !== EMAIL || body.display_name !== DISPLAY_NAME || body.password !== PASSWORD) {
  fail("submitted body does not match the form's own field values: " + JSON.stringify(body));
}

console.log("OK: invite form submitted invite_token=" + JSON.stringify(body.invite_token) + " (non-empty, matches the URL fragment)");
process.exit(0);
