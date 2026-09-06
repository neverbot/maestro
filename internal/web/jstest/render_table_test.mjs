// The harness for the `table` renderer:
// internal/web/static/render/table.js, over the cell vocabulary it
// shares with the text twin (internal/web/static/render/twin.js) and the
// controls every renderer declares (render/controls.js).
//
// What this layer covers that no Go test can, and what is this
// renderer's alone.
//
// **That an absent value and the empty string stay two answers, in the
// one renderer where a reader meets them side by side.** An absent value
// is an em dash and the empty string is a blank cell, and the pair of
// tests is the assertion: a renderer that wrote `""` for both passes
// either one alone. This is internal/views/execute.go's own distinction,
// preserved end to end, and a table is the last place it could be thrown
// away.
//
// **That a page count is never a content count.** views.run has no
// cursor — a page of a graph is not a graph — so `page_size` pages the
// rows the client already holds and the pager says `capped` when the
// answer itself hit its cap. Both fixtures are asserted, because the
// word appearing always is the same defect as the word appearing never.
//
// **That sorting asks the server for nothing**, over a stubbed global
// `fetch` that counts, for the reason `nested`'s expansion does: a
// renderer that grew a client would pass a check that only read its
// imports.
//
// **That a number column sorts as numbers.** Comparing the text would
// put 10 before 9, which is a wrong answer that looks like a right one
// in the renderer designers use most.
//
// **That `color_by` is neither offered nor honoured.** §4.7: a slot
// another renderer would colour is a column here, which is the honest
// form of the same information.
//
// Run directly: `node internal/web/jstest/render_table_test.mjs`.
// internal/web/static_appjs_browser_test.go shells out to it too.

import { addressOf } from "../static/address.js";
import { UNSET_LABEL } from "../static/palette.js";
import { ABSENT_TEXT, twinFor } from "../static/render/twin.js";
import { controlNamed } from "../static/render/controls.js";
import {
  ASCENDING,
  BUILTIN_KEY,
  BUILTIN_NAME,
  BUILTIN_TYPE,
  CONTROLS,
  DESCENDING,
  PARAM_COLUMNS,
  PARAM_GROUP_BY,
  PARAM_PAGE_SIZE,
  PARAM_SORT,
  RENDERER,
  SORT_READER,
  SORT_VIEW,
  STYLE_MONO,
  STYLE_MUTED,
  STYLE_PLAIN,
  STYLE_SERIF,
  tableScene,
} from "../static/render/table.js";

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
  return { id: `id-${key}`, type: "quest", key, name: `Quest ${key}`, attrs, ...extra };
}

function envelopeOf(nodes, edges = [], extra = {}) {
  return {
    nodes,
    edges,
    stats: { nodes: nodes.length, edges: edges.length, max_depth_reached: 1, duration_ms: 3 },
    truncated: { nodes: false, edges: false, depth: false },
    stale: [],
    ...extra,
  };
}

function address(key) {
  return addressOf({ type: "quest", key });
}

function cellOf(result, rowKey, column) {
  const at = result.columns.findIndex((c) => c.key === column);
  const row = result.rows.find((r) => r.key === address(rowKey));
  if (!row) throw new Error(`no row for quest/${rowKey}`);
  return row.cells[at];
}

// --- Absent, and empty -----------------------------------------------

check("anAbsentValueIsAnEmDashNotAnEmptyCell", () => {
  // `zone` resolved for one quest and found nothing for the next, which
  // is the ordinary shape of a projection: an empty cell here is
  // indistinguishable from a rendering bug, so it is a mark a reader can
  // see plus a flag a stylesheet can act on.
  const envelope = envelopeOf([
    node("a", { label: "A", zone: "Elwynn" }),
    node("b", { label: "B" }),
  ]);
  const result = tableScene(envelope, { [PARAM_COLUMNS]: [BUILTIN_NAME, "zone"] });

  const missing = cellOf(result, "b", "zone");
  assertEqual(missing.text, ABSENT_TEXT, "a slot that found nothing is an em dash");
  assertEqual(missing.absent, true, "and says so, because a game may hold an em dash as a value");
  assert(missing.text !== "", "and is emphatically not a blank cell");

  const found = cellOf(result, "a", "zone");
  assertEqual(found.text, "Elwynn", "and a slot that found something is that value");
  assertEqual(found.absent, false, "and is not flagged");
});

