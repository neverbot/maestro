package metamodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// Ref addresses an entity the way an agent thinks of it: type key plus
// key. Neither is validated as a key here — both address a row this
// package only reads, so a malformed one has one honest answer, "there
// is no such thing", and it gets it from the lookup.
type Ref struct {
	TypeKey string
	Key     string
}

// RelationInput is an upsert request for one edge.
//
// **There is no ExpectedVersion, and that is a decision.** `relations`
// carries no version column: an edge is identified by (relation type,
// source, target) and its own fields are all it holds, so re-writing
// them is the whole operation rather than a lost update to guard
// against, and the last writer wins. Two designers editing one edge's
// fields at once therefore do not conflict — the second overwrites the
// first, silently. Giving edges the same compare-and-set the other three
// tables have would need a migration adding the column; Task 7 owns that
// call if the MCP surface wants edge concurrency.
//
// **This decision and UpsertRelation's refusal of parallel edges are one
// pair, and must be revisited as a pair.** Read alone each is defensible
// and neither is being reversed here; read together they compound.
// Forbidding two edges of one type between one ordered pair *pushes
// multiplicity into an edge's fields* — that is the escape hatch
// UpsertRelation offers by name, `passages: ["door", "vent"]` on a
// single `connects_to` — and this decision then leaves exactly those
// fields with no concurrency protection at all. A list two designers
// extend at the same time is the textbook lost update, and it is the
// example the other decision leans on. Verified, not reasoned about:
// two upserts of the same triple with `{"note":"A"}` then `{"note":"B"}`
// hit one row id, the second wins whole, no error is raised and the two
// `relation.upserted` events are indistinguishable, so nothing in the
// system — not the caller, not a subscriber, not the row — records that
// a write was lost. Entity fields never had this exposure: they have
// `version`, and an entity is where multiplicity would otherwise have
// gone.
//
// So the escape hatch is real but lossy under concurrent editing, which
// is a smaller claim than the one the other doc comment makes on its
// own. **Task 7 owns the migration** if it wants edge concurrency, and
// whoever opens either question should read the other first: adding
// `version` here makes the parallel-edge refusal cost what it was
// assumed to cost, and relaxing the uniqueness index instead would make
// this decision moot. Neither is a Task 5 change — one is a migration,
// the other rewrites the ON CONFLICT target that makes a re-seed
// idempotent.
type RelationInput struct {
	TypeKey string
	Source  Ref
	Target  Ref
	Fields  map[string]any
	Actor   Actor
}

// RelationFilter narrows a relation listing. Every field is optional;
// the zero value lists the game's edges.
type RelationFilter struct {
	TypeKey  string
	SourceID *uuid.UUID
	TargetID *uuid.UUID
	Limit    int32
}

// RelationBulkResult reports what a batch of edges did.
//
// It is a second type rather than BulkResult because BulkResult carries
// `Succeeded []dbq.Entity`; making that generic would have changed a
// public type every existing caller and test names. Succeeded is
// `json:"-"` for the same reason it is there — a dbq.Relation is a
// database row and not a wire shape — so a marshalled result reports
// failures and nothing else, and **Task 7 must decide what a successful
// batch tells an agent**.
type RelationBulkResult struct {
	Succeeded []dbq.Relation `json:"-"`
	Failed    []BulkFailure  `json:"failed"`
}

// relationEvent is the payload of the relation.* events: the identity of
// the edge — its id, the stored spelling of its type key, and both
// endpoint ids — and none of its fields. A struct, not a hand-built JSON
// string; see entityEvent.
type relationEvent struct {
	ID       uuid.UUID `json:"id"`
	TypeKey  string    `json:"type_key"`
	SourceID uuid.UUID `json:"source_id"`
	TargetID uuid.UUID `json:"target_id"`
}

// upsertedRelation is a written edge together with the stored spelling of
// its relation type's key, which is the one piece of its identity the row
// itself does not carry. The two travel together so that a caller
// assembling events after its transaction has committed cannot pair an
// edge with the wrong type key by getting an index wrong.
type upsertedRelation struct {
	row     dbq.Relation
	typeKey string
}

func (u upsertedRelation) event() relationEvent {
	return relationEvent{
		ID: u.row.ID, TypeKey: u.typeKey,
		SourceID: u.row.SourceID, TargetID: u.row.TargetID,
	}
}

