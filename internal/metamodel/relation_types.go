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

// RelationTypeInput is an upsert request for a relation type.
type RelationTypeInput struct {
	Key             string
	Label           string
	Description     string
	SourceTypeKeys  []string
	TargetTypeKeys  []string
	SemanticRole    string
	AnalysisTraits  []string
	Schema          Schema
	ExpectedVersion *int32
	Actor           Actor
}

// SemanticRoles are the classifications a relation type may declare, in
// the order 0004_metamodel.sql's CHECK lists them.
var SemanticRoles = []string{
	"prerequisite", "unlock", "containment", "spatial", "availability", "reward",
}

// checkSemanticRole refuses a role the column would refuse, as
// invalid_input at the argument's own path.
func checkSemanticRole(role string) []FieldError {
	if role == "" {
		return nil
	}
	for _, allowed := range SemanticRoles {
		if role == allowed {
			return nil
		}
	}
	quoted := make([]string, len(SemanticRoles))
	for i, allowed := range SemanticRoles {
		quoted[i] = fmt.Sprintf("%q", allowed)
	}
	return []FieldError{{
		Path: "semantic_role",
		Message: fmt.Sprintf(
			"must be one of %s, or omitted: a relation type need not classify itself",
			strings.Join(quoted, ", ")),
	}}
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
	problems = append(problems, checkSemanticRole(in.SemanticRole)...)
	if len(problems) > 0 {
		return dbq.RelationType{}, &ValidationError{Code: codeInvalidInput, Fields: problems}
	}
	// After the invalid_input pass and beside the schema check, because
	// it is the same kind of fault as that one: a declaration that cannot
	// stand, reported as invalid_schema. See checkAnalysisTraits.
	if err := checkAnalysisTraits(in.AnalysisTraits); err != nil {
		return dbq.RelationType{}, err
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
		sourceIDs, problems, err := checkEndpointTypes(ctx, q, projectID, "source_type_keys", in.SourceTypeKeys)
		if err != nil {
			return err
		}
		targetIDs, targetProblems, err := checkEndpointTypes(ctx, q, projectID, "target_type_keys", in.TargetTypeKeys)
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
			// No row, and a version claimed: the row was removed. The
			// rule and its argument are RemovedError's, applied here
			// rather than restated — a relation type's removal takes
			// every edge of it, so a resurrection under a new id leaves
			// a type standing whose whole graph is gone.
			if in.ExpectedVersion != nil {
				return &RemovedError{
					Subject: "relation type",
					Address: fmt.Sprintf("%q", in.Key),
					Claimed: *in.ExpectedVersion,
				}
			}
			// Creation: no version to match, nothing to lock.
		default:
			return fmt.Errorf("lookup relation type: %w", err)
		}

		params := dbq.UpsertRelationTypeParams{
			ProjectID:        projectID,
			Key:              in.Key,
			Label:            in.Label,
			Description:      in.Description,
			SourceTypeIds:    endpointList(sourceIDs),
			TargetTypeIds:    endpointList(targetIDs),
			FieldSchema:      raw,
			ExpectedVersion:  expected,
			UpdatedByUserID:  in.Actor.UserID,
			UpdatedByTokenID: in.Actor.TokenID,
		}
		if in.SemanticRole != "" {
			role := in.SemanticRole
			params.SemanticRole = &role
		}
		// An empty list reaches the column as NULL, not as `{}`: NULL is
		// "this type has never been given an opinion", the check
		// constraint refuses `{}` outright, and pgx encodes a nil slice
		// as SQL NULL. So clearing a type's traits is sending an empty
		// list, and what comes back is undeclared rather than inert.
		if len(in.AnalysisTraits) > 0 {
			params.AnalysisTraits = in.AnalysisTraits
		}

		row, err = q.UpsertRelationType(ctx, params)
		if errors.Is(err, pgx.ErrNoRows) {
			// The guarded DO UPDATE matched nothing: between the read
			// above and this statement another writer created or advanced
			// the row.
			return conflictOnRelationTypeKey(ctx, q, projectID, in.Key)
		}
		if err != nil {
			if mapped := ActorConstraintViolation(err); errors.Is(mapped, ErrActorNotInGame) {
				return mapped
			}
			return fmt.Errorf("upsert relation type: %w", err)
		}
		// **No post-write spelling check here any more**, for the reason
		// UpsertEntityType's own comment states at length: a version
		// claim against a row the locked read cannot see is refused up
		// there, so the only writer that could ever have reached this
		// check with a foreign spelling no longer gets here.
		// conflictOnRelationTypeKey is what a *creation* racing another
		// spelling meets, and it is still exercised.

		// A schema change can invalidate stored edges. Re-check them
		// rather than rejecting the change or inventing values for a new
		// field — the same rule, and the same call, the entity type path
		// makes.
		return s.revalidateRelationsOfType(ctx, q, row)
	})
	if err != nil {
		return dbq.RelationType{}, err
	}

	s.publish(projectID, eventRelationTypeUpserted, relationTypeEventMinRole, relationTypeEventHumanOnly,
		relationTypeEvent{ID: row.ID, Key: row.Key})
	return row, nil
}

