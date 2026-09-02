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

// RelationTypeInput is an upsert request for a relation type.
//
// SourceTypeIDs and TargetTypeIDs are the entity types allowed at each
// endpoint, and an empty list means "any type" rather than "no type": a
// relation type that declares neither is the ordinary case, not a
// relation type nothing can instance. Every id in them must name an
// entity type of this same game — the columns are plain uuid[] with no
// foreign key of their own, so an id from another game would otherwise
// be stored and simply never match, leaving a rule no write can satisfy
// and a refusal that names the wrong problem.
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
//
// **A known gap, recorded because the code does not close it.** Editing
// an entity type's field schema re-checks that type's entities and flags
// the ones that no longer fit (revalidateEntitiesOfType). Nothing of the
// kind happens here: `relations` has no `invalid` column, so an edge
// stored against this type keeps whatever fields it had and is never
// re-judged against the schema this call may just have changed. It is
// filed as its own piece of work; what this call does is exactly what is
// written above it and no more.
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
		// Both endpoint lists in one pass, before anything is written, so
		// a declaration naming two unknown types is fixed in one round
		// trip. They are read inside the transaction because they are read
		// against the same game the write lands in.
		problems, err := checkEndpointTypes(ctx, q, projectID, "source_type_ids", in.SourceTypeIDs)
		if err != nil {
			return err
		}
		targetProblems, err := checkEndpointTypes(ctx, q, projectID, "target_type_ids", in.TargetTypeIDs)
		if err != nil {
			return err
		}
		problems = append(problems, targetProblems...)
		if len(problems) > 0 {
			return &ValidationError{Code: codeInvalidInput, Fields: problems}
		}

		// Read under the row lock, so the spelling and the version this
		// caller is told about are the ones its own write will meet.
		existing, err := q.GetRelationTypeByKeyForUpdate(ctx, dbq.GetRelationTypeByKeyForUpdateParams{
			ProjectID: projectID, Key: in.Key,
		})
		switch {
		case err == nil:
			// Spelling before version, so a caller failing for both
			// reasons hears the one it can act on. See UpsertEntityType.
			if existing.Key != in.Key {
				return keyRespellingError("key", in.Key, existing.Key)
			}
			if in.ExpectedVersion == nil || *in.ExpectedVersion != existing.Version {
				return &VersionConflictError{Current: existing.Version}
			}
		case errors.Is(err, pgx.ErrNoRows):
			// Creation: no version to match, nothing to lock.
		default:
			return fmt.Errorf("lookup relation type: %w", err)
		}

		params := dbq.UpsertRelationTypeParams{
			ProjectID:        projectID,
			Key:              in.Key,
			Label:            in.Label,
			Description:      in.Description,
			SourceTypeIds:    endpointList(in.SourceTypeIDs),
			TargetTypeIds:    endpointList(in.TargetTypeIDs),
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
			// The guarded DO UPDATE matched nothing: between the read
			// above and this statement another writer created or advanced
			// the row.
			return conflictOnRelationTypeKey(ctx, q, projectID, in.Key)
		}
		if err != nil {
			if mapped := actorConstraintViolation(err); errors.Is(mapped, ErrActorNotInGame) {
				return mapped
			}
			return fmt.Errorf("upsert relation type: %w", err)
		}
		// The locked read cannot be the only place the spelling is
		// checked: on the creation path there is nothing to lock, so a
		// writer racing a creator with a matching expected version passes
		// both the read and the guard and updates a row it never saw. The
		// upsert returns the row it touched and key is not in the SET
		// list, so comparing the stored spelling to the submitted one
		// after the write closes both; withTx rolls the write back.
		if row.Key != in.Key {
			return keyRespellingError("key", in.Key, row.Key)
		}
		return nil
	})
	if err != nil {
		return dbq.RelationType{}, err
	}

	s.publish(projectID, eventRelationTypeUpserted, relationTypeEventMinRole, relationTypeEventHumanOnly,
		relationTypeEvent{ID: row.ID, Key: row.Key})
	return row, nil
}

// endpointList turns an undeclared endpoint list into an empty array.
//
// pgx encodes a nil slice as SQL NULL, and source_type_ids and
// target_type_ids are NOT NULL, so passing the zero value straight
// through fails the insert outright — the ordinary case, since most
// relation types declare no endpoint rules at all. Empty is also what
// nil means here: an undeclared list accepts any type, and that is the
// column's own default.
func endpointList(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}

