# Maestro — core and metamodel (design)

Date: 2026-08-31
Status: approved, ready for implementation planning
Scope: first of seven sub-projects (see "Roadmap" at the end)

> **Reconciled against implementation, 2026-09-02.** This approved spec
> was written before the metamodel was built; the following statements
> were corrected against `internal/db/migrations/0004_metamodel.sql`,
> `internal/metamodel/` and the corrections blocks of
> `docs/superpowers/plans/2026-08-31-metamodel.md`, and three open
> questions raised by the 2026-09-02 sub-project specs are recorded here
> rather than being answered in two places:
>
> - "Field schemas" — the list type is `list<text>`, not `list<…>`; the
>   field-key rule and the required-versus-default rule are stated.
> - "Field schemas" — **O3**: a relation cannot carry a typed reference
>   to a third entity.
> - "Metamodel" — **O1**: whether `semantic_role` is the analysis
>   mechanism, or descriptive metadata beside a separate
>   `analysis_traits` vocabulary.
> - "Idempotency" — **O2**: re-running a seed over rows that already
>   exist is not conflict-free; the old wording read as though it were.
> - "Error shapes" and "Field validation" — `invalid_schema` exists as a
>   seventh code, distinct from `schema_violation`.
>
> Nothing else in the spec changed.

> **Decided 2026-09-02.** O1, O2 and O3 are answered; none of them is
> open any more. In short:
>
> - **O1 — both axes exist.** `semantic_role` stays as it shipped and
>   says what a relation *means*; the analysis sub-project adds
>   `analysis_traits`, which says how it *behaves* in a graph walk. See
>   "Metamodel" below.
> - **O2 — bulk upserts gain `on_conflict`**, decided but **not yet
>   implemented**. See "Idempotency" below.
> - **O3 — no reference field type.** A pointer to another entity is a
>   relation; an edge field naming an entity is a soft reference that
>   nothing validates. See "Field schemas" below.
>
> The other statement the sub-project specs asked about, **one key rule
> or two**, was already settled in code and stays settled: two rules,
> for the reason "Field schemas" gives.

## 1. What Maestro is

Maestro is an open-source, self-hosted tool where **game designers and
their AI agents co-design the content of a video game**: characters,
places, missions, progression and narrative.

