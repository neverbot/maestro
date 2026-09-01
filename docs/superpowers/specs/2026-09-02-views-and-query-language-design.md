# Maestro — views and the D2 query language (design)

Date: 2026-09-02
Status: proposed
Scope: sub-project 4 of the roadmap in
`docs/superpowers/specs/2026-08-31-core-and-metamodel-design.md`
Depends on: sub-projects 1 (core) and 2 (metamodel), both specified and
planned. This spec writes against the committed schema in
`docs/superpowers/plans/2026-08-31-metamodel.md`.

## 1. What this spec decides, and why it exists

The core spec settled that **a view is a saved record holding a query
and a layout** ("D2"), and deferred the design. This is that design.

The decision it implements, restated so the constraint is visible:

- **D1 — a fixed catalogue of built-in views** (a "quest list" tab, a
  "zone map" tab, a "talent tree" tab). Rejected: Maestro ships no
  built-in game concepts, so a built-in view has nothing to bind to. A
  genre Maestro has never seen would get nothing at all, and a genre it
  has seen would get a view that is subtly wrong for *this* game.
- **D2 — a view is a saved query plus a layout, authored by an agent
  over MCP.** Chosen. The agent working on a game has context Maestro
  does not: it knows this game calls its places `Circuit`, that
  progression runs through `unlocks`, and that the designer just asked
  for something nobody anticipated. Rendering stays fixed: Maestro
  ships a small catalogue of renderers and the agent picks one and
  parameterises it.
- **D3 — agents author renderer code.** Rejected: arbitrary
  agent-written JavaScript executing in every teammate's browser, an
  unbounded surface to review, for a handful of diagram shapes that a
  parameterised renderer already covers.

The test this design has to pass is a concrete one, from the design
conversation:

> *the quests a Mage can reach between level 20 and 30, coloured by
> zone*

must be a saved view an agent composes in one call — not a feature
request, not a code change, not a hard-coded tab. Section 3 shows it
expressed in the language, together with a racing-career progression
and a metroidvania room graph with ability-gated doors.

Out of scope here, deliberately: what any of this **looks like**. The
visual identity and the renderers' actual appearance belong to
sub-project 5 and to `/impeccable`. This spec specifies contracts —
what a renderer consumes and what knobs it exposes — not pixels.

## 2. The query language

### 2.1 Why JSON, and not a text DSL

The obvious candidate given MCP is a JSON document, but it deserves an
argument rather than an assumption.

**Chosen: a versioned JSON document.** The reasons:

1. The primary author is an agent calling an MCP tool. Its output is
   already JSON; a text DSL means the agent emits a string that
   Maestro must parse, and every parse failure becomes a round trip
   over a syntax error rather than over a semantic one.
2. The secondary author is Maestro's own UI, which will grow a
   point-and-click query builder. A builder edits a tree; it does not
   edit a string.
3. Errors get **structural addresses**. A `query_invalid` can carry a
   JSON pointer (`/traverse/0/where/value`) and say exactly what was
   expected. A text DSL would need line/column bookkeeping to be as
   useful, and would be less useful anyway.
4. It stores as `jsonb` in the `views.query` column with no escaping,
   which means the dependency extraction in §6 can be done in SQL.
5. Validating it reuses the machinery that already validates
   `field_schema` payloads — one validator, one error shape.

**Rejected: a Cypher/GraphQL-flavoured text DSL.** It reads better to
a human typing into a box, and that is a real cost we are accepting.
But a human typing a traversal by hand is not the workflow this
product is built around: the designer asks in prose, the agent
composes the view. If a text surface is ever wanted it can be a
*front-end* that compiles to this JSON, which is strictly easier than
the reverse.

**Rejected: raw SQL from agents.** It cannot be bounded, it cannot be
made safe, it leaks the physical schema into saved content forever,
and it defeats the project-isolation invariant, which is enforced in
SQL and would then be enforced by whatever the agent felt like
writing.

### 2.2 Shape

```json
{
  "v": 1,
  "params": [ … ],
  "from": [ … ],
  "traverse": [ … ],
  "nodes": [ … ],
  "edges": [ … ],
  "project": { … },
  "limits": { … }
}
```

`v` is required and is `1`. Every stored query carries it, so the
language can change without a migration that rewrites authored
content.

#### `from` — the seed set

A list of selectors, unioned. A selector picks entities of one type:

```json
{ "type": "quest", "as": "quests", "keys": ["hogger"], "where": { … } }
```

- `type` — an entity type **key** (required).
- `keys` — optional list of entity keys, a shortcut for an `in`
  predicate on the key.
- `where` — an optional predicate (§2.3).
- `as` — a name for the resulting set, used by `traverse`, `nodes` and
  `edges`. Defaults to the type key.

#### `traverse` — walking relations

