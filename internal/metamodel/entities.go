package metamodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// BulkMode decides how a batch behaves when one item fails.
type BulkMode string

// The two bulk modes.
const (
	// BulkPartial lands the valid items and reports the rest. Default,
	// because a first seeding pass always has a few bad rows and losing the
	// other four hundred helps nobody.
	BulkPartial BulkMode = "partial"
	// BulkAtomic runs the whole batch in one transaction.
	BulkAtomic BulkMode = "atomic"
)

// EntityInput is an upsert request, addressed by type key plus entity key.
//
// ExpectedVersion carries exactly the meaning EntityTypeInput's does, and
// the same caveat: on the insert path it is passed as the guard on the
// DO UPDATE and is never evaluated, so a claim against a row that does
// not exist creates one rather than being refused. See
// EntityTypeInput.ExpectedVersion for the argument.
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
//
// A payload never carries a value a client could treat as current —
// Service.publish's doc comment argues why — so this holds ids and keys
// and not the name that just changed. It is also a struct and not a
// hand-built JSON string: interpolating caller-supplied keys into
// `{"type":"…"}` would put unescaped text on the SSE wire, which is a
// frame injection the Core already closed once.
//
// Both keys are the *stored* spellings, not the submitted ones. Keys are
// matched without regard to case, so a caller may address type "Quest" as
// "quest"; an event repeating that spelling would name an identity no
// other reader of the game sees, and a subscriber's only use for the
// payload is to go and re-read the row it names.
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