check("theEmptyStringIsNotAnEmDash", () => {
  // The control that makes the previous test mean something. A slot
  // present with "" in it is a value the game means, and it looks like
  // what it is.
  const envelope = envelopeOf([
    node("a", { label: "A", zone: "" }),
    node("b", { label: "B" }),
  ]);
  const result = tableScene(envelope, { [PARAM_COLUMNS]: [BUILTIN_NAME, "zone"] });

  const empty = cellOf(result, "a", "zone");
  assertEqual(empty.text, "", "the empty string is a blank cell");
  assertEqual(empty.absent, false, "and is not an absence");
  assertEqual(cellOf(result, "b", "zone").text, ABSENT_TEXT, "while the absent one still is");
  assert(
    empty.text !== cellOf(result, "b", "zone").text,
    "and the two are two different cells, which is the whole of this pair",
  );

  // A value that *looks* like the mark is still not the mark, which is
  // why the flag is the carrier and the glyph is only the sign.
  const decoy = tableScene(envelopeOf([node("c", { label: "C", zone: ABSENT_TEXT })]), {
    [PARAM_COLUMNS]: ["zone"],
  });
  assertEqual(decoy.rows[0].cells[0].text, ABSENT_TEXT, "a game may hold an em dash");
  assertEqual(decoy.rows[0].cells[0].absent, false, "and it is not an absence");
});

// --- The header ------------------------------------------------------

check("theHeaderRowSurvivesZeroRows", () => {
  // Zero rows is the empty state in table clothing (§4.7): the header
  // stays and the sentence sits under it. A table with no header is not
  // an empty table, it is a broken one.
  const result = tableScene(envelopeOf([]), {
    [PARAM_COLUMNS]: [BUILTIN_NAME, "zone", "level"],
  });
  assertEqual(result.rows.length, 0, "no rows");
  assertDeepEqual(
    result.columns.map((column) => column.key),
    [BUILTIN_NAME, "zone", "level"],
    "and every declared column is still a header",
  );
  assertEqual(result.page.total, 0, "the pager counts none");
  assertEqual(result.page.text, "", "and says nothing, because there is no page to name");

  // And a table that declares nothing still has the identity columns,
  // so an empty answer is never a table with no columns at all.
  const bare = tableScene(envelopeOf([]), {});
  assertDeepEqual(
    bare.columns.map((column) => column.key),
    [BUILTIN_NAME, BUILTIN_TYPE, BUILTIN_KEY],
    "the default table is the identity",
  );
});

check("columnsKeepTheirDeclaredOrder", () => {
  // A designer chose these and chose this order; a table that sorted
  // them, or that appended the ones it knew about, would be answering a
  // question nobody asked.
  const nodes = [node("a", { label: "A", zone: "Elwynn", level: 5, reward: "gold" })];
  const declared = ["level", BUILTIN_KEY, "zone", BUILTIN_NAME];
  const result = tableScene(envelopeOf(nodes), { [PARAM_COLUMNS]: declared });
  assertDeepEqual(
    result.columns.map((column) => column.key),
    declared,
    "the columns are drawn in the order the view declares",
  );
  // `reward` is a slot the answer carries and the view did not name: it
  // is not a column, because the view said which columns it wants.
  assert(
    !result.columns.some((column) => column.key === "reward"),
    "and a slot the view did not name is not appended to them",
  );

  // The built-ins get §4.7's faces and a projection slot gets none: the
  // name is the editorial thing on the row, a key is an identifier, a
  // type is context.
  const styleOf = (key) => result.columns.find((column) => column.key === key).style;
  assertEqual(styleOf(BUILTIN_NAME), STYLE_SERIF, "@name is serif");
  assertEqual(styleOf(BUILTIN_KEY), STYLE_MONO, "@key is mono");
  assertEqual(styleOf("zone"), STYLE_PLAIN, "and a projection slot is the game's value, in no face of ours");
  const withType = tableScene(envelopeOf(nodes), { [PARAM_COLUMNS]: [BUILTIN_TYPE] });
  assertEqual(withType.columns[0].style, STYLE_MUTED, "@type is muted");
  // The sigil is the query language's and not the reader's.
  assertEqual(withType.columns[0].label, "type", "and a built-in is headed by its bare word");

  // A declared column no node carries is still a column, with an absent
  // cell in every row: the server refuses one the query cannot produce
  // at save time, so dropping it here would hide a view it already
  // judged and leave a designer with a table that lost a column and said
  // nothing.
  const ghost = tableScene(envelopeOf(nodes), { [PARAM_COLUMNS]: [BUILTIN_NAME, "nowhere"] });
  assertEqual(ghost.columns.length, 2, "the column the view named is drawn");
  assertEqual(ghost.rows[0].cells[1].absent, true, "with an absence in every row");
});

