# Maestro Interface Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Put a picture on the screen. A designer opens a game by its
slug, sees the three things a game contains, opens a saved view, binds
its parameters from the URL, and gets one of six drawings — with the
ambiguous nodes marked, the unplaced nodes shelved, the truncation named,
the staleness refused rather than smoothed over, and an empty answer that
looks like a successful answer. Then they drag four nodes, and the
arrangement survives a reload, a colleague's browser, and twelve more
entities arriving from an agent.

**Architecture:** No build step, no TypeScript, no framework beyond a
vendored Lit. The one architectural decision the whole plan rests on:
**a renderer is a pure function from the run envelope to a scene, and the
scene is plain data.** `render(envelope, params, viewport) -> Scene`,
where a `Scene` is an array of marks (`{kind, x, y, w, h, text, fill,
dash, class, key}`) plus legend rows, banners and a shelf. A separate,
dumb emitter turns a scene into SVG elements and nothing else. Everything
this sub-project must be right about — which node is dashed, which banner
fires, where an unplaceable node goes, what a truncated result says —
lives in the pure half, where it is data a test can read and a mutation
can turn red. The DOM half is small enough to hold in one file per
surface and is covered by a Node-driven harness plus one real browser
pass at the end.

**Tech Stack:** Lit 3 (vendored ESM, import map), `@dagrejs/dagre` +
`@dagrejs/graphlib` (vendored ESM, in a module Web Worker), vanilla CSS
with a token layer, ES modules served from `internal/web/static/` by the
existing embedded `serveAsset`. Tests: Node ≥20 (`node --test` is not
used; the harnesses are plain scripts, as the shipped ones are) driven
from Go by `runJSTest`, plus Go source-shape guards, plus one
browser-driven end-to-end task.

**Prerequisite:** sub-projects 1–4 landed. This plan is written against
**the code that shipped** on 2026-09-06, not against the plans that
produced it: `internal/views/execute.go` (`Node`, `Edge`, `Stats`,
`Truncated`, `Position`, `Diagnostic`, `Result`), `internal/views/renderers.go`
(the six renderers, their parameters and `ReadsBackground`),
`internal/views/events.go` (`view.upserted`, `view.removed`,
`view.positions`, `view.background`), `internal/metamodel/events.go`
(`type.renamed`, `relation_type.renamed`), `internal/markdown/events.go`
(`document.moved`), `internal/web/server.go` (every route quoted below),
`internal/web/static/{app.js,doc.js,styles.css,game.html}` and
`internal/web/jstest/`.

**Source spec:** `docs/superpowers/specs/2026-09-06-interface-design.md`,
roadmap item 5 of `docs/superpowers/specs/2026-08-31-core-and-metamodel-design.md`.

---

## Before the first task: how a front end verifies itself here

This repository's test discipline is Go, a real Postgres, and mutation —
*break the code, watch the named test go red, put it back*. None of that
transfers unexamined to a rendered SVG. This section says what the
equivalent is, and where the equivalent runs out.

### The four layers, and what each one can actually catch

1. **Scene tests (Node, `internal/web/jstest/`).** The renderers, the
   layout composition, the palette and the client's event rules are pure
   functions over plain objects. A scene test imports the real, unmodified
   module and asserts over the returned data: *this node's `dash` is
   `"unset"`, this banner's `code` is `truncated_nodes`, this shelf holds
   these three keys, these two runs of the same envelope produce
   byte-identical coordinates*. **This is the layer mutation applies to,
   and it is where most of this plan's assertions live.** Deleting the
   dashed-outline branch, collapsing *absent* and *empty string* into one
   legend row, or dropping the cycle mark are each one line, and each has
   a named test below that goes red for it.
2. **DOM tests (Node, the existing stub).** `internal/web/jstest/` already
   drives `app.js` and `doc.js` in a hand-written DOM stub. Task 1 extends
   that stub with `createElementNS`, an attribute map and `getAttribute`,
   so the emitter can be driven the same way: *the scene emitted N `<rect>`
   elements, this one carries `stroke-dasharray`, every game string
   arrived through `textContent`*. This layer catches emitter bugs and
   the class of bug the shipped harnesses were written for — a value that
   never reaches the DOM at all.
3. **Go source-shape guards (`internal/web/static_*_test.go`).** For
   properties with no runtime signature in any harness: no HTML sink in
   any shipped asset (the existing `static_sinks_test.go` perimeter,
   widened in Task 2 to `static/components/` and `static/render/` and to
   *exclude* `static/vendor/` explicitly rather than by accident), no
   absolute `http(s)://` URL in any module (a self-hosted instance with no
   outbound network must work), the vendored payload budget, every CSS
   token declared in both themes and every token referenced somewhere,
   every new shell registered as a route.
4. **A driven browser, once, at the end (Task 18).** Real layout, real
   CSS cascade, real focus, real frame timing. Task 18 drives Firefox
   against a running server and asserts through `evaluate_script`:
   computed styles resolve the tokens, the canvas element has non-zero
   box, `document.activeElement` is what the twin says it is, a drag of
   forty nodes stays under a frame budget measured with
   `performance.now()`, and the four `view.*` events reach a second page.

### What none of that can check, said plainly

- **Whether it looks right.** Whether eight hues at 11px over a busy
  canvas actually separate, whether the serif/sans/mono split reads as
  editorial rather than accidental, whether a thousand-node graph is
  legible or a hairball with good test coverage. A human opens it and
  says. Tasks 8–13 each end with a **hand check** line naming the exact
  thing to look at; they are not optional and they are not automatable.
- **Contrast and colour-blind separation are the exception, and are
  automated.** Both are arithmetic over the tokens: Task 1 parses the hex
  values out of `styles.css` in Go, computes WCAG relative luminance
  ratios, and simulates deuteranopia and protanopia to assert pairwise
  separation. A palette that fails is a palette that fails a test, not a
  palette somebody squints at.
- **Paint order and occlusion.** A scene says a label is drawn after its
  node; only a browser says the label is on top. Task 7 asserts scene
  order and Task 18 screenshots it. Between those two there is a gap, and
  the gap is a human looking at a screenshot.

### The rule from the last sub-project, applied here

**Correct and unasserted was the most productive question asked all
sub-project.** Every task below names, for each thing it gets right, the
test that goes red when it is wrong — and the "See red" step names the
*mutation*, not the feature. If a step cannot name a mutation, the
assertion is decoration and comes out.

**The negative half is the load-bearing half.** For the six renderers
this is not a slogan: a real game produces ambiguous nodes, absent slots,
truncated answers and stale views on an ordinary afternoon. Each renderer
task below spends more checklist items on the negative states than on the
drawing, in the same ratio the spec's §4 does.

### Two operational notes

- `TEST_DATABASE_URL` must be exported for the Go half, exactly as the
  views plan says; the Node half needs no database and no network.
- Node is optional at runtime and `nodeOrSkip` already skips when it is
  absent. **A task is not done on a machine without Node**: its scene
  tests are the whole of its evidence. Say so in the commit if a run
  skipped them, and do not.

---

## Open questions decided

The spec left ten and marked them open. A plan that forwards them is not
a plan. Each is decided here, with the argument, and where the answer is
"not this sub-project" it names the one that owns it.

**O1 — a human alone cannot create a view. Decided: no query builder,
and one narrow exception that ships, "Save as" (Task 16).** A
point-and-click builder over traversals, depth ranges, `edge_where` and
projections is a sub-project with its own spec, not a corner of this one;
building it here would be roughly the size of Tasks 4–13 put together and
would arrive as the least-tested thing in the product. What does ship is
the cheap intermediate the spec's O1 gestured at, narrowed until it is
honest: **"Save as" copies an existing view's query document verbatim
under a new key, and lets the designer change only the renderer, the
renderer parameters and the parameter defaults.** No stage of the query
is editable, so no builder exists, and the interface never has to explain
a traversal. It is one dialog, one `views.upsert` with the copied
document, and about one task — Task 16, which is deliberately last before
the seed removal so it can be dropped if the sub-project runs long. The
discomfort is not resolved: a designer with no agent still cannot ask a
question nobody has asked before, and Task 15's onboarding sentence says
so in the product rather than only here.

**O2 — an edge's endpoints are ids and nothing reads an entity by id.
Decided: no `entities.get_by_id`, not here.** Every read in the product
is `(type, key)`, and widening the addressing model of the content
surface to name the far end of a *stub* edge — a band that is
legitimately outside the picture — is a poor trade. The stub is drawn,
counted and listed in the text twin by id (Task 8), and the frame's
sentence says *"8 edges lead outside this picture"* without pretending to
name them. The analysis sub-project (6) needs id-to-entity resolution for
its own reports and is where the read belongs if it is ever added.

**O3 — `layout_seed` has no reader. Decided: remove it, Task 17.** With a
deterministic engine (§5.2, Task 6) nothing reads it on either side of
the wire, and this repository's standing rule is that a mechanism nothing
reads is a lie. Keeping it "documented as dormant" was the spec's
compromise; it costs a sentence in two doc comments *and* a column in the
wire, in the MCP tool schema, in `views.upsert`'s input, in the REST
mirror, in `dbq`, and in 56 references across eleven files — every one of
which a reader has to decide is real. **This is a wire and schema change
and it gets its own task**: migration `0012_drop_layout_seed.sql`, the
`ViewInput` field, the two surfaces, the generated tool description, and
the tests that named it. Re-adding it if a stochastic engine ever lands
is additive and cheap; a dropped column has never been the hard part.
Task 17 also records, in the migration comment, what a future seed would
have to seed.

**O4 — a position write costs a full query re-run to observe. Decided:
no new route, and measure instead.** `views.run` is the only reader of
`positions[]` and it stays that way in this sub-project. The coalescing
rules (Task 3: 750ms debounce per view, deferral during a drag or an
unacknowledged write, deferral while hidden) reduce a forty-node marquee
to one re-run, which is the case that motivated the question. Task 18
records the measured cost of that re-run for the worked example, so that
when someone argues this again they argue it with a number. If the number
is bad the fix is `views.run` growing a `positions_only` mode — one
execution path, one envelope — and **not** a second route; recorded here
so the second route is not invented twice.

**O5 — "which views draw this entity?" Decided: not answered, and the
entity page says nothing rather than guessing.** Answering it means an
index over entities that the analysis sub-project is already building
reachability machinery for. Running every saved view client-side is a
denial of service, and Task 15 pins the absence with
`TestTheEntityPanelRunsNoViews` — a test that fails if the panel ever
issues a `views/run` call — because "we deliberately do not do this" is
the kind of decision a later well-meaning edit undoes silently.

**O6 — who writes the rename repair. Decided: not the interface. It
belongs to the agent surface (sub-project 7, the skill bundle), as a
tool that returns the rewritten document for its caller to show.** Half
of this question expired while the spec was being written: `types.rename`
and `relation_types.rename` ship, so a `*_renamed` diagnostic is now
reachable in one call and Task 18's end-to-end drives it. What remains is
whether the interface offers a one-click repair, and it does not, for a
reason that is about diffs and not about scope: the repair **edits an
author's document**, and this interface has no query editor, no JSON diff
renderer, and no place to show the before and after — building those is
most of the cost, and it would build them for one button. What the
interface does instead is Task 4's quiet title-strip line naming the
pointers (*"this view names 2 things the game now spells differently"*),
which is a sentence a designer can hand to their agent verbatim. The
urgency is low by construction: a `*_renamed` diagnostic arrives beside a
**correct** picture, because the reference resolved by id.

**O7 — `view.upserted` for a viewer. Decided: the band, for everybody.**
A picture silently becoming a picture of a different question is the same
failure whether or not the reader could have caused it — and a viewer is
*more* exposed, not less, because they have no way to know an edit was in
flight. Role-conditional refresh behaviour would also be a second policy
to test for no gain. Task 3 pins it with
`TestAViewerGetsTheSameReloadBandAsAnEditor`, which drives the same event
under both roles and asserts one banner.

**O8 — one background layer. Decided: one, unchanged.** A second layer
changes the schema, the placement mode and the frame together; the views
spec already refused a join table for a plausible ask, and this
sub-project has no better reason. Task 11's code comment names what a
second layer would need so it is not rediscovered.

**O9 — system fonts versus a vendored face. Decided: system fonts, and
this closes the question for v1.** The payload budget (§8.2, 150KB of
vendored JavaScript, already mostly spent on Lit and dagre), a font
binary in a public repository, and a font-loading state to design for are
three real costs against an identity that is already carried by the
serif/sans/mono split and the achromatic-chrome rule. If it is revisited
it is revisited with a measured page weight and a licence review, not
with a preference.

**O10 — the fallback grid's threshold. Decided: 2000ms, in one exported
constant.** `LAYOUT_BUDGET_MS = 2000` and `LAYOUT_RETRY_MS = 10000` live
in `static/layout/budget.js` and nowhere else; the worker reads them, the
retry button reads them, and **the banner's sentence is generated from
the number** so the prose cannot drift from the timeout (Task 6 asserts
it by changing the constant and re-reading the sentence). Task 18 records
the real elapsed layout time for the largest view it can build, which is
the measurement the number should be revisited against.

### Decisions taken since the spec was written, folded in

