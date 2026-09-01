// Regression harness for the second bug a live-browser review found:
// safeReturnPath (internal/web/static/app.js) used to accept any raw
// query value starting with a single leading slash, rejecting only a
// second literal slash — but a browser's own URL parser treats a
// backslash as a path separator on a special scheme (the same as a
// second forward slash) and strips a bare control character such as a
// newline before it ever looks at the string, so "/\evil.example",
// "/\/evil.example" and "/\n/evil.example" all read as "one leading
// slash, no second slash" to a character-counting regex while still
// resolving to a different host once a browser actually navigates
// there. This script drives the real, unmodified app.js exactly the way
// a browser would — a successful login redirecting through
// safeReturnPath — with each of those three payloads in ?return=, and
// asserts every one lands on this origin, never on evil.example.
//
// Run directly: `node internal/web/jstest/safe_return_path_test.mjs`.
// internal/web/static_appjs_invite_test.go shells out to this file too.

const ORIGIN = "http://localhost:8124";

function fail(message) {
  console.error("FAIL: " + message);
  process.exit(1);
}

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

// A fresh page load per case: real app.js reads window.location once at
// module-evaluation time and again inside each submit handler, so each
// case gets its own globals and its own dynamic import (cache-busted
// with a query string) rather than reusing one module instance across
// cases.
async function runCase(returnValue, wantPathname) {
  let submitHandler = null;
  const loginFormEl = {
    _values: { email: "designer@example.com", password: "correcthorsebatterystaple12" },
    elements: [],
    addEventListener(type, handler) {
      if (type === "submit") submitHandler = handler;
    },
    querySelector() {
      return fakeElement({ textContent: "Sign in", dataset: {} });
    },
  };

  const elements = {
    login: loginFormEl,
    invite: null,
    "login-view": fakeElement(),
    "invite-view": fakeElement({ hidden: true }),
    "invite-copy": fakeElement(),
    "mode-notice": fakeElement({ hidden: true }),
    "register-toggle-wrap": fakeElement({ hidden: true }),
    "register-toggle": fakeElement(),
    "back-to-login": fakeElement(),
    "login-error": fakeElement(),
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

  const search = "?return=" + encodeURIComponent(returnValue);
  const location = {
    hash: "",
    pathname: "/login",
    search,
    origin: ORIGIN,
    href: "",
  };
  globalThis.window = { location };
  globalThis.history = { replaceState() {} };

  globalThis.fetch = async (url) => {
    if (url === "/api/auth/login") {
      return { ok: true, status: 200, json: async () => ({ user_id: "1" }) };
    }
    return { ok: false, status: 404, json: async () => ({ error: "not_found", message: "unexpected fetch: " + url }) };
  };

  // Cache-busted so Node re-evaluates the module fresh for each case
  // instead of reusing the first case's top-level state.
  await import(`../static/app.js?case=${encodeURIComponent(returnValue)}`);

  if (typeof submitHandler !== "function") {
    fail(`[return=${JSON.stringify(returnValue)}] app.js never registered a submit handler on the #login form`);
  }
  await submitHandler({ preventDefault() {} });

  if (location.href !== wantPathname) {
    fail(`[return=${JSON.stringify(returnValue)}] window.location.href = ${JSON.stringify(location.href)}, want ${JSON.stringify(wantPathname)}`);
  }
}

const bypassPayloads = [
  "/\\evil.example",
  "/\\/evil.example",
  "/\n/evil.example",
  "//evil.example",
  "https://evil.example/",
];

for (const payload of bypassPayloads) {
  await runCase(payload, "/");
}

// A legitimate same-origin path must still be honoured — the guard is
// meant to narrow this to same-origin paths, not disable it outright.
await runCase("/g/azeroth", "/g/azeroth");

console.log("OK: safeReturnPath rejected every off-site bypass payload and still honoured a legitimate same-origin path");
process.exit(0);