// UpsertRelation creates or updates one edge, resolving all three of its
// parents inside the game, checking both endpoints against the relation
// type's allowed lists and its fields against the type's schema.
//
// **An edge is idempotent by (type, source, target)**, which is what
// makes a re-seed update the edge rather than lay a second copy beside
// it, and what the ON CONFLICT target needs to exist. The cost is that a
// game cannot hold two edges of one relation type between one ordered
// pair of entities. A game that genuinely needs two has two ways to say
// so, and both are better records than a nameless duplicate: declare the
// second meaning as its own relation type (`connects_to` and
// `connects_to_secretly`, or `unlocks` and `unlocks_at_max_rank`), or
// put the multiplicity in the edge's own fields, which is what edge
// fields are for — one `connects_to` from room A to room B carrying
// `passages: ["door", "vent"]` rather than two identical edges nothing
// tells apart. **That second escape hatch is qualified by RelationInput's
// decision that edges carry no version**: the list it hands the problem
// to is unprotected against a concurrent extension, and the loss is
// silent. The two decisions are recorded there as one pair, because
// changing either changes what the other costs; both stand, and Task 7
// owns any migration. **Self-loops are allowed**: source and target may be the
// same entity, and the index permits it. A championship that counts
// towards itself is a modelling mistake a designer should be able to
// make and then see; Maestro is not the arbiter of a game's graph, and
// the analysis sub-project is where cycles and self-references are
// reported.
func (s *Service) UpsertRelation(ctx context.Context, projectID uuid.UUID, in RelationInput) (dbq.Relation, error) {
	var written upsertedRelation
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		var err error
		written, err = s.upsertRelationWith(ctx, q, projectID, in)
		return err
	})
	if err != nil {
		return dbq.Relation{}, err
	}
	s.publish(projectID, eventRelationUpserted, relationEventMinRole, relationEventHumanOnly,
		written.event())
	return written.row, nil
}

