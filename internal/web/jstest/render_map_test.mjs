// The harness for the `map` renderer: internal/web/static/render/map.js,
// over the drawing vocabulary it shares with the other five
// (internal/web/static/render/marks.js, render/controls.js), the frame's
// own bands (render/scene.js) and the one reader of a saved arrangement
// (internal/web/static/positions.js).
//
// What this layer covers that no Go test can, and what is this
// renderer's alone.
//
// **That a node with no coordinate is never at the origin.** `map` is
// the one renderer whose coordinates are *required*, and the two ways a
// node can fail to have one — a declared field that is absent, and a
// saved arrangement that has no row — both end on the shelf. `(0,0)` is
// a place a designer may deliberately have used, which is why
// internal/views/execute.go refuses to return "unplaced" as a
// coordinate; a picture that put an unplaceable node there would invent
// a position and let a designer drag and save it. So the assertion is
// not "the node is on the shelf" alone: it is that **no mark in the
// whole scene sits at (0,0)**.
//
// **That a fresh manual map is not the empty state.** This is the
// product's first-run experience for its best feature: the query
// matched, the answer is full, and the only thing missing is the
// designer's own work. The generic *"This view matched nothing"* would
// say the opposite of all three. Both halves are asserted — the map's
// own sentence is present, and the frame's is absent — because a
// renderer that said both would still pass a test for either.
//
// **That a background can go away without taking the map with it.**
// `background_asset_id` is ON DELETE SET NULL, so a view losing its
// ground is an ordinary transition between two runs; every coordinate is
// asserted unchanged across it, which is the fact a designer needs and
// the one a re-fit would quietly destroy.
//
// **That the grid is `snap`'s and manual mode's**, at 1x zoom and above.
//
// Run directly: `node internal/web/jstest/render_map_test.mjs`.
// internal/web/static_appjs_browser_test.go shells out to it too.

import { addressOf } from "../static/address.js";
import {
  BANNER_BACKGROUND_MISSING,
  BANNER_PLACED,
  BANNER_SHELVED,
  KIND_EMPTY,
  KIND_PICTURE,
  MARK_ELEMENTS,
  MARK_IMAGE,
  MARK_ORIGINS,
  bannersFor,
  footerFor,
  frameFor,
} from "../static/render/scene.js";
import { twinFor } from "../static/render/twin.js";
import { controlNamed } from "../static/render/controls.js";
import { SOURCE_COMPUTED, SOURCE_STORED } from "../static/positions.js";
import {
  ABSENT_DASH,
  CLASS_BACKGROUND,
  CLASS_CHIP_ABSENT,
  CLASS_CHIP_LABEL,
  CLASS_EDGE,
  CLASS_GRID,
  CLASS_POINT,
  CLASS_POINT_LABEL,
  CLASS_POINT_UNPLACED,
  CLASS_STUB_RING,
  LABEL_HALO,
  LABEL_HALO_WIDTH,
  POINT_FILL,
  UNFILLED,
} from "../static/render/marks.js";
import {
  CONTROLS,
  COORDINATES_FIELDS,
  COORDINATES_MANUAL,
  FIRST_RUN_TEXT,
  PARAM_COORDINATE_SOURCE,
  PARAM_SNAP,
  RENDERER,
  mapScene,
  shelfCaption,
} from "../static/render/map.js";

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

// --- Fixtures --------------------------------------------------------

function node(key, attrs = {}, extra = {}) {
  return { id: `id-${key}`, type: "zone", key, name: `Zone ${key}`, attrs, ...extra };
}

function edge(from, to, type = "connects_to") {
  return { id: `e-${from}-${to}`, type, source: `id-${from}`, target: `id-${to}` };
}

function envelopeOf(nodes, edges = [], extra = {}) {
  return {
    nodes,
    edges,
    stats: { nodes: nodes.length, edges: edges.length, max_depth_reached: 1, duration_ms: 4 },
    truncated: { nodes: false, edges: false, depth: false },
    stale: [],
    ...extra,
  };
}

// A saved arrangement, in the envelope's own spelling — the one
// internal/views.Position marshals and views.set_positions takes.
function position(key, x, y, pinned = true) {
  return { entity_type: "zone", entity_key: key, x, y, pinned, updated_at: "2026-09-06T00:00:00Z" };
}

