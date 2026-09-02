# Maestro — the markdown domain (design)

Date: 2026-09-02
Status: draft, open questions marked
Scope: third of seven sub-projects (roadmap item 3 in
`2026-08-31-core-and-metamodel-design.md`)

> **Reconciled against implementation, 2026-09-02.** Changed:
>
> - §2 — the committed migration is `0004_metamodel.sql`, not
>   `0003_metamodel.sql`.
> - §2 and §6 — the new tables' foreign keys to `entities`, `documents`
>   and `api_tokens` must follow the committed migration's composite-key
>   convention. "No change to the metamodel schema is needed" remains
>   true; "these keys as written are fine" was not.
> - §7 — `expected_version: 0` for a create is a real difference from
>   the metamodel surface, not a restatement of it, and it bears on the
>   core spec's open question O2. **Decided 2026-09-02: O2 is closed the
>   other way** — the metamodel's bulk upserts gain
>   `on_conflict: "fail" | "skip" | "overwrite"` rather than adopting
>   this spec's convention, so the difference is permanent and
>   deliberate. `docs.write` keeps `expected_version: 0`.
>
> Unchanged and confirmed correct against the committed migration: the
> observation that `entities.search` is a plain `tsvector` column rather
> than `GENERATED … STORED`.

## 1. What this sub-project is

The core spec settled that **long prose lives in a versioned markdown
store linked to entities, not in entity fields** (decision 4). This
spec says what that store is.

The material is the writing a game design produces and a field schema
cannot hold: a quest's dialogue script, a zone's lore, a character's
backstory, a faction's history, the tone guide the whole team writes
against. It is long, it is edited repeatedly, it is edited by several
people and agents at once, and *what it said last month* is a question
designers ask constantly. Fields answer none of that.

Two properties define it and everything below follows from them:

1. **It is attached to game content.** A script belongs to a quest; a
   backstory belongs to a character. Opening a quest in the UI must
   show its prose, and an agent asked to rewrite the Hogger dialogue
   must find it from the quest, not by guessing a filename.
2. **It is versioned.** Every save is a snapshot with an author and a
   message. Nothing is ever silently overwritten and nothing is lost.

### What it is deliberately not

Named up front, because this is the sub-project most likely to grow
sideways.

- **Not a wiki.** No `[[wikilinks]]`, no backlink index, no page
  hierarchy with inherited semantics, no per-page discussion. Links
  between things in Maestro are *relations between entities*; that rule
  is the reason view queries work at all (core spec, "Field schemas"),
  and a second, parallel, invisible link graph living inside prose
  would break it.
- **Not a CMS.** No editorial workflow, no draft/review/publish states,
  no scheduling, no media library, no binary uploads. A document is
  text; it is either the current text or a past version of it.
- **Not a general document store.** Documents are scoped to a game and
  are about that game's design. There is no instance-global document
  scope (Nottario has one; see §3), no "my notes" area, no file tree to
  be used as a personal drive.
- **Not a branching-dialogue engine.** A conversation tree with
  conditions and outcomes is a graph, and the core spec already ruled
  that graphs are entities plus relations. A `.md` script holding the
  *written lines* is prose; the *structure* of a branching conversation
  is metamodel content. If a project wants both, it gets both, and the
  script document attaches to the dialogue-node entities.
- **Not a place to smuggle structured data.** Frontmatter exists (§6)
  and is deliberately inert: Maestro never interprets it, never
  validates it against anything, and never derives links or fields from
  it. Anything the system must understand is an entity field or a
  relation.

The line: **if a query, a view or the analysis engine would ever need
to read inside it, it does not belong in a document.**

## 2. Attachment — the decision

**A document is a first-class row, and attachment is a separate
many-to-many link table.**

```
documents          (project_id, path, kind, title, body_md, …)
document_links     (project_id, document_id, entity_id, role)
document_versions  (project_id, document_id, version, body_md, …)
```

A document exists on its own terms, addressed by a `path` unique within
the game (`lore/duskwood/history`, `scripts/wanted-hogger`). It may
link to zero entities, one, or several. Attachment carries an optional
free-text `role` (`script`, `lore`, `backstory`) so a UI can group the
documents shown on an entity page.

Why:

- **Zero entities is a real case.** The game bible, the tone guide, the
  naming conventions for Draenei settlements, the "how magic works in
  this world" note. These are the documents a designer writes first,
  before a single entity exists, and they never stop being edited.
- **Two entities is a real case.** One piece of lore explains why the
  Defias Brotherhood hates Stormwind: it is about a faction *and* a
  city, and forcing the author to pick one, or to copy-paste, makes the
  copy wrong within a month.
- **Versioning wants a row of its own anyway.** History is a child
  table keyed by document; a document that is merely a column on
  something else has no stable identity for that history to hang from.

### Rejected alternatives

- **A `longtext` field on the entity.** Simplest, and wrong: no
  version history, no author, no sharing between two entities, no
  document without an entity, and it drags whole prose bodies into
  every `entities.list` payload the core spec worked to keep slim.
  The core spec's `longtext` field type stays, for genuinely short
  prose ("one-paragraph summary"), and the boundary is exactly
  "does it need history?".
- **A `documents` table with a single `entity_id` foreign key.** Gets
  history and slim entity payloads, still cannot express the game
  bible or the shared lore piece. Rejected for those two cases alone;
  the link table costs one small table and a join.
- **Making documents entities of a built-in `Document` entity type,
  attached with ordinary relations.** Genuinely tempting — it would
  reuse the whole relation machinery and give link types for free. It
  is rejected because Maestro ships *no* built-in entity types; that
  purity is the product (core spec §1). A built-in `Document` type
  would appear in every project's type list, in every view query, in
  every analysis pass over "unreachable content", and would need
  versioning bolted onto `entities` for its sake alone. Documents are
  *about* game content; they are not game content.

### Consequence for the metamodel schema as committed

None. `document_links` references an entity with `ON DELETE CASCADE`,
so deleting an entity drops its links and leaves the document — which is
right: a quest is cut, its lore survives for the next quest. No change
to `0004_metamodel.sql` is needed.

The key itself must be **composite**, though:
`(entity_id, project_id) REFERENCES entities (id, project_id) ON DELETE
CASCADE`, not `entities (id)` alone. The committed migration settled
that every foreign key to a project-scoped parent carries `project_id`
so that the database, not Go, refuses a cross-game reference. §5 argues
at length that a cross-game link must be "impossible by construction,
not by review" — a single-column key makes it exactly the review-level
guarantee that section rejects, since nothing then forces
`document_links.project_id` to agree with the entity's. The same applies
to `document_versions.document_id` and `document_links.document_id`
against `documents`, which therefore needs a `UNIQUE (id, project_id)`,
and to every `*_by_token_id` against `api_tokens` — where a composite
`ON DELETE SET NULL` must name its column, `SET NULL
(created_by_token_id)`, because a bare one would try to null the
`NOT NULL` `project_id`. `*_by_user_id` stays single-column: `users` is
global. The views and analysis specs state their own DDL in the same
uncorrected shape; the convention lives in
`2026-08-31-core-and-metamodel-design.md`, "Constraints and indexes".

Worth
noting for the implementer: the committed `entities.search` is a plain
`tsvector` column, not `GENERATED … STORED` as Nottario's are, so it
needs a trigger or an application-side write. Documents (§5) should use
a generated column, and the inconsistency between the two is a small
wart the metamodel plan should probably fix rather than propagate.

## 3. Versioning

Modelled directly on Nottario's `documents` / `document_versions` pair,
which has been in production long enough to trust.

**Full snapshots, not diffs.** Every save writes a complete copy of the
body into `document_versions`. Prose documents are kilobytes; a
thousand versions of a long quest script is a few megabytes, which is
nothing next to the operational cost of a diff chain that must be
replayed to answer "show me version 12" and that corrupts every later
version if one link is wrong. Diffs are computed on read, when a human
asks to compare two versions — never stored.

**A version records:** `version` (a per-document integer starting at
1), the full `body_md`, `title`, the inert `frontmatter`, a free-text
`message` (why this edit), the author as **either** `author_user_id`
**or** `author_token_id`, and `created_at`. The core already
distinguishes the two kinds of actor everywhere it records audit; the
version table uses exactly the same pair of nullable columns, so
"rewritten by the lore agent" and "rewritten by Ana" are
distinguishable in the history view without a synthetic user per agent.

