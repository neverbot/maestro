package metamodel

import (
	"context"
	"errors"
	"fmt"
	"sort"

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
// a silent overwrite.
//
// What it is *not*, on that path, is a claim this service checks. If no
// row exists when the write runs — because none ever did, or because a
// rival deleted the one this caller held a version of — the insert lands
// and the guard is never evaluated: a fresh row appears under a new id,
// with version 1, and the call returns nil. Nothing is overwritten, and
// nothing is silent either: this type is addressed by (project, key) and
// not by id, the input carries no id to be surprised about, and a
// returned Version of 1 is the caller's own signal that it created
// rather than updated. Refusing instead was considered and rejected —
// TestAVersionClaimAgainstAMissingTypeCreatesItRatherThanRefusing
// records the argument and pins the outcome.
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
			// Checked before the version, and that order is the whole
			// remaining job of this branch: correction 15's post-write
			// check catches every respelling this one does, so deleting
			// these three lines is invisible to every test where the
			// version also matches. Where it does not, the caller is
			// failing for two reasons at once and hears the one it can
			// act on — the message naming both spellings — rather than
			// "current version is N", which would send it to retry with a
			// version refused again for the same reason.
			// TestARespellingIsNamedEvenWhenTheVersionIsAlsoStale pins it.
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
//
// A missing key is named rather than reported through the generic
// notFound helper: the caller supplied this key, so it is the one thing
// it can act on, and EntityByKey resolves a type through here before it
// can look at an entity at all — a bare "not_found" from that call would
// not even say which of its two keys was the wrong one. The write paths
// have named it since they shipped; this is the same message from the
// read path. **EntityTypeByID deliberately keeps the bare sentinel**: a
// caller addressing a row by id already holds the id it sent, and there
// is no second argument for it to tell apart.
func (s *Service) EntityTypeByKey(ctx context.Context, projectID uuid.UUID, key string) (dbq.EntityType, error) {
	row, err := s.q.GetEntityTypeByKey(ctx, dbq.GetEntityTypeByKeyParams{ProjectID: projectID, Key: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbq.EntityType{}, fmt.Errorf("%w: no entity type %q in this game", ErrNotFound, key)
	}
	if err != nil {
		return dbq.EntityType{}, fmt.Errorf("lookup entity type: %w", err)
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
//
// **It also prunes the type's id out of every relation type's endpoint
// lists, in the same transaction.** `source_type_ids` and
// `target_type_ids` are plain `uuid[]`, and Postgres has no foreign key
// from an array element, so without this the id outlives the type it
// names and Task 5's correction 6 — endpoint lists that always name a
// type of this game — holds only until the first removal. What it leaves
// behind is worse than untidy: the relation type holds a rule nothing can
// satisfy, a designer who recreates the key gets a fresh id and is
// refused with `entity type "zone" cannot be the target of relation type
// "takes_place_in"` while `zone` visibly *is* the declared target, and
// the type cannot be repaired through its own API at all, because
// re-declaring it with the list it currently holds is refused as
// `invalid_input`.
//
// **Pruning here rather than a real referential constraint**, and that is
// the choice rather than the cheap way out of it. A referential
// constraint over these lists does not exist in Postgres: it would mean
// replacing both columns with junction tables carrying a composite
// `ON DELETE CASCADE` key, which is a migration plus a rewrite of every
// read and write of an endpoint rule, and it changes the shape
// `RelationTypeInput` presents to an agent. That may well be the right
// end state — it would also give the lists an index and let a view join
// on them — but it is a schema decision, and Task 5's job is to close the
// invariant Task 5 created.
//
// **The prune alone closes the sequential path and not the concurrent
// one**, and it takes a second mechanism to close both. `DeleteEntityType`
// is the only path by which an entity type disappears — deleting a
// project cascades the relation types with it — so the prune is exact for
// every id that exists when its statement runs. It is one `UPDATE` under
// READ COMMITTED, though, and a relation type *created* after it has run
// and before this transaction commits is a row it never saw: the update
// path is caught by the prune's own row lock and READ COMMITTED's
// re-check, the creation path had no row to lock. What closes it is on
// the other side, in `checkEndpointTypes`: the endpoint read there holds
// a share lock on every type it names, so the `DELETE` above waits for
// that writer and the prune below then finds its row.
// `TestARelationTypeCreatedDuringATypeRemovalCannotKeepTheRemovedID`
// stages the interleaving deterministically.
//
// **The prune is announced, not left to be inferred.** It changes rows
// the caller never named and moves no `version`, so a subscriber holding
// an endpoint rule has nothing else to learn from; `relation_type.upserted`
// goes out for each row changed, after `type.removed`. See the cascade
// note in events.go, which covers deleted edges and deliberately not this.
//
// **One consequence, recorded because it is a widening.** Pruning the
// last id of a list leaves it empty, and an empty list means "any type"
// rather than "no type" (see `endpointList`). A relation type that
// accepted only `zone` at its target therefore accepts anything once
// `zone` is removed. That is the lesser of the two: the widening is
// visible in the row a designer reads and is one edit away from being
// narrowed again, where the dangling id was neither visible nor
// repairable.
func (s *Service) RemoveEntityType(ctx context.Context, projectID, id uuid.UUID, cascade bool) error {
	var removedKey string
	var pruned []dbq.PruneEntityTypeFromEndpointListsRow
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

		// The type is gone; nothing in the schema takes its id out of the
		// relation types that named it. See PruneEntityTypeFromEndpointLists.
		pruned, err = q.PruneEntityTypeFromEndpointLists(ctx, dbq.PruneEntityTypeFromEndpointListsParams{
			ProjectID: projectID, EntityTypeID: id,
		})
		return err
	})
	if err != nil {
		return err
	}

	s.publish(projectID, eventTypeRemoved, typeEventMinRole, typeEventHumanOnly,
		entityTypeEvent{ID: id, Key: removedKey})
	// Every relation type the prune touched now states a different rule
	// than the one its subscribers hold, and nothing else says so: the
	// caller named none of these rows, no version moved, and the cascade
	// note in events.go covers deleted edges rather than edited rules.
	// relation_type.upserted is the event a caller-visible edit of the
	// same two columns publishes, and this is the same change arriving by
	// another route. Announcing them one by one is affordable here in a
	// way the per-edge cascade is not: a game has a handful of relation
	// types and thousands of edges.
	sort.Slice(pruned, func(i, j int) bool { return pruned[i].Key < pruned[j].Key })
	for _, row := range pruned {
		s.publish(projectID, eventRelationTypeUpserted, relationTypeEventMinRole,
			relationTypeEventHumanOnly, relationTypeEvent{ID: row.ID, Key: row.Key})
	}
	return nil
}

// revalidateEntitiesOfType re-checks every stored entity against its
// type's current schema and flags the ones that no longer fit. Nothing is
// deleted and nothing is back-filled: the designer decides what a newly
// required field should hold, and a validation pass is not an edit of
// their content.
//
// CheckValues, never Validate — an intent, not a behaviour. CheckValues
// *is* Validate with the map discarded (validate.go), so the two return
// the same verdict on every input and no test can tell this sweep's call
// from the other. What the narrower call earns is that there is no
// normalised map in scope to write back: Validate hands one back with
// declared defaults injected, and a later edit that stored it would
// back-fill every row the sweep touched, silently, with values no
// designer chose. TestSchemaChangeDoesNotBackFillDeclaredDefaults pins
// the outcome; this line is what keeps the temptation out of reach.
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
