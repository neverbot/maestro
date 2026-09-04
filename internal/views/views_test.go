package views

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/realtime"
)

// The two query documents this file saves. questsOnly names one entity
// type and nothing else; questsToZones names three references at three
// pointers, which is what makes the refs assertions say anything.
const (
	questsOnly    = `{"v":1,"from":[{"type":"quest","as":"q"}]}`
	questsToZones = `{"v":1,"from":[{"type":"quest","as":"q"}],
		"traverse":[{"from":"q","via":"takes_place_in","to_type":"zone","as":"z"}],
		"edges":[{"from_step":"z"}]}`
)

func ptrInt32(v int32) *int32 { return &v }

// saveable is the input every test that does not care about the
// arguments starts from: a query that resolves, a renderer that can draw
// it, and nothing else set.
func saveable(key, doc string) ViewInput {
	return ViewInput{Key: key, Name: strings.ToUpper(key[:1]) + key[1:], Query: []byte(doc),
		Renderer: RendererGraph}
}

// TestAViewIsReadBackWithEveryFieldItWasSavedWith is the read-back that
// catches a write-only column.
//
// It is not a formality: `relations.fields` was write-only for a whole
// sub-project because every test asserted the call succeeded and none
// read the row back, and `renderer_params` is the same shape — a jsonb
// column nothing in this package reads afterwards. layout_mode,
// layout_seed and description are the other three a caller can set here
// and no other test in this file looks at, so all four are asserted
// together, off a second read rather than off the returned row: the
// returned row is what the INSERT said, and only a read says what was
// stored.
func TestAViewIsReadBackWithEveryFieldItWasSavedWith(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()

	in := saveable("mage_route", questsToZones)
	in.Name = "Mage route"
	in.Description = "Every quest a mage can take,\nby zone."
	in.RendererParams = map[string]any{"arrows": true, "edge_labels": false}
	in.LayoutMode = LayoutManual
	in.LayoutSeed = ptrInt32(7)
	written, err := g.views.UpsertView(ctx, g.projectID, in)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if written.Version != 1 {
		t.Fatalf("Version = %d, want 1 on a creation", written.Version)
	}

	got, err := g.views.ViewByKey(ctx, g.projectID, "mage_route")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.ID != written.ID || got.Key != "mage_route" || got.Name != "Mage route" {
		t.Fatalf("identity = (%s, %q, %q), want the row that was written",
			got.ID, got.Key, got.Name)
	}
	if got.Description != in.Description {
		t.Fatalf("Description = %q, want %q", got.Description, in.Description)
	}
	if got.Renderer != RendererGraph {
		t.Fatalf("Renderer = %q, want %q", got.Renderer, RendererGraph)
	}
	var params map[string]any
	if err := json.Unmarshal(got.RendererParams, &params); err != nil {
		t.Fatalf("decode renderer_params: %v", err)
	}
	if len(params) != 2 || params["arrows"] != true || params["edge_labels"] != false {
		t.Fatalf("renderer_params = %v, want both parameters as they were saved", params)
	}
	if got.LayoutMode != LayoutManual {
		t.Fatalf("LayoutMode = %q, want %q", got.LayoutMode, LayoutManual)
	}
	if got.LayoutSeed != 7 {
		t.Fatalf("LayoutSeed = %d, want 7", got.LayoutSeed)
	}
	// The query is stored as authored, which is what makes Task 12's
	// "a rename does not rewrite the query" checkable at all.
	var stored, sent any
	if err := json.Unmarshal(got.Query, &stored); err != nil {
		t.Fatalf("decode the stored query: %v", err)
	}
	if err := json.Unmarshal([]byte(questsToZones), &sent); err != nil {
		t.Fatalf("decode the sent query: %v", err)
	}
	if fmt.Sprint(stored) != fmt.Sprint(sent) {
		t.Fatalf("stored query = %v, want the document as it was written: %v", stored, sent)
	}
}

// TestTheDefaultsAViewIsStoredWithAreTheColumnsOwn pins that a caller
// who says nothing about layout gets the mode and the seed 0008_views.sql
// declares, rather than an empty string that the CHECK would refuse and a
// seed of zero that would draw a different diagram from the default.
func TestTheDefaultsAViewIsStoredWithAreTheColumnsOwn(t *testing.T) {
	g, _ := newGame(t)
	row, err := g.views.UpsertView(context.Background(), g.projectID,
		saveable("plain", questsOnly))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if row.LayoutMode != DefaultLayoutMode {
		t.Fatalf("LayoutMode = %q, want %q", row.LayoutMode, DefaultLayoutMode)
	}
	if row.LayoutSeed != DefaultLayoutSeed {
		t.Fatalf("LayoutSeed = %d, want %d", row.LayoutSeed, DefaultLayoutSeed)
	}
	if string(row.RendererParams) != "{}" {
		t.Fatalf("renderer_params = %s, want an empty object rather than null: a reader "+
			"must not have to handle two spellings of \"no parameters\"", row.RendererParams)
	}
}