**Versions are never pruned.** No retention window, no compaction, no
`squash`. The history of the writing *is* part of the design record,
and a designer who cannot see what a mission said before the rewrite
has lost something the tool existed to keep. If a game ever
accumulates enough history to matter, that is a real finding and gets
its own decision then; guessing at a retention policy now would only
delete something irreplaceable.

**Delete is soft.** `deleted_at` on the document; history is preserved;
writing to the same path resurrects the document and continues its
version numbering. Same as Nottario, same reasoning: an agent deleting
a document by mistake is a recoverable accident, not a data loss.
Hard deletion is a database operation an instance admin performs
deliberately, not a tool.

**Revert writes forward.** `documents.revert(path, to_version,
expected_version)` creates a *new* version whose body equals
`to_version`, with an automatic message. History is append-only; there
is no state in which version 9 exists and version 8 does not.

### Where this diverges from Nottario

- **No `scope`.** Nottario documents are `project` or `global`;
  Maestro's are always project-scoped, `project_id NOT NULL`. A
  cross-game document has no meaning here — an instance hosts unrelated
  games — and the nullable `project_id` a global scope requires is
  precisely the shape that makes the isolation invariant (§5) hard to
  enforce mechanically. Dropping it is a deliberate simplification.
- **No fixed `kind` enum.** Nottario's kinds are `skill` / `context` /
  `note`. Maestro cannot ship a vocabulary — the same rule that forbids
  a `Quest` table forbids a `Lore` document kind. `kind` is free text,
  defaulting to empty, used only for filtering and grouping. A project
  writes `lore` or `script` or `pitch`; Maestro attaches no meaning.
- **`revert` exists.** Nottario has no revert tool; a caller re-reads an
  old version and writes it back. With agents as the heaviest writers,
  "undo the last agent's rewrite" is frequent enough to deserve one
  atomic call that cannot half-happen.
- **`project_id` is denormalised onto `document_versions`.** Nottario's
  version rows reach their project through the document. Maestro
  carries it on the row (§5).

Everything else — the write path taking `expected_version`, the
`SELECT … FOR UPDATE` inside the transaction, the unique index on
`(document_id, version)` as the second line of defence, soft delete,
the split between a `list` returning bodiless summaries and a `read`
returning the body — is copied, not re-litigated.

## 4. Concurrent editing

**Optimistic concurrency with `expected_version`, exactly as the core
spec settled for entities.** Every write passes the version it was
based on; a mismatch returns `version_conflict` carrying the current
version; the caller re-reads, merges, retries. `current_version` on the
document row is bumped inside the same transaction that inserts the
version row, and writers serialise on a `FOR UPDATE` read of the
document.

Two agents and a designer editing one mission script is the expected
case, so one deliberate addition on top of the core's error shape:

**`version_conflict` on a document write carries the current body as
well as the current version**, gated by a `include_current` argument
that defaults to true for MCP callers. For an entity the conflicting
caller can re-fetch cheaply and merge field by field; for prose it
needs the text to merge at all, and forcing a second round-trip on
every conflict wastes an agent's turn and tokens for nothing. The
argument exists so a caller writing a 200 KB document can turn the
echo off.

### What is deliberately not done

- **No operational transformation, no CRDT.** Character-level merge
  machinery is a large, subtle, permanently-owned dependency, and it
  buys smooth simultaneous typing — which matters for a shared cursor
  in a live editor and matters much less when the writers are agents
  submitting whole bodies. If real-time co-typing is wanted later it is
  a separate decision with its own spec.
- **No locking.** A designer who takes a lock and goes to lunch blocks
  an agent that was going to succeed. Optimistic wins on the numbers:
  conflicts are rare, and a rare retry beats a common block.
- **No automatic three-way merge in the server.** A caller that hits a
  conflict is handed both versions and decides. An LLM agent is
  extremely good at exactly this merge; the server is not, and a server
  that silently merges prose wrong is worse than one that refuses.

**Open question.** Section-scoped writes — "replace the heading
`## Act II`" or "append to the end" — would make most concurrent edits
non-conflicting, because two agents usually work on different parts of
a script. It is real value and it is real complexity (a markdown
section addressing scheme, and its behaviour when a heading is renamed
or duplicated). Not decided. v1 is whole-body writes; if conflict rates
in practice are annoying, this is the first thing to add.

