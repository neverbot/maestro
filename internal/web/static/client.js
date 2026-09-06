// The data client: the one module in this front end that talks to the
// server. Every call and the event stream go through here, so a
// component is a function from data to marks and nothing else.
//
// Three rules hold this file together, and each of them is asserted
// rather than only stated.
//
// **It never composes an error sentence.** A refusal reaches a designer
// as the server's own code, pointer and message, unmodified. The views
// sub-project generated its agent-facing prose from its own structures
// and asserted it in both directions precisely so that a second copy
// could not drift; restating any of that wording here is exactly the
// drift that work refused. What this module adds is navigation — which
// path, which body, which decision — and never wording. The guard is
// mechanical and lives in internal/web/static_client_test.go: no string
// literal in this file contains a space, because a sentence has spaces
// and a protocol token does not.
//
// **The reducer never patches state from a payload.** Publication order
// is not commit order (internal/views/events.go argues it at length), so
// a payload value can be older than what this client already holds.
// That is why every event kind in this product carries identity and
// nothing a client could mistake for current: view.positions and
// view.background carry an id and a key and no coordinates at all;
// view.upserted carries a version whose own doc comment says it is not a
// snapshot to trust. applyEvent therefore decides only what to *do* —
// re-read, band, gone or ignore — with the reason, and writes nothing.
//
// **Parameter binding lives in the URL.** A parameterised view is a
// link, so `?p.class=mage` is parsed and serialised here and every
// surface that navigates agrees on what it means.
//
// No absolute URL appears anywhere below: every path is relative to this
// instance, which is what lets a self-hosted Maestro with no outbound
// route work completely (static_vendor_test.go's own network scan).

// --- The decisions ---------------------------------------------------

// The four things a received event can cause, and the two coordinates a
// caller needs to act on one: a decision and the surface it applies to.
// They are exported constants rather than bare strings at the call sites
// so a renamed decision is a compile-time-shaped change here and a
// visible diff everywhere else.
export const REREAD = "reread";
export const BAND = "band";
export const GONE = "gone";
export const IGNORE = "ignore";

// What a decision applies to. A re-read of the *picture* re-runs the
// view; a re-read of the *view* re-reads the saved row without running
// it, which is what a vocabulary rename asks for — the view may have
// just become stale, and re-running it silently would either swap the
// picture or raise a refusal nobody asked for. `prose` is the document
// surfaces, `everything` is the stream-gap resync, and `nothing` is what
// an ignored event applies to.
export const TARGET_PICTURE = "picture";
export const TARGET_VIEW = "view";
export const TARGET_PROSE = "prose";
export const TARGET_EVERYTHING = "everything";
export const TARGET_NOTHING = "nothing";

// The debounce window for a re-read, in milliseconds. One number, named
// once: an agent seeding a game publishes one event per row, and forty
// of them inside this window are one re-read.
export const REREAD_DEBOUNCE_MS = 750;

// The reconnect backoff. The server closes every stream after its own
// lifetime *by design* (internal/web/events.go's defaultSSEMaxLifetime),
// so a client that does not reconnect looks healthy for four minutes and
// then goes quiet. These bounds are the client half of that design.
export const RECONNECT_BASE_MS = 1000;
export const RECONNECT_MAX_MS = 15000;

