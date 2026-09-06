# Maestro — the interface (design)

Date: 2026-09-06
Status: proposed
Scope: sub-project 5 of the roadmap in
`docs/superpowers/specs/2026-08-31-core-and-metamodel-design.md`
Depends on: sub-projects 1 (core), 2 (metamodel), 3 (markdown) and 4
(views), all four shipped. This spec writes against committed code:
`internal/views/renderers.go` (the renderer catalogue),
`internal/views/execute.go` (the result envelope),
`internal/views/stale.go` (the diagnostics and `on_stale`),
`internal/views/events.go` (the four `view.*` events),
`internal/db/migrations/0008_views.sql` (the columns), and the routes in
`internal/web/server.go`.

The views spec deliberately ended with "Not designed here: colour
palettes, node shapes, typography, density, interaction affordances,
empty states. Sub-project 5." This is that design, and nothing here
changes the contract that spec enforces at save time. **What a renderer
consumes was decided there. What it looks like is decided here.**

## 1. What this spec decides, and why it exists

Today a view's answer is JSON. A designer can ask for *the quests a Mage
can reach between level 20 and 30, coloured by zone*, an agent can save
that view, the server can run it, and then a human has to read an
envelope. The product's entire premise — that a designer and their agent
work on the same game, and the designer *looks* at the result — is
unbuilt above the transport.

This spec decides four things and refuses a fifth:

1. **What Maestro looks like** (§2), as a committed direction with
   tokens, not a menu.
2. **What each of the six renderers draws** (§4), including — and mostly
   — what it draws when the envelope is not clean. The views
   sub-project's staleness design assumes a stale view is the *normal*
   case; a renderer specified only for the happy path is a renderer
   specified for the rare case.
3. **Where layout runs and on what** (§5), closing open question O1 of
   the views spec, and exactly how a computed arrangement composes with
   the one a designer dragged.
4. **What the writing interactions do, and what a second browser does**
   (§6), given that the four `view.*` events carry an identity and
   nothing else, on purpose.

And §7 decides navigation: how a designer gets from "I opened a game" to
"I am looking at the talent tree", in a product that currently has one
page.

Refused here, and stated up front because it shapes everything else:
**there is no query builder.** §9 argues it.

Two rules from the views plan's "What this sub-project learned" are
carried into this one verbatim, because they are the two failures a
front end makes most easily:

- **A mechanism nothing reads is a lie.** Every token, every column and
  every flag named below has a stated reader. Where something has no
  reader — `layout_seed` is the live example (§5.6) — this spec says so
  instead of implying one.
- **The negative half is the load-bearing half.** §4 spends more words
  on ambiguity, absence, truncation, staleness and emptiness than on the
  drawing, because that ratio is the ratio in which a designer will meet
  them.

## 2. The identity

### 2.1 Who is on the other side of the glass

A designer writing the lore and progression of a game, in a document
they will live inside for months, usually with an agent doing the typing
beside them. Not a programmer triaging work. The screen they came from
is a wiki, a spreadsheet and a whiteboard; the screen they are going to
is one where four hundred missions have to become legible.

Three consequences, and they are the whole design:

- **The content is the interesting thing on the page.** A quest is
  called *Wanted: Hogger*; a zone is called *Duskwood*. Those words were
  written by someone who cared about them, and the interface's job is to
  not compete with them.
- **A picture is the point, not an accent.** A view is the product's
  central object, and it is a diagram. The chrome is what fits around a
  diagram, not the other way round.
- **They will be here a long time.** Nothing may be loud, because
  everything loud is loud on the four-hundredth day too.

### 2.2 The committed direction: ink on paper, colour reserved for data

**Maestro is an editorial surface — near-black ink on a warm off-white
ground, generous measure for prose, hairline rules instead of boxes and
shadows — on which the *only saturated colour is the designer's own
data*.**

That is one decision with a hard rule inside it, and the rule is what
makes it a design rather than a mood:

> **The chrome is achromatic. Every hue on screen belongs to the game.**

No coloured buttons, no coloured navigation, no coloured section
headers, no status pills in six tints. When `color_by: "zone"` paints
eleven zones in eleven hues, those hues are the only hues present, so
they read instantly and mean exactly one thing. An interface that has
already spent blue on its primary button and green on its success toast
has to shout to make a *zone* legible.

Two exceptions, and only two:

- **`--danger`**, already in `styles.css`, for a destructive
  confirmation and for a refusal. It is the one colour that means "this
  needs your attention", exactly as that file's comment already says.
- **`--focus`**, a single accent, used for the focus ring, the current
  selection and the current item in navigation, and for nothing else.
  One accent used in three places is a system; four accents are a
  palette nobody can learn.

**Reasons, not taste.** Colour in this product is a *channel the query
language binds* — `color_by` is a renderer parameter and its argument is
a projection slot. A UI that also uses colour semantically is competing
for a channel the user has been given the right to assign. Reserving it
is the only way `color_by` is worth having.

### 2.3 Tokens

Extending, not replacing, the vocabulary `internal/web/static/styles.css`
already established (`--ink`, `--paper`, `--line`, `--muted`,
`--danger`): the existing tokens are already this identity, chosen
before it had a name.

```css
:root {
  color-scheme: light dark;

  --paper: #faf8f5;   /* reading surfaces */
  --ground: #f2efe9;  /* the diagram canvas: one step back from paper */
  --ink: #14140f;     /* body text, node labels */
  --muted: #6b6459;   /* secondary text, axis labels, counts */
  --line: #ded7cb;    /* hairlines, table rules, node outlines */
  --focus: #2f4a8a;   /* the one accent: focus, selection, current */
  --danger: #a4262c;  /* refusals and destructive confirmation */

  --unset:  transparent;      /* a slot that found nothing: no fill */
  --dropped: #b0a89a;         /* what best_effort removed */
  --pending: #8f8677;         /* a write in flight (see §6.2) */

  --data-1 … --data-8;        /* the categorical palette, §2.5 */
}
```

`--ground` being *darker* than `--paper` rather than lighter is
deliberate: the diagram is a thing lying on the desk, and the reading
panels around it are the paper. It also keeps a white node fill legible
against it without a border, which matters at a thousand nodes.

Dark mode is not an inversion. `--ink`/`--paper` swap to a warm
near-black ground (`#16150f`) and a bone foreground, and **the data
palette is a second, separately tuned set of eight** — the same hues at
the lightness a dark ground needs, in the same order, so a legend read
in one theme reads the same in the other. Eight hues inverted
mechanically produce two that vanish and two that glow; that is the
whole reason the second set exists rather than a filter.