// --- The order -------------------------------------------------------

check("sortingIsClientSideAndIssuesNoFetch", () => {
  const nodes = [
    node("c", { label: "C", zone: "Westfall" }),
    node("a", { label: "A", zone: "Elwynn" }),
    node("b", { label: "B", zone: "Redridge" }),
  ];
  const envelope = envelopeOf(nodes);

  // Every fetch this module could make, counted. It makes none, and the
  // assertion is over the count rather than over the absence of an
  // import, because a renderer that grew a client would still pass a
  // structural check.
  const original = globalThis.fetch;
  let fetches = 0;
  globalThis.fetch = () => {
    fetches++;
    return Promise.reject(new Error("a renderer does not fetch"));
  };
  try {
    const opened = tableScene(envelope, { [PARAM_COLUMNS]: [BUILTIN_NAME, "zone"], [PARAM_SORT]: "zone" });
    assertDeepEqual(
      opened.rows.map((row) => row.node.key),
      ["a", "b", "c"],
      "the view opens in its own order",
    );
    assertEqual(opened.sort.direction, ASCENDING, "ascending, which is what a designer means");
    assertEqual(opened.sort.source, SORT_VIEW, "and the ordering is the view's");

    // A reader clicks the header: the same rows, the other way round,
    // and no request.
    const clicked = tableScene(
      envelope,
      { [PARAM_COLUMNS]: [BUILTIN_NAME, "zone"], [PARAM_SORT]: "zone" },
      { sort: { column: "zone", direction: DESCENDING } },
    );
    assertDeepEqual(
      clicked.rows.map((row) => row.node.key),
      ["c", "b", "a"],
      "and a click re-sorts it",
    );
    assertEqual(clicked.sort.source, SORT_READER, "and the ordering is now the reader's");
    assertEqual(fetches, 0, "and nothing asked the server for anything");
  } finally {
    if (original === undefined) delete globalThis.fetch;
    else globalThis.fetch = original;
  }
});

check("aNumberColumnSortsAsNumbers", () => {
  // Comparing the cell's text puts 10 before 9. It is invisible in a
  // screenshot, wrong in every row, and in the renderer designers reach
  // for most — so the cell carries its underlying value and the
  // comparison uses it.
  const envelope = envelopeOf([
    node("a", { label: "A", level: 9 }),
    node("b", { label: "B", level: 10 }),
    node("c", { label: "C", level: 80 }),
  ]);
  const result = tableScene(envelope, { [PARAM_COLUMNS]: ["level"], [PARAM_SORT]: "level" });
  assertDeepEqual(
    result.rows.map((row) => row.node.key),
    ["a", "b", "c"],
    "9, 10, 80 — and not 10, 80, 9",
  );
  // The text is still the palette's own rendering of the value, so the
  // twin's cell and this one are the same characters.
  assertDeepEqual(
    result.rows.map((row) => row.cells[0].text),
    ["9", "10", "80"],
    "and the cells say the numbers",
  );
});

