# The analysis screens: what each of the four answers looks like

**Date:** 2026-09-11
**Task:** Analysis UI 1 (`14a69d15`), under "Analysis has no screen" (`eed0fd90`).
**Decides:** where analysis lives in the product, and the shape of each of
the four answers. No code.

The engine ships and nothing in the interface reads it. Four analyses
answer four differently shaped questions and none of them is a renderer
the catalogue already has, so this is a design pass before a build one.

## 0. Where it lives

**Analysis is a destination of its own**, the fifth in the header bar
after Views, Catalogue, Prose and Images. It is not a lane inside the
catalogue and not a renderer inside views.

Why not inside the catalogue: the catalogue answers *what is in this
game*, one declared type at a time, and every one of its screens is
scoped to a type. Three of the four analyses are not scoped to anything;
they are readings of the whole game at once, and the most useful of them
(unreachable) is exactly the question the catalogue cannot answer,
because it is about paths between types rather than rows of one.

Why not inside views: a view is a saved query plus a renderer, written by
an agent, and its whole contract is that the person reading it did not
compose it. An analysis is not composed at all — it is run, and the only
parameters are bounds. Putting it under Views would make "a saved thing
an agent wrote" and "a question anyone can ask" the same word.

The frame settled in
`docs/superpowers/specs/2026-09-10-global-look-design.md` already
reserves Analysis as the fifth destination and deliberately does not ship
it, "because a destination pointing at nothing is worse than one that is
missing". This is the task that gives it somewhere to point.

**Routes are inside Analysis, not beside it.** A route is a saved claim
about a path and its only purpose is to be checked; its verdict is an
analysis. It is the one part with stored state, so it gets a list and a
detail under `/g/{slug}/analysis/routes`, with the other three living on
`/g/{slug}/analysis` as sections of one page.

## 1. The rule that outranks every layout below

**An empty findings list is not a verdict.** The engine is built around
this: `CyclesResult` carries `SeedTotal`, `EdgesWalked`,
`InvalidEdgesFollowed`, `Truncated` and `DepthLimited`; `OrphansResult`
carries `ConsideredTotal` and `ExcludedRelationTypes`;
`UnreachableResult` carries `ReachableTotal`, `UnreachableTotal`,
`PerType`, `Seeds` and `Gating`. Every one of those fields exists because
an empty list means two entirely different things — *the game is clean*
or *the walk followed nothing* — and the JSON is otherwise identical.

So every analysis section on screen has two parts, always, in this order:

1. **The verdict line**, one sentence in ink.
2. **The findings**, in the shape below.
3. **What this run looked at**, a muted line under the findings that is
   present when the list is empty and when it is full, never only on one
   of them.

And a run that was cut short says so *above* its findings, not below: a
truncated or depth-limited answer is a different claim from a complete
one, and a reader who scrolls past the findings has already believed it.

## 2. Cycles

**A list, not eleven drawings.**

The graph renderer can draw a subgraph and the temptation is to draw each
loop. Rejected: a cycle's content is *which edges close it*, and at the
sizes these come in — three or four nodes — a drawing spends a hundred
square pixels to say what one line says better. Eleven drawings on one
page is also eleven separate coordinate systems for a reader to orient in,
one after another, for no gain.

Each finding is one row, and the row **is the loop, spelled**:

```
Ash 3  --requires-->  Cinder 12  --requires-->  Ember 4  --requires-->  Ash 3
```

The entity names are the game's own words, so they are the serif and each
is a link to its entity page. The relation type is the game's word too
(`CycleEdge` carries the type), set in the label role between them, and
the arrow is text. The row wraps on narrow screens rather than scrolling,
because a loop is read in order and losing the order loses the finding.

**Two lists, not one.** `Cycles` and `ContainmentCycles` are separate
fields because they are separate findings: a containment loop is a
hierarchy that is not one, a prerequisite loop is a gate nobody can open.
Different sentence, different fix. Two sections with their own headings
and their own verdict lines, never a single list with a type column.

**The verdict lines.** *"Nothing depends on itself."* and *"Nothing
contains itself."* when empty; *"Three prerequisite loops."* when not.

**What this run looked at:** *"Started from 412 entities and followed
1,207 edges."* Plus, when either flag is set, a line above the findings:
*"This answer is not complete: the walk stopped at its depth bound, so a
longer loop may exist and this run did not look."*

**The one drawing worth having**, and it is deferred to the build pass,
not designed here: a single link per finding that opens that loop in the
graph renderer as a view. It costs nothing on this page and it is the
right place for a picture.

## 3. Unreachable content

**A list whose content is the reason.**

