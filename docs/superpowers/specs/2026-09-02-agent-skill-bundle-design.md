# Maestro — agent skill bundle and genre templates (design)

Date: 2026-09-02
Status: draft, open questions listed in section 11
Scope: sub-project 7 of the roadmap in
`2026-08-31-core-and-metamodel-design.md`

> **Reconciled against implementation, 2026-09-02.** This document is
> agent-facing by construction — it specifies what the bundle *teaches*
> — so a false claim here propagates into every game an agent seeds.
> Changed:
>
> - §3 — **`has_default` is not an input key.** The previous text told
>   the bundle to teach it. A default is declared by the presence of the
>   `"default"` key and by nothing else; `has_default` is tagged
>   `json:"-"` in `internal/metamodel/schema.go` and is never read from
>   or written to the wire. The same paragraph now also carries the
>   `required`-plus-default rejection, the `"default": null` rule, the
>   field-key rule, and the `invalid_schema` / `schema_violation` split,
>   all against the committed validator.
> - §3 and §4.5 — `invalid_schema` and `schema_violation` are two codes
>   with two recoveries, not one code.
> - §4.4 — "singular" is a bundle convention; the spelling of a key is
>   constrained by the server. §10.5 carries the two committed key rules
>   and open question 9 is closed against them.
> - §10.1 and §10.2 — both findings were verified and accepted; the
>   decisions they ask for now live as open questions O2 and O1 in the
>   core spec, and §11.3–11.4 point there instead of posing them again.
> - §10.3 — the views spec's operator table has been corrected to
>   `list<text>`; the substantive gap this section reports is unchanged.
>
> **Re-reconciled, 2026-09-02 (second pass).** §10.5 said the respelling
> refusal arrives as `schema_violation`; the committed code returns
> `invalid_input`, and had done since the commit two after the one that
> wrote that sentence. §10.5, §3's surface list and §3's error-recovery
> paragraph now carry `invalid_input` as the third code, and the new
> §10.6 documents the descriptive-column rules — `label` required, the
> caps, the four hex colour forms, the icon-name rule — which no spec
> stated at all.

## 1. What this is for

An agent arriving at a fresh Maestro game sees four generic primitives
and no vocabulary. There is no `Quest`, no `Zone`, no `Character` — by
design, and that design is the product. The requirement recorded at the
very start of this project was that **agents are not expected to guess
the metamodel**. This sub-project is the thing that turns four
primitives into competence: an agent should arrive knowing how to
declare types and seed a few hundred entities without being told twice.

The bundle has to teach five surfaces that landed in five different
specs:

| Surface | Spec |
|---|---|
| the four primitives, field schemas, validation, concurrency | `2026-08-31-core-and-metamodel-design.md` |
| versioned documents linked to entities | `2026-09-02-markdown-domain-design.md` |
| the view query language and the renderer catalogue | `2026-09-02-views-and-query-language-design.md` |
| analysis traits, the three analyses, routes | `2026-09-02-analysis-engine-design.md` |
| the modelling judgement that ties them together | this spec |

The last row is the reason this is a sub-project and not a `readme`
section. The hard part for an agent is not calling `entities.upsert`.
It is deciding whether "level requirement" is a field on a quest or a
relation to a level entity — a decision that costs nothing on the day it
is made and costs a three-hundred-row rewrite six weeks later.

## 2. The shape of the bundle: a tree with a budget

### 2.1 The budget comes first

A bundle nobody reads is worse than no bundle, and the way a bundle
stops being read is by being expensive. Nottario's entry point —
`.claude/skills/nottario/skill.md`, the working precedent for all of
this — is 325 lines, and its full tree is 2,212 lines, roughly 27k
tokens. That entry point is already past the size where an agent reads
it attentively on every session, and Nottario's domain is *smaller*
than Maestro's: three domains against five surfaces.

So the split is decided by an explicit budget, not by taste:

| Tier | Content | Budget |
|---|---|---|
| **always loaded** | `skill.md` | **≤ 170 lines / ~2,000 tokens** |
| **loaded on demand, one page per task** | `reference/`, `modelling/` | ≤ 250 lines each |
| **loaded once, when the task matches** | `genres/`, `recipes/` | ≤ 300 lines each |

`skill.md` is not a summary of the bundle. It is the set of facts an
agent must hold **before its first write**, plus a routing table saying
which page to load next. Anything an agent can afford to learn one
round-trip late belongs on a deeper page.

Rejected: **one file**. It is what a first draft always becomes, and at
Maestro's surface area it lands somewhere near 1,500 lines. Every agent
pays for the analysis engine while seeding entities.

Rejected: **a stub entry point that only routes**. Tempting, and wrong
in a specific way: the four modelling laws in §4 must be known *before*
the first `types.upsert`, because the mistakes they prevent are the
expensive ones. An agent that has to fetch a page to learn that a
reference must be a relation will have already written the field.

### 2.2 The tree

```
maestro/
  skill.md                    ~170   ALWAYS LOADED

  reference/                         mechanics: what the calls are
    tools.md                 ~200    the whole MCP surface (GENERATED)
    fields.md                ~120    field types, validation, error text
    errors.md                 ~90    every error code and its recovery
    queries.md               ~240    the view query language
    analysis.md              ~180    traits, the three analyses, routes
    documents.md             ~110    the markdown domain

  modelling/                         judgement: what the calls should say
    deciding.md              ~220    field or relation, one type or two
    naming.md                ~120    keys, direction, types that views can use
    mistakes.md              ~200    before/after, and the cost at 300 entities

  genres/                            worked examples
    mmorpg.md   + mmorpg.json
    racing.md   + racing.json
    metroidvania.md + metroidvania.json

  recipes/                           composed workflows
    seeding-a-game.md        ~140
    joining-a-game.md         ~90    arriving at content you did not seed
    composing-a-view.md      ~120
    auditing-a-design.md     ~110
```

`reference/` answers "what is the exact argument". `modelling/` answers
"what should I put in it". Splitting on that line — mechanics against
judgement — is deliberate: the mechanics pages are the ones that can be
generated or vocabulary-checked (§8), and the judgement pages are the
ones that must be written by a human and can never be. Mixing them puts
generated tables inside prose nobody can regenerate.

### 2.3 What `skill.md` holds, exactly

