// The view frame's model: a pure function from what the server said to
// the plain data the frame draws. No DOM, no fetch, no state, no import.
//
// This module is written **before any renderer** and on purpose. The six
// drawings are drawings; this is where a designer learns the drawing is
// not the whole truth, and if each renderer owned its own version of
// these states there would be six of them, differing. Everything below
// is decided once, here, and `mst-view-frame.js` only paints it.
//
// One rule governs the wording, and it is a refusal.
//
// **The frame never says "complete".** internal/views/execute.go is
// explicit that `truncated.depth` cannot be set at all by a query whose
// every step is one hop, and that `truncated.nodes`/`.edges` can
// over-report; so a false flag means "not measured" as often as it means
// "not truncated". A picture with three false flags is a picture nothing
// is known to be missing from, which is not the same statement as a
// complete one, and the envelope refuses to make the second. When
// nothing is flagged this module says **nothing** — `bannersFor` returns
// an empty array and no sentence anywhere claims a whole picture.
//
// The second rule is Task 3's, carried one step along: **where the
// server has a sentence, it is rendered verbatim and never composed.**
// A staleness refusal carries `details.fields[].message`, generated from
// internal/views/stale.go's own structures and asserted there in both
// directions; restating any of it here is the drift that work refused.
// What this module writes in its own words is the half the server says
// nothing about — a truncation, an ambiguous node, an automatic
// placement, an empty answer — because for those there is no server
// sentence to render.
//
// A third, quieter rule: this module counts what the envelope counts.
// `node.ambiguous` is a flag on the **node** (execute.go argues it: a
// node with two related slots, one of them ambiguous, is flagged, and
// which of the two is deliberately not said), so the ambiguity band
// counts nodes and its sentence names nodes. Counting slots would be
// inventing a distinction the server declined to carry.

// --- The banner vocabulary -------------------------------------------

// One code per band. They are exported constants rather than bare
// strings at the call sites so a renamed band is a visible diff
// everywhere it is named, and so a test can ask for a band by identity
// rather than by matching its prose.
export const BANNER_STALE = "stale";
export const BANNER_DROPPED = "dropped";
export const BANNER_TRUNCATED_NODES = "truncated_nodes";
export const BANNER_TRUNCATED_EDGES = "truncated_edges";
export const BANNER_TRUNCATED_DEPTH = "truncated_depth";
export const BANNER_PLACED = "placed_automatically";
export const BANNER_AMBIGUOUS = "ambiguous";

// The order the stack is drawn in, fixed rather than incidental: a
// designer learns positions, and two of these routinely co-occur — a
// best-effort picture is very often also truncated. The three truncation
// codes sit together and in flag order (nodes, edges, depth) because
// they are three sentences about one cap family, never one summary.
//
// It is a list and not a comparison function so that the whole order is
// one readable thing a test can assert against.
export const BANNER_ORDER = [
  BANNER_STALE,
  BANNER_DROPPED,
  BANNER_TRUNCATED_NODES,
  BANNER_TRUNCATED_EDGES,
  BANNER_TRUNCATED_DEPTH,
  BANNER_PLACED,
  BANNER_AMBIGUOUS,
];

// The four kinds of thing the frame can be showing. They are four and
// not a set of booleans because they are mutually exclusive answers to
// "what is under the title strip", and a caller that has to combine
// flags to find out is a caller that will combine them wrongly.
//
//   picture     — a drawing. The ordinary case, negative bands or not.
//   empty       — a successful run that matched nothing.
//   diagnostics — a refusal. **No picture at all**, which is the whole
//                 point of the failing on_stale default.
//   unbound     — a refusal whose every diagnostic is param_unbound.
//                 Answered in the parameter bar, which is the place that
//                 can fix it, and not with a JSON pointer.
export const KIND_PICTURE = "picture";
export const KIND_EMPTY = "empty";
export const KIND_DIAGNOSTICS = "diagnostics";
export const KIND_UNBOUND = "unbound";