### 2.4 Typography

Three stacks, no web fonts, no build step, nothing downloaded:

- **The game's words** — entity names, document prose, view titles — in
  a serif (`ui-serif, Georgia, 'Times New Roman', serif`). This is the
  one place the identity is expressive, and it is expressive about the
  right thing: the content.
- **The tool's words** — labels, buttons, table headers, counts,
  banners, legends, axis ticks, node labels inside a diagram — in
  `system-ui`. Node labels are sans on purpose even though they are the
  game's words: at 11px over a busy canvas, a serif is a smudge.
- **Machine text** — entity keys, JSON pointers in diagnostics, renderer
  and parameter names, version numbers — in `ui-monospace`. A pointer
  like `/traverse/0/via` is a thing to be copied, and monospace is what
  says so.

**Rejected: a vendored variable display face.** It would give a sharper
identity and it costs a binary in a public repository, a font-loading
state to design for, and a download on a product whose stated goal is a
small image and a build-free page. Recorded as open question O9 rather
than silently dropped, because "our own identity" and "system fonts" are
in genuine tension and the resolution may be worth revisiting once
someone has looked at it for a week.

### 2.5 The data palette

Eight categorical hues, plus two non-hue treatments that are part of the
palette and not afterthoughts:

- **`--data-1 … --data-8`** — eight hues at matched chroma and
  lightness, distinguishable under deuteranopia and protanopia (the
  common pair), and ordered so that adjacent indices are maximally
  separated rather than adjacent on the wheel.
- **`--unset`** — a node whose `color_by` slot is **absent from
  `attrs`**: no fill, a 1px dashed `--line` outline. The envelope
  guarantees this is distinguishable — "a slot that found nothing is
  absent, not empty" (`execute.go`) — so *not set* and *the empty
  string* get two legend rows, never one. Collapsing them would throw
  away a guarantee the server went out of its way to provide.
- **A hatched swatch for the tail.** More than eight distinct values is
  the common case for `color_by: "zone"` in a real game. The eight most
  frequent values take the eight hues; everything else is drawn with a
  fine diagonal hatch over `--line` and appears in the legend as
  `other (37 values)`, expandable to the list. Nine hues would be
  unreadable and a nine-hue palette that grows to thirty is a palette
  with no meaning at all.

**Hue is assigned by hashing the value's text, not by its rank in the
result.** Rank-based assignment recolours the whole map the day someone
adds a zone, which destroys the one property a saved view exists for —
being recognisable — exactly as an unseeded force layout would (views
spec §5.2 makes this argument for geometry; it is the same argument for
colour). The cost is collisions: two values can hash to one hue. The
legend disambiguates, node labels carry the text, and the collision is
stable rather than surprising. The hash is over the value's JSON text,
so a projected number and the string of it are different values, matching
the envelope's own type preservation.

**Colour is never the only carrier.** Every diagram has a legend; every
diagram has a text twin (§8.1); a value's text is available on hover and
on focus. A design where the answer is only in the hue is a design that
excludes about one reader in twenty and every printed page.

### 2.6 Density, and how a diagram sits on the page

Two densities, and the switch between them is which kind of surface it
is:

- **Reading surfaces** — an entity, a document, a catalogue row — keep
  the `max-width: 68ch` measure `styles.css` already sets. Prose is
  read, not scanned.
- **The diagram canvas is full-bleed.** It fills the viewport below the
  game header, and the chrome that belongs to it — the view's title, its
  parameter bar, its legend, its banners, its stats — floats over the
  canvas edges on a translucent `--paper` panel with a hairline, and can
  be collapsed to a single strip. A diagram inside a centred 68ch column
  is a diagram nobody can use.

The frame is otherwise flat: hairlines, no shadows except the 1px
translucent lift under a floating panel, 4px radii, and one elevation
level. Every card, every drop shadow and every rounded box is a fence
around content that did not need fencing.

### 2.7 What this is not

Said explicitly, because each of these is a place a reasonable
implementer would land by default:

- **Not Nottario.** No GitHub-like chrome: no dense grey-blue header bar
  with avatars and counters, no octicon-flavoured iconography, no
  labels-as-coloured-pills, no activity feed. The sibling project tracks
  work and looks like the place developers track work; this one is a
  design document.
- **Not a dashboard.** No KPI tiles, no card grid, no sparklines on the
  home page. "How much content is in each zone" is a question the
  *analysis* sub-project will answer inside a view, and it is not the
  first thing a designer should meet.
- **Not game art.** No parchment, no gothic display face, no leather
  texture, no fantasy iconography. The tool is not the game; a racing
  career and an MMORPG are equally at home here, and any skin that
  flatters one insults the other. This is the strongest constraint the
  genericity of the metamodel places on the visual design, and it is the
  easiest to violate charmingly.
- **Not a dark neon graph tool.** Light is the default. Dark is
  supported and is warm, not blue-black.
- **Not motion-forward.** Transitions ≤150ms, no settling animation
  (§5.2 kills the one thing that would have needed it), no parallax,
  `prefers-reduced-motion` honoured by removing transitions, not by
  shortening them.

## 3. How the front end is built

Committing the invariants from `.claude/CLAUDE.md` to specifics, since
"vanilla CSS + Lit, no build step" has several possible shapes.

- **Lit, vendored, ESM, loaded through an import map.**
  `internal/web/static/vendor/lit-core.min.js` (the pre-bundled ESM
  build, ~17KB gzipped, no dependencies) plus a
  `<script type="importmap">` in each shell HTML mapping `lit` to it.
  The import map is the piece that makes "no build step" survive
  `import { LitElement, html } from 'lit'` in ordinary component
  sources, which is what keeps those sources ordinary. Vendored rather
  than CDN-loaded: a self-hosted instance on a private network must work
  with no outbound internet at all, and that is not negotiable.
- **Components are custom elements with real tag names**
  (`<mst-view-frame>`, `<mst-graph>`, `<mst-legend>`,
  `<mst-catalogue>`), one file each under `static/components/`.
- **Shadow DOM off** (`createRenderRoot() { return this; }`) for every
  component. The design is one flat token system in one stylesheet;
  per-component style encapsulation buys isolation this product does not
  need and costs the ability to style an SVG subtree from one place.
- **Rendering is ours, in SVG**, per the invariant. Not canvas, not
  WebGL: an SVG node is a DOM node, which is a focus target, a hit test,
  a hover target and an accessibility object for free, and the node
  counts here (§8.2) are within SVG's comfortable range. Canvas would
  buy headroom this product's caps say it does not need and would cost
  every one of those four for-frees.
