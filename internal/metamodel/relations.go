package metamodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// Ref addresses an entity the way an agent thinks of it: type key plus
// key.
type Ref struct {
	TypeKey string
	Key     string
}

// RelationInput is an upsert request for one edge.
type RelationInput struct {
	TypeKey         string
	Source          Ref
	Target          Ref
	Fields          map[string]any
	ExpectedVersion *int32
	Actor           Actor
}

// RelationFilter narrows a relation listing. Every field is optional;
// the zero value lists the game's edges.
type RelationFilter struct {
	TypeKey string
	Invalid *bool
	// Source and Target narrow to the edges at one endpoint, addressed
	// the way every other tool on this surface addresses an entity.
	Source *Ref
	Target *Ref
	Cursor string
	Limit  int32
}

// RelationPage is one page of edges plus the cursor for the next, in the
// shape EntityPage already has, and its NextCursor obeys the contract
// EntityPage documents. The one difference is in its favour: this
// listing sorts on created_at, which nothing edits, so the "a renamed
// row moves behind the reader" clause cannot bite here.
type RelationPage struct {
	Relations  []dbq.Relation
	NextCursor string
}

// RelationWrite is one edge a batch landed, in the shape a caller reads.
// It is BulkWrite's edge counterpart; see that type for the argument
// about what a success report should and should not carry.
type RelationWrite struct {
	TypeKey  string    `json:"type_key"`
	ID       uuid.UUID `json:"id"`
	SourceID uuid.UUID `json:"source_id"`
	TargetID uuid.UUID `json:"target_id"`
	Version  int32     `json:"version"`
}