// The one action the diagnostics panel offers, and the wire value it
// sends. No repair action exists anywhere in this module: the repair
// edits an author's query document, and this interface has no query
// editor and no diff to show one in (design spec O6).
export const ACTION_RUN_BEST_EFFORT = "run_best_effort";
export const ON_STALE_BEST_EFFORT = "best_effort";

// The staleness diagnostic codes this module has to tell apart. The
// vocabulary is internal/views/stale.go's; these three are the ones the
// frame treats specially, and every other code falls through to the
// panel unexamined, which is what keeps a ninth code from being silently
// dropped here.
export const DIAG_ENTITY_TYPE_RENAMED = "entity_type_renamed";
export const DIAG_RELATION_TYPE_RENAMED = "relation_type_renamed";
export const DIAG_PARAM_UNBOUND = "param_unbound";

// The wire codes a refusal arrives under. `query_stale` is the only one
// "run anyway" can answer: it means the *game* moved under a saved
// document, which is exactly what best effort prunes on.
export const CODE_QUERY_STALE = "query_stale";

// --- The banner stack ------------------------------------------------

// bannersFor reads the envelope and returns the bands, in BANNER_ORDER.
//
// `placedAutomatically` is a count the caller owns: the envelope says
// which nodes have a saved position and the layout engine knows which of
// them it had to place, and neither half alone is the number. `droppedRefs`
// is likewise the caller's: a successful envelope does not echo the
// policy it ran under, so "this picture was drawn best-effort" is a fact
// only the caller that asked for it holds. Both default to nothing, so a
// caller that knows neither gets no band rather than a guessed one.
//
// Returns `[]` for a clean envelope. That empty array is the refusal at
// the top of this file, in code: there is no "everything is fine" band,
// because the envelope cannot support one.
export function bannersFor(envelope, options = {}) {
  const env = envelope && typeof envelope === "object" ? envelope : {};
  const placed = countOf(options.placedAutomatically);
  const dropped = Array.isArray(options.droppedRefs) ? options.droppedRefs : [];
  const banners = [];

  // The document's own staleness, and this run's losses. They are two
  // statements — one about the saved query, one about the picture in
  // front of the designer — and they are never both made, because they
  // would be the same list of pointers under two headings. The dropped
  // band wins when the caller has it: it carries the refusal's own
  // sentences, which the success envelope's diagnostics do not have.
  if (dropped.length > 0) {
    banners.push({
      code: BANNER_DROPPED,
      css: "var(--dropped)",
      text: `This picture was drawn without ${count(dropped.length, "reference", "references")} this view names.`,
      rows: dropped.map((ref) => ({
        code: text(ref && ref.code),
        pointer: text(ref && ref.pointer),
        was: text(ref && ref.was),
        now: text(ref && ref.now),
        // The server's own sentence, carried across the re-run from the
        // refusal that provoked it, unmodified.
        message: text(ref && ref.message),
        css: "var(--dropped)",
      })),
    });
  } else {
    const stale = staleRefsOf(env);
    if (stale.length > 0) {
      banners.push({
        code: BANNER_STALE,
        css: "var(--muted)",
        text: `This view names ${count(stale.length, "thing", "things")} this game no longer has.`,
        rows: stale,
      });
    }
  }

  // Three flags, three sentences, and only for the flags that are set.
  // A summary ("this picture is incomplete") would collapse three
  // different recoveries — raise the node cap, raise the edge cap, walk
  // deeper — into one sentence naming none of them.
  const truncated = env.truncated && typeof env.truncated === "object" ? env.truncated : {};
  if (truncated.nodes === true) {
    banners.push({
      code: BANNER_TRUNCATED_NODES,
      css: "var(--muted)",
      text: "This picture hit its node cap and the answer had more nodes than it shows.",
      rows: [],
    });
  }
  if (truncated.edges === true) {
    banners.push({
      code: BANNER_TRUNCATED_EDGES,
      css: "var(--muted)",
      text: "This picture hit its edge cap and the answer had more edges than it shows.",
      rows: [],
    });
  }
  if (truncated.depth === true) {
    banners.push({
      code: BANNER_TRUNCATED_DEPTH,
      css: "var(--muted)",
      text: "A traversal was cut short at its depth bound; there is more graph beyond it.",
      rows: [],
    });
  }

  if (placed > 0) {
    banners.push({
      code: BANNER_PLACED,
      css: "var(--muted)",
      text: `${count(placed, "new node was", "new nodes were")} placed automatically.`,
      rows: [],
    });
  }

  // Counted over nodes, because the flag is on the node. The sentence
  // says nodes for the same reason, and says nothing about which slot.
  const ambiguous = (Array.isArray(env.nodes) ? env.nodes : []).filter(
    (node) => node && node.ambiguous === true,
  ).length;
  if (ambiguous > 0) {
    banners.push({
      code: BANNER_AMBIGUOUS,
      css: "var(--muted)",
      text:
        `${count(ambiguous, "node", "nodes")} resolved a related attribute ambiguously — ` +
        "more than one entity matched and the first by name was used.",
      rows: [],
    });
  }

  return banners;
}