- **One data client.** A single module owns `POST …/views/run`, the
  error shapes (`query_invalid`, `renderer_requirements`,
  `query_stale`, `query_timeout`, `limit_exceeded`,
  `version_conflict`), and the SSE subscription. Every component reads
  from it and none of them fetches.
- **Errors are rendered from the server's own sentences.** The views
  sub-project generated its agent-facing prose from its structures and
  asserted it in both directions; restating those sentences in
  JavaScript is exactly the drift that work refused. The client renders
  `code`, `pointer` and `message` as given, and adds only navigation
  (which view, which step, what to do next).

## 4. The six renderers, drawn

### 4.1 The frame every renderer sits in

One component wraps all six, so that the negative states below are
designed once and cannot drift between renderers. Top to bottom:

1. **Title strip** — the view's name (serif), its key (mono, muted), its
   renderer, and a stale badge when `views.list` said so.
2. **Parameter bar** — one control per parameter the query declares
   (`params`, views spec §2.2). Enum-typed parameters get a select,
   others a text or number field. The bar is present only when the view
   declares parameters, and the current binding is **in the URL**
   (`?p.class=mage`), so a parameterised view is linkable and a
   colleague opens the same picture rather than the default one.
3. **Banner stack** — zero or more bands, in fixed order: stale, dropped
   (best-effort), truncation, auto-placement, ambiguity. Fixed order
   because a designer learns positions, and because two of these
   routinely co-occur.
4. **The drawing.**
5. **Footer strip** — `stats.nodes`, `stats.edges`,
   `stats.max_depth_reached`, `stats.duration_ms`, and the layout
   engine's own time when it ran (§5.4). Muted, small, always present:
   the first question about a slow view is whether it was the query or
   the answer's size, and the envelope was built to answer it.

**Banners are bands, never toasts.** Every state below is a property of
the picture in front of the designer, and a message that disappears
after four seconds is a property of the last four seconds.

### 4.2 The negative states, once, for all six

Each of these is a real member of the envelope. This section is the
contract; §4.3–§4.8 only say where each one lands in a particular
drawing.

**A slot that found nothing** (`attrs` has no entry for the slot).
Drawn with `--unset`: no fill, dashed outline, and a legend row
`— not set (n)`. Never the palette's first colour, never grey-as-a-hue,
and never merged with the empty string.

**An ambiguous node** (`node.ambiguous`). A small open mark at the
node's top-right corner, `--muted`, and a banner: *"14 nodes resolved a
related attribute ambiguously — more than one entity matched and the
first by name was used."* The mark is on the node and not on the slot,
because the envelope's flag is on the node and not on the slot, and a
UI that implied otherwise would be inventing a distinction the server
explicitly declined to carry. The banner links to the query's projection
so the designer can go and disambiguate it. **Ambiguity is never
silent**, which is the entire reason that boolean exists.

**A node with no position.** Two different situations, two different
answers:

- *`map` with `coordinate_source: "fields"` and a node whose x or y
  field is absent.* It cannot be placed at all. It goes to the **shelf**:
  a strip along the bottom edge of the canvas holding unplaceable nodes
  as labelled chips, with a count and a banner. Never at the origin —
  `(0,0)` is a place a designer may deliberately have put something, and
  `execute.go` refuses to return unplaced as a coordinate for that exact
  reason. The shelf is the visual form of the same refusal.
- *Any other case* — a new entity in a saved arrangement. It is placed
  by the layout engine (§5.3), drawn unpinned (hollow anchor dot rather
  than solid), and counted: *"12 new nodes were placed automatically."*
  The banner is what tells a designer in `manual` mode that there is
  arranging to do, as the views spec §5.3 requires.

**A truncated result** (`truncated.nodes`, `.edges`, `.depth`). Three
independent flags, three separate sentences, never one summary:

- nodes — *"This picture is capped at 1000 nodes and the answer had
  more."*
- edges — *"…and more edges than the cap."*
- depth — *"A traversal was cut short at its depth bound; there is more
  graph beyond it."*

And the sentence the flags do **not** let us say: when all three are
false, the frame says **nothing**, and in particular it never says
"complete". `execute.go` is explicit that a query whose every step is
one hop cannot set `truncated.depth` at all, so a false depth means
"not measured" as often as it means "not truncated". A UI that printed
"complete picture" off three false booleans would be asserting something
the envelope refuses to assert. The canvas edge additionally gets a
hatched margin when `truncated.nodes` is set, so the cap is visible in
the picture and not only in the prose.

**An edge whose endpoint is not in `nodes`.** Normal, and the envelope
says so twice: the caps are independent, and an `edges: [{between: …}]`
entry legitimately draws relations between sets the document chose not
to draw. It is drawn as a **stub**: the edge leaves its known endpoint
and terminates in a small hollow ring, no label. The footer counts them
(*"8 edges lead outside this picture"*). It is not an error and never
tinted `--danger`. What the interface cannot do is say *which* entity is
on the other end: an edge carries `source`/`target` as ids, and there is
no read-an-entity-by-id call anywhere in the product (O2).

**A stale view.** `on_stale` defaults to `fail`, and the interface keeps
that default: a run that fails staleness draws **no picture at all**,
and the canvas is replaced by the diagnostic panel — one row per
`Diagnostic`, showing the server's own sentence, the pointer in mono,
and `was` → `now`. Under it, one button: **Run anyway (best effort)**,
which re-runs with `on_stale: "best_effort"`. The reasoning is the views
spec's and is not re-litigated here: a diagram that silently dropped its
level filter looks exactly like a correct diagram. What this spec adds
is that the recovery is **one click and explicitly the designer's**, and
that a best-effort picture is drawn under a permanent amber band naming
what was dropped, with the dropped parts' legend rows shown in
`--dropped` so that the reduced picture never impersonates the whole one.

`*_renamed` diagnostics are the special case, and they arrive beside a
*full and correct* picture — the id resolved. They are therefore not a
banner in the stack above but a quiet line in the title strip:
*"this view names 2 things the game now spells differently"*, expanding
to the pointers. **No repair action is offered here**; §9 says why.

**An unbound parameter.** `param_unbound` is a staleness diagnostic, so
it arrives through the same path, and the interface answers it in the
place that can fix it: the parameter bar highlights the missing control
and the panel says which one. A designer who opened a link without
`?p.class=` is one field away from a picture, and sending them to a JSON
pointer for that would be absurd.

