// One saved view, drawn.
//
// This is the page every task from 4 to 14 was building for, and it
// mounts rather than decides: the frame owns every negative state
// (Task 4), the twin is the accessible content of the answer (Task 5),
// the budget owns giving up on a layout (Task 6), the canvas owns the
// drawing, the pan, the zoom and the drag layer (Task 7), the six
// renderers own what a picture looks like (Tasks 8–13), and the
// arrangement and the ground panel own the two writes (Task 14).
//
// **What is new here is that any of it happens at all.** Task 14's
// controllers were driven by a harness and by nothing else — no pointer
// reached `Arrangement.pointerDown`, no key reached `nudge`, no button
// reached `MstGround.commit`. Every one of them is wired to a real event
// below, which is what turns a tested mechanism into a product.
//
// **A view is a URL.** The key is a path segment and every bound
// parameter is a `p.` pair in the query string, so a parameterised view
// is a link a colleague can be sent and a designer can edit in the
// address bar. client.js's `readParams`/`writeParams` spell both ends of
// that, so the bar, the link and the address agree by construction.
//
// **The layout runs in a worker and the page is where a rejection
// lands.** budget.js supervises the deadline and the canvas places the
// band; the *throw* was left to "the page that calls the worker", which
// is this one — a worker that failed is not a worker that was slow, and
// offering a longer budget for an exception offers to wait longer for
// the same throw.

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
  destinations,
  entityURL,
  expired,
  gameURL,
  openGame,
  say,
  segmentsOf,
  setBreadcrumb,
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
//
// budget.js deliberately declines this path: its own sentence explains a
// deadline, and dressing an exception as one would offer a longer wait
// for the same throw. So the band carries the engine's own message,
// unmodified, exactly as a refusal carries the server's — this page
// composes the heading and never the reason.
export const BANNER_LAYOUT_FAILED = "layout_failed";
export const LAYOUT_FAILED_HEADING = "The layout engine failed; nodes are arranged in a grid.";

// The two notices the stream produces that no other surface owns.
//
// The frame's model is built from an envelope or a refusal, and neither
// of these is either: "somebody changed this view's query" and "somebody
// deleted this view" are facts about the *stream*, and Task 4's rule —
// a page invents no negative state — is about the three states of an
// answer. These are the page's, they are two sentences, and the picture
// is left exactly as it is under both: **nothing is swapped under a
// designer and nothing is auto-navigated**, for any role.
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
//
// **The branch it feeds had been dead on the wire since Task 11.**
// render/map.js's own header promises that on a map which *has* a saved
// arrangement, a node new to it "is placed by the client, drawn with a
// hollow anchor, and counted — *12 new nodes were placed
// automatically*". `coordinatesFor` reads that from `options.layout`,
// LAYOUT_REQUESTS above names only `graph` and `layered`, and the SCENES
// entry for `map` used to pass `{background, axis, zoom}` and no layout
// at all — so `placementsOf(options.layout)` was empty on every real
// page and the exception could not fire. render_map_test.mjs passed a
// composition by hand and proved the module; nothing proved the call.
// Measured in a browser before the fix, on a map with four pinned rows
// and twelve quests added afterwards: twelve shelf chips, zero hollow
// anchors, and `scene.automatic` 0, so the band never appeared.
//
// It is composed here on the main thread rather than through the worker
// because a map is not a graph: nothing about it wants dagre. What a
// node with no stored row needs is *a* coordinate near the arrangement,
// which is exactly `gridFallback` fitted by `compose` against the pins.
//
// `fields` maps get none, and that is not an optimisation: there the
// game states every coordinate, `coordinatesFor` never looks at a
// composition, and handing one over would be a placement nothing reads.
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
//
// **Nothing did this, and the symptom was a background that silently was
// not there.** A view's three background columns are written by
// views.set_background and read by the `map` renderer, and this page —
// the only thing that mounts a renderer — passed `undefined` as the
// asset on every draw, so backgroundOf answered an entry with an empty
// href on every view that named a ground. An empty href is how this page
// spells "the image is gone", so a correctly placed background drew
// nothing and said nothing about it. Found by opening a map view with
// two hundred pins over an uploaded image (Task 15).
//
// It is resolved by paging the asset listing rather than by fetching the
// asset itself: `GET …/view-assets/{id}` answers the image *bytes*, and
// the width and height a renderer needs live only in the listing.
// `previous` short-circuits the walk, so a redraw of a view whose ground
// has not changed costs nothing.
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
//
// **Nothing supplied it, and an enum axis silently became a number
// one.** render/timeline.js's axisFor is explicit that the declaration
// "arrives from the caller because it is not in the envelope": the
// answer carries the values a query found and an axis is what the *type*
// says exists, which is how a declared option with no node still gets a
// tick. With no declaration axisFor has only a number axis to fall back
// on, and on a number axis every enum value is off-axis — so a
// championship over six declared stages drew four lanes, one tick
// reading "0", and all eighty-two of its events piled into the "no value
// for stage" region to the left of the origin. Nothing said so, because
// "this node has no value on this axis" is a picture the renderer draws
// on purpose. Found by opening a timeline (Task 15).
//
// The declaration is looked for across every type the query puts in
// scope and the first one found wins, which is sound because the
// renderer catalogue refuses to save a timeline whose axis_field is not
// declared identically by all of them (internal/views/renderers.go).
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
//
// The worker is created per attempt and terminated on the timeout path,
// because `terminate()` is the only thing that actually stops a
// synchronous dagre run. A **throw** is caught here and banded with the
// engine's own message: this is the path budget.js leaves to the page
// that calls the worker, and this is that page.
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
//
// The fallback is re-applied in a `finally` rather than at the three
// places `drawPicture` returns, and that is the whole reason this
// wrapper exists: `drawPicture` builds a *new* `Arrangement` on every
// draw and sets `canvas.hidden` from the renderer, so a redraw arriving
// while the window is narrow — a stream re-read, a parameter change —
// would put the drawing back and hand a designer a freshly armed
// writing path. Every exit from a draw goes through the fallback,
// including the ones that threw.
async function draw(state, envelope, error, options) {
  try {
    return await drawPicture(state, envelope, error, options);
  } finally {
    applyWidth(state, state.narrow === true);
  }
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

  // Fitted once, on the first drawing of this view, and *last*: the
  // measurement it needs is a laid-out canvas, and everything above this
  // line is what puts one on the screen.
  if (!state.fitted) {
    state.fitted = await fitOnce(state.canvas, scene.marks, { frame: state.frame, settle: options.settle });
  }
  return state;
}