function address(key) {
  return addressOf({ type: "zone", key });
}

// A background this instance would serve: a same-origin absolute path,
// which is the whole legitimate set render/scene.js's isDrawableHref
// admits.
const GROUND = {
  href: "/api/games/azeroth/views/assets/11111111-1111-1111-1111-111111111111",
  scale: 2,
  offset: { x: -100, y: -50 },
  width: 400,
  height: 300,
};

function marksOfClass(result, className) {
  return result.marks.filter((mark) => mark.class === className);
}

// pointsOf is every coordinate pair a mark carries, through the same
// MARK_ORIGINS the drag layer translates by — so a mark kind added later
// is covered by the origin assertion without anybody remembering to add
// it here.
function pointsOf(mark) {
  return (MARK_ORIGINS[mark.kind] || []).map(([x, y]) => [mark[x], mark[y]]);
}

// --- A coordinate that is not there ----------------------------------

check("aNodeWithNoCoordinateFieldGoesToTheShelfNotTheOrigin", () => {
  // "fields" mode: the game states the position. `zone/lost` declares
  // neither number.
  const envelope = envelopeOf([
    node("here", { label: "Here", east: 120, north: 40 }),
    node("lost", { label: "Lost" }),
  ]);
  const result = mapScene(envelope, {
    [PARAM_COORDINATE_SOURCE]: COORDINATES_FIELDS,
    x_field: "east",
    y_field: "north",
  });

  // **The origin scan comes first**, and the order is the assertion.
  // Written after the two below it never runs: the mutation this test
  // exists against — a missing coordinate defaulted to 0 — fails the
  // shelf comparison as well, so the scan would be correct and never
  // executed, which is exactly the defect Task 8's own ambiguity check
  // found in itself.
  for (const mark of result.marks) {
    for (const [x, y] of pointsOf(mark)) {
      assert(
        !(x === 0 && y === 0),
        `no mark sits at (0,0): a ${mark.kind} of class ${JSON.stringify(mark.class)} does`,
      );
    }
  }

  assertDeepEqual(result.shelf, [{ key: address("lost"), label: "Lost" }], "the node with no coordinate is on the shelf");
  assertDeepEqual(result.placed, [address("here")], "and only the one with a coordinate has a pin");

  // The shelf chip is dashed — render/marks.js's one spelling for
  // "something this box needs is not here" — and carries the name, so a
  // designer can find the thing that has nowhere to go.
  const chips = marksOfClass(result, CLASS_CHIP_ABSENT);
  assertEqual(chips.length, 1, "one chip for the one shelved node");
  assertEqual(chips[0].dash, ABSENT_DASH, "and it is dashed");
  assertEqual(chips[0].key, address("lost"), "and it is addressed");
  const labels = marksOfClass(result, CLASS_CHIP_LABEL);
  assertEqual(labels[0].text, "Lost", "the chip names the node");

  // And it is counted in a band, in words.
  const banners = bannersFor(envelope, { shelved: result.shelf.length });
  const band = banners.find((banner) => banner.code === BANNER_SHELVED);
  assert(band !== undefined, "the shelf is banded");
  assert(
    band.text.includes("1 node has no coordinate"),
    `and the band counts it in the singular: ${band.text}`,
  );
  // A map with nothing on the shelf says nothing about shelves, which is
  // render/scene.js's own first rule.
  assertEqual(
    bannersFor(envelope, { shelved: 0 }).filter((b) => b.code === BANNER_SHELVED).length,
    0,
    "and a map with nothing shelved is silent",
  );
});