**An empty answer that is legitimately empty.** `nodes: []` with no
diagnostics and no truncation is a *successful* run and must look like
one: the same frame, the same footer reading `0 nodes, 0 edges`, and one
sentence in the canvas — *"This view matched nothing."* — plus what ran:
the renderer, the sets the query names, and the parameter values in
force. **It never borrows error styling, `--danger`, or a warning icon.**

What the empty state must *not* claim is why. The envelope does not
carry how many entities were considered, so the interface cannot say "0
of 340 quests matched" without a second call it does not make. It offers
the two things it does have: edit the parameters (in the bar), or open
the query (read-only, §7.4). Guessing at a cause — "try widening your
filter" — is advice generated from no information.

### 4.3 `graph`

**What it looks like.** Nodes as rounded rectangles sized to their label
— not circles; a name is the identifying thing and a circle either
clips it or floats it. Fill from `color_by`, 1px `--line` outline,
`--ink` label at 11–13px. Edges as 1px `--muted` curves, `arrows`
drawing a small filled head at the target, `edge_labels` drawing
`edge.label` on a `--ground` plate at the curve's midpoint. `size_by`
maps a numeric slot to node *area* across a bounded 1×–3× range —
bounded because an unbounded map makes one boss the size of the canvas.
`group_by` draws a hairline enclosure with a heading per value;
`cluster_by` does not draw anything, it feeds the layout (§5.3), and the
difference between the two is the sentence the tooltip on each control
gives.

**Negative half.** `edge_labels` cannot be on without a labelled edge —
the catalogue refuses that combination at save time, so the client never
meets it. A node whose `size_by` slot is absent gets the range's
minimum, and is *also* dashed if `color_by` is likewise absent; two
absences are two marks, not one. Stubs (§4.2) leave the enclosure of
their `group_by` box, which is correct and is why enclosures are
hairlines rather than filled panels.

### 4.4 `layered`

**What it looks like.** The same node vocabulary, arranged in ranks
along `rank_direction`, edges preferentially straight. `layer_labels`
draws a muted rule and a caption per rank — the rank index for
`rank_by: "edges"`, the field's value for a numeric `rank_by`, which is
the more useful caption and the reason that parameter exists.
`align` positions a node within its rank band.

**Negative half.** The interesting case is a **cycle** in a renderer
whose consumption note says "expected mostly acyclic". The engine breaks
cycles by reversing edges; the interface must not hide that. A reversed
edge is drawn with its arrowhead in its true direction and a small
double-slash mark on the curve, and the frame carries a line:
*"3 edges run against the ranking; this graph has a cycle."* That is
also, not incidentally, the single most useful thing this renderer can
tell a designer about a progression — the analysis sub-project will name
the cycle properly, and until then the picture at least does not lie
about it. A node whose numeric `rank_by` field is absent is placed in a
trailing **unranked** band, captioned as such, rather than at rank zero
where it would read as a starting point.

### 4.5 `nested`

**What it looks like.** Boxes inside boxes: a container is a `--paper`
rectangle with a hairline and its name on the top-left; children are laid
out inside it; the recursion stops at `max_depth`. Leaves use
`leaf_label`'s slot when given. Colour from `color_by` tints the box's
header strip rather than filling it, because a filled nest of four
levels is four overlapping fills and no legible text.

**Negative half.** Three cases, all real:

- **Beyond `max_depth`.** A container that has children it is not
  drawing shows a count chip (`+12`) and expands on click, client-side,
  without a re-run — the nodes are already in the envelope. `max_depth`
  is a drawing depth, and treating it as a fetch boundary would make a
  parameter mean two things.
- **A node with no container.** Every node the query drew that is not a
  `contain_via` target of another is a root, which is normal; but a node
  reached only as the *source* of a containment edge whose target was
  truncated away is an orphan of the cap, not of the game. Both are
  drawn at the top level; the second is dashed and counted in the
  truncation banner, because "this thing is top-level" and "this thing's
  parent did not fit" must not look alike.
- **A containment cycle.** A contains B contains A is data the metamodel
  permits. Drawing it recurses forever, so the nesting stops at the
  repeat, marks the repeated box with a cycle glyph and names both ends
  in the frame. Silently truncating the recursion would draw a plausible
  tree over a graph that is not one.

### 4.6 `map`

**What it looks like.** The one renderer with a ground. The background
asset is drawn at `background_scale` from `background_offset`, at 100%
opacity, and everything else is drawn over it: nodes as small solid
marks (a 7px disc, not a labelled box — a map with two hundred boxes is
not a map), labels offset to the upper right at 11px with a 2px
`--ground` halo so they survive over any image, edges as thin lines when
the query drew any. Zoom and pan move image and nodes together, always;
they are one coordinate space and any interface where they can drift
apart is a broken map. Labels do **not** scale with zoom past a 0.75×–1.5×
band: at 4× zoom a designer wants more detail, not bigger text.

`snap` (manual mode only, per the catalogue) draws a grid at that
spacing at ≥1× zoom, muted, under the background — visible enough to aim
at and never competing with the image.

**Negative half.** Unplaceable nodes go to the shelf (§4.2). A view in
`coordinate_source: "manual"` where *no* node has a position — a fresh
map — is not empty and must not use the empty state: it draws the
background, puts every node on the shelf, and says *"Nothing has been
placed yet. Drag a node from the shelf onto the map."* That is the
product's actual first-run experience for its most impressive feature,
and leaving it to the generic empty state would waste it.

A **missing or deleted background asset** — `background_asset_id` is
`ON DELETE SET NULL`, so it can simply become absent between two runs —
draws the plain `--ground` and one line in the frame: *"this view's
background image was removed."* Nodes keep their coordinates: positions
are per view and were never anchored to the image.

### 4.7 `table`

**What it looks like.** The most-used renderer, per the catalogue's own
comment, so it gets the most care and none of the drama: rows on
`--paper`, hairline rules, no zebra striping (stripes are a fix for
rules that are too weak), sticky header, `sort` applied on open and
re-sortable by clicking a header — client-side, since the server
returned everything. `group_by` renders a sticky sub-header per value
with a count. `columns` are drawn in their declared order; the built-ins
(`@name`, `@key`, `@type`) get sensible defaults — name in serif, key in
mono, type as muted text. `color_by` has no meaning here and the
catalogue does not offer it; where a table's rows carry a slot that
*another* renderer would colour, it is a column, which is the honest
form of the same information.

