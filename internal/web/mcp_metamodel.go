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
	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/views"
)

// This file is the game-content half of the MCP surface: the tools an
// agent uses to declare a game's vocabulary (entity types and relation
// types) and to fill it (entities, relations), plus the two ways of
// getting rows back out (listing with a cursor, and search).
//
// **Every exported MCP* function here starts with requireScope, and
// every one of them delegates to an unexported core of the same name.**
// The check is not belt and braces over addScopedTool's own: these
// functions are exported and tested directly, without the wire wrapper,
// precisely so the isolation invariant is pinned here too — and the
// query layer underneath takes the project id as a parameter and filters
// on it in SQL, which is where the invariant is actually enforced (see
// the metamodel plan's Task 7 requirement). A handler that forgot to
// pass a project id would not compile; a handler that merely forgot to
// *check* one would.
//
// The unexported cores exist for Task 8's REST mirror
// (api_metamodel.go), and the split is exactly where the two surfaces
// differ and nowhere else. requireScope asks one question — "is this
// *token* bound to this game" — and a session caller has no binding to
// check, so it refuses every human by construction. What stands in its
// place on the REST side is requireProject, which resolves the game from
// the URL and the caller's membership in it before a handler runs, and
// registerProjectRoute plus TestEveryGameScopedRouteGoesThroughRequireProject
// are what make that unskippable. So: one implementation of every tool,
// two admission checks, each asking the question its own credential can
// answer. A core must never be called from anywhere that has not already
// resolved a scope, which is why none of the sixteen is exported.
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
// TypesRemoveInput removes one entity type by key.
//
// **It took a uuid until Metamodel 14.** See EntitiesRemoveInput for the
// argument; this tool was not among the three Task 9's seeding run
// named, which is exactly why it is here — a rule applied to the tools a
// finding happened to list, and not to the one it missed, is this
// repository's most repeated defect.
type TypesRemoveInput struct {
	ScopedArgs
	Key     string `json:"key"`
	Cascade bool   `json:"cascade,omitempty"`
}

// TypesRenameInput changes one entity type's key, and
// RelationTypesRenameInput does the same for a relation type.
//
// **Addressed by the old key, not by an id**, which is the same
// addressing decision Metamodel 14 made for every removal on this
// surface: keys replace ids here rather than sitting beside them, and
// `from` plus `to` says the whole operation in its own arguments.
//
// ExpectedVersion is required and is a plain requirement rather than an
// optional guard: a rename advances the version, so an unguarded one
// would land on top of an edit the caller never read. It is a pointer
// because absent and zero are different things to say, and absent is
// invalid_input at its own path rather than a guess.
type TypesRenameInput struct {
	ScopedArgs
	From            string `json:"from"`
	To              string `json:"to"`
	ExpectedVersion *int32 `json:"expected_version"`
}

// RelationTypesRenameInput is TypesRenameInput for a relation type. It
// is a separate type rather than the same struct reused, for the reason
// DocsLinkAddInput and DocsLinkRemoveInput are separate: the SDK reports
// a validation failure against the input schema's *name*, and one name
// covering two tools tells an agent which shape was wrong but not which
// call.
type RelationTypesRenameInput struct {
	ScopedArgs
	From            string `json:"from"`
	To              string `json:"to"`
	ExpectedVersion *int32 `json:"expected_version"`
}