1. **Identify yourself.** `whoami` first; the token is bound to exactly
   one game; `scope_violation` is not a bug to route around.
2. **The four primitives**, one table, six lines. `EntityType`,
   `Entity`, `RelationType`, `Relation`.
3. **The four laws** (§4.1). Stated flat, no argument — the argument
   lives in `modelling/deciding.md`.
4. **The seeding loop**: declare types → declare relation types *with
   their analysis traits* → bulk-write entities → wire relations →
   read back. With the concurrency rule (`expected_version` on every
   update, `version_conflict` carries the current version, re-read and
   merge) because it is the first thing that bites.
5. **Response discipline**: responses are slim by default; ask for
   `fields` / `include_fields` deliberately; paginate; do not re-read a
   page you already loaded this session.
6. **The routing table.** One line per page: "declaring types for a game
   whose shape you have not decided → `modelling/deciding.md`";
   "composing a diagram → `reference/queries.md` then
   `recipes/composing-a-view.md`"; "the game is an MMORPG-shaped thing
   → `genres/mmorpg.md`, and read it for the reasoning, not the
   vocabulary".
7. **Install and version handshake** (§7.3), four lines.

## 3. The five surfaces, and how deeply each is taught

The bundle does not teach every surface to the same depth, because
agents do not use them equally.

**Metamodel — taught completely.** Every field type
(`text`, `longtext`, `number`, `bool`, `enum`, `list<text>`), every
validation rule, every error. This is the surface where an agent writes
hundreds of rows and where a misunderstanding is expensive. Six
validator rules get their own paragraph in `reference/fields.md` because
they are the ones an agent gets wrong, and every one of them is checked
against `internal/metamodel/` by §8.2:

- An unknown field is an **error**, never silently dropped.
- An absent optional field with no default stays **absent**, not
  zero-filled.
- A declared default is applied when the field is absent or null. **A
  default is declared by the presence of the `"default"` key in the
  field declaration and by nothing else.** There is no `has_default`
  input key — it is a Go-side flag, tagged `json:"-"`, never read from
  the wire and never written to it. An agent that sends `has_default`
  gets silence: the key is ignored and the default is not declared.
  This is what makes a declared default of `false` or `0` behave like
  any other default, because presence, not value, is what is being
  recorded. An explicit `"default": null` declares **no** default.
- `required` together with a default is **rejected**. The default is
  applied before the field could be reported missing, so the pair makes
  `required` unreachable.
- A field `key` must match `^[a-z][a-z0-9_]*$` and be at most 64
  characters. Entity, entity-type and relation-type keys have a second,
  wider rule of their own — `^[A-Za-z0-9][A-Za-z0-9_-]*$`, also capped
  at 64 — see §10.5, which the bundle must teach as two rules rather
  than flatten into one.
- Three error codes, not one: `invalid_schema` for a type declaration
  that cannot stand, `schema_violation` for a row of values that does
  not fit a declaration that can, and `invalid_input` for the call's own
  arguments being malformed — a key, a label, a colour, an icon (§10.5,
  §10.6). `reference/errors.md` recovers from all three differently —
  the first is fixed in `types.upsert`, the second in `entities.upsert`,
  the third in whichever call carried the bad argument — and an agent
  that treats them as one code retries the wrong call.

**Views — taught completely, because the query language is where an
agent is worst.** It is JSON with named sets, and the failure mode is
silent: a query with a typo'd relation type key does not crash, it
returns fewer nodes. The page teaches the envelope (`v`, `from`,
`traverse`, `nodes`, `edges`, `project`, `limits`, `params`), the `@`
sigil, the operator table typed against declared field types, the
one-hop `{"related": …}` attribute reference, and — most importantly —
the **compose loop**: `views.validate` before `views.upsert`, always.
The views spec's own definition of done assumes an agent that "calls
`views.validate` twice to fix its own mistakes"; the bundle is what
makes that the reflex rather than an accident.

**Analysis — taught at declaration time, not at analysis time.** The
critical fact is not how to call `analysis.cycles`; it is that
`analysis_traits` belong on the `relation_types.upsert` that creates the
type, months before anyone runs an analysis. `reference/analysis.md`
leads with the trait vocabulary and the resolution ladder (caller
override → declared traits → mapped from `semantic_role` →
`semantics_undeclared`), and only then covers the three analyses and
routes. The page states plainly that `semantics_undeclared` is what a
game with no declarations gets, and that this is deliberate: an
analysis engine that had nothing to read must never answer "no problems
found".

**Documents — taught briefly, one page.** The tool surface is small and
the concurrency flow is the one the agent already knows from entities.
The page's real content is one line borrowed from the markdown spec and
worth its space: *if a query, a view or the analysis engine would ever
need to read inside it, it does not belong in a document.* Plus the trap
that `docs.write` with `links` **replaces** the link set and without
`links` leaves it untouched.

**Errors — taught as recovery, not as a list.** `reference/errors.md` is
a table of code → what actually happened → the next call to make.
`version_conflict` → re-read, merge, retry with the returned version.
`schema_violation` → the response carries `fields.<key>` paths for the
row that failed; fix all of them in one pass, because the validator
reports every problem at once and an agent that fixes one at a time pays
a round-trip per typo. `invalid_schema` → a *different* code with the
same one-pass property, carrying `field_schema[<i>]` paths, and fixed by
re-declaring the type rather than by editing the row. `invalid_input` →
a **third** code with the same one-pass property, and the one an agent
is likeliest to meet first: the call's own arguments are malformed, at
paths that name the argument (`key`, `label`, `label_plural`,
`description`, `color`, `icon`) rather than a field inside it. Fix the
argument and re-issue the same call — do not go looking at entity
values, which is where `schema_violation` sends you and is why these are
three codes and not two. The respelling refusal of §10.5 is an
`invalid_input` too, and its message carries both spellings.
`semantics_undeclared` → the error itself carries what to declare; read
it rather than guessing. `query_stale` → the design moved under the
view; repair with `views.upsert`, do not switch to `on_stale:
best_effort` to make the error go away.

## 4. Teaching judgement, not syntax

This is the part that justifies a sub-project. `modelling/` is three
pages, and they are the only pages in the bundle a later contributor
must not compress.

### 4.1 The four laws