**Negative half.** An absent value renders as an em dash in `--muted`,
never as an empty cell — an empty cell is indistinguishable from a
rendering bug. `page_size` pages the rows **the client already holds**,
and the pager says so when the result is also truncated: *"showing
1–50 of 1000, capped"*, so nobody reads a page count as a content count.
Zero rows is the empty state of §4.2 in table clothing: the header row
stays, and the sentence sits under it.

### 4.8 `timeline`

**What it looks like.** An axis along the horizontal, labelled from
`axis_label`; nodes as marks at their `axis_field` value, or as bars
from `axis_field` to `axis_end_field` when the view declares a span;
`lane_by` splits into horizontal lanes with a caption each and a
hairline between. A number axis gets numeric ticks; an enum axis gets
one tick per declared option, **in declaration order** — that order is
the axis, which is exactly why the catalogue refuses two enums whose
options differ.

**Negative half.** A node whose `axis_field` is absent cannot be placed
on the axis: it goes to an **unplaced lane** pinned to the left, before
the axis begins, captioned *"no value for `level`"*, and counted. A span
whose end is *before* its start is data, not a bug in the drawing: it is
drawn as a zero-length mark with a caret and named in the frame, because
that is a content defect a designer wants to know about and a renderer
that silently sorted the two ends would hide it. Overlapping marks
within one lane stack up to three deep and then collapse to a count chip
that expands on click.

## 5. Layout

This closes open question O1 of the views spec, which asked where layout
runs, on what, and whether the answer differs per renderer.

### 5.1 Which renderers need an engine at all

Three of the six do not. `map` reads coordinates (dragged or declared),
`table` has rows, `timeline` has an axis. Their geometry is data, and
running a graph layout for them would be inventing an arrangement over
one the designer or the game already stated.

So the engine serves `graph`, `layered` and `nested`, plus one narrow
job for `map`: nothing — `map`'s unplaced nodes go to the shelf, not
through the engine. That is three consumers, and it is worth noticing
before choosing an engine that they are all *ranked or hierarchical*
shapes.

### 5.2 The engine: `@dagrejs/dagre`, vendored

**Chosen: `@dagrejs/dagre`** (with `@dagrejs/graphlib`), vendored as ESM
under `static/vendor/`, ~90KB unminified across the two, no
dependencies, no build step required to consume it. It does layered
ranking with edge routing, and it does *compound* graphs — nodes that
contain nodes — which is `nested` almost exactly. One engine, three
consumers, one set of bugs.

**Rejected: ELK.js.** The views spec named it as the obvious layered
candidate and it is the better layout algorithm — better edge routing,
better port handling, genuine orthogonal routing. It is also
~1.5MB of transpiled Java in its bundled form, wants a Web Worker
wrapper it ships as a separate build, and would be, by a wide margin,
the largest thing in a product whose first technical invariant is that
lightweight is a first-order goal. The quality difference is real and it
is not worth an order of magnitude of payload for three diagram shapes
at a thousand nodes.

**Rejected: d3-force.** It is small and it was the obvious `graph`
candidate. Three reasons against, and the third is decisive:

1. A force layout is a *simulation*: it settles over time, which means
   either an animation this design has already refused (§2.7) or a
   synchronous tick loop that blocks for as long as the animation would
   have taken.
2. Its output depends on initial conditions, which is why the views spec
   had to invent `layout_seed` to keep a saved view recognisable.
   Choosing a deterministic engine removes the need for a seed rather
   than managing it.
3. It has no notion of rank, so `layered` needs a second engine anyway —
   and "the answer may be different per renderer", which the views spec
   allowed for, means two layout implementations, two sets of geometry
   bugs, and two different-looking pictures of the same graph depending
   on which renderer a view names. One engine that draws `graph` as a
   ranked layout is a *better* answer than two engines: a game's content
   graph is overwhelmingly directional — `requires`, `unlocks`,
   `connects_to` — and a ranked drawing of it is more legible than a
   hairball that happened to relax into a shape.

**Rejected: server-side layout.** A drag must re-flow instantly, the
server holds no canvas, and layout output is not cacheable across
`map`-style manual edits. The one thing it would buy — identical
pictures for two designers — is bought instead by determinism.

### 5.3 The composition rule: manual wins, exactly

The views spec settled three `layout_mode` values, and `0008_views.sql`
is explicit that **no server code reads the column**: it is a contract
with the client. This section is that contract, and it is the client
that reads it — which is the answer to "what reads this knob".

Given the envelope's `positions[]` (present, possibly empty, for a saved
view; absent for an inline query) and each row's `pinned`:

- **`auto`** — stored positions are ignored entirely (and not deleted).
  The engine lays out every node. **Dragging is disabled**, and the
  canvas says why: a drag in `auto` would write a row nothing reads,
  which is this project's oldest recurring defect wearing a mouse. An
  editor gets a one-click *"switch this view to mixed"*, which is an
  ordinary `views.upsert` with `expected_version` and bumps the version
  like any other edit; a viewer gets the sentence without the button.
- **`manual`** — every node with a stored position is drawn there,
  pinned or not. Nodes without one are laid out by the engine, over the
  sub-graph of *unplaced* nodes only, and the result is **not written
  back**: they stay unpinned and absent from `view_positions` until a
  human drags one. This is only safe because the engine is deterministic
  and the envelope's node order is stable, so the same unplaced node
  lands in the same spot on every load. Auto-placement being reproducible
  is what lets it not be persisted.
- **`mixed`** (default) — the interesting one, and the one dagre cannot
  do by itself: it has no fixed-node constraint. So the composition is
  ours, in three deterministic steps:
  1. Lay out the **whole** graph with the engine, ignoring stored
     positions. This produces a *shape*.
  2. Fit that shape to the pinned nodes with a similarity transform —
     translation and uniform scale, **no rotation** — minimising squared
     error against the pinned coordinates. With ≥2 pinned nodes the
     transform is determined; with exactly 1 it degenerates to a
     translation; with 0 it is the identity and `mixed` behaves as
     `auto`.
  3. Restore every pinned node to its exact stored coordinate, then run
     one deterministic separation pass over the *unpinned* nodes only —
     pushing apart along the axis of least displacement, in entity-key
     order — to reduce overlap with the pinned ones.
  **Pinned nodes never move.** The separation pass is a reduction, not a
  guarantee: it is stated here as a reduction so that nobody later reads
  a promise of non-overlap into it and builds on one. An unpinned node
  that *has* a stored position (written with `pinned: false`) is used as
  its starting coordinate in step 3 and may be moved; that is the only
  difference between an unpinned stored row and no row at all in this
  mode, and it is the difference `manual` does not make.