- **A game is addressed by its slug, everywhere.** Every route below is
  `/g/{slug}/…` and `/api/games/{slug}/…`; no uuid appears in any URL the
  interface constructs. Task 15 carries
  `TestNoPageURLContainsAUUID` as a source-shape guard over the page
  modules, because the stale habit is one template literal away.
- **A type can be renamed, so `type.renamed` and
  `relation_type.renamed` are events the client must handle.** Task 3
  treats them as staleness signals: they invalidate the *view row* (a
  view may have just become stale) but never the picture silently — the
  client re-reads the row and, if the run now fails staleness, shows the
  panel. It does not re-run automatically over a rename.
- **Documents can be moved and a game's kinds are discoverable.** Task 15
  reads `GET /docs/kinds` for the prose lane's vocabulary line (as
  `game.html` already does) and subscribes to `document.moved`, which is
  why the client must treat a document `path` as a value that changes
  rather than as an identity to cache.
- **A game can be counted in one call.** `GET /api/games/{slug}/summary`
  is the only call the game home makes for its catalogue lane counts —
  Task 15 asserts the call count, so a later edit that fetches per type
  fails a test.
- **Rows are addressed by key on every verb**, so the client holds keys
  and never ids as addresses. The single exception is `Node.ID`, which the
  client uses only to join edges to nodes inside one envelope and never
  stores, sends or puts in a URL. Task 3 asserts it:
  `TestTheClientNeverSendsAnEntityID`.

---

## What in the shipped code makes this harder than the spec assumes

Five things, each with the task that deals with it.

1. **There is no SSE client anywhere.** `app.js` and `doc.js` contain no
   `EventSource`; the stream at `GET /api/games/{game}/events` has been
   served since Core and has never had a browser reading it. Task 3 is
   therefore writing the first consumer of the hub, including reconnect
   (`defaultSSEMaxLifetime` closes every stream after 5 minutes *by
   design*, so a client that does not reconnect looks fine for four
   minutes and then goes quiet) and the heartbeat comment lines, which
   fire no `message` event and must not be mistaken for silence.
2. **The envelope has no viewport, no sizes and no text metrics.** Node
   labels are the game's words and a box has to fit one. There is no
   server-side measurement and no canvas measuring in a worker, so Task 7
   measures once, in the page, with a hidden `<svg><text>` and a memoised
   per-string cache, and hands *sizes* to the layout worker. A worker that
   guessed at label widths would produce overlaps that only appear on
   long names, which is every name in a real game.
3. **`Node.Ambiguous` is per node, not per slot** (`execute.go` argues
   it). The interface must mark the node and must not imply which slot,
   which is a constraint on the mark's position and on the banner's
   sentence, both stated in Task 4.
4. **An edge's endpoints are not guaranteed to be in `nodes`**, and this
   is normal rather than exceptional — `edges: [{between: …}]` draws
   relations between sets the query chose not to draw. Every renderer
   must tolerate it in its first version, not as a later fix; Task 7's
   scene builder helper `joinEdges` returns `{drawn, stubs}` and the six
   renderers consume both halves.
5. **`Truncated` can over-report and `MaxDepthReached` is measured, not
   declared.** `execute.go` says both plainly. So the interface may never
   print "complete", and its three truncation sentences are three, never
   one summary. Task 4 pins the absence with
   `TestACleanEnvelopeProducesNoCompletenessClaim`.

---

## File structure

| Path | Responsibility |
|---|---|
| `internal/web/static/styles.css` | the token layer, both themes, the two densities, the flat frame |
| `internal/web/static/palette.js` | eight hues, the hash assignment, `unset`, the hatched tail, legend rows |
| `internal/web/static/vendor/` | `lit-core.min.js`, `dagre.mjs`, `graphlib.mjs`, `manifest.json`, `licenses/` |
| `internal/web/static/client.js` | the one data client: run, error shapes, parameter binding, the SSE subscription |
| `internal/web/static/render/scene.js` | the scene vocabulary and the shared helpers (`joinEdges`, `banners`, `legend`) |
| `internal/web/static/render/{graph,layered,nested,map,table,timeline}.js` | six pure scene builders |
| `internal/web/static/layout/{budget,engine,compose,worker}.js` | the dagre call, the composition rule, the worker and its budget |
| `internal/web/static/components/mst-view-frame.js` | title strip, parameter bar, banner stack, footer |
| `internal/web/static/components/mst-canvas.js` | pan/zoom, the SVG emitter, the drag layer |
| `internal/web/static/components/mst-twin.js` | the text twin |
| `internal/web/static/components/mst-legend.js` | the legend, including the hatched tail's expansion |
| `internal/web/static/pages/{home,views,view,types,catalogue,entity,assets}.js` | one module per route |
| `internal/web/static/*.html` | one shell per route, each carrying the import map |
| `internal/web/jstest/*.mjs` | the Node harnesses, one per module under test |
| `internal/web/static_*_test.go` | the source-shape guards and the `runJSTest` drivers |
| `internal/db/migrations/0012_drop_layout_seed.sql` | Task 17 |

---

### Task 1: The token layer, the data palette, and the arithmetic that judges them

**Files:**
- Modify: `internal/web/static/styles.css`
- Create: `internal/web/static/palette.js`
- Create: `internal/web/jstest/palette_test.mjs`
- Create: `internal/web/static_tokens_test.go`
- Modify: `internal/web/static_appjs_browser_test.go` (one `runJSTest` driver)

`styles.css` keeps every selector it has — `app.js`, `doc.js` and the four
shells depend on them — and gains the token layer of the spec's §2.3
plus a second, separately tuned dark set. The existing `--accent` /
`--accent-ink` pair is **renamed to `--focus` / `--focus-ink`** in the
same commit as its call sites, because the identity's rule is that one
accent means focus, selection and current, and a token named `accent`
invites a second one.

```css
:root {
  color-scheme: light dark;
  --paper: #faf8f5;  --ground: #f2efe9;  --ink: #14140f;
  --muted: #6b6459;  --line: #ded7cb;
  --focus: #2f4a8a;  --focus-ink: #ffffff;  --danger: #a4262c;
  --unset: transparent;  --dropped: #b0a89a;  --pending: #8f8677;
  --data-1: …; /* eight hues, light-ground tuning */
  --serif: ui-serif, Georgia, "Times New Roman", serif;
  --sans: system-ui, sans-serif;
  --mono: ui-monospace, SFMono-Regular, Menlo, monospace;
}
@media (prefers-color-scheme: dark) {
  :root { --paper: #1c1a14; --ground: #16150f; --ink: #ece5d8; …
          --data-1: …; /* the same eight hues at dark-ground lightness */ }
}
```

`palette.js` is pure and exports four things:

```js
export const DATA_SLOTS = 8;

// hueFor assigns a slot by hashing the value's JSON text, never by its
// rank in the result: rank assignment recolours the whole picture the day
// someone adds a zone, which destroys the one property a saved view
// exists for. Collisions are the price and are stable rather than
// surprising; the legend and the label carry the text.
export function hueFor(jsonText) { /* FNV-1a over UTF-8 code units, % 8 */ }

// legendFor turns the nodes' values for one slot into legend rows:
// the eight most frequent values take the eight hues, everything else
// becomes one hatched `other` row carrying its member list, and a node
// whose slot is *absent from attrs* becomes its own `unset` row —
// never merged with the empty string, which is a value the game means.
export function legendFor(nodes, slot) { /* -> {rows, byValue} */ }

export function fillFor(row) { /* -> {kind: "hue"|"hatch"|"unset", index} */ }
```

- [x] Tests (`internal/web/jstest/palette_test.mjs`, driven by
  `TestPaletteRules`):
  `hueIsStableAcrossResultOrder` — shuffle the node array, assert every
  value keeps its slot; `absentAndEmptyStringAreTwoRows` — one node with
  no `attrs.zone`, one with `""`, assert two rows with distinct kinds and
  distinct counts; `theNinthValueGoesToTheHatchedTail` — eleven zones,
  assert eight hue rows plus one `other` row whose member list has three
  entries and whose label reads `other (3 values)`;
  `theTailIsChosenByFrequencyNotByName` — make the alphabetically-first
  value the rarest and assert it is in the tail;
  `aNumberAndItsStringAreDifferentValues` — `20` and `"20"` get two rows,
  matching the envelope's own type preservation;
  `legendRowsAreOrderedByCountThenValue` — deterministic ordering,
  asserted twice on shuffled input.

- [x] Tests (`internal/web/static_tokens_test.go`, pure Go over the CSS
  source): `TestEveryTokenIsDeclaredInBothThemes` — parse `:root` and the
  dark media block, assert the two declare the same token names;
  `TestEveryDeclaredTokenIsUsed` and `TestEveryUsedTokenIsDeclared` —
  scan every `.css` and `.js` under `static/` for `var(--…)` and compare
  both directions (this is the bidirectional data-table guard the views
  plan named as the thing that kept working);
  `TestTextContrastMeetsWCAG` — compute the relative-luminance ratio of
  `--ink` on `--paper`, `--ink` on `--ground`, `--muted` on `--paper`,
  `--danger` on `--paper` and `--focus-ink` on `--focus`, assert ≥4.5:1
  in **both** themes; `TestMeaningfulOutlinesMeetThreeToOne` — `--line`
  and each `--data-n` against `--ground`, ≥3:1;
  `TestTheDataHuesSeparateUnderDeuteranopiaAndProtanopia` — simulate both
  (the standard LMS matrices, ~30 lines of Go, in the test file with its
  own comment) and assert every pair of the eight is above a stated ΔE
  threshold in each simulation, in both themes.

- [x] See red: change `--data-3` in the light block to a near-duplicate of
  `--data-5` and watch `TestTheDataHuesSeparateUnderDeuteranopiaAndProtanopia`
  name the pair. Then set `--muted` to `#8f8f8f` and watch
  `TestTextContrastMeetsWCAG` fail on `--muted` over `--paper`. Then make
  `legendFor` fold an absent slot into the empty-string row and watch
  `absentAndEmptyStringAreTwoRows` fail with two counts on one row.

```bash
git add internal/web/static/styles.css internal/web/static/palette.js \
        internal/web/jstest/palette_test.mjs internal/web/static_tokens_test.go \
        internal/web/static_appjs_browser_test.go
git commit -m "feat(web): the interface token layer and a data palette judged by arithmetic"
```

**Hand check.** Open the game page in both themes and look at eight
swatches at the size a node label actually is — about 11px — scattered
over the canvas ground rather than lined up in a legend. The arithmetic
below proves every pair is far apart in CIELAB under normal vision and
under both simulated dichromacies; it cannot say whether a reader
*recognises* the third hue as the same third hue two hundred nodes away,
and it says nothing at all about anomalous trichromacy, which is far
commoner than the dichromacies simulated here. Look also at the
serif/sans/mono split on the document page: whether it reads as
editorial or as an accident is the other thing no test in this task can
answer.

#### Corrections made during implementation

1. **A second rule token, `--line-strong`, was added, and `--line` is
   deliberately *not* held to 3:1.** The task asks
   `TestMeaningfulOutlinesMeetThreeToOne` to hold "`--line` and each
   `--data-n` against `--ground`". `--line` is `#ded7cb` on `#f2efe9`:
   1.25:1, and any value that reaches 3:1 is a strong grey — at which
   point the spec's "hairline rules instead of boxes and shadows" is
   gone, because a rule at 3:1 against its own surface is a box. The two
   demands are both right about different strokes, so there are now two
   tokens: `--line` stays the decorative hairline (a table rule, a panel
   edge, which WCAG 1.4.11 exempts as decoration), and `--line-strong`
   is the stroke that *is* the signal — the dashed outline of a node
   whose `color_by` slot found nothing, and the palette's hatched tail.
   `--line-strong` and the eight hues are what the test guards.

2. **`--dropped` and `--pending` are not declared yet.** The spec lists
   them in §2.3 and this task's code block repeats them, but nothing in
   this task reads either one, and this plan's own preamble says a
   mechanism nothing reads is a lie. They land with the banner stack
   (Task 4) and the write-in-flight state (Task 14) that read them.
   `TestEveryDeclaredTokenIsUsed` therefore ships with **no exception
   list**, deliberately: an exemption is the hiding place every unread
   token in the future would use.

3. **The renamed accent did not follow its call site into the button.**
   `--accent` / `--accent-ink` are renamed to `--focus` / `--focus-ink`
   as the task requires, but the one call site — the primary button's
   fill — was moved to `--ink` / `--paper` rather than to `--focus`. A
   button filled with `--focus` is a coloured button, which is exactly
   what the identity's central rule forbids, and it would spend the
   accent on a fourth meaning. `--focus` instead gained its real
   readers in the same commit: the `:focus-visible` ring and
   `::selection`. Note that the old `--accent` was `#2b2b2b`, an
   achromatic near-black, so this is what the rename *preserved* rather
   than a change of look.

4. **The both-themes guard compares colour tokens, not all tokens.**
   `--serif`, `--sans` and `--mono` are declared once: a font stack does
   not have a theme, and redeclaring three identical stacks in the dark
   block to satisfy a name-set comparison would be noise pretending to
   be rigour. The classification is mechanical (a value is a colour if
   it is a six-digit hex or `transparent`), not a hand-maintained list,
   so a colour added to `:root` and forgotten in the dark block still
   fails — and the test additionally asserts that the dark block
   declares *nothing but* colours, so the exemption cannot become a
   hiding place either.