Stated in `skill.md`, argued in `modelling/deciding.md`:

1. **A reference to another entity is a relation. Always.** There is no
   reference field type, and this is not an omission.
2. **Data that belongs to the connection goes on the relation.** Edges
   carry their own typed fields.
3. **Declare the direction when you declare the relation type**, and
   name the type so the direction is readable. Queries take
   `direction` literally; nothing infers it.
4. **Declare `analysis_traits` at the same moment you declare the
   relation type.** Not later. *Contingent on open question O1 in the
   core spec: `analysis_traits` does not exist in the committed schema
   and the analysis spec proposes it. If O1 lands on "no traits", this
   law becomes the same instruction about `semantic_role`, and §4.5 is
   rewritten rather than dropped — the point is that the analytical
   meaning of an edge is declared with the edge type, whatever the
   column ends up being called.*

### 4.2 Decision 1 — is this a field, or a relation?

The rule the page gives: **if any other entity in this game is, or could
plausibly become, the same thing this value names, it is a relation.**

- `min_level: number` on a quest is a field. `22` is a number, not a
  thing the game will ever have a row for.
- `zone: "elwynn"` on a quest is a **relation**, `takes_place_in`, and
  the fact that it is spelled like a string is the trap.

Worked, with the cost stated:

> **Before.** The agent declares `quest` with
> `{"key": "zone", "type": "text"}` and seeds 300 quests with zone keys
> in that field. Every write validates. Every row looks right in a
> table. `entities.list` filtered by `zone = "elwynn"` even works.
>
> **Three hundred entities later.** A designer asks for the quest map
> coloured by zone. `traverse` cannot follow a text field — it walks
> relations. `{"related": {"via": "takes_place_in", …}}` has no relation
> type to name. `analysis.orphans` reports all 300 quests as orphans,
> because a quest with no edges *is* an orphan and the engine cannot see
> into jsonb; the analysis that exists to find half-finished content
> reports the whole game. Deleting a zone leaves 40 quests pointing at a
> key that resolves to nothing, and nothing tells anyone.
>
> **The repair** is not a migration. It is: declare `zone` as an entity
> type, seed the zones, declare `takes_place_in`, read all 300 quests
> back, write 300 relations, then remove the field — which requires
> touching all 300 rows again because an unknown field is a
> `schema_violation`, not a silently dropped key. Six calls become six
> hundred.

The page also states the inverse mistake, briefly, so the rule does not
over-apply: modelling `min_level` as a relation to a `Level` entity
gives you 60 content-free rows, a `requires_level` type that
`analysis.cycles` now has to be told to ignore, and no ability to ask
"quests between level 20 and 30" without a traversal, because `between`
is a `number` operator and there is no number left.

### 4.3 Decision 2 — one relation type with a field, or two types?

The rule: **if the two edges must be *analysed* differently, they are
two types. If they differ only in something a human reads, one type with
a field.**

This one is decided by the analysis engine's shape, and the page says so
explicitly. `analysis_traits` are declared **per relation type**. A
single `requires` type carrying `{"key": "kind", "type": "enum",
"options": ["hard", "recommended"]}` cannot be `prerequisite_of` for
half its rows and `annotation` for the other half. Declare it gating and
`analysis.cycles` reports loops through recommendations that are not
bugs; declare it inert and the real prerequisite cycles go unreported.
Two types — `requires` with `{prerequisite_of}` and `recommends` with
`{annotation}` — cost one extra `relation_types.upsert` and are correct
forever.

The counter-example, in the same page, so the rule does not become "two
types always": a metroidvania's `connects_to` carrying
`requires_ability: text` is **one** type. Both gated and ungated doors
are the same spatial edge, they get the same trait (`symmetric`), and
`edge_where` splits them at query time without a second type existing.
The difference between the two cases is exactly whether the distinction
changes the *graph semantics* or only the *selection*.

### 4.4 Decision 3 — naming keys so a view can find them later

Keys are the API. Every saved view, every route and every analysis
override references entity type keys, relation type keys and entity keys
as text. A rename does not rewrite a stored query; it makes it stale.

`modelling/naming.md` gives the rules and the reason for each:

- **Entity type keys are singular**: `quest`, not `quests`. The plural
  has a home (`label_plural`); a key that is sometimes plural makes
  every query a guess. This one is a *bundle* rule — the server accepts
  either.
- **Lower snake is the bundle's default spelling, not the server's
  rule.** The server enforces `^[A-Za-z0-9][A-Za-z0-9_-]*$` capped at 64
  on every key that addresses a row, so `quest`, `Quest` and
  `quest-line` are all legal, while `main quest 1` and `misión-01` are
  refused outright (§10.5 gives the rule and the reason it is wider than
  the field-key rule). Within one game, pick one spelling and keep it:
  keys are case-insensitively unique — `entity_types_key_key` is over
  `(project_id, lower(key))` — so `Quest` and `quest` are one key, and
  writing the second when the first is stored is **refused**, naming
  both spellings, rather than updating the stored row. A game whose own
  vocabulary capitalises its handles (`Elwynn_Forest`, `GP_Monaco`) is
  free to, and should then do it everywhere.
- **Relation type keys are verb phrases that read source → target**:
  `takes_place_in`, `available_to`, `unlocks`, `connects_to`. Because
  `direction` in a query is literal, a name that does not encode
  direction ("zone_link") forces every future query author to look the
  type up and guess.
- **Never declare a field named `name`, `key`, `type`, `invalid` or
  `created_at`.** It is legal — the validator's key rule permits all
  five — and the built-ins stay reachable through the `@` sigil, so
  nothing breaks. It just makes every predicate in the game ambiguous to
  a human reader.
- **Numbers are `number`.** A level stored as `text` cannot be
  range-filtered (`between`, `gte` are number-only), cannot rank a
  `layered` view, and cannot be a `timeline` axis. The cost is invisible
  on the day of the write and total on the day of the diagram.
- **Ordered categories are `enum` with the options in order.**
  `timeline` accepts an ordered `enum` as an axis; a `text` field of the
  same values accepts nothing.
- **Entity keys are stable and human-readable**, because upserts address
  by `(project, type, key)` and re-seeding depends on them being the
  same string next time. Derive them from something that will not change
  — not from a display name a writer will rewrite.