// applyEvent decides what a received event does.
//
// It is a pure function of the state it is given and the event: it reads
// `state` to know which views are open and writes nothing, anywhere. The
// decision it returns carries identity — a view key, a rename's two
// spellings — and never a value from the payload that a caller could
// store as current. In particular it never carries a `version`: the
// version on view.upserted is the token a *write* must carry, not a
// snapshot, and the re-read that follows returns the current one.
//
// The default arm is `ignore` with the kind recorded, so an unhandled
// kind is visible in a test rather than absent from behaviour.
export function applyEvent(state, event) {
  const kind = event && typeof event.kind === "string" ? event.kind : "";
  const data = event && event.data && typeof event.data === "object" ? event.data : {};
  switch (kind) {
    // A placement moved and the question did not: re-run the same view
    // with the same parameters. Automatic, because nothing a designer
    // asked for has changed.
    case "view.positions":
    case "view.background":
      return forView(state, kind, data, REREAD, TARGET_PICTURE, "placement.moved");
    // The query changed. The picture is *not* swapped under the
    // designer, for any role: a band says so and they choose.
    case "view.upserted":
      return forView(state, kind, data, BAND, TARGET_PICTURE, "query.changed");
    // A sentence and a link back. Nothing is auto-navigated.
    case "view.removed":
      return forView(state, kind, data, GONE, TARGET_PICTURE, "view.removed");
    // The game renamed a type or a relation type. Re-read the view row
    // only: what a run of it now reports may be a staleness diagnostic,
    // and discovering that by silently re-running is how a designer
    // loses the picture they were reading.
    case "type.renamed":
    case "relation_type.renamed":
      return renamed(kind, data, TARGET_VIEW, "vocabulary.renamed");
    // A path is a value that changes and not an identity to cache.
    case "document.moved":
      return renamed(kind, data, TARGET_PROSE, "path.changed");
    // The stream itself lost events: internal/web/events.go writes this
    // synthetic frame when a subscription's buffer overflowed, and its
    // whole meaning is "you missed something, go and re-read". Treating
    // it as an unknown kind would make the one signal the server sends
    // about its own gaps the one signal this client drops.
    case "resync":
      return decision(kind, REREAD, TARGET_EVERYTHING, "stream.gap");
    default:
      return decision(kind, IGNORE, TARGET_NOTHING, "kind.unhandled");
  }
}

// forView applies a view-keyed rule, ignoring an event about a view this
// client does not have open — the picture on screen is unaffected by a
// drag somebody made on another one.
function forView(state, kind, data, verdict, target, reason) {
  const key = typeof data.key === "string" ? data.key : "";
  if (!isOpen(state, key)) {
    return decision(kind, IGNORE, TARGET_NOTHING, "view.notopen");
  }
  const out = decision(kind, verdict, target, reason);
  out.key = key;
  return out;
}

// renamed carries both spellings, which is the whole reason these kinds
// exist as kinds of their own: a client holding the old handle cannot
// act on an event that names only the new one. Both are identity — two
// spellings of one row — and neither is content.
function renamed(kind, data, target, reason) {
  const out = decision(kind, REREAD, target, reason);
  out.from = typeof data.from === "string" ? data.from : "";
  out.to = typeof data.to === "string" ? data.to : "";
  return out;
}

function decision(kind, verdict, target, reason) {
  return { kind, decision: verdict, target, reason };
}

function isOpen(state, key) {
  if (!key) return false;
  const views = state && state.views && typeof state.views === "object" ? state.views : null;
  if (!views) return false;
  return Object.prototype.hasOwnProperty.call(views, key);
}

// --- Parameter binding -----------------------------------------------

// The URL prefix a bound parameter wears. A parameterised view is a
// link, so the binding has to survive a copy-paste into a colleague's
// address bar, and it has to be told apart from the page's own query
// keys.
export const PARAM_PREFIX = "p.";

// readParams pulls the bound parameters out of a query string.
//
// Every value is **text**, always, including one that looks like a
// number: a URL carries no types, and guessing that `20` is the number
// twenty would send the number to a text parameter and be refused for a
// reason the designer never wrote. typeParams below is where a
// declaration — which does know the type — turns text into a value.
//
// A parameter bound to nothing (`p.class=`) is present and empty; a
// parameter that is not in the URL at all is absent. Those are two
// different statements and this function keeps them apart, exactly as
// the palette keeps an absent slot apart from an empty one.
export function readParams(search) {
  const out = {};
  const params = new URLSearchParams(stripLeadingQuestion(search));
  for (const [name, value] of params) {
    if (!name.startsWith(PARAM_PREFIX)) continue;
    const key = name.slice(PARAM_PREFIX.length);
    if (key === "") continue;
    out[key] = value;
  }
  return out;
}

// writeParams serialises bound parameters back into a query string,
// keeping every non-parameter key the caller already had — a page that
// dropped its own tab or cursor on the way to binding a parameter would
// be a link that means something slightly different each time.
//
// The result is a query string without its leading `?`, so a caller
// composes the URL it wants.
export function writeParams(params, search) {
  const out = new URLSearchParams();
  const existing = new URLSearchParams(stripLeadingQuestion(search));
  for (const [name, value] of existing) {
    if (name.startsWith(PARAM_PREFIX)) continue;
    out.append(name, value);
  }
  const keys = Object.keys(params || {}).sort();
  for (const key of keys) {
    const value = params[key];
    if (value === undefined || value === null) continue;
    out.append(PARAM_PREFIX + key, String(value));
  }
  return out.toString();
}

