// The reading view: one document of one game, rendered, with the
// entities it is attached to, its history, a comparison between two of
// its versions and a revert.
import {
  fallbackMessage,
  fetchAPI,
  fetchGames,
  goToLogin,
  postJSON,
  setFormBusy,
  fetchMe,
  renderHeader,
} from "./app.js";
import { nextCursorOf } from "./rows.js";
import { locale, t } from "./i18n.js";
// The chrome every other page gets. This reading view was rendering the
// header with no destinations at all, which is one of the three
// different chromes the 2026-09-09 audit found inside a single game.
import {
  DESTINATION_PROSE,
  destinations,
  fillState,
  gameURL,
  setBreadcrumb,
  setReadOnly,
  whoWrites,
} from "./pages/page.js";

// The empty state, in the page rather than in the shell.
export const NOT_ATTACHED_HEADING = t("document.notAttached.heading");
export const ATTACHES_IT = t("document.attachesIt");
// A relative specifier, not "/static/app.js": the browser resolves it
// against this module's own URL and gets the same file either way, and
// Node — which internal/web/jstest drives this page with — can resolve
// only the relative one.

// The two routes whose answers are markup rather than text. Both are
// produced by internal/markdown: Render for a body (goldmark with no
// html.WithUnsafe, so raw HTML in a document is never emitted, and every
// link and image destination whose scheme is not http, https or mailto
// is rewritten to "#") and RenderDiff for a comparison (every line
// escaped, then wrapped in a classed <div>). Neither can carry a tag a
// designer typed, which is why this page may insert them and why nothing
// else on it may.
function setRenderedHTML(el, html) {
  el.innerHTML = typeof html === "string" ? html : "";
}

// showFailure is the page's one failure state: the server's own words,
// with everything else hidden. A reading view that failed to load must
// never look like a document that happens to be empty — which is
// precisely what leaving the (empty) article and the (empty) history
// visible would look like.
function showFailure(message) {
  const errorEl = document.getElementById("doc-error");
  const metaEl = document.getElementById("doc-meta");
  const bodyEl = document.getElementById("doc-body");
  const contentEl = document.getElementById("doc-content");
  if (metaEl) metaEl.textContent = "";
  if (bodyEl) bodyEl.hidden = true;
  if (contentEl) contentEl.hidden = true;
  if (errorEl) {
    errorEl.textContent = message;
    errorEl.hidden = false;
  }
}

// describeAuthor turns a version's author into a sentence a designer can
// read.
function describeAuthor(version, membersByID) {
  if (version.author_label) {
    return version.author_label;
  }
  if (version.author_kind === "token") {
    return t("author.agent");
  }
  if (version.author_kind === "user") {
    if (!version.author_id) {
      return t("author.former");
    }
    // Present-but-nameless is a different fact from absent, and saying
    // "a former member" about someone still on the members list is
    // simply false. GET /members can answer with an empty display_name,
    // so the two cases are told apart by whether the id is *in* the map
    // rather than by whether the name is truthy.
    if (!membersByID.has(version.author_id)) {
      return t("author.former");
    }
    return membersByID.get(version.author_id) || t("author.noName");
  }
  return t("author.unknown");
}

// describeTime formats a timestamp in the reader's own locale, and falls
// back to the server's ISO string rather than to "Invalid Date" if it is
// not parseable.
function describeTime(raw) {
  const when = new Date(raw);
  return Number.isNaN(when.getTime()) ? String(raw ?? "") : when.toLocaleString(locale);
}

// entityRow is one line of the "Attached to" list: the entity's name,
// the type key and key an agent addresses it by, and the free-text role
// the link carries when it has one.
function entityRow(link) {
  const item = document.createElement("li");

  const name = document.createElement("span");
  name.className = "catalogue-label";
  name.textContent = link.name || link.entity_key || "";
  item.append(name);

  const handle = document.createElement("code");
  handle.className = "catalogue-key";
  handle.textContent = `${link.entity_type_key ?? ""}/${link.entity_key ?? ""}`;
  item.append(handle);

  const role = document.createElement("span");
  role.className = "catalogue-count";
  role.textContent = link.role ?? "";
  item.append(role);

  return item;
}

