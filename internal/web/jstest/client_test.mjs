// The harness for internal/web/static/client.js: it imports the real,
// unmodified module and drives it the way a page does — a stubbed fetch,
// a stubbed event stream carrying real `text/event-stream` bytes, and
// injected timers so a 750ms coalescing window is a millisecond of test
// time rather than a sleep.
//
// What this covers that no Go test can. The client is the only module in
// this front end that fetches, and everything that makes it safe is a
// property of the JavaScript: that forty events are one re-read, that a
// re-read waits for a drag and for an unacknowledged write and for a
// hidden tab, that a payload never becomes state, that a position write
// carries a type and a key and no uuid, that a heartbeat comment is not
// an event, that the stream reconnects when the server closes it — which
// it always does, on purpose — and that a refusal reaches the caller as
// the server's own sentence, character for character.
//
// Run directly: `node internal/web/jstest/client_test.mjs`.
// internal/web/static_appjs_browser_test.go shells out to it too.

import {
  BAND,
  GONE,
  IGNORE,
  PARAM_PREFIX,
  REREAD,
  REREAD_DEBOUNCE_MS,
  TARGET_EVERYTHING,
  TARGET_NOTHING,
  TARGET_PICTURE,
  TARGET_PROSE,
  TARGET_VIEW,
  applyEvent,
  client,
  parseFrames,
  readParams,
  typeParams,
  writeParams,
} from "../static/client.js";

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

// settle lets every already-resolved promise in the client's own async
// chain run before the next assertion. The client awaits a stubbed fetch
// and a stubbed stream reader, so "the work is done" is a microtask
// question and not a time question.
const settle = () => new Promise((resolve) => setImmediate(resolve));

// walkStrings visits every string in a value, however deeply nested.
function walkStrings(value, visit) {
  if (typeof value === "string") {
    visit(value);
    return;
  }
  if (Array.isArray(value)) {
    for (const item of value) walkStrings(item, visit);
    return;
  }
  if (value && typeof value === "object") {
    for (const key of Object.keys(value)) {
      visit(key);
      walkStrings(value[key], visit);
    }
  }
}

// --- The injected timers ---------------------------------------------

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

// --- The stubbed stream ----------------------------------------------

// A stream the test writes raw SSE bytes into, exactly as the server
// writes them: comment lines included, because a comment line is the one
// frame this client must decide nothing about.
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

function frame(kind, data) {
  return `event: ${kind}\ndata: ${JSON.stringify(data)}\n\n`;
}

// --- The stubbed server ----------------------------------------------

function stubServer() {
  const calls = [];
  let streams = [];
  const answers = new Map();

  const server = {
    calls,
    streams,
    // answer registers what a path replies with. Absent, a POST answers
    // an empty 200 so a test that is not about the answer says nothing
    // about it.
    answer(path, value) {
      answers.set(path, value);
    },
    countOf(fragment) {
      return calls.filter((call) => call.path.includes(fragment)).length;
    },
    lastBody(fragment) {
      const hit = [...calls].reverse().find((call) => call.path.includes(fragment));
      return hit ? JSON.parse(hit.init.body) : null;
    },
    fetchImpl(path, init) {
      calls.push({ path, init: init || {} });
      if (path.endsWith("/events")) {
        const stream = sseStream();
        streams.push(stream);
        return Promise.resolve(stream.response());
      }
      for (const [fragment, value] of answers) {
        if (path.includes(fragment)) return Promise.resolve(value);
      }
      return Promise.resolve(jsonResponse(200, {}));
    },
  };
  return server;
}

function jsonResponse(status, body) {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  };
}

// harness wires a client to the stubbed server and the fake clock, runs
// one view so there is a picture on screen, and opens the stream.
async function harness(options) {
  const opts = options || {};
  const clock = fakeClock();
  const server = stubServer();
  if (opts.runResult) server.answer("/views/run", jsonResponse(200, opts.runResult));
  const c = client({
    slug: "azeroth",
    fetchImpl: server.fetchImpl,
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
    random: () => 0,
  });
  const decisions = [];
  await c.runView("world", { class: "mage" });
  c.connect((verdict) => decisions.push(verdict));
  await settle();
  return { clock, server, client: c, decisions, stream: () => server.streams[server.streams.length - 1] };
}

