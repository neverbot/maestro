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
  concurrency, because it would need a migration.

- [ ] **Step 1: Add the queries**

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

- [ ] **Step 2: State this task's event gating in `events.go`**

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

- [ ] **Step 3: Write the failing test**

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

- [ ] **Step 4: Run the test to verify it fails**

Run: `go test ./internal/metamodel/ -run TestRelation -v`
Expected: FAIL, `undefined: metamodel.RelationTypeInput`.

- [ ] **Step 5: Write the relation-type implementation**

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

- [ ] **Step 6: Write the relation implementation**

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

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./internal/metamodel/ -v`
Expected: PASS, every test in the package.

- [ ] **Step 8: Commit**

```bash
git add internal/db/queries internal/db/dbq internal/metamodel
git commit -m "feat: relation types and relations with endpoint validation"
```

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

- [ ] **Step 1: Add the listing and search queries**

`ListEntitiesPage` is printed in Task 4's Step 1 but was **not** added
there: nothing in Task 4 calls it, and a query landing a task ahead of
its only caller ships generated code no test exercises and pre-commits
this task's keyset shape sight unseen. It is this task's query, so add
it here, and settle its cursor while doing so — the block below compares
`(name, id::text)` against two separate nargs (`after_name`, `after`),
which is a shape this task's own cursor tests have to justify.
`ListEntitiesOfType`, also printed in Task 4, is called by no task in
this plan and was dropped; add it if and when a caller appears.

Append to `internal/db/queries/metamodel.sql`:

```sql
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

- [ ] **Step 2: Write the failing test**

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

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/metamodel/ -run 'TestList|TestOneHop|TestSearch' -v`
Expected: FAIL, `undefined: metamodel.EntityFilter`.

- [ ] **Step 4: Write the implementation**

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

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/metamodel/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

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

**Files:**
- Create: `internal/web/mcp_metamodel.go`
- Modify: `internal/web/server.go`, `internal/web/mcp.go`
- Test: `internal/web/mcp_metamodel_test.go`

- [ ] **Step 1: Write the failing test**

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

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/web/ -run TestMCPTypes -v`
Expected: FAIL, `unknown field Metamodel in web.MCPDeps`.

- [ ] **Step 3: Write the implementation**

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

- [ ] **Step 4: Add the remaining tools**

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

- [ ] **Step 5: Register the tools on the MCP server**

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

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/web/ -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/web
git commit -m "feat: mcp tools for types, entities, relations and search"
```

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

---

### Task 9: End-to-end seeding of a real game

**Files:**
- Create: `internal/web/seed_e2e_test.go`

This task proves the spec's definition of done: an agent declares a real game's
types and seeds hundreds of rows through the same surface it will use in
production.

- [ ] **Step 1: Write the test**

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

- [ ] **Step 2: Run it**

Run: `go test ./internal/web/ -run TestSeedARacingGame -v`
Expected: PASS.

- [ ] **Step 3: Verify by hand against a running instance**

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

- [ ] **Step 4: Commit**

```bash
git add internal/web/seed_e2e_test.go
git commit -m "test: end-to-end seeding of a game through the mcp surface"
```

---

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

Deliberately **not** here, and correctly so: transitive traversal and
reachability (views sub-project), prerequisite-cycle and unreachable-content
detection (analysis sub-project), the markdown domain, and the agent skill
bundle with its genre templates.
