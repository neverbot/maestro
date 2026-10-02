// The language this product is read in, decided in the browser.
//
// **Only the catalogue that is needed is fetched.** The module carries
// no strings of its own: English is a catalogue like any other, so a
// Spanish reader never downloads English and an English one never
// downloads Spanish.
//
// **A key missing from a catalogue is a failing test, not a fallback.**
// internal/web/static_i18n_test.go refuses a locale that does not carry
// every key English does, so nothing here has to guess at runtime. An
// unknown key renders as the key itself, which is visible and gets
// fixed; an empty string is a hole nobody sees.
//
// The top-level await is deliberate: every module that imports this one
// waits for the catalogue before it evaluates, so a string constant
// declared at module scope already has its words. It costs one request
// before the first paint, which is the price of never showing a reader
// a language they did not ask for.

export const DEFAULT_LOCALE = "en";

// The locales that ship. The guard in static_i18n_test.go reads this
// list and fails on a catalogue it does not name, and on a name with no
// catalogue.
export const LOCALES = ["en", "es"];

// Where the browser keeps its copy of the account's choice. It is a
// cache and never the truth: the column on the account is, and this
// exists so the catalogue request can go out before /api/me answers.
export const STORED_LOCALE_KEY = "maestro.locale";

// negotiate picks the catalogue to fetch. A stored choice wins, then the
// browser's own languages in order, then English. A language is matched
// on its primary subtag, so `es-419` and `es-ES` both read Spanish.
export function negotiate(stored, languages, available = LOCALES) {
  const admits = (tag) => {
    const want = String(tag || "").trim().toLowerCase();
    if (!want) return "";
    if (available.includes(want)) return want;
    const primary = want.split("-")[0];
    return available.includes(primary) ? primary : "";
  };
  const chosen = admits(stored);
  if (chosen) return chosen;
  for (const tag of Array.isArray(languages) ? languages : []) {
    const match = admits(tag);
    if (match) return match;
  }
  return DEFAULT_LOCALE;
}

function storedLocale() {
  try {
    return globalThis.localStorage?.getItem(STORED_LOCALE_KEY) ?? "";
  } catch {
    // A private window refuses storage. The browser's languages are a
    // perfectly good answer and this is not worth failing a page over.
    return "";
  }
}

function browserLanguages() {
  const nav = globalThis.navigator;
  if (!nav) return [];
  if (Array.isArray(nav.languages) && nav.languages.length > 0) return nav.languages;
  return nav.language ? [nav.language] : [];
}

export const locale = negotiate(storedLocale(), browserLanguages());

async function fetchCatalogue(tag) {
  // **Loaded from disk when this module is on disk.** A browser serves
  // this over http and fetches the catalogue the same way; a Node
  // harness imports the file itself, where a relative fetch has no
  // origin to resolve against and `fetch` refuses a file: URL outright.
  // The condition is where this module came from, which is a fact, and
  // not whether a test is running, which this module has no business
  // knowing.
  if (import.meta.url.startsWith("file:")) {
    const { readFile } = await import("node:fs/promises");
    const { fileURLToPath } = await import("node:url");
    const path = fileURLToPath(new URL(`./i18n/${tag}.json`, import.meta.url));
    return JSON.parse(await readFile(path, "utf8"));
  }
  const answer = await fetch(`/static/i18n/${tag}.json`, { headers: { accept: "application/json" } });
  if (!answer.ok) throw new Error(`catalogue ${tag}: ${answer.status}`);
  return answer.json();
}

let messages = {};
try {
  messages = await fetchCatalogue(locale);
} catch (err) {
  // The catalogue is served by the same binary that just served this
  // module, so this is the shape of failure where nothing else works
  // either. English is tried once rather than leaving the product mute.
  if (locale !== DEFAULT_LOCALE) {
    try {
      messages = await fetchCatalogue(DEFAULT_LOCALE);
    } catch {
      messages = {};
    }
  }
}