// RelationTypesUpsertInput is the argument shape of
// relation_types.upsert.
//
// **The two endpoint lists are entity type keys**, and they were entity
// type *ids* until Metamodel 14. Task 9's seeding run found what that
// cost: a second session — one that did not itself declare the types and
// so never saw their ids — had to call types.list and build a
// key-to-id map by hand before it could declare or edit a single
// relation type. The ids were in the database the whole time; the wire
// is where the translation belongs, and UpsertRelationType now does it
// inside the same transaction and under the same lock the endpoint check
// already needed.
//
// **Keys replace ids here rather than being accepted beside them.** The
// argument is in the tool description and in the plan; the short of it
// is that a key is not a nickname for an id in this game — it is unique
// per game and the address every other tool on this surface speaks — so
// a second spelling would buy a caller nothing and cost every reader of
// this struct a decision.
//
// **The argument used to say "immutable" and no longer can**:
// relation_types.rename moves a type's key. It survives the loss, and
// this is where to say why. The stored column is uuid[], so a renamed
// endpoint type keeps satisfying every rule that names it; what a
// rename changes is the *spelling* a caller reads back here, which is
// the same spelling it would read from relation_types.get. A key is
// still a total replacement for an id on this surface — there is
// nothing an id can address that a key cannot — and it is now a
// spelling that can move, which is exactly why the answer states the
// endpoint rules as keys rather than expecting a caller to have cached
// them.
//
// The stored column is still uuid[], which does not change and should
// not: an id is what the entity type removal's prune can remove from an
// endpoint list, and nothing but a deletion can invalidate it.
type RelationTypesUpsertInput struct {
	ScopedArgs
	Key             string       `json:"key"`
	Label           string       `json:"label"`
	Description     string       `json:"description,omitempty"`
	SourceTypeKeys  []string     `json:"source_type_keys,omitempty"`
	TargetTypeKeys  []string     `json:"target_type_keys,omitempty"`
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
// RelationTypesRemoveInput removes one relation type by key; see
// TypesRemoveInput.
type RelationTypesRemoveInput struct {
	ScopedArgs
	Key     string `json:"key"`
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

// EntitiesRemoveInput removes one entity by the address it was written
// under. Its edges go with it, by cascade.
//
// **It took a uuid until Metamodel 14, and keys *replace* ids here
// rather than being accepted beside them.** Task 9's seeding run
// measured the cost of the old shape: an agent thinks in keys — they are
// what the game's own vocabulary is written in, what every other tool
// speaks and what every refusal quotes back — so removing a row it had
// just written cost it a resolving read first.
//
// Accepting both was considered and rejected. A key here is not a
// nickname for an id: it is stable (the first spelling stored stands, a
// respelling is refused, and only an explicit types.rename moves it),
// unique per game and type, and therefore a *total* replacement rather
// than a convenience —
// there is nothing an id can address that a key cannot. What a second
// spelling would cost is real and paid at every call site: an
// "exactly one of" refusal path, two branches in every description, and
// a caller decision at each call. This repository already pays that
// where the two spellings are two genuinely different questions —
// views.run takes a saved key *or* an inline query and refuses both, and
// docs.links.list reads the join from either side and refuses neither
// and both — and neither of those is two names for one row. It also has
// one open inconsistency of exactly this shape already, routes
// addressing a game by uuid while the page addresses it by slug, and
// adding a second would make that worse rather than better.
//
// The ids have not gone anywhere: every reader still returns them and
// every removal event still carries them. They are simply no longer the
// address.
type EntitiesRemoveInput struct {
	ScopedArgs
	TypeKey string `json:"type_key"`
	Key     string `json:"key"`
}

// RelationsUpsertInput is the argument shape of relations.upsert.
type RelationsUpsertInput struct {
	ScopedArgs
	Mode  string              `json:"mode,omitempty"`
	Items []RelationItemInput `json:"items"`
}

// RelationItemInput is one edge of a relations.upsert batch. It carries
// no actor: see this file's header.
//
// **expected_version is here for the reason EntityItemInput's is**, and
// it did not exist until 0009 gave edges a version column: updating an
// existing edge means claiming the revision being updated, so a blind
// rewrite of a row somebody else has edited is refused instead of losing
// their work. The written entries of a previous call carry the number to
// send next.
type RelationItemInput struct {
	TypeKey         string         `json:"type_key"`
	Source          RefInput       `json:"source"`
	Target          RefInput       `json:"target"`
	Fields          map[string]any `json:"fields,omitempty"`
	ExpectedVersion *int32         `json:"expected_version,omitempty"`
}

// RefInput addresses an entity the way an agent thinks of it.
type RefInput struct {
	TypeKey string `json:"type_key"`
	Key     string `json:"key"`
}

// RelationsListInput is the argument shape of relations.list.
//
// Verbose is off by default, the same rule EntitiesListInput states and
// for a stronger version of the same reason: a graph has more edges than
// nodes, so a page of five hundred edges carrying their fields is more
// of the game back in one answer than the listing that rule was written
// for. An agent walking a game's graph wants what each edge joins; it
// asks for the values when it means to read them, and when it wants one
// edge's values it has relations.get, which never needs the flag.
// Invalid is EntitiesListInput.Invalid for edges, and it is spelled the
// same because it is the same question: an agent that has just edited a
// relation type's field_schema asks which of its edges that broke.
type RelationsListInput struct {
	ScopedArgs
	TypeKey string    `json:"type_key,omitempty"`
	Invalid *bool     `json:"invalid,omitempty"`
	Source  *RefInput `json:"source,omitempty"`
	Target  *RefInput `json:"target,omitempty"`
	Cursor  string    `json:"cursor,omitempty"`
	Limit   int32     `json:"limit,omitempty"`
	Verbose bool      `json:"verbose,omitempty"`
}

// RelationsGetInput reads one edge by the address it was written under:
// the relation type's key and both endpoints as (type_key, key) refs.
//
// It is deliberately the same address relations.upsert takes, and not
// the endpoint *ids* relations.list filters on. An agent that has just
// written an edge holds the three strings, not the ids; the rest of this
// surface addresses a row the way a designer names it (entities.get,
// relation_types.get), and Task 9 recorded the resolving read a
// by-id-only address costs. The ids remain in the answer, where a
// removal still needs them.
type RelationsGetInput struct {
	ScopedArgs
	TypeKey string   `json:"type_key"`
	Source  RefInput `json:"source"`
	Target  RefInput `json:"target"`
}

// RelationsRemoveInput removes one edge by id.
// RelationsRemoveInput removes one edge by the address it was written
// under: the relation type's key and both endpoints as (type_key, key)
// refs. It is deliberately the same address relations.upsert writes with
// and relations.get reads by; see EntitiesRemoveInput for the argument.
type RelationsRemoveInput struct {
	ScopedArgs
	TypeKey string   `json:"type_key"`
	Source  RefInput `json:"source"`
	Target  RefInput `json:"target"`
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
	Description    string           `json:"description"`
	SourceTypeKeys []string         `json:"source_type_keys"`
	TargetTypeKeys []string         `json:"target_type_keys"`
	SemanticRole   string           `json:"semantic_role,omitempty"`
	Schema         metamodel.Schema `json:"field_schema"`
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

// RefOutput is one endpoint of an edge, in the terms it was written
// with: the entity's type key, its own key, and its name.
//
// Name is here as well as the two keys because every caller that
// resolves an endpoint is about to show or log it, and the entity read
// that produced the keys already carried the name — omitting it would
// buy nothing and cost a round trip per endpoint.
type RefOutput struct {
	TypeKey string `json:"type_key"`
	Key     string `json:"key"`
	Name    string `json:"name"`
}

// RelationOutput is one edge.
//
// It carries its endpoints twice, and both are load-bearing: the ids are
// what the row holds and what relations.remove and the two endpoint
// filters address entities by, and Source/Target are the (type key, key)
// refs the edge was actually written with. Task 7 shipped this answer
// with the ids alone, honestly documented, because resolving a page of
// edges needed a bulk entity-by-ids read that did not exist;
// metamodel.EntitiesByIDs is that read, and one query per page is what
// it costs.
//
// Source and Target are pointers, and a nil one is not an error: the
// endpoint read happens after the page was listed, so an entity removed
// in between (which takes its edges with it, by cascade) leaves an edge
// in hand whose endpoint no longer exists. The id is still reported;
// the ref is simply absent, which is the honest answer rather than a
// ref with empty strings in it that a client would render as a row
// named "".
//
// **Fields is the edge's own declared values**, and its absence was the
// defect Metamodel 12 closed. A relation type may declare a field
// schema, relations.upsert validates an edge's values against it and
// stores them, and until this task nothing on either surface returned
// them: a whole declared feature was write-only, and the readme's own
// example of why typed edges exist — a door declaring which ability
// opens it — could be written and never shown. relation_types.get is
// where the schema those values answer to is published.
//
// It is present only when the caller asked to be verbose, exactly as
// EntityOutput.Fields is; relations.get always fills it, because a
// caller naming one edge is asking for its content. `omitempty` is
// load-bearing on both: a client must be able to tell "not asked for"
// from "asked for and empty".
// **Version and Invalid are here because EntityOutput carries them**, and
// they are not optional on either: an agent cannot send an
// expected_version it was never told, and a flag it cannot see is a flag
// it cannot act on. Both are unconditional, listing and get alike, and
// neither is behind verbose — verbose gates the edge's own content, not
// its identity or its state.
type RelationOutput struct {
	ID       uuid.UUID      `json:"id"`
	TypeKey  string         `json:"type_key"`
	SourceID uuid.UUID      `json:"source_id"`
	TargetID uuid.UUID      `json:"target_id"`
	Version  int32          `json:"version"`
	Invalid  bool           `json:"invalid"`
	Source   *RefOutput     `json:"source,omitempty"`
	Target   *RefOutput     `json:"target,omitempty"`
	Fields   map[string]any `json:"fields,omitempty"`
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

// EntitiesRepairInput is the argument shape of entities.repair, and
// RelationsRepairInput is the same shape for edges.
//
// **A repair is not an editor**, and the arguments are what make that
// true rather than a promise in prose. There is no per-row list: one
// `set` and one `drop_unknown` cover every row the type's current schema
// rejects, because a repair is one decision about what a newly required
// field means. There is no key, no name, no endpoint and no
// expected_version, because a repair writes the row it read with its
// values changed and nothing else. Per-row values, with the version
// claim that belongs to editing content, are entities.upsert.
//
// metamodel/repair.go's header carries the whole of what a repair may
// and may not do, and why each restriction is enforced by a mechanism
// rather than by intention.
type EntitiesRepairInput struct {
	ScopedArgs
	TypeKey     string         `json:"type_key"`
	Set         map[string]any `json:"set,omitempty"`
	DropUnknown bool           `json:"drop_unknown,omitempty"`
	Limit       int32          `json:"limit,omitempty"`
}

// RelationsRepairInput is EntitiesRepairInput for edges. It is a second
// type rather than a shared one because the SDK infers each tool's input
// schema by reflection from its own struct, and type_key means an entity
// type in one and a relation type in the other.
type RelationsRepairInput struct {
	ScopedArgs
	TypeKey     string         `json:"type_key"`
	Set         map[string]any `json:"set,omitempty"`
	DropUnknown bool           `json:"drop_unknown,omitempty"`
	Limit       int32          `json:"limit,omitempty"`
}

// EntitiesRepairOutput reports one repair pass.
//
// Scanned is how many flagged rows the pass read, and is what tells a
// caller whether the limit was the binding constraint — the same job
// search's `truncated` does, stated as a number because a repair loop
// wants one. Repaired and Failed are the batch's own report, so a row
// that still does not fit comes back with the code entities.upsert
// would have given it.
type EntitiesRepairOutput struct {
	Scanned  int                     `json:"scanned"`
	Repaired []metamodel.BulkWrite   `json:"repaired"`
	Failed   []metamodel.BulkFailure `json:"failed"`
}

// RelationsRepairOutput is EntitiesRepairOutput for edges; an edge has
// no key, so it names what it repaired the way relations.upsert does.
type RelationsRepairOutput struct {
	Scanned  int                       `json:"scanned"`
	Repaired []metamodel.RelationWrite `json:"repaired"`
	Failed   []metamodel.BulkFailure   `json:"failed"`
}

// RemovedOutput is what every removal answers with. A removal has
// nothing to return but the fact that it happened, and a tool that
// returned nothing at all would have no structured content for a client
// to distinguish from an error it failed to parse.
type RemovedOutput struct {
	Removed bool `json:"removed"`
}

// BrokenViewOutput is one saved view a type removal broke, at the
// pointer in its own query document that names the type.
//
// **One row per reference and not per view**: a view naming a type at
// three positions comes back three times, because the pointers are the
// repair and a designer holding the name three times has learned
// nothing. A caller counting rows is counting positions, not views.
type BrokenViewOutput struct {
	ViewKey string `json:"view_key"`
	Name    string `json:"name"`
	Key     string `json:"key"`
	Pointer string `json:"pointer"`
}

// TypeRemovedOutput is what removing an entity type or a relation type
// answers with: the fact of the removal, and the saved views that
// referenced the type.
//
// **Deleting a type views depend on is allowed and the report is the
// courtesy that makes it survivable.** The design spec is explicit that
// refusing with in_use is the wrong call — a view is derived and can be
// rewritten in one call, while making a type undeletable because a
// six-month-old diagram mentions it pushes designers into deleting views
// in order to delete types. What they are owed instead is the list, with
// the pointer into each document, so the repair is a known amount of
// work rather than a surprise the next time a view is opened.
//
// broke_views is `[]` and never null, the rule this surface applies to
// every list it hands back: a client reading "nothing broke" must not
// have two spellings of it to handle.
type TypeRemovedOutput struct {
	Removed    bool               `json:"removed"`
	BrokeViews []BrokenViewOutput `json:"broke_views"`
}

// removeTypeReportingViews is the one place a type is removed on this
// surface, for either kind.
//
// The list has to be read *before* the removal and inside its
// transaction: view_refs' foreign key is ON DELETE SET NULL, so the same
// deletion that lets a ref row outlive its type empties the column the
// lookup matches on, and asking afterwards finds nothing and reports
// that nothing broke — a wrong answer rather than an error. That
// ordering lives in views.RemoveTypeReportingViews, which is why this
// goes through it rather than reading the list here and then calling the
// metamodel.
//
// **The nil-Views branch is a build without a views service, not a
// shortcut.** MCPDeps.Views is optional — a server built without one
// registers no views tools at all — and there a removal can break no
// view, so the report is empty by construction rather than by omission.
func removeTypeReportingViews(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	kind string, id uuid.UUID, cascade bool) (TypeRemovedOutput, error) {
	out := TypeRemovedOutput{BrokeViews: []BrokenViewOutput{}}
	if deps.Views == nil {
		var err error
		switch kind {
		case views.KindEntityType:
			err = deps.Metamodel.RemoveEntityType(ctx, projectID, id, cascade)
		default:
			err = deps.Metamodel.RemoveRelationType(ctx, projectID, id, cascade)
		}
		if err != nil {
			return TypeRemovedOutput{}, err
		}
		out.Removed = true
		return out, nil
	}
	broke, err := deps.Views.RemoveTypeReportingViews(ctx, projectID, kind, id, cascade)
	if err != nil {
		return TypeRemovedOutput{}, err
	}
	for _, dep := range broke {
		out.BrokeViews = append(out.BrokeViews, BrokenViewOutput{
			ViewKey: dep.ViewKey, Name: dep.Name, Key: dep.RefKey, Pointer: dep.Pointer,
		})
	}
	out.Removed = true
	return out, nil
}

// --- Tool implementations ---

// GameCountsInput is the argument shape of games.counts. It takes
// nothing but the scope every tool takes: a game counts itself whole,
// and there is no filter that would make the answer smaller — it is one
// row per declared type, which is a handful of hand-written rows however
// much content the game holds.
type GameCountsInput struct{ ScopedArgs }

// MCPGameCounts implements games.counts.
func MCPGameCounts(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, _ GameCountsInput) (GameCountsOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return GameCountsOutput{}, err
	}
	return gameCounts(ctx, deps, projectID)
}

// MCPTypesUpsert implements types.upsert.
func MCPTypesUpsert(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in TypesUpsertInput) (TypeDetailOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return TypeDetailOutput{}, err
	}
	return typesUpsert(ctx, deps, caller, projectID, in)
}

// typesUpsert is MCPTypesUpsert without the token-binding check, for the
// REST mirror (api_metamodel.go), whose caller is a person whose
// standing requireProject already resolved. See this file's header.
func typesUpsert(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in TypesUpsertInput) (TypeDetailOutput, error) {
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
	return typesList(ctx, deps, caller, projectID, TypesListInput{})
}

// typesList is MCPTypesList without the token-binding check, for the
// REST mirror (api_metamodel.go), whose caller is a person whose
// standing requireProject already resolved. See this file's header.
func typesList(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, _ TypesListInput) (TypesListOutput, error) {
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
	return typesGet(ctx, deps, caller, projectID, in)
}

// typesGet is MCPTypesGet without the token-binding check, for the
// REST mirror (api_metamodel.go), whose caller is a person whose
// standing requireProject already resolved. See this file's header.
func typesGet(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in TypesGetInput) (TypeDetailOutput, error) {
	row, err := deps.Metamodel.EntityTypeByKey(ctx, projectID, in.Key)
	if err != nil {
		return TypeDetailOutput{}, err
	}
	return typeDetailOf(row)
}

// MCPTypesRemove implements types.remove.
func MCPTypesRemove(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in TypesRemoveInput) (TypeRemovedOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return TypeRemovedOutput{}, err
	}
	return typesRemove(ctx, deps, caller, projectID, in)
}

// typesRemove is MCPTypesRemove without the token-binding check, for the
// REST mirror (api_metamodel.go), whose caller is a person whose
// standing requireProject already resolved. See this file's header.
func typesRemove(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in TypesRemoveInput) (TypeRemovedOutput, error) {
	// Resolved here rather than in the domain, and this is the one
	// removal where that is right: the views service needs the type's id
	// to find the saved views that reference it, and it takes it *before*
	// the deletion — view_refs' foreign key is ON DELETE SET NULL, so
	// asking afterwards finds nothing. The read is the server's now
	// either way, which is the whole point.
	row, err := deps.Metamodel.EntityTypeByKey(ctx, projectID, in.Key)
	if err != nil {
		return TypeRemovedOutput{}, err
	}
	return removeTypeReportingViews(ctx, deps, projectID, views.KindEntityType, row.ID, in.Cascade)
}

// MCPTypesRename implements types.rename.
func MCPTypesRename(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in TypesRenameInput) (TypeDetailOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return TypeDetailOutput{}, err
	}
	return typesRename(ctx, deps, caller, projectID, in)
}

