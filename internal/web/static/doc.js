// The reading view: one document of one game, rendered, with the
// entities it is attached to, its history, a comparison between two of
// its versions and a revert.
//
// It shares app.js's helpers by importing them rather than repeating
// them — one fetch wrapper, one 401 policy, one header — and app.js's
// own page blocks are guarded on elements this page does not have
// (#games, #login, #game-name), so importing it runs none of them.
//
// **This file is the one place in internal/web/static that inserts
// server-supplied HTML into the DOM**, and setRenderedHTML below is the
// only sink. app.js has none at all and must keep none:
// TestAppScriptNeverWritesRawHTML pins that, and
// TestTheDocumentScriptHasExactlyOneHTMLSink pins that this file has
// exactly one and that it is fed from a rendered view.
import {
  fallbackMessage,
  fetchAPI,
  fetchGames,
  goToLogin,
  postJSON,
  renderHeader,
} from "./app.js";
// The chrome every other page gets. This reading view was rendering the
// header with no destinations at all, which is one of the three
// different chromes the 2026-09-09 audit found inside a single game.
import { DESTINATION_PROSE, destinations, gameURL, setBreadcrumb, setReadOnly } from "./pages/page.js";
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
//
// The Content-Security-Policy on every response from this server
// (default-src 'self', internal/web's securityHeaders) is a second line
// and not the first: innerHTML never executes a <script> it inserts, but
// an event-handler attribute would run without one, and the renderer not
// emitting attributes at all is what actually stops that.
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
//
// **author_label is the answer whenever the server gives one**, and it
// is what this function was missing. VersionOutput used to carry only a
// kind and an id, so a token could not be resolved at all — its label
// lives on api_tokens and nothing published it — and every agent's work
// read as "an agent", ten times over for ten versions by three agents.
// The server resolves both kinds now, in one query per page, and a
// revoked token still comes back named: revoking it changed what it may
// do next, not who wrote this.
//
// The member map stays as the fallback for a user the server could not
// name, which is the older of the two paths and still the right answer
// there. A raw uuid never reaches the screen either way: a designer
// reading a history does not know what one is.
function describeAuthor(version, membersByID) {
  if (version.author_label) {
    return version.author_label;
  }
  if (version.author_kind === "token") {
    return "an agent";
  }
  if (version.author_kind === "user") {
    if (!version.author_id) {
      return "a former member";
    }
    // Present-but-nameless is a different fact from absent, and saying
    // "a former member" about someone still on the members list is
    // simply false. GET /members can answer with an empty display_name,
    // so the two cases are told apart by whether the id is *in* the map
    // rather than by whether the name is truthy.
    if (!membersByID.has(version.author_id)) {
      return "a former member";
    }
    return membersByID.get(version.author_id) || "a member with no display name";
  }
  return "an unknown author";
}