### 4.5 Decision 4 — traits and roles at declaration time

Retrofitting `analysis_traits` onto 30 relation types after 400
relations exist means re-deriving, from memory, what each type meant.
The page's instruction is blunt: `relation_types.upsert` without
`analysis_traits` is an incomplete declaration, and the only correct
reason to omit them is that you genuinely do not know yet — in which
case say so to the human rather than defaulting.

It also teaches the two things an agent will otherwise get wrong:

- `annotation` is not the same as declaring nothing. It is the
  declaration that stops `is_illustrated_by` from making every
  illustrated quest look connected when computing orphans.
- Passing `relation_types` explicitly on every `analysis.*` call is the
  **escape hatch**, not the workflow. It puts the semantics in the
  caller, and the analysis spec names the consequence: every agent
  invents its own list, the lists drift, and two people get two answers
  about the same game.

### 4.6 Decision 5 — field, `longtext`, or document?

One paragraph, one rule, borrowed verbatim from the markdown spec: if a
query, a view or the analysis engine would ever need to read *inside*
it, it does not belong in a document. `longtext` is for prose short
enough to sit in a table cell. A quest's objective summary is a field; a
quest's dialogue script is a document linked with `role: "script"`.

## 5. Genre templates

### 5.1 What ships

Each genre ships as **two files**: a prose page that narrates the
modelling decisions, and a machine-readable transcript of the calls that
build it.

- `genres/mmorpg.md` + `genres/mmorpg.json`
- `genres/racing.md` + `genres/racing.json`
- `genres/metroidvania.md` + `genres/metroidvania.json`

The transcript is an ordered list of **ordinary tool calls** —
`types.upsert`, `relation_types.upsert`, `entities.upsert`,
`relations.upsert`, and one or two `views.upsert` — with their exact
argument payloads. It is not a file format. It is what the agent would
have typed, written down, so it can be applied verbatim through the
public surface or read as an example and adapted.

Scale: each transcript declares the genre's full type vocabulary and
seeds roughly **a dozen entities per type** — enough for every relation
type to have edges, for a view to draw something, and for
`analysis.cycles` to have a graph. Not more.

Coverage per genre, as the user named them:

- **MMORPG** — `class`, `race`, `profession`, `zone`, `dungeon`,
  `quest`, `talent`, `faction`; `connects_to`, `takes_place_in`,
  `requires`, `available_to`, `rewards`. The page's own hard problem:
  talent trees, where `requires` gates within a tree and a
  `layered` renderer wants a rank.
- **Racing career** — `driver`, `car`, `upgrade`, `circuit`, `race`,
  `championship`, `licence`; `unlocks`, `requires`, `contains`,
  `takes_place_in`. Hard problem: a championship *contains* races and
  a licence *unlocks* them, and those are two different traits on two
  types that both feel like "belongs to".
- **Metroidvania** — `room`, `ability`, `boss`, `item`; `connects_to`
  carrying `requires_ability` on the edge. Hard problem: the gate lives
  on the edge, and this is the genre that exists in the bundle to prove
  it.

### 5.2 Why a transcript, and what was rejected

**Rejected: a server-side `templates.apply` tool.** This is the fastest
path to a designer having content, and it is the wrong product. Three
reasons, in increasing order of seriousness. It creates a second write
path into the metamodel that does not go through the tool surface's
validation, versioning and isolation discipline — the exact class of
thing the metamodel plan's Task 7 preamble spends a page insisting on.
It gives the shipped vocabulary a lifecycle: a version, a migration
story, a bug tracker, and eventually a user who filed an issue because
the MMORPG template does not have `Mount`. And it makes the genre
vocabulary **a feature of the server**, which is the built-in domain
this whole product exists not to have.

**Rejected: prose only.** An agent copying a 40-line field schema out of
a markdown code fence retypes it, and retypes it slightly wrong. The
machine-readable twin costs a few kilobytes and removes a whole class of
transcription error — and, more valuably, it is the thing a test can
execute (§8.3).

**Rejected: full seeded games.** Hundreds of entities per genre would
make the bundle megabytes, would make the examples read as *content to
keep* rather than a shape to learn from, and would end with somebody's
shipped game containing Hogger.

### 5.3 The boundary — what stops a template from being built-in vocabulary

This is the delicate part and it needs to be an invariant, not an
intention. A shipped "MMORPG template" is built-in vocabulary by another
name unless four rules hold.

**B1 — No server code ever reads a genre file.** No handler, no
migration, no validator branch, no MCP tool named after a genre or a
genre's concepts. Genre files are inert data inside the skill bundle,
reachable only by an agent reading them. This is testable and §8.4 makes
it a test.

**B2 — A template is applied by the agent, through the same tools any
designer would use, one call at a time.** There is no privileged import
path. Anything a template can do, an agent that never read one can do
with the same calls. The corollary the page states out loud: *if
applying a template is ever faster than doing it by hand for reasons
other than typing, something has been built that should not exist.*

**B3 — A genre page's product is the reasoning, not the vocabulary.**
Each page is required to carry at least one **"here we deliberately
differ"** note, naming a modelling decision it took differently from the
other two genres and why. The racing page contains no talent trees; the
metroidvania page puts a gate on an edge where the MMORPG page puts it
on a relation to a class. If the three genres ever agree on everything,
they have collapsed into one built-in domain and the bundle is lying
about genericity — that convergence is the failure signal to watch for.

**B4 — Nothing in the bundle is required.** The definition of done (§9)
includes seeding a **fourth genre nobody wrote a page for** — a city
builder, a deckbuilder, a farming sim — from `skill.md` and `modelling/`
alone. If that fails, the vocabulary has been smuggled into the
templates and the templates are load-bearing.

Anti-ossification, stated as a rule for future contributors: **a genre
page may never be cited as justification in a code review of
`internal/`.** "The MMORPG template needs this" is not an argument for a
server change. If a genre cannot be expressed, the gap is in the
metamodel and belongs in a metamodel spec, where it will be argued
generically or not at all.

## 6. Recipes

Four composed workflows, each a page, each ending in a state an agent
can verify:

- **`seeding-a-game.md`** — from an empty game to a few hundred
  entities. Types first, relation types with traits second, entities in
  `atomic` batches third, relations fourth. Includes the honest warning
  from the analysis spec that a game mid-seed is *supposed* to look like
  a field of orphans, and that running `analysis.orphans` before the
  edges are written is a self-inflicted wound.