### 5.4 Bounds, and the engine being slower than a designer

The server already caps the answer at 1000 nodes, so the engine's input
is bounded by construction. That is not enough: dagre's cost is
super-linear in edges, and a thousand-node dense graph is a real thing a
designer will produce by accident.

- **Layout runs in a module Web Worker**, so a slow layout never freezes
  the page, the pan/zoom, or a drag already in flight. A module worker
  imports the vendored ESM directly; no build step, again.
- **A 2-second budget.** At 2s the worker is terminated and the canvas
  draws the **fallback arrangement**: a deterministic grid, ordered by
  node type then entity key, with edges drawn as they fall. The frame
  says so plainly — *"Layout did not finish in 2 seconds; nodes are
  arranged in a grid. Narrow the query, or drag what matters."* —
  because a grid presented without explanation reads as the graph having
  no structure, which is a false statement about the game.
- **Retry is explicit, and per-view.** A *"try again with 10 seconds"*
  button exists for the designer who knows their graph is big and wants
  to wait. There is no automatic escalation: the second attempt is on
  the same failing input.
- **The engine's elapsed time is in the footer strip** beside
  `duration_ms`, so "the query is slow" and "the picture is slow" are
  distinguishable without a profiler. Both numbers are the measurement
  the views spec's own O2 (indexed jsonb fields) is to be re-argued
  with; a UI that reported one and not the other would answer half of
  that question.
- **During layout the canvas shows the previous arrangement, dimmed**,
  not a spinner over blank space, when there is one. Re-running a view
  should not cost the designer the picture they were looking at.

### 5.5 Layout across re-runs

A re-run (a parameter change, an SSE-driven refresh, a manual refresh)
must not reshuffle a picture the designer is working in. So layout is
**incremental by identity**: nodes present in both the previous and the
new envelope keep their computed coordinates; the engine is run over the
new nodes only, in the `manual` fashion, treating the retained ones as
obstacles. A full re-layout happens only when the designer asks for it
(*"re-arrange"*), or when the retained set is under half the new one, at
which point the old arrangement is not a picture anyone recognises
anyway. Both thresholds are stated here so the behaviour is a rule
rather than an accident of an implementation.

### 5.6 `layout_seed` has no reader, and this spec says so

`views.layout_seed` exists because the views spec assumed a
force-directed layout that needed seeding to be reproducible. With a
deterministic engine and a deterministic node order, **nothing reads
it** — not the server (`0008_views.sql` says so) and, after this
design, not the client either.

Rather than inventing a reader to justify the column — permuting the
input order by the seed would change pictures for no benefit, which is
the definition of a knob that lies — this spec records the column as
**dormant**, names the one thing it is for (an engine that is ever
stochastic), and leaves the choice between keeping and dropping it as
O3. Both the code comment and the tool description should say "no reader
today" for as long as that is true; a silence around a real limit is the
same defect as a false claim, with the sign flipped.

## 6. The interactions that write

Two of them, and they are the only writes this sub-project performs
against a view: dragging a node, and placing a background.

### 6.1 Dragging

Drag with the pointer; snap to `snap` when the parameter is set and the
mode is `manual`; shift-click and marquee for a multi-selection, which
drags as one body and writes as **one** `set_positions` call. Arrow keys
nudge the selection by one grid unit (or 1px with no grid), which is the
keyboard path to the same write.

**The write happens on drop, not during.** A drag is one intention; a
write per frame is sixty writes and sixty `view.positions` events fanned
out to every other browser in the game.

**Pinning.** A dragged node is written with `pinned: true`. That is what
a drag means: *I decided where this goes*. Unpinning is a separate
explicit act (*"let the layout place this again"*), which writes
`pinned: false` for that node — and in `manual` mode has no visible
effect until the position is cleared, which the same menu offers
alongside it, because a control that appears to do nothing is worse than
one that is absent.

**Undo.** One level, per browser session: the pre-drag coordinates are
kept and Ctrl/Cmd-Z re-writes them. `view_positions` has no history —
there is no server-side undo to reach for, and a mis-drag of a
forty-node selection is otherwise unrecoverable. Its bound is stated in
the menu: undo covers the last drag in this tab, and nothing else.

**Failure.** A refused write reverts the nodes to where they were and
bands the frame with the server's sentence. Optimistic movement with
silent failure would leave a designer's screen disagreeing with the
database indefinitely, which is the one outcome a shared design tool may
not produce.

**Concurrency is last-writer-wins, and this is said out loud.**
`set_positions` carries no `expected_version` and a position write does
not advance the view's version. Two designers dragging the same node
end with the later drop, and the earlier designer learns by re-reading
(§6.3). This is the right trade for coordinates — a conflict dialog over
a node's x is absurd — and it is documented rather than hidden, since it
is the one place in this product where a write silently loses.

### 6.2 Placing a background

Upload is REST and multipart (`POST …/view-assets`), from the browser
only, per the views spec. The picker is filtered to the three accepted
types and **states the refusal before the file is chosen** — 8 MB,
PNG/JPEG/WebP, *SVG is refused because an SVG served inline can carry
script* — repeating the server's own reason rather than paraphrasing it.
A refused upload shows the server's error verbatim.

Placement is a mode: the image is drawn under the nodes, and while
*adjust ground* is active it is the thing that drags and scales while
the nodes hold still. Committing the mode writes one
`set_background {asset_id, scale, offset}`. Cancelling writes nothing.
Clearing is `asset_id: null`, which is the only spelling for it, and it
is behind a confirmation because it is destructive to a piece of the
picture and cheap to hit by accident.

While either write is in flight the affected elements carry `--pending`
— a reduced-opacity treatment, not a spinner — so that the difference
between *saved* and *about to be saved* is visible without being
theatrical.

### 6.3 What a second browser does

The four events — `view.upserted`, `view.removed`, `view.positions`,
`view.background` — carry the view's id and key (and, for the first two,
a version), and deliberately carry no content. `events.go` argues that
at length: publication order is not commit order, so any value in a
payload is a value a client can render staler than what it already had.
**The client therefore never patches from an event. It re-reads.**

The rules, per kind:

- **`view.positions` and `view.background`** — a *placement* changed.
  Re-read and apply **automatically**: the meaning of the picture has
  not changed, only where things sit, and asking a designer to press a
  button to learn that a colleague moved a node is friction with no
  information in it.