check("aNodeMissingOnlyYIsAlsoShelved", () => {
  // Half a coordinate is not half a position. A renderer that used the
  // origin for the missing half would put this node on an axis nobody
  // chose — and it would look placed, which is worse than looking
  // missing.
  const envelope = envelopeOf([
    node("here", { label: "Here", east: 120, north: 40 }),
    node("halfx", { label: "Half X", east: 60 }),
    node("halfy", { label: "Half Y", north: 60 }),
  ]);
  const result = mapScene(envelope, {
    [PARAM_COORDINATE_SOURCE]: COORDINATES_FIELDS,
    x_field: "east",
    y_field: "north",
  });
  assertDeepEqual(
    result.shelf.map((entry) => entry.key).sort(),
    [address("halfx"), address("halfy")].sort(),
    "both halves are shelved, neither is placed",
  );
  assertDeepEqual(result.placed, [address("here")], "and only the complete one is on the map");

  // A coordinate of zero is a coordinate. This is the other half of the
  // same rule and the reason the reader tests presence rather than
  // truthiness: a node at the origin the *game* declared is placed.
  const atOrigin = mapScene(
    envelopeOf([node("zero", { label: "Zero", east: 0, north: 0 })]),
    { [PARAM_COORDINATE_SOURCE]: COORDINATES_FIELDS, x_field: "east", y_field: "north" },
  );
  assertDeepEqual(atOrigin.shelf, [], "a declared 0,0 is a place, not an absence");
  assertDeepEqual(atOrigin.placed, [address("zero")], "and it gets a pin");
});

check("declaredCoordinatesAreDrawnAsTheGameWroteThem", () => {
  // The declared fields are not guaranteed to be in any particular
  // range, and this renderer does not put them in one: rescaling to fit
  // the window would make the same view a different map every time a
  // single node's number changed, and would make two designers looking
  // at the same coordinates see different pictures.
  const envelope = envelopeOf([
    node("far", { label: "Far", east: 100000, north: -50 }),
    node("near", { label: "Near", east: 12.5, north: 7 }),
  ]);
  const result = mapScene(envelope, {
    [PARAM_COORDINATE_SOURCE]: COORDINATES_FIELDS,
    x_field: "east",
    y_field: "north",
  });
  const pin = (key) => marksOfClass(result, CLASS_POINT).find((mark) => mark.key === address(key));
  assertEqual(pin("far").cx, 100000, "the game's own x, exactly");
  assertEqual(pin("far").cy, -50, "and its y, negative and untouched");
  assertEqual(pin("near").cx, 12.5, "and a fractional one is not rounded to a grid");
});

// --- The first run ---------------------------------------------------

check("aFreshManualMapIsNotTheEmptyState", () => {
  // Three nodes, a background, and nobody has dragged anything. The
  // query matched; the map is empty of the designer's work and of
  // nothing else.
  const envelope = envelopeOf([node("a"), node("b"), node("c")], [], { positions: [] });
  // **The composition is present and computed a coordinate for every
  // one of them**, which is what gives this test teeth: a page that
  // composed before it knew the arrangement was empty hands the renderer
  // three perfectly good coordinates, and a renderer that used them
  // would scatter the map and then tell the designer that nothing has
  // been placed yet. Without this the rule is correct and unasserted —
  // the fixture would pass for a renderer that had no such rule at all.
  const layout = {
    placements: [
      { key: address("a"), x: 40, y: 40, pinned: false, source: SOURCE_COMPUTED },
      { key: address("b"), x: 140, y: 40, pinned: false, source: SOURCE_COMPUTED },
      { key: address("c"), x: 240, y: 40, pinned: false, source: SOURCE_COMPUTED },
    ],
  };
  const result = mapScene(envelope, {}, { background: GROUND, layout });

  assertEqual(result.shelf.length, 3, "every node is on the shelf");
  assertDeepEqual(result.placed, [], "and none of them has a pin");
  assertEqual(result.background.drawn, true, "the background is still drawn");
  assertEqual(marksOfClass(result, CLASS_BACKGROUND).length, 1, "as one image mark");
  assert(result.firstRun !== null, "and the map says what to do");
  assertEqual(result.firstRun.text, FIRST_RUN_TEXT, "in the sentence §4.6 names");

  // The other half, and the one that makes this test mean something: the
  // frame's generic empty state is **absent**. A renderer that said both
  // would pass an assertion for either.
  const frame = frameFor({ envelope, view: { renderer: RENDERER } });
  assertEqual(frame.kind, KIND_PICTURE, "this is a picture, not the empty state");
  assert(frame.kind !== KIND_EMPTY, "and emphatically not the empty answer");
  assertEqual(frame.message, "", "so the frame says nothing about matching nothing");
  assert(
    !frame.message.includes("matched nothing"),
    `and never the generic sentence: ${JSON.stringify(frame.message)}`,
  );

  // An answer that really matched nothing is still the empty state: this
  // renderer's sentence is about an arrangement, not about a result.
  const nothing = mapScene(envelopeOf([], [], { positions: [] }), {});
  assertEqual(nothing.firstRun, null, "an empty answer has nothing to place");
  assertEqual(frameFor({ envelope: envelopeOf([], []) }).kind, KIND_EMPTY, "and the frame says so");

  // And "fields" mode never says it: the game declares those
  // coordinates, so telling a designer to drag would send them somewhere
  // the picture cannot go.
  const declared = mapScene(envelopeOf([node("a")]), {
    [PARAM_COORDINATE_SOURCE]: COORDINATES_FIELDS,
    x_field: "east",
    y_field: "north",
  });
  assertEqual(declared.firstRun, null, "a fields map with nothing placed is not a fresh map");
  assertEqual(declared.shelf.length, 1, "its node is still shelved");
});