5. **"Matched chroma and lightness" could not survive the colour-blind
   requirement, and the lightness varies on purpose.** Under
   deuteranopia the red-green axis collapses, so eight hues at one
   lightness collapse with it; separation has to come from lightness for
   at least half the pairs. The shipped sets run 3.3:1 to 6.7:1 against
   the light ground and 4.8:1 to 10.9:1 against the dark one. Both sets
   were found by search under three simultaneous constraints — ≥3:1
   against the ground, ≥28° apart on the hue wheel, and maximum minimum
   pairwise ΔE across normal vision, deuteranopia and protanopia — and
   the dark set is the same eight hue angles at the lightness a dark
   ground needs, in the same order, so a legend reads the same in both.

6. **The hash is FNV-1a over UTF-8 bytes, not over JS string code
   units.** FNV is defined over bytes; hashing UTF-16 code units would
   make the slot a property of the host's string representation rather
   than of the value.

7. **Two guards the task did not name were added, both because the
   guards it did name could otherwise pass for the wrong reason.**
   `TestATokenNamedOnlyInACommentIsNotAUse` pins the comment strip in
   the reference scan: without it a token mentioned only in a doc
   comment counts as used and keeps an orphan alive forever, and an
   example spelled in a comment fails the other direction for a name no
   stylesheet was ever asked to have. (This was not hypothetical: the
   first run failed on `var(--data-N)` inside `palette.js`'s own doc
   comment.) `TestThePaletteModuleAndTheStylesheetAgreeOnEight` is the
   seam between the two halves: the stylesheet's `--data-n` count, the
   module's `DATA_SLOTS` and the eight token names it writes out are one
   number, where each of the other guards only ever sees its own half.

8. **`legendFor`'s row order is fixed beyond what the task specified.**
   Count-descending-then-value-ascending orders the *hue* rows; the task
   does not say where the hatched tail and the unset row sit. They are
   last, in that order, always, and the harness asserts the whole row
   shape as one string rather than the ordering of the hues alone.

9. **A known gap, named rather than fixed.** `.diff-added` in
   `styles.css` still hard-codes two greens tuned for a light ground —
   the one place the chrome still spends a hue, and now also the one
   place the new dark theme is wrong. Fixing it means deciding what
   "added" looks like in an achromatic chrome, which is a reading-view
   decision and not a token-layer one; it belongs to the task that owns
   the reading view. Two translucent-black backgrounds (`.notice`,
   `.diff-hunk`) *were* fixed here, to `--ground`, because a black wash
   simply disappears on a dark ground and `--ground` is exactly the "one
   step back from paper" they wanted.

---

### Task 2: The vendored runtime, the import map, and the payload budget

**Files:**
- Create: `internal/web/static/vendor/lit-core.min.js`, `dagre.mjs`,
  `graphlib.mjs`, `manifest.json`, `licenses/`
- Modify: `internal/web/static/game.html` (the import map, as the first
  shell to carry one)
- Create: `internal/web/static_vendor_test.go`
- Modify: `internal/web/static_sinks_test.go`

The import map is what makes `import { LitElement, html } from 'lit'`
work with no build step, and it goes in every shell:

```html
<script type="importmap">
{"imports": {
  "lit": "/static/vendor/lit-core.min.js",
  "dagre": "/static/vendor/dagre.mjs",
  "graphlib": "/static/vendor/graphlib.mjs"
}}
</script>
```

`manifest.json` records, per vendored file: the package, the exact
version, the upstream URL it was fetched from, its SHA-256, its byte
size, and its licence file's path. It exists so the budget and the
provenance are data rather than a claim in a comment — a vendored file in
a public repository is a file this project answers for.

- [x] Tests: `TestVendoredFilesMatchTheirManifest` — for every file under
  `static/vendor/` that is not the manifest or a licence, assert it is
  listed, and assert its SHA-256 and byte size match (a re-vendored file
  that skipped the manifest fails here, which is the only way anyone
  notices); `TestNoVendoredFileIsUnlisted` — the other direction, walking
  the tree; `TestTheVendoredPayloadIsUnderBudget` — sum the sizes,
  assert ≤150KB, and **log the number** so the margin is visible in every
  run rather than only at the failure; `TestEveryVendoredPackageHasItsLicence`;
  `TestNoModuleFetchesFromTheNetwork` — scan every `.js` under `static/`
  (vendor included) for `https?://` outside a comment and for
  `importScripts(`, asserting none: a self-hosted instance on a private
  network has no outbound route and a CDN import would fail there and
  nowhere else; `TestTheImportMapIsIdenticalInEveryShell` — parse the
  `importmap` block out of each shell and compare, because a shell with a
  stale map is a page that 404s one module and renders three quarters of
  itself.
- [x] Modify `static_sinks_test.go`'s perimeter: it walks
  `static/` recursively already; make it **skip `static/vendor/`
  explicitly, with the reason in a comment** (minified upstream code
  contains sink spellings and is not ours to edit) and assert in
  `TestTheSinkPerimeterCoversEveryOwnModule` that every non-vendor `.js`
  file was actually scanned — the shipped comment already warns that the
  perimeter used to name two files literally and miss a third.

- [x] See red: bump one manifest size by a byte and watch
  `TestVendoredFilesMatchTheirManifest` fail on that file; drop the
  `lit` entry from `game.html`'s map and watch
  `TestTheImportMapIsIdenticalInEveryShell` fail; add
  `// see https://cdn.example/x.js` as *code* rather than a comment and
  watch `TestNoModuleFetchesFromTheNetwork` fail.

```bash
git add internal/web/static/vendor internal/web/static/game.html \
        internal/web/static_vendor_test.go internal/web/static_sinks_test.go
git commit -m "build(web): vendor lit and dagre behind an import map, with a manifest and a budget"
```

#### Corrections made during implementation

1. **The import map went into all four shells, not only `game.html`.**
   The task's file list names `game.html` alone ("the first shell to
   carry one"), but its own prose says the map "goes in every shell" and
   the guard it asks for compares the map *across* shells — which, with
   one shell carrying a map, is a comparison of one thing with itself and
   passes on an empty repository. `index.html`, `login.html`,
   `game.html` and `document.html` carry the same map, byte for byte,
   and `TestTheImportMapIsIdenticalInEveryShell` refuses a shell that has
   none rather than skipping it. This is the standing failure pattern of
   this project — a rule established and not carried one step along —
   and it was landing *inside* the task that establishes the rule.

2. **`lit-core.min.js` is not on npm; it comes from the Lit team's own
   distribution repository.** The `lit` package ships entry points that
   re-export three other packages by bare specifier, so it cannot be
   vendored as one file. The single-file bundle lives at
   `github.com/lit/dist`, whose tags track Lit's releases; the manifest
   records the raw URL at tag `v3.3.3` rather than a CDN's
   semver-resolving alias, because an alias is a URL whose bytes can
   change. Its licence is the `lit` package's own `LICENSE`
   (BSD-3-Clause), recorded with its own URL in the manifest.

3. **The network scan reads `.js` *and* `.mjs`.** The task says "scan
   every `.js` under `static/` (vendor included)", and two of the three
   vendored files are `.mjs` — so the sentence as written would have
   exempted two thirds of the code this project did not write, and the
   scan would have reported "no module reaches the network" while never
   opening dagre. `TestTheNetworkScanReadsEveryModuleIncludingTheVendoredOnes`
   exists to make that specific narrowing fail, and it spells the two
   extensions out itself rather than sharing the scanner's list: the
   first version shared it, and narrowing the shared list narrowed the
   scan and its own expectation together, so the mutation that was meant
   to turn it red left it green. Two spellings of one list is the price
   of either being able to judge the other.

4. **The two manifest walks were split by direction rather than by the
   task's wording.** The task gives `TestVendoredFilesMatchTheirManifest`
   both the "assert it is listed" and the hash/size duties and then asks
   `TestNoVendoredFileIsUnlisted` for "the other direction", which is the
   same duty twice. As shipped: the first walks the *manifest* and checks
   each entry against the bytes on disk; the second walks the *tree* and
   fails on any file — module or licence — that nothing lists. A file
   with no provenance and a manifest entry with no file are two different
   accidents and now have two different failures.

5. **Two assertions the task did not name, both because a guard that
   only reads files can be satisfied by files that do not work.**
   `TestEveryImportMapTargetIsServedAsJavaScript` asks the real server
   for every mapped path: `.mjs` is the first extension this repository
   ships that no earlier test made the file server name, and a browser
   refuses a module served as anything but a JavaScript MIME type.
   `TestTheImportMapPrecedesEveryModuleScript` pins the HTML rule that a
   map arriving after the first module script is ignored with an error —
   true today by the habit of putting scripts at the bottom, and habit is
   not a guard.

6. **A Node harness loads the three modules through the shipped map**
   (`internal/web/jstest/vendor_modules_test.mjs`, driven by
   `TestTheVendoredRuntimeLoads`). Hash, size, budget, map and MIME type
   are all satisfiable by three files that do not parse. The harness
   reads the map out of `game.html`, resolves each specifier the way a
   browser would, imports the file and asserts the exports the coming
   tasks import by name. It also pins the thing the third vendored file
   exists for: `@dagrejs/dagre`'s ESM build bundles *its own* copy of
   graphlib and re-exports it, so the `Graph` in `graphlib.mjs` is a
   different constructor, and `dagre.layout` accepts it only because
   dagre reads a graph structurally. Task 6 depends on that entirely and
   a minor version could withdraw it without a word, so it is asserted
   against the real pair — including a check that fails the day dagre
   stops bundling its own copy, since on that day `graphlib.mjs` is
   redundant weight in the budget and a human should decide.

7. **The payload budget lives in the test, not in `manifest.json`.** A
   budget a contributor can raise by editing the same file it is checked
   against is not a budget. The manifest says what each file is; the
   ceiling is a constant in `static_vendor_test.go`, so raising it is a
   diff to a test.

**What was vendored.** `lit` 3.3.3 (15,734 B, BSD-3-Clause),
`@dagrejs/dagre` 3.1.1 (48,559 B, MIT) and `@dagrejs/graphlib` 4.0.5
(13,113 B, MIT): 77,406 B of the 153,600 B budget, 50.4% used. All three
licences permit redistribution provided the notice travels with the code,
which is what `vendor/licenses/` is and what
`TestEveryVendoredPackageHasItsLicence` holds in both directions.

---

### Task 3: The data client — one fetcher, the server's own sentences, and the SSE rules

**Files:**
- Create: `internal/web/static/client.js`
- Create: `internal/web/jstest/client_test.mjs`
- Modify: `internal/web/static_appjs_browser_test.go`

One module owns every call and the stream. No component fetches.

```js
export function client({ slug, fetchImpl = fetch, now = () => Date.now() }) { … }

// runView POSTs …/views/run and returns {ok:true, result} or
// {ok:false, error:{code, pointer, message}} — the server's own three
// fields, unmodified. This client never composes an error sentence: the
// views sub-project generated its prose from its structures and asserted
// it in both directions, and restating it here is exactly the drift that
// work refused. What this client adds is navigation, never wording.
async function runView(key, params, { onStale } = {}) { … }
```

Parameter binding lives in the URL (`?p.class=mage`) and is parsed and
serialised here, so every surface agrees; a parameterised view is a link.

The SSE rules, all four of them from §6.3, live in one reducer that is
exported for testing:

```js
// applyEvent decides what a received event does. It NEVER patches state
// from a payload: publication order is not commit order (events.go), so
// a payload value can be older than what the client already holds. It
// returns one of {reread, band, gone, ignore} plus a reason.
export function applyEvent(state, event) { … }
```

- `view.positions`, `view.background` → `reread` (automatic; a placement
  changed and the question did not).
- `view.upserted` → `band` (the query changed; the picture is not swapped
  under the designer, for **any** role — O7).
- `view.removed` → `gone` (a sentence and a link back; nothing is
  auto-navigated).
- `type.renamed`, `relation_type.renamed` → `reread` **of the view row
  only**, never a silent re-run: the view may have just become stale.
- `document.moved` → `reread` on the prose surfaces, because a path is a
  value that changes and not an identity to cache.
- anything else → `ignore`, and the reducer's default arm is
  `ignore` *with the kind recorded*, so an unhandled kind is visible in a
  test rather than absent from behaviour.

Coalescing is a 750ms debounce keyed by view; a re-read is deferred while
`state.dragging` or `state.pendingWrites > 0`; a hidden document marks
dirty and flushes on `visibilitychange`. The client's own writes come
back as events it cannot distinguish from anyone else's — the payload
carries no writer identity — and that is made fine rather than worked
around: a re-read is idempotent and the three rules keep it cheap.