// staleRefsOf is the envelope's non-rename diagnostics, as rows.
//
// Renames are excluded here and answered in the title strip: they arrive
// beside a **full and correct** picture, because the reference resolved
// by id, so banding them would spend the loudest surface the frame has
// on the one diagnostic that costs the designer nothing today.
//
// A success envelope carries `stale[]` with a code, a pointer and the
// was/now pair and **no sentence** — the sentences live on the refusal
// (internal/views/stale.go's staleQuery puts them in Fields, which
// internal/web publishes as details.fields[].message). So these rows
// carry an empty message rather than a composed one, and the frame shows
// the pointer, which is an address a designer can act on.
export function staleRefsOf(envelope) {
  const env = envelope && typeof envelope === "object" ? envelope : {};
  const diags = Array.isArray(env.stale) ? env.stale : [];
  return diags
    .filter((d) => d && !isRenamed(d.code))
    .map((d) => ({
      code: text(d.code),
      pointer: text(d.pointer),
      was: text(d.was),
      now: text(d.now),
      message: "",
      css: "var(--dropped)",
    }));
}

// renamedFor is the quiet title-strip line, and it is deliberately not a
// band and offers no action.
//
// Returns null when nothing was renamed, so the caller has no empty
// object to test for. The line expands to the pointers: the designer's
// repair is `views.upsert` with the new spelling, which they hand to
// their agent, and a pointer is what an agent needs to find the place.
export function renamedFor(envelope) {
  const env = envelope && typeof envelope === "object" ? envelope : {};
  const rows = (Array.isArray(env.stale) ? env.stale : [])
    .filter((d) => d && isRenamed(d.code))
    .map((d) => ({
      code: text(d.code),
      pointer: text(d.pointer),
      was: text(d.was),
      now: text(d.now),
      message: "",
      css: "var(--muted)",
    }));
  if (rows.length === 0) return null;
  return {
    count: rows.length,
    css: "var(--muted)",
    text: `This view names ${count(rows.length, "thing", "things")} the game now spells differently.`,
    rows,
  };
}

function isRenamed(code) {
  return code === DIAG_ENTITY_TYPE_RENAMED || code === DIAG_RELATION_TYPE_RENAMED;
}

// --- The refusal -----------------------------------------------------

// diagnosticsFor turns a refusal into the panel that replaces the
// picture, plus the unbound parameters the parameter bar answers.
//
// It walks `details.fields` and not `details.stale`, which is the list
// with the **sentences** on it and is also the wider list: a stale
// document can be broken for a second reason at a position no diagnostic
// covers (internal/views/stale.go's staleQuery carries both), and
// walking the diagnostics would drop that reason silently. The code and
// the was/now pair are read off `details.stale` by pointer, which is the
// join the server built the two lists to allow.
//
// `param_unbound` comes out into `unbound`: a designer who opened a link
// without `?p.class=` is one field away from a picture, and sending them
// to a JSON pointer for that would be absurd.
export function diagnosticsFor(error) {
  const err = error && typeof error === "object" ? error : {};
  const details = err.details && typeof err.details === "object" ? err.details : {};
  const fields = Array.isArray(details.fields) ? details.fields : [];
  const stale = Array.isArray(details.stale) ? details.stale : [];

  const byPointer = new Map();
  for (const d of stale) {
    if (d && typeof d.pointer === "string" && !byPointer.has(d.pointer)) byPointer.set(d.pointer, d);
  }

  const rows = [];
  const unbound = [];
  for (const field of fields) {
    if (!field || typeof field !== "object") continue;
    const pointer = text(field.path);
    const diag = byPointer.get(pointer) || {};
    const row = {
      code: text(diag.code),
      pointer,
      was: text(diag.was),
      now: text(diag.now),
      // Verbatim. The whole reason this module reads `fields` at all.
      message: text(field.message),
      css: "var(--danger)",
    };
    if (row.code === DIAG_PARAM_UNBOUND) {
      unbound.push({ param: row.was, pointer: row.pointer, message: row.message });
      continue;
    }
    rows.push(row);
  }
  return { rows, unbound };
}