// describeTime formats a timestamp in the reader's own locale, and falls
// back to the server's ISO string rather than to "Invalid Date" if it is
// not parseable.
function describeTime(raw) {
  const when = new Date(raw);
  return Number.isNaN(when.getTime()) ? String(raw ?? "") : when.toLocaleString();
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
  //
  // It is fetched **before the header** and before the "no document
  // asked for" branch, which it did not used to be: the header carries
  // the game switcher now (app.js's renderHeader) and the switcher is
  // this list. A reading view that skipped the fetch on its own error
  // paths would be a page with no way off it, which is the exact defect
  // the switcher exists to close — and this page has more error paths
  // than any other in the product.
  const games = await fetchGames();
  if (!games.ok && games.expired) {
    goToLogin();
  } else {
    const list = games.ok ? games.games : [];
    const game = list.find((g) => g.slug === slug) || null;
    const nav = game === null ? null : destinations(document, game.slug, DESTINATION_PROSE);
    renderHeader({ games: list, current: game, nav });
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
      titleEl.textContent = "No document asked for";
      showFailure("This address names no document. Open one from the game's Documents list.");
    } else if (!games.ok) {
      titleEl.textContent = "Could not load this document";
      showFailure(games.message);
    } else if (!game) {
      titleEl.textContent = "Game not found";
      showFailure("You may not have access to this game, or it no longer exists.");
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
//
// The reading view comes first and its failure is the page's failure:
// there is nothing worth showing beside a document that could not be
// read.
// `game` is the **slug**, not the row: every fetch below addresses the
// game by slug. The name comes in beside it because the trail says the
// game's name and reading it off a slug string gave an empty first
// crumb — found by opening the page.
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
    if (titleEl) titleEl.textContent = "Could not read this document";
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
  if (summary.ok) setReadOnly(document, summary.body.role, "writes this document");
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

  fillEntities(Array.isArray(doc.links) ? doc.links : []);

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
function fillEntities(links) {
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
//
// It is the *fallback* for describeAuthor now that the server resolves
// author_label itself, and it is kept rather than deleted because the
// server answers no label for a user it could not resolve, where this
// page can still know the name from the member list it loads anyway.
//
// A failure is not the page's failure: the history still renders, with
// "a former member" where a name would have been, which is the same
// sentence a departed author gets and is honest in both cases — the page
// genuinely does not know who that id is.
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
//
// **The role decides what is offered, never what is allowed.** A viewer
// is refused a revert by registerContentRoute (internal/web/server.go)
// on every request, whatever this page renders; hiding the button only
// stops offering an action the server will refuse. An unreadable summary
// therefore falls back to showing the button — the server is the check,
// and a page that hid every action whenever a side request failed would
// be lying about what the reader may do.
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
//
// currentVersion is the document's own version, and it is what every
// revert sends as expected_version: a revert is a compare-and-set like
// any other write, so if somebody else saved while this page was open
// the server answers version_conflict and the page says so rather than
// overwriting them.
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
      "Your role in this game is viewer, so this instance will refuse a revert from you: " +
      "an editor, an admin or the owner reverts a document.";
    noteEl.hidden = false;
  }

  let cursor = null;

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
    tombstone.textContent = "deleted here";
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
//
// A `coarse` comparison is the whole document replaced because the two
// versions were too large to compare line by line; without the sentence
// it reads as a change nobody made. And two identical versions produce a
// unified diff of nothing but its own file headers, which renders as two
// grey lines and looks like a page that failed to load rather than like
// an answer.
//
// The third is a comparison that spans a deletion, and it is the one
// this function got *wrong* rather than merely left unsaid until the
// prose sub-project's end-to-end run: a tombstone version carries the
// body the document had when it was deleted, so the unified diff between
// the last live version and the tombstone is empty, and this function
// answered "These two versions are identical" about a comparison whose
// whole content is that the document was deleted. The deletion is
// checked before the emptiness for exactly that reason. from_deleted and
// to_deleted come from the server (DocComparisonOutput), because the
// page cannot tell a tombstone from an unchanged version by looking at
// the diff.
function describeComparison(comparison) {
  if (comparison.coarse) {
    return (
      "These two versions were too large to compare line by line, " +
      "so this shows the whole document replaced."
    );
  }
  if (comparison.to_deleted && !comparison.from_deleted) {
    return "The document was deleted at this version; the text is what it held when it went.";
  }
  if (comparison.from_deleted && !comparison.to_deleted) {
    return "The document was deleted at the earlier of these two versions and written again after it.";
  }
  const unified = typeof comparison.unified === "string" ? comparison.unified : "";
  const changed = unified.split("\n").some(
    (line) =>
      (line.startsWith("+") && !line.startsWith("+++")) ||
      (line.startsWith("-") && !line.startsWith("---")),
  );
  if (!changed) {
    return "These two versions are identical.";
  }
  return "";
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
    setRenderedHTML(outEl, result.body?.html);
    outEl.hidden = false;
    // Both of these are *correct* answers, which is why they go in
    // #compare-note and not in #compare-error: the error line is red,
    // and a reader who is told the truth in the colour reserved for
    // failure reads it as one.
    const note = describeComparison(result.body ?? {});
    if (noteEl && note) {
      noteEl.textContent = note;
      noteEl.hidden = false;
    }
  });
}