- [x] Tests (`internal/web/jstest/client_test.mjs`, driven by
  `TestTheDataClientRules`):
  `fortyPositionEventsCauseOneReread` — forty events inside the window,
  assert exactly one fetch; `arereadIsDeferredWhileDragging` — event
  during a drag, assert zero fetches, end the drag, assert one;
  `aRereadIsDeferredWhileAWriteIsUnacknowledged` — same shape over
  `pendingWrites`; `aHiddenTabDefersUntilVisible`;
  `anUpsertedEventBandsAndDoesNotFetchThePicture` — assert the decision
  is `band` and that no `views/run` call was made;
  `TestAViewerGetsTheSameReloadBandAsAnEditor` — the same event under
  both roles, one decision; `aRemovedEventNavigatesNothing` — assert the
  decision is `gone` and that `location.href` was not written;
  `theClientNeverPatchesFromAPayload` — send a `view.positions` event
  carrying plausible coordinates in its payload and assert the state's
  coordinates are unchanged until the re-read's response arrives;
  `TestTheClientNeverSendsAnEntityID` — drive a position write from a
  scene and assert the request body carries `type` and `key` and no uuid;
  `heartbeatCommentsAreNotEvents` — feed `": ping"` frames and assert no
  decision was taken; `theStreamReconnectsWhenTheServerClosesIt` — close
  the stub source, assert a new connection inside the backoff window,
  because the server closes every stream after its lifetime *by design*;
  `errorSentencesAreRenderedVerbatim` — a `query_stale` body with a
  pointer and a message, asserted character-for-character on the returned
  object; `unknownEventKindsAreIgnoredAndRecorded`.

- [x] See red: remove the `pendingWrites` term from the deferral guard
  and watch `aRereadIsDeferredWhileAWriteIsUnacknowledged` fail with one
  fetch instead of zero; make `applyEvent` return `reread` for
  `view.upserted` and watch two tests fail (the band test and the viewer
  test) — **two reds for one mutation is the signal that the second test
  is a control, and it is kept for that reason**; drop the debounce and
  watch `fortyPositionEventsCauseOneReread` report forty.

```bash
git add internal/web/static/client.js internal/web/jstest/client_test.mjs \
        internal/web/static_appjs_browser_test.go
git commit -m "feat(web): one data client for views, with the SSE rules the events payload forces"
```

#### Corrections made during implementation

1. **The stream is read through the same `fetchImpl` as every call, with
   this module's own SSE frame parser, rather than through an
   `EventSource`.** Three of this task's own demands are unmeetable with
   an `EventSource`. It swallows comment lines inside the browser, so
   `heartbeatCommentsAreNotEvents` would have been an assertion about
   Firefox rather than about our code; it reconnects on its own schedule,
   so `theStreamReconnectsWhenTheServerClosesIt` would have asserted the
   browser's backoff and not the one this task is asked to write against
   `defaultSSEMaxLifetime`; and it would have been a second network
   primitive in a module whose whole claim is that it is the only one.
   With one primitive, "no other module fetches" is a source guard over
   one word rather than a list of spellings that a new API could grow
   past. The cost is `parseFrames`, about thirty lines, which is exported
   and asserted directly — a heartbeat, a frame split across two chunks,
   and a comment line inside a frame that must not forge its payload.

2. **`resync` is a fifth rule, and the task's list does not name it.**
   `internal/web/events.go` writes a synthetic `resync` frame whenever a
   subscription's buffer overflowed — its whole meaning is "you missed
   something, go and re-read" — and the task's "anything else → ignore"
   would have sent the one signal the server gives about its own gaps to
   the default arm. It is `reread` over `everything`, with the reason
   `stream.gap`. This is the standing failure pattern of this project (a
   rule established and not carried one step along) and it was landing
   inside the task that establishes the rules.

3. **A decision carries a *target* as well as a verdict**, because two of
   the task's own rules are both `reread` and are not the same call. A
   placement re-runs the picture; a `type.renamed` re-reads the **view
   row only** and must not silently re-run, which the task states and
   which a single `reread` cannot express. The four targets are
   `picture`, `view`, `prose` and `everything`, plus `nothing` for an
   ignore, and `aRenameOverTheWireReadsTheRowAndDoesNotRunTheQuery`
   asserts the two calls are different calls.

4. **A decision never carries a `version`, and `applyEvent` uses `state`
   for relevance.** `view.upserted` does carry one, and its own doc
   comment in `internal/views/events.go` says what it is: the token a
   *write* must carry, never a snapshot to render. Passing it through
   would put a value a caller could store where the rule says only
   identity may go, so the reducer drops it and the re-read returns the
   current one. `theClientNeverPatchesFromAPayload` runs every kind with
   a poison value in every non-identity slot and asserts nothing but the
   named identity fields appears anywhere in the decision — and it deep
   **freezes** the state it passes in, so a reducer that patched would
   throw rather than merely fail an assertion.

5. **The error object carries `details` and `status` beside the three
   fields.** A query refusal can name several problems (`details.fields`)
   and a staleness report carries its own diagnostics
   (`details.stale`), so handing on only the first pointer would hide the
   rest; `details` travels whole and unmodified, `pointer` is the first
   address the refusal named. `status` is there for the case the task
   does not discuss: a transport failure or an unreadable body. That
   answer comes back with an **empty code and an empty message** rather
   than an invented sentence — the module has no prose and does not grow
   any for this — and the surface that renders negative states (Task 4)
   owns the words. `anUnreadableAnswerGetsNoInventedSentence` pins it.

6. **The rule against composing a sentence is held by shape, in
   `internal/web/static_client_test.go` — a fourth file this task did not
   name.** Stating the rule in a comment is the thing this plan's
   preamble calls a mechanism nothing reads, and the property has no
   runtime signature: a client that composed one sentence would pass
   every harness check that did not provoke that exact refusal. So: **no
   string literal in `client.js` carries whitespace, and none is longer
   than 48 characters.** A sentence has spaces; a path segment, a header
   name, an event kind, a decision and a reason do not — which is why the
   module's own reasons are spelled `placement.moved` and
   `kind.unhandled`. The length bound is the second half, because the
   scanner reads `\n` as its two characters (the frame separator `"\n\n"`
   is protocol, not prose) and a sentence could otherwise hide behind
   escapes. `TestTheSentenceGuardReadsWhatItClaimsTo` is the guard on the
   guard, in both directions: it pins literals the module is known to
   contain, and it feeds the scanner a fixture carrying two sentences —
   one of them in a comment, which must *not* be read — and asserts it
   finds exactly the two in code. The one place the module genuinely
   needs a space is the optional one after an SSE field's colon, and it
   is spelled `charCodeAt(0) === 32`.

7. **The same file holds the fetch perimeter, which is the guard that
   keeps Task 2's network scan meaning what it says.** That scan asks
   *where* a module reaches; this task creates the module that reaches at
   all, so a second guard asks *which* module:
   `TestOnlyTheDataClientAndTheShippedBundleFetch` reads every own module
   for `fetch`, `EventSource`, `XMLHttpRequest`, `WebSocket`,
   `sendBeacon` and `importScripts`, and admits exactly two — `client.js`
   and the shipped `app.js`, whose wrappers the four existing shells
   already call. `doc.js` is deliberately not on the list even though it
   is a page, because it reaches the server only through app.js's
   wrappers, which is what makes the list mean something. Every entry
   must also *name a file that exists and really does fetch*, so an
   allowance cannot outlive its reason and sit there for the next module.
   `TestTheDataClientNavigatesNothing` is the third: the module names no
   `location`, `pushState`, `replaceState` or `window.open`, which is the
   half of `aRemovedEventNavigatesNothing` that holds for every event
   rather than for the one the harness sends.

8. **A URL binds text, always, and a declaration converts it.**
   `readParams` returns strings for everything, including `20`: a URL
   carries no types, and guessing would collapse `20` and `"20"` into one
   value, which is a distinction the envelope preserves and the palette
   spends a legend row on. `typeParams(declarations, text)` is where the
   query document's own `params` — `text`, `number`, `bool` — turn text
   into values, and **a value it cannot convert passes through
   unchanged** so the server refuses it with its own pointer and its own
   sentence rather than this module refusing it with a copy of a rule
   that lives elsewhere. `writeParams` keeps the page's own query keys,
   so binding a parameter does not silently drop a tab or a cursor.

9. **The coalescing window is not a resetting debounce.** A resetting one
   is reset forever by an agent writing steadily, and the picture would
   never catch up; the window opens on the first event and closes once,
   so a re-read happens at most `REREAD_DEBOUNCE_MS` after the first
   event that asked for one.

10. **The `client` signature gained three injected seams and lost one.**
    `setTimer`, `clearTimer` and `random` are injected, each defaulting to
    the browser's own, because a 750ms window and a reconnect backoff
    cannot be asserted on a real clock without sleeping and a harness that
    sleeps is a harness that flakes. The task's `now` came *out* again:
    nothing in the shipped client reads a wall clock — the window and the
    backoff are both expressed as timers, which is the seam a test
    actually needs — and an injected clock nothing called would be a
    mechanism nothing reads. Two other unread surfaces went the same way
    in the same pass (`state.lastDecision`, and `receive` on the returned
    object), and `clearTimer` earned its place by acquiring a real reader:
    `disconnect` now cancels a re-read that has not happened yet, because
    a timer firing into a closed surface is a request nobody asked for.

11. **A comment skip that no mutation could turn red came out again.**
    `parseFrame` began with an explicit `line.startsWith(":")` skip for
    heartbeat comments. Removing it changed the parse of no input anybody
    can write — a comment line has nothing before its colon, so its field
    name is empty and is neither `event` nor `data` — which makes it a
    guard no test could ever fail on. What carries the rule instead is
    the empty-block check, whose removal *does* turn
    `heartbeatCommentsAreNotEvents` and `aHeartbeatOverTheWireTakesNoDecision`
    red, and a new frame case asserting a comment line inside a frame
    cannot forge that frame's payload. It shipped in a follow-up commit
    rather than being quietly fixed, because the first version of this
    task shipped it.