// typesRename is MCPTypesRename without the token-binding check, for the
// REST mirror (api_metamodel.go), whose caller is a person whose
// standing requireProject already resolved. See this file's header.
func typesRename(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in TypesRenameInput) (TypeDetailOutput, error) {
	row, err := deps.Metamodel.RenameEntityType(ctx, projectID, metamodel.RenameInput{
		From:            in.From,
		To:              in.To,
		ExpectedVersion: in.ExpectedVersion,
		Actor:           actorOf(caller),
	})
	if err != nil {
		return TypeDetailOutput{}, err
	}
	return typeDetailOf(row)
}

// MCPRelationTypesRename implements relation_types.rename.
func MCPRelationTypesRename(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in RelationTypesRenameInput) (RelationTypeDetailOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return RelationTypeDetailOutput{}, err
	}
	return relationTypesRename(ctx, deps, caller, projectID, in)
}

// relationTypesRename is MCPRelationTypesRename without the
// token-binding check, for the REST mirror. Its answer carries the
// endpoint rules as keys, exactly as relation_types.upsert's does, so a
// caller that renamed a type reads back the same shape it would have
// read back from an edit.
func relationTypesRename(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in RelationTypesRenameInput) (RelationTypeDetailOutput, error) {
	row, err := deps.Metamodel.RenameRelationType(ctx, projectID, metamodel.RenameInput{
		From:            in.From,
		To:              in.To,
		ExpectedVersion: in.ExpectedVersion,
		Actor:           actorOf(caller),
	})
	if err != nil {
		return RelationTypeDetailOutput{}, err
	}
	names, err := entityTypeKeys(ctx, deps, projectID)
	if err != nil {
		return RelationTypeDetailOutput{}, err
	}
	return relationTypeDetailOf(row, names)
}

// MCPRelationTypesUpsert implements relation_types.upsert.
func MCPRelationTypesUpsert(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in RelationTypesUpsertInput) (RelationTypeDetailOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return RelationTypeDetailOutput{}, err
	}
	return relationTypesUpsert(ctx, deps, caller, projectID, in)
}

// relationTypesUpsert is MCPRelationTypesUpsert without the token-binding check, for the
// REST mirror (api_metamodel.go), whose caller is a person whose
// standing requireProject already resolved. See this file's header.
func relationTypesUpsert(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in RelationTypesUpsertInput) (RelationTypeDetailOutput, error) {
	schema, err := schemaOf(in.Schema)
	if err != nil {
		return RelationTypeDetailOutput{}, err
	}
	row, err := deps.Metamodel.UpsertRelationType(ctx, projectID, metamodel.RelationTypeInput{
		Key:             in.Key,
		Label:           in.Label,
		Description:     in.Description,
		SourceTypeKeys:  in.SourceTypeKeys,
		TargetTypeKeys:  in.TargetTypeKeys,
		SemanticRole:    in.SemanticRole,
		Schema:          schema,
		ExpectedVersion: in.ExpectedVersion,
		Actor:           actorOf(caller),
	})
	if err != nil {
		return RelationTypeDetailOutput{}, err
	}
	names, err := entityTypeKeys(ctx, deps, projectID)
	if err != nil {
		return RelationTypeDetailOutput{}, err
	}
	return relationTypeDetailOf(row, names)
}