## 5. Isolation

The invariant the Core sub-project was built to defend applies here
unchanged and in one sentence: **the game is resolved from the token or
session, never from a caller-supplied parameter, and every statement
filters on it.**

Concretely:

- `documents.project_id` is `NOT NULL` and references `projects`.
- Every document query is
  `WHERE project_id = $1 AND path = $2` — never `WHERE path = $2`,
  never `WHERE id = $2`. A document id in a caller's hand is not
  authority to read it.
- `document_links` carries `project_id` as well, and links are refused
  unless the document *and* the entity both resolve within the caller's
  game. A cross-game link is impossible by construction, not by
  review.
- **`document_versions` carries `project_id` too**, denormalised,
  `NOT NULL`. It could be reached by joining the parent document, and
  Nottario reaches it that way. Maestro does not, because
  `documents.read_version` and `documents.history` are the two calls
  where "join to the parent, then check" is one forgotten join away
  from serving another game's prose, and the whole point of the
  invariant is that it does not depend on anyone remembering. The
  denormalised column is written by the same transaction that writes
  the version; a `CHECK` cannot express the consistency, so an
  integration test asserts it.
- A token for game A asking for any path, id or version of game B gets
  `scope_violation`, admin or not.

This gets the same mandatory test treatment as the metamodel: a game-A
token against every documents tool aimed at game-B resources,
including `read_version`, `history` and `links.add`.

## 6. Data model

```sql
CREATE TABLE documents (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    path            text NOT NULL,
    kind            text NOT NULL DEFAULT '',
    title           text NOT NULL DEFAULT '',
    summary         text NOT NULL DEFAULT '',
    body_md         text NOT NULL DEFAULT '',
    frontmatter     jsonb NOT NULL DEFAULT '{}'::jsonb,
    current_version integer NOT NULL DEFAULT 1,
    deleted_at      timestamptz,
    search          tsvector GENERATED ALWAYS AS (…) STORED,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    created_by_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_by_token_id uuid REFERENCES api_tokens (id) ON DELETE SET NULL,
    updated_by_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by_token_id uuid REFERENCES api_tokens (id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX documents_path_key ON documents (project_id, lower(path));
CREATE INDEX documents_search_idx ON documents USING gin (search);

CREATE TABLE document_versions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    document_id  uuid NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    version      integer NOT NULL,
    title        text NOT NULL DEFAULT '',
    summary      text NOT NULL DEFAULT '',
    body_md      text NOT NULL DEFAULT '',
    frontmatter  jsonb NOT NULL DEFAULT '{}'::jsonb,
    message      text NOT NULL DEFAULT '',
    author_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    author_token_id uuid REFERENCES api_tokens (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX document_versions_key ON document_versions (document_id, version);

CREATE TABLE document_links (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    document_id uuid NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    entity_id   uuid NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
    role        text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX document_links_key ON document_links (document_id, entity_id);
CREATE INDEX document_links_entity_idx ON document_links (entity_id);
```

Notes on the shape:

- **The foreign keys above are illustrative and not yet right.** Every
  reference to `documents`, `entities` or `api_tokens` must be
  composite, carrying `project_id`; see §2, "Consequence for the
  metamodel schema as committed".
- **`path` is the key**, unique per game, case-insensitively, like
  every other key in Maestro (`documents_path_key` mirrors
  `entities_key_key`). It is what makes writes idempotent: an agent
  re-running its seeding script updates rather than duplicates.
  Slashes in a path are a naming convention for humans and a filter
  prefix; they imply no folder table and no inherited anything.
- **`title` and `summary` are derived, not authored twice.** Taken from
  frontmatter if present, else from the first `# heading`, else the
  path — Nottario's rule, kept.
- **`frontmatter` is stored, echoed back, and never interpreted.** It
  is split off the body on write so the body an agent reads back is the
  body it wrote. It is not searched for links and does not create
  attachments (§7).
- **`current_version` on the document is the concurrency token.** It is
  the same integer as the newest `document_versions.version`; the
  unique index makes the invariant enforceable rather than hoped for.

## 7. MCP and REST surface

Following the core spec's conventions: MCP is first-class, rows are
addressed by key not id, lists are slim and paginated, errors are
stable and machine-readable.

### MCP tools (`maestro.docs.*`)

