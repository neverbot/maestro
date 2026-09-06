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
//
// ExpectedVersion carries exactly the meaning EntityTypeInput's does,
// including the rule that a claim against a row that is not there is a
// RemovedError rather than a creation. See EntityTypeInput.ExpectedVersion
// for the argument; this is the table it costs the most, because an
// entity's id is what every edge touching it and every view position
// placing it names.
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

// BulkWrite is one row a batch landed, in the shape a caller reads.
//
// It carries exactly what an agent cannot work out from the batch it
// sent, plus the address that says which of its own items this is:
//
//   - ID, because every removal in this package is addressed by id
//     (RemoveEntity), and a caller that has only ever spoken in keys has
//     no other way to get one but a second read.
//   - Version, because that is the value ExpectedVersion takes on the
//     next edit of this row. Nothing else reports it, and re-reading a
//     row to learn the version of a write you just made is a round trip
//     the write already knew the answer to.
//   - TypeKey and Key, the row's own address, so a partial batch's
//     report can be matched item by item against what was sent. Both are
//     the *stored* spellings, not the caller's, for the reason
//     upsertedEntity exists: keys are matched without regard to case, so
//     the two can differ and the stored one is the one that is true.
//
// It deliberately does not carry the row's fields. A caller that sent
// them has them, and a four-hundred-row seed would otherwise get its own
// payload back.
type BulkWrite struct {
	TypeKey string    `json:"type_key"`
	Key     string    `json:"key"`
	ID      uuid.UUID `json:"id"`
	Version int32     `json:"version"`
}

// BulkResult reports what a batch did.
//
// Succeeded is `json:"-"` because a dbq.Entity is a database row and not
// a wire shape — it carries the audit columns and the raw jsonb, and
// serialising it here would publish a shape no design decision has been
// made about. It stays, unchanged, for the callers inside this repository
// that want the whole row.
//
// **Written is what a successful batch tells an agent**, and it is Task
// 7's answer to the question this comment used to pose. Until it existed
// a marshalled BulkResult reported failures and nothing else, so a
// perfect four-hundred-row batch answered with nothing at all — and the
// obvious alternative, a count, is the one answer that cannot be acted
// on: an agent already knows how many items it sent, and subtracting the
// failures gives it the same number. See BulkWrite for what each entry
// carries and why.
//
// Written and Succeeded are built from the same slice in the same loop
// and are always the same length in the same order, so they cannot
// disagree about what landed.
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
//
// The batch machinery itself — the two modes and what each promises, the
// per-item loop, the cancellation contract, the up-front duplicate check
// and the mapping to a wire code — is bulk.go's, shared with every other
// bulk write in this package. What is entity-shaped and stays here is
// entityBulkSpec: what identifies an entity, what to say when a batch
// names one twice, how one is written, and what is announced once it
// lands.
//
// **A key repeated inside one batch** is refused before it can be
// misdiagnosed, and the two modes answer it differently for the reason
// each mode exists: in partial the later occurrence is a per-item
// invalid_input failure and the rest of the batch is undisturbed, and in
// atomic the whole batch is refused before anything is written. Both
// arguments are at their call sites in bulk.go.
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
//
// Identity is (type key, key), both folded, because that is what the
// unique index folds: one key under two types is two rows. FoldedIdentity
// does the folding and the length-prefixed join, and records why both are
// what they are.
//
// The message names both indices and the case-folding rule, because the
// caller cannot see either from what it sent: a batch built from a file
// repeats a key by accident, and the two spellings need not match.
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
//
// Both misses are named, and for the reason endpointEntity gives about
// an edge's parents: this call takes two keys, either of them can be the
// wrong one, and a caller told only "not_found" cannot tell whether the
// type does not exist or the entity within it does not. The type's own
// message comes from EntityTypeByKey.
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
//
// It is deliberately a map and not a slice: every caller so far joins it
// back onto rows it already has, and handing back a slice would make
// each of them build the same index. An id with no row is simply absent
// — a leaked id from another game (the project filter is in SQL, where
// every other statement here puts it), a removal that raced the listing
// that produced it, or a caller's typo all produce the same gap, and
// none of the three is a failure of this read. A caller that needs to
// distinguish them compares the map's size against what it asked for.
//
// Duplicate ids are fine and cost nothing: `= ANY` does not care, and a
// dense node named on both ends of many edges is the ordinary case. The
// caller is expected to ask for at most a page's worth; nothing here
// bounds the list, because nothing here is reachable from a
// caller-supplied array — every call site builds the ids from rows it
// just read under its own limit.
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
//
// **It took a uuid until Metamodel 14.** Every other tool on this
// surface addresses a row the way a designer names it, so an agent
// holding the (type key, key) it had just written paid a resolving read
// before every removal — measured by Task 9's seeding run and recorded
// as a limitation there. The resolution has not gone away; it has moved
// inside this transaction, where it costs no round trip and cannot race
// the delete it precedes.
//
// The id is still returned by every reader and by the removal event, so
// nothing that had one has lost it. What it is no longer is the address.
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