// --- The reducer, on its own -----------------------------------------

// A state with one view open. applyEvent is pure over it, and every
// reducer check below deep-freezes it: this module runs as an ES module
// and therefore in strict mode, so an assignment into a frozen object
// throws rather than silently succeeding. A reducer that patched state
// from a payload would not fail an assertion here; it would throw.
function openState(extra) {
  const state = { views: { world: { params: {}, result: { nodes: [] } } } };
  Object.assign(state, extra || {});
  Object.freeze(state.views.world);
  Object.freeze(state.views);
  Object.freeze(state);
  return state;
}

check("theFourOutcomesEachHaveACaseOnlyTheyProduce", () => {
  const state = openState();
  const cases = [
    [{ kind: "view.positions", data: { id: "u1", key: "world" } }, REREAD, TARGET_PICTURE],
    [{ kind: "view.upserted", data: { id: "u1", key: "world", version: 4 } }, BAND, TARGET_PICTURE],
    [{ kind: "view.removed", data: { id: "u1", key: "world", version: 4 } }, GONE, TARGET_PICTURE],
    [{ kind: "entity.upserted", data: { id: "u1" } }, IGNORE, TARGET_NOTHING],
  ];
  const seen = new Set();
  for (const [event, want, target] of cases) {
    const verdict = applyEvent(state, event);
    assertEqual(verdict.decision, want, `decision for ${event.kind}`);
    assertEqual(verdict.target, target, `target for ${event.kind}`);
    seen.add(verdict.decision);
  }
  assertEqual(seen.size, 4, "four distinct outcomes, each from a case only it produces");
});

check("theClientNeverPatchesFromAPayload", () => {
  // Every payload field this product ever puts on the wire, plus a
  // poison value in each non-identity slot and a field no kind has. The
  // identity a decision may carry is named per kind; anything else
  // appearing anywhere in the decision is a value the client took from a
  // payload, which is the thing publication order makes unsafe.
  const poison = "P0IS0N";
  const table = [
    ["view.positions", { id: poison, key: "world", positions: [{ x: 1234, y: 5678 }] }, ["world"]],
    ["view.background", { id: poison, key: "world", asset_id: poison }, ["world"]],
    ["view.upserted", { id: poison, key: "world", version: 4321 }, ["world"]],
    ["view.removed", { id: poison, key: "world", version: 4321 }, ["world"]],
    ["type.renamed", { id: poison, from: "zone", to: "region" }, ["zone", "region"]],
    ["relation_type.renamed", { id: poison, from: "a", to: "b" }, ["a", "b"]],
    ["document.moved", { id: poison, from: "x", to: "y", version: 4321 }, ["x", "y"]],
    ["resync", {}, []],
  ];
  for (const [kind, data, identity] of table) {
    const state = openState();
    const before = JSON.stringify(state);
    const verdict = applyEvent(state, { kind, data });
    assertEqual(JSON.stringify(state), before, `state after ${kind}`);
    const text = JSON.stringify(verdict);
    assert(!text.includes(poison), `${kind} decision carries a payload value: ${text}`);
    assert(!text.includes("4321"), `${kind} decision carries a version: ${text}`);
    assert(!text.includes("1234") && !text.includes("5678"), `${kind} decision carries coordinates`);
    for (const value of Object.values(verdict)) {
      if (typeof value !== "string") continue;
      const known = [kind, verdict.decision, verdict.target, verdict.reason, ...identity];
      assert(known.includes(value), `${kind} decision carries an unexpected value: ${value}`);
    }
  }
});