// --- Below tablet width, the twin is the view ------------------------

// The width a view page stops drawing at (spec §9: "layouts respond
// down to tablet width; below it, view pages fall back to the text twin
// and the writing interactions are disabled rather than shrunk").
//
// 48rem and not 768px: the whole of what makes a picture usable at a
// width is how much text fits beside it, so a reader who has turned
// their font size up reaches the fallback sooner, which is the right
// direction. Measured before this existed, on the seeded thousand-quest
// instance at a 600px viewport: the canvas drew at 536px with every
// writing interaction live.
export const NARROW_QUERY = "(max-width: 47.99rem)";

// What the page says while it is not drawing.
//
// **The picture is not removed silently.** A canvas that vanished with
// no sentence would be indistinguishable from a view that answered
// nothing, which is the one thing this front end's negative states exist
// to keep apart. It is the page's own sentence and not the frame's: the
// frame speaks only its model's words, and "this window is narrow" is a
// fact about the window rather than about the answer.
export const NOTICE_TOO_NARROW =
  "This window is too narrow to draw the picture, so this view is shown as its " +
  "table below. The table is the same answer. Arranging the picture is off " +
  "until the window is wider.";

// applyWidth is the fallback, and it is two halves.
//
// The first half is that the twin becomes the content: the drawing goes,
// the ground panel goes with it (placing a background is an alignment
// against a picture, and there is no picture), and a sentence says so.
//
// **The second half is the one that matters, and it is why this is a
// function rather than a media query.** Hiding the canvas in CSS leaves
// `Arrangement` believing it may write: the twin is still on screen and
// still focusable, its rows still select nodes, and one arrow key would
// then write a position against a drawing nobody can see — the same
// half-truth `auto` mode refuses a drag for. `setDrawn(false)` disarms
// every write in that class, and jstest/writes_test.mjs fails if a
// hidden canvas still accepts a nudge.
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
  say(state.narrowEl, fell ? NOTICE_TOO_NARROW : "");
  return fell;
}