const titleEl = document.getElementById("doc-title");
// --- Writing the document ---------------------------------------------

export const EDIT_DOC_LABEL = t("document.edit");
export const TRUNCATED_REFUSAL = t("document.tooLong");
export const EMPTY_MESSAGE = t("document.sayWhatChanged");
export const EDIT_CONFLICT = t("document.conflict");

// wireDocEditor puts the Edit control where the read-only notice would
// be, and swaps the rendered document for its source when it is pressed.
function wireDocEditor(game, docPath, state) {
  const actions = document.getElementById("page-actions");
  const form = document.getElementById("doc-edit");
  const body = document.getElementById("doc-edit-body");
  const message = document.getElementById("doc-edit-message");
  const errorEl = document.getElementById("doc-edit-error");
  const cancel = document.getElementById("doc-edit-cancel");
  const rendered = document.getElementById("doc-body");
  if (!actions || !form || !body || !message) return null;

  const open = document.createElement("button");
  open.type = "button";
  open.className = "ghost";
  open.textContent = EDIT_DOC_LABEL;
  actions.replaceChildren(open);

  const close = () => {
    form.hidden = true;
    open.hidden = false;
    if (rendered) rendered.hidden = false;
    if (errorEl) errorEl.textContent = "";
  };
  if (cancel) cancel.addEventListener("click", close);

  open.addEventListener("click", async () => {
    open.disabled = true;
    const source = await fetchAPI(
      `/api/games/${game}/docs/one?path=${encodeURIComponent(docPath)}`,
    );
    open.disabled = false;
    if (!source.ok) {
      if (source.expired) {
        goToLogin();
        return;
      }
      if (errorEl) errorEl.textContent = source.message || fallbackMessage;
      form.hidden = false;
      return;
    }
    const read = source.body ?? {};
    if (read.truncated === true) {
      // Refused rather than offered: saving a cut body would cut the
      // document, and the reader would have no way to know it had.
      if (errorEl) errorEl.textContent = TRUNCATED_REFUSAL;
      form.hidden = false;
      body.hidden = true;
      return;
    }
    body.hidden = false;
    body.value = String(read.body ?? "");
    message.value = "";
    state.version = Number(read.version ?? state.version);
    form.hidden = false;
    open.hidden = true;
    if (rendered) rendered.hidden = true;
    if (typeof body.focus === "function") body.focus();
  });

  form.addEventListener("submit", async (event) => {
    if (event && typeof event.preventDefault === "function") event.preventDefault();
    const said = String(message.value ?? "").trim();
    if (said === "") {
      if (errorEl) errorEl.textContent = EMPTY_MESSAGE;
      return;
    }
    if (errorEl) errorEl.textContent = "";
    setFormBusy(form, true, t("action.saving"));
    const result = await postJSON(`/api/games/${game}/docs`, {
      path: docPath,
      content: String(body.value ?? ""),
      message: said,
      // The version this page was drawn from. A save somebody else
      // landed in the meantime is a conflict, not a silent overwrite.
      expected_version: Number(state.version),
      // `links` is deliberately absent. See this section's own comment:
      // an empty array would detach everything.
    });
    setFormBusy(form, false);
    if (result.ok) {
      // **Re-read rather than render.** This page's one HTML sink may
      // only ever be fed a rendered view (internal/web/static_docjs_test.go),
      // and the markdown just saved is not one. Reloading is also what
      // the revert does, and for the same reason: a write moves the
      // version, the history, both compare pickers and the body.
      window.location.reload();
      return;
    }
    if (result.expired) {
      goToLogin();
      return;
    }
    if (errorEl) {
      // **Two sentences, on two lines, and neither is edited.** A
      // conflict is the one refusal this page can explain better than
      // the server can — what happened to *your* text is not something
      // the server knows — and the server's own sentence says which
      // version the document is on now, which this page cannot write for
      // itself. Concatenated they ran together as one lowercase
      // half-sentence; stacked, each is whole and the server's is
      // character for character its own.
      if (result.status === 409) {
        const mine = document.createElement("span");
        mine.textContent = EDIT_CONFLICT;
        const theirs = document.createElement("span");
        theirs.className = "muted";
        theirs.textContent = result.message || "";
        errorEl.replaceChildren(mine, document.createElement("br"), theirs);
      } else {
        errorEl.textContent = result.message || fallbackMessage;
      }
    }
  });

  return form;
}

