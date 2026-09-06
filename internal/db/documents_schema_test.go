package db_test

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/testutil"
)

// documentGame is one seeded game: a project with one entity type and
// one entity in it. Every cross-game test below needs two of them, and
// builds its illegal row out of one game's document and the other's
// entity, entity type or token.
type documentGame struct {
	projectID string
	entityID  string
}

func seedDocumentGame(t *testing.T, ctx context.Context, pool *pgxpool.Pool, slug string) documentGame {
	t.Helper()

	var g documentGame
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (slug, name) VALUES ($1, $1) RETURNING id`, slug).Scan(&g.projectID); err != nil {
		t.Fatalf("insert project %s: %v", slug, err)
	}
	var typeID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO entity_types (project_id, key, label, label_plural)
		 VALUES ($1, 'quest', 'Quest', 'Quests') RETURNING id`, g.projectID).Scan(&typeID); err != nil {
		t.Fatalf("insert entity type in %s: %v", slug, err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO entities (project_id, entity_type_id, key, name)
		 VALUES ($1, $2, 'wanted-hogger', 'Wanted: Hogger') RETURNING id`,
		g.projectID, typeID).Scan(&g.entityID); err != nil {
		t.Fatalf("insert entity in %s: %v", slug, err)
	}
	return g
}

// seedDocument inserts one document into the given game and returns its
// id. The columns it leaves out are exactly the ones with defaults, so
// this catches a column losing its default (e.g. body_md's empty-string
// default, or current_version's 1) while it stays NOT NULL. It does not
// catch a column that becomes nullable with no default at all --
// frontmatter made nullable with no default leaves this insert, and the
// rest of the suite, green.
func seedDocument(t *testing.T, ctx context.Context, pool *pgxpool.Pool, projectID, path string) string {
	t.Helper()

	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO documents (project_id, path, body_md) VALUES ($1, $2, 'body') RETURNING id`,
		projectID, path).Scan(&id); err != nil {
		t.Fatalf("insert document %s: %v", path, err)
	}
	return id
}

// assertUniqueViolation is the 23505 counterpart of
// assertForeignKeyViolation (metamodel_schema_test.go). Asserting the
// SQLSTATE and not merely "an error" is what stops a test from passing
// because the statement was rejected for some unrelated reason.
func assertUniqueViolation(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a unique violation, but the statement succeeded")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected a *pgconn.PgError, got %T: %v", err, err)
	}
	if pgErr.Code != "23505" {
		t.Fatalf("expected SQLSTATE 23505 (unique_violation), got %s: %v", pgErr.Code, err)
	}
}