// TestASeedOfZeroIsStoredRatherThanReplacedByTheDefault is why
// LayoutSeed is a pointer. With a plain int32 the zero value and the
// deliberate choice are one value, and a designer who pinned a layout on
// seed 0 would silently get seed 1 — a different diagram, saved under the
// number they chose.
func TestASeedOfZeroIsStoredRatherThanReplacedByTheDefault(t *testing.T) {
	g, _ := newGame(t)
	in := saveable("zero_seed", questsOnly)
	in.LayoutSeed = ptrInt32(0)
	row, err := g.views.UpsertView(context.Background(), g.projectID, in)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if row.LayoutSeed != 0 {
		t.Fatalf("LayoutSeed = %d, want 0: a seed a caller chose must not be read as unset",
			row.LayoutSeed)
	}
}

// TestASecondUpsertMustCarryTheStoredVersion pins the compare-and-set
// from the caller's side: no version at all against an existing view is
// a conflict rather than an overwrite, a stale one is a conflict naming
// the version to merge onto, and the right one lands and bumps.
func TestASecondUpsertMustCarryTheStoredVersion(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	if _, err := g.views.UpsertView(ctx, g.projectID, saveable("route", questsOnly)); err != nil {
		t.Fatalf("create: %v", err)
	}

	silent := saveable("route", questsOnly)
	silent.Name = "Overwritten"
	_, err := g.views.UpsertView(ctx, g.projectID, silent)
	var conflict *metamodel.VersionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want a version conflict: a caller with no version is not "+
			"creating over a view that already exists", err)
	}
	if conflict.Current != 1 {
		t.Fatalf("Current = %d, want 1", conflict.Current)
	}

	stale := saveable("route", questsOnly)
	stale.ExpectedVersion = ptrInt32(7)
	if _, err := g.views.UpsertView(ctx, g.projectID, stale); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("err = %v, want a version conflict for a stale version", err)
	}

	ok := saveable("route", questsToZones)
	ok.Name = "Mine"
	ok.ExpectedVersion = ptrInt32(1)
	row, err := g.views.UpsertView(ctx, g.projectID, ok)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if row.Version != 2 || row.Name != "Mine" {
		t.Fatalf("row = (%d, %q), want version 2 and the new name", row.Version, row.Name)
	}
	// The refused writes above left nothing behind: one row, at the
	// version this call produced.
	got, err := g.views.ViewByKey(ctx, g.projectID, "route")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Version != 2 || got.Name != "Mine" {
		t.Fatalf("stored = (%d, %q), want only the accepted write to have landed",
			got.Version, got.Name)
	}
}

