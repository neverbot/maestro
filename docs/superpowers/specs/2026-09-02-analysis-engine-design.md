# Maestro — analysis engine (design)

Date: 2026-09-02
Status: draft, open questions listed in section 12
Scope: sub-project 6 of the roadmap in
`2026-08-31-core-and-metamodel-design.md`

> **Reconciled against implementation, 2026-09-02.** Changed:
>
> - Header — the open questions are in §12, not §10.
> - §2D — `analysis_traits` is a **proposal**, not a settled choice. The
>   decision it depends on is now stated once, as open question O1 in
>   the core spec, and §11.5 and §12.3 cross-reference it instead of
>   restating it. Nothing about the vocabulary itself changed.
> - §2 and §9 — an incoherent trait combination is `invalid_schema`, not
>   `schema_violation`. The committed `internal/metamodel/errors.go`
>   makes those two distinct sentinels, split by who is at fault, and a
>   bad *declaration* is the first.
> - §4 — the empty-seed-set refusal is neither of those two codes; it
>   needs one of its own, and that is now recorded as open.
> - §6 and §11 — the new tables' foreign keys must follow the committed
>   migration's composite-key convention.
> - §7 and §11.4 — `relations` **does** have an index leading with
>   `project_id` (`relations_project_idx (project_id, created_at)`).
>   The index this spec wants is still absent, but the stated reason for
>   it was false.

## 1. What this is for

A game design of four hundred missions cannot be read by eye. The
errors that hurt are not typos in a mission description; they are
structural, and they are invisible at the scale of a list:

- **Prerequisite cycles.** Mission A requires B, B requires C, C
  requires A. Every row looks fine on its own. Nobody can open the
  game.
- **Unreachable content.** A quest, a zone, a car that no player can
  ever get to, because nothing unlocks it or its precondition cannot
  be met. Content that was designed, written, paid for, and is dead.
- **Orphans.** Entities nothing points at and that point at nothing —
  usually a half-finished idea or a seeding pass that lost its edges.
- **Routes.** A named ordered walk through the content — *Mage
  levelling 1-20*, *the F3 championship season* — kept as a stored
  artefact and re-checked, so a designer can prove a progression
  actually holds together and know the moment it stops holding.

These are the four analyses named during the original design
conversation. This spec defines them, the vocabulary that makes them
possible on a metamodel that knows nothing about games, and how they
compute.

## 2. The central problem: Maestro knows nothing about games

Maestro ships no built-in game concepts. It has no idea which relation
types mean "requires" and which mean "is decorated by". That
genericity is the product: the same schema serves an MMORPG and a
racing career, and a genre nobody anticipated must fit without a code
change.

This lands hardest on analysis. A cycle detector cannot simply look
for cycles:

- `connects_to` between two rooms is a **legitimate** cycle. A world
  map with no loops is a bad world map.
- `requires` between two missions is a **bug**. A prerequisite loop is
  unplayable.
- `contains` between a championship and its races must *never* loop,
  but for a different reason: it is a hierarchy, not a gate.

The same graph shape means three different things depending on the
relation type, and the relation type is a string the game invented.
So the engine must be told, per game, which relation types carry which
**analytical semantics**.

### Alternatives considered

**A. Infer semantics from usage.** Observe the graph: a relation type
whose edges never form a cycle is probably acyclic; one whose edges
are always reciprocal is probably symmetric.

Rejected, and not narrowly. Inference makes the bug define the rule:
the first prerequisite cycle a designer creates teaches the engine
that `requires` is a cyclic relation type, and the analysis that
exists to find that cycle stops reporting it. Worse, the classification
changes silently as content grows, so the same design analysed on
Tuesday and Thursday gives different answers with no write in between.
An analysis tool whose verdict is not reproducible is not a tool.

**B. An explicit parameter on every analysis call.** The caller passes
the relation type keys to treat as prerequisites on each run.

Rejected as the *only* mechanism, kept as an override. It puts the
semantics in the caller instead of the design, so every agent and
every human invents its own list, the lists drift, and two people get
two answers about the same game. It also makes the knowledge
unshareable: nothing in the game's own record says that `requires`
gates and `flavour_text_for` does not. But as an override it is
genuinely useful — "run this once treating `recommends` as gating too"
— so it stays.

**C. Reuse `semantic_role` alone.** The committed schema already has
`relation_types.semantic_role`, nullable, constrained to
`prerequisite | unlock | containment | spatial | availability |
reward`. The core spec introduced it for exactly this sub-project.

