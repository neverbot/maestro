// Package comments is the log a game's designers and their agents keep
// beside the content: how a thing was imported, what was rewritten and
// why, an idea about the philosophy of a type, something worth doing
// later. Markdown, append-only, and about the work rather than about the
// game.
//
// **It is not a fifth primitive.** A game's own model is four things and
// this changes none of them: nothing here is something a player can be,
// go to, do or unlock, and no view, query or analysis reads a comment. A
// field holds the game; a comment holds what was thought about it.
//
// **It carries no state, and that is the line this package will not
// cross.** No status, no assignee, no due date, no "done". The day one of
// those appears, this has become a project tracker, which is the one
// thing the product says it is not.
package comments

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
)

// Actor is the metamodel's, aliased rather than redeclared: the audit
// columns on a comment are filled from the same caller.
type Actor = metamodel.Actor

// MaxBodyRunes bounds one comment. **A comment is a note, not a page**:
// prose that wants a title, a history and an address of its own is a
// document, and the bundle draws that line. The bound is here so the
// difference is a refusal rather than a habit.
const MaxBodyRunes = 4000

// DefaultPage and MaxPage bound a listing, the way every other listing on
// this surface is bounded.
const (
	DefaultPage int32 = 50
	MaxPage     int32 = 200
)

// Kind is what a comment is about.
type Kind string

// The three things that can carry one, and they are the three with a
// page. **A relation cannot**, since 0018: an edge has no screen, so a
// note left on one was reachable by this package and by nothing a
// designer opens, which is a place to write where nothing reads. **A
// document cannot** either: it already keeps a message per version,
// which is the same note in the place that can say which change it was
// about.
const (
	OnEntity       Kind = "entity"
	OnEntityType   Kind = "entity_type"
	OnRelationType Kind = "relation_type"
)

// Target addresses the one thing a comment is about, in the terms the
// rest of this surface speaks: keys, never ids.
type Target struct {
	Kind Kind
	// TypeKey is the entity type's key for an entity, and the type's own
	// key for either kind of type.
	TypeKey string
	// Key is an entity's own key, and is read for OnEntity alone.
	Key string
}

// Comment is one entry in the log, in the shape every surface reads: the
// four target columns collapse to the one Kind they encode, and the two
// audit columns collapse to the author's own name.
type Comment struct {
	ID        uuid.UUID
	Kind      Kind
	Body      string
	CreatedAt time.Time
	// Author is the token's label for an agent and the person's display
	// name for a person. Empty when the row that wrote it is gone, which
	// is what the audit columns do on a deleted user.
	Author string
	// ByAgent says which kind of author that was. **It is a fact of its
	// own and not a guess from the name**: a token's label is a word
	// somebody chose, and a game's own name reads as a person to anyone
	// who does not already know otherwise.
	ByAgent bool
	// AuthorOf is the person an agent's token traces back to, and the
	// author themselves when a person wrote it. It is what makes "whose
	// agent" answerable, which in a game with two designers is the
	// question under "who wrote this".
	AuthorOf string
}

// entry folds any of the five generated row shapes into one Comment.
// They are identical structs with five different names, which is what
// sqlc emits for five statements over one table.
func entry(id uuid.UUID, entityID, entityTypeID, tokenID *uuid.UUID, body string,
	createdAt pgtype.Timestamptz, author, authorOf string,
) Comment {
	kind := OnRelationType
	switch {
	case entityID != nil:
		kind = OnEntity
	case entityTypeID != nil:
		kind = OnEntityType
	}
	return Comment{
		ID: id, Kind: kind, Body: body, CreatedAt: createdAt.Time,
		Author: author, ByAgent: tokenID != nil, AuthorOf: authorOf,
	}
}

// Service is the comment log.
type Service struct {
	pool *pgxpool.Pool
	q    *dbq.Queries
	meta *metamodel.Service
}

// New builds the service. meta is what resolves a target's address to the
// row it names, so a comment cannot be written against a thing this game
// does not have.
func New(pool *pgxpool.Pool, meta *metamodel.Service) *Service {
	if pool == nil {
		return &Service{meta: meta}
	}
	return &Service{pool: pool, q: dbq.New(pool), meta: meta}
}

// Add writes one comment.
func (s *Service) Add(ctx context.Context, projectID uuid.UUID, target Target, body string, actor Actor) (Comment, error) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return Comment{}, refuse("body", "a comment with nothing in it says nothing")
	}
	if utf8.RuneCountInString(trimmed) > MaxBodyRunes {
		return Comment{}, refuse("body", fmt.Sprintf("holds at most %d characters and this one holds %d; "+
			"prose that wants a title and a history of its own is a document",
			MaxBodyRunes, utf8.RuneCountInString(trimmed)))
	}
	id, err := s.resolve(ctx, projectID, target)
	if err != nil {
		return Comment{}, err
	}
	params := dbq.InsertCommentParams{
		ProjectID: projectID,
		Body:      trimmed,
	}
	switch target.Kind {
	case OnEntity:
		params.EntityID = &id
	case OnEntityType:
		params.EntityTypeID = &id
	case OnRelationType:
		params.RelationTypeID = &id
	}
	if actor.UserID != nil {
		params.CreatedByUserID = actor.UserID
	}
	if actor.TokenID != nil {
		params.CreatedByTokenID = actor.TokenID
	}
	row, err := s.q.InsertComment(ctx, params)
	if err != nil {
		return Comment{}, fmt.Errorf("write comment: %w", err)
	}
	return entry(row.ID, row.EntityID, row.EntityTypeID, row.CreatedByTokenID, row.Body, row.CreatedAt, row.Author, row.AuthorOf), nil
}