// runAction performs the one action the panel offers.
//
// It takes the client rather than importing it: this module fetches
// nothing and imports nothing, which is what lets a Node harness drive
// it, and it is the property internal/web/static_client_test.go's fetch
// perimeter holds for the whole front end. An action it does not know is
// a no-op returning null rather than a guess.
export function runAction(action, client, options = {}) {
  if (!action || action.code !== ACTION_RUN_BEST_EFFORT) return null;
  if (!client || typeof client.runView !== "function") return null;
  return client.runView(text(options.key), options.params || {}, {
    onStale: ON_STALE_BEST_EFFORT,
  });
}

// --- The strips ------------------------------------------------------

// footerFor is the strip that is always present, on every kind.
//
// **Two elapsed numbers, never one.** `stats.duration_ms` is what the
// query cost and `layoutMs` is what the picture cost, and "this view is
// slow" is unanswerable without both — the first question is which half.
// The layout number is absent until Task 6 runs an engine, and absent is
// how it renders: a zero would claim a measurement nobody made.
//
// `max_depth_reached` is measured and not declared (execute.go), so it
// is reported as what was reached and never as what was asked for.
export function footerFor(envelope, options = {}) {
  const env = envelope && typeof envelope === "object" ? envelope : {};
  const stats = env.stats && typeof env.stats === "object" ? env.stats : {};
  const nodes = countOf(stats.nodes);
  const edges = countOf(stats.edges);
  const maxDepth = countOf(stats.max_depth_reached);
  const durationMs = countOf(stats.duration_ms);
  const layoutMs = options.layoutMs === undefined || options.layoutMs === null
    ? null
    : countOf(options.layoutMs);
  const parts = [
    `${count(nodes, "node", "nodes")}, ${count(edges, "edge", "edges")}`,
    `depth ${maxDepth}`,
    `query ${durationMs} ms`,
  ];
  if (layoutMs !== null) parts.push(`layout ${layoutMs} ms`);
  return {
    nodes,
    edges,
    maxDepth,
    durationMs,
    layoutMs,
    css: "var(--muted)",
    text: parts.join(" · "),
  };
}

// barFor is the parameter bar: one control per parameter the query
// declares, present only when it declares any.
//
// `marked` is the unbound highlight. It is on the control and carries
// the server's own sentence, because the place that can fix an unbound
// parameter is the field, and the sentence is still the server's.
export function barFor(declarations, values, options = {}) {
  const declared = Array.isArray(declarations) ? declarations : [];
  const bound = values && typeof values === "object" ? values : {};
  const unbound = new Map();
  for (const entry of Array.isArray(options.unbound) ? options.unbound : []) {
    if (entry && typeof entry.param === "string") unbound.set(entry.param, entry);
  }
  const controls = declared
    .filter((decl) => decl && typeof decl.key === "string")
    .map((decl) => {
      const marked = unbound.get(decl.key) || null;
      return {
        key: decl.key,
        type: text(decl.type) || "text",
        options: Array.isArray(decl.options) ? decl.options.slice() : null,
        value: Object.prototype.hasOwnProperty.call(bound, decl.key) ? bound[decl.key] : null,
        bound: Object.prototype.hasOwnProperty.call(bound, decl.key),
        marked: marked !== null,
        message: marked ? text(marked.message) : "",
      };
    });
  return { present: controls.length > 0, controls };
}