// TestTheReportedCurrentViewVersionIsTheOneTheWriteWouldHaveMet pins the
// FOR UPDATE on GetViewByKeyForUpdate.
//
// Deleting the lock leaves every other test in this file green: the
// guarded DO UPDATE refuses a lost update on its own. What the lock earns
// is the *number* the caller is told to merge onto. Without it the read
// runs against this transaction's snapshot and reports the version
// committed when it started, so a caller racing an in-flight edit
// re-issues with a version that is already stale and is refused again —
// a loop it cannot leave by doing what the error said.
func TestTheReportedCurrentViewVersionIsTheOneTheWriteWouldHaveMet(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	row, err := g.views.UpsertView(ctx, g.projectID, saveable("route", questsOnly))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	rival, err := g.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = rival.Rollback(ctx) }()
	if _, err := rival.Exec(ctx,
		`UPDATE views SET version = version + 1, name = 'Theirs' WHERE id = $1`,
		row.ID); err != nil {
		t.Fatalf("rival update: %v", err)
	}

	// No ExpectedVersion, so the refusal is decided by the locked read
	// alone and the version reported is that read's answer.
	result := make(chan error, 1)
	go func() {
		_, err := g.views.UpsertView(ctx, g.projectID, saveable("route", questsOnly))
		result <- err
	}()

	select {
	case err := <-result:
		t.Fatalf("the upsert returned %v without waiting for the rival's row lock", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := rival.Commit(ctx); err != nil {
		t.Fatalf("rival commit: %v", err)
	}

	select {
	case err := <-result:
		var conflict *metamodel.VersionConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("err = %v, want a *VersionConflictError", err)
		}
		if conflict.Current != 2 {
			t.Fatalf("Current = %d, want 2: the caller must be told the version its own "+
				"write would have met, not the one visible before the rival committed",
				conflict.Current)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the upsert never returned after the rival committed")
	}
}

// TestTheGuardedUpsertIsWhatRefusesACreationThatRacedAnother reaches the
// path the locked read cannot: on a creation there is nothing to lock, so
// the guard on the DO UPDATE is the only thing between the loser of the
// race and a silent overwrite.
//
// The overlap is staged rather than hoped for. Unstaged, the locked read
// finds the committed row and refuses in Go, and the SQL guard is never
// evaluated at all.
func TestTheGuardedUpsertIsWhatRefusesACreationThatRacedAnother(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()

	// Another writer's creation, begun and not yet committed. Ours cannot
	// see it, cannot lock it, and will meet it at views_key_key.
	other, err := g.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = other.Rollback(ctx) }()
	if _, err := other.Exec(ctx,
		`INSERT INTO views (project_id, key, name, query, renderer)
		 VALUES ($1, 'route', 'Theirs', '{"v":1}'::jsonb, 'graph')`, g.projectID); err != nil {
		t.Fatalf("the other writer's insert: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		in := saveable("route", questsOnly)
		in.Name = "Ours"
		_, err := g.views.UpsertView(ctx, g.projectID, in)
		done <- err
	}()

	waitForABlockedStatement(t, g.pool)
	if err := other.Commit(ctx); err != nil {
		t.Fatalf("commit the other writer: %v", err)
	}

	err = <-done
	var conflict *metamodel.VersionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want a version conflict from the losing creation, got %#v", err)
	}
	if conflict.Current != 1 {
		t.Fatalf("Current = %d, want 1: the key was created while this write was in flight",
			conflict.Current)
	}
	got, err := g.views.ViewByKey(ctx, g.projectID, "route")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Name != "Theirs" || got.Version != 1 {
		t.Fatalf("view = (%q, %d), want the winner's row untouched at version 1",
			got.Name, got.Version)
	}
}

// waitForABlockedStatement waits until something in this database is
// waiting on a lock, which is how a staged race knows the second writer
// has reached the statement that will block.
func waitForABlockedStatement(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var blocked int
		if err := pool.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity
			  WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&blocked); err != nil {
			t.Fatalf("inspect pg_stat_activity: %v", err)
		}
		if blocked > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no statement blocked on a lock within 10s: the race this test stages did not happen")
}

// TestARespeltViewKeyIsRefusedRatherThanRewritingTheStoredOne pins that
// keys are matched without regard to case and the first spelling stands:
// the key is the handle a designer bookmarks and an agent re-seeds
// against, and a second spelling is a rewrite of it rather than a new
// view.
func TestARespeltViewKeyIsRefusedRatherThanRewritingTheStoredOne(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	if _, err := g.views.UpsertView(ctx, g.projectID, saveable("route", questsOnly)); err != nil {
		t.Fatalf("create: %v", err)
	}
	in := saveable("Route", questsOnly)
	in.ExpectedVersion = ptrInt32(1)
	_, err := g.views.UpsertView(ctx, g.projectID, in)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want invalid_input naming the stored spelling", err)
	}
	if !strings.Contains(err.Error(), `"route"`) {
		t.Fatalf("err = %v, want the stored spelling in the message", err)
	}
	got, err := g.views.ViewByKey(ctx, g.projectID, "ROUTE")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Key != "route" || got.Version != 1 {
		t.Fatalf("view = (%q, %d), want the first spelling at version 1", got.Key, got.Version)
	}
}

// TestEveryTypeAQueryNamesGetsARefAtItsPointer pins the dependency index
// the whole view_refs table exists for: one row per reference, carrying
// the kind, the key as the query spells it, the resolved id and the
// pointer that addresses it.
func TestEveryTypeAQueryNamesGetsARefAtItsPointer(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	row, err := g.views.UpsertView(ctx, g.projectID, saveable("route", questsToZones))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	refs, err := g.views.ViewRefs(ctx, g.projectID, row.ID)
	if err != nil {
		t.Fatalf("list refs: %v", err)
	}
	want := map[string][2]string{
		"/from/0/type":          {KindEntityType, "quest"},
		"/traverse/0/to_type/0": {KindEntityType, "zone"},
		"/traverse/0/via/0":     {KindRelationType, "takes_place_in"},
	}
	if len(refs) != len(want) {
		t.Fatalf("%d refs, want %d: %+v", len(refs), len(want), refs)
	}
	for _, ref := range refs {
		expect, ok := want[ref.Pointer]
		if !ok {
			t.Fatalf("unexpected ref at %s", ref.Pointer)
		}
		if ref.Kind != expect[0] || ref.RefKey != expect[1] {
			t.Fatalf("ref at %s = (%q, %q), want (%q, %q)",
				ref.Pointer, ref.Kind, ref.RefKey, expect[0], expect[1])
		}
		// The id is what makes a rename resolve, and which column holds
		// it is what the table's own CHECK is about.
		switch ref.Kind {
		case KindEntityType:
			if ref.EntityTypeID == nil || ref.RelationTypeID != nil {
				t.Fatalf("ref at %s carries the wrong id column: %+v", ref.Pointer, ref)
			}
		case KindRelationType:
			if ref.RelationTypeID == nil || ref.EntityTypeID != nil {
				t.Fatalf("ref at %s carries the wrong id column: %+v", ref.Pointer, ref)
			}
		}
	}
}