func TestDocumentTablesExist(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()

	for _, table := range []string{"documents", "document_versions", "document_links"} {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)`,
			table).Scan(&exists); err != nil {
			t.Fatalf("query %s: %v", table, err)
		}
		if !exists {
			t.Fatalf("table %s was not created", table)
		}
	}
}

// TestADocumentPathIsUniquePerGameWithoutRegardToCase pins
// documents_path_key. The path is the handle a seeding script re-runs
// against, so a second run under different casing has to collide with
// the row it already wrote instead of leaving a twin beside it.
func TestADocumentPathIsUniquePerGameWithoutRegardToCase(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	azeroth := seedDocumentGame(t, ctx, pool, "azeroth")
	outland := seedDocumentGame(t, ctx, pool, "outland")

	insert := `INSERT INTO documents (project_id, path, body_md) VALUES ($1, $2, 'body')`
	if _, err := pool.Exec(ctx, insert, azeroth.projectID, "lore/duskwood/history"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	_, err := pool.Exec(ctx, insert, azeroth.projectID, "Lore/Duskwood/History")
	assertUniqueViolation(t, err)

	// The same path in another game is a different document.
	if _, err := pool.Exec(ctx, insert, outland.projectID, "lore/duskwood/history"); err != nil {
		t.Fatalf("the same path in another game must be allowed: %v", err)
	}
}

// TestADocumentLinkCannotCrossGames pins the composite key
// document_links (entity_id, project_id) -> entities (id, project_id).
// A single-column key to entities (id) would accept this row, and one
// game's prose would hang off another game's quest.
func TestADocumentLinkCannotCrossGames(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	azeroth := seedDocumentGame(t, ctx, pool, "azeroth")
	outland := seedDocumentGame(t, ctx, pool, "outland")
	docID := seedDocument(t, ctx, pool, azeroth.projectID, "scripts/wanted-hogger")

	insert := `INSERT INTO document_links (project_id, document_id, entity_id) VALUES ($1, $2, $3)`
	_, err := pool.Exec(ctx, insert, azeroth.projectID, docID, outland.entityID)
	assertForeignKeyViolation(t, err)

	// The link within one game is accepted, so the failure above is the
	// key doing its job and not the insert being malformed.
	if _, err := pool.Exec(ctx, insert, azeroth.projectID, docID, azeroth.entityID); err != nil {
		t.Fatalf("same-game link: %v", err)
	}
}

// TestADocumentLinkCannotBorrowAnotherGamesDocument is the other half of
// the same rule, for the link's own end of the edge.
func TestADocumentLinkCannotBorrowAnotherGamesDocument(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	azeroth := seedDocumentGame(t, ctx, pool, "azeroth")
	outland := seedDocumentGame(t, ctx, pool, "outland")
	docID := seedDocument(t, ctx, pool, azeroth.projectID, "scripts/wanted-hogger")

	_, err := pool.Exec(ctx,
		`INSERT INTO document_links (project_id, document_id, entity_id) VALUES ($1, $2, $3)`,
		outland.projectID, docID, outland.entityID)
	assertForeignKeyViolation(t, err)
}

// TestADocumentVersionCannotCrossGames pins the composite key
// document_versions (document_id, project_id) -> documents (id,
// project_id). document_versions.project_id is denormalised so that
// history and read_version can filter on it without joining to the
// parent; this key is the only thing that keeps that copy honest.
func TestADocumentVersionCannotCrossGames(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	azeroth := seedDocumentGame(t, ctx, pool, "azeroth")
	outland := seedDocumentGame(t, ctx, pool, "outland")
	docID := seedDocument(t, ctx, pool, azeroth.projectID, "scripts/wanted-hogger")

	insert := `INSERT INTO document_versions (project_id, document_id, version, path, body_md) VALUES ($1, $2, $3, 'bible', 'body')`
	_, err := pool.Exec(ctx, insert, outland.projectID, docID, 1)
	assertForeignKeyViolation(t, err)

	if _, err := pool.Exec(ctx, insert, azeroth.projectID, docID, 1); err != nil {
		t.Fatalf("same-game version: %v", err)
	}
}

// TestADocumentVersionNumberIsUniquePerDocument pins
// document_versions_key. It is the second line of defence behind
// documents.current_version: whatever two concurrent writers believe,
// only one of them gets to be version 4.
func TestADocumentVersionNumberIsUniquePerDocument(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	azeroth := seedDocumentGame(t, ctx, pool, "azeroth")
	docA := seedDocument(t, ctx, pool, azeroth.projectID, "lore/a")
	docB := seedDocument(t, ctx, pool, azeroth.projectID, "lore/b")

	insert := `INSERT INTO document_versions (project_id, document_id, version, path, body_md) VALUES ($1, $2, $3, 'bible', 'body')`
	if _, err := pool.Exec(ctx, insert, azeroth.projectID, docA, 1); err != nil {
		t.Fatalf("first version: %v", err)
	}
	_, err := pool.Exec(ctx, insert, azeroth.projectID, docA, 1)
	assertUniqueViolation(t, err)

	// The number is per document, not per game.
	if _, err := pool.Exec(ctx, insert, azeroth.projectID, docB, 1); err != nil {
		t.Fatalf("version 1 of another document: %v", err)
	}
}

// TestADocumentHasOneRoleOnOneEntity pins document_links_key. role is
// deliberately not part of the key, so re-adding a link updates the role
// rather than growing a second edge -- the decision the plan's "Open
// questions settled" records.
func TestADocumentHasOneRoleOnOneEntity(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	azeroth := seedDocumentGame(t, ctx, pool, "azeroth")
	docID := seedDocument(t, ctx, pool, azeroth.projectID, "scripts/wanted-hogger")

	insert := `INSERT INTO document_links (project_id, document_id, entity_id, role) VALUES ($1, $2, $3, $4)`
	if _, err := pool.Exec(ctx, insert, azeroth.projectID, docID, azeroth.entityID, "script"); err != nil {
		t.Fatalf("first link: %v", err)
	}
	// A second role on the same pair is the same edge, not a new one.
	_, err := pool.Exec(ctx, insert, azeroth.projectID, docID, azeroth.entityID, "notes")
	assertUniqueViolation(t, err)
}

// TestADocumentCannotRecordAnotherProjectsToken covers the audit
// columns. api_tokens is project-scoped, so every *_by_token_id and
// author_token_id key is composite like the rest. This is the hole the
// metamodel's own schema had to be corrected for a second time: without
// it, "last edited by <token label>" on a document renders another
// game's token.
func TestADocumentCannotRecordAnotherProjectsToken(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	azeroth := seedDocumentGame(t, ctx, pool, "azeroth")
	outland := seedDocumentGame(t, ctx, pool, "outland")
	tokenA := seedToken(t, ctx, pool, azeroth.projectID, "token-a")
	tokenB := seedToken(t, ctx, pool, outland.projectID, "token-b")
	docID := seedDocument(t, ctx, pool, azeroth.projectID, "lore/duskwood")

	cases := []struct {
		column string
		sql    string
		args   func(token string) []any
	}{
		{
			column: "documents.created_by_token_id",
			sql: `INSERT INTO documents (project_id, path, created_by_token_id)
			      VALUES ($1, $2, $3)`,
			args: func(token string) []any { return []any{azeroth.projectID, "created/" + token[:8], token} },
		},
		{
			column: "documents.updated_by_token_id",
			sql: `INSERT INTO documents (project_id, path, updated_by_token_id)
			      VALUES ($1, $2, $3)`,
			args: func(token string) []any { return []any{azeroth.projectID, "updated/" + token[:8], token} },
		},
		{
			column: "document_versions.author_token_id",
			sql: `INSERT INTO document_versions (project_id, document_id, version, path, author_token_id)
			      VALUES ($1, $2, $3, 'bible', $4)`,
			args: func(token string) []any {
				// A distinct version number per case, so the unique key
				// on (document_id, version) cannot be what fails.
				return []any{azeroth.projectID, docID, int(token[0]%64) + 100, token}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.column, func(t *testing.T) {
			_, err := pool.Exec(ctx, tc.sql, tc.args(tokenB)...)
			assertForeignKeyViolation(t, err)

			if _, err := pool.Exec(ctx, tc.sql, tc.args(tokenA)...); err != nil {
				t.Fatalf("this game's own token on %s: %v", tc.column, err)
			}
		})
	}
}

// TestDeletingATokenClearsOnlyTheDocumentTokenColumn pins the one
// subtlety of a composite ON DELETE SET NULL: a bare SET NULL would try
// to null project_id too, which is NOT NULL, and the failure would
// surface at token-revocation time rather than here. The key names its
// column, so revoking a token leaves the row and its game alone.
func TestDeletingATokenClearsOnlyTheDocumentTokenColumn(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	azeroth := seedDocumentGame(t, ctx, pool, "azeroth")
	tokenID := seedToken(t, ctx, pool, azeroth.projectID, "token-a")

	var docID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO documents (project_id, path, created_by_token_id, updated_by_token_id)
		 VALUES ($1, 'lore/duskwood', $2, $2) RETURNING id`, azeroth.projectID, tokenID).Scan(&docID); err != nil {
		t.Fatalf("insert stamped document: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO document_versions (project_id, document_id, version, path, author_token_id)
		 VALUES ($1, $2, 1, 'bible', $3)`, azeroth.projectID, docID, tokenID); err != nil {
		t.Fatalf("insert stamped version: %v", err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM api_tokens WHERE id = $1`, tokenID); err != nil {
		t.Fatalf("delete token: %v", err)
	}

	var createdBy, updatedBy, author *string
	var docProject, versionProject string
	if err := pool.QueryRow(ctx,
		`SELECT created_by_token_id, updated_by_token_id, project_id FROM documents WHERE id = $1`,
		docID).Scan(&createdBy, &updatedBy, &docProject); err != nil {
		t.Fatalf("read document after token deletion: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT author_token_id, project_id FROM document_versions WHERE document_id = $1`,
		docID).Scan(&author, &versionProject); err != nil {
		t.Fatalf("read version after token deletion: %v", err)
	}
	for _, got := range []struct {
		column string
		value  *string
	}{
		{"documents.created_by_token_id", createdBy},
		{"documents.updated_by_token_id", updatedBy},
		{"document_versions.author_token_id", author},
	} {
		if got.value != nil {
			t.Fatalf("%s = %q, want NULL after the token was deleted", got.column, *got.value)
		}
	}
	if docProject != azeroth.projectID || versionProject != azeroth.projectID {
		t.Fatalf("project_id moved: document %q, version %q, want %q; the SET NULL must not touch it",
			docProject, versionProject, azeroth.projectID)
	}
}

// TestDeletingAnEntityDropsItsLinksAndKeepsTheDocument pins the CASCADE
// on document_links -> entities. Cutting a quest must not take its
// design notes with it: the prose survives for whatever replaces the
// quest.
func TestDeletingAnEntityDropsItsLinksAndKeepsTheDocument(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	azeroth := seedDocumentGame(t, ctx, pool, "azeroth")
	docID := seedDocument(t, ctx, pool, azeroth.projectID, "scripts/wanted-hogger")

	if _, err := pool.Exec(ctx,
		`INSERT INTO document_links (project_id, document_id, entity_id) VALUES ($1, $2, $3)`,
		azeroth.projectID, docID, azeroth.entityID); err != nil {
		t.Fatalf("insert link: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM entities WHERE id = $1`, azeroth.entityID); err != nil {
		t.Fatalf("delete entity: %v", err)
	}

	assertRowCount(t, ctx, pool, `SELECT count(*) FROM document_links WHERE document_id = $1`, docID, 0)
	assertRowCount(t, ctx, pool, `SELECT count(*) FROM documents WHERE id = $1`, docID, 1)
}

// TestDeletingADocumentTakesItsVersionsAndLinks pins the CASCADE on the
// two composite keys into documents. A version row orphaned from its
// document would still be readable by id and would still carry a
// project_id, which is exactly the shape of a leak.
func TestDeletingADocumentTakesItsVersionsAndLinks(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	azeroth := seedDocumentGame(t, ctx, pool, "azeroth")
	docID := seedDocument(t, ctx, pool, azeroth.projectID, "scripts/wanted-hogger")

	if _, err := pool.Exec(ctx,
		`INSERT INTO document_versions (project_id, document_id, version, path, body_md) VALUES ($1, $2, 1, 'bible', 'body')`,
		azeroth.projectID, docID); err != nil {
		t.Fatalf("insert version: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO document_links (project_id, document_id, entity_id) VALUES ($1, $2, $3)`,
		azeroth.projectID, docID, azeroth.entityID); err != nil {
		t.Fatalf("insert link: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM documents WHERE id = $1`, docID); err != nil {
		t.Fatalf("delete document: %v", err)
	}

	assertRowCount(t, ctx, pool, `SELECT count(*) FROM document_versions WHERE document_id = $1`, docID, 0)
	assertRowCount(t, ctx, pool, `SELECT count(*) FROM document_links WHERE document_id = $1`, docID, 0)
	// The entity is content, not a child of the document.
	assertRowCount(t, ctx, pool, `SELECT count(*) FROM entities WHERE id = $1`, azeroth.entityID, 1)
}

// TestTheDocumentsUpdatedAtTriggerFires keeps documents on the same
// mechanism as every other table with an updated_at: the trigger owns
// the column, and no query writes it. Two mechanisms for one column
// diverge silently and with no test to say so.
func TestTheDocumentsUpdatedAtTriggerFires(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	azeroth := seedDocumentGame(t, ctx, pool, "azeroth")
	docID := seedDocument(t, ctx, pool, azeroth.projectID, "lore/duskwood")

	var before, after time.Time
	const read = `SELECT updated_at FROM documents WHERE id = $1`
	if err := pool.QueryRow(ctx, read, docID).Scan(&before); err != nil {
		t.Fatalf("read updated_at before: %v", err)
	}
	// The update deliberately does not set updated_at: the point is that
	// the trigger does it.
	if _, err := pool.Exec(ctx, `UPDATE documents SET body_md = 'rewritten' WHERE id = $1`, docID); err != nil {
		t.Fatalf("update document: %v", err)
	}
	if err := pool.QueryRow(ctx, read, docID).Scan(&after); err != nil {
		t.Fatalf("read updated_at after: %v", err)
	}
	if !after.After(before) {
		t.Fatalf("updated_at did not move: %s -> %s", before, after)
	}
}

// TestTheDocumentSearchVectorIsGeneratedAndWeighted pins the generated
// column: the title outranks the summary, which outranks the body, and
// nothing in Go has to remember to write any of it.
func TestTheDocumentSearchVectorIsGeneratedAndWeighted(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	azeroth := seedDocumentGame(t, ctx, pool, "azeroth")

	if _, err := pool.Exec(ctx,
		`INSERT INTO documents (project_id, path, title, summary, body_md)
		 VALUES ($1, 'lore/duskwood', 'Duskwood', 'A haunted forest', 'The worgen came at dusk')`,
		azeroth.projectID); err != nil {
		t.Fatalf("insert document: %v", err)
	}

	const read = `SELECT search::text FROM documents WHERE project_id = $1 AND path = 'lore/duskwood'`
	var vector string
	if err := pool.QueryRow(ctx, read, azeroth.projectID).Scan(&vector); err != nil {
		t.Fatalf("read search vector: %v", err)
	}
	assertLexemeWeight(t, vector, "duskwood", "A")
	assertLexemeWeight(t, vector, "haunted", "B")
	assertLexemeWeight(t, vector, "worgen", "C")

	// Generated, not written: an UPDATE of the body moves the vector
	// with no application code involved.
	if _, err := pool.Exec(ctx,
		`UPDATE documents SET body_md = 'the gnolls came at dawn' WHERE project_id = $1 AND path = 'lore/duskwood'`,
		azeroth.projectID); err != nil {
		t.Fatalf("update body: %v", err)
	}
	if err := pool.QueryRow(ctx, read, azeroth.projectID).Scan(&vector); err != nil {
		t.Fatalf("re-read search vector: %v", err)
	}
	assertLexemeWeight(t, vector, "gnolls", "C")
	if regexp.MustCompile(`'worgen':`).MatchString(vector) {
		t.Fatalf("the generated vector kept a lexeme the body no longer has: %q", vector)
	}

	// The column is generated, so it cannot be written to directly. That
	// is the property the rest of this plan leans on: unlike
	// entities.search, no write path can forget to maintain it or write
	// it wrong.
	_, err := pool.Exec(ctx,
		`UPDATE documents SET search = to_tsvector('simple', 'anything') WHERE project_id = $1`,
		azeroth.projectID)
	if err == nil {
		t.Fatal("documents.search accepted a direct write; it must be a generated column")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected a *pgconn.PgError, got %T: %v", err, err)
	}
	// 428C9: cannot insert into a generated column.
	if pgErr.Code != "428C9" {
		t.Fatalf("expected SQLSTATE 428C9 (generated_always), got %s: %v", pgErr.Code, err)
	}
}

// TestTheDocumentSearchVectorCannotBeSetOnInsert is
// TestTheDocumentSearchVectorIsGeneratedAndWeighted's refusal, proved on
// INSERT rather than UPDATE. A generated column refuses a write in both
// statements, but only the UPDATE path was pinned; this closes the
// other half.
func TestTheDocumentSearchVectorCannotBeSetOnInsert(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	azeroth := seedDocumentGame(t, ctx, pool, "azeroth")

	_, err := pool.Exec(ctx,
		`INSERT INTO documents (project_id, path, body_md, search)
		 VALUES ($1, 'lore/insert-search', 'body', to_tsvector('simple', 'anything'))`,
		azeroth.projectID)
	if err == nil {
		t.Fatal("INSERT into documents.search accepted a direct value; it must be a generated column")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected a *pgconn.PgError, got %T: %v", err, err)
	}
	// 428C9: cannot insert into a generated column.
	if pgErr.Code != "428C9" {
		t.Fatalf("expected SQLSTATE 428C9 (generated_always), got %s: %v", pgErr.Code, err)
	}
}

// TestDocumentKeysAreNotDeferrable pins that the composite keys this
// migration relies on for isolation refuse a row at statement time, not
// commit time. Postgres defaults to NOT DEFERRABLE, so nothing in the
// migration says this explicitly -- but a later edit that added
// DEFERRABLE INITIALLY DEFERRED to any of them would move the refusal
// from the statement inside a transaction to the COMMIT, which lets a
// transaction observe a transiently cross-game row before it fails. The
// review's method note: without this test that regression is invisible,
// because every other test here only checks that the final state is
// refused, never when.
func TestDocumentKeysAreNotDeferrable(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	azeroth := seedDocumentGame(t, ctx, pool, "azeroth")
	outland := seedDocumentGame(t, ctx, pool, "outland")
	docID := seedDocument(t, ctx, pool, azeroth.projectID, "scripts/wanted-hogger")

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	_, err = tx.Exec(ctx,
		`INSERT INTO document_links (project_id, document_id, entity_id) VALUES ($1, $2, $3)`,
		azeroth.projectID, docID, outland.entityID)
	assertForeignKeyViolation(t, err)
}

// TestTheDocumentSearchVectorIsBounded pins the left(body_md, 131072)
// in the generated expression. Without it a large body raises SQLSTATE
// 54000 from the generated column itself and the INSERT fails, with a
// message about a tsvector the caller never mentioned. The bound
// mirrors metamodel's searchTextLimit (128 KiB).
func TestTheDocumentSearchVectorIsBounded(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	azeroth := seedDocumentGame(t, ctx, pool, "azeroth")

	// Distinct words, so nothing about this body is deduplicated into a
	// small vector: unbounded, it is far past the 1 MB tsvector limit.
	var body strings.Builder
	for i := range 300000 {
		fmt.Fprintf(&body, "w%d ", i)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO documents (project_id, path, body_md) VALUES ($1, 'lore/long', $2)`,
		azeroth.projectID, body.String()); err != nil {
		t.Fatalf("insert a body larger than the index bound: %v", err)
	}

	var indexed bool
	if err := pool.QueryRow(ctx,
		`SELECT search @@ plainto_tsquery('simple', 'w0') FROM documents WHERE project_id = $1 AND path = 'lore/long'`,
		azeroth.projectID).Scan(&indexed); err != nil {
		t.Fatalf("query the bounded vector: %v", err)
	}
	if !indexed {
		t.Fatal("the head of the body was not indexed")
	}
}

// TestAnOversizedTitleOrSummaryDoesNotFailTheInsert pins left(title,
// 131072) and left(summary, 131072), the other two inputs to the
// generated expression. Without them, a title or summary alone can
// exceed the roughly 1 MB a single to_tsvector call accepts and raise
// SQLSTATE 54000 from the generated column -- the same "an oversized
// field makes the row unsavable" shape the metamodel shipped and had to
// fix, on a column body_md's own bound does nothing for.
func TestAnOversizedTitleOrSummaryDoesNotFailTheInsert(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	azeroth := seedDocumentGame(t, ctx, pool, "azeroth")

	var oversized strings.Builder
	for i := range 300000 {
		fmt.Fprintf(&oversized, "w%d ", i)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO documents (project_id, path, title) VALUES ($1, 'lore/long-title', $2)`,
		azeroth.projectID, oversized.String()); err != nil {
		t.Fatalf("insert a title larger than the index bound: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO documents (project_id, path, summary) VALUES ($1, 'lore/long-summary', $2)`,
		azeroth.projectID, oversized.String()); err != nil {
		t.Fatalf("insert a summary larger than the index bound: %v", err)
	}
}

// assertLexemeWeight fails unless the printed tsvector carries lexeme at
// the given weight label. Positions are deliberately not asserted: they
// shift with the length of whatever precedes them in the concatenation,
// and the weight is what the ranking reads.
func assertLexemeWeight(t *testing.T, vector, lexeme, weight string) {
	t.Helper()
	pattern := regexp.MustCompile(`'` + regexp.QuoteMeta(lexeme) + `':(\d+[A-D],?)*\d+` + weight + `\b`)
	if !pattern.MatchString(vector) {
		t.Fatalf("search vector %q does not carry %q at weight %s", vector, lexeme, weight)
	}
}

func assertRowCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query, arg string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, query, arg).Scan(&got); err != nil {
		t.Fatalf("count (%s): %v", query, err)
	}
	if got != want {
		t.Fatalf("count (%s) = %d, want %d", query, got, want)
	}
}