- **`joining-a-game.md`** — arriving at content someone else seeded.
  `types.list`, `relation_types.list`, then a small `entities.list` per
  type to see real values before writing anything. This recipe is the
  one most affected by an orientation gap in the current surface (§10.4).
- **`composing-a-view.md`** — the validate loop. Start from the nearest
  worked example in `genres/`, `views.validate`, read the JSON pointer in
  `query_invalid`, fix, validate again, then `views.upsert`. Use
  `views.run` with an inline query for a one-off question; save a view
  only for something a designer will return to.
- **`auditing-a-design.md`** — traits are declared, so run
  `analysis.cycles`, then `analysis.unreachable` with a real seed set,
  then `analysis.orphans` with `ignore_entity_types` for the decorative
  types. Report findings to the human; do not repair a design without
  being asked. A cycle is frequently a wrong trait declaration rather
  than a wrong design, and the bundle says to check that first.

## 7. Delivery, installation and versioning

### 7.1 Delivery follows Nottario exactly

Nottario is the working precedent and there is nothing to improve here:

- The bundle is `//go:embed`-ed into the binary under
  `internal/skill/files/`.
- An MCP tool, `skill.install`, returns a small JSON descriptor:
  `download_url`, `format: "zip"`, `bundle_version: "sha256:…"`, and an
  `install` block naming `preferred_dir`
  (`<workspace>/.claude/skills/maestro`), `fallback_dir`
  (`~/.claude/skills/maestro`) and prose instructions.
- The agent fetches the zip with its own HTTP tool and extracts it. **The
  bundle bytes never pass through the MCP response** — only the URL and
  the descriptor do. This is the whole reason the mechanism exists: a
  20k-token bundle inlined into a tool result is a bundle that costs its
  full price on every install.
- `bundle_version` is a stable sha256 over the resolved bundle, stashed
  next to the installed files in a small manifest; a matching hash means
  skip the download.
- The install instructions must tell the human, explicitly, that their
  agent runtime reads skills at session start and needs restarting. The
  agent cannot infer this and the human cannot infer it from tool
  output.

### 7.2 The signing key Maestro does not have

Nottario signs the `/skill.zip` URL with an HMAC over an expiry,
**keyed by the session signing key**, so the agent can fetch with a
plain HTTP tool and no `Authorization` header.

Maestro has no such key. Core's Task 22 deleted `SESSION_KEY` on the
grounds that it was a phantom control — sessions in this product are
random tokens in a table and nothing signed anything. That deletion was
correct and this is the first thing that wants a key back.

Three options, none free:

- **(a) Mint a random 32-byte key at process start, held in memory.** A
  five-minute signed URL has no reason to survive a restart, and a
  multi-replica deployment is not something Maestro claims to support
  today. Zero configuration, zero operator burden, and it cannot become
  a phantom control because it is generated by the code that uses it.
- **(b) Re-introduce a configured key.** Now with a real job — but it
  reintroduces the exact environment variable Task 22 removed, and an
  operator who has read the readme's history will reasonably ask why.
- **(c) Drop signed URLs; serve `/skill.zip` behind the ordinary
  `Authorization: Bearer` header.** Simplest, and it puts the game token
  into whatever `curl` invocation the agent constructs — which is a
  token in a shell command in a transcript.

**Recommended: (a)**, with (c) documented as the fallback the day
Maestro grows replicas. Recorded as an open question (§11.1) because it
touches configuration and that is the user's call.

### 7.3 The version handshake

The descriptor carries the server's version alongside `bundle_version`,
and the installed manifest records both. `skill.md`'s first section
tells the agent: if `whoami` reports a server version different from the
one in the manifest on disk, re-run `skill.install` before doing
anything else.

This is belt and braces on purpose. The tests in §8 keep the bundle
honest against the server **inside the repository**. The handshake keeps
a bundle that was installed three months ago honest against a server
that has moved since. Neither covers the other.

### 7.4 Overrides: not in v1

Nottario lets an instance override any bundle file by writing a
`kind=skill` document at `global/skills/<path>` with `scope=global`;
overrides are transparent to `skill.install` and change
`bundle_version`.

Maestro's markdown domain has **no global scope**. Every document is
`project_id NOT NULL`, and the isolation invariant is the point. An
override would therefore be per-game, which is arguably the better
shape — one instance, two games, two house styles — and which works
because tokens are per-game anyway, so `bundle_version` being per-game
is coherent.

**Decision: ship embedded-only.** No override mechanism in v1. Nobody
has asked for one, and an extension point whose first user is its author
is designed against a guess. The per-game shape is recorded here as what
it would be if it is ever wanted; the resolution order and the
`bundle_version` semantics carry over from Nottario unchanged.

## 8. Keeping the bundle honest

A bundle documenting a tool that no longer exists is worse than no
bundle: it is confidently wrong, and an agent has no way to tell. This
is not hypothetical here. The Core sub-project's final task existed to
repair exactly this class of defect — a documented admin-recovery path
that did not work, and a configuration variable that controlled nothing
— found not by a code sweep but by someone trying the documented remedy
against a running instance and watching it fail.

So the answer is mechanisms, in three classes, plus one thing that
cannot be mechanised.

### 8.1 Tool-surface drift → generation

`reference/tools.md` is **generated**, not written. Source of truth is
the registered `mcp.Tool` table — the same registrations
`addScopedTool` consumes — so a tool cannot be added, renamed or removed
without the generated file moving. The file is committed (agents read it
from a zip, not from a build), and `make check` gains:

```
skill-check:
	$(GO) test ./internal/skill/ -run 'TestToolReference|TestBundleVocabularies|TestGenreTranscripts'
```

`TestToolReferenceIsCurrent` regenerates in memory and diffs against the
committed file. This is precisely the `sqlc diff` pattern already in
`make check`: generated artefact committed, test fails when the source
moved and the artefact did not.

### 8.2 Vocabulary drift → a golden test over delimited blocks

Prose cannot be generated and should not be. But every *closed
vocabulary* the prose enumerates already has exactly one Go declaration
that is its source of truth:

| Vocabulary | Source of truth |
|---|---|
| field types | `metamodel.FieldText …` in `internal/metamodel/schema.go` |
| error codes | the `Err*` vars in `internal/metamodel/errors.go` and the surface's own set |
| analysis traits | the trait constant slice and the `relation_types_traits_vocab` check |
| `semantic_role` values | the column's `CHECK` |
| renderer names and their required params | the renderer catalogue |
| query operators, per field type | the operator table in the query compiler |

Each bundle page carries those enumerations inside an explicitly
delimited, machine-readable block:

````
```vocab:field_types
text longtext number bool enum list<text>
```
````

`TestBundleVocabulariesMatchCode` parses every `vocab:` block in the
bundle and asserts **set equality** against the Go declaration it names.
Adding `list<number>` to the validator and not to the bundle fails the
build; removing `semantic_role` and leaving it documented fails the
build.

Rejected: **checking the prose**. Not possible, and attempts produce a
test that fails on rewording.

Rejected: **generating whole pages**. The judgement pages are the
product, and generated prose is bad prose. The block delimiters exist so
that the checkable part and the human part can live on the same page
without either constraining the other.

### 8.3 Example drift → execute the transcripts

`TestGenreTranscriptsApply` runs each `genres/*.json` against an
ephemeral database through the real handlers: every call must succeed,
and every `views.upsert` / `views.validate` in the transcript must come
back clean. A worked example that no longer applies is the single worst
page in the bundle, because it is the one an agent copies verbatim
without reading.

This test also means the transcripts are integration coverage the
project would otherwise have to write by hand — the metamodel plan's
Task 9 already proves the shape works at 200 rows; the transcripts prove
it works across five surfaces at once.

### 8.4 Boundary drift → grep as a test

`TestNoGenreVocabularyInServerCode` asserts that no identifier or string
literal outside `internal/skill/files/` and the tests names a genre
concept. A small denylist — `quest`, `zone`, `dungeon`, `circuit`,
`championship`, `metroidvania`, `mmorpg` — is enough, because the
failure mode this guards against is not subtle: it is somebody adding a
convenience for a shape the templates made feel official.

Crude, and it will produce a false positive one day on an unrelated
word. That is acceptable: the alternative is B1 as a promise, and §8's
whole premise is that promises are what fail.

### 8.5 What cannot be mechanised

Judgement pages can go stale in ways no test sees: advice that was right
when it was written and is now merely survivable. The only mechanism is
a rule, and the rule is that **a spec that changes a modelling
consequence names the bundle page it invalidates** — the way the
analysis spec's §11 names the core spec's `semantic_role` sentence as
needing amendment. Cheap, and it works exactly as far as people follow
it, which is why it is listed here as the residue rather than as a
mechanism.

### 8.6 Ship order

The bundle spans sub-projects 2, 3, 4 and 6, and 3, 4 and 6 are specs,
not code. The rule: **a page describing an unimplemented surface must
not exist.** Not as a stub, not as "coming soon". An empty
`reference/queries.md` is honest; a promised one is the defect this
section exists to prevent. The bundle therefore ships in slices, one per
sub-project as it lands, with `skill.md`'s routing table shrinking to
match. Sub-project 7 in the roadmap is the slice that completes it and
adds `genres/` and `recipes/`, not the slice that starts it.

## 9. Testing and definition of done

Tests, all in `internal/skill/`:

1. `TestToolReferenceIsCurrent` — §8.1.
2. `TestBundleVocabulariesMatchCode` — §8.2, one sub-test per vocabulary.
3. `TestGenreTranscriptsApply` — §8.3, against an ephemeral database.
4. `TestNoGenreVocabularyInServerCode` — §8.4.
5. `TestBundleVersionIsStable` — the same bundle hashes the same twice;
   a one-byte change moves it. Inherited from Nottario's
   length-prefixed hash.
6. `TestEntryPointFitsBudget` — `skill.md` is at most 170 lines. A
   budget nobody enforces is a budget that becomes 325 lines.

Definition of done, three claims, each demonstrated rather than asserted:

- **The taught case.** From a clean MCP client with only the bundle
  installed, an agent declares an MMORPG's types with their analysis
  traits, seeds three hundred entities and their relations, composes the
  "quests a Mage can reach between level 20 and 30, coloured by zone"
  view in at most two `views.validate` iterations, saves it, and runs
  the three analyses without hitting `semantics_undeclared`. A designer
  opens the view in a browser and it is correct.
- **The untaught case (B4).** The same agent, given a genre with no page
  — a city builder — produces a type vocabulary that a human reviewer
  judges sound, with references as relations, edge data on edges, and
  traits declared. This is a human-judged bar and it is stated as one
  deliberately; automating it would test something else.
- **The honesty case.** Renaming any MCP tool, adding any field type,
  or adding a trait to the vocabulary fails `make check` until the
  bundle is updated.

## 10. Problems in the surface as committed that the bundle has to work around

A spec that surfaces a real problem beats one that papers over it. Four,
in descending order of how much they cost an agent.

### 10.1 Re-seeding is not as idempotent as advertised

The core spec says an agent re-running its seeding script "creates no
duplicates". True about rows, and misleading about the experience. The
metamodel plan's own Task 9 asserts the opposite of what an agent
expects:

> a re-seed without `expected_version` must conflict on every row

Re-running a 200-item payload produces 200 `version_conflict` failures.
There is no `on_conflict: skip`, no `if_not_exists`, and no bulk
read that returns key → version cheaply enough to precede a 500-row
write without a page walk.

So the bundle must teach a workaround: seed with `atomic`, **keep the
versions the response returns**, and on any re-run first `entities.list`
the type with a large limit and build a key → version map before
writing. That is several extra calls and one extra piece of state an
agent must carry across a session boundary it frequently does not
survive.

A workaround in a teaching document is debt. **Recommendation, aimed at
the metamodel and not at this sub-project:** `entities.upsert` and
`relations.upsert` gain `on_conflict: "fail" | "skip" | "overwrite"`,
default `"fail"` so nothing changes for existing callers. `"skip"` makes
a re-seed genuinely idempotent; `"overwrite"` makes a corrective re-seed
one call.