| Tool | Arguments |
|---|---|
| `docs.list` | `path_prefix`, `kind`, `entity_key`+`entity_type`, `include_deleted`, cursor, limit — summaries only, no bodies |
| `docs.read` | `path`, optional `head_only` (frontmatter plus a short preview, with `{truncated, body_length}`) |
| `docs.write` | `path`, `content`, `kind`, `message`, `expected_version` (0 for a new document — see below), optional `links` |
| `docs.delete` | `path`, `expected_version` — soft |
| `docs.history` | `path`, cursor — version metadata only, newest first |
| `docs.read_version` | `path`, `version` |
| `docs.revert` | `path`, `to_version`, `expected_version`, `message` |
| `docs.diff` | `path`, `from_version`, `to_version` — unified diff, computed on read |
| `docs.links.list` | `path` **or** `entity_key`+`entity_type` — the join, from either side |
| `docs.links.add` | `path`, `entity_key`, `entity_type`, `role` |
| `docs.links.remove` | `path`, `entity_key`, `entity_type` |

Four deliberate choices in that table:

- **`expected_version: 0` spells a create.** This is a genuine
  difference from the metamodel surface, not a restatement of it, and it
  is worth naming because the two sub-projects should not end up
  disagreeing by accident. There, an upsert that finds an existing row
  and carries no `expected_version` returns `version_conflict`, so a
  re-run of a seeding payload conflicts on every row it already wrote;
  here, the version is always required and `0` is the explicit "I expect
  this not to exist". Whether the metamodel should adopt the same shape,
  or grow an `on_conflict` mode instead, was open question O2 in
  `2026-08-31-core-and-metamodel-design.md`, "Idempotency". **Decided
  2026-09-02: the metamodel grows `on_conflict`**
  (`"fail" | "skip" | "overwrite"`, default `"fail"`, not yet
  implemented), and does not adopt `expected_version: 0`. So the two
  surfaces keep two conventions on purpose, and this one does not
  change: a document has one version line and always carries it, while a
  metamodel batch of five hundred rows should not have to learn five
  hundred versions before it can be re-run. Naming the difference here
  is what stops it being read as an accident.
- **`docs.write` takes an optional `links` array**, so an agent seeding
  a game creates the script and attaches it to its quest in one call.
  Passing `links` *replaces* the document's link set; omitting it
  leaves the links untouched. The distinction is explicit in the tool
  description because "omitted means empty" would silently detach
  everything on every ordinary edit.
- **Attachment is never inferred from content.** Not from frontmatter,
  not from a `key:` line, not from a heading matching an entity name.
  Inference is convenient exactly until it is wrong, and a wrong
  attachment is invisible — the document simply shows up on the wrong
  quest and nobody notices. Links are an explicit act.
- **`docs.list` filtered by entity is how the UI builds an entity
  page**, and how an agent asked to "rewrite the Hogger dialogue"
  finds the document from the quest instead of guessing a path.

### Error shapes

The core spec's set, reused verbatim: `not_found`,
`version_conflict` (carries `current_version`, and for documents also
the current body unless `include_current: false`), `scope_violation`,
`in_use`. Two additions specific to this domain:

- `invalid_path` — empty, or containing characters reserved for the
  path grammar.
- `entity_not_found` — a link targets an entity key that does not exist
  in this game. Distinct from `not_found`, which is about the document,
  because an agent that gets one flat `not_found` from
  `docs.links.add` cannot tell which of the two it typo'd.

### Pagination and size

Same discipline as the core. `docs.list` and `docs.history` return
summaries and paginate with an opaque cursor. **Bodies are only ever
returned by `read`, `read_version` and `diff`** — never by a list, never
by search. Prose is the largest payload in the system and agents are
its main consumer; a `list` that returned bodies would blow a context
window on the first call against a real game.

### REST and SSE

`/api` mirrors the tools for the UI, and adds what agents never need:
server-rendered HTML for the reading view (markdown rendered
server-side and sanitised — the raw body is what MCP always gets) and
side-by-side version comparison.

`/events` gains `document.written`, `document.deleted`,
`document.reverted` and `document.linked`, per game, so a designer
watching a mission page sees an agent's rewrite land.

## 8. Search

