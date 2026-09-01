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
		{Key: "repeatable", Label: "Repeatable", Type: FieldBool, Default: false},
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
	// An absent optional field stays absent: it is never zero-filled.
	if _, present := out["repeatable"]; present {
		t.Fatal("an omitted optional field must not appear in the output")
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
	schema := Schema{{Key: "repeatable", Type: FieldBool, Default: true}}
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
	ErrNotFound            = errors.New("not_found")
	ErrVersionConflict     = errors.New("version_conflict")
	ErrEndpointTypeMismatch = errors.New("endpoint_type_mismatch")
	ErrInUse               = errors.New("in_use")
	ErrSchemaViolation     = errors.New("schema_violation")
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
type Field struct {
	Key      string    `json:"key"`
	Label    string    `json:"label,omitempty"`
	Type     FieldType `json:"type"`
	Required bool      `json:"required,omitempty"`
	Default  any       `json:"default,omitempty"`
	Options  []string  `json:"options,omitempty"`
	Min      *float64  `json:"min,omitempty"`
	Max      *float64  `json:"max,omitempty"`
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

// Check validates the schema itself, before anything is stored against it.
func (s Schema) Check() error {
	seen := make(map[string]struct{}, len(s))
	var problems []FieldError

	for i, f := range s {
		path := fmt.Sprintf("field_schema[%d]", i)
		if f.Key == "" {
			problems = append(problems, FieldError{Path: path, Message: "key is required"})
			continue
		}
		if _, dup := seen[f.Key]; dup {
			problems = append(problems, FieldError{Path: path, Message: "duplicate key " + f.Key})
		}
		seen[f.Key] = struct{}{}

		switch f.Type {
		case FieldText, FieldLongText, FieldNumber, FieldBool, FieldListText:
		case FieldEnum:
			if len(f.Options) == 0 {
				problems = append(problems, FieldError{Path: path, Message: "an enum field needs options"})
			}
		default:
			problems = append(problems, FieldError{Path: path, Message: "unknown type " + string(f.Type)})
		}
	}

	if len(problems) > 0 {
		return &ValidationError{Fields: problems}
	}
	return nil
}
```

`internal/metamodel/validate.go`:

```go
package metamodel

import "fmt"

// Validate checks a value map against the schema and returns the normalised
// map to store. Every problem is reported at once.
//
// Three rules matter more than the rest, and each exists for a reason:
//   - An unknown field is an error, never dropped: an agent that types
//     "min_lvl" must find out immediately, not six hundred rows later.
//   - An absent optional field with no default stays absent: zero-filling
//     would make "not set" indistinguishable from "set to zero".
//   - Declared defaults are applied, so a schema change gives every new row
//     the value the designer intended.
func (s Schema) Validate(values map[string]any) (map[string]any, error) {
	byKey := make(map[string]Field, len(s))
	for _, f := range s {
		byKey[f.Key] = f
	}

	var problems []FieldError
	out := make(map[string]any, len(values))

	for key := range values {
		if _, known := byKey[key]; !known {
			problems = append(problems, FieldError{
				Path:    "fields." + key,
				Message: "unknown field for this type",
			})
		}
	}

	for _, f := range s {
		raw, present := values[f.Key]
		path := "fields." + f.Key

		if !present || raw == nil {
			switch {
			case f.Default != nil:
				out[f.Key] = f.Default
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

// coerce checks one value against one field declaration.
func coerce(f Field, raw any) (any, error) {
	switch f.Type {
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

// toFloat accepts every numeric shape JSON decoding can produce.
func toFloat(raw any) (float64, bool) {
	switch n := raw.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/metamodel/ -v`
Expected: PASS, eleven tests.

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

---

### Task 3: Entity types

**Files:**
- Create: `internal/db/queries/metamodel.sql`, `internal/metamodel/service.go`, `internal/metamodel/types.go`
- Test: `internal/metamodel/types_test.go`

- [ ] **Step 1: Write the queries**

`internal/db/queries/metamodel.sql`:

```sql
-- name: UpsertEntityType :one
INSERT INTO entity_types (project_id, key, label, label_plural, description, color, icon, field_schema)
VALUES (sqlc.arg('project_id')::uuid, sqlc.arg('key')::text, sqlc.arg('label')::text,
        sqlc.arg('label_plural')::text, sqlc.arg('description')::text,
        sqlc.arg('color')::text, sqlc.arg('icon')::text, sqlc.arg('field_schema')::jsonb)
ON CONFLICT (project_id, lower(key)) DO UPDATE
SET label        = excluded.label,
    label_plural = excluded.label_plural,
    description  = excluded.description,
    color        = excluded.color,
    icon         = excluded.icon,
    field_schema = excluded.field_schema,
    version      = entity_types.version + 1,
    updated_at   = now()
RETURNING *;

-- name: GetEntityTypeByKey :one
SELECT * FROM entity_types
WHERE project_id = sqlc.arg('project_id')::uuid AND lower(key) = lower(sqlc.arg('key')::text);

-- name: GetEntityTypeByID :one
SELECT * FROM entity_types
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;

-- name: ListEntityTypes :many
SELECT * FROM entity_types
WHERE project_id = sqlc.arg('project_id')::uuid
ORDER BY label;

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

-- name: MarkEntitiesOfTypeInvalid :exec
UPDATE entities SET invalid = sqlc.arg('invalid')::boolean
WHERE project_id = sqlc.arg('project_id')::uuid
  AND entity_type_id = sqlc.arg('entity_type_id')::uuid
  AND id = ANY(sqlc.arg('ids')::uuid[]);
```

Run: `make sqlc`

- [ ] **Step 2: Write the failing test**

`internal/metamodel/types_test.go`:

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

// newProject creates a bare project row to scope a test's data.
func newProject(t *testing.T, svc *metamodel.Service) uuid.UUID {
	t.Helper()
	id, err := svc.CreateBareProjectForTest(context.Background(), "azeroth-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return id
}

func TestUpsertEntityTypeIsIdempotentByKey(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, svc)

	first, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: metamodel.Schema{{Key: "min_level", Type: metamodel.FieldNumber}},
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if first.Version != 1 {
		t.Fatalf("Version = %d, want 1", first.Version)
	}

	second, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema:          metamodel.Schema{{Key: "min_level", Type: metamodel.FieldNumber}},
		ExpectedVersion: ptrInt32(1),
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second.ID != first.ID {
		t.Fatal("upserting the same key created a second row")
	}
	if second.Version != 2 {
		t.Fatalf("Version = %d, want 2", second.Version)
	}
}

func ptrInt32(v int32) *int32 { return &v }

func TestUpsertEntityTypeRejectsStaleVersion(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, svc)

	if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest renamed", LabelPlural: "Quests",
		ExpectedVersion: ptrInt32(7),
	})
	if !errors.Is(err, metamodel.ErrVersionConflict) {
		t.Fatalf("err = %v, want ErrVersionConflict", err)
	}
	var conflict *metamodel.VersionConflictError
	if !errors.As(err, &conflict) || conflict.Current != 1 {
		t.Fatalf("the conflict must carry the current version, got %v", err)
	}
}

func TestUpsertEntityTypeRejectsBadSchema(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, svc)

	_, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: metamodel.Schema{{Key: "difficulty", Type: metamodel.FieldEnum}},
	})
	if !errors.Is(err, metamodel.ErrSchemaViolation) {
		t.Fatalf("err = %v, want ErrSchemaViolation", err)
	}
}

func TestRemoveEntityTypeRefusesWhenInUse(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, svc)

	typ, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{Key: "quest", Label: "Quest", LabelPlural: "Quests"})
	if err != nil {
		t.Fatalf("upsert type: %v", err)
	}
	if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
	}); err != nil {
		t.Fatalf("upsert entity: %v", err)
	}

	if err := svc.RemoveEntityType(ctx, project, typ.ID, false); !errors.Is(err, metamodel.ErrInUse) {
		t.Fatalf("err = %v, want ErrInUse", err)
	}
	if err := svc.RemoveEntityType(ctx, project, typ.ID, true); err != nil {
		t.Fatalf("cascade removal: %v", err)
	}
	if _, err := svc.EntityTypeByKey(ctx, project, "quest"); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound after removal", err)
	}
}

func TestTypesAreScopedToTheirProject(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	mine, theirs := newProject(t, svc), newProject(t, svc)

	if _, err := svc.UpsertEntityType(ctx, mine, metamodel.EntityTypeInput{Key: "quest", Label: "Quest", LabelPlural: "Quests"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := svc.EntityTypeByKey(ctx, theirs, "quest"); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("a type must not be visible from another project, got %v", err)
	}

	types, err := svc.ListEntityTypes(ctx, theirs)
	if err != nil {
		t.Fatalf("ListEntityTypes: %v", err)
	}
	if len(types) != 0 {
		t.Fatalf("the other project sees %d types, want 0", len(types))
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/metamodel/ -run TestUpsertEntityType -v`
Expected: FAIL, `undefined: metamodel.New`.

- [ ] **Step 4: Write the service scaffolding**

`internal/metamodel/service.go`:

```go
package metamodel

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/realtime"
)

// Actor records who performed a write, for the audit columns.
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

// New builds the service. The hub may be nil in tests.
func New(pool *pgxpool.Pool, hub *realtime.Hub) *Service {
	return &Service{pool: pool, q: dbq.New(pool), hub: hub}
}

// publish emits a change event, if a hub is attached.
func (s *Service) publish(projectID uuid.UUID, kind, payload string) {
	if s.hub == nil {
		return
	}
	s.hub.Publish(realtime.Event{ProjectID: projectID, Kind: kind, Payload: payload})
}

// CreateBareProjectForTest inserts a project row directly. Tests of this
// package need a project to scope their data without importing the projects
// service, which would make an import cycle.
func (s *Service) CreateBareProjectForTest(ctx context.Context, slug string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx,
		`INSERT INTO projects (slug, name) VALUES ($1, $1) RETURNING id`, slug).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("create project: %w", err)
	}
	return id, nil
}
```

- [ ] **Step 5: Write the entity-type operations**

`internal/metamodel/types.go`:

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

// EntityTypeInput is an upsert request. ExpectedVersion is required when the
// type already exists and ignored on creation.
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

// UpsertEntityType creates or updates a type, addressed by its key.
func (s *Service) UpsertEntityType(ctx context.Context, projectID uuid.UUID, in EntityTypeInput) (dbq.EntityType, error) {
	if in.Key == "" {
		return dbq.EntityType{}, &ValidationError{Fields: []FieldError{{Path: "key", Message: "is required"}}}
	}
	if err := in.Schema.Check(); err != nil {
		return dbq.EntityType{}, err
	}

	existing, err := s.q.GetEntityTypeByKey(ctx, dbq.GetEntityTypeByKeyParams{ProjectID: projectID, Key: in.Key})
	switch {
	case err == nil:
		if in.ExpectedVersion == nil || *in.ExpectedVersion != existing.Version {
			return dbq.EntityType{}, &VersionConflictError{Current: existing.Version}
		}
	case errors.Is(err, pgx.ErrNoRows):
		// Creation: no version to match.
	default:
		return dbq.EntityType{}, fmt.Errorf("lookup entity type: %w", err)
	}

	raw, err := in.Schema.JSON()
	if err != nil {
		return dbq.EntityType{}, err
	}

	row, err := s.q.UpsertEntityType(ctx, dbq.UpsertEntityTypeParams{
		ProjectID:   projectID,
		Key:         in.Key,
		Label:       in.Label,
		LabelPlural: in.LabelPlural,
		Description: in.Description,
		Color:       in.Color,
		Icon:        in.Icon,
		FieldSchema: raw,
	})
	if err != nil {
		return dbq.EntityType{}, fmt.Errorf("upsert entity type: %w", err)
	}

	// A schema change can invalidate stored rows. Re-check them rather than
	// rejecting the change or inventing values for the new field.
	if err := s.revalidateEntitiesOfType(ctx, row); err != nil {
		return dbq.EntityType{}, err
	}

	s.publish(projectID, "type.upserted", `{"key":"`+row.Key+`"}`)
	return row, nil
}

// EntityTypeByKey loads one type.
func (s *Service) EntityTypeByKey(ctx context.Context, projectID uuid.UUID, key string) (dbq.EntityType, error) {
	row, err := s.q.GetEntityTypeByKey(ctx, dbq.GetEntityTypeByKeyParams{ProjectID: projectID, Key: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbq.EntityType{}, ErrNotFound
	}
	if err != nil {
		return dbq.EntityType{}, fmt.Errorf("lookup entity type: %w", err)
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
// entities is refused: silently deleting a game's content is never the right
// reading of "remove this type".
func (s *Service) RemoveEntityType(ctx context.Context, projectID, id uuid.UUID, cascade bool) error {
	count, err := s.q.CountEntitiesOfType(ctx, dbq.CountEntitiesOfTypeParams{
		ProjectID: projectID, EntityTypeID: id,
	})
	if err != nil {
		return fmt.Errorf("count entities: %w", err)
	}
	if count > 0 && !cascade {
		return ErrInUse
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := dbq.New(tx)
	if cascade {
		if err := q.DeleteEntitiesOfType(ctx, dbq.DeleteEntitiesOfTypeParams{
			ProjectID: projectID, EntityTypeID: id,
		}); err != nil {
			return fmt.Errorf("delete entities: %w", err)
		}
	}
	rows, err := q.DeleteEntityType(ctx, dbq.DeleteEntityTypeParams{ProjectID: projectID, ID: id})
	if err != nil {
		return fmt.Errorf("delete entity type: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	s.publish(projectID, "type.removed", `{"id":"`+id.String()+`"}`)
	return nil
}
```

- [ ] **Step 6: Add the re-validation helper**

Append to `internal/metamodel/types.go`:

```go
// revalidateEntitiesOfType re-checks every stored entity against its type's
// current schema and flags the ones that no longer fit. Nothing is deleted and
// nothing is back-filled: the designer decides what a newly required field
// should hold.
func (s *Service) revalidateEntitiesOfType(ctx context.Context, typ dbq.EntityType) error {
	schema, err := ParseSchema(typ.FieldSchema)
	if err != nil {
		return err
	}

	rows, err := s.q.ListEntitiesOfType(ctx, dbq.ListEntitiesOfTypeParams{
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
		if _, err := schema.Validate(values); err != nil {
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
		if err := s.q.MarkEntitiesOfTypeInvalid(ctx, dbq.MarkEntitiesOfTypeInvalidParams{
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

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./internal/metamodel/ -v`
Expected: PASS. `TestRemoveEntityTypeRefusesWhenInUse` needs `UpsertEntity` from Task 4; run it again at the end of that task.

- [ ] **Step 8: Commit**

```bash
git add internal/db/queries internal/db/dbq internal/metamodel
git commit -m "feat: entity types with optimistic concurrency and cascade removal"
```

---

### Task 4: Entities, single and bulk

**Files:**
- Create: `internal/metamodel/entities.go`
- Modify: `internal/db/queries/metamodel.sql`
- Test: `internal/metamodel/entities_test.go`

- [ ] **Step 1: Add the queries**

Append to `internal/db/queries/metamodel.sql`:

```sql
-- name: UpsertEntity :one
INSERT INTO entities (project_id, entity_type_id, key, name, fields, search)
VALUES (sqlc.arg('project_id')::uuid, sqlc.arg('entity_type_id')::uuid,
        sqlc.arg('key')::text, sqlc.arg('name')::text, sqlc.arg('fields')::jsonb,
        to_tsvector('simple', sqlc.arg('name')::text || ' ' || sqlc.arg('search_text')::text))
ON CONFLICT (project_id, entity_type_id, lower(key)) DO UPDATE
SET name       = excluded.name,
    fields     = excluded.fields,
    search     = excluded.search,
    invalid    = false,
    version    = entities.version + 1,
    updated_at = now()
RETURNING *;

-- name: GetEntityByKey :one
SELECT * FROM entities
WHERE project_id = sqlc.arg('project_id')::uuid
  AND entity_type_id = sqlc.arg('entity_type_id')::uuid
  AND lower(key) = lower(sqlc.arg('key')::text);

-- name: GetEntityByID :one
SELECT * FROM entities
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;

-- name: ListEntitiesOfType :many
SELECT * FROM entities
WHERE project_id = sqlc.arg('project_id')::uuid
  AND entity_type_id = sqlc.arg('entity_type_id')::uuid
ORDER BY name;

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

- [ ] **Step 2: Write the failing test**

`internal/metamodel/entities_test.go`:

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
	project := newProject(t, svc)
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
	project := newProject(t, svc)
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
	project := newProject(t, svc)
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

func TestBulkPartialLandsTheGoodRowsAndReportsTheRest(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, svc)
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
	if _, err := svc.EntityByKey(ctx, project, "quest", "c"); err != nil {
		t.Fatalf("the row after the failure must still have landed: %v", err)
	}
}

func TestBulkAtomicRollsBackEverythingOnOneFailure(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, svc)
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

func TestSchemaChangeFlagsRowsInvalidWithoutTouchingThem(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, svc)
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

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/metamodel/ -run TestUpsertEntity -v`
Expected: FAIL, `undefined: metamodel.EntityInput`.

- [ ] **Step 4: Write the implementation**

`internal/metamodel/entities.go`:

```go
package metamodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
type EntityInput struct {
	TypeKey         string
	Key             string
	Name            string
	Fields          map[string]any
	ExpectedVersion *int32
	Actor           Actor
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
	q := s.q
	row, err := s.upsertEntityWith(ctx, q, projectID, in)
	if err != nil {
		return dbq.Entity{}, err
	}
	s.publish(projectID, "entity.upserted", `{"type":"`+in.TypeKey+`","key":"`+row.Key+`"}`)
	return row, nil
}

// UpsertEntities writes a batch in the requested mode.
func (s *Service) UpsertEntities(ctx context.Context, projectID uuid.UUID, items []EntityInput, mode BulkMode) (BulkResult, error) {
	if mode == BulkAtomic {
		return s.upsertEntitiesAtomic(ctx, projectID, items)
	}

	var result BulkResult
	for i, in := range items {
		row, err := s.upsertEntityWith(ctx, s.q, projectID, in)
		if err != nil {
			result.Failed = append(result.Failed, failureFor(i, in.Key, err))
			continue
		}
		result.Succeeded = append(result.Succeeded, row)
	}
	if len(result.Succeeded) > 0 {
		s.publish(projectID, "entity.upserted", fmt.Sprintf(`{"count":%d}`, len(result.Succeeded)))
	}
	return result, nil
}

func (s *Service) upsertEntitiesAtomic(ctx context.Context, projectID uuid.UUID, items []EntityInput) (BulkResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return BulkResult{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := dbq.New(tx)
	var result BulkResult
	for i, in := range items {
		row, err := s.upsertEntityWith(ctx, q, projectID, in)
		if err != nil {
			return BulkResult{}, fmt.Errorf("item %d (%s): %w", i, in.Key, err)
		}
		result.Succeeded = append(result.Succeeded, row)
	}
	if err := tx.Commit(ctx); err != nil {
		return BulkResult{}, fmt.Errorf("commit: %w", err)
	}
	s.publish(projectID, "entity.upserted", fmt.Sprintf(`{"count":%d}`, len(result.Succeeded)))
	return result, nil
}

// upsertEntityWith does the work against any queries handle, so the same code
// serves the single, partial and atomic paths.
func (s *Service) upsertEntityWith(ctx context.Context, q *dbq.Queries, projectID uuid.UUID, in EntityInput) (dbq.Entity, error) {
	if in.Key == "" {
		return dbq.Entity{}, &ValidationError{Fields: []FieldError{{Path: "key", Message: "is required"}}}
	}

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

	existing, err := q.GetEntityByKey(ctx, dbq.GetEntityByKeyParams{
		ProjectID: projectID, EntityTypeID: typ.ID, Key: in.Key,
	})
	switch {
	case err == nil:
		if in.ExpectedVersion == nil || *in.ExpectedVersion != existing.Version {
			return dbq.Entity{}, &VersionConflictError{Current: existing.Version}
		}
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return dbq.Entity{}, fmt.Errorf("lookup entity: %w", err)
	}

	encoded, err := json.Marshal(values)
	if err != nil {
		return dbq.Entity{}, fmt.Errorf("encode fields: %w", err)
	}

	row, err := q.UpsertEntity(ctx, dbq.UpsertEntityParams{
		ProjectID:    projectID,
		EntityTypeID: typ.ID,
		Key:          in.Key,
		Name:         in.Name,
		Fields:       encoded,
		SearchText:   searchTextOf(values),
	})
	if err != nil {
		return dbq.Entity{}, fmt.Errorf("upsert entity: %w", err)
	}
	return row, nil
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
	if errors.Is(err, pgx.ErrNoRows) {
		return dbq.Entity{}, ErrNotFound
	}
	if err != nil {
		return dbq.Entity{}, fmt.Errorf("lookup entity: %w", err)
	}
	return row, nil
}

// RemoveEntity deletes one entity. Its relations go with it, by cascade.
func (s *Service) RemoveEntity(ctx context.Context, projectID, id uuid.UUID) error {
	rows, err := s.q.DeleteEntity(ctx, dbq.DeleteEntityParams{ProjectID: projectID, ID: id})
	if err != nil {
		return fmt.Errorf("delete entity: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	s.publish(projectID, "entity.removed", `{"id":"`+id.String()+`"}`)
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

// searchTextOf flattens the text values of a row so search can index them.
func searchTextOf(values map[string]any) string {
	var b strings.Builder
	for _, v := range values {
		if s, ok := v.(string); ok {
			b.WriteString(s)
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// failureFor maps a domain error to the wire shape of a bulk failure.
func failureFor(index int, key string, err error) BulkFailure {
	f := BulkFailure{Index: index, Key: key, Message: err.Error()}
	switch {
	case errors.Is(err, ErrSchemaViolation):
		f.Code = "schema_violation"
	case errors.Is(err, ErrVersionConflict):
		f.Code = "version_conflict"
	case errors.Is(err, ErrNotFound):
		f.Code = "not_found"
	case errors.Is(err, ErrEndpointTypeMismatch):
		f.Code = "endpoint_type_mismatch"
	default:
		f.Code = "internal_error"
	}
	return f
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/metamodel/ -v`
Expected: PASS, including `TestRemoveEntityTypeRefusesWhenInUse` from Task 3.

- [ ] **Step 6: Commit**

```bash
git add internal/db/queries internal/db/dbq internal/metamodel
git commit -m "feat: entities with bulk partial and atomic writes"
```

---

### Task 5: Relation types and relations

**Files:**
- Create: `internal/metamodel/relation_types.go`, `internal/metamodel/relations.go`
- Modify: `internal/db/queries/metamodel.sql`
- Test: `internal/metamodel/relations_test.go`

- [ ] **Step 1: Add the queries**

Append to `internal/db/queries/metamodel.sql`:

```sql
-- name: UpsertRelationType :one
INSERT INTO relation_types (project_id, key, label, description, source_type_ids, target_type_ids, semantic_role, field_schema)
VALUES (sqlc.arg('project_id')::uuid, sqlc.arg('key')::text, sqlc.arg('label')::text,
        sqlc.arg('description')::text, sqlc.arg('source_type_ids')::uuid[],
        sqlc.arg('target_type_ids')::uuid[], sqlc.narg('semantic_role')::text,
        sqlc.arg('field_schema')::jsonb)
ON CONFLICT (project_id, lower(key)) DO UPDATE
SET label           = excluded.label,
    description     = excluded.description,
    source_type_ids = excluded.source_type_ids,
    target_type_ids = excluded.target_type_ids,
    semantic_role   = excluded.semantic_role,
    field_schema    = excluded.field_schema,
    version         = relation_types.version + 1,
    updated_at      = now()
RETURNING *;

-- name: GetRelationTypeByKey :one
SELECT * FROM relation_types
WHERE project_id = sqlc.arg('project_id')::uuid AND lower(key) = lower(sqlc.arg('key')::text);

-- name: ListRelationTypes :many
SELECT * FROM relation_types WHERE project_id = sqlc.arg('project_id')::uuid ORDER BY label;

-- name: CountRelationsOfType :one
SELECT count(*) FROM relations
WHERE project_id = sqlc.arg('project_id')::uuid
  AND relation_type_id = sqlc.arg('relation_type_id')::uuid;

-- name: DeleteRelationType :execrows
DELETE FROM relation_types
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;

-- name: UpsertRelation :one
INSERT INTO relations (project_id, relation_type_id, source_id, target_id, fields)
VALUES (sqlc.arg('project_id')::uuid, sqlc.arg('relation_type_id')::uuid,
        sqlc.arg('source_id')::uuid, sqlc.arg('target_id')::uuid, sqlc.arg('fields')::jsonb)
ON CONFLICT (relation_type_id, source_id, target_id) DO UPDATE
SET fields = excluded.fields, updated_at = now()
RETURNING *;

-- name: ListRelations :many
SELECT r.* FROM relations r
WHERE r.project_id = sqlc.arg('project_id')::uuid
  AND (sqlc.narg('relation_type_id')::uuid IS NULL OR r.relation_type_id = sqlc.narg('relation_type_id')::uuid)
  AND (sqlc.narg('source_id')::uuid IS NULL OR r.source_id = sqlc.narg('source_id')::uuid)
  AND (sqlc.narg('target_id')::uuid IS NULL OR r.target_id = sqlc.narg('target_id')::uuid)
ORDER BY r.created_at
LIMIT sqlc.arg('limit')::int;

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
ORDER BY e.name;
```

Run: `make sqlc`

- [ ] **Step 2: Write the failing test**

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
	project := newProject(t, svc)
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
	project := newProject(t, svc)
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
	project := newProject(t, svc)
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
	project := newProject(t, svc)
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

func TestDeletingAnEntityDeletesItsRelations(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, svc)
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

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/metamodel/ -run TestRelation -v`
Expected: FAIL, `undefined: metamodel.RelationTypeInput`.

- [ ] **Step 4: Write the relation-type implementation**

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

// RelationTypeInput is an upsert request for a relation type. Empty endpoint
// lists mean "any type".
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

// UpsertRelationType creates or updates a relation type.
func (s *Service) UpsertRelationType(ctx context.Context, projectID uuid.UUID, in RelationTypeInput) (dbq.RelationType, error) {
	if in.Key == "" {
		return dbq.RelationType{}, &ValidationError{Fields: []FieldError{{Path: "key", Message: "is required"}}}
	}
	if err := in.Schema.Check(); err != nil {
		return dbq.RelationType{}, err
	}

	existing, err := s.q.GetRelationTypeByKey(ctx, dbq.GetRelationTypeByKeyParams{ProjectID: projectID, Key: in.Key})
	switch {
	case err == nil:
		if in.ExpectedVersion == nil || *in.ExpectedVersion != existing.Version {
			return dbq.RelationType{}, &VersionConflictError{Current: existing.Version}
		}
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return dbq.RelationType{}, fmt.Errorf("lookup relation type: %w", err)
	}

	raw, err := in.Schema.JSON()
	if err != nil {
		return dbq.RelationType{}, err
	}
	params := dbq.UpsertRelationTypeParams{
		ProjectID:     projectID,
		Key:           in.Key,
		Label:         in.Label,
		Description:   in.Description,
		SourceTypeIds: in.SourceTypeIDs,
		TargetTypeIds: in.TargetTypeIDs,
		FieldSchema:   raw,
	}
	if in.SemanticRole != "" {
		role := in.SemanticRole
		params.SemanticRole = &role
	}

	row, err := s.q.UpsertRelationType(ctx, params)
	if err != nil {
		return dbq.RelationType{}, fmt.Errorf("upsert relation type: %w", err)
	}
	s.publish(projectID, "relation_type.upserted", `{"key":"`+row.Key+`"}`)
	return row, nil
}

// RelationTypeByKey loads one relation type.
func (s *Service) RelationTypeByKey(ctx context.Context, projectID uuid.UUID, key string) (dbq.RelationType, error) {
	row, err := s.q.GetRelationTypeByKey(ctx, dbq.GetRelationTypeByKeyParams{ProjectID: projectID, Key: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbq.RelationType{}, ErrNotFound
	}
	if err != nil {
		return dbq.RelationType{}, fmt.Errorf("lookup relation type: %w", err)
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
func (s *Service) RemoveRelationType(ctx context.Context, projectID, id uuid.UUID, cascade bool) error {
	count, err := s.q.CountRelationsOfType(ctx, dbq.CountRelationsOfTypeParams{
		ProjectID: projectID, RelationTypeID: id,
	})
	if err != nil {
		return fmt.Errorf("count relations: %w", err)
	}
	if count > 0 && !cascade {
		return ErrInUse
	}
	rows, err := s.q.DeleteRelationType(ctx, dbq.DeleteRelationTypeParams{ProjectID: projectID, ID: id})
	if err != nil {
		return fmt.Errorf("delete relation type: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	s.publish(projectID, "relation_type.removed", `{"id":"`+id.String()+`"}`)
	return nil
}
```

- [ ] **Step 5: Write the relation implementation**

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

// UpsertRelation creates or updates one edge, validating both endpoints
// against the relation type's allowed lists and the fields against its schema.
func (s *Service) UpsertRelation(ctx context.Context, projectID uuid.UUID, in RelationInput) (dbq.Relation, error) {
	relType, err := s.RelationTypeByKey(ctx, projectID, in.TypeKey)
	if err != nil {
		return dbq.Relation{}, err
	}

	source, err := s.EntityByKey(ctx, projectID, in.Source.TypeKey, in.Source.Key)
	if err != nil {
		return dbq.Relation{}, fmt.Errorf("source %s/%s: %w", in.Source.TypeKey, in.Source.Key, err)
	}
	target, err := s.EntityByKey(ctx, projectID, in.Target.TypeKey, in.Target.Key)
	if err != nil {
		return dbq.Relation{}, fmt.Errorf("target %s/%s: %w", in.Target.TypeKey, in.Target.Key, err)
	}

	if !endpointAllowed(relType.SourceTypeIds, source.EntityTypeID) {
		return dbq.Relation{}, fmt.Errorf("%w: %s cannot be the source of %s", ErrEndpointTypeMismatch, in.Source.TypeKey, relType.Key)
	}
	if !endpointAllowed(relType.TargetTypeIds, target.EntityTypeID) {
		return dbq.Relation{}, fmt.Errorf("%w: %s cannot be the target of %s", ErrEndpointTypeMismatch, in.Target.TypeKey, relType.Key)
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

	row, err := s.q.UpsertRelation(ctx, dbq.UpsertRelationParams{
		ProjectID:      projectID,
		RelationTypeID: relType.ID,
		SourceID:       source.ID,
		TargetID:       target.ID,
		Fields:         encoded,
	})
	if err != nil {
		return dbq.Relation{}, fmt.Errorf("upsert relation: %w", err)
	}
	s.publish(projectID, "relation.upserted", `{"type":"`+relType.Key+`"}`)
	return row, nil
}

// UpsertRelations writes a batch of edges in the requested mode.
func (s *Service) UpsertRelations(ctx context.Context, projectID uuid.UUID, items []RelationInput, mode BulkMode) (BulkResult, error) {
	var result BulkResult
	if mode == BulkAtomic {
		// Relations are validated with several reads per item, so the atomic
		// path runs the same code inside one transaction by deferring the
		// commit until every item has passed.
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return BulkResult{}, fmt.Errorf("begin: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		txSvc := &Service{pool: s.pool, q: dbq.New(tx), hub: nil}
		for i, in := range items {
			if _, err := txSvc.UpsertRelation(ctx, projectID, in); err != nil {
				return BulkResult{}, fmt.Errorf("item %d: %w", i, err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return BulkResult{}, fmt.Errorf("commit: %w", err)
		}
		s.publish(projectID, "relation.upserted", fmt.Sprintf(`{"count":%d}`, len(items)))
		return BulkResult{}, nil
	}

	for i, in := range items {
		if _, err := s.UpsertRelation(ctx, projectID, in); err != nil {
			result.Failed = append(result.Failed, failureFor(i, in.Source.Key+"->"+in.Target.Key, err))
		}
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

// RemoveRelation deletes one edge.
func (s *Service) RemoveRelation(ctx context.Context, projectID, id uuid.UUID) error {
	rows, err := s.q.DeleteRelation(ctx, dbq.DeleteRelationParams{ProjectID: projectID, ID: id})
	if err != nil {
		return fmt.Errorf("delete relation: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	s.publish(projectID, "relation.removed", `{"id":"`+id.String()+`"}`)
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

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/metamodel/ -v`
Expected: PASS, every test in the package.

- [ ] **Step 7: Commit**

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

- [ ] **Step 1: Add the search query**

Append to `internal/db/queries/metamodel.sql`:

```sql
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
	project := newProject(t, svc)
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
	project := newProject(t, svc)
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
	project := newProject(t, svc)
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
	project := newProject(t, svc)
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

func decodeCursor(s string) (cursor, error) {
	if s == "" {
		return cursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor{}, fmt.Errorf("malformed cursor")
	}
	var c cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return cursor{}, fmt.Errorf("malformed cursor")
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