// MCPRelationTypesList implements relation_types.list.
func MCPRelationTypesList(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, _ RelationTypesListInput) (RelationTypesListOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return RelationTypesListOutput{}, err
	}
	return relationTypesList(ctx, deps, caller, projectID, RelationTypesListInput{})
}

// relationTypesList is MCPRelationTypesList without the token-binding check, for the
// REST mirror (api_metamodel.go), whose caller is a person whose
// standing requireProject already resolved. See this file's header.
func relationTypesList(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, _ RelationTypesListInput) (RelationTypesListOutput, error) {
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
	return relationTypesGet(ctx, deps, caller, projectID, in)
}

// relationTypesGet is MCPRelationTypesGet without the token-binding check, for the
// REST mirror (api_metamodel.go), whose caller is a person whose
// standing requireProject already resolved. See this file's header.
func relationTypesGet(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in RelationTypesGetInput) (RelationTypeDetailOutput, error) {
	row, err := deps.Metamodel.RelationTypeByKey(ctx, projectID, in.Key)
	if err != nil {
		return RelationTypeDetailOutput{}, err
	}
	names, err := entityTypeKeys(ctx, deps, projectID)
	if err != nil {
		return RelationTypeDetailOutput{}, err
	}
	return relationTypeDetailOf(row, names)
}

// MCPRelationTypesRemove implements relation_types.remove.
func MCPRelationTypesRemove(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in RelationTypesRemoveInput) (TypeRemovedOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return TypeRemovedOutput{}, err
	}
	return relationTypesRemove(ctx, deps, caller, projectID, in)
}

// relationTypesRemove is MCPRelationTypesRemove without the token-binding check, for the
// REST mirror (api_metamodel.go), whose caller is a person whose
// standing requireProject already resolved. See this file's header.
func relationTypesRemove(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in RelationTypesRemoveInput) (TypeRemovedOutput, error) {
	row, err := deps.Metamodel.RelationTypeByKey(ctx, projectID, in.Key)
	if err != nil {
		return TypeRemovedOutput{}, err
	}
	return removeTypeReportingViews(ctx, deps, projectID, views.KindRelationType, row.ID, in.Cascade)
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
	return entitiesUpsert(ctx, deps, caller, projectID, in)
}

// entitiesUpsert is MCPEntitiesUpsert without the token-binding check, for the
// REST mirror (api_metamodel.go), whose caller is a person whose
// standing requireProject already resolved. See this file's header.
func entitiesUpsert(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in EntitiesUpsertInput) (EntitiesUpsertOutput, error) {
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

// MCPEntitiesRepair implements entities.repair.
func MCPEntitiesRepair(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in EntitiesRepairInput) (EntitiesRepairOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return EntitiesRepairOutput{}, err
	}
	return entitiesRepair(ctx, deps, caller, projectID, in)
}

// entitiesRepair is MCPEntitiesRepair without the token-binding check,
// for the REST mirror (api_metamodel.go), whose caller is a person whose
// standing requireProject already resolved. See this file's header.
func entitiesRepair(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in EntitiesRepairInput) (EntitiesRepairOutput, error) {
	result, err := deps.Metamodel.RepairEntities(ctx, projectID, metamodel.RepairInput{
		TypeKey: in.TypeKey, Set: in.Set, DropUnknown: in.DropUnknown,
		Limit: in.Limit, Actor: actorOf(caller),
	})
	if err != nil {
		return EntitiesRepairOutput{}, err
	}
	// Both slices are arrays even when empty, the rule entitiesUpsert
	// states: "the pass reported no failures" and "the pass reported
	// nothing" must not look the same to a client walking either one.
	out := EntitiesRepairOutput{
		Scanned: result.Scanned, Repaired: result.Repaired, Failed: result.Failed,
	}
	if out.Repaired == nil {
		out.Repaired = []metamodel.BulkWrite{}
	}
	if out.Failed == nil {
		out.Failed = []metamodel.BulkFailure{}
	}
	return out, nil
}

// MCPRelationsRepair implements relations.repair.
func MCPRelationsRepair(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in RelationsRepairInput) (RelationsRepairOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return RelationsRepairOutput{}, err
	}
	return relationsRepair(ctx, deps, caller, projectID, in)
}

// relationsRepair is MCPRelationsRepair without the token-binding check;
// see entitiesRepair.
func relationsRepair(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in RelationsRepairInput) (RelationsRepairOutput, error) {
	result, err := deps.Metamodel.RepairRelations(ctx, projectID, metamodel.RepairInput{
		TypeKey: in.TypeKey, Set: in.Set, DropUnknown: in.DropUnknown,
		Limit: in.Limit, Actor: actorOf(caller),
	})
	if err != nil {
		return RelationsRepairOutput{}, err
	}
	out := RelationsRepairOutput{
		Scanned: result.Scanned, Repaired: result.Repaired, Failed: result.Failed,
	}
	if out.Repaired == nil {
		out.Repaired = []metamodel.RelationWrite{}
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
	return entitiesList(ctx, deps, caller, projectID, in)
}

// entitiesList is MCPEntitiesList without the token-binding check, for the
// REST mirror (api_metamodel.go), whose caller is a person whose
// standing requireProject already resolved. See this file's header.
func entitiesList(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in EntitiesListInput) (EntitiesListOutput, error) {
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
	return entitiesGet(ctx, deps, caller, projectID, in)
}

// entitiesGet is MCPEntitiesGet without the token-binding check, for the
// REST mirror (api_metamodel.go), whose caller is a person whose
// standing requireProject already resolved. See this file's header.
func entitiesGet(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in EntitiesGetInput) (EntityOutput, error) {
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
	return entitiesRemove(ctx, deps, caller, projectID, in)
}

// entitiesRemove is MCPEntitiesRemove without the token-binding check, for the
// REST mirror (api_metamodel.go), whose caller is a person whose
// standing requireProject already resolved. See this file's header.
func entitiesRemove(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in EntitiesRemoveInput) (RemovedOutput, error) {
	if err := deps.Metamodel.RemoveEntity(ctx, projectID, in.TypeKey, in.Key); err != nil {
		return RemovedOutput{}, err
	}
	return RemovedOutput{Removed: true}, nil
}

// MCPRelationsUpsert implements relations.upsert.
func MCPRelationsUpsert(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in RelationsUpsertInput) (RelationsUpsertOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return RelationsUpsertOutput{}, err
	}
	return relationsUpsert(ctx, deps, caller, projectID, in)
}