// BulkFailure is one rejected item of a batch.
//
// Index and Key are what let a caller retry only what failed: an agent
// seeding four hundred rows re-sends the handful named here rather than
// the batch. Message is the item's own error, which in this package is a
// path and a rule — never another item's values, since a batch report is
// the one place a row's content could leak into a neighbour's error.
type BulkFailure struct {
	Index   int    `json:"index"`
	Key     string `json:"key"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// BulkResult reports what a batch did.
type BulkResult struct {
	Succeeded []dbq.Entity  `json:"-"`
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
//
// **The partial-failure contract, which is the whole point of the two
// modes.** In BulkPartial every item is its own transaction: item 200
// landing does not depend on item 3, the rows that fit are stored, and
// the ones that do not come back in Failed with their index, their key
// and the code that says how to fix them. The caller retries the named
// items and nothing else. In BulkAtomic one bad row rolls the whole batch
// back and the call returns an error naming the item that failed; nothing
// is reported as done, because nothing was.
//
// A partial batch therefore returns a nil error even when items failed —
// the failures are the result, not an error — and an atomic batch returns
// an error with an empty result. The one case that returns both is a
// cancelled context: see the loop below.
//
// Both modes publish only after their transaction has committed, and
// publish one identity event per row that landed rather than one event
// carrying a count: a count is a value, not an identity, and a
// subscriber's only correct reaction to an entity event is to re-read the
// rows it names.
func (s *Service) UpsertEntities(ctx context.Context, projectID uuid.UUID, items []EntityInput, mode BulkMode) (BulkResult, error) {
	switch mode {
	case BulkAtomic:
		return s.upsertEntitiesAtomic(ctx, projectID, items)
	case BulkPartial, "":
		// The empty mode is the documented default. An omitted argument is
		// not a typo, and the spec names partial as the default.
	default:
		// Anything else is refused rather than read as partial. Task 7
		// builds this value straight from an agent-supplied string, so a
		// typo would otherwise silently downgrade an all-or-nothing
		// request into one that lands rows the caller asked to have
		// rolled back — a failure the caller has no way of seeing.
		return BulkResult{}, &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
			Path:    "mode",
			Message: fmt.Sprintf("must be %q or %q", BulkPartial, BulkAtomic),
		}}}
	}

	var result BulkResult
	for i, in := range items {
		// Nothing else stops this loop: every item has its own
		// transaction, so a cancelled caller would otherwise turn a
		// 500-row batch into 500 failed round trips whose report nobody
		// is left to read, and the call would still return nil, which
		// reads as "the batch ran". Whatever had already landed is
		// returned with the error, because rows landing before the
		// cancellation is partial mode's contract rather than a fact to
		// hide.
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("bulk upsert stopped at item %d: %w", i, err)
		}

		var written upsertedEntity
		err := s.withTx(ctx, func(q *dbq.Queries) error {
			var err error
			written, err = s.upsertEntityWith(ctx, q, projectID, in)
			return err
		})
		if err != nil {
			result.Failed = append(result.Failed, failureFor(i, in.Key, err))
			continue
		}
		result.Succeeded = append(result.Succeeded, written.row)
		s.publish(projectID, eventEntityUpserted, entityEventMinRole, entityEventHumanOnly, written.event())
	}
	return result, nil
}

func (s *Service) upsertEntitiesAtomic(ctx context.Context, projectID uuid.UUID, items []EntityInput) (BulkResult, error) {
	var (
		rows   []dbq.Entity
		events []entityEvent
	)
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		rows, events = nil, nil
		for i, in := range items {
			written, err := s.upsertEntityWith(ctx, q, projectID, in)
			if err != nil {
				// The index and the key name the item to fix, and %w keeps
				// the code — schema_violation, invalid_input, whichever —
				// matchable through the wrapping.
				return fmt.Errorf("item %d (%q): %w", i, in.Key, err)
			}
			rows = append(rows, written.row)
			events = append(events, written.event())
		}
		return nil
	})
	if err != nil {
		return BulkResult{}, err
	}
	for _, event := range events {
		s.publish(projectID, eventEntityUpserted, entityEventMinRole, entityEventHumanOnly, event)
	}
	return BulkResult{Succeeded: rows}, nil
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

	typ, err := q.GetEntityTypeByKey(ctx, dbq.GetEntityTypeByKeyParams{ProjectID: projectID, Key: in.TypeKey})
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
		if mapped := actorConstraintViolation(err); errors.Is(mapped, ErrActorNotInGame) {
			return upsertedEntity{}, mapped
		}
		return upsertedEntity{}, fmt.Errorf("upsert entity: %w", err)
	}
	// The locked read cannot be the only place the spelling is checked:
	// on the creation path there is nothing to lock, so a writer racing a
	// creator with a matching expected version passes both the read and
	// the guard and updates a row it never saw. The upsert returns the
	// row it touched and key is not in the SET list, so comparing the
	// stored spelling to the submitted one after the write closes both;
	// the caller's transaction rolls the write back.
	if row.Key != in.Key {
		return upsertedEntity{}, keyRespellingError("key", in.Key, row.Key)
	}
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
	if err != nil {
		return dbq.Entity{}, notFound(err, "lookup entity")
	}
	return row, nil
}

// RemoveEntity deletes one entity. Its relations go with it, by cascade.
func (s *Service) RemoveEntity(ctx context.Context, projectID, id uuid.UUID) error {
	// Read the row, and its type, before deleting: entity.removed
	// declares the same {id, type_key, key} identity entity.upserted
	// does, and a removal announced with an empty key tells a client a row
	// keyed empty string is gone. A client reading a declared field cannot
	// tell "not carried" from "empty".
	var removed entityEvent
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		row, err := q.GetEntityByID(ctx, dbq.GetEntityByIDParams{ProjectID: projectID, ID: id})
		if err != nil {
			return notFound(err, "lookup entity")
		}
		typ, err := q.GetEntityTypeByID(ctx, dbq.GetEntityTypeByIDParams{
			ProjectID: projectID, ID: row.EntityTypeID,
		})
		if err != nil {
			return notFound(err, "lookup entity type")
		}
		removed = entityEvent{ID: row.ID, TypeKey: typ.Key, Key: row.Key}

		rows, err := q.DeleteEntity(ctx, dbq.DeleteEntityParams{ProjectID: projectID, ID: id})
		if err != nil {
			return fmt.Errorf("delete entity: %w", err)
		}
		if rows == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.publish(projectID, eventEntityRemoved, entityEventMinRole, entityEventHumanOnly, removed)
	return nil
}

// searchTextOf flattens the text values of a row so search can index
// them. The keys are sorted so the same values always produce the same
// tsvector input: map iteration order is randomised, and a search column
// that differs between two identical writes is a diff nobody can explain.
//
// Only string values are collected, which is the whole of what a text
// search over this domain can use: numbers, booleans and dates are found
// by filtering on the jsonb, not by matching words.
func searchTextOf(values map[string]any) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		if s, ok := values[k].(string); ok {
			b.WriteString(s)
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// failureFor maps a domain error to the wire shape of a bulk failure.
//
// Every wire code in errors.go has an arm here. The default arm is
// internal_error, which is the honest answer for something nobody
// planned for — and precisely the wrong answer for a malformed key, so
// ErrInvalidInput is matched explicitly: it is a caller's own argument,
// at a path, fixable in place, and reporting it as internal_error tells
// an agent to give up on a call it could have fixed.
//
// ErrActorNotInGame is deliberately *not* given an arm. It is not a wire
// code, for the reason errors.go records — the actor is never
// caller-supplied, so an agent can do nothing about it — and
// internal_error is the correct report for a fault it cannot fix.
func failureFor(index int, key string, err error) BulkFailure {
	f := BulkFailure{Index: index, Key: key, Message: err.Error()}
	switch {
	case errors.Is(err, ErrInvalidInput):
		f.Code = "invalid_input"
	case errors.Is(err, ErrSchemaViolation):
		f.Code = "schema_violation"
	case errors.Is(err, ErrInvalidSchema):
		f.Code = "invalid_schema"
	case errors.Is(err, ErrVersionConflict):
		f.Code = "version_conflict"
	case errors.Is(err, ErrNotFound):
		f.Code = "not_found"
	case errors.Is(err, ErrEndpointTypeMismatch):
		f.Code = "endpoint_type_mismatch"
	case errors.Is(err, ErrInUse):
		f.Code = "in_use"
	default:
		f.Code = "internal_error"
	}
	return f
}