// TestTheRefsAreRewrittenWithTheQueryTheyIndex is the second half of the
// same invariant: an edit that drops a reference drops its row, so the
// index never describes a query that is no longer stored.
func TestTheRefsAreRewrittenWithTheQueryTheyIndex(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	row, err := g.views.UpsertView(ctx, g.projectID, saveable("route", questsToZones))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	in := saveable("route", questsOnly)
	in.ExpectedVersion = ptrInt32(1)
	if _, err := g.views.UpsertView(ctx, g.projectID, in); err != nil {
		t.Fatalf("update: %v", err)
	}
	refs, err := g.views.ViewRefs(ctx, g.projectID, row.ID)
	if err != nil {
		t.Fatalf("list refs: %v", err)
	}
	if len(refs) != 1 || refs[0].Pointer != "/from/0/type" || refs[0].RefKey != "quest" {
		t.Fatalf("refs = %+v, want only the reference the new query makes", refs)
	}
}

// TestARefusedUpsertLeavesTheRefsOfTheQueryThatIsStored is the placement
// half of "the refs are rewritten in the same transaction". They are
// written after the compare-and-set and inside it, so a write the guard
// refuses leaves the index describing the query that is actually
// stored — where refs written before the guard, or outside the
// transaction, would leave a view indexed by a query nobody wrote.
func TestARefusedUpsertLeavesTheRefsOfTheQueryThatIsStored(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	row, err := g.views.UpsertView(ctx, g.projectID, saveable("route", questsOnly))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	stale := saveable("route", questsToZones)
	stale.ExpectedVersion = ptrInt32(99)
	if _, err := g.views.UpsertView(ctx, g.projectID, stale); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("err = %v, want a version conflict", err)
	}
	refs, err := g.views.ViewRefs(ctx, g.projectID, row.ID)
	if err != nil {
		t.Fatalf("list refs: %v", err)
	}
	if len(refs) != 1 || refs[0].RefKey != "quest" {
		t.Fatalf("refs = %+v, want the stored query's single reference: a refused write "+
			"must not leave its own refs behind", refs)
	}
}

// TestAQueryThatDoesNotResolveIsRefusedAndNothingIsStored is why the
// stored document is the one resolution returned rather than the one
// ParseQuery returned. The document below parses — it is structurally a
// query — and names a type this game does not declare, which only
// resolution can know.
func TestAQueryThatDoesNotResolveIsRefusedAndNothingIsStored(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	in := saveable("route", `{"v":1,"from":[{"type":"qeust","as":"q"}]}`)
	_, err := g.views.UpsertView(ctx, g.projectID, in)
	if !errors.Is(err, ErrQueryInvalid) {
		t.Fatalf("err = %v, want query_invalid", err)
	}
	var qe *QueryError
	if !errors.As(err, &qe) || len(qe.Fields) != 1 || qe.Fields[0].Path != "/from/0/type" {
		t.Fatalf("err = %#v, want one problem at /from/0/type", err)
	}
	if _, err := g.views.ViewByKey(ctx, g.projectID, "route"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ViewByKey = %v, want not_found: nothing may be stored for a query "+
			"that resolves against nothing", err)
	}
}

