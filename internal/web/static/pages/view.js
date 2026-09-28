// One saved view, drawn.

import {
  BAND,
  GONE,
  PARAM_PREFIX,
  REREAD,
  TARGET_PICTURE,
  TARGET_VIEW,
  client as makeClient,
  readParams,
  typeParams,
  writeParams,
} from "../client.js";
import { TOO_MUCH, roundTrips } from "../query/compose.js";
import { frameFor } from "../render/scene.js";
import { graphLayoutRequest, graphScene, RENDERER as RENDERER_GRAPH } from "../render/graph.js";
import { layeredLayoutRequest, layeredScene, RENDERER as RENDERER_LAYERED } from "../render/layered.js";
import { nestedScene, RENDERER as RENDERER_NESTED } from "../render/nested.js";
import {
  COORDINATES_FIELDS,
  mapScene,
  PARAM_COORDINATE_SOURCE,
  RENDERER as RENDERER_MAP,
} from "../render/map.js";
import { tableScene, RENDERER as RENDERER_TABLE } from "../render/table.js";
import { timelineScene, PARAM_AXIS_FIELD, RENDERER as RENDERER_TIMELINE } from "../render/timeline.js";
import { compose, gridFallback } from "../layout/compose.js";
import { storedFrom } from "../positions.js";
import { LAYOUT_BUDGET_MS, runWithBudget } from "../layout/budget.js";
import { Arrangement, MstCanvas, worldDelta } from "../components/mst-canvas.js";
import { MstGround } from "../components/mst-ground.js";
import {
  ACTION_CANCEL as SAVE_AS_CANCEL,
  ACTION_OPEN as SAVE_AS_OPEN,
  ACTION_SAVE as SAVE_AS_SAVE,
  FIELD_DEFAULT,
  FIELD_KEY,
  FIELD_PARAM,
  FIELD_RENDERER,
  MstSaveAs,
} from "../components/mst-save-as.js";
import { SELECT_EVENT } from "../components/mst-twin.js";
import "../components/mst-view-frame.js";
import "../components/mst-table.js";
import { entityBody, readEntity } from "./entity.js";
import {
  DESTINATION_VIEWS,
  countLabel,
  destinations,
  entityURL,
  expired,
  gameURL,
  openGame,
  say,
  segmentsOf,
  setBreadcrumb,
  builderURL,
  viewsURL,
} from "./page.js";
import { goToLogin } from "../app.js";

// The worker's URL, absolute from the instance root. A module worker
// resolves its own imports relative to itself, which is why every import
// under layout/ is a relative path and none is a bare specifier: a
// worker has no import map.
export const WORKER_URL = "/static/layout/worker.js";

// The band a *failed* layout wears, and the one thing on this page that
// is neither the server's sentence nor the frame's.
export const BANNER_LAYOUT_FAILED = "layout_failed";
export const LAYOUT_FAILED_HEADING = "The layout engine failed; nodes are arranged in a grid.";

// The two notices the stream produces that no other surface owns.
export const NOTICE_QUERY_CHANGED =
  "Somebody changed this view's query. What is on screen is the answer to the old one; reload to run the new one.";
export const NOTICE_VIEW_REMOVED = "This view has been deleted. What is on screen is the last answer it gave.";
export const NOTICES = {
  "query.changed": NOTICE_QUERY_CHANGED,
  "view.removed": NOTICE_VIEW_REMOVED,
};

// The renderers that need a layout before they can be drawn, and the
// request each builds. `nested`, `map`, `table` and `timeline` place
// their own marks — a tree packs itself, a map reads coordinates the
// game or the designer wrote, a table is rows and a timeline is an axis
// — so asking dagre for any of them would be 48 kB of graph algorithm
// spent on an answer nobody reads.
const LAYOUT_REQUESTS = {
  [RENDERER_GRAPH]: graphLayoutRequest,
  [RENDERER_LAYERED]: layeredLayoutRequest,
};

// One entry per renderer, from a scene to what the canvas draws. The
// table is the odd one and says so: it is the only renderer of the six
// whose scene is not marks, so it is painted by `mst-table` rather than
// emitted into the SVG surface.
const SCENES = {
  [RENDERER_GRAPH]: (envelope, layout, params) => graphScene(envelope, layout, params),
  [RENDERER_LAYERED]: (envelope, layout, params) => layeredScene(envelope, layout, params),
  [RENDERER_NESTED]: (envelope, layout, params, options) => nestedScene(envelope, params, options),
  [RENDERER_MAP]: (envelope, layout, params, options) =>
    mapScene(envelope, params, { ...options, layout: mapComposition(envelope, options.row, params) }),
  [RENDERER_TIMELINE]: (envelope, layout, params, options) => timelineScene(envelope, params, options),
  [RENDERER_TABLE]: (envelope, layout, params, options) => tableScene(envelope, params, options),
};

// mapComposition is the composition a `manual` map reads, and it exists
// because the map renderer was never given one.
export function mapComposition(envelope, row, params = {}) {
  if (params && params[PARAM_COORDINATE_SOURCE] === COORDINATES_FIELDS) return null;
  const nodes = (Array.isArray(envelope && envelope.nodes) ? envelope.nodes : [])
    .filter((node) => node && typeof node === "object" && typeof node.key === "string" && node.key !== "")
    .map((node) => ({ type: typeof node.type === "string" ? node.type : "", key: node.key }));
  if (nodes.length === 0) return null;
  return compose(
    row && typeof row === "object" ? row.layout_mode : undefined,
    { placements: gridFallback(nodes) },
    storedFrom(envelope && envelope.positions),
  );
}