- **`view.upserted`** — the *query* changed. Re-read the row, but do
  **not** swap the picture underneath the designer. Band the frame:
  *"This view was edited elsewhere. Reload to see the new query."* with
  the button. A picture that silently becomes a picture of a different
  question is the same class of failure as a silently dropped filter,
  which is the failure the whole `on_stale: fail` default exists to
  prevent, and the interface must not reintroduce it one layer up.
- **`view.removed`** — the view is gone. The canvas is replaced with a
  sentence and a link back to the view list. Nothing is auto-navigated:
  a designer with unsaved arrangement intent should be told, not moved.

Four practical rules the re-read needs, each because of a real failure
mode:

1. **Coalesce.** Events are debounced over 750ms and collapsed per view,
   so a colleague dragging a marquee of forty nodes causes one re-read.
2. **Never re-read over live work.** While a drag is in progress, or a
   write is unacknowledged, the re-read is deferred until both are
   settled. Otherwise a colleague's event yanks the node out of the
   user's hand.
3. **Defer while hidden.** A hidden tab marks itself dirty and re-reads
   on `visibilitychange`. Fifty background tabs re-running graph queries
   is a self-inflicted load test.
4. **The client cannot recognise its own echo.** The payload carries no
   writer identity, so every write comes back as an event about a change
   the client already made. That is *fine* and must be made fine rather
   than worked around: a re-read is idempotent, and rules 1–3 keep it
   cheap. The alternative — a client-generated write token echoed
   through the event — is a change to the server's event payload, and
   the payload's minimality is a decision `events.go` made deliberately.
   Recorded as O4 with its real cost, which is that a position write
   currently costs a full query re-run to observe: **there is no
   positions-only read** in REST or MCP, and `views.run` is the only way
   to get `positions[]` back.

## 7. Navigation

Today the product has one page per game listing types, documents and
relation types. This is what replaces it.

### 7.1 The shape

A game is the unit of navigation — the core spec settled single-game
navigation — so the header carries the game's name, a game switcher when
the user has more than one, and three destinations, which are the three
things a game contains:

**Views · Catalogue · Prose**

Three, not a sidebar of twelve. The metamodel has four primitives and
the product has three surfaces over them, and a designer should be able
to hold the whole map of this tool in their head on day one.

### 7.2 Routes

Client-side routes, each served by a static shell the server maps the
same way it already maps `GET /g/{slug}` and `GET /g/{slug}/doc`:

| Route | What it is |
| :-- | :-- |
| `/g/{slug}` | game home |
| `/g/{slug}/views` | every saved view: name, renderer, stale badge |
| `/g/{slug}/v/{key}` | one view, drawn; `?p.<name>=` binds parameters |
| `/g/{slug}/types` | the declared entity types and relation types |
| `/g/{slug}/t/{typeKey}` | the catalogue of one type: search, filter, page |
| `/g/{slug}/e/{typeKey}/{key}` | one entity |
| `/g/{slug}/doc?path=` | one document (exists) |
| `/g/{slug}/assets` | the background images this game has uploaded |

Every one of these is a real URL a designer can send to a colleague.
That is not a nicety in a tool used by a team of three arguing about a
progression: *"look at this"* is the most common sentence in the room,
and it has to be a link.

### 7.3 Game home

Not a dashboard (§2.7). Three lanes, in this order:

1. **Views** — the six most recently updated, by name and renderer, with
   stale badges. Views are the reason to open this product.
2. **Catalogue** — each entity type with its entity count, as a link
   into that type's catalogue.
3. **Prose** — the most recently edited documents.

Each lane's empty state is the one already in `game.html` and keeps its
existing rule, which this design adopts rather than re-deciding: the
sentence that says what to do about it depends on the reader's role, and
a viewer is never told to do something the server will refuse.

A game with no views at all gets the one piece of onboarding in this
product: a sentence saying that views are written by an agent over MCP,
with a link to the skill bundle's documentation. It is honest about the
workflow instead of pretending to a *New view* button that leads
nowhere (§9).

### 7.4 An entity

The page a designer reaches from a diagram by clicking a node, and the
page they will spend the most time on after the canvas:

- Name (serif, large), type and key (mono, muted).
- **Fields**, in the type's declared order, rendered by declared type: a
  number right-aligned, an enum as its value with the option list on
  hover, text as text. A field the entity does not carry is shown as
  declared-and-unset rather than hidden, because *what could be filled
  in here* is the question a designer is usually asking.
- **Relations**, in two lists, *out* and *in*, grouped by relation type,
  each row naming the far entity and carrying the relation's **own
  fields** — this is where `is_one_way` and `requires_ability` are
  visible, and typed edges being a first-class idea in this product means
  they get a first-class place on this page rather than a tooltip.
- **Documents** linked to this entity, by path, with a preview line.
- **Where this entity appears**: nothing. See O5 — there is no query
  from an entity to the views that draw it, and inventing one client-side
  by running every saved view is not a feature, it is a denial of
  service.

The view frame's node click opens this page in a side panel over the
canvas rather than navigating away, with the full page one click
further; losing a diagram's arrangement to read a field would make
reading fields feel expensive.

### 7.5 A catalogue

The list of one type's entities: a search box over the existing search
endpoint, sortable columns for the type's declared fields, paging over
the existing cursor. It is deliberately close in appearance to the
`table` renderer without being it — the catalogue is a fixed listing
over one type, the renderer is an answer to a query — and it says which
it is at the top so nobody confuses a saved view with a browse.

## 8. Accessibility, and what is measured

### 8.1 Every view has a text twin

The canvas is `aria-hidden`. Beside it, one keystroke away and always
present in the DOM, is the **text twin**: the same envelope rendered as
a table — node name, type, and every projection slot as a column; then
the edges as a second table of source, type, target, label. It is a
real, focusable, screen-reader-navigable representation of the answer,
and it is the accessible content of every view including the five
graphical ones.

This is not a compromise version. It is cheap (the `table` renderer's
machinery already exists), it is honest (an SVG hairball with ARIA roles
sprinkled on it is not navigable by anyone), and it is useful to sighted
designers too — "show me this as a list" is a thing people want from a
diagram constantly. It is also the answer to keyboard navigation of the
canvas: focus moves through the twin, and the selected row highlights
its node.

Beyond it: visible focus rings in `--focus` everywhere, contrast ≥4.5:1
for text and ≥3:1 for the node outlines and rules that carry meaning, no
information in hue alone (§2.5), a full keyboard path to both writes
(arrow-key nudge; a background placement panel with numeric fields
beside the drag), and `prefers-reduced-motion` removing transitions.

