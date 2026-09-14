package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/analysis"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/testutil"
)

// TestTheDemoSeedsTheCasesTheScreensNeed runs the demo against a
// throwaway database and asserts the three findings it exists to produce.
//
// **The fixture is the point, not the count.** Three defects in this
// product were found the first time a screen was ever rendered with
// data: rows the wrong height, a verdict that disagreed with its own
// number, and a whole grouping that had never drawn. What stops that
// happening again is a demo that *contains* those cases — so this test
// asks the analysis engine the three questions those screens ask, and
// fails if the demo has stopped answering any of them.
//
// It also makes the header's claim checkable: every write goes through
// the domain, so a schema change that breaks the demo breaks this test
// rather than the next person's afternoon.
func TestTheDemoSeedsTheCasesTheScreensNeed(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx := context.Background()
	owner := seedUser(t, pool)

	slug, err := seed(ctx, pool, owner, "demo")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if slug != "demo" {
		t.Fatalf("seeded %q", slug)
	}

	game := gameID(t, pool, "demo")
	engine := analysis.New(pool, nil)

	cycles, err := engine.Cycles(ctx, game, analysis.CyclesInput{})
	if err != nil {
		t.Fatalf("cycles: %v", err)
	}
	if len(cycles.Cycles) == 0 {
		t.Error("the demo has no prerequisite loop, so the Loops report draws nothing on it")
	}

	unreachable, err := engine.Unreachable(ctx, game, analysis.UnreachableInput{})
	if err != nil {
		t.Fatalf("unreachable: %v", err)
	}
	if len(unreachable.Findings) == 0 {
		t.Error("the demo has nothing out of reach, so Out of reach draws nothing on it — " +
			"which is the exact state that hid a whole grouping until it was hand-seeded")
	}

	orphans, err := engine.Orphans(ctx, game, analysis.OrphansInput{Mode: analysis.OrphanIsolated})
	if err != nil {
		t.Fatalf("orphans: %v", err)
	}
	if len(orphans.Findings) != 1 {
		t.Errorf("the demo has %d isolated entities; one is what the verdict's singular case needs, "+
			"and that sentence shipped reading \"1 entity are connected to nothing\"", len(orphans.Findings))
	}

	// The screens that only exist with enough rows: the pager, the
	// ordering and the "showing 3 of 6 declared fields" sentence.
	meta := metamodel.New(pool, nil)
	page, err := meta.ListEntities(ctx, game, metamodel.EntityFilter{TypeKey: "creature", Limit: 500})
	if err != nil {
		t.Fatalf("list creatures: %v", err)
	}
	if len(page.Entities) < 100 {
		t.Errorf("the demo holds %d creatures; the catalogue's paging and ordering need a type "+
			"that does not fit one page", len(page.Entities))
	}
	quest, err := meta.EntityTypeByKey(ctx, game, "quest")
	if err != nil {
		t.Fatalf("read the quest type: %v", err)
	}
	schema, err := metamodel.ParseSchema(quest.FieldSchema)
	if err != nil {
		t.Fatalf("parse the quest schema: %v", err)
	}
	if len(schema) <= 3 {
		t.Errorf("the quest type declares %d fields; the catalogue draws three and says how many "+
			"it is not showing, and that sentence needs a type with more", len(schema))
	}
}

// seedUser writes the account the demo game belongs to. The demo
// resolves its owner by email, so this only has to exist.
func seedUser(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	const email = "demo-owner@example.com"
	_, err := pool.Exec(context.Background(),
		`INSERT INTO users (email, password_hash, display_name) VALUES ($1, 'x', 'Demo owner')`, email)
	if err != nil {
		t.Fatalf("seed the owner: %v", err)
	}
	return email
}

func gameID(t *testing.T, pool *pgxpool.Pool, slug string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM projects WHERE slug = $1`, slug).Scan(&id); err != nil {
		t.Fatalf("read the game back: %v", err)
	}
	_ = projects.New(pool)
	return id
}
