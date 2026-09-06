# Auditing a design

You have a game with content in it and a question a listing cannot
answer: is this actually playable? A player has to be able to get
somewhere, in an order the design admits, and nothing here checks that
until somebody asks.

This is the order to ask in. The mechanics behind each step —
what the engine reads, what a route's three states mean, why a flagged
edge is followed — are in `reference/analysis.md`.

## Step 0: declare how your edges behave, once

Every analysis but the orphan report has to be told how your relation
types behave — `analysis_traits`, or a `semantic_role` it translates —
and a game that says neither is refused rather than reported healthy. Do
this before the first audit and not again:

1. `relation_types.list` — read the catalogue with its versions.
2. For each type that gates, orders or contains: `relation_types.upsert`
   with `analysis_traits` and the `expected_version` you just read.
3. For each type that is decoration — an illustration, a note, a
   cross-reference — declare it `annotation` deliberately. Declaring
   nothing and declaring inertness are different statements, and only
   one of them keeps a half-finished idea off the orphan list.

Everything else may stay undeclared. An edge the engine was told nothing
about is one it walks nowhere, which is the safe answer.

## Step 1: loops, before anything else

`analysis.cycles` first, because a prerequisite loop poisons every other
answer: nobody can open any entity in the loop, so everything behind it
reports unreachable too. One loop can be forty findings in step 2 and one
fix here.

Two lists come back and they are two different problems — a gate nobody
can open, and a containment hierarchy that is not one. Fix by fixing the
design, or by fixing the declaration:

> **From `analysis.cycles`'s own description:**
> The fix is a trait declaration, not a change to the content — which is
> why every edge here names its type.

Mutual exclusion is the case that bites: two edges of one type saying
each option locks the other is a real two-cycle in a type that reads
like a prerequisite, and it is the declaration that is wrong, not the
content.

## Step 2: content nobody can reach

`analysis.unreachable`, once the loops are gone. Say where a player
starts — by naming entities, by naming a whole entity type, or by
pointing at a saved route — and read the reason on every finding before
you touch anything: a walk that stopped at its own depth bound has not
earned a claim about your game, and it says so rather than pretending.

Expect findings that are not defects:

> **From `analysis.unreachable`'s own description:**
> **Content reachable by a mechanism the design never wrote down is
> reported.** A vendor sells it, an NPC offers it, the player just walks
> there — none of that is an edge, so the engine cannot see it.

Each one is a decision, not a bug: either the design is missing an edge
it should have, or the whole category is handled somewhere Maestro
cannot see and you say so — deliberately, per category, and never about
one you have not read.

## Step 3: orphans, when the seed is finished

`analysis.orphans` last, and not in the middle of a seeding session:

> **From `analysis.orphans`'s own description:**
> **Bulk seeding routinely passes through a state where half the content
> is orphaned**: entities land before the edges that join them. An
> orphan count taken during a seed is not a finding.

Three modes ask three questions — nothing joined at all, content that
leads nowhere, content nothing leads to — and every finding carries both
of its degrees, so the three are checkable against each other rather
than believed one at a time.

## Step 4: turn a progression you care about into a route

An audit is a moment; a route is the same question asked again next
month, automatically. The lifecycle is four calls:

1. `routes.list` — what this game already claims, with each one's health.
2. `routes.upsert` — the ordered steps, plus the parameters this claim
   is to be judged under. Pass `expected_version` 0 for a new one, or
   the version you read for a replacement.
3. `routes.check` — prove it, and store the proof. Each step comes back
   with one of four answers, and one of them is about **order** rather
   than reachability: a step the design says must come later than the
   one before it is a claim about your route, not a missing prerequisite.
4. `routes.remove` — when the progression is retired. Read it first;
   this is not the deletion a saved view has:

> **From `routes.remove`'s own description:**
> **It takes an expected_version, and views.remove does not.**

Re-check after a seeding session, not during one. Every write to this
game's design marks every route in it stale, and stale is neither green
nor red — `reference/analysis.md` §4 has the three states and why the
middle one is a state of its own.

## What an audit is not

It is not a quality bar. Nothing here says whether a progression is fun,
whether forty missions is too many, or whether a zone is worth building.
It answers whether the design says what you think it says — and a design
that is loop-free, fully reachable and free of orphans can still be a
bad game.

It is also not a substitute for reading. The false positives above are
the shape of that: this engine sees the edges you wrote down and
nothing else, which is exactly as much as you have told it.