check("aNewEntityInASavedArrangementIsPlacedUnpinnedAndCounted", () => {
  // A saved arrangement exists — two nodes were dragged — and twelve
  // entities have since been added to the game. They are placed by the
  // client, drawn hollow, and counted, which is what tells a
  // manual-mode designer there is arranging to do.
  const fresh = Array.from({ length: 12 }, (_, i) => node(`new${i}`));
  const envelope = envelopeOf([node("old1"), node("old2"), ...fresh], [], {
    positions: [position("old1", 10, 10), position("old2", 60, 10)],
  });
  const layout = {
    placements: [
      { key: address("old1"), x: 10, y: 10, pinned: true, source: SOURCE_STORED },
      { key: address("old2"), x: 60, y: 10, pinned: true, source: SOURCE_STORED },
      ...fresh.map((n, i) => ({
        key: address(n.key),
        x: 200 + i * 20,
        y: 200,
        pinned: false,
        source: SOURCE_COMPUTED,
      })),
    ],
  };
  const result = mapScene(envelope, {}, { layout });

  assertEqual(result.placed.length, 14, "everything is on the map");
  assertDeepEqual(result.shelf, [], "and nothing is on the shelf");
  assertEqual(result.automatic, 12, "twelve of them are at a coordinate nobody chose");
  assertEqual(result.firstRun, null, "and this is not a fresh map");

  const hollow = marksOfClass(result, CLASS_POINT_UNPLACED);
  assertEqual(hollow.length, 12, "twelve hollow anchors");
  assertEqual(hollow[0].fill, UNFILLED, "a hollow anchor has nothing in it");
  const solid = marksOfClass(result, CLASS_POINT);
  assertEqual(solid.length, 2, "and two solid ones, for the coordinates a designer chose");
  assertEqual(solid[0].fill, POINT_FILL, "which are filled");

  // The band §4.2 asks for, in the frame's own words.
  const banners = bannersFor(envelope, { placedAutomatically: result.automatic });
  const band = banners.find((banner) => banner.code === BANNER_PLACED);
  assert(band !== undefined, "the automatic placement is banded");
  assertEqual(band.text, "12 new nodes were placed automatically.", "and counted");
});