check("unknownEventKindsAreIgnoredAndRecorded", () => {
  const state = openState();
  for (const kind of ["entity.upserted", "relation.removed", "member.updated", ""]) {
    const verdict = applyEvent(state, { kind, data: {} });
    assertEqual(verdict.decision, IGNORE, `decision for ${kind}`);
    assertEqual(verdict.kind, kind, "the kind is recorded rather than dropped");
    assertEqual(verdict.reason, "kind.unhandled", `reason for ${kind}`);
  }
});

check("aRenameRereadsTheViewRowAndNotThePicture", () => {
  const state = openState();
  for (const kind of ["type.renamed", "relation_type.renamed"]) {
    const verdict = applyEvent(state, { kind, data: { id: "u", from: "zone", to: "region" } });
    assertEqual(verdict.decision, REREAD, `decision for ${kind}`);
    assertEqual(verdict.target, TARGET_VIEW, `${kind} re-reads the row, never re-runs the query`);
    assertEqual(verdict.from, "zone", "the old spelling, which is what a client is holding");
    assertEqual(verdict.to, "region", "the new spelling");
  }
  const moved = applyEvent(state, { kind: "document.moved", data: { from: "a/b.md", to: "c/d.md" } });
  assertEqual(moved.target, TARGET_PROSE, "a moved document is a prose re-read");
});

check("aStreamGapIsAReadAndNotAnUnknownKind", () => {
  const verdict = applyEvent(openState(), { kind: "resync", data: {} });
  assertEqual(verdict.decision, REREAD, "resync decision");
  assertEqual(verdict.target, TARGET_EVERYTHING, "resync target");
  assertEqual(verdict.reason, "stream.gap", "resync reason");
});

check("anEventForAnotherViewIsIgnored", () => {
  const verdict = applyEvent(openState(), { kind: "view.positions", data: { key: "elsewhere" } });
  assertEqual(verdict.decision, IGNORE, "decision");
  assertEqual(verdict.reason, "view.notopen", "reason");
});

// The control test the plan asks for by name. This client has no notion
// of a role at all, and that is the point: the same event under a viewer
// and under an editor is one decision, so a viewer is never shown a
// picture that swapped under them while an editor is banded, or the
// reverse. It fails alongside the band test for one mutation, which is
// what says it is a control rather than a duplicate.
check("aViewerGetsTheSameReloadBandAsAnEditor", () => {
  const event = { kind: "view.upserted", data: { id: "u1", key: "world", version: 9 } };
  const asViewer = applyEvent(openState({ role: "viewer" }), event);
  const asEditor = applyEvent(openState({ role: "editor" }), event);
  assertDeepEqual(asViewer, asEditor, "one decision under both roles");
  assertEqual(asViewer.decision, BAND, "and it is a band");
});

// --- Parameter binding -----------------------------------------------

check("parametersRoundTripThroughTheURL", () => {
  const cases = [
    { class: "mage" },
    { class: "" },
    { "a.b": "x" },
    { class: "fire&ice=hot" },
    { class: "20" },
    { class: "a b" },
    { "class.name": "p.q" },
    { class: "ünïcode" },
  ];
  for (const want of cases) {
    const search = writeParams(want, "");
    const got = readParams("?" + search);
    assertDeepEqual(got, want, `round trip of ${JSON.stringify(want)}`);
  }
});

check("aMissingParameterIsNotAParameterBoundToNothing", () => {
  const bound = readParams("?p.class=");
  assert("class" in bound, "a parameter bound to nothing is present");
  assertEqual(bound.class, "", "and its value is the empty string");

  const missing = readParams("?tab=graph");
  assert(!("class" in missing), "a parameter nobody bound is absent, not empty");
  assertDeepEqual(missing, {}, "and nothing else leaks in from the page own keys");
});

