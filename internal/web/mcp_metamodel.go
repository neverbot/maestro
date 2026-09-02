package web

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
)

// This file is the game-content half of the MCP surface: the tools an
// agent uses to declare a game's vocabulary (entity types and relation
// types) and to fill it (entities, relations), plus the two ways of
// getting rows back out (listing with a cursor, and search).
//
// **Every function here takes both a Caller and a resolved project id,
// and every one starts with requireScope.** That is not belt and braces
// over addScopedTool's own check: these functions are exported and
// tested directly, without the wire wrapper, precisely so the isolation
// invariant is pinned here too — and the query layer underneath takes
// the project id as a parameter and filters on it in SQL, which is where
// the invariant is actually enforced (see the metamodel plan's Task 7
// requirement). A handler that forgot to pass a project id would not
// compile; a handler that merely forgot to *check* one would.
//
// **No *input* type here is a domain type.** Every input is a struct
// declared in this file, converted into the domain's own input by hand.
// The one shape this rules out is the important one: metamodel's
// EntityInput and RelationInput carry an Actor, and an Actor is the
// audit record of *who wrote this row*. Accepting the domain type
// straight off the wire would let an agent name any user or token it
// liked as the author of its writes. actorOf builds it from the
// authenticated caller and nothing else.
//
// **The outputs are a different claim, and a weaker one — review
// finding L1.** This comment used to say "no wire type here is a domain
// type", unqualified, and that was false in the output direction:
// TypeDetailOutput.Schema and RelationTypeDetailOutput.Schema are
// metamodel.Schema, and the two bulk outputs carry []metamodel.
// BulkWrite, []metamodel.RelationWrite and []metamodel.BulkFailure. The
// hand-written output schemas do not shut the gap either — a field
// added to metamodel.BulkWrite reached the wire through them. So the
// four are re-exported deliberately (their field lists are the wire
// contract, and a shadow struct would be a copy to keep in step), and
// what holds the line is a *test* rather than the type system:
// TestTheDomainTypesOnTheWireCarryExactlyTheseKeys marshals each of the
// four and pins its key set, so a field added to any of them fails here
// and its author decides whether an agent should see it.
//
// **Ids cross the wire as strings.** The SDK infers a tool's input
// schema from its Go input type by reflection, and uuid.UUID is a
// [16]byte, which reflects to an array of integers and not to the string
// it actually marshals as. Outputs keep uuid.UUID because their schemas
// are hand-written (see mcp.go's own note on this), but inputs cannot,
// so every id an agent sends is a string this file parses — and a
// malformed one is invalid_input at that argument's own path, never a
// 500.

// --- Inputs ---

// TypesUpsertInput is the argument shape of types.upsert.
type TypesUpsertInput struct {
	ScopedArgs
	Key             string       `json:"key"`
	Label           string       `json:"label"`
	LabelPlural     string       `json:"label_plural"`
	Description     string       `json:"description,omitempty"`
	Color           string       `json:"color,omitempty"`
	Icon            string       `json:"icon,omitempty"`
	Schema          []FieldInput `json:"field_schema,omitempty"`
	ExpectedVersion *int32       `json:"expected_version,omitempty"`
}

// FieldInput is one declared field of a type's schema, in the shape an
// agent writes it.
//
// It mirrors metamodel.Field's *wire* form rather than its Go form, and
// the difference is Default. On metamodel.Field, Default is `json:"-"`
// and paired with a HasDefault flag, because "declared as false" and
// "not declared" are different declarations and a bare `any` cannot tell
// them apart; the wire form has just the one key, present or absent.
// Converting through JSON (schemaOf, below) hands that distinction to
// metamodel's own decoder instead of re-deriving it here, so there is
// one rule about what a default means and it lives in the domain.
//
// Default is `any` rather than json.RawMessage for the reflection reason
// this file's header gives: json.RawMessage is []byte and would reflect
// to a string schema, refusing every non-string default an agent sends.
type FieldInput struct {
	Key      string   `json:"key"`
	Label    string   `json:"label,omitempty"`
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Default  any      `json:"default,omitempty"`
	Options  []string `json:"options,omitempty"`
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
}

// TypesListInput and RelationTypesListInput carry nothing but the
// optional project_id confirmation every scoped tool accepts. They are
// named types rather than anonymous structs so the SDK's generated input
// schema has a name to report in a validation message, and exported for
// the same reason every other input here is: this package's external
// test package calls the MCP* functions directly, without the wire
// wrapper, which is where the scope invariant is pinned.
type TypesListInput struct{ ScopedArgs }

// RelationTypesListInput is TypesListInput for relation types.
type RelationTypesListInput struct{ ScopedArgs }

// TypesGetInput and TypesRemoveInput address one entity type: by key for
// a read, by id for a removal. The asymmetry is the domain's — see
// EntityTypeByKey and RemoveEntityType — and it is why types.get's
// answer carries the id a later removal needs.
type TypesGetInput struct {
	ScopedArgs
	Key string `json:"key"`
}

// TypesRemoveInput removes one entity type. Cascade decides whether a
// type that still has entities is refused (in_use) or taken down with
// them.
type TypesRemoveInput struct {
	ScopedArgs
	ID      string `json:"id"`
	Cascade bool   `json:"cascade,omitempty"`
}

// RelationTypesUpsertInput is the argument shape of
// relation_types.upsert. The two endpoint lists are entity type *ids*,
// which types.upsert and types.list are where an agent gets them.
type RelationTypesUpsertInput struct {
	ScopedArgs
	Key             string       `json:"key"`
	Label           string       `json:"label"`
	Description     string       `json:"description,omitempty"`
	SourceTypeIDs   []string     `json:"source_type_ids,omitempty"`
	TargetTypeIDs   []string     `json:"target_type_ids,omitempty"`
	SemanticRole    string       `json:"semantic_role,omitempty"`
	Schema          []FieldInput `json:"field_schema,omitempty"`
	ExpectedVersion *int32       `json:"expected_version,omitempty"`
}

// RelationTypesGetInput reads one relation type by key.
type RelationTypesGetInput struct {
	ScopedArgs
	Key string `json:"key"`
}

// RelationTypesRemoveInput removes one relation type by id.
type RelationTypesRemoveInput struct {
	ScopedArgs
	ID      string `json:"id"`
	Cascade bool   `json:"cascade,omitempty"`
}

// EntitiesUpsertInput is the argument shape of entities.upsert. It takes
// a batch; one entity is a batch of one, which is why there is no
// separate single-row tool to keep in step with this one.
type EntitiesUpsertInput struct {
	ScopedArgs
	Mode  string            `json:"mode,omitempty"`
	Items []EntityItemInput `json:"items"`
}

// EntityItemInput is one row of an entities.upsert batch. It carries no
// actor: see this file's header.
type EntityItemInput struct {
	TypeKey         string         `json:"type_key"`
	Key             string         `json:"key"`
	Name            string         `json:"name"`
	Fields          map[string]any `json:"fields,omitempty"`
	ExpectedVersion *int32         `json:"expected_version,omitempty"`
}