// --- Reading the address ---------------------------------------------

// viewKeyOf is the key in /g/{slug}/v/{key}. It is a **key** and never an
// id, on this route as on every other one in this product.
export function viewKeyOf(pathname) {
  return segmentsOf(pathname)[1] || "";
}

// declarationsOf is the query document's own parameter declarations,
// which is what turns the text a URL carries into the values a run
// needs. A query that declares none has an empty bar and no binding.
export function declarationsOf(row) {
  const query = row && typeof row === "object" ? row.query : null;
  if (!query || typeof query !== "object") return [];
  return Array.isArray(query.params) ? query.params : [];
}

// setsOf is what the empty state names instead of guessing a cause: the
// seed sets this query draws from, by the name the query gave them.
export function setsOf(row) {
  const query = row && typeof row === "object" ? row.query : null;
  if (!query || typeof query !== "object") return [];
  const from = Array.isArray(query.from) ? query.from : [];
  return from.map((selector) => String((selector && (selector.as || selector.type)) || ""));
}

// backgroundOf is what the `map` renderer needs to draw a ground: the
// view's three background columns joined to the asset's own pixel size.
// **Its presence is this page's statement that the view names a ground**
// — an href that cannot be drawn is then a background that is *gone*,
// which the renderer bands, and only the caller can tell that apart from
// a view that never had one.
export function backgroundOf(row, asset) {
  const from = row && typeof row === "object" ? row : {};
  if (typeof from.background_asset_id !== "string" || from.background_asset_id === "") return null;
  const offset = from.background_offset && typeof from.background_offset === "object" ? from.background_offset : {};
  const image = asset && typeof asset === "object" ? asset : {};
  return {
    href: typeof image.url === "string" ? image.url : "",
    scale: Number.isFinite(from.background_scale) && from.background_scale > 0 ? from.background_scale : 1,
    offset: { x: Number(offset.x ?? 0), y: Number(offset.y ?? 0) },
    width: Number(image.width ?? 0),
    height: Number(image.height ?? 0),
  };
}

// backgroundAssetFor resolves a view's `background_asset_id` to the asset
// row that carries the URL and the pixel size, which is what
// backgroundOf needs and what the view row does not have.
export async function backgroundAssetFor(client, row, previous) {
  const id = row && typeof row === "object" ? row.background_asset_id : null;
  if (typeof id !== "string" || id === "") return null;
  if (previous && previous.id === id) return previous;
  if (!client || typeof client.listAssets !== "function") return null;
  let cursor = "";
  // Bounded, because a listing that never ends is a page that never
  // draws: a ground a designer cannot find in the first few hundred
  // images is answered as absent, which the renderer already bands.
  for (let page = 0; page < 10; page += 1) {
    const answer = await client.listAssets(cursor === "" ? {} : { cursor });
    const body = answer && answer.ok === false ? null : answer && answer.result ? answer.result : answer;
    const assets = body && Array.isArray(body.assets) ? body.assets : [];
    for (const asset of assets) {
      if (asset && asset.id === id) return asset;
    }
    cursor = body && typeof body.next_cursor === "string" ? body.next_cursor : "";
    if (cursor === "") return null;
  }
  return null;
}

// typeKeysOf is every entity type a query puts in scope: the types it
// selects from and the types it traverses to. It is what a page has to
// walk to find a field's declaration, because a declaration belongs to a
// type and an envelope carries values rather than types.
export function typeKeysOf(row) {
  const query = row && typeof row === "object" ? row.query : null;
  if (!query || typeof query !== "object") return [];
  const keys = [];
  const add = (value) => {
    if (typeof value === "string" && value !== "" && !keys.includes(value)) keys.push(value);
  };
  for (const selector of Array.isArray(query.from) ? query.from : []) {
    if (selector && typeof selector === "object") add(selector.type);
  }
  for (const step of Array.isArray(query.traverse) ? query.traverse : []) {
    if (step && typeof step === "object") add(step.to_type);
  }
  return keys;
}

// axisDeclarationFor is the `{type, options}` the `timeline` renderer
// lays its axis out from.
export async function axisDeclarationFor(client, row, params, previous) {
  const view = row && typeof row === "object" ? row : {};
  if (String(view.renderer ?? "") !== RENDERER_TIMELINE) return null;
  const field = params && typeof params === "object" ? params[PARAM_AXIS_FIELD] : null;
  if (typeof field !== "string" || field === "") return null;
  if (previous && previous.key === field) return previous;
  if (!client || typeof client.getType !== "function") return null;
  for (const typeKey of typeKeysOf(view)) {
    const answer = await client.getType(typeKey);
    const body = answer && answer.ok === false ? null : answer && answer.result ? answer.result : answer;
    const schema = body && Array.isArray(body.field_schema) ? body.field_schema : [];
    for (const declared of schema) {
      if (!declared || declared.key !== field) continue;
      return {
        key: field,
        type: String(declared.type ?? ""),
        options: Array.isArray(declared.options) ? declared.options : [],
      };
    }
  }
  return null;
}

// --- The layout ------------------------------------------------------

