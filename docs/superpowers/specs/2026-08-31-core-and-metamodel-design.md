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
> - "Field schemas" — **open question (O3)**: a relation cannot carry a
>   typed reference to a third entity.
> - "Metamodel" — **open question (O1)**: whether `semantic_role` is the
>   analysis mechanism, or descriptive metadata beside a separate
>   `analysis_traits` vocabulary.
> - "Idempotency" — **open question (O2)**: re-running a seed over rows
>   that already exist is not conflict-free; the old wording read as
>   though it were.
> - "Error shapes" and "Field validation" — `invalid_schema` exists as a
>   seventh code, distinct from `schema_violation`.
>
> Nothing else in the spec changed. The open questions are the user's to
> decide; no later spec may answer them on its own.

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
`fields` (jsonb).

`semantic_role` is one of `prerequisite`, `unlock`, `containment`,
`spatial`, `availability`, `reward`, or null, enforced by a `CHECK` on
the column. It is **not** used for drawing — views select relation types
explicitly. It exists so the analysis sub-project can answer questions
that need real semantics: "is this quest unreachable?", "is there a
prerequisite cycle?". Those are the questions a designer cannot answer
by eye across 400 missions.

> **Open question O1 — is `semantic_role` the analysis mechanism?**
> Recorded here, in the one place that owns the column, so that no other
> spec answers it on its own.
>
> `2026-09-02-analysis-engine-design.md` §2 argues that this column
> conflates two orthogonal axes — what an edge *means* to a designer
> (`reward`, `availability`) and how it *behaves* in a graph walk
> (acyclic, symmetric, direction of dependency) — that a closed enum of
> meanings must grow every time a genre invents a meaning, and that it
> still cannot say whether a cycle in a given type is a bug. It proposes
> a second, orthogonal vocabulary, `relation_types.analysis_traits
> text[]`, and keeps `semantic_role` as descriptive metadata plus a
> fixed compatibility mapping.
> `2026-09-02-agent-skill-bundle-design.md` §10.2 reports that two
> vocabularies for one job is what makes an agent choose wrong
> consistently across thirty relation types, and that its
> `reference/analysis.md` page cannot be written until this is settled.
>
> Three outcomes are open, and **the user decides**:
> (a) traits are added and `semantic_role` is kept as a human-readable
> label with no analytical meaning; (b) traits are added and
> `semantic_role` is dropped in a migration, before any real game is
> seeded; (c) traits are rejected and `semantic_role` is extended
> instead.
>
> Until it is decided, this sentence is the authority: `semantic_role`
> is what the committed schema has, and whether it *drives* analysis is
> undecided. The analysis and skill-bundle specs cross-reference this
> question rather than restating it.

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

> **Open question O3 — a relation cannot carry a typed reference to a
> third entity.** Recorded here because it is a metamodel question, not
> a views question, and it must be stated in exactly one place.
>
> The rule above is stated for *entities*, and it holds there: an
> entity pointing at an entity is an edge. It does not resolve the case
> where the thing doing the pointing is itself an edge. A metroidvania
> door is a `connects_to` relation from room to room whose gate is
> "requires the Mothwing Cloak" — a reference to a third entity, the
> `Ability`. A relation's own fields are scalars of the six types above,
> so the only available spelling is a `text` field holding an ability
> key, which nothing validates and no traversal can follow; and a
> relation cannot itself be an endpoint of another relation. The
> capability the rule promises for entities therefore has no equivalent
> for edges.
>
> `2026-09-02-views-and-query-language-design.md` §3.3 hits this on its
> metroidvania example and cross-references this question; its `map`
> view renders the text field happily, and the dangling key is invisible
> to it.
>
> Options, none chosen: leave it, and let the analysis engine report
> dangling keys later; add an `entity_ref` field type that is validated
> on write but is deliberately not traversable; model the gate as its
> own entity with two relations. **The user decides**, and the decision
> is cheaper now than after a game is seeded.
>
> Note for whoever decides: `readme.md`'s genre table presents this
> exact metroidvania door as the example that justifies typed edges.
> Edges *do* carry typed fields, so the readme is not wrong about that —
> but the ability reference itself is untyped and unvalidated, and the
> readme's framing reads as a stronger promise than the design makes.
> `readme.md` was out of scope for this reconciliation pass and has not
> been edited.

### Constraints and indexes

`key` unique per project on types; unique per (project, type) on
entities — enforced by a unique index, not a pre-flight check, so two
concurrent agents cannot slip between a `SELECT` and an `INSERT`.
Relation endpoints are validated against the relation type's allowed
lists on write. `tsvector` + GIN over names and text fields for
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
existing entities are neither rejected nor back-filled with invented
values. They are flagged as invalid and `entities.list` can filter for
them (`invalid: true`) so a human or agent repairs them deliberately.

### Concurrency

Every type and entity carries an integer `version`; every update passes
`expected_version`. A conflict returns `version_conflict` **with the
current version**, so the caller re-reads, merges and retries. This is
Nottario's optimistic-concurrency pattern applied to the whole domain,
and it matters here because several agents seed content in parallel.

### Audit

Every write records the actor (user id or token id). No full version
history for entities in this spec; that belongs to the markdown domain,
and if entity history is ever needed it gets its own spec.

## 4. MCP and REST surface

The MCP surface is first-class — most content is written by agents.
REST exists for the web UI and is not promised stable.

### MCP tools (`maestro.*`)

Context — `whoami`, `games.list`, `games.get`.

Schema — `types.list`, `types.get`, `types.upsert`, `types.remove`,
`relation_types.list`, `relation_types.get`, `relation_types.upsert`,
`relation_types.remove`.

Content — `entities.list`, `entities.get`, `entities.upsert`,
`entities.remove`, `relations.list`, `relations.upsert`,
`relations.remove`.

Cross-cutting — `search` (free text over names and text fields,
filterable by type).

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

> **Open question O2 — should a bulk upsert have a conflict mode?**
> Recorded here rather than in the sub-project specs that ran into it.
>
> As designed, an agent re-seeding must first read every affected row to
> learn its version, then write with those versions. That is a page walk
> before a 500-row write, plus one piece of state carried across a
> session boundary an agent frequently does not survive.
> `2026-09-02-agent-skill-bundle-design.md` §10.1 concludes the bundle
> must teach that read-then-write loop, and correctly names a workaround
> in a teaching document as debt.
>
> Its recommendation, aimed at this spec: `entities.upsert` and
> `relations.upsert` gain `on_conflict: "fail" | "skip" | "overwrite"`,
> defaulting to `"fail"` so nothing changes for existing callers.
> `"skip"` makes a re-seed genuinely idempotent, `"overwrite"` makes a
> corrective re-seed one call. The counter-argument is that
> `"overwrite"` is a documented way to lose a concurrent editor's work,
> which is the failure `expected_version` exists to prevent.
>
> Not decided. **The user decides**; it is a change to this spec's MCP
> surface, so it belongs to the metamodel sub-project and not to the
> bundle. Compare `2026-09-02-markdown-domain-design.md` §7, which
> spells a create as `expected_version: 0` — an explicit "I expect this
> not to exist" rather than an omission.

### Bulk writes

Seeding a real game is hundreds of entities, so `entities.upsert` and
`relations.upsert` take a batch as well as a single item, in two modes:

- `atomic` — one transaction, all or nothing.
- `partial` (default) — valid items land; failures come back with
  their index and reason.

`partial` is what makes real seeding workable, because a first pass
always has a few bad rows.

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
`requires_ability` on the edge, not on either room). What that field
cannot be is a *typed reference to the `Ability`* — see open question O3
under "Field schemas".