check("anAbsentValueSortsLastInBothDirections", () => {
  // Reversing the direction reverses the rows that have an answer. It
  // does not promote the ones that have none to the top, where they
  // would read as the extreme of the column rather than as its absence.
  const envelope = envelopeOf([
    node("a", { label: "A", level: 9 }),
    node("gap", { label: "Gap" }),
    node("b", { label: "B", level: 10 }),
  ]);
  const params = { [PARAM_COLUMNS]: ["level"], [PARAM_SORT]: "level" };
  const up = tableScene(envelope, params);
  const down = tableScene(envelope, params, { sort: { column: "level", direction: DESCENDING } });
  assertDeepEqual(up.rows.map((row) => row.node.key), ["a", "b", "gap"], "ascending, the absence is last");
  assertDeepEqual(down.rows.map((row) => row.node.key), ["b", "a", "gap"], "descending, it is still last");
});

check("sortingIsStableAcrossEqualKeys", () => {
  // Three quests at level 5, in the order the server returned them. A
  // sort that reordered them would make a table jump about under a
  // designer for no reason they can see, and would make two runs of one
  // view two different documents.
  const nodes = ["d", "a", "c", "b"].map((key) => node(key, { label: key.toUpperCase(), level: 5 }));
  const result = tableScene(envelopeOf(nodes), {
    [PARAM_COLUMNS]: [BUILTIN_NAME, "level"],
    [PARAM_SORT]: "level",
  });
  assertDeepEqual(
    result.rows.map((row) => row.node.key),
    ["d", "a", "c", "b"],
    "equal keys keep the answer's own order",
  );
  // And the other direction too: an implementation that reversed the
  // whole array to sort descending would reverse the ties with it.
  const down = tableScene(
    envelopeOf(nodes),
    { [PARAM_COLUMNS]: [BUILTIN_NAME, "level"], [PARAM_SORT]: "level" },
    { sort: { column: "level", direction: DESCENDING } },
  );
  assertDeepEqual(
    down.rows.map((row) => row.node.key),
    ["d", "a", "c", "b"],
    "in both directions, because a tie has no direction",
  );
});

// --- The pager -------------------------------------------------------

check("pagingSaysWhenTheResultIsAlsoTruncated", () => {
  const nodes = Array.from({ length: 1000 }, (_, i) => node(`q${i}`, { label: `Q${i}` }));
  const params = { [PARAM_COLUMNS]: [BUILTIN_NAME], [PARAM_PAGE_SIZE]: 50 };

  const capped = tableScene(envelopeOf(nodes, [], { truncated: { nodes: true, edges: false, depth: false } }), params);
  assertEqual(capped.page.text, "showing 1–50 of 1000, capped", "a truncated answer says so");
  assertEqual(capped.page.rows.length, 50, "and shows one page");
  assertEqual(capped.page.pages, 20, "of twenty");
  assertEqual(capped.rows.length, 1000, "while the table holds every row it was given");

  // The control that makes the previous assertion mean something: the
  // same page of an untruncated answer says nothing about a cap. A word
  // that is always there is as useless as one that is never there.
  const whole = tableScene(envelopeOf(nodes), params);
  assertEqual(whole.page.text, "showing 1–50 of 1000", "an untruncated answer does not");
  assert(!whole.page.text.includes("capped"), `and never says capped: ${whole.page.text}`);

  // A later page counts from where it starts.
  const second = tableScene(envelopeOf(nodes), params, { page: 2 });
  assertEqual(second.page.text, "showing 51–100 of 1000", "the second page names its own range");
  assertEqual(second.page.rows[0].node.key, "q50", "and holds the rows that follow the first");

  // A page number nobody can reach is clamped rather than empty: a table
  // that looked like it had lost its rows would be worse than a table
  // that shows the last of them.
  const past = tableScene(envelopeOf(nodes), params, { page: 99 });
  assertEqual(past.page.number, 20, "a page past the end is the last one");
  assertEqual(past.page.rows.length, 50, "with rows in it");

  // One page of an untruncated answer says nothing at all, which is
  // render/scene.js's own first rule about the absence of a thing.
  const small = tableScene(envelopeOf(nodes.slice(0, 7)), { [PARAM_COLUMNS]: [BUILTIN_NAME] });
  assertEqual(small.page.text, "", "seven rows on one page need no sentence");
  assertEqual(small.page.rows.length, 7, "and every one of them is shown");
  // Unless the answer was capped, where the count is the one thing a
  // reader must not take at face value.
  const smallCapped = tableScene(
    envelopeOf(nodes.slice(0, 7), [], { truncated: { nodes: true, edges: false, depth: false } }),
    { [PARAM_COLUMNS]: [BUILTIN_NAME] },
  );
  assertEqual(smallCapped.page.text, "showing 1–7 of 7, capped", "a capped answer always says so");
});