**Two indexes, one tool.** Documents get their own generated
`tsvector` column with a GIN index, weighted `title` > `summary` >
`body_md`, alongside the entity index the metamodel already defines.
`maestro.search` unions the two and returns hits tagged with what they
are — the same shape Nottario's unified search uses across tasks, docs
and architecture nodes.

Why two indexes rather than one shared table:

- The two things being searched have different weightings and different
  best answers. A designer searching `hogger` wants the *quest* first;
  a designer searching a half-remembered line of dialogue wants the
  *script*. A single flattened index cannot rank both well.
- A generated column per table is maintained by Postgres and cannot
  drift. A shared index table is a second write on every content
  change, which is exactly the kind of thing that silently stops
  happening.

So the answer to "does a designer searching for a word find the entity,
the document, or both" is **both, in one result list, each labelled**,
with a filter to narrow to one kind. That is the only answer that does
not require the searcher to already know where the word was written —
which is the situation search exists for.

Two consequences worth stating:

- **Entity search does not reach into attached prose.** A word that
  appears only in a quest's script produces a *document* hit, not a
  quest hit. The document hit names its linked entities, so the
  designer gets there in one more click, and the ranking stays honest.
- **Only the current version is indexed.** Searching the full history
  ("which version introduced this line?") is not offered. It would
  multiply the index by the number of versions to answer a question
  that, in practice, is asked about one known document — where
  `history` plus `diff` answers it better.

**Open question.** Nottario indexes `simple`, `english` and `spanish`
configurations together, which works well for its bilingual content. A
game's prose may be written in any language, and a per-project text
search configuration is probably the right answer rather than a fixed
trio. Not decided; the fixed trio is a fine v1 and the migration to a
per-project configuration is mechanical.

## 9. Testing and definition of done

Mandatory areas, in the core spec's spirit — the ones that fail
silently:

1. **Game isolation** — a game-A token against every tool aimed at
   game-B resources, `read_version` and `history` explicitly included.
2. **Concurrency** — two simultaneous writes to one document: one
   wins, one gets `version_conflict` carrying the current version and
   body; no gap and no duplicate in the version sequence.
3. **Version integrity** — `current_version` always equals the newest
   version row; revert appends rather than rewrites; soft delete then
   rewrite resurrects and continues numbering.
4. **Links** — a link to an entity in another game is refused; deleting
   an entity drops its links and leaves the document; `links` on write
   replaces, omitting it preserves.
5. **Search** — a word in a quest name and a word in an attached script
   both return, labelled, ranked plausibly.

**Definition of done.** An agent writes a quest's dialogue script over
MCP, attaches it to the quest, a second agent rewrites it and hits a
conflict, merges and succeeds; a designer opens the quest in the
browser, reads the script, sees both authors in its history — one human,
one token — compares two versions, and reverts. Searching a line of the
dialogue finds the document; searching the quest's name finds the
quest.

## 10. Open questions

Left open deliberately rather than answered with false confidence:

1. **Section-scoped writes** (§4). The single biggest lever on conflict
   frequency, and the single biggest complexity addition. Deferred
   until real conflict rates are observable.
2. **Per-project text search configuration** (§8). The fixed
   simple/english/spanish trio is inherited, not chosen.
3. **Should `role` on a link be declared, like a relation type, rather
   than free text?** Free text is right for v1 and will produce
   `script`, `Script` and `dialogue` in the same game within a week. A
   project-declared vocabulary of document roles is the obvious fix and
   also the first step down the road towards documents-as-entities,
   which §2 rejects. Needs a decision before the UI groups by role.
4. **Attachment to relations, not only entities.** A metroidvania door
   with `requires_ability` is an edge, and an author may well want a
   paragraph about *that door*. `relations` as committed have no stable
   key — they are addressed by `(type, source, target)` — so a link
   table pointing at them is possible but not symmetric with entity
   links. Out of scope here; flagged because someone will ask.
5. **Does a document need a `kind` at all, given projects can leave it
   empty?** It costs a column and earns a filter; it may earn nothing
   once links carry a role. Cheap either way, worth revisiting when the
   UI is designed.
6. **Export.** "Give me the game bible and every attached lore document
   as a folder of `.md` files" is an obviously desirable operation and
   is not specified here. It touches archiving and backups more than
   this domain, and probably belongs with them.