check("aNumberInTheURLStaysTextUntilADeclarationSaysOtherwise", () => {
  const bound = readParams("?p.level=20");
  assertEqual(bound.level, "20", "a URL carries no types");
  assertEqual(typeof bound.level, "string", "so the bound value is text");

  const declared = [
    { key: "level", type: "number" },
    { key: "name", type: "text" },
    { key: "live", type: "bool" },
  ];
  const typed = typeParams(declared, { level: "20", name: "20", live: "true" });
  assertEqual(typed.level, 20, "a declared number becomes a number");
  assertEqual(typed.name, "20", "a declared text stays the text it was");
  assertEqual(typed.live, true, "a declared bool becomes a bool");

  const refused = typeParams(declared, { level: "many" });
  assertEqual(refused.level, "many", "an unconvertible value passes through for the server to refuse");
});

check("writingParametersKeepsThePageOwnQueryKeys", () => {
  const search = writeParams({ class: "mage" }, "?tab=graph&p.class=warrior");
  const params = new URLSearchParams(search);
  assertEqual(params.get("tab"), "graph", "the page own key survives");
  assertEqual(params.get(PARAM_PREFIX + "class"), "mage", "the binding is replaced, not appended");
  assertEqual(params.getAll(PARAM_PREFIX + "class").length, 1, "exactly one binding per parameter");
});

// --- The frames ------------------------------------------------------

check("heartbeatCommentsAreNotEvents", () => {
  const raw = ": connected\n\n: ping\n\n: ping\n\n";
  const { frames, rest } = parseFrames(raw);
  assertEqual(frames.length, 0, "a comment line fires nothing");
  assertEqual(rest, "", "and leaves no tail behind");

  const mixed = parseFrames(": ping\n\n" + frame("view.positions", { key: "world" }) + ": ping\n\n");
  assertEqual(mixed.frames.length, 1, "the one real event between two heartbeats");
  assertEqual(mixed.frames[0].kind, "view.positions", "and it is the event, not a ping");

  // A comment line inside a frame is ignored without disturbing the
  // frame around it, which is the other half of "a comment is not a
  // field": the block below still names one event and carries its whole
  // payload.
  const inside = parseFrames(': ping\nevent: view.positions\n: data: {"key":"forged"}\ndata: {"key":"world"}\n\n');
  assertEqual(inside.frames.length, 1, "one event");
  assertEqual(inside.frames[0].kind, "view.positions", "its kind survived the comment lines");
  assertEqual(inside.frames[0].data.key, "world", "and a comment could not forge its payload");
});

check("aFrameSplitAcrossChunksIsOneEvent", () => {
  const whole = frame("view.upserted", { key: "world", version: 2 });
  const first = parseFrames(whole.slice(0, 12));
  assertEqual(first.frames.length, 0, "half a frame is not an event");
  const second = parseFrames(first.rest + whole.slice(12));
  assertEqual(second.frames.length, 1, "the completed frame");
  assertEqual(second.frames[0].data.key, "world", "with its payload");
});

// --- The client ------------------------------------------------------

check("fortyPositionEventsCauseOneReread", async () => {
  const h = await harness();
  const before = h.server.countOf("/views/run");
  for (let i = 0; i < 40; i++) {
    h.stream().write(frame("view.positions", { id: "u1", key: "world" }));
  }
  await settle();
  assertEqual(h.server.countOf("/views/run") - before, 0, "nothing runs inside the window");
  await h.clock.advance(REREAD_DEBOUNCE_MS);
  assertEqual(h.server.countOf("/views/run") - before, 1, "forty events are one re-read");
  assertEqual(h.decisions.length, 40, "and forty decisions were taken");
});

check("aRereadRepeatsTheSameQuestion", async () => {
  const h = await harness();
  // The same question includes how this run wanted staleness handled: a
  // re-read that quietly dropped on_stale would answer a different
  // question from the one on screen, and would do it at the moment a
  // rename made the difference matter.
  await h.client.runView("world", { class: "mage" }, { onStale: "best_effort" });
  assertEqual(h.server.lastBody("/views/run").on_stale, "best_effort", "the run carried it");

  h.stream().write(frame("view.positions", { id: "u1", key: "world" }));
  await settle();
  await h.clock.advance(REREAD_DEBOUNCE_MS);
  const body = h.server.lastBody("/views/run");
  assertEqual(body.key, "world", "the same view");
  assertDeepEqual(body.params, { class: "mage" }, "with the parameters it was run with");
  assertEqual(body.on_stale, "best_effort", "and the same staleness handling");

  // And a run that asked for nothing says nothing: an absent on_stale is
  // the server default, which is not the same statement as naming it.
  await h.client.runView("world", {});
  assert(!("on_stale" in h.server.lastBody("/views/run")), "an unasked-for on_stale is absent");
});