// upsertRelationWith does the work against any queries handle, so the
// single, partial and atomic paths share one implementation and the
// atomic path resolves its endpoints against its own transaction — an
// agent seeding entities and the edges between them in one call needs to
// see rows the same transaction has just written. Nothing in here
// publishes.
//
// **No public caller reaches that yet.** UpsertRelations writes edges and
// nothing else, so an atomic batch's endpoints are always already
// committed and every lookup here could be routed through the pool
// without a single external test noticing; Task 9's seeding of a whole
// game in one call is where the claim gets a public path. Until then it
// is pinned at the only level where it is true, by the package's own
// TestAnEdgeResolvesItsEndpointsAgainstItsOwnTransaction.
func (s *Service) upsertRelationWith(ctx context.Context, q *dbq.Queries, projectID uuid.UUID, in RelationInput) (upsertedRelation, error) {
	relType, err := q.GetRelationTypeByKey(ctx, dbq.GetRelationTypeByKeyParams{
		ProjectID: projectID, Key: in.TypeKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Named, not a bare not_found: an edge has three parents that can
		// be missing, and an agent told only "not found" cannot tell which
		// of the three to go and create.
		return upsertedRelation{}, fmt.Errorf("%w: no relation type %q in this game", ErrNotFound, in.TypeKey)
	}
	if err != nil {
		return upsertedRelation{}, fmt.Errorf("lookup relation type: %w", err)
	}

	// Both ends are resolved before either is reported, and that is the
	// same argument checkEndpointTypes makes twenty lines from here about
	// the two endpoint *lists*: an agent holding two bad endpoints fixes
	// the one it was told about, resends, and is told about the other.
	// Returning on the source cost a round trip per bad end and made the
	// two halves of one decision disagree. The price is the two lookups
	// the target end costs on an item that was going to fail anyway; the
	// success path always paid them.
	source, sourceErr := endpointEntity(ctx, q, projectID, "source", in.Source)
	target, targetErr := endpointEntity(ctx, q, projectID, "target", in.Target)
	if err := bothEndpoints(sourceErr, targetErr); err != nil {
		return upsertedRelation{}, err
	}

	// The stored key of the endpoint's own type, not the caller's
	// spelling: this message is read beside the game's type list.
	if !endpointAllowed(relType.SourceTypeIds, source.typ.ID) {
		return upsertedRelation{}, fmt.Errorf(
			"%w: source: entity type %q cannot be the source of relation type %q",
			ErrEndpointTypeMismatch, source.typ.Key, relType.Key)
	}
	if !endpointAllowed(relType.TargetTypeIds, target.typ.ID) {
		return upsertedRelation{}, fmt.Errorf(
			"%w: target: entity type %q cannot be the target of relation type %q",
			ErrEndpointTypeMismatch, target.typ.Key, relType.Key)
	}

	schema, err := ParseSchema(relType.FieldSchema)
	if err != nil {
		return upsertedRelation{}, err
	}
	// Validate, not CheckValues: this is a write, and the declared
	// defaults belong in the row being written. CheckValues is for the
	// re-validation of rows nobody is editing.
	values, err := schema.Validate(in.Fields)
	if err != nil {
		return upsertedRelation{}, err
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return upsertedRelation{}, fmt.Errorf("encode fields: %w", err)
	}

	row, err := q.UpsertRelation(ctx, dbq.UpsertRelationParams{
		ProjectID:        projectID,
		RelationTypeID:   relType.ID,
		SourceID:         source.row.ID,
		TargetID:         target.row.ID,
		Fields:           encoded,
		UpdatedByUserID:  in.Actor.UserID,
		UpdatedByTokenID: in.Actor.TokenID,
	})
	if err != nil {
		if mapped := actorConstraintViolation(err); errors.Is(mapped, ErrActorNotInGame) {
			return upsertedRelation{}, mapped
		}
		if mapped := edgeParentViolation(err, in); errors.Is(mapped, ErrNotFound) {
			return upsertedRelation{}, mapped
		}
		return upsertedRelation{}, fmt.Errorf("upsert relation: %w", err)
	}
	return upsertedRelation{row: row, typeKey: relType.Key}, nil
}

// edgeParentViolation recognises a write refused because one of an edge's
// three parents was no longer there when the insert ran, and names which
// one; every other error passes through unchanged.
//
// The lookups above this are the ordinary answer, and they cannot be the
// only one: they read under READ COMMITTED, so a rival transaction that
// deletes a parent after the lookup and commits before the insert leaves
// this call holding an id the composite foreign keys then refuse. Left
// unmapped that arrives as `internal_error` — the code that means "give
// up" — carrying "violates foreign key constraint
// relations_target_id_project_id_fkey", which names neither which of the
// three parents is gone nor that anything is missing at all. In atomic
// mode one raced endpoint aborts a whole batch with it. The right answer
// is the one the lookup would have given a moment earlier, `not_found`
// naming the parent, because the recovery is the same: go and create the
// row, then retry.
//
// The mapping is on the constraint's column, as actorConstraintViolation
// is, and the three columns are distinct; RemoveEntityType and
// RemoveRelationType already catch the same SQLSTATE for the same class
// of race. The names in the message are the caller's own spellings —
// there is nothing stored left to read them from, which is precisely the
// condition being reported.
func edgeParentViolation(err error, in RelationInput) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		return err
	}
	switch {
	case strings.Contains(pgErr.ConstraintName, "relation_type_id"):
		return fmt.Errorf("%w: no relation type %q in this game", ErrNotFound, in.TypeKey)
	case strings.Contains(pgErr.ConstraintName, "source_id"):
		return fmt.Errorf("%w: source: no entity %q of type %q in this game",
			ErrNotFound, in.Source.Key, in.Source.TypeKey)
	case strings.Contains(pgErr.ConstraintName, "target_id"):
		return fmt.Errorf("%w: target: no entity %q of type %q in this game",
			ErrNotFound, in.Target.Key, in.Target.TypeKey)
	}
	return err
}

// bothEndpoints folds the two endpoint resolutions into the one answer
// their caller returns.
//
// Two failures are joined with "; " rather than through errors.Join,
// whose newline would put a batch report's Message on two lines, and each
// half keeps its own sentinel: both %w verbs are wrapped, so errors.Is
// still matches whichever of the two the caller asks about and
// failureFor still files the item under not_found.
func bothEndpoints(sourceErr, targetErr error) error {
	switch {
	case sourceErr != nil && targetErr != nil:
		return fmt.Errorf("%w; %w", sourceErr, targetErr)
	case sourceErr != nil:
		return sourceErr
	case targetErr != nil:
		return targetErr
	}
	return nil
}

// endpoint is a resolved end of an edge: the entity and the type it
// belongs to. The type comes back because the endpoint rule is stated in
// type ids and reported in type keys, and re-reading it afterwards would
// be a second round trip for a row already in hand.
type endpoint struct {
	row dbq.Entity
	typ dbq.EntityType
}