// endpointList turns an undeclared endpoint list into an empty array.
func endpointList(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}

// checkEndpointTypes reports every id of an endpoint list that names no
// entity type of this game, at its own indexed path so a caller can see
// which element to fix.
func checkEndpointTypes(ctx context.Context, q *dbq.Queries, projectID uuid.UUID,
	path string, keys []string,
) ([]uuid.UUID, []FieldError, error) {
	if len(keys) == 0 {
		return nil, nil, nil
	}
	// **Every element is pattern-checked before the query runs**, at its
	// own indexed path, and a malformed one stops the query rather than
	// merely joining the report. It has to: these keys reach Postgres as
	// a `text[]`, so a NUL byte in one of them fails the whole statement
	// with SQLSTATE 22021 — an internal_error over the caller's own
	// argument, and over the *list* rather than the element. It is the
	// same rule ListRelations' type_key and endpoint filters follow, and
	// the same failure this repository has closed twice before.
	var malformed []FieldError
	for i, key := range keys {
		malformed = append(malformed, rowKeyProblems(fmt.Sprintf("%s[%d]", path, i), key)...)
	}
	if len(malformed) > 0 {
		return nil, malformed, nil
	}

	// Folded before the query and folded again to read the answer, so
	// the caller's own spelling of a key finds the row and the message
	// still quotes the caller's spelling back. Entity type keys are
	// ASCII (rowKeyPattern, just enforced above), so this fold and SQL's
	// lower() agree; the same premise FoldedIdentity rests on.
	folded := make([]string, 0, len(keys))
	for _, key := range keys {
		folded = append(folded, strings.ToLower(key))
	}
	found, err := q.LockEndpointEntityTypes(ctx, dbq.LockEndpointEntityTypesParams{
		ProjectID: projectID, Keys: folded,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("lock endpoint entity types (%s): %w", path, err)
	}
	known := make(map[string]uuid.UUID, len(found))
	for _, row := range found {
		known[strings.ToLower(row.Key)] = row.ID
	}

	// **A key repeated inside one list is refused rather than folded**,
	// and the refusal is here rather than left to the database, which
	// would store the duplicate id happily. An endpoint list is a set —
	// "these types may be a source" — so naming one twice cannot mean
	// anything a caller could have intended, and silently deduplicating
	// it would leave the answer disagreeing with what was sent.
	ids := make([]uuid.UUID, 0, len(keys))
	seen := make(map[string]int, len(keys))
	var problems []FieldError
	for i, key := range keys {
		lowered := folded[i]
		id, ok := known[lowered]
		if !ok {
			problems = append(problems, FieldError{
				Path:    fmt.Sprintf("%s[%d]", path, i),
				Message: "names no entity type of this game: " + key,
			})
			continue
		}
		if first, repeated := seen[lowered]; repeated {
			problems = append(problems, FieldError{
				Path: fmt.Sprintf("%s[%d]", path, i),
				Message: fmt.Sprintf(
					"names the same entity type as element %d; keys are matched without "+
						"regard to case and an endpoint list is a set", first),
			})
			continue
		}
		seen[lowered] = i
		ids = append(ids, id)
	}
	if len(problems) > 0 {
		return nil, problems, nil
	}
	return ids, nil, nil
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
func (s *Service) RemoveRelationType(ctx context.Context, projectID, id uuid.UUID, cascade bool) error {
	var removedKey string
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		typ, err := q.GetRelationTypeByID(ctx, dbq.GetRelationTypeByIDParams{ProjectID: projectID, ID: id})
		if err != nil {
			return notFoundByID(err, "relation type", id, "lookup relation type")
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
				return stillInUse("relation type", typ.Key,
					fmt.Sprintf("%d edges", count))
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
				return stillInUse("relation type", typ.Key,
					"edges, one of them written while it was being removed")
			}
			return fmt.Errorf("delete relation type: %w", err)
		}
		if rows == 0 {
			return missingByID("relation type", id)
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

// revalidateRelationsOfType re-checks every stored edge against its
// relation type's current schema and flags the ones that no longer fit.
func (s *Service) revalidateRelationsOfType(ctx context.Context, q *dbq.Queries, typ dbq.RelationType) error {
	return revalidate(ctx, sweep{
		fieldSchema: typ.FieldSchema,
		subject:     "relations",
		list: func(ctx context.Context) ([]storedFields, error) {
			rows, err := q.ListRelationFieldsOfType(ctx, dbq.ListRelationFieldsOfTypeParams{
				ProjectID: typ.ProjectID, RelationTypeID: typ.ID,
			})
			if err != nil {
				return nil, err
			}
			return storedFieldsOf(rows, func(row dbq.ListRelationFieldsOfTypeRow) storedFields {
				return storedFields{ID: row.ID, Fields: row.Fields}
			}), nil
		},
		mark: func(ctx context.Context, ids []uuid.UUID, invalid bool) error {
			return q.MarkRelationsOfTypeInvalid(ctx, dbq.MarkRelationsOfTypeInvalidParams{
				ProjectID:      typ.ProjectID,
				RelationTypeID: typ.ID,
				Ids:            ids,
				Invalid:        invalid,
			})
		},
	})
}
