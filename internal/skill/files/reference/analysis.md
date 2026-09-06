# Analysis

Four questions about a game that no listing answers, because each is
about the shape of the whole graph rather than about any row in it.

| The question | Where the answer comes from |
|---|---|
| do my prerequisites loop? | `analysis.cycles` |
| can a player reach everything? | `analysis.unreachable` |
| what is joined to nothing? | `analysis.orphans` |
| does this progression still hold? | `routes.upsert`, then `routes.check` |

The workflow that puts them in order is `recipes/auditing-a-design.md`.
This page is the five things about the engine that no single call can
tell you, each of which decides whether you believe its answer.

## 1. Declare behaviour, or the engine declines to guess

A relation type carries two independent declarations, and making one is
not making the other:

- `semantic_role`: what the edge **means**, which is what a view reads.
- `analysis_traits`: how the edge **behaves** in a graph walk, which is
  what this engine reads.

The trait vocabulary is closed, and `relation_types.upsert`'s own
description carries it together with the combinations that contradict
each other. Nothing is enumerated here, because a second copy of a
vocabulary is a copy that goes quietly false.

A game whose relation types declare neither is refused with
`semantics_undeclared`, carrying its whole relation type catalogue with
whatever each one currently says. That refusal is the first thing you
will meet, and its recovery is neither "change your argument" nor "send
it again": go and declare something about the edges that gate
progression. `reference/errors.md` has it beside the other codes.

A type that declares no traits but carries a `semantic_role` is read
through a fixed translation, so a game seeded before traits existed
analyses without an edit. The translation is printed in the trait table
the analyses carry in their own descriptions, and the refusal above
names it too.

**A type with neither is not walked at all.** It is not treated as inert
and it is not guessed at; it is an edge the engine was told nothing
about. Silence is the one thing never read as a statement — except in
the orphan report, which asks the vocabulary for one word only:

> **From `analysis.orphans`'s own description:**
> **This is the one analysis that does not refuse a game which declared
> nothing.** It asks the vocabulary for exactly one thing — which types
> are `annotation` — and a game with none is a perfectly meaningful
> input. The other three refuse, because each would otherwise report a
> clean bill of health from an engine with no edge it was allowed to
> walk.

## 2. Every answer carries the reading it rests on

Each of the four results embeds `semantics_source`: one entry per
relation type the run walked, with the traits it was read as carrying
and where that reading came from.

| `source` | What it means |
|---|---|
| `declared` | the type's own `analysis_traits` |
| `derived_from_role` | no traits; its `semantic_role`, translated |
| `caller_supplied` | this call named the relation types itself |

Read it before acting on a finding. A verdict whose interpretation of
the game is invisible is one you can only accept or reject wholesale,
and the usual surprise — why is `available_to` a gate? — is answered
there, by a role somebody set months ago.

The third row shadows the other two: when a call names its own relation
types, and `analysis.cycles` and `analysis.unreachable` both take a
list, that list is the set the run walks. A key naming no relation type
of this game is refused rather than quietly dropped, because a filter
that silently emptied itself would answer "your whole game is
unreachable" to somebody who did narrow the question.

## 3. Flagged edges are followed here, and excluded in a view

A relation is flagged `invalid` when **its own fields** stop fitting its
relation type's `field_schema` — after a schema edit, most often. It
says nothing about the row's two endpoints or its type, and endpoints
and type are the only things any of these analyses read.

So the engine walks flagged edges by default, and a saved view does not.
That disagreement is deliberate on both sides. A picture asserts a
relationship and a reader believes it, so drawing an edge whose own
values are known-broken is a claim the game does not support. A verdict
about structure is the other case: it must not move when a field schema
moves, or adding a required field to `requires` would turn forty
missions unreachable for a reason with no relationship at all to whether
a player can get to them.

Every result reports `invalid_edges_followed` either way, and the
stricter reading is available per call for the day you want it.

## 4. A route is a stored claim, plus a verdict about it

`routes.upsert` stores an ordered claim — these entities, in this order,
are a walk a player can make — together with the parameters that define
what holding together means for it. `routes.check` turns that claim into
a verdict and stores it. Two people checking one route get one answer,
because the check runs under the route's own stored parameters rather
than either caller's defaults.

The status is the part clients get wrong, and it has three values:

> **From `routes.get`'s own description:**
> **A route has three states, not two.** `never_checked` is not `stale`,
> and `stale` is neither green nor red: it says the verdict is about a
> game that has since changed and says nothing about whether the route
> holds now. Any write to this game's types, relation types, entities or
> relations moves its `design_version`, which marks every route in the
> game stale — coarse on purpose, because a per-route dependency set
> would be subtly wrong the moment a new relation made a previously
> irrelevant entity relevant, and re-checking is cheap while believing a
> stale green is not. Editing a route's own steps makes its own verdict
> stale too.

Two consequences worth holding on to. A seed of two hundred entities
marks every route in the game stale, so re-check after a seeding session
rather than during one. And a route you have just rewritten is stale
about **itself**: the stored verdict is about a claim nobody is making
any more.

## 5. An empty findings list is not a verdict

Every one of these answers carries the counts that tell "nothing is
wrong" apart from "this run looked at nothing" — how many entities were
considered, how many were reached, how many edges were walked, whether a
depth bound or a result cap cut the run short. A walk seeded from
nowhere reports no unreachable content, and so does a healthy game.

Read the counts first and the findings second. It is the one property of
an analysis no finding can report, because the finding that is missing
is the one that would have told you.