check("aRereadIsDeferredWhileDragging", async () => {
  const h = await harness();
  const before = h.server.countOf("/views/run");
  h.client.setDragging(true);
  h.stream().write(frame("view.positions", { id: "u1", key: "world" }));
  await settle();
  await h.clock.advance(REREAD_DEBOUNCE_MS * 4);
  assertEqual(h.server.countOf("/views/run") - before, 0, "no re-read swaps coordinates under a pointer");
  h.client.setDragging(false);
  await settle();
  assertEqual(h.server.countOf("/views/run") - before, 1, "and it happens the moment the drag ends");
});

check("aRereadIsDeferredWhileAWriteIsUnacknowledged", async () => {
  const h = await harness();
  const before = h.server.countOf("/views/run");
  // A write whose answer the test holds back, so pendingWrites is above
  // zero for exactly as long as this test wants it to be.
  let settleWrite;
  h.server.answer("/positions", new Promise((resolve) => {
    settleWrite = () => resolve(jsonResponse(200, { written: 1 }));
  }));
  const writing = h.client.writePositions("world", [{ type: "zone", key: "elwynn", x: 1, y: 2 }]);
  await settle();
  assertEqual(h.client.state.pendingWrites, 1, "the write is in flight");

  h.stream().write(frame("view.positions", { id: "u1", key: "world" }));
  await settle();
  await h.clock.advance(REREAD_DEBOUNCE_MS * 4);
  assertEqual(h.server.countOf("/views/run") - before, 0, "no re-read reads back a write in flight");

  settleWrite();
  await writing;
  await settle();
  assertEqual(h.client.state.pendingWrites, 0, "the write is acknowledged");
  assertEqual(h.server.countOf("/views/run") - before, 1, "and the deferred re-read runs");
});

check("aHiddenTabDefersUntilVisible", async () => {
  const h = await harness();
  const before = h.server.countOf("/views/run");
  h.client.setHidden(true);
  h.stream().write(frame("view.positions", { id: "u1", key: "world" }));
  await settle();
  await h.clock.advance(REREAD_DEBOUNCE_MS * 4);
  assertEqual(h.server.countOf("/views/run") - before, 0, "a hidden tab is work nobody is looking at");
  h.client.setHidden(false);
  await settle();
  assertEqual(h.server.countOf("/views/run") - before, 1, "and it flushes when the tab comes back");
});

check("anUpsertedEventBandsAndDoesNotFetchThePicture", async () => {
  const h = await harness();
  const before = h.server.countOf("/views/run");
  h.stream().write(frame("view.upserted", { id: "u1", key: "world", version: 7 }));
  await settle();
  await h.clock.advance(REREAD_DEBOUNCE_MS * 4);
  assertEqual(h.decisions.length, 1, "one decision");
  assertEqual(h.decisions[0].decision, BAND, "and it is a band");
  assertEqual(h.server.countOf("/views/run") - before, 0, "the picture is not swapped under the designer");
});

check("aRemovedEventNavigatesNothing", async () => {
  const h = await harness();
  // If the client wrote location.href, this would record it. Nothing in
  // the module touches a global at all, which is what makes the pure
  // half testable without a browser; the assertion is here so a future
  // convenience navigation fails rather than surprises somebody.
  const navigations = [];
  globalThis.location = { get href() { return "/"; }, set href(value) { navigations.push(value); } };
  const before = h.server.countOf("/views/run");
  h.stream().write(frame("view.removed", { id: "u1", key: "world", version: 7 }));
  await settle();
  await h.clock.advance(REREAD_DEBOUNCE_MS * 4);
  assertEqual(h.decisions[0].decision, GONE, "the decision");
  assertEqual(navigations.length, 0, "nothing was auto-navigated");
  assertEqual(h.server.countOf("/views/run") - before, 0, "and nothing was re-run");
  delete globalThis.location;
});