### 8.2 Budgets

Stated as numbers so they can fail:

- **First contentful paint of a game page under 1s** on a laptop over a
  local network, with no framework beyond the vendored Lit.
- **A 500-node `graph` drawn in under 1s total**, including layout.
- **Layout hard-stopped at 2s** (§5.4).
- **A drag stays at 60fps at 1000 nodes.** This is what forces the
  implementation detail worth naming: during a drag only the dragged
  nodes and their incident edges are re-rendered, via a transform on a
  detached layer, never a full re-render of the SVG tree.
- **Total vendored JavaScript under 150KB minified**, all of it read
  before it is committed. Every dependency justifies its presence, and
  in a public repository a vendored file is a file this project is now
  answering for.

## 9. What this sub-project does not build

Stated so the next one is not surprised, and so that nobody reads an
omission as an oversight.

- **No query builder, and no way for a human to create a view.** This is
  the big one and it is deliberate. D2 chose agent-authored views, and a
  point-and-click builder over the full query language — traversals,
  depth ranges, `edge_where`, projections — is a sub-project in itself,
  not a corner of this one. A designer with no agent can *open, run,
  parameterise, arrange and back* a view; they cannot make one. The
  views spec anticipated a builder ("Maestro's own UI, which will grow a
  point-and-click query builder") and this spec does not deliver it. The
  onboarding sentence in §7.3 is what stands in for it, and the
  discomfort is real: recorded as O1.
- **No repair action for a rename.** The views spec §6.4 imagines a
  "one-click update references" that bumps the version like any other
  edit. It edits an author's document and it needs a diff shown before
  it is applied, so it stays out of scope here. *The second half of this
  entry has expired*: it read "the rename operation it exists to repair
  does not exist yet", and `types.rename` and `relation_types.rename`
  now exist, so the repair is no longer speculative — a designer can
  produce a `*_renamed` banner in one call. O6 is now a question about
  when, not about whether.
- **No analysis.** Cycles, unreachable content, orphans, routes — all of
  sub-project 6. §4.4's "3 edges run against the ranking" is a drawing
  artefact honestly reported, not an analysis, and must not grow into
  one here.
- **No editing of content.** Entities, types, relations and documents
  are *read* in this interface. They are written by agents over MCP and
  by the REST API. An entity editor is a real product need and a
  different design.
- **No export.** No PNG, no SVG download, no print stylesheet.
- **No presence, no cursors, no comments.** Two designers in one view
  see each other's writes (§6.3) and nothing else.
- **No i18n.** The UI ships English strings, per the language policy.
  The *content* is whatever the game is written in and is never
  transformed.
- **Phones read, they do not arrange.** Layouts respond down to tablet
  width; below it, view pages fall back to the text twin and the
  writing interactions are disabled rather than shrunk. A 1000-node
  drag target on a 390px screen is not a thing to design.
- **No admin UI.** Invites and tokens stay on the command line, as the
  readme documents.

## 10. Open questions

Genuinely open. None blocks the implementation plan; each wants an
answer before the sub-project closes.

**O1 — a human alone cannot make a view.** §9 argues the deferral. Is
there a cheap intermediate — *duplicate this view and edit its
parameters*, which is an `upsert` of an existing document with no
builder anywhere — that removes the worst of it? It is one call and
about a day of interface. It is left out here because "duplicate" needs
a key-naming UI and a story for what a designer does when the duplicate
is also wrong.

**O2 — an edge's endpoints are ids, and nothing reads an entity by id.**
Every REST and MCP read of an entity is by `(type, key)`. So a stub edge
(§4.2) can be drawn and counted and never named, and a node clicked in a
diagram is navigable only because the *node* carries its key. Adding
`entities.get_by_id` is small; whether the interface's need justifies a
new read on the content surface is the sub-project owner's call.

**O3 — `layout_seed` has no reader** (§5.6). Keep the column dormant and
documented, or drop it in a migration? Keeping it costs a sentence in
two doc comments; dropping it costs a migration and forecloses a
stochastic engine.

**O4 — a position write costs a full query re-run to observe** (§6.3).
There is no positions-only read. Either `views.run` grows a
`positions_only` mode, or a `GET …/views/by-key/{key}/positions` lands,
or the coalescing in §6.3 is simply accepted as sufficient. Measure
before adding a route.

**O5 — "which views draw this entity?"** is a question the entity page
should answer and cannot: it would mean running every saved view. A
`view_refs`-style index over *entities* is not a small thing and may
belong to the analysis sub-project, which is already building reachability
over the same graph.

**O6 — who writes the rename repair** (§9), and does it belong to the
interface at all? The half of this question that asked whether the wound
was reachable is answered: `types.rename` and `relation_types.rename`
ship, so a designer can rename a type and meet the banner. What is still
open is whether the one-click repair belongs to this interface or to the
agent surface, and what diff it shows before it edits an author's
document.

**O7 — `view.upserted` for a viewer.** §6.3 refuses to swap the picture
automatically. For a *viewer* who cannot have composed the edit and has
nothing in flight, is the band still right, or is a viewer's screen
better off simply following the document? Two defensible answers; the
banner is the conservative one.

**O8 — one background layer** (views spec O7). This design assumes one
ground per view. A world map with a political overlay is a plausible
near-term ask and would change the placement mode, the frame and the
schema together.

**O9 — system fonts versus a vendored face** (§2.4). The identity's
sharpest edge against the payload budget and a binary in a public repo.

**O10 — the fallback grid's threshold** (§5.4). 2 seconds is a guess
about a designer's patience, not a measurement. It should be revisited
against a real game's largest view, and the number belongs in one
constant so that revisiting it is an edit and not an archaeology.

## 11. Definition of done

A designer opens a game they have never seen, lands on its home page,
and can name the three things it contains without being told. They open
*Mage quests 20–30*, bind `class` to `mage` in the parameter bar, and
see a graph coloured by zone with a legend they can read, over a
background someone uploaded, at a thousand nodes, in under a second.
They drag four quests; the arrangement survives a reload, survives a
colleague's browser learning about it through one coalesced re-read, and
survives the agent adding twelve more quests — which arrive
auto-placed, unpinned, and counted in a band that says so.

Then the agent renames a relation type. The view does not draw. The
panel names the step, in the server's own words, with the pointer; one
click runs it best-effort and the reduced picture arrives under a band
naming what was dropped. Nothing anywhere in that sequence showed the
designer a picture that was quietly wrong.