// layoutWith runs one attempt in a worker against budget.js's deadline.
export async function layoutWith(request, options = {}) {
  const makeWorker = options.makeWorker || ((url) => new Worker(url, { type: "module" }));
  const budgetMs = Number.isFinite(options.budgetMs) ? options.budgetMs : LAYOUT_BUDGET_MS;
  const worker = makeWorker(WORKER_URL);
  const answered = new Promise((resolve, reject) => {
    worker.addEventListener("message", (event) => {
      const data = event && event.data ? event.data : {};
      if (data.ok) resolve(data.layout);
      else reject(new Error(String(data.error ?? "")));
    });
    worker.addEventListener("error", (event) => reject(new Error(String((event && event.message) ?? ""))));
  });
  const send = () => {
    worker.postMessage(request);
    return answered;
  };
  try {
    const result = await runWithBudget({
      run: send,
      budgetMs,
      cancel: () => worker.terminate(),
      fallback: () => ({ placements: gridFallback(request.nodes || []) }),
    });
    if (result.ok) worker.terminate();
    return result;
  } catch (error) {
    worker.terminate();
    return {
      ok: false,
      layout: { placements: gridFallback(request.nodes || []) },
      elapsedMs: 0,
      budgetMs,
      banner: {
        code: BANNER_LAYOUT_FAILED,
        css: "var(--muted)",
        text: LAYOUT_FAILED_HEADING + " " + String(error && error.message ? error.message : ""),
        rows: [],
      },
      // No retry: a longer budget for an exception is a longer wait for
      // the same throw.
      retry: null,
    };
  }
}

// --- The page --------------------------------------------------------

export async function viewPage(opened, options = {}) {
  const doc = opened.document;
  const errorEl = doc.getElementById("view-error");
  const rootEl = doc.getElementById("view-root");
  const panelEl = doc.getElementById("entity-panel");

  if (opened.game === null) {
    say(errorEl, opened.failure ?? "You may not have access to this game, or it no longer exists.");
    return null;
  }
  setBreadcrumb(doc, [
    { label: opened.game.name, href: gameURL(opened.slug) },
    { label: DESTINATION_VIEWS, href: viewsURL(opened.slug) },
  ]);
  // The last crumb and the tab's name, once the view is read. The trail
  // ended at "Views" — a link to somewhere else — so the one screen the
  // product exists for was the only one that never said where you were,
  // and four view tabs were four tabs called "View · Maestro".

  const key = viewKeyOf(opened.location.pathname);
  if (key === "") {
    say(errorEl, "This address names no view.");
    return null;
  }

  const client = opened.client;
  // The caller's own role, for the page's *words* and never for a
  // permission: every refusal is still the server's, made again on the
  // next request. What it decides here is whether the arrangement menu
  // offers a viewer the one button the server will not honour, which is
  // worse than a menu that explains.
  const [row, summary] = await Promise.all([client.readView(key), client.summary()]);
  if (!row.ok) {
    if (expired(row)) {
      goToLogin();
      return null;
    }
    // The one thing this page says for itself. A view that does not
    // exist is neither an envelope nor a refused run, so the frame has
    // nothing to be built from and would render as a blank strip.
    say(errorEl, row.error.message);
    return null;
  }

  // The last crumb and the tab's name, now that the view has one. The
  // trail ended at "Views" — a link to somewhere else — so the one screen
  // the product exists for was the only one never saying where you were,
  // and four view tabs were four tabs called "View · Maestro".
  const viewName = String(row.result.name || row.result.key || key);
  setBreadcrumb(doc, [
    { label: opened.game.name, href: gameURL(opened.slug) },
    { label: DESTINATION_VIEWS, href: viewsURL(opened.slug) },
    { label: viewName },
  ]);
  doc.title = viewName + " \u00b7 Maestro";
  // The page head, at Display, where every other screen in the product
  // puts the name of the thing being looked at.
  say(doc.getElementById("view-title"), viewName);

  // **The builder's door, and it only exists when the builder can hold
  // what is behind it.** §4 of the builder spike: the builder generates
  // and never edits, so it opens a stored query only if that document
  // round-trips through it unchanged — and when it does not, the product
  // says so in words rather than offering a control that would drop a
  // clause. The check is against the stored bytes, here, every time.
  sayBuilderDoor(doc, opened.slug, row.result);

  const surface = mount(doc, rootEl, opened.slug, key, row.result, client, options);
  surface.role = summary.ok ? String(summary.result.role ?? "") : "";
  // Before the first run, so a narrow window never draws a picture it is
  // about to take away.
  watchWidth(surface, options);
  await surface.run();
  wire(doc, panelEl, opened.slug, client, surface);
  client.connect((verdict) => surface.receive(verdict));
  return surface;
}

// sayBuilderDoor puts either the way into the builder or the reason
// there is none in the line under the view's own name.
export function sayBuilderDoor(doc, slug, row) {
  const el = doc.getElementById("view-builder");
  if (!el) return null;
  const query = row && row.query !== undefined ? row.query : null;
  if (query === null) {
    el.hidden = true;
    return null;
  }
  if (!roundTrips(JSON.stringify(query))) {
    el.textContent = TOO_MUCH;
    el.className = "muted";
    el.hidden = false;
    return null;
  }
  const link = doc.createElement("a");
  link.className = "quiet";
  link.href = builderURL(slug) + "?from=" + encodeURIComponent(String(row.key ?? ""));
  link.textContent = OPEN_IN_BUILDER;
  el.replaceChildren(link);
  el.hidden = false;
  el.className = "";
  return link;
}

// OPEN_IN_BUILDER says what it opens and, by being a link rather than a
// button, says it is a place: the builder has an address, and a middle
// click belongs to the reader.
export const OPEN_IN_BUILDER = "Open in the builder";

