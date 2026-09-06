// The harness for the two writes: the Arrangement in
// internal/web/static/components/mst-canvas.js and the MstGround in
// internal/web/static/components/mst-ground.js, driven against a stubbed
// data client whose every request is counted.
//
// What this layer covers that no Go test can.
//
// **That one drag is one write.** The property is a *count of requests
// across a gesture*, and a gesture does not exist on the server: by the
// time internal/web sees anything, sixty writes and one write differ
// only in how many rows were already written. So the instrument is the
// stubbed fetch's call list, and the fixture is forty nodes and sixty
// pointermoves, where the wrong answer is unmissable rather than a near
// miss.
//
// **That the same write comes out of the keyboard.** Two write paths are
// two chances to differ, so the assertion is not that the arrow keys work
// — it is that the *shape* of the body a nudge posts is the shape of the
// body a drag posts, compared structurally rather than by reading both
// and agreeing they look similar.
//
// **That a screen coordinate is never written.** A drag ends inside a
// pan-and-zoom transform. The fixture drags at 2× with the world panned,
// and asserts the number that reaches the wire is the game's, which is
// the number the layout composed against.
//
// **That a refused write reverts.** This is the one outcome a shared
// design tool may not produce: a screen that disagrees with the database
// indefinitely and says nothing. The assertion is on the model *and* on
// the coordinate in the DOM, because a revert that only moved the model
// would leave a designer looking at the arrangement they did not get.
//
// Run directly: `node internal/web/jstest/writes_test.mjs`.
// internal/web/static_appjs_browser_test.go shells out to it too.

import { install } from "./svg_dom.mjs";

const dom = install();

const { LAYER_IMAGE, LAYER_NODES, MARK_IMAGE, MARK_LABEL, MARK_RECT } = await import(
  "../static/render/scene.js"
);
const { addressOf } = await import("../static/address.js");
const { MODE_AUTO, MODE_MANUAL, MODE_MIXED } = await import("../static/positions.js");
const { client } = await import("../static/client.js");

const {
  ACTION_SWITCH_MODE,
  Arrangement,
  CLASS_PENDING,
  MstCanvas,
  NOTE_LAST_WRITER_WINS,
  NOTE_UNDO_BOUND,
  NOTE_UNPIN_NEEDS_CLEAR,
  REASON_AUTO_NO_DRAG,
  ROLE_VIEWER,
  arrangementMenu,
  snapTo,
  worldDelta,
} = await import("../static/components/mst-canvas.js");

const {
  ACTION_CLEAR,
  ACTION_CONFIRM_CLEAR,
  CONFIRM_CLEAR,
  FACTS,
  FACT_FORMATS,
  FACT_SIZE,
  FACT_SVG,
  MAX_ASSET_BYTES,
  MstGround,
} = await import("../static/components/mst-ground.js");

let failures = 0;
const pending = [];