// typeParams turns the text a URL carried into the values a run needs,
// using the query document's own parameter declarations.
//
// A value it cannot convert is passed through **unchanged** rather than
// repaired or refused here: the server declares the type, the server
// judges the value, and its refusal names the pointer and says why. A
// client that refused first would be composing the sentence this module
// exists not to compose, and it would do it from a copy of a rule that
// lives somewhere else.
export function typeParams(declarations, text) {
  const declared = new Map();
  for (const decl of Array.isArray(declarations) ? declarations : []) {
    if (decl && typeof decl.key === "string") declared.set(decl.key, decl.type);
  }
  const out = {};
  for (const key of Object.keys(text || {})) {
    const raw = text[key];
    switch (declared.get(key)) {
      case "number": {
        const value = Number(raw);
        out[key] = raw !== "" && Number.isFinite(value) ? value : raw;
        break;
      }
      case "bool":
        if (raw === "true") out[key] = true;
        else if (raw === "false") out[key] = false;
        else out[key] = raw;
        break;
      default:
        out[key] = raw;
    }
  }
  return out;
}

function stripLeadingQuestion(search) {
  const text = typeof search === "string" ? search : "";
  return text.startsWith("?") ? text.slice(1) : text;
}

// --- The stream frames -----------------------------------------------

// parseFrames turns a chunk of `text/event-stream` bytes into the events
// it completed, and returns whatever tail was left mid-frame.
//
// **A comment line is not an event.** The server writes `: connected`
// once and `: ping` every fifteen seconds, for a reverse proxy's benefit
// and to prove the connection is alive; neither fires anything on a
// browser's own EventSource and neither may fire anything here. A client
// that mistook a heartbeat for silence would reconnect every fifteen
// seconds; one that mistook it for an event would take a decision on it.
export function parseFrames(buffer) {
  const frames = [];
  let rest = buffer;
  for (;;) {
    const cut = rest.indexOf("\n\n");
    if (cut < 0) break;
    const block = rest.slice(0, cut);
    rest = rest.slice(cut + 2);
    const frame = parseFrame(block);
    if (frame) frames.push(frame);
  }
  return { frames, rest };
}

// parseFrame reads one block into an event, and returns null for a block
// that carried none.
//
// **A comment line is ignored because its field name is empty**, which
// is the SSE grammar's own reason and not a special case bolted on here:
// a line beginning with a colon has nothing before that colon, so its
// name is neither `event` nor `data` and it contributes nothing. There
// was an explicit `startsWith(":")` skip here and it came out again:
// removing it changed the behaviour of no input anybody could write, so
// it was a guard no test could ever turn red — a mechanism nothing
// reads. What carries the rule instead is the empty-block check below,
// which a mutation does turn red.
function parseFrame(block) {
  let kind = "";
  const dataLines = [];
  for (const line of block.split("\n")) {
    const colon = line.indexOf(":");
    const name = colon < 0 ? line : line.slice(0, colon);
    let value = colon < 0 ? "" : line.slice(colon + 1);
    // The one optional space after a field's colon, which the SSE
    // grammar says to strip. Spelled by code point rather than as a
    // literal because the guard over this module refuses a string
    // literal carrying whitespace, and it refuses it for the right
    // reason: whitespace in a literal here is what a sentence looks
    // like. 32 is the space this protocol means.
    if (value.charCodeAt(0) === 32) value = value.slice(1);
    if (name === "event") kind = value;
    else if (name === "data") dataLines.push(value);
  }
  if (kind === "" && dataLines.length === 0) return null;
  return { kind, data: parseJSON(dataLines.join("\n")) };
}

function parseJSON(text) {
  if (text === "") return {};
  try {
    const value = JSON.parse(text);
    return value && typeof value === "object" ? value : {};
  } catch (err) {
    return {};
  }
}

// --- The client ------------------------------------------------------

