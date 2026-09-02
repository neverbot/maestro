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
// the batch. Message is the item's own error, and what it holds depends
// on the fault: a field path and a rule where the item's own arguments
// or values are wrong (`schema_violation: fields.min_level: expected
// number, got string`), and otherwise the plain reason the item was
// refused, which may name nothing of the item at all (`not_found: no
// entity type "quest" in this game`). What it never holds is another
// item's values: a batch report is the one place a row's content could
// leak into a neighbour's error, and
// TestBulkPartialLandsTheGoodRowsAndReportsTheRest pins that it does
// not.
type BulkFailure struct {
	Index   int    `json:"index"`
	Key     string `json:"key"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// BulkResult reports what a batch did.
//
// Succeeded is `json:"-"` because a dbq.Entity is a database row and not
// a wire shape — it carries the audit columns and the raw jsonb, and
// serialising it here would publish a shape no design decision has been
// made about. The consequence is that a marshalled BulkResult reports
// failures and nothing else, so **Task 7 must decide what a successful
// batch tells an agent** — how many rows landed, under which keys, at
// which versions — because today the honest answer is nothing at all.
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
// **A key repeated inside one batch** is refused by repeatedKeys before
// it can be misdiagnosed, and the two modes answer it differently for
// the reason each mode exists: in partial the later occurrence is a
// per-item invalid_input failure and the rest of the batch is
// undisturbed, and in atomic the whole batch is refused before anything
// is written. Both arguments are at their call sites.
//
// In partial mode the later occurrence is refused **whether or not the
// first one lands**: `[{key x, min_level: "not a number"}, {key x,
// valid}]` reports index 0 as schema_violation and index 1 as
// invalid_input, so x does not exist afterwards and the caller needs a
// second call. Deliberate — the batch as submitted names one row twice
// and nothing in it says which of the two was meant, so falling back to
// "whichever survived validation" would make the result depend on the
// other item's mistakes. Recorded in the core design's "Bulk writes"
// too, because it costs an agent a round trip it can avoid by folding
// repeated keys before it sends.
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

	repeats := repeatedKeys(items)
	var result BulkResult
	for i, in := range items {
		// Before anything else this loop does with an item, including
		// the faults it can diagnose without touching the database.
		// Nothing else stops this loop: every item has its own
		// transaction, so a cancelled caller would otherwise turn a
		// 500-row batch into 500 failed round trips whose report nobody
		// is left to read, and the call would still return nil, which
		// reads as "the batch ran". The repeat check below used to run
		// first and `continue` without consulting the context at all, so
		// a batch whose trailing item was a repeat returned that nil
		// after the caller had gone — the exact failure this guard
		// exists to prevent, escaping through the one arm that never
		// asked. Whatever had already landed is returned with the error,
		// because rows landing before the cancellation is partial mode's
		// contract rather than a fact to hide.
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("bulk upsert stopped at item %d: %w", i, err)
		}

		// A repeated key is the item's own fault and needs no round trip
		// to diagnose. In partial mode it fails alone: the first
		// occurrence is a perfectly good item, and refusing the batch
		// over it would throw away the other three hundred rows, which
		// is the failure this mode exists to prevent.
		if problem, ok := repeats[i]; ok {
			result.Failed = append(result.Failed, failureFor(i, in.Key,
				&ValidationError{Code: codeInvalidInput, Fields: []FieldError{problem}}))
			continue
		}

		var written upsertedEntity
		err := s.withTx(ctx, func(q *dbq.Queries) error {
			var err error
			written, err = s.upsertEntityWith(ctx, q, projectID, in)
			return err
		})
		if err != nil {
			// The guard above only catches a cancellation that lands
			// *between* two items. The ordinary case is the other one:
			// the caller goes away while an item is in flight, that
			// item's own transaction fails with the cancellation, and
			// failureFor would file it as internal_error — the code
			// reserved for what nobody planned for. One cancellation
			// would then surface twice, once as this stop error and once
			// as a server fault against an item whose only problem was
			// that nobody was left to hear about it. The cancellation
			// can also land between the write and the commit, where the
			// two are indistinguishable from in here.
			if stopped := ctx.Err(); stopped != nil {
				return result, fmt.Errorf("bulk upsert stopped at item %d: %w", i, stopped)
			}
			result.Failed = append(result.Failed, failureFor(i, in.Key, err))
			continue
		}
		result.Succeeded = append(result.Succeeded, written.row)
		s.publish(projectID, eventEntityUpserted, entityEventMinRole, entityEventHumanOnly, written.event())
	}
	return result, nil
}

func (s *Service) upsertEntitiesAtomic(ctx context.Context, projectID uuid.UUID, items []EntityInput) (BulkResult, error) {
	// Refused before anything is written, and refused whole. An atomic
	// batch that names one row twice cannot be satisfied as submitted —
	// the caller asked for n rows and at most n-1 can exist — and there
	// is no per-item answer to give in a mode that lands everything or
	// nothing. Doing it up front also makes the report deterministic:
	// left to the writes, a repetition surfaces as whichever conflict
	// the two items' versions happen to produce, and two items chaining
	// the versions the other will leave behind both commit, so
	// Succeeded comes back carrying the same row id twice and the caller
	// is told two rows landed where one exists.
	if repeats := repeatedKeys(items); len(repeats) > 0 {
		fields := make([]FieldError, 0, len(repeats))
		for i := range items {
			if problem, ok := repeats[i]; ok {
				fields = append(fields, problem)
			}
		}
		return BulkResult{}, &ValidationError{Code: codeInvalidInput, Fields: fields}
	}

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

// repeatedKeys finds the items of a batch that address a row an earlier
// item already addressed, keyed by index and carrying the problem to
// report.
//
// A seeding agent building a batch from a file produces this by
// accident, and it is the one fault a batch can hold that the database
// cannot diagnose: the two items are one row, so the second is refused
// as a version conflict against a caller that never claimed a version —
// which sends an agent to re-read the row and retry with the version it
// is handed, at which point its own second item quietly overwrites its
// first. The real answer is that the key appears twice, and only the
// batch can see that.
//
// Identity is (type key, key), both folded, because that is what the
// unique index folds: one key under two types is two rows. Folding with
// strings.ToLower is exact here — rowKeyPattern admits ASCII only, so
// there is no case where it and the index's lower() can disagree — and
// an item whose key is malformed enough to escape that is refused on its
// own arguments anyway.
func repeatedKeys(items []EntityInput) map[int]FieldError {
	type identity struct{ typeKey, key string }
	first := make(map[identity]int, len(items))
	var repeats map[int]FieldError
	for i, in := range items {
		id := identity{strings.ToLower(in.TypeKey), strings.ToLower(in.Key)}
		if at, seen := first[id]; seen {
			if repeats == nil {
				repeats = make(map[int]FieldError)
			}
			repeats[i] = FieldError{
				Path: fmt.Sprintf("items[%d].key", i),
				Message: fmt.Sprintf(
					"%q is already addressed by item %d of this batch, and keys are matched "+
						"without regard to case: give one of the two items a different key, "+
						"or merge them into one", in.Key, at),
			}
			continue
		}
		first[id] = i
	}
	return repeats
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
		// searchTextLimit already keeps the search vector under
		// Postgres's cap, so this is a backstop and not the fix; see
		// searchLimitExceeded for why it is here anyway.
		if mapped := searchLimitExceeded(err); errors.Is(mapped, ErrInvalidInput) {
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

// searchTextLimit bounds, in bytes, what one row hands to to_tsvector.
//
// Postgres refuses to build a tsvector larger than 1,048,575 bytes
// (SQLSTATE 54000), and nothing bounds a text or longtext value: a
// designer pasting a lore document of a megabyte or two had the row
// refused outright, which is the wrong trade in both directions. The
// content is the product — a game's own writing, stored in `fields`
// untouched — and the index is a convenience, so the index is what
// gives way.
//
// 128 KiB, not something nearer the cap, because the vector is bigger
// than the text it is built from and by how much depends on the words:
// 1.5 MB of sixteen-byte distinct words measured 1.79 MB, and short
// distinct words are worse still, since every entry pays its own
// per-lexeme and position overhead. Eight times' headroom holds under
// any of that while still indexing on the order of twenty thousand
// words, which is more of one row than any search over this domain
// reaches for.
//
// The bound is over the row's whole flattened text, not per field, since
// the vector is built from the concatenation. A row longer than this is
// searchable by the words in its first 128 KiB and not by the ones after
// them; nothing about the stored values changes, and a re-read returns
// exactly what was written. searchLimitExceeded is the backstop for a
// value that reaches the cap by some other path.
const searchTextLimit = 128 << 10

// searchTextOf flattens the text values of a row so search can index
// them. The keys are sorted so the same values always produce the same
// tsvector input: map iteration order is randomised, and a search column
// that differs between two identical writes is a diff nobody can explain.
//
// What is collected is every word a text search over this domain can
// use: the strings — text, longtext and the chosen option of an enum,
// which Validate leaves as a plain string — and the elements of a
// list<text>, which are the tags and aliases a designer searches for
// more often than anything else. What is left out is the two types that
// carry no words at all, number and bool: those are found by filtering on
// the jsonb, and putting them here would only fill the tsvector with
// digits that match nothing anyone types. (There is no date type in this
// package; if one is ever added it belongs with the filters, not here.)
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