if (titleEl) {
  // /g/{slug}/doc?path=… — the slug in the path, the document's path in
  // the query string, mirroring the API's own shape (a document path
  // never occupies a URL segment; see internal/web/api_docs.go).
  const slugMatch = window.location.pathname.match(/^\/g\/([^/]+)\/doc$/);
  const slug = slugMatch ? decodeURIComponent(slugMatch[1]) : null;
  const docPath = new URLSearchParams(window.location.search).get("path") ?? "";
  // The game's row, from the same list every other page resolves a slug
  // against: /g/{slug} serves a static shell and resolves nothing
  // server-side, and this page adds none. It is fetched for the game's
  // name and to confirm the slug reaches a game at all — not, since the
  // routes started taking slugs, to translate one address into another.
  const [who, games] = await Promise.all([fetchMe(), fetchGames()]);
  if (!games.ok && games.expired) {
    goToLogin();
  } else {
    const list = games.ok ? games.games : [];
    const game = list.find((g) => g.slug === slug) || null;
    const nav = game === null ? null : destinations(document, game.slug, DESTINATION_PROSE);
    renderHeader({ me: who.ok ? who.body : null, games: list, current: game, nav });
    if (game !== null) {
      // The document's own title is not known yet — it arrives with the
      // fetch below, which rewrites the last crumb. Until then the trail
      // carries the path, which is what the address already says.
      setBreadcrumb(document, [
        { label: game.name, href: gameURL(game.slug) },
        { label: DESTINATION_PROSE, href: gameURL(game.slug) + "#prose" },
        { label: docPath },
      ]);
    }

    if (!docPath) {
      titleEl.textContent = t("document.noneAsked");
      showFailure(t("document.noSuchDocument"));
    } else if (!games.ok) {
      titleEl.textContent = t("document.unloadable");
      showFailure(games.message);
    } else if (!game) {
      titleEl.textContent = t("error.gameNotFound");
      showFailure(t("error.noAccessToGame"));
    } else {
      // The stored slug, for the reason app.js's own call says.
      await renderDocument(game.slug, docPath, game.name);
    }
  }
}