// TestARendererThatCannotDrawTheQueryIsRefusedAtSaveTime is the reason
// this call reaches CheckRenderer at all: a view that saves and then
// cannot be drawn is the failure the renderer catalogue exists to
// prevent, and save time is the only moment at which anybody is there to
// read the refusal.
//
// The two halves are the catalogue's two codes, which have two different
// recoveries: an unknown renderer is "fix this argument", and a renderer
// whose requirements this query cannot meet is "pick another renderer, or
// change the query".
func TestARendererThatCannotDrawTheQueryIsRefusedAtSaveTime(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()

	unknown := saveable("route", questsOnly)
	unknown.Renderer = "sankey"
	_, err := g.views.UpsertView(ctx, g.projectID, unknown)
	if !errors.Is(err, ErrQueryInvalid) {
		t.Fatalf("err = %v, want query_invalid for a renderer that is not in the catalogue", err)
	}

	// layered ranks nodes by the edges between them, and questsOnly draws
	// none. The renderer is real, the parameters are fine, and the query
	// cannot feed it.
	unmeetable := saveable("route", questsOnly)
	unmeetable.Renderer = RendererLayered
	_, err = g.views.UpsertView(ctx, g.projectID, unmeetable)
	if !errors.Is(err, ErrRendererRequirements) {
		t.Fatalf("err = %v, want renderer_requirements", err)
	}
	if _, err := g.views.ViewByKey(ctx, g.projectID, "route"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ViewByKey = %v, want not_found: a view that cannot be drawn is not "+
			"stored", err)
	}

	// The control: the same renderer over a query that does draw edges.
	ok := saveable("route", questsToZones)
	ok.Renderer = RendererLayered
	if _, err := g.views.UpsertView(ctx, g.projectID, ok); err != nil {
		t.Fatalf("layered over a query that draws edges must save: %v", err)
	}
}

// TestARendererParameterIsJudgedAgainstTheQueryItIsSavedWith reaches the
// half of the catalogue a Requires cannot: a parameter whose value is
// well formed and names something this query does not carry.
func TestARendererParameterIsJudgedAgainstTheQueryItIsSavedWith(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	in := saveable("route", questsOnly)
	in.RendererParams = map[string]any{"color_by": "group_by"}
	_, err := g.views.UpsertView(ctx, g.projectID, in)
	if !errors.Is(err, ErrRendererRequirements) {
		t.Fatalf("err = %v, want renderer_requirements: the query declares no group_by slot", err)
	}
	var qe *QueryError
	if !errors.As(err, &qe) || len(qe.Fields) != 1 ||
		qe.Fields[0].Path != "/renderer_params/color_by" {
		t.Fatalf("err = %#v, want one problem addressed at the parameter", err)
	}
	// The control: the same parameter over a query whose projection
	// declares the slot it names.
	ok := saveable("route", `{"v":1,"from":[{"type":"quest","as":"q"}],
		"project":{"group_by":"@type"}}`)
	ok.RendererParams = map[string]any{"color_by": "group_by"}
	if _, err := g.views.UpsertView(ctx, g.projectID, ok); err != nil {
		t.Fatalf("a slot the projection declares must save: %v", err)
	}
}

// TestAViewsOwnArgumentsAreRefusedBeforePostgresSeesThem covers the four
// the row carries besides its query. Each of them is a caller's argument
// at its own path, and each would otherwise reach the caller as an
// untyped server fault — a CHECK violation (SQLSTATE 23514) for the
// layout mode, `invalid byte sequence for encoding "UTF8"` (22021) for a
// control character — over a value the caller itself supplied.
func TestAViewsOwnArgumentsAreRefusedBeforePostgresSeesThem(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		in   func(ViewInput) ViewInput
		path string
	}{
		{"an empty key", func(in ViewInput) ViewInput { in.Key = ""; return in }, "/key"},
		{"an empty name", func(in ViewInput) ViewInput { in.Name = ""; return in }, "/name"},
		{"a name with a newline in it",
			func(in ViewInput) ViewInput { in.Name = "one\ntwo"; return in }, "/name"},
		{"an overlong name", func(in ViewInput) ViewInput {
			in.Name = strings.Repeat("x", MaxViewNameLen+1)
			return in
		}, "/name"},
		{"a description with a NUL in it",
			func(in ViewInput) ViewInput { in.Description = "a\x00b"; return in }, "/description"},
		{"an overlong description", func(in ViewInput) ViewInput {
			in.Description = strings.Repeat("x", MaxViewDescriptionLen+1)
			return in
		}, "/description"},
		{"a layout mode outside the three",
			func(in ViewInput) ViewInput { in.LayoutMode = "grid"; return in }, "/layout_mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := g.views.UpsertView(ctx, g.projectID, tc.in(saveable("route", questsOnly)))
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("err = %v, want invalid_input", err)
			}
			var ve *metamodel.ValidationError
			if !errors.As(err, &ve) || len(ve.Fields) != 1 || ve.Fields[0].Path != tc.path {
				t.Fatalf("err = %#v, want one problem at %s", err, tc.path)
			}
		})
	}
	// A description *may* hold a paragraph break, which is the one
	// asymmetry between the two pieces of prose a view carries; without
	// this control the rule above would be satisfied by refusing every
	// control character in both.
	in := saveable("route", questsOnly)
	in.Description = "two\nparagraphs"
	if _, err := g.views.UpsertView(ctx, g.projectID, in); err != nil {
		t.Fatalf("a description is prose and may hold a newline: %v", err)
	}
}