// **A pinned anchor and an unpinned one are two different marks**, which
// is the one distinction the mixed layout mode is entirely about and
// which the drawing did not carry.
//
// Found by opening a manual map in a browser whose stored rows
// alternated `pinned: true` and `pinned: false`: all 105 drawn anchors
// came back identical — `class="point"`, `fill: var(--ink)`, `r: 3.5` —
// so *which nodes did I place and which did the engine* was written,
// stored, returned in `positions[]` and never drawn. The check that
// existed (above) only ever fed pinned rows, so it could not fail.
//
// The ring is reused rather than a third mark invented, because the two
// facts are one fact from a designer's side: a coordinate the client
// computed and a stored coordinate nobody is holding are both *nobody
// put this here*.
check("aStoredPositionThatIsNotPinnedIsDrawnAsTheEnginesAndNotAsADesignersOwn", () => {
  const nodes = [node("held"), node("released")];
  const envelope = envelopeOf(nodes, [], {
    positions: [position("held", 10, 10, true), position("released", 60, 10, false)],
  });
  const result = mapScene(envelope, {}, {});

  assertEqual(result.placed.length, 2, "both are on the map: unpinning moves nothing");
  assertDeepEqual(result.shelf, [], "and neither is shelved");

  const solid = marksOfClass(result, CLASS_POINT);
  const hollow = marksOfClass(result, CLASS_POINT_UNPLACED);
  assertEqual(solid.length, 1, "one anchor is a designer's own");
  assertEqual(hollow.length, 1, "and one is the engine's");
  assertEqual(solid[0].key, address("held"), "the pinned row is the solid one");
  assertEqual(hollow[0].key, address("released"), "and the unpinned row is the ring");
  assertEqual(solid[0].fill, POINT_FILL, "a designer's anchor is filled");
  assertEqual(hollow[0].fill, UNFILLED, "and the engine's has nothing in it");
  assert(solid[0].stroke !== hollow[0].stroke, "and the two are told apart by more than the fill");

  // **And the band is not the rings.** The mark answers "who put this
  // here"; the band answers "how many nodes are new to this
  // arrangement", and they are different questions about different
  // sets. Reading one off the other announced *"53 new nodes were placed
  // automatically"* over an arrangement in which nothing was new — seen
  // on screen one reload after the ring was fixed, which is this
  // repository's most repeated defect: a rule established correctly and
  // not carried one step along.
  assertEqual(result.automatic, 0, "nothing here is new: both nodes have a stored row");
  assertEqual(
    bannersFor(envelope, { placedAutomatically: result.automatic }).find(
      (banner) => banner.code === BANNER_PLACED,
    ),
    undefined,
    "so no band claims anything was placed automatically",
  );

  // The control: with both rows pinned there is no ring at all, so a
  // change that drew every anchor hollow would fail here rather than
  // pass both directions of the assertion above.
  const bothHeld = mapScene(
    envelopeOf(nodes, [], {
      positions: [position("held", 10, 10, true), position("released", 60, 10, true)],
    }),
    {},
    {},
  );
  assertEqual(marksOfClass(bothHeld, CLASS_POINT).length, 2, "two pinned rows are two solid anchors");
  assertEqual(marksOfClass(bothHeld, CLASS_POINT_UNPLACED).length, 0, "and no rings");
});

// --- The ground ------------------------------------------------------

check("aRemovedBackgroundKeepsEveryCoordinate", () => {
  // The same view, twice: once with its ground and once after the asset
  // was deleted. ON DELETE SET NULL makes this an ordinary transition
  // between two runs rather than a fault.
  const envelope = envelopeOf([node("a"), node("b")], [], {
    positions: [position("a", 30, 40), position("b", 90, 120)],
  });
  const before = mapScene(envelope, {}, { background: GROUND });
  const after = mapScene(envelope, {}, { background: { ...GROUND, href: null } });

  assertEqual(before.background.drawn, true, "the first run has a ground");
  assertEqual(before.background.missing, false, "and nothing to report about it");
  assertEqual(after.background.drawn, false, "the second draws the plain ground");
  assertEqual(after.background.missing, true, "and reports the image is gone");
  assertEqual(
    after.marks.filter((mark) => mark.kind === MARK_IMAGE).length,
    0,
    "with no image mark at all",
  );

  // The coordinates are the fact a designer needs: positions are per
  // view and were never anchored to the image.
  const pinAt = (result, key) => {
    const mark = result.marks.find(
      (m) => m.key === address(key) && (m.class === CLASS_POINT || m.class === CLASS_POINT_UNPLACED),
    );
    return [mark.cx, mark.cy];
  };
  assertDeepEqual(pinAt(after, "a"), pinAt(before, "a"), "zone/a has not moved");
  assertDeepEqual(pinAt(after, "b"), pinAt(before, "b"), "nor has zone/b");
  assertDeepEqual(pinAt(after, "a"), [30, 40], "and they are the saved coordinates");

  // The frame carries the line, and says the second half out loud,
  // because an image that vanished looks exactly like an arrangement
  // that vanished with it.
  const band = bannersFor(envelope, { backgroundMissing: after.background.missing }).find(
    (banner) => banner.code === BANNER_BACKGROUND_MISSING,
  );
  assert(band !== undefined, "the removed background is banded");
  assert(band.text.includes("was removed"), `and says so: ${band.text}`);
  assert(band.text.includes("keeps the coordinate"), `and says the arrangement survived: ${band.text}`);

  // A view that names no ground says nothing at all about grounds, which
  // is not the same statement as "the ground is gone".
  const never = mapScene(envelope, {});
  assertEqual(never.background.missing, false, "a map with no background reports nothing");
  assertEqual(
    bannersFor(envelope, { backgroundMissing: never.background.missing }).filter(
      (banner) => banner.code === BANNER_BACKGROUND_MISSING,
    ).length,
    0,
    "and is not banded",
  );
});