An ordered list of steps. Each step reads from an earlier named set
and produces a new one:

```json
{
  "from": "mage",
  "via": "available_to",
  "direction": "in",
  "depth": 1,
  "to_type": "quest",
  "where": { … },
  "edge_where": { … },
  "as": "mage_quests"
}
```

- `from` — the name of a set produced by `from` or by an earlier step.
  Required; there is no implicit "previous step", because an implicit
  chain makes a two-branch query impossible to read.
- `via` — a relation type key, or a list of them (union).
- `direction` — `out` (source → target), `in` (target → source), or
  `any`.
- `depth` — `1` (default), an integer, or `{"min": 1, "max": 4}`.
  Anything above 1 compiles to a recursive CTE (§4).
- `to_type` — optional; restricts reached entities to one entity type
  or a list of them.
- `where` — a predicate on the reached entity.
- `edge_where` — a predicate on the **relation's own fields**. This is
  what makes ability-gated doors expressible without a schema hack:
  the condition lives on the edge, and the language can filter on it.
- `as` — required if the set is referenced later.

A step's result is a set of entities **plus** the relations that were
walked to reach them; both are available to `nodes` and `edges`.

#### `nodes` — what gets drawn

A list of set names. Defaults to every named set. A set can be listed
with a `role`, which renderers use for grouping (`"role": "seed"` for
the Mage in the example below, so a graph renderer can anchor it).

#### `edges` — what gets drawn between them

Two forms:

- `{"from_step": "mage_quests"}` — draw the relations that step
  actually traversed. The common case.
- `{"via": "requires", "between": ["mage_quests", "mage_quests"],
  "direction": "out"}` — draw relations of a type *between nodes
  already in the result*, without traversing to fetch new nodes.
  This is how a prerequisite chain appears inside an otherwise
  level-filtered set.

