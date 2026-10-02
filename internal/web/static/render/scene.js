// The view frame's model: a pure function from what the server said to
// the plain data the frame draws. No DOM, no fetch, no state, and one
// import — the text twin's model, because the twin is the accessible
// content of *every* view (spec §8.1) and a frame that could be built
// without one is a frame that eventually is.

import { countLabel } from "../rows.js";
import { fillFor } from "../palette.js";
import { t } from "../i18n.js";
import { twinFor } from "./twin.js";

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
// A containment cycle: A contains B contains A. It is `nested`'s, it is
// data the metamodel permits, and it is the one band whose rows **name
// the entities** — §4.5 is explicit that both ends are named, because a
// nest that stopped at the repeat without saying where would have drawn
// a plausible tree over a graph that is not one. That is not the same
// refusal `layered`'s cycle sentence makes: there, naming a cycle means
// choosing *which* cycle out of a whole graph, which is an analysis
// nobody ran; here the repeat is the two boxes the drawing itself just
// stopped between.
export const BANNER_CONTAINMENT_CYCLE = "containment_cycle";
// A span whose end is before its start. It is **data and not a drawing
// fault** — a quest declared from level 40 to level 10 is a content
// defect a designer wants to know about — so the picture draws a
// zero-length mark with a caret rather than sorting the two ends, and
// this band names the entities. The second band whose rows name things,
// for BANNER_CONTAINMENT_CYCLE's reason: the drawing stopped at two
// specific values of one specific node, and a band that said only "this
// view has an inverted span" would leave a designer to find it.
export const BANNER_INVERTED_SPAN = "inverted_span";
// The shelf: `map`'s nodes with no coordinate at all. It is a band and
// not only a strip of chips because a node that is not on the map is a
// node a designer scanning the map cannot see is missing — spec §4.2
// asks for the count in words, and the shelf is the same refusal drawn.
export const BANNER_SHELVED = "shelved";
// `timeline`'s own version of the same refusal: a node whose axis field
// found nothing, or whose value is not on the axis this view declares.
// It is a second band rather than a clause on the shelf's because the
// two say different things about different pictures, and it names the
// field, which is the one thing a designer can act on.
export const BANNER_OFF_AXIS = "off_axis";
// A `map` whose background asset is gone. `background_asset_id` is
// ON DELETE SET NULL, so this is an ordinary transition between two runs
// rather than a fault, and §4.6 asks for exactly one line about it —
// with the coordinates explicitly untouched, because positions are per
// view and were never anchored to the image.
export const BANNER_BACKGROUND_MISSING = "background_missing";

// The order the stack is drawn in, fixed rather than incidental: a
// designer learns positions, and two of these routinely co-occur — a
// best-effort picture is very often also truncated. The three truncation
// codes sit together and in flag order (nodes, edges, depth) because
// they are three sentences about one cap family, never one summary.
export const BANNER_ORDER = [
  BANNER_STALE,
  BANNER_DROPPED,
  BANNER_TRUNCATED_NODES,
  BANNER_TRUNCATED_EDGES,
  BANNER_TRUNCATED_DEPTH,
  BANNER_PLACED,
  BANNER_SHELVED,
  BANNER_OFF_AXIS,
  BANNER_BACKGROUND_MISSING,
  BANNER_AMBIGUOUS,
  BANNER_CONTAINMENT_CYCLE,
  BANNER_INVERTED_SPAN,
];