// TestEveryProblemWithAViewsArgumentsIsReportedInOnePass: an agent whose
// name and layout mode are both wrong fixes both in one round trip.
func TestEveryProblemWithAViewsArgumentsIsReportedInOnePass(t *testing.T) {
	g, _ := newGame(t)
	in := saveable("route", questsOnly)
	in.Key = "not a key"
	in.Name = ""
	in.LayoutMode = "grid"
	_, err := g.views.UpsertView(context.Background(), g.projectID, in)
	var ve *metamodel.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a *metamodel.ValidationError", err)
	}
	seen := map[string]bool{}
	for _, f := range ve.Fields {
		seen[f.Path] = true
	}
	for _, want := range []string{"/key", "/name", "/layout_mode"} {
		if !seen[want] {
			t.Fatalf("problems = %+v, want one at %s too", ve.Fields, want)
		}
	}
}

// TestAViewsOwnArgumentsAreAnsweredBeforeItsQuery pins the order of the
// four passes. A caller whose name is empty and whose query names nothing
// this game has hears about the name: every later pass needs a document
// that parsed, and one error carries one code.
func TestAViewsOwnArgumentsAreAnsweredBeforeItsQuery(t *testing.T) {
	g, _ := newGame(t)
	in := saveable("route", `{"v":1,"from":[{"type":"qeust","as":"q"}]}`)
	in.Name = ""
	_, err := g.views.UpsertView(context.Background(), g.projectID, in)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want the argument problem rather than the query's", err)
	}
}

// TestReadingAnotherGamesViewIsNotFound is the isolation test, and it is
// why newGame seeds two games with the same vocabulary: with one game,
// every project filter in views.sql could be deleted and nothing here
// would notice.
func TestReadingAnotherGamesViewIsNotFound(t *testing.T) {
	azeroth, outland := newGame(t)
	ctx := context.Background()
	mine, err := azeroth.views.UpsertView(ctx, azeroth.projectID, saveable("route", questsOnly))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// The positive control: outland has quests too, and the same key, so
	// the only thing that differs between the two reads is the game.
	theirs, err := outland.views.UpsertView(ctx, outland.projectID, saveable("route", questsOnly))
	if err != nil {
		t.Fatalf("upsert in the other game: %v", err)
	}
	if theirs.ID == mine.ID {
		t.Fatal("the same key in two games must be two rows")
	}
	got, err := outland.views.ViewByKey(ctx, outland.projectID, "route")
	if err != nil || got.ID != theirs.ID {
		t.Fatalf("ViewByKey = (%v, %v), want outland's own row", got.ID, err)
	}
	if _, err := outland.views.ViewByID(ctx, outland.projectID, mine.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ViewByID = %v, want not_found: a view id in a caller's hand is not "+
			"authority to read it", err)
	}
	refs, err := outland.views.ViewRefs(ctx, outland.projectID, mine.ID)
	if err != nil {
		t.Fatalf("list refs: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("refs = %+v, want none: another game's view id reads no refs here", refs)
	}
}

// TestRemovingAViewTakesItsRefsAndAnswersTheSecondCallWithNotFound.
func TestRemovingAViewTakesItsRefsAndAnswersTheSecondCallWithNotFound(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	row, err := g.views.UpsertView(ctx, g.projectID, saveable("route", questsToZones))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := g.views.RemoveView(ctx, g.projectID, "ROUTE"); err != nil {
		t.Fatalf("remove, matched without regard to case: %v", err)
	}
	if _, err := g.views.ViewByKey(ctx, g.projectID, "route"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ViewByKey = %v, want not_found", err)
	}
	var refs int
	if err := g.pool.QueryRow(ctx,
		`SELECT count(*) FROM view_refs WHERE view_id = $1`, row.ID).Scan(&refs); err != nil {
		t.Fatalf("count refs: %v", err)
	}
	if refs != 0 {
		t.Fatalf("%d refs survived the view, want 0", refs)
	}
	if err := g.views.RemoveView(ctx, g.projectID, "route"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second remove = %v, want not_found", err)
	}
}

