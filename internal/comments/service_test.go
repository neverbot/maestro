package comments_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/comments"
	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/testutil"
)

// game is one project with the two types and the one edge every claim
// below writes a comment against.
type game struct {
	pool      *pgxpool.Pool
	meta      *metamodel.Service
	log       *comments.Service
	projectID uuid.UUID
}

func newGame(t *testing.T) (*game, *game) {
	t.Helper()
	pool := testutil.NewPool(t)
	suffix := "-" + uuid.NewString()[:8]
	build := func(slug string) *game {
		ctx := context.Background()
		var projectID uuid.UUID
		if err := pool.QueryRow(ctx,
			`INSERT INTO projects (slug, name) VALUES ($1, $1) RETURNING id`, slug+suffix).
			Scan(&projectID); err != nil {
			t.Fatalf("insert project: %v", err)
		}
		meta := metamodel.New(pool, nil)
		g := &game{pool: pool, meta: meta, log: comments.New(pool, meta), projectID: projectID}
		g.seed(t)
		return g
	}
	return build("azeroth"), build("outland")
}

func (g *game) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, key := range []string{"quest", "zone"} {
		if _, err := g.meta.UpsertEntityType(ctx, g.projectID, metamodel.EntityTypeInput{
			Key: key, Label: key, LabelPlural: key + "s",
		}); err != nil {
			t.Fatalf("declare %s: %v", key, err)
		}
	}
	if _, err := g.meta.UpsertRelationType(ctx, g.projectID, metamodel.RelationTypeInput{
		Key: "takes_place_in", Label: "takes place in",
	}); err != nil {
		t.Fatalf("declare relation type: %v", err)
	}
	for _, pair := range [][2]string{{"quest", "hogger"}, {"zone", "elwynn"}} {
		if _, err := g.meta.UpsertEntity(ctx, g.projectID, metamodel.EntityInput{
			TypeKey: pair[0], Key: pair[1], Name: pair[1],
		}); err != nil {
			t.Fatalf("seed %v: %v", pair, err)
		}
	}
	if _, err := g.meta.UpsertRelation(ctx, g.projectID, metamodel.RelationInput{
		TypeKey: "takes_place_in",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Target:  metamodel.Ref{TypeKey: "zone", Key: "elwynn"},
	}); err != nil {
		t.Fatalf("seed edge: %v", err)
	}
}

func onEntity() comments.Target {
	return comments.Target{Kind: comments.OnEntity, TypeKey: "quest", Key: "hogger"}
}

func onEdge() comments.Target {
	return comments.Target{
		Kind: comments.OnRelation, TypeKey: "takes_place_in",
		From: metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		To:   metamodel.Ref{TypeKey: "zone", Key: "elwynn"},
	}
}