**This finding was verified and accepted.** The core spec's
"Idempotency" section has been amended to say that idempotency here
means row identity and not a conflict-free re-run, and the
recommendation above is recorded there as **open question O2** — the one
place it is stated, because it is a change to the core spec's MCP
surface. Compare `2026-09-02-markdown-domain-design.md` §7, which
spells a create as `expected_version: 0`; if the metamodel adopts that
shape instead, this section's workaround becomes a two-line rule rather
than a page.

### 10.2 Two overlapping semantic vocabularies

`relation_types` carries `semantic_role` (six values, in the shipped
`CHECK`) and will carry `analysis_traits` (seven values). The analysis
spec keeps the first as descriptive metadata and as a fallback mapping,
and its own open question 3 asks whether it survives at all.

The bundle would have to teach both, explain that one is a compatibility
fallback for the other, and explain that `ordering` and bare `acyclic`
are unreachable through the fallback. That is two vocabularies for one
job and it is precisely the kind of thing that makes an agent choose
wrong — and then choose wrong consistently, across thirty relation
types. The bundle cannot fix this; it can only document it at length,
which is the tell.

**Recommendation:** decide before the bundle is written. If
`semantic_role` survives, the bundle teaches traits only and treats the
role as a human-readable label with no analytical meaning.

**This finding was verified and accepted.** The question is now stated
in exactly one place — **open question O1** in
`2026-08-31-core-and-metamodel-design.md`, "Metamodel" — because
`semantic_role` is that spec's column, and because the analysis spec's
§2D and its own open question 3 were posing it twice more. The analysis
spec's trait vocabulary is now marked as a *proposal* contingent on that
answer, rather than as chosen. §11.3 no longer poses the question
separately; it names what blocks on it.

### 10.3 `list<text>` is the only list type

`internal/metamodel/schema.go` declares exactly one list type. The views
spec's operator table originally spelled that row `list<T>`, with
`contains_any`, `length_gte` and friends, which read as though
`list<number>` and `list<enum>` exist. They do not; that table now says
`list<text>`, so the two documents no longer disagree — but the gap the
disagreement pointed at is real and is the subject of this section.

An agent modelling "reward credit amounts" or "allowed difficulty tiers"
gets `list<text>` and loses every number and enum operator, plus the
validation that would have caught `"medum"`. The workarounds — separate
entities, or a `list<text>` plus discipline — are both worse than the
field type. Small, cheap to add, and worth adding before the bundle
teaches around it.

### 10.4 There is no cheap way to learn a game's shape

An agent arriving at an existing game has `types.list` and
`relation_types.list`, which return schemas but not shape: no entity
counts per type, no edge counts per relation type, no sample row. So the
`joining-a-game.md` recipe costs one call per type just to find out
which types are actually populated, and an agent will routinely spend
its first ten calls orienting.

A `games.get` that returned per-type entity counts and per-relation-type
edge counts would collapse that to one call, and it is information the
database already has cheaply. Recorded as a recommendation rather than a
requirement; the bundle works without it, just expensively. Open
question §11.5.

### 10.5 Two key rules, and why they are not one

**Resolved.** When this section was first written the surface checked
row keys for non-emptiness alone, and it asked for a decision before a
real game was seeded. The decision was taken in the entity-type work and
is now enforced; what follows is the rule, not a question.

There are two rules, deliberately:

| what it addresses | rule | cap |
| --- | --- | --- |
| declared **field** keys (inside `field_schema`, and inside an entity's `fields`) | `^[a-z][a-z0-9_]*$` | 64 |
| keys that address **rows** — entity type keys, relation type keys, entity keys | `^[A-Za-z0-9][A-Za-z0-9_-]*$` | 64 |

They differ because the mechanism behind each differs. A field key lives
inside a jsonb object, which has no case folding at all: `Level` and
`level` would be two distinct keys in one row and nothing downstream
would ever catch the collision, so that rule has to forbid case itself.
A row key cannot produce that collision — every uniqueness index over
these keys is `UNIQUE (project_id, lower(key))`, so the database folds
case for them. Both rules therefore deliver the same guarantee, *no two
keys differ only by case*, through the mechanism each context actually
has.

Forbidding capitals in row keys as well would buy nothing and cost the
thing the folding index was chosen for: a game's handles are the game's
own vocabulary, and `Hogger`, `Elwynn_Forest` and `GP_Monaco` should be
spellable the way the design documents spell them. A leading digit is
allowed for the same reason — `1999_season`, `500_miles`.

What the row-key rule does exclude earns its place: no dot, slash, space
or percent, so a key drops into a REST path segment and a view-query
token without escaping; no leading punctuation, so no key reads as a
flag or a relative path; ASCII only, because `lower()` folds case but
not Unicode normalisation form, so two normalisations of one accented
word would otherwise coexist as two keys. That last one settles the
language question open question 8 raises: `mision` is a legal key and
`misión` is not, in *both* rules, and the reason is the index rather
than a preference for English.

**What an agent must teach around.** A write whose key matches a stored
key only case-insensitively is refused — not silently applied to the
existing row — with a message naming both spellings:

```
key: "hogger" already exists here spelled "Hogger", and keys are matched
without regard to case: use "Hogger" to update it, or pick a key that
differs by more than capitalisation
```

The recovery is to re-issue the call with the stored spelling, or to
choose a key that differs by more than capitalisation. A re-seed that
spells its keys the same way every run never meets this, which is the
whole population of correct callers — but a bundle that teaches
re-seeding has to say so, because "the second run used a capital" is
otherwise an inexplicable failure. The error arrives as `invalid_input`
at path `key` — not `schema_violation` and not `invalid_schema`. The
caller is not declaring a schema, so it is not `invalid_schema`; and it
is not a row of values failing a declaration that stands, so it is not
`schema_violation` either. Every problem with a row's *own arguments* —
its key, its labels, its description, its colour, its icon — carries
`invalid_input`, and the recovery is to change that argument and re-issue
the same call. `reference/errors.md` must carry all three, because
`schema_violation` sends an agent to fix entity values, which is
exactly the wrong move for a key with a space in it.

### 10.6 The descriptive columns are validated too

Committed in `internal/metamodel/descriptors.go`, and inherited
unchanged by entity, relation-type and relation writes, so the bundle
teaches it once:

| column | rule |
| --- | --- |
| `label` | **required**, at most 200 characters |
| `label_plural` | optional, at most 200 characters |
| `description` | optional, at most 4000 characters |
| `color` | optional; a CSS hex colour and nothing else — `#c13`, `#c13f`, `#c41e3a` or `#c41e3a80` |
| `icon` | optional; a lower-case icon *name*, `^[a-z0-9][a-z0-9_-]*$`, at most 64 characters |

Caps are counted in **runes**, not bytes, so an accented label is not
shorter than an unaccented one.

`label` is required because listings order by it: an unlabelled type
sorts to the front of every list a designer sees and names itself
nothing. `color` refuses named CSS colours and `rgb()`/`hsl()` because
Maestro's own renderers derive contrasting text and border tones
arithmetically from the channels, and a form they cannot decompose is a
colour some views honour and others silently drop; all four hex forms
decompose, alpha included. `icon` is a *name* — a handle into whichever
icon set the frontend ships — not an image, not markup, and (for now)
not an emoji, which would make the column two things at once. The
pending visual-identity spec owns the emoji question and the choice of
set; the rule permits both `scroll-2` and `local_fire_department` so it
does not pre-commit that spec to one naming convention.

Every one of these problems arrives as `invalid_input` at the column's
own path (`label`, `color`, `icon`, …), reported **together** with any
key problem in the same response: the validator makes one pass, so an
agent fixing a seed script sees the key and the colour in one answer
rather than paying a round trip per typo.

## 11. Open questions

1. **The zip signing key.** Nottario signs with the session key; Core
   Task 22 deleted Maestro's. Recommendation: an ephemeral in-memory key
   minted at start-up (§7.2 option a). Needs the user's call because it
   touches deployment.
2. **Overrides.** Ship none in v1 (§7.4), or build the per-game document
   path now? Recommendation: none. The counter-argument is that adding
   an extension point later means the first user is already forked.
3. **Does `semantic_role` survive?** Moved: the question is now stated
   once, as **open question O1** in
   `2026-08-31-core-and-metamodel-design.md`, "Metamodel". What blocks
   on it here: `reference/analysis.md` cannot be written, and neither
   can §4.1's fourth law or §4.5, since all three are about what an
   agent declares on `relation_types.upsert`. **Do not answer it here.**
4. **`on_conflict` on the bulk upserts** (§10.1). Moved: **open
   question O2** in the core spec, "Idempotency". What blocks on it
   here: `recipes/seeding-a-game.md` is either two paragraphs or two
   pages depending on the answer, and the read-then-write loop is the
   only part of the bundle that asks an agent to carry state across a
   session boundary. **Do not answer it here.**
5. **A shape-summary on `games.get`** (§10.4). Worth it, or premature?
6. **Is the fourth-genre acceptance test worth its cost?** It is the
   only anti-ossification mechanism proposed here, and it is
   human-judged, which makes it slow and slightly subjective. The
   alternative is trusting B1–B3, which are enforceable but only cover
   the code, not the teaching.
7. **Should there be a `skill.read` MCP tool?** Nottario ships both
   install-to-disk and read-over-MCP (`skill.list` / `skill.read`).
   Install-only is cheaper and keeps bundle bytes out of every context
   window; a host that cannot write files to a skills directory has no
   way in. Recommendation: install-only for v1, add `skill.read` the
   first time a real host needs it.
8. **Key language.** All Maestro artefacts are English, and this spec and
   the bundle are. But an entity type key is *the game's* vocabulary, not
   Maestro's — a Spanish studio may reasonably want `mision` and
   `personaje`. The bundle should probably say the choice is the game's
   and only ask for consistency within one game. Confirming that is the
   user's call, since it is the one place the language policy touches
   user data rather than artefacts. Question 9, now closed, has already
   settled half of it and narrowed what is left: the committed key rules
   are ASCII-only, so `mision` is a legal key and `misión` is not, in
   both of them. That is a consequence of the case-folding index rather
   than a preference for English (`lower()` folds case, not Unicode
   normalisation form), but it lands on a Spanish studio all the same.
   What remains open is only whether the bundle should say the choice of
   language is the game's — it should — and how it phrases the accent
   restriction without reading as a policy about language.

9. ~~**One key rule or two?**~~ **Closed: two, and the split is the
   answer.** (§10.5.) Declared field keys are `^[a-z][a-z0-9_]*$` capped
   at 64; entity, entity-type and relation-type keys are
   `^[A-Za-z0-9][A-Za-z0-9_-]*$` capped at 64, and a spelling that
   differs from a stored key only by case is refused. The two rules are
   not an inconsistency: jsonb has no case folding, so the field-key
   rule must forbid case itself, while the row-key uniqueness index
   folds case in the database. Both therefore guarantee that no two keys
   differ only by case. Decided while the entity-type surface was
   implemented, which is before any real game was seeded, as this
   question asked for. It also answers the half of question 8 that hid
   inside it: `misión` is illegal under both rules, because `lower()`
   folds case and not normalisation form, so two normalisations of one
   word would otherwise be two keys.

## 12. What this sub-project deliberately does not do

Drawn explicitly so a later contributor does not drift across it.

- **It does not teach game design.** Nothing in the bundle says what
  makes a good quest, how long a talent tree should be, or how to pace a
  progression. It teaches how to record a design, not how to have one.
- **It does not ship game content.** The genre transcripts are examples
  at the scale of a dozen entities per type. No designer should end up
  shipping anything from them.
- **It adds no genre-specific server behaviour.** No `templates.apply`,
  no handler that reads a genre file, no tool named after a game
  concept. §5.3 B1–B4 and the test in §8.4 are the enforcement.
- **It does not replace tool descriptions.** Each MCP tool's own
  description stays sufficient for that single call. The bundle covers
  what spans calls: sequence, judgement, and the consequences a single
  tool description cannot see.
- **It does not teach the web UI, the visual identity or renderer
  appearance.** Sub-project 5 owns all of that; the bundle names
  renderers and their required parameters and stops there.
- **It carries none of Maestro's own development workflow.** Maestro is
  built against Nottario's bundle and tracked in Nottario. Maestro's
  bundle must never mention tasks, cycles or commits — the two bundles
  will sit side by side in `.claude/skills/` on this author's machine
  and any overlap between them is a bug.
- **It does not version per game, and it does not branch per host.** One
  embedded bundle, one hash, installed the same way everywhere.