// TestRemovingAnotherGamesViewIsNotFound: the delete is addressed by id,
// and an id alone is not authority.
func TestRemovingAnotherGamesViewIsNotFound(t *testing.T) {
	azeroth, outland := newGame(t)
	ctx := context.Background()
	if _, err := azeroth.views.UpsertView(ctx, azeroth.projectID,
		saveable("route", questsOnly)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := outland.views.RemoveView(ctx, outland.projectID, "route"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want not_found", err)
	}
	if _, err := azeroth.views.ViewByKey(ctx, azeroth.projectID, "route"); err != nil {
		t.Fatalf("azeroth's view must still be there: %v", err)
	}
}

// TestViewsDependingOnATypeAreFoundByIdWithTheirPointers pins the lookup
// ON DELETE SET NULL exists to serve: which views a type holds up, and
// where in each query, as one indexed query rather than a scan over every
// stored document.
func TestViewsDependingOnATypeAreFoundByIdWithTheirPointers(t *testing.T) {
	azeroth, outland := newGame(t)
	ctx := context.Background()
	if _, err := azeroth.views.UpsertView(ctx, azeroth.projectID,
		saveable("route", questsToZones)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// The other game's view names the same keys and must not be listed.
	if _, err := outland.views.UpsertView(ctx, outland.projectID,
		saveable("route", questsToZones)); err != nil {
		t.Fatalf("upsert in the other game: %v", err)
	}
	cat, err := azeroth.views.LoadCatalogue(ctx, azeroth.projectID)
	if err != nil {
		t.Fatalf("catalogue: %v", err)
	}

	zone := cat.EntityTypes["zone"]
	got, err := azeroth.views.ViewsDependingOn(ctx, azeroth.projectID, KindEntityType, zone.ID)
	if err != nil {
		t.Fatalf("depending on zone: %v", err)
	}
	if len(got) != 1 || got[0].ViewKey != "route" || got[0].Pointer != "/traverse/0/to_type/0" {
		t.Fatalf("got %+v, want azeroth's view at the pointer that names zone", got)
	}

	via := cat.RelationTypes["takes_place_in"]
	got, err = azeroth.views.ViewsDependingOn(ctx, azeroth.projectID, KindRelationType, via.ID)
	if err != nil {
		t.Fatalf("depending on takes_place_in: %v", err)
	}
	if len(got) != 1 || got[0].Pointer != "/traverse/0/via/0" || got[0].RefKey != "takes_place_in" {
		t.Fatalf("got %+v, want the relation-type reference", got)
	}

	// The control: a type no view names is held up by nothing. Without it
	// an implementation that ignored its argument and returned every ref
	// would pass both assertions above.
	class := cat.EntityTypes["class"]
	got, err = azeroth.views.ViewsDependingOn(ctx, azeroth.projectID, KindEntityType, class.ID)
	if err != nil {
		t.Fatalf("depending on class: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want nothing: no view names class", got)
	}
	// And the relation type's own id asked as an entity type finds
	// nothing, which is what keeps the two arms from being one.
	got, err = azeroth.views.ViewsDependingOn(ctx, azeroth.projectID, KindEntityType, via.ID)
	if err != nil {
		t.Fatalf("depending on a relation type asked as an entity type: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want nothing: the two kinds are two columns", got)
	}
}

// TestViewEventsReachEveryMemberOfTheGameIncludingAgents pins the gating
// events.go argues: MinRole empty, HumanOnly false. The two subscribers
// here are the ones a wrong decision would silently cut out — a viewer,
// excluded by any MinRole above viewer, and a token caller, excluded by
// HumanOnly regardless of role.
func TestViewEventsReachEveryMemberOfTheGameIncludingAgents(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	hub := realtime.NewHub()
	svc := New(g.pool, hub)

	viewer := hub.Subscribe(g.projectID, "viewer", false)
	defer hub.Unsubscribe(viewer)
	agent := hub.Subscribe(g.projectID, "viewer", true)
	defer hub.Unsubscribe(agent)

	row, err := svc.UpsertView(ctx, g.projectID, saveable("route", questsOnly))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	for who, sub := range map[string]*realtime.Subscription{"viewer": viewer, "agent": agent} {
		got := receive(t, sub)
		if got.Kind != "view.upserted" {
			t.Fatalf("%s got %q, want view.upserted", who, got.Kind)
		}
		assertViewPayload(t, who, got, row.ID, "route", 1)
	}

	if err := svc.RemoveView(ctx, g.projectID, "route"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	for who, sub := range map[string]*realtime.Subscription{"viewer": viewer, "agent": agent} {
		got := receive(t, sub)
		if got.Kind != "view.removed" {
			t.Fatalf("%s got %q, want view.removed", who, got.Kind)
		}
		assertViewPayload(t, who, got, row.ID, "route", 1)
	}
}

// assertViewPayload checks the {id, key, version} an event carries. The
// payload type is unexported, so the assertion goes through the JSON
// shape, which is the only thing a client ever sees and the only thing
// this package promises.
func assertViewPayload(t *testing.T, who string, e realtime.Event,
	wantID uuid.UUID, wantKey string, wantVersion float64,
) {
	t.Helper()
	raw, err := json.Marshal(e.Payload)
	if err != nil {
		t.Fatalf("%s: marshal payload: %v", who, err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%s: decode payload: %v", who, err)
	}
	if len(got) != 3 {
		t.Fatalf("%s: payload = %v, want identity only: id, key and version", who, got)
	}
	if got["id"] != wantID.String() || got["key"] != wantKey || got["version"] != wantVersion {
		t.Fatalf("%s: payload = %v, want {%s, %q, %v}", who, got, wantID, wantKey, wantVersion)
	}
}

func receive(t *testing.T, sub *realtime.Subscription) realtime.Event {
	t.Helper()
	select {
	case got := <-sub.C:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an event")
		return realtime.Event{}
	}
}

func requireNothing(t *testing.T, sub *realtime.Subscription, why string) {
	t.Helper()
	select {
	case got := <-sub.C:
		t.Fatalf("%s, but %q was published: %+v", why, got.Kind, got.Payload)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestNoViewEventIsPublishedWhenARefusedWriteRollsBack is the ordinary
// half: a write refused inside the transaction announces nothing.
func TestNoViewEventIsPublishedWhenARefusedWriteRollsBack(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	hub := realtime.NewHub()
	svc := New(g.pool, hub)

	if _, err := svc.UpsertView(ctx, g.projectID, saveable("route", questsOnly)); err != nil {
		t.Fatalf("create: %v", err)
	}
	sub := hub.Subscribe(g.projectID, "owner", false)
	defer hub.Unsubscribe(sub)

	stale := saveable("route", questsOnly)
	stale.ExpectedVersion = ptrInt32(99)
	if _, err := svc.UpsertView(ctx, g.projectID, stale); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("err = %v, want a version conflict", err)
	}
	requireNothing(t, sub, "the write was refused")
}

// TestNoViewEventIsPublishedWhenTheCommitFails is the one placement a
// refusal test cannot catch: a publish written as the last statement
// inside the transaction's callback differs from the correct one only by
// the commit that follows, and a rolled-back write and a refused write
// look identical from outside.
//
// It is reachable because testutil.NewPool hands every test its own
// throwaway database, so this test may install a constraint no other test
// sees. A deferred foreign key from views.id to projects.id is satisfied
// by nothing — a view's id is not a project id — but being DEFERRABLE
// INITIALLY DEFERRED it is checked at COMMIT and not before, so every
// statement inside the transaction succeeds and only the commit fails,
// with SQLSTATE 23503. That is exactly the window a last-statement
// publish would announce into: the database has accepted every write, and
// then thrown all of them away.
//
// internal/metamodel's test of the same name is the model.
func TestNoViewEventIsPublishedWhenTheCommitFails(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	hub := realtime.NewHub()
	svc := New(g.pool, hub)

	// Added while views is empty: ADD CONSTRAINT validates the rows
	// already stored.
	if _, err := g.pool.Exec(ctx,
		`ALTER TABLE views ADD CONSTRAINT zz_fail_at_commit
		   FOREIGN KEY (id) REFERENCES projects (id) DEFERRABLE INITIALLY DEFERRED`); err != nil {
		t.Fatalf("install the deferred constraint: %v", err)
	}

	sub := hub.Subscribe(g.projectID, "owner", false)
	defer hub.Unsubscribe(sub)

	_, err := svc.UpsertView(ctx, g.projectID, saveable("route", questsToZones))
	if err == nil {
		t.Fatal("the commit must fail under the deferred constraint")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("err = %v, want a deferred foreign-key violation at commit", err)
	}
	requireNothing(t, sub, "the transaction never committed")

	if _, err := svc.ViewByKey(ctx, g.projectID, "route"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ViewByKey = %v, want not_found: nothing was stored", err)
	}
	// The refs go with it, which is the other half of "one change": they
	// are written inside the same transaction, so a commit that fails
	// takes them too.
	var refs int
	if err := g.pool.QueryRow(ctx,
		`SELECT count(*) FROM view_refs WHERE project_id = $1`, g.projectID).Scan(&refs); err != nil {
		t.Fatalf("count refs: %v", err)
	}
	if refs != 0 {
		t.Fatalf("%d ref rows survived a transaction that never committed, want 0", refs)
	}
}