func TestCommentsArea(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("a comment lands on each of the four things that can carry one", func(t *testing.T) {
		g, _ := newGame(t)
		for _, target := range []comments.Target{
			onEntity(),
			onEdge(),
			{Kind: comments.OnEntityType, TypeKey: "quest"},
			{Kind: comments.OnRelationType, TypeKey: "takes_place_in"},
		} {
			row, err := g.log.Add(ctx, g.projectID, target, "imported from the old build", comments.Actor{})
			assert.Must(t, err == nil, "add on %s: %v", target.Kind, err)
			assert.Must(t, row.Body == "imported from the old build", "body = %q", row.Body)

			got, err := g.log.List(ctx, g.projectID, target, 0)
			assert.Must(t, err == nil, "list on %s: %v", target.Kind, err)
			assert.Must(t, len(got) == 1, "%s carries %d comments, want 1", target.Kind, len(got))
		}
	})

	t.Run("a log is newest first", func(t *testing.T) {
		g, _ := newGame(t)
		for _, body := range []string{"first", "second", "third"} {
			if _, err := g.log.Add(ctx, g.projectID, onEntity(), body, comments.Actor{}); err != nil {
				t.Fatalf("add %q: %v", body, err)
			}
		}
		got, err := g.log.List(ctx, g.projectID, onEntity(), 0)
		assert.Must(t, err == nil, "list: %v", err)
		assert.Must(t, len(got) == 3 && got[0].Body == "third" && got[2].Body == "first",
			"a log read oldest first is a log nobody reads: %v", bodies(got))
	})

	t.Run("a comment about a thing this game does not have is refused", func(t *testing.T) {
		g, _ := newGame(t)
		_, err := g.log.Add(ctx, g.projectID,
			comments.Target{Kind: comments.OnEntity, TypeKey: "quest", Key: "nobody"},
			"about nothing", comments.Actor{})
		assert.Must(t, errors.Is(err, metamodel.ErrNotFound), "err = %v, want ErrNotFound: a comment on a uuid "+
			"nobody can reach is a note the log fills with and nothing ever reads", err)
	})

	t.Run("a comment with nothing in it is refused, and so is a page of prose", func(t *testing.T) {
		g, _ := newGame(t)
		for _, body := range []string{"", "   \n\t "} {
			_, err := g.log.Add(ctx, g.projectID, onEntity(), body, comments.Actor{})
			assert.Must(t, errors.Is(err, metamodel.ErrInvalidInput), "body %q: err = %v, want ErrInvalidInput", body, err)
		}
		_, err := g.log.Add(ctx, g.projectID, onEntity(), strings.Repeat("a", comments.MaxBodyRunes+1), comments.Actor{})
		assert.Must(t, errors.Is(err, metamodel.ErrInvalidInput), "err = %v, want ErrInvalidInput: prose that long is a document", err)
	})

	t.Run("one game's log is not another's", func(t *testing.T) {
		mine, theirs := newGame(t)
		row, err := mine.log.Add(ctx, mine.projectID, onEntity(), "mine", comments.Actor{})
		assert.Must(t, err == nil, "add: %v", err)

		// Their game has an entity under the same key, and its log is empty.
		got, err := mine.log.List(ctx, theirs.projectID, onEntity(), 0)
		assert.Must(t, err == nil, "list in the other game: %v", err)
		assert.Must(t, len(got) == 0, "the other game's entity carries %d of this game's comments", len(got))

		// And it cannot be removed from there either.
		err = mine.log.Remove(ctx, theirs.projectID, row.ID)
		assert.Must(t, errors.Is(err, metamodel.ErrNotFound), "err = %v, want ErrNotFound: a comment id is a value a "+
			"previous answer handed back, and nothing but the project filter keeps it inside one game", err)
	})

	t.Run("a comment can be removed and there is no way to rewrite one", func(t *testing.T) {
		g, _ := newGame(t)
		row, err := g.log.Add(ctx, g.projectID, onEntity(), "written against the wrong thing", comments.Actor{})
		assert.Must(t, err == nil, "add: %v", err)
		assert.Must(t, g.log.Remove(ctx, g.projectID, row.ID) == nil, "remove")
		got, err := g.log.List(ctx, g.projectID, onEntity(), 0)
		assert.Must(t, err == nil, "list: %v", err)
		assert.Must(t, len(got) == 0, "the comment survived its removal")
		assert.Must(t, errors.Is(g.log.Remove(ctx, g.projectID, row.ID), metamodel.ErrNotFound), "removing it twice is not_found")
	})

	t.Run("removing the thing takes its log with it", func(t *testing.T) {
		g, _ := newGame(t)
		if _, err := g.log.Add(ctx, g.projectID, onEntity(), "about Hogger", comments.Actor{}); err != nil {
			t.Fatalf("add: %v", err)
		}
		if err := g.meta.RemoveEntity(ctx, g.projectID, "quest", "hogger"); err != nil {
			t.Fatalf("remove entity: %v", err)
		}
		var left int
		if err := g.pool.QueryRow(ctx, `SELECT count(*) FROM comments WHERE project_id = $1`, g.projectID).Scan(&left); err != nil {
			t.Fatalf("count: %v", err)
		}
		assert.Must(t, left == 0, "%d comments outlived the thing they were about, and nothing can ever read them", left)
	})

	t.Run("the game's whole log is every comment whatever it is about", func(t *testing.T) {
		g, _ := newGame(t)
		for _, target := range []comments.Target{onEntity(), onEdge(), {Kind: comments.OnEntityType, TypeKey: "quest"}} {
			if _, err := g.log.Add(ctx, g.projectID, target, "a note", comments.Actor{}); err != nil {
				t.Fatalf("add on %s: %v", target.Kind, err)
			}
		}
		got, err := g.log.ListGame(ctx, g.projectID, 0)
		assert.Must(t, err == nil, "list the game: %v", err)
		assert.Must(t, len(got) == 3, "the game's log holds %d, want 3", len(got))
	})

	t.Run("a page of rows is counted in one query", func(t *testing.T) {
		g, _ := newGame(t)
		hogger, err := g.meta.EntityByKey(ctx, g.projectID, "quest", "hogger")
		assert.Must(t, err == nil, "read hogger: %v", err)
		elwynn, err := g.meta.EntityByKey(ctx, g.projectID, "zone", "elwynn")
		assert.Must(t, err == nil, "read elwynn: %v", err)
		for range 2 {
			if _, err := g.log.Add(ctx, g.projectID, onEntity(), "a note", comments.Actor{}); err != nil {
				t.Fatalf("add: %v", err)
			}
		}
		counts, err := g.log.CountsForEntities(ctx, g.projectID, []uuid.UUID{hogger.ID, elwynn.ID})
		assert.Must(t, err == nil, "counts: %v", err)
		assert.Must(t, counts[hogger.ID] == 2, "hogger = %d, want 2", counts[hogger.ID])
		_, counted := counts[elwynn.ID]
		assert.Must(t, !counted, "a row with no comments is absent from the counts rather than present as zero")
	})
}

func bodies(rows []dbq.Comment) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Body)
	}
	return out
}