// EntitiesListInput is the argument shape of entities.list.
//
// Verbose is off by default, and that is the decision this listing turns
// on: a page of five hundred entities with their fields is the whole
// game back in one answer, and an agent walking a catalogue almost
// always wants keys and names. It asks for fields when it means to read
// them.
type EntitiesListInput struct {
	ScopedArgs
	TypeKey   string          `json:"type_key,omitempty"`
	Invalid   *bool           `json:"invalid,omitempty"`
	RelatedTo *RelatedToInput `json:"related_to,omitempty"`
	Cursor    string          `json:"cursor,omitempty"`
	Limit     int32           `json:"limit,omitempty"`
	Verbose   bool            `json:"verbose,omitempty"`
}

// RelatedToInput is the one-hop traversal an entity listing can be
// anchored to: the entities reachable from (or reaching) one entity over
// one relation type.
//
// **Direction carries no `omitempty`, and that is the whole point.** The
// SDK infers this tool's input schema by reflection and puts exactly the
// fields without `omitempty` in `required`, so the tag is the wire
// contract. `listRelated` (internal/metamodel/list.go) refuses an absent
// or unrecognised direction outright, arguing that answering half a
// neighbourhood is a wrong answer rather than a refusal — and it is
// right. Until review finding H2 this struct and the tool description
// both told an agent the field was optional and defaulted to
// "outgoing", so the only way to discover the domain's rule was to trip
// over it. The three agree now: required in the schema, required in the
// prose, refused by the domain.
type RelatedToInput struct {
	RelationTypeKey string `json:"relation_type_key"`
	EntityTypeKey   string `json:"entity_type_key"`
	EntityKey       string `json:"entity_key"`
	Direction       string `json:"direction"`
}

// EntitiesGetInput reads one entity by its address.
type EntitiesGetInput struct {
	ScopedArgs
	TypeKey string `json:"type_key"`
	Key     string `json:"key"`
}

// EntitiesRemoveInput removes one entity by id. Its edges go with it, by
// cascade.
type EntitiesRemoveInput struct {
	ScopedArgs
	ID string `json:"id"`
}

// RelationsUpsertInput is the argument shape of relations.upsert.
type RelationsUpsertInput struct {
	ScopedArgs
	Mode  string              `json:"mode,omitempty"`
	Items []RelationItemInput `json:"items"`
}

// RelationItemInput is one edge of a relations.upsert batch. There is no
// expected_version: an edge has no version column, and
// metamodel.RelationInput's own doc comment records that decision and
// what it costs.
type RelationItemInput struct {
	TypeKey string         `json:"type_key"`
	Source  RefInput       `json:"source"`
	Target  RefInput       `json:"target"`
	Fields  map[string]any `json:"fields,omitempty"`
}

// RefInput addresses an entity the way an agent thinks of it.
type RefInput struct {
	TypeKey string `json:"type_key"`
	Key     string `json:"key"`
}