check("theClientNeverPatchesStateFromAPayloadOverTheWire", async () => {
  const first = { nodes: [{ id: "u1", type: "zone", key: "elwynn" }], positions: [{ entity_key: "elwynn", x: 10, y: 20 }] };
  const second = { nodes: [{ id: "u1", type: "zone", key: "elwynn" }], positions: [{ entity_key: "elwynn", x: 30, y: 40 }] };
  const h = await harness({ runResult: first });
  assertDeepEqual(h.client.state.views.world.result, first, "the picture the run returned");

  // A payload carrying plausible coordinates. The server never sends
  // these — viewPlacementEvent is an id and a key — and if it did, they
  // could be older than what this client holds.
  h.server.answer("/views/run", jsonResponse(200, second));
  h.stream().write(frame("view.positions", { id: "u1", key: "world", positions: [{ entity_key: "elwynn", x: 999, y: 999 }] }));
  await settle();
  assertDeepEqual(h.client.state.views.world.result, first, "unchanged until the re-read answers");

  await h.clock.advance(REREAD_DEBOUNCE_MS);
  assertDeepEqual(h.client.state.views.world.result, second, "and then it is the re-read answer");
  assert(!JSON.stringify(h.client.state).includes("999"), "no payload coordinate ever reached the state");
});

check("theClientNeverSendsAnEntityID", async () => {
  const h = await harness();
  // A scene node as a renderer builds one: it carries the envelope id,
  // because the envelope does.
  const node = {
    id: "6f1b5c34-8f0a-4a45-9c2e-2b7c9a1d0e11",
    type: "zone",
    key: "elwynn",
    name: "Elwynn Forest",
    x: 12.5,
    y: -3,
    pinned: true,
  };
  await h.client.writePositions("world", [node]);
  const body = h.server.lastBody("/positions");
  assertEqual(body.positions.length, 1, "one placement");
  const placement = body.positions[0];
  assertDeepEqual(Object.keys(placement).sort(), ["entity_key", "entity_type", "pinned", "x", "y"],
    "the body names an address and coordinates, and nothing else");
  assertEqual(placement.entity_type, "zone", "the type");
  assertEqual(placement.entity_key, "elwynn", "the key");
  const uuidish = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i;
  assert(!uuidish.test(JSON.stringify(body)), `a uuid reached the wire: ${JSON.stringify(body)}`);
  const url = h.server.calls[h.server.calls.length - 1].path;
  assert(!uuidish.test(url), `a uuid reached the path: ${url}`);
});

check("aHeartbeatOverTheWireTakesNoDecision", async () => {
  const h = await harness();
  h.stream().write(": ping\n\n: ping\n\n");
  await settle();
  await h.clock.advance(REREAD_DEBOUNCE_MS * 4);
  assertEqual(h.decisions.length, 0, "a comment is not silence and is not an event either");
  assertEqual(h.server.countOf("/views/run"), 1, "nothing was re-read");
});

check("theStreamReconnectsWhenTheServerClosesIt", async () => {
  const h = await harness();
  assertEqual(h.server.countOf("/events"), 1, "one connection");
  // The server closes every stream after its own lifetime, by design. A
  // client that treated that as the end would look healthy for four
  // minutes and then go quiet.
  h.stream().close();
  await settle();
  assertEqual(h.server.countOf("/events"), 1, "not yet: the backoff has not elapsed");
  await h.clock.advance(60000);
  assertEqual(h.server.countOf("/events"), 2, "a new connection inside the backoff window");

  // And a reconnection re-reads: the hub keeps no history, so whatever
  // was published while this client was away is simply gone.
  await h.clock.advance(REREAD_DEBOUNCE_MS);
  assertEqual(h.server.countOf("/views/run"), 2, "the reconnect re-read the picture");
  h.client.disconnect();
});