check("aBackgroundThisInstanceWouldNotFetchIsNotDrawnAndIsNotSilent", () => {
  // `<image>` is the one mark field a browser resolves rather than
  // draws, so render/scene.js admits same-origin absolute paths and
  // nothing else. An href outside that set draws nothing — and is
  // reported, because a ground that is simply missing from the picture
  // looks exactly like a view that never had one.
  const envelope = envelopeOf([node("a")], [], { positions: [position("a", 5, 5)] });
  for (const href of ["javascript:alert(1)", "//example.test/map.png", "https://example.test/m.png", ""]) {
    const result = mapScene(envelope, {}, { background: { ...GROUND, href } });
    assertEqual(
      result.marks.filter((mark) => mark.kind === MARK_IMAGE).length,
      0,
      `${JSON.stringify(href)} draws no image`,
    );
    assertEqual(result.background.drawn, false, `${JSON.stringify(href)} is not a ground`);
    assertEqual(result.background.missing, true, `and ${JSON.stringify(href)} is reported`);
  }
  const good = mapScene(envelope, {}, { background: GROUND });
  assertEqual(good.background.drawn, true, "and the instance's own asset path draws");
  const image = good.marks.find((mark) => mark.kind === MARK_IMAGE);
  assertEqual(image.href, GROUND.href, "at the href it was given");
  assertEqual(image.opacity, 1, "at full opacity, which is §4.6's own word");
  // The scale multiplies the asset's own pixels and the offset places
  // its corner: both are columns of the view, written with
  // views.set_background, and neither is a renderer parameter.
  assertEqual(image.x, -100, "the offset places the corner");
  assertEqual(image.y, -50, "in both axes");
  assertEqual(image.w, 800, "and the scale multiplies the asset's width");
  assertEqual(image.h, 600, "and its height");
});

// --- The grid --------------------------------------------------------

check("snapIsIgnoredOutsideManualMode", () => {
  // Two nodes far enough apart that a 25px grid really has lines to
  // draw across them: a one-node fixture spans less than one cell, where
  // "no grid" and "a grid with nothing in view" look alike and the test
  // would pass for a renderer that never drew one.
  const envelope = envelopeOf(
    [node("a", { label: "A", east: 0, north: 0 }), node("b", { label: "B", east: 200, north: 160 })],
    [],
    { positions: [position("a", 0, 0), position("b", 200, 160)] },
  );
  const manual = mapScene(envelope, { [PARAM_SNAP]: 25 }, { zoom: 1 });
  assert(marksOfClass(manual, CLASS_GRID).length > 0, "manual mode draws the grid");
  assertEqual(manual.grid.drawn, true, "and says so");

  // In "fields" mode the catalogue refuses the parameter outright: a
  // node's coordinates come off its declared fields, nothing is dragged,
  // and a grid size changes no picture. A client that drew it would be
  // drawing a knob the server would not have stored.
  const fields = mapScene(
    envelope,
    { [PARAM_COORDINATE_SOURCE]: COORDINATES_FIELDS, x_field: "east", y_field: "north", [PARAM_SNAP]: 25 },
    { zoom: 1 },
  );
  assertEqual(marksOfClass(fields, CLASS_GRID).length, 0, "fields mode draws none");
  assertEqual(fields.grid.drawn, false, "and says so");
  // And the mode is what decided it, not the absence of a coordinate:
  // the fields fixture really is drawing a pin.
  assertEqual(fields.placed.length, 2, "with pins on the map all the same");

  // Zero is the catalogue's own "no grid", and it is not a grid of one
  // line at the origin.
  const off = mapScene(envelope, { [PARAM_SNAP]: 0 }, { zoom: 1 });
  assertEqual(marksOfClass(off, CLASS_GRID).length, 0, "snap 0 draws no grid");
});