// --- The groups ------------------------------------------------------

check("groupSubheadersCarryTheirCount", () => {
  const envelope = envelopeOf([
    node("a", { label: "A", zone: "Westfall", level: 2 }),
    node("b", { label: "B", zone: "Elwynn", level: 3 }),
    node("c", { label: "C", zone: "Elwynn", level: 1 }),
    node("d", { label: "D" }),
  ]);
  const result = tableScene(envelope, {
    [PARAM_COLUMNS]: [BUILTIN_NAME, "zone", "level"],
    [PARAM_GROUP_BY]: "zone",
    [PARAM_SORT]: "level",
  });

  assertDeepEqual(
    result.groups.map((group) => [group.caption, group.count, group.from]),
    [
      ["Elwynn", 2, 0],
      ["Westfall", 1, 2],
      [UNSET_LABEL, 1, 3],
    ],
    "one group per value with its count and where it starts",
  );
  // The absence is the **last** group: a group of "the ones we know
  // nothing about" at the top would read as the leading group of the
  // answer, which is the mistake `layered` refuses when it puts its
  // unranked band at the end.
  assertEqual(result.groups[result.groups.length - 1].caption, UNSET_LABEL, "and the absence is last");
  assertEqual(result.groups[0].caption !== UNSET_LABEL, true, "never first");

  // Grouping is a second axis and not a second sort: within a group the
  // rows keep the order the sort gave them.
  assertDeepEqual(
    result.rows.map((row) => row.node.key),
    ["c", "b", "a", "d"],
    "the groups are contiguous and each is still sorted by level",
  );

  // A view that groups nothing has **no** groups, which is not an empty
  // list of them.
  const plain = tableScene(envelope, { [PARAM_COLUMNS]: [BUILTIN_NAME] });
  assertEqual(plain.groups, null, "an ungrouped table has no groups");
});

// --- What this renderer is not ---------------------------------------

check("colorByIsNotOfferedHere", () => {
  // The catalogue does not offer it, and §4.7 says why: a slot another
  // renderer would colour is a **column** here, which is the honest form
  // of the same information and the one a reader who cannot separate two
  // hues can still read.
  assertEqual(controlNamed(CONTROLS, "color_by"), null, "no control offers it");

  const envelope = envelopeOf([
    node("a", { label: "A", color_by: "elite" }),
    node("b", { label: "B", color_by: "trash" }),
  ]);
  const result = tableScene(envelope, { [PARAM_COLUMNS]: [BUILTIN_NAME], color_by: "color_by" });
  assertEqual(result.columns.length, 1, "and a view carrying it draws the columns it declared");
  assertEqual(result.legend, undefined, "there is no legend");
  for (const row of result.rows) {
    for (const [field, value] of Object.entries(row)) {
      assert(
        typeof value !== "string" || !value.startsWith("var(--data"),
        `no row carries a hue: ${field} is ${JSON.stringify(value)}`,
      );
    }
  }

  // And as a column it is the honest form of the same information: the
  // value is right there, in words.
  const asColumn = tableScene(envelope, { [PARAM_COLUMNS]: [BUILTIN_NAME, "color_by"] });
  assertEqual(cellOf(asColumn, "a", "color_by").text, "elite", "the slot reads as a column");
});