// client owns one game: its calls, its stream, and the deferral rules
// that keep a re-read cheap.
//
// Every seam a test needs is injected, and every one of them defaults to
// the browser's own: fetchImpl, the timers and the jitter source. There
// is no injected wall clock, though the plan's sketch of this signature
// names one: nothing here reads the time. The coalescing window and the
// reconnect backoff are both expressed as timers, which is the seam a
// test needs, and an injected `now` that nothing called would be a
// mechanism nothing reads. There is exactly one network primitive in
// this module —
// fetchImpl — and the stream goes through it too, which is what makes
// "one module owns every call and the stream" a property a source guard
// can check rather than a habit.
export function client({
  slug,
  fetchImpl = fetch,
  setTimer = setTimeout,
  clearTimer = clearTimeout,
  random = Math.random,
  onAnswer = null,
} = {}) {
  const state = {
    // The views this client has open, keyed by their key. Each holds the
    // parameters its last run was given and the envelope that run
    // returned. A re-read repeats the same question.
    views: {},
    dragging: false,
    pendingWrites: 0,
    hidden: false,
    connects: 0,
  };

  // The pending re-reads. Two sets, because a re-read of the *picture*
  // and a re-read of the *view row* are two different calls: a rename
  // asks for the row and must not silently re-run the query, and a
  // placement asks for the run. TARGET_EVERYTHING stands in dirtyRuns
  // for the whole-instance re-read a stream gap asks for.
  const dirtyRuns = new Set();
  const dirtyRows = new Set();
  let timer = null;
  let deferredFlush = false;
  let listener = null;
  let running = false;
  let backoff = RECONNECT_BASE_MS;

  const base = "/api/games/" + encodeURIComponent(String(slug || ""));
  const jsonHeaders = { "content-type": "application/json" };

  function emit(value) {
    if (listener) listener(value);
  }

  // deferred is the whole coalescing rule in one expression. A re-read
  // while a drag is in flight would swap the coordinates under the
  // pointer; one while a write is unacknowledged would read back the
  // state the write is about to change; one into a hidden tab is work
  // nobody is looking at.
  function isDeferred() {
    return state.dragging || state.pendingWrites > 0 || state.hidden;
  }

  function scheduleRun(key) {
    dirtyRuns.add(key);
    arm();
  }

  function scheduleRows() {
    for (const key of Object.keys(state.views)) dirtyRows.add(key);
    arm();
  }

  function arm() {
    // Not a resetting debounce: an agent writing steadily would reset a
    // resetting one forever and the picture would never catch up. The
    // window opens on the first event and closes once.
    if (timer !== null) return;
    timer = setTimer(fire, REREAD_DEBOUNCE_MS);
  }

  function fire() {
    timer = null;
    if (isDeferred()) {
      deferredFlush = true;
      return;
    }
    flush();
  }

  function flush() {
    deferredFlush = false;
    const runs = [...dirtyRuns];
    const rows = [...dirtyRows];
    dirtyRuns.clear();
    dirtyRows.clear();
    for (const key of rows) readView(key);
    for (const key of runs) {
      if (key === TARGET_EVERYTHING) {
        for (const open of Object.keys(state.views)) reread(open);
        continue;
      }
      reread(key);
    }
  }

  function release() {
    if (deferredFlush && !isDeferred()) flush();
  }

  function reread(key) {
    const open = state.views[key];
    if (!open) return;
    return runView(key, open.params, { onStale: open.onStale });
  }

  // runView asks the server the question and hands back what it said.
  //
  // The answer is either {ok: true, result} or {ok: false, error}, where
  // error is the server's own code, pointer and message, unmodified.
  async function runView(key, params, options) {
    const opts = options || {};
    const body = { key, params: params || {} };
    if (typeof opts.onStale === "string" && opts.onStale !== "") {
      body.on_stale = opts.onStale;
    }
    const answer = await send(base + "/views/run", body);
    if (answer.ok) {
      state.views[key] = {
        params: params || {},
        onStale: opts.onStale,
        result: answer.result,
      };
      // The seam a surface redraws on.
      //
      // **Without it the re-read is a mechanism nothing reads.** A
      // coalesced re-read is this module's own call, made on its own
      // timer, and until Task 15 its answer went into `state.views` and
      // no further — a page could ask a question and could not be told
      // that the answer had changed, which is the whole point of the
      // stream. It is one optional callback rather than an event target
      // because there is exactly one surface per view, and it carries
      // the envelope and the key and nothing else: what to *do* with a
      // new answer is the page's decision and never this module's.
      if (typeof onAnswer === "function") onAnswer(key, answer.result);
    }
    return answer;
  }

  // readView re-reads one saved view row without running it.
  //
  // This is what a vocabulary rename asks for and it is deliberately not
  // a run: a renamed type may have made the saved query stale, and a
  // silent re-run would either swap the picture under a designer or
  // raise a refusal they did not ask for. The row says what the view is
  // now; whether to re-run it is theirs to decide.
  async function readView(key) {
    const path = base + "/views/by-key/" + encodeURIComponent(key);
    const answer = await get(path);
    if (answer.ok && state.views[key]) state.views[key].row = answer.result;
    return answer;
  }

  // writePositions sends an arrangement.
  //
  // **The body is built from four named fields and never by copying a
  // node**, which is what makes "this client never sends an entity id" a
  // property of the construction rather than of a caller remembering to
  // delete one: a scene node carries `id`, positions are addressed by
  // (entity_type, entity_key), and an id in this body would be a second
  // address for a row whose key is the address everywhere else.
  async function writePositions(key, nodes) {
    const positions = [];
    for (const node of Array.isArray(nodes) ? nodes : []) {
      const placement = {
        entity_type: String(node.type),
        entity_key: String(node.key),
        x: Number(node.x),
        y: Number(node.y),
      };
      if (typeof node.pinned === "boolean") placement.pinned = node.pinned;
      positions.push(placement);
    }
    const path = base + "/views/by-key/" + encodeURIComponent(key) + "/positions";
    return counted(() => send(path, { positions }));
  }

  // clearPositions drops saved coordinates for the nodes it is given.
  //
  // **It refuses an empty list rather than sending one**, because on this
  // route an absent `entities` clears the *whole* view's arrangement and
  // `[]` is refused by the server: internal/web/mcp_views.go's
  // ViewsClearPositionsInput spends a pointer on exactly that distinction.
  // A caller whose selection came out empty must not wipe a designer's
  // afternoon of map work, and the shape that would do it is one absent
  // member away, so the guard is here rather than at the call site.
  async function clearPositions(key, nodes) {
    const entities = [];
    for (const node of Array.isArray(nodes) ? nodes : []) {
      entities.push({ entity_type: String(node.type), entity_key: String(node.key) });
    }
    if (entities.length === 0) return { ok: false, error: emptyError(0) };
    const path = base + "/views/by-key/" + encodeURIComponent(key) + "/positions/clear";
    return counted(() => send(path, { entities }));
  }

  // writeBackground points this view at an uploaded image, or clears it.
  //
  // **`asset_id: null` is the only spelling for a clear**, which is
  // internal/web/mcp_views.go's ViewsSetBackgroundInput contract, so this
  // function distinguishes `null` from absent rather than treating both
  // as "leave it": a caller that omitted the member wants the previous
  // image, a caller that sent null wants none, and a body that dropped
  // the null would silently be the first of those.
  //
  // The scale and the offset are sent only when the caller named them,
  // for the same reason and the same contract: nil there means "the
  // defaults", never "keep what is there", and a browser that always
  // sent its current knobs would inherit the previous image's arithmetic
  // onto a new one.
  async function writeBackground(key, background) {
    const input = background && typeof background === "object" ? background : {};
    const body = {};
    if (input.assetId !== undefined) {
      body.asset_id = input.assetId === null ? null : String(input.assetId);
    }
    if (Number.isFinite(input.scale)) body.scale = input.scale;
    const offset = input.offset;
    if (offset && Number.isFinite(offset.x) && Number.isFinite(offset.y)) {
      body.offset = { x: offset.x, y: offset.y };
    }
    const path = base + "/views/by-key/" + encodeURIComponent(key) + "/background";
    return counted(() => send(path, body));
  }

  // upsertView writes a saved view row back.
  //
  // The whole row goes, because internal/web/api_views.go's upsert takes
  // a whole row: there is no partial update on this surface, and a
  // client that sent only the field it changed would blank the query.
  // **It carries the version it read**, which is what makes the one
  // structural write in this sub-project — switching a view out of
  // `auto` so it can be dragged — refuse rather than overwrite a change
  // somebody else made in between. That is the opposite trade from
  // writePositions above, and both are deliberate.
  async function upsertView(row, changes) {
    const from = row && typeof row === "object" ? row : {};
    const patch = changes && typeof changes === "object" ? changes : {};
    const body = {
      key: String(from.key || ""),
      name: String(from.name || ""),
      query: from.query,
      renderer: String(from.renderer || ""),
      renderer_params: from.renderer_params || {},
      layout_mode: String(patch.layoutMode || from.layout_mode || ""),
      expected_version: Number.isFinite(from.version) ? from.version : null,
    };
    if (typeof from.description === "string" && from.description !== "") {
      body.description = from.description;
    }
    return counted(() => send(base + "/views", body));
  }

  // uploadAsset sends the image's bytes.
  //
  // The body is the file itself and the filename rides in the query
  // string, which is internal/web/api_view_assets.go's contract and its
  // header says why: one file per request needs no second parser over
  // hostile bytes, and the name is prose that is stored and never
  // consulted for the format. No content-type is set — the server sniffs
  // the mime out of the bytes and refuses anything it was told.
  async function uploadAsset(file, filename) {
    const path = base + "/view-assets?filename=" + encodeURIComponent(String(filename || ""));
    return counted(() => request(path, { method: "POST", body: file }));
  }

  // listAssets pages this game's uploaded images, the way every other
  // listing in this product is paged.
  async function listAssets(options) {
    const opts = options && typeof options === "object" ? options : {};
    const search = new URLSearchParams();
    if (typeof opts.cursor === "string" && opts.cursor !== "") search.set("cursor", opts.cursor);
    if (Number.isFinite(opts.limit)) search.set("limit", String(opts.limit));
    const query = search.toString();
    return get(base + "/view-assets" + (query === "" ? "" : "?" + query));
  }

  // --- The reads the pages navigate by --------------------------------
  //
  // Task 15 put seven page modules on seven routes, and every one of
  // them needed a shape this module did not have: the game's own name,
  // its catalogue, its saved views, its prose, one entity type's field
  // schema, one entity and the edges either side of it. **The rule that
  // sent the work here is this file's own** — one module owns every call
  // — and internal/web/static_client_test.go's two guards are what make
  // that a property rather than a habit, so a page that needed a shape
  // widened the client instead of reaching for `fetch`.
  //
  // Every one of them is a GET, every one returns the server's own body
  // untouched, and none of them composes a word. The paging arguments
  // are spelled once, in `paged`, because a listing that spelled its own
  // cursor is a listing that can disagree with the next one about what a
  // cursor is called.

  // games is the one call on this client that is not scoped to a game,
  // and it is here rather than in a page for the rule's sake: the page
  // that shows a game's name has to know the slug reaches a game this
  // caller can open at all, which is what tells "not found" apart from a
  // blank page.
  async function games() {
    return get("/api/games");
  }

  async function summary() {
    return get(base + "/summary");
  }

  async function listViews(options) {
    return get(paged(base + "/views", options));
  }

  async function listDocs(options) {
    return get(paged(base + "/docs", options));
  }

  async function docKinds() {
    return get(base + "/docs/kinds");
  }

  async function listTypes() {
    return get(base + "/types");
  }

  // getType is the field schema an entity page renders its rows from —
  // in declared order, including the fields the entity does not carry.
  // The listing does not carry a schema, so this is a call and not a
  // filter over one.
  async function getType(key) {
    return get(base + "/types/by-key/" + encodeURIComponent(String(key || "")));
  }

  async function listRelationTypes() {
    return get(base + "/relation-types");
  }

  async function getRelationType(key) {
    return get(base + "/relation-types/by-key/" + encodeURIComponent(String(key || "")));
  }

  // listEntities is the catalogue page's own listing, and `verbose` is
  // its caller's decision rather than this module's: a catalogue of two
  // hundred rows does not want two hundred field objects, and the entity
  // page that does asks for one row.
  async function listEntities(options) {
    const opts = options && typeof options === "object" ? options : {};
    const search = new URLSearchParams();
    if (typeof opts.typeKey === "string" && opts.typeKey !== "") {
      search.set("type_key", opts.typeKey);
    }
    if (opts.verbose === true) search.set("verbose", "true");
    return get(paged(base + "/entities", opts, search));
  }

  async function getEntity(typeKey, key) {
    const path =
      base +
      "/entities/by-key/" +
      encodeURIComponent(String(typeKey || "")) +
      "/" +
      encodeURIComponent(String(key || ""));
    return get(path);
  }

  // listRelations asks the edges from one side. **Both directions are
  // two calls and never one filtered list**: the route filters on an
  // endpoint, an edge is directed, and a page that asked once and sorted
  // the answer would be inventing a direction the server did not state.
  async function listRelations(options) {
    const opts = options && typeof options === "object" ? options : {};
    const search = new URLSearchParams();
    const ends = [
      ["source_type_key", opts.sourceType],
      ["source_key", opts.sourceKey],
      ["target_type_key", opts.targetType],
      ["target_key", opts.targetKey],
    ];
    for (const [name, value] of ends) {
      if (typeof value === "string" && value !== "") search.set(name, value);
    }
    if (opts.verbose === true) search.set("verbose", "true");
    return get(paged(base + "/relations", opts, search));
  }

  // listEntityDocs is the join from the entity's side. The route refuses
  // a call that names both sides and a call that names neither, so this
  // one names exactly one.
  async function listEntityDocs(typeKey, key, options) {
    const search = new URLSearchParams();
    search.set("entity_type", String(typeKey || ""));
    search.set("entity_key", String(key || ""));
    return get(paged(base + "/docs/links", options, search));
  }

  // paged appends the two arguments every listing on this surface takes,
  // onto whatever narrowing the caller already built.
  function paged(path, options, search) {
    const opts = options && typeof options === "object" ? options : {};
    const query = search instanceof URLSearchParams ? search : new URLSearchParams();
    if (typeof opts.cursor === "string" && opts.cursor !== "") query.set("cursor", opts.cursor);
    if (Number.isFinite(opts.limit)) query.set("limit", String(opts.limit));
    const text = query.toString();
    return path + (text === "" ? "" : "?" + text);
  }

  // counted is the bookkeeping every write shares: while one is in
  // flight a re-read is deferred, because a re-read that raced a write
  // would read back the state the write is about to change. It is a
  // wrapper rather than four copies for the reason writePositions'
  // `finally` exists at all — a write that threw and left the counter
  // raised would defer every re-read for the life of the page.
  async function counted(call) {
    state.pendingWrites += 1;
    try {
      return await call();
    } finally {
      state.pendingWrites -= 1;
      release();
    }
  }

  // send is the one place a request body becomes an answer, so the error
  // shape is built once. A transport failure and an unreadable body come
  // back with an empty code and an empty message and the status: this
  // module has no sentence for them and does not invent one, and the
  // surface that renders negative states owns that prose.
  async function send(path, body) {
    return request(path, {
      method: "POST",
      headers: jsonHeaders,
      body: JSON.stringify(body),
    });
  }

  async function get(path) {
    return request(path, { headers: { accept: "application/json" } });
  }

  async function request(path, init) {
    let res;
    try {
      res = await fetchImpl(path, init);
    } catch (err) {
      return { ok: false, error: emptyError(0) };
    }
    const payload = await readJSON(res);
    if (res.ok) return { ok: true, result: payload === null ? {} : payload };
    return { ok: false, error: errorFrom(res.status, payload) };
  }

  async function readJSON(res) {
    try {
      return await res.json();
    } catch (err) {
      return null;
    }
  }

  // connect opens the stream and keeps it open, reconnecting when the
  // server closes it — which it always eventually does, on purpose.
  //
  // A *re*connection schedules a re-read, because the hub keeps no
  // history: events published while this client was disconnected are
  // gone, and the server's own contract says a reconnected client
  // refetches over REST and trusts the stream from there. The first
  // connection schedules nothing: the page has just run its view.
  function connect(onDecision) {
    listener = onDecision || listener;
    if (running) return;
    running = true;
    loop();
  }

  // disconnect stops the stream and cancels a re-read that has not
  // happened yet: a page that has navigated away has no picture to
  // refresh, and a timer that fires into a closed surface is a request
  // nobody asked for.
  function disconnect() {
    running = false;
    if (timer !== null) {
      clearTimer(timer);
      timer = null;
    }
    dirtyRuns.clear();
    dirtyRows.clear();
    deferredFlush = false;
  }

  async function loop() {
    while (running) {
      const opened = await readStream();
      if (!running) return;
      if (opened) backoff = RECONNECT_BASE_MS;
      const wait = Math.min(backoff, RECONNECT_MAX_MS);
      backoff = Math.min(backoff * 2, RECONNECT_MAX_MS);
      await sleep(wait + Math.floor(random() * wait));
    }
  }

  async function readStream() {
    let res;
    try {
      res = await fetchImpl(base + "/events", {
        headers: { accept: "text/event-stream" },
      });
    } catch (err) {
      return false;
    }
    if (!res || !res.ok || !res.body || typeof res.body.getReader !== "function") {
      return false;
    }
    state.connects += 1;
    if (state.connects > 1) {
      for (const key of Object.keys(state.views)) scheduleRun(key);
    }
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    for (;;) {
      let chunk;
      try {
        chunk = await reader.read();
      } catch (err) {
        return true;
      }
      if (chunk.done) return true;
      buffer += decoder.decode(chunk.value, { stream: true });
      const parsed = parseFrames(buffer);
      buffer = parsed.rest;
      for (const frame of parsed.frames) receive(frame);
      if (!running) return true;
    }
  }

  function sleep(ms) {
    return new Promise((resolve) => setTimer(resolve, ms));
  }

  // receive routes one parsed frame through the reducer and acts on what
  // it said. It writes no state from the frame: a re-read is scheduled,
  // a band and a gone are handed to the surface, an ignore is recorded.
  function receive(frame) {
    const verdict = applyEvent(state, frame);
    if (verdict.decision === REREAD) {
      if (verdict.target === TARGET_EVERYTHING) scheduleRun(TARGET_EVERYTHING);
      else if (verdict.target === TARGET_PICTURE) scheduleRun(verdict.key);
      else if (verdict.target === TARGET_VIEW) scheduleRows();
    }
    emit(verdict);
    return verdict;
  }

  function setDragging(dragging) {
    state.dragging = Boolean(dragging);
    release();
  }

  function setHidden(hidden) {
    state.hidden = Boolean(hidden);
    release();
  }

  return {
    state,
    runView,
    readView,
    writePositions,
    clearPositions,
    writeBackground,
    upsertView,
    uploadAsset,
    listAssets,
    games,
    summary,
    listViews,
    listDocs,
    docKinds,
    listTypes,
    getType,
    listRelationTypes,
    getRelationType,
    listEntities,
    getEntity,
    listRelations,
    listEntityDocs,
    connect,
    disconnect,
    setDragging,
    setHidden,
  };
}

// errorFrom carries the server's three fields across unchanged.
//
// `error` on the wire is the code (internal/web's writeCodedError), the
// message is the domain's own sentence, and the pointer is the first
// address the refusal named — a JSON pointer into the query document for
// a field problem, or a staleness diagnostic's own pointer. `details`
// travels whole beside them, because a refusal can name several problems
// and handing on only the first would hide the rest.
function errorFrom(status, payload) {
  const body = payload && typeof payload === "object" ? payload : {};
  const details = body.details && typeof body.details === "object" ? body.details : null;
  return {
    code: typeof body.error === "string" ? body.error : "",
    message: typeof body.message === "string" ? body.message : "",
    pointer: pointerFrom(details),
    details,
    status,
  };
}

function emptyError(status) {
  return { code: "", message: "", pointer: "", details: null, status };
}

function pointerFrom(details) {
  if (!details) return "";
  const fields = Array.isArray(details.fields) ? details.fields : [];
  if (fields.length > 0 && typeof fields[0].path === "string") return fields[0].path;
  const stale = Array.isArray(details.stale) ? details.stale : [];
  if (stale.length > 0 && typeof stale[0].pointer === "string") return stale[0].pointer;
  return "";
}
