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
import { mapScene, RENDERER as RENDERER_MAP } from "../render/map.js";
import { tableScene, RENDERER as RENDERER_TABLE } from "../render/table.js";
import { timelineScene, RENDERER as RENDERER_TIMELINE } from "../render/timeline.js";
import { gridFallback } from "../layout/compose.js";
import { LAYOUT_BUDGET_MS, runWithBudget } from "../layout/budget.js";
import { Arrangement, MstCanvas, worldDelta } from "../components/mst-canvas.js";
import { MstGround } from "../components/mst-ground.js";
import "../components/mst-view-frame.js";
import "../components/mst-table.js";
import { entityBody, readEntity } from "./entity.js";
import {
  destinations,
  DESTINATION_VIEWS,
  entityURL,
  expired,
  openGame,
  say,
  segmentsOf,
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
  [RENDERER_MAP]: (envelope, layout, params, options) => mapScene(envelope, params, options),
  [RENDERER_TIMELINE]: (envelope, layout, params, options) => timelineScene(envelope, params, options),
  [RENDERER_TABLE]: (envelope, layout, params, options) => tableScene(envelope, params, options),
};

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
  const back = doc.getElementById("back-to-views");
  if (back) back.href = viewsURL(opened.slug);

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

  const surface = mount(doc, rootEl, opened.slug, key, row.result, client, options);
  surface.role = summary.ok ? String(summary.result.role ?? "") : "";
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
    arrangement: null,
    scene: null,
    // The bound parameters, as text, straight off the URL. They are
    // typed against the query's declarations only at the moment of the
    // run, because a URL carries no types and guessing that `20` is the
    // number twenty would send a number to a text parameter.
    params: readParams(options.search ?? globalThis.window.location.search),
  };

  state.noticeEl = doc.getElementById("view-error");
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
async function draw(state, envelope, error, options) {
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

  const scene = envelope === null ? null : SCENES[renderer]
    ? SCENES[renderer](envelope, layout, params, {
        background: backgroundOf(state.row, state.background),
        zoom: state.canvas.view.k,
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
  });
  state.frame.params = state.params;

  if (scene === null) {
    state.table.hidden = true;
    return state;
  }
  if (renderer === RENDERER_TABLE) {
    state.table.hidden = false;
    state.table.table = scene;
    state.canvas.hidden = true;
    return state;
  }
  state.table.hidden = true;
  state.canvas.hidden = false;
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
  return state;
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
  const opened = await openGame();
  if (opened !== null) {
    const doc = opened.document;
    if (opened.game === null) {
      await viewPage(opened);
    } else {
      doc.body.prepend(destinations(doc, opened.slug, DESTINATION_VIEWS));
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