check("disconnectingCancelsAReadNobodyIsWaitingFor", async () => {
  const h = await harness();
  const before = h.server.countOf("/views/run");
  h.stream().write(frame("view.positions", { id: "u1", key: "world" }));
  await settle();
  h.client.disconnect();
  await h.clock.advance(REREAD_DEBOUNCE_MS * 4);
  assertEqual(h.server.countOf("/views/run") - before, 0, "a timer must not fire into a closed surface");
  assertEqual(h.clock.pendingCount(), 0, "and the timer itself was cancelled, not merely ignored");
});

check("errorSentencesAreRenderedVerbatim", async () => {
  const h = await harness();
  // The server body, exactly as internal/web's writeCodedError composes
  // it for a stale query. Every character of the message and the pointer
  // below belongs to internal/views; this client hands them on and adds
  // nothing.
  const message = "the type \"zone\" this query names was renamed to \"region\": " +
    "repair the query, or re-run with on_stale: \"best_effort\"";
  const pointer = "/from/0/type";
  h.server.answer("/views/run", jsonResponse(409, {
    error: "query_stale",
    message,
    details: {
      fields: [{ path: pointer, message }],
      stale: [{ code: "type_renamed", pointer, was: "zone", now: "region" }],
    },
  }));
  const answer = await h.client.runView("world", {});
  assertEqual(answer.ok, false, "the run was refused");
  assertEqual(answer.error.code, "query_stale", "the server own code");
  assertEqual(answer.error.message, message, "the server own message, character for character");
  assertEqual(answer.error.pointer, pointer, "the server own pointer");
  assertEqual(answer.error.status, 409, "and the status it came with");
  assertDeepEqual(answer.error.details.stale, [{ code: "type_renamed", pointer, was: "zone", now: "region" }],
    "the whole details object travels, so a refusal naming several problems keeps them all");
  // And the client adds no prose of its own anywhere in the answer: the
  // only string in it carrying a space is the one the server wrote.
  const invented = [];
  walkStrings(answer, (value) => {
    if (/\s/.test(value) && value !== message) invented.push(value);
  });
  assertDeepEqual(invented, [], "strings this client composed rather than received");
});

check("anUnreadableAnswerGetsNoInventedSentence", async () => {
  const h = await harness();
  h.server.answer("/views/run", {
    ok: false,
    status: 502,
    json: () => Promise.reject(new Error("not json")),
  });
  const answer = await h.client.runView("world", {});
  assertEqual(answer.ok, false, "refused");
  assertEqual(answer.error.code, "", "no code was invented");
  assertEqual(answer.error.message, "", "and no sentence was invented");
  assertEqual(answer.error.status, 502, "the status is all this client honestly knows");
});

check("aRenameOverTheWireReadsTheRowAndDoesNotRunTheQuery", async () => {
  const h = await harness();
  h.server.answer("/views/by-key/world", jsonResponse(200, { key: "world" }));
  h.stream().write(frame("type.renamed", { id: "u1", from: "zone", to: "region" }));
  await settle();
  await h.clock.advance(REREAD_DEBOUNCE_MS);
  assertEqual(h.server.countOf("/views/run"), 1, "the query was not silently re-run");
  assertEqual(h.server.countOf("/views/by-key/world"), 1, "the view row was re-read");
  assertDeepEqual(h.client.state.views.world.row, { key: "world" }, "and the row the server sent is what landed");
});

// --- Run -------------------------------------------------------------

for (const [name, fn] of pending) {
  try {
    await fn();
    console.log("ok   " + name);
  } catch (err) {
    failures++;
    console.error("FAIL " + name + ": " + (err && err.message ? err.message : err));
  }
}

if (failures > 0) {
  console.error(`${failures} client check(s) failed`);
  process.exit(1);
}
console.log("client.js: all checks passed");