// endpointEntity resolves a Ref against a transaction's handle and names
// what is missing when it cannot: which end of the edge, and which of
// the two rows.
//
// Both lookups are scoped to the project, so an entity of another game
// reads as absent rather than as somebody else's row. The composite
// foreign keys on relations.source_id and .target_id are the backstop
// underneath that, not the mechanism.
func endpointEntity(ctx context.Context, q *dbq.Queries, projectID uuid.UUID, role string, ref Ref) (endpoint, error) {
	typ, err := q.GetEntityTypeByKey(ctx, dbq.GetEntityTypeByKeyParams{ProjectID: projectID, Key: ref.TypeKey})
	if errors.Is(err, pgx.ErrNoRows) {
		return endpoint{}, fmt.Errorf("%w: %s: no entity type %q in this game",
			ErrNotFound, role, ref.TypeKey)
	}
	if err != nil {
		return endpoint{}, fmt.Errorf("lookup %s entity type: %w", role, err)
	}
	row, err := q.GetEntityByKey(ctx, dbq.GetEntityByKeyParams{
		ProjectID: projectID, EntityTypeID: typ.ID, Key: ref.Key,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return endpoint{}, fmt.Errorf("%w: %s: no entity %q of type %q in this game",
			ErrNotFound, role, ref.Key, ref.TypeKey)
	}
	if err != nil {
		return endpoint{}, fmt.Errorf("lookup %s entity: %w", role, err)
	}
	return endpoint{row: row, typ: typ}, nil
}

// UpsertRelations writes a batch of edges in the requested mode.
//
// The batch machinery — the two modes and what each promises, the
// per-item loop, the cancellation contract, the up-front duplicate check
// and the mapping to a wire code — is bulk.go's, shared with entities.
// What is edge-shaped and stays here is relationBulkSpec.
func (s *Service) UpsertRelations(ctx context.Context, projectID uuid.UUID, items []RelationInput, mode BulkMode) (RelationBulkResult, error) {
	written, failed, err := bulkUpsert(ctx, s, items, mode, s.relationBulkSpec(projectID))
	var rows []dbq.Relation
	for _, w := range written {
		rows = append(rows, w.row)
	}
	return RelationBulkResult{Succeeded: rows, Failed: failed}, err
}

// relationBulkSpec is the edge half of a bulk write: everything bulk.go
// deliberately does not know.
//
// **Identity is the triple relations_edge_key folds on**, spelled in the
// terms an item actually carries: the relation type key and both
// endpoints' (type key, key). All five parts are folded, because every
// one of them is resolved through a lower(key) lookup, so two items
// spelling a key differently address one edge. They are joined
// length-prefixed by foldedIdentity for the reason it records — the
// driver folds an item to one string and none of these parts is
// pattern-validated here.
//
// **repeated cannot say what the entity message says.** An edge has no
// key of its own, so "give one of the two a different key" is advice
// about a field that does not exist; what a caller needs to hear is that
// its two items are one edge, and what to do about it. The path is the
// item rather than a field of it, for the same reason.
func (s *Service) relationBulkSpec(projectID uuid.UUID) bulkSpec[RelationInput, upsertedRelation] {
	return bulkSpec[RelationInput, upsertedRelation]{
		identity: func(in RelationInput) string {
			return foldedIdentity(in.TypeKey,
				in.Source.TypeKey, in.Source.Key, in.Target.TypeKey, in.Target.Key)
		},
		key: func(in RelationInput) string {
			return fmt.Sprintf("%s: %s/%s -> %s/%s", in.TypeKey,
				in.Source.TypeKey, in.Source.Key, in.Target.TypeKey, in.Target.Key)
		},
		repeated: func(i, first int, in RelationInput) FieldError {
			return FieldError{
				Path: fmt.Sprintf("items[%d]", i),
				Message: fmt.Sprintf(
					"an edge of type %q from %q/%q to %q/%q is already addressed by item %d of "+
						"this batch, and keys are matched without regard to case: an edge is "+
						"identified by its type and its two endpoints, so the two items are one "+
						"edge — merge their fields into one item, or point one of them at a "+
						"different pair",
					in.TypeKey, in.Source.TypeKey, in.Source.Key,
					in.Target.TypeKey, in.Target.Key, first),
			}
		},
		write: func(ctx context.Context, q *dbq.Queries, in RelationInput) (upsertedRelation, error) {
			return s.upsertRelationWith(ctx, q, projectID, in)
		},
		publish: func(written upsertedRelation) {
			s.publish(projectID, eventRelationUpserted, relationEventMinRole, relationEventHumanOnly,
				written.event())
		},
	}
}

// ListRelations returns the edges of a game matching a filter.
//
// An unknown TypeKey is a not_found rather than an empty listing: a
// caller that mistyped a key has to hear about the key, not be told this
// game has no such edges.
func (s *Service) ListRelations(ctx context.Context, projectID uuid.UUID, f RelationFilter) ([]dbq.Relation, error) {
	params := dbq.ListRelationsParams{
		ProjectID: projectID,
		SourceID:  f.SourceID,
		TargetID:  f.TargetID,
		Limit:     relationPageSize(f.Limit),
	}
	if f.TypeKey != "" {
		relType, err := s.RelationTypeByKey(ctx, projectID, f.TypeKey)
		if err != nil {
			return nil, err
		}
		params.RelationTypeID = &relType.ID
	}

	rows, err := s.q.ListRelations(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list relations: %w", err)
	}
	return rows, nil
}

// The bounds on one relation listing. There is no cursor here — Task 6
// owns pagination — so a caller that asks for nothing at all gets the
// default rather than the whole table.
const (
	defaultRelationPage int32 = 100
	maxRelationPage     int32 = 500
)

// relationPageSize turns a caller's requested limit into the one this
// listing will use.
//
// **Asking for nothing and asking for too much are two different
// requests, and they now get two different answers.** A zero or negative
// limit is "no opinion" and gets the default. A limit above the cap is
// an opinion — a caller that wants as many rows as it is allowed — and
// gets the cap. Folding both onto the default meant `Limit: 501`
// silently returned 100 rows while `Limit: 500` returned 500: asking for
// slightly too much gave strictly less than asking for the maximum,
// which is the one answer no caller can have meant, and it is silent, so
// a caller that trusted it under-read the game's graph without ever
// being told. Clamping is what every other paginated API in reach does
// and what a caller writing `Limit: math.MaxInt32` to mean "everything"
// expects.
//
// Task 6 owns the cursor, and when it arrives this stays the per-page
// bound; nothing here is a promise about how many pages exist.
func relationPageSize(limit int32) int32 {
	switch {
	case limit <= 0:
		return defaultRelationPage
	case limit > maxRelationPage:
		return maxRelationPage
	default:
		return limit
	}
}

// RemoveRelation deletes one edge, reading it and its type first so that
// relation.removed declares the same identity relation.upserted does. A
// removal announced with an empty type key tells a client an edge of a
// type it has never seen is gone, and a client reading a declared field
// cannot tell "not carried" from "empty".
func (s *Service) RemoveRelation(ctx context.Context, projectID, id uuid.UUID) error {
	var removed relationEvent
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		row, err := q.GetRelationByID(ctx, dbq.GetRelationByIDParams{ProjectID: projectID, ID: id})
		if err != nil {
			return notFound(err, "lookup relation")
		}
		// The generic helper is right here, unlike on every by-key
		// accessor: this id comes from the row just read, not from the
		// caller, so there is no key to name and nothing for a caller to
		// fix. `relations.relation_type_id` is `ON DELETE RESTRICT` and
		// this runs in the same transaction as the read, so no-rows is
		// unreachable rather than merely unlikely; if it ever fires, the
		// schema is inconsistent and `not_found` is a thin report of it.
		typ, err := q.GetRelationTypeByID(ctx, dbq.GetRelationTypeByIDParams{
			ProjectID: projectID, ID: row.RelationTypeID,
		})
		if err != nil {
			return notFound(err, "lookup relation type")
		}
		removed = relationEvent{
			ID: row.ID, TypeKey: typ.Key,
			SourceID: row.SourceID, TargetID: row.TargetID,
		}

		rows, err := q.DeleteRelation(ctx, dbq.DeleteRelationParams{ProjectID: projectID, ID: id})
		if err != nil {
			return fmt.Errorf("delete relation: %w", err)
		}
		if rows == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.publish(projectID, eventRelationRemoved, relationEventMinRole, relationEventHumanOnly, removed)
	return nil
}

// endpointAllowed reports whether an entity type may sit at an endpoint.
// An empty allow-list means the relation type accepts anything, which is
// what an undeclared list means and not "nothing may sit here".
func endpointAllowed(allowed []uuid.UUID, typeID uuid.UUID) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, id := range allowed {
		if id == typeID {
			return true
		}
	}
	return false
}
