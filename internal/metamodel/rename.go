package metamodel

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// This file is the one call that changes a type's key.
type RenameInput struct {
	From            string
	To              string
	ExpectedVersion *int32
	Actor           Actor
}

// typeRenameEvent is the payload of type.renamed and
// relation_type.renamed: the row's id and both spellings.
type typeRenameEvent struct {
	ID   uuid.UUID `json:"id"`
	From string    `json:"from"`
	To   string    `json:"to"`
}

// RenameEntityType changes an entity type's key, addressed by the old
// key and guarded by the caller's version.
func (s *Service) RenameEntityType(ctx context.Context, projectID uuid.UUID, in RenameInput) (dbq.EntityType, error) {
	if err := checkRenameInput("entity type", in); err != nil {
		return dbq.EntityType{}, err
	}

	var row dbq.EntityType
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		read := func(ctx context.Context, key string) (renamedRow, error) {
			got, err := q.GetEntityTypeByKeyForUpdate(ctx, dbq.GetEntityTypeByKeyForUpdateParams{
				ProjectID: projectID, Key: key,
			})
			return renamedRow{Key: got.Key, Version: got.Version}, err
		}
		if err := judgeRename(ctx, "entity type", in, read); err != nil {
			return err
		}

		var err error
		row, err = q.RenameEntityType(ctx, dbq.RenameEntityTypeParams{
			ProjectID:        projectID,
			FromKey:          in.From,
			ToKey:            in.To,
			ExpectedVersion:  *in.ExpectedVersion,
			UpdatedByUserID:  in.Actor.UserID,
			UpdatedByTokenID: in.Actor.TokenID,
		})
		return renameFailure(err, "entity type", in)
	})
	if err != nil {
		return dbq.EntityType{}, err
	}

	s.publish(projectID, eventTypeRenamed, typeEventMinRole, typeEventHumanOnly,
		typeRenameEvent{ID: row.ID, From: in.From, To: row.Key})
	return row, nil
}

// RenameRelationType changes a relation type's key. It is
// RenameEntityType over the other table, in every respect including the
// two things a caller most needs to know — the rename is addressed by
// the old key, and it does not repair the saved views that name this
// type — so that comment carries the argument and this one does not
// restate it.
func (s *Service) RenameRelationType(ctx context.Context, projectID uuid.UUID, in RenameInput) (dbq.RelationType, error) {
	if err := checkRenameInput("relation type", in); err != nil {
		return dbq.RelationType{}, err
	}

	var row dbq.RelationType
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		read := func(ctx context.Context, key string) (renamedRow, error) {
			got, err := q.GetRelationTypeByKeyForUpdate(ctx, dbq.GetRelationTypeByKeyForUpdateParams{
				ProjectID: projectID, Key: key,
			})
			return renamedRow{Key: got.Key, Version: got.Version}, err
		}
		if err := judgeRename(ctx, "relation type", in, read); err != nil {
			return err
		}

		var err error
		row, err = q.RenameRelationType(ctx, dbq.RenameRelationTypeParams{
			ProjectID:        projectID,
			FromKey:          in.From,
			ToKey:            in.To,
			ExpectedVersion:  *in.ExpectedVersion,
			UpdatedByUserID:  in.Actor.UserID,
			UpdatedByTokenID: in.Actor.TokenID,
		})
		return renameFailure(err, "relation type", in)
	})
	if err != nil {
		return dbq.RelationType{}, err
	}

	s.publish(projectID, eventRelationTypeRenamed, relationTypeEventMinRole,
		relationTypeEventHumanOnly, typeRenameEvent{ID: row.ID, From: in.From, To: row.Key})
	return row, nil
}

// renamedRow is the two columns judgeRename reads off either table's
// locked row. Neither dbq type implements an interface, and a rename
// needs nothing from a type but its stored spelling and its version.
type renamedRow struct {
	Key     string
	Version int32
}