// mount builds the frame, the canvas, the table painter and the ground
// panel, and returns the one object every wired event calls into.
function mount(doc, rootEl, slug, key, row, client, options) {
  const frame = doc.createElement("mst-view-frame");
  const canvas = options.canvas || new MstCanvas({ document: doc });
  const table = doc.createElement("mst-table");
  const ground = options.ground || new MstGround({ document: doc, client, canvas, viewKey: key });
  // The "Save as" dialog is mounted into its own hole in the shell and
  // **not** into the frame: the frame's drawing wrapper is aria-hidden
  // because the twin is the accessible content of the answer, and a
  // button a keyboard can reach inside it is a button a screen reader
  // will not announce.
  const saveAs = options.saveAs
    || new MstSaveAs({
      document: doc,
      client,
      slug,
      row,
      params: readParams(options.search ?? globalThis.window.location.search),
    });
  mountSaveAs(doc, saveAs);

  frame.client = client;
  frame.append(canvas);
  frame.append(table);
  rootEl.replaceChildren(frame, ground);

  const state = {
    key,
    row,
    slug,
    client,
    canvas,
    frame,
    table,
    ground,
    saveAs,
    arrangement: null,
    scene: null,
    // Whether the renderer put a drawing in the slot, which is not the
    // same question as whether the window is wide enough to show one.
    pictured: false,
    narrow: false,
    // The bound parameters, as text, straight off the URL. They are
    // typed against the query's declarations only at the moment of the
    // run, because a URL carries no types and guessing that `20` is the
    // number twenty would send a number to a text parameter.
    params: readParams(options.search ?? globalThis.window.location.search),
  };

  state.noticeEl = doc.getElementById("view-error");
  // The fallback's own hole, separate from #view-error: a stream notice
  // and "this window is too narrow" are two facts, and one element
  // holding both would show whichever arrived last.
  state.narrowEl = doc.getElementById("view-narrow");
  // What the canvas is not showing, said in words under the picture.
  state.outsideEl = doc.getElementById("view-outside");
  state.run = async () => run(state, options);
  state.redraw = (envelope) => draw(state, envelope, null, options);
  state.receive = (verdict) => receive(state, verdict);
  return state;
}

async function run(state, options) {
  const declarations = declarationsOf(state.row);
  const typed = typeParams(declarations, state.params);
  const answer = await state.client.runView(state.key, typed, {});
  return draw(state, answer.ok ? answer.result : null, answer.ok ? null : answer.error, options);
}

// draw is the whole picture: the layout, the renderer's scene, the
// frame's model and the canvas.
// draw is a picture followed, always, by the width the window actually
// has.
async function draw(state, envelope, error, options) {
  try {
    return await drawPicture(state, envelope, error, options);
  } finally {
    applyWidth(state, state.narrow === true);
    // **The fit comes after the width, and that is the bug this line
    // closes.** It used to be the last thing inside `drawPicture`, which
    // runs *before* this `finally` — and `watchWidth` applies the width
    // once at mount, when `state.pictured` is still false, so the canvas
    // is `hidden` for the whole of the first draw. A hidden element
    // measures 0x0, `fitView` rightly answers null twice, and `fitOnce`
    // reports false: the view stayed at the origin at 1x on every first
    // load, and a 105-node graph drew 48 of its nodes outside a 1392x571
    // clip with no scrollbar and no notice. Everything was correct in
    // the module and dead at the call site, again, and only a browser
    // could see it — measured on `marks-check/v/palette`, where calling
    // `fitOnce` by hand afterwards brought all 105 into view.
    await fitAfterLayout(state, options);
  }
}

// fitAfterLayout is the fit, once per view, now that the canvas is
// whatever size the window has decided. A narrow window draws no
// picture at all, so there is nothing to fit and nothing to mark done:
// widening it redraws, and the fit happens then.
export async function fitAfterLayout(state, options = {}) {
  if (state.fitted || state.narrow === true || state.pictured !== true) return;
  const scene = state.scene;
  const marks = scene && Array.isArray(scene.marks) ? scene.marks : null;
  if (marks === null) return;
  state.fitted = await fitOnce(state.canvas, marks, { frame: state.frame, settle: options.settle });
  sayOutside(state);
}

// sayOutside is the admission beside the fit: how much of the answer is
// off the canvas right now. A fitted view says nothing, which is the
// common case and the quiet one.
export function sayOutside(state) {
  const canvas = state.canvas;
  if (!canvas || typeof canvas.outside !== "function") return "";
  const counted = canvas.outside();
  const text = counted.hidden === 0
    ? ""
    : counted.hidden + " of " + countLabel(counted.total, "node", "nodes") + " are outside the view.";
  say(state.outsideEl, text);
  return text;
}