// checkEndpointTypes reports every id of an endpoint list that names no
// entity type of this game, at its own indexed path so a caller can see
// which element to fix.
//
// The id is repeated in the message rather than left to the path alone:
// a caller that built the list from a map has the ids and not the
// positions in front of it.
//
// **The read holds a share lock on every type it finds**, which is what
// makes correction 6's invariant survive a concurrent removal rather
// than only a sequential one. `RemoveEntityType` prunes the removed id
// out of the endpoint lists, but the prune is one `UPDATE` over
// `relation_types` under READ COMMITTED and the *creation* path here has
// no row for it to find; before this lock, a relation type created
// between the prune's statement and the removal's commit kept a dangling
// id, and the row it produced was the unrepairable one `RemoveEntityType`
// describes. With the lock, the removal's `DELETE` waits for this
// transaction, and its prune — which runs after the delete — then sees
// the row this one wrote. See LockEndpointEntityTypes for why the lock is
// taken here, before the relation type's own row lock, and not later.
//
// One statement per list rather than one lookup per id, because the lock
// and the check are the same read: a per-id loop would take the same
// locks one round trip at a time.
//
// **A failure to read at all is returned as an error, not folded into a
// FieldError.** It used to be: any error from the lock query, including
// one this transaction had no way to satisfy, came back as
// `FieldError{Message: "could not be checked: " + err.Error()}`, which
// the caller then wrapped as `codeInvalidInput`. That is a lie a lock
// timeout can now tell that it could not before this function started
// taking `FOR SHARE`: the old per-id `GetEntityTypeByID` took no lock and
// could not be cancelled by `lock_timeout`, so nothing reached this
// branch except a connection actually down. `FOR SHARE` can be parked
// behind another transaction's row lock and cancelled by
// `lock_timeout`/`statement_timeout` (SQLSTATE 55P03 / 57014) — a
// retryable contention event, not a problem with the ids the caller
// sent, and `invalid_input` is read by a seeding agent as "resending
// this unchanged is pointless," which for contention is exactly wrong.
// `FieldError.Message` is also a bare string: flattening the error into
// one erases the `*pgconn.PgError` a caller further up could otherwise
// recover with `errors.As`. Returning the error instead keeps its
// SQLSTATE intact and lets it propagate past `ValidationError` entirely,
// landing on the `retryable` wire code: Task 7 took the call this
// comment left open and gave contention a code of its own, so an agent
// meeting a lock timeout here is told to resend the same call rather
// than merely told it failed. IsRetryable (service.go) is the
// classifier, failureFor (bulk.go) and mcpErrorFor
// (internal/web/mcp_errors.go) are the two boundaries that read it, and
// TestALockTimeoutOnTheEndpointCheckIsNotReportedAsInvalidInput proves
// it end to end against a real held lock. What makes any of it possible
// is that the error keeps its `*pgconn.PgError`, which is what
// flattening it into a `FieldError` destroyed.
//
// A failure to read at all is still attributed to the list and not an
// element in spirit — nothing was checked, so no index is the one at
// fault — but now as an opaque error rather than a field-shaped one.
func checkEndpointTypes(ctx context.Context, q *dbq.Queries, projectID uuid.UUID, path string, ids []uuid.UUID) ([]FieldError, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	found, err := q.LockEndpointEntityTypes(ctx, dbq.LockEndpointEntityTypesParams{
		ProjectID: projectID, Ids: ids,
	})
	if err != nil {
		return nil, fmt.Errorf("lock endpoint entity types (%s): %w", path, err)
	}
	known := make(map[uuid.UUID]struct{}, len(found))
	for _, id := range found {
		known[id] = struct{}{}
	}

	var problems []FieldError
	for i, id := range ids {
		if _, ok := known[id]; !ok {
			problems = append(problems, FieldError{
				Path:    fmt.Sprintf("%s[%d]", path, i),
				Message: "names no entity type of this game: " + id.String(),
			})
		}
	}
	return problems, nil
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

// RelationTypeByKey loads one relation type, matched without regard to
// case as every key in this domain is.
//
// A missing key is named, not reported through the generic notFound
// helper. The caller supplied this key: ListRelations filters on it and
// says so in its own doc comment, and "not_found" on its own tells an
// agent that mistyped `requires` as `require` nothing about which of the
// arguments it sent was wrong — the one thing it needs to fix the call.
// upsertRelationWith has named it since it shipped; this is the same
// message from the read path.
func (s *Service) RelationTypeByKey(ctx context.Context, projectID uuid.UUID, key string) (dbq.RelationType, error) {
	row, err := s.q.GetRelationTypeByKey(ctx, dbq.GetRelationTypeByKeyParams{ProjectID: projectID, Key: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbq.RelationType{}, fmt.Errorf("%w: no relation type %q in this game", ErrNotFound, key)
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
//
// One transaction, as RemoveEntityType is: the count, the cascade delete
// and the delete itself are one decision, and a count taken outside the
// transaction is a count another writer can invalidate before the delete
// runs. The row is read first for its key, so relation_type.removed
// carries the identity it declares rather than an empty string.
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
			// relations.relation_type_id is ON DELETE RESTRICT, so an edge
			// written between the count and this statement raises 23503.
			// It is the same refusal, and a caller should not have to tell
			// a race apart from the ordinary case.
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

	s.publish(projectID, eventRelationTypeRemoved, relationTypeEventMinRole, relationTypeEventHumanOnly,
		relationTypeEvent{ID: id, Key: removedKey})
	return nil
}