// --- The frame -------------------------------------------------------

// frameFor is the whole model, and the one function the component reads.
//
// Exactly one `kind` comes back, and each of the four is produced by a
// condition no other kind can meet: a refusal with rows is diagnostics,
// a refusal with only unbound parameters is unbound, a clean run with
// nothing in it is empty, and everything else is a picture.
//
// The empty case is a **success** and looks like one: the same frame,
// the same footer, one sentence and what ran. No danger colour, no
// warning, and **no guess at the cause** — the envelope does not carry
// how many entities were considered, so "0 of 340 matched" is a number
// the interface does not have, and "try widening your filter" is advice
// generated from no information.
export function frameFor(input = {}) {
  const view = input.view && typeof input.view === "object" ? input.view : {};
  const envelope = input.envelope && typeof input.envelope === "object" ? input.envelope : null;
  const error = input.error && typeof input.error === "object" ? input.error : null;
  const params = input.params && typeof input.params === "object" ? input.params : {};
  const declarations = Array.isArray(input.declarations) ? input.declarations : [];

  const title = {
    name: text(view.name),
    key: text(view.key),
    renderer: text(view.renderer),
    renamed: envelope ? renamedFor(envelope) : null,
  };

  if (error) {
    const { rows, unbound } = diagnosticsFor(error);
    const bar = barFor(declarations, params, { unbound });
    // A refusal whose every problem is an unbound parameter is answered
    // by the bar alone: no panel, no pointer, no repair.
    if (rows.length === 0 && unbound.length > 0) {
      return {
        kind: KIND_UNBOUND,
        title,
        bar,
        banners: [],
        diagnostics: null,
        unbound,
        actions: [],
        message: "",
        ran: null,
        footer: footerFor(null, input),
      };
    }
    return {
      kind: KIND_DIAGNOSTICS,
      title,
      bar,
      banners: [],
      diagnostics: { css: "var(--danger)", rows },
      unbound,
      // Run anyway is offered for staleness and for nothing else: best
      // effort prunes what the *game* moved out from under a saved
      // document, and offering it for a refusal it cannot answer would
      // be a button that fails the same way twice.
      actions:
        text(error.code) === CODE_QUERY_STALE
          ? [{ code: ACTION_RUN_BEST_EFFORT, onStale: ON_STALE_BEST_EFFORT }]
          : [],
      message: "",
      ran: null,
      footer: footerFor(null, input),
    };
  }

  const banners = bannersFor(envelope, input);
  const footer = footerFor(envelope, input);
  const bar = barFor(declarations, params, {});
  const empty =
    footer.nodes === 0 &&
    footer.edges === 0 &&
    banners.length === 0 &&
    (Array.isArray(envelope && envelope.stale) ? envelope.stale.length === 0 : true);

  return {
    kind: empty ? KIND_EMPTY : KIND_PICTURE,
    title,
    bar,
    banners,
    diagnostics: null,
    unbound: [],
    actions: [],
    message: empty ? "This view matched nothing." : "",
    ran: empty ? ranFor(view, params, input.sets) : null,
    footer,
  };
}

// ranFor is what the empty state says instead of a cause: the renderer,
// the sets the query names and the parameter values in force. Every one
// of those is a thing the interface knows for certain.
function ranFor(view, params, sets) {
  const bound = Object.keys(params)
    .sort()
    .map((key) => ({ key, value: params[key] }));
  return {
    renderer: text(view.renderer),
    sets: Array.isArray(sets) ? sets.map((s) => text(s)) : [],
    params: bound,
    css: "var(--muted)",
  };
}

// --- Small shared shapes ---------------------------------------------

function text(value) {
  return typeof value === "string" ? value : "";
}

function countOf(value) {
  return typeof value === "number" && Number.isFinite(value) ? Math.trunc(value) : 0;
}

// count writes a number beside the noun it counts, in the right number.
// One node is not "1 nodes", and a frame that says so reads like a
// machine talking to itself.
function count(n, one, many) {
  return `${n} ${n === 1 ? one : many}`;
}