// The four kinds of thing the frame can be showing. They are four and
// not a set of booleans because they are mutually exclusive answers to
// "what is under the title strip", and a caller that has to combine
// flags to find out is a caller that will combine them wrongly.
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
      text: t(dropped.length === 1 ? "picture.dropped.one" : "picture.dropped.many", { count: dropped.length }),
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
        text: t(stale.length === 1 ? "picture.stale.one" : "picture.stale.many", { count: stale.length }),
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
    // `orphans` is the caller's, for `outside`'s reason: a node whose
    // container is not in the picture is discovered by joining the node
    // list to the edge list, which the renderer does and the envelope
    // cannot. It is a **clause on this band** rather than a band of its
    // own because it is not a second fact — it is what the cap did to
    // the drawing, and §4.5 asks for exactly that: "counted in the
    // truncation banner".
    const orphans = countOf(options.orphans);
    banners.push({
      code: BANNER_TRUNCATED_NODES,
      css: "var(--muted)",
      text:
        t("picture.truncatedNodes") +
        (orphans > 0
          ? t(orphans === 1 ? "picture.orphanBoxes.one" : "picture.orphanBoxes.many", { count: orphans })
          : ""),
      rows: [],
    });
  }
  if (truncated.edges === true) {
    banners.push({
      code: BANNER_TRUNCATED_EDGES,
      css: "var(--muted)",
      text: t("picture.truncatedEdges"),
      rows: [],
    });
  }
  if (truncated.depth === true) {
    banners.push({
      code: BANNER_TRUNCATED_DEPTH,
      css: "var(--muted)",
      text: t("picture.truncatedDepth"),
      rows: [],
    });
  }

  if (placed > 0) {
    banners.push({
      code: BANNER_PLACED,
      css: "var(--muted)",
      text: t(placed === 1 ? "picture.placed.one" : "picture.placed.many", { count: placed }),
      rows: [],
    });
  }

  // The shelf, and the ground under it. Both are `map`'s and both are
  // the caller's to report, for `outside`'s reason: whether a node has a
  // coordinate is a question about a *drawing* — a declared field this
  // renderer reads, or a saved arrangement — and the envelope answers
  // neither. A count of zero says nothing, and `background` absent from
  // the options is not the same as a background that was removed: a
  // renderer that never had one has nothing to report.
  const shelved = countOf(options.shelved);
  if (shelved > 0) {
    banners.push({
      code: BANNER_SHELVED,
      css: "var(--muted)",
      text: t(shelved === 1 ? "picture.shelved.one" : "picture.shelved.many", { count: shelved }),
      rows: [],
    });
  }
  // `timeline`'s unplaceable nodes. The field is named because it is the
  // recovery: a designer told that three nodes have no value can look at
  // three nodes, and a designer told *which field* can go and fill it
  // in. It is the caller's, for the shelf's reason — whether a node has
  // a value for this view's axis is a question about a drawing.
  const offAxis = options.offAxis && typeof options.offAxis === "object" ? options.offAxis : null;
  const offAxisCount = offAxis ? countOf(offAxis.count) : 0;
  if (offAxisCount > 0) {
    banners.push({
      code: BANNER_OFF_AXIS,
      css: "var(--muted)",
      text: t(offAxisCount === 1 ? "picture.offAxis.one" : "picture.offAxis.many", {
        count: offAxisCount,
        field: text(offAxis.field) === "" ? "" : ` (${text(offAxis.field)})`,
      }),
      rows: [],
    });
  }

  if (options.backgroundMissing === true) {
    banners.push({
      code: BANNER_BACKGROUND_MISSING,
      css: "var(--muted)",
      // Two sentences, because the second is the one a designer needs:
      // an image that vanished looks exactly like an arrangement that
      // vanished with it, and it did not.
      text: t("picture.backgroundMissing"),
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
      text: t(ambiguous === 1 ? "picture.ambiguous.one" : "picture.ambiguous.many", { count: ambiguous }),
      rows: [],
    });
  }

  // The containment cycle, last, because it is the only band that names
  // entities and a reader who has met the others learns its position.
  const cycles = Array.isArray(options.containmentCycles) ? options.containmentCycles : [];
  if (cycles.length > 0) {
    banners.push({
      code: BANNER_CONTAINMENT_CYCLE,
      css: "var(--muted)",
      text: t(cycles.length === 1 ? "picture.containmentCycle.one" : "picture.containmentCycle.many", { count: cycles.length }),
      rows: cycles.map((cycle) => ({
        code: BANNER_CONTAINMENT_CYCLE,
        // The two ends, as the labels the picture drew, so the band and
        // the boxes say the same words about the same entities.
        inner: text(cycle && cycle.inner),
        outer: text(cycle && cycle.outer),
        message: t("picture.containmentCycle.row", { outer: text(cycle && cycle.outer), inner: text(cycle && cycle.inner) }),
        css: "var(--muted)",
      })),
    });
  }

  // The inverted spans, last, beside the containment cycle for the same
  // reason: they are the two bands whose rows name entities, and a
  // reader who has met one has learned where to look for the other.
  const inverted = Array.isArray(options.invertedSpans) ? options.invertedSpans : [];
  if (inverted.length > 0) {
    banners.push({
      code: BANNER_INVERTED_SPAN,
      css: "var(--muted)",
      text: t(inverted.length === 1 ? "picture.invertedSpan.one" : "picture.invertedSpan.many", { count: inverted.length }),
      rows: inverted.map((span) => ({
        code: BANNER_INVERTED_SPAN,
        name: text(span && span.name),
        from: text(span && span.from),
        to: text(span && span.to),
        message: t("picture.invertedSpan.row", { name: text(span && span.name), from: text(span && span.from), to: text(span && span.to) }),
        css: "var(--muted)",
      })),
    });
  }

  return banners;
}