// List reads one thing's log, newest first.
func (s *Service) List(ctx context.Context, projectID uuid.UUID, target Target, limit int32) ([]Comment, error) {
	id, err := s.resolve(ctx, projectID, target)
	if err != nil {
		return nil, err
	}
	bound := page(limit)
	out := []Comment{}
	switch target.Kind {
	case OnEntity:
		rows, err := s.q.ListCommentsOnEntity(ctx, dbq.ListCommentsOnEntityParams{ProjectID: projectID, EntityID: id, Lim: bound})
		if err != nil {
			return nil, fmt.Errorf("read log: %w", err)
		}
		for _, row := range rows {
			out = append(out, entry(row.ID, row.EntityID, row.EntityTypeID, row.CreatedByTokenID, row.Body, row.CreatedAt, row.Author, row.AuthorOf))
		}
	case OnEntityType:
		rows, err := s.q.ListCommentsOnEntityType(ctx, dbq.ListCommentsOnEntityTypeParams{ProjectID: projectID, EntityTypeID: id, Lim: bound})
		if err != nil {
			return nil, fmt.Errorf("read log: %w", err)
		}
		for _, row := range rows {
			out = append(out, entry(row.ID, row.EntityID, row.EntityTypeID, row.CreatedByTokenID, row.Body, row.CreatedAt, row.Author, row.AuthorOf))
		}
	default:
		rows, err := s.q.ListCommentsOnRelationType(ctx, dbq.ListCommentsOnRelationTypeParams{ProjectID: projectID, RelationTypeID: id, Lim: bound})
		if err != nil {
			return nil, fmt.Errorf("read log: %w", err)
		}
		for _, row := range rows {
			out = append(out, entry(row.ID, row.EntityID, row.EntityTypeID, row.CreatedByTokenID, row.Body, row.CreatedAt, row.Author, row.AuthorOf))
		}
	}
	return out, nil
}

// ListGame reads the game's whole log, newest first, whatever each
// comment is about.
func (s *Service) ListGame(ctx context.Context, projectID uuid.UUID, limit int32) ([]Comment, error) {
	rows, err := s.q.ListCommentsInProject(ctx, dbq.ListCommentsInProjectParams{ProjectID: projectID, Lim: page(limit)})
	if err != nil {
		return nil, fmt.Errorf("read the game's log: %w", err)
	}
	out := make([]Comment, 0, len(rows))
	for _, row := range rows {
		out = append(out, entry(row.ID, row.EntityID, row.EntityTypeID, row.CreatedByTokenID, row.Body, row.CreatedAt, row.Author, row.AuthorOf))
	}
	return out, nil
}

// Remove takes one comment out. There is no update: a log that can be
// rewritten is not a log, and a note written against the wrong thing is
// still worse than a gap.
func (s *Service) Remove(ctx context.Context, projectID, id uuid.UUID) error {
	_, err := s.q.DeleteComment(ctx, dbq.DeleteCommentParams{ID: id, ProjectID: projectID})
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: no comment %q in this game", metamodel.ErrNotFound, id)
	}
	if err != nil {
		return fmt.Errorf("remove comment: %w", err)
	}
	return nil
}

// CountsForEntities is how many comments each of these entities carries.
// It is one query for a page of rows rather than one per row.
func (s *Service) CountsForEntities(ctx context.Context, projectID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]int64, error) {
	out := map[uuid.UUID]int64{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.q.ListCommentCountsForEntities(ctx, dbq.ListCommentCountsForEntitiesParams{ProjectID: projectID, EntityIds: ids})
	if err != nil {
		return nil, fmt.Errorf("count comments: %w", err)
	}
	for _, row := range rows {
		if row.EntityID != nil {
			out[*row.EntityID] = row.Comments
		}
	}
	return out, nil
}

// resolve turns the address a caller speaks into the row it names, and
// refuses an address this game does not have. **The refusal is the
// point**: without it a comment would be written against a uuid nobody
// can reach, and the log would quietly fill with notes about nothing.
func (s *Service) resolve(ctx context.Context, projectID uuid.UUID, target Target) (uuid.UUID, error) {
	switch target.Kind {
	case OnEntity:
		row, err := s.meta.EntityByKey(ctx, projectID, target.TypeKey, target.Key)
		return row.ID, err
	case OnEntityType:
		row, err := s.meta.EntityTypeByKey(ctx, projectID, target.TypeKey)
		return row.ID, err
	case OnRelationType:
		row, err := s.meta.RelationTypeByKey(ctx, projectID, target.TypeKey)
		return row.ID, err
	default:
		return uuid.Nil, refuse("target.on", fmt.Sprintf("must be %q, %q or %q",
			OnEntity, OnEntityType, OnRelationType))
	}
}

// refuse is this package's one refusal shape. **A ValidationError and not
// a wrapped sentinel**: both surfaces read the path out of it and report
// it beside the argument it is about, and a message that merely names the
// path leaves a client parsing prose.
func refuse(path, problem string) error {
	return &metamodel.ValidationError{
		Code:   metamodel.CodeInvalidInput,
		Fields: []metamodel.FieldError{{Path: path, Message: problem}},
	}
}

func page(limit int32) int32 {
	switch {
	case limit <= 0:
		return DefaultPage
	case limit > MaxPage:
		return MaxPage
	default:
		return limit
	}
}
