# Maestro Metamodel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give Maestro its domain: game-declared entity types and relation types with field schemas, the entities and relations that instance them, and the MCP tools an agent uses to declare a game's vocabulary and seed hundreds of rows into it.

**Architecture:** Four tables (`entity_types`, `entities`, `relation_types`, `relations`), all scoped by `project_id`, with user-declared field schemas stored as jsonb and validated by one pure-Go validator used at every entry point. Writes are idempotent by `(project, type, key)` and guarded by `expected_version`; bulk writes run either as one transaction or item by item. Every mutation publishes an SSE event through the hub the Core plan built.

**Tech Stack:** Go 1.25+, pgx/v5, sqlc, Postgres 16 (`jsonb`, `tsvector` + GIN, unique indexes), the official MCP Go SDK.

**Prerequisite:** `docs/superpowers/plans/2026-08-31-core.md` is fully implemented. This plan assumes `internal/config`, `internal/db` (pool, goose migrations, sqlc), `internal/testutil.NewPool`, `internal/identity`, `internal/projects`, `internal/realtime` and the `web.Caller` / `requireScope` machinery already exist.

**Source spec:** `docs/superpowers/specs/2026-08-31-core-and-metamodel-design.md`, roadmap item 2.

---

## File structure

| Path | Responsibility |
|---|---|
| `internal/db/migrations/0004_metamodel.sql` | the four domain tables, indexes, search vector |
| `internal/db/migrations/0005_entity_listing_index.sql` | `entities_listing_idx`, the entity keyset's own sort order (Task 6's correction 20) |
| `internal/db/queries/metamodel.sql` | every domain SQL statement |
| `internal/metamodel/schema.go` | field-schema types and their JSON encoding |
| `internal/metamodel/validate.go` | the one validator: schema + values → normalised or errors |
| `internal/metamodel/errors.go` | the domain's stable error values |
| `internal/metamodel/types.go` | entity types: upsert, list, get, remove |
| `internal/metamodel/relation_types.go` | relation types: upsert, list, get, remove |
| `internal/metamodel/entities.go` | entities: upsert (single and bulk), list, get, remove |
| `internal/metamodel/relations.go` | relations: upsert (single and bulk), list, remove |
| `internal/metamodel/search.go` | cross-cutting text search |
| `internal/web/mcp_metamodel.go` | the `maestro.types.*`, `maestro.entities.*`, `maestro.relations.*`, `maestro.search` tools |
| `internal/web/api_metamodel.go` | the REST mirror the UI reads |

---

### Task 1: The metamodel schema

**Files:**
- Create: `internal/db/migrations/0004_metamodel.sql`
- Test: `internal/db/metamodel_schema_test.go`

Note: the number depends on what the Core plan landed. It landed as
`0004_metamodel.sql`, after `0001_identity.sql`, `0002_membership_last_owner_guard.sql`
and `0003_api_token_hint.sql`; the rest of this task is identical whatever the number.

- [ ] **Step 1: Write the migration**

`internal/db/migrations/0004_metamodel.sql`:

```sql
-- +goose Up
CREATE TABLE entity_types (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    key          text NOT NULL,
    label        text NOT NULL,
    label_plural text NOT NULL,
    description  text NOT NULL DEFAULT '',
    color        text NOT NULL DEFAULT '',
    icon         text NOT NULL DEFAULT '',
    field_schema jsonb NOT NULL DEFAULT '[]'::jsonb,
    version      integer NOT NULL DEFAULT 1,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    updated_by_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by_token_id uuid REFERENCES api_tokens (id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX entity_types_key_key ON entity_types (project_id, lower(key));

CREATE TABLE relation_types (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id     uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    key            text NOT NULL,
    label          text NOT NULL,
    description    text NOT NULL DEFAULT '',
    source_type_ids uuid[] NOT NULL DEFAULT '{}',
    target_type_ids uuid[] NOT NULL DEFAULT '{}',
    semantic_role  text CHECK (semantic_role IN
                     ('prerequisite','unlock','containment','spatial','availability','reward')),
    field_schema   jsonb NOT NULL DEFAULT '[]'::jsonb,
    version        integer NOT NULL DEFAULT 1,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    updated_by_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by_token_id uuid REFERENCES api_tokens (id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX relation_types_key_key ON relation_types (project_id, lower(key));

CREATE TABLE entities (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id     uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    entity_type_id uuid NOT NULL REFERENCES entity_types (id) ON DELETE RESTRICT,
    key            text NOT NULL,
    name           text NOT NULL,
    fields         jsonb NOT NULL DEFAULT '{}'::jsonb,
    invalid        boolean NOT NULL DEFAULT false,
    version        integer NOT NULL DEFAULT 1,
    search         tsvector,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    updated_by_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by_token_id uuid REFERENCES api_tokens (id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX entities_key_key ON entities (project_id, entity_type_id, lower(key));
CREATE INDEX entities_type_idx ON entities (entity_type_id);
CREATE INDEX entities_search_idx ON entities USING gin (search);
CREATE INDEX entities_fields_idx ON entities USING gin (fields jsonb_path_ops);

CREATE TABLE relations (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id       uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    relation_type_id uuid NOT NULL REFERENCES relation_types (id) ON DELETE RESTRICT,
    source_id        uuid NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
    target_id        uuid NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
    fields           jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    updated_by_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by_token_id uuid REFERENCES api_tokens (id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX relations_edge_key ON relations (relation_type_id, source_id, target_id);
CREATE INDEX relations_source_idx ON relations (source_id);
CREATE INDEX relations_target_idx ON relations (target_id);

-- +goose Down
DROP TABLE relations;
DROP TABLE entities;
DROP TABLE relation_types;
DROP TABLE entity_types;
```

- [ ] **Step 2: Write the test**

`internal/db/metamodel_schema_test.go`:

```go
package db_test

import (
	"context"
	"testing"

	"github.com/neverbot/maestro/internal/testutil"
)

func TestMetamodelTablesExist(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx := context.Background()

	for _, table := range []string{"entity_types", "relation_types", "entities", "relations"} {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`,
			table).Scan(&exists); err != nil {
			t.Fatalf("query %s: %v", table, err)
		}
		if !exists {
			t.Fatalf("table %s was not created", table)
		}
	}
}

func TestEntityKeyIsUniquePerTypeAndProject(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx := context.Background()

	var projectID, typeID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (slug, name) VALUES ('azeroth', 'Azeroth') RETURNING id`).Scan(&projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO entity_types (project_id, key, label, label_plural)
		 VALUES ($1, 'quest', 'Quest', 'Quests') RETURNING id`, projectID).Scan(&typeID); err != nil {
		t.Fatalf("insert entity type: %v", err)
	}

	insert := `INSERT INTO entities (project_id, entity_type_id, key, name) VALUES ($1, $2, $3, $4)`
	if _, err := pool.Exec(ctx, insert, projectID, typeID, "wanted-hogger", "Wanted: Hogger"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	// The uniqueness is case-insensitive, so a re-seed with different casing
	// must collide rather than create a twin.
	if _, err := pool.Exec(ctx, insert, projectID, typeID, "Wanted-Hogger", "Wanted: Hogger"); err == nil {
		t.Fatal("expected a unique violation on a duplicate key")
	}
}
```

- [ ] **Step 3: Run the tests**

Run: `go test ./internal/db/ -v`
Expected: PASS, including the Core plan's migration test.

- [ ] **Step 4: Commit**

```bash
git add internal/db/migrations internal/db/metamodel_schema_test.go
git commit -m "feat: metamodel tables for types, entities and relations"
```

**Corrections made during implementation** (a follow-up pass over the
landed migration, decided before the later tasks build on its shape):

1. Every key from a child to its parent is now **composite**, carrying
   `project_id` alongside the parent id: `entities (entity_type_id,
   project_id) -> entity_types (id, project_id)`, and the same shape for
   `relations`' three parents (`relation_type_id`, `source_id`,
   `target_id`). As written above, each key referenced `id` alone, so the
   database happily accepted an entity instancing another game's entity
   type, or an edge whose type, source and target straddled two games.
   Isolation in Maestro is enforced in SQL, not in Go — the Core
   sub-project was attacked with live cross-tenant requests on that
   premise across twenty-two tasks — and this is the first table set
   holding game content, so leaving it to Go-level validation in Tasks
   3-6 would have contradicted the rule exactly where it matters most.
   The `ON DELETE` behaviour of each key is unchanged: `RESTRICT` for
   both type references, `CASCADE` for both endpoints.
2. `entity_types`, `relation_types` and `entities` each gained a
   `UNIQUE (id, project_id)`, which is what those composite keys
   reference. `projects` needs nothing: the `project_id` columns
   reference its `id`, already a primary key on its own, so no new
   migration was required — the correction was made in place in
   `0004_metamodel.sql`, which no instance has ever run.
3. `internal/db/metamodel_schema_test.go` gained four tests asserting the
   *database* rejects each cross-project reference with SQLSTATE 23503:
   an entity borrowing another game's entity type, and a relation whose
   `relation_type_id`, `source_id` or `target_id` belongs to another
   game. Each was verified to go red when its own composite key is
   reduced back to a single-column reference.

This makes the endpoint and type resolution in Tasks 3-6 belt-and-braces
rather than load-bearing, and needs no edit there: those tasks resolve
every parent by project-scoped key or by a project-scoped `...ByID`
query, so no Go path ever hands the database a foreign id, and no test in
them expects a Go error where the database would now raise one first.

**Corrections made during review** (a second pass over the landed
migration, after a reviewer verified the composite keys live and could
not break them):

4. `updated_by_token_id` was the one key the header comment's claim did
   not actually cover. It referenced `api_tokens (id)` alone, and
   `api_tokens` is project-scoped (`0001_identity.sql`), so a token owned
   by game B was accepted as the last editor of a game-A row — proved
   live. All four keys are now composite,
   `(updated_by_token_id, project_id) -> api_tokens (id, project_id)`,
   which required `api_tokens` to gain a `UNIQUE (id, project_id)`. That
   `ALTER TABLE` sits at the top of `0004_metamodel.sql`'s Up, not in a
   later migration: `0004` is where the referencing keys are created, so
   a `0005` would order wrong, and `0004` has never been run by any
   instance. `0004`'s Down drops the constraint again after the tables.
   Each key keeps its `ON DELETE SET NULL`, but names its column —
   `ON DELETE SET NULL (updated_by_token_id)`, Postgres 15+ — because a
   bare `SET NULL` would try to null `project_id` too, which is
   `NOT NULL`. `updated_by_user_id` needs none of this: `users` is
   global, not project-scoped, so there is no outer scope to carry. The
   header comment now says exactly that, and no more.
5. The four tables gained `set_updated_at` triggers, matching Core's
   tables. The cost of leaving them out was not a stale timestamp but
   two mechanisms for one column — Core's rows from a trigger, the
   metamodel's from every query remembering `updated_at = now()` — which
   nothing tests and which fails silently the first time a query is
   written without the clause. `set_updated_at()` already exists from
   `0001`, whose Down drops it, so the Down side needs nothing. The
   explicit `updated_at = now()` that Tasks 4-5 write is unaffected: the
   trigger sets the same value, so no later task changes.
6. `CREATE INDEX relations_project_idx ON relations (project_id, created_at)`.
   `relations` had no index leading with `project_id`, so Task 5's
   `ListRelations` with every optional filter null — the ordinary "show
   me this game's edges" call, on the table expected to hold the most
   rows — was a sequential scan across every game's relations plus a
   sort. It also gives the `projects` delete cascade an index it lacked.
   The reverse-traversal index the views spec asks for,
   `relations (relation_type_id, target_id)`, is **deliberately not**
   added here: no query in this plan walks an edge inwards, the views
   spec is still being written and its final access shape (in
   particular whether it wants `project_id` leading) is not settled, and
   an index nothing reads is pure write cost. It belongs to the task
   that introduces the query it serves.
7. Four constraints that survived deletion with a green suite are now
   pinned by tests in `internal/db/metamodel_schema_test.go`:
   `TestCannotRecordAnotherProjectsToken` (all four tables, SQLSTATE
   23503), `TestUpdatedAtTriggerFires` (all four tables),
   `TestDuplicateRelationEdgeIsRejected` (SQLSTATE 23505 — load-bearing
   for Task 5's `ON CONFLICT (relation_type_id, source_id, target_id)`,
   which would otherwise fail at runtime rather than at test time), and
   `TestDeletingAnEntityTypeWithInstancesIsRejected` (SQLSTATE 23503 —
   nothing previously stopped the `RESTRICT` being flipped to `CASCADE`
   and silently deleting a type's content). Each was proved red by
   mutating the schema: reverting the token keys to single-column,
   dropping the `entities` trigger, dropping the `UNIQUE` from
   `relations_edge_key`, and flipping `RESTRICT` to `CASCADE`. Each
   mutation failed exactly its own test.
   `TestDeletingATokenClearsOnlyTheTokenColumn` additionally pins the
   column-list `SET NULL` from correction 4.
8. Query hygiene in the plan text itself. Task 7 states the rule that
   every query over a metamodel table filters on the resolved project
   id, but five queries written earlier in this plan broke it —
   `CountEntitiesOfType`, `DeleteEntitiesOfType`,
   `MarkEntitiesOfTypeInvalid`, `ListEntitiesOfType` and
   `CountRelationsOfType` all filtered on a type id alone. Harmless
   today, since type ids are globally unique primary keys, but Task 3's
   and Task 5's implementers copy this text, and the convention has to
   be right where it is copied from. All five now carry
   `project_id = $1`, and their call sites in the plan pass it.
9. `sqlc.yaml` gained a `tsvector` override, so `entities.search` no
   longer generates as `interface{}`. Not `string` or `[]byte`, though:
   pgx/v5 returns tsvector in binary format and refuses to scan it into
   either (verified against Postgres 16 — `cannot scan tsvector (OID
   3614) in binary format`). It does ship `pgtype.TSVector`, which is
   what the column is mapped to, in both the nullable and non-nullable
   forms; the type carries its own `Valid` flag, so no pointer is
   needed. `internal/db/dbq/models.go` was regenerated in the same
   commit. Task 3 is the first task to run `make sqlc`, and now inherits
   a typed column instead of an untyped one.

---

### Task 2: Field schemas and their validator

**Files:**
- Create: `internal/metamodel/schema.go`, `internal/metamodel/errors.go`, `internal/metamodel/validate.go`
- Test: `internal/metamodel/validate_test.go`

- [ ] **Step 1: Write the failing test**

`internal/metamodel/validate_test.go`:

```go
package metamodel

import (
	"strings"
	"testing"
)

func questSchema() Schema {
	return Schema{
		{Key: "min_level", Label: "Minimum level", Type: FieldNumber, Required: true, Min: ptrFloat(1), Max: ptrFloat(70)},
		{Key: "summary", Label: "Summary", Type: FieldLongText},
		{Key: "repeatable", Label: "Repeatable", Type: FieldBool, HasDefault: true, Default: false},
		{Key: "difficulty", Label: "Difficulty", Type: FieldEnum, Options: []string{"trivial", "normal", "elite"}},
		{Key: "tags", Label: "Tags", Type: FieldListText},
	}
}

func ptrFloat(v float64) *float64 { return &v }

func TestValidateAcceptsAWellFormedRow(t *testing.T) {
	out, err := questSchema().Validate(map[string]any{
		"min_level":  float64(20),
		"summary":    "Kill twelve boars.",
		"difficulty": "normal",
		"tags":       []any{"starter", "kill"},
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if out["min_level"] != float64(20) {
		t.Fatalf("min_level = %v", out["min_level"])
	}
	// repeatable declares a default of false, so an omitted field with a
	// declared default is filled in — never left absent, and never dropped
	// for being the zero value of its type.
	if v, present := out["repeatable"]; !present || v != false {
		t.Fatalf("repeatable = %v, present=%v; want the declared default false to be applied", v, present)
	}
}

func TestValidateRejectsUnknownField(t *testing.T) {
	_, err := questSchema().Validate(map[string]any{"min_level": float64(1), "min_lvl": float64(3)})
	if err == nil {
		t.Fatal("an unknown field must be an error, never silently dropped")
	}
	if !strings.Contains(err.Error(), "min_lvl") {
		t.Fatalf("the error must name the offending field, got %q", err)
	}
}

func TestValidateRejectsMissingRequiredField(t *testing.T) {
	_, err := questSchema().Validate(map[string]any{"summary": "no level given"})
	if err == nil {
		t.Fatal("a missing required field must be an error")
	}
	if !strings.Contains(err.Error(), "min_level") {
		t.Fatalf("error = %q, want it to name min_level", err)
	}
}

func TestValidateRejectsWrongType(t *testing.T) {
	_, err := questSchema().Validate(map[string]any{"min_level": "veinte"})
	if err == nil {
		t.Fatal("a string in a number field must be an error")
	}
	if !strings.Contains(err.Error(), "fields.min_level") {
		t.Fatalf("error = %q, want the field path", err)
	}
}

func TestValidateEnforcesRange(t *testing.T) {
	if _, err := questSchema().Validate(map[string]any{"min_level": float64(999)}); err == nil {
		t.Fatal("a number above max must be an error")
	}
	if _, err := questSchema().Validate(map[string]any{"min_level": float64(0)}); err == nil {
		t.Fatal("a number below min must be an error")
	}
}

func TestValidateEnforcesEnumOptions(t *testing.T) {
	_, err := questSchema().Validate(map[string]any{"min_level": float64(5), "difficulty": "impossible"})
	if err == nil {
		t.Fatal("a value outside the enum options must be an error")
	}
}

func TestValidateChecksListElements(t *testing.T) {
	_, err := questSchema().Validate(map[string]any{"min_level": float64(5), "tags": []any{"ok", 3}})
	if err == nil {
		t.Fatal("a non-text element in a list<text> must be an error")
	}
}

func TestValidateAppliesDefaults(t *testing.T) {
	schema := Schema{{Key: "repeatable", Type: FieldBool, HasDefault: true, Default: true}}
	out, err := schema.Validate(map[string]any{})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if out["repeatable"] != true {
		t.Fatalf("repeatable = %v, want the declared default", out["repeatable"])
	}
}

func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	_, err := questSchema().Validate(map[string]any{"difficulty": "impossible", "nonsense": 1})
	if err == nil {
		t.Fatal("expected errors")
	}
	msg := err.Error()
	for _, want := range []string{"min_level", "difficulty", "nonsense"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not mention %q; a seeding agent needs every problem in one pass", msg, want)
		}
	}
}

func TestSchemaRejectsDuplicateKeys(t *testing.T) {
	schema := Schema{{Key: "a", Type: FieldText}, {Key: "a", Type: FieldNumber}}
	if err := schema.Check(); err == nil {
		t.Fatal("a schema with duplicate keys must be rejected")
	}
}

func TestSchemaRejectsEnumWithoutOptions(t *testing.T) {
	schema := Schema{{Key: "difficulty", Type: FieldEnum}}
	if err := schema.Check(); err == nil {
		t.Fatal("an enum field with no options must be rejected")
	}
}
```

The block above is the starting point, not the finished file. The landed
`validate_test.go` grew to 84 tests as the corrections below went in; every
negative test there asserts the exact message *and* path it expects, so it
can only be satisfied by the failure it names. Read the file, not this
block, for the full list.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/metamodel/ -v`
Expected: FAIL, `undefined: Schema`.

- [ ] **Step 3: Write the implementation**

`internal/metamodel/errors.go`:

```go
// Package metamodel is Maestro's domain: game-declared types and the entities
// and relations that instance them.
package metamodel

import (
	"errors"
	"fmt"
	"strings"
)

// Stable domain errors. Their names match the wire codes the MCP surface
// returns, so a change here is a change to the public contract.
var (
	ErrNotFound             = errors.New("not_found")
	ErrVersionConflict      = errors.New("version_conflict")
	ErrEndpointTypeMismatch = errors.New("endpoint_type_mismatch")
	ErrInUse                = errors.New("in_use")
	ErrSchemaViolation      = errors.New("schema_violation")
	ErrInvalidSchema        = errors.New("invalid_schema")
)

// FieldError is one problem with one field.
type FieldError struct {
	Path    string
	Message string
}

func (e FieldError) Error() string { return e.Path + ": " + e.Message }

// ValidationError carries every problem found in one pass, so a seeding agent
// fixes all of them at once instead of discovering them one round-trip apart.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		parts = append(parts, f.Error())
	}
	return fmt.Sprintf("schema_violation: %s", strings.Join(parts, "; "))
}

// Is makes errors.Is(err, ErrSchemaViolation) true for validation failures.
func (e *ValidationError) Is(target error) bool { return target == ErrSchemaViolation }

// SchemaError carries every problem found in a schema *declaration*, at
// field_schema[<i>] paths.
//
// It is deliberately a different error from ValidationError, and satisfies a
// different sentinel. The two failures are told apart by who is at fault: an
// invalid_schema is a type whose declaration cannot stand, a
// schema_violation is a row of values that does not fit a declaration that
// can. The MCP surface returns those as two codes, and a caller must not
// have to match on path spelling to tell them apart. Introducing the
// distinction now is cheap; Task 7 puts it on the wire, and after that it is
// not.
type SchemaError struct {
	Fields []FieldError
}

func (e *SchemaError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		parts = append(parts, f.Error())
	}
	return fmt.Sprintf("invalid_schema: %s", strings.Join(parts, "; "))
}

// Is makes errors.Is(err, ErrInvalidSchema) true for schema declaration
// failures — and, deliberately, leaves errors.Is(err, ErrSchemaViolation)
// false.
func (e *SchemaError) Is(target error) bool { return target == ErrInvalidSchema }

// VersionConflictError reports the version the caller must merge onto.
type VersionConflictError struct {
	Current int32
}

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("version_conflict: current version is %d", e.Current)
}

// Is makes errors.Is(err, ErrVersionConflict) true.
func (e *VersionConflictError) Is(target error) bool { return target == ErrVersionConflict }
```


`internal/metamodel/schema.go`:

```go
package metamodel

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// FieldType is one of the declarative field types a game may use. There is
// deliberately no reference type: a pointer to another entity is a relation,
// which keeps the graph complete and visible to every traversal.
type FieldType string

// The field types.
const (
	FieldText     FieldType = "text"
	FieldLongText FieldType = "longtext"
	FieldNumber   FieldType = "number"
	FieldBool     FieldType = "bool"
	FieldEnum     FieldType = "enum"
	FieldListText FieldType = "list<text>"
)

// Field is one declared field of an entity type or relation type.
//
// HasDefault is not part of the wire format. On the wire a default is
// declared by the presence of the "default" key and by nothing else, so
// there is exactly one source of truth for the fact; UnmarshalJSON sets
// HasDefault from that presence and MarshalJSON writes the key back only
// when HasDefault is set. See Field.UnmarshalJSON.
type Field struct {
	// Key identifies the field inside the row's jsonb object. It must be
	// lower_snake_case ASCII, at most maxKeyLen characters; see keyPattern
	// for why the rule is that narrow.
	Key      string    `json:"key"`
	Label    string    `json:"label,omitempty"`
	Type     FieldType `json:"type"`
	Required bool      `json:"required,omitempty"`
	// HasDefault reports whether this field declares a default at all,
	// independently of what Default holds. It exists because Default is
	// `any`: its Go zero value is nil, the very same value an undeclared
	// field carries, and false, 0 and "" are ordinary, legitimate defaults
	// a game may want to declare (repeatable: false, starting_credits: 0).
	// A reflect-based "is Default the zero value" rule cannot tell "declared
	// false" from "never declared" apart — that confusion is exactly what
	// this field replaces.
	HasDefault bool     `json:"-"`
	Default    any      `json:"-"`
	Options    []string `json:"options,omitempty"`
	Min        *float64 `json:"min,omitempty"`
	Max        *float64 `json:"max,omitempty"`
}

// fieldJSON is Field's wire shape. Default is a *json.RawMessage so the
// decoder can tell "the key was absent" from "the key was present and held
// false, 0 or an empty string" — the distinction a plain `any` destroys.
type fieldJSON struct {
	Key      string           `json:"key"`
	Label    string           `json:"label,omitempty"`
	Type     FieldType        `json:"type"`
	Required bool             `json:"required,omitempty"`
	Default  *json.RawMessage `json:"default,omitempty"`
	Options  []string         `json:"options,omitempty"`
	Min      *float64         `json:"min,omitempty"`
	Max      *float64         `json:"max,omitempty"`
}

// UnmarshalJSON decodes a field, declaring a default when — and only when —
// the "default" key is present and not null.
//
// This is the path every agent-authored schema takes: MCP hands Task 3 a
// field_schema straight off the wire, so a default that is not inferred here
// is a default that is silently dropped. An explicit null is not a default:
// null is how this package spells "not set" everywhere else, and Validate
// already treats a null value as an absent one.
func (f *Field) UnmarshalJSON(raw []byte) error {
	var w fieldJSON
	if err := json.Unmarshal(raw, &w); err != nil {
		return err
	}
	*f = Field{
		Key:      w.Key,
		Label:    w.Label,
		Type:     w.Type,
		Required: w.Required,
		Options:  w.Options,
		Min:      w.Min,
		Max:      w.Max,
	}
	if w.Default != nil && string(*w.Default) != "null" {
		if err := json.Unmarshal(*w.Default, &f.Default); err != nil {
			return err
		}
		f.HasDefault = true
	}
	return nil
}

// MarshalJSON writes the "default" key only for a field that declares one,
// so the value round trips back through UnmarshalJSON to the same
// HasDefault. A field with HasDefault set and a nil Default cannot be
// spelled on the wire; Check rejects it, since nil is not a value any field
// type could hold.
func (f Field) MarshalJSON() ([]byte, error) {
	w := fieldJSON{
		Key:      f.Key,
		Label:    f.Label,
		Type:     f.Type,
		Required: f.Required,
		Options:  f.Options,
		Min:      f.Min,
		Max:      f.Max,
	}
	if f.HasDefault && f.Default != nil {
		encoded, err := json.Marshal(f.Default)
		if err != nil {
			return nil, err
		}
		w.Default = (*json.RawMessage)(&encoded)
	}
	return json.Marshal(w)
}

// Schema is the ordered list of fields a type declares.
type Schema []Field

// ParseSchema decodes a schema from its stored jsonb representation.
func ParseSchema(raw []byte) (Schema, error) {
	if len(raw) == 0 {
		return Schema{}, nil
	}
	var s Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("decode field schema: %w", err)
	}
	return s, nil
}

// JSON encodes the schema for storage.
func (s Schema) JSON() ([]byte, error) {
	if s == nil {
		s = Schema{}
	}
	return json.Marshal(s)
}

// maxKeyLen caps a field key. It is generous for a human-readable
// identifier and short enough that a key can be used verbatim wherever
// Maestro later needs one — a jsonb key, a view-query token, a flattened
// column name.
const maxKeyLen = 64

// keyPattern is the field-key rule: lower_snake_case ASCII, starting with a
// letter. It is deliberately narrow, and every exclusion pays for itself:
//
//   - No dot, because "fields.<key>" is the error path this package returns;
//     a key containing a dot makes "fields.a.b" ambiguous between the field
//     "a.b" and a nested "b" inside "a".
//   - No upper case, so two keys can never differ only by case — a collision
//     no case-sensitive map would catch but every human reader would trip
//     over. Lowercasing by rule beats detecting the collision after the fact.
//   - No whitespace, brackets or dashes, so a key is a single token wherever
//     it is later quoted, flattened or parsed.
//   - ASCII only, so no two spellings of one key (NFC and NFD forms of the
//     same accented word) can coexist as different keys.
//
// The rule is enforced at declaration time, where an agent can still act on
// the news, rather than being discovered six hundred rows later.
var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Check validates the schema itself, before anything is stored against it.
// Every problem in the whole schema is reported in one pass, including
// several problems on one field: an agent authoring a schema should be able
// to fix all of it at once.
func (s Schema) Check() error {
	seen := make(map[string]struct{}, len(s))
	var problems []FieldError

	for i, f := range s {
		path := fmt.Sprintf("field_schema[%d]", i)
		problem := func(msg string) {
			problems = append(problems, FieldError{Path: path, Message: msg})
		}

		// A bad key never short-circuits the rest of the field: it would hide
		// problems the same agent has to fix in the same edit.
		switch {
		case f.Key == "":
			problem("key is required")
		case len(f.Key) > maxKeyLen:
			problem(fmt.Sprintf("key must be at most %d characters", maxKeyLen))
		case !keyPattern.MatchString(f.Key):
			problem("key must be lower_snake_case: a letter, then letters, digits or underscores")
		default:
			if _, dup := seen[f.Key]; dup {
				problem("duplicate key " + f.Key)
			}
			seen[f.Key] = struct{}{}
		}

		typeOK := false
		switch f.Type {
		case "":
			problem("type is required")
		case FieldText, FieldLongText, FieldNumber, FieldBool, FieldListText:
			typeOK = true
		case FieldEnum:
			if len(f.Options) == 0 {
				problem("an enum field needs options")
			} else {
				typeOK = true
			}
		default:
			problem("unknown type " + string(f.Type))
		}

		// Options and bounds are per-type facilities. Declaring one on a type
		// that cannot use it is never what the author meant, and silence here
		// reads to them as acceptance.
		if f.Type != FieldEnum && len(f.Options) > 0 {
			problem("options apply only to an enum field")
		}
		if f.Type == FieldEnum {
			seenOpt := make(map[string]struct{}, len(f.Options))
			for j, opt := range f.Options {
				if strings.TrimSpace(opt) == "" {
					problem(fmt.Sprintf("option %d is empty", j))
					continue
				}
				if _, dup := seenOpt[opt]; dup {
					problem(fmt.Sprintf("duplicate option %q", opt))
				}
				seenOpt[opt] = struct{}{}
			}
		}
		if f.Type != FieldNumber && (f.Min != nil || f.Max != nil) {
			problem("min and max apply only to a number field")
		}
		if f.Min != nil && f.Max != nil && *f.Min > *f.Max {
			problem(fmt.Sprintf("min %v is above max %v, so no value is legal", *f.Min, *f.Max))
		}

		// Required and a default are mutually exclusive. Validate applies the
		// default before it can ever complain that the field is missing, so
		// the pair makes Required unreachable; rejecting it here says so at
		// the only moment the author can still choose which one they meant.
		if f.Required && f.HasDefault {
			problem("a required field cannot also declare a default: the default would always win")
		}

		// A declared default must itself be a value the field could actually
		// hold — same coercion and bounds logic Validate applies to a real
		// value, so a schema can never declare a default no direct write
		// could ever produce (Default: "yes" on a bool field, Default: 200
		// on a number field whose Max is 70, and so on).
		if typeOK && f.HasDefault {
			if _, err := coerce(f, f.Default); err != nil {
				problem("default: " + err.Error())
			}
		}
	}

	if len(problems) > 0 {
		return &SchemaError{Fields: problems}
	}
	return nil
}
```


`internal/metamodel/validate.go`:

```go
package metamodel

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
)

// Validate checks a value map against the schema and returns the normalised
// map to store. Every problem is reported at once.
//
// Three rules matter more than the rest, and each exists for a reason:
//   - An unknown field is an error, never dropped: an agent that types
//     "min_lvl" must find out immediately, not six hundred rows later.
//   - An absent optional field with no default stays absent: zero-filling
//     would make "not set" indistinguishable from "set to zero".
//   - A declared default is applied when the field is absent or null, so a
//     schema change gives every new row the value the designer intended.
//     See hasDefault for what counts as declared. The default goes through
//     the same coercion a hand-written value does, so it lands in the row
//     with the same Go type and cannot smuggle past the field's bounds.
func (s Schema) Validate(values map[string]any) (map[string]any, error) {
	byKey := make(map[string]Field, len(s))
	for _, f := range s {
		byKey[f.Key] = f
	}

	var problems []FieldError
	out := make(map[string]any, len(values))

	// Unknown keys are found by ranging a map, whose order Go randomises on
	// every run. They are sorted so the message a caller sees is a function
	// of the input alone: Tasks 3-6 return these strings to agents, and a
	// golden test over them has to be possible.
	var unknown []string
	for key := range values {
		if _, known := byKey[key]; !known {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	for _, key := range unknown {
		problems = append(problems, FieldError{
			Path:    "fields." + key,
			Message: "unknown field for this type",
		})
	}

	for _, f := range s {
		raw, present := values[f.Key]
		path := "fields." + f.Key

		if !present || raw == nil {
			switch {
			case hasDefault(f):
				// The default is coerced exactly like a value written by
				// hand. A schema read back from jsonb never passes through
				// Check again, so without this a schema that was never
				// checked would write an unchecked value into every row it
				// touches, forever.
				value, err := coerce(f, f.Default)
				if err != nil {
					problems = append(problems, FieldError{
						Path:    path,
						Message: "the schema's default is invalid: " + err.Error(),
					})
					continue
				}
				out[f.Key] = value
			case f.Required:
				problems = append(problems, FieldError{Path: path, Message: "is required"})
			}
			continue
		}

		value, err := coerce(f, raw)
		if err != nil {
			problems = append(problems, FieldError{Path: path, Message: err.Error()})
			continue
		}
		out[f.Key] = value
	}

	if len(problems) > 0 {
		return nil, &ValidationError{Fields: problems}
	}
	return out, nil
}

// CheckValues re-validates a stored row against this schema and reports
// whether it still fits, returning nothing a caller could accidentally write
// back.
//
// This is the re-validation path. When a type's field schema is edited,
// every stored row of that type has to be re-examined and flagged, and the
// design is explicit that flagging must not alter the rows: an entity's
// content is the designer's, and a validation pass is not an edit. Validate
// cannot serve that purpose safely — it returns a normalised map with
// declared defaults injected, so a caller who re-validates with it and then
// stores what came back silently back-fills every row it touched. Returning
// only an error removes the possibility rather than documenting against it.
//
// The rules are exactly Validate's, so the two never disagree about whether
// a row is valid; the difference is only what comes back.
func (s Schema) CheckValues(values map[string]any) error {
	_, err := s.Validate(values)
	return err
}

// hasDefault reports whether the field declares a default for Validate to
// apply. Presence, not value, decides it: Field.HasDefault is set
// independently of what Default holds, so a declared default of false, 0 or
// "" — an ordinary thing for a game to declare — is applied exactly like any
// other declared default. See Field.HasDefault's doc comment for why a
// value-based rule (checking whether Default is the zero value) cannot make
// this distinction.
func hasDefault(f Field) bool {
	return f.HasDefault
}

// coerce checks one value against one field declaration.
func coerce(f Field, raw any) (any, error) {
	switch f.Type {
	case "":
		return nil, errors.New("the schema declares no type for this field")

	case FieldText, FieldLongText:
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("expected text, got %T", raw)
		}
		return s, nil

	case FieldNumber:
		n, ok := toFloat(raw)
		if !ok {
			return nil, fmt.Errorf("expected number, got %T", raw)
		}
		// NaN compares false against every bound, so without this it would
		// slip past both Min and Max; the infinities pass whenever the
		// matching bound is unset. Neither survives being written to jsonb,
		// and the failure there carries no field path, so it is caught here.
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, errors.New("must be a finite number")
		}
		if f.Min != nil && n < *f.Min {
			return nil, fmt.Errorf("must be at least %v", *f.Min)
		}
		if f.Max != nil && n > *f.Max {
			return nil, fmt.Errorf("must be at most %v", *f.Max)
		}
		return n, nil

	case FieldBool:
		b, ok := raw.(bool)
		if !ok {
			return nil, fmt.Errorf("expected true or false, got %T", raw)
		}
		return b, nil

	case FieldEnum:
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("expected one of %v, got %T", f.Options, raw)
		}
		for _, opt := range f.Options {
			if opt == s {
				return s, nil
			}
		}
		return nil, fmt.Errorf("%q is not one of %v", s, f.Options)

	case FieldListText:
		list, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("expected a list of text, got %T", raw)
		}
		out := make([]any, 0, len(list))
		for i, item := range list {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("element %d is %T, expected text", i, item)
			}
			out = append(out, s)
		}
		return out, nil

	default:
		return nil, fmt.Errorf("unknown field type %q", f.Type)
	}
}

// toFloat widens every numeric shape that can reach the validator to
// float64. That is more than encoding/json's default decode produces: a
// decoder configured with UseNumber yields json.Number, which the MCP SDK is
// free to do, and Go callers inside Maestro pass the small and unsigned
// widths. Missing any of them would break every number field at once, and
// widening is far cheaper than finding out which decoder is in play.
//
// Finiteness is deliberately not decided here: NaN and the infinities widen
// cleanly, and coerce rejects them with a message that says what is actually
// wrong rather than claiming the value is not a number.
func toFloat(raw any) (float64, bool) {
	switch n := raw.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}
```


- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/metamodel/ -v`
Expected: PASS. Eleven tests as first written; 72 as landed, after the
corrections below.

- [ ] **Step 5: Commit**

```bash
git add internal/metamodel
git commit -m "feat: field schemas and their validator"
```

**Corrections made during implementation** (a follow-up pass over the
landed package, made before Task 3 starts building on `Field`'s shape):

1. `Field.Default` gained a sibling, `HasDefault bool`. As landed, the plan's
   `hasDefault` treated any zero value of `Default` (`false`, `0`, `""`) as
   "no default declared", on the reasoning that `Field.Default`'s
   `json:",omitempty"` tag would drop a zero-valued default during storage
   anyway, so an in-memory schema had to behave the same way for consistency.
   That reasoning does not hold: `encoding/json`'s `omitempty` on an `any`
   field only omits a nil interface, not a non-nil interface wrapping a zero
   concrete value, so `Default: false` already survives `Schema.JSON` /
   `ParseSchema` unchanged (`TestSchemaRoundTripPreservesADeclaredZeroValuedDefault`
   pins this both ways: a declared `false` round-trips and is applied, and an
   undeclared field round-trips to genuinely absent). But `repeatable: false`
   and `starting_credits: 0` are exactly the kind of thing a game should be
   able to declare — Maestro does not second-guess a game's vocabulary — and
   the old rule silently dropped them regardless of storage. `HasDefault` is
   an explicit presence flag, decided independently of `Default`'s value, so
   "declared and zero" is distinguishable from "undeclared" without leaning
   on the very reflect-based zero-check that produced this bug. `questSchema()`'s
   `repeatable` field genuinely means to default to false (most quests are
   not repeatable), so it now sets `HasDefault: true`; `TestValidateAcceptsAWellFormedRow`,
   which the plan wrote to assert `repeatable` stays absent from a row that
   never set it, was asserting the bug — it now asserts `repeatable` comes
   back `false`. `TestValidateIgnoresAZeroValuedDefault` is gone; its
   replacement, `TestValidateAppliesADeclaredZeroValuedDefault`, asserts the
   opposite. Proved red by reverting `hasDefault` to `return false` and
   confirming three tests fail:
   `TestValidateAcceptsAWellFormedRow`, `TestValidateAppliesADeclaredZeroValuedDefault`,
   `TestSchemaRoundTripPreservesADeclaredZeroValuedDefault`.
2. `Schema.Check()` did not check `Default` against the field it belongs to,
   so a schema declaring `Default: "yes"` on a bool field, or `Default: 200`
   on a number field capped at `Max: 70`, was accepted — and would write a
   value no direct call to `Validate` could ever produce. `Check()` now runs
   `coerce(f, f.Default)` for every field with `HasDefault: true` and a valid
   `Type`, reusing the exact type/bounds/enum/list-element logic `Validate`
   already applies to real values rather than duplicating it, and reports at
   the same `field_schema[<i>]` path the package uses for every other
   schema-level problem. Proved red by gating the new check behind `if false
   && ...` and confirming four tests fail: `TestSchemaRejectsADefaultOfTheWrongType`,
   `TestSchemaRejectsADefaultOutsideItsBounds`, `TestSchemaRejectsADefaultNotInEnumOptions`,
   `TestSchemaRejectsANonTextElementInAListTextDefault`.

**Corrections from the review of the first two** (a second pass, still
before Task 3 begins; the code blocks above were refreshed from the landed
files in the same pass, so what they show is what shipped):

3. `HasDefault` was a plain JSON field, so **every default an agent declared
   over the wire was silently dropped**. `ParseSchema` never inferred it from
   the presence of the `"default"` key:
   `[{"key":"repeatable","type":"bool","default":false}]` parsed to
   `HasDefault: false`, `Validate({})` returned an empty map, and `Check()`
   skipped the default too — the guarantee correction 1 added evaporated on
   exactly the input shape it was written for. Task 3 accepts `field_schema`
   straight from MCP and no agent would ever know to send an undocumented
   `has_default` sibling. Correction 1's reasoning about `omitempty` on an
   `any` field was right about marshalling and beside the point about
   unmarshalling, which is where the fact was being lost.

   Fixed at the wire boundary rather than at the call sites: `Field` now has
   `UnmarshalJSON` and `MarshalJSON` over a shadow struct whose `Default` is
   a `*json.RawMessage`, and both `HasDefault` and `Default` are tagged
   `json:"-"`.

   **Decision — `has_default` is not an accepted input key.** A default is
   declared by the presence of `"default"` and by nothing else. Accepting
   both would give one fact two sources of truth on the wire, which is the
   shape of bug this correction exists to close: an agent sending `default`
   without `has_default`, or the two disagreeing, has no defensible answer.
   `MarshalJSON` writes `"default"` only when `HasDefault` is set, so a
   schema round trips to the same `HasDefault` it started with, and
   `has_default` never appears on the wire in either direction.

   **An explicit `"default": null` is no default.** `null` is how this
   package spells "not set" everywhere else — `Validate` already treats a
   null value as an absent one — so a null default declares nothing rather
   than declaring a default of nil.

   `TestSchemaRoundTripPreservesADeclaredZeroValuedDefault`'s comment claimed
   it covered the path Tasks 3-6 use while covering only Go -> JSON -> Go; it
   now covers JSON -> Go first (parse an agent-shaped `field_schema`,
   validate a row against it) and the comment is true. Proved red by deleting
   the `f.HasDefault = true` inference:
   `TestParseSchemaInfersHasDefaultFromThePresenceOfTheDefaultKey` and
   `TestSchemaRoundTripPreservesADeclaredZeroValuedDefault` both fail.

4. **Defaults were applied raw, bypassing `coerce`.** `out[f.Key] = f.Default`
   meant `Default: 20` declared as a Go `int` stored `int(20)` while an
   explicit `20` in the row stored `float64(20)` — two rows of one type
   carrying two Go types for one field. Worse, a schema that never went
   through `Check()` wrote an unchecked value forever:
   `Schema{{Key: "b", Type: FieldBool, HasDefault: true, Default: "yes"}}.Validate(map[string]any{})`
   returned `map[b:"yes"]` with a nil error, and schemas read back from jsonb
   via `ParseSchema` are never re-`Check`ed, so it was reachable in
   production. The default now goes through `coerce` and a failure is
   reported at `fields.<key>` as `the schema's default is invalid: ...`.
   Proved red by `TestValidateNormalisesAnAppliedDefault` and
   `TestValidateRejectsAnUncheckedBadDefault`, neither of which the package
   had — the change broke no existing test, which was itself the finding.

5. **`NaN` and the infinities passed validation, bounds included.** `NaN`
   compares false against both `Min` and `Max`, so every number field
   accepted it; `+Inf` passed whenever `Max` was unset; a `NaN` default
   passed `Check()`. The row then died inside Task 4's bulk write as an
   opaque `json: unsupported value: NaN` with no field path. `coerce` now
   rejects a non-finite number with `must be a finite number` at the field
   path. The check sits in `coerce`, not in `toFloat`, so the message says
   what is actually wrong instead of claiming the value is not a number.

6. **`toFloat` did not accept every numeric shape its comment claimed.** It
   rejected `json.Number` — what `Decoder.UseNumber` produces, which the MCP
   SDK is free to do — and every unsigned and small integer width. One
   decoder setting would have broken every number field at once. Widened to
   `json.Number` and all the integer widths; the comment now says what it
   does and why finiteness is decided elsewhere.

7. **`Check()` accepted contradictory and meaningless declarations**, all
   returning nil: `Min: 70, Max: 1`; `Min`/`Max` on a `text` or `bool` field;
   `Options` on a non-enum field; duplicate or empty enum options; a
   whitespace-only key; a key containing a dot; keys differing only by case.
   There was no key-format rule at all. Agents author these schemas, and
   silence here becomes six hundred rows of a field that never validates.
   Each is now a problem at `field_schema[<i>]`.

   **Decision — the key rule is `^[a-z][a-z0-9_]*$`, at most 64 characters.**
   Lower_snake_case ASCII, starting with a letter. Every exclusion pays for
   itself:

   - No dot, because `fields.<key>` is the error path this package returns
     and a key holding a dot makes `fields.a.b` ambiguous between the field
     `a.b` and a nested `b` inside `a`.
   - No upper case, so two keys can never differ only by case. That is a
     collision no case-sensitive map catches and every human reader trips
     over; ruling it out by construction beats detecting it afterwards.
   - No whitespace, dashes or brackets, so a key stays a single token
     wherever it is later quoted, flattened or parsed — Task 8's flattening
     and the view query language both want that.
   - ASCII only, so two Unicode normalisations of one accented word cannot
     coexist as two different keys.

   Sixty-four characters is generous for a readable identifier and short
   enough to be usable verbatim as a column name if Task 8 ever needs one.

8. **`Required` plus a default made `Required` dead**, undocumented and
   untested, because `Validate`'s default branch runs before it could ever
   complain the field is missing. **Decision: the combination is rejected in
   `Check()`.** A field either has a fallback or it does not; the pair is
   always one of the two written by mistake, and declaration time is the only
   moment the author can still say which.

9. **`Check()` short-circuited on a bad key**, so `{Key: "", Type: "rgb"}`
   reported one problem where two existed, denting the package's "every
   problem in one pass" contract. A bad key is now recorded and the rest of
   the field is checked anyway; only the duplicate-key bookkeeping is skipped
   for a key that is not well formed.

10. **Unknown-field errors came out in map order** — three runs, three
    orderings. Tasks 3-6 return these strings to agents and will want golden
    tests. Unknown keys are now sorted and reported first, then each declared
    field in schema order, so the whole message is a function of the input
    alone.

11. **Enum matching compares exact bytes**, so `Normal` does not match
    `normal` and an NFD spelling does not match its NFC option. That is the
    intended behaviour, not an oversight: an option is a token the game
    declared, and Maestro does not decide on a game's behalf that two
    spellings are one word. It was undocumented and untested; it is now both
    (`TestValidateMatchesEnumOptionsByExactBytes`).

12. **Six negative tests passed for the wrong reason.** The reviewer proved
    each of these mutations survived all 26 tests: replacing the whole text
    branch with `return fmt.Sprint(raw), nil` — silently stringifying maps,
    numbers and bools, the most dangerous coercion the package could make —
    survived, because there was no negative test for `text`/`longtext` at
    all; swapping the two bound messages survived, and so did skipping the
    minimum check for non-integral values, because `TestValidateEnforcesRange`
    asserted only `err != nil`; making enum comparison case-insensitive
    survived; dropping the list element check survived; and there was no
    `Validate`-level negative test for `bool` at all.

    Every negative test now asserts the exact message and path, so it can
    only be satisfied by the failure it names, and each of the six mutations
    was re-run and confirmed red. The standard for the rest of the plan: a
    negative test that asserts only `err != nil` is not a test of the thing
    it is named after.

13. **`Schema.CheckValues(values) error` is the re-validation path.** The
    plan's `TestSchemaChangeFlagsRowsInvalidWithoutTouchingThem` (Task 4)
    needs hundreds of stored rows re-validated against a new schema and
    flagged invalid *without altering their data*, and the only entry point
    was `Validate`, which returns a normalised map with defaults injected —
    so Task 4 had to remember to discard it or it would back-fill silently,
    the exact thing the design forbids. `CheckValues` applies precisely
    `Validate`'s rules and returns only an error, so there is nothing to
    forget. **Task 4 must use it** for the flagging pass; `Validate` is for
    writes.

14. **`ErrInvalidSchema` is a distinct sentinel from `ErrSchemaViolation`.**
    `Check()` used to return an error satisfying
    `errors.Is(err, ErrSchemaViolation)` — the same sentinel as a bad row of
    values, distinguishable only by path convention, so an MCP surface
    wanting `invalid_schema` separate from `schema_violation` would have had
    to match on path spelling. `Check()` now returns `*SchemaError`, whose
    message leads with `invalid_schema:` and which satisfies
    `ErrInvalidSchema` and deliberately *not* `ErrSchemaViolation`. The two
    failures differ in who is at fault: a declaration that cannot stand,
    versus a row that does not fit a declaration that can. **Task 7 puts this
    on the wire**; adding it now is free and adding it later would be a wire
    change.

**Corrections from the re-review** (a third pass over the package, made
after eight fix commits landed correction 3-14; approved with these
findings):

15. **Unknown keys in a field declaration were silently dropped — the
    original bug again, one level up.** `UnmarshalJSON`'s plain
    `json.Unmarshal` into `fieldJSON` ignored anything it did not know:
    `[{"key":"a","type":"text","nonsense":42,"defualt":"typo"}]` parsed
    clean, `Check()` returned nil, and re-encoding lost the typo'd
    `"defualt"` without a trace — exactly the defect the whole `HasDefault`
    correction (3 above) existed to fix, now reachable through any other
    misspelled key. `Validate`'s own doc comment already states the
    opposite contract for values ("an unknown field is an error, never
    dropped"); the declaration path was doing the reverse. `UnmarshalJSON`
    now decodes through a `json.Decoder` with `DisallowUnknownFields`,
    which also turns a stray `has_default` — plausible from an agent that
    remembers the pre-correction-3 wire shape — into an explicit error
    instead of silence. `TestParseSchemaIgnoresHasDefaultOnTheWire` asserted
    the old, wrong behaviour; it is now
    `TestParseSchemaRejectsHasDefaultOnTheWire`, alongside
    `TestParseSchemaRejectsAnUnknownKeyInAFieldDeclaration`. Proved red by
    reverting to a plain `json.Unmarshal`: both tests failed.
16. **`ParseSchema`'s decode failure satisfied no sentinel.** A
    `field_schema` that is not an array, an array of scalars, or truncated
    JSON returned a bare `fmt.Errorf("decode field schema: %w", …)`, and
    `errors.Is(err, ErrInvalidSchema)` was false — the sentinel split
    correction 14 added exists precisely so Task 7 need not match on path
    spelling, and the commonest wire failure of all fell outside the set.
    Wrapped with `ErrInvalidSchema` alongside the underlying error. Proved
    red by reverting the wrap: `TestParseSchemaWrapsAMalformedSchemaAsErrInvalidSchema`
    failed on all three malformed shapes it covers.
17. **Non-finite bounds were accepted and silently disabled the bound.**
    Correction 5 rejects a non-finite *value*; nothing rejected `Min: NaN`
    or an infinite `Max`. `coerce`'s own comment already gives the
    reasoning ("NaN compares false against every bound") — it was never
    applied to the bounds themselves. Unreachable over JSON, since
    `json.Unmarshal` never produces `NaN`/`Inf`, but reachable from any Go
    caller building a `Schema` in code, which Tasks 3 and 4 both do.
    `Check()` now rejects a non-finite `Min` or `Max` with `"min must be
    finite"` / `"max must be finite"`. Proved red by removing the two new
    checks: `TestSchemaRejectsANonFiniteMinimum`,
    `TestSchemaRejectsANonFiniteMaximum` and
    `TestSchemaRejectsANonFiniteMinimumEvenReachedOnlyFromGo` all failed.
18. **`MarshalJSON` laundered an invalid schema into a valid one.**
    `HasDefault: true, Default: nil` encoded with no `"default"` key and
    re-parsed as `HasDefault: false` — silently turning a state `Check()`
    rejects into one that round-trips clean, on the premise (stated
    explicitly by the re-review) that a schema may never have been through
    `Check()` before it reaches the wire. **Decision: reject it at marshal
    time.** `MarshalJSON` now returns an error for `HasDefault` set with a
    nil `Default`, so the round trip's "lossless" claim holds because that
    state can no longer enter it, rather than merely being untested in the
    one direction that mattered. Proved red by allowing the encode:
    `TestFieldMarshalJSONRejectsHasDefaultWithANilDefault` failed.
19. **Go-native `[]string` defaults were rejected while `[]any` was
    accepted.** `Default: []string{"x"}` on a `list<text>` field failed
    `Check()` with `expected a list of text, got []string`, while the same
    schema after a JSON round trip passed, because a JSON decoder only ever
    produces `[]any`. `Default: 7` already worked on a number field because
    `toFloat` widens every numeric shape; lists were the only field kind
    left with this asymmetry, and a test written with the idiomatic Go
    literal would hit it. **Decision: accept both.** `coerce`'s
    `FieldListText` branch now takes a `[]string` fast path alongside its
    `[]any` path. Proved red by removing the `[]string` branch:
    `TestSchemaAcceptsAGoNativeStringSliceDefaultOnAListTextField` failed.
20. **Pinned the untested edges** the re-review named as surviving
    mutations: `maxKeyLen`'s boundary (`TestMaxKeyLenBoundary`, exactly 64
    accepted, 65 rejected — pins `>` against a `>=` mutant);
    `ParseSchema([]byte{})` returning a non-nil `Schema{}`
    (`TestParseSchemaOfEmptyBytesReturnsAnEmptyNonNilSchema`); a nil
    `Schema.JSON()` encoding as `[]` rather than `null`
    (`TestNilSchemaJSONEncodesAsAnEmptyArrayNotNull`, a Task 3 storage
    concern — a nil-schema write must never store SQL `NULL`); and the two
    previously-unpinned messages, `"an enum field needs options"`
    (`TestSchemaRejectsEnumWithoutOptionsMessage`) and the `"default: "`
    prefix (`TestSchemaRejectsABadDefaultWithTheDefaultPrefix`). None of
    these needed a code change; they close gaps the re-review found by
    mutation testing, not behaviour bugs.
21. **The plan's Task 3 block called the wrong API.** The re-validation
    sweep at what is now the Step 6 code block still read
    `if _, err := schema.Validate(values); err != nil {`, while correction
    13 already says Task 4 must use `CheckValues` — an earlier
    regeneration pass refreshed the three Task 2 source blocks and missed
    this one, and Task 3's implementer would have copied the block, not the
    correction, silently reintroducing the back-fill-on-flag bug correction
    13 exists to prevent. Fixed to `if err := schema.CheckValues(values);
    err != nil {`. The rest of the plan's `.Validate(` call sites (Task 4's
    `CreateEntity` and Task 5's `CreateRelation`, both writes that need the
    normalised map back) were checked and are correct as written; this was
    the only re-validation site outside `CheckValues`'s own doc comment and
    correction 13's prose.
22. **The key-policy split is recorded as a third open item below, and
    deliberately not resolved here.** `keyPattern` constrains field keys
    only; entity keys, entity-type keys and relation-type keys are
    unconstrained `text` in `internal/db/migrations/0004_metamodel.sql`
    (lines 34, 65, 94), with `UNIQUE (project_id, lower(key))` indexes — so
    the database *folds* case for those keys while `Check()` *forbids* it
    for field keys, and `Key: "Hogger"` / `Key: "hogger"` collide at the
    database with a raw unique violation and no field path. This is a
    product decision, not a validator fix, and neither the migration nor
    `keyPattern` were touched.

**Open, deliberately not implemented here** — decisions for the tasks that
first feel them, recorded so they are not rediscovered:

- **Schema-evolution classification.** Nothing tells a caller whether an edit
  to a field schema *widens* it (a new optional field, a new enum option, a
  loosened bound — every stored row stays valid) or *narrows* it (a new
  required field, a removed option, a tightened bound, a changed type — some
  stored rows become invalid). Task 4 needs the distinction to decide whether
  a schema edit can skip the re-validation sweep entirely, and Task 3 is
  where the edit is accepted, so **this is a Task 3/4 decision**. Until it
  lands, a schema edit must assume it narrows and sweep with `CheckValues`.
- **Reserved-key policy.** Nothing stops a game declaring a field named
  `key`, `name`, `id`, `type` or `version`. There is no live collision today
  because game fields live inside the `fields` jsonb, walled off from the
  row's own columns — but **Task 8's flattening is where it would bite**, the
  moment a field is lifted alongside an entity's own attributes in a REST
  payload or a page's data model. **This is a Task 3/8 decision**: either
  reserve a list in `Check()` (cheap, and a breaking change once games exist)
  or keep the namespaces separated by construction wherever flattening
  happens.
- **Key-policy split between field keys and every other key kind.**
  `keyPattern` (lower_snake_case ASCII, no case variance) constrains field
  keys only. Entity keys, entity-type keys and relation-type keys are
  declared as unconstrained `text` in
  `internal/db/migrations/0004_metamodel.sql:34,65,94`, and their uniqueness
  indexes are `UNIQUE (project_id, lower(key))` — so the database *folds*
  case for those keys while `Check()` *forbids* it for field keys. Two
  incompatible key policies live in one metamodel: `Key: "Hogger"` and
  `Key: "hogger"` pass every check this package runs and then collide at
  the database with a raw unique-violation error and no field path.
  **Settled in Task 3** (see its correction 4): row keys get their own
  rule in `internal/metamodel/keys.go` — `^[A-Za-z0-9][A-Za-z0-9_-]*$`,
  at most 64 characters — which permits case, because the folding unique
  index already delivers the guarantee `keyPattern` gets by forbidding it;
  and a spelling differing from a stored key only by case is refused with
  a message naming both spellings, instead of silently updating the other
  row or surfacing a raw unique violation. Neither the migration nor
  `keyPattern` was changed.

---

### Task 3: Entity types

**Files:**
- Create: `internal/db/queries/metamodel.sql`, `internal/metamodel/service.go`,
  `internal/metamodel/keys.go`, `internal/metamodel/types.go`
- Test: `internal/metamodel/types_test.go`

Every code block below was refreshed from the landed files after the task
was implemented, so what they show is what shipped. The corrections block
at the end of the task records what changed from the original plan and
why.

- [x] **Step 1: Write the queries**

`internal/db/queries/metamodel.sql`:

```sql
-- name: UpsertEntityType :one
-- Every statement in this file filters on the resolved project id,
-- including the ones addressing a row by its primary key: isolation
-- between games is enforced in SQL, not in Go, so a query trusting an id
-- alone would hand a caller another game's row the moment an id leaked.
--
-- No write here sets updated_at. 0004_metamodel.sql puts a set_updated_at
-- trigger on all four tables, so the column has one mechanism behind it
-- rather than a trigger plus a clause every future query must remember.
--
-- The DO UPDATE is guarded by the caller's expected version, so the whole
-- compare-and-set happens in one statement and two concurrent writers
-- cannot both read version 1 and both succeed. A creating caller has no
-- version to expect and passes noVersion, which no stored version can
-- equal, so the guard is a no-op on the insert path and a guaranteed
-- mismatch when a row it did not know about turns out to exist. A guard
-- that fails returns no row rather than an error; UpsertEntityType turns
-- that into the typed conflict.
--
-- Idempotent by (project, key), matched case-insensitively through the
-- entity_types_key_key index. The key column itself is deliberately not in
-- the SET list: the first spelling stored stays, so a re-seed cannot
-- rewrite the handle other rows and documents refer to. UpsertEntityType
-- in Go refuses a spelling that differs from the stored one before it ever
-- gets here, so this clause is what makes the identical spelling a no-op
-- rather than what resolves a conflict.
INSERT INTO entity_types (project_id, key, label, label_plural, description, color, icon,
                          field_schema, updated_by_user_id, updated_by_token_id)
VALUES (sqlc.arg('project_id')::uuid, sqlc.arg('key')::text, sqlc.arg('label')::text,
        sqlc.arg('label_plural')::text, sqlc.arg('description')::text,
        sqlc.arg('color')::text, sqlc.arg('icon')::text, sqlc.arg('field_schema')::jsonb,
        sqlc.narg('updated_by_user_id')::uuid, sqlc.narg('updated_by_token_id')::uuid)
ON CONFLICT (project_id, lower(key)) DO UPDATE
SET label               = excluded.label,
    label_plural        = excluded.label_plural,
    description         = excluded.description,
    color               = excluded.color,
    icon                = excluded.icon,
    field_schema        = excluded.field_schema,
    version             = entity_types.version + 1,
    updated_by_user_id  = excluded.updated_by_user_id,
    updated_by_token_id = excluded.updated_by_token_id
WHERE entity_types.version = sqlc.arg('expected_version')::integer
RETURNING *;

-- name: GetEntityTypeByKey :one
SELECT * FROM entity_types
WHERE project_id = sqlc.arg('project_id')::uuid AND lower(key) = lower(sqlc.arg('key')::text);

-- name: GetEntityTypeByID :one
SELECT * FROM entity_types
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;

-- name: ListEntityTypes :many
-- Ordered by label then id: labels are not unique, and a label-only order
-- reshuffles ties between calls in whatever order Postgres returns them.
SELECT * FROM entity_types
WHERE project_id = sqlc.arg('project_id')::uuid
ORDER BY label, id;

-- name: CountEntitiesOfType :one
SELECT count(*) FROM entities
WHERE project_id = sqlc.arg('project_id')::uuid
  AND entity_type_id = sqlc.arg('entity_type_id')::uuid;

-- name: DeleteEntityType :execrows
DELETE FROM entity_types
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;

-- name: DeleteEntitiesOfType :exec
DELETE FROM entities
WHERE project_id = sqlc.arg('project_id')::uuid
  AND entity_type_id = sqlc.arg('entity_type_id')::uuid;

-- name: ListEntityFieldsOfType :many
-- The re-validation sweep reads nothing but the stored values and the id
-- to flag, so it does not pay to load whole rows — a type can hold
-- hundreds of entities and every schema edit sweeps all of them.
SELECT id, fields FROM entities
WHERE project_id = sqlc.arg('project_id')::uuid
  AND entity_type_id = sqlc.arg('entity_type_id')::uuid
ORDER BY id;

-- name: MarkEntitiesOfTypeInvalid :exec
-- The invalid <> flag guard keeps the sweep from rewriting rows whose
-- verdict has not changed. Without it, every schema edit would touch each
-- of the type's entities and the set_updated_at trigger would move their
-- updated_at, so a validation pass would read as an edit of content
-- nobody edited.
UPDATE entities SET invalid = sqlc.arg('invalid')::boolean
WHERE project_id = sqlc.arg('project_id')::uuid
  AND entity_type_id = sqlc.arg('entity_type_id')::uuid
  AND id = ANY(sqlc.arg('ids')::uuid[])
  AND invalid <> sqlc.arg('invalid')::boolean;

-- name: GetEntityTypeByKeyForUpdate :one
-- The upsert's own read, taken inside its transaction with the row lock
-- held, so the spelling and version a caller is told about are the ones
-- their write will actually meet. Without the lock, two writers racing on
-- one key both read the same version and the second's report of "current
-- version is N" is stale by the time it is returned.
SELECT * FROM entity_types
WHERE project_id = sqlc.arg('project_id')::uuid AND lower(key) = lower(sqlc.arg('key')::text)
FOR UPDATE;
```

Run: `make sqlc`

- [x] **Step 2: Write the failing test**

`internal/metamodel/types_test.go`. Not reproduced here: it is some
seven hundred lines and every one of its assertions is pinned to an exact
message and path, which does not compress into a plan block. What it
covers, by test name:

- `TestUpsertEntityTypeIsIdempotentByKey`,
  `TestUpsertEntityTypeStoresTheDeclaredSchemaAndTheActor`
- `TestUpsertEntityTypeRejectsStaleVersion`,
  `TestUpsertEntityTypeRejectsAMissingExpectedVersionOnAnExistingType`,
  `TestACreationThatLosesTheRaceForItsKeyIsRefused`
- `TestUpsertEntityTypeRejectsBadSchema`, `TestUpsertEntityTypeRequiresAKey`,
  `TestUpsertEntityTypeRejectsAKeyThatIsNotAHandle`,
  `TestUpsertEntityTypeAcceptsTheKeysAGameActuallyWrites`,
  `TestUpsertEntityTypeRejectsAnOverlongKey`,
  `TestUpsertEntityTypeRefusesAKeyThatDiffersOnlyByCase`
- `TestSchemaChangeFlagsRowsInvalidWithoutTouchingThem`,
  `TestSchemaChangeDoesNotBackFillDeclaredDefaults`,
  `TestSchemaChangeClearsTheFlagWhenTheRowFitsAgain`
- `TestRemoveEntityTypeRefusesWhenInUse`,
  `TestRemoveEntityTypeReportsAnUnknownID`
- `TestTypesAreScopedToTheirProject`, `TestListEntityTypesIsOrderedByLabel`

The file also holds the helpers Tasks 4-6 reuse: `newProject(t, pool)`,
`insertEntity`, `entityState`, `newUser`, `requireFieldError`,
`ptrInt32`, `ptrFloat`. Note the signature: `newProject` takes the pool,
not the service — see correction 1.

- [x] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/metamodel/ -run TestUpsertEntityType -v`
Expected: FAIL, `undefined: metamodel.New`.

- [x] **Step 4: Write the service scaffolding**

`internal/metamodel/service.go`:

```go
package metamodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/realtime"
)

// Actor records who performed a write, for the audit columns. Both fields
// are optional and both may be set at once: a human editing through the UI
// carries a UserID, an agent carries the TokenID of the credential it
// authenticated with, and a token always belongs to the member who minted
// it. A token id that belongs to another game is rejected by the database,
// not here (0004_metamodel.sql's composite keys).
type Actor struct {
	UserID  *uuid.UUID
	TokenID *uuid.UUID
}

// Service is the metamodel domain.
type Service struct {
	pool *pgxpool.Pool
	q    *dbq.Queries
	hub  *realtime.Hub
}

// New builds the service. The hub may be nil, in which case nothing is
// published; the package's own tests run that way.
func New(pool *pgxpool.Pool, hub *realtime.Hub) *Service {
	return &Service{pool: pool, q: dbq.New(pool), hub: hub}
}

// publish emits a change event, if a hub is attached.
//
// minRole and humanOnly are passed explicitly rather than inferred from
// kind, exactly as internal/web's own publish does, so each call site
// shows the gating it chose instead of inheriting one from a table three
// files away. The values themselves are named constants declared beside
// the kind they belong to, in events.go, which is where the reasoning
// for each lives; a helper that could not express these fields at all —
// the shape this package shipped with — silently made every event as
// open as the hub's zero value, whether or not that was the right answer.
//
// A payload carries only the identity of what changed — a key, an id —
// and never a value a client could then treat as current. Publication
// order is not commit order (internal/web/publish.go's package comment
// works through why), so a payload holding, say, a type's new label could
// stably tell a client the wrong label with nothing to signal it. The
// client re-reads instead.
//
// **Every caller must call this after withTx has returned, never from
// inside fn.** An event published inside the transaction announces a
// change that may still roll back, and a subscriber that re-reads on
// hearing it — which is the only thing this hub's payloads let it do —
// would read the state before the change and cache it as the state
// after. The hub itself cannot enforce that; the metamodel's own tests
// pin it (TestNoEventIsPublishedWhenTheWriteIsRolledBack and
// TestNothingIsAnnouncedWhileTheTransactionIsStillOpen).
func (s *Service) publish(projectID uuid.UUID, kind string, minRole roles.Role, humanOnly bool, payload any) {
	if s.hub == nil {
		return
	}
	s.hub.Publish(realtime.Event{
		ProjectID: projectID,
		Kind:      kind,
		MinRole:   string(minRole),
		HumanOnly: humanOnly,
		Payload:   payload,
	})
}

// withTx runs fn inside a transaction, rolling back unless it returns nil.
//
// Every mutation in this package needs one: a write and the re-validation
// sweep that follows it are one change, and half of it landing would leave
// a type declaring a schema its own entities were never re-checked against.
func (s *Service) withTx(ctx context.Context, fn func(*dbq.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(dbq.New(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// decodeFields turns a stored jsonb blob back into a value map.
func decodeFields(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode fields: %w", err)
	}
	return out, nil
}

// notFound maps pgx's no-rows sentinel onto the domain's, leaving every
// other error wrapped with what was being looked up.
func notFound(err error, what string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("%s: %w", what, err)
}
```

- [x] **Step 5: Settle the row-key policy**

`internal/metamodel/keys.go`. This file is the answer to the open item
Task 2's corrections left for this task; correction 4 below argues the
decision.

```go
package metamodel

import (
	"fmt"
	"regexp"
)

// maxRowKeyLen caps the keys that address rows. Sixty-four matches
// maxKeyLen, the field-key cap, for the same reasons: generous for a
// readable handle, short enough to sit in a URL path segment, an export
// filename or a view-query token without anyone thinking about it.
const maxRowKeyLen = 64

// rowKeyPattern is the rule for the keys that *address rows* — entity-type
// keys, relation-type keys and entity keys. It is deliberately wider than
// keyPattern, the rule for the field keys inside a row's jsonb, and the
// difference is not an inconsistency:
//
//   - keyPattern forbids upper case because two field keys differing only
//     by case would be two distinct keys in a jsonb object, and nothing
//     downstream would catch the collision.
//   - A row key cannot produce that collision at all. Every uniqueness
//     index over these keys is UNIQUE (project_id, lower(key)) — the
//     database folds case for them (0004_metamodel.sql) — so "Hogger" and
//     "hogger" are one key, by construction, in the only place it matters.
//
// So both rules deliver the same guarantee, "no two keys differ only by
// case", through the mechanism each context actually has. Forbidding upper
// case in row keys as well would buy nothing and cost the thing the
// folding index was chosen for: a game whose own vocabulary capitalises
// its handles ("Hogger", "Elwynn_Forest", "GP_Monaco") can spell them the
// way its design documents do, and the spelling the designer chose is the
// one stored — Maestro neither folds it nor rewrites it. What the folding
// index buys is that a re-seed under a different casing addresses the
// existing row rather than creating a twin beside it; it does not update
// it silently, and it is not meant to. keyRespellingError below refuses
// exactly that write, naming both spellings, because a changed casing is
// far likelier to be a typo than a deliberate rename of a handle other
// rows and documents already refer to.
//
// What the pattern does exclude earns its place:
//
//   - No dot, slash, space or percent, so a key drops into a REST path
//     segment (Task 8 addresses a type as /games/<slug>/types/<key>) and
//     into a view query without escaping, and cannot be mistaken for a
//     path separator or a nested reference.
//   - No leading punctuation, so no key can look like a flag, an option
//     or a relative path to a shell, a CLI or a query parser.
//   - ASCII only, so two Unicode normalisations of one accented word
//     cannot coexist as two keys — lower() folds case, not normalisation
//     form, so the index would let both through.
//
// Digits are allowed to lead: "1999_season" and "500_miles" are ordinary
// entity keys for a racing game, and there is no reason to make a designer
// rename their content to satisfy an identifier convention Maestro does
// not otherwise impose.
var rowKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// rowKeyProblems validates a key that addresses a row, reporting the
// problem at the caller's path ("key") so an agent sees where it is.
//
// A key problem is a ValidationError, not a SchemaError: the caller is
// writing a row, not declaring a schema, and the sentinel split states
// exactly that difference (see SchemaError's doc comment).
//
// It hands back the problems rather than a wrapped error so that a caller
// checking a key *and* a row's descriptive columns reports both in one
// ValidationError, rather than making an agent fix the key, call again,
// and only then learn the label was empty too. At most one problem is
// ever reported for a single key: the three conditions are ordered from
// most to least fundamental, and telling a caller their empty key is also
// not a handle helps nobody.
func rowKeyProblems(path, key string) []FieldError {
	switch {
	case key == "":
		return []FieldError{{Path: path, Message: "is required"}}
	case len(key) > maxRowKeyLen:
		return []FieldError{{
			Path:    path,
			Message: fmt.Sprintf("must be at most %d characters", maxRowKeyLen),
		}}
	case !rowKeyPattern.MatchString(key):
		return []FieldError{{
			Path:    path,
			Message: "must be letters, digits, underscores or hyphens, starting with a letter or a digit",
		}}
	}
	return nil
}

// keyRespellingError is what a caller sees when their key matches an
// existing row's key only case-insensitively.
//
// This is the collision the folding index makes possible, and it is the
// one place the row-key policy has to say something a designer can act on.
// Silently updating the differently-spelled row would let a typo'd capital
// overwrite content; letting the database raise it would surface as a raw
// unique-violation with no field path, or — since the upsert carries an ON
// CONFLICT clause — as a version conflict, which says nothing about the
// actual problem. Naming both spellings and both remedies is the whole
// answer a designer needs, and they never have to know an index folds
// case.
func keyRespellingError(path, requested, stored string) error {
	return &ValidationError{Fields: []FieldError{{
		Path: path,
		Message: fmt.Sprintf(
			"%q already exists here spelled %q, and keys are matched without regard to case: "+
				"use %q to update it, or pick a key that differs by more than capitalisation",
			requested, stored, stored),
	}}}
}
```

- [x] **Step 6: Write the entity-type operations**

`internal/metamodel/types.go`:

```go
package metamodel

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// noVersion is the expected_version an upsert passes when its caller has
// no version to expect. Versions start at 1 and only ever climb, so no
// stored row can equal it: the guarded DO UPDATE is then a no-op on the
// insert path and a guaranteed mismatch if a row turns out to exist after
// all.
const noVersion int32 = -1

// EntityTypeInput is an upsert request.
//
// ExpectedVersion must match the stored version when the type already
// exists; a nil ExpectedVersion against an existing type is a conflict,
// not an overwrite.
//
// On creation there is nothing to match, but the field is *not* ignored:
// it is still passed as the guard on the upsert's DO UPDATE, because a
// caller that believes it is creating may in fact be racing a creator, and
// the guard is the only thing standing between the loser of that race and
// a silent overwrite. A seeding script may therefore pass the same value
// on every run, but a value it did not read from this service is not a
// free pass — it is a claim about a row, checked as one.
type EntityTypeInput struct {
	Key             string
	Label           string
	LabelPlural     string
	Description     string
	Color           string
	Icon            string
	Schema          Schema
	ExpectedVersion *int32
	Actor           Actor
}

// entityTypeEvent is the payload of the type.* events. It carries the
// identity of what changed and nothing else; see Service.publish.
type entityTypeEvent struct {
	ID  uuid.UUID `json:"id"`
	Key string    `json:"key"`
}

// UpsertEntityType creates or updates a type, addressed by its key.
//
// The whole operation is one transaction: the type's row and the verdict
// on every entity already stored against it change together, so a schema
// edit can never land with its instances left judged by the old schema.
func (s *Service) UpsertEntityType(ctx context.Context, projectID uuid.UUID, in EntityTypeInput) (dbq.EntityType, error) {
	problems := rowKeyProblems("key", in.Key)
	problems = append(problems,
		checkDescriptors(in.Label, in.LabelPlural, in.Description, in.Color, in.Icon)...)
	if len(problems) > 0 {
		return dbq.EntityType{}, &ValidationError{Code: codeInvalidInput, Fields: problems}
	}
	if err := in.Schema.Check(); err != nil {
		return dbq.EntityType{}, err
	}
	raw, err := in.Schema.JSON()
	if err != nil {
		return dbq.EntityType{}, fmt.Errorf("encode field schema: %w", err)
	}

	expected := noVersion
	if in.ExpectedVersion != nil {
		expected = *in.ExpectedVersion
	}

	var row dbq.EntityType
	err = s.withTx(ctx, func(q *dbq.Queries) error {
		// Read under the row lock, so the spelling and the version this
		// caller is told about are the ones its own write will meet.
		existing, err := q.GetEntityTypeByKeyForUpdate(ctx, dbq.GetEntityTypeByKeyForUpdateParams{
			ProjectID: projectID, Key: in.Key,
		})
		switch {
		case err == nil:
			if existing.Key != in.Key {
				return keyRespellingError("key", in.Key, existing.Key)
			}
			if in.ExpectedVersion == nil || *in.ExpectedVersion != existing.Version {
				return &VersionConflictError{Current: existing.Version}
			}
		case errors.Is(err, pgx.ErrNoRows):
			// Creation: no version to match, nothing to lock.
		default:
			return fmt.Errorf("lookup entity type: %w", err)
		}

		row, err = q.UpsertEntityType(ctx, dbq.UpsertEntityTypeParams{
			ProjectID:        projectID,
			Key:              in.Key,
			Label:            in.Label,
			LabelPlural:      in.LabelPlural,
			Description:      in.Description,
			Color:            in.Color,
			Icon:             in.Icon,
			FieldSchema:      raw,
			ExpectedVersion:  expected,
			UpdatedByUserID:  in.Actor.UserID,
			UpdatedByTokenID: in.Actor.TokenID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// The guarded DO UPDATE matched nothing: between the read above
			// and this statement another writer created or advanced the row.
			return conflictOnEntityTypeKey(ctx, q, projectID, in.Key)
		}
		if err != nil {
			if mapped := actorConstraintViolation(err); errors.Is(mapped, ErrActorNotInGame) {
				return mapped
			}
			return fmt.Errorf("upsert entity type: %w", err)
		}
		// The locked read above cannot be the only place the spelling is
		// checked. It runs before the write and only ever sees a row that is
		// already visible, so on the creation path — where there is nothing
		// to lock — a writer racing a creator, holding an ExpectedVersion
		// that happens to match the version the winner lands on, passed both
		// the read and the guarded DO UPDATE and updated a row it never saw,
		// stored under a different spelling, returning no error at all. The
		// upsert returns the row it actually touched, so comparing the stored
		// spelling to the submitted one *after* the write closes the pre-read
		// path and the race with one check; withTx rolls the write back.
		if row.Key != in.Key {
			return keyRespellingError("key", in.Key, row.Key)
		}

		// A schema change can invalidate stored rows. Re-check them rather
		// than rejecting the change or inventing values for a new field.
		return s.revalidateEntitiesOfType(ctx, q, row)
	})
	if err != nil {
		return dbq.EntityType{}, err
	}

	s.publish(projectID, eventTypeUpserted, typeEventMinRole, typeEventHumanOnly,
		entityTypeEvent{ID: row.ID, Key: row.Key})
	return row, nil
}

// conflictOnEntityTypeKey re-reads a key whose guarded upsert matched no
// row and names what actually stands in the way. Both outcomes are real:
// the winning writer may have created the key with a different spelling,
// or advanced a version this caller was holding.
func conflictOnEntityTypeKey(ctx context.Context, q *dbq.Queries, projectID uuid.UUID, key string) error {
	row, err := q.GetEntityTypeByKey(ctx, dbq.GetEntityTypeByKeyParams{ProjectID: projectID, Key: key})
	if err != nil {
		return fmt.Errorf("re-read entity type after a failed upsert: %w", err)
	}
	if row.Key != key {
		return keyRespellingError("key", key, row.Key)
	}
	return &VersionConflictError{Current: row.Version}
}

// EntityTypeByKey loads one type by its key, matched without regard to
// case, as every key in this domain is.
func (s *Service) EntityTypeByKey(ctx context.Context, projectID uuid.UUID, key string) (dbq.EntityType, error) {
	row, err := s.q.GetEntityTypeByKey(ctx, dbq.GetEntityTypeByKeyParams{ProjectID: projectID, Key: key})
	if err != nil {
		return dbq.EntityType{}, notFound(err, "lookup entity type")
	}
	return row, nil
}

// EntityTypeByID loads one type by its id. The id is not enough on its
// own: the query filters on the project too, so an id belonging to
// another game reads as not found rather than as somebody else's type.
func (s *Service) EntityTypeByID(ctx context.Context, projectID, id uuid.UUID) (dbq.EntityType, error) {
	row, err := s.q.GetEntityTypeByID(ctx, dbq.GetEntityTypeByIDParams{ProjectID: projectID, ID: id})
	if err != nil {
		return dbq.EntityType{}, notFound(err, "lookup entity type")
	}
	return row, nil
}

// ListEntityTypes returns every type of a project.
func (s *Service) ListEntityTypes(ctx context.Context, projectID uuid.UUID) ([]dbq.EntityType, error) {
	rows, err := s.q.ListEntityTypes(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list entity types: %w", err)
	}
	return rows, nil
}

// RemoveEntityType deletes a type. Without cascade, a type that still has
// entities is refused: silently deleting a game's content is never the
// right reading of "remove this type".
func (s *Service) RemoveEntityType(ctx context.Context, projectID, id uuid.UUID, cascade bool) error {
	var removedKey string
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		// Read the row before deleting it, for its key: type.removed
		// carries the same {id, key} identity type.upserted does, and a
		// removal announced with an empty key tells a subscriber a type
		// keyed "" is gone. The id alone would have been a defensible
		// payload, but entityTypeEvent declares a key field and a client
		// reading one cannot tell "not carried" from "empty".
		typ, err := q.GetEntityTypeByID(ctx, dbq.GetEntityTypeByIDParams{ProjectID: projectID, ID: id})
		if err != nil {
			return notFound(err, "lookup entity type")
		}
		removedKey = typ.Key

		// The count is not the only thing standing between a caller and a
		// silently emptied type: entities.entity_type_id is ON DELETE
		// RESTRICT, so the delete below fails on its own if any instance
		// exists, and removing this check alone leaves
		// TestRemoveEntityTypeRefusesWhenInUse green. What it earns is the
		// refusal arriving as a typed ErrInUse without a transaction
		// aborting on a raw constraint violation first.
		if !cascade {
			count, err := q.CountEntitiesOfType(ctx, dbq.CountEntitiesOfTypeParams{
				ProjectID: projectID, EntityTypeID: id,
			})
			if err != nil {
				return fmt.Errorf("count entities: %w", err)
			}
			if count > 0 {
				return ErrInUse
			}
		} else if err := q.DeleteEntitiesOfType(ctx, dbq.DeleteEntitiesOfTypeParams{
			ProjectID: projectID, EntityTypeID: id,
		}); err != nil {
			return fmt.Errorf("delete entities: %w", err)
		}

		rows, err := q.DeleteEntityType(ctx, dbq.DeleteEntityTypeParams{ProjectID: projectID, ID: id})
		if err != nil {
			// The RESTRICT foreign key described above is what catches an
			// entity written between the count and this statement. It is
			// the same refusal, and a caller should not have to tell a race
			// apart from the ordinary case.
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" {
				return ErrInUse
			}
			return fmt.Errorf("delete entity type: %w", err)
		}
		if rows == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return err
	}

	s.publish(projectID, eventTypeRemoved, typeEventMinRole, typeEventHumanOnly,
		entityTypeEvent{ID: id, Key: removedKey})
	return nil
}
```

- [x] **Step 7: Add the re-validation helper**

Appended to `internal/metamodel/types.go`:

```go
// revalidateEntitiesOfType re-checks every stored entity against its
// type's current schema and flags the ones that no longer fit. Nothing is
// deleted and nothing is back-filled: the designer decides what a newly
// required field should hold, and a validation pass is not an edit of
// their content.
//
// CheckValues, never Validate: Validate hands back a normalised map with
// declared defaults injected, and writing that back would silently
// back-fill every row the sweep touched.
func (s *Service) revalidateEntitiesOfType(ctx context.Context, q *dbq.Queries, typ dbq.EntityType) error {
	schema, err := ParseSchema(typ.FieldSchema)
	if err != nil {
		return err
	}

	rows, err := q.ListEntityFieldsOfType(ctx, dbq.ListEntityFieldsOfTypeParams{
		ProjectID: typ.ProjectID, EntityTypeID: typ.ID,
	})
	if err != nil {
		return fmt.Errorf("list entities: %w", err)
	}

	var invalid, valid []uuid.UUID
	for _, row := range rows {
		values, err := decodeFields(row.Fields)
		if err != nil {
			invalid = append(invalid, row.ID)
			continue
		}
		if err := schema.CheckValues(values); err != nil {
			invalid = append(invalid, row.ID)
			continue
		}
		valid = append(valid, row.ID)
	}

	for _, batch := range []struct {
		ids  []uuid.UUID
		flag bool
	}{{invalid, true}, {valid, false}} {
		if len(batch.ids) == 0 {
			continue
		}
		if err := q.MarkEntitiesOfTypeInvalid(ctx, dbq.MarkEntitiesOfTypeInvalidParams{
			ProjectID:    typ.ProjectID,
			EntityTypeID: typ.ID,
			Ids:          batch.ids,
			Invalid:      batch.flag,
		}); err != nil {
			return fmt.Errorf("flag entities: %w", err)
		}
	}
	return nil
}
```

- [x] **Step 8: Run the tests to verify they pass**

Run: `make check` with `TEST_DATABASE_URL` set.
Expected: PASS, seventeen tests in this file.

- [x] **Step 9: Commit**

```bash
git add internal/db/queries internal/db/dbq internal/metamodel
git commit -m "feat: entity types with optimistic concurrency and cascade removal"
```

**Corrections made during implementation** (a pass over the landed
package, written as Task 3 shipped so Tasks 4-6 build on what exists
rather than on what was planned):

1. **`Service.CreateBareProjectForTest` was not written.** It would have
   shipped in the binary, exported, and been callable from the MCP surface
   Task 7 mounts — production API whose only purpose is a test fixture.
   The test file creates its project with one `INSERT` through the pool it
   already holds, which needs no production surface and no import of
   `internal/projects`. **The helper's signature changed with it:**
   `newProject(t *testing.T, pool *pgxpool.Pool) uuid.UUID`. The Task 4,
   5 and 6 test blocks below still read `newProject(t, svc)`; they mean
   `newProject(t, pool)`.
2. **The plan's `TestUpsertEntityTypeRejectsBadSchema` asserted the wrong
   sentinel.** It expected `ErrSchemaViolation` from a schema whose enum
   declares no options, but Task 2's correction 14 made `Schema.Check`
   return a `*SchemaError` satisfying `ErrInvalidSchema` and deliberately
   *not* `ErrSchemaViolation` — the plan block predated the split. The
   landed test asserts `ErrInvalidSchema`, asserts `ErrSchemaViolation` is
   false, pins the exact problem (`field_schema[0]: an enum field needs
   options`), and checks nothing was stored.
3. **`Actor` was a dead field.** The plan declared it on
   `EntityTypeInput`, and `0004_metamodel.sql` has the
   `updated_by_user_id` / `updated_by_token_id` columns to hold it, but no
   code block ever passed it to a query: every write would have recorded a
   NULL editor. `UpsertEntityType` (the SQL) now takes both, on the insert
   and on the conflict update, and `TestUpsertEntityTypeStoresTheDeclaredSchemaAndTheActor`
   pins it. Proved red by passing `nil` in place of `in.Actor.UserID`.
4. **The row-key policy is settled here, as Task 2's third open item
   asked. Decision: entity-type, relation-type and entity keys get their
   own rule — `^[A-Za-z0-9][A-Za-z0-9_-]*$`, at most 64 characters —
   which permits case, and a spelling that differs from a stored key only
   by case is refused with a message naming both spellings.**

   The split with `keyPattern` is not an inconsistency once the reason for
   each rule is stated. `keyPattern` forbids upper case because two field
   keys differing only by case are two distinct keys inside a jsonb
   object and nothing downstream would ever catch the collision. A row key
   cannot produce that collision at all: every uniqueness index over these
   keys is `UNIQUE (project_id, lower(key))`, so the database folds case
   for them. Both rules deliver the same guarantee — no two keys differ
   only by case — through the mechanism each context actually has.

   The two alternatives were weighed and rejected:

   - *Extend `keyPattern`, forbidding capitals in row keys.* It buys
     nothing the folding index does not already give, and it costs the
     thing the folding index was chosen for. `0004_metamodel.sql` says so
     in its own comment on `entity_types_key_key`: keys are matched
     case-insensitively "so a second run with different casing collides
     with the existing row instead of creating a twin". Games capitalise
     their handles — `Hogger`, `Elwynn_Forest`, `GP_Monaco` — and a
     designer should not have to rename their content to satisfy an
     identifier convention Maestro does not otherwise impose. Leading
     digits are allowed for the same reason: `1999_season` and
     `500_miles` are ordinary entity keys.
   - *Fold case in Go before the database sees it.* Storing `hogger` for
     a designer who wrote `Hogger` silently rewrites the handle their
     design documents, exports and relations refer to. Maestro does not
     edit a game's vocabulary on its behalf.

   **What a designer sees at a collision.** This is the half the open item
   insisted on, and the reason the rule alone is not the whole answer.
   `Key: "Hogger"` exists; someone writes `Key: "hogger"`. Three outcomes
   were possible and only one is defensible:

   - The `ON CONFLICT (project_id, lower(key))` clause would have
     *silently updated* the existing row under the other spelling — a
     typo'd capital quietly overwriting content.
   - Letting it reach the database bare would raise SQLSTATE 23505 with
     no field path, or — because the upsert does carry an `ON CONFLICT` —
     a version conflict, which says nothing whatever about the real
     problem.
   - So the upsert reads the row first, under its lock, and refuses a
     spelling that differs from the stored one:

     ```
     key: "hogger" already exists here spelled "Hogger", and keys are
     matched without regard to case: use "Hogger" to update it, or pick a
     key that differs by more than capitalisation
     ```

     It names both spellings and both remedies, and a designer never has
     to know an index folds case. Re-seeding is unaffected: a script that
     spells its keys consistently never meets this, which is the whole
     population of correct callers.

   `rowKeyProblems` (named `checkRowKey` when it shipped; see correction
   23) returns a `*ValidationError` at path `key`, not a
   `*SchemaError`: the caller is writing a row, not declaring a schema,
   which is exactly the distinction the two sentinels carry. The
   migration was not touched, and `keyPattern` was not touched. **Tasks 4
   and 5 apply `rowKeyProblems` to entity keys and relation-type keys**; it
   is written once, in `internal/metamodel/keys.go`, for all three.
5. **The version check is a compare-and-set in SQL, not a read followed by
   a write.** The plan's `SELECT` then unguarded `ON CONFLICT DO UPDATE`
   is a lost update: two writers both read version 1, both succeed, and
   the second overwrites a row it never saw. The landed upsert reads
   through `GetEntityTypeByKeyForUpdate` (`FOR UPDATE`, inside the
   transaction, so the version a caller is told about is the one its write
   will meet) *and* guards the `DO UPDATE` with
   `WHERE entity_types.version = @expected_version`. A creating caller has
   no version to expect and passes `noVersion` (-1), which no stored
   version can equal — a no-op on the insert path, a guaranteed mismatch
   if a row turns out to exist. A guard that matches nothing returns no
   row, which `conflictOnEntityTypeKey` turns into the typed error, and it
   re-reads to say which conflict it actually is: a rival may have taken
   the key under a different spelling rather than advanced a version.
   The lock alone does not cover this: before the first row exists there
   is nothing to lock. `TestACreationThatLosesTheRaceForItsKeyIsRefused`
   drives the interleaving with an open rival transaction rather than a
   second goroutine, so it is deterministic; proved red by loosening the
   guard to `>=`.
6. **The upsert and the re-validation sweep are one transaction.** The
   plan committed the type row and then swept. Half of that landing
   leaves a type declaring a schema its own entities were never judged
   against, with nothing to notice. `Service.withTx` wraps both, and
   `revalidateEntitiesOfType` takes the transaction's `*dbq.Queries`.
7. **The sweep does not rewrite rows whose verdict has not changed.**
   `MarkEntitiesOfTypeInvalid` carries `AND invalid <> @invalid`. Without
   it every schema edit updates every entity of the type — including the
   ones flipping from `false` to `false` — and the `set_updated_at`
   trigger Task 1's correction 5 added moves their `updated_at`, so a
   validation pass reads as an edit of content nobody edited.
   `TestSchemaChangeFlagsRowsInvalidWithoutTouchingThem` asserts
   `updated_at` and the stored `fields` are both untouched; proved red by
   dropping the guard.
8. **The sweep reads a narrow query.** `ListEntityFieldsOfType` selects
   `id, fields` only: it runs over every entity of a type on every schema
   edit and needs nothing else. `ListEntitiesOfType`, which the plan named
   here, is not in the file — **Task 4 adds it**, for its own listing.
   `decodeFields`, which Task 4's block defines in `entities.go`, already
   exists in `service.go`; Task 4 must not redefine it.
9. **No write in `metamodel.sql` sets `updated_at`.** The trigger from
   Task 1's correction 5 owns the column. Carrying the clause as well is
   the two-mechanisms-for-one-column shape that correction went in to
   remove — harmless while every query remembers it, silently wrong the
   first time one does not. Tasks 4 and 5 should drop it from their blocks
   for the same reason.
10. **`ListEntityTypes` orders by `label, id`.** Labels are not unique —
    two types can both be called "Zone" — and a label-only order
    reshuffles ties between calls, the same reason `ListProjectsForUser`
    and `ListMembers` already carry an id tiebreak.
11. **`RemoveEntityType` counts inside the transaction and maps 23503 to
    `ErrInUse`.** The plan counted before opening one, so an entity
    written in between produced a raw foreign-key violation instead of the
    refusal the count exists to give. The count is defence in depth, not
    the enforcing mechanism: `entities.entity_type_id` is `ON DELETE
    RESTRICT`, and removing the count alone leaves
    `TestRemoveEntityTypeRefusesWhenInUse` green — what it earns is a
    typed `ErrInUse` without a transaction aborting on a constraint
    first. Removing both the count and the 23503 mapping is what turns the
    test red.
12. **Event payloads are typed and carry only identity.**
    `Service.publish` takes `any`, matching `realtime.Event.Payload`,
    rather than the plan's hand-built JSON string, and `type.upserted` /
    `type.removed` carry `{id, key}` and nothing else. The reasoning is
    `internal/web/publish.go`'s: publication order is not commit order, so
    a payload holding a value (a label, a schema) could stably tell a
    client the wrong one with nothing to signal it. The client re-reads.
13. **Every negative assertion names its own failure.** Each test above
    was proved red by breaking exactly the code it covers: removing
    `checkRowKey`, removing the spelling refusal, removing
    `Schema.Check`, dropping `ErrNotFound` on a zero-row delete, loosening
    the SQL version guard, dropping the `invalid <>` guard, dropping the
    project filter from `GetEntityTypeByID`, ordering the listing by key,
    dropping the actor, and making the sweep demand every declared field
    be present. `TestSchemaChangeDoesNotBackFillDeclaredDefaults` is
    partly a regression guard: no code writes values back today, so only
    its "a row missing a defaulted field still fits" half can be mutated
    red.
14. **Schema-evolution classification is still open, and Task 4 still owns
    it.** Nothing here tells a caller whether an edit widens or narrows a
    schema, so every edit sweeps. That is correct but unconditional: a
    type with hundreds of entities pays a full re-validation for a
    changed label. The mitigation Task 2 asked for — always sweep, with
    `CheckValues` — is what shipped.

**Corrections from the review of Task 3** (a second pass over the landed
package, made before Task 4 begins. Tasks 4-6 copy this task's file shape,
so each correction below fixes the shape and not only the symptom; where a
correction adds a helper, the helper is written once here for all three.)

15. **The respelling refusal was bypassable, and the bypass was a silent
    overwrite.** The refusal lived only in the locked pre-read, which runs
    before the guarded write and only sees a row that is already visible.
    On the creation path there is nothing to lock, so a writer racing a
    creator — carrying an `ExpectedVersion` that happens to match the
    version the winner lands on — passed both the read (no row) and the
    `ON CONFLICT ... DO UPDATE WHERE version = @expected_version` guard,
    and updated a row it never saw. Proved live: a rival inserts `Hogger`,
    the service upserts `hogger` with `ExpectedVersion: 1`, `err` is
    `nil` and the stored row is `key="Hogger" label="MINE" version=2` —
    exactly the outcome correction 4 rules out.

    The fix checks *after* the write as well as before it. The upsert
    returns the row it touched, and the `key` column is deliberately not
    in the `SET` list, so the returned key is still the stored spelling:
    comparing it to the submitted key closes the pre-read path and the
    race with one check, and `withTx` rolls the write back.
    `TestARaceThatWouldLandUnderAnotherSpellingIsRefused` drives it with
    an open rival transaction, so the interleaving is the test's;
    proved red by deleting the check (`err = <nil>`). **Tasks 4-6 must
    carry the same post-write check** on every key-addressed upsert they
    add — the pre-read alone is not a refusal.

    The doc comment on `EntityTypeInput.ExpectedVersion` said the field
    "is ignored" on creation. It is not — it is passed as the guard, and
    that is precisely how this hole opened. It now says so.
16. **The comment arguing the key policy claimed the opposite of the
    code.** `keys.go` said permitting case buys "a re-seed that changes
    the casing updates the existing row instead of failing". It fails —
    `keyRespellingError`, fifteen lines below, refuses it — and this was
    the load-bearing benefit the whole policy rested on. What permitting
    case actually buys is that the spelling a designer chose is the
    spelling stored; what the folding index buys is that a re-seed under
    a different casing *addresses* the existing row rather than creating a
    twin beside it. Fixed in `keys.go` and in this plan's copy of the
    same block.
17. **The policy was settled in code and still open in two specs.**
    `2026-08-31-core-and-metamodel-design.md` said row keys were "checked
    for non-emptiness alone" and pointed at an open question;
    `2026-09-02-agent-skill-bundle-design.md` §10.5 said `Quest`,
    `main quest 1` and `misión-01` were all accepted (two of the three are
    now refused), §4.4 taught "singular, lower snake" as though the server
    had no opinion, and open question 9 was left dangling. All four are
    updated to the rule that shipped —
    `^[A-Za-z0-9][A-Za-z0-9_-]*$` capped at 64 for row keys against
    `^[a-z][a-z0-9_]*$` capped at 64 for field keys — with the reason the
    two differ, and question 9 is closed. The skill-bundle spec is
    agent-facing, so its examples are now ones that work, and it carries
    the respelling message and its recovery.
18. **The race branch that makes the refusal reachable had no test.**
    Deleting the respelling branch inside `conflictOnEntityTypeKey` left
    the whole suite green:
    `TestACreationThatLosesTheRaceForItsKeyIsRefused` races two writers
    spelling the key the same way, so it can only ever observe the
    version-conflict branch.
    `TestACreationThatLosesItsKeyToAnotherSpellingIsNamedAsARespelling`
    covers the other one; proved red by deleting the branch
    (`version_conflict: current version is 1`).
19. **`Service.publish` could not express gating, and no test ever
    observed an event.** Every test built the service with a nil hub, so
    the helper's signature — `(projectID, kind, payload)` — silently made
    every event as open as `realtime.Event`'s zero value, whether or not
    that was right, and moving the call inside the transaction left the
    suite green. The helper now takes `minRole` and `humanOnly`
    explicitly, exactly as `internal/web/publish.go`'s does, so a call
    site shows the gating it chose.

    **The gating decision for entity-type events, recorded rather than
    inherited: `MinRole` empty, `HumanOnly` false.** It lives in the new
    `internal/metamodel/events.go`, beside the kinds it applies to, which
    is where **Tasks 4, 5 and 6 state their own** rather than copying a
    neighbouring call site. `HumanOnly: false` is the considered opposite
    of every member, token and invite event in `internal/web`, and the
    reason those set it true does not apply: each of them mirrors a REST
    listing `requireHumanCaller` refuses a token caller outright, whereas
    Task 7 mounts `types.list` and `types.upsert` on MCP *for agents*. An
    agent that may read every type on demand loses nothing by being told
    one changed, and it is the subscriber with the most to lose from not
    being told — a seeding agent's next `entities.upsert` is judged
    against the schema that just moved. `MinRole` empty because reading
    types is not role-gated anywhere: a viewer's browser renders the type
    list, and gating the invalidation above viewer would leave exactly the
    reader who cannot re-fetch on demand watching a list drift.

    Four tests now observe events with a hub attached:
    `TestTypeEventsReachEveryMemberOfTheGameIncludingAgents` (a viewer and
    a token subscriber both receive both kinds — proved red by setting
    `HumanOnly: true`, and again by `MinRole: owner`),
    `TestNoEventIsPublishedForARefusedWrite`,
    `TestNoEventIsPublishedWhenTheWriteIsRolledBack` (the respelling
    refusal fires after the row is written, so the database has seen the
    change and nothing may be announced), and
    `TestNothingIsAnnouncedWhileTheTransactionIsStillOpen`, which holds
    the transaction open on purpose — a rival takes a row lock on one of
    the type's entities and the re-validation sweep's `UPDATE` blocks on
    it — and proves nothing is on the wire in that window. Proved red by
    moving `s.publish` next to the write inside the transaction. This
    correction originally closed by saying that one placement could be
    pinned by no test in the package — a publish as the *very last*
    statement inside `withTx`'s callback, which differs from the correct
    placement only by the commit that immediately follows it — and left
    the invariant stated on `Service.publish` instead. **That was wrong.
    Correction 29 pins it.**
20. **Three project filters were presented as the isolation mechanism and
    are not.** `metamodel.sql`'s header said "every statement in this file
    filters on the resolved project id" as though that were what isolates
    games. It is, for the statements addressing `entity_types` itself. It
    is not for `ListEntityFieldsOfType`, `MarkEntitiesOfTypeInvalid` and
    `DeleteEntitiesOfType`: `0004_metamodel.sql` gives entities a
    composite `FOREIGN KEY (entity_type_id, project_id)`, so an entity's
    project is already determined by its type's, dropping the filter from
    those three changes no result, and no test can catch it. They stay as
    defence in depth against a future schema that relaxes the composite
    key, and so the next entities query copies the safe shape — but the
    comment now says which statements are load-bearing and which are not,
    because a comment claiming otherwise gets relied on. **Tasks 4-6
    inherit the filter and the honesty about it.**

    Same treatment for the sweep's `CheckValues`: `CheckValues` *is*
    `Validate` with the map discarded, so the two agree on every input and
    the choice cannot be caught by a test. The comment no longer implies
    it can; what the narrower call earns is that no normalised map is in
    scope to write back.
21. **`FOR UPDATE` is now pinned.** Removing it left every test green,
    because the compare-and-set in the `DO UPDATE` refuses every lost
    update on its own. What the lock earns is the *number* the caller is
    told to merge onto: without it the read runs against the
    transaction's snapshot, so a caller racing an in-flight edit is told
    "current version is 1", re-issues with 1 and is refused again — a loop
    it cannot escape by doing what the error said.
    `TestTheReportedCurrentVersionIsTheOneTheWriteWouldHaveMet` holds a
    rival's uncommitted version bump and asserts the refusal reports 2;
    proved red by dropping `FOR UPDATE` (reports 1, and does not block).
22. **`type.removed` carried `"key": ""`.** `entityTypeEvent` declares a
    key field, so a client could not tell "not carried" from "a type keyed
    empty string is gone". `RemoveEntityType` now reads the row for its
    key before deleting it, inside the same transaction — which also
    replaces the `rows == 0` path as the primary source of `ErrNotFound`,
    leaving that check as the race guard it always was.
23. **The descriptive columns were unvalidated and unbounded.** An empty
    label, a 5000-character colour and an `<script>` icon were all
    accepted, and `ListEntityTypes` orders by a column that could be
    empty. The new `internal/metamodel/descriptors.go` holds the decision
    **for Tasks 4-6 as well**, since entity `Name` inherits exactly this
    silence:

    - `label` required, at most 200 characters. An unlabelled row sorts
      to the front of every listing and names itself nothing.
    - `label_plural` at most 200, `description` at most 4000, both
      optional. Counted in *runes*, not bytes: a byte cap makes an
      accented label shorter than an unaccented one for no reason a
      designer could guess.
    - `color` must be a CSS hex colour when set. Named CSS colours and
      `rgb()` were rejected because Maestro's own renderers derive
      contrasting tones arithmetically from the channels, and a form
      they cannot decompose is a colour some views honour and others drop.
      (**Correction 34** widened this to all four hex forms; as first
      shipped it took only `#rgb` and `#rrggbb`.)
    - `icon` must be a lower-case icon *name*, at most 64 characters.
      Emoji are deliberately excluded for now — they would make the column
      two things at once — and the pending visual-identity spec owns that.
      (**Correction 34** added `_` to the pattern.)

    Maestro validates the *shape* of a descriptor and never its content:
    it is the game's own prose. This is not the escaping strategy either;
    Task 8's templates escape what they render regardless, because a label
    legitimately contains any character a game's language uses. Problems
    are reported together with the key's, in one `ValidationError`, so an
    agent fixing a seed script sees everything in one answer
    (`TestUpsertEntityTypeReportsEveryProblemAtOnce`); `checkRowKey` was
    replaced by `rowKeyProblems`, which hands back the slice, for that.
24. **A cross-game token passed as `Actor` surfaced raw.** The database
    backstop worked — `0004_metamodel.sql`'s composite
    `FOREIGN KEY (updated_by_token_id, project_id)` — but the refusal read
    `upsert entity type: ... violates foreign key constraint ...
    (SQLSTATE 23503)`, which tells an operator nothing about a token
    scoped to the wrong game. `actorConstraintViolation` (in `service.go`,
    matching on the constraint's *column* so Tasks 4-6 reuse it unchanged)
    maps it to the new `ErrActorNotInGame`.

    That sentinel is deliberately outside `errors.go`'s wire-code block.
    An `Actor` is never caller-supplied content — `internal/web` resolves
    it from the credential the request authenticated with — so reaching
    this means the scope resolution above the package is wrong, not that a
    designer typed something. Unmapped in `mcp_errors.go` it becomes
    `internal_error` to an agent and a legible line to an operator, which
    is the right pairing for a fault an agent cannot fix. **Task 7 decides
    whether it earns a wire code**; giving it one now would only invite a
    retry.
25. **Key and descriptor problems no longer read as `schema_violation`.**
    The `ValidationError`-not-`SchemaError` choice stands — a malformed
    key is the caller's own input at a path, fixable in place, exactly
    like a bad value — but the *code* was wrong. `schema_violation` is
    what the skill bundle teaches an agent to recover from by fixing
    entity values, which cannot help anyone whose `types.upsert` carried a
    key with a space in it. `ValidationError` grew a `Code` field
    defaulting to `schema_violation` (so the value validator is
    unchanged), and row-argument problems carry `invalid_input`;
    `ValidationError.Is` matches the sentinel its own code names and not
    the other. Three codes, three recoveries: fix the declaration
    (`invalid_schema`), fix the values (`schema_violation`), fix the
    argument (`invalid_input`). **Task 7 reads `Code`** rather than
    re-deriving it from path spelling.
26. **Route-shaped keys are a Task 8 decision, recorded not fixed.**
    `new`, `index`, `id`, `null`, `select`, `games` and `types` are all
    valid keys and nothing breaks today, because no route addresses a type
    by key yet. `keys.go` reasoned about path *escaping* and not path
    *collision*; it now says so. Task 8 introduces
    `/games/<slug>/types/<key>` and owns the choice — a reserved-word
    list, a route shape that cannot collide, or resolution in the router —
    because only Task 8 knows which segments exist. Reserving words here
    would guess wrong, and tightening a key rule after a game is seeded
    costs renames.


**Second re-review, 2026-09-02.** The high finding from the previous
round — a respelled key landing as a silent overwrite — was re-attacked
four ways and held. Corrections 27-35 close what the re-review found
instead. Read them as one theme: *a fix that does not chase its own
consequence is half a fix.* Three of the nine are a previous correction's
own text going stale within two commits of being written.

27. **Correction 25 changed a wire code and left the agent-facing spec
    saying the old one.** `2026-09-02-agent-skill-bundle-design.md` §10.5
    closed with "the error arrives as `schema_violation` at path `key`,
    not `invalid_schema`" — written by correction 17, invalidated by
    correction 25 two commits later, and proved false live: it arrives as
    `invalid_input`. Correction 17's own summary claimed "its examples are
    now ones that work". §10.5 now names `invalid_input` and says why it
    is neither of the other two. Both specs were swept for anything else
    correction 25 invalidated; nothing else asserted a code for a key.

    This is the defect the project keeps producing, and the reason the
    spec is the place it hurts most: the bundle *is* what an agent is
    taught, so a stale code here is not a stale comment, it is a wrong
    recovery in every game seeded from it.
28. **`invalid_input` was documented nowhere.** `errors.go` says the
    sentinels "match the wire codes the MCP surface returns", and the
    core spec's canonical **Error shapes** list enumerated seven codes
    without it, while the bundle's error-recovery table taught two. Every
    malformed key, label, plural, description, colour or icon on
    `types.upsert` returned a code no spec named. Both now carry it as
    the third code, with its recovery — fix the argument, re-issue the
    same call — and with the rule that a `Code` that is set but
    unrecognised matches no sentinel and therefore surfaces as
    `internal_error` (correction 32).

    The descriptor rules from correction 23 were undocumented too:
    neither spec said `label` was required, what the caps were, that
    `color` must be hex or that `icon` must be a bare name. The core
    spec's "Metamodel" section and the bundle's new §10.6 now state them
    once each, flagged as shared by every table in the domain, because
    Task 4's entity `name` and Task 5's relation-type `label` inherit
    them.
29. **The "untestable" invariant was testable, and correction 19 said
    otherwise in three places.** `events_test.go`, the plan, and
    `Service.publish` all claimed no test could make a commit fail on
    demand, so a publish sitting as the *last* statement inside `withTx`'s
    callback could only be pinned by a comment. False: `testutil.NewPool`
    gives every test its own throwaway database, so a test can install a
    constraint nothing else sees —

    ```sql
    ALTER TABLE entity_types ADD CONSTRAINT zz_fail_at_commit
      FOREIGN KEY (id) REFERENCES projects (id) DEFERRABLE INITIALLY DEFERRED;
    ```

    An entity type's id is not a project id, so this is satisfied by
    nothing; being `DEFERRABLE INITIALLY DEFERRED` it is checked at
    `COMMIT` and not before, so every statement inside the transaction
    succeeds and only the commit fails, with SQLSTATE 23503. It is added
    while the table is empty, because `ADD CONSTRAINT` validates the rows
    already stored.

    `TestNoEventIsPublishedWhenTheCommitFails` asserts the commit failed,
    that nothing was published, and that nothing was stored. Proved red by
    moving `s.publish` to the last statement inside the callback: the
    other four event tests stayed green and this one failed with "the
    transaction never committed, but `type.upserted` was published". The
    false claim is corrected in the comment, on `Service.publish` and in
    correction 19. **Tasks 4-6 copy this invariant**, and now they can
    copy a test for it too.
30. **Tasks 4, 5 and 6's plan blocks re-introduced everything Task 3
    fixed.** They were written before the review and never refreshed, so
    their implementer would have copied, in Task 4 alone: a `failureFor`
    matching `ErrSchemaViolation` and falling to `default:
    internal_error`, so a `ValidationError{Code: invalid_input}` matched
    *neither* arm and a malformed key in a bulk upsert reported
    `internal_error` to an agent; `s.publish(projectID,
    "entity.upserted", …)`, the three-argument signature correction 19
    removed, stating no gating at all; payloads hand-built as JSON
    *strings* with `in.TypeKey` and `row.Key` interpolated unescaped — an
    SSE frame injection the Core already closed once, carrying values the
    rule now stated in `events.go` forbids in a payload at all; an
    `UpsertEntity` with no `WHERE entities.version = expected_version`
    guard on the `DO UPDATE`, which is the lost update correction 4
    closed, plus the `updated_at = now()` correction 5 removed in favour
    of the trigger; no post-write `row.Key != in.Key` check, no
    `rowKeyProblems`, no descriptor check, and a missing-key
    `ValidationError` with no `Code`. Task 5 carried the same list for
    relation types, plus a `RemoveRelationType` whose count and delete
    were not in one transaction. Task 6 writes nothing, so it carried
    only `newProject(t, svc)` and a malformed cursor reported as a bare
    untyped error.

    All three are rewritten against the files that shipped, each defect
    annotated with the correction it comes from. Two shapes are new and
    stated rather than implied: `checkName`, because an entity's one
    descriptive column is `name` and an agent must be told about the path
    it sent; and the fact that `relations` has **no version column**, so
    `RelationInput` has no `ExpectedVersion` and the last writer of an
    edge wins by design — a decision, recorded, not an omission.

    A plan whose code blocks are stale is worse than a plan with no code
    blocks, because it is copied rather than read.
31. **The pre-read spelling check was dead to the suite.** Deleting
    `types.go`'s three-line `existing.Key != in.Key` branch left the whole
    package green, the re-review's new tests included, because correction
    15's post-write check catches every respelling the pre-read does —
    whenever the version also matches. Its remaining job is the *order*:
    a caller failing for two reasons at once (a respelled key and a stale
    version) must hear the one it can act on, because "current version is
    1" sends it to retry with a version that will be refused again for
    the same reason. `TestARespellingIsNamedEvenWhenTheVersionIsAlsoStale`
    pins that; proved red by deleting the branch (`version_conflict:
    current version is 1`). The branch now carries the reason.
32. **`code()` and `Is` disagreed, and the disagreement defaulted to the
    most-taught recovery.** An unrecognised `Code` fell through `Is`'s
    `default` arm to `ErrSchemaViolation`, so
    `&ValidationError{Code: "invalid_inptu"}` printed the typo and still
    satisfied `errors.Is(err, ErrSchemaViolation)` — a typo silently
    landing on the one code the skill bundle teaches an agent to spend
    round trips on. `Is` now switches on both known codes and matches
    nothing otherwise, so an unrecognised code is unmapped in
    `mcp_errors.go` and surfaces as `internal_error`: the honest report
    for a code no spec documents. The zero value still means
    `schema_violation`, which is the default the value validator relies
    on and not a fallback for anything else; both halves are pinned by
    `TestAnUnrecognisedCodeMatchesNoSentinel`.
33. **`events.go` claimed a precedent did not exist.** It said
    `HumanOnly: false` was "the one place in the codebase where that is
    the considered answer rather than the default". False:
    `internal/web/api_projects.go`'s `handleDeleteGame` publishes
    `eventGameDeleted` with exactly this gating and `publish.go` argues it
    at length — no REST listing to mirror, and no reason to withhold from
    a connection the one signal it will ever get. Both conditions hold
    here. Citing it strengthens the argument rather than weakening it: the
    project already refuses the member-event inertia where the reasoning
    does not apply, so this is the second instance of a rule, not a
    one-off exception.
34. **Two descriptor rules were narrower than their own arguments.**
    - `iconPattern` was `^[a-z0-9][a-z0-9-]*$`, which forbids `_` and so
      rejects `local_fire_department`. Material Symbols names every icon
      in snake_case, and no icon set has been chosen — the visual-identity
      spec is pending — so a kebab-only rule silently pre-committed that
      spec to a set spelling its names with hyphens. It was also the only
      one of the project's three key-shaped rules (`keyPattern`,
      `rowKeyPattern`, this) that forbade `_`, which was the tell.
      Underscores are now allowed.
    - `colorPattern` was `#RGB` or `#RRGGBB`, over a comment calling those
      "the two CSS hex forms". CSS Color 4 defines four; `#RGBA` and
      `#RRGGBBAA` were rejected. The argument for excluding named colours
      and `rgb()` is sound and stands — the renderers decompose channels
      arithmetically — but it does not exclude the alpha forms, which are
      the same channels plus one and decompose just as easily, and a
      translucent overlay colour is an ordinary thing for a game to want.
      All four are accepted and the comment is true.

    Both messages changed with the rules, so an agent is told what is
    actually allowed.
35. **What an `ExpectedVersion` claims on the insert path is now
    stated.** Update racing delete resurrects a type under a new id with
    `err = nil`: row `Quest` v1 exists, a rival deletes it, the service
    upserts `quest` with `ExpectedVersion: 1`, and a brand-new row
    appears. Nothing is overwritten, so this is not the high finding — but
    `EntityTypeInput`'s doc said a version "is a claim about a row,
    checked as one", and on the insert path it is a documented no-op.

    **The call: narrow the comment, do not refuse the write.** Refusing a
    non-nil `ExpectedVersion` that reaches the insert path was the
    alternative and it loses on three counts. Nothing is overwritten, so
    the lost update correction 4 closed is not in play. The contract's
    identity is `(project, key)` and not the uuid — `EntityTypeInput`
    carries no id field at all, so a caller cannot address a row this
    could surprise it about, and the returned `Version` of 1 *is* the
    caller's own signal that it created rather than updated. And refusing
    would make correction 15's post-write check unreachable: with the
    insert path closed to a real expected version, every remaining route
    into the guarded `DO UPDATE` comes from the locked pre-read, which has
    already compared spellings — retiring a defence this same review round
    spent three commits hardening, to hard-fail the caller who wants this
    most, a re-seed restoring a game's vocabulary after a botched delete.

    What refusing would have bought was honesty in a doc comment, and the
    doc comment was narrowed instead.
    `TestAVersionClaimAgainstAMissingTypeCreatesItRatherThanRefusing`
    records the argument and pins the outcome, so the behaviour is decided
    rather than incidental. **Tasks 4 and 5 inherit it**, and say so on
    their own inputs.

### Task 4: Entities, single and bulk

**Files:**
- Create: `internal/metamodel/entities.go`
- Modify: `internal/db/queries/metamodel.sql`, `internal/metamodel/events.go`,
  `internal/metamodel/descriptors.go`
- Test: `internal/metamodel/entities_test.go`

**Implemented, 2026-09-02.** The landed package is the authority, not
the blocks below: they were followed closely but not exactly, and
"Corrections made during implementation" at the end of this task lists
every divergence and why. Read that list before copying anything from
here.

**Refreshed after Task 3's review, 2026-09-02.** Every block below was
rewritten against the files Task 3 actually landed. The original blocks
re-introduced six defects Task 3 closed — an unguarded `DO UPDATE`, an
`updated_at = now()` the trigger owns, a three-argument `s.publish`,
payloads hand-built as JSON strings with caller values interpolated
unescaped, no post-write spelling check, and a `failureFor` that reported
a malformed key as `internal_error` — and the point of a plan is that its
implementer can copy it. Task 3's corrections 4, 5, 15, 19, 21, 23 and 25
are the reasons; each is cited where it bites.

- [x] **Step 1: Add the queries**

Append to `internal/db/queries/metamodel.sql`:

```sql
-- name: UpsertEntity :one
-- The same shape as UpsertEntityType, for the same reasons, and that
-- statement's comment carries the full argument. In short:
--
--   * The DO UPDATE is guarded by the caller's expected version, so the
--     whole compare-and-set is one statement and two writers cannot both
--     read version 1 and both succeed (correction 4). A creating caller
--     passes noVersion, which no stored version can equal.
--   * The key column is deliberately not in the SET list. The first
--     spelling stored stands, and the returned row therefore still
--     carries it — which is what lets Go refuse a respelling *after* the
--     write, closing the creation-race hole (correction 15).
--   * No write sets updated_at: 0004_metamodel.sql puts a
--     set_updated_at trigger on all four tables, and a clause here would
--     be a second mechanism behind one column (correction 5).
--   * The audit columns are carried, so the composite
--     FOREIGN KEY (updated_by_token_id, project_id) catches a token
--     scoped to another game (correction 24).
--
-- invalid is reset to false because the caller has just validated these
-- values against the type's current schema; a row that is being written
-- is a row that has been judged.
INSERT INTO entities (project_id, entity_type_id, key, name, fields, search,
                      updated_by_user_id, updated_by_token_id)
VALUES (sqlc.arg('project_id')::uuid, sqlc.arg('entity_type_id')::uuid,
        sqlc.arg('key')::text, sqlc.arg('name')::text, sqlc.arg('fields')::jsonb,
        to_tsvector('simple', sqlc.arg('name')::text || ' ' || sqlc.arg('search_text')::text),
        sqlc.narg('updated_by_user_id')::uuid, sqlc.narg('updated_by_token_id')::uuid)
ON CONFLICT (project_id, entity_type_id, lower(key)) DO UPDATE
SET name                = excluded.name,
    fields              = excluded.fields,
    search              = excluded.search,
    invalid             = false,
    version             = entities.version + 1,
    updated_by_user_id  = excluded.updated_by_user_id,
    updated_by_token_id = excluded.updated_by_token_id
WHERE entities.version = sqlc.arg('expected_version')::integer
RETURNING *;

-- name: GetEntityByKey :one
SELECT * FROM entities
WHERE project_id = sqlc.arg('project_id')::uuid
  AND entity_type_id = sqlc.arg('entity_type_id')::uuid
  AND lower(key) = lower(sqlc.arg('key')::text);

-- name: GetEntityByKeyForUpdate :one
-- FOR UPDATE, for the reason correction 21 records: without the lock the
-- read runs against the transaction's snapshot, so a caller racing an
-- in-flight edit is told to merge onto a version that is already stale
-- by the time it retries, and retries into the same refusal forever.
-- What the lock buys is not the refusal — the guarded DO UPDATE refuses
-- on its own — but the *number* the caller is told to merge onto.
SELECT * FROM entities
WHERE project_id = sqlc.arg('project_id')::uuid
  AND entity_type_id = sqlc.arg('entity_type_id')::uuid
  AND lower(key) = lower(sqlc.arg('key')::text)
FOR UPDATE;

-- name: GetEntityByID :one
SELECT * FROM entities
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;

-- name: ListEntitiesOfType :many
SELECT * FROM entities
WHERE project_id = sqlc.arg('project_id')::uuid
  AND entity_type_id = sqlc.arg('entity_type_id')::uuid
ORDER BY name, id;

-- name: ListEntitiesPage :many
SELECT * FROM entities
WHERE project_id = sqlc.arg('project_id')::uuid
  AND (sqlc.narg('entity_type_id')::uuid IS NULL OR entity_type_id = sqlc.narg('entity_type_id')::uuid)
  AND (sqlc.narg('invalid')::boolean IS NULL OR invalid = sqlc.narg('invalid')::boolean)
  AND (sqlc.narg('after')::text IS NULL OR (name, id::text) > (sqlc.narg('after_name')::text, sqlc.narg('after')::text))
ORDER BY name, id
LIMIT sqlc.arg('limit')::int;

-- name: DeleteEntity :execrows
DELETE FROM entities
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;
```

Run: `make sqlc`

- [x] **Step 2: State this task's event gating in `events.go`**

Correction 19: the gating of an event is a decision about who may learn a
fact, and it is stated beside the kind, not passed as two bare literals at
a call site. Add to `internal/metamodel/events.go`:

```go
	// eventEntityUpserted and eventEntityRemoved fire from UpsertEntity,
	// UpsertEntities and RemoveEntity once their transaction has
	// committed.
	//
	// The gating is the same as the type events above and for the same
	// reasons, restated rather than inherited: MinRole empty, because a
	// viewer's browser renders the entity list and gating the change
	// above viewer leaves exactly the reader who cannot re-fetch on
	// demand watching a stale list; HumanOnly false, because Task 7
	// mounts entities.list and entities.upsert on MCP for agents, and a
	// seeding agent is the subscriber with the most to lose from not
	// being told its content moved.
	eventEntityUpserted = "entity.upserted"
	eventEntityRemoved  = "entity.removed"
```

with

```go
const (
	entityEventMinRole   = roles.Role("")
	entityEventHumanOnly = false
)
```

beside the type constants.

- [x] **Step 3: Give `descriptors.go` the entity's one descriptive column**

An entity carries `name` where a type carries `label`, and correction 23
exists so that Task 4 does not re-ship the same silence under a different
column name. Add:

```go
// checkName is the entity flavour of checkDescriptors. An entity carries
// one descriptive column and it obeys the label rule — required, capped
// at maxLabelLen, counted in runes — at its own wire path, so an agent
// sees "name", not "label", for the argument it actually sent.
//
// It is a second function rather than a call to checkDescriptors with
// empty descriptors because the *path* has to differ: checkDescriptors
// reports at "label", and an agent that sent `name` must be told about
// `name`. Rows whose descriptive columns really are label-shaped —
// relation types, Task 5 — call checkDescriptors directly instead.
func checkName(name string) []FieldError {
	var problems []FieldError
	if name == "" {
		problems = append(problems, FieldError{Path: "name", Message: "is required"})
	} else {
		tooLong("name", name, maxLabelLen, &problems)
	}
	return problems
}
```

- [x] **Step 4: Write the failing test**

`internal/metamodel/entities_test.go`. Note `newProject(t, pool)` — the
helper takes the pool, not the service (`types_test.go`), and the same
correction applies to every test block in Tasks 5 and 6.

```go
package metamodel_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/testutil"
)

// seedQuestType declares a Quest type with a required numeric field.
func seedQuestType(t *testing.T, svc *metamodel.Service, project uuid.UUID) {
	t.Helper()
	_, err := svc.UpsertEntityType(context.Background(), project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: metamodel.Schema{
			{Key: "min_level", Type: metamodel.FieldNumber, Required: true},
			{Key: "summary", Type: metamodel.FieldLongText},
		},
	})
	if err != nil {
		t.Fatalf("seed quest type: %v", err)
	}
}

func TestUpsertEntityValidatesAgainstItsType(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	row, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
		Fields: map[string]any{"min_level": float64(10), "summary": "Kill Hogger."},
	})
	if err != nil {
		t.Fatalf("UpsertEntity: %v", err)
	}
	if row.Version != 1 {
		t.Fatalf("Version = %d, want 1", row.Version)
	}

	_, err = svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "broken", Name: "Broken",
		Fields: map[string]any{"min_level": "ten"},
	})
	if !errors.Is(err, metamodel.ErrSchemaViolation) {
		t.Fatalf("err = %v, want ErrSchemaViolation", err)
	}
}

func TestUpsertEntityIsIdempotentAndBumpsVersion(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	in := metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
		Fields: map[string]any{"min_level": float64(10)},
	}
	first, err := svc.UpsertEntity(ctx, project, in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}

	in.ExpectedVersion = ptrInt32(1)
	in.Name = "Wanted: Hogger (revised)"
	second, err := svc.UpsertEntity(ctx, project, in)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.ID != first.ID {
		t.Fatal("a re-seed created a duplicate row")
	}
	if second.Version != 2 {
		t.Fatalf("Version = %d, want 2", second.Version)
	}
}

func TestUpsertEntityRejectsStaleVersion(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Hogger",
		Fields: map[string]any{"min_level": float64(10)},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Hogger again",
		Fields:          map[string]any{"min_level": float64(11)},
		ExpectedVersion: ptrInt32(99),
	})
	if !errors.Is(err, metamodel.ErrVersionConflict) {
		t.Fatalf("err = %v, want ErrVersionConflict", err)
	}
}

// TestUpsertEntityRefusesARespelledKey is Task 3's correction 15 for
// entities. The pre-read alone is not a refusal: on the creation path
// there is nothing to lock, so the check has to run again on the row the
// upsert returns. Drive the race with an open rival transaction, as
// TestARaceThatWouldLandUnderAnotherSpellingIsRefused does, so the
// interleaving is the test's rather than the scheduler's.
func TestUpsertEntityRefusesARespelledKey(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "Hogger", Name: "Hogger",
		Fields: map[string]any{"min_level": float64(10)},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "MINE",
		Fields:          map[string]any{"min_level": float64(1)},
		ExpectedVersion: ptrInt32(1),
	})
	requireFieldError(t, err, "key",
		`"hogger" already exists here spelled "Hogger", and keys are matched without regard to case: `+
			`use "Hogger" to update it, or pick a key that differs by more than capitalisation`)

	stored, err := svc.EntityByKey(ctx, project, "quest", "HOGGER")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	if stored.Key != "Hogger" || stored.Name != "Hogger" || stored.Version != 1 {
		t.Fatalf("the refused upsert changed the row: %+v", stored)
	}
}

// TestAMalformedEntityArgumentIsInvalidInput is correction 25 for
// entities, and the finding Task 4's original block would have shipped:
// a bad key reported as schema_violation sends an agent to inspect
// entity *values*, and a bad key reported through failureFor's default
// arm reports internal_error, which tells it nothing at all.
func TestAMalformedEntityArgumentIsInvalidInput(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	_, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "main quest", Name: "",
		Fields: map[string]any{"min_level": float64(1)},
	})
	if !errors.Is(err, metamodel.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	if errors.Is(err, metamodel.ErrSchemaViolation) {
		t.Fatalf("err = %v must not also read as a schema violation", err)
	}
	// Both problems in one pass, as correction 23 requires: an agent
	// fixing a seed script must not learn about the name only after the
	// key is fixed.
	var invalid *metamodel.ValidationError
	if !errors.As(err, &invalid) || len(invalid.Fields) != 2 {
		t.Fatalf("want the key and the name reported together, got %v", err)
	}

	// And the same fault inside a bulk batch carries the same code, not
	// internal_error.
	result, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
		{TypeKey: "quest", Key: "main quest", Name: "A",
			Fields: map[string]any{"min_level": float64(1)}},
	}, metamodel.BulkPartial)
	if err != nil {
		t.Fatalf("UpsertEntities: %v", err)
	}
	if len(result.Failed) != 1 || result.Failed[0].Code != "invalid_input" {
		t.Fatalf("failures = %+v, want one invalid_input", result.Failed)
	}
}

func TestBulkPartialLandsTheGoodRowsAndReportsTheRest(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	result, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
		{TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)}},
		{TypeKey: "quest", Key: "b", Name: "B", Fields: map[string]any{"min_level": "nope"}},
		{TypeKey: "quest", Key: "c", Name: "C", Fields: map[string]any{"min_level": float64(3)}},
	}, metamodel.BulkPartial)
	if err != nil {
		t.Fatalf("UpsertEntities: %v", err)
	}
	if len(result.Succeeded) != 2 {
		t.Fatalf("succeeded = %d, want 2", len(result.Succeeded))
	}
	if len(result.Failed) != 1 {
		t.Fatalf("failed = %d, want 1", len(result.Failed))
	}
	if result.Failed[0].Index != 1 {
		t.Fatalf("the failure must carry its index, got %d", result.Failed[0].Index)
	}
	if result.Failed[0].Code != "schema_violation" {
		t.Fatalf("code = %q, want schema_violation for a bad value", result.Failed[0].Code)
	}
	if _, err := svc.EntityByKey(ctx, project, "quest", "c"); err != nil {
		t.Fatalf("the row after the failure must still have landed: %v", err)
	}
}

func TestBulkAtomicRollsBackEverythingOnOneFailure(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	_, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
		{TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)}},
		{TypeKey: "quest", Key: "b", Name: "B", Fields: map[string]any{"min_level": "nope"}},
	}, metamodel.BulkAtomic)
	if err == nil {
		t.Fatal("an atomic batch with a bad row must fail as a whole")
	}
	if _, err := svc.EntityByKey(ctx, project, "quest", "a"); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatal("the good row of a failed atomic batch must have been rolled back")
	}
}

// TestNoEntityEventIsPublishedForARolledBackBatch is correction 19 for
// this task: publication happens after the transaction commits, and the
// atomic path is where that is easiest to get wrong, because the write
// and the commit are in different functions. A batch that rolls back
// must put nothing on the wire.
func TestNoEntityEventIsPublishedForARolledBackBatch(t *testing.T) {
	pool := testutil.NewPool(t)
	hub := realtime.NewHub()
	svc := metamodel.New(pool, hub)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	sub := hub.Subscribe(project, "viewer", true)
	defer hub.Unsubscribe(sub)

	if _, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
		{TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)}},
		{TypeKey: "quest", Key: "b", Name: "B", Fields: map[string]any{"min_level": "nope"}},
	}, metamodel.BulkAtomic); err == nil {
		t.Fatal("the batch must fail")
	}
	requireNothing(t, sub, "the atomic batch rolled back")

	// The subscriber is a viewer *and* a token caller: the gating stated
	// in events.go excludes neither, so a passing batch reaches it.
	if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)},
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got := receive(t, sub); got.Kind != "entity.upserted" {
		t.Fatalf("Kind = %q, want entity.upserted", got.Kind)
	}
}

func TestSchemaChangeFlagsRowsInvalidWithoutTouchingThem(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Hogger",
		Fields: map[string]any{"min_level": float64(10)},
	}); err != nil {
		t.Fatalf("seed entity: %v", err)
	}

	// Add a required field: the stored row no longer satisfies the schema.
	if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: metamodel.Schema{
			{Key: "min_level", Type: metamodel.FieldNumber, Required: true},
			{Key: "summary", Type: metamodel.FieldLongText},
			{Key: "faction", Type: metamodel.FieldText, Required: true},
		},
		ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("evolve schema: %v", err)
	}

	row, err := svc.EntityByKey(ctx, project, "quest", "hogger")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	if !row.Invalid {
		t.Fatal("the row should be flagged invalid after the schema change")
	}
	if row.Name != "Hogger" {
		t.Fatal("the row's data must not have been altered")
	}
}
```

Note that `TestSchemaChangeFlagsRowsInvalidWithoutTouchingThem` already
exists in `types_test.go`, written against `insertEntity`; Task 4's job is
to move it here and drive it through the service instead, not to declare
it twice.

- [x] **Step 5: Run the test to verify it fails**

Run: `go test ./internal/metamodel/ -run TestUpsertEntity -v`
Expected: FAIL, `undefined: metamodel.EntityInput`.

- [x] **Step 6: Write the implementation**

`internal/metamodel/entities.go`:

```go
package metamodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// BulkMode decides how a batch behaves when one item fails.
type BulkMode string

// The two bulk modes.
const (
	// BulkPartial lands the valid items and reports the rest. Default,
	// because a first seeding pass always has a few bad rows and losing the
	// other four hundred helps nobody.
	BulkPartial BulkMode = "partial"
	// BulkAtomic runs the whole batch in one transaction.
	BulkAtomic BulkMode = "atomic"
)

// EntityInput is an upsert request, addressed by type key plus entity key.
//
// ExpectedVersion carries exactly the meaning EntityTypeInput's does, and
// the same caveat: on the insert path it is passed as the guard on the
// DO UPDATE and is never evaluated, so a claim against a row that does
// not exist creates one rather than being refused. See
// EntityTypeInput.ExpectedVersion for the argument.
type EntityInput struct {
	TypeKey         string
	Key             string
	Name            string
	Fields          map[string]any
	ExpectedVersion *int32
	Actor           Actor
}

// entityEvent is the payload of the entity.* events: the identity of
// what changed and nothing else.
//
// A payload never carries a value a client could treat as current —
// Service.publish's doc comment argues why — so this holds ids and keys
// and not the name that just changed. It is also a struct and not a
// hand-built JSON string: interpolating in.TypeKey and row.Key into
// `{"type":"…"}` would put unescaped caller-controlled text on the SSE
// wire, which is a frame-injection the Core already closed once, and it
// would do it with values this rule says must not be there at all.
type entityEvent struct {
	ID      uuid.UUID `json:"id"`
	TypeKey string    `json:"type_key"`
	Key     string    `json:"key"`
}

// BulkFailure is one rejected item of a batch.
type BulkFailure struct {
	Index   int    `json:"index"`
	Key     string `json:"key"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// BulkResult reports what a batch did.
type BulkResult struct {
	Succeeded []dbq.Entity  `json:"-"`
	Failed    []BulkFailure `json:"failed"`
}

// UpsertEntity creates or updates one entity.
func (s *Service) UpsertEntity(ctx context.Context, projectID uuid.UUID, in EntityInput) (dbq.Entity, error) {
	var row dbq.Entity
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		var err error
		row, err = s.upsertEntityWith(ctx, q, projectID, in)
		return err
	})
	if err != nil {
		return dbq.Entity{}, err
	}
	s.publish(projectID, eventEntityUpserted, entityEventMinRole, entityEventHumanOnly,
		entityEvent{ID: row.ID, TypeKey: in.TypeKey, Key: row.Key})
	return row, nil
}

// UpsertEntities writes a batch in the requested mode.
//
// Both modes publish only after their transaction has committed, and
// publish one identity event per row that landed rather than one event
// carrying a count: a count is a value, not an identity, and a
// subscriber's only correct reaction to an entity event is to re-read
// the rows it names.
func (s *Service) UpsertEntities(ctx context.Context, projectID uuid.UUID, items []EntityInput, mode BulkMode) (BulkResult, error) {
	if mode == BulkAtomic {
		return s.upsertEntitiesAtomic(ctx, projectID, items)
	}

	var result BulkResult
	for i, in := range items {
		// Each item is its own transaction: a partial batch's whole point
		// is that item 200 landing does not depend on item 3.
		var row dbq.Entity
		err := s.withTx(ctx, func(q *dbq.Queries) error {
			var err error
			row, err = s.upsertEntityWith(ctx, q, projectID, in)
			return err
		})
		if err != nil {
			result.Failed = append(result.Failed, failureFor(i, in.Key, err))
			continue
		}
		result.Succeeded = append(result.Succeeded, row)
		s.publish(projectID, eventEntityUpserted, entityEventMinRole, entityEventHumanOnly,
			entityEvent{ID: row.ID, TypeKey: in.TypeKey, Key: row.Key})
	}
	return result, nil
}

func (s *Service) upsertEntitiesAtomic(ctx context.Context, projectID uuid.UUID, items []EntityInput) (BulkResult, error) {
	var result BulkResult
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		result = BulkResult{}
		for i, in := range items {
			row, err := s.upsertEntityWith(ctx, q, projectID, in)
			if err != nil {
				return fmt.Errorf("item %d (%s): %w", i, in.Key, err)
			}
			result.Succeeded = append(result.Succeeded, row)
		}
		return nil
	})
	if err != nil {
		return BulkResult{}, err
	}
	for i, row := range result.Succeeded {
		s.publish(projectID, eventEntityUpserted, entityEventMinRole, entityEventHumanOnly,
			entityEvent{ID: row.ID, TypeKey: items[i].TypeKey, Key: row.Key})
	}
	return result, nil
}

// upsertEntityWith does the work against any queries handle, so the same
// code serves the single, partial and atomic paths. Every caller runs it
// inside a transaction, and none of them publishes from in here.
func (s *Service) upsertEntityWith(ctx context.Context, q *dbq.Queries, projectID uuid.UUID, in EntityInput) (dbq.Entity, error) {
	problems := rowKeyProblems("key", in.Key)
	problems = append(problems, checkName(in.Name)...)
	if len(problems) > 0 {
		return dbq.Entity{}, &ValidationError{Code: codeInvalidInput, Fields: problems}
	}
	// type_key is deliberately not validated as a key here: it addresses
	// a row this call only reads, so a malformed one has one honest
	// answer — there is no such type — and it already gets it below.

	typ, err := q.GetEntityTypeByKey(ctx, dbq.GetEntityTypeByKeyParams{ProjectID: projectID, Key: in.TypeKey})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbq.Entity{}, fmt.Errorf("%w: no entity type %q in this game", ErrNotFound, in.TypeKey)
	}
	if err != nil {
		return dbq.Entity{}, fmt.Errorf("lookup entity type: %w", err)
	}

	schema, err := ParseSchema(typ.FieldSchema)
	if err != nil {
		return dbq.Entity{}, err
	}
	values, err := schema.Validate(in.Fields)
	if err != nil {
		return dbq.Entity{}, err
	}

	expected := noVersion
	if in.ExpectedVersion != nil {
		expected = *in.ExpectedVersion
	}

	// Read under the row lock, so the spelling and the version this
	// caller is told about are the ones its own write will meet.
	existing, err := q.GetEntityByKeyForUpdate(ctx, dbq.GetEntityByKeyForUpdateParams{
		ProjectID: projectID, EntityTypeID: typ.ID, Key: in.Key,
	})
	switch {
	case err == nil:
		// Spelling before version: a caller failing for both reasons
		// hears the one it can act on. See UpsertEntityType.
		if existing.Key != in.Key {
			return dbq.Entity{}, keyRespellingError("key", in.Key, existing.Key)
		}
		if in.ExpectedVersion == nil || *in.ExpectedVersion != existing.Version {
			return dbq.Entity{}, &VersionConflictError{Current: existing.Version}
		}
	case errors.Is(err, pgx.ErrNoRows):
		// Creation: no version to match, nothing to lock.
	default:
		return dbq.Entity{}, fmt.Errorf("lookup entity: %w", err)
	}

	encoded, err := json.Marshal(values)
	if err != nil {
		return dbq.Entity{}, fmt.Errorf("encode fields: %w", err)
	}

	row, err := q.UpsertEntity(ctx, dbq.UpsertEntityParams{
		ProjectID:        projectID,
		EntityTypeID:     typ.ID,
		Key:              in.Key,
		Name:             in.Name,
		Fields:           encoded,
		SearchText:       searchTextOf(values),
		ExpectedVersion:  expected,
		UpdatedByUserID:  in.Actor.UserID,
		UpdatedByTokenID: in.Actor.TokenID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The guarded DO UPDATE matched nothing: between the read above
		// and this statement another writer created or advanced the row.
		return dbq.Entity{}, conflictOnEntityKey(ctx, q, projectID, typ.ID, in.Key)
	}
	if err != nil {
		if mapped := actorConstraintViolation(err); errors.Is(mapped, ErrActorNotInGame) {
			return dbq.Entity{}, mapped
		}
		return dbq.Entity{}, fmt.Errorf("upsert entity: %w", err)
	}
	// Correction 15, for entities. The locked read cannot be the only
	// place the spelling is checked: on the creation path there is
	// nothing to lock, so a writer racing a creator with a matching
	// expected version passed both the read and the guard and updated a
	// row it never saw. The upsert returns the row it touched and key is
	// not in the SET list, so comparing the stored spelling to the
	// submitted one after the write closes both; the caller's
	// transaction rolls the write back.
	if row.Key != in.Key {
		return dbq.Entity{}, keyRespellingError("key", in.Key, row.Key)
	}
	return row, nil
}

// conflictOnEntityKey re-reads a key whose guarded upsert matched no row
// and names what actually stands in the way — a respelling, or a version
// this caller was holding that has since moved.
func conflictOnEntityKey(ctx context.Context, q *dbq.Queries, projectID, typeID uuid.UUID, key string) error {
	row, err := q.GetEntityByKey(ctx, dbq.GetEntityByKeyParams{
		ProjectID: projectID, EntityTypeID: typeID, Key: key,
	})
	if err != nil {
		return fmt.Errorf("re-read entity after a failed upsert: %w", err)
	}
	if row.Key != key {
		return keyRespellingError("key", key, row.Key)
	}
	return &VersionConflictError{Current: row.Version}
}

// EntityByKey loads one entity by type key and entity key.
func (s *Service) EntityByKey(ctx context.Context, projectID uuid.UUID, typeKey, key string) (dbq.Entity, error) {
	typ, err := s.EntityTypeByKey(ctx, projectID, typeKey)
	if err != nil {
		return dbq.Entity{}, err
	}
	row, err := s.q.GetEntityByKey(ctx, dbq.GetEntityByKeyParams{
		ProjectID: projectID, EntityTypeID: typ.ID, Key: key,
	})
	if err != nil {
		return dbq.Entity{}, notFound(err, "lookup entity")
	}
	return row, nil
}

// RemoveEntity deletes one entity. Its relations go with it, by cascade.
func (s *Service) RemoveEntity(ctx context.Context, projectID, id uuid.UUID) error {
	// Read the row before deleting it, for its identity: correction 22 —
	// an event declaring a key field and carrying "" tells a client a
	// row keyed empty string is gone.
	var removed dbq.Entity
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		var err error
		removed, err = q.GetEntityByID(ctx, dbq.GetEntityByIDParams{ProjectID: projectID, ID: id})
		if err != nil {
			return notFound(err, "lookup entity")
		}
		rows, err := q.DeleteEntity(ctx, dbq.DeleteEntityParams{ProjectID: projectID, ID: id})
		if err != nil {
			return fmt.Errorf("delete entity: %w", err)
		}
		if rows == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.publish(projectID, eventEntityRemoved, entityEventMinRole, entityEventHumanOnly,
		entityEvent{ID: id, Key: removed.Key})
	return nil
}

// searchTextOf flattens the text values of a row so search can index
// them. The keys are sorted so the same values always produce the same
// tsvector input: map iteration order is randomised, and a search column
// that differs between two identical writes is a diff nobody can explain.
func searchTextOf(values map[string]any) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		if s, ok := values[k].(string); ok {
			b.WriteString(s)
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// failureFor maps a domain error to the wire shape of a bulk failure.
//
// Every code this package can produce has an arm. The default arm is
// internal_error, which is the honest answer for something nobody
// planned for — and precisely the wrong answer for a malformed key, so
// ErrInvalidInput is matched explicitly. Correction 25 split that code
// out for exactly this reason: it is a caller's own argument, at a path,
// fixable in place, and reporting it as internal_error tells an agent to
// give up on a call it could have fixed.
func failureFor(index int, key string, err error) BulkFailure {
	f := BulkFailure{Index: index, Key: key, Message: err.Error()}
	switch {
	case errors.Is(err, ErrInvalidInput):
		f.Code = "invalid_input"
	case errors.Is(err, ErrSchemaViolation):
		f.Code = "schema_violation"
	case errors.Is(err, ErrInvalidSchema):
		f.Code = "invalid_schema"
	case errors.Is(err, ErrVersionConflict):
		f.Code = "version_conflict"
	case errors.Is(err, ErrNotFound):
		f.Code = "not_found"
	case errors.Is(err, ErrEndpointTypeMismatch):
		f.Code = "endpoint_type_mismatch"
	case errors.Is(err, ErrInUse):
		f.Code = "in_use"
	default:
		f.Code = "internal_error"
	}
	return f
}
```

`decodeFields` is **not** declared here: Task 3 landed it in
`service.go`, beside `withTx` and `actorConstraintViolation`, and a
second copy will not compile.

- [x] **Step 7: Run the tests to verify they pass**

Run: `go test ./internal/metamodel/ -v`
Expected: PASS, including `TestRemoveEntityTypeRefusesWhenInUse` from Task 3.

- [x] **Step 8: Commit**

```bash
git add internal/db/queries internal/db/dbq internal/metamodel
git commit -m "feat: entities with bulk partial and atomic writes"
```

**Corrections made during implementation** (a pass over the landed
package, written as Task 4 shipped so Tasks 5-9 build on what exists
rather than on what was planned):

1. **The two listing queries were not added.** `ListEntitiesPage` and
   `ListEntitiesOfType` are printed in Step 1, and no line of Task 4
   calls either. A query landing a task ahead of its only caller ships
   generated code no test exercises, and `ListEntitiesPage` in
   particular would have pre-committed Task 6's cursor shape — two
   separate nargs and an `id::text` comparison — before the pagination
   tests that have to justify it exist. `ListEntitiesPage` moved to
   Task 6's Step 1, which is where its caller is. `ListEntitiesOfType`
   is called by no task in this plan at all and was dropped; Task 3's
   correction 8 says "Task 4 adds it, for its own listing", and Task 4
   has no listing.
2. **`upsertEntityWith` returns the type key beside the row, and the
   events carry the stored spellings.** The block published
   `entityEvent{TypeKey: in.TypeKey}` — the *caller's* spelling. Keys are
   matched without regard to case, so a game whose type is stored as
   `Quest` may be addressed as `quest`, and an event repeating that names
   an identity no other reader of the game sees; a subscriber's only use
   for the payload is to go and re-read the row it names. The two travel
   together in an unexported `upsertedEntity` rather than as parallel
   slices, which is also what removed the atomic path's index pairing
   (correction 4). `TestEntityEventsCarryTheStoredIdentity` pins it,
   proved red by publishing `in.TypeKey`.

   **Superseded by correction 14 — read that before relying on this
   one.** "The events carry the stored spellings" was true of the code
   and pinned only on the single path: both bulk paths could publish the
   caller's own spelling of the type key and leave the whole suite
   green. Correction 14 is where all three paths are actually pinned.
3. **`RemoveEntity` reads the type as well as the row.** The block
   published `entityEvent{ID: id, Key: removed.Key}`, leaving `type_key`
   empty on every removal — Task 3's correction 22 exactly, under a
   different column: `entityEvent` declares the field, and a client
   reading one cannot tell "not carried" from "empty". The removal now
   reads the entity's type inside the same transaction and fills it.
   Same test, second half, proved red by dropping the field.
4. **The atomic path builds its events inside the transaction and
   publishes them after the commit.** The block published
   `entityEvent{..., TypeKey: items[i].TypeKey}` by walking
   `result.Succeeded` and indexing back into `items`. That alignment
   holds only because every item succeeded, and it is exactly the kind of
   pairing a later edit breaks silently. It also reset `result` inside
   `withTx`'s callback, which reads as a retry loop the helper does not
   have.
5. **An unrecognised bulk mode is refused, at path `mode`.** The block's
   `if mode == BulkAtomic { ... }` read every other string — `"atomic "`,
   `"all-or-nothing"`, a typo — as partial. Task 7 builds this value
   straight from an agent-supplied string
   (`metamodel.BulkMode(in.Mode)`), so that is a silent downgrade of an
   all-or-nothing request into one that lands rows the caller asked to
   have rolled back: a failure the caller has no way of seeing. The empty
   mode stays partial, because an omitted argument is not a typo and the
   spec names partial as the default.
   `TestBulkRejectsAnUnknownMode` pins both halves.
6. **A cancelled context stops a partial batch.** Every item is its own
   transaction, so nothing else ends the loop: a cancelled caller turned
   a 500-row batch into 500 failed round trips whose report nobody was
   left to read, and the call still returned a nil error, which reads as
   "the batch ran". `UpsertEntities` now returns whatever had already
   landed *and* the error — rows landing before the cancellation is
   partial mode's contract, not a fact to hide.
   `TestBulkPartialStopsWhenTheCallerIsGone` pins it.
7. **`TestSchemaChangeFlagsRowsInvalidWithoutTouchingThem` was not
   moved.** Step 4 says to move it out of `types_test.go` and drive it
   through the service; declaring it in both files does not compile, and
   the existing one is the stronger test — it asserts the sweep leaves
   `fields` and `updated_at` untouched, which the replacement drops, and
   it builds its invalid row with `insertEntity`, which the service path
   cannot do because `UpsertEntity` refuses undeclared fields.
   `entities_test.go` adds
   `TestSchemaChangeFlagsAnEntityWrittenThroughTheService` instead: a row
   written through `UpsertEntity`, judged by the same sweep after a new
   required field appears, and un-flagged by writing it again.
8. **`failureFor`'s comment overclaimed.** "Every code this package can
   produce has an arm" — `ErrActorNotInGame` is produced by this package
   and deliberately has none, because it is not a wire code (`errors.go`
   argues why) and `internal_error` is the correct report for a fault an
   agent cannot fix. The comment now says "every wire code" and names the
   exception.
9. **`Service.EntityByID` was written and then removed.** It had no
   caller in this task, and shipping an exported accessor ahead of one is
   the same fault as correction 1 with a Go signature instead of a SQL
   statement. `q.GetEntityByID` stays, because `RemoveEntity` uses it.
   Task 8 adds the accessor if its routes need it.
10. **Tests the block did not carry**, all of them proved by breaking the
    code under them: the compare-and-set guard
    (`TestAnEntityCreationThatLosesTheRaceForItsKeyIsRefused` — two
    writers spelling one key the same way, where the post-write spelling
    check has nothing to catch and the guard is all that stands between
    the loser and a silent overwrite); the pre-read's spelling-before-
    version ordering
    (`TestARespelledEntityKeyIsNamedEvenWhenTheVersionIsAlsoStale`, the
    entity twin of `TestARespellingIsNamedEvenWhenTheVersionIsAlsoStale`,
    and the only thing that pre-read uniquely buys); the search vector
    (`TestUpsertEntityWritesAndRewritesTheSearchVector`, below); game
    isolation and the cross-game actor
    (`TestEntitiesAreScopedToTheirProject`,
    `TestAnEntityActorFromAnotherGameIsNamed` — both mandatory areas in
    the design spec's testing section); the name cap; and an unknown type
    key, whose message has to name the type or a seeding agent cannot
    tell a missing type from a missing entity.
11. **`entities.search` got its own test, and its own comment in the
    SQL.** It is application-computed — `search_text` is derived from
    user-declared jsonb whose text fields only the Go validator can pick
    out, so no generated column could compute it — which means every
    write path that changes `name` or `fields` has to come through
    `UpsertEntity` or the row stays indexed under its previous words and
    silently stops being findable, with nothing to signal why.
    `TestUpsertEntityWritesAndRewritesTheSearchVector` reads the
    `tsvector` column back through `plainto_tsquery` (the query shape
    Task 6 will use) and asserts both arms of the upsert write it: the
    insert indexes the name and the text fields, and the update drops the
    replaced field's words and indexes the new ones. Proved red three
    ways — dropping `search = excluded.search` from the `DO UPDATE`,
    dropping `search_text` from the `to_tsvector` call, and making
    `searchTextOf` return nothing. **Task 5 and Task 6 inherit the
    obligation**: `insertEntity` in `types_test.go` writes rows with a
    NULL `search`, so any test that expects to *find* a row must create
    it through the service.

**Corrections made after the Task 4 review** (a second pass, over the
same landed package, closing the findings of the review that read it
end to end; the design itself was approved — the partial-failure
contract held under attack, no isolation hole was reachable, and the
no-leakage claim survived probing with secret-marked neighbours):

12. **A cancelled batch no longer fabricates an `internal_error`.** The
    loop's `ctx.Err()` guard only ran *between* two items. A
    cancellation reaching the item in flight failed that item inside its
    own transaction, `failureFor` filed it under the default arm, and
    one cancellation surfaced twice: as the stop error and as a
    `{Index: n, Code: internal_error, Message: "begin: context
    canceled"}` against an item whose only problem was that nobody was
    left to hear about it — precisely what `failureFor`'s own comment
    reserves for "something nobody planned for". The cancellation can
    also land between the write and the commit, where from inside the
    item the two are indistinguishable. The failure arm now re-checks
    `ctx.Err()` before appending.
    `TestBulkPartialStopsWhenTheCallerIsGone` cancels before item 0 and
    could never see this, so
    `TestACancelledBatchDoesNotReportTheItemInFlightAsAServerFault`
    stops the batch *inside* item 1 — a rival transaction holds that
    row's lock, so the interleaving is the test's and not the
    scheduler's — and asserts the stop error names item 1 and that
    `Failed` is empty. Proved red: before the fix it reported the stop
    at item 2 and filed item 1 as `internal_error`.
13. **`list<text>` values reach the search index, and the comment names
    what does not.** `searchTextOf` collected `values[k].(string)` only,
    so a quest with `tags: ["elite", "dungeon"]` matched neither word —
    and tags and aliases are the archetypal thing a designer searches
    for. Its comment justified the omission as "numbers, booleans and
    dates are found by filtering on the jsonb", naming a `date` type
    this package does not have and not naming the one text-bearing type
    actually excluded. Both are fixed: list elements are indexed, and
    the comment now names what is left out (number and bool, the two
    types carrying no words) and why. `TestAListOfTextIsSearchable`
    pins it on both arms of the upsert. Task 6 inherits a search that
    finds tags.
14. **All three write paths pin the stored type-key spelling in their
    events.** Correction 2 claimed to pin this; it pinned the single
    path. Rewriting either bulk path as
    `entityEvent{TypeKey: in.TypeKey, …}` — the exact shape correction 2
    calls wrong — left the whole suite green, because
    `TestEntityEventsCarryTheStoredIdentity` exercises `UpsertEntity`
    alone and the bulk event test asserted the *entity* key, which the
    database returns however the caller spelled it.
    `TestBulkEventsCarryTheStoredIdentity` runs both modes against a
    type stored as `Quest` and addressed as `quest`, proved red by that
    mutation on each path in turn.
15. **`conflictOnEntityKey`'s respelling arm has a test.** Replacing the
    whole `row.Key != key` branch with the version conflict was green:
    both entity race tests end at the *post-write* spelling check, since
    their guard passes and the upsert returns the rival's row. The
    branch is reachable — a rival commits `"Hogger"` while our writer
    holds a version that row does not have, so the guarded `DO UPDATE`
    matches nothing and the re-read is the only thing left that can name
    the respelling.
    `TestAnEntityLosingItsKeyToAnotherSpellingIsNamedAsARespelling` is
    the entity twin of Task 3's
    `TestACreationThatLosesItsKeyToAnotherSpellingIsNamedAsARespelling`,
    proved red by that replacement.
16. **`FOR UPDATE` on `GetEntityByKeyForUpdate` is pinned.** Deleting it
    left the suite green: both race tests block on the unique index
    inside the `INSERT`, not on this lock, and the guarded `DO UPDATE`
    refuses every lost update on its own. What the lock buys is the
    *number* the caller is told to merge onto, which is a specific,
    testable claim the SQL comment makes.
    `TestTheReportedCurrentEntityVersionIsTheOneTheWriteWouldHaveMet` is
    the entity twin of Task 3's
    `TestTheReportedCurrentVersionIsTheOneTheWriteWouldHaveMet`, proved
    red by removing `FOR UPDATE` and regenerating.
17. **The entity isolation filters are pinned one by one, and the SQL
    header says which are load-bearing.** Dropping `project_id` from
    `GetEntityByID` or from `DeleteEntity` was green in each case
    alone — their only caller is `RemoveEntity`, which reads the row,
    then its type, then deletes, so the three filters mask each other
    and only the three-way mutation turned
    `TestEntitiesAreScopedToTheirProject` red. That is no pin at all: it
    says the three together are load-bearing without saying any one is.
    `TestTheEntityQueriesThatAddressARowByIDAreScopedToTheProject`
    asserts each filter over the query itself, through `dbq`, and each
    was proved red on its own by neutralising that filter
    (`(project_id = … OR true)`, which keeps the parameter and so keeps
    the generated signature). `GetEntityByKey`, `GetEntityByKeyForUpdate`
    and `UpsertEntity` are genuinely unobservable — the composite
    `FOREIGN KEY (entity_type_id, project_id)` already determines an
    entity's project from its type's — so the file header now lists them
    beside the three type-scoped statements it already covered, as
    defence in depth rather than as the mechanism.
18. **A key repeated inside one batch is diagnosed as such.**
    `{Index: 1, Key: "dup", Code: version_conflict, Message: "current
    version is 1"}` was the report for a caller that never claimed a
    version — which sends an agent to re-read the row and retry with the
    version it is handed, at which point its own second item quietly
    overwrites its first. In atomic mode it was worse than a
    misdiagnosis: two items chaining the versions the other leaves
    behind both commit, so `Succeeded` came back holding the same row id
    twice and the caller was told two rows landed where one exists. A
    seeding agent producing an accidental duplicate from a file is the
    ordinary case, and the batch is the only place the real diagnosis
    exists. `repeatedKeys` now finds them up front, keyed on
    (type key, key) both folded — one key under two types is two rows,
    and `strings.ToLower` is exact because `rowKeyPattern` admits ASCII
    only — and reports `invalid_input` naming both indices.

    **The modes answer it differently, and deliberately.** In
    `BulkPartial` the later occurrence is a per-item failure and nothing
    else changes: the first occurrence is a perfectly good item, and
    refusing the batch over one repeated key would throw away the other
    three hundred rows, which is the failure partial mode exists to
    prevent. In `BulkAtomic` the whole batch is refused *before anything
    is written*: a batch naming one row twice cannot be satisfied as
    submitted — the caller asked for n rows and at most n-1 can exist —
    there is no per-item answer to give in a mode that lands everything
    or nothing, and refusing up front makes the report deterministic
    instead of dependent on which of the two items ran first.
    `TestABatchThatRepeatsAKeyIsDiagnosedAsSuch` covers partial with
    both an exact repeat and a differently-capitalised one, and
    `TestAnAtomicBatchThatRepeatsAKeyIsRefusedWhole` covers atomic
    including the version-chained pair, and that one key under two types
    is not a repetition. Both were proved red against the landed code.
19. **Two comments that overclaimed.** `searchTextOf`'s key sort is now
    pinned by `TestTheSearchVectorIsTheSameForTheSameValues`, which
    writes six rows from identical values and requires the stored
    `search::text` — positions included — to match; proved red by
    deleting `sort.Strings`, which shuffles the positions and changes
    nothing about search results, exactly as the comment argues. And
    `BulkFailure`'s doc no longer says a message "is a path and a rule":
    `not_found: no entity type "nosuch" in this game` is neither, so it
    now says what a message actually holds, keeping only the claim that
    holds without exception — never another item's values.
20. **What Tasks 6 and 7 inherit, recorded here so neither reads the
    event decision as finished.**

    *Coalescing is what makes the stated benefit real.* The per-row
    event decision is safer than this plan admitted, for a reason it did
    not give: `realtime.Hub` buffers 64 events per subscription
    (`subscriberBuffer`) and drops the rest, each drop leaves a gap in
    that subscription's own `Seq`, and `internal/web/events.go` already
    turns the gap into a synthetic `resync`. A 400-event burst therefore
    degrades to "refetch your state" rather than to silent loss. But
    that cuts against `events.go`'s own argument: the seeding agent it
    names as the subscriber with the most to lose is exactly the one
    whose burst overflows the buffer, so what it receives is a resync,
    not the per-row warning the comment promised. The warning is real
    for a subscriber watching someone else's burst and for the
    designer's browser; for the agent driving the batch it becomes real
    only once the bursts are coalesced. The comment now says so, and
    **Tasks 6 and 7 own coalescing as the thing that makes the benefit
    true, not as polish.**

    *A successful batch currently tells an agent nothing.*
    `BulkResult.Succeeded` is `json:"-"` — correctly, since `dbq.Entity`
    is a database row and not a wire shape — so a marshalled result
    reports failures and nothing else. **Task 7 must decide what a
    successful batch tells an agent**: how many rows landed, under which
    keys, at which versions. Recorded on the struct as well.

**Corrections from the second review of Task 4** (a third pass over the
same package, closing what the review of the corrections above found;
the design was approved again, and what follows is one real defect, two
ordering-and-wording fixes, and two things the plan was not saying):

21. **A long field no longer makes the row unsavable.** `to_tsvector`
    refuses to build a vector larger than 1,048,575 bytes (SQLSTATE
    54000) and nothing bounded a `text` or `longtext` value — `coerce`
    checks the type and not the length. A `longtext` of 1.49 MB of
    distinct words came back as `ERROR: string is too long for tsvector
    (2197988 bytes, max 1048575 bytes)`, filed under `internal_error` in
    a partial batch and taking the whole batch down in atomic mode: the
    code reserved for what nobody planned for, telling an agent to give
    up on a call it could have fixed. Correction 13 enlarged the surface,
    since list elements now feed the vector too. **A designer pasting a
    lore document simply could not save the row.**

    What gives way is the index, not the content. `fields` is stored
    untouched — a game's writing is the product, and refusing it over an
    index limit is the wrong trade — and `searchTextOf` now bounds what
    it hands the vector at `searchTextLimit`, 128 KiB over the row's
    whole flattened text, cutting back off any rune the bound splits (a
    partial rune is invalid UTF-8, which Postgres refuses outright, which
    would have turned the fix back into the bug). 128 KiB and not
    something nearer the cap because the vector is *bigger* than the text
    it is built from and by how much depends on the words: 1.5 MB of
    sixteen-byte distinct words measured 1.79 MB, and shorter distinct
    words are worse, every entry paying its own lexeme and position
    overhead. Eight times' headroom survives any of that and still
    indexes on the order of twenty thousand words. The documented
    consequence, in the code and in `entities.upsert`'s behaviour: a row
    longer than that is searchable by the words in its first 128 KiB and
    not by the ones after them, and nothing about the stored values
    changes.

    `searchLimitExceeded` maps SQLSTATE 54000 to `invalid_input` as a
    backstop, carrying Postgres's own message, for a future path that
    builds an index from a value without going through `searchTextOf`;
    every other SQLSTATE passes through untouched.
    `TestAnOversizedFieldIsStoredWholeAndFoundByAWordNearItsStart` writes
    1.5 MB of distinct words, requires the row to save, reads the field
    back byte-identical, requires a word near the start to be found and
    requires a word at the end **not** to be — so the bound cannot be
    quietly widened or dropped either way. Proved red on the landed code
    (the exact reported error) and again by raising the limit past the
    cap. `TestAValueTooLargeToIndexIsCallerFixable` pins the mapping,
    proved red by changing the SQLSTATE it matches.
22. **The cancellation guard is consulted before the repeat check.** The
    `repeats[i]` arm ran first and `continue`d without ever asking
    `ctx.Err()`, so a batch whose trailing item was a repeated key
    reported that repeat and returned `(result, nil)` — "the batch ran"
    — after the caller had gone. The guard's whole purpose escaped
    through the one arm that never asked. The check is hoisted above
    everything else the loop does with an item.

    The review that found it could not force it deterministically and
    reported it from the code path; it is forceable.
    `TestACancelledBatchDoesNotAnswerARepeatedKeyInstead` supplies a
    context that arms itself *from inside `Err()`* by asking a second
    connection whether item 0's row exists: that row is visible only once
    item 0's transaction has committed, which is exactly the window
    wanted, and it cannot disturb the item it observes. No timing
    assumption, no `cancel()` aimed at a window a scheduler owns. Proved
    red on the landed code, which returned a nil error and an
    `invalid_input` failure for item 1.
23. **The SQL header's own overclaim.** Correction 17 listed
    `UpsertEntity` among six statements whose `project_id` filter "can be
    caught by no test" and is kept as defence in depth. `UpsertEntity`
    has no project *filter*: `project_id` is a `NOT NULL` column value it
    writes and part of its `ON CONFLICT (project_id, entity_type_id,
    lower(key))` target, so removing it is a hard error and trivially
    observable — the opposite of unobservable. The other five are
    correctly classified; the sentence now names five, `UpsertEntity` is
    called out separately for what it actually is, and the header's
    opening line no longer says every statement "filters on" the project
    id when the upserts carry it as the column they write.
24. **Correction 2 now points forward to correction 14.** A reader
    reaching it first was told the events carry the stored spellings,
    backed by a test that exercised one of the three write paths.
25. **A repeated key is refused even when its first occurrence is
    doomed, and that is now written down.** In partial mode
    `[{key x, min_level: "not a number"}, {key x, valid}]` returns index
    0 as `schema_violation` and index 1 as `invalid_input`, so `x` is
    written by neither item and fixing it costs a second round trip. It
    is the right answer — the batch as submitted names one row twice and
    nothing in it says which was meant, and falling back to "whichever
    survived validation" would make one item's outcome depend on
    another's mistakes — but it was undocumented, and an agent meets it
    the first time it builds a batch from a file. Recorded in the core
    design's "Bulk writes", where an agent's author reads, and on
    `UpsertEntities`. `TestARepeatIsRefusedEvenWhenTheFirstOccurrenceIsDoomed`
    pins it so the claim stays true, proved red by disabling the partial
    path's repeat arm.

26. **The batch machinery is extracted, before Task 5 copies it.** The
    bulk loop, the two modes and their dispatch, the cancellation
    contract, the up-front duplicate check and `failureFor` all lived in
    `entities.go` and were all entity-shaped. Relations fold on
    (relation type, source, target) rather than on (type key, key), so
    Task 5 writing its own would have produced a second duplicate
    detector, a second context guard and a second copy of every finding
    the last three review rounds closed — three of them the cancellation
    guard alone — to be reviewed and fixed twice from then on.

    `internal/metamodel/bulk.go` now holds `BulkMode`, `BulkFailure`,
    `failureFor`, `bulkUpsert` and its two mode arms, and
    `repeatedIdentities`. What differs per kind is a `bulkSpec`: the
    identity two items are folded to, the `FieldError` to report when two
    share it, the name a failure carries, the per-item write, and the
    publish that runs only after the commit. `entityBulkSpec` supplies
    the entity half, and `UpsertEntities` is now the spec plus the
    unwrapping of `upsertedEntity` into the rows a caller is owed.
    `BulkResult` stays in `entities.go` with `Succeeded []dbq.Entity`:
    making it generic would have changed a public type every caller and
    test names, and the batch driver does not need it.

    **No test changed, which was the acceptance test for the
    extraction.** Every existing assertion passed against the extracted
    code unedited, and the seam was re-mutated afterwards to confirm the
    old tests still reach it: dropping either cancellation check, the
    atomic repeat refusal, moving the atomic publish inside the
    transaction, reading an unknown mode as partial, and dropping
    `failureFor`'s `invalid_input` arm each turned the suite red, naming
    the same tests as before.

    **Task 5's printed blocks predate this file.** Where they show a
    relation batch reproducing the loop, the loop is `bulkUpsert`'s and
    what Task 5 writes is a `bulkSpec` — its own identity (relation
    type, source, target, folded as that unique index folds), its own
    repetition message (the advice "give one of the two a different key"
    says nothing about a pair of edges sharing their endpoints), its own
    write and its own publish.

**The partial-failure contract, stated once.** In `BulkPartial` each item
is its own transaction: the rows that fit land, the ones that do not come
back in `Failed` with their index, their key and their code, and the
caller retries the named items and nothing else — item 200 landing never
depended on item 3. The call returns a nil error, because the failures
*are* the result. In `BulkAtomic` one bad row rolls the whole batch back
and the call returns an error naming the failing item
(`item 1 ("b"): schema_violation: …`, with the sentinel still matchable
through the wrapping) and an empty result, because nothing was done. A
failure message is the item's own error — a field path and a rule where
the item's own arguments or values are at fault, and otherwise the plain
reason it was refused, which may name nothing of the item at all
(`not_found: no entity type "quest" in this game`) — and never another
item's values: a batch report is the one place a row's content could leak
into a neighbour's error, and
`TestBulkPartialLandsTheGoodRowsAndReportsTheRest` asserts the failing
item's message names `fields.min_level` and none of the names or values
of the items around it.

**The entity events' gating**, argued in `events.go` rather than
inherited from the type events it happens to agree with: `MinRole` empty,
because a viewer's browser renders the entity list and gating the change
above viewer leaves exactly the reader who cannot re-fetch on demand
watching a list that drifts; `HumanOnly` false, because Task 7 mounts
`entities.list` and `entities.upsert` on MCP for agents, so an agent may
already read every one of these rows and withholding the push buys
nothing while costing a seeding agent the one warning that would explain
its next `schema_violation`. The known consequence is recorded rather
than gated around: these events fire once per row, so a 400-row seed puts
400 events on every subscription. Role gating would not fix that and
would only starve the viewer the decision exists to serve; coalescing a
burst belongs to the transport, where the subscriber and its backlog are
visible, and **Tasks 6 and 7 own it** — see correction 20 for what
actually reaches a subscriber from a burst that size, and why coalescing
is what makes the benefit claimed here true rather than a polish item.

---

### Task 5: Relation types and relations

**Files:**
- Create: `internal/metamodel/relation_types.go`, `internal/metamodel/relations.go`
- Modify: `internal/db/queries/metamodel.sql`, `internal/metamodel/events.go`
- Test: `internal/metamodel/relations_test.go`

**Refreshed after Task 3's review, 2026-09-02**, for the same reasons and
against the same corrections as Task 4: the blocks below previously
carried an unguarded `DO UPDATE`, an `updated_at = now()` the trigger
owns, no audit columns, a three-argument `s.publish` with hand-built JSON
string payloads, no key or descriptor validation, no `Code` on the
`ValidationError`, no post-write spelling check and no transaction around
the read-then-delete in `RemoveRelationType`.

Two differences from entity types, both from the schema
(`0004_metamodel.sql`) and neither an oversight:

- `relation_types` has `label` and `description` and no plural, colour or
  icon, so it calls `checkDescriptors` with the columns it does not have
  left empty — empty means "not set", which is exactly true here.
- `relations` has **no `version` column** at all. An edge is identified by
  `(relation_type_id, source_id, target_id)` and carries only its own
  fields, so there is no lost update to guard against: re-writing an edge
  with different fields is the whole operation, and the last writer wins
  by design. `RelationInput` therefore has no `ExpectedVersion`, and this
  is a decision rather than an omission — record it if Task 7 wants edge
  concurrency, because it would need a migration. **It is also half of a
  pair**: correction 21 below records why it must be revisited together
  with the refusal of parallel edges, which is what sends a game's
  multiplicity into the very edge fields this leaves unprotected.

**Corrections made during implementation** (a pass over the landed
package, written as Task 5 shipped so Tasks 6-9 build on what exists
rather than on what was planned):

1. **`ListEntitiesRelatedTo` was not added.** It is printed in Step 1
   below and called by no line of Task 5; its only caller is Task 6's
   one-hop traversal. A query landing a task ahead of its caller ships
   generated code no test exercises, which is Task 4's correction 1
   exactly. It moved to Task 6's Step 1, where its caller is.
2. **`UpsertRelations` returns a `RelationBulkResult`, not a
   `BulkResult`.** `BulkResult.Succeeded` is `[]dbq.Entity` (Task 4's
   correction 26 keeps it that way deliberately, since making it generic
   would change a public type every existing caller and test names), so
   the printed signature does not compile. The new type is the same
   shape with `[]dbq.Relation`, carrying the same `json:"-"` and the same
   note that **Task 7 must decide what a successful batch tells an
   agent**.
3. **The batch is `bulkUpsert`'s; Task 5 writes only a `bulkSpec`.** The
   printed `UpsertRelations` reproduces the two-mode loop by hand, which
   is the copy correction 26 extracted `bulk.go` to prevent — it would
   have arrived without the duplicate check, without the cancellation
   guard and without the up-front atomic refusal. What is edge-shaped is
   `relationBulkSpec`: the identity (relation type key plus both
   endpoints' type key and key, all five folded, since every one of them
   is resolved through a `lower(key)` lookup), the repetition message,
   the failure name and the publish.

   The length-prefixed join moved into `foldedIdentity` in `bulk.go` and
   both specs call it. Entities had it inline for two parts; edges have
   five, and two kinds folding identity differently is how the two
   silently drift apart.
4. **Every relation event carries the *stored* spelling of the relation
   type's key, on all three write paths**, and `RemoveRelation` reads the
   type to fill it. The printed code publishes `TypeKey: in.TypeKey` —
   the caller's spelling, Task 4's correction 2 under a different column
   — and its removal publishes no type key at all, which is Task 3's
   correction 22: `relationEvent` declares the field and a client reading
   one cannot tell "not carried" from "empty". A written edge travels
   with its type's stored key in an unexported `upsertedRelation`, as
   `upsertedEntity` does, so no caller pairs a row with the wrong key by
   getting an index wrong.
   `TestRelationEventsReachEveryMemberOfTheGameIncludingAgents` runs
   every path against a type stored as `Requires` and addressed as
   `requires`; proved red on the bulk publish and on the removal in turn.
5. **Four gating constants, not two.** The step asks for one
   `relationEventMinRole` / `relationEventHumanOnly` pair covering both
   kinds. A relation type is the game's edge *vocabulary* and a relation
   is its content; the two decisions agree today, and naming one in terms
   of the other would make a later change to either silently change both
   — the argument `entityEventMinRole` already records. Both are argued
   in `events.go` rather than inherited: `MinRole` empty because a
   viewer's browser renders the graph and gating the invalidation above
   viewer starves exactly the reader who cannot re-fetch, `HumanOnly`
   false because Task 7 mounts these tools on MCP for agents and an
   endpoint rule moving under a seeding agent is the one warning that
   explains its next `endpoint_type_mismatch`.
6. **The endpoint lists are checked when they are declared.**
   `source_type_ids` and `target_type_ids` are plain `uuid[]` with no
   foreign key of their own, so an id from another game — or from
   nothing at all — was stored happily and then matched no entity ever,
   leaving a relation type nothing could instance and a refusal
   ("cannot be the source of") naming the wrong problem. Each id is
   resolved against this project inside the write's own transaction and
   reported at `source_type_ids[i]` / `target_type_ids[i]` as
   `invalid_input`, both lists in one pass.
   `TestARelationTypeEndpointListMustNameTypesOfThisGame` pins it.
7. **An undeclared endpoint list is written as an empty array.** pgx
   encodes a nil slice as SQL `NULL` and both columns are `NOT NULL`, so
   passing the zero value through failed the insert outright — the
   *ordinary* case, since most relation types declare no endpoint rules.
   Empty is also what nil means here, and what the column defaults to.
8. **Every missing piece of an edge is named, and so is the endpoint
   that broke a rule.** An edge has three parents, any of which may be
   absent, and `fmt.Errorf("source %s/%s: %w", …)` puts the sentinel's
   own text (`not_found`) at the end of the sentence. The messages are
   `not_found: no relation type "x" in this game`,
   `not_found: source: no entity type "x" in this game`,
   `not_found: target: no entity "y" of type "x" in this game`, and
   `endpoint_type_mismatch: source: entity type "class" cannot be the
   source of relation type "takes_place_in"` — each naming which end to
   fix, since an agent with two bad endpoints cannot otherwise tell.
   `TestEachMissingPieceOfAnEdgeIsNamed` covers the four.
9. **An unknown `TypeKey` in a listing filter is a `not_found`, not an
   empty listing**, so a caller that mistyped a key hears about the key.
   The page bounds are named constants beside the listing rather than two
   literals in the middle of it; Task 6 owns the cursor.
10. **Parallel edges and self-loops are decided and written down**, on
    `UpsertRelation` and in the design spec's "Constraints and indexes".
    The `relations_edge_key` index is the ON CONFLICT target that makes a
    re-seed idempotent, so a game cannot hold two edges of one type
    between one ordered pair. A game that needs two says so as a second
    relation type or in the edge's own fields (`passages: ["door",
    "vent"]`), both of which are better records than a nameless
    duplicate. Self-loops are allowed and pinned by
    `TestAnEdgeMayJoinAnEntityToItself`: a self-referencing prerequisite
    is a design mistake to surface in analysis, not a write to block.
11. **The re-validation gap is recorded where it bites.** Editing an
    entity type's schema re-checks its entities and flags the ones that
    no longer fit; editing a relation type's schema does nothing of the
    kind, because `relations` has no `invalid` column. Nothing here makes
    that harder to close — `upsertRelationWith` validates on write
    through the same `Schema.Validate`, and a sweep would need the column,
    a `ListRelationFieldsOfType` query and a `MarkRelationsOfTypeInvalid`
    beside their entity twins — but `UpsertRelationType`'s doc comment now
    says plainly that it does not re-judge existing edges, rather than
    leaving a reader to infer it from the entity path.
12. **Tests the block did not carry**, each proved by breaking the code
    under it: the relation-type creation race
    (`TestARelationTypeCreationThatLosesTheRaceForItsKeyIsRefused`, the
    guarded `DO UPDATE`); the post-write spelling check
    (`…CreationRacingAnotherSpellingIsRefusedAfterTheWrite`, the only
    path the locked pre-read cannot reach); `conflictOnRelationTypeKey`'s
    respelling arm (`…LosingItsKeyToAnotherSpellingIsNamedAsARespelling`);
    the `FOR UPDATE`
    (`TestTheReportedCurrentRelationTypeVersionIsTheOneTheWriteWouldHaveMet`,
    which needs *no* `ExpectedVersion` — with one, the guard refuses on
    its own and the lock is unobservable, exactly as Task 4's correction
    16 found); every project filter, one at a time, over the queries
    themselves
    (`TestTheRelationQueriesThatAddressARowByIDAreScopedToTheProject`,
    since `RemoveRelation` reads, re-reads and deletes, so its three
    filters mask each other); the three optional filters of
    `ListRelations`; both endpoint rules and the empty-list-means-any
    half; the cross-game endpoint and the cross-game actor; the batch in
    both modes, including that an atomic rollback publishes nothing and
    that a repeated edge is diagnosed as a repeated *edge*.

    One mutation deliberately left green: dropping `RemoveRelationType`'s
    in-use count still refuses, because `relations.relation_type_id` is
    `ON DELETE RESTRICT` and the delete raises 23503, which the same
    function maps to `ErrInUse`. That is what `types.go` already says
    about the entity twin — the count earns a typed refusal without a
    transaction aborting on a raw constraint violation, not the refusal
    itself.

**Corrections from the review of Task 5** (a second pass over the landed
package, after the implementation corrections above. Eight findings, four
of which were fixed before this block was written; all eight are recorded
here, because a correction nobody can find is a correction that gets
undone. Two of them are the same fault the earlier reviews kept finding —
*a doc comment that states an invariant and a test that does not pin
it* — and one is a claim that had gone false.)

13. **A missing parent of an edge is named instead of arriving as
    `internal_error`.** The three composite foreign keys under
    `relations` — on `relation_type_id`, `source_id` and `target_id` —
    fire when a rival transaction deletes a parent between
    `upsertRelationWith`'s lookup and its insert; the lookups read under
    `READ COMMITTED`, so they cannot be the only answer. Unmapped, that
    reached a caller as `internal_error` carrying `violates foreign key
    constraint relations_target_id_project_id_fkey`, which names neither
    which of the three parents is gone nor that anything is missing at
    all, and in atomic mode aborted a whole batch with it.
    `edgeParentViolation` maps SQLSTATE 23503 on the constraint's column,
    exactly as `actorConstraintViolation` does, to the `not_found` the
    lookup would have given a moment earlier — the recovery is the same:
    create the row and retry. The names in the message are the caller's
    own spellings, because there is nothing left to read them from, which
    is the condition being reported.
    `TestARaceOnAnEdgesParentsNamesWhichParentIsGone` drives each of the
    three constraints with a missing id against the real schema, so the
    constraint names are Postgres's and not a guess. Commit `fce2df7`.

14. **`foldedIdentity`'s length prefix is pinned.** Correction 3 moved the
    length-prefixed join into `bulk.go` so entities and edges could not
    fold identity two different ways, and then no test held the prefix in
    place: a join on a plain separator passed every existing test while
    letting two different five-part edges collide on one string.
    `bulk_internal_test.go` now pins the shape and the collisions it
    exists to prevent. Commit `d0a72fd`.

15. **Removing an entity type prunes its id out of every relation type's
    endpoint lists.** `source_type_ids` and `target_type_ids` are plain
    `uuid[]`; Postgres has no foreign key from an array element, so
    correction 6's invariant — an endpoint list always names a type of
    this game — held only until the first removal. What it left behind
    was not untidy but unrepairable: the relation type held a rule
    nothing could satisfy, a designer who recreated the key got a fresh
    id and was refused with `entity type "zone" cannot be the target of
    relation type "takes_place_in"` while `zone` visibly *was* the
    declared target, and re-declaring the type with the list it currently
    held was refused as `invalid_input`.
    `PruneEntityTypeFromEndpointLists` runs in the same transaction as
    the delete. **Pruning rather than a real referential constraint** is
    the choice and not the cheap way out of it: a constraint over these
    lists would mean junction tables with a composite `ON DELETE
    CASCADE`, a migration, a rewrite of every read and write of an
    endpoint rule, and a change to the shape `RelationTypeInput` presents
    to an agent — quite possibly the right end state, and a schema
    decision rather than Task 5's. **One consequence is recorded because
    it is a widening**: pruning the last id of a list leaves it empty, and
    empty means "any type", so a relation type that accepted only `zone`
    at its target accepts anything once `zone` is removed. That is the
    lesser of the two — the widening is visible in the row a designer
    reads and is one edit away from being narrowed again, where the
    dangling id was neither visible nor repairable. Commit `4c0247f`.

    **Superseded in part by corrections 22 and 23 — read those before
    relying on this one.** The prune alone closes only the sequential
    path, and it publishes.

16. **Both bad endpoints of an edge are reported in one answer.**
    Returning on the source made the two halves of one decision disagree:
    `checkEndpointTypes` already reports both endpoint *lists* in one
    pass, on exactly the argument that an agent holding two bad endpoints
    otherwise fixes the one it was told about, resends, and is told about
    the other. `bothEndpoints` joins two failures with `"; "` rather than
    through `errors.Join`, whose newline would put a batch report's
    `Message` on two lines, and wraps both with `%w` so `errors.Is` still
    matches whichever the caller asks about and `failureFor` still files
    the item under `not_found`. The price is the two lookups the target
    end costs on an item that was going to fail anyway; the success path
    always paid them. Commit `05e5e33`.

    **Extended by correction 25**, which found this covered resolution
    only and not the endpoint rule the same function checks next.

17. **Correction 9 was false as written, and the test that should have
    caught it was the reason.** It claims an unknown `TypeKey` in a
    listing filter is "a `not_found`, not an empty listing, so a caller
    that mistyped a key hears about the key". The listing path went
    through `RelationTypeByKey`, which used the generic `notFound`
    helper, so the whole message was the bare word `not_found` and the
    key was never mentioned — the write path (`upsertRelationWith`) got
    it right and the read path did not. The test carried the correct
    comment and asserted only `errors.Is(err, ErrNotFound)`, which is
    green against precisely the message the decision refuses to return:
    *the negative test this whole series has been closing, one file away
    from where it was last closed.*

    The three by-key accessors — `EntityTypeByKey`, `RelationTypeByKey`,
    `EntityByKey` — now name what they did not find, and the listing test
    asserts the message and not just the sentinel.
    `TestTheGenericNotFoundIsNotUsedWhereACallerSuppliedAKey` is the
    standing reminder in test form.

    **The sweep behind it, and where the generic helper is still
    right.** Every remaining `notFound` call is a lookup *by id*, and
    they fall in two groups. Those addressing a row by an id the caller
    sent (`EntityTypeByID`, and the reads inside `RemoveEntityType`,
    `RemoveRelationType`, `RemoveRelation`, `RemoveEntity`) have no
    second argument to tell apart — the caller already holds the id it
    sent — which is the rule `EntityTypeByKey`'s comment states. Two more
    (`RemoveRelation`'s relation-type read, `RemoveEntity`'s entity-type
    read) take their id from the row just read rather than from the
    caller at all, and both foreign keys are `ON DELETE RESTRICT` inside
    the read's own transaction, so no-rows there is unreachable rather
    than unlikely: there is no key to name, and if it ever fires the
    schema is inconsistent. Both sites now say so, so the next sweep does
    not have to re-derive it. Commit `fce2df7` and the code commit below.

    **That last sentence was itself false, and correction 24 fixes it.**
    `ON DELETE RESTRICT` does not make either read unreachable: both
    cascading removals delete the children first and the parent second.
    The rest of the sweep — which lookups may use the generic helper —
    stands.

18. **A test proved nothing its name claimed, and the claim was the
    premature half.** `TestAnAtomicRelationBatchSeesItsOwnEntities`
    pre-seeded both entities through separate committed calls, and
    `UpsertRelations` has no path that creates an entity, so routing all
    three of `upsertRelationWith`'s parent lookups through `s.q` instead
    of the transaction handle left the suite green — leaving
    `upsertRelationWith`'s central doc comment, that the atomic path
    resolves endpoints against its own transaction, unpinned.

    **The call: both halves, because the claim is true and its public
    path is not.** Taking the transaction handle is right today — it is
    what lets the three paths share one implementation, it costs nothing,
    and reversing it now would be a decision to redo in Task 9 — so the
    claim is pinned at the only level where it holds, by the package's
    own `TestAnEdgeResolvesItsEndpointsAgainstItsOwnTransaction`, which
    writes an entity inside a transaction and then an edge naming it,
    where no other connection can see the entity. What is narrowed is the
    *reach* of the claim: `upsertRelationWith`'s doc comment now says
    plainly that no public caller reaches it, and that **Task 9's seeding
    of a whole game in one call is where a mixed batch actually
    appears**. The external test is renamed
    `TestAnAtomicRelationBatchLandsEveryEdgeOfTheBatch`, which is what it
    proves, and records what it used to claim and why it did not. Commit
    `fce2df7`.

19. **A cascaded removal is announced only as its parent's event, and
    `events.go` now says a subscriber must read those as edge
    invalidations.** Three removals delete edges without a
    `relation.removed` each: `entity.removed` takes every edge touching
    the entity (both endpoint keys are `ON DELETE CASCADE`),
    `relation_type.removed` with cascade takes every edge of that type,
    and `type.removed` with cascade takes the type's entities and through
    them their edges. It is inferable from the schema, and inference is
    not a contract when the consumers are two tasks away: Task 6's
    one-hop traversal caches a node's neighbourhood and Task 8's page
    draws the graph from it, and every *other* way an edge disappears is
    announced one by one, so a client written against `relation.removed`
    alone renders dead edges.

    **Publishing one `relation.removed` per cascaded edge is the
    alternative and is deliberately not taken**: the parent's event
    already carries enough to invalidate, and a cascade over a
    well-connected entity would put thousands of events on a 64-deep
    subscription buffer — the coalescing problem `events.go` already
    records, at its worst. If Tasks 6-8 find the coarse signal too blunt,
    the answer is a payload carrying the count or the ids, not a per-edge
    event.

    **Correction 23 adds a fourth effect of `type.removed` that this note
    deliberately does not cover**, because it deletes nothing: the
    endpoint-list prune, which is announced per row rather than inferred.

20. **`Limit: 501` yielded 100 rows, so asking for slightly too much got
    strictly less than asking for the cap.** `ListRelations` folded "no
    opinion" (0 or negative) and "more than the cap" onto one arm and
    gave both the default. They are two different requests: the second is
    a caller that wants as many rows as it is allowed, which is what
    `Limit: math.MaxInt32` means and what every paginated API in reach
    does with it. The failure was silent, so a caller that trusted it
    under-read the game's graph and was never told.
    `relationPageSize` now clamps to `maxRelationPage` and keeps the
    default only for the no-opinion arm.

    The tests exercise the values that are neither 0 nor 100, which is
    what let the bug live: `TestARelationPageAsksForTooMuchAndGetsTheCap`
    walks -7, 0, 1, 37, 100, 499, 500, 501 and 2^20 — the boundaries
    either side of the cap included, since a clamp written with the wrong
    comparison passes at 501 and fails at exactly 500 — and the external
    listing test gained an explicit `Limit: 2` and a `Limit: 501` case,
    so the wiring is pinned and not only the arithmetic.

21. **Two decisions recorded as linked rather than independent: no
    parallel edges, and no version on edges.** Each is defensible alone
    and neither is reversed here; together they compound, and the
    compounding was invisible because the two were written in separate
    doc comments. Forbidding two edges of one type between one ordered
    pair **pushes multiplicity into the edge's fields** — that is the
    escape hatch `UpsertRelation` offers by name, `passages: ["door",
    "vent"]` on a single `connects_to` — and `relations` having no
    `version` column then **leaves exactly those fields with no
    concurrency protection at all**. The example the first decision leans
    on is precisely a list two designers extend at once, and entity
    fields never had this exposure: they have `version`, and an entity is
    where multiplicity would otherwise have gone.

    Verified rather than reasoned about, and now pinned by
    `TestConcurrentEditsToOneEdgesFieldsAreLostSilently`: two upserts of
    the same triple with `passages: "door"` then `passages: "vent"` hit
    one row id, the second takes the row whole, no error is raised, and
    the two
    `relation.upserted` payloads are byte-identical — so nothing in the
    system records that a write was lost. The test pins the loss
    deliberately and goes red the day `version` arrives, which is the
    correct outcome: both doc comments need rewriting with it.

    **Both decisions stand and Task 7 owns any migration.** What changed
    is that each doc comment now names the other, so whoever opens either
    question sees both: adding `version` makes the parallel-edge refusal
    cost what it was assumed to cost, and relaxing `relations_edge_key`
    instead makes the version question moot — and that index is the
    `ON CONFLICT` target a re-seed's idempotence rests on.

**Corrections from the re-review of Task 5** (a third pass, over the
landed package *and* the corrections block above. Five findings: one race
the corrections above claimed to have closed and had not, one silent
write nothing announced, and three doc comments that told the next reader
more than the code does. Every one of them is a sentence that had gone
false — which is the failure mode this series keeps finding, now in the
corrections themselves.)

22. **Correction 15's prune closed the sequential path and left the
    concurrent one open, and the doc comment said otherwise.**
    `RemoveEntityType`'s comment claimed the prune was "exact for the
    only way an id can go dangling today". It was exact for every id that
    existed when its statement ran, which is not the same thing:
    `PruneEntityTypeFromEndpointLists` is a single `UPDATE` under READ
    COMMITTED, and `UpsertRelationType`'s **creation** path had no row for
    it to find and took no lock of its own against `entity_types`. A
    relation type created between the prune's statement and the removal's
    commit kept the removed id. The *update* path was genuinely safe —
    the prune's own row lock plus READ COMMITTED's re-check catch it — so
    creation was the whole hole, and creation is the common case for a
    seeding agent. The victim reached exactly the state correction 15
    calls unrepairable.

    **The fix is a share lock and not a re-run of the prune**, of the
    three shapes on the table. Re-running the prune as `RemoveEntityType`'s
    last statement narrows the window without closing it — it is another
    unlocked `UPDATE`, and a creation committing after *it* is the same
    bug one statement later. The junction-table migration closes it
    properly and is still the right end state, and is still a schema
    decision rather than this correction's. What is left is to make the
    two writers agree on a lock, and the read that has to happen anyway is
    the natural place for it: `checkEndpointTypes` now reads through
    `LockEndpointEntityTypes`, `SELECT … WHERE id = ANY(…) FOR SHARE`, so
    the removal's `DELETE` waits for any transaction currently declaring
    a rule over that type, and the prune — which runs after the delete —
    then sees the row that transaction wrote. `FOR SHARE` and not `FOR
    UPDATE`: two writers may name the same entity type at once and only a
    delete of it has to wait. It also folds a per-id loop into one
    statement, since the lock and the check are the same read.

    **Lock order is load-bearing and is recorded in the query.**
    `UpsertRelationType` takes this lock *before* the `FOR UPDATE` on its
    own `relation_types` row, and `RemoveEntityType` deletes the entity
    type before pruning the relation types; both take `entity_types`
    first, so neither holds what the other waits for. Moving the endpoint
    check after the relation type's row lock reintroduces a deadlock
    between exactly these two calls.

    `TestARelationTypeCreatedDuringATypeRemovalCannotKeepTheRemovedID`
    stages the interleaving rather than racing for it: a third connection
    holds an uncommitted `relation_types` row spelled `takes_place_in`,
    which parks the upsert on the unique index after it has read and
    locked its endpoint types and before it writes anything; the removal
    then runs into the share lock. The test is deterministic in both
    directions — it polls `pg_stat_activity` in its own throwaway
    database rather than sleeping — and it was watched fail with the
    dangling id before the lock existed and again with `FOR SHARE`
    stripped out of the generated query. Commit `c6a2fb1`.

23. **The prune wrote rows nobody named and announced nothing; it now
    publishes `relation_type.upserted` for each.** Correction 15
    introduced a fourth effect of `type.removed` that correction 19's
    cascade note does not cover, and could not: that note is about edges
    that are *deleted*, and here nothing is deleted — the relation type is
    still there and what changed is the rule it states. No caller-visible
    write bumped its `version` either, so a subscriber diffing versions
    saw nothing, and Task 8's rendering of an endpoint rule would show a
    list the database no longer holds.

    **Publishing beats widening the note**, which is the opposite call to
    correction 19 and for the reason correction 19 itself gives. That note
    refuses a per-edge event because a cascade over a well-connected
    entity would put thousands of events on a 64-deep buffer; a game has a
    handful of relation types, so the same argument does not reach here.
    And a subscriber told only "`type.removed` also invalidates endpoint
    lists" has to re-read every relation type of the game to find out
    which; the event names the row. `relation_type.upserted` and not a new
    event kind, because it is the same change a caller-visible edit of the
    same two columns publishes, arriving by another route.
    `PruneEntityTypeFromEndpointLists` returns the identity of each row it
    changed, and the caller sorts them by key before publishing — an
    `UPDATE` cannot order its `RETURNING`, and an unordered burst is a
    burst that arrives differently twice.
    `TestAPrunedEndpointListIsAnnouncedToTheRowsOwnSubscribers` pins the
    event and the untouched relation type that must *not* be announced.
    Commit `b0de96b`.

24. **"Unreachable rather than merely unlikely" was false at both sites,
    and it is the last paragraph of correction 17 that said so.**
    `RemoveRelation` and `RemoveEntity` each read their row and then read
    its parent type, and both comments argued the second read cannot come
    back empty because the foreign key is `ON DELETE RESTRICT` inside the
    read's own transaction. RESTRICT does not protect it: it refuses a
    delete that would orphan a child, and both cascading removals delete
    the children *first* and the type second. The first read takes no row
    lock, so under READ COMMITTED the type can vanish between the two
    statements. Verified live at both sites.

    **A doc defect and not a behavioural one**, which is why nothing
    changed but the comments. The `not_found` the branch produces is the
    right answer: the row being read is itself being deleted by the same
    cascade, so the call was going to fail a statement later anyway. What
    was wrong was telling the next sweep the branch cannot fire and that
    firing would mean a corrupt schema — both halves false, and the second
    one is the kind of claim that gets a real incident misdiagnosed. Both
    comments now say it is reachable only against a cascading removal of
    the parent, where `not_found` is correct regardless. Commit `967e2da`.

25. **Correction 16 covered resolution only, and its own comment claimed
    otherwise.** `upsertRelationWith` justified reporting both ends
    together as "the same argument `checkEndpointTypes` makes twenty lines
    from here". `checkEndpointTypes` genuinely reports both endpoint lists
    in one pass; the edge path did not — the two `endpointAllowed` checks
    still returned one at a time, so two wrongly-typed ends yielded only
    the source's mismatch. Worse, a caller with one *missing* end and one
    *wrongly-typed* end heard only the `not_found`, fixed it, resent, and
    only then heard the mismatch: the hidden second hop, inside the code
    whose comment said it had been removed.

    Both ends are now judged — resolved and checked against the endpoint
    rule — before either is reported; an end that did not resolve has no
    type to judge and its own failure stands. **`not_found` wins when the
    two halves disagree**, and it falls out of `failureFor`'s existing
    order rather than needing a rule of its own. That is the right way
    round and not an accident: an end that does not exist cannot be judged
    against the endpoint rule at all, and creating it is the step that
    decides which type it will have — so `not_found` names the work to do
    next, and the mismatch travels in the message so the retry already
    knows about it. `TestBothBadEndsOfAnEdgeAreAnsweredInOnePass` pins the
    message and, through `UpsertRelations`, the wire code; flipping the
    two arms of `failureFor` turns it red.

    **The doubled prefix is gone with it.** Two ends failing the same way
    printed `not_found: source: …; not_found: target: …`, naming a code
    the reader had already been given. `bothEndpoints` returns a
    `joinedEndpointError` whose `Unwrap() []error` keeps both sentinels
    reachable — `errors.Is` and `failureFor` behave exactly as before —
    and whose message drops the second copy of the prefix *only* when the
    two halves agree on it. When they name different codes both are kept,
    because then the second code is news. Commit `8ebc29e`.

26. **A caveat, recorded rather than closed: a raced edge names one
    missing parent per answer.** Postgres reports the first constraint a
    statement violates and stops, so a race deleting two of an edge's
    three parents at once is answered with one of them and the caller
    meets the second on its retry — the same class of hidden second hop
    correction 25 closes for the ordinary path. It is left open
    deliberately: closing it would mean re-reading all three parents after
    a failed write to find out which are still gone, work on a path only a
    race reaches to save a round trip only a rarer race costs.
    `edgeParentViolation`'s comment now says so. Commit `5478acc`.

**Corrections from the locking verification of Task 5** (a fourth pass,
made after `FOR SHARE` landed, that held a real lock with a real
`lock_timeout` against it rather than reading the code. Two findings
closed, one recorded because it was never a bug: the sort correction 22's
own re-review left unpinned survives with the pin removed, and it does so
because the two orders it is defensive against — a plan flip, and
`lower(key)` versus raw byte order — had never been made to disagree in a
test.)

27. **A lock timeout on the endpoint check was reported as
    `invalid_input`, telling an agent not to do the one thing that would
    have worked.** `checkEndpointTypes` turned any error from
    `LockEndpointEntityTypes` — including one this transaction had no way
    to satisfy — into `FieldError{Message: "could not be checked: " +
    err.Error()}`, and `UpsertRelationType` wrapped that as
    `ValidationError{Code: codeInvalidInput}`. Verified live with
    `lock_timeout = '300ms'` held against a conflicting row lock: the
    caller got `invalid_input: target_type_ids: could not be checked:
    ERROR: canceling statement due to lock timeout (SQLSTATE 55P03)`, and
    `errors.Is(err, metamodel.ErrInvalidInput)` was true. This path was
    unreachable before correction 22 — the old per-id
    `GetEntityTypeByID` took no lock and could not be cancelled by
    `lock_timeout` — so `FOR SHARE` is what opened it: any deployment
    setting `lock_timeout` or `statement_timeout` now has a retryable
    contention event on this call reported as the one code that tells an
    agent resending its input unchanged is pointless, when resending
    unchanged is exactly the correct recovery. `FieldError.Message` also
    flattened the error to a string, so nothing downstream could recover
    the SQLSTATE either — `errors.As` for `*pgconn.PgError` returned
    nothing.

    **The fix returns the error instead of a field problem.**
    `checkEndpointTypes` now returns `([]FieldError, error)`; a failure
    to read is the second return, propagates out of `UpsertRelationType`
    untouched, and reaches the caller wrapped only enough (`fmt.Errorf`
    with `%w`) to keep `*pgconn.PgError` reachable through `errors.As`
    — its SQLSTATE intact. It matches none of the package's domain
    sentinels, so it lands on `failureFor`'s and `mcpErrorFor`'s existing
    default arm, `internal_error`: an honest "something on the server
    side went wrong" rather than a caller-fixable field problem.

    **Whether 55P03 and 57014 earn a fourth wire code, so an agent is
    told to retry rather than merely told the call failed, is left
    open.** None of the three field-shaped codes (`invalid_input`,
    `schema_violation`, `invalid_schema`) nor the three others this
    package's sentinels carry (`not_found`, `version_conflict`,
    `endpoint_type_mismatch`, `in_use`) say "retryable" — a caller
    reading `internal_error` learns nothing about whether trying again
    would help. `internal_error` is still the more honest of the two
    options on the table today, which is why it is what ships here: it
    does not claim the input is wrong, which `invalid_input` did and was
    false. A dedicated code is a wire-contract decision for whichever
    task maps this package's errors onto MCP — mcp_errors.go's own doc
    comment already names that mapping as unbuilt — and this fix
    forecloses nothing: the SQLSTATE survives on the error precisely so
    that mapping can switch on it later without another trip through the
    database layer.

    **No other site in the package flattens a database read error into a
    FieldError this way.** Every other `FieldError{..., Message: ... +
    err.Error()}` construction (`validate.go`, `schema.go`) reports a
    *value* or *schema* problem the caller supplied — a bad default, an
    invalid regex — not a failure to read the database at all.

    `TestALockTimeoutOnTheEndpointCheckIsNotReportedAsInvalidInput`
    (relations_test.go) gives every connection in a pool of its own a
    real `SET lock_timeout = '300ms'`, holds a conflicting `FOR UPDATE`
    on the endpoint's own row from a separate connection, and asserts the
    resulting error is neither a `*ValidationError` nor
    `errors.Is(err, ErrInvalidInput)`, while `errors.As` still recovers a
    `*pgconn.PgError` with `Code == "55P03"`. It was watched fail with
    the exact message quoted above before this fix, against the code on
    `relation_types.go` as it stood after correction 22. Commit
    `f3f8aca`.

28. **Correction 22's own defensive sort survived deletion, because
    nothing had forced its two orders to disagree.** `RemoveEntityType`'s
    `sort.Slice(pruned, func(i, j int) bool { return pruned[i].Key <
    pruned[j].Key })` is correct and was added because `RETURNING` on
    `PruneEntityTypeFromEndpointLists`' single `UPDATE` promises no order
    at all — but with it removed, the whole suite stayed green, this
    task's own `TestAPrunedEndpointListIsAnnouncedToTheRowsOwnSubscribers`
    included. A probe against this suite's live database explains why:
    the statement's actual plan today is an index scan on
    `relation_types_key_key`, so `RETURNING` already arrives ordered by
    that index — `lower(key)` — before Go ever sorts it, and every prior
    test used keys whose byte order and folded order happened to agree.

    A second, independent gap sits under the first: the Go sort compares
    raw `Key` in byte order, and the index it happens to ride orders by
    `lower(key)`; stored keys permit uppercase, so the two orders are not
    the same order. Both are deterministic — nothing here was ever a
    correctness bug — but a comment claiming the sort's order matched the
    database's own would have been as false as correction 24's "cannot
    fire."

    **Pinned rather than merely documented**, because the disagreement is
    reproducible without forcing a plan flip: two relation types keyed
    `Zone_rel` and `apple_rel` (capital `Z` = 0x5A sorts before lowercase
    `a` = 0x61 in bytes, but `zone_rel` folds after `apple_rel`) give the
    Go sort and the index's natural order the exact opposite sequence.
    `TestPrunedEndpointListsArePublishedInSortOrderNotDatabaseOrder`
    (events_test.go) prunes both, in one `RemoveEntityType` call, and
    asserts the `relation_type.upserted` events arrive `Zone_rel` before
    `apple_rel` — the sort's order, and the reverse of the index scan's.
    It was watched fail with `sort.Slice` removed (the events arrived
    `apple_rel` first) and pass with it restored. The sort's own comment
    now names both orders explicitly and points at the test.

29. **Two costs of the same lock, measured during this verification and
    written down rather than left for the next reader to rediscover.**
    Neither changes behaviour; both are now on `LockEndpointEntityTypes`
    (metamodel.sql) and `RemoveEntityType`'s own doc comment.
    - A transaction holding the endpoint's `FOR SHARE` parks
      `RemoveEntityType` against the same entity type with nothing to
      release it and no default timeout. Accepted: removals are rare and
      the alternative is the dangling id correction 22 closes, but the
      wait is unbounded and worth a deployment's own `lock_timeout` if it
      cares — which is the other half of finding 27.
    - `FOR SHARE` here conflicts with `UpsertEntityType`'s `FOR UPDATE`
      on its own row, so declaring a relation type's endpoint rule over
      an entity type now serialises against editing that entity type
      while the declaration's transaction holds the lock, and the
      reverse. Measured at 200 relation-type declarations sharing six
      endpoint types: 251ms on one worker, 140ms spread across eight —
      no measured throughput problem, since share locks do not conflict
      with each other, and no deadlock, since both call paths take
      `entity_types` before `relation_types`. Only a genuinely concurrent
      *edit* of the shared entity type pays this, and it was not
      previously written down anywhere. Commit `b4516d1`.

- [x] **Step 1: Add the queries**

Append to `internal/db/queries/metamodel.sql`:

```sql
-- name: UpsertRelationType :one
-- Guarded, audited and trigger-owned exactly as UpsertEntityType is; see
-- that statement's comment for the full argument. key is not in the SET
-- list, so the stored spelling stands and the returned row is what lets
-- Go refuse a respelling after the write.
INSERT INTO relation_types (project_id, key, label, description,
                            source_type_ids, target_type_ids, semantic_role, field_schema,
                            updated_by_user_id, updated_by_token_id)
VALUES (sqlc.arg('project_id')::uuid, sqlc.arg('key')::text, sqlc.arg('label')::text,
        sqlc.arg('description')::text, sqlc.arg('source_type_ids')::uuid[],
        sqlc.arg('target_type_ids')::uuid[], sqlc.narg('semantic_role')::text,
        sqlc.arg('field_schema')::jsonb,
        sqlc.narg('updated_by_user_id')::uuid, sqlc.narg('updated_by_token_id')::uuid)
ON CONFLICT (project_id, lower(key)) DO UPDATE
SET label               = excluded.label,
    description         = excluded.description,
    source_type_ids     = excluded.source_type_ids,
    target_type_ids     = excluded.target_type_ids,
    semantic_role       = excluded.semantic_role,
    field_schema        = excluded.field_schema,
    version             = relation_types.version + 1,
    updated_by_user_id  = excluded.updated_by_user_id,
    updated_by_token_id = excluded.updated_by_token_id
WHERE relation_types.version = sqlc.arg('expected_version')::integer
RETURNING *;

-- name: GetRelationTypeByKey :one
SELECT * FROM relation_types
WHERE project_id = sqlc.arg('project_id')::uuid AND lower(key) = lower(sqlc.arg('key')::text);

-- name: GetRelationTypeByKeyForUpdate :one
-- FOR UPDATE, for the reason correction 21 records: the lock is what
-- makes the reported current version the one this caller's own write
-- would have met, so "re-read and retry with 2" is advice that works.
SELECT * FROM relation_types
WHERE project_id = sqlc.arg('project_id')::uuid AND lower(key) = lower(sqlc.arg('key')::text)
FOR UPDATE;

-- name: GetRelationTypeByID :one
SELECT * FROM relation_types
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;

-- name: ListRelationTypes :many
-- Ordered by label then id: labels are not unique, and a label-only
-- order reshuffles ties between calls.
SELECT * FROM relation_types
WHERE project_id = sqlc.arg('project_id')::uuid ORDER BY label, id;

-- name: CountRelationsOfType :one
SELECT count(*) FROM relations
WHERE project_id = sqlc.arg('project_id')::uuid
  AND relation_type_id = sqlc.arg('relation_type_id')::uuid;

-- name: DeleteRelationsOfType :exec
DELETE FROM relations
WHERE project_id = sqlc.arg('project_id')::uuid
  AND relation_type_id = sqlc.arg('relation_type_id')::uuid;

-- name: DeleteRelationType :execrows
DELETE FROM relation_types
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;

-- name: UpsertRelation :one
-- No version guard, because relations carry no version column: an edge
-- is identified by (type, source, target) and re-writing its fields is
-- the operation, not a lost update. No updated_at either — the
-- set_updated_at trigger owns that column on all four tables.
INSERT INTO relations (project_id, relation_type_id, source_id, target_id, fields,
                       updated_by_user_id, updated_by_token_id)
VALUES (sqlc.arg('project_id')::uuid, sqlc.arg('relation_type_id')::uuid,
        sqlc.arg('source_id')::uuid, sqlc.arg('target_id')::uuid, sqlc.arg('fields')::jsonb,
        sqlc.narg('updated_by_user_id')::uuid, sqlc.narg('updated_by_token_id')::uuid)
ON CONFLICT (relation_type_id, source_id, target_id) DO UPDATE
SET fields              = excluded.fields,
    updated_by_user_id  = excluded.updated_by_user_id,
    updated_by_token_id = excluded.updated_by_token_id
RETURNING *;

-- name: ListRelations :many
SELECT r.* FROM relations r
WHERE r.project_id = sqlc.arg('project_id')::uuid
  AND (sqlc.narg('relation_type_id')::uuid IS NULL OR r.relation_type_id = sqlc.narg('relation_type_id')::uuid)
  AND (sqlc.narg('source_id')::uuid IS NULL OR r.source_id = sqlc.narg('source_id')::uuid)
  AND (sqlc.narg('target_id')::uuid IS NULL OR r.target_id = sqlc.narg('target_id')::uuid)
ORDER BY r.created_at, r.id
LIMIT sqlc.arg('limit')::int;

-- name: GetRelationByID :one
SELECT * FROM relations
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;

-- name: DeleteRelation :execrows
DELETE FROM relations
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;

-- name: ListEntitiesRelatedTo :many
SELECT e.* FROM entities e
JOIN relations r ON (r.source_id = e.id OR r.target_id = e.id)
WHERE e.project_id = sqlc.arg('project_id')::uuid
  AND r.relation_type_id = sqlc.arg('relation_type_id')::uuid
  AND ((sqlc.arg('direction')::text = 'incoming' AND r.target_id = sqlc.arg('anchor_id')::uuid AND e.id = r.source_id)
    OR (sqlc.arg('direction')::text = 'outgoing' AND r.source_id = sqlc.arg('anchor_id')::uuid AND e.id = r.target_id))
ORDER BY e.name, e.id;
```

Run: `make sqlc`

- [x] **Step 2: State this task's event gating in `events.go`**

Correction 19 again — stated here, not copied from a neighbouring call
site. Add to `internal/metamodel/events.go`:

```go
	// eventRelationTypeUpserted, eventRelationTypeRemoved,
	// eventRelationUpserted and eventRelationRemoved fire from this
	// task's writes once their transaction has committed.
	//
	// Gating as above — MinRole empty, HumanOnly false — and for the
	// same reasons: relation types are the game's own vocabulary and
	// relations are its content, both readable on demand by every member
	// including a token caller, and a seeding agent wiring edges is the
	// subscriber that most needs to know an endpoint rule moved under
	// it.
	eventRelationTypeUpserted = "relation_type.upserted"
	eventRelationTypeRemoved  = "relation_type.removed"
	eventRelationUpserted     = "relation.upserted"
	eventRelationRemoved      = "relation.removed"
```

with `relationEventMinRole` / `relationEventHumanOnly` beside the other
gating constants.

- [x] **Step 3: Write the failing test**

`internal/metamodel/relations_test.go`:

```go
package metamodel_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/testutil"
)

// Note newProject(t, pool): the helper in types_test.go takes the pool,
// not the service.

// seedWorld declares Quest and Zone types plus two entities of each.
func seedWorld(t *testing.T, svc *metamodel.Service, project uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	for _, spec := range []struct{ key, label, plural string }{
		{"quest", "Quest", "Quests"},
		{"zone", "Zone", "Zones"},
		{"class", "Class", "Classes"},
	} {
		if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
			Key: spec.key, Label: spec.label, LabelPlural: spec.plural,
		}); err != nil {
			t.Fatalf("seed type %s: %v", spec.key, err)
		}
	}
	for _, spec := range []struct{ typeKey, key, name string }{
		{"quest", "hogger", "Wanted: Hogger"},
		{"quest", "kobold-camp", "Kobold Camp"},
		{"zone", "elwynn", "Elwynn Forest"},
		{"class", "mage", "Mage"},
	} {
		if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: spec.typeKey, Key: spec.key, Name: spec.name,
		}); err != nil {
			t.Fatalf("seed entity %s: %v", spec.key, err)
		}
	}
}

func TestRelationEndpointsAreCheckedAgainstTheirType(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	quest, _ := svc.EntityTypeByKey(ctx, project, "quest")
	zone, _ := svc.EntityTypeByKey(ctx, project, "zone")

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "takes_place_in", Label: "takes place in",
		SourceTypeIDs: []uuid.UUID{quest.ID},
		TargetTypeIDs: []uuid.UUID{zone.ID},
		SemanticRole:  "spatial",
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}

	if _, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "takes_place_in",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Target:  metamodel.Ref{TypeKey: "zone", Key: "elwynn"},
	}); err != nil {
		t.Fatalf("valid relation: %v", err)
	}

	// A Class is not an allowed source for this relation type.
	_, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "takes_place_in",
		Source:  metamodel.Ref{TypeKey: "class", Key: "mage"},
		Target:  metamodel.Ref{TypeKey: "zone", Key: "elwynn"},
	})
	if !errors.Is(err, metamodel.ErrEndpointTypeMismatch) {
		t.Fatalf("err = %v, want ErrEndpointTypeMismatch", err)
	}
}

func TestRelationCarriesItsOwnFields(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	// A metroidvania door: the condition belongs to the edge, not to a room.
	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "connects_to", Label: "connects to", SemanticRole: "spatial",
		Schema: metamodel.Schema{{Key: "requires_ability", Type: metamodel.FieldText}},
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}

	rel, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "connects_to",
		Source:  metamodel.Ref{TypeKey: "zone", Key: "elwynn"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Fields:  map[string]any{"requires_ability": "mothwing_cloak"},
	})
	if err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}
	if len(rel.Fields) == 0 {
		t.Fatal("the relation stored no fields")
	}

	_, err = svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "connects_to",
		Source:  metamodel.Ref{TypeKey: "zone", Key: "elwynn"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Fields:  map[string]any{"requires_ability": 42},
	})
	if !errors.Is(err, metamodel.ErrSchemaViolation) {
		t.Fatalf("err = %v, want ErrSchemaViolation on a bad edge field", err)
	}
}

func TestRelationUpsertIsIdempotent(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{Key: "requires", Label: "requires"}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}
	in := metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
	}
	first, err := svc.UpsertRelation(ctx, project, in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := svc.UpsertRelation(ctx, project, in)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.ID != second.ID {
		t.Fatal("re-seeding an edge created a duplicate")
	}
}

func TestPrerequisiteCyclesAreAllowed(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires", SemanticRole: "prerequisite",
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}

	// A prerequisite cycle is a design mistake to surface later, not a write
	// error to block here.
	for _, pair := range [][2]string{{"hogger", "kobold-camp"}, {"kobold-camp", "hogger"}} {
		if _, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
			TypeKey: "requires",
			Source:  metamodel.Ref{TypeKey: "quest", Key: pair[0]},
			Target:  metamodel.Ref{TypeKey: "quest", Key: pair[1]},
		}); err != nil {
			t.Fatalf("edge %v: %v", pair, err)
		}
	}
}

// TestUpsertRelationTypeRefusesARespelledKeyAndAMalformedOne is Task 3's
// corrections 15, 23 and 25 for relation types: a respelling is refused
// after the write as well as before it, a key or label problem is
// invalid_input and not schema_violation, and both are reported in one
// pass.
func TestUpsertRelationTypeRefusesARespelledKeyAndAMalformedOne(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	_, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{Key: "takes place in"})
	if !errors.Is(err, metamodel.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	var invalid *metamodel.ValidationError
	if !errors.As(err, &invalid) || len(invalid.Fields) != 2 {
		t.Fatalf("want the key and the label reported together, got %v", err)
	}

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "Takes_Place_In", Label: "takes place in",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "takes_place_in", Label: "MINE", ExpectedVersion: ptrInt32(1),
	})
	requireFieldError(t, err, "key",
		`"takes_place_in" already exists here spelled "Takes_Place_In", and keys are matched `+
			`without regard to case: use "Takes_Place_In" to update it, or pick a key that `+
			`differs by more than capitalisation`)
}

// TestUpsertRelationTypeRejectsStaleVersion is correction 4 for relation
// types: the DO UPDATE is guarded, so a blind re-declaration of a type
// somebody else has edited is refused rather than landing.
func TestUpsertRelationTypeRejectsStaleVersion(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// No ExpectedVersion at all is the blind overwrite, and is refused
	// too.
	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "requires", Label: "renamed",
	}); !errors.Is(err, metamodel.ErrVersionConflict) {
		t.Fatalf("err = %v, want ErrVersionConflict", err)
	}
	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "requires", Label: "renamed", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("the matching version must be accepted: %v", err)
	}
}

func TestDeletingAnEntityDeletesItsRelations(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{Key: "requires", Label: "requires"}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}
	if _, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
	}); err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}

	hogger, _ := svc.EntityByKey(ctx, project, "quest", "hogger")
	if err := svc.RemoveEntity(ctx, project, hogger.ID); err != nil {
		t.Fatalf("RemoveEntity: %v", err)
	}

	rels, err := svc.ListRelations(ctx, project, metamodel.RelationFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if len(rels) != 0 {
		t.Fatalf("%d relations survived their entity", len(rels))
	}
}
```

- [x] **Step 4: Run the test to verify it fails**

Run: `go test ./internal/metamodel/ -run TestRelation -v`
Expected: FAIL, `undefined: metamodel.RelationTypeInput`.

- [x] **Step 5: Write the relation-type implementation**

`internal/metamodel/relation_types.go`:

```go
package metamodel

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// RelationTypeInput is an upsert request for a relation type. Empty
// endpoint lists mean "any type".
//
// ExpectedVersion carries the same meaning and the same insert-path
// caveat as EntityTypeInput.ExpectedVersion; see it.
type RelationTypeInput struct {
	Key             string
	Label           string
	Description     string
	SourceTypeIDs   []uuid.UUID
	TargetTypeIDs   []uuid.UUID
	SemanticRole    string
	Schema          Schema
	ExpectedVersion *int32
	Actor           Actor
}

// relationTypeEvent is the payload of the relation_type.* events:
// identity only, as a struct rather than a hand-built JSON string. See
// entityEvent.
type relationTypeEvent struct {
	ID  uuid.UUID `json:"id"`
	Key string    `json:"key"`
}

// UpsertRelationType creates or updates a relation type.
func (s *Service) UpsertRelationType(ctx context.Context, projectID uuid.UUID, in RelationTypeInput) (dbq.RelationType, error) {
	problems := rowKeyProblems("key", in.Key)
	// relation_types has label and description and no plural, colour or
	// icon; empty means "not set", which is what those columns are.
	problems = append(problems, checkDescriptors(in.Label, "", in.Description, "", "")...)
	if len(problems) > 0 {
		return dbq.RelationType{}, &ValidationError{Code: codeInvalidInput, Fields: problems}
	}
	if err := in.Schema.Check(); err != nil {
		return dbq.RelationType{}, err
	}
	raw, err := in.Schema.JSON()
	if err != nil {
		return dbq.RelationType{}, fmt.Errorf("encode field schema: %w", err)
	}

	expected := noVersion
	if in.ExpectedVersion != nil {
		expected = *in.ExpectedVersion
	}

	var row dbq.RelationType
	err = s.withTx(ctx, func(q *dbq.Queries) error {
		existing, err := q.GetRelationTypeByKeyForUpdate(ctx, dbq.GetRelationTypeByKeyForUpdateParams{
			ProjectID: projectID, Key: in.Key,
		})
		switch {
		case err == nil:
			// Spelling before version; see UpsertEntityType.
			if existing.Key != in.Key {
				return keyRespellingError("key", in.Key, existing.Key)
			}
			if in.ExpectedVersion == nil || *in.ExpectedVersion != existing.Version {
				return &VersionConflictError{Current: existing.Version}
			}
		case errors.Is(err, pgx.ErrNoRows):
		default:
			return fmt.Errorf("lookup relation type: %w", err)
		}

		params := dbq.UpsertRelationTypeParams{
			ProjectID:        projectID,
			Key:              in.Key,
			Label:            in.Label,
			Description:      in.Description,
			SourceTypeIds:    in.SourceTypeIDs,
			TargetTypeIds:    in.TargetTypeIDs,
			FieldSchema:      raw,
			ExpectedVersion:  expected,
			UpdatedByUserID:  in.Actor.UserID,
			UpdatedByTokenID: in.Actor.TokenID,
		}
		if in.SemanticRole != "" {
			role := in.SemanticRole
			params.SemanticRole = &role
		}

		row, err = q.UpsertRelationType(ctx, params)
		if errors.Is(err, pgx.ErrNoRows) {
			return conflictOnRelationTypeKey(ctx, q, projectID, in.Key)
		}
		if err != nil {
			if mapped := actorConstraintViolation(err); errors.Is(mapped, ErrActorNotInGame) {
				return mapped
			}
			return fmt.Errorf("upsert relation type: %w", err)
		}
		// Correction 15: the pre-read alone is not a refusal.
		if row.Key != in.Key {
			return keyRespellingError("key", in.Key, row.Key)
		}
		return nil
	})
	if err != nil {
		return dbq.RelationType{}, err
	}

	s.publish(projectID, eventRelationTypeUpserted, relationEventMinRole, relationEventHumanOnly,
		relationTypeEvent{ID: row.ID, Key: row.Key})
	return row, nil
}

// conflictOnRelationTypeKey names what stands in the way of a guarded
// upsert that matched no row: a respelling, or a moved version.
func conflictOnRelationTypeKey(ctx context.Context, q *dbq.Queries, projectID uuid.UUID, key string) error {
	row, err := q.GetRelationTypeByKey(ctx, dbq.GetRelationTypeByKeyParams{ProjectID: projectID, Key: key})
	if err != nil {
		return fmt.Errorf("re-read relation type after a failed upsert: %w", err)
	}
	if row.Key != key {
		return keyRespellingError("key", key, row.Key)
	}
	return &VersionConflictError{Current: row.Version}
}

// RelationTypeByKey loads one relation type.
func (s *Service) RelationTypeByKey(ctx context.Context, projectID uuid.UUID, key string) (dbq.RelationType, error) {
	row, err := s.q.GetRelationTypeByKey(ctx, dbq.GetRelationTypeByKeyParams{ProjectID: projectID, Key: key})
	if err != nil {
		return dbq.RelationType{}, notFound(err, "lookup relation type")
	}
	return row, nil
}

// ListRelationTypes returns every relation type of a project.
func (s *Service) ListRelationTypes(ctx context.Context, projectID uuid.UUID) ([]dbq.RelationType, error) {
	rows, err := s.q.ListRelationTypes(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list relation types: %w", err)
	}
	return rows, nil
}

// RemoveRelationType deletes a relation type, refusing while it is in use
// unless the caller says cascade.
//
// One transaction, as RemoveEntityType is: the count, the cascade delete
// and the delete itself are one decision, and a count taken outside the
// transaction is a count another writer can invalidate before the delete
// runs. The row is read first for its key, so relation_type.removed
// carries the identity it declares rather than an empty string
// (correction 22).
func (s *Service) RemoveRelationType(ctx context.Context, projectID, id uuid.UUID, cascade bool) error {
	var removedKey string
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		typ, err := q.GetRelationTypeByID(ctx, dbq.GetRelationTypeByIDParams{ProjectID: projectID, ID: id})
		if err != nil {
			return notFound(err, "lookup relation type")
		}
		removedKey = typ.Key

		if !cascade {
			count, err := q.CountRelationsOfType(ctx, dbq.CountRelationsOfTypeParams{
				ProjectID: projectID, RelationTypeID: id,
			})
			if err != nil {
				return fmt.Errorf("count relations: %w", err)
			}
			if count > 0 {
				return ErrInUse
			}
		} else if err := q.DeleteRelationsOfType(ctx, dbq.DeleteRelationsOfTypeParams{
			ProjectID: projectID, RelationTypeID: id,
		}); err != nil {
			return fmt.Errorf("delete relations: %w", err)
		}

		rows, err := q.DeleteRelationType(ctx, dbq.DeleteRelationTypeParams{ProjectID: projectID, ID: id})
		if err != nil {
			// relations.relation_type_id is ON DELETE RESTRICT, so an
			// edge written between the count and this statement raises
			// 23503. It is the same refusal.
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" {
				return ErrInUse
			}
			return fmt.Errorf("delete relation type: %w", err)
		}
		if rows == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return err
	}

	s.publish(projectID, eventRelationTypeRemoved, relationEventMinRole, relationEventHumanOnly,
		relationTypeEvent{ID: id, Key: removedKey})
	return nil
}
```

(`pgconn` joins the imports for that last branch, as it does in
`types.go`.)

- [x] **Step 6: Write the relation implementation**

`internal/metamodel/relations.go`:

```go
package metamodel

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// Ref addresses an entity the way an agent thinks of it: type key plus key.
type Ref struct {
	TypeKey string
	Key     string
}

// RelationInput is an upsert request for one edge.
//
// There is no ExpectedVersion: relations carry no version column. See
// this task's header for why that is a decision and not an omission.
type RelationInput struct {
	TypeKey string
	Source  Ref
	Target  Ref
	Fields  map[string]any
	Actor   Actor
}

// RelationFilter narrows a relation listing.
type RelationFilter struct {
	TypeKey  string
	SourceID *uuid.UUID
	TargetID *uuid.UUID
	Limit    int32
}

// relationEvent is the payload of the relation.* events: the identity of
// the edge — its id, its type key and both endpoint ids — and no fields.
// A struct, not a hand-built JSON string; see entityEvent.
type relationEvent struct {
	ID       uuid.UUID `json:"id"`
	TypeKey  string    `json:"type_key"`
	SourceID uuid.UUID `json:"source_id"`
	TargetID uuid.UUID `json:"target_id"`
}

// UpsertRelation creates or updates one edge, validating both endpoints
// against the relation type's allowed lists and the fields against its
// schema.
func (s *Service) UpsertRelation(ctx context.Context, projectID uuid.UUID, in RelationInput) (dbq.Relation, error) {
	var row dbq.Relation
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		var err error
		row, err = s.upsertRelationWith(ctx, q, projectID, in)
		return err
	})
	if err != nil {
		return dbq.Relation{}, err
	}
	s.publish(projectID, eventRelationUpserted, relationEventMinRole, relationEventHumanOnly,
		relationEvent{ID: row.ID, TypeKey: in.TypeKey, SourceID: row.SourceID, TargetID: row.TargetID})
	return row, nil
}

// upsertRelationWith does the work against any queries handle, so the
// single, partial and atomic paths share one implementation and the
// atomic path needs no second Service value to carry a transaction.
// Nothing in here publishes.
func (s *Service) upsertRelationWith(ctx context.Context, q *dbq.Queries, projectID uuid.UUID, in RelationInput) (dbq.Relation, error) {
	relType, err := q.GetRelationTypeByKey(ctx, dbq.GetRelationTypeByKeyParams{
		ProjectID: projectID, Key: in.TypeKey,
	})
	if err != nil {
		return dbq.Relation{}, notFound(err, "lookup relation type")
	}

	source, err := entityByKeyWith(ctx, q, projectID, in.Source)
	if err != nil {
		return dbq.Relation{}, fmt.Errorf("source %s/%s: %w", in.Source.TypeKey, in.Source.Key, err)
	}
	target, err := entityByKeyWith(ctx, q, projectID, in.Target)
	if err != nil {
		return dbq.Relation{}, fmt.Errorf("target %s/%s: %w", in.Target.TypeKey, in.Target.Key, err)
	}

	if !endpointAllowed(relType.SourceTypeIds, source.EntityTypeID) {
		return dbq.Relation{}, fmt.Errorf("%w: %s cannot be the source of %s",
			ErrEndpointTypeMismatch, in.Source.TypeKey, relType.Key)
	}
	if !endpointAllowed(relType.TargetTypeIds, target.EntityTypeID) {
		return dbq.Relation{}, fmt.Errorf("%w: %s cannot be the target of %s",
			ErrEndpointTypeMismatch, in.Target.TypeKey, relType.Key)
	}

	schema, err := ParseSchema(relType.FieldSchema)
	if err != nil {
		return dbq.Relation{}, err
	}
	values, err := schema.Validate(in.Fields)
	if err != nil {
		return dbq.Relation{}, err
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return dbq.Relation{}, fmt.Errorf("encode fields: %w", err)
	}

	row, err := q.UpsertRelation(ctx, dbq.UpsertRelationParams{
		ProjectID:        projectID,
		RelationTypeID:   relType.ID,
		SourceID:         source.ID,
		TargetID:         target.ID,
		Fields:           encoded,
		UpdatedByUserID:  in.Actor.UserID,
		UpdatedByTokenID: in.Actor.TokenID,
	})
	if err != nil {
		if mapped := actorConstraintViolation(err); errors.Is(mapped, ErrActorNotInGame) {
			return dbq.Relation{}, mapped
		}
		return dbq.Relation{}, fmt.Errorf("upsert relation: %w", err)
	}
	return row, nil
}

// entityByKeyWith resolves a Ref against a transaction's handle, so an
// atomic batch sees the entities it has just written.
func entityByKeyWith(ctx context.Context, q *dbq.Queries, projectID uuid.UUID, ref Ref) (dbq.Entity, error) {
	typ, err := q.GetEntityTypeByKey(ctx, dbq.GetEntityTypeByKeyParams{ProjectID: projectID, Key: ref.TypeKey})
	if err != nil {
		return dbq.Entity{}, notFound(err, "lookup entity type")
	}
	row, err := q.GetEntityByKey(ctx, dbq.GetEntityByKeyParams{
		ProjectID: projectID, EntityTypeID: typ.ID, Key: ref.Key,
	})
	if err != nil {
		return dbq.Entity{}, notFound(err, "lookup entity")
	}
	return row, nil
}

// UpsertRelations writes a batch of edges in the requested mode, with the
// same publication discipline as UpsertEntities: after the commit, one
// identity event per edge that landed, never a count.
func (s *Service) UpsertRelations(ctx context.Context, projectID uuid.UUID, items []RelationInput, mode BulkMode) (BulkResult, error) {
	var landed []dbq.Relation
	var result BulkResult

	if mode == BulkAtomic {
		err := s.withTx(ctx, func(q *dbq.Queries) error {
			landed = nil
			for i, in := range items {
				row, err := s.upsertRelationWith(ctx, q, projectID, in)
				if err != nil {
					return fmt.Errorf("item %d: %w", i, err)
				}
				landed = append(landed, row)
			}
			return nil
		})
		if err != nil {
			return BulkResult{}, err
		}
	} else {
		for i, in := range items {
			var row dbq.Relation
			err := s.withTx(ctx, func(q *dbq.Queries) error {
				var err error
				row, err = s.upsertRelationWith(ctx, q, projectID, in)
				return err
			})
			if err != nil {
				result.Failed = append(result.Failed,
					failureFor(i, in.Source.Key+"->"+in.Target.Key, err))
				continue
			}
			landed = append(landed, row)
		}
	}

	for i, row := range landed {
		s.publish(projectID, eventRelationUpserted, relationEventMinRole, relationEventHumanOnly,
			relationEvent{ID: row.ID, TypeKey: items[i].TypeKey,
				SourceID: row.SourceID, TargetID: row.TargetID})
	}
	return result, nil
}

// ListRelations returns edges matching a filter.
func (s *Service) ListRelations(ctx context.Context, projectID uuid.UUID, f RelationFilter) ([]dbq.Relation, error) {
	params := dbq.ListRelationsParams{ProjectID: projectID, Limit: f.Limit}
	if params.Limit <= 0 || params.Limit > 500 {
		params.Limit = 100
	}
	if f.TypeKey != "" {
		relType, err := s.RelationTypeByKey(ctx, projectID, f.TypeKey)
		if err != nil {
			return nil, err
		}
		params.RelationTypeID = &relType.ID
	}
	params.SourceID = f.SourceID
	params.TargetID = f.TargetID

	rows, err := s.q.ListRelations(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list relations: %w", err)
	}
	return rows, nil
}

// RemoveRelation deletes one edge, reading it first for the identity its
// event declares (correction 22).
func (s *Service) RemoveRelation(ctx context.Context, projectID, id uuid.UUID) error {
	var removed dbq.Relation
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		var err error
		removed, err = q.GetRelationByID(ctx, dbq.GetRelationByIDParams{ProjectID: projectID, ID: id})
		if err != nil {
			return notFound(err, "lookup relation")
		}
		rows, err := q.DeleteRelation(ctx, dbq.DeleteRelationParams{ProjectID: projectID, ID: id})
		if err != nil {
			return fmt.Errorf("delete relation: %w", err)
		}
		if rows == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.publish(projectID, eventRelationRemoved, relationEventMinRole, relationEventHumanOnly,
		relationEvent{ID: id, SourceID: removed.SourceID, TargetID: removed.TargetID})
	return nil
}

// endpointAllowed reports whether a type may sit at an endpoint. An empty
// allow-list means the relation type accepts anything.
func endpointAllowed(allowed []uuid.UUID, typeID uuid.UUID) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, id := range allowed {
		if id == typeID {
			return true
		}
	}
	return false
}
```

- [x] **Step 7: Run the tests to verify they pass**

Run: `go test ./internal/metamodel/ -v`
Expected: PASS, every test in the package.

- [x] **Step 8: Commit**

```bash
git add internal/db/queries internal/db/dbq internal/metamodel
git commit -m "feat: relation types and relations with endpoint validation"
```

**Corrections from Metamodel 12 (a fifth pass, made long after Tasks 7
and 8 shipped the two surfaces, against the one thing nine review rounds
never asked: whether anything could read back what this task had built a
writer for. It could not. One finding, closed on both surfaces and in the
domain; one adjacent hole recorded and deliberately not closed here.)**

30. **An edge's field values were write-only.** This task gave relation
    types a field schema and gave `UpsertRelation` the validation that
    enforces it, and `TestRelationCarriesItsOwnFields` proved a value
    goes in and lands in the row. Nothing ever proved one comes back
    out, because nothing could: `RelationOutput` carried no fields,
    there was no `relations.get`, and `relations.list` — the only tool
    that returns an edge at all — answered with identity and endpoints
    and nothing else. Task 9's end-to-end seeding wrote a validated
    `seat` and `season` onto 120 `drives` edges and could not read one
    of them back by any means short of SQL. It is the readme's own
    example of why typed edges exist — a door declaring which ability
    opens it — written and unreadable, and it blocked the views
    sub-project, which renders an edge's declared values.

    Every review round verified the write. The defect was found by using
    the product end to end, which is the general lesson: a feature is
    not shipped until a public reader returns what a public writer
    accepted, and a test that only writes proves half of it.

    **The fix is three pieces, and the shape was the decision.**

    - `RelationByEdge` (internal/metamodel/relations.go) reads one edge
      by the triple that identifies it — the relation type's key plus
      both endpoints as `(type_key, key)` refs — over a new
      `GetRelationByEdge`, which seeks the `relations_edge_key` unique
      index and so returns at most one row. It is `EntityByKey`'s
      counterpart, and the address is deliberately the one
      `relations.upsert` writes an edge under, not the endpoint *ids*
      `relations.list` filters on: an agent that has just written an
      edge holds the three strings and not the ids, and Task 9's own
      finding 8 measured what a by-id-only address costs. The three
      not_founds stay distinct — an unknown relation type, an unknown
      endpoint, and a real address holding no edge are three different
      mistakes.
    - `RelationOutput` grows `fields`, and `relations.list` grows the
      `verbose` flag that fills it. **Both surfaces, one core**: they
      share `relationsList`/`relationsGet` and one `relationOf`, which
      is what stops a later change from putting the fields back on one
      surface only.
    - `relations.get` on MCP and `GET /relations/one` on REST, with the
      address in the query string because an edge is named by five keys
      and this file's route rule keeps a row key out of a path segment
      it shares with anything — `/docs/one` already does this.

    **On `verbose`: the same default as `entities.list`, for a stronger
    version of the same reason.** That listing defaults its fields off
    arguing that five hundred rows with their values is the whole game
    back in one answer. A graph has more edges than nodes and the edge
    listing has the same 500-row cap, so a full verbose page of edges is
    strictly more of the game than the page that argument was written
    for, and an agent walking a graph almost always wants what each edge
    *joins* rather than what it carries. So: off by default on the
    listing, and always on for `relations.get`, exactly as
    `entities.get` always answers with an entity's fields — a caller
    naming one edge is asking for its content. A caller that wants one
    edge's values now pays one call instead of a filtered page, which is
    the second reason the `get` earned its place rather than the flag
    alone carrying the fix.

    **The relation type's schema was already discoverable**, and stays
    where it is: `relation_types.get` publishes `field_schema` in full,
    and both tool descriptions now point a caller reading an edge's
    values at it. `relation_types.list` remains slim, the same shape
    `types.list` has.

    **Task 9's pin was a test that passed for the wrong reason**, and
    that is worth recording separately. It asserted that a marshalled
    edge contains neither `season` nor `seat`, with a note to delete the
    case once an edge reports its fields — but it listed without
    `verbose`, so it stayed green through the entire fix. It is replaced
    by the read-back it was standing in for: the seeded values go in
    through `relations.upsert` and come out through `relations.get` and
    a verbose listing, on a driver whose `seat` was written explicitly
    rather than defaulted, so a reader that echoed the schema instead of
    the row would fail it.

31. **Recorded, not fixed: the write path can still be made to answer
    with a Postgres error.** `RelationByEdge` bounds all five of its
    keys with `rowKeyProblems` before any lookup, the guard
    `ListRelations` already runs on its own type key filter, because a
    by-key statement matches on `lower(key)` and `lower()` on a string
    carrying a NUL byte is SQLSTATE 22021 — the caller's bad argument
    leaving the domain as a server fault. `UpsertRelation` has no such
    guard: measured, a NUL byte in the type key answers `lookup relation
    type: ERROR: invalid byte sequence for encoding "UTF8" (SQLSTATE
    22021)`, and one in an endpoint key aborts the transaction and
    reports both that and a 25P02. Closing it changes a write path and
    the per-item failure codes a bulk batch reports, which a read defect
    is not the place for; `Ref`'s own doc comment now says the hole is
    there rather than implying it is closed.

**Does this change the case for Metamodel 10?** *(Metamodel 10 has since
landed; see its own section below. This paragraph is left as written,
because it is the argument that decided the shape of that task.)*
Metamodel 10 records
that relations have no `invalid` flag and no re-validation path, so
editing a relation type's field schema leaves existing edges unchecked
where entities get flagged. **It strengthens it, and changes its
character from cosmetic to substantive.** While an edge's values were
write-only the gap was invisible: nothing could show a caller a value
that no longer fits its schema, so "unflagged" and "unreadable" looked
the same from outside. Now `relations.get` and a verbose
`relations.list` hand a caller values that may not satisfy the schema
they claim to answer to, with nothing in the answer saying so — and the
views sub-project is about to render exactly those values beside the
relation type that declares them. An entity in the same state carries
`invalid: true`; an edge carries silence. It is still not this task's
change — it wants a migration for the column and a re-validation pass —
but the argument for doing it is no longer "for symmetry with
entities".

---

### Task 6: Listing, one-hop traversal, pagination and search

**Files:**
- Create: `internal/metamodel/list.go`, `internal/metamodel/search.go`
- Modify: `internal/db/queries/metamodel.sql`
- Test: `internal/metamodel/list_test.go`

**Refreshed after Task 3's review, 2026-09-02.** This task writes
nothing, so the corrections about guarded upserts, publication order and
event payloads do not reach it; what did reach it was `newProject(t,
svc)` (the helper takes the pool) and a malformed cursor reported with no
code at all, which correction 25 says is an `invalid_input` — it is the
caller's own argument, at a path, fixable in place.

**Implemented 2026-09-02, with the decisions and departures below.** The
code blocks printed under Steps 1 and 4 are a starting point, not what
shipped; where they differ, the reason is here.

1. **The cursor is a keyset on `(name, id)` carrying a fingerprint of
   its own filter.** The position half is the plan's. The fingerprint is
   not, and it closes a hole the position alone cannot see: every filter
   of this listing shares one sort order, so a cursor issued for the
   quest listing pages perfectly into the zone listing and answers with
   zones — a wrong answer to a call nobody meant to make, with nothing in
   it to say so. `fingerprintOf` digests the *resolved* filter (the
   entity type id, the invalid flag, and a traversal's relation type,
   anchor and direction), length-prefixed the way `foldedIdentity` is, so
   two filters whose parts divide differently cannot collide. A mismatch
   is an `invalid_input` at path `cursor` distinct from the malformed
   one, and the malformed check runs first — a truncated cursor reported
   as belonging to another listing sends a caller to inspect its filter,
   which is not where the problem is.

   **It is explicitly not a capability and not signed**, and `cursor`'s
   doc comment says so rather than letting a reader assume otherwise. It
   is base64 of JSON; a caller can rewrite the position and recompute the
   fingerprint from values it already holds. That buys nothing, because
   the position only ever becomes a `>` comparison inside a statement
   already filtered by the caller's own project id — the worst a forged
   cursor does is skip the caller's own rows.
   `TestAForgedCursorCannotReachAnotherGamesRows` feeds one listing a
   position lifted from another game and pins that the answer is still
   the caller's own rows.

2. **The keyset compares uuid to uuid, not `id::text`.** The plan's block
   compares `(name, id::text)` against two nargs while ordering by
   `(name, id)`, which makes the comparison's agreement with its own sort
   depend on the database's text collation. Measured before changing it:
   over 300,000 random pairs under this project's `en_US.utf8`, `a < b`
   and `a::text < b::text` never disagreed, so the cast was not a live
   bug — it was a correctness resting on a setting the deployment
   chooses, for nothing gained. The narg is now `after_id::uuid` and
   guards the clause alone; `after_name` is read only when it is set,
   because a row comparison against a NULL half yields NULL and would
   return an empty page rather than a refusal.

3. **`ListEntitiesRelatedTo` gained the same keyset, and the plan's
   truncate-in-Go was dropped.** The block under Step 4 slices the
   neighbour set to the limit and returns no cursor, which makes every
   neighbour past the limit unreachable with nothing in the answer to say
   so — the exact defect class this read surface is most exposed to. The
   keyset is on the query, so a traversal pages like every other listing.
   Its own `NextCursor` is fingerprinted to the traversal, so it cannot
   be carried over to the plain listing of the same entity type.

4. **`ListRelations` was paginated too** (finding recorded in the task
   brief as "disclose or fix"). It had a clamped `LIMIT` and no cursor,
   so a game with more edges than the cap could not be read past it. The
   keyset is on `(created_at, id)` — this listing's own sort order, and a
   value nothing edits, so its page boundary cannot move the way a
   renamed entity moves an entity listing's. `RelationFilter` gains
   `Cursor` and the method now returns a `RelationPage`; the
   `relations_project_idx` on `(project_id, created_at)` already serves
   the seek.

5. **An unrecognised traversal direction is refused, not defaulted.** The
   plan's `listRelated` silently rewrites anything that is not
   `"incoming"` to `"outgoing"`. Both that and passing it through — where
   it would match no arm of the query and return an empty page — answer a
   question the caller did not ask. It is an `invalid_input` at
   `related_to.direction` naming both spellings.

6. **There is no `"both"` direction, deliberately.** Two entities joined
   by one edge each way would appear twice in a listing whose rows are
   entities, and collapsing the pair discards the one thing the caller
   asked about. Two calls answer it, and the caller knows which half each
   row came from. The views sub-project, whose rows are edges, is where an
   undirected walk belongs.

7. **A self-loop appears once, and it is the anchor itself.** The join's
   two arms are written so that exactly one can hold for a given
   (entity, edge) pair, so the anchor comes back in its own neighbour
   list once under either direction. Nothing filters it out: the edge
   exists and it does point there, and the analysis sub-project is where
   a self-reference is reported as a modelling problem.
   `TestASelfLoopAppearsOnceInItsOwnNeighbourList` pins both directions.

8. **An edge whose relation type was deleted contributes nothing,
   because there is no such edge.** `relations.relation_type_id` is
   `ON DELETE RESTRICT` and `RemoveRelationType(cascade)` deletes the
   edges first, so the two ways a type goes away either refuse or take
   the edges with them. Pinned by
   `TestAnEdgeCannotOutliveItsRelationType` rather than asserted, and
   nothing in the query filters for a case that cannot arise.

9. **`TypeKey` and `Invalid` apply to a traversal as well as to a plain
   listing.** The plan's `listRelated` ignores both — it takes only the
   limit — so "which quests happen in Elwynn" would have answered with
   the classes too, silently. Both are nargs on
   `ListEntitiesRelatedTo` now, and each half of the claim has a test
   whose failure was watched with the corresponding clause removed.

10. **Search refuses a query with no word in it.**
    `plainto_tsquery('simple', …)` turns `""`, `"   "` and `"..."` into
    an empty tsquery, which matches nothing, so every one of them came
    back as a clean empty answer indistinguishable from "this game has no
    such content" — which an agent acts on by seeding a duplicate. It is
    an `invalid_input` at path `query`. The test is "does the query hold
    a letter or a digit", and that is tied to the `simple` configuration,
    which has no stopword list: under `english` a query of pure stopwords
    would pass this check and still match nothing, so whoever changes the
    configuration changes `checkSearchQuery` with it.

11. **Search ranks by `ts_rank` over an unweighted vector, and says so.**
    A row whose *name* is the query does **not** outrank one that merely
    mentions it in a paragraph, because `entities.search` is one flat
    vector built by `UpsertEntity`. Fixing that means `setweight` in a
    *write* statement plus a rewrite of every stored row, which this
    read-only task declined to take on its own; **Task 7 owns the call**
    when it decides what the search tool promises.
    `TestSearchRanksTheStrongerMatchFirst` pins the ranking as it is, and
    is the test that would change. Ties break by name then id, so two
    identical calls answer identically.

12. **Search is not paginated, and the 128 KiB index bound is stated
    where a caller reads it.** The answer is the top `limit` rows by
    rank; a rank is not a position a caller can resume from, and the
    recovery for too many hits is a narrower query. `searchTextLimit`
    means the tail of a very long field is stored and re-read whole but
    is not findable — `TestOnlyTheIndexedHeadOfALongFieldIsSearchable`
    pins both halves, and `Search`'s doc comment says it.

13. **An over-large limit clamps to the cap on every listing**, matching
    `ListRelations`. The plan's `ListEntities` and `Search` blocks fold
    both "nothing asked for" and "too much" onto the default, which is
    the bug `relationPageSize` already fixed once: `Limit: 501` returning
    strictly fewer rows than `Limit: 500`, silently. `pageSize` is now
    one shared helper and `relationPageSize` calls it, so the three
    listings cannot drift apart. The search test seeds sixty rows —
    above the default of fifty, below the cap of two hundred — because
    with fewer rows than the default in the game, clamping and folding
    return the same answer and a test built that way passes either way.

14. **What the SQL header claims about project filters was checked, not
    assumed.** `ListEntitiesPage` and `SearchEntities` carry
    load-bearing project filters — their positions and query text name no
    parent whose composite key could scope them — and dropping either was
    watched to fail its scoping test. `ListEntitiesRelatedTo`'s two
    filters are *not* the mechanism: its anchor and relation type are
    resolved from keys inside the project by the Go caller, and the
    composite keys put an edge, its type and both endpoints in one game,
    so removing both filters was watched to leave the whole traversal
    suite green — including its own scoping test. The comment says
    exactly that rather than claiming an isolation it does not provide.

15. **`ListEntitiesOfType` stays dropped**, as the plan says: still no
    caller.

**Corrections from the review of Task 6** (a second pass over the landed
package, made against a real database rather than by reading. Five
findings, three of them medium, plus what the later sub-projects inherit.
The theme is that every one of them is a *wrong answer with nothing in it
that says so*, which is the failure mode this whole read surface is most
exposed to: a cursor that pages the wrong game, a caller's own argument
reported as a server fault, and a sort agreement defended by a comment.)

16. **The cursor fingerprint digested the filter but not the game, so a
    cursor paged silently into another game's listing.** Decision 1 above
    introduced `fingerprintOf` to close exactly this hole — a position is
    valid in any listing that shares the sort order, so it has to be
    bound to the listing it came from — and then stopped one step short:
    the parts were `("entities", typePart, invalidPart)`, and an
    *unfiltered* listing has no type part at all, so two games' unfiltered
    listings hashed to one fingerprint. `ListRelations` was worse: its
    parts are the type and the two endpoints, all optional, and its sort
    key is `created_at`, which is not game-specific in any way.

    Proved live before the fix. Game A paged to row 8; A's cursor handed
    to game B's listing was accepted and returned 2 of B's 10 rows,
    hiding the first 8. On relations, with the two games seeded in
    sequence so B's edges are all newer than A's, B's cursor fed to A
    returned **0 of A's 2 edges**, with no error and no cursor — which an
    agent reads as "this game has no edges" and acts on.

    **Say what this was accurately: a wrong answer, not a leak.** No row
    of another game was ever returned, and could not be; the listings'
    project filters see to that, and
    `TestAForgedCursorCannotReachAnotherGamesRows` already pinned it. What
    crossed the boundary was the *position*, which is why nothing failed
    and why it is worth a correction rather than a note.

    **The fix is the project id as the first part of all three
    `fingerprintOf` calls** (`list.go`'s two, `relations.go`'s one).
    `TestACursorFromAnotherGameIsRefused` pins each of the three
    listings, and a fourth subtest pins that a game's own cursor still
    pages it, since narrowing a fingerprint could as easily have
    invalidated every cursor as the wrong ones. Every within-game
    mismatch decision 1 already caught is still caught —
    `TestACursorIssuedForAnotherListingIsRefused` and
    `TestARelationCursorBelongsToItsOwnFilter` are untouched and green.

    One honest note on what the new test kills: the traversal subtest
    passed *before* the fix, because a traversal's fingerprint already
    carried the anchor entity id and the relation type id, both of which
    are per-game UUIDs. The traversal was never exposed. It carries the
    project id anyway, because the next reader adding a listing copies
    the shape it sees, and a rule with an exception in it is not a rule.

17. **The search query was unbounded and reported the caller's own
    argument as an internal error.** `checkSearchQuery` asked one
    question — is there a letter or a digit — and decision 10 above
    records why. It asked nothing about size or about what the bytes
    were. Measured live through `Search`, before the fix:

    | query | result |
    |---|---|
    | `"hogger"`, a NUL, `"gnoll"` | `ERROR: invalid byte sequence for encoding "UTF8": 0x00 (SQLSTATE 22021)` |
    | 126 KiB of distinct words | answered in 0.36 s |
    | 263 KiB | `ERROR: stack depth limit exceeded (SQLSTATE 54001)` after 1.4 s |
    | 536 KiB | the same, after 5.5 s |

    Both failures were untyped, so both reached an agent as
    `internal_error` — "the server is broken" — over a value the agent
    itself supplied. A NUL is six characters of JSON escape, so an agent
    assembling a query from a file produces one by accident. And the
    growth is quadratic (four times the text for fifteen times the work),
    so a handful of concurrent calls carrying text nobody vetted is a
    denial of service the server does to itself. Correction 25's rule
    applies without qualification: a caller's own argument, at a path, is
    `invalid_input`.

    The doc had spent a paragraph on the 128 KiB *index* bound (decision
    12) while the *query* side was unbounded and undisclosed, which is
    the worse of the two ways to be wrong about a limit — a caller
    reading only the disclosed one concludes the other does not exist.

    **The fix bounds the query at `MaxSearchQuery`, 4 KiB, and refuses
    any control character**, both as `invalid_input` at path `query`,
    both before a byte reaches Postgres. The bound is *exported* because
    the point of a bound is that a caller can read it: Task 7 states it
    in the search tool's description beside the row bound. Control
    characters are refused rather than stripped — including newline and
    tab — because deleting part of a caller's query answers a question it
    did not ask; `simple` can make a lexeme of none of them, and a query
    carrying one is a caller assembling text wrongly.
    `TestASearchQueryIsBoundedAndReportedAsTheCallersOwnArgument` runs
    the payloads above and pins that a query of exactly the bound still
    searches while one byte more does not.

18. **The keyset's `ORDER BY` tiebreak was pinned by nothing.** Dropping
    `id` from `ORDER BY name, id` on `ListEntitiesPage` and from
    `ORDER BY e.name, e.id` on `ListEntitiesRelatedTo`, regenerating and
    running the whole 229-test suite left it entirely green. Every test
    in it seeded distinct names, so the tiebreak never mattered.

    The code was correct; what was missing was the pin, and the SQL
    comment is what makes that a finding rather than an omission. It
    argues at length that a keyset whose comparison disagrees with its
    own `ORDER BY` skips or repeats rows and says nothing about it — and
    then defends only the collation half of that agreement (decision 2),
    with a 300,000-pair measurement, while the half a one-line edit could
    break went undefended. Duplicate names are ordinary in game content:
    "Kobold", "Bandit", "Wolf" across a dozen zones.

    `TestPagingIsStableWhenEveryRowSharesOneName` seeds ten entities
    sharing one name and pages by three, over both listings. Under the
    mutation it returns 5 distinct rows of 10 with 3 of them twice; with
    `id` restored it walks all ten exactly once. **Both listings were
    proved red under the mutation and green with it reverted**, and the
    SQL comment now says which half of the agreement each argument
    defends.

19. **The cursor's contract was documented where no caller could read
    it.** The best writing in this task was on the *unexported* `cursor`
    type. `go doc metamodel EntityFilter` ended with "see cursor";
    `go doc metamodel cursor` reports no such symbol. `ListEntities` and
    `RelationFilter.Cursor` pointed at it too — three dangling
    references, and a caller left holding the headline "a page is a
    position, not a snapshot" with none of the content behind it.

    The content matters, because the non-snapshot behaviour is real and
    unreported: a row not yet read, renamed between two pages to sort
    before the cursor's position, is never returned by that listing
    again, however many pages remain. An agent walking a game it is also
    editing can finish the walk having never seen a row that existed
    throughout.

    **The substance moved to `EntityPage`**, which is where every cursor
    in the package comes from and which `go doc metamodel EntityPage`
    resolves: what a position buys, the three ways a mutable sort key
    lets a row be missed or repeated, that a cursor belongs to its game
    and filter, and that it is neither signed nor a capability.
    `EntityFilter.Cursor`, `RelationFilter.Cursor`, `RelationPage`,
    `ListEntities` and `ListRelations` all now point there;
    `RelationPage` adds the one difference in its favour, that nothing
    edits `created_at`. `cursor` keeps only the shape of its three
    fields. `TestARenamedRowCanMoveBehindTheReader` pins the behaviour
    the doc now promises, which nothing pinned while it was documented
    out of reach.

20. **Three smaller ones.**

    - **"The planner evaluates it once" was false.** `SearchEntities`
      builds `plainto_tsquery` twice, in the projection and in the
      predicate, because a `WHERE` cannot name an output alias, and the
      comment claimed the planner folded them. Measured, 200
      non-constant evaluations: 0.63 ms each written once against
      1.44 ms written twice at 4 KiB, and 4.09 ms against 8.70 ms at
      15 KiB. There is no common-subexpression elimination; the second
      build costs what the first did. The claim is corrected and the
      measurement recorded. It stays written twice: with correction 17's
      4 KiB bound the doubling is worth about 0.8 ms on the largest query
      this surface accepts, and the obvious single-build rewrite
      (`WITH q AS MATERIALIZED (...)` joined in) was measured too — it
      keeps the Bitmap Index Scan on `entities_search_idx`, but the
      tsquery stops being a constant the planner can see, and the row
      estimate for the match went from 5 (exact) to 100 (a default guess)
      on the same data. Trading a correct selectivity estimate on every
      search for 0.8 ms on the worst accepted query is the wrong way
      round; whoever raises `MaxSearchQuery` should measure both again.

    - **`entities` had no index serving the listing's sort**, only
      `entities_key_key` and the two GINs. `relations` got
      `relations_project_idx` in 0004 and its comment rightly says the
      cursor's position "seeks rather than scans"; the entity keyset,
      which is read far more often, sorted the whole game on every page.
      **The index was added**, as `0005_entity_listing_index.sql`:
      `entities_listing_idx` on `(project_id, name, id)`, the filter and
      the whole sort key. Measured over 50,000 entities across 20 games,
      paging the middle game 50 rows at a time: without it, a Bitmap
      Index Scan over the game's 2,499 rows into a top-N sort, 75 shared
      buffers a page, 200 pages in 235 ms; with it, an Index Scan with
      the cursor's `(name, id)` folded into the Index Cond, 50 rows read,
      52 buffers a page, 200 pages in 49 ms.

      **What it costs**, since an index added without its price is half a
      decision: 3,320 kB over those 50,000 rows, about 68 bytes a row,
      and one more b-tree entry on every insert *and on every update that
      moves `name`* — which is every rename, so this is a cost a content
      editor pays and not only a bulk import. Over 2,000 single-row
      inserts it did not rise above the round trip (580 ms with against
      609 ms without), which is to say the cost is real but below what
      this workload can measure. The migration carries all of it.

    - **A stale forward reference.** `relationPageSize` still said "Task
      6 owns the cursor, and when it arrives this stays the per-page
      bound", sixty lines below the cursor that had arrived. Both that
      sentence and the const block's "there is no cursor here" now
      describe what is there.

    One thing changed that the review did not ask for:
    `TestMigrateUpDownUp` counted its `migrateDown` calls with a
    hand-written four and a comment saying a fifth migration needs a
    fifth call. Adding 0005 duly broke it, in a way that reads as a
    broken rollback and is really a stale literal. It now takes the count
    from the embedded migrations directory.

**What the later sub-projects inherit from Task 6** (five things analysis
and the views engine will copy, recorded here because that is where they
will look for them, and each of them is a decision made once here that
gets three copies if it is not.)

21. **Fix the fingerprint before there are three copies of it.**
    Correction 16 is done, and the reason it is recorded as an
    inheritance rather than only as a bug is that a view query's cursor
    is *far* likelier to cross games than an entity listing's: a saved
    view is a named, shared, re-run thing, and its position is the kind
    of value that ends up in a URL, a config file or an agent's memory of
    "where I was". Whatever the views engine issues as a cursor carries
    the project id in its fingerprint, in the first position, and it
    inherits `fingerprintOf` rather than growing a second digest with its
    own idea of which parts matter.

22. **The `ORDER BY`/keyset agreement is a convention, not a mechanism.**
    Correction 18 pinned the two listings that exist. Nothing stops a
    views query being written with a keyset whose comparison and sort
    disagree — no type, no test, no generated code; the only defence is
    that someone reads the comment on the query above. That held for two
    listings written in one sitting by one author. It will not hold for a
    query language that emits SQL. **Before the third listing is written,
    decide whether a shared helper should emit both halves from one
    declaration of the sort key**, so that they cannot disagree, and
    record the decision either way. If the answer is no, the reason has
    to be better than "the comment is clear".

23. **The traversal's join is right for one hop and the wrong shape to
    recurse with.** `ListEntitiesRelatedTo`'s two arms are exclusive only
    because `direction` is a scalar constant, which is what makes a
    self-loop produce one row rather than two (decision 7). Hand that
    join a `both`, or wrap it in a recursive CTE, and the property
    evaporates: the same edge satisfies both arms and every walk doubles.
    A transitive walk needs the edge-row shape the design doc assigns to
    views — rows that *are* edges, carrying their direction — not this
    join generalised. Whoever writes the walk should read decision 6's
    argument for why there is no `"both"` here before deciding what a
    view's rows are.

24. **Unbounded caller text reaching Postgres is finding 17 today and the
    D2 query parser tomorrow.** The rule is established here, so that it
    is inherited rather than rediscovered: **a caller's own argument is
    bounded before it reaches the database, and a failure over it is
    `invalid_input` at that argument's path, never an untyped error.** A
    view query string is exactly the same shape of input as a search
    query — arbitrary caller text, of arbitrary length, handed to a
    parser — and it will have the same two failure modes, a byte the
    parser cannot accept and a length the parser is quadratic in. The
    views sub-project states its own `MaxQuery`, exports it, and states
    it in the tool description, the way `MaxSearchQuery` does.

25. **Search ranking is deferred to Task 7, and Task 7 must not ship
    without settling it.** Decision 11 records that `ts_rank` over the
    unweighted vector means **a row whose name *is* the query does not
    outrank a row that merely mentions it in a paragraph**, and that the
    fix is `setweight` in `UpsertEntity` plus a rewrite of every stored
    row, since existing vectors carry no weights at all. That deferral
    currently lives in a Go comment and in decision 11, where a Task 7
    implementer reading only their own section will not meet it.
    **Task 7's exit condition therefore includes it explicitly** (see
    Task 7's requirement list): the search tool does not ship until the
    question "does a name match outrank a body match" has an answer that
    is either implemented or written down as a promise the tool
    description makes. `TestSearchRanksTheStrongerMatchFirst` is the test
    that has to change when it is implemented, and it says so.

**Re-review, 2026-09-02.** Correction 20's index and the fixes
`checkSearchQuery` already applied (the NUL and length refusals from
correction 17) were verified sound and stayed as they were. What the
re-review found is what was said *about* them, and one place the same
rule those fixes state was never applied at all — five findings, closed
below.

26. **`checkSearchQuery`'s UTF-8 check refused a NUL and let every other
    invalid byte through.** `strings.IndexFunc(query, unicode.IsControl)`
    decodes an invalid byte as U+FFFD, the replacement character, which
    is not a control character, so `unicode.IsControl` never sees it and
    the byte reaches `plainto_tsquery` unexamined. Proved live through
    `Search`: `"hello \xed\xa0\x80 world"` (an unpaired UTF-16 surrogate
    encoded as if it were valid UTF-8) and `"hello \xff world"` (a lone
    continuation byte, not valid UTF-8 under any reading) both returned
    `ERROR: invalid byte sequence for encoding "UTF8" (SQLSTATE 22021)`,
    untyped, over the caller's own argument — the identical failure
    correction 17 was written to close, just not closed for this class of
    byte.

    The doc comment compounded it, claiming "NUL is the one Postgres
    itself rejects." False: Postgres rejects every invalid UTF-8 sequence
    with SQLSTATE 22021, not only a NUL, and the claim that both refusals
    happen "before a byte reaches Postgres" did not hold for a
    non-NUL invalid sequence either. `checkSearchQuery` now calls
    `utf8.ValidString` before the control-character scan, and the two
    comments — on `checkSearchQuery` and on `Search` itself — say what
    Postgres actually refuses.
    `TestASearchQueryIsBoundedAndReportedAsTheCallersOwnArgument` gained
    the two live cases above; **proved red** by reverting the
    `utf8.ValidString` check (keeping the import to isolate the one
    change): both new subtests failed with exactly the SQLSTATE 22021
    quoted above, reaching the caller as `internal_error`, and the
    existing five subtests in the same test stayed green, confirming the
    baseline ran before the mutation (`go test -v`, every subtest name
    printed).

27. **The refusal search.go states was missing entirely on the write
    path — the third instance of a project pattern.** Correction 17 gave
    `Search` a rule for caller-supplied text; `internal/projects/`'s
    `validateName` had its own, older, version of the same rule; and
    every entity name, type label, plural, description, and text or
    longtext field value in this package refused nothing. Proved live
    through `UpsertEntity`: `name = "Hog\x00ger"` returned `ERROR:
    invalid byte sequence for encoding "UTF8" (SQLSTATE 22021)`, untyped;
    a `longtext` field carrying the same NUL returned a *different*
    untyped error, `ERROR: unsupported Unicode escape sequence (SQLSTATE
    22P05)`, because jsonb encodes a NUL as the six-character escape
    `\u0000` and Postgres's json input routine refuses that
    escape outright; `name = "Hog\xffger"` (invalid UTF-8, no NUL involved)
    returned SQLSTATE 22021 again; and `name = "Hog\nger"` was accepted
    and stored. All three failures were fully reachable through the
    intended agent surface — plain JSON, no crafted bytes — and none of
    them carried a field path or a code an agent's retry logic could act
    on.

    **The one rule settled**, in `descriptors.go`'s new `textProblem`:
    every caller-supplied string this package stores or renders — an
    entity or type `name`, a `label`, a `label_plural`, a `description`,
    a `text` or `longtext` field value, an element of a `list<text>` —
    must be valid UTF-8 and hold no control character, refused as the
    caller's own argument (`invalid_input` at the row's own path for a
    name or descriptor, `schema_violation` at `fields.<key>` for a field
    value, matching the split correction 25 of Task 4's re-review
    already draws between the two). Colour and icon needed no change:
    their existing patterns already admit only a fixed ASCII alphabet, so
    invalid UTF-8 and every control character were already excluded by
    construction. Keys needed no change for the same reason —
    `rowKeyPattern` is ASCII-only.

    **What was decided about newline and tab, stated rather than left to
    fall out of the implementation:** `textProblem` takes
    `allowNewlineAndTab`. A `longtext` field value and a row's
    `description` are free-form prose — a lore document, a designer's
    notes — and a newline in either is the caller's own paragraph break,
    not malformed input, so both keep newline and tab and refuse every
    other control character. Every other text this rule touches — `name`,
    `label`, `label_plural`, a `text` field value, one element of a
    `list<text>` — is rendered as a single line (a page title, a game
    picker, a listing row, a tag chip), so a newline or a tab there is
    refused exactly like any other control character, matching
    `validateName`'s existing treatment of a project name and
    `checkSearchQuery`'s treatment of a query.

    Relation and relation-type paths needed no separate check:
    `RelationInput` carries no descriptive column at all — decision on
    that already recorded in `relations.go` — so its only caller-supplied
    prose is field values, which go through the same `Schema.Validate`
    and therefore the same `textProblem` entity values do; relation
    *types* call `checkDescriptors` directly, so they inherit the fix
    with no separate wiring.

    `TestUpsertEntityRefusesUnprintableTextBeforeItReachesPostgres` pins
    the table above plus the newline asymmetry. **Proved red** by
    reverting the `textProblem` calls in `checkName` and in `coerce`'s
    `FieldText`/`FieldLongText` case (keeping the unused import removed
    so the mutation isolates the check, not a compile error): all three
    unprintable-text subtests failed, reproducing the exact live errors
    above byte for byte (SQLSTATE 22021 twice, SQLSTATE 22P05 once), and
    the newline-in-name assertion failed too (`err = <nil>`, the row was
    accepted). `go test -v` confirmed the baseline ran, subtest by
    subtest, before the mutation. Restoring the calls returned the suite
    to green; `make check` is clean with the fix in.

28. **The migration's justification named three readers and served
    one.** `0005_entity_listing_index.sql` said the index served "every
    listing, every traversal's far end, and the game home page."
    Measured on the same 50,000-row, 20-game dataset correction 20
    already used: a type-filtered listing — `ListEntities{TypeKey: ...}`,
    the common agent call — still plans through `entities_key_key` with a
    top-N sort (1,675 buffers, 1.93 ms); `entities_listing_idx` is absent
    from that plan, because `entities_key_key`'s
    `(project_id, entity_type_id, key)` is the better match for a
    type-filtered predicate. The one-hop traversal
    (`ListEntitiesRelatedTo`) plans as a Nested Loop into a top-N sort
    (10,079 buffers, 5.87 ms) and never touches this index either; its
    far end is served by `entities_id_project_id_key`, an index 0004
    added for a different join, so the migration's comment was taking
    credit for an existing win. **What the index actually buys is the
    *unfiltered* entity listing** — no type, no invalid filter — which is
    the shape the game home page and a bulk export use; the 235 ms →
    49 ms, 200-page measurement already in the migration is that reader's
    number and needed no re-measuring, only re-labelling.

    More substantively: **the traversal still sorts its whole
    neighbourhood on every page and cannot seek to its cursor**, which is
    the exact defect this migration exists to fix, left open on the one
    path the original comment claimed it had already covered. The
    migration's comment now names one reader instead of three, states the
    two it does not help and why, and says the traversal's gap stays
    open. `TestMigrateUpDownUp` and the package suite stayed green
    against the corrected comment — nothing about the index's DDL
    changed, only what is claimed about it. See correction 23 for the
    traversal's other open issue (the join shape does not generalise to a
    recursive walk); the seek gap here is a second, independent reason a
    views query cannot inherit `ListEntitiesRelatedTo` unchanged, and it
    is added to what correction 24 already tells the views sub-project to
    expect from this listing.

29. **Two comments recorded contradictory measurements of the same class
    of experiment, presented as if comparable.** `search.go`'s
    `MaxSearchQuery` comment: 126 KiB answered in 0.36 s, 263 KiB failed
    after 1.4 s, 536 KiB failed after 5.5 s, all with *distinct* words.
    `search_test.go`'s bound test: 146 KiB failed after 2.1 s, 292 KiB
    after 8.5 s, 585 KiB after 33.5 s, all with *one word repeated*.
    Roughly six times apart on the number that justifies the bound, both
    read as measured on this project's own Postgres, and the direction
    runs backwards from what the framing implies — a repeated word reads
    as the easy case and measured six times worse. Neither comment said
    it was timing a different workload from the other.

    Reconciling them by re-running one workload under the other's
    wording would manufacture a third number nobody asked for; the
    honest fix is to say what each already was. Both comments now name
    their own workload explicitly, say in as many words that they are
    separate runs and not a comparison, and `search.go`'s comment points
    at `search_test.go`'s for the second data point rather than letting a
    reader find two unattributed measurements and assume they were
    meant to agree.

30. **Recorded, not fixed**, matching how the previous round's own
    informational findings were handled:

    - `unicode.IsControl` covers Unicode category Cc only, so `U+200B`
      (zero-width space) and `U+2028` (line separator) pass every check
      in this file and in `checkSearchQuery` alike. Harmless today, and
      "one line of text" is a looser promise than what the check
      enforces — recorded so a future tightening starts here instead of
      rediscovering the gap.
    - `TestMigrateUpDownUp` still asserts only that goose *reported* each
      `Down` succeeded, not that each `Down` undid its `Up`; a `Down`
      that silently did nothing would pass. Pre-existing, not introduced
      by 0005, and 0005's own `Down` (`DROP INDEX entities_listing_idx`)
      was checked by hand this round rather than by the suite.

- [x] **Step 1: Add the listing and search queries**

`ListEntitiesPage` is printed in Task 4's Step 1 but was **not** added
there: nothing in Task 4 calls it, and a query landing a task ahead of
its only caller ships generated code no test exercises and pre-commits
this task's keyset shape sight unseen. It is this task's query, so add
it here, and settle its cursor while doing so — the block below compares
`(name, id::text)` against two separate nargs (`after_name`, `after`),
which is a shape this task's own cursor tests have to justify.
`ListEntitiesOfType`, also printed in Task 4, is called by no task in
this plan and was dropped; add it if and when a caller appears.

`ListEntitiesRelatedTo` is printed in Task 5's Step 1 and was **not**
added there either, for the same reason: nothing in Task 5 calls it, and
its only caller is this task's `RelatedTo`. It is reprinted below.

Append to `internal/db/queries/metamodel.sql`:

```sql
-- name: ListEntitiesRelatedTo :many
SELECT e.* FROM entities e
JOIN relations r ON (r.source_id = e.id OR r.target_id = e.id)
WHERE e.project_id = sqlc.arg('project_id')::uuid
  AND r.relation_type_id = sqlc.arg('relation_type_id')::uuid
  AND ((sqlc.arg('direction')::text = 'incoming' AND r.target_id = sqlc.arg('anchor_id')::uuid AND e.id = r.source_id)
    OR (sqlc.arg('direction')::text = 'outgoing' AND r.source_id = sqlc.arg('anchor_id')::uuid AND e.id = r.target_id))
ORDER BY e.name, e.id;

-- name: ListEntitiesPage :many
SELECT * FROM entities
WHERE project_id = sqlc.arg('project_id')::uuid
  AND (sqlc.narg('entity_type_id')::uuid IS NULL OR entity_type_id = sqlc.narg('entity_type_id')::uuid)
  AND (sqlc.narg('invalid')::boolean IS NULL OR invalid = sqlc.narg('invalid')::boolean)
  AND (sqlc.narg('after')::text IS NULL OR (name, id::text) > (sqlc.narg('after_name')::text, sqlc.narg('after')::text))
ORDER BY name, id
LIMIT sqlc.arg('limit')::int;

-- name: SearchEntities :many
SELECT e.*, ts_rank(e.search, plainto_tsquery('simple', sqlc.arg('query')::text)) AS rank
FROM entities e
WHERE e.project_id = sqlc.arg('project_id')::uuid
  AND (sqlc.narg('entity_type_id')::uuid IS NULL OR e.entity_type_id = sqlc.narg('entity_type_id')::uuid)
  AND e.search @@ plainto_tsquery('simple', sqlc.arg('query')::text)
ORDER BY rank DESC, e.name
LIMIT sqlc.arg('limit')::int;
```

Run: `make sqlc`

- [x] **Step 2: Write the failing test**

`internal/metamodel/list_test.go`:

```go
package metamodel_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/testutil"
)

func TestListPaginatesWithACursor(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	for i := 0; i < 25; i++ {
		if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest",
			Key:     fmt.Sprintf("quest-%02d", i),
			Name:    fmt.Sprintf("Quest %02d", i),
			Fields:  map[string]any{"min_level": float64(i + 1)},
		}); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	first, err := svc.ListEntities(ctx, project, metamodel.EntityFilter{TypeKey: "quest", Limit: 10})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Entities) != 10 {
		t.Fatalf("first page has %d rows, want 10", len(first.Entities))
	}
	if first.NextCursor == "" {
		t.Fatal("a full page must carry a cursor")
	}

	second, err := svc.ListEntities(ctx, project, metamodel.EntityFilter{TypeKey: "quest", Limit: 10, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Entities) != 10 {
		t.Fatalf("second page has %d rows, want 10", len(second.Entities))
	}
	if second.Entities[0].Key == first.Entities[0].Key {
		t.Fatal("the second page repeated the first")
	}

	last, err := svc.ListEntities(ctx, project, metamodel.EntityFilter{TypeKey: "quest", Limit: 10, Cursor: second.NextCursor})
	if err != nil {
		t.Fatalf("third page: %v", err)
	}
	if len(last.Entities) != 5 {
		t.Fatalf("third page has %d rows, want 5", len(last.Entities))
	}
	if last.NextCursor != "" {
		t.Fatal("a short page must not carry a cursor")
	}
}

func TestListFiltersByInvalid(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "ok", Name: "Fine", Fields: map[string]any{"min_level": float64(1)},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: metamodel.Schema{
			{Key: "min_level", Type: metamodel.FieldNumber, Required: true},
			{Key: "faction", Type: metamodel.FieldText, Required: true},
		},
		ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("evolve: %v", err)
	}

	invalid := true
	page, err := svc.ListEntities(ctx, project, metamodel.EntityFilter{TypeKey: "quest", Invalid: &invalid, Limit: 50})
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(page.Entities) != 1 {
		t.Fatalf("got %d invalid rows, want 1", len(page.Entities))
	}
}

func TestOneHopTraversal(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "takes_place_in", Label: "takes place in", SemanticRole: "spatial",
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}
	if _, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "takes_place_in",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Target:  metamodel.Ref{TypeKey: "zone", Key: "elwynn"},
	}); err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}

	// Which quests happen in Elwynn Forest?
	page, err := svc.ListEntities(ctx, project, metamodel.EntityFilter{
		RelatedTo: &metamodel.RelatedFilter{
			RelationTypeKey: "takes_place_in",
			EntityTypeKey:   "zone",
			EntityKey:       "elwynn",
			Direction:       "incoming",
		},
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(page.Entities) != 1 || page.Entities[0].Key != "hogger" {
		t.Fatalf("one-hop traversal returned %+v", page.Entities)
	}
}

func TestSearchFindsByNameAndTextField(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
		Fields: map[string]any{"min_level": float64(10), "summary": "Defeat the gnoll chieftain."},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	byName, err := svc.Search(ctx, project, "Hogger", "", 10)
	if err != nil {
		t.Fatalf("Search by name: %v", err)
	}
	if len(byName) != 1 {
		t.Fatalf("search by name returned %d rows, want 1", len(byName))
	}

	byField, err := svc.Search(ctx, project, "gnoll", "", 10)
	if err != nil {
		t.Fatalf("Search by field: %v", err)
	}
	if len(byField) != 1 {
		t.Fatalf("search by field returned %d rows, want 1", len(byField))
	}
}
```

- [x] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/metamodel/ -run 'TestList|TestOneHop|TestSearch' -v`
Expected: FAIL, `undefined: metamodel.EntityFilter`.

- [x] **Step 4: Write the implementation**

`internal/metamodel/list.go`:

```go
package metamodel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// RelatedFilter is the single hop of traversal this sub-project offers.
// Transitive walks belong to the views query engine.
type RelatedFilter struct {
	RelationTypeKey string
	EntityTypeKey   string
	EntityKey       string
	Direction       string // "outgoing" or "incoming", relative to the anchor
}

// EntityFilter narrows an entity listing.
type EntityFilter struct {
	TypeKey   string
	Invalid   *bool
	RelatedTo *RelatedFilter
	Cursor    string
	Limit     int32
}

// EntityPage is one page of results plus the cursor for the next.
type EntityPage struct {
	Entities   []dbq.Entity
	NextCursor string
}

// cursor is the keyset position of the last row of a page.
type cursor struct {
	Name string    `json:"n"`
	ID   uuid.UUID `json:"i"`
}

func encodeCursor(c cursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeCursor reads a page position back.
//
// A malformed cursor is invalid_input at path "cursor", not a bare
// error: correction 25's rule is that a caller's own argument being
// wrong is a code with a recovery, and this one's recovery is "page from
// the cursor a previous call handed you, or start from none". An
// untyped error here would reach an agent as internal_error and read as
// "the server is broken" over a value the agent itself supplied.
func decodeCursor(s string) (cursor, error) {
	if s == "" {
		return cursor{}, nil
	}
	malformed := &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
		Path:    "cursor",
		Message: "is malformed: page from the cursor a previous call returned, or omit it to start",
	}}}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor{}, malformed
	}
	var c cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return cursor{}, malformed
	}
	return c, nil
}

// ListEntities returns one page of entities.
func (s *Service) ListEntities(ctx context.Context, projectID uuid.UUID, f EntityFilter) (EntityPage, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}

	if f.RelatedTo != nil {
		return s.listRelated(ctx, projectID, *f.RelatedTo, limit)
	}

	params := dbq.ListEntitiesPageParams{ProjectID: projectID, Limit: limit, Invalid: f.Invalid}
	if f.TypeKey != "" {
		typ, err := s.EntityTypeByKey(ctx, projectID, f.TypeKey)
		if err != nil {
			return EntityPage{}, err
		}
		params.EntityTypeID = &typ.ID
	}
	if f.Cursor != "" {
		c, err := decodeCursor(f.Cursor)
		if err != nil {
			return EntityPage{}, err
		}
		id := c.ID.String()
		params.After = &id
		params.AfterName = &c.Name
	}

	rows, err := s.q.ListEntitiesPage(ctx, params)
	if err != nil {
		return EntityPage{}, fmt.Errorf("list entities: %w", err)
	}

	page := EntityPage{Entities: rows}
	if int32(len(rows)) == limit {
		last := rows[len(rows)-1]
		page.NextCursor = encodeCursor(cursor{Name: last.Name, ID: last.ID})
	}
	return page, nil
}

// listRelated resolves the one-hop filter.
func (s *Service) listRelated(ctx context.Context, projectID uuid.UUID, rel RelatedFilter, limit int32) (EntityPage, error) {
	relType, err := s.RelationTypeByKey(ctx, projectID, rel.RelationTypeKey)
	if err != nil {
		return EntityPage{}, err
	}
	anchor, err := s.EntityByKey(ctx, projectID, rel.EntityTypeKey, rel.EntityKey)
	if err != nil {
		return EntityPage{}, err
	}
	direction := rel.Direction
	if direction != "incoming" && direction != "outgoing" {
		direction = "outgoing"
	}

	rows, err := s.q.ListEntitiesRelatedTo(ctx, dbq.ListEntitiesRelatedToParams{
		ProjectID:      projectID,
		RelationTypeID: relType.ID,
		AnchorID:       anchor.ID,
		Direction:      direction,
	})
	if err != nil {
		return EntityPage{}, fmt.Errorf("list related entities: %w", err)
	}
	// The one-hop query is not paginated: it returns the whole neighbour
	// set and this truncates it, so a page beyond the limit is silently
	// unreachable and NextCursor is deliberately empty rather than
	// misleading. That is acceptable for one hop over one anchor and it
	// is not acceptable for the views engine, which is where transitive
	// walks live. **Task 7 states the cap in the tool description** so an
	// agent knows the answer can be truncated; if a real game needs more,
	// the fix is a keyset on ListEntitiesRelatedTo, not a bigger limit.
	if int32(len(rows)) > limit {
		rows = rows[:limit]
	}
	return EntityPage{Entities: rows}, nil
}
```

`internal/metamodel/search.go`:

```go
package metamodel

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// Search runs a full-text query over entity names and their text fields.
func (s *Service) Search(ctx context.Context, projectID uuid.UUID, query, typeKey string, limit int32) ([]dbq.SearchEntitiesRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	params := dbq.SearchEntitiesParams{ProjectID: projectID, Query: query, Limit: limit}
	if typeKey != "" {
		typ, err := s.EntityTypeByKey(ctx, projectID, typeKey)
		if err != nil {
			return nil, err
		}
		params.EntityTypeID = &typ.ID
	}
	rows, err := s.q.SearchEntities(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("search entities: %w", err)
	}
	return rows, nil
}
```

- [x] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/metamodel/ -v`
Expected: PASS.

- [x] **Step 6: Commit**

```bash
git add internal/db/queries internal/db/dbq internal/metamodel
git commit -m "feat: entity listing with cursor pagination, one-hop traversal and search"
```

---

### Task 7: The MCP tool surface

**Requirement, before any query in this task is written:** every `:one`
and `:many` sqlc query this task adds over a metamodel table
(entity_types, entities, relation_types, relations, and anything else
this table gains later) takes the resolved project id as a parameter and
filters on it in the SQL itself — `WHERE project_id = $1 AND id = $2`,
never `WHERE id = $1` alone. This includes a lookup that looks
uniquely-keyed by an entity or relation-type id: a globally unique id
does not make a cross-game read acceptable, and "this id is already
unique, the project filter is redundant" is exactly the reasoning that
would make it optional in practice. Core's Task 13 (`addScopedTool`,
`internal/web/mcp.go`) hands every tool handler a resolved, already-
checked project id — but it only checks the standardised `project_id`
field on a tool's own input (`ScopedArgs`); it has no way to check a
*differently named* id a tool carries and then uses as a lookup key, so
a handler that takes an `entity_id` and queries `GetEntity(ctx, entityID)`
without also passing the resolved project id would relocate the exact
defect a Task 13 quality review found and fixed, under a name that
review's fix does not cover. The query layer is where the resolved
project id actually becomes enforcement, not the tool wrapper: a query
that cannot be executed without a project id cannot be misused by a
handler that forgot to pass one, whereas a handler that merely forgot to
*check* one compiles and runs fine right up until it leaks another
game's data. Every entity/relation/type table already carries
`project_id` precisely so this is expressible in SQL; use it in every
query this task writes, not only the ones that feel like they need it.

**Second requirement, inherited from Task 6 and blocking on the search
tool alone: settle the ranking before the tool ships.** Task 6's `Search`
ranks with `ts_rank` over an unweighted vector, which means **a row whose
name *is* the query does not outrank a row that merely mentions it in a
paragraph** — `TestSearchRanksTheStrongerMatchFirst` pins that today, and
pins it as a limitation rather than as a promise. Task 6 deferred the
call here deliberately (its decision 11 and correction 25), because
fixing it is `setweight` in `UpsertEntity`'s own statement — a write path
— plus a rewrite of every row already stored, since existing vectors
carry no weights at all, and neither belongs in a read-only task.

So this task does not ship `entities.search` until the question "does a
name match outrank a body match?" has an answer, and the answer is one of
exactly two things:

1. **Implemented**: `setweight` in the write path, a migration or backfill
   rewriting stored vectors, `ts_rank` given the weight array, and
   `TestSearchRanksTheStrongerMatchFirst` rewritten to pin the new order
   — it is the test that has to change, and it says so.
2. **Written down**: the tool description states that ranking is
   frequency-based and unweighted, in the same breath as the two bounds
   below, so an agent that gets a lore paragraph above the character it
   named knows why and does not conclude the character is missing.

Shipping it with the deferral living only in a Go comment is the failure
this requirement exists to prevent: the limitation is invisible from the
tool surface, and an agent's recovery for a bad top hit is to search
again, harder, forever.

**The search tool's description states both bounds**, which Task 6 made
readable for exactly this: `metamodel.MaxSearchQuery` (4 KiB of query
text, refused above that as `invalid_input` at path `query`, along with
any control character) and the 128 KiB per-row index bound
(`searchTextLimit`), which is why a word deep inside a very long lore
field is stored and re-read but not findable. It also states that the
answer is a top-N by rank and not a page, since a caller holding exactly
`limit` rows cannot otherwise tell whether there were more.

**Files:**
- Create: `internal/web/mcp_metamodel.go`
- Modify: `internal/web/server.go`, `internal/web/mcp.go`
- Test: `internal/web/mcp_metamodel_test.go`

- [x] **Step 1: Write the failing test**

`internal/web/mcp_metamodel_test.go`:

```go
package web_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/web"
)

func TestMCPTypesUpsertAndList(t *testing.T) {
	_, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, "designer@studio.com", "Designer", "password12345", false)
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	token, _, _ := ids.CreateAPIToken(ctx, project.ID, user.ID, "agent")
	caller, err := web.CallerForToken(ctx, ids, token)
	if err != nil {
		t.Fatalf("CallerForToken: %v", err)
	}

	deps := web.MCPDeps{Identity: ids, Projects: projSvc, Metamodel: newMetamodelService(t)}

	if _, err := web.MCPTypesUpsert(ctx, deps, caller, web.TypesUpsertInput{
		ProjectID: project.ID,
		Key:       "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: metamodel.Schema{{Key: "min_level", Type: metamodel.FieldNumber}},
	}); err != nil {
		t.Fatalf("MCPTypesUpsert: %v", err)
	}

	types, err := web.MCPTypesList(ctx, deps, caller, project.ID)
	if err != nil {
		t.Fatalf("MCPTypesList: %v", err)
	}
	if len(types) != 1 || types[0].Key != "quest" {
		t.Fatalf("types = %+v", types)
	}
}

func TestMCPRefusesAnotherProject(t *testing.T) {
	_, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, "designer@studio.com", "Designer", "password12345", false)
	mine, _ := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	theirs, _ := projSvc.Create(ctx, "le-mans", "Le Mans", user.ID)
	token, _, _ := ids.CreateAPIToken(ctx, mine.ID, user.ID, "agent")
	caller, _ := web.CallerForToken(ctx, ids, token)

	deps := web.MCPDeps{Identity: ids, Projects: projSvc, Metamodel: newMetamodelService(t)}

	_, err := web.MCPTypesUpsert(ctx, deps, caller, web.TypesUpsertInput{
		ProjectID: theirs.ID, Key: "circuit", Label: "Circuit", LabelPlural: "Circuits",
	})
	if !errors.Is(err, web.ErrScopeViolation) {
		t.Fatalf("err = %v, want ErrScopeViolation", err)
	}
}

func TestMCPEntitiesUpsertBulkReportsFailures(t *testing.T) {
	_, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, "designer@studio.com", "Designer", "password12345", false)
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	token, _, _ := ids.CreateAPIToken(ctx, project.ID, user.ID, "agent")
	caller, _ := web.CallerForToken(ctx, ids, token)

	deps := web.MCPDeps{Identity: ids, Projects: projSvc, Metamodel: newMetamodelService(t)}

	if _, err := web.MCPTypesUpsert(ctx, deps, caller, web.TypesUpsertInput{
		ProjectID: project.ID, Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: metamodel.Schema{{Key: "min_level", Type: metamodel.FieldNumber, Required: true}},
	}); err != nil {
		t.Fatalf("MCPTypesUpsert: %v", err)
	}

	out, err := web.MCPEntitiesUpsert(ctx, deps, caller, web.EntitiesUpsertInput{
		ProjectID: project.ID,
		Mode:      string(metamodel.BulkPartial),
		Items: []metamodel.EntityInput{
			{TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)}},
			{TypeKey: "quest", Key: "b", Name: "B", Fields: map[string]any{"min_level": "nope"}},
		},
	})
	if err != nil {
		t.Fatalf("MCPEntitiesUpsert: %v", err)
	}
	if out.Written != 1 {
		t.Fatalf("Written = %d, want 1", out.Written)
	}
	if len(out.Failed) != 1 || out.Failed[0].Code != "schema_violation" {
		t.Fatalf("Failed = %+v", out.Failed)
	}
}

// newMetamodelService builds a metamodel service over the same ephemeral
// database the test server uses.
func newMetamodelService(t *testing.T) *metamodel.Service {
	t.Helper()
	return metamodelServiceForTest(t)
}

var _ = uuid.Nil
```

Note for the implementer: `newTestServer` from the Core plan returns the
services but not the pool. Extend it to return the `*pgxpool.Pool` as well and
add `metamodelServiceForTest` in the same helper file, building
`metamodel.New(pool, nil)` from it. Update the Core plan's existing callers in
the same commit.

- [x] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/web/ -run TestMCPTypes -v`
Expected: FAIL, `unknown field Metamodel in web.MCPDeps`.

- [x] **Step 3: Write the implementation**

`internal/web/mcp_metamodel.go`:

```go
package web

import (
	"context"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/metamodel"
)

// TypesUpsertInput is the argument shape of maestro.types.upsert.
type TypesUpsertInput struct {
	ProjectID       uuid.UUID         `json:"project_id"`
	Key             string            `json:"key"`
	Label           string            `json:"label"`
	LabelPlural     string            `json:"label_plural"`
	Description     string            `json:"description,omitempty"`
	Color           string            `json:"color,omitempty"`
	Icon            string            `json:"icon,omitempty"`
	Schema          metamodel.Schema  `json:"field_schema,omitempty"`
	ExpectedVersion *int32            `json:"expected_version,omitempty"`
}

// TypeOutput is the slim shape returned for an entity type.
type TypeOutput struct {
	ID          uuid.UUID `json:"id"`
	Key         string    `json:"key"`
	Label       string    `json:"label"`
	LabelPlural string    `json:"label_plural"`
	Version     int32     `json:"version"`
}

// EntitiesUpsertInput is the argument shape of maestro.entities.upsert. It
// takes a batch; a single item is a batch of one.
type EntitiesUpsertInput struct {
	ProjectID uuid.UUID                `json:"project_id"`
	Mode      string                   `json:"mode,omitempty"`
	Items     []metamodel.EntityInput  `json:"items"`
}

// EntitiesUpsertOutput reports what a batch did. Successful rows are counted
// rather than echoed: an agent seeding four hundred entities does not need
// them all back.
type EntitiesUpsertOutput struct {
	Written int                     `json:"written"`
	Failed  []metamodel.BulkFailure `json:"failed,omitempty"`
}

// MCPTypesUpsert implements maestro.types.upsert.
func MCPTypesUpsert(ctx context.Context, deps MCPDeps, caller Caller, in TypesUpsertInput) (TypeOutput, error) {
	if err := requireScope(caller, in.ProjectID); err != nil {
		return TypeOutput{}, err
	}
	row, err := deps.Metamodel.UpsertEntityType(ctx, in.ProjectID, metamodel.EntityTypeInput{
		Key:             in.Key,
		Label:           in.Label,
		LabelPlural:     in.LabelPlural,
		Description:     in.Description,
		Color:           in.Color,
		Icon:            in.Icon,
		Schema:          in.Schema,
		ExpectedVersion: in.ExpectedVersion,
		Actor:           actorOf(caller),
	})
	if err != nil {
		return TypeOutput{}, err
	}
	return TypeOutput{ID: row.ID, Key: row.Key, Label: row.Label, LabelPlural: row.LabelPlural, Version: row.Version}, nil
}

// MCPTypesList implements maestro.types.list.
func MCPTypesList(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID) ([]TypeOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return nil, err
	}
	rows, err := deps.Metamodel.ListEntityTypes(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]TypeOutput, 0, len(rows))
	for _, row := range rows {
		out = append(out, TypeOutput{ID: row.ID, Key: row.Key, Label: row.Label, LabelPlural: row.LabelPlural, Version: row.Version})
	}
	return out, nil
}

// MCPEntitiesUpsert implements maestro.entities.upsert.
func MCPEntitiesUpsert(ctx context.Context, deps MCPDeps, caller Caller, in EntitiesUpsertInput) (EntitiesUpsertOutput, error) {
	if err := requireScope(caller, in.ProjectID); err != nil {
		return EntitiesUpsertOutput{}, err
	}
	mode := metamodel.BulkMode(in.Mode)
	if mode != metamodel.BulkAtomic {
		mode = metamodel.BulkPartial
	}
	for i := range in.Items {
		in.Items[i].Actor = actorOf(caller)
	}
	result, err := deps.Metamodel.UpsertEntities(ctx, in.ProjectID, in.Items, mode)
	if err != nil {
		return EntitiesUpsertOutput{}, err
	}
	return EntitiesUpsertOutput{Written: len(result.Succeeded), Failed: result.Failed}, nil
}

// actorOf maps an authenticated caller onto the audit columns.
func actorOf(caller Caller) metamodel.Actor {
	userID := caller.UserID
	return metamodel.Actor{UserID: &userID, TokenID: caller.TokenID}
}
```

- [x] **Step 4: Add the remaining tools**

In the same file, following the pattern above and reusing `requireScope`, add:

- `MCPTypesGet(ctx, deps, caller, projectID, key)` → `TypeOutput` plus its
  `field_schema`, via `deps.Metamodel.EntityTypeByKey`.
- `MCPTypesRemove(ctx, deps, caller, projectID, id, cascade)` via
  `RemoveEntityType`.
- `MCPRelationTypesUpsert` / `List` / `Get` / `Remove`, mirroring the entity-type
  four over `UpsertRelationType`, `ListRelationTypes`, `RelationTypeByKey` and
  `RemoveRelationType`.
- `MCPEntitiesList(ctx, deps, caller, projectID, metamodel.EntityFilter)` →
  `{entities, next_cursor}`, slim rows (id, key, name, version, invalid) with
  `fields` included only when the input sets `"verbose": true`.
- `MCPEntitiesGet`, `MCPEntitiesRemove`.
- `MCPRelationsUpsert` (batch, both modes, over `UpsertRelations`),
  `MCPRelationsList`, `MCPRelationsRemove`.
- `MCPSearch(ctx, deps, caller, projectID, query, typeKey, limit)`.

Every one of them starts with `requireScope(caller, projectID)`. That call is
the game-isolation invariant, and it is the reason no tool takes its scope from
anything but the caller's own token.

- [x] **Step 5: Register the tools on the MCP server**

In `internal/web/mcp.go`, extend `MCPDeps`:

```go
// MCPDeps are the domain services the MCP tools need.
type MCPDeps struct {
	Identity  *identity.Service
	Projects  *projects.Service
	Metamodel *metamodel.Service
}
```

and register every tool above on the SDK server built in `NewServer`, named
`maestro.types.upsert`, `maestro.types.list`, `maestro.types.get`,
`maestro.types.remove`, `maestro.relation_types.*`, `maestro.entities.*`,
`maestro.relations.*` and `maestro.search`. Each wrapper reads the `Caller`
from the request context and calls the matching `MCP*` function. Map domain
errors onto the wire codes with one helper:

```go
// mcpError maps a domain error onto the stable wire code an agent sees.
func mcpError(err error) (string, string) {
	var conflict *metamodel.VersionConflictError
	switch {
	case errors.Is(err, ErrScopeViolation):
		return "scope_violation", err.Error()
	case errors.As(err, &conflict):
		return "version_conflict", err.Error()
	case errors.Is(err, metamodel.ErrSchemaViolation):
		return "schema_violation", err.Error()
	case errors.Is(err, metamodel.ErrEndpointTypeMismatch):
		return "endpoint_type_mismatch", err.Error()
	case errors.Is(err, metamodel.ErrInUse):
		return "in_use", err.Error()
	case errors.Is(err, metamodel.ErrNotFound):
		return "not_found", err.Error()
	default:
		return "internal_error", "the server could not complete the request"
	}
}
```

- [x] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/web/ -v`
Expected: PASS.

- [x] **Step 7: Commit**

```bash
git add internal/web
git commit -m "feat: mcp tools for types, entities, relations and search"
```

**Corrections made during implementation** (a pass over the landed
surface, written as Task 7 shipped so Tasks 8 and 9 build on what exists
rather than on what was planned).

**The three blocking decisions, settled.**

1. **Search ranking is implemented, not documented away.** The write
   path now builds `setweight(to_tsvector(name), 'A') || setweight(
   to_tsvector(name || ' ' || search_text), 'B')` and
   `0006_weighted_entity_search.sql` rewrites every stored row.
   `TestSearchRanksTheStrongerMatchFirst` became
   `TestSearchRanksTheNameMatchFirst` and asserts the opposite order,
   with the mentioned row carrying the word three times to the named
   row's once so it cannot pass on frequency.

   **The B half is deliberately the whole old text, name included, and
   that is what makes the backfill exact.** A fields-only B half would
   have needed the migration to know which of a row's jsonb fields carry
   indexable text, which is a rule the Go validator owns and the schema
   would have had to re-derive in SQL. With the name in both halves, the
   backfill is `setweight(to_tsvector(name),'A') || setweight(search,
   'B')` — a pure function of what is already stored — so a row migrated
   here and the same row re-seeded afterwards produce identical vectors.
   `TestTheSearchBackfillIsExact` (internal/db) asserts exactly that,
   over three shapes including a row whose fields repeat its own name,
   and was proved red by relabelling one side.

   Ranking uses `ts_rank`'s default weight array; no explicit array is
   passed anywhere, and `Search`'s doc comment says so.

2. **A successful batch reports every row it landed.**
   `BulkResult.Written []BulkWrite` and `RelationBulkResult.Written
   []RelationWrite` are the wire half; `Succeeded` stays `json:"-"` and
   unchanged for callers inside the repository. A `BulkWrite` carries
   `type_key`, `key`, `id` and `version`; a `RelationWrite` carries
   `type_key`, `id`, `source_id` and `target_id`, and no version,
   because an edge has none.

   **A count alone was rejected**: an agent already knows how many items
   it sent, and subtracting the failures gives it the same number. What
   it genuinely cannot derive is the row's `id` — every removal in this
   package is addressed by id — and its `version`, which is not a
   convenience but a requirement: `upsertEntityWith` *refuses* an update
   of an existing row without a matching `ExpectedVersion`, so without
   this an agent could not edit what it had just written without a
   second read.

3. **Retryable contention earns a wire code, and it is `retryable`.**
   `metamodel.IsRetryable` admits four SQLSTATEs — 40001, 40P01, 55P03,
   57014 — and `failureFor` and `mcpErrorFor` both read it.

   It is named for the *recovery* and not for the cause, deliberately.
   `contention` would be wrong for a 57014 raised by a slow statement
   rather than by a lock; what all four share is exactly one property —
   the identical call, resent unchanged, may succeed — and that property
   is the only thing an agent can act on. It is also the only code in
   the vocabulary that says *change nothing*: every other one names
   something the caller must change first.

   It is a **predicate rather than a sentinel** because contention can
   surface from any statement in the package, so a sentinel would have
   to be wrapped in at dozens of call sites and would be missing from
   whichever one a later change forgot. Checked *last* in both switches,
   after every domain code, so it can only ever intercept errors that
   were heading for `internal_error`. Class-prefix matching was rejected:
   class 40 also holds transaction_rollback and class 57 holds 57P01-05,
   which are the server going away, and telling an agent to resend into
   a shutting-down instance is not a recovery.
   `TestALockTimeoutOnTheEndpointCheckIsNotReportedAsInvalidInput` now
   proves it on a real held lock, not on a hand-built `PgError`.

4. **`ErrActorNotInGame` does not earn a wire code.** Decided here and
   recorded on the sentinel. A code exists to name a recovery, and this
   condition has none on the agent's side.

5. **Relations still have no `version`, and Task 7 declined the
   migration.** Recorded on `RelationInput`. Adding the column alone
   would harden the wrong half — the exposure is an edge's *fields*,
   which is where the parallel-edge refusal pushes multiplicity — and
   the two remedies have to be chosen between together. What Task 7 did
   instead is stop the surface from implying a guarantee that is not
   there: `relations.upsert`'s description states that an edge is
   last-writer-wins.

6. **Event coalescing: settled, and not by coalescing.** `events.go`
   claimed Tasks 6 and 7 owned it "as the thing that makes the benefit
   true". Two things closed it. First, coalescing cannot deliver that
   benefit at all: collapsing many events into one necessarily discards
   the per-row identity, so the coalesced event says "re-read", which is
   what the resync already says — with a bounded buffer and no durable
   log, per-row payloads and burst-proof delivery are not both
   available. Second, decision 2 removed the need on the path the
   comment worried about: the agent driving a batch now learns every row
   that landed, with its version, from its own synchronous answer, so it
   is not racing a stream for it. For every other subscriber —
   a designer's browser, an agent watching someone else's writes —
   overflow-to-resync is already the correct behaviour. What is left
   open, and named as such, is *batching* in the SSE writer (one
   flush per drained burst instead of one per event), which changes no
   payload and belongs to whoever owns the realtime surface.

**Where the plan was wrong, and what shipped instead.**

7. **The printed tool functions took a caller-supplied `ProjectID` as a
   struct field.** That is the shape Core's Task 13 review removed: a
   caller-supplied id used as a lookup key. Every input here embeds
   `ScopedArgs` instead, and every `MCP*` function takes the *resolved*
   project id `addScopedTool` hands it, plus a `Caller` it re-checks with
   `requireScope`.

8. **The printed `EntitiesUpsertInput` carried `[]metamodel.EntityInput`
   straight off the wire.** `metamodel.EntityInput` has an `Actor`, which
   is the audit record of who wrote the row — so that shape would have
   let an agent name any user or token it liked as the author of its
   writes. It also has no json tags, so the wire keys would have been
   `TypeKey`, `Key`, `Name`. **No domain type is used as an *input* type
   anywhere in `mcp_metamodel.go`**; `EntityItemInput` and
   `RelationItemInput` carry no actor and `actorOf` builds one from the
   authenticated caller.
   `TestMCPEntitiesUpsertRecordsTheCallersOwnToken` pins it, proved red
   by zeroing the actor. The unqualified version of that sentence, which
   this correction and the file header both carried, was wrong about the
   output direction; correction 26 fixes it.

9. **Tools are not prefixed `maestro.`.** Step 5 names them
   `maestro.types.upsert`; Core shipped `whoami`, `games.list`,
   `games.get`, and the server's own implementation name is already
   `maestro`. Sixteen tools carrying a redundant prefix the three
   existing ones do not was the wrong half of the inconsistency to keep.

10. **Ids cross the wire as strings, not `uuid.UUID`.** The SDK infers a
    tool's *input* schema from the Go type by reflection, and
    `uuid.UUID` is a `[16]byte` — an array of integers, not the string
    it marshals as. Outputs keep `uuid.UUID` because their schemas are
    hand-written, exactly as Core does. A malformed id is
    `invalid_input` at that argument's own path (`source_type_ids[1]`
    for an element of a list), never a 500;
    `TestMCPMalformedIDIsTheCallersOwnArgument` pins it.

11. **`newTestServer` was not changed to return the pool.** Step 1's
    note asks for it plus an update of every existing caller. Several
    dozen callers growing an ignored fourth result to reach a service
    none of them use is churn for nothing, and a `Server` built without
    a metamodel service is a shape this package supports deliberately.
    `newMetamodelTestServer` is a second helper beside it.

12. **The printed `mcpError` helper was not added; `mcpErrorFor` grew
    the arms instead.** A second mapping function beside the one Core
    already has is two vocabularies to keep in step. The arms match with
    `errors.Is` against the sentinels and **never read `Code` off a
    `*ValidationError`**, which is what makes an unrecognised code fall
    through to `internal_error` rather than being published as whichever
    code is most taught — the pairing `ValidationError.Is` argues from
    the other side. `fieldDetails` puts the field paths in the error's
    `details` as data. Every arm was proved load-bearing by deleting it
    and watching its own case fail.

**What else landed.**

13. **The tool descriptions interpolate the domain's own constants.**
    `metamodel` now exports `DefaultEntityPage`, `MaxEntityPage`,
    `DefaultRelationPage`, `MaxRelationPage`, `DefaultSearchLimit`,
    `MaxSearchLimit` and `MaxIndexedText` beside the already-exported
    `MaxSearchQuery`, for the rule correction 24 established: a bound a
    caller cannot read is a bound a caller trips over. No number in a
    description is typed out, so a description cannot go on promising a
    cap that moved. `search`'s description states the ranking, both
    bounds and that the answer is a top-N and not a page.

    **The clause about `entities.list` that stood here was false, and so
    was the description built from it** — see correction 20. The
    `related_to` traversal was never unpaged and never truncated. The
    rule this correction states is about *numbers*; it does nothing
    about a sentence, and a sentence is what went wrong.

14. **`TestEveryMCPToolGoesThroughAddScopedTool`** is the MCP
    counterpart of `TestEveryGameScopedRouteGoesThroughRequireProject`.
    `addScopedTool` records every tool it registers on the `Server`, and
    the test compares that record against the tool list a real client
    reads back over the real transport — in both directions. Proved red
    by registering one tool with `mcp.AddTool` directly.

15. **`TestMCPToolsRefuseAnotherGame`** calls all sixteen tools against a
    second game the caller's own *user* owns and its *token* is not
    bound to, and asserts `ErrScopeViolation` from every one, then that
    nothing landed. One table rather than sixteen tests, because a
    per-tool test is sixteen chances to forget the seventeenth.

16. **The metamodel service was not wired into the binary, and now is.**
    `cmd/maestro/main.go` built `web.Options` without one, so a
    production instance would have served the Core three tools and
    nothing else — and every test in `internal/web` would have kept
    passing, because they all build their own `Server`. It also now
    builds one `realtime.Hub` and hands the same instance to both the
    web server and the metamodel service; two of them would have left a
    designer's browser watching a stream nothing writes to.
    `TestTheRunningBinaryServesTheGameContentTools` (cmd/maestro) seeds
    an entity and searches for it through the real endpoint with a real
    token, and was proved red by removing the wiring.

17. **`entities.list`, `entities.get` and `search` resolve
    `entity_type_id` to a type key**, at the cost of one extra small
    query per call. A mixed listing whose rows carry a uuid the agent
    cannot interpret is not an answer, and the recovery would have been
    the same query one round trip later. A game's type vocabulary is a
    handful of hand-written rows, not a table that grows with content.

18. **`relations.list` answers with endpoint *ids*, and says so.** The
    row holds ids and there is no bulk entity-by-ids query; resolving a
    page of edges to `(type_key, key)` refs would need one. The
    description says plainly that walking a game's graph in keys is
    `entities.list` with `related_to`, and that this tool is for the
    edges themselves — removing one, or reading a dense node's edges
    past the traversal cap. **This is a real limitation and the fix is a
    `ListEntitiesByIDs` query**; Task 8's REST mirror faces the same
    question for the graph view and is where it is cheapest to add.

19. **Batch answers emit empty arrays, never null.** A nil slice
    marshals to JSON `null`, which fails the output schema and, more to
    the point, makes "the batch reported no failures" look like "the
    batch reported nothing".

**A second review round, closing what it found.** Isolation and
authorship came through clean; every finding below is about what the
surface *says* and what an agent does after reading it.

20. **`entities.list`'s description claimed the traversal was not
    paged, and every clause of that claim was false.** It said the
    one-hop `related_to` walk returned at most `MaxEntityPage`
    neighbours, never set `next_cursor`, and silently dropped anything
    past the cap — and sent an agent to `relations.list` as the escape
    hatch. `listRelated` has always computed a fingerprint, decoded the
    cursor and returned `pageOf(...)`: the same paging as the plain
    listing, with the same `pageSize(f.Limit, 50, 500)`, so **50 by
    default and not 500**. Proved live: 60 neighbours give a 50-item
    page with a cursor, then a 10-item page. The sentence was written
    new, against code that already paged.

    An agent following it would have stopped at the first page believing
    it held a whole neighbourhood. Fixed in the description, in the
    `MaxEntityPage` comment that fed it (`internal/metamodel/list.go`)
    and in correction 13 above. `relations.list`'s description is
    repaired by the same change: it was offering the id-shaped tool as
    the way around a cap that does not exist, and now says what it is
    actually for — an edge's own fields, which a traversal over entities
    never returns.

    `TestATraversalPagesLikeEveryOtherListing` had been pinning the
    truth at the domain layer all along; what was missing was a guard at
    the wire, where the sentence lives.
    `TestTheTraversalPagesAndItsDirectionIsRequiredOnTheWire`
    (internal/web) pages a traversal over a real MCP client and was
    proved red by making `listRelated` return its rows without a cursor.

21. **`related_to.direction` was documented and typed optional and is
    mandatory.** The description called `"outgoing"` its default,
    `RelatedToInput.Direction` carried `omitempty`, and the served input
    schema therefore left it out of `required` — while `listRelated`
    refuses an absent or unrecognised value outright, arguing that
    defaulting to outgoing is "the same fault with a fuller page".

    **The domain's refusal is right and the wire now agrees with it.**
    Half a neighbourhood presented as the whole one is a wrong answer,
    not a convenience. The `omitempty` is gone — the SDK infers this
    schema by reflection, so the tag *is* the wire contract — the prose
    names no default, and the same test above asserts `direction` is in
    the served `required` list and that omitting it cannot produce a
    listing. Proved red by putting `omitempty` back.

22. **`semantic_role` reported `internal_error` for a value the
    description enumerates.** Nothing in Go validated it; the only guard
    was the `CHECK` in `0004_metamodel.sql`, so `semantic_role:
    "nonsense"` reached Postgres and came back as
    `{"error":"internal_error"}` with a check-constraint violation in
    the log — something an agent typed and could fix, reported as a
    server fault, with no path and no list of what would be accepted.
    It is the only reachable `CHECK` on the metamodel tables.

    `metamodel.SemanticRoles` is now the exported list, `UpsertRelationType`
    refuses anything outside it as `invalid_input` at path
    `semantic_role` naming all six, and `relation_types.upsert`'s
    description is built from the same slice (correction 13's rule,
    applied to a list rather than to a number) so the two cannot drift.
    The constraint stays as the backstop for a writer that does not come
    through this package.

23. **Ranking: the promise was true for one word and false for more, and
    the fix is a sort key rather than a weight.** The description
    promised that a row the query *names* outranks a row that only
    mentions the words in a field, "however often it mentions them".
    `ts_rank` saturates towards 1.0 as a lexeme repeats, so a single
    word under label A wins comfortably — a name match beat 5000
    repetitions — but a multi-word query is a weighted sum of several
    saturating terms and frequency overtakes the name. **Four
    repetitions of a two-word phrase was enough**: `search "gnoll pack"`
    ranked a lore row above the entity actually named `Gnoll Pack`.
    `TestSearchRanksTheNameMatchFirst` only ever exercised one word.

    **Qualifying the description was rejected in favour of fixing the
    ranking**, because the promise is the useful one: an agent's first
    hit being the wrong row has no recovery but to search again, harder,
    which is the whole reason 0006 exists. An explicit weight array was
    rejected too — it moves the number of repetitions it takes and
    leaves the shape of the curve alone. `SearchEntities` now leads its
    `ORDER BY` with `ts_filter(search, '{a}') @@ query`: the A half is
    the name and nothing else, so the predicate asks exactly the
    question the promise is about, and as a leading key it is a
    guarantee rather than a tendency. `ts_rank` still orders within each
    of the two groups, which is the work the weights were introduced
    for. The test is now a table — one word, two words, two words
    repeated 500 times, three words — and the two-word cases were proved
    red against the old `ORDER BY`.

24. **Bulk failures leaked raw Postgres text, the same hole `mcpErrorFor`
    was already closing one step along.** `failureFor` set
    `Message: err.Error()` unconditionally, *before* the switch, so
    `retryable` and `internal_error` carried `canceling statement due to
    lock timeout (SQLSTATE 55P03)`, `relation "entities_secret" does not
    exist (SQLSTATE 42P01)`, constraint names. `mcp_errors.go`
    deliberately withholds exactly that and a test asserts it — and the
    bulk path is where contention was actually observed, so this was the
    likelier route out, not the rarer one. It also undercut `retryable`
    itself: the message an agent read beside the code was the text the
    design says it must not see.

    Both arms now carry a fixed message, and `failureFor` takes a
    context so it can `slog` what it withholds — withholding a message
    must not lose it, which is the pairing `mcpErrorFor` already had.
    Every other arm keeps `err.Error()`, because every other code names
    something the caller sent.

25. **The retryable ordering is now pinned on both sides.** Moving
    `IsRetryable` to the front of `failureFor`, and to the front of
    `mcpErrorFor`, left the full suite green both times — an ordering
    argued at length in two doc comments and enforced by nothing. An
    error that is both a domain refusal and a contention SQLSTATE
    (`errors.Join`) must report the domain code: that is the one a
    caller can act on, and "resend unchanged" is the worst possible
    advice about a row that will be refused again.
    `TestABulkFailureNeverCarriesTheDatabasesOwnWords` and
    `TestADomainCodeOutranksAContentionSQLSTATE` pin it; both mutations
    are now red.

26. **"No wire type here is a domain type" was false in the output
    direction, and is now stated accurately and *tested*.**
    `TypeDetailOutput.Schema` and `RelationTypeDetailOutput.Schema` are
    `metamodel.Schema`; the bulk outputs carry `[]metamodel.BulkWrite`,
    `[]metamodel.RelationWrite` and `[]metamodel.BulkFailure`. Nothing
    leaks today, but an `updated_by_user_id` added to `BulkWrite` **did**
    reach the wire, unblocked by the hand-written output schema and
    caught only by a golden test in another package. The invariant held
    by that test, not by the structural rule the comment claimed.

    Shadowing the four was rejected: their field lists *are* the wire
    contract, and a shadow struct beside each is a copy to keep in step —
    the failure this avoids rather than the one it causes. Instead the
    header says input and output separately, and
    `TestTheDomainTypesOnTheWireCarryExactlyTheseKeys` marshals all four
    and pins their key sets, so growing one of them fails in the package
    whose comment makes the claim. Proved red twice.

27. **`57014` stays in `IsRetryable`, and the advice beside it was
    completed instead.** 40001, 40P01 and 55P03 are contention by
    construction; `query_canceled` is also what an operator's
    `statement_timeout` raises on a query that is simply too expensive,
    every time it is run, and "change nothing and resend" is wrong
    advice there. **Dropping it is worse**: a lock wait that runs into
    `statement_timeout` rather than into `lock_timeout` is exactly the
    contention this code exists for, and it would report as
    `internal_error` — a caller told the server broke when the recovery
    was to wait a moment.

    So the code is kept, because it still names the one recovery all
    four share, and what was missing is the *next* step when resending
    stops helping. On a read there always is one: ask for less.
    `mcpErrorFor`'s shared message says so for every tool, and
    `entities.list`, `relations.list` and `search` each carry the read
    counterpart of the back-off advice `entities.upsert` already had.

28. **The backfill test did not guard the shipped migration.**
    `TestTheSearchBackfillIsExact` wrote both SQL expressions out in its
    own literal and compared them, and its doc said "change either and
    this test says so". It did not: changing the *actual migration's*
    `UPDATE` to a non-equal expression left every test green, because
    nothing in the suite ever applied `0006` to a row. It proved an
    algebra identity about a copy.

    The identity itself was correct — verified independently over twelve
    shapes with no mismatches — so nothing about the migration changed.
    The test did. It now seeds real rows carrying the pre-0006 vector,
    executes the Up arm **read out of the file that ships**, then writes
    the same rows through `dbq.UpsertEntity` — the generated caller of
    the statement that ships — and compares the two columns. Neither
    side is written out in the test any more. It also covers a NULL
    vector (which is what `coalesce` is for) and runs the Down arm.
    Proved red by mutating the migration's Up arm, the Down arm, and
    `UpsertEntity`'s B half in turn.

29. **Small things.** `types.remove` on a type still in use answered
    `{"error":"in_use","message":"in_use"}`, and all four removals'
    not-found answered `message:"not_found"` — a code repeated as prose,
    where `entities.get` and `types.get` have named their misses since
    Task 4. `missingByID` now names the kind of row and the id back (the
    only part of the call a caller can compare against what it holds),
    and `stillInUse` names the type, how much content stands behind the
    refusal and that `cascade` is the way through — which is the actual
    recovery and was nowhere in the answer.

    And `limit: -1` silently becomes the default on all three listings
    while the descriptions documented only the over-cap case; each now
    says both.

**Corrections from the Task 7 re-review** (a fourth pass, after
corrections 20-29 landed, over what the ranking fix's own wire shape and
its own SQL comment said about themselves).

30. **The rank on the wire no longer explained the order it was handed
    in, and correction 23 is the reason.** That correction changed
    `SearchEntities`'s `ORDER BY` to `(name_match, rank)` so a name match
    always sorts first, but the wire still carried only `rank` — `dbq.
    SearchEntitiesRow` had a `NameMatch` column (typed `interface{}`,
    because `ts_filter(...) @@ plainto_tsquery(...)` alone is a type
    sqlc could not infer) that `MCPSearch` read the sort by but never
    put on `SearchHit`. `SearchHit`'s own doc comment still said "an
    entity plus the rank it matched at", `SearchOutput`'s said "the top
    `limit` rows by rank", and `Search`'s said the same — all three were
    true before correction 23 and false after it, and nothing on the
    wire let a caller recover the grouping the order was actually built
    from. Proved live exactly as the re-review found it: `Gnoll Pack`
    (rank 0.999224) sorts before `Wanted: Hogger` (rank 1.000000)
    because the first is name-matched and the second is not; a caller
    that re-sorted by `rank`, or simply trusted that 1.0 beats 0.999,
    would reconstruct the wrong order.

    Fixed by putting `name_match` on the wire rather than only
    correcting the prose: `(ts_filter(e.search, '{a}') @@
    plainto_tsquery(...))::bool AS name_match` in
    `internal/db/queries/metamodel.sql` gives sqlc a type it can infer
    (`bool` instead of `interface{}`), `SearchHit.NameMatch bool
    json:"name_match"` carries it, `searchHitOutputSchema` requires it,
    and `MCPSearch` sets it from the row. `SearchHit`, `SearchOutput`
    and `Search`'s doc comments are corrected to describe the order as
    `(name_match, rank)`, with `rank` ordering only within a group, and
    the `search` tool description states the same thing an agent reads
    before calling it.
    `TestMCPSearchNameMatchAgreesWithTheOrderItExplains`
    (internal/web) pins it at the wire: it fails to compile if
    `name_match` is dropped from `SearchHit`, and fails outright if the
    order `SearchEntities` produced and the `name_match` values on the
    wire ever disagree in either direction. Proved red by reverting the
    `ORDER BY` to `rank DESC` alone, which reproduces the wrong order the
    field exists to explain.

31. **The SQL comment's count and cost figure went stale in the same
    commit that made them wrong, and both halves of the reviewer's
    finding held up.** "The tsquery is built twice" became three —
    correction 30 added the `name_match` projection alongside `rank`'s
    and the `WHERE`'s — and the comment's "worth about 0.8 ms on the
    worst query this surface will accept" was left over from measuring
    the two-copy case, so it understated the real repetition by roughly
    half even before asking whether the repetition costs anything at
    all.

    Re-measured on the real call path rather than trusting either
    account: `dbq.SearchEntities` reaches Postgres through pgx's
    extended protocol with `query` bound as parameter `$1`, and
    `plainto_tsquery` is `IMMUTABLE`, so once the bound value is known at
    plan time every occurrence folds to the same `::tsquery` literal.
    `EXPLAIN (ANALYZE, VERBOSE)` over the exact statement, through the
    exact bound-parameter path, on real seeded rows, printed all three
    occurrences as the identical literal rather than as a repeated
    function call. Timing 200 runs of the statement as shipped (three
    occurrences) against a hand-written version built once via a
    `LATERAL` join and read three times, through the same `pool.Query`
    path: 0.370 ms/run against 0.389 ms/run — indistinguishable, and if
    anything the "write it once" form was the slower one, which is what
    folding predicts once nothing is left to save.

    **This does not overturn the old measurement — it was answering a
    different question.** 0.63 ms against 1.44 ms at 4 KiB was measured
    against a *non-constant* `plainto_tsquery`, built by splicing the
    query text into the SQL itself rather than binding it as a
    parameter, which is exactly the case that defeats the folding just
    measured. Nothing in this package does that. The SQL comment in
    `internal/db/queries/metamodel.sql` (mirrored in the generated
    `internal/db/dbq/metamodel.sql.go`) now states the count correctly,
    states which measurement applies to which case, and says explicitly
    that a future caller reaching this statement any other way than a
    bound parameter should re-measure rather than trust either number
    here. No code path changed — the statement still writes the tsquery
    three times, and that stays right, because the `WITH ... MATERIALIZED`
    alternative correction 20's era already measured is worse, not
    neutral: it costs the planner its exact row estimate (5 rows exact
    versus 100, a default guess) for a folding it already gets for free.

32. **Informational — recorded, not fixed.** `0006_weighted_entity_
    search.sql`'s Down-arm assertion, inside `TestTheSearchBackfillIsExact`
    (`internal/db/metamodel_schema_test.go:594`), checks only "no weight,
    no NULL" —
    `search IS NULL OR search::text LIKE '%A%'` counts zero. The
    migration's own comment says the Down arm removes every weight
    **and every position**, but the test pins only the weight half:
    replacing `strip(search)` with `setweight(search, 'D')` in the Down
    arm would survive this guard, because `setweight` clears no weight
    letters other than the one it sets and the row would carry no `'A'`
    substring either way — while leaving every lexeme's position data
    intact, which `strip` removes and `setweight` does not.

    Behaviourally harmless today: `name_match`'s leading predicate reads
    the A weight, and a `setweight(search, 'D')` row would report
    `name_match = false` exactly as a fully stripped row does, so nothing
    downstream would have told the difference. The guard is real for the
    claim it makes and half of what the migration's prose claims — the
    next person touching `0006`'s Down arm should not read this test's
    green as proof the "every position" half holds.

---

### Task 8: REST mirror and the game home page

**Files:**
- Create: `internal/web/api_metamodel.go`
- Modify: `internal/web/server.go`, `internal/web/static/game.html`, `internal/web/static/app.js`
- Test: `internal/web/api_metamodel_test.go`

- [ ] **Step 1: Write the failing test**

`internal/web/api_metamodel_test.go`:

```go
package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRESTTypesRequireMembership(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, "owner@studio.com", "Owner", "password12345", false)
	if _, err := ids.CreateUser(ctx, "stranger@studio.com", "Stranger", "password12345", false); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)

	cookie := loginAs(t, srv, "stranger@studio.com")
	req := httptest.NewRequest(http.MethodGet, "/api/games/"+project.ID.String()+"/types", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestRESTCreateAndListTypes(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, "owner@studio.com", "Owner", "password12345", false)
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	cookie := loginAs(t, srv, "owner@studio.com")

	body := strings.NewReader(`{"key":"quest","label":"Quest","label_plural":"Quests"}`)
	create := httptest.NewRequest(http.MethodPost, "/api/games/"+project.ID.String()+"/types", body)
	create.AddCookie(cookie)
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, create)
	if createRec.Code != http.StatusOK && createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", createRec.Code, createRec.Body.String())
	}

	list := httptest.NewRequest(http.MethodGet, "/api/games/"+project.ID.String()+"/types", nil)
	list.AddCookie(cookie)
	listRec := httptest.NewRecorder()
	srv.ServeHTTP(listRec, list)

	var payload struct {
		Types []struct {
			Key string `json:"key"`
		} `json:"types"`
	}
	if err := json.NewDecoder(listRec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Types) != 1 || payload.Types[0].Key != "quest" {
		t.Fatalf("types = %+v", payload.Types)
	}
}

func TestRESTSchemaViolationReturns422(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, "owner@studio.com", "Owner", "password12345", false)
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	cookie := loginAs(t, srv, "owner@studio.com")

	body := strings.NewReader(`{"key":"quest","label":"Quest","label_plural":"Quests","field_schema":[{"key":"difficulty","type":"enum"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/games/"+project.ID.String()+"/types", body)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "schema_violation") {
		t.Fatalf("body = %s, want the schema_violation code", rec.Body.String())
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/web/ -run TestREST -v`
Expected: FAIL, 404.

- [ ] **Step 3: Write the implementation**

`internal/web/api_metamodel.go`:

```go
package web

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/neverbot/maestro/internal/metamodel"
)

// writeDomainError maps a domain error onto an HTTP status and the same code
// the MCP surface uses, so both surfaces stay one contract.
func writeDomainError(w http.ResponseWriter, err error) {
	var conflict *metamodel.VersionConflictError
	switch {
	case errors.Is(err, ErrScopeViolation):
		writeError(w, http.StatusForbidden, "scope_violation", err.Error())
	case errors.As(err, &conflict):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":           "version_conflict",
			"message":         err.Error(),
			"current_version": conflict.Current,
		})
	case errors.Is(err, metamodel.ErrSchemaViolation):
		writeError(w, http.StatusUnprocessableEntity, "schema_violation", err.Error())
	case errors.Is(err, metamodel.ErrEndpointTypeMismatch):
		writeError(w, http.StatusUnprocessableEntity, "endpoint_type_mismatch", err.Error())
	case errors.Is(err, metamodel.ErrInUse):
		writeError(w, http.StatusConflict, "in_use", err.Error())
	case errors.Is(err, metamodel.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "the server could not complete the request")
	}
}

func (s *Server) handleListTypes(w http.ResponseWriter, r *http.Request, caller Caller) {
	projectID, ok := s.requireProjectAccess(w, r, caller)
	if !ok {
		return
	}
	rows, err := s.opts.Metamodel.ListEntityTypes(r.Context(), projectID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	types := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		types = append(types, map[string]any{
			"id": row.ID, "key": row.Key, "label": row.Label,
			"label_plural": row.LabelPlural, "version": row.Version,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"types": types})
}

func (s *Server) handleUpsertType(w http.ResponseWriter, r *http.Request, caller Caller) {
	projectID, ok := s.requireProjectAccess(w, r, caller)
	if !ok {
		return
	}
	var body struct {
		Key             string           `json:"key"`
		Label           string           `json:"label"`
		LabelPlural     string           `json:"label_plural"`
		Description     string           `json:"description"`
		Schema          metamodel.Schema `json:"field_schema"`
		ExpectedVersion *int32           `json:"expected_version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "malformed JSON body")
		return
	}

	row, err := s.opts.Metamodel.UpsertEntityType(r.Context(), projectID, metamodel.EntityTypeInput{
		Key: body.Key, Label: body.Label, LabelPlural: body.LabelPlural,
		Description: body.Description, Schema: body.Schema,
		ExpectedVersion: body.ExpectedVersion, Actor: actorOf(caller),
	})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": row.ID, "key": row.Key, "version": row.Version,
	})
}

func (s *Server) handleListEntities(w http.ResponseWriter, r *http.Request, caller Caller) {
	projectID, ok := s.requireProjectAccess(w, r, caller)
	if !ok {
		return
	}
	page, err := s.opts.Metamodel.ListEntities(r.Context(), projectID, metamodel.EntityFilter{
		TypeKey: r.URL.Query().Get("type"),
		Cursor:  r.URL.Query().Get("cursor"),
		Limit:   50,
	})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	entities := make([]map[string]any, 0, len(page.Entities))
	for _, row := range page.Entities {
		entities = append(entities, map[string]any{
			"id": row.ID, "key": row.Key, "name": row.Name,
			"version": row.Version, "invalid": row.Invalid,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entities": entities, "next_cursor": page.NextCursor})
}
```

- [ ] **Step 4: Register the routes and extend Options**

Add `Metamodel *metamodel.Service` to `web.Options`, and in `NewServer`:

```go
	s.mux.Handle("GET /api/games/{game}/types", requireCaller(s.handleListTypes))
	s.mux.Handle("POST /api/games/{game}/types", requireCaller(s.handleUpsertType))
	s.mux.Handle("GET /api/games/{game}/entities", requireCaller(s.handleListEntities))
```

Wire it in `cmd/maestro/main.go`:

```go
		Metamodel: metamodel.New(pool, hub),
```

where `hub` is the `*realtime.Hub` the Core plan created, so domain writes reach
open browsers.

- [ ] **Step 5: Show the counts on the game page**

Replace `internal/web/static/game.html`:

```html
<!doctype html>
<meta charset="utf-8">
<title>Maestro</title>
<link rel="stylesheet" href="/static/styles.css">
<h1 id="game-name">Loading…</h1>
<ul class="games" id="types"></ul>
<p id="empty" hidden>This game has no types yet. An agent can declare them over MCP.</p>
<script type="module" src="/static/app.js"></script>
```

Append to `internal/web/static/app.js`:

```js
const typesList = document.getElementById("types");
if (typesList) {
  const slug = window.location.pathname.split("/")[2];
  const games = await (await fetch("/api/games")).json();
  const game = (games.games ?? []).find((g) => g.slug === slug);
  if (game) {
    document.getElementById("game-name").textContent = game.name;
    const types = await (await fetch(`/api/games/${game.id}/types`)).json();
    for (const type of types.types ?? []) {
      const item = document.createElement("li");
      item.textContent = `${type.label_plural} (${type.key})`;
      typesList.append(item);
    }
    document.getElementById("empty").hidden = (types.types ?? []).length > 0;
  }
}
```

- [ ] **Step 6: Run the whole suite**

Run: `make check`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/web cmd/maestro/main.go
git commit -m "feat: rest mirror for types and entities, and the game home page"
```

**Corrections made during implementation** (a pass over the landed
surface, written as Task 8 shipped so Task 9 builds on what exists rather
than on what was planned).

**The three decisions earlier tasks recorded as Task 8's, settled.**

1. **Route-shaped keys: the route shape changed, the key rule did not.**
   Task 5's `keys.go` recorded three options — a reserved-word list in
   the domain, a route shape that cannot collide, or resolving the
   ambiguity in the router — and left the choice here. **The route shape
   wins.** Every row is addressed behind a fixed discriminator:
   `/api/games/{game}/types/by-key/{key}`, `.../types/by-id/{id}`,
   `.../entities/by-key/{type}/{key}`, `.../entities/by-id/{id}`, and the
   same for relation types and relations. No key ever occupies a segment
   a literal could also claim, so `new`, `index`, `id`, `null`, `games`,
   `types`, `search` — and `by-key` and `by-id` themselves — are all
   ordinary keys. `TestARouteShapedKeyIsStillAddressable` declares a type
   for each of those words, reads it back by key, and removes it by id.

   The word list was rejected because it forbids `new` to a game with a
   perfectly good reason to name a type that, and because a key rule
   tightened after a game is seeded costs renames — `keys.go` argued that
   itself. **Resolving in the router was rejected because it fails
   silently**, which is worth stating precisely: Go's `ServeMux` prefers
   a literal segment over a wildcard with no error and no warning
   (confirmed directly — a mux holding both `GET /types/{key}` and `GET
   /types/new` routes `/types/new` to the literal and `/types/quest` to
   the wildcard), so a `/types/new` page added a year from now would take
   an existing type offline with nothing failing anywhere. `keys.go` now
   records the decision instead of the question.

2. **Reserved keys: nothing is reserved, because nothing flattens.**
   Task 2's open list and Task 3's correction block both named this as a
   Task 3/8 decision, with two options: reserve `key`, `name`, `id`,
   `type`, `version` in `Check()`, or keep the two namespaces separated
   by construction wherever flattening happens. **The second.** A game's
   values live in their own `fields` object at every layer — the jsonb
   column, `EntityOutput.Fields` on the MCP wire, the same field on the
   REST wire, and the page, which renders counts and never a row's
   fields at all. There is no flattening anywhere in this task, so there
   is no collision to prevent, and no game is told to rename a field it
   legitimately calls `name`.
   `TestAGameFieldNamedLikeARowColumnNeverShadowsIt` declares a type
   whose six fields are named after six row columns (`id`, `key`, `name`,
   `version`, `invalid`, `type_key`), writes an entity whose values for
   all six are lies about the row, and reads it back to prove the row's
   own identity is untouched and every value came back under `fields`.

   This is a constraint on future work, not a closed question: the day a
   view *does* flatten a row into a table column or an export, that
   surface owns the collision, and the answer there is a prefix or a
   two-level shape — not a reservation retrofitted onto games that are
   already seeded.

3. **`relations.list` now answers with refs as well as ids, on both
   surfaces.** Task 7's finding 18 shipped endpoint ids honestly
   documented, for want of a bulk entity-by-ids read, and named this task
   as where the fix was cheapest. It is: `ListEntitiesByIDs`
   (`internal/db/queries/metamodel.sql`) and `Service.EntitiesByIDs` are
   that read — one query per page, project-filtered in SQL like every
   other statement — and `endpointRefs` (`mcp_metamodel.go`) joins it
   onto a page of edges. `RelationOutput` carries `source_id`/`target_id`
   *and* `source`/`target` (`type_key`, `key`, `name`), because the ids
   are still what a removal and the endpoint filters address.

   Three things went with it, because a fix that closes one path and
   leaves the same hole one step along is this project's third standing
   lesson: the MCP tool's description, which said the opposite of what
   the tool now does; the hand-written `relationsListOutputSchema`, which
   would otherwise have advertised a shape the tool no longer returns
   (`TestTheServedRelationsListSchemaAdvertisesTheEndpointRefs` pins
   both); and the REST mirror, which faces the same question for the
   graph view and answers it from the same code.

   A missing endpoint is reported as an *absent* ref, not an empty one:
   the endpoint read happens after the page was listed, so an entity
   removed in between leaves an edge whose endpoint no longer resolves,
   and a ref with empty strings in it would render as a row named "".

**What else changed from the plan.**

4. **The plan's own `schema_violation` test asserted the wrong code.**
   Its `TestRESTSchemaViolationReturns422` declares a field schema with
   an optionless `enum` and expects `schema_violation`. That is
   `invalid_schema` — Task 3's correction 25 split the three codes
   precisely so a caller knows whether to fix the declaration, the
   values, or the argument, and an optionless enum is a broken
   declaration. Both halves are now pinned separately
   (`TestRESTDeclaringABrokenSchemaIsInvalidSchema` and
   `TestRESTAValueThatDoesNotFitItsSchemaIsSchemaViolation`), each
   asserting the code *and* the field path, never only the status.

5. **The REST handlers call the MCP tools' own implementations.** The
   plan's snippet had them call `s.opts.Metamodel` directly and build
   their own payloads, which would have been a second copy of every
   input conversion and every output shape — the drift this project has
   found in nine consecutive tasks, invited in by construction. Instead
   each of the sixteen `MCP*` functions was split in two: the exported
   one does `requireScope` and delegates; the unexported core does the
   work. REST decodes into the same input struct, calls the same core,
   and answers with the same output struct. One implementation, one wire
   vocabulary, two admission checks — `requireScope` for a token,
   `requireProject` for a person — because those are the only thing the
   two surfaces do differently.

6. **`writeDomainError` maps more than the plan listed, and never
   reports something fixable as `internal_error`.** The plan's version
   had no arm for `invalid_schema`, `invalid_input` or `retryable`, and
   sent `ErrScopeViolation` — which is a `*MCPError`, not a sentinel —
   through `errors.Is`. It is now the REST twin of `mcpErrorFor`, arm for
   arm and matched the same way: 400 for `invalid_input`, 422 for
   `invalid_schema`/`schema_violation`/`endpoint_type_mismatch`, 409 for
   `version_conflict` (carrying `current_version`) and `in_use`, 404 for
   `not_found`, 503 for `retryable`, 500 only for what nobody planned
   for — and that arm logs.

7. **A viewer cannot write.** The MCP surface never had to decide this: a
   token is editor-equivalent by construction. A session caller's role is
   real, and `requireEditor` gates every write on the REST surface,
   naming the caller's actual role in the refusal so a quietly demoted
   designer has something to act on. `TestRESTWritesAreRefusedToAViewer`
   covers three of the eight write routes — which a Round 3 review found
   was three too few, and which correction 16 replaces with a mechanism
   and a test over every write route the server registers. Read this
   paragraph as the intent; correction 16 is what shipped.

8. **A `project_id` in a REST body is a confirmation, exactly as
   `ScopedArgs` says it is on the MCP surface.** Disagreeing with the
   URL, the call is refused with `scope_violation`; agreeing, it is
   accepted; absent, nothing happens. Silently letting the URL win would
   tell a client that had lost track of which game it was editing that
   its write succeeded — in the other game.

9. **A game-content body gets its own 4 MiB bound.** `decodeJSONBody`'s
   16 KiB is right for a login and refuses an ordinary seed batch as
   malformed. `decodeJSONBodyLimit` shares the media-type check, the
   `MaxBytesReader` and both refusals, so the two surfaces cannot answer
   a too-large body differently.

10. **Upserts answer 200, not 201.** The route is idempotent by key and
    the same request may create or edit, so a status claiming "created"
    would be wrong half the time; the answer carries the row's version,
    which is what a client actually needs. A partial batch also answers
    200 with its report — nineteen of twenty rows landing is not a failed
    request, and the failures are in the body with their index, key and
    code.

11. **The home page is a catalogue with counts, and that is a bound
    rather than a stage.** `GET /api/games/{game}/summary` answers with
    one row per declared type — the game's vocabulary, a handful of
    hand-written rows — each carrying its entity or relation count, plus
    three totals. Four queries, none of which grows with the game's
    content: the two type listings and two grouped counts
    (`CountEntitiesPerType`, `CountRelationsPerType`). So a game holding
    four hundred entities renders exactly as fast, and as small, as one
    holding four, and the page never has a page-boundary problem to
    solve. Paging content belongs to the views sub-project.

    The plan's own snippet for `game.html` listed the type keys with no
    counts and re-fetched `GET /api/games` to find the game's name; the
    name lookup stayed (there is still no server-side slug resolution on
    `/g/{slug}`), the bare key list did not.

    **What a new game shows:** the game's name, "No content yet.", and
    two empty states — one explaining that a game declares its own entity
    types and that an agent over MCP is what declares them today, one
    saying the same for relation types. Both say plainly that nothing on
    this page creates one, because nothing does. **What a game with four
    hundred entities shows:** the same page, with one row per type
    carrying its label, its key and its count, and a totals line reading
    e.g. "401 entities · 12 relations · 3 no longer fit their type" —
    the last clause only when there are any, so the one number that asks
    a designer to do something is never buried in a permanent zero.

12. **`entity_types` and `relation_types` are always arrays, never
    `null`.** A page iterating "the types this game has" must not have to
    tell "none" from "the server said nothing" — the same rule Task 7's
    correction 19 applied to batch answers.

13. **A third Node harness covers the page itself.**
    `jstest/game_summary_test.mjs` drives the real, unmodified `app.js`
    through a stubbed DOM and a stubbed `fetch`, and pins four things a
    Go test of the API underneath cannot see: a crafted label reaches the
    DOM as text and never as markup; a count of one is spelled in the
    singular; an empty game gets its empty states rather than two blank
    lists; a failed summary leaves the server's own message on screen
    rather than an empty catalogue that reads exactly like a game with
    nothing in it. It also asserts the page issues exactly two requests
    and that neither is a listing, which is the property that makes the
    page a summary at all.

14. **Single-game navigation needed nothing.** `handleRoot`
    (`api_projects.go`) already redirects a caller with exactly one game
    straight into it, and the picker's remembered-game shortcut already
    covers the second one; this task only had to not break either.

15. **A token caller may use the REST content routes for its own game.**
    `mcp.go` claimed "the REST surface is deliberately closed to token
    callers", and that was only ever true of the routes that decide who
    holds standing in the product — games, membership, tokens, invites,
    all gated by `requireHumanCaller`. A token *is* a credential for
    exactly one game's content, so refusing it on the content mirror
    would deny over REST what the same token already does over MCP, for
    no security gained. The binding still holds, in `requireProject`:
    the game in the URL must be the game the token is bound to
    (`TestATokenMayReadItsOwnGamesContentAndNoOthers`). The stale
    sentence in `mcp.go` now names which routes it means.

**Round 3 review (the REST mirror and the game home page).** Seven
findings and one thing recorded rather than fixed. Every test below was
watched fail before its fix landed.

16. **Role enforcement is a mechanism now, not a convention.**
    `requireEditor` was a line each write handler had to remember, and
    the review proved what that costs: the call was stripped from five of
    the eight write handlers and `go test ./internal/web/` stayed green,
    because correction 7's `TestRESTWritesAreRefusedToAViewer` exercised
    three routes by hand. The content surface now registers through
    `registerContentRoute` (`server.go`), which reads the method out of
    the pattern and wraps every non-GET route in `requireEditor` itself;
    no handler in `api_metamodel.go` calls it any more. Two tests close
    both halves: `TestEveryContentWriteRouteRefusesAViewer` drives a real
    viewer at every write route the server registered, read back from the
    routing table with a subtest each, so a route added tomorrow is
    covered without anyone editing the test; and
    `TestEveryContentRouteIsRegisteredAsContent` fails on a content path
    registered through `registerProjectRoute` instead — the same shape as
    `TestEveryGameScopedRouteGoesThroughRequireProject`, which is what
    the Core did with the analogous problem.

17. **An unrecognised `invalid` filter is refused, not inverted.**
    `?invalid=maybe` returned only the *valid* rows — the exact opposite
    of what the page's invalid count sends a designer looking for.
    `queryBool`'s leniency rests on a flag having exactly two meanings,
    and `invalid` has three (absent, true, false), so it now goes through
    `queryTriState`, which accepts both spellings of both sides and
    refuses anything else as `invalid_input` at path `invalid`.
    `TestTheInvalidFilterIsTriStateAndRefusesAnythingElse` pins the
    refusal and both directions of the parsing; nothing had tested either.

18. **The `project_id` confirmation is one rule shared by both surfaces.**
    Correction 8 claimed the REST check was "exactly as `ScopedArgs` says
    it is" and it was not: `{"project_id":"not-a-uuid"}` answered
    `403 scope_violation` with "names a different game than the URL",
    which is false — it names no game — and `{"project_id":""}` was
    accepted outright where MCP refuses it. `statedProjectProblem`
    (`mcp.go`) is now the single judgement both surfaces call; each still
    writes its own message, because "this token is bound to another game"
    means nothing to a designer holding a session cookie.
    `TestAStatedProjectIDIsJudgedTheWayTheMCPSurfaceJudgesIt` pins all
    four cases.

19. **A wrong-typed field is named.** `{"items":"not-an-array"}` answered
    `bad_request` / "malformed JSON body" — false, since the JSON parsed,
    and unactionable, since the caller learned neither which field nor
    what was expected, on a surface where every other refusal carries its
    path. `decodeJSONBodyLimit` now reads `*json.UnmarshalTypeError` and
    answers with the field and the JSON type it wanted, in the same
    `{"fields":[{"path","message"}]}` shape the domain's own errors use.
    A genuinely malformed body still says so.

20. **`writeDomainError` is the twin it claims to be.** It had no
    `projects.ErrProjectNotFound` arm (unreachable through a route, but
    the claim was still false), and its retryable message dropped the
    second sentence Task 7 added on purpose, so a browser client was told
    to keep resending a request that will fail every time.
    `TestWriteDomainErrorIsTheRESTTwinOfMCPErrorFor` now runs every error
    through *both* functions and fails if the codes differ, and
    `TestTheRetryableAdviceIsTheSameOnBothSurfaces` pins the advice.

21. **Four argued decisions are pinned.** Each of these mutations had
    survived the whole suite: `maxContentRequestBodyBytes` cut to 16 KiB
    (`TestASeedSizedBatchIsAccepted` sends a 45 KB seed and checks every
    row landed); `hasRelatedTo` inverted to "all four"
    (`TestAnIncompleteTraversalIsRefusedAndNeverAnsweredWithTheWholeGame`
    — under the mutation a one-part traversal answered 200 with the whole
    game, exactly what that function's comment argues against); `in_use`
    answering 400 (`TestRemovingATypeStillInUseIsAConflict`, which also
    removes the type with `?cascade=true` so the conflict is about the
    entities and not the route); and `statusForCode`'s default lowered to
    500 (`TestStatusForCodeDefaultsToUnprocessable`, a direct unit test,
    because every code the parsing layer produces today is mapped
    explicitly and the default arm is unreachable through a request).

22. **Two page nits.** `styles.css` had `#a4262c` typed twice, in the
    forms' `.error` and in the home page's invalid-row line, while its
    own comment said the second was "the error colour the forms use, not
    a colour of its own"; both now read `var(--danger)`. And
    `game.html`'s empty state said an MCP agent "is what declares them
    today", which correction 15 had already made untrue — the REST
    content routes take tokens and sessions — so the UI was implying
    something the code contradicts, in front of a designer. It now says
    types are declared through the instance's API, from an agent over MCP
    or over the game's content routes, and that nothing on this page
    creates one yet.

23. **Recorded, not fixed: one traversal permutation does not name its
    own path.** `?related_to.direction=outgoing` alone answers
    `404 not_found: no relation type "" in this game`, because the domain
    resolves the relation type before it checks anything else. The other
    three one-part permutations answer `400 invalid_input` at
    `related_to.direction`. `handleListEntities`' comment used to claim
    all four came back at their own path; it now says what is true, and
    the test pins all four answers as they are.

    **Round 4 correction: the parity sentence this entry shipped with was
    itself false, and Round 4 fixed the divergence rather than the
    sentence.** It read "the MCP surface answers identically — the core
    is shared". It does not, and it never did. `RelatedToInput`
    (`mcp_metamodel.go`) carries no `omitempty` on any of its four
    fields, so all four are `required` in the tool's served schema and
    the SDK's validator refuses an incomplete traversal — naming the
    absent properties — *before* the core is called. REST reached the
    domain and answered whatever its resolution order produced, so the
    two surfaces diverged on all four permutations, and the one REST
    answer this entry set out to record accurately was one MCP never
    produces. See Round 4, correction 2: REST refuses an incomplete
    `related_to` itself now, naming every missing part, and the parity
    claim is true because the code makes it so.

**Proved by breaking the code under them.** Every test named above was
watched fail with the fix removed, not merely watched pass: the project
filter stripped out of `ListEntitiesByIDs` (another game's entity
resolved); `endpointRefs`' source dropped (the ref came back nil);
`requireEditor` lowered to `roles.Viewer` (a viewer wrote a type);
`checkStatedProject`'s comparison loosened (another game's id was
accepted); the `FILTER (WHERE invalid)` replaced with `0` (the invalid
count went to zero); the two `make(...)` calls in the summary removed
(the arrays came back `null`); `countLabel`'s singular removed ("1
entities"); and the failed-summary message suppressed (the page went
blank).

Round 3's own mutations, each watched red and then reverted: the
`requireEditor` wrap removed from `registerContentRoute` (all eight write
subtests failed, where the old three-route test had caught nothing when
five handlers lost the check); one content route re-registered through
`registerProjectRoute` (the convention test named it); `"false"` parsed
as true and the unrecognised-value refusal replaced by `false` (the
tri-state test failed on each); `checkStatedProject`'s `bad_request` arm
removed; the wrong-typed-field arm disabled; the
`projects.ErrProjectNotFound` arm disabled and the retryable second
sentence reworded; `maxContentRequestBodyBytes` cut to 16 KiB;
`hasRelatedTo` inverted to "all four"; `in_use` answering 400; and
`statusForCode`'s default lowered to 500.

Every run of every test above had `TEST_DATABASE_URL` set, confirmed by
counting skips: 0 with it, 183 without.

**Round 4 review (the same surface, a fourth pass).** The role mechanism
held under it — no pattern shape got past `registerContentRoute`, GET is
genuinely unwrapped, and a lowercase method is dead on the mux. What
follows is the residue: one convention test that could be escaped, one
parity claim that was false in the commit that set out to stop overstating,
and a family of refusals that were wrong or silent about the caller's own
input.

1. **The convention test's allowlist is inverted.**
   `TestEveryContentRouteIsRegisteredAsContent` held a hand-written list
   of the segments that name game content and checked only those. The
   review proved the escape: registering
   `POST /api/games/{game}/views` through `registerProjectRoute` left
   *both* convention tests green and let a viewer write. The `checked ==
   0` guard catches a list gone wholly stale, never a single missing
   entry — and `views` is not hypothetical, since `api_metamodel.go`'s
   own header and `app.js` both name the views sub-project as the next
   thing built on this surface.

   The closed set is the other one. Under `/api/games/{game}/` this
   instance has four standing sub-resources of its own — `members`,
   `tokens`, `invites`, `events` — gated by owner/admin checks rather
   than by `requireEditor`, plus the bare game, which carries no trailing
   segment. Everything else under that prefix is game content **by
   default** and must go through `registerContentRoute`. A genuinely
   non-content sub-resource is added to `notContent` deliberately, in the
   same commit that registers it. Proved by re-running the `views`
   experiment: the inverted test names it, the other two stay green.

2. **The MCP parity claim was false, so the divergence was fixed.**
   `api_metamodel.go`, `api_metamodel_test.go` and Round 3's own entry 23
   all claimed the two surfaces answer the four one-part traversals
   identically because the core is shared. The core is shared; the
   question the two surfaces asked it was not. `RelatedToInput` has no
   `omitempty` on any field, so all four parts are `required` in the
   served schema and the SDK's validator refuses
   `{related_to:{direction:"outgoing"}}` with
   `required: missing properties: ["relation_type_key" "entity_type_key"
   "entity_key"]` before the core runs, while REST reached the domain and
   answered `404 not_found: no relation type "" in this game`.

   `queryRelatedTo` now decides completeness in the REST handler, the
   same place the MCP schema decides it, and refuses an incomplete
   traversal with `400 invalid_input` naming **every** missing part at
   its own path. A part written with no value (`?related_to.direction=`)
   is present and missing both, so it is listed among the missing rather
   than refused on its own — one answer naming everything absent beats
   four naming one thing each. All three claims were corrected to say
   what the code now does.

3. **The wrong-typed-field message was false for numbers.** The commit
   that replaced "malformed JSON body" with a named field and type
   introduced `"must be " + jsonTypeName(...) + ", not " + Value`, and
   `encoding/json` sets `UnmarshalTypeError.Value` to `"number
   <literal>"` when the JSON kind was right and the value was not. So
   `{"expected_version": 999999999999}` was answered
   `expected_version must be a number, not number 999999999999` — the
   library's own wording leaked, and the sentence denies that a number is
   one. Both branches are reachable live from `POST /types` (too wide for
   int32, and `1.5`). `wrongTypeProblem` now says what is actually wrong
   — the width or the fraction — with the field's real bounds in it:
   `must be a whole number between -2147483648 and 2147483647, not
   999999999999`. The object, list and string cases were correct and are
   unchanged, nested paths included.

4. **Three more in the same family.**

   - **A body of the wrong shape entirely.** `[]` or `"just a string"`
     carries no field for `encoding/json` to name, so the
     `wrongType.Field != ""` guard dropped through to "malformed JSON
     body" for well-formed JSON — the same defect one branch along.
     Naming no path is right; calling it malformed is not. It now reads
     `the request body must be a JSON object, not array`. `null` is
     deliberately not in this family: unmarshalling it into a struct is a
     no-op, so it reaches the domain as an empty body and is refused
     there, field by field, which is the right answer for it.
   - **`?limit=999999999999` said "limit is not a number".** It is a
     number; it does not fit the `int32` the field is, and a caller told
     their number is not one has nowhere to go. `strconv` reports range
     and syntax through one error, so `queryLimit` now separates them and
     the range case carries the bounds. `?limit=lots` still says it is
     not a number.
   - **Data after the JSON body was silently discarded.**
     `json.Decoder.Decode` reads one value and stops, so
     `{"key":"a"}{"key":"b"}` answered 200 having written only the first
     — on the surface whose stated rule is that nothing a caller wrote is
     silently ignored. `decodeJSONBodyLimit` checks `dec.More()` now, so
     both surfaces that share it (`/api/auth/login` included) refuse a
     body carrying more than one value, and the test confirms the first
     value does not land while the second is refused.

5. **Repeated and empty query parameters.** Two live holes on the same
   surface that had just refused an unrecognised `invalid` spelling.
   `?invalid=true&invalid=false` took the first and dropped the second
   without a word, and `?invalid=true&invalid=garbage` answered 200
   having never looked at the garbage — the one remaining path on which
   an unrecognised spelling of `invalid` was accepted, which is exactly
   what refusing it existed to close. `?invalid=` read as absent and
   answered with every row, while this same task refuses an empty
   `project_id` on the ground that an empty confirmation confirms
   nothing.

   Every query parameter this surface reads now comes in through one
   door, `querySingle`, and **the rule is stated as: a parameter the
   caller wrote must appear exactly once and must carry a value; only an
   absent parameter is absent.** Repetition is refused because
   `url.Values` keeps every value and reading the first is a silent
   choice between two things the caller asked for. An explicitly empty
   value is refused for the same reason `checkStatedProject` refuses an
   empty `project_id`: an empty filter filters nothing, and answering the
   whole listing to a designer whose client dropped the value of
   `invalid` is this surface's own "wrong answer that looks like a right
   one". So the query string does **not** get a different rule from the
   body — it gets the same one, and that is the answer to "decide and
   state which rule applies".

   The one deliberate cost is the bare-flag idiom: `?verbose` and
   `?cascade` reach Go as written-and-empty and are now refused rather
   than read as `true`. Refusing is the safe direction for a parameter
   one of whose callers is a cascading delete, and the caller is told
   exactly what to write. `queryBool` stays lenient about *spelling*,
   which was always the only thing its leniency argument covered.

6. **The empty state implied an action a viewer cannot take.** Round 3
   changed it to say types are declared over MCP or the game's content
   routes. True for an editor; a viewer sees the same sentence and can do
   neither, and telling someone to do the one thing the server will
   refuse is worse than telling them nothing. `GET /summary` now carries
   the caller's own `role` — free, since `requireProject` has already
   resolved it — and `app.js` writes the second half of the sentence from
   it: an editor is told how to declare the first type, a viewer is told
   that this instance will refuse a write from them and who to ask.
   `role` is never a permission; every refusal is still the server's,
   made again on the next request.

7. **Recorded, and one of the three fixed after all.**

   - **A route registered straight on `s.mux` was invisible to both
     convention tests**, because they walk `s.registeredPatterns`, which
     only `route()` populates — the remaining "one step along" for the
     claim that the editor gate is impossible to forget. This was flagged
     as record-only, with the source-grep test left as a judgement call.
     The judgement went the other way: `TestOnlyRouteTouchesTheMux` reads
     this package's own non-test sources and allows `s.mux.Handle`
     exactly once, inside `route()`. It is worth its bluntness because
     the grep is narrow (one method on one field), the failure explains
     itself, and the alternatives — an accessor, or a mux wrapper type —
     buy the same property with indirection in the one file that most
     needs to stay readable. Its cost is honest and recorded in its own
     doc comment: renaming `route()` or the `mux` field breaks it.
     Proved by re-running the `views` experiment through `s.mux.Handle`
     directly: both convention tests pass, this one names the file and
     line.
   - **`statusForCode` has no `errCodeBadRequest` arm**, so an
     `*MCPError` carrying it would be reported as 422. Unreachable today
     — nothing constructs one — but the default arm is now pinned as 422
     by a test, which makes the gap durable rather than transient.
     Recorded, not fixed: adding an arm for a code no constructor
     produces is dead code that reads as coverage.
   - **`PUT`/`PATCH`/`OPTIONS` on a content path** get the mux's
     plain-text `Method Not Allowed` rather than this surface's
     `{"error","message"}` envelope. Whole-API and pre-existing; it
     belongs to whatever task decides the envelope for method and route
     mismatches everywhere, not to this one.

**Proved by breaking the code under them.** Every test named above was
watched fail before its fix and pass after, with `TEST_DATABASE_URL` set
on every run and confirmed by the baseline subtests appearing in `-v`
output: a `views` route registered through `registerProjectRoute` (the
inverted convention test named it while the other two stayed green); the
same route registered straight on `s.mux` (only `TestOnlyRouteTouchesTheMux`
named it); the four one-part traversals and the empty-valued part (each
answered 200, a 404 or a single path before `queryRelatedTo`); a
too-wide and a fractional `expected_version` (each answered "not number
<literal>"); `[]`, `"just a string"` and `42` as whole bodies (each
answered "malformed JSON body"); two JSON values in one body (answered
200, having written the first); `?limit=999999999999` (answered "not a
number"); the repeated and empty parameters, one subtest each (each
answered 200 with a listing); and the viewer's empty state (the
role-aware branch short-circuited to the editor's sentence, and the
browser test named it).

---

### Task 9: End-to-end seeding of a real game

**Files:**
- Create: `internal/web/seed_e2e_test.go`

This task proves the spec's definition of done: an agent declares a real game's
types and seeds hundreds of rows through the same surface it will use in
production.

- [x] **Step 1: Write the test**

`internal/web/seed_e2e_test.go`:

```go
package web_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/web"
)

// TestSeedARacingGameEndToEnd walks the whole surface the way a seeding agent
// does: declare types, declare relation types, bulk-write entities, wire the
// edges, then read it back.
func TestSeedARacingGameEndToEnd(t *testing.T) {
	_, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, "designer@studio.com", "Designer", "password12345", false)
	project, _ := projSvc.Create(ctx, "le-mans", "Le Mans", user.ID)
	token, _, _ := ids.CreateAPIToken(ctx, project.ID, user.ID, "seed agent")
	caller, _ := web.CallerForToken(ctx, ids, token)
	deps := web.MCPDeps{Identity: ids, Projects: projSvc, Metamodel: newMetamodelService(t)}

	for _, spec := range []web.TypesUpsertInput{
		{ProjectID: project.ID, Key: "circuit", Label: "Circuit", LabelPlural: "Circuits",
			Schema: metamodel.Schema{{Key: "length_km", Type: metamodel.FieldNumber}}},
		{ProjectID: project.ID, Key: "car", Label: "Car", LabelPlural: "Cars",
			Schema: metamodel.Schema{{Key: "power_hp", Type: metamodel.FieldNumber, Required: true}}},
		{ProjectID: project.ID, Key: "race", Label: "Race", LabelPlural: "Races",
			Schema: metamodel.Schema{{Key: "laps", Type: metamodel.FieldNumber, Required: true}}},
	} {
		if _, err := web.MCPTypesUpsert(ctx, deps, caller, spec); err != nil {
			t.Fatalf("declare %s: %v", spec.Key, err)
		}
	}

	// Two hundred races: the scale a real seeding pass runs at.
	items := make([]metamodel.EntityInput, 0, 200)
	for i := 0; i < 200; i++ {
		items = append(items, metamodel.EntityInput{
			TypeKey: "race",
			Key:     fmt.Sprintf("race-%03d", i),
			Name:    fmt.Sprintf("Race %03d", i),
			Fields:  map[string]any{"laps": float64(10 + i%40)},
		})
	}
	out, err := web.MCPEntitiesUpsert(ctx, deps, caller, web.EntitiesUpsertInput{
		ProjectID: project.ID, Mode: string(metamodel.BulkAtomic), Items: items,
	})
	if err != nil {
		t.Fatalf("bulk seed: %v", err)
	}
	if out.Written != 200 {
		t.Fatalf("Written = %d, want 200", out.Written)
	}

	// Re-running the same seed is idempotent in row count, and every row is a
	// conflict because the caller passed no expected_version.
	rerun, err := web.MCPEntitiesUpsert(ctx, deps, caller, web.EntitiesUpsertInput{
		ProjectID: project.ID, Mode: string(metamodel.BulkPartial), Items: items,
	})
	if err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	if len(rerun.Failed) != 200 {
		t.Fatalf("a re-seed without expected_version must conflict on every row, got %d failures", len(rerun.Failed))
	}

	page, err := deps.Metamodel.ListEntities(ctx, project.ID, metamodel.EntityFilter{TypeKey: "race", Limit: 500})
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(page.Entities) != 200 {
		t.Fatalf("the game holds %d races, want 200", len(page.Entities))
	}
}
```

- [x] **Step 2: Run it**

Run: `go test ./internal/web/ -run TestSeedARacingGame -v`
Expected: PASS.

- [x] **Step 3: Verify by hand against a running instance**

```bash
docker compose up -d --build
# Log in, create a game and mint a token as in the Core plan's Task 16, then:
curl -fsS -X POST http://localhost:8080/api/games/<id>/types \
  -H "Authorization: Bearer <token>" \
  -H 'Content-Type: application/json' \
  -d '{"key":"quest","label":"Quest","label_plural":"Quests","field_schema":[{"key":"min_level","type":"number","required":true}]}'
```

Expected: 201 with the new type, and the game page lists `Quests (quest)` after
a refresh.

- [x] **Step 4: Commit**

```bash
git add internal/web/seed_e2e_test.go
git commit -m "test: end-to-end seeding of a game through the mcp surface"
```

**Corrections made during implementation** (a pass over the landed test,
and over what seeding a real game found in the eight tasks under it):

1. **The skeleton above does not compile against the surface that
   shipped, and the differences are all Task 7's.** `MCPTypesUpsert` and
   its fifteen siblings take the resolved `projectID` as their own
   parameter rather than reading one out of the input struct; every
   input embeds `ScopedArgs`; a type's schema is `[]web.FieldInput`, not
   `metamodel.Schema`, because the SDK infers a tool's input schema by
   reflection and a domain type cannot cross that boundary; a bulk
   batch's rows are `web.EntityItemInput`, not `metamodel.EntityInput`,
   for the same reason plus the actor argument in `mcp_metamodel.go`'s
   header; `identity.CreateUser` and `CreateAPIToken` take request
   structs; and the helper is `newMetamodelTestServer`, which hands back
   the metamodel service the skeleton's imagined `newMetamodelService`
   would have built. The landed test is written against what shipped.

2. **Step 3's expected `201` is wrong; the route answers `200`.** Task 8
   decided that an upsert answers 200 even when it creates, because the
   call is idempotent by key and a caller cannot tell the two apart
   without a race. Verified by hand against a real instance: a fresh
   binary on a throwaway database, an owner session, a minted token, and
   the step's own `curl` verbatim — `200` with the new type, and the
   game's home page listing `Quests (quest)` after a refresh, exactly as
   the step says apart from the number.

3. **Two hundred rows was not enough of a test, and the shape mattered
   more than the count.** The landed seed is 511 entities over seven
   entity types and 1,044 edges over eight relation types, chosen so that
   every shape the metamodel supports is exercised by content rather than
   by a unit test: a required field, an enum, a `list<text>`, a bounded
   number, three declared defaults (one of them `false`), a `longtext`, a
   self-referencing relation type used twice, a relation type with no
   endpoint rules at all, an entity type no relation type names, and one
   deliberately dense hub — 120 of the 200 races run at one circuit — so
   that a traversal has more neighbours than a page can hold.

4. **The claims the sub-project made along the way all held**, each
   pinned by its own subtest: a re-seed is idempotent in row identity and
   conflicts on every row without an `expected_version` (200 of 200,
   every one `version_conflict`, no id or version moved); a bulk batch
   reports every row it landed with its id and version, and every one of
   the 200 reports addresses the row a fresh listing returns; a schema
   change flags rows without back-filling or rejecting them, and without
   moving their versions; search finds a name, a tag inside a
   `list<text>` and a word buried in the middle of a long description,
   and a name match outranks a body match that repeats the word forty
   times; a traversal pages, and the dense hub's 120 neighbours come back
   across three pages with no row seen twice; and the home page renders
   the game's real counts.

5. **A schema edit that flags rows has no bulk repair.** `Check` refuses
   a field that is both `required` and defaulted — correctly, it is a
   self-contradiction — so once a required field is added under two
   hundred rows, nothing a designer can write into the *type* makes them
   fit again. The only recovery is to rewrite every flagged row, each
   carrying its own `expected_version`; and taking the field back out
   flags all two hundred a second time, because the value they now carry
   has become an unknown field. The test walks the whole four-step loop,
   because it is what the skill bundle will have to teach.

6. **An edge's field values cannot be read back.** A relation type may
   declare a field schema, the values are validated on write and stored,
   and no tool on either surface returns them: `RelationOutput` carries
   no fields, there is no `relations.get`, and `relations.list` is the
   only way to see an edge. This is the largest gap the seed found — a
   whole declared feature is write-only — and it was pinned as an
   expectation to delete rather than left in prose.

   **Closed by Metamodel 12**, whose corrections block sits at the end
   of Task 5, where the schema and its validation were built. The pin
   itself turned out to pass for the wrong reason — it listed without
   `verbose`, so it stayed green through the fix — and is replaced by
   the read-back it stood for.

7. **Search is unconditionally verbose and search does not index keys.**
   `entities.list` defaults `verbose` off, arguing that five hundred rows
   with their fields is the whole game back in one answer; `search` has
   no such argument and returns every hit's whole field payload,
   `longtext` included, up to its 200-row cap. Measured over the wire
   against rows carrying 25 KB of lore each, one 60-hit search answered
   with 1.6 MB of JSON. Separately, the search vector indexes names and
   text values but not row keys, so a designer who types the handle they
   see on every other screen gets nothing back.

8. **Recorded, not fixed.** Nothing on the MCP surface counts, so "how
   many races are there" is a full paged walk while the REST home page
   has the number; `entities.remove`, `relations.remove` and
   `relations.list`'s two endpoint filters address rows by uuid while
   every other tool speaks `(type_key, key)`, so an agent pays a
   resolving read; a relation type states its endpoints as entity type
   ids, so a second seeding session has to call `types.list` and build
   the key-to-id map before it can declare one; and a batch has no size
   bound at all — a 5,000-item atomic batch was accepted over the wire,
   ran in one transaction in 3.1 s and answered with 515 KB.

**Verified against a running instance.** A real binary on a throwaway
database (created and dropped), an MCP session over HTTP with a project
token: `initialize`, `tools/list` (19 tools), `types.upsert`, a 200-row
atomic `entities.upsert`, the same batch again in partial mode (200
`version_conflict` failures, nothing written), `entities.list`, `search`
and a `related_to` traversal with `direction` omitted — refused by the
SDK's own input-schema validation, which is where Task 7's decision to
leave the field without `omitempty` lands. Every number above was read
off that session, not inferred.

---

### Task 10: `on_conflict` on the bulk upserts

**Status: not started.** This task exists because a decision was taken,
not because a defect was found. Core open question O2 in
`docs/superpowers/specs/2026-08-31-core-and-metamodel-design.md`,
"Idempotency", was **decided on 2026-09-02**: a re-seed should be one
call rather than a read-then-write loop, so `entities.upsert` and
`relations.upsert` gain a conflict mode. Nothing below is implemented
yet, and until it is, a verbatim re-run of a seeding payload still
conflicts on every row that already exists — which Task 9 asserts and
which stays correct until this task lands.

**Files:**
- Modify: `internal/metamodel/bulk.go`, `internal/metamodel/entities.go`,
  `internal/metamodel/relations.go` (the write paths that resolve an
  existing row and compare `expected_version`)
- Modify: `internal/web/mcp_metamodel.go` and `internal/web/api_metamodel.go`
  (the tool and route inputs)
- Modify: `internal/metamodel/bulk_internal_test.go`,
  `internal/web/mcp_metamodel_test.go`, `internal/web/seed_e2e_test.go`

**The shape.** `entities.upsert` and `relations.upsert` take an optional
`on_conflict` of `"fail" | "skip" | "overwrite"`, single item and batch
alike:

- `"fail"` — **the default**, and exactly today's behaviour: a row that
  exists and whose `expected_version` is absent or stale is a
  `version_conflict`. Nothing changes for any existing caller, and an
  omitted argument must be indistinguishable from today.
- `"skip"` — a row that already exists is left untouched and reported as
  skipped, not as a failure. This is what makes a re-seed genuinely
  idempotent.
- `"overwrite"` — a row that already exists is written regardless of its
  version. This is a documented way to lose a concurrent editor's work,
  which is the failure `expected_version` exists to prevent, so it is
  opt-in per call and never a default.

**What the design decision requires the implementation to honour:**

- [ ] **Step 1: `on_conflict` and `expected_version` interact
  explicitly, not by accident.** Decide and test one rule: an item
  carrying an `expected_version` *and* `on_conflict: "overwrite"` is
  either a rejection (`invalid_input`, the two arguments contradict) or
  the version wins. Whichever is chosen, it is stated in the tool
  description; the one unacceptable outcome is that a caller's explicit
  version is silently ignored.
- [ ] **Step 2: a skipped row is a third outcome in the result
  envelope**, distinct from a landed row and from a failure. `partial`
  and `atomic` both need it, and a skip must not make an `atomic` batch
  roll back — a re-seed of two hundred rows where all two hundred are
  skipped is a success with nothing written.
- [ ] **Step 3: the reported outcome addresses the row.** A skipped item
  still comes back with its key, its id and its current version, because
  the caller's next act is frequently to correct the rows it skipped.
- [ ] **Step 4: the SSE events match what happened.** A skipped row
  publishes nothing; an overwrite publishes the same update event an
  ordinary write does.
- [ ] **Step 5: Task 9's end-to-end seed grows a re-seed subtest.** The
  same two hundred rows, re-sent with `"skip"`, land zero writes, zero
  failures, two hundred skips and no version moved; re-sent with
  `"overwrite"`, land two hundred writes and two hundred bumped
  versions. Task 9's existing assertion — a re-seed with no
  `on_conflict` conflicts on every row — stays, unchanged, as the proof
  that the default did not move.
- [ ] **Step 6: update the specs and the bundle.** The core spec's
  "Idempotency" note drops its "not implemented" status;
  `2026-09-02-agent-skill-bundle-design.md` §6 and §10.1 drop the
  pending-implementation marking and delete the interim read-then-write
  loop from `recipes/seeding-a-game.md`.

---

### Metamodel 10: relations have no invalid flag and no re-validation path

**Status: done.** Migration `0009_relation_invalid_and_version.sql` plus
the write path, the sweep, the read surfaces, the views compiler and both
API surfaces.

**The gap.** A relation type carries a `field_schema` exactly as an
entity type does, and `upsertRelationWith` had always validated an edge's
values against it. What it could not do was *re-judge* them: the core
spec's schema-evolution rule — flag the rows rather than reject the edit
or back-fill the data — was implementable for entities only, because
`relations` had no column to record a verdict in. Editing a relation
type's field schema therefore left every existing edge silently
unchecked: no sweep could mark them, no listing could find them, no count
could report them, and a validated edge was indistinguishable from one
that had never been judged. Task 5's own self-review had already recorded
that Metamodel 12 strengthened the case for closing this from cosmetic to
substantive: once `relations.get` and a verbose `relations.list` could
hand a caller an edge's values, they could hand back values that no
longer satisfied the schema they claim to answer to, with nothing in the
answer saying so.

**The decision, taken before the work started: relations get the same
treatment as entities, `invalid` *and* `version`.** Three reasons. The
asymmetry is not defensible when both tables carry a field schema judged
by the same `Schema.Validate`. Edge writes had no optimistic concurrency
at all, and a version gives them the compare-and-set every other write in
this repository has. And retrofitting either column after a real game is
seeded means a migration plus a sweep over every edge, so the two travel
together rather than in two passes over the same table.

**Sharing rather than copying was a requirement, not a preference.** This
repository's most repeated defect is a rule written once, copied, and
then fixed in one copy. What is shared:

- `internal/metamodel/revalidate.go` holds the schema-evolution rule
  itself — parse the schema, list the stored values, partition on
  `CheckValues`, write *both* verdicts through an `invalid <> flag`
  guard. `revalidateEntitiesOfType` and `revalidateRelationsOfType` are
  now four lines each: which two statements their table runs. The
  mutation that proves the sharing is real: deleting the `{valid, false}`
  batch turns **both** tables' "clears the flag when the row fits again"
  tests red from one edit.
- `TypeCounts` replaces the entity-only `EntityCounts` struct;
  `EntityCounts` and `RelationCounts` are aliases of it, so a caller
  holding one holds the other.
- `invalidFilterPart` (the cursor-fingerprint spelling of a tri-state
  filter) and `queryTriState` (the REST query-parameter parse) were
  already shared and are now called by the relation listing too, rather
  than re-spelled.

**The bound question the task asked about: the entity sweep has none, so
the edge sweep has none.** `ListEntityFieldsOfType` carries no `LIMIT`,
and `ListRelationFieldsOfType` does not either. It is stated in
`revalidate`'s doc comment rather than left implicit, with the note that
whoever bounds one bounds both — which is the point of there being one
function to bound.

**What changed, by surface.**

- **Migration.** `invalid boolean NOT NULL DEFAULT false` and `version
  integer NOT NULL DEFAULT 1`. No back-fill: both defaults are what a
  fresh write produces, and "presumed valid, never edited" is exactly the
  state every pre-existing edge was in, since the write path had always
  validated. **No new index**, and the claim is tested rather than
  asserted: `TestTheEdgeSweepSeeksAnIndexRatherThanScanning` runs EXPLAIN
  on the sweep's own statement over a game of twenty relation types.
  *The migration comment was wrong on its first draft* — it named
  `relations_edge_key` as the index the sweep would seek, and the
  measurement showed the planner picking `relations_type_target_idx`
  (0008) instead. Both lead with `relation_type_id`; the comment and the
  test now say so, and the test pins the *shape* of the plan (not a
  sequential scan) rather than a name the planner is free to choose
  between.
- **Write path.** `UpsertRelation` takes `ExpectedVersion`, reads under
  the row lock, and the DO UPDATE carries `relations.version =
  expected_version` beside its existing project guard. `invalid` resets
  to false on write, because a row that is being written is a row that
  has been judged. `conflictOnRelationEdge` re-reads a failed guard and
  reports the version to merge onto — it is `conflictOnEntityKey` minus
  the respelling arm, since an edge has no key of its own.
- **Sweep.** `UpsertRelationType` calls `revalidateRelationsOfType`
  inside its transaction, where `UpsertEntityType` calls its twin.
  `MarkRelationsOfTypeInvalid` deliberately does **not** move `version`:
  a sweep is a verdict about a row, not a new revision of it, and
  bumping it would refuse the next `expected_version` an agent is
  holding for a row whose values it never touched, over a schema edit
  somebody else made. `MarkEntitiesOfTypeInvalid` has always left the
  version alone, and the two must stay the same on this point.
- **Reads.** `RelationFilter.Invalid` (tri-state, in the cursor
  fingerprint), `RelationCountsByType` gains its invalid tally, the game
  summary's `RelationTypeSummary.invalid_count` and `totals.invalid`
  count edges as well as entities, and the game home page's relation-type
  catalogue shows the flag where it used to render a hard-coded zero.
- **MCP and REST.** `relations.upsert` takes `expected_version` and its
  description no longer promises last-writer-wins; `relations.list` takes
  `invalid` and `GET /relations?invalid=` mirrors it; `RelationOutput`
  carries `version` and `invalid` unconditionally on both the listing and
  `relations.get`, and the hand-written output schema advertises both;
  `relation_types.upsert`'s description now says that editing
  `field_schema` re-checks the type's edges.
- **Views.** Every arm of the compiler that names the `relations` table
  now excludes flagged edges unless `include_invalid` is set: a one-hop
  step's JOIN, a multi-hop walk, both `edges[]` collection points, and
  `project`'s one-hop related attribute.

**Two judgement calls inside the views change.**

1. **The walk's exclusion prunes the recursion rather than filtering its
   output**, which is the opposite of where the *node* exclusion goes.
   `walk`'s own doc comment already argued the distinction for
   `edge_where` against `to_type`: a condition on the entity a hop
   reached is applied outside, so a walk can pass *through* a node of the
   wrong type; a condition on the relation each hop walks is applied
   inside, because an edge the document excluded is an edge the walk must
   not follow. Filtering invalid edges on the way out would leave the
   nodes reached only through one of them drawn with nothing joining them
   to the picture — a floating node, which is a worse answer than either
   their presence or their absence.
2. **`@invalid` moved from the refused list to the admitted one on an
   edge scope**, in predicates and in `label_from` alike. A relation now
   has the column, so refusing it would have made the flag visible on
   half the graph — a designer could draw "the quests that no longer fit"
   and not "the edges that no longer fit". `@name` and `@key` are still
   refused; a relation still has neither, and the three messages that
   used to say "has no key, name or invalid flag of its own" now say "has
   no key or name of its own".

**One clause is redundant and says so.** `edges[].from_step` reads a
step's `via_relation`, and the step has already pruned; the filter there
changes no result and is caught by no test. It is kept, and its comment
records that, for the reason the metamodel's own redundant project
filters are kept: a statement that is safe only because of how today's
producer happens to fill its input is a trap for tomorrow's.

**Behaviour that changed for callers, deliberately.** A re-seed of an
existing edge with no `expected_version` is now a `version_conflict`
rather than a silent overwrite — exactly what a re-seed of an existing
entity has always been, and exactly what Task 10's `on_conflict: "skip"`
is being added to make ergonomic. When Task 10 lands it must cover
`relations.upsert` too, which the task's own file list already says.

**The test that was inverted.** Task 7 shipped
`TestConcurrentEditsToOneEdgesFieldsAreLostSilently`, which pinned the
loss deliberately and said in as many words that adding `version` to
`relations` would turn it red and that the correct response would be to
rewrite it. This task is that rewrite:
`TestConcurrentEditsToOneEdgesFieldsAreRefused` asserts the refusal, the
version reported, that the stored row still holds the first writer's
value (the error alone does not prove the write was refused *before* it
landed), that nothing was published, and that the merge the caller is
told to make lands on the same row.

**Mutations run, each restored afterwards.**

| Mutation | Test that went red |
|---|---|
| `invalid = false` dropped from `UpsertRelation`'s SET | `TestRewritingAFlaggedEdgeClearsItsFlag`: "the returned row still carries the flag after a validated write" |
| The SQL version guard made trivially true | `TestTheEdgeUpsertStatementRefusesAStaleVersion`: "UpsertRelation returned {…Version:2} (err = <nil>) for a version that does not match, want no rows" |
| The same, *plus* the Go locked-read check removed | `TestConcurrentEditsToOneEdgesFieldsAreRefused`: "second write: err = <nil>, want a VersionConflictError" |
| `ListRelations`' invalid clause made trivially true | `TestInvalidEdgesAreFindableThroughTheListing`: "listed 2 edges, want 1" on both filtered subtests |
| `invalidFilterPart` dropped from the relations fingerprint | `TestARelationsCursorCannotCrossTheInvalidFilter`: "err = <nil>, want invalid_input" |
| The sweep call removed from `UpsertRelationType` | six tests, including `TestAnEdgeSweepDoesNotReachAnotherGamesEdges`: "the edited game's own edge was not flagged" |
| `revalidate` writes only the invalid batch | `TestAnEdgeSchemaChangeClearsTheFlagWhenTheEdgeFitsAgain` **and** `TestSchemaChangeClearsTheFlagWhenTheRowFitsAgain` — one edit, both tables |
| The invalid FILTER dropped from `CountRelationsPerType` | `TestRelationCountsCarryTheInvalidTally`: "{Total:2 Invalid:0}, want {Total:2 Invalid:1}" |
| `hop` loses its edge filter | `TestNoArmOfAPictureDrawsAnEdgeThatNoLongerValidates`: "a step followed an edge that no longer validates: elwynn,westfall" |
| `walk` loses its edge-predicate exclusion | the same test: "a walk crossed an edge that no longer validates and drew elwynn,westfall" |
| `edges[].between` loses its filter | the same test: "a between entry drew 2 edges, want only the one that still validates" |
| `project`'s related hop loses its edge filter | the same test: "a node was coloured across an edge that no longer validates: elwynn" |
| both `relation_type_id`-leading indexes dropped | `TestTheEdgeSweepSeeksAnIndexRatherThanScanning`: "the sweep scans the whole table" |

The one mutation that did **not** go red is recorded above as such:
removing `edges[].from_step`'s filter leaves the suite green, because the
step feeding it has already pruned.

**Two tests were also corrected for the "passes for the wrong reason"
pattern.** `TestUpsertRelationsConflictPathCannotWriteAnotherGamesEdge`
drives `dbq.UpsertRelation` directly to observe the project guard on the
conflict path; with a version this caller did not know, the *version*
guard would refuse the statement on its own and leave the project filter
untested. It now sends the edge's true version, so the project filter is
the only thing left standing. `TestTheEdgeUpsertStatementRefusesAStaleVersion`
exists because the Go locked read answers every sequential
caller before the SQL guard runs — measured: making the guard trivially
true left the service-level concurrency test green — so the guard needed
a test that drives the statement.

---

### Metamodel 11: `UpsertEntity` deadlocks against a cascading entity-type removal

**Status: done.** Two locking reads (`GetEntityTypeByKeyForKeyShare`,
`GetEntityTypeByIDForUpdate`), one call site changed in each of the two
writers, two staged tests and one stress test.

**The defect, as measured.** Eight workers alternating `entities.upsert`
against `RemoveEntityType(cascade)` produced deadlocks steadily:
**15 in 15 seconds** on the harness kept as
`TestUpsertEntityAndACascadingRemovalDoNotDeadlock`, matching the profile
the item was filed with (8 in 15 s). Bisecting the operation set had
already isolated the pair — entity + entity-type writes alone: 0,
entity-type writes + removals: 0, entity writes + removals: 8 — in a game
holding no relation types at all, so `LockEndpointEntityTypes`' share
lock was never taken and this was not a consequence of that change.

**The cycle.** Two writers, two tables, opposite orders:

- `UpsertEntity` took `entities` `FOR UPDATE` (`GetEntityByKeyForUpdate`)
  and *then* `entity_types` `FOR KEY SHARE`, because the closing
  `INSERT ... ON CONFLICT` runs the `entity_type_id` foreign key and a
  foreign key locks its parent row. The type read in front of it took no
  lock at all.
- `RemoveEntityType(cascade)` took `entities` exclusively
  (`DeleteEntitiesOfType`) and *then* `entity_types` exclusively
  (`DeleteEntityType`) — whose own `ON DELETE RESTRICT` check reads
  `entities` back `FOR KEY SHARE`. Its type read in front of it took no
  lock either.

So each writer could hold what the other was waiting for. The victims
sampled from `pg_stat_activity` when the item was filed were
`DeleteEntityType` (6 of 7) and `DeleteEntitiesOfType` (1 of 7), parked
against `UpsertEntity`'s `INSERT ... ON CONFLICT` and
`GetEntityByKeyForUpdate`; the reproduction here saw the same three
statements named.

**The fix is the rule `LockEndpointEntityTypes` already established for
the other pair: both writers take `entity_types` first.** The entity
write's type read becomes `FOR KEY SHARE` — exactly the lock the foreign
key would take a few statements later, so it adds no conflict the write
did not already have, and two entity writes into one type still run
concurrently. The removal's type read becomes `FOR UPDATE`, which
conflicts with that share lock, which is the mutual exclusion that makes
the order matter: an entity write already in flight is waited for at the
*first* lock rather than met head-on at the second.

Every other writer that touches both tables was checked rather than
assumed. `UpsertEntityType` already locked `entity_types` `FOR UPDATE`
before its re-validation sweep marked `entities`, so it was already on
the right side of the rule. `RemoveEntity` locks neither: it reads
unlocked and deletes a child row, which takes no parent lock. The bulk
paths go through `upsertEntityWith` and inherit the fix. `UpsertRelation`
takes no lock on `entity_types` at all.

**Measured, and proved by inverting the order rather than by assertion.**
The same harness, same shape, 8 workers:

| build | deadlocks |
| --- | --- |
| before (both reads unlocked) | 15 in 15 s |
| after (both take `entity_types` first) | 0 in 15 s, and 0 over six further 3 s runs |
| **inverted: entity write fixed, removal reverted** | **16 in 15 s** |
| inverted: removal fixed, entity write reverted | 0 deadlocks, but 1,753 raw `23503` foreign-key violations — the write racing a removal it no longer waits for |

The third row is the proof that matters: with the lock present but taken
in the other order, the deadlocks come straight back. It is the ordering
and not the mere presence of a lock that closes this. The fourth row
records the second thing the fix bought — with the type row held, an
entity write can no longer land between a type's deletion and its own
`INSERT`, so the raw constraint violation an agent used to read as
`internal_error` is now the honest `not_found`.

At 3 seconds — the duration the kept test runs at — reverting either half
produced 3, 3, 3, 5 and 7 deadlocks over five runs, so the stress test is
red every time and is cheap enough to need no gate.

**Retryable SQLSTATEs on both surfaces: already correct, and now
asserted.** `IsRetryable` has admitted `40P01` since Task 7, and both
mapping boundaries route it to the `retryable` wire code —
`mcpErrorFor` (MCP) and `writeDomainError` (REST), each checking it after
every domain code and before the `internal_error` default. Nothing on the
`UpsertEntity` or `RemoveEntityType` paths intercepts a `*pgconn.PgError`
before it gets there: `ActorConstraintViolation` matches `23503` only,
`searchLimitExceeded` `54000` only, `notFound`/`notFoundByID` only
`pgx.ErrNoRows`, and `RemoveEntityType`'s own `23503` arm names the code
it catches. The reproduction confirms it end to end: every deadlock the
inverted-order runs produced was classified by `IsRetryable` on the way
out of a real service call.

What was missing was an assertion, which is this repository's other
standing failure. The MCP surface had pinned `40P01` since Task 7
(`TestMCPErrorForReportsContentionAsRetryable`); the REST twin table
pinned only its neighbour `40001`, under a case whose message read
"deadlock detected" while its code was `serialization_failure`. Both are
corrected, and the new row carries the error *wrapped* the way every
write path in `internal/metamodel` wraps one, so `errors.As` is exercised
rather than a bare `*pgconn.PgError` matched.

**One swallow found and deliberately not fixed here.** The game-content
surfaces (metamodel, documents, views, view assets) all route through
`writeDomainError`/`mcpErrorFor` and are covered. The game-*administration*
REST handlers — `api_projects.go`, `api_tokens.go`, `api_invites.go`,
`api_auth.go`, `api_admin.go`, `api_password.go` — do not: each maps every
error to `500 internal_error` with a fixed human sentence, so a contention
SQLSTATE there (`deleteProject` cascading over a game an agent is writing
to is the realistic one) is reported as a server fault. It is the same
misdiagnosis class, one surface along. It is recorded rather than fixed
because it is a contract change to a different vocabulary — those handlers
answer a human in a browser, not an agent — across a dozen call sites, with
no measurement behind it yet, and it deserves its own item.

**Tests.** Two staged and one stress, because the two answer different
questions:

- `TestAnEntityWriteTakesTheTypeRowBeforeItsOwnRow` and
  `TestACascadingRemovalTakesTheTypeRowBeforeAnyEntity` are
  deterministic. Each holds the *other* writer's first lock on the
  `entity_types` row from a connection of the test's own — `FOR UPDATE`
  for a removal, `FOR KEY SHARE` for an entity write — waits for the
  writer under test to park, and then asks with `FOR UPDATE NOWAIT`
  whether it is sitting on the entity row. Under the correct order it is
  not: it never got that far. That probe is the whole discrimination —
  both builds park, and only the broken one parks holding an entity.
  Each is red with its own half of the fix reverted.
- `TestUpsertEntityAndACascadingRemovalDoNotDeadlock` is the filed
  harness, shortened to 3 seconds and kept as an ordinary test that runs
  by default. It is timing-dependent and cannot prove absence, which is
  what the two staged tests are for; what it catches is a third statement
  crossing the rule that no staged test was written for. It is gated on
  nothing — this repository has no gate but `TEST_DATABASE_URL`, and a
  gated test is a test that stops running — and it asserts on its own
  traffic (at least 50 successful writes and 50 successful removals) so
  that a race which silently stopped racing cannot pass as a clean run.

---

### Metamodel 13: search is unconditionally verbose, and does not index row keys

**Status: done.** One migration (`0010_entity_key_search.sql`), one
line added to `UpsertEntity`, one flag on `SearchInput` and its REST
mirror, five new tests and three existing ones corrected.

Both halves come from Task 9's end-to-end seeding run, which drove the
surface the way an agent actually drives it, and both were pinned there
as *passing* limitations to be deleted when closed. They are deleted.

#### The payload: `verbose`, off by default

`entities.list` defaults `verbose` off and states the reason: a page of
five hundred entities with their fields is the whole game back in one
answer, and an agent walking a catalogue almost always wants keys and
names. `relations.list` restates it for edges. `search` had no such
argument at all and answered with every hit's whole field payload,
`longtext` included, up to its 200-row cap — **one sixty-hit search
measured at 1.6 MB of JSON** against a game whose rows carry 25 KB of
lore each.

**The fix follows the listing's rule and its reasoning rather than
inventing a second one.** Same spelling, same default, same argument —
and the argument is *stronger* here, not weaker: a search is what an
agent reaches for before it knows which row it wants, so without the
flag the payload it swallows is by definition the payload of rows it
has not chosen. A slim hit keeps everything choosing needs — `type_key`,
`key`, `name`, `invalid`, `version` — and `entities.get`, or a verbose
repeat, is the second call that reads the one it picked.

The flag gates **entity hits only**, and that is stated rather than
implied. A document hit has never carried a body (`DocumentHitOutput`
argues why, and `TestASearchHitCarriesNoBodyAtAll` pins it over every
field), so there is nothing on that side to withhold; a flag that
silently meant less on one of two kinds would be worse than no flag.

It is on **both** surfaces. The REST mirror reads it through `queryBool`,
the same helper the two listings read `verbose` with, so it takes the
same four true spellings and refuses the same value-less parameter — a
search that could only be asked for fields over MCP would be a mirror
answering a different question from the surface it mirrors.

#### The index: the key goes in at label C

The vector was `setweight(name, 'A') || setweight(name || field_text,
'B')`. A row's **key** — the handle `entities.get` takes, the handle
every refusal quotes back, the handle a designer reads off every listing
— was not in it, so typing `circuit-000` found nothing.

**Label C, and this is the whole decision.** `SearchEntities` leads its
ORDER BY with `ts_filter(search, '{a}') @@ query`, which is what turns
"a row the query *names* outranks a row that merely mentions the words"
from a tendency into a guarantee (review finding M1, Task 6). The A half
has to stay the name and nothing else. Keys minted from names
(`ironforge` for "Ironforge") would add nothing there; keys minted from
a counter would actively break it — the seeded racing game holds two
hundred rows keyed `race-000`…`race-199`, and under label A every one of
them would claim a name match on the word "race", ahead of the row
actually named Race. Under C the key is findable, `name_match` still
answers the question it was introduced for, and a hit found by its key
alone truthfully reports `name_match` false. B (0.4) outranking C (0.2)
costs such a hit nothing in practice: an exact handle is a lexeme almost
no other row carries, so it competes with no one.

**The migration follows 0006's precedent exactly, because 0006 is the
worked example for this shape of change** — a write-path edit plus a
rewrite of every stored vector. The new vector is the old one *plus* a
third `setweight`, never a re-derivation of an existing half, so the
backfill is a pure function of the stored value: `||` shifts the right
operand's positions past the left's, both the migration and
`UpsertEntity` evaluate `(A||B) || C`, and a migrated row is
byte-for-byte what the shipping write path produces.
`TestTheEntityKeyBackfillIsExact` asserts that over 0006's own twelve
shapes plus two the key adds, with the two sides of each pair carrying
the *identical* key under two entity types — comparing `migrated-07`
against `reseeded-07` would now compare two different vectors and pass
or fail for the wrong reason. `coalesce` for 0006's reason: a migration
must not be the thing that empties an index. The Down arm is
`ts_filter(…, '{a,b}')`, which is exact for every row this migration
wrote — a lexeme the key and the name share keeps its A and B positions
and loses only its C one.

#### Three existing tests corrected, each for a stated reason

- `TestTheSearchBackfillIsExact` (0006's own) compared whole vectors
  between a migrated row and a re-seeded one. Its two rows carry
  different keys by construction — one game, one type — so it now
  compares `ts_filter(…, '{a,b}')`, with the C half asserted whole by
  0010's own test next to it.
- `TestTheSearchVectorIsTheSameForTheSameValues` pins that two rows
  holding identical *values* index identically, which is what caught the
  missing key sort in `searchTextOf`. Its six rows are `q0`…`q5` and are
  now supposed to differ at label C, so `searchColumn` filters it out.
  This is the one that failed first and it failed correctly.
- Task 9's `what the surface makes an agent do the long way` block loses
  its cases 2 and 4, which is what closing a pinned limitation is
  supposed to do to it.

#### Mutation, applied

- **Key at label A instead of C.** `TestAKeyMatchDoesNotClaimToBeANameMatch`
  red: `row "race-002" was found by its key and reported name_match
  true`. `TestSearchFindsAnEntityByItsKeyOverTheToolSurface` red: `a hit
  found by its key alone claims name_match`.
- **The C half dropped from the write path.**
  `TestSearchFindsARowByItsKey` red: `Search("circuit-000") = [], want
  [circuit-000]`. `TestTheEntityKeyBackfillIsExact` red on every shape:
  `the migrated row's vector differs from the re-seeded row's`.
- **`verbose` ignored (`entityOf(..., true)` restored).**
  `TestASearchOmitsFieldsUnlessAskedToBeVerbose` red: `a search nobody
  asked to be verbose carried fields`. `TestTheRESTMirrorTakesTheSameVerboseFlag`
  red: `the REST mirror is verbose by default`. The seeded end-to-end run
  red too: `a search nobody asked to be verbose carried fields:
  ... Key:race-000 ... Fields:map[laps:10 night:false]`.

The seeded run now logs what the flag is worth on real content: one
200-hit search over 511 rows is **37,854 bytes slim against 49,956
verbose**, and the single hit carrying a long briefing is **220 bytes
slim against 4,944 verbose** — a 22× difference on exactly the row shape
that produced the original 1.6 MB.

---

### Metamodel 15: a schema edit that flags rows has no bulk repair, and batches have no size bound

**Status: done.** Two new tools on each surface (`entities.repair`,
`relations.repair`, and their two REST routes), two new statements, one
bound in the shared batch driver, and thirteen new tests.

Both halves come from Task 9's end-to-end seeding run, and both are the
last step of a rule that was established correctly and not carried one
step further.

#### The repair

**The decision that is *not* revisited.** A type's `field_schema` may be
edited at any time and the rows stored against it are neither rejected
nor back-filled: they are re-checked and the ones that no longer fit are
flagged. Task 9 verified that it holds — the sweep writes the flag and
nothing else, no values, no `version`, no `updated_at` on a row whose
verdict has not changed — and it is right. A validation pass is a verdict
about content, not an edit of it.

**What was missing is the other half.** Adding a required field under two
hundred rows flags all two hundred, and then nothing writable into the
*type* makes them fit again: `Schema.Check` refuses `required` together
with a `default`, correctly, because that pair is a self-contradiction.
So the only recovery was rewriting two hundred rows one at a time, each
carrying its own `expected_version` — and taking the field back out
flagged all two hundred a *second* time, because the value they now
carried had become an unknown field. Every schema experiment cost two
full rewrites, and the agent skill bundle would have had to teach that
loop as the recommended workflow.

**What a repair may do**, and every one of these is enforced by a
mechanism rather than promised in prose:

- Read **only rows the current schema rejects** — `invalid = true` and of
  the named type. The predicate is in both statements, and it is what
  stops a repair from becoming a bulk content editor.
- Write **only `fields`**. The pass hands the *stored* row's own key and
  name (or an edge's own endpoints) back to the ordinary write path, so
  there is no argument through which a key, a name or an endpoint could
  change.
- Apply exactly two operations, both stated by the caller in the same
  call: `drop_unknown` removes the values the type no longer declares,
  and `set` writes the values the caller names. Nothing is derived,
  guessed or defaulted by the repair itself.
- Clear the flag **by re-validating, never by fiat**. Every row goes back
  through `UpsertEntity`/`UpsertRelation`, so the flag comes off for the
  one reason it ever comes off.
- Report every row it could not fix, at its own key, with the code
  `entities.upsert` would have given it.

**What it may not do, and how the back door is nailed shut:**

- It may not run as a side effect of a schema edit. That is *the* way
  this design could quietly undo the rule it exists to complete, and
  `TestASchemaEditRepairsNothingByItself` is the standing check.
- It may not touch a valid row.
- It may not accept per-row values. One `set` covers the pass, because a
  repair is one decision about what a newly required field means. Per-row
  values, with the version claim that belongs to editing content, are
  `entities.upsert`.
- It may not create, rename or re-point a row.
- A pass stating **neither** operation is refused, not run: rewriting
  every flagged row with the values it already holds either does nothing
  or quietly injects a declared default into two hundred rows nobody
  asked about — a back-fill arrived at by a call that looks like a no-op.
- A `set` key the type does not declare is refused up front, so a
  caller's own mistake is not reported two hundred times as a property of
  the game's content.

**It is not a new power**, and that is the property that makes all of the
above hold together: a repair does exactly what an agent could already do
with a listing and a batch of upserts, with one decision instead of two
hundred round trips. There is no value it can put in a row that
`entities.upsert` could not, and no row it can reach that a designer has
not already been told is broken.

**Concurrency.** Each row is written with the `expected_version` the pass
read, so a designer editing a row while a pass is running gets a
`version_conflict` for that row and their work is not lost. A row someone
fixes between the selection and the write leaves the selection on the
next pass, and `TestARepairTouchesOnlyTheRowsTheSchemaRejects` drives the
hand-fixed case.

**Bounded, with no cursor, and the absence is the design.** A repaired row
leaves the selection, so calling again works on what the last call did not
fix; the loop terminates because `repaired` goes empty, and `failed` then
names what is left.
`TestARepairPassIsBoundedAndConverges` runs it over 250 rows at a
hundred-row limit and asserts the pass count, which is what would break
if a pass ever re-read rows it had already repaired.

**Both kinds.** `relations.repair` exists because 0009 gave edges a field
schema's flag and a version, so a relation type's schema edit flags edges
exactly as an entity type's flags entities. `revalidate` is one function
over two tables for that reason; a repair covering only entities would
have been this repository's most repeated defect.

#### The batch bound

`metamodel.MaxBulkItems = 500`, checked in `BulkUpsert` **before the mode
switch and before any transaction opens**, reported as `invalid_input` at
path `items` naming both numbers.

- **Why it was needed.** Task 9 sent a 5,000-item atomic batch over the
  wire: accepted, one transaction held open for 3.1 seconds, 515 KB of
  answer. An atomic batch is one transaction by construction, so its size
  is directly how long every other writer waits — and this was the one
  caller-supplied bound on the surface Postgres was left to discover.
  Every other one is checked in Go first: the search query at 4 KiB, a
  page limit clamped to its cap, the request body at 4 MiB.
- **Why 500.** `MaxEntityPage` and `MaxRelationPage` are both 500, so
  "the most rows one call moves" is one number across reads and writes.
  It is also well above every batch this repository's own seeding sends.
- **Why refused and not clamped.** A page limit clamps because a caller
  asking for too many rows still has a correct answer — the cap's worth,
  plus a cursor. A batch has none: silently writing the first 500 of
  5,000 leaves 4,500 rows unwritten with nothing saying so, and writing
  all of them is the behaviour the bound exists to stop.
- **In the shared place, and all three kinds.** The check is in
  `BulkUpsert`, which entities, relations and — since the markdown
  sub-project — `docs.write_many` all reach. `internal/markdown` inherits
  it rather than declaring it, and
  `TestADocumentBatchIsBoundedByTheSameCeiling` is the third kind's own
  pin, in its own package, because nothing in `internal/metamodel`'s tests
  can see that call site. The three tool descriptions state the number,
  asserted over the real transport by
  `TestAnOverLargeBatchIsRefusedOnBothSurfaces`.

The repair pass's own ceiling is `MaxBulkItems` too, because a pass *is*
a batch: the rows it reads are the items it writes.

#### Mutation, applied

- **The bound disabled (`if false && len(items) > MaxBulkItems`).** Red in
  all three packages: `TestABatchIsBoundedAndReportedAsTheCallersOwnArgument`
  — `err = <nil>, want a *metamodel.ValidationError`;
  `TestADocumentBatchIsBoundedByTheSameCeiling` — `want a refusal at
  "items", got nil`; `TestAnOverLargeBatchIsRefusedOnBothSurfaces` —
  `err = <nil>, want a *metamodel.ValidationError`.
- **`AND invalid` dropped from `ListInvalidEntitiesOfType`**, so a pass
  reads every row of the type. `TestARepairTouchesOnlyTheRowsTheSchemaRejects`
  red: `the pass scanned 3 and repaired 3, want 2 and 2`.
- **The no-operation refusal disabled.** Red on all four surfaces that
  assert it: `TestARepairThatStatesNoOperationIsRefused` (`err = <nil>`),
  `TestARepairPassAnswersOverTheToolSurface` (`a repair stating no
  operation was accepted`), `TestTheRESTMirrorRepairsToo` (`an empty
  repair over REST = 200 {"scanned":0,"repaired":[],"failed":[]}`), and
  the seeded end-to-end run.
- **The back door opened**: `UpsertEntityType` calling `RepairEntities`
  after its sweep. `TestASchemaEditRepairsNothingByItself` red: `2 rows
  flagged after removing a field, want 3`.

**One mutation stayed green, and fixing that is part of this task.** The
first attempt at the back door — a schema edit repairing with every
declared default — left `TestASchemaEditRepairsNothingByItself` passing,
because that test only walked the *narrowing* direction: the rows were
flagged for a required field, a required field may not declare a default,
so there was nothing the back door could invent and the repair failed
every row. A fixture too small to distinguish any policy. The test now
walks the widening direction too — removing a field, where dropping the
now-unknown value fixes every row with no decision to make, which is the
one case an automatic repair genuinely *could* succeed at — and the back
door is red there.

#### The seeded run

Task 9's four-step loop is now walked through the repair on two hundred
real rows: three passes at the default hundred-row limit for the `set`
half, three for the `drop_unknown` half, and a sixth step asserting that a
pass stating no operation is refused. The one-at-a-time rewrite it
replaced stays in the file, still exercised by the read-then-write loop
case, because it is what the repair costs when there is no repair.

---

### Metamodel 14: an agent cannot count, and pays a resolving read for mixed addressing

**Status: done.** One new tool and its shared assembly, six argument
shapes moved from uuids to keys, four REST routes re-shaped, and eleven
tests — three of them existing ones that could no longer say what they
used to say.

All three halves come from Task 9's seeding run, and all three are the
same defect in different clothes: the surface knew something an agent
could not ask it for.

#### Counting

**`games.counts`**, one call, answering "how many races are there" with
one row per declared entity type and per declared relation type — each
with how many rows instance it and how many of those a schema edit
flagged — plus three totals. Before it, that question was a full paged
walk: five calls in the seeded game, while the REST home page had the
number the whole time from two grouped queries.

**Only the exposure was missing, so only the exposure was added.** The
page's handler was split into `gameCounts`, which both surfaces now call,
and a `GameCountsOutput` the page embeds. The page keeps `role` — it
needs the caller's own membership to word its empty state — and the tool
does not carry it, because an agent has a token rather than a membership
row and a `"role": ""` would be a field that says nothing. One assembly
means the page and the tool cannot report different numbers, and
`TestGamesCountsAnswersTheQuestionAPagedWalkUsedTo` calls both against
one game and compares them.

**Both kinds are counted**, which is where "a rule not carried one step
along" would have bitten: entity types alone would answer half the game,
and since 0009 an edge is flagged by the same sweep an entity is. The
seeded run asserts the per-type edge counts sum to the total.

**Prose deliberately is not.** The markdown service is optional and a
document has no declared type to group by, so there is no row this shape
could carry — and adding a count to one of the two surfaces this
assembly serves and not the other is exactly the drift one assembly
exists to prevent.

#### Addressing: the decision, and the argument

**Keys replace ids. They are not accepted beside them.**

What moved: `entities.remove`, `relations.remove`, `types.remove` and
`relation_types.remove` took uuids; `relations.list` filtered its two
endpoints by entity uuid. Task 9 named the first three; **`types.remove`
and `relation_types.remove` it did not, and they were in exactly the same
state** — a finding applied to the tools it happened to list and not to
the ones it missed is the defect this repository produces most, so all
five moved together.

The argument for replacing rather than accepting both:

1. **A key here is not a nickname for an id.** It is immutable — the
   first spelling stored stands, and `keyRespellingError` refuses a
   respelling *after* the write — and unique per game and type. So there
   is nothing an id can address that a key cannot, and this is a total
   replacement rather than a convenience alias.
2. **A second spelling costs at every call site and buys nothing.** An
   "exactly one of" refusal path, two branches in every tool description,
   a decision for every caller. This repository already pays that in two
   places, and in both the two spellings are two genuinely different
   *questions*: `views.run` takes a saved key or an inline query and
   refuses both, and `docs.links.list` reads the join from either side
   and refuses neither and both. Two names for one row is not that.
3. **It would make an existing inconsistency worse.** The routes address
   a game by uuid while `/g/{slug}` addresses it by slug, and there is
   still no server-side slug resolution (Task 8's corrections record it).
   Adding a second dual-addressing surface is the wrong direction from
   there.
4. **Nothing is lost.** Every reader still returns the ids, every removal
   event still carries them, and the stored endpoint columns are still
   `uuid[]` — which is right, because an id is what a deleted type's
   prune can remove from an endpoint list.
   `TestEveryAddressOnThisSurfaceIsAKey` asserts both halves: the rows
   really go by their address, and the ids are still on the wire.

**The resolution did not disappear; it moved to where it costs nothing.**
`RemoveEntity` and `RemoveRelation` resolve inside their own
transaction, so what used to be a round trip is now two statements that
cannot race the delete they precede. The two type removals resolve in the
web layer, and that is the one place it is right to: the views service
needs the type's id to find the saved views a removal breaks, and it has
to take that list *before* the delete, because `view_refs`' foreign key
is `ON DELETE SET NULL`.

**On REST the last `by-id` segment is gone.** The content routes are
`by-key` throughout; an edge, which has no key of its own, is addressed
by `GET`/`DELETE /relations/one` with the same five query parameters on
both — the address `relations.upsert` writes it under. The endpoint
filters travel as `source_type_key`/`source_key` and their target twins,
and **half a ref is refused rather than read as no filter**: answering a
narrowed question with the whole listing is this surface's own wrong
answer that looks like a right one.
`TestTheRESTMirrorAddressesRowsByKeyToo` drives every one of those
routes.

#### Endpoint rules as keys

`source_type_ids`/`target_type_ids` became `source_type_keys`/
`target_type_keys` on the way in and on the way back. A second seeding
session had to call `types.list` and build a key-to-id map before it
could declare or edit one relation type; now what it reads back is what
it can send again, which
`TestARelationTypeReadsBackTheEndpointKeysItWasDeclaredWith` asserts by
round-tripping a declaration through its own answer.

- The translation is one statement: `LockEndpointEntityTypes` matches on
  `lower(key)` — the unique index — and returns `(id, key)`, inside the
  same transaction and under the same `FOR SHARE` lock the endpoint check
  already needed. No extra round trip and no new lock, so the lock-order
  argument that statement carries is untouched.
- The answer states the **stored** spelling, not the caller's, which is
  the rule every event and every message on this surface follows.
- **A key repeated inside one list is refused**, at the later element's
  own indexed path. The database would have stored the duplicate id
  happily; an endpoint list is a set — "these types may be a source" — so
  naming one twice cannot mean anything a caller intended, and silently
  folding it would leave the answer disagreeing with what was sent.
- An undeclared list is `[]` and never `null`, the rule this surface
  applies to every list it hands back.

#### Three existing tests that could no longer say what they said

- `TestMCPMalformedIDIsTheCallersOwnArgument` drove three tools that took
  uuids and now drives none of them: a key that names nothing is
  `not_found`, a different answer to a different question. What is left
  is the shape the rule still applies to — an endpoint list naming the
  *element* at fault — plus the new repeated-key refusal.
- `TestARouteShapedKeyIsStillAddressable` removed each route-shaped type
  through `by-id`. It removes through `by-key` now, so a key spelled like
  a route has to survive the removal as well as the read.
- `TestARemovalSaysWhatItCouldNotFindAndWhatStillHoldsIt` asserted that
  all four removals name the id they could not find. Two of them have no
  id to name any more, so they name the address the caller sent — and the
  table grew the two cases only key addressing can have: a type key that
  names no type, and a real pair of entities with no edge between them.
- Task 9's `what the surface makes an agent do the long way` block is
  gone, replaced by its positive form: the same three findings, driven as
  the calls an agent now makes, over the same two-hundred-row game.

#### Mutation, applied

- **The repeated-endpoint-key refusal disabled.**
  `TestMCPMalformedIDIsTheCallersOwnArgument` red: `err = <nil>, want the
  repeated element named`.
- **`relationTypeDetailOf` given an empty id-to-key map**, so endpoint
  rules answer with `[]`.
  `TestARelationTypeReadsBackTheEndpointKeysItWasDeclaredWith` red:
  `endpoint rules read back as {… SourceTypeKeys:[] TargetTypeKeys:[] …}`,
  and the seeded end-to-end run red at the same place.
- **The counts assembly stops adding the relation totals.**
  `TestGamesCountsAnswersTheQuestionAPagedWalkUsedTo` red:
  `Totals:{Entities:2 Relations:0 Invalid:0}`; the seeded run red with
  `the per-type edge counts sum to 1044 and the total says 0` — which is
  the cross-check that catches a total drifting from the rows it is
  summed from.
- **An endpoint filter naming no entity silently ignored instead of
  refused.** `TestEveryAddressOnThisSurfaceIsAKey` red: `an endpoint
  filter naming no entity was answered with a page`, and the seeded run
  red with the same sentence.
- **`RemoveEntity` stops deleting** (the statement replaced with a
  success). `TestRemoveEntity` red: `err = <nil>, want ErrNotFound after
  removal`; `TestEveryAddressOnThisSurfaceIsAKey` red one step later,
  `types.remove: in_use: the entity type "quest" still has 1 entities`,
  which is the removal being observed through a *different* tool rather
  than through its own return value.
- **The endpoint refs' pattern check disabled.**
  `TestEveryAddressOnThisSurfaceIsAKey` red with exactly the failure the
  check exists to prevent: `source: lookup entity: ERROR: invalid byte
  sequence for encoding "UTF8": 0x00 (SQLSTATE 22021)` — an
  internal_error over the caller's own argument. The check was owed by
  the rule the `type_key` filter beside it already follows, and adding
  the endpoint filters without it would have been that rule not carried
  one step along inside the very change that carried the addressing
  rule.
- **The endpoint *keys*' pattern check disabled**, which is the same rule
  at the third new place caller-supplied text reaches SQL.
  `TestARelationTypeReadsBackTheEndpointKeysItWasDeclaredWith` red:
  `lock endpoint entity types (source_type_keys): ERROR: invalid byte
  sequence for encoding "UTF8": 0x00 (SQLSTATE 22021)` — and note it
  fails over the *list*, because the keys travel as one `text[]`, so
  without the check one bad element takes the good ones down with it and
  the report cannot say which.

---

### Metamodel 16: a type cannot be renamed, and the staleness machinery built for a rename has no producer

**Status: done.** Two new domain calls, two SQL statements, two MCP
tools, two REST routes, two event kinds, and the staleness helpers in
`internal/views` moved off a staged `UPDATE` onto the real operation.

**What was wrong.** Both type upserts are addressed by key and
idempotent by it, so writing a different key created a *second* type and
left the first standing. There was no rename anywhere in the product,
and `internal/views` said so in the tool description an agent reads.
Fixing a misspelled handle meant declaring the new type, moving every
entity onto it and deleting the old one: several calls, and the row's
id, history and version went with the row that was deleted.

**Why it was worth building rather than documenting permanence.** The
machinery a rename needs was already built and idle. A saved view
resolves its type references *by id* precisely so a rename is
transparent to the picture, and two of the eight staleness diagnostics —
`entity_type_renamed` and `relation_type_renamed` — exist to report a
stored query that still spells a type the old way. All of that shipped
for a state no caller could produce; `internal/views/stale_test.go` had
to run `UPDATE entity_types SET key = …` on the row to reach it, and
`views_e2e_test.go` did the same in the middle of a walk whose whole
claim is that an agent could have made every call in it. Both now drive
the real tools.

**The shape.** `types.rename(from, to, expected_version)` and
`relation_types.rename`, addressed by the **old key** rather than by an
id — Metamodel 14 decided keys *replace* ids on this surface rather than
sitting beside them, and addressing a rename by `from` keeps that intact
and reads as what it is. One `UPDATE` of one column under the row's own
version, plus an event carrying the row's id and both spellings. No
child row is touched because no child row carries the key: entities,
edges, endpoint lists and `view_refs` all point at the id.

**Five refusals, none of them new in kind:**

- `expected_version` is required — a rename advances the version, so an
  unguarded one lands on top of an edit the caller never read. This is
  the rule `internal/markdown`'s `Move` states for a document.
- A **case-only respelling** is refused, and this was not a fresh
  judgement: the unique index folds case, so the two spellings are one
  address written two ways, and the "rename" would change no address
  while announcing a change no reader can observe. `keyRespellingError`
  refuses it on the write path and `docs.move` refuses it for a path;
  this makes three, and the fold comparison runs *after* validation in
  all three, because `strings.EqualFold` means what SQL's `lower()`
  means only once both values are known to be ASCII.
- A **taken destination** is `invalid_input` at `to`, naming the stored
  spelling when it differs — the shape a duplicate is refused with, and
  deliberately not a conflict: no amount of retrying frees a key.
- A `from` that names no type is `not_found`, judged before the version.
- The two ends are locked in **folded-key order**, not from-then-to
  order, so two opposite renames cannot deadlock —
  `internal/markdown`'s `lockBothEnds` again, over a different table.
  `TestTwoOppositeRenamesDoNotDeadlock` is red (SQLSTATE 40P01, within
  two rounds) with the ordering removed.

**What a rename deliberately does not do, and the trap inside it.** It
does not repair saved views: a view that named the type keeps resolving
by id and keeps reporting `*_renamed` until it is saved again. The tool
descriptions say so, because a caller that met the diagnostic without
being told would read it as a defect.

The trap is one step further in. A rename must **not** tidy
`view_refs.ref_key` to the new spelling. `staleness.storedID` resolves
by id only when the ref row and the stored query agree on the spelling,
since a disagreement is how an index written from another version of the
document is detected — so a helpful tidy would make every affected view
fall through to the by-key lookup, find nothing, and report its type
**missing**: the feature causing the exact failure it exists to prevent.
`TestARenameLeavesTheViewReferenceIndexSpellingTheOldKey` asserts the
ref row still spells the old key, still carries the same non-null type
id, and that the run therefore reports a rename and draws its three
nodes. Teaching the SQL to update `view_refs` was measured: that test
fails on the spelling, and
`TestARenamedEntityTypeStillJudgesTheProjectionThatDrawsIt` fails with
`query_stale: this view names the entity type "quest" and this game no
longer has it`.

**Everything that assumed a type key never changes**, found and carried:
`viewsKeyDoc` (the views tools' own description, which told an agent no
rename exists anywhere), its guard in `mcp_views_test.go`,
`keys.go`'s row-key policy, `RelationTypesUpsertInput`'s and
`EntitiesRemoveInput`'s "a key is immutable" arguments for keys
replacing ids, the two staleness helpers in `internal/views`, step 8 of
the views end-to-end walk, and this repository's own specs. A **view's**
key and an **entity's** key still have no rename, and the descriptions
now say which is which rather than making a blanket claim the metamodel
no longer honours.

**One route-shape consequence.** `POST /api/games/{game}/types/rename`
is a literal sibling of the collection route, so a type keyed `rename`
is exactly the collision the `by-key` discriminator exists to make
impossible. `rename` joins the word list in
`TestARouteShapedKeyIsStillAddressable`.

## Self-review notes

Checked against `2026-08-31-core-and-metamodel-design.md`, section by section:

- **Data model** — the four tables with `project_id`, keys unique per project
  (and per type for entities) by unique index, `semantic_role` as a nullable
  check-constrained column, edge field schemas, `version` columns, `invalid`
  flag, audit columns, `tsvector` + GIN: Task 1.
- **Field schemas** — the declarative field list, the six types, the deliberate
  absence of a reference type, unknown-field rejection, no zero-filling,
  defaults, ranges, enum options: Task 2.
- **Deletion and schema evolution** — `in_use` without cascade, cascade
  delete, re-validation flagging rather than rejecting or back-filling: Tasks 3
  and 4.
- **Concurrency** — `expected_version` on types, relation types and entities,
  the conflict carrying the current version, unique indexes rather than
  pre-flight checks: Tasks 3, 4, 5.
- **MCP surface** — idempotent upserts keyed by `(project, type, key)`, batches
  in both modes, one-hop `related_to`, cursor pagination, slim responses,
  search, and the stable error codes: Tasks 4, 6, 7.
- **Game isolation** — enforced in SQL first: every key from these tables to a
  project-scoped parent is composite and carries `project_id`, so the database
  itself rejects a cross-game reference (Task 1, and its corrections block),
  and every query over a metamodel table filters on the resolved project id
  (Task 7's requirement, applied throughout). `requireScope` on every tool,
  tested for a plain token and for an admin's token, is the second layer:
  Task 7, and the Core plan's Task 13.
- **Definition of done** — Task 9 seeds two hundred rows through the real
  surface and reads them back.
- **Idempotency** — Tasks 4 and 5 give the surface row identity by
  `(project, type, key)`, and Task 9 pins that a re-run without
  `expected_version` conflicts on every existing row. The spec's open
  question about that was **decided on 2026-09-02**: the bulk upserts
  gain `on_conflict`, so a re-seed becomes one call. That is a change to
  the write path and the tool surface, so it is Task 10 and it is not
  implemented; everything Tasks 4, 5 and 9 assert stays true until it
  lands.

Deliberately **not** here, and correctly so: transitive traversal and
reachability (views sub-project), prerequisite-cycle and unreachable-content
detection (analysis sub-project), the markdown domain, and the agent skill
bundle with its genre templates.