check("theGridHidesBelowOneTimesZoom", () => {
  const envelope = envelopeOf([node("a"), node("b")], [], {
    positions: [position("a", 0, 0), position("b", 200, 160)],
  });
  const at = (zoom) => mapScene(envelope, { [PARAM_SNAP]: 20 }, { zoom });

  assertEqual(at(0.9).grid.drawn, false, "below 1x there is no grid");
  assertEqual(marksOfClass(at(0.9), CLASS_GRID).length, 0, "and no line drawn for it");
  assertEqual(at(1).grid.drawn, true, "at exactly 1x there is");
  assert(marksOfClass(at(1), CLASS_GRID).length > 0, "and the lines are drawn");
  assert(marksOfClass(at(4), CLASS_GRID).length > 0, "and above it too");

  // The grid is under the background, which is what keeps it from
  // competing with a designer's own image.
  const over = mapScene(envelope, { [PARAM_SNAP]: 20 }, { zoom: 1, background: GROUND });
  const grid = marksOfClass(over, CLASS_GRID);
  const image = over.marks.findIndex((mark) => mark.kind === MARK_IMAGE);
  const lastGrid = over.marks.reduce((at, mark, i) => (mark.class === CLASS_GRID ? i : at), -1);
  assert(grid.length > 0 && image >= 0, "the fixture draws both");
  assert(lastGrid < image, "every grid line is drawn before the image, so the image paints over it");
  assertEqual(grid[0].layer, "image", "and they share the layer under the edges");
});

// --- Labels ----------------------------------------------------------

check("labelHalosAreEmittedForEveryLabel", () => {
  // A label on a map has to survive over a picture this interface did
  // not choose, and the halo is the whole of that answer (§4.6). Every
  // one of them carries it: a single label without is one name lost over
  // the one dark region of somebody's map.
  const nodes = Array.from({ length: 6 }, (_, i) => node(`n${i}`, { label: `Place ${i}` }));
  const envelope = envelopeOf(nodes, [], {
    positions: nodes.map((n, i) => position(n.key, i * 30, i * 20)),
  });
  const result = mapScene(envelope, {}, { background: GROUND });
  const labels = marksOfClass(result, CLASS_POINT_LABEL);
  assertEqual(labels.length, 6, "one label per pin");
  for (const label of labels) {
    assertEqual(label.halo, LABEL_HALO, `${label.text} carries the halo`);
    assertEqual(label.haloWidth, LABEL_HALO_WIDTH, "at the width §4.6 names");
    assert(label.text !== "", "and says something");
  }
  // Offset up and to the right of the disc, so it never sits on the mark
  // it names.
  const pin = marksOfClass(result, CLASS_POINT)[0];
  const label = labels.find((mark) => mark.key === pin.key);
  assert(label.x > pin.cx, "the label is to the right of its pin");
  assert(label.y < pin.cy, "and above it");
});

// --- The twin, and the emitter's contract ----------------------------

check("thePictureAndTheTwinDescribeTheSameAnswer", () => {
  // The twin is built from the envelope alone and the scene from the
  // envelope and an arrangement; two descriptions of one answer that
  // disagree is the defect this ordering exists to catch.
  //
  // The fixture carries every way this renderer can meet an edge: one
  // drawn between two pins, one whose far end is on the shelf, one that
  // leaves the picture, and one from an entity to itself.
  const envelope = envelopeOf(
    [node("a"), node("b"), node("shelved"), node("c")],
    [edge("a", "b"), edge("a", "shelved"), edge("b", "gone"), edge("c", "c")],
    { positions: [position("a", 0, 0), position("b", 90, 30), position("c", 40, 90)] },
  );
  const result = mapScene(envelope, {});
  const twin = twinFor(envelope);

  assertEqual(twin.nodes.rows.length, 4, "the twin has every node");
  assertEqual(
    result.placed.length + result.shelf.length,
    twin.nodes.rows.length,
    "every row the twin has is a pin on the map or a chip on the shelf",
  );
  assertDeepEqual(
    [...result.placed, ...result.shelf.map((entry) => entry.key)].sort(),
    twin.nodes.rows.map((row) => row.key).sort(),
    "and they are the same nodes, by the same address",
  );

  const lines = marksOfClass(result, CLASS_EDGE).length;
  assertEqual(lines, 1, "one edge has both ends on the map");
  assertEqual(result.offMap, 1, "one has an end on the shelf");
  assertEqual(result.stubs.total, 1, "one leaves the picture");
  assertEqual(result.loops, 1, "and one is a relation to itself");
  assertEqual(
    lines + result.offMap + result.stubs.total + result.loops,
    twin.edges.rows.length,
    "every edge row is a line, an end on the shelf, a stub or a loop",
  );

  // The label a pin wears and the cell its row carries are the same
  // characters, because both go through the palette's own labelFor.
  const named = twinFor(envelopeOf([node("a", { label: "Elwynn Forest" })]));
  const scene = mapScene(
    envelopeOf([node("a", { label: "Elwynn Forest" })], [], { positions: [position("a", 1, 1)] }),
    {},
  );
  assertEqual(
    marksOfClass(scene, CLASS_POINT_LABEL)[0].text,
    named.nodes.rows[0].cells.find((cell) => cell.column === "label").text,
    "a pin's label and its twin row's cell are one string",
  );

  // The footer counts the answer's edges that leave the picture, as
  // every renderer does.
  const footer = footerFor(envelope, { outside: result.stubs.total });
  assert(
    footer.text.includes("1 edge leads outside this picture"),
    `and the strip says so, in the singular: ${footer.text}`,
  );
  // A stub is drawn as a line out of its known end, ending in a ring.
  assertEqual(marksOfClass(result, CLASS_STUB_RING).length, 1, "the stub has a terminus");
});