12. **A reconnection schedules a re-read; the first connection does not.**
    The hub keeps no history (`realtime.Hub`'s own doc comment, and
    `handleEvents`'s), so everything published while this client was away
    is gone and the server's own contract is that a reconnected client
    refetches over REST. The first connection is the exception because
    the page has just run its view, and a re-read there would be a
    duplicate request on every page load.

13. **The harness checks keep the plan's names, with the leading article
    normalised** (`aRereadIsDeferredWhileDragging`), and the two the plan
    spells with a Go `Test` prefix —
    `TestAViewerGetsTheSameReloadBandAsAnEditor` and
    `TestTheClientNeverSendsAnEntityID` — are JavaScript checks inside
    `client_test.mjs`, because both are properties of the module and not
    of any route. The whole harness is driven from Go by
    `TestTheDataClientRules`.

---

### Task 4: The view frame and every negative state, once, for all six

**Files:**
- Create: `internal/web/static/render/scene.js`
- Create: `internal/web/static/components/mst-view-frame.js`
- Create: `internal/web/jstest/frame_test.mjs`
- Modify: `internal/web/static_appjs_browser_test.go`

This task is the load-bearing one. The six renderers are drawings; the
frame is where a designer learns that the drawing is not the whole truth,
and it is written **before** any renderer so no renderer can invent its
own version of these states.

`scene.js` exports the vocabulary and the banner builder:

```js
// bannersFor reads the envelope and returns the banner stack in the
// fixed order the spec commits to: stale, dropped, truncation,
// auto-placement, ambiguity. Fixed because a designer learns positions
// and because two of these routinely co-occur.
export function bannersFor(envelope, { placedAutomatically, droppedRefs }) { … }
```

The rules it encodes, each of which is a test below:

- Three truncation flags produce **three** sentences, never one summary,
  and only for the flags that are set.
- When all three are false the frame says **nothing**. It never says
  "complete": `execute.go` is explicit that a one-hop query cannot set
  `truncated.depth` at all, so `false` means "not measured" as often as
  it means "not truncated".
- `ambiguous` produces one banner counting the nodes, whose sentence
  names *nodes* and not slots, because the flag is on the node.
- `stale` with `on_stale: fail` produces **no picture**: the frame
  returns `{kind: "diagnostics", rows}` instead of a scene, one row per
  `Diagnostic` with the server's sentence, the pointer in mono, and
  `was → now`, plus the single **Run anyway (best effort)** action.
- A best-effort run bands permanently, in amber, naming what was dropped,
  and the dropped legend rows render in `--dropped`.
- `*_renamed` diagnostics do **not** enter the banner stack: the picture
  is full and correct, so they are a quiet title-strip line that expands
  to the pointers, and no repair action is offered (O6).
- `param_unbound` is answered in the parameter bar — the missing control
  is highlighted and named — and not with a JSON pointer.
- An empty answer with no diagnostics and no truncation is a **success**:
  the same frame, footer reading `0 nodes, 0 edges`, one sentence, plus
  what ran (renderer, sets, parameter values). No `--danger`, no warning
  glyph, and **no guess at the cause** — the envelope does not carry how
  many entities were considered, so "0 of 340 matched" is unavailable and
  "try widening your filter" is advice from no information.

The footer strip is always present: `stats.nodes`, `stats.edges`,
`stats.max_depth_reached`, `stats.duration_ms`, and the layout engine's
own elapsed milliseconds when it ran (Task 6 supplies it). Two numbers,
because "the query is slow" and "the picture is slow" must be
distinguishable without a profiler.

- [x] Tests (`internal/web/jstest/frame_test.mjs`, driven by
  `TestTheViewFrame`): `threeTruncationFlagsAreThreeSentences`;
  `oneTruncationFlagIsOneSentence`;
  `TestACleanEnvelopeProducesNoCompletenessClaim` — a clean envelope,
  assert the banner list is empty **and** assert the rendered text
  contains neither "complete" nor "all"; `ambiguityCountsNodesNotSlots` —
  two ambiguous nodes with two slots each, assert the sentence says 2;
  `aStaleFailureDrawsNoScene` — assert the returned kind is
  `diagnostics`, that no node marks exist, and that the row carries the
  server's message unmodified and its pointer;
  `theBestEffortActionRerunsWithTheFlag` — click it, assert the client
  was called with `on_stale: "best_effort"`;
  `aBestEffortPictureBandsPermanentlyAndGreysTheDropped`;
  `aRenamedDiagnosticIsNotABannerAndOffersNoRepair` — assert the banner
  stack is empty, the title strip carries the line, and no action with a
  repair verb exists in the emitted tree;
  `anUnboundParameterHighlightsItsControl` — assert the bar marks
  `class` and that the diagnostics panel is not shown;
  `anEmptyAnswerIsASuccess` — assert no `--danger` class anywhere in the
  emitted tree, that the footer reads `0 nodes, 0 edges`, and that the
  sentence names the renderer and the bound parameters;
  `anEmptyAnswerGuessesNoCause` — assert the text contains no "try", no
  "of" count and no "filter"; `bannersKeepTheirFixedOrder` — an envelope
  that is stale-best-effort, truncated, auto-placed and ambiguous at
  once, assert the five bands in the declared order;
  `parametersRoundTripThroughTheURL` — bind two, serialise, re-parse,
  assert equality, including a value containing `&` and a UTF-8 name.

- [x] See red: delete the `truncated.depth` arm and watch
  `threeTruncationFlagsAreThreeSentences` report two; make the empty
  state reuse the error class and watch `anEmptyAnswerIsASuccess` name
  the class; move `*_renamed` into `bannersFor` and watch two tests fail
  (the renamed test, and `bannersKeepTheirFixedOrder`, whose expected
  list grows) — the second is the control; add the string "complete
  picture" to the footer and watch
  `TestACleanEnvelopeProducesNoCompletenessClaim` fail.

- [x] Hand check: open a stale view and read the panel. The question is
  whether a designer who did not write the query can tell **which step**
  broke from the pointer alone.

```bash
git add internal/web/static/render/scene.js \
        internal/web/static/components/mst-view-frame.js \
        internal/web/jstest/frame_test.mjs internal/web/static_appjs_browser_test.go
git commit -m "feat(web): the view frame, and the negative states designed once for all six renderers"
```

#### Corrections made during implementation

1. **A `Diagnostic` on the wire carries no sentence, and the panel is
   built from `details.fields` instead.** The task says "one row per
   `Diagnostic` with the server's sentence"; `internal/views/stale.go`'s
   `Diagnostic.message()` is **unexported**, and `internal/web`'s
   `staleDetails` publishes `details.stale[]` as `{code, pointer, was,
   now}` and nothing else. The sentences are in `details.fields[]`, as
   `{path, message}`, addressed by the same pointer — `staleQuery` fills
   both lists from one source precisely so the two can be joined.
   `diagnosticsFor` therefore walks `fields`, which is also the **wider**
   list: a stored query can be broken at a position no diagnostic covers,
   and walking the diagnostics would have dropped that reason silently.
   The code and the was/now pair are read off `details.stale` by pointer.

2. **A *successful* best-effort envelope carries no sentence either**, so
   the stale band shows pointers rather than prose. `Result.Stale` is the
   same `Diagnostic` list with no message on it, and composing one here
   is exactly what Task 3's rule forbids. The **dropped** band is the one
   that has sentences, because they come from the refusal the designer
   overrode, carried across the re-run in `droppedRefs`.

3. **The stale slot and the dropped slot never both speak.** They are one
   fact — a non-rename diagnostic can only reach a *successful* envelope
   through best effort, since every one of them is raised beside a
   resolution problem that makes `fail` refuse — so banding both would
   tell a designer the same thing twice under two headings. The dropped
   band wins when the caller has the refusal's own sentences; the stale
   band answers alone when it does not (a page loaded straight into a
   best-effort link has the envelope and not the refusal).
   `theStaleBandAndTheDroppedBandAreNeverBothSaid` pins both directions.

4. **The band is `--dropped`, not amber.** The spec's §4.2 says amber and
   its own §2.3 declares no amber: Task 1's central rule is that the
   chrome is achromatic with exactly two chromatic exemptions
   (`--danger`, `--focus`), and a third one for this band would spend a
   channel the product has already given away to `color_by`.
   `--dropped: #b0a89a` is what §2.3 declares for "what best_effort
   removed" and is what the band and its rows wear.

5. **`--dropped` is declared in this task, which is where Task 1 left
   it.** Task 1's correction 2 deferred it here with the argument that a
   token nothing reads is a lie; carrying that one step along is this
   task's job, and it now has two readers — `scene.js` names it on the
   band and its rows, and the component's swatch paints with it. It is
   deliberately not held to 3:1: it is never the only carrier, since the
   band counts the dropped references in a sentence and lists every one
   of them as text.

6. **The truncation sentences cannot name the cap.** The spec writes
   *"This picture is capped at 1000 nodes"*; the envelope carries no cap
   — `limit` lives in the query document, which the frame does not read —
   so the number would have been invented or plumbed through a second
   surface for one word. The three sentences say that a cap was hit and
   that the answer had more, which is exactly what `truncated` knows.

7. **`param_unbound` gets its own frame kind rather than a flag.** The
   four kinds (`picture`, `empty`, `diagnostics`, `unbound`) are mutually
   exclusive answers to "what is under the title strip", and each is
   produced by a condition no other can meet — which is what lets
   `everyThingHasItsOwnState` assert that nine fixtures have nine
   distinct signatures, rather than each negative test standing alone on
   a fixture that might also satisfy its neighbour.

8. **A refused run's footer counts nothing rather than counting zero.**
   Found by the hand check: the panel read `0 nodes, 0 edges · depth 0 ·
   query 0 ms` under a refusal, which is the *empty answer's* footer,
   word for word, on a run that measured nothing at all. The strip is
   still always present and is now silent, for the same reason
   `execute.go` refuses to return an unplaced node at the origin.
   `aRefusalCountsNothingBecauseItMeasuredNothing` holds it with the
   empty answer as its control.

9. **Run anyway is offered for `query_stale` and for nothing else.** Best
   effort prunes what the *game* moved; offering it on a `query_invalid`
   refusal would be a button that fails the same way twice.
   `anInvalidRefusalOffersNoBestEffortAction` is also what tells the two
   refusal fixtures apart in the signature test above.

10. **`placedAutomatically` and `droppedRefs` are the caller's, because
    the envelope cannot supply either.** A successful envelope does not
    echo the `on_stale` it ran under (`runOutput` sends nodes, edges,
    stats, truncated, stale and positions), and "which node the engine
    had to place" is knowable only where the saved positions and the
    layout meet. Both default to nothing, so a caller that knows neither
    gets no band rather than a guessed one.

11. **The component's silence is held by a source-shape guard in a fifth
    file the task did not name**, `internal/web/static_frame_test.go`.
    Every sentence lives in `scene.js`, which a harness reads and a
    mutation turns red; a component that hard-coded one would render
    perfectly and pass every check in `frame_test.mjs`, which never loads
    it — and the next five renderers would each grow their own copy,
    which is the exact drift this task exists to prevent.
    `TestTheViewFrameSpeaksOnlyTheModelsWords` holds that every text node
    in the templates is whitespace or an interpolation, and
    `TestTheTemplateTextScanReadsWhatItClaimsTo` is the guard on the
    guard, in both directions: it catches a planted sentence, it does not
    read an interpolation, a comparison, an arrow function or a comment
    as prose, and it proves the `css` skip did not eat the templates
    after it.

12. **The task's own control for the rename mutation does not fire, and
    was replaced.** Moving `*_renamed` into `bannersFor` was to fail
    `bannersKeepTheirFixedOrder` "whose expected list grows"; it does
    not, because that fixture's dropped band suppresses the stale slot
    and its diagnostic is a missing rather than a rename — a rename would
    ride into the stale band's *rows* without changing a single code.
    `theStaleSlotSitsAheadOfEverythingElse` now carries a rename beside a
    missing field and asserts the rows, so the mutation turns two checks
    red as the task intended.

13. **Names normalised, as Task 3 did.** The plan's
    `TestACleanEnvelopeProducesNoCompletenessClaim` is the JavaScript
    check `aCleanEnvelopeProducesNoCompletenessClaim`: it is a property
    of the module and not of any route, and the whole harness is driven
    from Go by `TestTheViewFrame`.

14. **The hand check was made against the model, not a browser, and that
    is recorded rather than claimed otherwise.** No route mounts the
    frame until Task 15, so the panel was rendered for a three-step
    query — `/traverse/2/via/0`, `/from/0/where/value`,
    `/project/color_by` — and read. A designer who did not write the
    query can tell which step broke from the pointer alone: the first
    names the third traversal step and its first `via`. What they cannot
    do yet is *see* that step, because the read-only query view (spec
    §7.4) is Task 15's; until then the pointer is an address into a
    document they have to open elsewhere.

---

### Task 5: The text twin, and the accessibility spine

**Files:**
- Create: `internal/web/static/components/mst-twin.js`
- Create: `internal/web/jstest/twin_test.mjs`
- Modify: `internal/web/static/components/mst-view-frame.js`

The canvas is `aria-hidden`; the twin is the accessible content of every
view including the five graphical ones. Two tables from the same
envelope: nodes (name, type, key, and one column per projection slot) and
edges (source, type, target, label). It is always in the DOM, one
keystroke away, and it is also the keyboard path into the canvas —
focus moves through the twin's rows and the focused row highlights its
node.

It is built **before** the renderers on purpose: every renderer task can
then assert that its picture and the twin describe the same answer, and
the twin is what a designer falls back to below tablet width (§9).

- [ ] Tests: `everyNodeInTheEnvelopeHasARow` — including nodes the
  renderer shelved, dropped beyond `max_depth`, or collapsed into a count
  chip, because the twin is a description of the *answer* and not of the
  drawing; `everyEdgeHasARowIncludingStubs` — a stub's far end is its id,
  labelled as outside the picture; `absentSlotsRenderAsAnEmDashNotBlank`;
  `theTwinCarriesTheValueTextForEveryColouredNode` — the property that
  makes "colour is never the only carrier" true, asserted rather than
  claimed; `focusingARowSelectsItsNode` — assert the selection callback
  fired with the node key; `theCanvasIsAriaHiddenAndTheTwinIsNot`;
  `theTwinRendersEveryStringAsText` — a node named
  `<img src=x onerror=…>` arrives through `textContent`.

- [ ] See red: drop the shelved nodes from the twin's row source and
  watch `everyNodeInTheEnvelopeHasARow` fail with a count difference;
  remove `aria-hidden` from the canvas and watch its test fail.

```bash
git add internal/web/static/components/mst-twin.js \
        internal/web/jstest/twin_test.mjs \
        internal/web/static/components/mst-view-frame.js
git commit -m "feat(web): the text twin, which is every view's accessible content"
```

---

### Task 6: Layout — dagre in a worker, the composition rule, the budget and the fallback

**Files:**
- Create: `internal/web/static/layout/budget.js`, `engine.js`,
  `compose.js`, `worker.js`
- Create: `internal/web/jstest/layout_test.mjs`

`engine.js` wraps dagre and is pure: nodes with measured sizes and edges
in, coordinates out, no DOM. `compose.js` implements §5.3 exactly:

```js
// compose applies the layout_mode contract. The server reads none of
// these values (0008_views.sql says so); this function is the reader,
// which is the answer to "what reads that column".
export function compose(mode, computed, stored /* [{key, x, y, pinned}] */) {
  // auto:   stored positions ignored entirely, never deleted; dragging
  //         is disabled by the caller and the canvas says why.
  // manual: every stored position is honoured; the unplaced sub-graph is
  //         laid out and NOT written back — safe only because the engine
  //         is deterministic and the envelope's node order is stable.
  // mixed:  1. lay out the whole graph, ignoring stored positions
  //         2. fit that shape to the pinned nodes with a similarity
  //            transform — translation and uniform scale, NO rotation —
  //            minimising squared error; ≥2 pinned determines it, 1
  //            degenerates to a translation, 0 is the identity and mixed
  //            behaves as auto
  //         3. restore every pinned node to its exact stored coordinate,
  //            then one deterministic separation pass over the UNPINNED
  //            nodes only, in entity-key order.
  // The separation pass is a REDUCTION of overlap, not a guarantee of
  // none. Stated here so nobody later reads a promise into it.
}
```

`worker.js` is a module worker that imports the vendored dagre directly —
which is the second reason the no-build-step rule survives. The budget
lives in `budget.js` and **the banner sentence is generated from the
number**:

```js
export const LAYOUT_BUDGET_MS = 2000;   // O10: a guess about patience, not a measurement
export const LAYOUT_RETRY_MS = 10000;
export const budgetSentence = (ms) =>
  `Layout did not finish in ${ms / 1000} seconds; nodes are arranged in a grid. ` +
  `Narrow the query, or drag what matters.`;
```

The fallback is a deterministic grid ordered by node type then entity
key, and it is **always announced**: a grid presented without explanation
reads as the graph having no structure, which is a false statement about
the game. Retry is explicit, per view, one step to `LAYOUT_RETRY_MS`, and
never automatic — the second attempt is on the same failing input.

Re-runs are incremental by identity (§5.5): nodes in both envelopes keep
their coordinates, the engine runs over the new ones treating the
retained ones as obstacles, and a full re-layout happens only on an
explicit *re-arrange* or when the retained set is under half the new one.

- [ ] Tests (`internal/web/jstest/layout_test.mjs`, driven by
  `TestLayoutComposition`): `theEngineIsDeterministic` — the same input
  twice, byte-identical coordinates, and a third run with the node array
  shuffled asserting the same result, since the whole `manual` mode rests
  on it; `pinnedNodesNeverMove` — mixed mode with three pinned nodes,
  assert their output coordinates are their stored ones exactly (`===`,
  not within a tolerance); `mixedWithNoPinnedNodesIsAuto` — assert
  equality with the `auto` result; `mixedWithOnePinnedNodeTranslatesOnly`
  — assert every pairwise distance is preserved; `theFitDoesNotRotate` —
  construct pinned nodes whose best-fit rotation is 90°, assert the
  output is not the rotated one, because a rotated diagram is
  unrecognisable and rotation is exactly what a full similarity fit would
  choose; `autoIgnoresStoredPositionsAndDeletesNothing` — assert the
  stored array is unmutated and unused; `manualDoesNotWriteBackPlacements`
  — assert the composer returns no write intent;
  `unplacedNodesLandInTheSameSpotOnEveryLoad`;
  `theSeparationPassRunsInEntityKeyOrder` — two colliding unpinned nodes,
  assert which one moved, twice, on shuffled input;
  `theBudgetTerminatesAndTheGridIsAnnounced` — a stub engine that never
  returns, assert the grid, and assert the banner text equals
  `budgetSentence(LAYOUT_BUDGET_MS)` **computed from the constant**, so
  changing the constant changes the sentence;
  `theGridIsOrderedByTypeThenKey`;
  `theRetryUsesTheRetryConstantAndDoesNotEscalateTwice`;
  `aRerunKeepsRetainedCoordinates` — 40 nodes, 12 added, assert the 40
  are untouched and the 12 are new; `aMostlyNewResultRelaysOutFully` —
  retained under half, assert a full re-layout;
  `layoutElapsedIsReportedToTheFrame`.

- [ ] See red: delete the "no rotation" restriction from the fit and
  watch `theFitDoesNotRotate` fail; make `manual` write placements back
  and watch `manualDoesNotWriteBackPlacements` fail; change
  `LAYOUT_BUDGET_MS` to 3000 and watch the budget test fail **only** if
  the sentence were hard-coded — run it once with a hard-coded sentence
  to see the guard work, then restore the generated one; sort the
  separation pass by insertion order and watch its test fail on the
  shuffled run.

- [ ] Hand check: a 500-node graph in a browser. Does the ranked drawing
  read as structure? The engine choice (§5.2) rests on a claim — that a
  game's content graph is directional enough for a ranked layout to beat
  a force one — and this is the first moment anybody can see whether that
  is true.

```bash
git add internal/web/static/layout internal/web/jstest/layout_test.mjs \
        internal/web/static_appjs_browser_test.go
git commit -m "feat(web): deterministic layout in a worker, with the composition rule the layout modes promise"
```

---

### Task 7: The canvas — pan, zoom, the SVG emitter and the drag layer

**Files:**
- Create: `internal/web/static/components/mst-canvas.js`
- Modify: `internal/web/static/render/scene.js` (`joinEdges`, the emitter
  contract)
- Create: `internal/web/jstest/canvas_test.mjs`
- Modify: `internal/web/jstest/` DOM stub (`createElementNS`, attributes)

The emitter is deliberately dumb: one scene mark becomes one SVG element
with attributes taken from the mark. It has no opinion about colour,
absence or truncation — those were decided in Tasks 1 and 4 and are
carried in the mark. This is what makes the renderers testable without a
browser and the emitter testable without a renderer.

`joinEdges(nodes, edges)` returns `{drawn, stubs}` and is the one place
the "an endpoint may not be in `nodes`" rule is implemented, because six
implementations of it would be five bugs.

The drag layer is the one performance-shaped decision: during a drag only
the dragged nodes and their incident edges are re-rendered, on a detached
layer moved by a transform, and never the whole tree.

- [ ] Tests: `theEmitterWritesEveryGameStringAsText` — a node named with
  markup, assert `textContent`; `sceneOrderIsPaintOrder` — assert labels
  emit after their nodes and the drag layer after everything;
  `joinEdgesSeparatesStubs` — an edge with one endpoint outside `nodes`,
  assert it appears in `stubs` and not in `drawn`, with the positive
  control of a fully-joined edge in the same test;
  `bothEndpointsMissingIsAlsoAStub`; `zoomAndPanMoveOneTransform` —
  assert the image layer and the node layer share the transform (the
  property that makes a map honest);
  `labelsStopScalingOutsideTheBand` — 0.75×–1.5×, asserted at 4× zoom;
  `aDragTouchesOnlyTheDraggedSubtree` — assert the number of elements
  whose attributes changed equals the dragged nodes plus their incident
  edges, with a 200-node fixture so a full re-render is unmissable;
  `theCanvasIsFullBleedAndThePanelsFloat` — assert the emitted classes,
  since the 68ch measure applies to reading surfaces and a diagram inside
  it is unusable.

- [ ] See red: re-render the full tree on drag and watch
  `aDragTouchesOnlyTheDraggedSubtree` report 200-odd; apply the zoom
  transform to the nodes and not the background image and watch
  `zoomAndPanMoveOneTransform` fail — this is the mutation that produces
  a map whose pins drift off the terrain, which is the single worst bug
  this component can have.

```bash
git add internal/web/static/components/mst-canvas.js \
        internal/web/static/render/scene.js internal/web/jstest/canvas_test.mjs \
        internal/web/jstest internal/web/static_appjs_browser_test.go
git commit -m "feat(web): the SVG canvas, its dumb emitter and a drag layer that touches one subtree"
```

---

### Task 8: `graph`

**Files:**
- Create: `internal/web/static/render/graph.js`
- Create: `internal/web/jstest/render_graph_test.mjs`

Rounded rectangles sized to the label — not circles, which either clip a
name or float it. Fill from `color_by`, 1px `--line` outline, edges as
1px `--muted` curves, `arrows` drawing a head at the target,
`edge_labels` drawing `edge.label` on a `--ground` plate. `size_by` maps a
numeric slot to node **area** over a bounded 1×–3× range. `group_by`
draws a hairline enclosure with a heading; `cluster_by` draws nothing and
feeds the layout, and the tooltip on each control is the only place a
designer learns the difference.

**The negative half.**

- [ ] Tests: `anAbsentColourSlotIsDashedAndUnfilled` — assert
  `fill === "none"` and the dashed outline, plus a legend row with the
  `unset` kind; `anAbsentSizeSlotTakesTheRangeMinimum`;
  `twoAbsencesAreTwoMarks` — a node missing both `color_by` and `size_by`,
  assert dashed **and** minimum, because a single "unknown" treatment
  would collapse two facts into one; `sizeIsBoundedAtThreeTimes` — a slot
  value of 10^6, assert the area ratio is exactly 3;
  `sizeMapsAreaNotDiameter` — a value four times another produces twice
  the width, asserted numerically because getting this wrong is invisible
  and universal; `anAmbiguousNodeCarriesItsMarkAtTheCorner` — and assert
  the mark is on the node, with no per-slot mark anywhere;
  `stubsLeaveTheirGroupEnclosure` — assert the stub's terminus is outside
  the enclosure rect, which is why enclosures are hairlines and not
  filled panels; `clusterByDrawsNothing` — assert the scene has no mark
  attributable to it, and assert the layout call received the clustering,
  so "draws nothing" does not decay into "does nothing";
  `edgeLabelsWithoutLabelledEdgesCannotHappen` — a comment-level
  assertion plus a test that the renderer does not crash if it ever does,
  since the catalogue refuses the combination at save time and the client
  never meets it; `theTwinAndTheSceneAgreeOnNodeCount`.

- [ ] See red: return the range minimum for a *present* size slot and
  watch `sizeIsBoundedAtThreeTimes` and `sizeMapsAreaNotDiameter` fail;
  drop the dashed outline for an absent colour and watch its test name
  the node; put the ambiguity mark on the slot and watch its assertion
  find a per-slot mark.

- [ ] Hand check: a hundred nodes coloured by an eleven-value slot. Do
  eight hues plus a hatch read at 11px, and is the hatched tail legible
  as *a tail* rather than as a ninth colour?

```bash
git add internal/web/static/render/graph.js internal/web/jstest/render_graph_test.mjs \
        internal/web/static_appjs_browser_test.go
git commit -m "feat(web): the graph renderer, absences marked twice where there are two"
```

---

### Task 9: `layered`

**Files:**
- Create: `internal/web/static/render/layered.js`
- Create: `internal/web/jstest/render_layered_test.mjs`

Ranks along `rank_direction`, edges preferentially straight,
`layer_labels` drawing a muted rule and a caption per rank — the rank
index for `rank_by: "edges"`, the **field's value** for a numeric
`rank_by`, which is the more useful caption and the reason that parameter
exists. `align` positions a node within its band.

**The negative half**, and here it is the whole point: this renderer's
consumption note says "expected mostly acyclic", the engine breaks cycles
by reversing edges, and the interface must not hide that.

- [ ] Tests: `aReversedEdgeKeepsItsTrueArrowhead` — a three-node cycle,
  assert the arrowhead of the reversed edge points at its real target and
  that the mark carries the double-slash;
  `aCycleIsCountedInTheFrame` — assert the sentence names 3 and says the
  graph has a cycle; `theCycleSentenceIsNotAnAnalysisClaim` — assert the
  text does not name the nodes in the cycle or use the word "unreachable":
  this is a drawing artefact honestly reported and sub-project 6 owns the
  real answer; `anAbsentNumericRankGoesToATrailingUnrankedBand` — assert
  the band exists, is captioned, is **last**, and is not rank zero, where
  it would read as a starting point;
  `layerCaptionsAreTheFieldValueForANumericRankBy` and
  `layerCaptionsAreTheIndexForRankByEdges` — two tests, because one
  fixture cannot tell the two policies apart;
  `rankDirectionLRTransposesTheScene`; `alignPositionsWithinTheBand` —
  start, center and end all asserted, since two of the three would pass a
  test written for one.

- [ ] See red: sort the reversed edge's endpoints instead of marking it
  and watch `aReversedEdgeKeepsItsTrueArrowhead` fail; place unranked
  nodes at rank zero and watch its test find them first; caption every
  rank with its index and watch
  `layerCaptionsAreTheFieldValueForANumericRankBy` fail while the other
  caption test stays green — the pair is what makes the mutation visible.

- [ ] Hand check: a progression with a genuine cycle. Is the
  double-slash findable without being told it is there?

```bash
git add internal/web/static/render/layered.js internal/web/jstest/render_layered_test.mjs \
        internal/web/static_appjs_browser_test.go
git commit -m "feat(web): the layered renderer, which reports the cycle it had to break"
```

---

### Task 10: `nested`

**Files:**
- Create: `internal/web/static/render/nested.js`
- Create: `internal/web/jstest/render_nested_test.mjs`

Boxes inside boxes; a container is a `--paper` rectangle with a hairline
and its name top-left; the recursion stops at `max_depth`; leaves use
`leaf_label` when given. `color_by` tints the **header strip**, not the
box, because a nest of four filled levels is four overlapping fills and
no legible text.

**The negative half**, all three cases real.

- [ ] Tests: `beyondMaxDepthACountChipExpandsWithoutARerun` — assert the
  chip reads `+12`, assert expanding it issues **zero** fetches, since
  the nodes are already in the envelope and treating a drawing depth as a
  fetch boundary would make one parameter mean two things;
  `aTopLevelNodeAndAnOrphanOfTheCapDoNotLookAlike` — two nodes at the top
  level, one a real root and one whose parent was truncated away; assert
  the second is dashed and counted in the truncation banner and the first
  is neither, **with both in one fixture** so the test cannot pass by
  treating them the same; `aContainmentCycleStopsAtTheRepeat` — A
  contains B contains A, assert the recursion terminates, the repeated
  box carries the cycle glyph, and the frame names **both** ends;
  `colourTintsTheHeaderNotTheBox` — assert the box fill is `--paper` at
  every level; `leafLabelFallsBackToTheName`;
  `theTwinListsEveryNodeIncludingThoseBeyondMaxDepth` — the twin is a
  description of the answer, not of the drawing.

- [ ] See red: recurse without the repeat check on the cyclic fixture and
  watch the test fail on a stack overflow rather than a glyph — then
  restore, because a crash is the failure mode this test exists to make
  loud; draw the orphan of the cap identically to a root and watch its
  test name the dash.

- [ ] Hand check: four levels deep with tinted headers. Is the nesting
  legible, or does it need the fills the design refuses?

```bash
git add internal/web/static/render/nested.js internal/web/jstest/render_nested_test.mjs \
        internal/web/static_appjs_browser_test.go
git commit -m "feat(web): the nested renderer, which will not draw a tree over a graph that is not one"
```

---

### Task 11: `map`

**Files:**
- Create: `internal/web/static/render/map.js`
- Create: `internal/web/jstest/render_map_test.mjs`

The one renderer with a ground. Background at `background_scale` from
`background_offset` at full opacity; nodes as 7px discs, not labelled
boxes; labels offset up-right at 11px with a 2px `--ground` halo so they
survive over any image; zoom and pan move image and nodes **together,
always**. `snap` (manual mode only) draws a muted grid at ≥1× zoom, under
the background.

**The negative half** — and the first-run case here is the product's most
impressive feature meeting a designer for the first time.

- [ ] Tests: `aNodeWithNoCoordinateFieldGoesToTheShelfNotTheOrigin` —
  `coordinate_source: "fields"` with an absent `x`; assert the node is on
  the shelf, is counted in a banner, and that **no mark exists at (0,0)**,
  because `(0,0)` is a place a designer may have deliberately used and
  `execute.go` refuses to return unplaced as a coordinate for exactly
  that reason; `aNodeMissingOnlyYIsAlsoShelved`;
  `aFreshManualMapIsNotTheEmptyState` — every node unplaced, assert the
  background is drawn, the shelf holds all of them, the sentence is
  *"Nothing has been placed yet. Drag a node from the shelf onto the
  map."*, and assert the generic empty-state sentence is **absent**;
  `aRemovedBackgroundKeepsEveryCoordinate` — `background_asset_id` gone
  (`ON DELETE SET NULL` makes this a normal transition between two runs),
  assert the ground is plain, the frame carries the line, and the node
  coordinates are unchanged; `snapIsIgnoredOutsideManualMode`;
  `theGridHidesBelowOneTimesZoom`;
  `aNewEntityInASavedArrangementIsPlacedUnpinnedAndCounted` — assert the
  hollow anchor and the *"12 new nodes were placed automatically"*
  banner, which is what tells a `manual`-mode designer there is arranging
  to do; `labelHalosAreEmittedForEveryLabel`.

- [ ] See red: place an unplaceable node at the origin instead of
  shelving it and watch the first test find a mark at (0,0); route the
  fresh map through the generic empty state and watch its test find the
  wrong sentence; drop the halo and watch its test count zero.

- [ ] Hand check: two hundred pins over a real image. Are labels readable
  over both the darkest and lightest regions of the picture? This is the
  one legibility question the halo exists for and arithmetic cannot
  answer it, because the background is the designer's file.

```bash
git add internal/web/static/render/map.js internal/web/jstest/render_map_test.mjs \
        internal/web/static_appjs_browser_test.go
git commit -m "feat(web): the map renderer, its shelf, and a first run that says what to do"
```

---

### Task 12: `table`

**Files:**
- Create: `internal/web/static/render/table.js`
- Create: `internal/web/jstest/render_table_test.mjs`

The most-used renderer per the catalogue's own comment, so it gets the
most care and none of the drama. Rows on `--paper`, hairline rules, no
zebra striping, sticky header, `sort` applied on open and re-sortable by
clicking a header — client-side, because the server returned everything.
`group_by` renders a sticky sub-header with a count. Built-in columns get
defaults: `@name` serif, `@key` mono, `@type` muted.

- [ ] Tests: `anAbsentValueIsAnEmDashNotAnEmptyCell` — an empty cell is
  indistinguishable from a rendering bug; `theEmptyStringIsNotAnEmDash` —
  the control that makes the previous test mean something;
  `theHeaderRowSurvivesZeroRows`;
  `pagingSaysWhenTheResultIsAlsoTruncated` — assert
  `showing 1–50 of 1000, capped`, and assert the un-truncated fixture
  says `showing 1–50 of 1000` with no "capped", so nobody reads a page
  count as a content count; `sortingIsClientSideAndIssuesNoFetch`;
  `sortingIsStableAcrossEqualKeys`; `columnsKeepTheirDeclaredOrder`;
  `colorByIsNotOfferedHere` — assert the renderer ignores it if present,
  since the catalogue does not offer it and a slot another renderer would
  colour is a **column** here, which is the honest form of the same
  information; `groupSubheadersCarryTheirCount`.

- [ ] See red: render an absent value as `""` and watch the em-dash test
  fail while `theEmptyStringIsNotAnEmDash` stays green — the pair is the
  assertion; drop "capped" from the pager and watch its test fail on the
  truncated fixture only.

```bash
git add internal/web/static/render/table.js internal/web/jstest/render_table_test.mjs \
        internal/web/static_appjs_browser_test.go
git commit -m "feat(web): the table renderer, where an absent value is a dash and a page is not a count"
```

---

### Task 13: `timeline`

**Files:**
- Create: `internal/web/static/render/timeline.js`
- Create: `internal/web/jstest/render_timeline_test.mjs`

An axis along the horizontal from `axis_label`; marks at `axis_field`, or
bars from `axis_field` to `axis_end_field` when the view declares a span;
`lane_by` splits into lanes with a caption and a hairline. A number axis
gets numeric ticks; an **enum axis gets one tick per declared option, in
declaration order** — that order is the axis, which is why the catalogue
refuses two enums whose options differ.

- [ ] Tests: `anEnumAxisFollowsDeclarationOrderNotSortOrder` — declare
  options in a non-alphabetical order and assert the tick order matches
  the declaration, with a fixture whose alphabetical order differs, or
  the test proves nothing; `everyDeclaredOptionGetsATickEvenWithNoNodes`;
  `aNodeWithNoAxisValueGoesToAPinnedUnplacedLane` — assert the lane is
  before the axis begins, captioned *"no value for `level`"*, and
  counted, and assert **no mark sits at the axis origin**;
  `aSpanEndingBeforeItsStartIsDrawnAndNamed` — assert a zero-length mark
  with a caret and a frame line, and assert the two ends were **not**
  silently sorted, because that is a content defect a designer wants to
  know about; `threeOverlappingMarksStackAndTheFourthCollapses` — assert
  the count chip and that expanding it fetches nothing;
  `laneCaptionsCarryTheirValue`; `aNumberAxisPicksTicksDeterministically`
  — the same data twice, identical ticks.

- [ ] See red: sort the two ends of an inverted span and watch its test
  fail; sort the enum options alphabetically and watch the axis-order
  test fail; place a valueless node at the axis origin and watch the
  unplaced-lane test find the mark.

```bash
git add internal/web/static/render/timeline.js internal/web/jstest/render_timeline_test.mjs \
        internal/web/static_appjs_browser_test.go
git commit -m "feat(web): the timeline renderer, whose axis is a declaration order and not a sort"
```

---

### Task 14: The two writes — dragging, and placing a background

**Files:**
- Modify: `internal/web/static/components/mst-canvas.js`
- Create: `internal/web/static/components/mst-ground.js`
- Create: `internal/web/jstest/writes_test.mjs`

The only two writes this sub-project performs against a view.

**Dragging.** Snap to `snap` when set and the mode is `manual`;
shift-click and marquee select; a multi-selection drags as one body and
writes as **one** `set_positions` call. Arrow keys nudge by one grid unit
(1px with no grid), which is the keyboard path to the same write. The
write happens **on drop, not during**: a drag is one intention, and a
write per frame is sixty writes and sixty fanned-out events. A dragged
node is written `pinned: true`, because that is what a drag means.
Unpinning is a separate explicit act, and in `manual` mode it has no
visible effect until the position is cleared — so the same menu offers
the clear beside it, because a control that appears to do nothing is
worse than one that is absent. Undo is one level, per tab, from the
pre-drag coordinates, and its bound is stated in the menu, since
`view_positions` has no history and there is no server-side undo to reach
for. A refused write **reverts the nodes** and bands the frame with the
server's sentence. Concurrency is last-writer-wins and the menu says so:
`set_positions` carries no `expected_version` and does not advance the
view's version, which is the right trade for a coordinate and the one
place in this product where a write silently loses.

**Placing a background.** Upload is multipart to `POST …/view-assets`.
The picker states the refusal **before** a file is chosen — 8 MB,
PNG/JPEG/WebP, and *SVG is refused because an SVG served inline can carry
script* — repeating the server's own reason rather than paraphrasing it.
A refused upload shows the server's error verbatim. Placement is a mode:
while *adjust ground* is active the image drags and scales and the nodes
hold still; committing writes one `set_background {asset_id, scale,
offset}`; cancelling writes nothing; clearing is `asset_id: null` behind
a confirmation. In-flight elements carry `--pending`, a reduced-opacity
treatment and not a spinner.

- [ ] Tests: `aFortyNodeDragIsOneWrite` — assert exactly one request with
  forty entries; `nothingIsWrittenUntilTheDrop` — assert zero requests
  across sixty pointermove events;
  `aDraggedNodeIsWrittenPinned` — assert `pinned: true` on every entry;
  `arrowKeysWriteTheSameShape` — assert the request body of a nudge is
  the same shape as a drag's, since two write paths are two chances to
  differ; `snapIsAppliedOnlyInManualMode`;
  `draggingIsDisabledInAutoAndTheCanvasSaysWhy` — assert zero requests
  **and** assert the sentence is present, because a silently inert canvas
  is the "write a row nothing reads" defect wearing a mouse;
  `theSwitchToMixedIsAnUpsertWithExpectedVersion` — assert the call
  carries the version; `aViewerGetsTheSentenceWithoutTheButton`;
  `aRefusedWriteRevertsAndBands` — assert the coordinates return to their
  pre-drag values and the server's message is rendered verbatim;
  `undoRewritesThePreDragCoordinates` — and
  `undoIsOneLevelOnly`, asserting the second Ctrl-Z does nothing;
  `theUploadPickerStatesTheRefusalBeforeAFileIsChosen` — assert the three
  facts are in the DOM with no file selected;
  `aRefusedUploadShowsTheServerSentence`;
  `cancellingThePlacementWritesNothing`;
  `clearingTheGroundSendsNullAndIsConfirmed`;
  `adjustGroundMovesTheImageAndNotTheNodes` — assert node coordinates are
  unchanged during the mode; `pendingElementsCarryThePendingTreatment`.

- [ ] See red: write on `pointermove` and watch `nothingIsWrittenUntilTheDrop`
  report sixty; drop the revert from the failure path and watch
  `aRefusedWriteRevertsAndBands` find the optimistic coordinates —
  **this is the mutation that produces the one outcome a shared design
  tool may not produce**, a screen that disagrees with the database
  indefinitely; enable dragging in `auto` and watch its test find a
  request; send `pinned: false` on a drag and watch its test fail.

- [ ] Hand check: drag a marquee of forty nodes on a thousand-node view
  and watch for stutter. The frame budget is measured in Task 18; what a
  human is judging here is whether the *snap* and the pinned/unpinned
  anchor marks are discernible while moving.

```bash
git add internal/web/static/components internal/web/jstest/writes_test.mjs \
        internal/web/static_appjs_browser_test.go
git commit -m "feat(web): dragging and background placement, written on drop and reverted on refusal"
```

---

### Task 15: Navigation — the routes, the home, the catalogue and the entity

**Files:**
- Create: `internal/web/static/views.html`, `view.html`, `types.html`,
  `type.html`, `entity.html`, `assets.html`
- Create: `internal/web/static/pages/{home,views,view,types,catalogue,entity,assets}.js`
- Modify: `internal/web/static/game.html`, `internal/web/static/app.js`
  (the game page becomes the three-lane home)
- Modify: `internal/web/server.go` (the shell routes)
- Create: `internal/web/static_pages_test.go`
- Create: `internal/web/jstest/pages_test.mjs`

Three destinations — **Views · Catalogue · Prose** — because the
metamodel has four primitives and the product has three surfaces over
them, and a designer should hold the whole map of this tool in their head
on day one. Every route is addressed by slug and every one is a URL a
colleague can be sent:

| Route | Shell |
| :-- | :-- |
| `/g/{slug}` | `game.html` (the three-lane home) |
| `/g/{slug}/views` | `views.html` |
| `/g/{slug}/v/{key}` | `view.html`, `?p.<name>=` binding parameters |
| `/g/{slug}/types` | `types.html` |
| `/g/{slug}/t/{typeKey}` | `type.html` |
| `/g/{slug}/e/{typeKey}/{key}` | `entity.html` |
| `/g/{slug}/doc?path=` | `document.html` (exists) |
| `/g/{slug}/assets` | `assets.html` |

The home makes **one** content call, `GET /api/games/{slug}/summary`, for
its catalogue lane; the views lane reads `GET …/views` and the prose lane
`GET …/docs` plus `GET …/docs/kinds` for the vocabulary line `game.html`
already carries. Each lane keeps the existing role-dependent empty state
rule — a viewer is never told to do something the server will refuse. A
game with **no views at all** gets the one piece of onboarding in this
product: a sentence saying views are written by an agent over MCP, with a
link to the skill bundle's documentation, instead of a *New view* button
that leads nowhere (O1).

The entity page shows the name in serif, the type and key in mono, the
fields **in declared order and rendered by declared type**, with a field
the entity does not carry shown as declared-and-unset rather than hidden
— *what could be filled in here* is the question a designer is usually
asking. Relations come in two lists, out and in, grouped by relation
type, each row carrying the relation's **own fields**, because typed
edges are a first-class idea in this product. Documents linked to the
entity are listed by path with a preview line. From a diagram, a node
click opens this page in a **side panel over the canvas**, with the full
page one click further: losing an arrangement to read a field would make
reading fields feel expensive.

- [ ] Tests (Go, `static_pages_test.go`):
  `TestEveryShellIsReachableByItsRoute` — enumerate the shells on disk
  and assert each has a registered `GET` route, so a shell added without
  a route fails here rather than 404ing in a browser;
  `TestNoShellIsServedTwice` — the `/static/` file server refuses
  `.html`, and the new shells must stay in that refusal;
  `TestNoPageURLContainsAUUID` — scan the page modules for a uuid-shaped
  template literal or `game.id` in a path position, asserting none: the
  product is addressed by slug on every route and in every confirmation;
  `TestEveryShellCarriesTheImportMapAndTheStylesheet`.
- [ ] Tests (Node, `pages_test.mjs`): `theHomeMakesOneSummaryCall` —
  assert exactly one call for the counts, so a later edit that fetches
  per type fails; `theHomeLanesAreViewsCatalogueProseInThatOrder`;
  `aGameWithNoViewsGetsTheAgentSentenceAndNoCreateButton` — assert the
  link and assert no element with a create verb;
  `emptyStateActionsFollowTheRole` — viewer and editor, two texts, one
  fixture each; `theProseLaneNamesTheGamesDocumentKinds`;
  `aMovedDocumentIsFollowedNotCached` — send `document.moved` and assert
  the lane re-reads rather than keeping the old path;
  `theEntityShowsDeclaredButUnsetFields` — assert a declared field absent
  from the entity still renders, labelled unset;
  `relationRowsCarryTheRelationsOwnFields` — the exact shape that was
  write-only for a whole sub-project;
  `bothDirectionsOfARelationAreListedSeparately`;
  `TestTheEntityPanelRunsNoViews` — assert zero `views/run` calls from
  the entity panel, pinning O5's deliberate absence;
  `aNodeClickOpensThePanelAndDoesNotNavigate` — assert the canvas is
  still mounted; `theCatalogueSaysItIsACatalogueAndNotAView` — assert the
  line, since it is deliberately close in appearance to the `table`
  renderer; `theCataloguePagesOverTheExistingCursor`.

- [ ] See red: fetch counts per type on the home and watch
  `theHomeMakesOneSummaryCall` report N; hide declared-but-unset fields
  and watch its test find the missing row; add a *New view* button and
  watch the onboarding test fail.

```bash
git add internal/web/static internal/web/server.go \
        internal/web/static_pages_test.go internal/web/jstest/pages_test.mjs \
        internal/web/static_appjs_browser_test.go
git commit -m "feat(web): three destinations, addressed by slug, with an entity page that reads a relation's own fields"
```

---

### Task 16: "Save as" — the only view a human can make

**Files:**
- Create: `internal/web/static/components/mst-save-as.js`
- Modify: `internal/web/static/pages/view.js`
- Create: `internal/web/jstest/save_as_test.mjs`

O1's narrow exception. The dialog copies the open view's query document
**verbatim** and lets the designer change three things and no others: the
new key, the renderer (from the catalogue the server already describes),
its parameters, and the declared parameters' defaults. It is one
`views.upsert`. No stage of the query is editable, so there is no builder
here and no traversal a designer has to understand.

What it must be honest about: a duplicate that is also wrong is a
duplicate, not a repair, so the dialog says the query is copied unchanged
and points at the agent workflow for changing it.

- [ ] Tests: `theQueryDocumentIsCopiedByteForByte` — assert the request's
  query is `===` the source's serialised document, which is what makes
  "no builder" true rather than aspirational;
  `onlyTheThreeEditableFieldsDiffer`; `aTakenKeyIsRefusedInTheServersWords`
  — assert the `version_conflict`/`invalid_input` sentence is rendered
  verbatim; `theKeyFieldRefusesAnIllegalKeyBeforeSending` — assert no
  request was made, and assert the message matches the row-key rule the
  server states; `theRendererChoiceComesFromTheServersCatalogue` — assert
  the options were read, not hard-coded, since a hard-coded list is drift
  with a date on it; `theDialogSaysTheQueryIsUnchanged`.

- [ ] See red: hard-code the renderer list and watch its test fail;
  mutate one character of the copied document and watch
  `theQueryDocumentIsCopiedByteForByte` fail.

```bash
git add internal/web/static internal/web/jstest/save_as_test.mjs \
        internal/web/static_appjs_browser_test.go
git commit -m "feat(web): save a view under a new key with a copied query, which is the only view a human can make"
```

---

### Task 17: Remove `layout_seed`

**Files:**
- Create: `internal/db/migrations/0012_drop_layout_seed.sql`
- Modify: `internal/db/queries/views.sql`, regenerate `internal/db/dbq/`
- Modify: `internal/views/views.go`, `positions.go`, `assets.go`
- Modify: `internal/web/mcp_views.go`, `internal/web/api_views.go`
- Modify: the tests that name it (`internal/views/views_test.go`,
  `positions_test.go`, `internal/web/mcp_views_test.go`)

O3, decided: the column has no reader on either side of the wire, and
this repository's rule is that a mechanism nothing reads is a lie. **This
is a wire and a schema change and that is why it is its own task.** It
touches 56 references across eleven files, both surfaces, the generated
tool description and `dbq`.

The migration comment carries what a future seed would have to seed, so
that re-adding it — additive, and cheap — does not start from
archaeology:

```sql
-- 0012: drop views.layout_seed.
--
-- It was added for a force-directed layout that needed seeding to keep a
-- saved view recognisable. The interface sub-project chose a
-- deterministic engine (dagre) over a stochastic one, precisely so that
-- two designers opening one view get the same picture without a seed, so
-- nothing has ever read this column: not the server (0008_views.sql said
-- so from the first day) and not the client.
--
-- If a stochastic engine is ever adopted, what it would need is a seed
-- per (view, engine) — this column was per view — and a stated rule for
-- what happens to a stored arrangement when the seed changes. Re-adding
-- a column is additive; keeping one nothing reads is a knob that lies.
ALTER TABLE views DROP COLUMN layout_seed;
```

- [ ] Tests: `TestMigrationsApply` (existing) must stay green;
  `TestNoSurfaceAcceptsALayoutSeed` — drive `views.upsert` on **both**
  MCP and REST with a `layout_seed` member and assert it is refused as an
  unknown field rather than silently ignored, because a silently ignored
  argument is the same lie one layer up; `TestTheViewToolDescriptionNamesNoSeed`
  — assert the generated description, which is where an agent learns the
  vocabulary; `TestUpsertRoundTripsWithoutASeed` as the positive control.
- [ ] Grep for the identifier across the repository (`layout_seed`,
  `LayoutSeed`) after the change and assert the only survivors are the
  migration and the two design documents, which are history.

- [ ] See red: leave the field on `ViewInput` and watch
  `TestNoSurfaceAcceptsALayoutSeed` accept it on one surface — **run this
  mutation on one surface only**, since the recurring defect of this
  project is a rule carried on one path and not the other.

```bash
git add internal/db/migrations/0012_drop_layout_seed.sql internal/db/queries/views.sql \
        internal/db/dbq internal/views internal/web
git commit -m "refactor(views): drop layout_seed, a knob no reader ever had"
```

---

### Task 18: End to end — the spec's definition of done, in a browser

**Files:**
- Create: `internal/web/interface_e2e_test.go`

No new document: the measurements this task takes go in the test's own
comments and in the commit message, where they are read next to the code
they judge.

This drives the spec's §11 as one sequence, over the real transports,
against a real server and a real browser. Everything before this task
proved a module; this task proves the product.

- [ ] Step 1: seed a game over MCP exactly as an agent would — types,
  entities, relations, one document, one saved view (*Mage quests 20–30*,
  `graph`, `color_by: "zone"`), one uploaded background.
- [ ] Step 2: open `/g/{slug}` in the browser. Assert the three lanes,
  and assert with `evaluate_script` that `getComputedStyle` resolves
  `--paper` and `--ink` to the token values — the one thing no Node
  harness can see, because it needs a cascade.
- [ ] Step 3: open `/g/{slug}/v/mage-quests` with `?p.class=mage`. Assert
  the parameter bar shows the binding, the legend has readable rows, and
  the footer carries both durations. Record `stats.duration_ms` and the
  layout elapsed for the commit message — this is O4's and O10's
  measurement.
- [ ] Step 4: assert the budget with `performance.now()` around the run:
  first contentful paint of the game page under 1s, a 500-node `graph`
  under 1s total including layout, layout hard-stopped at 2s. **Where a
  budget fails, record the number and open it as a finding rather than
  loosening the constant quietly.**
- [ ] Step 5: drag four quests. Assert one `POST …/positions`, reload,
  assert the four coordinates survive.
- [ ] Step 6: open a second page on the same view. Assert the first
  page's drag reaches it as **one** coalesced re-read, and that a
  colleague's drag does not arrive while a local drag is in flight.
- [ ] Step 7: add twelve quests over MCP. Assert they arrive auto-placed,
  unpinned (hollow anchors), and counted in the band that says so, and
  that the four pinned nodes did not move — asserted by coordinate
  equality, not by eye.
- [ ] Step 8: `types.rename` a relation type over MCP. Assert the view
  **does not draw**, that the diagnostics panel names the step in the
  server's own words with the pointer, that one click on *Run anyway*
  produces the reduced picture under the amber band, and that the dropped
  legend rows are `--dropped`. Assert that nowhere in the sequence was a
  picture drawn that was quietly wrong — concretely: assert the
  best-effort picture's banner names the dropped step, and assert the
  pre-repair render produced zero node marks.
- [ ] Step 9: drive the keyboard path alone — tab to the twin, move
  through rows, nudge a node with the arrow keys, assert the write, and
  assert `document.activeElement` never lands inside the `aria-hidden`
  canvas.
- [ ] Step 10: shrink the viewport below tablet width and assert the view
  page falls back to the twin with the writing interactions disabled
  rather than shrunk.
- [ ] **Hand check, and it is required.** A human opens the same game and
  answers four questions no assertion above can: is the eight-hue legend
  discriminable at a glance; does the chrome stay achromatic (is there
  any saturated pixel that does not belong to the game's data); is the
  thousand-node graph legible or a hairball; and does the stale panel
  read like an explanation rather than a stack trace. Record the answers
  in the commit message.

```bash
git add internal/web/interface_e2e_test.go
git commit -m "test(web): the interface end to end, from a seeded game to a stale view repaired by hand"
```

---

## Self-review notes

Checked against `2026-09-06-interface-design.md`, section by section:

- **§2 the identity** — tokens, both themes and the achromatic rule are
  Task 1, with contrast and colour-blindness as arithmetic rather than
  taste; the serif/sans/mono split is Task 1 and is what closes O9; the
  two densities and the full-bleed canvas are Tasks 1 and 7.
- **§3 how the front end is built** — Lit, the import map, shadow DOM
  off, SVG rendering and one data client are Tasks 2, 3 and 7; "errors
  are rendered from the server's own sentences" is asserted in Tasks 3,
  4, 14 and 16, in four places, because it is the rule this front end
  will break first.
- **§4 the six renderers** — the frame and every negative state are Task
  4; the six drawings are Tasks 8–13, each spending more of its checklist
  on absence, ambiguity, truncation, staleness and emptiness than on the
  drawing.
- **§5 layout** — the engine, the worker, the budget, the fallback, the
  composition rule and incrementality are Task 6; `layout_seed` is
  removed in Task 17.
- **§6 the writes** — Task 14, including the last-writer-wins statement
  and the revert on refusal; the second browser's rules are Task 3.
- **§7 navigation** — Task 15, addressed by slug throughout.
- **§8 accessibility and budgets** — the twin is Task 5, deliberately
  before every renderer; the budgets are measured in Task 18 and a failed
  budget is a finding, not a loosened constant.
- **§9 what this does not build** — held: no analysis (Task 9's cycle
  line is asserted **not** to become one), no content editing, no export,
  no presence, no i18n, no admin UI. The one deliberate departure is Task
  16, which is argued under O1.
- **§10 open questions** — all ten decided above, with O3 and O1 given
  their own tasks.
- **§11 definition of done** — Task 18, step by step, plus the hand
  check.

Three things this plan changes outside the interface, named so a reviewer
looks at them first:

1. **`internal/web/static/styles.css` renames `--accent` to `--focus`**
   (Task 1), which touches `app.js`, `doc.js` and four shells in the same
   commit.
2. **`internal/web/server.go` gains six shell routes** (Task 15), each
   through the same `routeFunc` shape `GET /g/{slug}` already uses, with
   a test that enumerates the shells rather than listing the routes.
3. **`views.layout_seed` is dropped** (Task 17): a migration, both wire
   surfaces, `dbq`, and the generated tool description.