// staleRefsOf is the envelope's non-rename diagnostics, as rows.
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
    text: t(rows.length === 1 ? "picture.renamed.one" : "picture.renamed.many", { count: rows.length }),
    rows,
  };
}

function isRenamed(code) {
  return code === DIAG_ENTITY_TYPE_RENAMED || code === DIAG_RELATION_TYPE_RENAMED;
}

// --- The refusal -----------------------------------------------------

// diagnosticsFor turns a refusal into the panel that replaces the
// picture, plus the unbound parameters the parameter bar answers.
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
export function runAction(action, client, options = {}) {
  if (!action || action.code !== ACTION_RUN_BEST_EFFORT) return null;
  if (!client || typeof client.runView !== "function") return null;
  return client.runView(text(options.key), options.params || {}, {
    onStale: ON_STALE_BEST_EFFORT,
  });
}

// --- The strips ------------------------------------------------------

// footerFor is the strip that is always present, on every kind.
export function footerFor(envelope, options = {}) {
  const layoutMs = options.layoutMs === undefined || options.layoutMs === null
    ? null
    : countOf(options.layoutMs);
  const outside = options.outside === undefined || options.outside === null
    ? null
    : countOf(options.outside);
  // `against` and `cyclic` are `layered`'s pair, and they are two
  // numbers rather than one sentence because they are two statements
  // that come apart. An edge running against the ranking is a **drawing**
  // fact: with `rank_by` naming a number field, a relation from level 5
  // to level 2 runs backwards up the picture and the graph is perfectly
  // acyclic. A cycle is a fact about the answer, found by the traversal
  // that had to break one to rank at all. §4.4 writes them as one
  // sentence — *"3 edges run against the ranking; this graph has a
  // cycle"* — and that sentence is only true when both hold, so the
  // clause is attached to the flag rather than to the count.
  const against = options.against === undefined || options.against === null
    ? null
    : countOf(options.against);
  const cyclic = options.cyclic === true;
  // A refused run measured nothing. The strip stays — it is always
  // present — and it says nothing, because "0 nodes, 0 edges" beside a
  // refusal reads exactly like an answer that matched nothing, which is
  // a different thing that also happens here and must not be confused
  // with it. Absent, not zero, for the same reason execute.go refuses to
  // return an unplaced node at the origin.
  if (!envelope || typeof envelope !== "object") {
    return {
      nodes: null,
      edges: null,
      maxDepth: null,
      durationMs: null,
      layoutMs,
      outside,
      against,
      cyclic,
      css: "var(--muted)",
      text: "",
    };
  }
  const stats = envelope.stats && typeof envelope.stats === "object" ? envelope.stats : {};
  const nodes = countOf(stats.nodes);
  const edges = countOf(stats.edges);
  const maxDepth = countOf(stats.max_depth_reached);
  const durationMs = countOf(stats.duration_ms);
  const parts = [
    `${countLabel(nodes, t("unit.node"), t("unit.nodes"))}, ${countLabel(edges, t("unit.edge"), t("unit.edges"))}`,
    t("picture.stats.depth", { depth: maxDepth }),
    t("picture.stats.query", { ms: durationMs }),
  ];
  if (layoutMs !== null) parts.push(t("picture.stats.layout", { ms: layoutMs }));
  if (outside !== null && outside > 0) {
    // "1 edge leads", not "1 edge lead": the verb agrees with the count
    // the same way the noun does. `count` writes the noun, so the verb
    // travels with it — a frame that says "1 edge lead outside this
    // picture" reads like a machine talking to itself, which is this
    // module's own argument for counting in words at all.
    parts.push(t(outside === 1 ? "picture.stats.outside.one" : "picture.stats.outside.many", { count: outside }));
  }
  if (against !== null && against > 0) {
    parts.push(
      t(against === 1 ? "picture.stats.against.one" : "picture.stats.against.many", { count: against }) +
        (cyclic ? t("picture.stats.cyclic") : ""),
    );
  }
  return {
    nodes,
    edges,
    maxDepth,
    durationMs,
    layoutMs,
    outside,
    against,
    cyclic,
    css: "var(--muted)",
    text: parts.join(" · "),
  };
}