It is not a project tracker. There is no backlog, no kanban, no gantt,
no sprints. Development work on Maestro itself is tracked in
[Nottario](https://github.com/neverbot/nottario). What Maestro stores
is the *game design*: everything a player can be, go to, do and unlock.

It never stores runtime data. No live game instances, no real players,
no telemetry. Every row is a design-time artefact.

### The metamodel, and why it is pure

Maestro ships **no built-in game concepts**. There is no `Quest` table,
no `Zone` table, no `Character` table. Each game declares its own
vocabulary out of four primitives:

| Primitive | Meaning |
|---|---|
| `EntityType` | a kind of thing, with a field schema |
| `Entity` | an instance of an entity type |
| `RelationType` | a kind of directed edge, with its own field schema |
| `Relation` | an instance of a relation type between two entities |

An MMORPG declares `Class`, `Race`, `Profession`, `Zone`, `Dungeon`,
`Quest`, `Talent`, `Faction`, and relation types `connects_to`,
`takes_place_in`, `requires`, `available_to`, `rewards`.

A racing career game declares `Driver`, `Car`, `Upgrade`, `Circuit`,
`Race`, `Championship`, `Licence`, and relation types `unlocks`,
`requires`, `contains`, `takes_place_in`.

A metroidvania declares `Room`, `Ability`, `Boss`, `Item`, with
`connects_to` edges carrying their own field (`requires_ability`).

Maestro understands none of those words. It understands typed entities
and typed directed edges. Genericity is the product: the same schema
must serve all three equally well, and a genre Maestro has never seen
must fit without a code change.

Agents are not expected to infer the metamodel. Maestro ships a skill
bundle teaching agents how to declare types and populate content, with
worked examples per genre (separate sub-project).

### Decisions already taken (context for this spec)

These were settled during design and constrain everything below:

1. **Pure metamodel** over fixed domain entities or a fixed-archetype
   hybrid. Rejected alternatives: hard-coded `Actor`/`Place`/
   `Objective` entities; archetypes with game-declared subtypes.
2. **Design plus saved routes**, never player state. A "route" is a
   named ordered walk through content ("Mage levelling 1-20") used to
   validate the design. It is a design artefact, not a save file.
3. **Views are queries plus layout** ("D2"). A view is a first-class
   record an agent can create over MCP: it selects entities, traverses
   relations, derives edges, and picks a layout from a fixed renderer
   catalogue. Rejected: fixed hard-wired view tabs (cannot express
   "quests a Mage can reach"); agent-supplied render modules (arbitrary
   code in every teammate's browser for a handful of genre-specific
   diagram shapes). Views are their own sub-project; this spec only
   makes sure the data model can serve them.
4. **Long prose lives in a versioned markdown store** linked to
   entities, not in entity fields. Branching dialogue stays entities +
   relations, because it is a graph. Own sub-project.
5. **Local accounts only** for humans in v1 — no GitHub OAuth, no
   email delivery. Game designers are not developers and an instance
   must be usable without any external account or SMTP server.

## 2. Architecture

One Go binary, `maestro`, serving four surfaces from one process,
backed by Postgres.

```
  browser (human)      MCP agent        REST client
        |                  |                 |
  +-----+------------------+-----------------+-----+
  |  HTTP:  /  ·  /api  ·  /mcp  ·  /events        |
  +------------------------+------------------------+
  |  domain: projects · identity · metamodel · search|
  +------------------------+------------------------+
  |  persistence: sqlc over pgx/v5                   |
  +------------------------+------------------------+
                           |
                     PostgreSQL
```

- `/` — web UI, embedded assets, Lit + vanilla CSS, no build step.
  Minimal in this spec: login, invite redemption, game list/home,
  content counters, token and member management. No views yet.
- `/api` — REST JSON for that UI. Not a stable public API.
- `/mcp` — MCP over HTTP+SSE, per-project bearer tokens. The
  first-class surface: most content arrives here.
- `/events` — SSE, fed by Postgres `LISTEN/NOTIFY`, so a human watches
  content appear while an agent seeds it.
- `/healthz`, `/version` — operational endpoints.

### Inherited from Nottario without further discussion

Validated in production, and Maestro deploys the same way: Go with
`net/http` and no framework; pgx/v5 with **all** SQL in
`internal/db/queries/*.sql` through sqlc, generated code committed;
embedded migrations; embedded frontend assets; SSE plus
`LISTEN/NOTIFY` for real-time; per-project bearer tokens for agents
(one token = one project, admins included); distroless container
published by CI; in-process scheduled `pg_dump` backups.

### Deliberately not inherited

Tasks, cycles, priorities, dependencies, team roles, notifications,
kanban, gantt — the entire work-tracking domain. And the visual
identity: Nottario is deliberately GitHub-like; Maestro gets its own,
designed separately.

### Multi-game, single-game ergonomics

One instance hosts several games; the author of this design will run at
least two. A token scoped to one game sees nothing of another.

URLs always carry the game slug (`/g/<slug>/entities/quest`) so links
pasted in a chat stay valid once a second game exists. Navigation
adapts instead: on `/`, the server counts the games **this user** can
see. Exactly one — redirect to its home, hide the game switcher. More
than one — show the picker, and remember the last visited game so a
user who has been going straight in for months lands where they expect
when a second game appears.

## 3. Data model

All domain tables carry `project_id` (a project is a game).

### Identity and access

- `users` — email, `display_name`, argon2id hash, `is_admin`.
- `sessions` — httpOnly cookie, expiry, invalidated on password change.
- `invites` — hashed token, target project and role, expiry, creator,
  redemption timestamp.
- `projects` — slug, name.
- `memberships` — user × project × role (`owner` / `editor` /
  `viewer`).
- `api_tokens` — hashed value, project, creator, last use.

### Metamodel

**`entity_types`** — `key` (`"quest"`), singular and plural labels
(`"Quest"` / `"Quests"`), description, colour/icon, `field_schema`
(jsonb), `version`.

The descriptive columns are validated in shape and never in content —
they are the game's own prose — by `internal/metamodel/descriptors.go`,
which every table in this section shares, entities included:
`label` is **required** and capped at 200 characters, `label_plural` at
200, `description` at 4000, all counted in runes rather than bytes;
`color`, when set, is a CSS hex colour and nothing else (`#c13`,
`#c13f`, `#c41e3a`, `#c41e3a80`); `icon`, when set, is a lower-case icon
*name* matching `^[a-z0-9][a-z0-9_-]*$`, capped at 64. `label` is
required because listings order by it. `color` refuses named colours and
`rgb()`/`hsl()` because Maestro's renderers derive contrasting tones
arithmetically from the channels and a form they cannot decompose is a
colour some views honour and others drop — all four hex forms decompose,
alpha included. `icon` is a name and not an image, markup or an emoji;
the pending visual-identity spec owns the emoji question and the choice
of icon set, and the rule admits both `scroll-2` and
`local_fire_department` so as not to pre-commit it. Every one of these
problems is an `invalid_input` at the column's own path, reported
together with any key problem in one pass.

**`entities`** — `entity_type_id`, stable `key`, `name`, `fields`
(jsonb, validated against the type's schema), `version`.

**`relation_types`** — `key` (`"takes_place_in"`), label, allowed
source and target entity types (lists of `entity_type_id`; empty means
any), cardinality, own `field_schema` (edges carry data: "requires
Mothwing Cloak"), optional `semantic_role`, `version`.

**`relations`** — `relation_type_id`, source entity, target entity,
`fields` (jsonb, validated against the relation type's schema),
`invalid`, `version`.

`semantic_role` is one of `prerequisite`, `unlock`, `containment`,
`spatial`, `availability`, `reward`, or null, enforced by a `CHECK` on
the column. It is **not** used for drawing — views select relation types
explicitly. It records what an edge *means* to a designer, in a form a
human reads on a type page and an agent can be taught.

It is not the analysis mechanism. The questions a designer cannot answer
by eye across 400 missions — "is this quest unreachable?", "is there a
prerequisite cycle?" — are answered from a second, orthogonal column,
`analysis_traits`, which the analysis sub-project adds and owns; see the
decision below.

> **Decided 2026-09-02 — O1: `semantic_role` stays, and
> `analysis_traits` is added beside it.** Outcome (a): the two are
> orthogonal axes and both exist. `semantic_role` says what a relation
> *means* to a designer (`reward`, `availability`); the analysis
> sub-project adds `relation_types.analysis_traits text[]`, which says
> how a relation *behaves* in a graph walk (acyclic, symmetric,
> direction of dependency). Neither replaces the other.
>
> The argument in `2026-09-02-analysis-engine-design.md` §2 is accepted,
> and it is precisely the reason there are two columns rather than one:
> a single axis conflates meaning with behaviour, a closed enum of
> meanings must grow every time a genre invents a meaning, and it still
> cannot say whether a cycle in a given type is a bug. What follows from
> accepting that is that behaviour needs its own vocabulary — not that
> meaning stops being worth recording.
>
> `semantic_role` does not move. It is already shipped: the column, its
> `CHECK`, `metamodel.SemanticRoles` and the wire format all exist and
> are validated. Dropping it would be a migration and a wire change
> bought for nothing, since a human-readable label is exactly what it is
> good at.
>
> What this settles downstream: `semantic_role` is descriptive metadata
> with **no analytical meaning**; analysis reads `analysis_traits` and
> nothing else. The agent bundle therefore teaches traits for analysis
> and the role as a label, and never as two ways to say one thing.
> `analysis_traits` itself is not implemented — it belongs to the
> analysis sub-project, which owns its vocabulary and its validation.

### Field schemas

A `field_schema` is a declarative list of
`{key, label, type, required, default}` with exactly six types: `text`,
`longtext`, `number` (optional `min`/`max`), `bool`, `enum` (with
options), and `list<text>`. `list<text>` is the **only** list type —
there is no `list<number>` and no `list<enum>`.

Three rules the validator enforces at declaration time:

- A field `key` must match `^[a-z][a-z0-9_]*$` and be at most 64
  characters. Lower snake case with no dots, so `fields.<key>` stays an
  unambiguous error path and two keys can never differ only by case.
  This rule applies to declared **field** keys only. The keys that
  *address rows* — entity-type keys, relation-type keys and entity keys
  — have their own rule, `^[A-Za-z0-9][A-Za-z0-9_-]*$`, also capped at
  64 characters: hyphens and capitals are allowed, and a key may start
  with a digit.

  **The two rules differ because the mechanism behind them differs, and
  this is settled, not open.** A field key lives inside a jsonb object,
  which has no case folding: `Level` and `level` would be two distinct
  keys in one row and nothing downstream would ever catch the collision,
  so the rule has to forbid case itself. A row key cannot produce that
  collision at all — every uniqueness index over these keys is
  `UNIQUE (project_id, lower(key))`, so the database folds case for
  them. Both rules therefore deliver the same guarantee, "no two keys
  differ only by case", through the mechanism each context actually has,
  and forbidding capitals in row keys would buy nothing while costing
  what the folding index was chosen for: a game whose own vocabulary
  capitalises its handles (`Hogger`, `Elwynn_Forest`, `GP_Monaco`) spells
  them the way its design documents do, and `1999_season` or
  `500_miles` are ordinary keys for a racing game.

  A write whose key matches a stored key only case-insensitively is
  **refused**, naming both spellings, rather than silently updating the
  row under the other spelling or surfacing a raw unique violation.
  Maestro never folds or rewrites a key on the designer's behalf: the
  spelling first stored is the one that stands. That refusal, and every
  other problem with a key's spelling, arrives as `invalid_input` at
  path `key` — see "Error shapes".
- A default is declared by the **presence of the `"default"` key** in
  the JSON and by nothing else. There is no `has_default` input key,
  and an explicit `"default": null` declares no default. A declared
  default of `false` or `0` is an ordinary default and is applied.
- `required` together with a default is **rejected**. The default is
  applied before the field could ever be reported missing, so the pair
  makes `required` unreachable.

There is deliberately **no reference type**. A pointer to another
entity *is a relation*. That single rule keeps the graph complete and
is what makes view queries work at all — a reference hidden inside a
jsonb field would be invisible to every traversal.

> **Decided 2026-09-02 — O3: there will be no reference field type, and
> an edge field naming an entity is a soft reference.** The first option
> is taken: leave it. A pointer to another entity *is* a relation, and
> that stays the whole of the rule. No `entity_ref` type is added.
>
> The case the question raised is real and is now stated as a known
> property rather than as a gap. The rule above is stated for
> *entities*, and it holds there: an entity pointing at an entity is an
> edge. It does not extend to the case where the thing doing the
> pointing is itself an edge. A metroidvania door is a `connects_to`
> relation from room to room whose gate is "requires the Mothwing
> Cloak" — a reference to a third entity, the `Ability`. A relation's
> own fields are scalars of the six types above, so the only available
> spelling is a `text` field holding an ability key; a relation cannot
> itself be an endpoint of another relation.
>
> **That text field is a soft reference: a convention the game keeps,
> not a link the server checks.** Nothing validates that the key names
> an existing entity, no traversal follows it, and no view resolves it —
> `2026-09-02-views-and-query-language-design.md` §3.3's `map` view
> renders a dangling key exactly as convincingly as a real one. Naming
> that honestly is the decision; an `entity_ref` type validated on write
> but deliberately not traversable would have been a second, weaker kind
> of reference sitting beside the real one, which is the ambiguity this
> metamodel exists to avoid. A game that needs the gate to be a first
> class, traversable thing models it as its own entity with two
> relations, which the metamodel already supports.
>
> Reporting dangling reference keys is left to the analysis sub-project,
> as an optional check a game opts into, and not to the write path.
>
> `readme.md` has been corrected as part of this decision: its genre
> table used to present the metroidvania door's `requires_ability` as
> the example justifying typed edges, which read as a stronger promise
> than the design makes.

### Constraints and indexes

`key` unique per project on types; unique per (project, type) on
entities — enforced by a unique index, not a pre-flight check, so two
concurrent agents cannot slip between a `SELECT` and an `INSERT`.
Relation endpoints are validated against the relation type's allowed
lists on write, and every id in those lists must name an entity type of
the same game — the columns are plain `uuid[]`, so an id from elsewhere
would declare a rule no write could ever satisfy.

**One edge per (relation type, source, target)**, by unique index. That
index is what an upsert conflicts on, and it is what makes re-seeding a
game idempotent instead of laying a second copy of every edge beside the
first. The cost is deliberate: a game cannot hold two edges of one
relation type between one ordered pair of entities. A game that needs
two says so either as a second relation type (`connects_to` and
`connects_to_secretly`) or in the edge's own fields, which is what edge
fields are for — one `connects_to` carrying `passages: ["door", "vent"]`
rather than two identical edges nothing tells apart. **Self-loops are
allowed**: source and target may be the same entity. A quest that
requires itself is a design mistake the analysis sub-project reports, not
a write to refuse. `tsvector` + GIN over names and text fields for
cross-cutting search. `entities.search` is a plain `tsvector` column
written by the application, not a `GENERATED … STORED` column.

**Isolation is enforced in SQL, not in Go**, and the committed
migration settled the mechanism: every foreign key from a domain table
to a **project-scoped** parent is composite, carrying `project_id`
alongside the parent id — `(entity_type_id, project_id) → entity_types
(id, project_id)`, and the same shape for a relation's type and both
endpoints and for `updated_by_token_id → api_tokens`. A composite key
needs a `UNIQUE (id, project_id)` on the parent to reference, which is
why the types and entities carry one. A composite `ON DELETE SET NULL`
must name its column, since a bare one would try to null `project_id`.
Only `project_id → projects (id)` and `updated_by_user_id → users (id)`
are single-column, because neither parent has an outer scope.

**Every later sub-project's tables inherit this rule.** A new table that
references `entities`, `entity_types`, `relation_types` or `api_tokens`
by id alone reopens exactly the cross-game hole the metamodel migration
closed, regardless of whether the table also carries `project_id` of its
own.

### Deletion

Deleting an entity type that still has entities is refused without an
explicit `cascade: true`. Deleting a relation type with live relations,
likewise. Deleting an entity deletes its relations.

### Schema evolution

When a type's `field_schema` changes — a new required field, say —
existing rows are neither rejected nor back-filled with invented
values. They are flagged as invalid so a human or agent repairs them
deliberately.

**The rule is the same for entities and for relations**, because both
carry a `field_schema` and both have their `fields` judged against it by
the same validator. Editing an `entity_type`'s schema re-checks that
type's entities; editing a `relation_type`'s re-checks that type's
edges. Either listing filters for the flagged rows (`invalid: true` on
`entities.list` and on `relations.list`), the game summary counts them
per type and in its totals, and the view query language excludes them
from a picture unless `include_invalid` is set — on edges as well as on
nodes, so a view never draws a relationship whose own fields the game no
longer states.

Rewriting a flagged row with values that fit clears the flag, and so
does widening the schema back: the sweep clears as readily as it sets,
or a designer's list of things to fix would never empty. The sweep
writes nothing but the flag — no values, no `version`, and no
`updated_at` on a row whose verdict has not changed — because a
validation pass is a verdict about content, not an edit of it.

*(Edges gained `invalid` in migration 0009; before it, editing a
relation type's schema left every existing edge silently unjudged.)*

**Repairing the flagged rows is one call, and it is the other half of
this rule rather than a softening of it.** Metamodel 15 added
`entities.repair` and `relations.repair`. Task 9 measured what was
missing: adding a required field under two hundred rows flags all two
hundred, correctly, and then nothing writable into the *type* makes them
fit again — a field cannot be both `required` and defaulted, and the
schema checker is right to refuse that pair — so the only recovery was
rewriting two hundred rows one at a time, and taking the field back out
flagged all two hundred a second time because the value they now carried
had become an unknown field.

A repair pass names a type and states one of two operations: `set`,
which writes the values the caller names into every flagged row, and
`drop_unknown`, which removes the values the type no longer declares.
It is deliberately narrow, and each restriction is enforced by a
mechanism rather than promised:

- It reads **only rows the current schema rejects**, so it cannot be
  used to rewrite content that fits.
- It writes **only `fields`** — never a key, a name or an endpoint —
  because it hands the stored row's own address back to the ordinary
  write path.
- It clears the flag **only by re-validating**. There is no "mark these
  valid" argument and nowhere to put one.
- It **never runs as a side effect of a schema edit**. A schema edit
  still flags and never back-fills; a repair is a separate call a
  designer makes afterwards, with values a designer chose.
- A pass stating neither operation is **refused**, because rewriting
  every flagged row with the values it already holds would either do
  nothing or quietly back-fill a declared default.

It is not a new power: it does exactly what a listing plus a batch of
upserts could already do, with one decision instead of two hundred round
trips. That is what keeps it from becoming a back-fill by the back door.
A pass is bounded and has no cursor — a repaired row leaves the
selection, so a caller loops until a pass repairs nothing, and `failed`
then names every row its values could not fix.

### Concurrency

Every row of all four tables — entity types, entities, relation types
and relations — carries an integer `version`; every update passes
`expected_version`. A conflict returns `version_conflict` **with the
current version**, so the caller re-reads, merges and retries. This is
Nottario's optimistic-concurrency pattern applied to the whole domain,
and it matters here because several agents seed content in parallel.

*(Relations gained `version` in migration 0009. Before it an edge was
last-writer-wins, which mattered most exactly where the schema pushes
multiplicity: an edge is unique per `(type, source, target)`, so two
meanings between one pair of entities go into the edge's own fields, and
those fields were the one place in the metamodel with no protection
against a concurrent write.)*

### Audit

Every write records the actor (user id or token id). No full version
history for entities in this spec; that belongs to the markdown domain,
and if entity history is ever needed it gets its own spec.

## 4. MCP and REST surface

The MCP surface is first-class — most content is written by agents.
REST exists for the web UI and is not promised stable.

### MCP tools (`maestro.*`)

Context — `whoami`, `games.list`, `games.get`, `games.counts`.

`games.counts` was added by Metamodel 14 and is the whole of "how many
races are there": one row per declared entity type and per declared
relation type with what the game holds of it, plus three totals. Task 9
found that nothing on the agent surface counted, so answering that
question was a full paged walk — five calls in a two-hundred-row game —
while the REST home page had the number from two grouped queries. Both
surfaces now go through one assembly, so the page and the tool cannot
report different numbers. Its cost does not grow with the game's
content, which is why it has no page and no cursor.

Schema — `types.list`, `types.get`, `types.upsert`, `types.remove`,
`relation_types.list`, `relation_types.get`, `relation_types.upsert`,
`relation_types.remove`.

A relation type's endpoint rules are stated and read back as entity type
**keys** (`source_type_keys`, `target_type_keys`), not ids — see
"Addressing" below. A key naming no type of the game, or one an earlier
element of the same list already named, is `invalid_input` at that
element's own indexed path: an endpoint list is a set.

Content — `entities.list`, `entities.get`, `entities.upsert`,
`entities.remove`, `entities.repair`, `relations.list`,
`relations.get`, `relations.upsert`, `relations.remove`,
`relations.repair`.

The two `repair` tools were not in this spec's own list and were added
by Metamodel 15; §3's "Schema evolution" states what they may and may
not do.

`relations.get` was not in this spec's own list and was added by
Metamodel 12: a relation type may declare a field schema and the values
an edge carries were validated on write, stored, and returned by
nothing. An edge is read by the address it was written under — the
relation type's key plus both endpoints as `(type_key, key)` — and
`relations.list` grows the same `verbose` flag `entities.list` has, off
by default for the same reason.

Cross-cutting — `search` (free text over names, row keys and text
fields, filterable by type).

`search` grew two things in Metamodel 13, both found by Task 9's seeding
run and both a consequence of the surface being driven the way an agent
drives it:

- **It indexes a row's key.** The vector was built from the name and the
  type's indexable field values, so a designer typing the handle they
  read off every listing and every refusal got nothing back. The key
  goes in at a *lower* weight than the name (`0010_entity_key_search.sql`
  carries the argument), so a row found only by its key comes back with
  `name_match` false and the ranking promise below keeps meaning what it
  says.
- **It takes `verbose`, off by default.** It answered with every hit's
  whole field payload, `longtext` included, up to its 200-row cap — one
  sixty-hit search measured at 1.6 MB of JSON. That is now the rule the
  two listings already stated, applied to the read that needs it most: a
  search is what an agent reaches for *before* it knows which row it
  wants. The flag gates entity hits only; a document hit has never
  carried a body.

### Addressing

**Every tool on this surface addresses a row by key, and none of them
takes a uuid.** Metamodel 14 finished this: `entities.remove`,
`relations.remove`, `types.remove` and `relation_types.remove` took ids,
`relations.list` filtered its two endpoints by entity id, and a relation
type stated its endpoint rules as entity type ids. Task 9's seeding run
measured what that cost an agent that thinks in keys — which is what the
skill bundle will teach, because keys are what the game's own vocabulary
is written in: a resolving read before every one of those calls, and, for
a second session that never saw the ids, a `types.list` plus a
key-to-id map before it could declare a single relation type.

**Keys *replace* ids rather than being accepted beside them**, and the
argument is that a key here is not a nickname for an id. It is
immutable — the first spelling stored stands, and a respelling is
refused after the write — and unique per game and type, so there is
nothing an id can address that a key cannot. Accepting both would buy a
caller nothing and cost every call site an "exactly one of" refusal path,
every description two branches, and every caller a decision. This
repository already pays that where the two spellings are two genuinely
different *questions* — `views.run` takes a saved key or an inline query
and refuses both, `docs.links.list` reads the join from either side and
refuses neither and both — and neither of those is two names for one
row. It also carries one open inconsistency of exactly this shape,
routes addressing a game by uuid while the page addresses it by slug,
and a second would make that worse rather than better. **That
inconsistency is closed, 2026-09-06, in the direction this argument
points — see "A game is addressed by its slug" below.**

### A game is addressed by its slug

**Decided 2026-09-06.** Creating a game asks for a slug and a name, the
page is served at `/g/{slug}`, and every REST route under the games path
then took the game's **uuid** — the project middleware parsed the path
value as one, so a slug there answered "no such game": true, but reading
as though the game did not exist rather than as though the identifier
was the wrong kind. A person who had just created a game and was looking
at it by name had to go and find a uuid before they could mint a token
or seed anything, and an agent handed a token bound to one game still had
to learn that game's uuid before its first REST call.

**The slug replaces the uuid; both forms are not accepted.** The filing
proposed accepting either, and this refuses that for exactly the reason
the row decision above refuses it: two names for one thing cost every
call site a decision and an "exactly one of" refusal path and buy a
caller nothing. A game is not an exception to that rule — if anything it
is the clearest case of it. A slug is unique per instance under a
case-folding index, immutable (there is no rename), and already the
address a human types; `validateSlug` refuses any slug that `uuid.Parse`
accepts, so the two spellings can never be confused for one another.
There is nothing a uuid can address here that a slug cannot. Nothing is
released and no caller outside this repository depended on the uuid form.

Three consequences, each decided rather than inherited:

- **A refusal names what was tried.** `no game named "azeroth" is
  available to you`, and a value that parses as a uuid additionally gets
  "a game is addressed by its slug — the name in its /g/ URL — and not by
  its id", which is the sentence that helps a caller holding the right
  game and the wrong name for it.
- **"No such game" and "a game you are not in" are one answer.** A uuid
  is not guessable, so the old routes could safely answer a non-member
  with "you are not a member of this game"; a slug is a name someone
  chose, so the same answer would be an enumeration oracle a stranger
  could walk. `projects.BySlugForUser` joins membership into the lookup
  so both collapse to one `not_found` — the shape Task 8's Correction 12
  specified when it removed the bare `BySlug`, now built.
- **A token caller is compared, not resolved.** Its game is fixed at the
  moment the token was minted, so the slug it typed is checked against
  its own binding and never looked up; a mismatch is a `scope_violation`
  naming both games, which is honest because both are the caller's own.

The optional `project_id` confirmation on both surfaces became `game`
and takes a slug in the same change. Leaving it demanding a uuid would
have made it the only place in the product where a caller had to learn
one — the rule established and not carried one step, which is the defect
class this repository keeps finding.

The ids have not gone anywhere here either: `whoami` and `games.get`
still return `project_id`, every event still carries one, and
`ProjectScope` is still built around one. The id is simply not what a
caller types.

The ids have not gone anywhere: every reader still returns them, every
removal event still carries them, and the stored endpoint columns are
still `uuid[]`, which is right — an id is what a deleted type's prune
can remove from an endpoint list. They are simply no longer the address.
An edge, which has no key of its own, is addressed by the triple it was
written under: the relation type's key and both endpoints as
`(type_key, key)` refs, on `relations.get`, `relations.remove` and
`relations.list`'s endpoint filters alike.

On REST the same change removed the last `by-id` segment: the content
routes are `by-key` throughout, and an edge's two by-address routes are
`GET`/`DELETE /relations/one` with the same five query parameters.

### Idempotency

Every `upsert` addresses rows by `(project, type, key)`, never by id.
An agent re-running its seeding script creates no duplicates. UUIDs
exist and are returned; agents work with readable keys.

**Idempotency here means row identity, not a conflict-free re-run.**
The concurrency rule below applies to every upsert that finds an
existing row: the call must carry the matching `expected_version` or it
returns `version_conflict`. A verbatim re-run of a seeding payload
therefore conflicts on **every** row that already exists — the row
count is unchanged, which is the guarantee, but nothing is written and
every item comes back as a failure. The metamodel plan's Task 9 asserts
exactly this, and it is correct.

> **Decided 2026-09-02 — O2: bulk upserts gain `on_conflict`. Decided,
> and not yet implemented.**
>
> `entities.upsert` and `relations.upsert` grow
> `on_conflict: "fail" | "skip" | "overwrite"`, defaulting to `"fail"`
> so nothing changes for an existing caller. `"skip"` makes a re-seed
> genuinely idempotent; `"overwrite"` makes a corrective re-seed one
> call.
>
> Why: as designed, an agent re-seeding must first read every affected
> row to learn its version, then write with those versions — a page walk
> before a 500-row write, plus one piece of state carried across a
> session boundary an agent frequently does not survive.
> `2026-09-02-agent-skill-bundle-design.md` §10.1 concluded the bundle
> would have to teach that loop, and correctly named a workaround in a
> teaching document as debt. A re-seed should be one call.
>
> The counter-argument stands and is answered by the default rather than
> by refusing the mode: `"overwrite"` is a documented way to lose a
> concurrent editor's work, which is exactly what `expected_version`
> exists to prevent. It is therefore opt-in per call, never the default,
> and the bundle teaches `"skip"` for re-seeding and `"overwrite"` only
> for a deliberate correction of rows the caller owns.
>
> **Status: not implemented.** This is a change to the bulk write path
> and to the MCP tool surface, so it is a task and not a documentation
> edit. It is filed in `docs/superpowers/plans/2026-08-31-metamodel.md`
> as Task 10. Until that task lands, the paragraph above this note
> describes the shipped behaviour and a verbatim re-run still conflicts
> on every existing row.
>
> Compare `2026-09-02-markdown-domain-design.md` §7, which spells a
> create as `expected_version: 0` — an explicit "I expect this not to
> exist" rather than an omission. That convention is not adopted here;
> `on_conflict` covers the same ground for a batch without making every
> single-item caller carry a version it does not have.

### Bulk writes

Seeding a real game is hundreds of entities, so `entities.upsert` and
`relations.upsert` take a batch as well as a single item, in two modes:

- `atomic` — one transaction, all or nothing.
- `partial` (default) — valid items land; failures come back with
  their index and reason.

`partial` is what makes real seeding workable, because a first pass
always has a few bad rows. A `partial` batch reports each rejected item
with its index, its key and its code, so a caller retries the named items
and nothing else, and it returns no error of its own: the failures are
the result. An `atomic` batch that fails returns an error naming the
failing item and reports nothing as done.

**A key repeated inside one batch** is refused rather than written
twice, and the two modes answer it differently. In `atomic` the whole
batch is refused before anything is written, as `invalid_input` naming
every repeated index: the caller asked for n rows and at most n-1 can
exist, so the batch cannot be satisfied as submitted. In `partial` only
the later occurrence fails, as `invalid_input` at `items[<i>].key`
naming the earlier index, and the rest of the batch is undisturbed.

The consequence worth knowing before it costs a round trip: in
`partial` the later occurrence is refused *whether or not the first one
lands*. A batch holding `[{key: "x", min_level: "not a number"},
{key: "x", <valid>}]` comes back with index 0 as `schema_violation` and
index 1 as `invalid_input`, so key `x` does not exist afterwards and
fixing it takes a second call. This is deliberate: the batch as
submitted names one row twice and there is no reading of it that says
which of the two the caller meant, so guessing "the one that happened to
survive validation" would make the result depend on the other item's
mistakes. An agent that builds a batch from a file should fold repeated
keys itself before sending.

**A batch carries at most 500 items**, on every batch tool of every kind
— `entities.upsert`, `relations.upsert` and the markdown domain's
`docs.write_many`, which all reach one shared driver — and over that is
`invalid_input` at path `items` naming both numbers. It is *refused* and
not clamped, unlike a page limit: a caller asking for more rows than a
page may hold still has a correct answer, and a batch does not, because
silently writing the first 500 of 5,000 leaves 4,500 rows unwritten with
nothing in the report saying so. 500 is `MaxEntityPage` and
`MaxRelationPage`, so the most rows one call moves is one number across
reads and writes. Added by Metamodel 15, after Task 9 sent a 5,000-item
atomic batch that was accepted, held one transaction open for 3.1
seconds and answered with 515 KB — the one caller-supplied bound on this
surface that Postgres was left to discover rather than Go.

A `mode` that is neither word is **refused** as `invalid_input` at path
`mode`, rather than read as the default. Reading a typo as `partial`
would silently downgrade an all-or-nothing request into one that lands
rows the caller asked to have rolled back, which is a failure the caller
has no way of seeing. An omitted `mode` is still `partial`.

### Queries, deliberately shallow here

`entities.list` filters by type, by field values and by text, and
offers **one hop** of relation traversal
(`related_to: {relation_type, entity_key, direction}`). Transitive
walks, reachability and derived edges are the D2 query engine and
belong to the views sub-project. Designing them here would mean
designing them twice.

### Pagination and response size

Every `list` paginates with an opaque cursor and a maximum page size.
Responses are **slim** by default — jsonb payloads omitted unless
requested. Token discipline is a first-order concern: agents are the
main consumer.

**`search` obeys the same rule**, which it did not until Metamodel 13.
It is a top-N rather than a page, so it has no cursor, but `verbose`
is spelled and defaulted exactly as `entities.list`'s and
`relations.list`'s are. A read that answers with more than a page can
and does exist on this surface; a read that answers with more than a
page and cannot be asked not to does not.

### Error shapes

Stable and machine-readable: `not_found`, `version_conflict` (carries
the current version), `schema_violation` (carries field path and what
was expected), `invalid_schema`, `invalid_input`,
`endpoint_type_mismatch`, `scope_violation`, `in_use`.

`invalid_schema`, `schema_violation` and `invalid_input` are three
codes, not one, and the committed `internal/metamodel/errors.go` gives
them three sentinels. They are told apart by who is at fault, and each
has its own recovery:

- `invalid_schema` — a **type declaration** that cannot stand (a bad
  field key, an enum with no options, `required` plus a default, a
  default the field could not hold), reported at `field_schema[<i>]`
  paths. Fixed by re-declaring the type.
- `schema_violation` — a **row of values** that does not fit a
  declaration that can, reported at `fields.<key>` paths. Fixed by
  changing the values and writing the row again.
- `invalid_input` — a write's **own arguments** being malformed: its
  `key`, `label`, `label_plural`, `description`, `color` or `icon`,
  reported at those paths. Fixed by changing that argument and
  re-issuing the same call. It is a distinct code because
  `schema_violation` would send an agent off to inspect entity values
  over a `types.upsert` that carried a key with a space in it, and
  because the same fault reaches the entity, relation-type and relation
  upserts unchanged. The case-respelling refusal a row key can meet is
  an `invalid_input` at path `key` too.

A caller must never have to match on path spelling to tell them apart.
`ValidationError.Code` is what carries the distinction; a code that is
set but unrecognised matches no sentinel at all, so it surfaces as
`internal_error` rather than silently defaulting into the most-taught
recovery.

### REST and SSE

`/api` mirrors the above for the UI, plus what agents never need:
login, sessions, invite redemption, membership and token management.

`/events` streams `type.*`, `entity.*` and `relation.*` per game.

## 5. Validation, security, concurrency

### Field validation

One validator turns `field_schema` + `fields` into either a normalised
value or a list of errors with paths
(`fields.min_level: expected number, got "veinte"`). It is used at
every entry point — MCP, REST, any future importer.

Rules: an unknown field is an error, never silently dropped (an agent
typing `min_lvl` must feel it); a missing optional field stays absent
rather than being zero-filled; a declared default is applied when the
field is absent or null, and goes through the same coercion a
hand-written value does; enums validate against their options; numbers
honour their optional range and must be finite.

The schema **declaration** is checked separately, before anything is
stored against it, and fails with `invalid_schema` rather than
`schema_violation` — see "Error shapes". Re-validating a stored row
against an edited schema reports only whether it still fits; it never
returns a normalised map, so a re-validation pass cannot back-fill
defaults into rows the designer did not touch.

### Graph integrity

Relation endpoints are checked against the relation type's allowed
lists on write. Cycles are **not** rejected: a prerequisite cycle is a
design error to be *surfaced*, not a write error to be blocked. That
lives in the analysis sub-project.

### Security

argon2id with parameters in config. Session cookies httpOnly,
`SameSite=Lax`, `Secure` behind TLS, expiring, invalidated on password
change. Invite and API tokens stored hashed; the clear value is shown
once, at creation. Rate limiting on login and on invite redemption.

Registration modes, instance-wide config:

- `invite_only` (default) — entry only through an invite link.
- `domain_open` — anyone with an email in `ALLOWED_EMAIL_DOMAINS` can
  self-register, no link to hand out.

`ALLOWED_EMAIL_DOMAINS` (empty means unrestricted) is enforced
server-side in both modes, on registration and on invite redemption.
Registering grants no access to any game by itself; membership stays
explicit.

The hard invariant is game isolation: every call resolves its game from
the token or session, and **scope is never taken from a caller-supplied
parameter**. A token touching another game gets `scope_violation`,
admin or not. This is covered by explicit tests, not by review.

### Concurrency

`expected_version` is required on every update. `atomic` batches run in
one transaction; `partial` batches item by item. Key uniqueness is
enforced by a unique index.

## 6. Quality, testing, deployment

### Testing

Unit tests over the schema validator and type mapping — where the
subtle logic concentrates. Integration tests against a real Postgres,
each using an ephemeral database created and dropped per test (never
the live instance; Nottario's `testutil.NewPool` helper is replicated
as-is). Surface tests calling every MCP tool for real and asserting
both success and error shapes.

Four areas are mandatory because they fail silently:

1. **Game isolation** — a token for game A against resources of game B,
   including an admin token.
2. **Concurrency** — two simultaneous writes to one entity; two
   simultaneous creates with the same key.
3. **Partial batches** — valid items land, invalid ones are reported
   with index and reason.
4. **Schema evolution** — changing a type's schema with entities
   already stored.

### Pre-commit gate

`make check` chains formatting, `go vet`, linters, `sqlc diff`
(generated code matches the queries), a parse check over frontend
`.js`, `make docs-check`, and `go test ./...`. Never bypassed with
`--no-verify`; a failure means fixing the cause.

### Deployment

`compose.yml` brings up `maestro` plus Postgres. Distroless image
published by CI on every push to master. Embedded migrations run at
start-up. Scheduled in-process `pg_dump` backups. Configuration by
environment: connection string, session key, first admin, allowed email
domains, registration mode, argon2id parameters.

### Documentation

`readme.md` with a tour and local start-up. A public documentation site
at `neverbot.github.io/maestro`, built the way Nottario builds its own:
a small generator at `cmd/maestro-docs`, content under `docs/site/`,
published by CI, with `make docs-check` in the commit gate from day
one. Site contents: what Maestro is, how to run it, the conceptual
model (the four primitives, with the MMORPG and racing examples), MCP
tool reference, deployment guide.

### Definition of done

From a clean MCP client, an agent declares the types of a real game,
seeds a few hundred entities and their relations, and queries them
back; a human logs in through the browser with a local account and sees
the game exists and how much content it holds. No views yet — that is
sub-project 4.

## 7. Roadmap

This spec covers 1 and 2.

1. **Core** — binary, Postgres, migrations, projects, human and agent
   identity, REST + MCP + SSE, Docker, deployment.
2. **Metamodel** — the four primitives, field schemas and validation,
   optional semantic roles, search, full MCP surface.
3. **Markdown** — versioned documents with optimistic concurrency,
   linked to entities.
4. **Views** — the D2 query language, saved views, layout modes, manual
   positions, background images.
5. **Interface and renderers** — Maestro's own visual identity and the
   renderer catalogue (graph, layered DAG, nested boxes, map with
   background, table, timeline).
6. **Analysis** — prerequisite cycles, unreachable content, orphans,
   saved routes.
7. **Skills and genre templates** — the bundle teaching agents the
   metamodel, with MMORPG, racing and metroidvania examples.

Order: 1 → 2 → (3 and 4 in parallel) → 5 → 6 → 7. Sub-projects 1, 2
and 5 together are the first genuinely usable product.

### Views: what the data model must not break

Recorded here because it constrains this spec even though it is built
later. A view is a saved record holding a query and a layout. The query
selects entities by type and field, traverses relations to N hops,
filters by reachability from a seed, derives edges, and groups into
layers. The layout picks a renderer from a fixed catalogue, a mode
(`auto`, `manual`, mixed), optional per-view manual coordinates
`(view_id, entity_id, x, y)`, an optional background image and scale.

Positions live per view, not on the entity: the same zone sits at its
real map coordinates in a *World map* view and wherever the algorithm
puts it in a *Mage route* view.

Two data-model consequences this spec must honour, and does: references
between entities are always relations (never hidden in jsonb), and edges
carry their own typed fields (a metroidvania door needs
`requires_ability` on the edge, not on either room). What that field is
*not* is a typed reference to the `Ability`: it is a `text` key nothing
validates and no traversal follows — a soft reference, decided
2026-09-02 under "Field schemas".