async function drawPicture(state, envelope, error, options) {
  state.envelope = envelope;
  const declarations = declarationsOf(state.row);
  const params = state.row.renderer_params && typeof state.row.renderer_params === "object"
    ? state.row.renderer_params
    : {};
  const renderer = String(state.row.renderer ?? "");

  let layout = null;
  let layoutMs = null;
  if (envelope !== null && LAYOUT_REQUESTS[renderer]) {
    const request = LAYOUT_REQUESTS[renderer](envelope, params);
    const result = await layoutWith(
      {
        id: state.key,
        mode: state.row.layout_mode,
        nodes: request.nodes,
        edges: request.edges,
        positions: envelope.positions,
      },
      options,
    );
    layout = result.layout;
    layoutMs = Math.round(result.elapsedMs);
    // The canvas places the band and the retry, which is Task 6's
    // decision and Task 7's mechanism; this page only hands it the
    // answer, whether that answer is a timeout or a throw.
    state.canvas.setLayoutResult(result);
  }

  // The ground the `map` renderer draws over, resolved before the scene
  // is composed. See backgroundAssetFor: the view row carries the
  // reference and only the asset carries the URL and the pixel size, and
  // until Task 15 mounted a map nothing in this page joined the two.
  state.background = await backgroundAssetFor(state.client, state.row, state.background);

  // The axis the `timeline` renderer draws, resolved the same way and
  // for the same reason as the ground above: it is a *declaration* the
  // game holds, not a value the answer carries. See axisDeclarationFor.
  state.axis = await axisDeclarationFor(state.client, state.row, params, state.axis);

  const scene = envelope === null ? null : SCENES[renderer]
    ? SCENES[renderer](envelope, layout, params, {
        background: backgroundOf(state.row, state.background),
        axis: state.axis,
        zoom: state.canvas.view.k,
        // The saved row, for the one renderer that has to compose its own
        // placements — see mapComposition. Every other SCENES entry
        // ignores it.
        row: state.row,
      })
    : null;
  state.scene = scene;

  state.frame.frame = frameFor({
    view: state.row,
    envelope,
    error,
    params: typeParams(declarations, state.params),
    declarations,
    sets: setsOf(state.row),
    layoutMs,
    outside: scene && scene.stubs ? scene.stubs.total : null,
    against: scene && scene.against !== undefined ? scene.against : null,
    cyclic: scene ? scene.cyclic === true : false,
    placedAutomatically: scene && scene.automatic ? scene.automatic : 0,
    // The renderer's own colour key, on its way to the screen for the
    // first time. Four of the six renderers have returned one since
    // Tasks 8-13 and nothing read it; see scene.js's legendModel.
    legend: scene && scene.legend ? scene.legend : null,
  });
  state.frame.params = state.params;

  if (scene === null) {
    state.table.hidden = true;
    return state;
  }
  if (renderer === RENDERER_TABLE) {
    state.table.hidden = false;
    state.table.table = scene;
    // What the renderer wants on screen, which `applyWidth` then
    // intersects with what the window is wide enough for.
    state.pictured = false;
    return state;
  }
  state.table.hidden = true;
  state.pictured = true;
  state.canvas.draw({ marks: scene.marks });

  // The arrangement is rebuilt on every draw, from the coordinates the
  // composition actually used: an arrangement holding the previous
  // picture's nodes would move things that are no longer on screen.
  state.arrangement = new Arrangement({
    canvas: state.canvas,
    client: state.client,
    viewKey: state.key,
    row: state.row,
    mode: state.row.layout_mode,
    snap: Number(params.snap ?? 0),
    role: state.role ?? "",
    nodes: nodesOfScene(scene, layout),
  });
  state.canvas.showArrangement(state.arrangement);

  // The fit is *not* here. It needs a canvas the window has already
  // decided to show, and that decision is `applyWidth`, which runs in
  // `draw`'s finally — after this function returns. See fitAfterLayout.
  return state;
}

// --- Below tablet width, the twin is the view ------------------------

// The width a view page stops drawing at (spec §9: "layouts respond
// down to tablet width; below it, view pages fall back to the text twin
// and the writing interactions are disabled rather than shrunk").
export const NARROW_QUERY = "(max-width: 47.99rem)";

// What the page says while it is not drawing.
export const NOTICE_TOO_NARROW =
  "This window is too narrow to draw the picture, so this view is shown as its " +
  "table below. The table is the same answer. Arranging the picture is off " +
  "until the window is wider.";

// **A renderer that draws no picture takes nothing away at a narrow
// width**, so the sentence above would be describing a loss that did not
// happen: on a `table` view at 500px it read "too narrow to draw the
// picture" over the same table it had drawn at 1440. A view with no
// drawing to lose says nothing at all.
export function narrowNotice(state) {
  return state && state.pictured === true ? NOTICE_TOO_NARROW : "";
}

// applyWidth is the fallback, and it is two halves.
export function applyWidth(state, narrow) {
  const fell = narrow === true;
  state.narrow = fell;
  // What the renderer wanted, intersected with what the window allows.
  // Written on every application rather than only when narrow, so a
  // window widened back gets its drawing without waiting for a redraw.
  if (state.canvas) state.canvas.hidden = fell || state.pictured !== true;
  if (state.ground) {
    // A placement in flight is cancelled and not committed: cancelling
    // writes nothing, which is what makes the mode safe to leave.
    if (fell && typeof state.ground.cancel === "function") state.ground.cancel();
    state.ground.hidden = fell;
  }
  if (state.arrangement && typeof state.arrangement.setDrawn === "function") {
    state.arrangement.setDrawn(!fell);
    // The menu is rebuilt so the sentence explaining the refusal is
    // there for whoever widens the window and looks.
    if (state.canvas && typeof state.canvas.showArrangement === "function") {
      state.canvas.showArrangement(state.arrangement);
    }
  }
  say(state.narrowEl, fell ? narrowNotice(state) : "");
  return fell;
}

// watchWidth binds the fallback to the viewport and applies it once.
export function watchWidth(state, options = {}) {
  const media = options.media
    || (typeof globalThis.matchMedia === "function" ? globalThis.matchMedia(NARROW_QUERY) : null);
  if (!media) return null;
  state.media = media;
  const apply = () => applyWidth(state, media.matches === true);
  if (typeof media.addEventListener === "function") media.addEventListener("change", apply);
  apply();
  return media;
}

// FIT_MARGIN is the space left around a picture that has just been fitted
// to the window, as a fraction of the drawing's own size. A picture drawn
// hard against the edges reads as a picture that has been cut off.
export const FIT_MARGIN = 0.04;

// The zoom a fit will not exceed. Fitting a two-node answer to a
// thousand-pixel window would draw two enormous boxes and say nothing
// about the game.
export const FIT_MAX_ZOOM = 1;