// relationsUpsert is MCPRelationsUpsert without the token-binding check, for the
// REST mirror (api_metamodel.go), whose caller is a person whose
// standing requireProject already resolved. See this file's header.
func relationsUpsert(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in RelationsUpsertInput) (RelationsUpsertOutput, error) {
	actor := actorOf(caller)
	items := make([]metamodel.RelationInput, 0, len(in.Items))
	for _, item := range in.Items {
		items = append(items, metamodel.RelationInput{
			TypeKey:         item.TypeKey,
			Source:          metamodel.Ref{TypeKey: item.Source.TypeKey, Key: item.Source.Key},
			Target:          metamodel.Ref{TypeKey: item.Target.TypeKey, Key: item.Target.Key},
			Fields:          item.Fields,
			ExpectedVersion: item.ExpectedVersion,
			Actor:           actor,
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
	return relationsList(ctx, deps, caller, projectID, in)
}

// relationsList is MCPRelationsList without the token-binding check, for the
// REST mirror (api_metamodel.go), whose caller is a person whose
// standing requireProject already resolved. See this file's header.
func relationsList(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in RelationsListInput) (RelationsListOutput, error) {
	filter := metamodel.RelationFilter{
		TypeKey: in.TypeKey,
		Invalid: in.Invalid,
		Cursor:  in.Cursor,
		Limit:   in.Limit,
	}
	// The two endpoint filters, as refs. A nil pointer is "no opinion";
	// the domain resolves a given ref to an entity id and answers
	// not_found if it names none, so an empty page never stands in for a
	// mistyped key.
	if in.Source != nil {
		filter.Source = &metamodel.Ref{TypeKey: in.Source.TypeKey, Key: in.Source.Key}
	}
	if in.Target != nil {
		filter.Target = &metamodel.Ref{TypeKey: in.Target.TypeKey, Key: in.Target.Key}
	}
	page, err := deps.Metamodel.ListRelations(ctx, projectID, filter)
	if err != nil {
		return RelationsListOutput{}, err
	}
	names, err := relationTypeKeys(ctx, deps, projectID)
	if err != nil {
		return RelationsListOutput{}, err
	}
	refs, err := endpointRefs(ctx, deps, projectID, page.Relations)
	if err != nil {
		return RelationsListOutput{}, err
	}
	items := make([]RelationOutput, 0, len(page.Relations))
	for _, row := range page.Relations {
		out, err := relationOf(row, names, refs, in.Verbose)
		if err != nil {
			return RelationsListOutput{}, err
		}
		items = append(items, out)
	}
	result := RelationsListOutput{Items: items}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		result.NextCursor = &cursor
		result.Truncated = true
	}
	return result, nil
}

// MCPRelationsGet implements relations.get. It always answers with the
// edge's fields, for the reason entities.get does: a caller naming one
// edge is asking for what it carries, which is the opposite of the
// listing's default.
func MCPRelationsGet(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in RelationsGetInput) (RelationOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return RelationOutput{}, err
	}
	return relationsGet(ctx, deps, caller, projectID, in)
}

// relationsGet is MCPRelationsGet without the token-binding check, for the
// REST mirror (api_metamodel.go), whose caller is a person whose
// standing requireProject already resolved. See this file's header.
func relationsGet(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in RelationsGetInput) (RelationOutput, error) {
	row, err := deps.Metamodel.RelationByEdge(ctx, projectID, in.TypeKey,
		metamodel.Ref{TypeKey: in.Source.TypeKey, Key: in.Source.Key},
		metamodel.Ref{TypeKey: in.Target.TypeKey, Key: in.Target.Key})
	if err != nil {
		return RelationOutput{}, err
	}
	names, err := relationTypeKeys(ctx, deps, projectID)
	if err != nil {
		return RelationOutput{}, err
	}
	// The same endpoint resolution a page gets, over a slice of one: an
	// edge read on its own must not answer in a poorer shape than the
	// same edge read in a listing.
	refs, err := endpointRefs(ctx, deps, projectID, []dbq.Relation{row})
	if err != nil {
		return RelationOutput{}, err
	}
	return relationOf(row, names, refs, true)
}

// MCPRelationsRemove implements relations.remove.
func MCPRelationsRemove(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in RelationsRemoveInput) (RemovedOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return RemovedOutput{}, err
	}
	return relationsRemove(ctx, deps, caller, projectID, in)
}

// relationsRemove is MCPRelationsRemove without the token-binding check, for the
// REST mirror (api_metamodel.go), whose caller is a person whose
// standing requireProject already resolved. See this file's header.
func relationsRemove(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in RelationsRemoveInput) (RemovedOutput, error) {
	err := deps.Metamodel.RemoveRelation(ctx, projectID, in.TypeKey,
		metamodel.Ref{TypeKey: in.Source.TypeKey, Key: in.Source.Key},
		metamodel.Ref{TypeKey: in.Target.TypeKey, Key: in.Target.Key})
	if err != nil {
		return RemovedOutput{}, err
	}
	return RemovedOutput{Removed: true}, nil
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

// relationTypeDetailOf builds one relation type's full answer. names is
// the entity-type id-to-key map entityTypeKeys built for this game, and
// it is what turns the stored endpoint ids back into the keys the input
// speaks — the read half of Metamodel 14's endpoint change, without
// which the tool would take keys and answer with ids.
//
// **An endpoint id missing from names is dropped, not rendered as an
// empty key.** It is reachable: RemoveEntityType prunes a deleted type's
// id out of every endpoint list in the same transaction, so the window
// is small, but a page read against a replica or an id that outlived its
// prune would otherwise produce `""` in a list of keys — a name no type
// has, in a list a caller may send straight back to the writer. Dropping
// it is the same judgement RelationOutput makes about an endpoint whose
// entity is gone: absent, rather than named "".
func relationTypeDetailOf(row dbq.RelationType, names map[uuid.UUID]string) (RelationTypeDetailOutput, error) {
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
		SourceTypeKeys:     endpointKeysOf(row.SourceTypeIds, names),
		TargetTypeKeys:     endpointKeysOf(row.TargetTypeIds, names),
		Schema:             schema,
	}
	if row.SemanticRole != nil {
		out.SemanticRole = *row.SemanticRole
	}
	return out, nil
}

// endpointKeysOf renders one stored endpoint list as keys. Always a
// slice, never nil: an undeclared endpoint rule is `[]`, which is what
// it means — this type accepts any — and a client must not have to tell
// that from a server that said nothing.
func endpointKeysOf(ids []uuid.UUID, names map[uuid.UUID]string) []string {
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		if key, ok := names[id]; ok {
			keys = append(keys, key)
		}
	}
	return keys
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