// t reads one string. `vars` fills `{name}` placeholders, which is the
// whole of this product's formatting: no plural rules, no dates, no
// library. A count that changes its noun is countLabel's job in rows.js,
// which takes both words from the caller.
export function t(key, vars) {
  const line = Object.prototype.hasOwnProperty.call(messages, key) ? messages[key] : key;
  if (!vars) return line;
  return line.replace(/\{(\w+)\}/g, (whole, name) =>
    Object.prototype.hasOwnProperty.call(vars, name) ? String(vars[name]) : whole,
  );
}

// has answers whether a key is in the catalogue at all, which is how a
// caller translates something the server named — an error code — without
// inventing a sentence for a code it has never seen.
export function has(key) {
  return Object.prototype.hasOwnProperty.call(messages, key);
}

// --- What the server said ---------------------------------------------

// **The wire stays English and the reader does not.** A refusal arrives
// as {"error": code, "message": text}, and one code carries several
// different sentences ("not_found" is a game, a user, a token), so the
// code alone cannot pick the words. The sentence itself is the key: it
// is slugged, and a catalogue entry under that slug translates it. The
// guard in internal/web/static_errors_test.go reads every message this
// server writes and fails on one no catalogue carries, which is what
// keeps this from drifting the first time a message is reworded.
//
// A message with no entry reaches the reader as the server wrote it,
// which is English and true, rather than as a key or as nothing.
export function errorKey(message) {
  const slug = String(message ?? "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, ERROR_SLUG_MAX)
    // Trimmed again: a cut that lands on a separator would otherwise
    // leave a trailing dash, and the key is a key or it is nothing.
    .replace(/-+$/, "");
  return slug === "" ? "" : "error." + slug;
}

// How much of a message the slug keeps. Long enough that no two of this
// server's messages collide, which the same guard checks.
export const ERROR_SLUG_MAX = 72;

// serverSays translates one refusal's message, or hands back the words
// the server chose.
export function serverSays(message) {
  const key = errorKey(message);
  return key !== "" && has(key) ? t(key) : String(message ?? "");
}

// applyTo fills every element the shell marked with `data-i18n`, and the
// attributes marked with `data-i18n-attr` ("placeholder:form.email").
// The shells carry no English: an element waiting for its words is
// empty, so nothing can be read in a language the reader did not ask
// for, not even for a frame.
export function applyTo(root) {
  const scope = root ?? globalThis.document;
  if (!scope || typeof scope.querySelectorAll !== "function") return 0;
  let filled = 0;
  for (const el of scope.querySelectorAll("[data-i18n]")) {
    el.textContent = t(el.getAttribute("data-i18n"));
    filled += 1;
  }
  for (const el of scope.querySelectorAll("[data-i18n-attr]")) {
    for (const pair of String(el.getAttribute("data-i18n-attr")).split(",")) {
      const [attr, key] = pair.split(":").map((part) => part.trim());
      if (attr && key) el.setAttribute(attr, t(key));
    }
    filled += 1;
  }
  return filled;
}

// remember stores the account's choice where the next page load can read
// it before /api/me answers.
export function remember(tag) {
  try {
    if (tag) globalThis.localStorage?.setItem(STORED_LOCALE_KEY, tag);
    else globalThis.localStorage?.removeItem(STORED_LOCALE_KEY);
  } catch {
    // Same as reading it: not worth failing a page over.
  }
}

// **Every page, not every page that remembered to ask.** A module script
// is deferred, so the document is parsed by the time this runs, and the
// catalogue is already here. Putting this in a page's own start-up left
// the screens that do not open a game — the account, the login — with
// their elements empty, which is how this was first found.
if (globalThis.document && typeof globalThis.document.querySelectorAll === "function") {
  applyTo(globalThis.document);
  if (globalThis.document.documentElement) {
    globalThis.document.documentElement.setAttribute("lang", locale);
  }
}