An edge entry may carry `label_from` (a relation field key, or
`@type` for the relation type's label).

#### `project` — how a node presents itself

Projection hints, consumed by renderers:

- `label` — `@name` (default), `@key`, or a field key.
- `color_by`, `group_by`, `size_by`, `sort_by` — each an *attribute
  reference* (§2.4).
- `fields` — extra field keys to include in each node's payload.
  Omitted by default: token discipline (§5.5).

#### `limits`

Per-query overrides of the defaults in §4.3, always clamped to the
hard caps. A query asking for more than a cap is a `limit_exceeded`
error at save time, not a silent clamp — an agent that asked for depth
20 should learn that it cannot have it.

#### `params` — one view, many subjects

A view may declare parameters:

```json
"params": [ { "key": "class_key", "type": "text", "default": "mage" } ]
```

referenced anywhere a literal value is accepted as
`{"param": "class_key"}`. `views.run` accepts `params` to override the
defaults.

This is in v1 on purpose. Without it, "quests a Mage can reach"
becomes nine near-identical saved views in an MMORPG with nine
classes, and the designer who adds a tenth class gets nothing. With
it, the agent authors one view and the UI grows a selector. The cost
is one extra resolution pass before compilation and one extra
validation rule (a param's type must match the operator it feeds).

### 2.3 Predicates

Boolean combinators and typed leaves:

```json
{ "all": [ … ] }         { "any": [ … ] }        { "not": { … } }
{ "field": "min_level", "op": "between", "value": [20, 30] }
```

Field references are either a **declared field key** (`min_level`,
resolved against the entity type's or relation type's `field_schema`)
or a built-in addressed with an `@` sigil: `@name`, `@key`, `@type`,
`@invalid`, `@created_at`. The sigil exists because a game is free to
declare a field literally called `name`, and an ambiguity there would
be discovered by a designer looking at a wrong diagram.

Operators are **typed against the declared field type**, and a
mismatch is refused at save time:

| Declared type | Operators |
|---|---|
| `text`, `longtext` | `eq` `neq` `in` `contains` `starts_with` `matches` (case-insensitive substring/prefix; `matches` is a literal-anchored glob, not a regex) `exists` |
| `number` | `eq` `neq` `lt` `lte` `gt` `gte` `between` `in` `exists` |
| `bool` | `eq` `exists` |
| `enum` | `eq` `neq` `in` `exists` — and the value must be one of the declared options |
| `list<T>` | `contains` `contains_any` `contains_all` `empty` `length_eq` `length_gte` `length_lte` |

An enum value that is not a declared option is a `query_invalid`, not
an empty result. Empty results that are really typos are the single
most expensive failure mode in a query language whose author is not in
the room.

No regular expressions. A regex over jsonb is unbounded work inside a
statement timeout we are trying to keep small, and nothing in the
worked examples needs one. If it turns out to be needed it can be
added as a `regex` operator with its own cost cap.

### 2.4 Attribute references

`color_by`, `group_by`, `label_from` and friends take either a field
reference as above, or a **one-hop related attribute**:

```json
{ "related": { "via": "takes_place_in", "direction": "out",
                "type": "zone", "attr": "@name" } }
```

That is exactly what *"coloured by zone"* needs: the colour is not a
property of the quest, it is the name of the zone one hop away.
Restricting it to one hop is deliberate — a multi-hop colour source is
a traversal, and traversals belong in `traverse` where they are
bounded and visible.

If the hop yields several entities, the first by name is used and the
node carries `ambiguous: true` so a renderer can mark it. Silently
picking one and saying nothing would produce a map that is wrong in a
way nobody can see.

### 2.5 Result shape

Every query, whatever the renderer, produces the same envelope:

```json
{
  "nodes": [
    { "id": "…uuid…", "key": "hogger", "type": "quest",
      "name": "Wanted: Hogger", "set": "mage_quests",
      "attrs": { "color_by": "Elwynn Forest", "min_level": 22 } }
  ],
  "edges": [
    { "id": "…uuid…", "type": "requires", "source": "…", "target": "…",
      "label": null, "fields": { } }
  ],
  "stats": { "nodes": 41, "edges": 63, "max_depth_reached": 3,
             "duration_ms": 84 },
  "truncated": { "nodes": false, "edges": false, "depth": false },
  "stale": [ ]
}
```

One shape for every renderer is the point: a renderer is a consumer of
nodes and edges, and swapping `graph` for `table` on a saved view must
never require rewriting the query.

## 3. Worked examples

### 3.1 MMORPG — quests a Mage can reach between level 20 and 30, coloured by zone

Types: `class`, `quest`, `zone`. Relations: `available_to`
(quest → class), `requires` (quest → quest), `takes_place_in`
(quest → zone).

```json
{
  "v": 1,
  "params": [ { "key": "class_key", "type": "text", "default": "mage" } ],
  "from": [
    { "type": "class", "as": "cls",
      "where": { "field": "@key", "op": "eq",
                 "value": { "param": "class_key" } } }
  ],
  "traverse": [
    { "from": "cls", "via": "available_to", "direction": "in",
      "to_type": "quest", "depth": 1, "as": "reachable",
      "where": { "all": [
        { "field": "min_level", "op": "gte", "value": 20 },
        { "field": "min_level", "op": "lte", "value": 30 }
      ] } }
  ],
  "nodes": [ { "set": "cls", "role": "seed" }, { "set": "reachable" } ],
  "edges": [
    { "from_step": "reachable" },
    { "via": "requires", "between": ["reachable", "reachable"],
      "direction": "out" }
  ],
  "project": {
    "label": "@name",
    "color_by": { "related": { "via": "takes_place_in",
                               "direction": "out", "type": "zone",
                               "attr": "@name" } },
    "fields": ["min_level"]
  },
  "limits": { "max_nodes": 500 }
}
```

Renderer: `graph`, or `layered` with `rank_by: {"field": "min_level"}`
to get a levelling ladder instead of a cloud. Same query, different
saved view.

Note what the second `edges` entry buys: prerequisite arrows *within*
the level band, without pulling in the level-19 quests that would
otherwise arrive as traversal targets. The designer asked about 20–30;
the diagram shows 20–30.

### 3.2 Racing career — what a Rookie licence opens up

Types: `licence`, `championship`, `race`, `circuit`, `car`. Relations:
`unlocks` (licence → championship, championship → licence, …),
`contains` (championship → race), `takes_place_in` (race → circuit),
`requires` (championship → car).

```json
{
  "v": 1,
  "from": [
    { "type": "licence", "as": "start",
      "where": { "field": "@key", "op": "eq", "value": "rookie" } }
  ],
  "traverse": [
    { "from": "start", "via": "unlocks", "direction": "out",
      "depth": { "min": 1, "max": 4 }, "as": "progression" },
    { "from": "progression", "via": "contains", "direction": "out",
      "to_type": "race", "depth": 1, "as": "races" },
    { "from": "races", "via": "takes_place_in", "direction": "out",
      "to_type": "circuit", "depth": 1, "as": "circuits" }
  ],
  "nodes": [ { "set": "start", "role": "seed" },
             { "set": "progression" }, { "set": "races" },
             { "set": "circuits" } ],
  "edges": [ { "from_step": "progression" }, { "from_step": "races" },
             { "from_step": "circuits" } ],
  "project": { "label": "@name", "color_by": "@type",
               "group_by": "@type" },
  "limits": { "max_depth": 4, "max_nodes": 800 }
}
```

Renderer: `layered`, `rank_direction: "LR"`. The career ladder reads
left to right, and the `max: 4` is what stops a fully connected
progression graph from returning the whole game.

### 3.3 Metroidvania — the room graph, and which doors are gated

Types: `room`, `ability`. Relation `connects_to` (room → room) with
its own field `requires_ability` (`text`, referencing an ability key —
see the open question in §7 about that).

```json
{
  "v": 1,
  "from": [ { "type": "room", "as": "rooms" } ],
  "traverse": [],
  "nodes": [ { "set": "rooms" } ],
  "edges": [
    { "via": "connects_to", "between": ["rooms", "rooms"],
      "direction": "out", "label_from": "requires_ability" }
  ],
  "project": { "label": "@name", "color_by": "area" },
  "limits": { "max_nodes": 1200, "max_edges": 4000 }
}
```

Renderer: `map`, with the game's world map as `background_asset_id`
and `coordinate_source: "manual"` so the designer drags rooms onto the
real map and they stay there.

And the gated subset, as a second view sharing nothing but the schema:

```json
{
  "v": 1,
  "from": [ { "type": "room", "as": "rooms" } ],
  "traverse": [
    { "from": "rooms", "via": "connects_to", "direction": "out",
      "depth": 1, "as": "gated",
      "edge_where": { "field": "requires_ability", "op": "exists",
                      "value": true } }
  ],
  "nodes": [ { "set": "gated" } ],
  "edges": [ { "from_step": "gated" } ],
  "project": { "label": "@name", "color_by": "area" }
}
```

`edge_where` doing the work here is the whole argument for edges
carrying typed fields, which the core spec already committed to.

## 4. Compiling to SQL

### 4.1 Resolution before compilation

Nothing an agent wrote is ever concatenated into SQL. A compilation
pass first resolves, against the caller's already-resolved project id:

- every entity type key → `entity_types.id`
- every relation type key → `relation_types.id`
- every field key → its declared `{type, options}` from the relevant
  `field_schema`
- every `{"param": …}` → a literal, type-checked against the param
  declaration

Unresolvable names fail before a single byte of SQL exists. After
this pass the only agent-controlled values that reach Postgres are
**bind parameters**: uuids, numbers, strings and jsonb path keys. Even
a jsonb field key travels as `fields ->> $n`, never as interpolated
text — the alternative is one careless `fmt.Sprintf` away from an
injection, and this code will be edited by people who are not thinking
about that at the time.

### 4.2 Structure of the generated statement

Each `from` selector and each `traverse` step becomes a CTE. Depth-1
steps are plain joins; steps with `max > 1` become `WITH RECURSIVE`.

The recursive form, sketched for one step:

```sql
WITH RECURSIVE walk (id, depth, path, via_relation) AS (
    SELECT e.id, 0, ARRAY[e.id], NULL::uuid
    FROM entities e
    WHERE e.project_id = $1 AND e.id = ANY($2)          -- the seed set
  UNION ALL
    SELECT
        CASE WHEN $3 = 'out' THEN r.target_id ELSE r.source_id END,
        w.depth + 1,
        w.path || CASE WHEN $3 = 'out' THEN r.target_id ELSE r.source_id END,
        r.id
    FROM walk w
    JOIN relations r
      ON r.project_id = $1
     AND r.relation_type_id = ANY($4)
     AND ((($3 = 'out') AND r.source_id = w.id)
       OR (($3 = 'in')  AND r.target_id = w.id))
    WHERE w.depth < $5
      AND NOT (CASE WHEN $3 = 'out' THEN r.target_id ELSE r.source_id END)
              = ANY(w.path)
)
SELECT … FROM walk …
```

Three things in there are load-bearing:

- **`project_id = $1` on every table reference, including inside the
  recursive term.** The isolation invariant is enforced in SQL, not in
  Go. A CTE that joins `relations` without its project filter would
  work perfectly in every test with one game in the database.
- **`NOT … = ANY(path)`** — cycle protection. The core spec
  deliberately allows prerequisite cycles as design errors to be
  surfaced; a traversal that did not carry a visited path would
  therefore hang on real content, on the very games the analysis
  sub-project exists to help.
- **`depth < $5`** — the bound, applied in the recursive term where it
  actually prunes, not in an outer `WHERE` that would materialise the
  whole walk first.

`direction: "any"` compiles to the union of both branches with the
same path guard.

Typed field predicates compile with an explicit type guard before the
cast, because the metamodel allows entities to be *flagged* invalid
rather than rejected when a schema evolves — so a column declared
`number` can genuinely hold `"veinte"` in a row written before the
schema changed:

```sql
jsonb_typeof(e.fields -> $n) = 'number'
  AND (e.fields ->> $n)::numeric BETWEEN $m AND $k
```

Rows with `invalid = true` are **excluded by default**, with
`include_invalid: true` on the query to opt in. Rationale: a view is a
picture a designer will trust, and half-migrated rows silently altering
its shape is worse than their absence, which the invalid-entity list in
`entities.list` already surfaces properly.

### 4.3 Bounds

An agent *will* write a query that walks a 400-mission graph
unbounded. The design has to survive that rather than hope.

| Bound | Default | Hard cap | Behaviour past it |
|---|---|---|---|
| `max_depth` | 4 | 12 | `limit_exceeded` at save; truncated at run with `truncated.depth = true` |
| `max_nodes` | 1000 | 5000 | result truncated, `truncated.nodes = true` |
| `max_edges` | 4000 | 20000 | result truncated, `truncated.edges = true` |
| statement timeout | 5 s | 15 s | `query_timeout` error |
| CTE steps per query | — | 8 | `query_invalid` at save |

Mechanics:

- Every execution runs in a **read-only transaction** with
  `SET LOCAL default_transaction_read_only = on` and
  `SET LOCAL statement_timeout`. The compiler only ever emits
  `SELECT`, and the transaction makes that a guarantee rather than a
  property of the current code.
- Node and edge caps are applied as `LIMIT cap + 1`, so truncation is
  **detected**, not inferred. A truncated result is returned with the
  flag set, not replaced by an error: a designer asking about a huge
  graph should see a thousand nodes and a warning, not a stack trace.
- The recursive walk carries its own `LIMIT max_nodes * 4` inside the
  CTE so a pathological branching factor cannot build a giant
  intermediate before the outer limit applies.
- Execution duration and row counts go into `stats`, always. The first
  question about a slow view is which of the two it is.

### 4.4 What the existing indexes do and do not cover

Reading the committed migration honestly:

- Traversal *forward* by relation type is served: the unique index
  `relations_edge_key (relation_type_id, source_id, target_id)` has the
  right leading columns.
- Traversal *backwards* by relation type is not: only
  `relations_target_idx (target_id)` exists, so an `in` walk over a
  game with one dominant relation type filters after the fact. **A
  `(relation_type_id, target_id)` index should be added.** Reverse
  walks are not an edge case — `available_to` in example 3.1 is walked
  inwards.
- Field predicates are **not** index-served. `entities_fields_idx` is
  `gin (fields jsonb_path_ops)`, which supports containment (`@>`) and
  nothing else — no ranges, no `>`/`<`, not even key existence. So
  `min_level BETWEEN 20 AND 30` is a scan of the type's entities.

The last one is acceptable and should be stated as such rather than
fixed speculatively: entity counts per type in a real game are
hundreds to low thousands, the `entities_type_idx` filter runs first,
and a scan of two thousand jsonb rows inside a 5-second timeout is not
close to a problem. If it ever is, the fix is a declared `indexed:
true` flag in `field_schema` that creates an expression index when the
type is upserted — see §7, open.

## 5. Renderers, layout and storage

### 5.1 The renderer catalogue

Renderers are fixed, Maestro-authored, and parameterised. Each
declares what a result must contain for it to be saveable against that
renderer; `views.upsert` refuses the combination otherwise with
`renderer_requirements`, so a broken view is caught when it is written
rather than when a designer opens it.

| Renderer | Consumes | Key parameters |
|---|---|---|
| `graph` | nodes, edges | `color_by`, `group_by`, `size_by`, `edge_labels`, `arrows`, `cluster_by` |
| `layered` | nodes, edges (expected mostly acyclic) | `rank_direction` (`TB`/`LR`), `rank_by` (`edges` — longest path — or a numeric field), `layer_labels`, `align` |
| `nested` | nodes, edges of one containment relation type | `contain_via` (relation type key, required), `max_depth`, `leaf_label` |
| `map` | nodes, edges optional, coordinates required | `background_asset_id`, `background_scale`, `background_offset`, `coordinate_source` (`manual` \| `fields`), `x_field`, `y_field`, `snap` |
| `table` | nodes only | `columns` (field or attribute references, including one-hop related), `sort`, `group_by`, `page_size` |
| `timeline` | nodes with a numeric or ordinal axis attribute | `axis_field` (required), `axis_end_field`, `lane_by`, `axis_label` |

Requirements, concretely: `nested` refuses a query with no containment
edges; `timeline` refuses one whose `axis_field` is not `number` or
`enum` with ordered options; `map` in `coordinate_source: "fields"`
refuses fields that are not `number`.

Not designed here: colour palettes, node shapes, typography, density,
interaction affordances, empty states. Sub-project 5.

`table` is in the catalogue on purpose and is probably the most-used
one. "Every quest in Elwynn with its level and its rewards" is a
question designers ask far more often than anything graph-shaped, and
the same query language answers it.

### 5.2 Layout: automatic, manual, and both

Three modes on the view row:

- `auto` — the engine lays out every node each time. Stored positions
  are ignored (not deleted).
- `manual` — every node with a stored position uses it; a node without
  one is placed **by the engine once**, and stays unpinned until a
  human drags it. It is never dropped at the origin, which would stack
  every new room on top of the first.
- `mixed` (default) — pinned nodes are fixed; the engine lays out the
  rest around them, treating pinned nodes as obstacles.

Force-directed layout is seeded from `views.layout_seed` so that two
designers opening the same view see the same picture. An unseeded
force layout produces a different diagram every load, which destroys
the one thing a saved view is for: being recognisable.

### 5.3 Positions and the data moving underneath them

```sql
CREATE TABLE view_positions (
    view_id    uuid NOT NULL REFERENCES views (id)     ON DELETE CASCADE,
    entity_id  uuid NOT NULL REFERENCES entities (id)  ON DELETE CASCADE,
    project_id uuid NOT NULL REFERENCES projects (id)  ON DELETE CASCADE,
    x double precision NOT NULL,
    y double precision NOT NULL,
    pinned     boolean NOT NULL DEFAULT true,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (view_id, entity_id)
);
```

Positions are **per view**, as the core spec already required: a zone
sits at its real map coordinates in a *World map* view and wherever the
algorithm put it in a *Mage route* view. They carry `project_id`
redundantly so every query can filter on it (§5.4).

Two failure modes, both normal:

- **A node disappears.** Deleting an entity cascades its position rows
  away. Nothing else happens; positions are a cache of where a human
  put something, never a claim that it exists.
- **A node appears.** No stored row, so it is laid out automatically
  and joins the picture unpinned. In `manual` mode a small "N new
  nodes placed automatically" signal is what tells a designer to go
  arrange them.

Positions never affect query results, and re-running a query never
rewrites positions. That separation is what lets a query be edited
without losing an afternoon of map work.

### 5.4 Storage

```sql
CREATE TABLE views (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    key         text NOT NULL,
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    query           jsonb   NOT NULL,
    renderer        text    NOT NULL,
    renderer_params jsonb   NOT NULL DEFAULT '{}'::jsonb,
    layout_mode     text    NOT NULL DEFAULT 'mixed'
                    CHECK (layout_mode IN ('auto','manual','mixed')),
    layout_seed     integer NOT NULL DEFAULT 1,
    background_asset_id uuid REFERENCES view_assets (id) ON DELETE SET NULL,
    background_scale  double precision NOT NULL DEFAULT 1,
    background_offset jsonb NOT NULL DEFAULT '{"x":0,"y":0}'::jsonb,
    version     integer NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by_user_id  uuid REFERENCES users (id)      ON DELETE SET NULL,
    updated_by_token_id uuid REFERENCES api_tokens (id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX views_key_key ON views (project_id, lower(key));
```

Mirroring the metamodel tables deliberately: `key` unique per project
so `views.upsert` is idempotent, `version` for the same optimistic
concurrency, the same audit columns.

**`view_refs`** — the dependency index that makes §6 work:

```sql
CREATE TABLE view_refs (
    view_id          uuid NOT NULL REFERENCES views (id) ON DELETE CASCADE,
    project_id       uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    kind             text NOT NULL CHECK (kind IN ('entity_type','relation_type')),
    ref_key          text NOT NULL,
    entity_type_id   uuid REFERENCES entity_types (id)   ON DELETE SET NULL,
    relation_type_id uuid REFERENCES relation_types (id) ON DELETE SET NULL,
    pointer          text NOT NULL
);
```

Written from the query on every `views.upsert`. `ON DELETE SET NULL`
rather than `CASCADE` is the whole point: when a type is deleted the
row survives with a null id and its key text intact, so "which views
did that break, and where in each query" is one indexed SQL query
rather than a scan over every stored jsonb document.

**`view_assets`** — background images, stored as `bytea` in Postgres,
served from `/g/<slug>/assets/<id>`.

```sql
CREATE TABLE view_assets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    filename   text NOT NULL,
    mime       text NOT NULL,
    width      integer NOT NULL,
    height     integer NOT NULL,
    bytes      bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
```

In Postgres, not on disk and not in object storage: a world map is one
file of a few megabytes, and adding a storage backend to a
single-binary deployment for that is disproportionate. It also means
`pg_dump` backups already cover it, which a filesystem directory would
not. Limits: 8 MB per asset, `image/png`, `image/jpeg`, `image/webp`
only. **SVG is refused** — it is a script-execution vector when served
inline, and a raster map is what a designer has anyway. Assets are
per-project and reusable across views, not owned by one view.

### 5.5 The MCP surface

Following the naming and error conventions the core spec established:

| Tool | Purpose |
|---|---|
| `views.list` | saved views in the game; slim rows (key, name, renderer, stale flag) |
| `views.get` | one view including its query and renderer params |
| `views.upsert` | create or replace by `(project, key)`, with `expected_version` |
| `views.remove` | delete a view, its positions and its refs |
| `views.run` | execute a saved view, or an inline query, and return the result envelope |
| `views.validate` | compile and validate without saving and without executing |
| `views.set_positions` | write `[{entity_key, x, y, pinned}]` for a view |
| `views.clear_positions` | drop stored positions for a view, or for listed entities |
| `views.set_background` | point a view at an existing `view_asset`, with scale and offset |

Two of these carry a specific argument.

**`views.run` accepts an inline `query` with no saved view.** "Which
quests can a Mage reach?" asked once, in conversation, should not
leave a saved artefact behind in the game. Saving is for views a
designer will come back to.

**`views.validate` exists so composing a view is a loop.** An agent
writing a five-step traversal against an unfamiliar game will get it
wrong twice; validating without executing is fast, free of timeouts,
and returns the same structured errors, so the loop closes in seconds
instead of after a 5-second graph walk.

Background image *upload* is REST-only, from the browser. Pushing
megabytes of base64 through an MCP tool call to save a human from
opening the UI is the wrong trade; the agent references an asset that
already exists.

**Response size.** `views.run` returns node ids, keys, types, names
and the attributes the projection asked for. Full `fields` payloads
come only with `include_fields`. A thousand-node result with every
jsonb field inlined is a five-figure token bill for a picture the agent
is not going to look at anyway.

**Isolation.** Every query in this sub-project takes the resolved
project id and filters on it in SQL —
`WHERE project_id = $1 AND id = $2`, never by id alone. This applies
to `view_positions` and `view_assets` as much as to `views`, which is
why both carry `project_id` even though it is derivable through a
join. The metamodel plan's Task 7 preamble states the reasoning at
length; it applies here unchanged, and the same isolation tests must
cover every tool above.

**Errors.** Reused from the core spec: `not_found`,
`version_conflict` (carrying the current version), `scope_violation`,
`schema_violation`. New to this sub-project:

- `query_invalid` — carries a JSON pointer into the query, what was
  found and what was expected. Every structural and type failure.
- `renderer_requirements` — the renderer cannot consume what this
  query produces; carries which requirement failed.
- `limit_exceeded` — a declared limit above its hard cap; carries the
  cap.
- `query_timeout` — the statement timeout fired; carries the elapsed
  time and a suggestion of which bound to lower.
- `query_stale` — §6.

Truncation is **not** an error. It is a flag on a successful result.

**SSE.** `/events` gains `view.*` per game, so a designer watching a
view sees it marked out of date when an agent changes the underlying
content. Whether the UI re-runs automatically or shows a "refresh"
affordance is an interface decision for sub-project 5.

## 6. Validation and staleness

Stale views are the normal case, not the exception. A game's
vocabulary keeps moving for as long as the game is being designed, and
a view written in March references types that were renamed in June.

### 6.1 Types are referenced by key, and tracked by id

The stored query names types by **key** — `"via": "available_to"` —
because a query full of uuids is unreadable to the human reviewing it
and to the agent editing it. But `view_refs` records the resolved
**id** alongside. Resolution at execution time therefore goes:

1. Resolve through `view_refs` by id. A renamed type still resolves,
   because a rename does not change its id.
2. If the id is null (the type was deleted), fall back to the key. A
   type deleted and re-created under the same key resolves, which is
   the common shape of a designer fixing a mistake.
3. Neither resolves: the reference is dead.

### 6.2 Two moments, two strictnesses

**At save time, strict.** `views.upsert` refuses a query that
references an unknown type, an unknown field, an operator the declared
field type does not support, an enum value outside its options, or a
renderer whose requirements the query cannot satisfy. The write does
not land. An agent must not be able to save a view that has never been
capable of running.

**At execution time, permissive by default about *reporting*, strict
by default about *drawing*.** `views.run` takes `on_stale`:

- `fail` (default) — any stale reference aborts with `query_stale`
  carrying the full diagnostic list.
- `best_effort` — the view runs with the stale parts dropped, and the
  diagnostics come back in the `stale` array of the result envelope
  for the UI to show as a banner.

`fail` is the default because a diagram that silently dropped its
level filter looks exactly like a correct diagram, and a designer will
believe it. A wrong picture is worse than no picture. `best_effort`
exists because sometimes seeing most of the graph is what you need,
and it is the designer's explicit choice.

### 6.3 Diagnostics

```json
{ "code": "relation_type_renamed", "pointer": "/traverse/0/via",
  "was": "requires", "now": "depends_on" }
```

Codes: `entity_type_missing`, `relation_type_missing`,
`entity_type_renamed`, `relation_type_renamed`, `field_missing`,
`field_type_changed`, `enum_option_missing`, `param_unbound`.

### 6.4 A rename does not rewrite the query

When resolution succeeds by id but the key text no longer matches,
Maestro reports `*_renamed` and **leaves the stored query alone**. It
runs correctly in the meantime — the id resolved.

Rejected: silently rewriting the stored key. A view is authored
content, and editing an author's document underneath them without a
version bump makes optimistic concurrency lie — the next
`expected_version` check would pass against a document nobody wrote.
Repair is an explicit `views.upsert` by an agent, or a one-click
"update references" in the UI, both of which bump the version like any
other edit.

### 6.5 Deleting a type that views depend on

Deleting an entity type or relation type that views reference **is
allowed**, and the deletion response lists the views it broke.

Rejected: refusing with `in_use`, the way the core spec refuses
deleting a type that still has entities. The cases are not alike. An
entity is content and losing it loses work; a view is derived and can
be rewritten in one call. Making a type undeletable because a
six-month-old diagram mentions it would push designers into deleting
views to delete types, which is worse.

## 7. Open questions

These are genuinely open. None of them blocks writing the
implementation plan for the query engine, but each needs an answer
before the sub-project closes.

1. **Where layout runs, and on what engine.** Client-side is the
   instinct — a drag has to re-flow instantly and the server holds no
   canvas — but "no build step, vendored ES modules" constrains the
   candidates hard. ELK.js (layered) and d3-force (graph) are the
   obvious ones; ELK.js is large and its distributed form may not drop
   into a build-free page cleanly. Needs a spike before sub-project 5,
   and the answer may be different per renderer.
2. **Indexed jsonb fields.** Should `field_schema` gain
   `indexed: true`, creating an expression index at type-upsert time?
   It is the only real fix for range filters at scale, and it is a
   change to a table this spec otherwise does not touch. Recommend
   deferring until a real game is measurably slow, but the decision is
   the user's.
3. **Who may save a view.** The core spec has `owner`/`editor`/
   `viewer`. Running a view is obviously `viewer`; saving one is
   presumably `editor`. Should there be private or draft views, so an
   agent can compose without publishing to everyone in the game? Not
   modelled above.
4. **Paging a `table` view.** The node cap is the wrong instrument for
   a table of two thousand quests, which a designer legitimately wants
   to scroll. Either `views.run` grows the same opaque cursor
   `entities.list` uses (and the result envelope stops being uniform),
   or `table` gets a separate execution path. Neither is obviously
   right.
5. **Aggregation.** Counts and grouped counts ("quests per zone") are
   not expressible. Deliberately: it changes the result envelope from
   nodes-and-edges into something else. But "how much content is in
   each zone" is a question a designer asks constantly, and the
   analysis sub-project will want it too. Should it live here as a
   `summarise` stage, or there?
6. **Ability references on edges.** Example 3.3 stores
   `requires_ability` as `text` holding an ability key, because the
   metamodel deliberately has no reference field type — a reference is
   a relation. That rule is right and makes the graph complete, but a
   *relation* cannot point at a third entity, so a gated door genuinely
   has nowhere typed to put "which ability". The text field works and
   the view above renders it, but nothing validates that the ability
   exists. Options: leave it (analysis flags dangling keys later); add
   a `entity_ref` field type that is validated but not traversable;
   model the gate as its own entity. This is the one place where the
   metamodel and this design rub against each other, and it should be
   decided rather than absorbed.
7. **Background image layers.** One image per view is assumed above.
   A world map with a separate overlay layer is a plausible near-term
   ask, and the schema as written would need a join table for it.

## 8. What the existing schema needs

Nothing in the committed metamodel blocks this design. The two rules
the core spec fought for — references are always relations, and edges
carry their own typed fields — are exactly what make the query
language possible; example 3.3 is unwritable without the second, and
every traversal in §3 is unwritable without the first.

Required additions: the tables in §5.4 (`views`, `view_positions`,
`view_refs`, `view_assets`), in one migration.

Recommended change to an existing table: an index on
`relations (relation_type_id, target_id)`, for reverse traversal
(§4.4).

Two frictions worth recording, neither fatal:

- `relations` has no `key` and no `version`. A relation is addressable
  only as `(relation_type_id, source_id, target_id)`. That is enough
  for views — an edge in a diagram is identified by its endpoints —
  but it means a view cannot pin or annotate a *specific* edge across
  a re-seed that recreated it, and there is no optimistic concurrency
  on relation writes at all.
- `entities_fields_idx` is `gin (fields jsonb_path_ops)`, which serves
  containment only. Every field predicate that is not equality is a
  scan. Acceptable at the content volumes this product targets; open
  question 2 is the escape hatch if it stops being.

## 9. Definition of done

An agent, given only the skill bundle and a game it has just seeded,
calls `views.validate` twice to fix its own mistakes, then
`views.upsert` once, and a designer opens the resulting view in the
browser and sees the quests a Mage can reach between level 20 and 30,
coloured by zone. The designer drags four of them; the positions
survive a reload and survive the agent adding twelve more quests. The
agent then renames a relation type, and the view reports itself stale
with a pointer to the step that broke, instead of quietly drawing
something false.