// renderDocument draws the whole page from four requests: the rendered
// reading view, the member list (for the history's author names), the
// game summary (for the caller's role, which decides whether a revert
// button is offered at all) and the first page of history.
async function renderDocument(game, docPath, gameName) {
  const titleEl = document.getElementById("doc-title");
  const metaEl = document.getElementById("doc-meta");
  const bodyEl = document.getElementById("doc-body");

  const rendered = await fetchAPI(
    `/api/games/${game}/docs/rendered?path=${encodeURIComponent(docPath)}`,
  );
  if (!rendered.ok) {
    if (rendered.expired) {
      goToLogin();
      return;
    }
    if (titleEl) titleEl.textContent = t("document.unreadable");
    showFailure(rendered.message);
    return;
  }

  const doc = rendered.body ?? {};
  const version = Number(doc.version ?? 0);
  // Loaded before the meta line rather than after the body, because the
  // meta line names the document's last author and describeAuthor falls
  // back to this map for a user the server could not resolve.
  const membersByID = await loadMembers(game);
  const docTitle = doc.title || doc.path || docPath;
  if (titleEl) titleEl.textContent = docTitle;
  // Every tab in the reading view was called "Maestro".
  document.title = docTitle + " \u00b7 Maestro";
  // The last crumb, now that the document has a title. It carried the
  // path until this line, which is what the address says and what a
  // reader who arrived by link already has.
  setBreadcrumb(document, [
    { label: gameName, href: gameURL(game) },
    { label: DESTINATION_PROSE, href: gameURL(game) + "#prose" },
    { label: docTitle },
  ]);
  // What this screen cannot do, where the action would be. The reading
  // view is the one screen whose content is most obviously editable-
  // looking and it said nothing at all.
  const summary = await fetchAPI(`/api/games/${game}/summary`);
  // **The notice is a claim about the screen**, so a screen that has
  // gained a write loses the half of the claim that said it had none —
  // the same swap the entity page makes. A viewer still gets the
  // notice, because for them it is still true.
  const mayWrite = summary.ok && summary.body.role !== "viewer";
  if (mayWrite) wireDocEditor(game, docPath, { version });
  else if (summary.ok) setReadOnly(document, summary.body.role, "writes.document");
  if (metaEl) {
    // The kind is optional on the wire, so the line is assembled from
    // the parts that are actually there rather than printing an empty
    // one.
    const parts = [doc.path ?? docPath];
    if (doc.kind) parts.push(doc.kind);
    parts.push(`version ${version}`);
    // When it last changed and who changed it, which is the question a
    // designer opens a document to ask and which used to take a
    // docs.history call to answer. describeAuthor takes a version-shaped
    // object, so the document's own author is handed to it in that
    // shape rather than duplicating its four cases here.
    if (doc.updated_at) {
      const who = describeAuthor(
        {
          author_kind: doc.updated_by?.kind,
          author_id: doc.updated_by?.id,
          author_label: doc.updated_by?.label,
        },
        membersByID,
      );
      parts.push(`changed ${describeTime(doc.updated_at)} · ${who}`);
    }
    metaEl.textContent = parts.join(" · ");
  }
  if (bodyEl) {
    setRenderedHTML(bodyEl, doc.html);
    bodyEl.hidden = false;
  }

  // The reader's own role, which the summary above already carries: the
  // empty state's last sentence depends on it and a viewer must not be
  // told to attach a document the server will refuse to attach.
  fillEntities(Array.isArray(doc.links) ? doc.links : [], summary.ok ? summary.body.role : "");

  const role = await loadRole(game);
  await renderHistory(game, docPath, version, membersByID, role);

  const content = document.getElementById("doc-content");
  if (content) {
    content.hidden = false;
  }
}

// fillEntities renders the attachments the reading view already carried,
// so this page does not ask /docs/links for what /docs/rendered just
// answered with.
function fillEntities(links, role) {
  fillState(document, "doc-entities-empty", {
    heading: NOT_ATTACHED_HEADING,
    sentence: whoWrites(role, ATTACHES_IT),
  });
  const listEl = document.getElementById("doc-entities");
  const emptyEl = document.getElementById("doc-entities-empty");
  if (!listEl) {
    return;
  }
  listEl.replaceChildren();
  for (const link of links) {
    listEl.append(entityRow(link));
  }
  listEl.hidden = links.length === 0;
  if (emptyEl) emptyEl.hidden = links.length > 0;
}

// loadMembers maps a user id to a display name.
async function loadMembers(game) {
  const result = await fetchAPI(`/api/games/${game}/members`);
  const byID = new Map();
  if (!result.ok) {
    return byID;
  }
  const members = Array.isArray(result.body?.members) ? result.body.members : [];
  for (const member of members) {
    if (member && member.id) {
      byID.set(member.id, member.display_name || "");
    }
  }
  return byID;
}

// loadRole reads the caller's own role off the game summary, which is
// the only place this instance publishes it to a browser.
async function loadRole(game) {
  const result = await fetchAPI(`/api/games/${game}/summary`);
  if (!result.ok) {
    return null;
  }
  return result.body?.role ?? null;
}