// barFor is the parameter bar: one control per parameter the query
// declares, present only when it declares any.
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
        // **The label is the key, read out.** The query language has no
        // name for a parameter — a declaration is a key, a type and a
        // default — so the bar labelled its controls `class_key`, which
        // is an identifier a designer never chose shown at the size of a
        // question. Adding a name to the language is the other answer
        // and is a change to a stored document, so the frame does what
        // this product already does for a relation type: the words for a
        // reader, the key in mono beside them.
        label: deslug(decl.key),
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

// deslug turns `class_key` into "Class key": the key's own words, in
// sentence case, because that is what the agent that wrote the query
// actually called it and this is the closest a reader gets to being told
// what to type. It is never a translation — a key of `k` stays "K",
// which is honest about how little the query said.
export function deslug(key) {
  const words = String(key ?? "").split(/[_\-.]+/).filter((word) => word !== "");
  if (words.length === 0) return String(key ?? "");
  const said = words.join(" ");
  return said.charAt(0).toUpperCase() + said.slice(1);
}

// legendModel is the colour key: the rows a renderer's `legend` already
// carries, paired with the paint each one wears.
export function legendModel(legend) {
  const rows = legend && Array.isArray(legend.rows) ? legend.rows : [];
  return {
    present: rows.length > 0,
    rows: rows.map((row) => ({
      kind: text(row.kind),
      label: text(row.label),
      count: Number.isFinite(row.count) ? row.count : 0,
      // The tail says how many values it swallowed in its own label
      // (palette.js writes it), so this carries no second sentence.
      index: fillFor(row).index,
    })),
  };
}

// --- The frame -------------------------------------------------------