// boundsOf is the extent of a scene, in the game's own coordinates. It
// reads the marks rather than the layout, because a scene carries marks
// the layout never placed — a shelf chip, an axis tick, a background —
// and a fit that ignored them would cut them off.
export function boundsOf(marks) {
  let minX = Infinity;
  let minY = Infinity;
  let maxX = -Infinity;
  let maxY = -Infinity;
  const span = (x, y, w, h) => {
    if (!Number.isFinite(x) || !Number.isFinite(y)) return;
    minX = Math.min(minX, x);
    minY = Math.min(minY, y);
    maxX = Math.max(maxX, x + (Number.isFinite(w) ? w : 0));
    maxY = Math.max(maxY, y + (Number.isFinite(h) ? h : 0));
  };
  for (const mark of Array.isArray(marks) ? marks : []) {
    if (Number.isFinite(mark.cx)) span(mark.cx - (mark.r || 0), mark.cy - (mark.r || 0), 2 * (mark.r || 0), 2 * (mark.r || 0));
    else if (Number.isFinite(mark.x1)) {
      span(Math.min(mark.x1, mark.x2), Math.min(mark.y1, mark.y2), Math.abs(mark.x2 - mark.x1), Math.abs(mark.y2 - mark.y1));
    } else span(mark.x, mark.y, mark.w, mark.h);
  }
  if (!Number.isFinite(minX) || !Number.isFinite(minY)) return null;
  return { x: minX, y: minY, width: Math.max(1, maxX - minX), height: Math.max(1, maxY - minY) };
}

// fitView is the pan and zoom that puts a whole answer on the screen.
export function fitView(bounds, width, height) {
  if (!bounds || !(width > 0) || !(height > 0)) return null;
  const margin = 1 + 2 * FIT_MARGIN;
  const k = Math.min(FIT_MAX_ZOOM, width / (bounds.width * margin), height / (bounds.height * margin));
  return {
    k,
    x: width / 2 - k * (bounds.x + bounds.width / 2),
    y: height / 2 - k * (bounds.y + bounds.height / 2),
  };
}

// settleLayout waits for the frame to have rendered and the browser to
// have laid it out, which is the difference between measuring a canvas
// and measuring nothing. The frame is a Lit element: on the first draw
// of a page its shadow DOM — the positioned, 70vh-tall box the canvas
// fills — has been *requested* and not yet produced, so a
// getBoundingClientRect taken in the same turn answers 0x0.
async function settleLayout(frame) {
  if (frame && frame.updateComplete && typeof frame.updateComplete.then === "function") {
    await frame.updateComplete;
  }
  if (typeof globalThis.requestAnimationFrame === "function") {
    await new Promise((resolve) => globalThis.requestAnimationFrame(() => resolve()));
  }
}

// fitOnce fits a canvas to a scene and **answers whether it did**.
export async function fitOnce(canvas, marks, options = {}) {
  const bounds = boundsOf(marks);
  if (!bounds || !canvas || typeof canvas.setView !== "function") return false;
  const settle = options.settle || settleLayout;
  for (let attempt = 0; attempt < 2; attempt += 1) {
    const box = typeof canvas.getBoundingClientRect === "function" ? canvas.getBoundingClientRect() : null;
    const fitted = fitView(bounds, box ? box.width : 0, box ? box.height : 0);
    if (fitted) {
      canvas.setView(fitted);
      return true;
    }
    if (attempt === 0) await settle(options.frame);
  }
  return false;
}

// nodesOfScene is the model the arrangement moves: one record per node,
// in the game's own coordinates. It reads the layout where there is one
// and the map's own pins where there is not, because those are the two
// places a coordinate comes from.
function nodesOfScene(scene, layout) {
  const placements = layout && Array.isArray(layout.placements) ? layout.placements : [];
  const nodes = [];
  for (const placement of placements) {
    const address = JSON.parse(placement.key);
    nodes.push({ address: placement.key, type: address[0], key: address[1], x: placement.x, y: placement.y });
  }
  if (nodes.length > 0) return nodes;
  for (const mark of Array.isArray(scene.marks) ? scene.marks : []) {
    if (typeof mark.key !== "string" || mark.key === "") continue;
    if (!Number.isFinite(mark.cx) && !Number.isFinite(mark.x)) continue;
    const address = JSON.parse(mark.key);
    nodes.push({
      address: mark.key,
      type: address[0],
      key: address[1],
      x: Number.isFinite(mark.cx) ? mark.cx : mark.x,
      y: Number.isFinite(mark.cy) ? mark.cy : mark.y,
    });
  }
  return nodes;
}

// --- The events Task 14's controllers were waiting for ----------------

// wire is where a pointer, a key and a button reach the two controllers
// Task 14 built and nothing drove.
export const DRAG_THRESHOLD_PX = 3;