// RelationBulkResult reports what a batch of edges did.
type RelationBulkResult struct {
	Succeeded []dbq.Relation  `json:"-"`
	Written   []RelationWrite `json:"written"`
	Failed    []BulkFailure   `json:"failed"`
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

	// Both ends are judged before either is reported — resolved *and*
	// checked against the endpoint rule — and that is the same argument
	// checkEndpointTypes makes about the two endpoint *lists*: an agent
	// holding two bad ends fixes the one it was told about, resends, and
	// is told about the other. Failing on the source cost a round trip
	// per bad end and made the two halves of one decision disagree.
	source, sourceErr := endpointEntity(ctx, q, projectID, "source", in.Source)
	target, targetErr := endpointEntity(ctx, q, projectID, "target", in.Target)

	// The stored key of the endpoint's own type, not the caller's
	// spelling: this message is read beside the game's type list. An end
	// that did not resolve has no type to judge, and its own failure is
	// the one that stands.
	if sourceErr == nil && !endpointAllowed(relType.SourceTypeIds, source.typ.ID) {
		sourceErr = fmt.Errorf(
			"%w: source: entity type %q cannot be the source of relation type %q",
			ErrEndpointTypeMismatch, source.typ.Key, relType.Key)
	}
	if targetErr == nil && !endpointAllowed(relType.TargetTypeIds, target.typ.ID) {
		targetErr = fmt.Errorf(
			"%w: target: entity type %q cannot be the target of relation type %q",
			ErrEndpointTypeMismatch, target.typ.Key, relType.Key)
	}
	if err := bothEndpoints(sourceErr, targetErr); err != nil {
		return upsertedRelation{}, err
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

	expected := noVersion
	if in.ExpectedVersion != nil {
		expected = *in.ExpectedVersion
	}

	// Read under the row lock, so the version this caller is told about is
	// the one its own write will meet — the same rule and the same reason
	// as upsertEntityWith's locked read. An edge has no key of its own, so
	// there is no respelling to check here and the version is the whole
	// content of the check.
	existing, err := q.GetRelationByEdgeForUpdate(ctx, dbq.GetRelationByEdgeForUpdateParams{
		ProjectID:      projectID,
		RelationTypeID: relType.ID,
		SourceID:       source.row.ID,
		TargetID:       target.row.ID,
	})
	switch {
	case err == nil:
		if in.ExpectedVersion == nil || *in.ExpectedVersion != existing.Version {
			return upsertedRelation{}, &VersionConflictError{Current: existing.Version}
		}
	case errors.Is(err, pgx.ErrNoRows):
		// No row, and a version claimed: the edge was removed. It is the
		// same rule as the three tables above (RemovedError), and it is
		// applied here even though an edge's id is referenced by less
		// than any other row's: the point is that the caller's belief
		// was false, and a domain where four upserts of one shape answer
		// the same question two ways is a domain where the answer is a
		// coincidence of which call you made.
		if in.ExpectedVersion != nil {
			return upsertedRelation{}, &RemovedError{
				Subject: "edge",
				Address: fmt.Sprintf("%q from %q to %q", relType.Key, in.Source.Key, in.Target.Key),
				Claimed: *in.ExpectedVersion,
			}
		}
		// Creation: no version to match, nothing to lock.
	default:
		return upsertedRelation{}, fmt.Errorf("lookup relation: %w", err)
	}

	row, err := q.UpsertRelation(ctx, dbq.UpsertRelationParams{
		ProjectID:        projectID,
		RelationTypeID:   relType.ID,
		SourceID:         source.row.ID,
		TargetID:         target.row.ID,
		Fields:           encoded,
		ExpectedVersion:  expected,
		UpdatedByUserID:  in.Actor.UserID,
		UpdatedByTokenID: in.Actor.TokenID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The guarded DO UPDATE matched nothing: between the locked read
		// above and this statement another writer created or advanced the
		// edge. That is reachable even with the lock held, because on the
		// creation path there is no row to lock — two creators race into
		// the unique index and the loser arrives here.
		return upsertedRelation{}, conflictOnRelationEdge(ctx, q, projectID,
			relType.ID, source.row.ID, target.row.ID)
	}
	if err != nil {
		if mapped := ActorConstraintViolation(err); errors.Is(mapped, ErrActorNotInGame) {
			return upsertedRelation{}, mapped
		}
		if mapped := edgeParentViolation(err, in); errors.Is(mapped, ErrNotFound) {
			return upsertedRelation{}, mapped
		}
		return upsertedRelation{}, fmt.Errorf("upsert relation: %w", err)
	}
	return upsertedRelation{row: row, typeKey: relType.Key}, nil
}

// conflictOnRelationEdge re-reads an edge whose guarded upsert matched no
// row and reports the version that actually stands in the way.
func conflictOnRelationEdge(ctx context.Context, q *dbq.Queries,
	projectID, relationTypeID, sourceID, targetID uuid.UUID,
) error {
	row, err := q.GetRelationByEdge(ctx, dbq.GetRelationByEdgeParams{
		ProjectID:      projectID,
		RelationTypeID: relationTypeID,
		SourceID:       sourceID,
		TargetID:       targetID,
	})
	if err != nil {
		return fmt.Errorf("re-read relation after a failed upsert: %w", err)
	}
	return &VersionConflictError{Current: row.Version}
}

// edgeParentViolation recognises a write refused because one of an edge's
// three parents was no longer there when the insert ran, and names which
// one; every other error passes through unchanged.
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

// bothEndpoints folds the two endpoint verdicts into the one answer
// their caller returns.
func bothEndpoints(sourceErr, targetErr error) error {
	switch {
	case sourceErr != nil && targetErr != nil:
		return &joinedEndpointError{source: sourceErr, target: targetErr}
	case sourceErr != nil:
		return sourceErr
	case targetErr != nil:
		return targetErr
	}
	return nil
}

// joinedEndpointError carries both ends' failures. It exists instead of
// fmt.Errorf("%w; %w", …) for the message alone: two ends failing the
// same way printed the sentinel twice — "not_found: source: …;
// not_found: target: …" — where the second copy names a code the reader
// has already been given. Unwrap returns both, so errors.Is answers for
// either and failureFor files the item exactly as it did.
type joinedEndpointError struct{ source, target error }

func (e *joinedEndpointError) Unwrap() []error { return []error{e.source, e.target} }

func (e *joinedEndpointError) Error() string {
	first := e.source.Error()
	second := e.target.Error()
	// The prefix is dropped only when the two halves agree on it; when
	// they name different codes both are kept, because then the second
	// code is news.
	if i := strings.Index(first, ": "); i >= 0 {
		if prefix := first[:i+2]; strings.HasPrefix(second, prefix) {
			second = second[len(prefix):]
		}
	}
	return first + "; " + second
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
func (s *Service) UpsertRelations(ctx context.Context, projectID uuid.UUID, items []RelationInput, mode BulkMode) (RelationBulkResult, error) {
	written, failed, err := BulkUpsert(ctx, s.withTx, items, mode, s.relationBulkSpec(projectID))
	var (
		rows    []dbq.Relation
		reports []RelationWrite
	)
	for _, w := range written {
		rows = append(rows, w.row)
		reports = append(reports, RelationWrite{
			TypeKey: w.typeKey, ID: w.row.ID,
			SourceID: w.row.SourceID, TargetID: w.row.TargetID,
			Version: w.row.Version,
		})
	}
	return RelationBulkResult{Succeeded: rows, Written: reports, Failed: failed}, err
}

// relationBulkSpec is the edge half of a bulk write: everything bulk.go
// deliberately does not know.
func (s *Service) relationBulkSpec(projectID uuid.UUID) BulkSpec[RelationInput, upsertedRelation] {
	return BulkSpec[RelationInput, upsertedRelation]{
		Identity: func(in RelationInput) string {
			return FoldedIdentity(in.TypeKey,
				in.Source.TypeKey, in.Source.Key, in.Target.TypeKey, in.Target.Key)
		},
		Key: func(in RelationInput) string {
			return fmt.Sprintf("%s: %s/%s -> %s/%s", in.TypeKey,
				in.Source.TypeKey, in.Source.Key, in.Target.TypeKey, in.Target.Key)
		},
		Repeated: func(i, first int, in RelationInput) FieldError {
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
		Write: func(ctx context.Context, q *dbq.Queries, in RelationInput) (upsertedRelation, error) {
			return s.upsertRelationWith(ctx, q, projectID, in)
		},
		Publish: func(written upsertedRelation) {
			s.publish(projectID, eventRelationUpserted, relationEventMinRole, relationEventHumanOnly,
				written.event())
		},
	}
}

// ListRelations returns one page of the edges of a game matching a
// filter.
func (s *Service) ListRelations(ctx context.Context, projectID uuid.UUID, f RelationFilter) (RelationPage, error) {
	limit := relationPageSize(f.Limit)
	params := dbq.ListRelationsParams{
		ProjectID: projectID,
		Invalid:   f.Invalid,
		Limit:     limit,
	}
	// The two endpoint refs, resolved before the listing runs. A ref that
	// names no entity is not_found rather than an empty page: "this game
	// holds no edge at that entity" and "this game holds no such entity"
	// are different answers, and only one of them is worth a second call.
	var endpointProblems []FieldError
	for _, end := range []struct {
		path string
		ref  *Ref
	}{{"source", f.Source}, {"target", f.Target}} {
		if end.ref == nil {
			continue
		}
		endpointProblems = append(endpointProblems,
			rowKeyProblems(end.path+".type_key", end.ref.TypeKey)...)
		endpointProblems = append(endpointProblems,
			rowKeyProblems(end.path+".key", end.ref.Key)...)
	}
	if len(endpointProblems) > 0 {
		return RelationPage{}, &ValidationError{Code: codeInvalidInput, Fields: endpointProblems}
	}
	for _, end := range []struct {
		path string
		ref  *Ref
		id   **uuid.UUID
	}{{"source", f.Source, &params.SourceID}, {"target", f.Target, &params.TargetID}} {
		if end.ref == nil {
			continue
		}
		row, err := s.EntityByKey(ctx, projectID, end.ref.TypeKey, end.ref.Key)
		if err != nil {
			return RelationPage{}, fmt.Errorf("%s: %w", end.path, err)
		}
		id := row.ID
		*end.id = &id
	}
	typePart := ""
	if f.TypeKey != "" {
		// Bounded before the lookup runs, the same rule and the same reason as
		// ListEntities' TypeKey: rowKeyProblems is what UpsertRelationType checks
		// a caller's key against before it is ever a row, and running it here
		// closes the same SQLSTATE 22021 this domain's own correction elsewhere
		// already closed for the markdown package's entity filter, rather than
		// leaving this sibling listing to reach Postgres unbounded.
		// TestRelationsArea's "a relation type key filter is bounded before
		// postgres sees it" case pins it.
		if problems := rowKeyProblems("type_key", f.TypeKey); len(problems) > 0 {
			return RelationPage{}, &ValidationError{Code: codeInvalidInput, Fields: problems}
		}
		relType, err := s.RelationTypeByKey(ctx, projectID, f.TypeKey)
		if err != nil {
			return RelationPage{}, err
		}
		params.RelationTypeID = &relType.ID
		typePart = relType.ID.String()
	}

	// The invalid filter is part of the fingerprint, exactly as it is on
	// the two entity listings: every filter of a listing shares one sort
	// order, so a cursor carried from "the invalid edges" to "all edges"
	// would page perfectly and answer a different question.
	// The endpoint filters go into the fingerprint *resolved*, not as
	// spelled, which is the rule ListEntities' traversal states: two
	// spellings of one key are one listing and must share one cursor.
	fingerprint := fingerprintOf(projectID.String(), "relations", typePart,
		endpointFilterPart(params.SourceID), endpointFilterPart(params.TargetID),
		invalidFilterPart(f.Invalid))
	after, err := decodeCursor(f.Cursor, fingerprint)
	if err != nil {
		return RelationPage{}, err
	}
	if after.ID != uuid.Nil {
		at, err := time.Parse(time.RFC3339Nano, after.Sort)
		if err != nil {
			// Only a hand-edited cursor reaches this: encodeCursor below writes the
			// same format this parses, and the fingerprint has already agreed. It is
			// still the caller's own argument, so it is answered as one rather than
			// as a server fault. TestRelationsArea's "a relations cursor with a
			// forged non timestamp sort is malformed" case reaches this arm with
			// exactly that: a cursor whose fingerprint agrees and whose sort half is
			// not RFC 3339.
			return RelationPage{}, malformedCursor("it carries no creation time")
		}
		params.AfterCreatedAt = pgtype.Timestamptz{Time: at, Valid: true}
		params.AfterID = &after.ID
	}

	rows, err := s.q.ListRelations(ctx, params)
	if err != nil {
		return RelationPage{}, fmt.Errorf("list relations: %w", err)
	}
	page := RelationPage{Relations: rows}
	if len(rows) == int(limit) {
		last := rows[len(rows)-1]
		page.NextCursor = encodeCursor(cursor{
			Sort:        last.CreatedAt.Time.Format(time.RFC3339Nano),
			ID:          last.ID,
			Fingerprint: fingerprint,
		})
	}
	return page, nil
}

// RelationByEdge reads one edge by the address it was written under:
// its relation type's key and both endpoints as (type key, key) refs.
func (s *Service) RelationByEdge(ctx context.Context, projectID uuid.UUID, typeKey string, source, target Ref) (dbq.Relation, error) {
	var problems []FieldError
	for _, part := range []struct{ path, key string }{
		{"type_key", typeKey},
		{"source.type_key", source.TypeKey}, {"source.key", source.Key},
		{"target.type_key", target.TypeKey}, {"target.key", target.Key},
	} {
		problems = append(problems, rowKeyProblems(part.path, part.key)...)
	}
	if len(problems) > 0 {
		return dbq.Relation{}, &ValidationError{Code: codeInvalidInput, Fields: problems}
	}

	relType, err := s.RelationTypeByKey(ctx, projectID, typeKey)
	if err != nil {
		return dbq.Relation{}, err
	}
	sourceRow, err := s.EntityByKey(ctx, projectID, source.TypeKey, source.Key)
	if err != nil {
		return dbq.Relation{}, err
	}
	targetRow, err := s.EntityByKey(ctx, projectID, target.TypeKey, target.Key)
	if err != nil {
		return dbq.Relation{}, err
	}
	row, err := s.q.GetRelationByEdge(ctx, dbq.GetRelationByEdgeParams{
		ProjectID:      projectID,
		RelationTypeID: relType.ID,
		SourceID:       sourceRow.ID,
		TargetID:       targetRow.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The stored spellings, not the caller's: keys are matched
		// without regard to case, so echoing the request back would tell
		// a designer who typed "Elwynn" that there is no edge from
		// "Elwynn" when the game holds "elwynn".
		return dbq.Relation{}, fmt.Errorf(
			"%w: no %q edge from %s %q to %s %q in this game",
			ErrNotFound, relType.Key,
			source.TypeKey, sourceRow.Key, target.TypeKey, targetRow.Key)
	}
	if err != nil {
		return dbq.Relation{}, fmt.Errorf("lookup relation: %w", err)
	}
	return row, nil
}

// endpointFilterPart spells an optional endpoint filter for a
// fingerprint, keeping "no opinion" distinct from any id.
func endpointFilterPart(id *uuid.UUID) string {
	if id == nil {
		return "any"
	}
	return id.String()
}

// The bounds on one relation listing. A caller that asks for nothing at
// all gets the default rather than the whole table, and reaches the rest
// through the cursor ListRelations hands back.
const (
	defaultRelationPage int32 = 100
	maxRelationPage     int32 = 500
)

// relationPageSize turns a caller's requested limit into the one this
// listing will use.
func relationPageSize(limit int32) int32 {
	return pageSize(limit, defaultRelationPage, maxRelationPage)
}

// RemoveRelation deletes one edge by the address it was written under:
// its relation type's key and both endpoints as (type key, key) refs.
func (s *Service) RemoveRelation(ctx context.Context, projectID uuid.UUID,
	typeKey string, source, target Ref,
) error {
	var problems []FieldError
	for _, part := range []struct{ path, key string }{
		{"type_key", typeKey},
		{"source.type_key", source.TypeKey}, {"source.key", source.Key},
		{"target.type_key", target.TypeKey}, {"target.key", target.Key},
	} {
		problems = append(problems, rowKeyProblems(part.path, part.key)...)
	}
	if len(problems) > 0 {
		return &ValidationError{Code: codeInvalidInput, Fields: problems}
	}

	var removed relationEvent
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		relType, err := q.GetRelationTypeByKey(ctx, dbq.GetRelationTypeByKeyParams{
			ProjectID: projectID, Key: typeKey,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: no relation type %q in this game", ErrNotFound, typeKey)
		}
		if err != nil {
			return fmt.Errorf("lookup relation type: %w", err)
		}
		// Both ends resolved before either is reported, the rule
		// upsertRelationWith argues: an agent holding two bad ends fixes
		// the one it was told about, resends, and is told about the
		// other.
		sourceEnd, sourceErr := endpointEntity(ctx, q, projectID, "source", source)
		targetEnd, targetErr := endpointEntity(ctx, q, projectID, "target", target)
		if err := bothEndpoints(sourceErr, targetErr); err != nil {
			return err
		}

		row, err := q.GetRelationByEdge(ctx, dbq.GetRelationByEdgeParams{
			ProjectID:      projectID,
			RelationTypeID: relType.ID,
			SourceID:       sourceEnd.row.ID,
			TargetID:       targetEnd.row.ID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// The stored spellings, not the caller's; RelationByEdge
			// carries the argument and this message matches its wording
			// deliberately, so a designer who reads the edge and then
			// fails to remove it sees one sentence rather than two.
			return fmt.Errorf(
				"%w: no %q edge from %s %q to %s %q in this game",
				ErrNotFound, relType.Key,
				sourceEnd.typ.Key, sourceEnd.row.Key, targetEnd.typ.Key, targetEnd.row.Key)
		}
		if err != nil {
			return fmt.Errorf("lookup relation: %w", err)
		}
		removed = relationEvent{
			ID: row.ID, TypeKey: relType.Key,
			SourceID: row.SourceID, TargetID: row.TargetID,
		}

		rows, err := q.DeleteRelation(ctx, dbq.DeleteRelationParams{ProjectID: projectID, ID: row.ID})
		if err != nil {
			return fmt.Errorf("delete relation: %w", err)
		}
		if rows == 0 {
			return fmt.Errorf(
				"%w: no %q edge from %s %q to %s %q in this game",
				ErrNotFound, relType.Key,
				sourceEnd.typ.Key, sourceEnd.row.Key, targetEnd.typ.Key, targetEnd.row.Key)
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
