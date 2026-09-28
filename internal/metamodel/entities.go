package metamodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/neverbot/maestro/internal/db/dbq"
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

// entityEvent is the payload of the entity.* events: the identity of
// what changed and nothing else.
type entityEvent struct {
	ID      uuid.UUID `json:"id"`
	TypeKey string    `json:"type_key"`
	Key     string    `json:"key"`
}

// upsertedEntity is a written row together with the key of the type it
// belongs to, which is the one piece of its identity the row itself does
// not carry. The two travel together so that a caller assembling events
// after its transaction has committed cannot pair a row with the wrong
// type key by getting an index wrong.
type upsertedEntity struct {
	row     dbq.Entity
	typeKey string
}

func (u upsertedEntity) event() entityEvent {
	return entityEvent{ID: u.row.ID, TypeKey: u.typeKey, Key: u.row.Key}
}

// BulkWrite is one row a batch landed, in the shape a caller reads.
type BulkWrite struct {
	TypeKey string    `json:"type_key"`
	Key     string    `json:"key"`
	ID      uuid.UUID `json:"id"`
	Version int32     `json:"version"`
}

// BulkResult reports what a batch did.
type BulkResult struct {
	Succeeded []dbq.Entity  `json:"-"`
	Written   []BulkWrite   `json:"written"`
	Failed    []BulkFailure `json:"failed"`
}

// UpsertEntity creates or updates one entity.
func (s *Service) UpsertEntity(ctx context.Context, projectID uuid.UUID, in EntityInput) (dbq.Entity, error) {
	var written upsertedEntity
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		var err error
		written, err = s.upsertEntityWith(ctx, q, projectID, in)
		return err
	})
	if err != nil {
		return dbq.Entity{}, err
	}
	s.publish(projectID, eventEntityUpserted, entityEventMinRole, entityEventHumanOnly, written.event())
	return written.row, nil
}

// UpsertEntities writes a batch in the requested mode.
func (s *Service) UpsertEntities(ctx context.Context, projectID uuid.UUID, items []EntityInput, mode BulkMode) (BulkResult, error) {
	written, failed, err := BulkUpsert(ctx, s.withTx, items, mode, s.entityBulkSpec(projectID))
	// The rows travel through the batch paired with their type key —
	// upsertedEntity's whole reason for existing, so that a caller
	// assembling events after its transaction has committed cannot pair a
	// row with the wrong type key by getting an index wrong. The pairing
	// has done its work by here, and what the caller is owed is rows.
	var (
		rows    []dbq.Entity
		reports []BulkWrite
	)
	for _, w := range written {
		rows = append(rows, w.row)
		reports = append(reports, BulkWrite{
			TypeKey: w.typeKey, Key: w.row.Key, ID: w.row.ID, Version: w.row.Version,
		})
	}
	return BulkResult{Succeeded: rows, Written: reports, Failed: failed}, err
}

// entityBulkSpec is the entity half of a bulk write: everything bulk.go
// deliberately does not know.
func (s *Service) entityBulkSpec(projectID uuid.UUID) BulkSpec[EntityInput, upsertedEntity] {
	return BulkSpec[EntityInput, upsertedEntity]{
		Identity: func(in EntityInput) string {
			return FoldedIdentity(in.TypeKey, in.Key)
		},
		Key: func(in EntityInput) string { return in.Key },
		Repeated: func(i, first int, in EntityInput) FieldError {
			return FieldError{
				Path: fmt.Sprintf("items[%d].key", i),
				Message: fmt.Sprintf(
					"%q is already addressed by item %d of this batch, and keys are matched "+
						"without regard to case: give one of the two items a different key, "+
						"or merge them into one", in.Key, first),
			}
		},
		Write: func(ctx context.Context, q *dbq.Queries, in EntityInput) (upsertedEntity, error) {
			return s.upsertEntityWith(ctx, q, projectID, in)
		},
		Publish: func(written upsertedEntity) {
			s.publish(projectID, eventEntityUpserted, entityEventMinRole, entityEventHumanOnly,
				written.event())
		},
	}
}