export function wire(doc, panelEl, slug, client, state) {
  const canvas = state.canvas;
  const surface = canvas.shell.surfaceHost;
  let press = null;

  surface.addEventListener("pointerdown", (event) => {
    const address = addressAt(event.target);
    press = { address, at: { x: event.clientX, y: event.clientY }, dragging: false, panning: false };
  });

  surface.addEventListener("pointermove", (event) => {
    if (press === null) return;
    const moved = Math.abs(event.clientX - press.at.x) + Math.abs(event.clientY - press.at.y);
    if (!press.dragging) {
      if (press.address === null || moved < DRAG_THRESHOLD_PX) return;
      press.dragging = state.arrangement.pointerDown(press.address, press.at) !== null;
      if (!press.dragging) return;
    }
    state.arrangement.pointerMove({ x: event.clientX, y: event.clientY });
  });

  surface.addEventListener("pointerup", async (event) => {
    const held = press;
    press = null;
    if (held === null) return;
    if (held.dragging) {
      await state.arrangement.pointerUp();
      canvas.showArrangement(state.arrangement);
      return;
    }
    // A press that never moved: the node is selected and its panel
    // opens over the canvas. The canvas stays mounted — losing an
    // arrangement to read a field would make reading fields expensive.
    // A press that panned the view is not a click either: it began on
    // nothing and ended somewhere else.
    if (held.address === null || held.panning === true) return;
    state.arrangement.select(held.address, { shift: event.shiftKey === true });
    await openPanel(doc, panelEl, slug, client, held.address);
  });

  // The twin's selection, arriving here.
  state.frame.addEventListener(SELECT_EVENT, (event) => {
    const node = event && event.detail ? event.detail : null;
    if (!node || typeof node.type !== "string" || typeof node.key !== "string") return;
    state.arrangement.select(JSON.stringify([node.type, node.key]));
    canvas.showArrangement(state.arrangement);
  });

  // The keyboard is the same gesture as the mouse and goes through the
  // same commit: one grid cell, or one pixel where there is no grid.
  surface.setAttribute("tabindex", "0");
  surface.addEventListener("keydown", async (event) => {
    if (event.key === "z" && (event.ctrlKey || event.metaKey)) {
      event.preventDefault();
      await state.arrangement.undo();
      canvas.showArrangement(state.arrangement);
      return;
    }
    const step = ARROWS[event.key];
    if (!step) return;
    event.preventDefault();
    await state.arrangement.nudge(step[0], step[1]);
    canvas.showArrangement(state.arrangement);
  });

  // The arrangement menu. Delegated on the panel host, which survives a
  // redraw, so a menu rebuilt after a write is still wired.
  let armed = null;
  const disarm = () => {
    if (armed === null) return;
    clearTimeout(armed.timer);
    if (armed.button.isConnected !== false) {
      armed.button.textContent = armed.label;
      armed.button.removeAttribute("data-armed");
    }
    armed = null;
  };
  canvas.shell.panels.addEventListener("click", async (event) => {
    const button = event.target;
    const action = button && button.getAttribute ? button.getAttribute("data-action") : null;
    if (!action) return;
    const run = MENU_ACTIONS[action];
    if (!run) return;
    const destructive = button.getAttribute("data-destructive") === "true";
    if (destructive && (armed === null || armed.button !== button)) {
      disarm();
      armed = { button, label: button.textContent, timer: setTimeout(disarm, 4000) };
      button.textContent = button.textContent + " — click again";
      button.dataset.armed = "true";
      return;
    }
    disarm();
    await run(state.arrangement);
    canvas.showArrangement(state.arrangement);
  });

  // The "Save as" dialog, delegated on its own root for the same reason,
  // and wired here rather than inside the component for the reason every
  // other controller in this page is: a component that bound its own
  // listeners could not be driven by a harness without a browser.
  wireSaveAs(state.saveAs);

  // The ground panel, delegated on its own root for the same reason.
  state.ground.root.addEventListener("change", (event) => {
    const files = event.target && event.target.files ? event.target.files : null;
    state.ground.choose(files && files.length > 0 ? files[0] : null);
  });
  state.ground.root.addEventListener("click", async (event) => {
    const action = event.target && event.target.getAttribute ? event.target.getAttribute("data-action") : null;
    if (!action) return;
    await GROUND_ACTIONS[action](state.ground);
  });

  // Adjust-ground moves the image and holds the nodes still, which is
  // the whole of it being a mode: a designer aligning an image to a
  // graph is moving one of the two things.
  surface.addEventListener("pointermove", (event) => {
    if (!state.ground.placing || press === null) return;
    const moved = worldDelta(event.movementX ?? 0, event.movementY ?? 0, canvas.view.k);
    state.ground.moveBy(moved.dx * canvas.view.k, moved.dy * canvas.view.k);
  });

  // Pan and zoom, the canvas's own two transforms.
  surface.addEventListener("wheel", (event) => {
    if (!event.ctrlKey && !event.metaKey) return;
    event.preventDefault();
    const factor = event.deltaY < 0 ? ZOOM_STEP : 1 / ZOOM_STEP;
    canvas.zoomTo(canvas.view.k * factor);
    sayOutside(state);
  });

  // **Dragging empty canvas moves the picture.** `panBy` has existed
  // since the canvas was written and nothing called it: a diagram bigger
  // than its box could not be moved at all, by any gesture, and the only
  // reason that was survivable is that the fit above now starts every
  // view showing the whole answer. A reader who zooms in still needs to
  // get to the rest of it.
  surface.addEventListener("pointermove", (event) => {
    if (press === null || press.address !== null || press.dragging) return;
    if (state.ground && state.ground.placing) return;
    const moved = Math.abs(event.clientX - press.at.x) + Math.abs(event.clientY - press.at.y);
    if (!press.panning && moved < DRAG_THRESHOLD_PX) return;
    press.panning = true;
    canvas.panBy(event.movementX ?? 0, event.movementY ?? 0);
    sayOutside(state);
  });

  // A hidden tab is work nobody is looking at, and the client defers a
  // re-read while it is true.
  if (doc.addEventListener) {
    doc.addEventListener("visibilitychange", () => client.setHidden(doc.hidden === true));
  }
}