// checkRenameInput judges a rename's arguments before any transaction is
// opened: both keys in one pass, the version claim, and only then the
// fold.
func checkRenameInput(what string, in RenameInput) error {
	problems := rowKeyProblems("from", in.From)
	problems = append(problems, rowKeyProblems("to", in.To)...)
	if in.ExpectedVersion == nil {
		problems = append(problems, FieldError{
			Path: "expected_version",
			Message: fmt.Sprintf(
				"is required: pass the version you read, so a rename cannot land on top of "+
					"an edit of this %s you never saw", what),
		})
	}
	if len(problems) > 0 {
		return &ValidationError{Code: codeInvalidInput, Fields: problems}
	}
	if strings.EqualFold(in.From, in.To) {
		return renameToSameAddressError(what, in.From, in.To)
	}
	return nil
}

// judgeRename takes the row lock on whichever of the two keys exist and
// decides what the rename is allowed to do.
func judgeRename(ctx context.Context, what string, in RenameInput,
	read func(context.Context, string) (renamedRow, error),
) error {
	first, second := in.From, in.To
	if strings.ToLower(second) < strings.ToLower(first) {
		first, second = second, first
	}
	rows := map[string]*renamedRow{}
	for _, key := range []string{first, second} {
		row, err := read(ctx, key)
		switch {
		case err == nil:
			locked := row
			rows[strings.ToLower(key)] = &locked
		case errors.Is(err, pgx.ErrNoRows):
			// Absent is the ordinary case for the destination and the
			// refused case for the source; both are judged below, once
			// both locks are held, so the judgement does not depend on
			// which key sorted first.
		default:
			return fmt.Errorf("lock %s for rename: %w", what, err)
		}
	}

	if taken := rows[strings.ToLower(in.To)]; taken != nil {
		return renameDestinationTakenError(what, in.To, taken.Key)
	}
	source := rows[strings.ToLower(in.From)]
	if source == nil {
		return fmt.Errorf("%w: no %s %q in this game", ErrNotFound, what, in.From)
	}
	if *in.ExpectedVersion != source.Version {
		return &VersionConflictError{Current: source.Version}
	}
	return nil
}

// renameFailure maps what the guarded UPDATE can answer with.
func renameFailure(err error, what string, in RenameInput) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return &VersionConflictError{Current: *in.ExpectedVersion}
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		// The winner's stored spelling is unavailable here — this
		// transaction never saw its row — so the message names what the
		// caller sent, which is the honest thing to name.
		return &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
			Path: "to",
			Message: fmt.Sprintf(
				"names a key another %s in this game already has (%q): another write took "+
					"it while this rename was in flight, so read it and pick a key that is free",
				what, in.To),
		}}}
	}
	if mapped := ActorConstraintViolation(err); errors.Is(mapped, ErrActorNotInGame) {
		return mapped
	}
	return fmt.Errorf("rename %s: %w", what, err)
}

// renameToSameAddressError refuses a rename whose two ends are one
// address.
func renameToSameAddressError(what, from, to string) error {
	if from == to {
		return &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
			Path: "to",
			Message: fmt.Sprintf(
				"is the key this %s already has (%q): a rename changes a type's handle, "+
					"and this one changes nothing", what, to),
		}}}
	}
	return &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
		Path: "to",
		Message: fmt.Sprintf(
			"differs from %q only in capitalisation, and keys are matched without regard "+
				"to case, so %q and %q are one key rather than two: the spelling a %s was "+
				"first declared under is the handle its content and its saved views refer "+
				"to and is not rewritten, so pick a key that differs by more than "+
				"capitalisation", from, from, to, what),
	}}}
}

// renameDestinationTakenError refuses a rename onto a key that already
// names a type.
func renameDestinationTakenError(what, requested, stored string) error {
	spelling := ""
	if stored != requested {
		spelling = fmt.Sprintf(" (spelled %q, and keys are matched without regard to case)", stored)
	}
	return &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
		Path: "to",
		Message: fmt.Sprintf(
			"names a key another %s in this game already has%s: a rename never merges two "+
				"types, so remove that one first or rename to a key that is free",
			what, spelling),
	}}}
}