check("everyMarkIsAKindTheContractNamesAndCanBeDragged", () => {
  const nodes = [node("a", { label: "A" }), node("b", { label: "B" }), node("lost", { label: "Lost" })];
  const envelope = envelopeOf(nodes, [edge("a", "b"), edge("b", "away")], {
    positions: [position("a", 0, 0), position("b", 120, 80)],
  });
  const result = mapScene(envelope, { [PARAM_SNAP]: 40 }, { zoom: 2, background: GROUND });
  assert(result.marks.length > 10, "the fixture draws every kind of thing this renderer has");
  for (const mark of result.marks) {
    assert(
      Object.prototype.hasOwnProperty.call(MARK_ELEMENTS, mark.kind),
      `mark kind ${JSON.stringify(mark.kind)} is one the emitter knows; anything else is dropped silently`,
    );
    assert(
      Array.isArray(MARK_ORIGINS[mark.kind]) && MARK_ORIGINS[mark.kind].length > 0,
      `mark kind ${JSON.stringify(mark.kind)} has coordinate pairs the drag layer can translate`,
    );
    for (const [field, value] of Object.entries(mark)) {
      if (field === "kind" || field === "layer") continue;
      assert(
        value === undefined || typeof value !== "object",
        `${field} on a ${mark.kind} is a scalar the emitter can write`,
      );
    }
  }
  // The shelf names itself and counts itself where the chips are, which
  // is §4.2's "with a count and a banner".
  const caption = result.marks.find((mark) => mark.text === shelfCaption(1));
  assert(caption !== undefined, "the shelf strip is captioned with its count");
});

check("anEmptyAnswerIsAnEmptyScene", () => {
  const result = mapScene(envelopeOf([], []), {});
  assertDeepEqual(result.marks, [], "no marks");
  assertEqual(result.legend, null, "no legend");
  assertDeepEqual(result.shelf, [], "and nothing on the shelf");
  assertEqual(result.firstRun, null, "and nothing to say about placing");
});

check("theControlsTeachWhatTheCatalogueDoesNot", () => {
  assertEqual(RENDERER, "map", "the module names the renderer it is");
  const source = controlNamed(CONTROLS, PARAM_COORDINATE_SOURCE);
  assert(source !== null, "coordinate_source has a control");
  assertDeepEqual(source.values, [COORDINATES_MANUAL, COORDINATES_FIELDS], "offering the two modes in order");
  // The sentence a designer needs is what each mode does to the
  // *picture*: one has a shelf and can be dragged, the other cannot.
  assert(source.tooltip.includes("shelf"), "whose sentence names the shelf");
  assert(source.tooltip.includes("dragged"), "and says which mode is draggable");
  const snap = controlNamed(CONTROLS, PARAM_SNAP);
  assert(snap.tooltip.includes("manual"), "and snap says where it is read");
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
  console.error(`${failures} map renderer check(s) failed`);
  process.exit(1);
}
console.log("map.js: all checks passed");