// watchWidth binds the fallback to the viewport and applies it once.
//
// The media query list is injectable for the harness's sake: a Node
// stub has no `matchMedia`, and a fallback that could only be driven by
// resizing a real window is a fallback with no test — which is how the
// step this closes went four tasks unbuilt in the first place.
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
//
// **A first view opens fitted, and that is a mounting decision this page
// owns.** A scene's coordinates are the game's, the canvas's view starts
// at the origin at 1x, and a five-hundred-node graph laid out from (0,0)
// to (12000, 9000) would open showing its top-left corner — a designer's
// first sight of their own game would be four boxes and a lot of ground.
// It is applied once, on the first draw of a view: every later draw keeps
// whatever the designer panned to, because moving a picture somebody is
// reading is the thing this whole sub-project refuses to do.
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
//
// The answer is the whole of why this is a function. The first version
// of this fit measured once and set `fitted = true` whatever came back,
// so on the first draw of a page — the only draw that was ever going to
// be fitted — it measured a canvas the frame had not laid out yet, got
// 0x0, computed no fit, and marked the view fitted anyway. The picture
// opened at the origin at 1x, which is exactly what the fit exists to
// prevent, and nothing anywhere said so: `fitView` returning null is
// indistinguishable from a fit that was not wanted. Found by mounting
// the first real view and reading the canvas's `view` afterwards.
//
// So: measure, and if the box has no size, let the frame lay out and
// measure again. If it still has none — a hidden tab, a canvas in a
// collapsed panel — the caller is told `false` and the *next* draw tries
// again, because a view that is never fitted is a bug and a view fitted
// on the second draw is a view a designer never saw unfitted.
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
//
// **A click is not a drag, and the difference is a threshold.** A drag
// starts on the first pointer movement past DRAG_THRESHOLD_PX and not on
// the button going down: `Arrangement.pointerUp` is the write, so a page
// that began a drag on every press would send a position write for every
// click that merely opened a panel.
export const DRAG_THRESHOLD_PX = 3;

export function wire(doc, panelEl, slug, client, state) {
  const canvas = state.canvas;
  const surface = canvas.shell.surfaceHost;
  let press = null;

  surface.addEventListener("pointerdown", (event) => {
    const address = addressAt(event.target);
    press = { address, at: { x: event.clientX, y: event.clientY }, dragging: false };
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
    if (held.address === null) return;
    state.arrangement.select(held.address, { shift: event.shiftKey === true });
    await openPanel(doc, panelEl, slug, client, held.address);
  });

  // The twin's selection, arriving here.
  //
  // **This listener is the fourth sighting of this sub-project's own
  // recurring defect and was found in a browser, not by a harness.**
  // mst-twin.js dispatches `mst-select` when a row takes focus, its
  // header says "the selection travels as a DOM event so the canvas can
  // highlight what the reader is standing on", and twin_test.mjs asserts
  // the event goes out "under the name a canvas will listen for" — and
  // nothing in the product listened for it. The consequence was the
  // whole keyboard path of spec §8.1: a reader could tab through the
  // twin and watch the rows mark themselves, then move to the canvas and
  // find the arrow keys moved nothing and wrote nothing, because
  // `Arrangement.selection` was still empty. Measured before the fix, on
  // the seeded game: focusing the *Wanted: Hogger* row set
  // `mst-twin.selected`, one ArrowRight on the focused surface left the
  // node's `x` at 287 and sent zero writes.
  //
  // It is bound on the frame element rather than on the twin: the event
  // is `composed`, so it crosses the frame's shadow boundary, and the
  // twin is rebuilt on every draw while the frame is not — a listener on
  // the twin would be a listener on whichever twin happened to exist
  // when the page was wired.
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
  canvas.shell.panels.addEventListener("click", async (event) => {
    const action = event.target && event.target.getAttribute ? event.target.getAttribute("data-action") : null;
    if (!action) return;
    const run = MENU_ACTIONS[action];
    if (!run) return;
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
    event.preventDefault();
    const factor = event.deltaY < 0 ? ZOOM_STEP : 1 / ZOOM_STEP;
    canvas.zoomTo(canvas.view.k * factor);
  });

  // A hidden tab is work nobody is looking at, and the client defers a
  // re-read while it is true.
  if (doc.addEventListener) {
    doc.addEventListener("visibilitychange", () => client.setHidden(doc.hidden === true));
  }
}

// mountSaveAs puts the dialog in the shell's own hole, which is
// **outside** the frame.
//
// It is a named function rather than three lines inside `mount` so that
// a harness can drive it, and it is separate from `wireSaveAs` because
// the two answer different questions: where the dialog is, and whether
// anything reaches it.
//
// Where it is, is the point. `mst-view-frame` wraps the drawing in an
// `aria-hidden` div because the text twin is the accessible content of
// the answer; anything appended to the frame is slotted into that
// wrapper, so a focusable control put there is one a keyboard can reach
// and a screen reader will never announce. The dialog therefore has a
// hole of its own in view.html and is never appended to the frame.
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
//
// It is exported because it is the whole of what makes the dialog a
// product rather than a mechanism: internal/web/jstest/save_as_test.mjs
// drives a designer's clicks and keystrokes through this function, which
// is the seam Task 15's finding was about — a controller that works and
// that no gesture reaches is a controller nobody has.
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
//
// A re-read of the *picture* is the client's own: it coalesces forty
// events into one call and hands the answer back through `onAnswer`, so
// this page redraws and never re-asks — asking here as well would be two
// runs for one event. A vocabulary rename re-reads the saved **row**
// without running it, which is exactly what that event means. A change
// to the query, and a deletion, are banded and change no pixel of the
// picture.
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