// relationOf builds one edge's answer: its identity, both endpoints in
// both addressings, and — when the caller asked for them — the values
// the edge itself carries.
//
// It is entityOf's counterpart and exists for the same reason: both
// relations.list and relations.get answer with a RelationOutput, and two
// hand-built copies are two chances for one of them to leave the fields
// out again, which is the whole of Metamodel 12.
//
// An edge whose relation type is missing from names is impossible (the
// row's relation_type_id is a foreign key into the same game's types)
// and answers with an empty type key rather than a panic if it happens.
// A missing endpoint ref is not impossible, and is left nil; see
// RelationOutput.
func relationOf(row dbq.Relation, names map[uuid.UUID]string, refs map[uuid.UUID]*RefOutput, verbose bool) (RelationOutput, error) {
	out := RelationOutput{
		ID:       row.ID,
		TypeKey:  names[row.RelationTypeID],
		SourceID: row.SourceID,
		TargetID: row.TargetID,
		Version:  row.Version,
		Invalid:  row.Invalid,
		Source:   refs[row.SourceID],
		Target:   refs[row.TargetID],
	}
	if !verbose {
		return out, nil
	}
	fields := map[string]any{}
	if len(row.Fields) > 0 {
		if err := json.Unmarshal(row.Fields, &fields); err != nil {
			return RelationOutput{}, fmt.Errorf("decode stored fields: %w", err)
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

// endpointRefs resolves every entity id a page of edges names into the
// ref the edge was written with. Two queries for the whole page — the
// game's type vocabulary, and one bulk entity read — never one per
// endpoint: this is the fix Task 7 recorded as owed and named this task
// as the place for.
//
// An id that resolves to nothing is left out of the map rather than
// mapped to an empty ref; see RelationOutput for why a caller reads a
// missing endpoint as absent and not as a row named "".
func endpointRefs(ctx context.Context, deps MCPDeps, projectID uuid.UUID, rows []dbq.Relation) (map[uuid.UUID]*RefOutput, error) {
	refs := make(map[uuid.UUID]*RefOutput, 2*len(rows))
	if len(rows) == 0 {
		return refs, nil
	}
	ids := make([]uuid.UUID, 0, 2*len(rows))
	for _, row := range rows {
		ids = append(ids, row.SourceID, row.TargetID)
	}
	entities, err := deps.Metamodel.EntitiesByIDs(ctx, projectID, ids)
	if err != nil {
		return nil, err
	}
	types, err := entityTypeKeys(ctx, deps, projectID)
	if err != nil {
		return nil, err
	}
	for id, row := range entities {
		refs[id] = &RefOutput{TypeKey: types[row.EntityTypeID], Key: row.Key, Name: row.Name}
	}
	return refs, nil
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
	// The two halves of the repair tools' descriptions, written once and
	// used by both, because a rule stated twice is a rule that drifts:
	// entities.repair and relations.repair are one design over two
	// tables, exactly as revalidate is one sweep over two tables.
	repairAdvice := "**A repair is not a bulk editor.** It reads only the rows the type's " +
		"current schema *rejects* — the ones a schema edit flagged — writes only their " +
		"fields, and clears a row's invalid flag only by re-validating it, never by " +
		"assertion. It cannot create a row, rename one, move an edge's endpoints, or " +
		"touch a row that already fits. Per-row values are what the upsert tools are " +
		"for, with the expected_version that belongs to editing content.\n\n" +
		"**Why it exists.** Adding a required field to a type under two hundred rows " +
		"flags all two hundred, correctly and by design: a schema edit re-checks " +
		"content and never back-fills it. But a field cannot be both required and " +
		"defaulted, so nothing writable into the *type* then makes those rows fit, and " +
		"taking the field back out flags them a second time because the value they " +
		"carry has become an unknown field. This is the other half: you say once what " +
		"the new field should hold, or that the old one should be forgotten.\n\n" +
		"**The two operations.** `set` writes the values you name into every flagged " +
		"row; its keys must be fields the type declares, and one that is not is " +
		"invalid_input at `set.<key>` rather than the same failure repeated on every " +
		"row. `drop_unknown` removes the values the type no longer declares, which is " +
		"the only way to spell \"forget this\" for a field the schema has no name for. " +
		"Give at least one of them: a pass stating neither is refused, because " +
		"rewriting every flagged row with the values it already holds would either do " +
		"nothing or quietly back-fill a default nobody asked for."

	// The half of both rename descriptions that is one decision written
	// once: what a rename does *not* do. Both tools carry it verbatim,
	// because a designer renaming an entity type and a designer renaming
	// a relation type meet the same diagnostic afterwards and must not
	// have to find the explanation under only one of the two names.
	renameLimits := "**A rename does not repair the saved views that name this type, and " +
		"that is the design rather than a limitation.** A view records the *id* of every " +
		"type its query names, so a renamed type still resolves and the picture is " +
		"unchanged; what the stored query still holds is the old spelling, and every run " +
		"of that view reports `entity_type_renamed` or `relation_type_renamed` in " +
		"`stale`, naming both spellings at the JSON pointer that has to change. It goes " +
		"on reporting it until somebody saves the view again with the new spelling " +
		"(views.upsert). Nothing rewrites a stored query behind its author: a silent " +
		"repair would make the next expected_version check pass against a document " +
		"nobody wrote.\n\n" +
		"It rewrites nothing else either, and nothing else needs rewriting: entities, " +
		"edges, endpoint rules, prose links and view references all name the type by id, " +
		"which a rename does not change. The row keeps its id, its history and its " +
		"content, which is the whole difference from the workaround it replaces — " +
		"declare the new type, move every row, delete the old one.\n\n" +
		"**A key differing only in capitalisation is not a different key.** Keys are " +
		"matched without regard to case, so renaming `quest` to `Quest` is refused as " +
		"invalid_input at `to`: it would change no address while announcing a change no " +
		"reader can observe. Renaming onto a key another type already holds is refused " +
		"at `to` as well — a rename never merges two types."

	repairLoop := fmt.Sprintf("**Loop until it stops repairing.** limit defaults to %d and is "+
		"capped at %d. There is no cursor and none is needed: a repaired row leaves "+
		"the selection, so calling again works on what the last call did not fix. "+
		"scanned is how many flagged rows this pass read — equal to the limit means "+
		"there may be more behind it. Stop when repaired is empty; failed then "+
		"names every row your values could not fix, at its own key, with the schema's "+
		"own complaint.",
		metamodel.DefaultRepairBatch, metamodel.MaxRepairBatch)

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "games.counts",
		Description: "How much of everything this game holds: one row per declared entity " +
			"type and per declared relation type, each with how many rows instance it and " +
			"how many of those are invalid, plus the three totals. This is the answer to " +
			"\"how many races are there\" — one call, rather than walking entities.list to " +
			"the last page and counting. " +
			"invalid_count is the number a designer has to act on: rows a schema edit " +
			"stopped fitting, kept and flagged rather than deleted. entities.list and " +
			"relations.list with `invalid: true` are how to see which ones, and " +
			"entities.repair and relations.repair are how to fix them in bulk. " +
			"The cost does not grow with the game's content — four queries, one row per " +
			"type — so there is no page and no cursor here. " +
			"Prose is not counted: use docs.list for that.",
		OutputSchema: gameCountsOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in GameCountsInput) (GameCountsOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPGameCounts(ctx, deps, caller, projectID, in)
	})

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
		Name: "types.rename",
		Description: "Give an entity type a different key, keeping the type. `from` is the " +
			"key it has now — matched without regard to case, like every other key on this " +
			"surface — and `to` is the key it will have. expected_version is required and " +
			"must match the version you read, so a rename cannot land on top of an edit " +
			"you never saw.\n\n" + renameLimits,
		OutputSchema: typeDetailOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in TypesRenameInput) (TypeDetailOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPTypesRename(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "types.remove",
		Description: "Remove an entity type, addressed by its key — the same handle " +
			"types.get takes and entities.upsert writes against. Without cascade, a type that still has entities is refused as " +
			"in_use. With cascade it takes its entities with it, and every edge touching " +
			"one of them goes too. Removing a type also prunes its id out of every relation " +
			"type's endpoint lists, which changes rules other content is judged against. " +
			"Deleting a type saved views reference is allowed, and broke_views lists them, " +
			"one row per reference with the JSON pointer into that view's own query " +
			"document, so the repair is a known amount of work rather than a surprise the " +
			"next time somebody opens one.",
		OutputSchema: typeRemovedOutputSchema,
		Annotations:  &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: boolPtr(true)},
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in TypesRemoveInput) (TypeRemovedOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPTypesRemove(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "relation_types.upsert",
		Description: fmt.Sprintf(
			"Declare or update a relation type: a kind of directed edge between "+
				"entities (takes_place_in, requires, unlocks, available_to). source_type_keys "+
				"and target_type_keys are entity type **keys** — the same handles "+
				"entities.upsert speaks, not ids — and they are the rule every edge of this "+
				"type is checked against; an omitted or empty list means any type. A key that "+
				"names no type of this game is invalid_input at that element's own indexed "+
				"path, and so is one that names a type an earlier element already named: an "+
				"endpoint list is a set. The answer states the rules the same way, so what "+
				"you read back is what you can send again. semantic_role is what a view uses to know what the edge means: it is "+
				"optional, and when given it is one of %s — anything else is invalid_input at "+
				"path `semantic_role`, listing these same values. "+
				"field_schema declares the fields every edge of the type carries and is what "+
				"edge values are judged against; changing it re-checks every existing edge "+
				"and marks the ones that no longer fit as invalid rather than deleting them "+
				"or filling in the missing values, exactly as types.upsert does for "+
				"entities. relations.list's `invalid` filter is how to find them. "+
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
		Name: "relation_types.rename",
		Description: "Give a relation type a different key, keeping the type and every edge " +
			"of it. `from` is the key it has now, `to` is the key it will have, and " +
			"expected_version is required and must match the version you read.\n\n" +
			renameLimits,
		OutputSchema: relationTypeDetailOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in RelationTypesRenameInput) (RelationTypeDetailOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPRelationTypesRename(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "relation_types.remove",
		Description: "Remove a relation type by its key, the address relation_types.get " +
			"takes. Without cascade, one that still has edges " +
			"is refused as in_use; with cascade every edge of the type goes with it. " +
			"Deleting a type saved views reference is allowed, and broke_views lists them, " +
			"one row per reference with the JSON pointer into that view's own query " +
			"document.",
		OutputSchema: typeRemovedOutputSchema,
		Annotations:  &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: boolPtr(true)},
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in RelationTypesRemoveInput) (TypeRemovedOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPRelationTypesRemove(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "entities.upsert",
		Description: fmt.Sprintf("Create or update entities. Always a batch — one entity is a batch of "+
			"one — because seeding a game is hundreds of rows, and **at most %d items in "+
			"one call**: over that is invalid_input at path `items` naming both numbers, "+
			"not a slow success, so split a longer seed. mode is \"partial\" (the "+
			"default: every item is its own transaction, the good rows land and the rest come "+
			"back in failed with their index, key and a code saying how to fix them) or "+
			"\"atomic\" (one transaction; one bad row rolls the whole batch back and nothing "+
			"is reported as done). Anything else is refused rather than read as partial. "+
			"Rows are idempotent by (type_key, key), so re-running a seed updates in place; "+
			"updating an existing entity requires expected_version, which the written entries "+
			"of a previous call carry. written names every row that landed with its id and "+
			"its new version; count is how many. A failure coded \"retryable\" means the "+
			"database refused that item over contention — send it again; if a batch keeps "+
			"producing them, send fewer rows at a time.", metamodel.MaxBulkItems),
		OutputSchema: entitiesUpsertOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in EntitiesUpsertInput) (EntitiesUpsertOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPEntitiesUpsert(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "entities.repair",
		Description: fmt.Sprintf("Make the entities of one type fit its schema again, in one call.\n\n"+
			"%s\n\n%s", repairAdvice, repairLoop),
		OutputSchema: entitiesRepairOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in EntitiesRepairInput) (EntitiesRepairOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPEntitiesRepair(ctx, deps, caller, projectID, in)
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
		Description: "Remove one entity, addressed by (type_key, key) — the address " +
			"entities.get reads it by and entities.upsert wrote it under. Every edge " +
			"touching it goes with it, by cascade, and those edge removals are not " +
			"announced one by one. The row's id is still returned by every reader and by " +
			"the removal event; it is simply not how you address a row.",
		OutputSchema: removedOutputSchema,
		Annotations:  &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: boolPtr(true)},
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in EntitiesRemoveInput) (RemovedOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPEntitiesRemove(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "relations.upsert",
		Description: fmt.Sprintf("Create or update edges between entities. A batch, with the same two "+
			"modes, the same **%d-item** ceiling and the same failure report "+
			"entities.upsert has. Each item names its "+
			"relation type by key and both endpoints by (type_key, key); both ends are "+
			"checked against the relation type's declared endpoint lists, and a violation is "+
			"endpoint_type_mismatch naming the end that is wrong. "+
			"**An edge is identified by (relation type, source, target) and carries a "+
			"version**: writing one that already exists replaces its fields whole and "+
			"requires expected_version, which the written entries of a previous call carry, "+
			"exactly as entities.upsert does. Sending the wrong one, or none, is "+
			"version_conflict reporting the version to merge onto — nothing is overwritten. "+
			"An edge's fields are validated against the relation type's field_schema, and a "+
			"successful write clears any invalid flag a schema edit had put on it. "+
			"A game cannot hold two edges of one "+
			"relation type between the same ordered pair — say the second meaning as its own "+
			"relation type, or as a field on the one edge.", metamodel.MaxBulkItems),
		OutputSchema: relationsUpsertOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in RelationsUpsertInput) (RelationsUpsertOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPRelationsUpsert(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "relations.repair",
		Description: fmt.Sprintf("Make the edges of one relation type fit its field schema again, "+
			"in one call. Everything entities.repair says holds here, over the edges a "+
			"relation type's schema edit flagged: relation types carry a field_schema and "+
			"an invalid flag exactly as entity types do, so they get the same repair.\n\n"+
			"%s\n\n%s", repairAdvice, repairLoop),
		OutputSchema: relationsRepairOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in RelationsRepairInput) (RelationsRepairOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPRelationsRepair(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "relations.list",
		Description: fmt.Sprintf(
			"List a game's edges, optionally narrowed by relation type key, by invalid "+
				"(edges whose values no longer fit their relation type's schema, the same "+
				"filter entities.list takes) and by either "+
				"endpoint, given as a `{type_key, key}` ref — the same address "+
				"relations.upsert writes an edge with. A ref that names no entity is "+
				"not_found rather than an empty page. Paged by next_cursor exactly as "+
				"entities.list is; "+
				"limit defaults to %d and is capped at %d, and a limit below one gets the "+
				"default rather than an error. "+
				"Each edge names both endpoints twice: `source_id`/`target_id`, the entity "+
				"ids the row holds, and `source`/`target`, the (type_key, key, name) "+
				"refs the edge was written with — the refs are what this tool's own "+
				"endpoint filters and relations.remove take. An endpoint whose entity was "+
				"removed while the page was being read has its id but no ref. "+
				"Every edge also carries `version`, which is what relations.upsert's "+
				"expected_version takes, and `invalid`, which is true when editing the "+
				"relation type's field_schema left the edge's stored values no longer "+
				"fitting it — the edge is kept and flagged, never deleted or back-filled. "+
				"Rewrite it through relations.upsert with values that fit to clear the flag. "+
				"**An edge's own fields are omitted unless verbose is true**, for the reason "+
				"entities.list omits an entity's: a game has more edges than entities, so a "+
				"full page of them with their values is most of the game in one answer. What "+
				"those values mean is declared by the relation type's field_schema, which "+
				"relation_types.get publishes. To read one edge's values, prefer "+
				"relations.get, which takes the address the edge was written under. "+
				"Use entities.list with "+
				"related_to to walk a game's graph; use this tool when the answer is about "+
				"the edges themselves. %s",
			metamodel.DefaultRelationPage, metamodel.MaxRelationPage, retryAdvice),
		OutputSchema: relationsListOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in RelationsListInput) (RelationsListOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPRelationsList(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "relations.get",
		Description: "Read one edge by the address it was written under — its relation " +
			"type's key plus both endpoints as (type_key, key) — with all of its fields. " +
			"This is the only way to read the values an edge carries without paging a " +
			"listing: relations.upsert validates them against the relation type's " +
			"field_schema and stores them, and relation_types.get is where that schema is " +
			"published. Keys are matched without regard to case. An unknown relation type, " +
			"an unknown endpoint and a real address holding no edge are all not_found, and " +
			"the message says which of the three it was. The answer is one edge in the same " +
			"shape relations.list returns, ids, refs, version and invalid included.",
		OutputSchema: relationOutputSchema(),
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in RelationsGetInput) (RelationOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPRelationsGet(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "relations.remove",
		Description: "Remove one edge by the address it was written under: the relation " +
			"type's key and both endpoints as (type_key, key) refs — the same address " +
			"relations.get reads it by and relations.upsert wrote it with. The entities it " +
			"joined are untouched. The edge's id is still returned by relations.list, " +
			"relations.get and the removal event; it is simply not how you address one.",
		OutputSchema: removedOutputSchema,
		Annotations:  &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: boolPtr(true)},
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in RelationsRemoveInput) (RemovedOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPRelationsRemove(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "search",
		Description: fmt.Sprintf(
			"Search this game's content: its entities and its documents, in one ranked "+
				"list, each hit labelled by kind. An entity hit carries `entity` and a "+
				"document hit carries `document`; `kind` says which, and is what to branch "+
				"on before reading anything else.\n\n"+
				"**Ranking.** A hit whose *name* or *title* satisfies the query always "+
				"outranks one that merely mentions the words in a field or a paragraph, "+
				"however often it repeats them — that is a guarantee and not a tendency, on "+
				"both indexes and across them. name_match is on the wire so you can see the "+
				"grouping the order is built from; `rank` only orders within a group, and "+
				"re-sorting by rank alone undoes the guarantee. The two indexes' ranks are "+
				"comparable because both are ts_rank over the same `simple` text-search "+
				"configuration and the same default weights — not because the two vectors "+
				"are built alike: an entity's B weight carries its name a second time "+
				"alongside its fields, a document's does not, so within name_match false a "+
				"document mentioning a phrase in its body can outrank an entity mentioning "+
				"the same phrase in a field.\n\n"+
				"**Narrowing.** kind is \"entity\", \"document\", or omitted for both; "+
				"type_key narrows entity hits and doc_kind narrows document hits, and "+
				"neither may be combined with the other kind. Anything else at kind is "+
				"invalid_input rather than a silently widened answer.\n\n"+
				"**Entity search does not reach into attached prose.** A word that appears "+
				"only in a quest's script produces a document hit, not a quest hit, and that "+
				"hit's linked_to names the entities the document is attached to so you can "+
				"get to the quest from it.\n\n"+
				"**Fields are omitted unless verbose is true**, the same default "+
				"entities.list applies and for a stronger version of the same reason: a "+
				"search is what you reach for before you know which row you want, so "+
				"without the flag every hit's whole payload — longtext included, up to the "+
				"row cap below — comes back for rows you have not chosen yet. A hit always "+
				"carries type_key, key, name, invalid and version, which is what picking "+
				"one needs. The flag gates entity hits only; a document hit has never "+
				"carried a body.\n\n"+
				"**What is indexed.** For an entity: its name, its key, plus the text its "+
				"values carry "+
				"— text, longtext, the chosen option of an enum, and the elements of a "+
				"list<text>; numbers and booleans are not, so filter for those with "+
				"entities.list. A row found by its key alone comes back with name_match "+
				"false: the key is indexed at a lower weight than the name precisely so "+
				"that the ranking guarantee above keeps meaning what it says. Only the "+
				"first %d bytes of one row's flattened text are "+
				"indexed. For a document: its title, its summary and its body, each by its "+
				"first %d characters — a document may hold far more body than that, and a "+
				"word past that point is stored and re-read intact but is not findable here. "+
				"A document's path and kind are not indexed at all; kind is a filter, not a "+
				"term. Only current versions are indexed, and deleted documents are absent; "+
				"use docs.history and docs.diff to find when a line changed.\n\n"+
				"**Matching.** Postgres's `simple` configuration, which does no stemming: "+
				"searching \"history\" does not find \"histories\".\n\n"+
				"**Bounds.** The query is at most %d bytes, must be valid UTF-8, must hold no "+
				"control character and must contain at least one letter or digit; each of "+
				"those is invalid_input at path `query` rather than an empty answer.\n\n"+
				"**This is a top-N, not a page.** limit defaults to %d and is capped at %d "+
				"(and a limit below one gets the default rather than an error), there is no "+
				"cursor, and truncated only means the answer filled the limit — the recovery "+
				"for too many hits is a narrower query, not a deeper page.\n\n"+
				"%s",
			metamodel.MaxIndexedText, markdown.MaxIndexedChars, metamodel.MaxSearchQuery,
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
		"source_type_keys", "target_type_keys", "field_schema"},
	Properties: map[string]*jsonschema.Schema{
		"id":               stringSchema(),
		"key":              stringSchema(),
		"label":            stringSchema(),
		"version":          {Type: "integer"},
		"description":      stringSchema(),
		"source_type_keys": arrayOf(stringSchema()),
		"target_type_keys": arrayOf(stringSchema()),
		"semantic_role":    stringSchema(),
		"field_schema":     arrayOf(fieldSchemaItemSchema),
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

// The search hit schemas. They live here, beside the other output
// schemas the SDK is handed, while the Go types they describe live in
// mcp_search.go beside the core that fills them; splitting them the
// other way would put a schema in a file that registers no tool.
//
// Every field of every one of these is Required except the two that are
// genuinely optional: a hit's `entity` and `document`, of which exactly
// one is present and which one is what `kind` says, and a link's `role`,
// which a document may be attached without.
var linkedRefOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"entity_type_key", "entity_key", "name"},
	Properties: map[string]*jsonschema.Schema{
		"entity_type_key": stringSchema(),
		"entity_key":      stringSchema(),
		"name":            stringSchema(),
		"role":            stringSchema(),
	},
}

var documentHitOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"id", "path", "title", "summary", "doc_kind", "version", "linked_to"},
	Properties: map[string]*jsonschema.Schema{
		"id":        stringSchema(),
		"path":      stringSchema(),
		"title":     stringSchema(),
		"summary":   stringSchema(),
		"doc_kind":  stringSchema(),
		"version":   {Type: "integer"},
		"linked_to": arrayOf(linkedRefOutputSchema),
	},
}

var searchHitOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"kind", "name_match", "rank"},
	Properties: map[string]*jsonschema.Schema{
		"kind":       stringSchema(),
		"name_match": boolSchema(),
		"rank":       numberSchema(),
		"entity":     entityOutputSchema,
		"document":   documentHitOutputSchema,
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

// refOutputSchema is one resolved endpoint. Not in any Required list of
// the edge that carries it: an endpoint whose entity was removed
// between the listing and the resolution is reported by id alone (see
// RelationOutput).
//
// A function rather than a var, unlike its neighbours, because the two
// endpoints of an edge would otherwise share one *jsonschema.Schema
// pointer, and the SDK refuses a schema whose nodes do not form a tree
// — it panics at AddTool, which is how this was found.
func refOutputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:     "object",
		Required: []string{"type_key", "key", "name"},
		Properties: map[string]*jsonschema.Schema{
			"type_key": stringSchema(),
			"key":      stringSchema(),
			"name":     stringSchema(),
		},
	}
}

// relationOutputSchema is one edge, and it is a function for the reason
// refOutputSchema is: relations.list embeds it in a page envelope while
// relations.get answers with it whole, and the SDK refuses a schema
// whose nodes do not form a tree.
//
// `fields` is not in Required, because it is absent from a listing that
// was not asked to be verbose. `version` and `invalid` are, for the
// reason entityOutputSchema requires them: every edge carries both,
// always.
func relationOutputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:     "object",
		Required: []string{"id", "type_key", "source_id", "target_id", "version", "invalid"},
		Properties: map[string]*jsonschema.Schema{
			"id":        stringSchema(),
			"type_key":  stringSchema(),
			"source_id": stringSchema(),
			"target_id": stringSchema(),
			"version":   {Type: "integer"},
			"invalid":   boolSchema(),
			"source":    refOutputSchema(),
			"target":    refOutputSchema(),
			"fields":    objectSchema(),
		},
	}
}

var relationsListOutputSchema = listEnvelopeSchema(relationOutputSchema())

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
			Required: []string{"type_key", "id", "source_id", "target_id", "version"},
			Properties: map[string]*jsonschema.Schema{
				"type_key":  stringSchema(),
				"id":        stringSchema(),
				"source_id": stringSchema(),
				"target_id": stringSchema(),
				"version":   {Type: "integer"},
			},
		}),
		"failed": arrayOf(bulkFailureSchema),
	},
}