Rejected as sufficient, kept as descriptive metadata. It is one axis
carrying two orthogonal things: what an edge *means* to a designer
(reward, availability) and how an edge *behaves* in a graph walk
(acyclic, symmetric, direction of dependency). Those do not line up.
`connects_to` is `spatial` **and** symmetric **and** legitimately
cyclic; `contains` is `containment` **and** acyclic **and** propagates
reachability downward; `follows` is a sequence with no role in the
list at all. A closed six-value enum of *meanings* has to grow every
time a genre invents a meaning, which is the exact failure mode the
pure metamodel exists to avoid, and it still would not say whether a
cycle in that type is a bug.

**D (proposed, and this spec's recommendation). A closed vocabulary of
analytical traits, declared per relation type, orthogonal to
`semantic_role`, with a per-analysis override.**

> This is a *proposal*, not a settled decision, and the correction
> matters because two other specs read it as settled. Adding
> `analysis_traits` beside `semantic_role` means the schema carries two
> vocabularies about relation types, which
> `2026-09-02-agent-skill-bundle-design.md` §10.2 identifies as a
> blocker for writing its analysis page. The question — traits plus a
> demoted `semantic_role`, traits with `semantic_role` dropped, or no
> traits at all — is recorded once, as **open question O1** in
> `2026-08-31-core-and-metamodel-design.md`, "Metamodel". **The user
> decides.** Everything below assumes the answer is "traits are added";
> if it is not, §2's vocabulary and §11.1 fall and the analyses
> themselves are unaffected, since they consume the *resolved* set of
> gating relation types and not the mechanism that produced it.

A game declares, on each relation type it cares about, which of a
small fixed set of *graph behaviours* that type has. The vocabulary is
about graph algebra, not about game meaning, so it stays small and
genre-independent: a racing career and a metroidvania both have
"things that gate other things" even though one calls it a licence and
the other calls it a double jump. Meaning stays in `semantic_role` and
in the type's own label, where designers can read it; behaviour goes
in traits, where the engine can use it.

This is the shape the user's own framing asked for throughout the
design: views and analyses are *driven by how a project classifies its
own relation types*.

### The vocabulary

| Trait | Meaning to the engine |
|---|---|
| `prerequisite_of` | the **target** must be satisfied before the **source** — reads "A requires B" |
| `unlocks` | the **source** must be satisfied before the **target** — reads "A unlocks B" |
| `containment` | the source contains the target; a hierarchy, and reachability propagates from container to contained |
| `ordering` | the edges form a sequence (`follows`, `next_race`); acyclic, and routes may be checked against it |
| `symmetric` | the edge means the same in both directions even though it is stored once (`connects_to`); traversal considers both, cycles are meaningless |
| `acyclic` | a cycle over this relation type is a design error, independently of gating |
| `annotation` | deliberately inert: this type carries no analytical weight |

`prerequisite_of` and `unlocks` are the same trait seen from two
directions, and both games and designers write both. Rather than a
separate direction flag, the direction is in the name: the engine
normalises every gating edge into one internal form, `needed →
dependent`, reversing `prerequisite_of` edges as it reads them. All
gating analyses then run over a single normalised dependency graph and
never think about direction again.

`prerequisite_of`, `unlocks`, `ordering` and `containment` each imply
`acyclic`; declaring it as well is allowed and redundant. `acyclic`
alone exists for a type that must not loop without gating anything —
a `variant_of` or `derived_from` edge.

`annotation` is not the same as declaring nothing. It is a designer
saying "I looked, and this type is decoration" — which is what stops
`is_illustrated_by` from being counted when deciding whether an entity
is an orphan. That distinction is the whole reason the trait exists.

Incoherent combinations are rejected at write time with
`invalid_schema`, naming the pair — **not** `schema_violation`. The
committed `internal/metamodel/errors.go` gives those two codes two
sentinels split by who is at fault: `invalid_schema` is a type
*declaration* that cannot stand, `schema_violation` is a *row of
values* that does not fit a declaration that can. A trait combination
arrives on `relation_types.upsert` and is part of the declaration, so
it is the first. The combinations rejected: `symmetric` with
`prerequisite_of`, `unlocks` or `ordering`; `annotation` with anything
else; `prerequisite_of` together with `unlocks` on the same type (a
type cannot gate in both directions at once — declare two types).

### Where traits are stored

A new nullable column on the existing table:

```sql
ALTER TABLE relation_types
  ADD COLUMN analysis_traits text[];

ALTER TABLE relation_types
  ADD CONSTRAINT relation_types_traits_vocab CHECK (
    analysis_traits IS NULL
    OR (cardinality(analysis_traits) > 0
        AND analysis_traits <@ ARRAY['prerequisite_of','unlocks','containment',
                                     'ordering','symmetric','acyclic','annotation']::text[])
  );
```

Rejected: a separate `relation_type_semantics` table. It would be
strictly 1:1 with `relation_types`, with no independent lifecycle, no
independent permissions and no independent history — a join and a
second write path in exchange for nothing. Traits are part of the type
declaration; they arrive on `relation_types.upsert`, they are covered
by that row's existing `version` and optimistic-concurrency check, and
they are deleted when the type is.

Rejected: jsonb. The vocabulary is closed and the database should say
so. `text[]` with a `<@` check gives a real constraint, indexes with
GIN if it ever needs to, and reads as data rather than as a blob.

`NULL` means **undeclared**. An empty array is refused by the check
constraint, so "I have nothing to say about this type" and "this type
is deliberately inert" cannot be confused — the second is
`{annotation}`.

### When a game has declared nothing

Analyses do not guess, with one narrow and explicit exception.

1. If the caller passed relation types explicitly, those are used, and
   traits are ignored. This is the escape hatch of alternative B.
2. Otherwise, relation types with non-null `analysis_traits` are used.
3. For a relation type whose `analysis_traits` is null but whose
   `semantic_role` is set, a fixed mapping applies:
   `prerequisite → {prerequisite_of}`, `unlock → {unlocks}`,
   `containment → {containment}`, `spatial → {symmetric}`,
   `availability → {unlocks}`, `reward → {annotation}`. This is not
   inference from usage — it is a translation of something the game
   already declared — and it means games seeded against the metamodel
   spec before this one get useful analyses without an edit.
4. If, after all of that, an analysis has no relation types to work
   with, it returns the error `semantics_undeclared`, carrying the
   list of the project's relation types with their current roles and
   traits, and a one-line explanation of what to declare. It does
   **not** return "no problems found". A clean bill of health from an
   engine that had nothing to read is the worst possible output.

The mapping in step 3 is reported in every result under
`semantics_source: declared | derived_from_role | caller_supplied`, per
relation type, so a designer can see that the engine treated
`available_to` as a gate because of a role they set months ago.

## 3. Analysis 1 — prerequisite cycles

**Definition.** A cycle in the normalised dependency graph built from
relation types carrying `prerequisite_of`, `unlocks` or `ordering`,
plus any type carrying `acyclic` (walked in its stored direction).
Types carrying `containment` are checked as a separate graph, because
a containment loop is a different report with a different fix.

**Inputs.**

| Field | Default |
|---|---|
| `relation_types` (keys) | traits resolution above |
| `entity_types` (keys, restrict the walk) | all |
| `max_depth` | 25, cap 100 |
| `max_results` | 100, cap 1000 |

**Result.** A list of cycles. Each cycle carries its entities in path
order (key, name, entity type), the relation ids and relation type
keys of the edges that close it, its length, and whether it is a
self-loop. Plus `truncated` and the `semantics_source` map.

Cycles are canonicalised before being returned — rotated so that the
smallest entity id comes first, and deduplicated — otherwise a
four-node cycle is discovered from four different starting points and
reported four times, which is how a useful report becomes noise.
Self-loops (A requires A) are reported as length-1 cycles rather than
being filtered out; they are always a bug and always cheap to fix.

**What a false positive looks like.** A relation type that is
genuinely cyclic but was declared as gating. The clearest real case is
mutual exclusion — "choosing the Horde locks the Alliance" modelled as
two `requires_not` edges — which is a legitimate two-cycle in a type
that superficially reads like a prerequisite. This is why every
finding names the relation type of every edge in the cycle: the first
thing a designer checks is whether the type should have been declared
gating at all. The fix is a trait declaration, not a code change.

## 4. Analysis 2 — unreachable content

Reachability is meaningless without a starting point, so the starting
set is an input, not an assumption. A designer must be able to say
"from the tutorial zone" or "at character creation".

**Inputs.**

| Field | Meaning | Default |
|---|---|---|
| `seed_entities` | entity keys, explicit start points | `[]` |
| `seed_entity_types` | every entity of these types is a start point (all `Class` at character creation) | `[]` |
| `include_ungated` | an entity with no incoming gating edge is reachable by definition | `true` |
| `gate` | `any` or `all` | `any` |
| `propagate_containment` | contained entities inherit their container's reachability | `true` |
| `ignore_entity_types` | never reported (lore notes, art references) | `[]` |
| `relation_types` | override | traits resolution |
| `max_depth` | | 25, cap 100 |
| `max_results` | | 100, cap 1000, cursor-paginated |

`include_ungated` is what makes the analysis usable on day one, before
anybody has defined a start point: everything that nothing gates is
treated as available from the beginning, which is what a game usually
means. With it off and no seeds, everything is unreachable and the
report is worthless — so an empty seed set with
`include_ungated: false` is refused rather than producing that report.

The code it is refused with is **not** `schema_violation`: that code is
defined by the committed validator as a row of values failing its type's
field schema, and there is no row and no field schema here. Nor is it
`invalid_schema`, which is about a *declaration*. This is an argument
that cannot produce a meaningful answer — a third thing. Recorded as
open question 8 in §12; the analyses need a general
argument-validation code and this spec should not invent one on its own
while the views spec is separately introducing `query_invalid`.

**`any` versus `all`.** An entity with two incoming gating edges —
does it need both prerequisites, or either? Maestro cannot know; the
metamodel has no way to express a requirement group. The default is
`any`: an entity is reachable as soon as one of its gates is
reachable.

The reason is asymmetric cost. Under `all`, a game that models
alternatives ("reachable via the Mage route or the Rogue route")
produces a flood of false "unreachable" verdicts, and a report that
cries wolf about healthy content is abandoned after the second read.
Under `any` the engine under-reports: some genuinely unreachable
content is called reachable. That is the safer error for a tool whose
value is that designers believe it. `all` remains available for games
that really do mean conjunction.

**Result.** Unreachable entities, grouped by entity type, each with
key, name, type, and a reason:

- `no_path` — gated only by entities that are themselves unreachable.
  Carries the nearest blocking entities, capped at five.
- `isolated_from_start` — no incoming gating edge at all and not a
  seed, only possible with `include_ungated: false`.
- `container_unreachable` — reachable only through a container that is
  not reachable.

Plus totals: reachable and unreachable counts overall and per entity
type, the resolved seed set as the engine understood it, and
`semantics_source`.

**What a false positive looks like.** Content reachable by a mechanism
the design never wrote down — a vendor sells the item, an NPC offers
the quest, and no relation records it. Strictly this is not a false
positive: the finding is "the design does not say how a player gets
here", which is frequently the more useful sentence. But it will be
read as a false positive, so the wording of the reason matters, and
`ignore_entity_types` plus `annotation` traits exist to let a team
silence the categories they know are handled elsewhere.

The second family is decorative entity types — `Lore`, `ConceptArt`,
`VoiceLine` — that nothing gates and that no player "reaches". Those
belong in `ignore_entity_types`, and the answer should be stored, not
retyped on every run. See open question 2.

## 5. Analysis 3 — orphans

**Definition.** An entity with no relations at all, counting every
relation type **except** those declared `annotation`. This is the
literal "nothing points at it and it points at nothing".

The metamodel's rule that a reference between entities is always a
relation and never a jsonb field is what makes this analysis
meaningful: there is no way for an entity to be quietly referenced
somewhere the walk cannot see. Orphan detection is the analysis that
would silently lie if that rule were ever relaxed, and it is worth
recording that here.

**Inputs.** `mode` (`isolated` default, `sink` for only-incoming,
`source` for only-outgoing), `entity_types`, `ignore_entity_types`,
`relation_types` override, `max_results` with a cursor.

**Result.** Entities with their in-degree and out-degree over the
counted types. No recursion is involved — this is an aggregate over
`relations` and is by far the cheapest of the four.

**What a false positive looks like.** An entity created seconds ago by
an agent that has not yet written its edges. Bulk seeding routinely
passes through a state where half the content is orphaned. This is not
worth solving with heuristics; it is worth stating in the tool
description so an agent does not panic mid-seed, and worth
remembering when the UI decides whether to show an orphan count as a
warning colour.

## 6. Analysis 4 — routes as stored objects

A route is a named, ordered walk through content, kept so that a
progression can be proved and re-proved. *Mage levelling 1-20* is a
route. *The F3 championship season* is a route.

### Schema

```sql
CREATE TABLE routes (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    key         text NOT NULL,
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    params      jsonb NOT NULL DEFAULT '{}'::jsonb,
    version     integer NOT NULL DEFAULT 1,
    last_checked_at            timestamptz,
    last_checked_design_version bigint,
    last_check  jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by_token_id uuid REFERENCES api_tokens (id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX routes_key_key ON routes (project_id, lower(key));

CREATE TABLE route_steps (
    route_id   uuid NOT NULL REFERENCES routes (id) ON DELETE CASCADE,
    position   integer NOT NULL,
    entity_id  uuid REFERENCES entities (id) ON DELETE SET NULL,
    entity_key text NOT NULL,
    note       text NOT NULL DEFAULT '',
    PRIMARY KEY (route_id, position)
);
CREATE INDEX route_steps_entity_idx ON route_steps (entity_id);
```

**The DDL above is illustrative and its foreign keys are not yet
right.** `route_steps.entity_id` and both `updated_by_token_id` columns
reference a **project-scoped** parent by id alone. The committed
metamodel migration settled that every such key is composite, carrying
`project_id` alongside the parent id, so the database itself refuses a
cross-game reference; `route_steps` therefore also needs its own
`project_id`. Concretely: `(entity_id, project_id) REFERENCES entities
(id, project_id) ON DELETE SET NULL (entity_id)` — the column list is
required, since a bare `SET NULL` would try to null `project_id` too —
and `(updated_by_token_id, project_id) REFERENCES api_tokens (id,
project_id) ON DELETE SET NULL (updated_by_token_id)`. `routes` needs a
`UNIQUE (id, project_id)` for `route_steps` to reference.
`updated_by_user_id` stays single-column: `users` is global. See
`2026-08-31-core-and-metamodel-design.md`, "Constraints and indexes";
the views and markdown specs state their own DDL in the same
uncorrected shape.

Steps are rows, not a jsonb array on the route. The reason is
deletion. With jsonb, deleting an entity leaves a dangling key that
nothing in the database knows about and nothing forces anyone to
re-check. With a real foreign key there is a choice, and the choice
matters: `ON DELETE CASCADE` would silently shrink the route, which is
the failure this section exists to prevent; `ON DELETE RESTRICT` would
block a legitimate deletion because some old route mentions the
entity, making routes a liability. `ON DELETE SET NULL` with
`entity_key` retained as a tombstone is the honest one — the step
survives, visibly, as *step 4: `hogger-hunt` (deleted)*, and the next
check reports `missing_entity`.

`params` holds the reachability parameters the route is checked under
(`gate`, `include_ungated`, extra seeds), so a route carries its own
definition of what "holds together" means and two people checking it
get the same verdict.

### Checking a route against the current design

`routes.check` walks consecutive steps. For each pair *(n, n+1)* it
asks whether step *n+1* is reachable given the seed set plus every
step up to *n* — the same walk as analysis 2, with the route prefix as
seeds. Per-step verdicts:

- `ok`
- `missing_entity` — the step's entity was deleted
- `unmet_prerequisite` — reachable set does not include it; names the
  blocking entities
- `out_of_order` — the step violates an `ordering` relation type that
  places it after a later step

The result is written back to `routes.last_check` with
`last_checked_at`. Storing it is deliberate: unlike the other three
analyses, a route's verdict is a statement about a named artefact that
a human wants to see in a list without running anything.

### When the design changes underneath a saved route

A stored verdict that quietly becomes wrong is worse than no verdict,
so staleness has to be detectable without re-running anything.

**Chosen:** a monotonic `design_version bigint NOT NULL DEFAULT 0` on
`projects`, bumped once per transaction by any write to
`entity_types`, `relation_types`, `entities` or `relations`. A route
whose `last_checked_design_version` is less than the project's current
value is **stale**, and stale is rendered and reported as its own
state — never as green, never as red. "Not checked against the current
design" is a different fact from "broken", and collapsing the two
would either alarm people wrongly or reassure them wrongly.

Rejected: re-checking every route on every write. Seeding a game is
hundreds of writes; the cost is quadratic in exactly the situation
that matters and the SSE stream would carry a storm of verdict
changes that are all provisional anyway.

Rejected: comparing `routes.last_checked_at` against
`max(entities.updated_at)`. It costs a scan per check, and it misses
deletions entirely — the one change most likely to break a route.

The counter is coarse: any write anywhere in the game marks every
route stale, including a typo fix in a description. That is accepted.
A precise dependency set per route would have to be maintained on
every write and would be wrong the moment a *new* relation makes a
previously irrelevant entity relevant. Coarse and correct beats
precise and subtly wrong, and re-checking is cheap.

## 7. How it computes

Recursive CTEs, as the project invariants require. Graph walks happen
in SQL, not by pulling the graph into Go.

The working case is four hundred entities with dense edges — a few
thousand relation rows. That is not a stress case; a recursive walk
over the existing `relations_source_idx` and `relations_target_idx`
resolves it in milliseconds. The bounds below exist to keep a
pathological design (or a malicious one) from turning an analysis into
an outage, not because the normal case is close to any of them.

**Every walk is bounded, all four of these, always:**

1. **Depth.** The recursive term carries a `depth` column and stops at
   `max_depth` (default 25, cap 100). A result that hit the ceiling
   carries `depth_limited: true`.
2. **Cycle guard.** Every recursive CTE carries the visited path as a
   `uuid[]` and refuses to revisit (`NOT next_id = ANY(path)`). This
   is mandatory even in the reachability walk, which is *expected* to
   run over cyclic graphs — a design with a prerequisite cycle is
   precisely the design someone will run the unreachability analysis
   on, and without the guard that walk does not terminate.
3. **Statement timeout.** `SET LOCAL statement_timeout` on the
   analysis transaction, default 5s, configurable, cap 30s. A timeout
   returns the stable error `analysis_timeout` carrying the parameters
   that would narrow the run (lower `max_depth`, an `entity_types`
   filter), not a 500.
4. **Result cap.** `max_results` with `truncated: true` when it bites;
   cursor pagination for `unreachable` and `orphans`, whose results
   are flat lists. Cycles are capped hard rather than paginated — a
   design with more than a thousand distinct prerequisite cycles has
   one problem, not a thousand, and paging through them helps nobody.

**Caching: none, for the three read-only analyses.** Computed on
demand, every time. Two reasons. The walk is milliseconds at the
working size, so a cache buys nothing measurable. And the parameter
space — seeds, gate mode, containment propagation, type filters,
relation type overrides — means near-every call is a distinct key, so
the hit rate would be poor even if the cost were high. Against that, a
stale cache is a correctness failure in the one tool whose entire
value is telling the truth about the design.

Rejected: materialised views. They cannot be parameterised by seed
set, and refreshing them would have to hook every metamodel write.

The single exception is a route's `last_check`, cached because it is a
persisted verdict on a named artefact, and paired with
`last_checked_design_version` so it can state its own staleness. That
is the pattern: nothing is cached unless it can say when it went out
of date.

**One index is added**, because every analysis query filters on exactly
this pair:

```sql
CREATE INDEX relations_project_type_idx ON relations (project_id, relation_type_id);
```

The justification originally given here — that `relations` has no index
leading with `project_id` — was false. The committed migration carries
`relations_project_idx (project_id, created_at)`, added by the metamodel
plan's Task 1, correction 6, for the unfiltered "show me this game's
edges" listing. It leads with the right column and the wrong second one,
so it does not serve a relation-type-filtered walk; the index above is
still worth adding, on that narrower ground. Before adding it, check it
against the second index
`2026-09-02-views-and-query-language-design.md` §8 asks for,
`relations (relation_type_id, target_id)`: `relations` would then carry
six indexes on the table expected to hold the most rows, and each one is
write cost on every seeded edge.

**One shared implementation.** The normalised-dependency walk and the
reachability closure live in `internal/analysis` as parameterised
queries, not inlined per tool. The views sub-project needs the same
reachability filter ("quests a Mage can reach") and must call this
component rather than writing a second walk that drifts from it.

## 8. Isolation

The invariant the whole Core sub-project was built to defend: every
query takes the **resolved** project id and filters in SQL, never in
Go — `WHERE project_id = $1 AND id = $2`, never by id alone. A
globally unique id does not make a cross-game read acceptable.

Recursive walks are where this is easiest to lose, in three specific
ways, and each has a rule:

1. **The project filter must appear in the recursive term, not only in
   the anchor.** An anchor-only filter seeds correctly and then lets
   the walk leave the project through any edge whose far side lives
   elsewhere. Today both relation endpoints carry `project_id` so such
   an edge should not exist — but the isolation invariant must not
   rest on a different table's data being correct. Both terms filter.

2. **Seeds are resolved by key through a project-filtered lookup.**
   `seed_entities` arrives as keys, never as ids, and resolution is
   `WHERE project_id = $1 AND entity_type_id = $2 AND lower(key) =
   lower($3)`. A key that does not resolve is `not_found` naming the
   key — never dropped silently, because a silently empty seed set
   turns "you gave me a bad key" into "your entire game is
   unreachable".

3. **Relation type overrides and route ids are project-filtered
   lookups too.** `routes.check` reads
   `WHERE project_id = $1 AND id = $2`. A `relation_types` override
   naming a key from another game is `not_found`, not an empty filter.

Mandatory tests, in the spirit of the metamodel plan's Task 7
preamble — these fail silently otherwise:

- Every analysis run with game A's token against `project_id` of game
  B, including with an admin's token → `scope_violation`.
- A seed key that exists only in game B → `not_found`, not an empty
  seed set.
- A relation row planted directly in the database whose endpoints
  belong to two different projects is never traversed by a walk
  started in either.
- `routes.get` and `routes.check` on a route id belonging to another
  game → `not_found`.

## 9. MCP and REST surface

Agents will run these analyses far more often than humans will, so the
MCP surface is the primary one and follows the conventions the core
spec set: idempotent upserts keyed by `(project, key)`, slim responses,
opaque cursors, stable machine-readable errors.

**New MCP tools (`maestro.*`)**

Analyses — `analysis.cycles`, `analysis.unreachable`,
`analysis.orphans`.

Routes — `routes.list`, `routes.get`, `routes.upsert`,
`routes.remove`, `routes.check`.

**Extended** — `relation_types.upsert` and `relation_types.get` gain
`analysis_traits`. This is the only change to an existing tool.

`routes.upsert` addresses by `(project, key)` and takes the full step
list, replacing it; steps are positional and rewriting them wholesale
is simpler for an agent than diffing, and a route is small.
`expected_version` applies as everywhere else.

**Error shapes.** Reuses `not_found`, `scope_violation`,
`version_conflict` and `invalid_schema` — the last for an incoherent
`analysis_traits` combination on `relation_types.upsert`, which is a
declaration failing its own rules. It does **not** reuse
`schema_violation`: nothing in this sub-project validates a row of
values against a field schema. See §4 and open question 8 for the
argument-validation code the analyses still lack. Adds two:

- `semantics_undeclared` — carries the project's relation types with
  their roles and traits, and what to declare.
- `analysis_timeout` — carries the parameters that would narrow the
  run.

Truncation and depth limiting are **flags on a successful result**,
not errors: the partial answer is useful and the caller must be able
to see both the findings and the fact that there may be more.

**REST.** `/api/g/<slug>/analysis/{cycles,unreachable,orphans}` and
`/api/g/<slug>/routes[/<key>[/check]]`, mirroring the above for the
UI. Not promised stable.

**SSE.** `route.*` events on create, update, delete and check.
Analyses are not streamed — they are pull, not push. Staleness needs
no event of its own: the project's `design_version` rides on the
events the UI already receives, and the client compares it against
each route's `last_checked_design_version` locally. A `route.stale`
event per write would be a storm during seeding and would carry no
information the client cannot derive.

## 10. Relationship to views

A view is a saved query plus a layout, designed in its own spec,
concurrently. The seam between the two surfaces:

**An analysis returns a set with annotations. A view turns a set into
a picture.** An analysis result is entity keys, reasons, and edge
references — no coordinates, no renderer, no colours, no layout. That
is what makes it usable by an agent over MCP, where a picture is
worthless.

**Where an analysis becomes something you can look at:** a view may
name an analysis as its source — conceptually
`source: {analysis: "unreachable", params: {…}}` — and render the
resulting entity set with whatever renderer it chooses. That is the
single point of contact, and it belongs to the views spec to define
precisely.

**Where the two stay separate.** The analysis engine never grows
layout parameters, and the view query language never grows cycle
detection. The shared piece is lower down: the reachability closure in
`internal/analysis` (section 7), which the D2 query language calls for
its own "reachable from" filter rather than reimplementing. One walk,
two callers, no drift.

## 11. Schema changes requested

Small, and much cheaper now than later. Four additions and one
documentation correction:

1. `relation_types.analysis_traits text[]` nullable, with the
   vocabulary check constraint. **Why:** section 2 — this is the
   entire mechanism by which a game tells a genre-agnostic engine what
   its edges mean.
2. `projects.design_version bigint NOT NULL DEFAULT 0`, bumped in the
   same transaction as any write to the four metamodel tables.
   **Why:** section 6 — the only cheap, correct staleness signal for
   stored route verdicts. Also useful to the UI and to the views
   sub-project for cache invalidation.
3. New tables `routes` and `route_steps` (section 6).
4. Index `relations (project_id, relation_type_id)`. **Why:** every
   analysis filters on exactly that pair, and the existing
   `relations_project_idx (project_id, created_at)` leads with the right
   column and the wrong second one. See §7 for the correction and for
   the interaction with the index the views spec asks for.
5. Not a schema change but a correction to the core spec's wording:
   `semantic_role` is described there as the mechanism the analysis
   sub-project will use. This spec argues in §2C that it is not
   sufficient. **That amendment has been made**: the core spec's
   "Metamodel" section now records it as **open question O1** — traits
   plus a demoted `semantic_role`, traits with `semantic_role` dropped,
   or no traits — rather than asserting either answer. The user decides;
   until then, §2D's vocabulary is a proposal and §11.1 is contingent on
   it. §12.3 no longer poses the question separately.

Every table added by items 2 and 3 follows the committed migration's
composite foreign-key convention — see §6 and
`2026-08-31-core-and-metamodel-design.md`, "Constraints and indexes".
Item 2 in particular is a change to a **Core** table, `projects`, and a
new obligation on every write path in the metamodel sub-project (Tasks
3–6 of `docs/superpowers/plans/2026-08-31-metamodel.md`): the counter has to be bumped in the same transaction as
the write, which is a place a later query can silently forget. That
cost belongs on the record beside the benefit.

## 12. Open questions

Stated plainly rather than answered with false confidence.

1. **`any` versus `all` gating, and where the answer belongs.** This
   spec picks `any` as a per-analysis default and argues the
   asymmetry, but the real answer is probably per relation type, or
   per requirement *group* — "any of these three licences, plus this
   car". Groups would live naturally in the edge's own field schema
   (`requires_group: "licences"`), which the metamodel already
   supports and which v1 of this engine deliberately does not read.
   Deciding this later is cheap; deciding it wrong now and encoding it
   in the trait vocabulary is not.

2. **Should seed sets be stored objects?** Right now
   `seed_entities` / `seed_entity_types` / `ignore_entity_types` are
   retyped on every call, which means two people analysing the same
   game get different answers for reasons neither can see. Candidates:
   a small `start_sets` table; reusing a route's steps as a seed set
   (natural — a route is an ordered start set); or letting a saved
   view carry analysis parameters. Leaning toward routes-as-seeds
   because it adds nothing, but not decided.

3. **Does `semantic_role` survive, and are traits added at all?**
   Moved. This question is now stated once, as **open question O1** in
   `2026-08-31-core-and-metamodel-design.md`, "Metamodel", because
   `semantic_role` is that spec's column and because
   `2026-09-02-agent-skill-bundle-design.md` §11.3 was posing the same
   question a third time. The substance is unchanged and it is still the
   user's call, still cheaper before any real game is seeded; this spec
   states its recommendation in §2D and consumes whichever answer comes
   back. **Do not answer it here.**

4. **Can a route step be a relation rather than an entity?** "Take the
   portal to Darnassus" is an edge, not a node. Modelling it as an
   entity step loses the traversal; modelling steps as either kind
   complicates the table and the check. Deferred until a real route
   needs it.

5. **Should an analysis be scopable to an arbitrary subset?** This
   spec offers `entity_types` and `relation_types` filters only. A
   full "run this analysis over the result of this D2 query" waits for
   the view query language, and should probably reuse it rather than
   inventing a second selector.

6. **Should reachability read entity fields?** A quest with
   `min_level: 40` in a design where nothing grants level 40 is
   unreachable in the way a designer means it, and invisible to a
   pure graph walk. Reading fields would mean Maestro understanding
   numeric progression, which it deliberately does not. Proposed
   answer: no, in this spec — but it will be asked, and the
   alternative (a declared "progression field" per entity type) is
   not absurd.

7. **Is the trait vocabulary complete?** The one obvious gap is mutual
   exclusion — faction choices, class-locked content, branching
   narrative where taking one path forecloses another. It is a real
   analytical semantic (it produces legitimate two-cycles, and it
   makes some content unreachable *for a given start*), and it is not
   expressible today. Adding a trait later is a migration and a code
   path; adding it now without a concrete game demanding it risks
   guessing its shape wrong.

8. **What error does a nonsensical analysis *argument* return?** §4
   originally spelled the empty-seed-set refusal `schema_violation`,
   which the committed sentinels reserve for a row of values failing a
   field schema; `invalid_schema` is reserved for a bad declaration.
   Neither fits an argument that cannot produce a meaningful answer.
   `2026-09-02-views-and-query-language-design.md` is separately adding
   `query_invalid`, carrying a JSON pointer into the offending document,
   for the same class of problem in a different surface. Either the
   analyses reuse that code, or the core error set gains a general
   `invalid_argument`. One of the two, decided once, rather than a third
   code per sub-project.

9. **Does the seam with views belong here or there?** §10 assigns two
   things to the views spec that it does not define — a view whose
   source is an analysis, and which package owns the single reachability
   walk. Recorded there as its open question 8. Whichever sub-project is
   planned first has to settle both.