check("theTableDrawsNoMarksAtAll", () => {
  // The one renderer with no picture in the SVG sense. A `marks: []` on
  // the way out would be a mechanism nothing reads pretending to be a
  // drawing, and a route that mounted a canvas for this view would draw
  // an empty one over a table.
  const result = tableScene(envelopeOf([node("a", { label: "A" })]), {});
  assert(!("marks" in result), "a table has no marks, not even an empty list of them");
  assert(!("legend" in result), "and no legend");
});

// --- The twin --------------------------------------------------------

check("theTableAndTheTwinDescribeTheSameAnswer", () => {
  // Both are tables of one answer, and they are computed independently:
  // the twin from the envelope alone, this from the envelope and the
  // view's parameters. Where they differ they differ on purpose, and
  // this test says where.
  const envelope = envelopeOf(
    [
      node("a", { label: "A", zone: "Elwynn", level: 5 }),
      node("b", { label: "B", level: 3 }),
      node("c", { label: "C", zone: "Westfall" }),
    ],
    [{ id: "e1", type: "leads_to", source: "id-a", target: "id-b" }],
  );
  const result = tableScene(envelope, { [PARAM_COLUMNS]: ["zone", BUILTIN_NAME], [PARAM_PAGE_SIZE]: 2 });
  const twin = twinFor(envelope);

  // Every node of the answer is a row in both, by the same address.
  assertEqual(result.rows.length, twin.nodes.rows.length, "one row each, in both");
  assertDeepEqual(
    result.rows.map((row) => row.key).sort(),
    twin.nodes.rows.map((row) => row.key).sort(),
    "and they are the same nodes",
  );

  // And a cell both carry is the same characters, because both go
  // through the twin's own valueCell.
  for (const row of result.rows) {
    const mirror = twin.nodes.rows.find((r) => r.key === row.key);
    const here = row.cells.find((cell) => cell.column === "zone");
    const there = mirror.cells.find((cell) => cell.column === "zone");
    assertEqual(here.text, there.text, `zone reads the same in both for ${row.key}`);
    assertEqual(here.absent, there.absent, "and the absence is the same absence");
  }

  // **The three deliberate differences.**
  //
  // Columns: the twin shows every slot in its own order because it
  // describes the answer; this shows what the view declared, in that
  // order, because a designer chose it.
  assertDeepEqual(result.columns.map((c) => c.key), ["zone", BUILTIN_NAME], "the view's columns, in its order");
  assert(
    twin.nodes.columns.some((column) => column.key === "level"),
    "while the twin carries a slot this table does not draw",
  );
  assert(
    !result.columns.some((column) => column.key === "level"),
    "which is the difference, and it is the view's own choice",
  );

  // Paging: the twin has none, and this pages the rows already here.
  assertEqual(result.page.rows.length, 2, "the table shows a page");
  assertEqual(result.rows.length, 3, "of the rows it holds");
  assertEqual(twin.nodes.rows.length, 3, "and the twin describes all of them regardless");

  // Edges: the twin has an edge table and this renderer draws no
  // relation. The catalogue consumes "nodes only" and deliberately does
  // not refuse a query that draws edges, so they are in the answer, in
  // the twin, and not in this picture.
  assertEqual(twin.edges.rows.length, 1, "the twin carries the relation");
  assert(!("edges" in result), "and the table has no edge table at all");
});

check("theControlsTeachWhatTheCatalogueDoesNot", () => {
  assertEqual(RENDERER, "table", "the module names the renderer it is");
  const size = controlNamed(CONTROLS, PARAM_PAGE_SIZE);
  assert(size !== null, "page_size has a control");
  // The sentence a designer needs is that the pager is about the rows
  // already on this machine: somebody who reads it as "how much was
  // fetched" has the mechanism backwards.
  assert(size.tooltip.includes("asks the server for nothing"), "whose sentence says it fetches nothing");
  assert(size.tooltip.includes("cap"), "and names the one thing that says how much is missing");
  const sort = controlNamed(CONTROLS, PARAM_SORT);
  assert(sort.tooltip.includes("Clicking"), "and sort says the header is clickable");
  assert(sort.tooltip.includes("go last"), "and where a row with nothing in the column ends up");
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
  console.error(`${failures} table renderer check(s) failed`);
  process.exit(1);
}
console.log("table.js: all checks passed");