// frameFor is the whole model, and the one function the component reads.
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
        twin: null,
        title,
        bar,
        banners: [],
        diagnostics: null,
        unbound,
        actions: [],
        message: "",
        ran: null,
        legend: { present: false, rows: [] },
        footer: footerFor(null, input),
      };
    }
    return {
      kind: KIND_DIAGNOSTICS,
      twin: null,
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
      legend: { present: false, rows: [] },
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
    // Every answer has a twin, and the empty answer's is the empty one:
    // "the accessible content of every view" is a claim the frame makes
    // structurally rather than a thing each renderer remembers to do. A
    // refusal has none because there is no answer to describe — the two
    // refusing kinds above set it to null, and a picture never can.
    twin: twinFor(envelope),
    title,
    bar,
    banners,
    diagnostics: null,
    unbound: [],
    actions: [],
    message: empty ? "This view matched nothing." : "",
    ran: empty ? ranFor(view, params, input.sets) : null,
    // The colour key, from the scene the page just drew. A refusal has
    // none for the same reason it has no twin: there is no answer, and a
    // key over a panel of diagnostics would read as an answer's key.
    legend: empty ? { present: false, rows: [] } : legendModel(input.legend),
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

// --- The scene: marks, layers, and the emitter's contract ------------
export const SVG_NS = "http://www.w3.org/2000/svg";

// The five mark kinds, and the element each becomes. Five and not six:
// a `path` mark has no producer today, and this plan's own rule is that
// a mechanism nothing reads is a lie. It also has no honest answer for
// the drag layer, which reshapes an edge whose one endpoint moved by
// rewriting its endpoints — a rule `d` cannot express — so the renderer
// that needs a curved edge adds the kind *and* that answer together, in
// a diff a human reads.
export const MARK_RECT = "rect";
export const MARK_DISC = "disc";
export const MARK_LINE = "line";
export const MARK_LABEL = "label";
export const MARK_IMAGE = "image";

export const MARK_ELEMENTS = {
  [MARK_RECT]: "rect",
  [MARK_DISC]: "circle",
  [MARK_LINE]: "line",
  [MARK_LABEL]: "text",
  [MARK_IMAGE]: "image",
};

// The five layers, in paint order, which is **document order** in SVG:
// later siblings paint over earlier ones. This is a correctness property
// and not a nicety — a label under its own node is a label nobody can
// read, and a dragged node under the graph it is being dragged through
// is a drag a designer loses track of.
export const LAYER_IMAGE = "image";
export const LAYER_EDGES = "edges";
export const LAYER_NODES = "nodes";
export const LAYER_LABELS = "labels";
export const LAYER_DRAG = "drag";
export const LAYER_ORDER = [LAYER_IMAGE, LAYER_EDGES, LAYER_NODES, LAYER_LABELS, LAYER_DRAG];

// Where a mark goes when it does not say. A mark **may** name a layer —
// a decorative disc belongs under the edges, a chip belongs over them —
// but it may never name the drag layer: that one belongs to the canvas,
// which empties it on every drop, and a mark parked there would vanish
// the first time somebody moved a node.
export const DEFAULT_LAYER = {
  [MARK_RECT]: LAYER_NODES,
  [MARK_DISC]: LAYER_NODES,
  [MARK_LINE]: LAYER_EDGES,
  [MARK_LABEL]: LAYER_LABELS,
  [MARK_IMAGE]: LAYER_IMAGE,
};
export const PLACEABLE_LAYERS = LAYER_ORDER.filter((layer) => layer !== LAYER_DRAG);

// The attributes every kind carries, and the one addressing attribute.
export const COMMON_ATTRIBUTES = {
  key: "data-key",
  class: "class",
};

// One map per kind, from the mark's field to the SVG attribute it
// becomes. This is the whole of the emitter's contract: **a field not
// named here does not reach the DOM.** That is what keeps the emitter
// dumb without making it a passthrough — a passthrough is how `onload`,
// `href` and `style` arrive on an element built from data.
export const MARK_ATTRIBUTES = {
  [MARK_RECT]: {
    x: "x",
    y: "y",
    w: "width",
    h: "height",
    radius: "rx",
    fill: "fill",
    stroke: "stroke",
    strokeWidth: "stroke-width",
    dash: "stroke-dasharray",
    opacity: "opacity",
  },
  [MARK_DISC]: {
    cx: "cx",
    cy: "cy",
    r: "r",
    fill: "fill",
    stroke: "stroke",
    strokeWidth: "stroke-width",
    dash: "stroke-dasharray",
    opacity: "opacity",
  },
  [MARK_LINE]: {
    x1: "x1",
    y1: "y1",
    x2: "x2",
    y2: "y2",
    stroke: "stroke",
    strokeWidth: "stroke-width",
    dash: "stroke-dasharray",
    opacity: "opacity",
  },
  [MARK_LABEL]: {
    x: "x",
    y: "y",
    fill: "fill",
    size: "font-size",
    anchor: "text-anchor",
    baseline: "dominant-baseline",
    halo: "stroke",
    haloWidth: "stroke-width",
    opacity: "opacity",
  },
  [MARK_IMAGE]: {
    x: "x",
    y: "y",
    w: "width",
    h: "height",
    href: "href",
    opacity: "opacity",
    fit: "preserveAspectRatio",
  },
};

// The positional attributes of each kind, as origin pairs. The drag
// layer translates a mark by adding to each pair; a kind with no pair
// here cannot be dragged, which is a property of the kind and not of the
// caller's memory.
export const MARK_ORIGINS = {
  [MARK_RECT]: [["x", "y"]],
  [MARK_DISC]: [["cx", "cy"]],
  [MARK_LINE]: [["x1", "y1"], ["x2", "y2"]],
  [MARK_LABEL]: [["x", "y"]],
  [MARK_IMAGE]: [["x", "y"]],
};

// isDrawableHref is the one URL rule this front end has, and it lives
// beside the one attribute that fetches.
export function offscreen(scene, view, size) {
  const marks = Array.isArray(scene) ? scene : Array.isArray(scene && scene.marks) ? scene.marks : [];
  const width = size && Number.isFinite(size.width) ? size.width : 0;
  const height = size && Number.isFinite(size.height) ? size.height : 0;
  const at = view && typeof view === "object" ? view : { x: 0, y: 0, k: 1 };
  const k = Number.isFinite(at.k) && at.k > 0 ? at.k : 1;
  let total = 0;
  let hidden = 0;
  for (const mark of marks) {
    // Nodes only: an edge is shown by its endpoints and a label by the
    // thing it names, so counting those would report one missing node
    // three times.
    if (!mark || mark.kind !== MARK_RECT || typeof mark.key !== "string" || mark.key === "") continue;
    total += 1;
    if (width <= 0 || height <= 0) continue;
    const left = mark.x * k + (at.x ?? 0);
    const top = mark.y * k + (at.y ?? 0);
    const right = left + (mark.w ?? 0) * k;
    const bottom = top + (mark.h ?? 0) * k;
    if (right < 0 || bottom < 0 || left > width || top > height) hidden += 1;
  }
  return { total, hidden };
}

export function isDrawableHref(value) {
  if (typeof value !== "string" || value.length < 2) return false;
  if (!value.startsWith("/") || value.startsWith("//")) return false;
  return !/[:\s\\]/.test(value);
}

// --- Joining edges to nodes ------------------------------------------

// Which end of an edge was not in the picture.
export const ENDPOINT_SOURCE = "source";
export const ENDPOINT_TARGET = "target";
export const ENDPOINT_BOTH = "both";

// joinEdges is the one place the rule lives.
export function joinEdges(nodes, edges) {
  const byID = new Map();
  for (const node of Array.isArray(nodes) ? nodes : []) {
    if (!node || typeof node !== "object") continue;
    if (typeof node.id !== "string" || node.id === "") continue;
    if (!byID.has(node.id)) byID.set(node.id, node);
  }

  const drawn = [];
  const stubs = [];
  for (const edge of Array.isArray(edges) ? edges : []) {
    if (!edge || typeof edge !== "object") continue;
    const source = byID.get(edge.source) ?? null;
    const target = byID.get(edge.target) ?? null;
    if (source !== null && target !== null) {
      drawn.push({ edge, source, target });
      continue;
    }
    const missing =
      source === null && target === null
        ? ENDPOINT_BOTH
        : source === null
          ? ENDPOINT_SOURCE
          : ENDPOINT_TARGET;
    stubs.push({ edge, source, target, missing });
  }
  return { drawn, stubs };
}