// renderHistory fills the version list, one page at a time, and wires
// the compare form's two pickers and each row's revert button as it
// goes.
async function renderHistory(game, docPath, currentVersion, membersByID, role) {
  const listEl = document.getElementById("doc-history");
  const errorEl = document.getElementById("doc-history-error");
  const moreEl = document.getElementById("doc-history-more");
  const noteEl = document.getElementById("doc-revert-note");
  const fromEl = document.getElementById("compare-from");
  const toEl = document.getElementById("compare-to");
  if (!listEl) {
    return;
  }
  if (noteEl && role === "viewer") {
    noteEl.textContent =
      t("document.viewerCannotRevert");
    noteEl.hidden = false;
  }

  let cursor = null;

  let shown = 0;

  function addOption(select, version) {
    if (!select) return;
    const option = document.createElement("option");
    option.value = String(version);
    option.textContent = `Version ${version}`;
    select.append(option);
  }

  async function loadPage() {
    if (moreEl) moreEl.disabled = true;
    const query = new URLSearchParams({ path: docPath });
    if (cursor) query.set("cursor", cursor);
    const result = await fetchAPI(`/api/games/${game}/docs/history?${query.toString()}`);
    if (!result.ok) {
      if (result.expired) {
        goToLogin();
        return;
      }
      // The rows already on the page stay: a failed *next* page is not a
      // reason to throw away the history a reader is looking at.
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
    for (const item of items) {
      listEl.append(historyRow(game, docPath, currentVersion, item, membersByID, role));
      addOption(fromEl, item.version);
      addOption(toEl, item.version);
    }
    shown += items.length;
    cursor = nextCursorOf(body);
    if (moreEl) {
      // The same rule the four list pages carry: a pager on a list that
      // rendered nothing is a control with nothing to fetch. This was the
      // one place it was not carried to, which is the shape this
      // repository calls its second most repeated defect.
      moreEl.hidden = cursor === null || shown === 0;
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

  // The two pickers default to "the last change": the previous version
  // on the left and the current one on the right, which is the
  // comparison a designer opening this page almost always wants.
  if (fromEl && toEl && fromEl.options.length > 1) {
    fromEl.selectedIndex = 1;
    toEl.selectedIndex = 0;
  }
  wireCompareForm(game, docPath);
}

// historyRow is one version: its number, its message, when it landed and
// who wrote it, plus a revert button on every version but the current
// one.
function historyRow(game, docPath, currentVersion, version, membersByID, role) {
  const item = document.createElement("li");

  const number = document.createElement("span");
  number.className = "history-version";
  number.textContent = `Version ${version.version}`;
  item.append(number);

  const message = document.createElement("span");
  message.className = "history-message";
  // A save may carry no message, and an empty cell says that better
  // than inventing words for a designer who chose not to write any.
  message.textContent = version.message ?? "";
  item.append(message);

  const who = document.createElement("span");
  who.className = "history-author";
  who.textContent = `${describeTime(version.created_at)} · ${describeAuthor(version, membersByID)}`;
  item.append(who);

  // A tombstone is a version like any other and shows in the history;
  // saying so is what stops a reader wondering why a version has no
  // message and no body behind it.
  if (version.deleted) {
    const tombstone = document.createElement("span");
    tombstone.className = "history-note";
    tombstone.textContent = t("document.deletedHere");
    item.append(tombstone);
  }

  if (Number(version.version) !== Number(currentVersion) && role !== "viewer") {
    const revert = document.createElement("button");
    revert.type = "button";
    revert.className = "link-button history-revert";
    revert.textContent = `Restore version ${version.version}`;
    revert.addEventListener("click", async () => {
      revert.disabled = true;
      const result = await postJSON(`/api/games/${game}/docs/revert`, {
        path: docPath,
        to_version: Number(version.version),
        // The version this page was drawn from, not the one being
        // restored: this is the compare-and-set, and it is what makes a
        // save somebody else landed in the meantime a conflict instead
        // of a silent overwrite.
        expected_version: Number(currentVersion),
        message: `Restore version ${version.version}`,
      });
      if (result.ok) {
        // Reloaded rather than patched in place: a revert adds a
        // version, moves the current one and changes the body, the
        // history and both pickers, and re-reading the page is how this
        // view shows the result of a write rather than guessing at it.
        window.location.reload();
        return;
      }
      revert.disabled = false;
      const note = document.getElementById("doc-history-error");
      if (note) {
        // The server's own words: a version_conflict says which version
        // the document is on now, which is the sentence the reader needs
        // and not one this page could write.
        note.textContent = result.message || fallbackMessage;
        note.hidden = false;
      }
    });
    item.append(revert);
  }

  return item;
}

// describeComparison names the three things a rendered diff cannot say
// for itself, and says nothing at all otherwise.
function describeComparison(comparison) {
  if (comparison.coarse) {
    return (
      t("document.diff.tooLarge")
    );
  }
  if (comparison.to_deleted && !comparison.from_deleted) {
    return t("document.diff.deletedAt");
  }
  if (comparison.from_deleted && !comparison.to_deleted) {
    return t("document.diff.deletedThenWritten");
  }
  if (!hasChangedLines(comparison.unified)) {
    return t("document.diff.identical");
  }
  return "";
}

// hasChangedLines reports whether a unified diff says anything at all.
export function hasChangedLines(unified) {
  const text = typeof unified === "string" ? unified : "";
  return text.split("\n").some(
    (line) =>
      (line.startsWith("+") && !line.startsWith("+++")) ||
      (line.startsWith("-") && !line.startsWith("---")),
  );
}

// wireCompareForm turns the two pickers into a request to
// /docs/comparison, whose html is the second and last thing on this page
// that goes in as markup.
function wireCompareForm(game, docPath) {
  const form = document.getElementById("compare-form");
  const fromEl = document.getElementById("compare-from");
  const toEl = document.getElementById("compare-to");
  const errorEl = document.getElementById("compare-error");
  const noteEl = document.getElementById("compare-note");
  const outEl = document.getElementById("comparison");
  if (!form || !fromEl || !toEl || !outEl) {
    return;
  }
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (errorEl) errorEl.textContent = "";
    if (noteEl) {
      noteEl.textContent = "";
      noteEl.hidden = true;
    }
    const query = new URLSearchParams({
      path: docPath,
      from_version: fromEl.value,
      to_version: toEl.value,
    });
    const result = await fetchAPI(`/api/games/${game}/docs/comparison?${query.toString()}`);
    if (!result.ok) {
      if (result.expired) {
        goToLogin();
        return;
      }
      outEl.hidden = true;
      if (errorEl) errorEl.textContent = result.message;
      return;
    }
    const comparison = result.body ?? {};
    // **Nothing to draw is not a box with nothing in it.** An identical
    // comparison rendered as a bordered frame holding the diff's own two
    // header lines, beside a sentence saying the versions are identical:
    // the reader was told the truth and shown something that looked like
    // a failed render of it.
    // **A diff with no changed line draws nothing.** That is every case
    // with an empty diff and not only the identical one: a comparison
    // spanning a deletion is empty too — the tombstone carries the body
    // the document had when it went — and its answer is the sentence
    // beside it, not a bordered box holding the diff's own two file
    // headers. A coarse comparison has real changed lines and draws like
    // any other, so there is no branch for it here; a branch nothing
    // reaches is the lie this file would have grown.
    const draws = hasChangedLines(comparison.unified);
    if (draws) setRenderedHTML(outEl, comparison.html);
    else outEl.replaceChildren();
    outEl.hidden = !draws;
    // Both of these are *correct* answers, which is why they go in
    // #compare-note and not in #compare-error: the error line is red,
    // and a reader who is told the truth in the colour reserved for
    // failure reads it as one.
    const note = describeComparison(comparison);
    if (noteEl && note) {
      noteEl.textContent = note;
      noteEl.hidden = false;
    }
  });
}