// mountSaveAs puts the dialog in the shell's own hole, which is
// **outside** the frame.
export function mountSaveAs(doc, saveAs) {
  const root = doc.getElementById("save-as-root");
  if (!root || !saveAs) return null;
  // The **element**, not its panel: the panel lives in the dialog's own
  // shadow root, where the adopted stylesheet is, and mounting the panel
  // alone would put an unstyled tree in the page and leave the shadow
  // root — and its styles — attached to nothing.
  root.replaceChildren(saveAs);
  return root;
}

// wireSaveAs is the dialog's three buttons and its four kinds of field.
export function wireSaveAs(saveAs) {
  if (!saveAs || !saveAs.root || typeof saveAs.root.addEventListener !== "function") return null;
  saveAs.root.addEventListener("click", async (event) => {
    const action = event.target && event.target.getAttribute ? event.target.getAttribute("data-action") : null;
    if (action === SAVE_AS_OPEN) await saveAs.show();
    else if (action === SAVE_AS_SAVE) await saveAs.save();
    else if (action === SAVE_AS_CANCEL) saveAs.hide();
  });
  const edit = (event) => {
    const target = event.target;
    if (!target || typeof target.getAttribute !== "function") return;
    const field = target.getAttribute("data-field");
    const value = typeof target.value === "string" ? target.value : "";
    if (field === FIELD_KEY) saveAs.setKey(value);
    else if (field === FIELD_RENDERER) saveAs.chooseRenderer(value);
    else if (field === FIELD_PARAM) saveAs.setParam(target.getAttribute("data-param"), value);
    else if (field === FIELD_DEFAULT) saveAs.setBinding(target.getAttribute("data-param"), value);
  };
  saveAs.root.addEventListener("input", edit);
  saveAs.root.addEventListener("change", edit);
  return saveAs;
}

const ARROWS = {
  ArrowLeft: [-1, 0],
  ArrowRight: [1, 0],
  ArrowUp: [0, -1],
  ArrowDown: [0, 1],
};

const ZOOM_STEP = 1.1;

// The menu's four actions, by the id the menu itself put on the button.
// A table rather than a switch so that an action the menu offers and
// this page cannot perform is a missing key a test can find.
const MENU_ACTIONS = {
  "switch-mode": (arrangement) => arrangement.switchToMixed(),
  unpin: (arrangement) => arrangement.unpin(),
  clear: (arrangement) => arrangement.clearPositions(),
  undo: (arrangement) => arrangement.undo(),
};

// The ground panel's six, the same way.
const GROUND_ACTIONS = {
  upload: (ground) => ground.upload(),
  adjust: (ground) => ground.adjust(),
  commit: (ground) => ground.commit(),
  cancel: (ground) => ground.cancel(),
  clear: (ground) => ground.clear({}),
  "confirm-clear": (ground) => ground.clear({ confirmed: true }),
};

// addressAt reads the entity address off the element a pointer landed
// on. `data-key` is the emitter's one addressing attribute
// (render/scene.js's COMMON_ATTRIBUTES) and it carries `(type, key)` and
// never an id.
function addressAt(target) {
  let node = target;
  while (node && typeof node.getAttribute === "function") {
    const key = node.getAttribute("data-key");
    if (typeof key === "string" && key !== "") return key;
    node = node.parentNode;
  }
  return null;
}

// openPanel is the entity page, over the canvas, one click from the full
// page. It reads the entity and never runs a view: a panel that ran a
// query to show a field would make reading a field cost a diagram.
export async function openPanel(doc, panelEl, slug, client, address) {
  if (!panelEl) return null;
  const [typeKey, key] = JSON.parse(address);
  const model = await readEntity(client, typeKey, key);
  if (!model.ok) {
    panelEl.replaceChildren();
    const message = doc.createElement("p");
    message.className = "error";
    message.textContent = model.error.message;
    panelEl.append(message);
    panelEl.hidden = false;
    return panelEl;
  }
  const heading = doc.createElement("h2");
  heading.textContent = model.entity.name || model.entity.key;
  const full = doc.createElement("a");
  full.href = entityURL(slug, typeKey, key);
  full.textContent = "Open the full page";
  panelEl.replaceChildren(heading, full, entityBody(doc, slug, model));
  panelEl.hidden = false;
  return panelEl;
}

// receive acts on one decision the stream produced.
function receive(state, verdict) {
  if (verdict.decision === REREAD && verdict.target === TARGET_VIEW) {
    return state.client.readView(state.key).then((answer) => {
      if (answer.ok) state.row = answer.result;
      return answer;
    });
  }
  if (verdict.decision === BAND || verdict.decision === GONE) {
    const notice = NOTICES[verdict.reason];
    if (notice) say(state.noticeEl, notice);
    return notice ?? null;
  }
  return null;
}

if (globalThis.document && globalThis.document.getElementById("view-root")) {
  const opened = await openGame({ destination: DESTINATION_VIEWS });
  if (opened !== null) {
    const doc = opened.document;
    if (opened.game === null) {
      await viewPage(opened);
    } else {
      // The client is rebuilt with the answer seam once the slug is
      // known, because that is the earliest moment either can exist.
      // **Every run comes back through it**, the page's own and the
      // coalesced re-read the client makes on its own timer alike, so a
      // stream event redraws the picture instead of quietly updating a
      // field nobody reads.
      let surface = null;
      opened.client = makeClient({
        slug: opened.slug,
        onAnswer: (key, envelope) => (surface !== null && key === surface.key ? surface.redraw(envelope) : null),
      });
      surface = await viewPage(opened);
    }
  }
}

// PARAM_PREFIX and writeParams are re-exported so the harness and the
// "Save as" dialog (Task 16) read the one spelling of a bound parameter
// rather than restating it.
export { PARAM_PREFIX, writeParams, TARGET_PICTURE };