function check(name, fn) {
  pending.push([name, fn]);
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function assertEqual(actual, expected, message) {
  if (actual !== expected) {
    throw new Error(`${message}: got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);
  }
}

function assertDeepEqual(actual, expected, message) {
  const a = JSON.stringify(actual);
  const b = JSON.stringify(expected);
  if (a !== b) throw new Error(`${message}: got ${a}, want ${b}`);
}

// settle lets every already-resolved promise run. Every await in these
// components is over a stubbed fetch, so "the work is done" is a
// microtask question and never a time question.
const settle = () => new Promise((resolve) => setImmediate(resolve));

// shapeOf reduces a value to its structure: object keys with the shape
// of each member, arrays to the *set* of their members' shapes, and
// every scalar to its type. It is what "the same shape" means when two
// write paths are compared — a drag of two nodes and a nudge of one must
// still be the same call, and a comparison of the two bodies as JSON
// would report the count and say nothing about the shape.
function shapeOf(value) {
  if (Array.isArray(value)) {
    const seen = [...new Set(value.map((item) => shapeOf(item)))].sort();
    return `[${seen.join("|")}]`;
  }
  if (value === null) return "null";
  if (typeof value === "object") {
    return `{${Object.keys(value)
      .sort()
      .map((key) => `${key}:${shapeOf(value[key])}`)
      .join(",")}}`;
  }
  return typeof value;
}

// --- The stubbed server ----------------------------------------------

function jsonResponse(status, body) {
  return { ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) };
}

// A stream the test writes raw text/event-stream bytes into, exactly as
// internal/web/events.go writes them. It is here rather than only in
// client_test.mjs because the question this file asks of the stream is a
// different one: not what a frame decides, but whether a write of our own
// coming back as an event fights the re-read it triggers.
function sseStream() {
  const chunks = [];
  const waiters = [];
  let closed = false;
  const encoder = new TextEncoder();

  function deliver() {
    while (waiters.length > 0 && (chunks.length > 0 || closed)) {
      const resolve = waiters.shift();
      if (chunks.length > 0) resolve({ done: false, value: chunks.shift() });
      else resolve({ done: true, value: undefined });
    }
  }

  return {
    write(text) {
      chunks.push(encoder.encode(text));
      deliver();
    },
    close() {
      closed = true;
      deliver();
    },
    response() {
      return {
        ok: true,
        status: 200,
        body: {
          getReader() {
            return {
              read() {
                if (chunks.length > 0) return Promise.resolve({ done: false, value: chunks.shift() });
                if (closed) return Promise.resolve({ done: true, value: undefined });
                return new Promise((resolve) => waiters.push(resolve));
              },
            };
          },
        },
      };
    },
  };
}

// placementFrame is the frame internal/views/events.go publishes when a
// placement moves: an identity and nothing a client could mistake for
// current. Spelled as bytes rather than as an object, so what is asserted
// is what the wire carries.
function placementFrame(key) {
  return `event: view.positions\ndata: {"key":"${key}","id":"a1"}\n\n`;
}

function stubServer() {
  const calls = [];
  const answers = new Map();
  const held = new Map();
  const streams = [];
  return {
    calls,
    streams,
    answer(fragment, value) {
      answers.set(fragment, value);
    },
    // hold makes one path's answer arrive when the test says so, which
    // is the only way to look at the page *while* a write is in flight.
    hold(fragment) {
      let release;
      const promise = new Promise((resolve) => {
        release = resolve;
      });
      held.set(fragment, promise);
      return (value) => {
        held.delete(fragment);
        release(value || jsonResponse(200, {}));
      };
    },
    countOf(fragment) {
      return calls.filter((call) => call.path.includes(fragment)).length;
    },
    bodiesOf(fragment) {
      return calls
        .filter((call) => call.path.includes(fragment))
        .map((call) => (typeof call.init.body === "string" ? JSON.parse(call.init.body) : call.init.body));
    },
    lastBody(fragment) {
      const bodies = stubBodies(calls, fragment);
      return bodies.length === 0 ? null : bodies[bodies.length - 1];
    },
    fetchImpl(path, init) {
      calls.push({ path, init: init || {} });
      if (path.endsWith("/events")) {
        const stream = sseStream();
        streams.push(stream);
        return Promise.resolve(stream.response());
      }
      for (const [fragment, promise] of held) {
        if (path.includes(fragment)) return promise;
      }
      for (const [fragment, value] of answers) {
        if (path.includes(fragment)) return Promise.resolve(value);
      }
      return Promise.resolve(jsonResponse(200, {}));
    },
  };
}

function stubBodies(calls, fragment) {
  return calls
    .filter((call) => call.path.includes(fragment))
    .map((call) => (typeof call.init.body === "string" ? JSON.parse(call.init.body) : call.init.body));
}

function fakeClock() {
  let at = 0;
  let seq = 0;
  const timers = new Map();
  return {
    setTimer(fn, ms) {
      const id = ++seq;
      timers.set(id, { at: at + ms, fn });
      return id;
    },
    clearTimer(id) {
      timers.delete(id);
    },
    pendingCount: () => timers.size,
    async advance(ms) {
      const target = at + ms;
      for (;;) {
        let due = null;
        for (const [id, timer] of timers) {
          if (timer.at <= target && (due === null || timer.at < due.timer.at)) due = { id, timer };
        }
        if (due === null) break;
        timers.delete(due.id);
        at = due.timer.at;
        due.timer.fn();
        await settle();
      }
      at = target;
      await settle();
    },
  };
}

// --- The fixtures ----------------------------------------------------

// A grid of `rows × 10` nodes, at (column × 100, row × 100). Six rows is
// sixty nodes, of which a marquee over the first four takes forty and
// leaves twenty behind — a fixture that selected everything would report
// "one write" whatever the marquee did.
const NODE_TYPE = "zone";

function gridNodes(rows) {
  const nodes = [];
  for (let row = 0; row < rows; row++) {
    for (let column = 0; column < 10; column++) {
      const key = `n${row}-${column}`;
      nodes.push({
        type: NODE_TYPE,
        key,
        address: addressOf({ type: NODE_TYPE, key }),
        x: column * 100,
        y: row * 100,
      });
    }
  }
  return nodes;
}

function gridScene(nodes, extra = []) {
  const marks = [];
  for (const node of nodes) {
    marks.push({ kind: MARK_RECT, key: node.address, x: node.x, y: node.y, w: 80, h: 24 });
    marks.push({ kind: MARK_LABEL, key: node.address, x: node.x, y: node.y, text: node.key });
  }
  return [...extra, ...marks];
}

const GROUND_HREF = "/api/games/azeroth/view-assets/9f1c";

function groundMark() {
  return {
    kind: MARK_IMAGE,
    layer: LAYER_IMAGE,
    x: 0,
    y: 0,
    w: 800,
    h: 600,
    href: GROUND_HREF,
    opacity: 1,
    fit: "none",
  };
}

const SAVED_ROW = {
  key: "world",
  name: "The world",
  query: { find: "entities" },
  renderer: "map",
  renderer_params: {},
  layout_mode: MODE_AUTO,
  version: 7,
};

// stage builds a canvas with a picture on it, a client wired to a
// stubbed server, and an Arrangement over the same nodes.
function stage(options = {}) {
  const rows = options.rows === undefined ? 6 : options.rows;
  const nodes = gridNodes(rows);
  const server = stubServer();
  const clock = fakeClock();
  const c = client({
    slug: "azeroth",
    fetchImpl: server.fetchImpl,
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
    random: () => 0,
  });
  const canvas = new MstCanvas({ document: dom.document });
  canvas.draw(gridScene(nodes, options.background ? [groundMark()] : []));
  if (options.view) canvas.setView(options.view);
  const arrangement = new Arrangement({
    canvas,
    client: c,
    viewKey: "world",
    row: options.row || SAVED_ROW,
    mode: options.mode || MODE_MANUAL,
    snap: options.snap,
    role: options.role,
    nodes,
  });
  return { nodes, server, clock, client: c, canvas, arrangement };
}

function rectOf(canvas, address) {
  return canvas.tree.nodes.get(address).shapes[0].element;
}

// --- Dragging --------------------------------------------------------

check("aFortyNodeDragIsOneWrite", async () => {
  const { server, canvas, arrangement, nodes } = stage();
  assertEqual(nodes.length, 60, "the fixture is sixty nodes, so a marquee can leave some out");

  // The marquee's edges fall between the rows and outside the columns,
  // so no node sits on its boundary: a fixture whose node was exactly on
  // the edge would be asserting a rounding rule instead of a selection.
  const selected = arrangement.marquee({ x1: -50, y1: -50, x2: 950, y2: 350 });
  assertEqual(selected.length, 40, "the marquee takes the first four rows and leaves the last two");

  arrangement.pointerDown(nodes[0].address, { x: 0, y: 0 });
  for (let i = 1; i <= 60; i++) arrangement.pointerMove({ x: i, y: 0 });
  assertEqual(server.countOf("/positions"), 0, "sixty pointermoves are still no write");

  await arrangement.pointerUp();
  assertEqual(server.countOf("/positions"), 1, "and the drop is one write, not forty and not sixty");
  const body = server.lastBody("/positions");
  assertEqual(body.positions.length, 40, "carrying every node in the selection");
  assertEqual(
    body.positions[0].x,
    60,
    "at the coordinate the body was dropped at",
  );
  // The twenty nodes the marquee left out are not in the body at all: a
  // write that carried the whole picture would also be "one write".
  const written = new Set(body.positions.map((row) => row.entity_key));
  assertEqual(written.size, 40, "forty distinct entities");
  assert(!written.has("n4-0"), "and no node the marquee excluded");
  assertEqual(canvas.tree.nodes.get(nodes[40].address).shapes[0].element.getAttribute("x"), "0",
    "which is also where the picture left it");
});

check("nothingIsWrittenUntilTheDrop", async () => {
  const { server, arrangement, nodes } = stage({ rows: 1 });
  arrangement.select(nodes[0].address);
  arrangement.pointerDown(nodes[0].address, { x: 0, y: 0 });
  for (let i = 1; i <= 60; i++) {
    arrangement.pointerMove({ x: i, y: i });
    await settle();
  }
  assertEqual(server.calls.length, 0, "sixty frames of a drag reach the server not once");
  await arrangement.pointerUp();
  assertEqual(server.countOf("/positions"), 1, "and then exactly once");
});

check("aDraggedNodeIsWrittenPinned", async () => {
  const { server, arrangement, nodes } = stage({ rows: 2 });
  arrangement.marquee({ x1: -50, y1: -50, x2: 950, y2: 50 });
  assertEqual(arrangement.selected().length, 10, "ten nodes, so this is not a claim about one row");
  arrangement.pointerDown(nodes[0].address, { x: 0, y: 0 });
  arrangement.pointerMove({ x: 10, y: 10 });
  await arrangement.pointerUp();
  const body = server.lastBody("/positions");
  assertEqual(body.positions.length, 10, "every dragged node is in the body");
  const flags = [...new Set(body.positions.map((row) => row.pinned))];
  assertDeepEqual(flags, [true], "and every one of them is written pinned, because that is what a drag means");
});

check("arrowKeysWriteTheSameShape", async () => {
  const dragged = stage({ rows: 2 });
  dragged.arrangement.marquee({ x1: -50, y1: -50, x2: 950, y2: 50 });
  dragged.arrangement.pointerDown(dragged.nodes[0].address, { x: 0, y: 0 });
  dragged.arrangement.pointerMove({ x: 5, y: 0 });
  await dragged.arrangement.pointerUp();
  const dragBody = dragged.server.lastBody("/positions");
  assert(dragBody !== null, "the drag really wrote something to compare against");

  const nudged = stage({ rows: 2 });
  nudged.arrangement.select(nudged.nodes[0].address);
  await nudged.arrangement.nudge(1, 0);
  const nudgeBody = nudged.server.lastBody("/positions");
  assertEqual(nudged.server.countOf("/positions"), 1, "an arrow key is one write too");

  assertEqual(
    shapeOf(nudgeBody),
    shapeOf(dragBody),
    "the keyboard posts the shape the mouse posts: two write paths are two chances to differ",
  );
  assertEqual(nudgeBody.positions[0].pinned, true, "including the pin a drag means");
  assertEqual(nudgeBody.positions[0].x, 1, "and one grid unit is one pixel where there is no grid");
});

check("snapIsAppliedOnlyInManualMode", async () => {
  // 43 and 17 are not multiples of 25, and 50 and 25 are not 43 and 17:
  // a fixture whose drag happened to land on the grid would pass under
  // either policy.
  const manual = stage({ rows: 1, mode: MODE_MANUAL, snap: 25 });
  manual.arrangement.select(manual.nodes[0].address);
  manual.arrangement.pointerDown(manual.nodes[0].address, { x: 0, y: 0 });
  manual.arrangement.pointerMove({ x: 43, y: 17 });
  await manual.arrangement.pointerUp();
  const snapped = manual.server.lastBody("/positions").positions[0];
  assertEqual(snapped.x, 50, "a dragged node lands on the grid in manual mode");
  assertEqual(snapped.y, 25, "in both axes");

  const mixed = stage({ rows: 1, mode: MODE_MIXED, snap: 25 });
  mixed.arrangement.select(mixed.nodes[0].address);
  mixed.arrangement.pointerDown(mixed.nodes[0].address, { x: 0, y: 0 });
  mixed.arrangement.pointerMove({ x: 43, y: 17 });
  await mixed.arrangement.pointerUp();
  const free = mixed.server.lastBody("/positions").positions[0];
  assertEqual(free.x, 43, "and lands where it was dropped in mixed, which re-fits the whole picture");
  assertEqual(free.y, 17, "in both axes");

  assertEqual(snapTo(43, 0), 43, "a snap of 0 is the identity, not a snap of one unit");
});

check("aDragWritesTheGamesCoordinatesAndNotTheScreens", async () => {
  // Panned and zoomed: the pointer travels 40 screen pixels, which is 20
  // in the space the layout composed against. A canvas that wrote what
  // the pointer did would drift every saved arrangement by whatever
  // viewport the designer happened to have open.
  const { server, arrangement, nodes } = stage({ rows: 1, view: { x: 137, y: -412, k: 2 } });
  arrangement.select(nodes[3].address);
  assertEqual(nodes[3].x, 300, "the node starts at a known game coordinate");
  arrangement.pointerDown(nodes[3].address, { x: 1000, y: 1000 });
  arrangement.pointerMove({ x: 1040, y: 1060 });
  await arrangement.pointerUp();
  const row = server.lastBody("/positions").positions[0];
  assertEqual(row.x, 320, "the write is the game's coordinate: 40 screen pixels at 2x is 20 of the game's");
  assertEqual(row.y, 30, "and the pan does not reach the wire at all");
  assertDeepEqual(worldDelta(40, 60, 2), { dx: 20, dy: 30 }, "which is the one function that divides");
});

check("draggingIsDisabledInAutoAndTheCanvasSaysWhy", async () => {
  const { server, canvas, arrangement, nodes } = stage({ rows: 1, mode: MODE_AUTO });
  assertEqual(arrangement.draggable, false, "auto ignores stored rows, so a drag there writes a row nothing reads");
  arrangement.select(nodes[0].address);
  assertEqual(arrangement.pointerDown(nodes[0].address, { x: 0, y: 0 }), null, "the drag does not start");
  arrangement.pointerMove({ x: 40, y: 40 });
  assertEqual(await arrangement.pointerUp(), null, "and there is no drop to write");
  assertEqual(await arrangement.nudge(1, 0), null, "nor does the keyboard find a way round");
  assertEqual(server.calls.length, 0, "nothing at all reaches the server");
  assertEqual(rectOf(canvas, nodes[0].address).getAttribute("x"), "0", "and nothing moved");

  // A silently inert canvas is the same defect wearing a mouse, so the
  // sentence has to be on screen and not only in a comment.
  const menu = canvas.showArrangement(arrangement);
  const text = menu.textContent;
  assert(text.includes(REASON_AUTO_NO_DRAG), `the canvas says why: got ${JSON.stringify(text)}`);
});

check("theSwitchToMixedIsAnUpsertWithExpectedVersion", async () => {
  const { server, arrangement } = stage({ rows: 1, mode: MODE_AUTO });
  const menu = arrangement.menu();
  assertDeepEqual(
    menu.actions.map((action) => action.id),
    [ACTION_SWITCH_MODE],
    "the one thing offered is the switch",
  );
  await arrangement.switchToMixed();
  assertEqual(server.countOf("/views"), 1, "which is one upsert");
  const body = server.lastBody("/views");
  assertEqual(body.layout_mode, MODE_MIXED, "carrying the new mode");
  assertEqual(body.expected_version, 7, "and the version it read the row at, because this one may not silently lose");
  assertEqual(body.key, "world", "on the row it read");
  assertEqual(arrangement.draggable, true, "and the canvas can be dragged afterwards");
});

check("aViewerGetsTheSentenceWithoutTheButton", async () => {
  const { server, canvas, arrangement, nodes } = stage({ rows: 1, mode: MODE_AUTO, role: ROLE_VIEWER });
  const menu = arrangement.menu();
  assertDeepEqual(menu.notes, [REASON_AUTO_NO_DRAG], "a viewer is told why the canvas does not move");
  assertDeepEqual(menu.actions, [], "and is not offered the button the server would refuse");
  const element = canvas.showArrangement(arrangement);
  assert(element.textContent.includes(REASON_AUTO_NO_DRAG), "the sentence is on screen");
  assertEqual(
    element.childNodes.filter((child) => child.tagName === "button").length,
    0,
    "and there is no button beside it",
  );
  assertEqual(await arrangement.switchToMixed(), null, "asking anyway writes nothing");
  assertEqual(server.calls.length, 0, "not one request");

  // The same reader in a mode that *can* be dragged still gets the
  // sentences and still gets no buttons: the rule is the role and not
  // the mode.
  const draggable = arrangementMenu({ mode: MODE_MANUAL, role: ROLE_VIEWER, canUndo: true });
  assertDeepEqual(draggable.actions, [], "a viewer is offered nothing in manual either");
  assertDeepEqual(
    draggable.notes,
    [NOTE_UNPIN_NEEDS_CLEAR, NOTE_UNDO_BOUND, NOTE_LAST_WRITER_WINS],
    "and reads the three things that are otherwise invisible",
  );
});

check("aRefusedWriteRevertsAndBands", async () => {
  const { server, canvas, arrangement, nodes } = stage({ rows: 1 });
  const sentence = "this view was removed while you were dragging";
  server.answer("/positions", jsonResponse(409, { error: "conflict", message: sentence }));

  const address = nodes[2].address;
  assertEqual(rectOf(canvas, address).getAttribute("x"), "200", "the node starts here");
  arrangement.select(address);
  arrangement.pointerDown(address, { x: 0, y: 0 });
  arrangement.pointerMove({ x: 60, y: 40 });
  assertEqual(rectOf(canvas, address).getAttribute("x"), "200", "the drag layer carries it, so its own x is untouched");
  const answer = await arrangement.pointerUp();
  assertEqual(answer.ok, false, "the write was refused");

  assertDeepEqual(arrangement.positionOf(address), { x: 200, y: 0 }, "the model goes back to the pre-drag coordinates");
  assertEqual(rectOf(canvas, address).getAttribute("x"), "200", "and so does the picture: a revert only in the model is a screen that lies");
  assertEqual(rectOf(canvas, address).getAttribute("y"), "0", "in both axes");
  assertEqual(arrangement.band, sentence, "and the band is the server's own sentence, verbatim");
  assertEqual(canvas.showArrangement(arrangement).textContent.includes(sentence), true, "rendered as it arrived");
  assertEqual(arrangement.undoStep, null, "with nothing to undo, because nothing happened");
});

check("undoRewritesThePreDragCoordinates", async () => {
  const { server, canvas, arrangement, nodes } = stage({ rows: 1 });
  const address = nodes[1].address;
  arrangement.select(address);
  arrangement.pointerDown(address, { x: 0, y: 0 });
  arrangement.pointerMove({ x: 37, y: 11 });
  await arrangement.pointerUp();
  assertDeepEqual(arrangement.positionOf(address), { x: 137, y: 11 }, "the drop moved it");
  assertEqual(rectOf(canvas, address).getAttribute("x"), "137", "in the picture too");

  await arrangement.undo();
  assertEqual(server.countOf("/positions"), 2, "undo is a write of its own: there is no undo on the server");
  const body = server.lastBody("/positions");
  assertEqual(body.positions[0].x, 100, "carrying the pre-drag coordinates");
  assertEqual(body.positions[0].y, 0, "in both axes");
  assertDeepEqual(arrangement.positionOf(address), { x: 100, y: 0 }, "and the model is back where it started");
  assertEqual(rectOf(canvas, address).getAttribute("x"), "100", "and so is the picture");
});

check("undoIsOneLevelOnly", async () => {
  const { server, arrangement, nodes } = stage({ rows: 1 });
  const address = nodes[1].address;
  arrangement.select(address);
  arrangement.pointerDown(address, { x: 0, y: 0 });
  arrangement.pointerMove({ x: 37, y: 0 });
  await arrangement.pointerUp();
  await arrangement.undo();
  assertEqual(server.countOf("/positions"), 2, "the drop and the undo");
  assertEqual(await arrangement.undo(), null, "the second Ctrl-Z has nothing to do");
  assertEqual(server.countOf("/positions"), 2, "and writes nothing: one level, and the menu says so");
  assertDeepEqual(arrangement.positionOf(address), { x: 100, y: 0 }, "the node stays where the undo put it");
  assert(NOTE_UNDO_BOUND.length > 0, "and the bound is stated rather than discovered");
});

check("unpinIsTheOnlyWriteThatDoesNotPin", async () => {
  const { server, arrangement, nodes } = stage({ rows: 1 });
  arrangement.select(nodes[0].address);
  await arrangement.unpin();
  assertEqual(server.countOf("/positions"), 1, "unpinning is a write");
  assertEqual(server.lastBody("/positions").positions[0].pinned, false, "and the one that sends false");
  assertEqual(server.lastBody("/positions").positions[0].x, 0, "at the coordinate the node already has");
});

check("clearingASelectionNeverClearsTheWholeView", async () => {
  const { server, client: c, arrangement, nodes } = stage({ rows: 1 });
  arrangement.select(nodes[0].address);
  arrangement.select(nodes[1].address, { shift: true });
  assertEqual(arrangement.selected().length, 2, "shift adds to the selection");
  await arrangement.clearPositions();
  const body = server.lastBody("/positions/clear");
  assertEqual(body.entities.length, 2, "the clear names exactly what is selected");
  assertDeepEqual(
    body.entities.map((entity) => entity.entity_key).sort(),
    ["n0-0", "n0-1"],
    "by type and key",
  );

  // An absent `entities` on that route clears the whole view, so an
  // empty selection must not become one. The guard is asked of **both**
  // layers: the menu refuses to ask, and the client refuses to send. A
  // test that only drove the menu would leave the client's guard correct
  // and unasserted, which is where it would quietly be lost — the next
  // caller of clearPositions is not this menu.
  arrangement.selection.clear();
  const refused = await arrangement.clearPositions();
  assertEqual(refused, null, "an empty selection clears nothing");
  assertEqual(server.countOf("/positions/clear"), 1, "and sends no second request");

  const direct = await c.clearPositions("world", []);
  assertEqual(direct.ok, false, "and the client refuses an empty list on its own");
  assertEqual(
    server.countOf("/positions/clear"),
    1,
    "without reaching the route at all: an unnamed clear there means the whole view",
  );
});

check("aWriteOfOursComingBackAsAnEventDoesNotFightTheReRead", async () => {
  const { server, clock, client: c, canvas, arrangement, nodes } = stage({ rows: 1 });
  server.answer("/views/run", jsonResponse(200, { nodes: [], edges: [], positions: [] }));
  await c.runView("world", {});
  c.connect(() => {});
  await settle();
  const runsBefore = server.countOf("/views/run");

  const address = nodes[0].address;
  arrangement.select(address);
  arrangement.pointerDown(address, { x: 0, y: 0 });
  arrangement.pointerMove({ x: 20, y: 0 });

  // Somebody else's placement event arrives mid-drag. A re-read now
  // would swap the coordinates under the pointer, so the client holds it.
  server.streams[0].write(placementFrame("world"));
  await settle();
  await clock.advance(2000);
  assertEqual(server.countOf("/views/run"), runsBefore, "a re-read never fires while a drag is in flight");
  assertEqual(rectOf(canvas, address).getAttribute("x"), "0", "and the node under the pointer does not jump");

  // Our own drop. Its event comes back to us the way every other
  // browser's does: identity, and nothing to patch from.
  const releasePositions = server.hold("/positions");
  const drop = arrangement.pointerUp();
  await settle();
  server.streams[0].write(placementFrame("world"));
  await settle();
  await clock.advance(2000);
  assertEqual(
    server.countOf("/views/run"),
    runsBefore,
    "nor while our own write is unacknowledged: a re-read then reads back the state the write is about to change",
  );

  releasePositions(jsonResponse(200, {}));
  await drop;
  await clock.advance(2000);
  assertEqual(
    server.countOf("/views/run"),
    runsBefore + 1,
    "and once the write has landed the answer is a re-read, exactly one, however many events asked for it",
  );
  assertEqual(c.state.pendingWrites, 0, "the write counter comes back down");
  assertEqual(c.state.dragging, false, "and the drag flag with it, or every later re-read would be deferred forever");
  assertDeepEqual(arrangement.positionOf(address), { x: 20, y: 0 }, "the coordinates we wrote are the ones we hold");
});

// --- The ground ------------------------------------------------------

function groundStage(options = {}) {
  const staged = stage({ rows: 1, background: true, view: options.view });
  const ground = new MstGround({
    document: dom.document,
    client: staged.client,
    canvas: staged.canvas,
    viewKey: "world",
    asset: options.asset,
    background: options.background,
  });
  return { ...staged, ground };
}

check("theUploadPickerStatesTheRefusalBeforeAFileIsChosen", () => {
  const { ground } = groundStage();
  assertEqual(ground.file, null, "no file has been chosen");
  const text = ground.root.textContent;
  for (const fact of FACTS) {
    assert(text.includes(fact), `the picker states it up front: ${JSON.stringify(fact)}`);
  }
  assertEqual(FACTS.length, 3, "and there are three of them");
  assert(FACT_SIZE.includes(String(MAX_ASSET_BYTES)), "the size is the server's own number");
  assert(FACT_FORMATS.includes("image/png"), "the formats are the server's own mimes");
  assert(FACT_SVG.includes("script"), "and the SVG refusal repeats the server's reason rather than paraphrasing it");
  const input = ground.root.childNodes.find((child) => child.tagName === "input");
  assertEqual(input.getAttribute("accept"), "image/png,image/jpeg,image/webp", "the picker offers the three");
});

check("aRefusedUploadShowsTheServerSentence", async () => {
  const { server, ground } = groundStage();
  const sentence =
    "look like an SVG, which Maestro does not accept as a background: an SVG served to a browser can carry script";
  server.answer("/view-assets", jsonResponse(400, { error: "invalid_input", message: sentence }));
  ground.choose({ name: "world-map.png", size: 12 });
  const answer = await ground.upload();
  assertEqual(answer.ok, false, "the upload was refused");
  assertEqual(ground.band, sentence, "and the sentence is the server's, unmodified");
  assert(ground.root.textContent.includes(sentence), "rendered verbatim");
  assertEqual(ground.asset, null, "nothing was placed");
  assertEqual(server.countOf("/background"), 0, "and no background write followed a failed upload");
  const upload = server.calls.find((call) => call.path.includes("/view-assets"));
  assert(upload.path.includes("filename=world-map.png"), `the filename rides in the query string: ${upload.path}`);
});

check("adjustGroundMovesTheImageAndNotTheNodes", async () => {
  const asset = { id: "9f1c", url: GROUND_HREF, width: 800, height: 600 };
  const { server, canvas, ground, nodes } = groundStage({ asset, view: { x: 0, y: 0, k: 2 } });
  const image = canvas.tree.layers.get(LAYER_IMAGE).childNodes[0];
  assertEqual(image.getAttribute("x"), "0", "the ground starts at the origin");
  const nodeRect = rectOf(canvas, nodes[4].address);
  assertEqual(nodeRect.getAttribute("x"), "400", "and a node at its own coordinate");

  ground.adjust(asset);
  dom.reset();
  ground.moveBy(40, 20);
  ground.scaleBy(1.5);
  assertEqual(image.getAttribute("x"), "20", "the image moves by the game's delta, not the pointer's");
  assertEqual(image.getAttribute("y"), "10", "in both axes");
  assertEqual(image.getAttribute("width"), "1200", "and scales");
  assertEqual(nodeRect.getAttribute("x"), "400", "while every node holds still");
  const touched = dom.touched();
  assertEqual(touched.length, 1, `only the ground was written to: ${touched.length} elements changed`);
  assert(touched.includes(image), "and it is the image");
  assertEqual(server.calls.length, 0, "adjusting writes nothing until it is committed");

  await ground.commit();
  assertEqual(server.countOf("/background"), 1, "committing is one write");
  const body = server.lastBody("/background");
  assertEqual(body.asset_id, "9f1c", "naming the asset");
  assertEqual(body.scale, 1.5, "the scale");
  assertDeepEqual(body.offset, { x: 20, y: 10 }, "and the offset in the game's coordinates");
});

check("cancellingThePlacementWritesNothing", async () => {
  const asset = { id: "9f1c", url: GROUND_HREF, width: 800, height: 600 };
  const { server, canvas, ground } = groundStage({ asset });
  const image = canvas.tree.layers.get(LAYER_IMAGE).childNodes[0];
  ground.adjust(asset);
  ground.moveBy(90, 90);
  ground.scaleBy(2);
  assertEqual(image.getAttribute("x"), "90", "the image really moved while the mode was on");
  assertEqual(image.getAttribute("width"), "1600", "and really scaled");

  ground.cancel();
  assertEqual(server.calls.length, 0, "cancelling reaches the server not once");
  assertEqual(ground.placing, null, "and leaves the mode");
  assertEqual(image.getAttribute("x"), "0", "the image goes back where the mode found it");
  assertEqual(image.getAttribute("width"), "800", "at the size it found it");
});

check("clearingTheGroundSendsNullAndIsConfirmed", async () => {
  const asset = { id: "9f1c", url: GROUND_HREF, width: 800, height: 600 };
  const { server, ground } = groundStage({
    asset,
    background: { asset_id: "9f1c", scale: 2, offset: { x: 5, y: 6 } },
  });
  assertDeepEqual(
    ground.actions().map((action) => action.id),
    ["adjust", ACTION_CLEAR],
    "the clear is offered",
  );
  await ground.clear();
  assertEqual(server.calls.length, 0, "the first ask writes nothing");
  assert(ground.root.textContent.includes(CONFIRM_CLEAR), "and puts the confirmation on screen");
  assertDeepEqual(
    ground.actions().map((action) => action.id),
    ["adjust", ACTION_CONFIRM_CLEAR],
    "with the confirming action in place of the asking one",
  );

  await ground.clear({ confirmed: true });
  assertEqual(server.countOf("/background"), 1, "confirming is one write");
  const body = server.lastBody("/background");
  assertEqual(body.asset_id, null, "and the only spelling for a clear is an explicit null");
  assert(!("scale" in body), "with no scale beside it: RemoveAsset resets those, and naming them would be a second opinion");
  assertEqual(ground.asset, null, "the view names no ground now");
});

check("adjustingANewImageDoesNotInheritThePreviousOnesArithmetic", async () => {
  const asset = { id: "9f1c", url: GROUND_HREF, width: 800, height: 600 };
  const { server, ground } = groundStage({
    asset,
    background: { asset_id: "9f1c", scale: 2, offset: { x: 5, y: 6 } },
  });
  const same = ground.adjust(asset);
  assertEqual(same.scale, 2, "adjusting the same image starts from where it is");
  assertDeepEqual(same.offset, { x: 5, y: 6 }, "offset included");

  const other = { id: "aa02", url: "/api/games/azeroth/view-assets/aa02", width: 400, height: 300 };
  const fresh = ground.adjust(other);
  assertEqual(fresh.scale, 1, "a new image starts from the defaults");
  assertDeepEqual(fresh.offset, { x: 0, y: 0 }, "at the origin");
  await ground.commit();
  const body = server.lastBody("/background");
  assertEqual(body.asset_id, "aa02", "and the write names the new image");
  assertEqual(body.scale, 1, "with its own arithmetic and not the old image's");
});

check("pendingElementsCarryThePendingTreatment", async () => {
  const asset = { id: "9f1c", url: GROUND_HREF, width: 800, height: 600 };
  const { server, canvas, ground, arrangement, nodes } = groundStage({ asset });
  const releaseBackground = server.hold("/background");
  ground.adjust(asset);
  const committing = ground.commit();
  await settle();
  assertEqual(ground.pending, true, "the write is in flight");
  assert(ground.root.getAttribute("class").includes(CLASS_PENDING), "and the panel wears the treatment");
  assertEqual(ground.root.textContent.includes("Loading"), false, "which is opacity and not a spinner");
  releaseBackground();
  await committing;
  assertEqual(ground.pending, false, "and takes it off again");
  assertEqual(ground.root.getAttribute("class").includes(CLASS_PENDING), false, "once the server has answered");

  const releasePositions = server.hold("/positions");
  arrangement.select(nodes[0].address);
  const nudging = arrangement.nudge(1, 0);
  await settle();
  assertEqual(arrangement.pending, true, "a position write is in flight too");
  assert(
    canvas.showArrangement(arrangement).getAttribute("class").includes(CLASS_PENDING),
    "and the arrangement menu wears the same treatment",
  );
  releasePositions();
  await nudging;
  assertEqual(arrangement.pending, false, "and takes it off");
  assertEqual(
    canvas.showArrangement(arrangement).getAttribute("class").includes(CLASS_PENDING),
    false,
    "when the write has landed",
  );
});

// --- Runner ----------------------------------------------------------

for (const [name, fn] of pending) {
  try {
    await fn();
    console.log(`ok   ${name}`);
  } catch (err) {
    failures++;
    console.error(`FAIL ${name}\n     ${err.message}`);
  }
}

if (failures > 0) {
  console.error(`${failures} check(s) failed`);
  process.exit(1);
}
console.log("the two writes: all checks passed");