The mistake to avoid is a list of names with the reason as a grey
annotation. The name is what the reader already has; the reason is what
they came for, and `Finding` carries a four-value vocabulary for it:
`isolated_from_start`, `no_path`, `container_unreachable` and
`depth_limited`. Those are the model's spellings and none of them reaches
the screen. In the reader's words:

| `Reason` | On screen |
|---|---|
| `isolated_from_start` | Nothing leads to it |
| `no_path` | Every way in is itself unreachable |
| `container_unreachable` | The place it is in cannot be reached |
| `depth_limited` | Further away than this run looked |

`depth_limited` is not a finding about the game, it is a finding about the
run, and it is separated out below the others under its own line: *"Four
more were further away than this run looked."* Mixing them would report
the tool's bound as the game's defect.

The row: the entity's name in the serif, its type beside it, the reason in
the tool's voice, and the **blockers** the engine already names — the
`SeedRef`s that would have let it through — as links. A finding with
blockers is a finding a designer can act on in one click, and the blockers
are the only part of this screen that is not already on another one.

**Grouped by reason**, not by type. A designer fixing "nothing leads to
it" does one kind of work; `PerType` goes in the run summary rather than
being the grouping, because the type is a column and the reason is the
section.

**What this run looked at:** *"Reached 380 of 412 entities from 6 starting
points."* and, when `Gating` says so, which relation types were treated as
gates. A reachability answer whose gate set is invisible is a claim the
reader cannot check.

## 4. Orphans

**The dense one, and therefore a paging and filtering question.**

`Orphan` carries `InDegree` and `OutDegree`, and `OrphansResult` carries a
`Mode`. The mode is the screen's one control: **nothing points at it**,
**it points at nothing**, or **both**. Three chips, exactly one selected,
which is the chip vocabulary the product already has.

The row is the catalogue row this product already ships — name, key, type
— widened with the two degrees as columns. It uses `rows.js` and nothing
else: an orphan list that looked different from a catalogue list would be
two vocabularies for one shape.

It pages with the shared "Show 50 more", and it carries the catalogue's
search when the list is long, because finding one suspected orphan by its
key is the second thing anybody does here.

**What this run looked at:** *"Considered 412 entities."* and, when
`ExcludedRelationTypes` is non-empty, *"Edges of these kinds were not
counted: annotates, tagged_with."* An orphan report that silently ignored
a relation type would be wrong in a way nobody could see.

## 5. Routes

**The only one with a lifecycle, so a list and a detail.**

`RouteStatus` has exactly three values and the screen must never collapse
them to two:

| Status | On screen | Treatment |
|---|---|---|
| `never_checked` | Never checked | muted, dashed outline |
| `stale` | Checked against an older design | the one place `--danger` is earned here |
| `checked` | Checked | ink |

`stale` is not a failure and must not read as one; it is *the answer may
no longer be about this game*. The word does the work, and the treatment
separates it from a route that was checked and from one that never was.
Two states is the failure mode: a screen that shows a tick or a cross
turns "we do not know" into "it is fine".

**The list** is the shared row: the route's name, its key, its status, and
when it was last checked. Status is a column, not an icon.

**The detail** shows the ordered claim beside the verdict, and they are
two columns because they are two things: the steps the route asserts, in
order, and what the last check found at each. A verdict about a game that
has since moved says so at the top, in the route's own words: *"Checked
against design version 41; this game is at 58."*

**The one write in the whole of Analysis** is "check this route", and it
is the primary button on the detail. Everything else on these screens is
read-only and says so with the shared Read-Only Notice, which currently
exists as a component and appears on no screen in the product.

## 6. What the four share

- **One page, four sections** at `/g/{slug}/analysis`, plus
  `/g/{slug}/analysis/routes` and `/analysis/routes/{key}`. The trail is
  `Game / Analysis` and `Game / Analysis / Routes / <name>`.
- **Each section runs on demand, not on load.** These are POSTs and some
  of them walk a whole game; a page that fires four of them on every visit
  is a page that costs a second of server time to glance at. Each section
  opens with its verdict line absent and a ghost button: *"Run"*. A
  section that has been run keeps its answer until the page is left.
- **The negative half is never optional.** See §1.
- **Nothing here is a new list shape.** Cycles is the only genuinely new
  row in the product; the other three are `rows.js` widened with columns,
  which is what the row was widened for in design pass 5.

## 7. What this deliberately does not decide

- **Whether a cycle links into the graph renderer as a saved view.** It
  should, and it is a build-pass decision about the query language rather
  than a layout one.
- **Bounds.** Depth, max results and the row cap are query parameters with
  server defaults; whether a designer may raise them from the screen is a
  question about who is trusted with a slow query, and it belongs with the
  query builder rather than here.
- **Whether analysis should run automatically and notify.** That is a
  different product (a watcher), and it needs the events the product
  already has plus a policy about noise.