// RelationsListInput is the argument shape of relations.list.
type RelationsListInput struct {
	ScopedArgs
	TypeKey  string `json:"type_key,omitempty"`
	SourceID string `json:"source_id,omitempty"`
	TargetID string `json:"target_id,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
	Limit    int32  `json:"limit,omitempty"`
}

// RelationsRemoveInput removes one edge by id.
type RelationsRemoveInput struct {
	ScopedArgs
	ID string `json:"id"`
}

// SearchInput is the argument shape of search.
type SearchInput struct {
	ScopedArgs
	Query   string `json:"query"`
	TypeKey string `json:"type_key,omitempty"`
	Limit   int32  `json:"limit,omitempty"`
}

// --- Outputs ---

// TypeOutput is the slim shape an entity type is listed under.
type TypeOutput struct {
	ID          uuid.UUID `json:"id"`
	Key         string    `json:"key"`
	Label       string    `json:"label"`
	LabelPlural string    `json:"label_plural"`
	Version     int32     `json:"version"`
}

// TypeDetailOutput is one entity type in full, including the field
// schema every entity of it is judged against. types.get answers with
// this and types.list does not: a schema is the largest thing a type
// carries and a catalogue of twenty types rarely wants twenty of them.
type TypeDetailOutput struct {
	TypeOutput
	Description string           `json:"description"`
	Color       string           `json:"color"`
	Icon        string           `json:"icon"`
	Schema      metamodel.Schema `json:"field_schema"`
}

// TypesListOutput is types.list's answer. It carries the pagination
// envelope every list tool on this surface carries (GamesListOutput's
// own doc comment argues for it) even though this listing is not paged:
// a game's type vocabulary is a handful of rows a designer wrote by
// hand, ListEntityTypes returns all of them, and next_cursor is
// therefore always absent. It is here so a client parses one list shape,
// and so the day this listing does need a bound it grows a cursor rather
// than a new envelope.
type TypesListOutput struct {
	Items      []TypeOutput `json:"items"`
	NextCursor *string      `json:"next_cursor,omitempty"`
	Truncated  bool         `json:"truncated"`
}

// RelationTypeOutput is the slim shape a relation type is listed under.
type RelationTypeOutput struct {
	ID      uuid.UUID `json:"id"`
	Key     string    `json:"key"`
	Label   string    `json:"label"`
	Version int32     `json:"version"`
}

// RelationTypeDetailOutput is one relation type in full: its endpoint
// rules, its semantic role and its field schema.
type RelationTypeDetailOutput struct {
	RelationTypeOutput
	Description   string           `json:"description"`
	SourceTypeIDs []uuid.UUID      `json:"source_type_ids"`
	TargetTypeIDs []uuid.UUID      `json:"target_type_ids"`
	SemanticRole  string           `json:"semantic_role,omitempty"`
	Schema        metamodel.Schema `json:"field_schema"`
}

// RelationTypesListOutput is relation_types.list's answer; see
// TypesListOutput for why the envelope is here.
type RelationTypesListOutput struct {
	Items      []RelationTypeOutput `json:"items"`
	NextCursor *string              `json:"next_cursor,omitempty"`
	Truncated  bool                 `json:"truncated"`
}

// EntityOutput is one entity.
//
// TypeKey is resolved from the row's entity_type_id, because an id an
// agent cannot interpret is not an answer: a listing that spans types —
// which the unfiltered one does — would otherwise say nothing about what
// each row is. Fields is present only when the caller asked to be
// verbose.
type EntityOutput struct {
	ID      uuid.UUID      `json:"id"`
	TypeKey string         `json:"type_key"`
	Key     string         `json:"key"`
	Name    string         `json:"name"`
	Version int32          `json:"version"`
	Invalid bool           `json:"invalid"`
	Fields  map[string]any `json:"fields,omitempty"`
}

// EntitiesListOutput is one page of entities.
//
// NextCursor is the position to resume from, and Truncated says the same
// thing in a boolean so a client can branch without a null check. They
// are set together, from one condition, so they cannot disagree.
type EntitiesListOutput struct {
	Items      []EntityOutput `json:"items"`
	NextCursor *string        `json:"next_cursor,omitempty"`
	Truncated  bool           `json:"truncated"`
}

// SearchHit is one search result: an entity, whether its *name* satisfied
// the query, and the rank it matched at.
//
// NameMatch is on the wire, not only in the sort, because the order
// SearchEntities produces is `(name_match, rank)` — rank alone does not
// explain it. A caller that re-sorts by rank, or simply reasons that a
// higher rank must come first, reconstructs the wrong order: a row with
// name_match false can carry a higher rank than one with it true and
// still sort after it. name_match is what lets an agent recover the
// grouping the order is actually built from.
type SearchHit struct {
	EntityOutput
	NameMatch bool    `json:"name_match"`
	Rank      float32 `json:"rank"`
}

// SearchOutput is search's answer.
//
// It carries no cursor, and that is not an omission: search returns the
// top `limit` rows by `(name_match, rank)` order — see SearchHit — and
// neither of those is a position a caller can resume from (Search's own
// doc comment argues it). Truncated is therefore the only thing this
// envelope can honestly say, and it says the weaker thing it can
// actually check — the answer is exactly as long as the limit allowed,
// so there may be more.
type SearchOutput struct {
	Items     []SearchHit `json:"items"`
	Truncated bool        `json:"truncated"`
}

// RelationOutput is one edge. Its endpoints are ids rather than the
// (type key, key) refs the caller wrote, because that is what the row
// holds; entities.list with related_to is how an agent walks the graph
// in the terms it thinks in.
type RelationOutput struct {
	ID       uuid.UUID `json:"id"`
	TypeKey  string    `json:"type_key"`
	SourceID uuid.UUID `json:"source_id"`
	TargetID uuid.UUID `json:"target_id"`
}

// RelationsListOutput is one page of edges.
type RelationsListOutput struct {
	Items      []RelationOutput `json:"items"`
	NextCursor *string          `json:"next_cursor,omitempty"`
	Truncated  bool             `json:"truncated"`
}

// EntitiesUpsertOutput reports what a batch of entities did.
//
// Written is the metamodel's own success report (metamodel.BulkWrite,
// which argues what it carries and why), and Failed the per-item
// failures. Count is len(Written), built at the one place both are
// assembled so the two cannot disagree; it is here because "did all four
// hundred land" is the first question and it should not need a client to
// walk an array.
type EntitiesUpsertOutput struct {
	Count   int                     `json:"count"`
	Written []metamodel.BulkWrite   `json:"written"`
	Failed  []metamodel.BulkFailure `json:"failed"`
}

// RelationsUpsertOutput reports what a batch of edges did; see
// EntitiesUpsertOutput.
type RelationsUpsertOutput struct {
	Count   int                       `json:"count"`
	Written []metamodel.RelationWrite `json:"written"`
	Failed  []metamodel.BulkFailure   `json:"failed"`
}

// RemovedOutput is what every removal answers with. A removal has
// nothing to return but the fact that it happened, and a tool that
// returned nothing at all would have no structured content for a client
// to distinguish from an error it failed to parse.
type RemovedOutput struct {
	Removed bool `json:"removed"`
}

// --- Tool implementations ---

// MCPTypesUpsert implements types.upsert.
func MCPTypesUpsert(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in TypesUpsertInput) (TypeDetailOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return TypeDetailOutput{}, err
	}
	schema, err := schemaOf(in.Schema)
	if err != nil {
		return TypeDetailOutput{}, err
	}
	row, err := deps.Metamodel.UpsertEntityType(ctx, projectID, metamodel.EntityTypeInput{
		Key:             in.Key,
		Label:           in.Label,
		LabelPlural:     in.LabelPlural,
		Description:     in.Description,
		Color:           in.Color,
		Icon:            in.Icon,
		Schema:          schema,
		ExpectedVersion: in.ExpectedVersion,
		Actor:           actorOf(caller),
	})
	if err != nil {
		return TypeDetailOutput{}, err
	}
	return typeDetailOf(row)
}

// MCPTypesList implements types.list.
func MCPTypesList(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, _ TypesListInput) (TypesListOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return TypesListOutput{}, err
	}
	rows, err := deps.Metamodel.ListEntityTypes(ctx, projectID)
	if err != nil {
		return TypesListOutput{}, err
	}
	items := make([]TypeOutput, 0, len(rows))
	for _, row := range rows {
		items = append(items, typeOf(row))
	}
	return TypesListOutput{Items: items}, nil
}

// MCPTypesGet implements types.get.
func MCPTypesGet(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in TypesGetInput) (TypeDetailOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return TypeDetailOutput{}, err
	}
	row, err := deps.Metamodel.EntityTypeByKey(ctx, projectID, in.Key)
	if err != nil {
		return TypeDetailOutput{}, err
	}
	return typeDetailOf(row)
}

// MCPTypesRemove implements types.remove.
func MCPTypesRemove(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in TypesRemoveInput) (RemovedOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return RemovedOutput{}, err
	}
	id, err := parseID("id", in.ID)
	if err != nil {
		return RemovedOutput{}, err
	}
	if err := deps.Metamodel.RemoveEntityType(ctx, projectID, id, in.Cascade); err != nil {
		return RemovedOutput{}, err
	}
	return RemovedOutput{Removed: true}, nil
}

// MCPRelationTypesUpsert implements relation_types.upsert.
func MCPRelationTypesUpsert(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in RelationTypesUpsertInput) (RelationTypeDetailOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return RelationTypeDetailOutput{}, err
	}
	schema, err := schemaOf(in.Schema)
	if err != nil {
		return RelationTypeDetailOutput{}, err
	}
	sources, err := parseIDs("source_type_ids", in.SourceTypeIDs)
	if err != nil {
		return RelationTypeDetailOutput{}, err
	}
	targets, err := parseIDs("target_type_ids", in.TargetTypeIDs)
	if err != nil {
		return RelationTypeDetailOutput{}, err
	}
	row, err := deps.Metamodel.UpsertRelationType(ctx, projectID, metamodel.RelationTypeInput{
		Key:             in.Key,
		Label:           in.Label,
		Description:     in.Description,
		SourceTypeIDs:   sources,
		TargetTypeIDs:   targets,
		SemanticRole:    in.SemanticRole,
		Schema:          schema,
		ExpectedVersion: in.ExpectedVersion,
		Actor:           actorOf(caller),
	})
	if err != nil {
		return RelationTypeDetailOutput{}, err
	}
	return relationTypeDetailOf(row)
}

// MCPRelationTypesList implements relation_types.list.
func MCPRelationTypesList(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, _ RelationTypesListInput) (RelationTypesListOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return RelationTypesListOutput{}, err
	}
	rows, err := deps.Metamodel.ListRelationTypes(ctx, projectID)
	if err != nil {
		return RelationTypesListOutput{}, err
	}
	items := make([]RelationTypeOutput, 0, len(rows))
	for _, row := range rows {
		items = append(items, relationTypeOf(row))
	}
	return RelationTypesListOutput{Items: items}, nil
}

// MCPRelationTypesGet implements relation_types.get.
func MCPRelationTypesGet(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in RelationTypesGetInput) (RelationTypeDetailOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return RelationTypeDetailOutput{}, err
	}
	row, err := deps.Metamodel.RelationTypeByKey(ctx, projectID, in.Key)
	if err != nil {
		return RelationTypeDetailOutput{}, err
	}
	return relationTypeDetailOf(row)
}

// MCPRelationTypesRemove implements relation_types.remove.
func MCPRelationTypesRemove(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in RelationTypesRemoveInput) (RemovedOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return RemovedOutput{}, err
	}
	id, err := parseID("id", in.ID)
	if err != nil {
		return RemovedOutput{}, err
	}
	if err := deps.Metamodel.RemoveRelationType(ctx, projectID, id, in.Cascade); err != nil {
		return RemovedOutput{}, err
	}
	return RemovedOutput{Removed: true}, nil
}

// MCPEntitiesUpsert implements entities.upsert.
//
// The mode string is passed through as the agent wrote it rather than
// being folded onto the default when it is not recognised: bulkUpsert
// refuses an unknown mode as invalid_input at path `mode`, deliberately,
// because reading a typo as "partial" would silently land rows a caller
// asked to have rolled back.
func MCPEntitiesUpsert(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in EntitiesUpsertInput) (EntitiesUpsertOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return EntitiesUpsertOutput{}, err
	}
	actor := actorOf(caller)
	items := make([]metamodel.EntityInput, 0, len(in.Items))
	for _, item := range in.Items {
		items = append(items, metamodel.EntityInput{
			TypeKey:         item.TypeKey,
			Key:             item.Key,
			Name:            item.Name,
			Fields:          item.Fields,
			ExpectedVersion: item.ExpectedVersion,
			Actor:           actor,
		})
	}
	result, err := deps.Metamodel.UpsertEntities(ctx, projectID, items, metamodel.BulkMode(in.Mode))
	if err != nil {
		return EntitiesUpsertOutput{}, err
	}
	// Both slices are emitted as arrays even when empty. A nil slice
	// marshals to JSON null, which is not what the output schema says
	// and — more to the point — is not what a client walking `failed`
	// can iterate: "the batch reported no failures" and "the batch
	// reported nothing" must not look the same.
	out := EntitiesUpsertOutput{
		Count:   len(result.Written),
		Written: result.Written,
		Failed:  result.Failed,
	}
	if out.Written == nil {
		out.Written = []metamodel.BulkWrite{}
	}
	if out.Failed == nil {
		out.Failed = []metamodel.BulkFailure{}
	}
	return out, nil
}

// MCPEntitiesList implements entities.list.
func MCPEntitiesList(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in EntitiesListInput) (EntitiesListOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return EntitiesListOutput{}, err
	}
	filter := metamodel.EntityFilter{
		TypeKey: in.TypeKey,
		Invalid: in.Invalid,
		Cursor:  in.Cursor,
		Limit:   in.Limit,
	}
	if in.RelatedTo != nil {
		filter.RelatedTo = &metamodel.RelatedFilter{
			RelationTypeKey: in.RelatedTo.RelationTypeKey,
			EntityTypeKey:   in.RelatedTo.EntityTypeKey,
			EntityKey:       in.RelatedTo.EntityKey,
			Direction:       in.RelatedTo.Direction,
		}
	}
	page, err := deps.Metamodel.ListEntities(ctx, projectID, filter)
	if err != nil {
		return EntitiesListOutput{}, err
	}
	names, err := entityTypeKeys(ctx, deps, projectID)
	if err != nil {
		return EntitiesListOutput{}, err
	}
	items := make([]EntityOutput, 0, len(page.Entities))
	for _, row := range page.Entities {
		out, err := entityOf(row, names, in.Verbose)
		if err != nil {
			return EntitiesListOutput{}, err
		}
		items = append(items, out)
	}
	result := EntitiesListOutput{Items: items}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		result.NextCursor = &cursor
		result.Truncated = true
	}
	return result, nil
}

// MCPEntitiesGet implements entities.get. It always answers with the
// row's fields: a caller asking for one entity by name is asking for its
// content, which is the opposite of the listing's default.
func MCPEntitiesGet(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in EntitiesGetInput) (EntityOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return EntityOutput{}, err
	}
	row, err := deps.Metamodel.EntityByKey(ctx, projectID, in.TypeKey, in.Key)
	if err != nil {
		return EntityOutput{}, err
	}
	names, err := entityTypeKeys(ctx, deps, projectID)
	if err != nil {
		return EntityOutput{}, err
	}
	return entityOf(row, names, true)
}

// MCPEntitiesRemove implements entities.remove.
func MCPEntitiesRemove(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in EntitiesRemoveInput) (RemovedOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return RemovedOutput{}, err
	}
	id, err := parseID("id", in.ID)
	if err != nil {
		return RemovedOutput{}, err
	}
	if err := deps.Metamodel.RemoveEntity(ctx, projectID, id); err != nil {
		return RemovedOutput{}, err
	}
	return RemovedOutput{Removed: true}, nil
}

// MCPRelationsUpsert implements relations.upsert.
func MCPRelationsUpsert(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in RelationsUpsertInput) (RelationsUpsertOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return RelationsUpsertOutput{}, err
	}
	actor := actorOf(caller)
	items := make([]metamodel.RelationInput, 0, len(in.Items))
	for _, item := range in.Items {
		items = append(items, metamodel.RelationInput{
			TypeKey: item.TypeKey,
			Source:  metamodel.Ref{TypeKey: item.Source.TypeKey, Key: item.Source.Key},
			Target:  metamodel.Ref{TypeKey: item.Target.TypeKey, Key: item.Target.Key},
			Fields:  item.Fields,
			Actor:   actor,
		})
	}
	result, err := deps.Metamodel.UpsertRelations(ctx, projectID, items, metamodel.BulkMode(in.Mode))
	if err != nil {
		return RelationsUpsertOutput{}, err
	}
	out := RelationsUpsertOutput{
		Count:   len(result.Written),
		Written: result.Written,
		Failed:  result.Failed,
	}
	if out.Written == nil {
		out.Written = []metamodel.RelationWrite{}
	}
	if out.Failed == nil {
		out.Failed = []metamodel.BulkFailure{}
	}
	return out, nil
}

// MCPRelationsList implements relations.list.
func MCPRelationsList(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in RelationsListInput) (RelationsListOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return RelationsListOutput{}, err
	}
	filter := metamodel.RelationFilter{
		TypeKey: in.TypeKey,
		Cursor:  in.Cursor,
		Limit:   in.Limit,
	}
	if in.SourceID != "" {
		id, err := parseID("source_id", in.SourceID)
		if err != nil {
			return RelationsListOutput{}, err
		}
		filter.SourceID = &id
	}
	if in.TargetID != "" {
		id, err := parseID("target_id", in.TargetID)
		if err != nil {
			return RelationsListOutput{}, err
		}
		filter.TargetID = &id
	}
	page, err := deps.Metamodel.ListRelations(ctx, projectID, filter)
	if err != nil {
		return RelationsListOutput{}, err
	}
	names, err := relationTypeKeys(ctx, deps, projectID)
	if err != nil {
		return RelationsListOutput{}, err
	}
	items := make([]RelationOutput, 0, len(page.Relations))
	for _, row := range page.Relations {
		items = append(items, RelationOutput{
			ID:       row.ID,
			TypeKey:  names[row.RelationTypeID],
			SourceID: row.SourceID,
			TargetID: row.TargetID,
		})
	}
	result := RelationsListOutput{Items: items}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		result.NextCursor = &cursor
		result.Truncated = true
	}
	return result, nil
}

// MCPRelationsRemove implements relations.remove.
func MCPRelationsRemove(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in RelationsRemoveInput) (RemovedOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return RemovedOutput{}, err
	}
	id, err := parseID("id", in.ID)
	if err != nil {
		return RemovedOutput{}, err
	}
	if err := deps.Metamodel.RemoveRelation(ctx, projectID, id); err != nil {
		return RemovedOutput{}, err
	}
	return RemovedOutput{Removed: true}, nil
}

// MCPSearch implements search. The hits carry their fields: a search is
// a caller looking for content, and a hit it then has to fetch one by
// one is a round trip per row.
func MCPSearch(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in SearchInput) (SearchOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return SearchOutput{}, err
	}
	rows, err := deps.Metamodel.Search(ctx, projectID, in.Query, in.TypeKey, in.Limit)
	if err != nil {
		return SearchOutput{}, err
	}
	names, err := entityTypeKeys(ctx, deps, projectID)
	if err != nil {
		return SearchOutput{}, err
	}
	items := make([]SearchHit, 0, len(rows))
	for _, row := range rows {
		entity, err := entityOf(dbq.Entity{
			ID: row.ID, ProjectID: row.ProjectID, EntityTypeID: row.EntityTypeID,
			Key: row.Key, Name: row.Name, Fields: row.Fields,
			Invalid: row.Invalid, Version: row.Version,
		}, names, true)
		if err != nil {
			return SearchOutput{}, err
		}
		items = append(items, SearchHit{EntityOutput: entity, NameMatch: row.NameMatch, Rank: row.Rank})
	}
	// The only thing this answer can honestly say about completeness:
	// the ranking was cut at the limit, so there may be more below it.
	// See SearchOutput. The comparison is done in int rather than int32
	// so no conversion is needed — SearchLimit's answer is bounded by
	// metamodel.MaxSearchLimit and cannot overflow either way.
	return SearchOutput{
		Items:     items,
		Truncated: len(items) == int(metamodel.SearchLimit(in.Limit)),
	}, nil
}

// --- Conversion helpers ---

// actorOf maps an authenticated caller onto the audit columns. It is the
// only place an Actor is ever built on this surface, and it reads
// nothing the caller sent: see this file's header.
func actorOf(caller Caller) metamodel.Actor {
	userID := caller.UserID
	return metamodel.Actor{UserID: &userID, TokenID: caller.TokenID}
}

// parseID turns a caller-supplied id into a uuid, reporting a malformed
// one as that argument's own problem rather than as a server fault. The
// path is the argument's name, which is the vocabulary every other
// caller-argument refusal in this codebase uses.
func parseID(path, raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, invalidInput(path, "is not a valid uuid")
	}
	return id, nil
}

// parseIDs is parseID over a list, reporting the element's own index so
// a caller sending twenty ids is told which one is wrong.
func parseIDs(path string, raw []string) ([]uuid.UUID, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, 0, len(raw))
	for i, value := range raw {
		id, err := uuid.Parse(value)
		if err != nil {
			return nil, invalidInput(fmt.Sprintf("%s[%d]", path, i), "is not a valid uuid")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// invalidInput builds the refusal this file gives for a caller's own
// argument. It is an *MCPError rather than a metamodel.ValidationError
// because the fault is in this layer's own parsing, not in anything the
// domain was ever shown — but it carries the same code and the same
// path-and-message details shape, so an agent sees one vocabulary
// whichever layer refused it.
func invalidInput(path, message string) error {
	return &MCPError{
		Code:    "invalid_input",
		Message: path + " " + message,
		Details: map[string]any{"fields": []map[string]string{{"path": path, "message": message}}},
	}
}

// schemaOf converts a wire schema into the domain's, through the
// domain's own JSON decoder.
//
// The round trip is deliberate and is the whole point: metamodel.Field's
// UnmarshalJSON is what decides that a `default` key present with the
// value false is a declared default and an absent one is not, and it
// refuses any key it does not know. Building a metamodel.Field field by
// field here would put a second copy of that rule in this package, to
// drift the first time the schema format grows anything.
func schemaOf(fields []FieldInput) (metamodel.Schema, error) {
	if len(fields) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, invalidInput("field_schema", "could not be encoded: "+err.Error())
	}
	schema, err := metamodel.ParseSchema(raw)
	if err != nil {
		return nil, err
	}
	return schema, nil
}

// typeOf and typeDetailOf build an entity type's two answer shapes.
func typeOf(row dbq.EntityType) TypeOutput {
	return TypeOutput{
		ID: row.ID, Key: row.Key, Label: row.Label,
		LabelPlural: row.LabelPlural, Version: row.Version,
	}
}

func typeDetailOf(row dbq.EntityType) (TypeDetailOutput, error) {
	schema, err := metamodel.ParseSchema(row.FieldSchema)
	if err != nil {
		return TypeDetailOutput{}, fmt.Errorf("decode stored field schema: %w", err)
	}
	if schema == nil {
		schema = metamodel.Schema{}
	}
	return TypeDetailOutput{
		TypeOutput:  typeOf(row),
		Description: row.Description,
		Color:       row.Color,
		Icon:        row.Icon,
		Schema:      schema,
	}, nil
}

func relationTypeOf(row dbq.RelationType) RelationTypeOutput {
	return RelationTypeOutput{ID: row.ID, Key: row.Key, Label: row.Label, Version: row.Version}
}

func relationTypeDetailOf(row dbq.RelationType) (RelationTypeDetailOutput, error) {
	schema, err := metamodel.ParseSchema(row.FieldSchema)
	if err != nil {
		return RelationTypeDetailOutput{}, fmt.Errorf("decode stored field schema: %w", err)
	}
	if schema == nil {
		schema = metamodel.Schema{}
	}
	out := RelationTypeDetailOutput{
		RelationTypeOutput: relationTypeOf(row),
		Description:        row.Description,
		SourceTypeIDs:      row.SourceTypeIds,
		TargetTypeIDs:      row.TargetTypeIds,
		Schema:             schema,
	}
	if out.SourceTypeIDs == nil {
		out.SourceTypeIDs = []uuid.UUID{}
	}
	if out.TargetTypeIDs == nil {
		out.TargetTypeIDs = []uuid.UUID{}
	}
	if row.SemanticRole != nil {
		out.SemanticRole = *row.SemanticRole
	}
	return out, nil
}

// entityOf builds one entity's answer. names is the id-to-key map
// entityTypeKeys built for this game; a row whose type is missing from
// it is impossible (an entity's entity_type_id is a foreign key into the
// same game's types) and answers with an empty type key rather than a
// panic if it ever happens.
func entityOf(row dbq.Entity, names map[uuid.UUID]string, verbose bool) (EntityOutput, error) {
	out := EntityOutput{
		ID:      row.ID,
		TypeKey: names[row.EntityTypeID],
		Key:     row.Key,
		Name:    row.Name,
		Version: row.Version,
		Invalid: row.Invalid,
	}
	if !verbose {
		return out, nil
	}
	fields := map[string]any{}
	if len(row.Fields) > 0 {
		if err := json.Unmarshal(row.Fields, &fields); err != nil {
			return EntityOutput{}, fmt.Errorf("decode stored fields: %w", err)
		}
	}
	out.Fields = fields
	return out, nil
}

// entityTypeKeys and relationTypeKeys map a game's type ids onto the
// keys an agent addresses them by.
//
// One extra query per listing, and it is worth it: without it every row
// of a mixed listing comes back carrying a uuid an agent has no way to
// interpret, and the recovery is the same query with an extra round
// trip. A game's type vocabulary is a handful of rows written by hand,
// not a table that grows with content, so this is a small, bounded read
// and not a second listing hiding inside the first.
func entityTypeKeys(ctx context.Context, deps MCPDeps, projectID uuid.UUID) (map[uuid.UUID]string, error) {
	rows, err := deps.Metamodel.ListEntityTypes(ctx, projectID)
	if err != nil {
		return nil, err
	}
	names := make(map[uuid.UUID]string, len(rows))
	for _, row := range rows {
		names[row.ID] = row.Key
	}
	return names, nil
}

func relationTypeKeys(ctx context.Context, deps MCPDeps, projectID uuid.UUID) (map[uuid.UUID]string, error) {
	rows, err := deps.Metamodel.ListRelationTypes(ctx, projectID)
	if err != nil {
		return nil, err
	}
	names := make(map[uuid.UUID]string, len(rows))
	for _, row := range rows {
		names[row.ID] = row.Key
	}
	return names, nil
}

// --- Registration ---

// addMetamodelTools registers the game-content tools on srv.
//
// Every one goes through addScopedTool, which is what makes the caller's
// game binding the only scope any of them can act in — see that
// function's own doc comment and
// TestEveryMCPToolGoesThroughAddScopedTool, which fails the build if a
// tool ever reaches the served list any other way.
//
// **The descriptions are built with the domain's own constants
// interpolated, never with the numbers typed out.** A bound an agent
// reads in a description and a bound the server enforces have to be the
// same number, and the only way to guarantee that is for there to be one
// of them: metamodel exports MaxSearchQuery, MaxIndexedText and the page
// bounds precisely so this file can quote them rather than repeat them.
func (s *Server) addMetamodelTools(srv *mcp.Server, deps MCPDeps) {
	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "types.upsert",
		Description: "Declare or update an entity type: a kind of thing this game contains " +
			"(Quest, Zone, Class; Driver, Car, Circuit). Maestro ships no built-in types — " +
			"a game declares its own. field_schema declares the fields every entity of the " +
			"type carries and is what entity values are judged against; changing it re-checks " +
			"every existing entity and marks the ones that no longer fit as invalid rather " +
			"than deleting them. Idempotent by key, so re-running a seed updates in place. " +
			"expected_version is required to update an existing type and must match the " +
			"stored version; on creation there is nothing to match.",
		OutputSchema: typeDetailOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in TypesUpsertInput) (TypeDetailOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPTypesUpsert(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "types.list",
		Description: "List this game's entity types. The answer is slim — id, key, label, " +
			"version — and carries no field schemas; use types.get for one type in full. " +
			"Every type of the game is returned: this listing is not paged, and next_cursor " +
			"is never set.",
		OutputSchema: typesListOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in TypesListInput) (TypesListOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPTypesList(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "types.get",
		Description: "Read one entity type in full, including its field schema and the id " +
			"types.remove and relation_types.upsert address it by. Keys are matched without " +
			"regard to case; an unknown key is not_found.",
		OutputSchema: typeDetailOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in TypesGetInput) (TypeDetailOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPTypesGet(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "types.remove",
		Description: "Remove an entity type, addressed by id (types.get and types.list " +
			"return it). Without cascade, a type that still has entities is refused as " +
			"in_use. With cascade it takes its entities with it, and every edge touching " +
			"one of them goes too. Removing a type also prunes its id out of every relation " +
			"type's endpoint lists, which changes rules other content is judged against.",
		OutputSchema: removedOutputSchema,
		Annotations:  &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: boolPtr(true)},
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in TypesRemoveInput) (RemovedOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPTypesRemove(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "relation_types.upsert",
		Description: fmt.Sprintf(
			"Declare or update a relation type: a kind of directed edge between "+
				"entities (takes_place_in, requires, unlocks, available_to). source_type_ids and "+
				"target_type_ids are entity type ids — from types.get or types.list — and they "+
				"are the rule every edge of this type is checked against; an empty list means "+
				"any type. semantic_role is what a view uses to know what the edge means: it is "+
				"optional, and when given it is one of %s — anything else is invalid_input at "+
				"path `semantic_role`, listing these same values. "+
				"Idempotent by key; expected_version is required to update an existing type.",
			quotedList(metamodel.SemanticRoles)),
		OutputSchema: relationTypeDetailOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in RelationTypesUpsertInput) (RelationTypeDetailOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPRelationTypesUpsert(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "relation_types.list",
		Description: "List this game's relation types, slim — id, key, label, version — and " +
			"without their endpoint rules or field schemas; use relation_types.get for one " +
			"in full. Not paged: every relation type of the game is returned.",
		OutputSchema: relationTypesListOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in RelationTypesListInput) (RelationTypesListOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPRelationTypesList(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "relation_types.get",
		Description: "Read one relation type in full: which entity types may be at each end, " +
			"its semantic role, its field schema, and the id relation_types.remove addresses " +
			"it by. An unknown key is not_found.",
		OutputSchema: relationTypeDetailOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in RelationTypesGetInput) (RelationTypeDetailOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPRelationTypesGet(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "relation_types.remove",
		Description: "Remove a relation type by id. Without cascade, one that still has edges " +
			"is refused as in_use; with cascade every edge of the type goes with it.",
		OutputSchema: removedOutputSchema,
		Annotations:  &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: boolPtr(true)},
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in RelationTypesRemoveInput) (RemovedOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPRelationTypesRemove(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "entities.upsert",
		Description: "Create or update entities. Always a batch — one entity is a batch of " +
			"one — because seeding a game is hundreds of rows. mode is \"partial\" (the " +
			"default: every item is its own transaction, the good rows land and the rest come " +
			"back in failed with their index, key and a code saying how to fix them) or " +
			"\"atomic\" (one transaction; one bad row rolls the whole batch back and nothing " +
			"is reported as done). Anything else is refused rather than read as partial. " +
			"Rows are idempotent by (type_key, key), so re-running a seed updates in place; " +
			"updating an existing entity requires expected_version, which the written entries " +
			"of a previous call carry. written names every row that landed with its id and " +
			"its new version; count is how many. A failure coded \"retryable\" means the " +
			"database refused that item over contention — send it again; if a batch keeps " +
			"producing them, send fewer rows at a time.",
		OutputSchema: entitiesUpsertOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in EntitiesUpsertInput) (EntitiesUpsertOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPEntitiesUpsert(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "entities.list",
		Description: fmt.Sprintf(
			"List a game's entities. Filter by type_key, by invalid (rows whose values no "+
				"longer fit their type's schema), or both. Pass the previous answer's "+
				"next_cursor to get the next page; a cursor belongs to the game and the "+
				"filter it was issued for and is refused against any other. limit defaults "+
				"to %d and is capped at %d — asking for more gets the cap, and asking for "+
				"less than one gets the default rather than an error. "+
				"fields are omitted unless verbose is true. "+
				"related_to turns this into a one-hop traversal: the entities reached from "+
				"(direction \"outgoing\") or reaching (\"incoming\") the entity named by "+
				"entity_type_key and entity_key, over relation_type_key. **direction is "+
				"required and has no default** — absent or unrecognised, it is invalid_input "+
				"at path `related_to.direction`, because half a neighbourhood presented as "+
				"the whole one is a worse answer than a refusal. **The traversal pages "+
				"exactly as the plain listing does**, with the same limit, the same cap and "+
				"the same next_cursor, so a densely connected entity is walked here and not "+
				"somewhere else. %s",
			metamodel.DefaultEntityPage, metamodel.MaxEntityPage, retryAdvice),
		OutputSchema: entitiesListOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in EntitiesListInput) (EntitiesListOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPEntitiesList(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "entities.get",
		Description: "Read one entity by its address — its type's key plus its own key — with " +
			"all of its fields. Keys are matched without regard to case. An unknown type key " +
			"and an unknown entity key are both not_found, and the message says which.",
		OutputSchema: entityOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in EntitiesGetInput) (EntityOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPEntitiesGet(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "entities.remove",
		Description: "Remove one entity, addressed by id (entities.get, entities.list and " +
			"entities.upsert's written entries all return it). Every edge touching it goes " +
			"with it, by cascade, and those edge removals are not announced one by one.",
		OutputSchema: removedOutputSchema,
		Annotations:  &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: boolPtr(true)},
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in EntitiesRemoveInput) (RemovedOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPEntitiesRemove(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "relations.upsert",
		Description: "Create or update edges between entities. A batch, with the same two " +
			"modes and the same failure report entities.upsert has. Each item names its " +
			"relation type by key and both endpoints by (type_key, key); both ends are " +
			"checked against the relation type's declared endpoint lists, and a violation is " +
			"endpoint_type_mismatch naming the end that is wrong. " +
			"**An edge is identified by (relation type, source, target) and has no version**: " +
			"writing one that already exists replaces its fields whole, last writer wins, and " +
			"there is no expected_version to guard it. A game cannot hold two edges of one " +
			"relation type between the same ordered pair — say the second meaning as its own " +
			"relation type, or as a field on the one edge.",
		OutputSchema: relationsUpsertOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in RelationsUpsertInput) (RelationsUpsertOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPRelationsUpsert(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "relations.list",
		Description: fmt.Sprintf(
			"List a game's edges, optionally narrowed by relation type key and by either "+
				"endpoint's entity id. Paged by next_cursor exactly as entities.list is; "+
				"limit defaults to %d and is capped at %d, and a limit below one gets the "+
				"default rather than an error. "+
				"**Endpoints come back as entity ids, not as the (type_key, key) refs they "+
				"were written with**, because that is what the row holds; to walk a game's "+
				"graph in keys, use entities.list with related_to instead. This tool is what "+
				"you want when the answer is about the edges themselves — removing one, or "+
				"reading an edge's own fields, which a traversal over entities never "+
				"returns. %s",
			metamodel.DefaultRelationPage, metamodel.MaxRelationPage, retryAdvice),
		OutputSchema: relationsListOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in RelationsListInput) (RelationsListOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPRelationsList(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "relations.remove",
		Description: "Remove one edge by id (relations.list and relations.upsert's written " +
			"entries return it). The entities it joined are untouched.",
		OutputSchema: removedOutputSchema,
		Annotations:  &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: boolPtr(true)},
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in RelationsRemoveInput) (RemovedOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPRelationsRemove(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "search",
		Description: fmt.Sprintf(
			"Full-text search over a game's entities, across every type unless type_key "+
				"narrows it — an agent looking for a name rarely knows whether the game "+
				"modelled it as a quest, a creature or a place.\n\n"+
				"**Ranking.** Every row whose *name* satisfies the query comes before every "+
				"row that only mentions the words in a field, however often it mentions them "+
				"— that is a guarantee and not a tendency, and each hit's own name_match says "+
				"which group it landed in. `rank` only orders within a group — it does not "+
				"explain the order between the two, and re-sorting by rank alone can undo the "+
				"guarantee. Within each group, rank goes by how many of the query's words a "+
				"row matches and how often. Ties break by name.\n\n"+
				"**What is indexed.** A row's name plus the text its values carry: text, "+
				"longtext, the chosen option of an enum, and the elements of a list<text>. "+
				"Numbers and booleans are not — filter for those with entities.list. Only the "+
				"first %d bytes of one row's flattened text are indexed, so a word deep inside "+
				"a very long lore field is stored and readable but not findable.\n\n"+
				"**Bounds.** The query is at most %d bytes, must be valid UTF-8, must hold no "+
				"control character and must contain at least one letter or digit; each of those "+
				"is invalid_input at path `query` rather than an empty answer.\n\n"+
				"**This is a top-N, not a page.** limit defaults to %d and is capped at %d "+
				"(and a limit below one gets the default rather than an error), "+
				"there is no cursor, and truncated only means the answer filled the limit — "+
				"the recovery for too many hits is a narrower query, not a deeper page.\n\n"+
				"%s",
			metamodel.MaxIndexedText, metamodel.MaxSearchQuery,
			metamodel.DefaultSearchLimit, metamodel.MaxSearchLimit, retryAdvice),
		OutputSchema: searchOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in SearchInput) (SearchOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPSearch(ctx, deps, caller, projectID, in)
	})
}

// retryAdvice is what every *read* tool says about the `retryable`
// code, and it exists because "resend the same call" is not always the
// whole recovery on a read.
//
// `metamodel.IsRetryable` admits 57014, `query_canceled`, which a lock
// wait cancelled by `statement_timeout` raises — contention, and
// resendable — but which an operator's `statement_timeout` also raises
// on a query that is simply too expensive, every single time it is run.
// The code is still right, because it names the recovery the four
// SQLSTATEs share; what a read tool has to add is what to do when that
// recovery keeps failing, which on a read is always available: ask for
// less. `entities.upsert` already carries the write side of the same
// advice (send fewer rows), and this is its read counterpart.
const retryAdvice = "A `retryable` error means the database refused the call and the same call, " +
	"resent unchanged, may succeed. If it keeps coming back, the call is too expensive as " +
	"written rather than unlucky: ask for less — a smaller limit, a narrower filter or query — " +
	"instead of resending it again."

// quotedList spells a domain-owned list of allowed values for a tool
// description, so the description cannot go on offering a value the
// domain stopped accepting — correction 13's rule, applied to a list
// rather than to a number.
func quotedList(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	return strings.Join(quoted, ", ")
}

// boolPtr is what mcp.ToolAnnotations.DestructiveHint takes: a *bool,
// because "not stated" and "stated false" are different claims to a
// client deciding whether a call needs confirmation.
func boolPtr(v bool) *bool { return &v }

// --- Hand-written output schemas ---
//
// Written by hand for the reason mcp.go's own schema block gives: the
// SDK validates a tool's output against its marshalled JSON, and its
// reflection-based inference gets that JSON wrong for any type whose
// marshalling comes from a method — uuid.UUID here, and metamodel.Field,
// whose MarshalJSON emits a `default` key its Go struct tags say is
// absent.

func numberSchema() *jsonschema.Schema { return &jsonschema.Schema{Type: "number"} }
func objectSchema() *jsonschema.Schema { return &jsonschema.Schema{Type: "object"} }

func arrayOf(items *jsonschema.Schema) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "array", Items: items}
}

// listEnvelopeSchema is the shape every list tool on this surface
// answers with, built once so the six of them cannot drift apart.
func listEnvelopeSchema(items *jsonschema.Schema) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:     "object",
		Required: []string{"items", "truncated"},
		Properties: map[string]*jsonschema.Schema{
			"items":       arrayOf(items),
			"next_cursor": stringSchema(),
			"truncated":   boolSchema(),
		},
	}
}

// fieldSchemaItemSchema is one declared field, in the shape
// metamodel.Field actually marshals as — including `default`, which its
// struct tags hide behind a custom marshaller.
var fieldSchemaItemSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"key", "type"},
	Properties: map[string]*jsonschema.Schema{
		"key":      stringSchema(),
		"label":    stringSchema(),
		"type":     stringSchema(),
		"required": boolSchema(),
		"default":  {},
		"options":  arrayOf(stringSchema()),
		"min":      numberSchema(),
		"max":      numberSchema(),
	},
}

var typeOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"id", "key", "label", "label_plural", "version"},
	Properties: map[string]*jsonschema.Schema{
		"id":           stringSchema(),
		"key":          stringSchema(),
		"label":        stringSchema(),
		"label_plural": stringSchema(),
		"version":      {Type: "integer"},
	},
}

var typeDetailOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"id", "key", "label", "label_plural", "version", "description", "color", "icon", "field_schema"},
	Properties: map[string]*jsonschema.Schema{
		"id":           stringSchema(),
		"key":          stringSchema(),
		"label":        stringSchema(),
		"label_plural": stringSchema(),
		"version":      {Type: "integer"},
		"description":  stringSchema(),
		"color":        stringSchema(),
		"icon":         stringSchema(),
		"field_schema": arrayOf(fieldSchemaItemSchema),
	},
}

var typesListOutputSchema = listEnvelopeSchema(typeOutputSchema)

var relationTypeOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"id", "key", "label", "version"},
	Properties: map[string]*jsonschema.Schema{
		"id":      stringSchema(),
		"key":     stringSchema(),
		"label":   stringSchema(),
		"version": {Type: "integer"},
	},
}

var relationTypeDetailOutputSchema = &jsonschema.Schema{
	Type: "object",
	Required: []string{"id", "key", "label", "version", "description",
		"source_type_ids", "target_type_ids", "field_schema"},
	Properties: map[string]*jsonschema.Schema{
		"id":              stringSchema(),
		"key":             stringSchema(),
		"label":           stringSchema(),
		"version":         {Type: "integer"},
		"description":     stringSchema(),
		"source_type_ids": arrayOf(stringSchema()),
		"target_type_ids": arrayOf(stringSchema()),
		"semantic_role":   stringSchema(),
		"field_schema":    arrayOf(fieldSchemaItemSchema),
	},
}

var relationTypesListOutputSchema = listEnvelopeSchema(relationTypeOutputSchema)

var entityOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"id", "type_key", "key", "name", "version", "invalid"},
	Properties: map[string]*jsonschema.Schema{
		"id":       stringSchema(),
		"type_key": stringSchema(),
		"key":      stringSchema(),
		"name":     stringSchema(),
		"version":  {Type: "integer"},
		"invalid":  boolSchema(),
		"fields":   objectSchema(),
	},
}

var entitiesListOutputSchema = listEnvelopeSchema(entityOutputSchema)

var searchHitOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"id", "type_key", "key", "name", "version", "invalid", "name_match", "rank"},
	Properties: map[string]*jsonschema.Schema{
		"id":         stringSchema(),
		"type_key":   stringSchema(),
		"key":        stringSchema(),
		"name":       stringSchema(),
		"version":    {Type: "integer"},
		"invalid":    boolSchema(),
		"fields":     objectSchema(),
		"name_match": boolSchema(),
		"rank":       numberSchema(),
	},
}

// searchOutputSchema is deliberately not listEnvelopeSchema: this answer
// has no next_cursor and never will, for the reason SearchOutput records,
// and a schema advertising one would invite a client to look for it.
var searchOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"items", "truncated"},
	Properties: map[string]*jsonschema.Schema{
		"items":     arrayOf(searchHitOutputSchema),
		"truncated": boolSchema(),
	},
}

var relationOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"id", "type_key", "source_id", "target_id"},
	Properties: map[string]*jsonschema.Schema{
		"id":        stringSchema(),
		"type_key":  stringSchema(),
		"source_id": stringSchema(),
		"target_id": stringSchema(),
	},
}

var relationsListOutputSchema = listEnvelopeSchema(relationOutputSchema)

// bulkFailureSchema is metamodel.BulkFailure's wire shape, shared by both
// batch answers.
var bulkFailureSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"index", "key", "code", "message"},
	Properties: map[string]*jsonschema.Schema{
		"index":   {Type: "integer"},
		"key":     stringSchema(),
		"code":    stringSchema(),
		"message": stringSchema(),
	},
}

var entitiesUpsertOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"count", "written", "failed"},
	Properties: map[string]*jsonschema.Schema{
		"count": {Type: "integer"},
		"written": arrayOf(&jsonschema.Schema{
			Type:     "object",
			Required: []string{"type_key", "key", "id", "version"},
			Properties: map[string]*jsonschema.Schema{
				"type_key": stringSchema(),
				"key":      stringSchema(),
				"id":       stringSchema(),
				"version":  {Type: "integer"},
			},
		}),
		"failed": arrayOf(bulkFailureSchema),
	},
}

var relationsUpsertOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"count", "written", "failed"},
	Properties: map[string]*jsonschema.Schema{
		"count": {Type: "integer"},
		"written": arrayOf(&jsonschema.Schema{
			Type:     "object",
			Required: []string{"type_key", "id", "source_id", "target_id"},
			Properties: map[string]*jsonschema.Schema{
				"type_key":  stringSchema(),
				"id":        stringSchema(),
				"source_id": stringSchema(),
				"target_id": stringSchema(),
			},
		}),
		"failed": arrayOf(bulkFailureSchema),
	},
}

var removedOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"removed"},
	Properties: map[string]*jsonschema.Schema{
		"removed": boolSchema(),
	},
}