// upsertEntityWith does the work against any queries handle, so the same
// code serves the single, partial and atomic paths. Every caller runs it
// inside a transaction, and none of them publishes from in here.
func (s *Service) upsertEntityWith(ctx context.Context, q *dbq.Queries, projectID uuid.UUID, in EntityInput) (upsertedEntity, error) {
	problems := rowKeyProblems("key", in.Key)
	problems = append(problems, checkName(in.Name)...)
	if len(problems) > 0 {
		return upsertedEntity{}, &ValidationError{Code: codeInvalidInput, Fields: problems}
	}
	// type_key is deliberately not validated as a key here: it addresses
	// a row this call only reads, so a malformed one has one honest
	// answer — there is no such type — and it already gets it below.

	// **The type's row is locked before the entity's, and that ordering
	// is load-bearing.** The write ends in an INSERT ... ON CONFLICT whose
	// foreign key takes FOR KEY SHARE on this same row regardless; taking
	// it here, before GetEntityByKeyForUpdate below, is what keeps this
	// writer and RemoveEntityType(cascade) — which locks entity_types
	// first too, since its own read is FOR UPDATE — from holding what the
	// other is waiting for. GetEntityTypeByKeyForKeyShare's comment
	// carries the measurement and the argument for the lock mode.
	typ, err := q.GetEntityTypeByKeyForKeyShare(ctx, dbq.GetEntityTypeByKeyForKeyShareParams{
		ProjectID: projectID, Key: in.TypeKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return upsertedEntity{}, fmt.Errorf("%w: no entity type %q in this game", ErrNotFound, in.TypeKey)
	}
	if err != nil {
		return upsertedEntity{}, fmt.Errorf("lookup entity type: %w", err)
	}

	schema, err := ParseSchema(typ.FieldSchema)
	if err != nil {
		return upsertedEntity{}, err
	}
	values, err := schema.Validate(in.Fields)
	if err != nil {
		return upsertedEntity{}, err
	}

	expected := noVersion
	if in.ExpectedVersion != nil {
		expected = *in.ExpectedVersion
	}

	// Read under the row lock, so the spelling and the version this
	// caller is told about are the ones its own write will meet.
	existing, err := q.GetEntityByKeyForUpdate(ctx, dbq.GetEntityByKeyForUpdateParams{
		ProjectID: projectID, EntityTypeID: typ.ID, Key: in.Key,
	})
	switch {
	case err == nil:
		// Spelling before version: a caller failing for both reasons
		// hears the one it can act on. See UpsertEntityType.
		if existing.Key != in.Key {
			return upsertedEntity{}, keyRespellingError("key", in.Key, existing.Key)
		}
		if in.ExpectedVersion == nil || *in.ExpectedVersion != existing.Version {
			return upsertedEntity{}, &VersionConflictError{Current: existing.Version}
		}
	case errors.Is(err, pgx.ErrNoRows):
		// No row, and a version claimed: the row was removed. This is
		// the table where the loss is largest — an entity's id is named
		// by every edge touching it and by every view position placing
		// it — so the rule is not merely inherited here, it is the case
		// RemovedError's argument is written about.
		if in.ExpectedVersion != nil {
			return upsertedEntity{}, &RemovedError{
				Subject: "entity",
				Address: fmt.Sprintf("%q of type %q", in.Key, typ.Key),
				Claimed: *in.ExpectedVersion,
			}
		}
		// Creation: no version to match, nothing to lock.
	default:
		return upsertedEntity{}, fmt.Errorf("lookup entity: %w", err)
	}

	encoded, err := json.Marshal(values)
	if err != nil {
		return upsertedEntity{}, fmt.Errorf("encode fields: %w", err)
	}

	row, err := q.UpsertEntity(ctx, dbq.UpsertEntityParams{
		ProjectID:        projectID,
		EntityTypeID:     typ.ID,
		Key:              in.Key,
		Name:             in.Name,
		Fields:           encoded,
		SearchText:       searchTextOf(values),
		ExpectedVersion:  expected,
		UpdatedByUserID:  in.Actor.UserID,
		UpdatedByTokenID: in.Actor.TokenID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The guarded DO UPDATE matched nothing: between the read above
		// and this statement another writer created or advanced the row.
		return upsertedEntity{}, conflictOnEntityKey(ctx, q, projectID, typ.ID, in.Key)
	}
	if err != nil {
		if mapped := ActorConstraintViolation(err); errors.Is(mapped, ErrActorNotInGame) {
			return upsertedEntity{}, mapped
		}
		// searchTextLimit already keeps the search vector under
		// Postgres's cap, so this is a backstop and not the fix; see
		// searchLimitExceeded for why it is here anyway.
		if mapped := searchLimitExceeded(err); errors.Is(mapped, ErrInvalidInput) {
			return upsertedEntity{}, mapped
		}
		return upsertedEntity{}, fmt.Errorf("upsert entity: %w", err)
	}
	// **No post-write spelling check here any more**, for the reason
	// UpsertEntityType's own comment states at length: a version claim
	// against a row the locked read cannot see is refused above, so the
	// writer that used to reach this check — a creation racing a creator
	// while holding the version the winner lands on — no longer gets
	// here. conflictOnEntityKey is what a creation racing another
	// spelling meets, and it is still exercised.
	return upsertedEntity{row: row, typeKey: typ.Key}, nil
}

// conflictOnEntityKey re-reads a key whose guarded upsert matched no row
// and names what actually stands in the way — a respelling, or a version
// this caller was holding that has since moved.
func conflictOnEntityKey(ctx context.Context, q *dbq.Queries, projectID, typeID uuid.UUID, key string) error {
	row, err := q.GetEntityByKey(ctx, dbq.GetEntityByKeyParams{
		ProjectID: projectID, EntityTypeID: typeID, Key: key,
	})
	if err != nil {
		return fmt.Errorf("re-read entity after a failed upsert: %w", err)
	}
	if row.Key != key {
		return keyRespellingError("key", key, row.Key)
	}
	return &VersionConflictError{Current: row.Version}
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
		return dbq.Entity{}, fmt.Errorf("%w: no entity %q of type %q in this game",
			ErrNotFound, key, typ.Key)
	}
	if err != nil {
		return dbq.Entity{}, fmt.Errorf("lookup entity: %w", err)
	}
	return row, nil
}

// EntitiesByIDs reads a set of this game's entities in one query,
// keyed by id. It is the read a caller needs when it holds ids and owes
// its own caller keys: a page of relations names its endpoints by id
// (that is what the row holds), and Task 7 shipped `relations.list`
// answering in ids for want of exactly this statement.
func (s *Service) EntitiesByIDs(ctx context.Context, projectID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]dbq.Entity, error) {
	byID := make(map[uuid.UUID]dbq.Entity, len(ids))
	if len(ids) == 0 {
		// No query at all, and not merely an empty answer: a listing
		// with no edges must not cost a round trip.
		return byID, nil
	}
	rows, err := s.q.ListEntitiesByIDs(ctx, dbq.ListEntitiesByIDsParams{ProjectID: projectID, Ids: ids})
	if err != nil {
		return nil, fmt.Errorf("lookup entities by id: %w", err)
	}
	for _, row := range rows {
		byID[row.ID] = row
	}
	return byID, nil
}

