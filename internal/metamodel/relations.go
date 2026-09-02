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
// tells apart. **Self-loops are allowed**: source and target may be the
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

	source, err := endpointEntity(ctx, q, projectID, "source", in.Source)
	if err != nil {
		return upsertedRelation{}, err
	}
	target, err := endpointEntity(ctx, q, projectID, "target", in.Target)
	if err != nil {
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
		Limit:     f.Limit,
	}
	if params.Limit <= 0 || params.Limit > maxRelationPage {
		params.Limit = defaultRelationPage
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
// owns pagination — so a caller asking for more than the cap, or for
// nothing at all, gets the default rather than the whole table.
const (
	defaultRelationPage int32 = 100
	maxRelationPage     int32 = 500
)

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