// The two repair answers. They are the two batch schemas with `count`
// replaced by `scanned` and `written` by `repaired`: a pass reports how
// many flagged rows it read rather than how many items it was sent,
// because nobody sent it a list.
var entitiesRepairOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"scanned", "repaired", "failed"},
	Properties: map[string]*jsonschema.Schema{
		"scanned": {Type: "integer"},
		"repaired": arrayOf(&jsonschema.Schema{
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

var relationsRepairOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"scanned", "repaired", "failed"},
	Properties: map[string]*jsonschema.Schema{
		"scanned": {Type: "integer"},
		"repaired": arrayOf(&jsonschema.Schema{
			Type:     "object",
			Required: []string{"type_key", "id", "source_id", "target_id", "version"},
			Properties: map[string]*jsonschema.Schema{
				"type_key":  stringSchema(),
				"id":        stringSchema(),
				"source_id": stringSchema(),
				"target_id": stringSchema(),
				"version":   {Type: "integer"},
			},
		}),
		"failed": arrayOf(bulkFailureSchema),
	},
}

// gameCountsOutputSchema is games.counts' answer. Each row is a type's
// slim shape plus its two counts, which is why the type properties are
// spelled here rather than referenced: the hand-written schemas in this
// file describe the wire and not the Go embedding that produces it.
var gameCountsOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"entity_types", "relation_types", "totals"},
	Properties: map[string]*jsonschema.Schema{
		"entity_types": arrayOf(&jsonschema.Schema{
			Type: "object",
			Required: []string{"id", "key", "label", "label_plural", "version",
				"entity_count", "invalid_count"},
			Properties: map[string]*jsonschema.Schema{
				"id": stringSchema(), "key": stringSchema(),
				"label": stringSchema(), "label_plural": stringSchema(),
				"version":       {Type: "integer"},
				"entity_count":  {Type: "integer"},
				"invalid_count": {Type: "integer"},
			},
		}),
		"relation_types": arrayOf(&jsonschema.Schema{
			Type: "object",
			Required: []string{"id", "key", "label", "version",
				"relation_count", "invalid_count"},
			Properties: map[string]*jsonschema.Schema{
				"id": stringSchema(), "key": stringSchema(), "label": stringSchema(),
				"version":        {Type: "integer"},
				"relation_count": {Type: "integer"},
				"invalid_count":  {Type: "integer"},
			},
		}),
		"totals": {
			Type:     "object",
			Required: []string{"entities", "relations", "invalid"},
			Properties: map[string]*jsonschema.Schema{
				"entities":  {Type: "integer"},
				"relations": {Type: "integer"},
				"invalid":   {Type: "integer"},
			},
		},
	},
}

// typeRemovedOutputSchema is removedOutputSchema plus the list a type
// removal owes its caller. broke_views is required rather than optional:
// a client that has to tell "nothing broke" from "this build does not
// report" is a client that will assume the first.
var typeRemovedOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"removed", "broke_views"},
	Properties: map[string]*jsonschema.Schema{
		"removed": boolSchema(),
		"broke_views": {Type: "array", Items: &jsonschema.Schema{
			Type:     "object",
			Required: []string{"view_key", "name", "key", "pointer"},
			Properties: map[string]*jsonschema.Schema{
				"view_key": stringSchema(), "name": stringSchema(),
				"key": stringSchema(), "pointer": stringSchema(),
			},
		}},
	},
}

var removedOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"removed"},
	Properties: map[string]*jsonschema.Schema{
		"removed": boolSchema(),
	},
}