// RemoveEntity deletes one entity by the address it was written under:
// its type's key and its own key. Its edges go with it, by cascade.
func (s *Service) RemoveEntity(ctx context.Context, projectID uuid.UUID, typeKey, key string) error {
	var problems []FieldError
	problems = append(problems, rowKeyProblems("type_key", typeKey)...)
	problems = append(problems, rowKeyProblems("key", key)...)
	if len(problems) > 0 {
		return &ValidationError{Code: codeInvalidInput, Fields: problems}
	}

	// Read the row, and its type, before deleting: entity.removed
	// declares the same {id, type_key, key} identity entity.upserted
	// does, and a removal announced with an empty key tells a client a row
	// keyed empty string is gone. A client reading a declared field cannot
	// tell "not carried" from "empty".
	var removed entityEvent
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		typ, err := q.GetEntityTypeByKey(ctx, dbq.GetEntityTypeByKeyParams{
			ProjectID: projectID, Key: typeKey,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: no entity type %q in this game", ErrNotFound, typeKey)
		}
		if err != nil {
			return fmt.Errorf("lookup entity type: %w", err)
		}
		row, err := q.GetEntityByKey(ctx, dbq.GetEntityByKeyParams{
			ProjectID: projectID, EntityTypeID: typ.ID, Key: key,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: no %s %q in this game", ErrNotFound, typ.Key, key)
		}
		if err != nil {
			return fmt.Errorf("lookup entity: %w", err)
		}
		// The stored spellings, not the caller's: keys are matched
		// without regard to case, and an event that echoed the request
		// would name an identity no other reader of the game sees.
		removed = entityEvent{ID: row.ID, TypeKey: typ.Key, Key: row.Key}

		rows, err := q.DeleteEntity(ctx, dbq.DeleteEntityParams{ProjectID: projectID, ID: row.ID})
		if err != nil {
			return fmt.Errorf("delete entity: %w", err)
		}
		if rows == 0 {
			// Reachable only against a concurrent removal of the same
			// row — the read above takes no lock — and not_found is the
			// answer this call owes either way.
			return fmt.Errorf("%w: no %s %q in this game", ErrNotFound, typ.Key, key)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.publish(projectID, eventEntityRemoved, entityEventMinRole, entityEventHumanOnly, removed)
	return nil
}

// searchTextLimit bounds, in bytes, what one row hands to to_tsvector.
const searchTextLimit = 128 << 10

// searchTextOf flattens the text values of a row so search can index
// them. The keys are sorted so the same values always produce the same
// tsvector input: map iteration order is randomised, and a search column
// that differs between two identical writes is a diff nobody can explain.
func searchTextOf(values map[string]any) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	write := func(v any) {
		s, ok := v.(string)
		if !ok || b.Len() >= searchTextLimit {
			return
		}
		if room := searchTextLimit - b.Len(); len(s) > room {
			s = s[:room]
			// Cut back off a rune split in half by the slice above: a
			// partial rune is invalid UTF-8, and Postgres refuses a
			// parameter that is not valid UTF-8 outright — turning a
			// bound index into a failed write, which is the failure this
			// whole bound exists to prevent. At most three bytes go.
			for len(s) > 0 {
				r, size := utf8.DecodeLastRuneInString(s)
				if r != utf8.RuneError || size != 1 {
					break
				}
				s = s[:len(s)-1]
			}
		}
		b.WriteString(s)
		b.WriteByte(' ')
	}
	for _, k := range keys {
		// Validate normalises a list<text> to []any of strings, whatever
		// the caller passed, so this is the only list shape to expect.
		if list, ok := values[k].([]any); ok {
			for _, item := range list {
				write(item)
			}
			continue
		}
		write(values[k])
	}
	return b.String()
}
