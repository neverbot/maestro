package web

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/views"
)

// This file is the saved-view surface: the tools an agent uses to
// compose, save, run and arrange a picture of a game's content.
//
// **Every exported MCPViews* function starts with requireScope and
// delegates to an unexported core of the same name**, which is the split
// mcp_docs.go's header argues: requireScope asks "is this token bound to
// this game", which a session caller cannot answer, and requireProject
// asks the equivalent question of a session. One implementation of every
// tool, two admission checks. TestEveryViewsToolRefusesAnotherGamesToken
// drives every one of them from the *registered* tool list, so an
// eleventh tool added tomorrow is covered without editing it.
//
// **Ten tools, and the plan's table named nine.** views.list_assets is
// the tenth and it is not optional: an agent has no other way to learn an
// asset's id, so without it views.set_background takes an argument no
// caller on this surface can obtain — the mechanism-nothing-can-reach
// this project refuses. Uploading stays REST-only and browser-only
// (api_view_assets.go says why); discovering what has been uploaded does
// not.
//
// **Every long description below is generated where the thing it
// describes is data.** The renderer catalogue comes from
// views.RendererDescription() and the operator table from
// views.OperatorDescription(), because both *are* contracts and a
// hand-written sentence beside a table is a sentence that goes false the
// first time the table moves. What is written by hand here is only what
// no table holds: the shape of the envelope, and the handful of rules an
// agent cannot infer from the document's own shape.

// --- The prose an agent cannot infer from the document ---

// viewsQueryDoc is what a caller has to know about the query language
// that the generated tables do not say. Every paragraph in it is a rule
// this sub-project's own review rounds found an agent would otherwise
// meet as an empty picture.
var viewsQueryDoc = "A query is a JSON document with five stages. `from` is one or more " +
	"selectors, each naming an entity type and optionally a `where`; `traverse` walks " +
	"relations from a named set; `nodes` and `edges` say what to draw; `project` says how " +
	"each node presents itself. `limits` bounds the walk and `params` declares values a run " +
	"binds. Every refusal is addressed by JSON pointer into the document — " +
	"`/traverse/0/via/0` names the first step's first relation type — so fix the position " +
	"the error names rather than re-reading the whole document.\n\n" +

	"**A misspelled type is refused by `@type eq`, `@type neq` and `@type in`, and draws " +
	"nothing silently under `contains`, `starts_with` and `matches`.** The first three " +
	"compare against the row's type id, so a key this game does not declare cannot be " +
	"compiled and you are told which key and where. The pattern operators cannot be " +
	"answered by an id and compare against the type's key as text, so `@type matches " +
	"\"qeust*\"` is a legal query that draws an empty picture and says nothing. That is " +
	"defensible for a pattern, which is a search rather than a name — but do not read one " +
	"rule off the other half: an empty result from a pattern is not evidence that the game " +
	"has nothing.\n\n" +

	"**An untyped `traverse` step switches off typo detection for the whole `project` " +
	"stage, including the types the document did name.** A step with no `to_type` reaches " +
	"entities of any type, so no schema can judge what a projected key means and the check " +
	"is skipped for the entire projection — a misspelt `color_by` is then accepted and the " +
	"picture comes back in one flat colour. An untyped step buys reach at the cost of every " +
	"typo check in `project`; name `to_type` wherever you can.\n\n" +

	"**A projected key and a compared key obey different rules, and they look identical in " +
	"the document.** A `where` field must be declared by *every* type in scope, because a " +
	"comparison against a type that does not declare it silently matches nothing there. A " +
	"`project` key need only be declared by *one*, because a node whose type does not " +
	"declare it simply carries no attribute — \"colour the quests by min_level, the zones " +
	"have none\" is a picture designers ask for. A key **no** type in scope declares is " +
	"refused either way, because that is a typo.\n\n" +

	"**A parameter is text, number or bool, and nothing else.** There is no enum or list " +
	"parameter: an enum's options are checked where the document writes the value, and a " +
	"value bound at run time never passes that check, so an enum misspelling would come " +
	"back as an empty picture instead of a refusal. Write the value out in the document " +
	"rather than parameterising it.\n\n" +

	"**What a condition costs, honestly.** Only one comparison this compiler emits can use " +
	"an index: `contains` on a `list<text>` field, which becomes a jsonb containment test. " +
	"Every other comparison against a declared field — equality included — is a scan of the " +
	"rows the selector's type filter left, so narrow with the selector and with `limits` " +
	"rather than expecting a `where` to be cheap. `stats.duration_ms` on every run is the " +
	"measurement, not a guess."

// viewsEnvelopeDoc is the run envelope, which is one shape for every
// renderer — swapping `graph` for `table` on a saved view must never
// require rewriting its query — and which carries three flags whose exact
// meaning only a description can state.
var viewsEnvelopeDoc = "The answer is one envelope: `nodes`, `edges`, `stats` and " +
	"`truncated`, plus `stale` when a saved view names something the game has moved, and " +
	"`positions` when a saved view was run by key.\n\n" +

	"**`attrs` is keyed by the projection *slot*, never by the field key the slot reads.** " +
	"A document that colours by a zone one hop away still answers under `attrs.color_by`, " +
	"so a renderer reads the slot it asked for whatever the query put there. `ambiguous` is " +
	"a property of the **node**, not of a slot: it says some related slot had more than one " +
	"candidate and does not say which, because the resolution is to look at the query.\n\n" +

	"**`truncated.nodes` and `truncated.edges` are about the caps, and they are " +
	"independent.** An edge's endpoints are not guaranteed to appear in `nodes` — an " +
	"`edges: [{between: …}]` entry legitimately draws relations between sets the document " +
	"chose not to draw as nodes — so a renderer that wants a closed graph filters on " +
	"`nodes` itself, whether or not anything was truncated.\n\n" +

	"**`truncated.depth` is about the *traversal*, not about the picture.** It is set when " +
	"a walk still had a hop to make when its `max_depth` bound stopped it, whether or not " +
	"the step's `to_type` or `where` would have drawn what lay beyond. A step asking for " +
	"exactly one hop is a neighbour query and never sets it, at any distance: `max_depth` " +
	"cannot have stopped a walk that was not walking. All three flags false on a query with " +
	"no traversal means the picture is whole."

// viewsStaleDoc is what a saved view answers when the game has moved
// under it, and the switch that decides.
var viewsStaleDoc = "**A saved view whose game has moved is the normal case, not the " +
	"exception.** A type renamed after the view was saved still resolves — the view records " +
	"ids, and a rename does not change one — and the run comes back whole with a " +
	"`relation_type_renamed` or `entity_type_renamed` entry in `stale` naming the old and " +
	"the new spelling at the pointer that has to change. A type that is *gone* is " +
	"`query_stale`: the run refuses, and the error carries `details.stale` with a code, a " +
	"pointer and the spelling for each thing that moved. Pass `on_stale: \"best_effort\"` " +
	"to draw what is left instead — the parts that cannot run are dropped whole and " +
	"reported, never run with a condition removed, because a diagram that quietly lost its " +
	"level filter looks exactly like a correct one.\n\n" +

	"**Repair is an explicit views.upsert.** Nothing rewrites a stored query behind its " +
	"author: a silent repair would make the next expected_version check pass against a " +
	"document nobody wrote."

// viewsKeyDoc is the fact about keys an agent will otherwise discover by
// trying.
//
// **It used to say a key is permanent everywhere, and that stopped being
// true**: types.rename and relation_types.rename move an entity type's
// or a relation type's key, keeping the row, its id and its history. A
// *view's* key has no rename, and that is what this paragraph is still
// about — so it says which is which rather than repeating a blanket
// claim the metamodel no longer honours. A description that told an
// agent no rename exists would send it to the several-call workaround
// for a job one call now does.
var viewsKeyDoc = "**A view's key is permanent.** views.upsert is addressed *by key*, so " +
	"upserting under a new one creates a second view and leaves the first standing. " +
	"Fixing a misspelled view key means creating the new view, moving what pointed at the " +
	"old one, and deleting the old one: several calls, and the history and the version go " +
	"with the row that is deleted. Spell a view's key deliberately the first time.\n\n" +
	"**Type keys are the exception.** types.rename and relation_types.rename move an " +
	"entity type's or a relation type's key while keeping the row — its id, its history " +
	"and its content — which is exactly why a saved view survives one: this view records " +
	"the *id* of every type it names. What a rename leaves behind is the old spelling in " +
	"this document, reported in `stale` on every run until the view is saved again."

// --- Inputs ---

// ViewsListInput narrows a listing of this game's saved views.
type ViewsListInput struct {
	ScopedArgs
	Renderer string `json:"renderer,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
	Limit    int32  `json:"limit,omitempty"`
}

// ViewsGetInput addresses one saved view by key.
type ViewsGetInput struct {
	ScopedArgs
	Key string `json:"key"`
}

// ViewsUpsertInput creates or replaces one saved view.
//
// **Query is a json.RawMessage and not a string**, so an agent writes the
// document as JSON in the tool call rather than as a string holding
// escaped JSON. The domain stores the bytes it is given, semantically as
// written; a second round of escaping would be a second place for a
// document to be wrong.
//
// **LayoutSeed is a *int32 and ExpectedVersion is a *int32**, for the two
// different reasons this codebase already has: 0 is a seed a designer may
// deliberately choose, so a plain int32 would store 1 for it; and an
// omitted expected_version must be distinguishable from `0`, which is the
// spelling of "this view must not exist yet".
type ViewsUpsertInput struct {
	ScopedArgs
	Key             string          `json:"key"`
	Name            string          `json:"name"`
	Description     string          `json:"description,omitempty"`
	Query           json.RawMessage `json:"query"`
	Renderer        string          `json:"renderer"`
	RendererParams  map[string]any  `json:"renderer_params,omitempty"`
	LayoutMode      string          `json:"layout_mode,omitempty"`
	LayoutSeed      *int32          `json:"layout_seed,omitempty"`
	ExpectedVersion *int32          `json:"expected_version"`
}

// ViewsRemoveInput deletes one saved view.
type ViewsRemoveInput struct {
	ScopedArgs
	Key string `json:"key"`
}

// ViewsRunInput executes a saved view by key, or an inline query.
//
// **Exactly one of Key and Query**, and naming both is refused rather
// than resolved by precedence: a caller that sent both meant one of them,
// and guessing which runs a picture nobody asked for.
type ViewsRunInput struct {
	ScopedArgs
	Key           string          `json:"key,omitempty"`
	Query         json.RawMessage `json:"query,omitempty"`
	Params        map[string]any  `json:"params,omitempty"`
	IncludeFields bool            `json:"include_fields,omitempty"`
	OnStale       string          `json:"on_stale,omitempty"`
}

// ViewsValidateInput judges a query without saving it and without
// running it.
type ViewsValidateInput struct {
	ScopedArgs
	Query          json.RawMessage `json:"query"`
	Params         map[string]any  `json:"params,omitempty"`
	Renderer       string          `json:"renderer,omitempty"`
	RendererParams map[string]any  `json:"renderer_params,omitempty"`
}

// ViewsPositionInput is one node's coordinates.
//
// Pinned is a *bool because false is a value a caller means: a plain bool
// would store the opposite of the column default for every caller that
// said nothing.
type ViewsPositionInput struct {
	EntityType string  `json:"entity_type"`
	EntityKey  string  `json:"entity_key"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Pinned     *bool   `json:"pinned,omitempty"`
}

// ViewsSetPositionsInput writes coordinates for one view.
type ViewsSetPositionsInput struct {
	ScopedArgs
	Key       string               `json:"key"`
	Positions []ViewsPositionInput `json:"positions"`
}

// ViewsEntityAddressInput names one node without carrying coordinates.
type ViewsEntityAddressInput struct {
	EntityType string `json:"entity_type"`
	EntityKey  string `json:"entity_key"`
}

// ViewsClearPositionsInput drops positions for one view.
//
// **Entities is a pointer, and the pointer is the whole safety of this
// call.** Omitting it — or sending `null`, which means the same — clears
// the *whole* view's arrangement; sending `[]` is refused. `omitempty` on
// a plain slice cannot carry that distinction, because an absent field
// and an explicit empty array unmarshal to the same Go value, and the two
// mean opposite things here: said nothing is not said none, and a client
// whose list of dirty nodes came out empty must not wipe a designer's
// afternoon of map work.
type ViewsClearPositionsInput struct {
	ScopedArgs
	Key      string                     `json:"key"`
	Entities *[]ViewsEntityAddressInput `json:"entities,omitempty"`
}

// ViewsPointInput is a background's offset, `{"x": …, "y": …}`.
//
// **An object and not the two-element array the spec's own table
// implies.** The column is an object; the array spelling belonged to a
// renderer parameter that was deleted when the background moved onto the
// row (Task 14), and shipping the array here would be a second spelling
// of one value with a conversion between them for nobody's benefit.
type ViewsPointInput struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// ViewsSetBackgroundInput points one view at one uploaded image.
//
// All three are pointers, and each for its own reason. AssetID nil is how
// a background is **cleared** — it is the only spelling for it. Scale and
// Offset nil mean "the defaults", not "leave what is there": a caller
// that names a new image and says nothing about the arithmetic must not
// inherit the previous image's, and an offset of {0,0} is a legal thing
// to ask for, so a plain struct could not tell the two apart.
type ViewsSetBackgroundInput struct {
	ScopedArgs
	Key     string           `json:"key"`
	AssetID *string          `json:"asset_id,omitempty"`
	Scale   *float64         `json:"scale,omitempty"`
	Offset  *ViewsPointInput `json:"offset,omitempty"`
}

// ViewsListAssetsInput pages this game's uploaded background images.
type ViewsListAssetsInput struct {
	ScopedArgs
	Cursor string `json:"cursor,omitempty"`
	Limit  int32  `json:"limit,omitempty"`
}

// --- Outputs ---

// ViewSummaryOutput is one row of views.list: enough to choose a view,
// and no query document.
//
// Stale is the flag, and it is a boolean by design: it answers "does this
// view still resolve against the game as it stands", which is what a
// picker needs, and views.run or views.get is where a caller learns
// *what* moved and at which pointer.
type ViewSummaryOutput struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Renderer string `json:"renderer"`
	Version  int32  `json:"version"`
	Stale    bool   `json:"stale"`
}

// ViewsListOutput is one page of saved views.
type ViewsListOutput struct {
	Items      []ViewSummaryOutput `json:"items"`
	NextCursor *string             `json:"next_cursor,omitempty"`
}

// ViewOutput is one saved view, whole.
//
// Query is a json.RawMessage so the document comes back as a document
// rather than as a string of escaped JSON — the same reason the input
// takes one.
type ViewOutput struct {
	ID                string          `json:"id"`
	Key               string          `json:"key"`
	Name              string          `json:"name"`
	Description       string          `json:"description,omitempty"`
	Query             json.RawMessage `json:"query"`
	Renderer          string          `json:"renderer"`
	RendererParams    map[string]any  `json:"renderer_params"`
	LayoutMode        string          `json:"layout_mode"`
	LayoutSeed        *int32          `json:"layout_seed,omitempty"`
	BackgroundAssetID *string         `json:"background_asset_id,omitempty"`
	BackgroundScale   float64         `json:"background_scale"`
	BackgroundOffset  ViewsPointInput `json:"background_offset"`
	Version           int32           `json:"version"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

// ViewsRemovedOutput is what a deletion answers with.
type ViewsRemovedOutput struct {
	Removed bool `json:"removed"`
}

// ViewsValidateOutput is a query that passed, with what the pass learned.
type ViewsValidateOutput struct {
	Valid  bool              `json:"valid"`
	Refs   []ViewsRefOutput  `json:"refs"`
	Limits ViewsLimitsOutput `json:"limits"`
}

// ViewsRefOutput is one type this query depends on, at the pointer that
// names it — the same list a save writes into the dependency index, so an
// agent can see before saving which deletions would break this view.
type ViewsRefOutput struct {
	Kind    string `json:"kind"`
	Key     string `json:"key"`
	Pointer string `json:"pointer"`
}

// ViewsLimitsOutput is the bounds a run would actually use: the
// document's where it set them, the defaults where it did not.
type ViewsLimitsOutput struct {
	MaxDepth int `json:"max_depth"`
	MaxNodes int `json:"max_nodes"`
	MaxEdges int `json:"max_edges"`
}

// ViewsPositionsWrittenOutput and ViewsPositionsRemovedOutput answer with
// a count rather than with a bare acknowledgement: a clear that removed
// nothing and a clear that removed forty are different facts, and only
// the number tells them apart.
type ViewsPositionsWrittenOutput struct {
	Written int `json:"written"`
}

// ViewsPositionsRemovedOutput is what a clear answers with.
type ViewsPositionsRemovedOutput struct {
	Removed int64 `json:"removed"`
}

// ViewsAssetsOutput is one page of this game's uploaded images.
type ViewsAssetsOutput struct {
	Items      []ViewAssetOutput `json:"items"`
	NextCursor *string           `json:"next_cursor,omitempty"`
}

// --- Conversions ---

// viewOutput projects one stored row. It is the one place a view becomes
// a wire value, so the MCP surface and the REST mirror cannot disagree
// about what a view is.
//
// **renderer_params is `{}` and never null.** A nil Go map marshals to
// `null`, and a client reading a view back would then have two spellings
// of "no parameters" to handle. The initialiser here is unobservable
// today and is kept anyway: the domain stores `{}` in the column rather
// than a JSON null, and unmarshalling `{}` allocates the map, so the
// nil is reachable only from a row written outside internal/views.
// TestAViewWithNoRendererParametersReadsBackAsAnObjectAndNotNull asserts
// the outcome rather than this line, so it holds whichever of the two
// keeps the promise — which is what makes it worth having when the other
// is a redundancy.
func viewOutput(row dbq.View) (ViewOutput, error) {
	out := ViewOutput{
		ID: row.ID.String(), Key: row.Key, Name: row.Name, Description: row.Description,
		Query: json.RawMessage(row.Query), Renderer: row.Renderer,
		RendererParams: map[string]any{}, LayoutMode: row.LayoutMode,
		LayoutSeed: &row.LayoutSeed, BackgroundScale: row.BackgroundScale,
		Version: row.Version, UpdatedAt: row.UpdatedAt.Time,
	}
	if len(row.RendererParams) > 0 {
		if err := json.Unmarshal(row.RendererParams, &out.RendererParams); err != nil {
			return ViewOutput{}, fmt.Errorf("the stored renderer parameters of view %q "+
				"do not decode: %w", row.Key, err)
		}
	}
	if row.BackgroundAssetID != nil {
		id := row.BackgroundAssetID.String()
		out.BackgroundAssetID = &id
	}
	if len(row.BackgroundOffset) > 0 {
		var point ViewsPointInput
		if err := json.Unmarshal(row.BackgroundOffset, &point); err != nil {
			return ViewOutput{}, fmt.Errorf("the stored background offset of view %q "+
				"does not decode: %w", row.Key, err)
		}
		out.BackgroundOffset = point
	}
	return out, nil
}

// runOutput is the envelope as it goes on the wire.
//
// **positions is an array on every run of a saved key, empty or full, and
// is absent on an inline query.** The domain's own struct cannot carry
// that distinction — `omitempty` collapses "nothing was ever dragged"
// into "this run has no arrangement at all" — and an agent that called
// one tool either way needs it: absent means "positions do not apply
// here", empty means "nobody has dragged anything yet".
func runOutput(result views.Result, saved bool) map[string]any {
	out := map[string]any{
		"nodes": result.Nodes, "edges": result.Edges,
		"stats": result.Stats, "truncated": result.Truncated,
	}
	if len(result.Stale) > 0 {
		out["stale"] = result.Stale
	}
	if saved {
		positions := result.Positions
		if positions == nil {
			positions = []views.Position{}
		}
		out["positions"] = positions
	}
	return out
}

// --- The tools ---

// MCPViewsList implements views.list.
func MCPViewsList(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in ViewsListInput) (ViewsListOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return ViewsListOutput{}, err
	}
	return viewsList(ctx, deps, projectID, in)
}

func viewsList(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in ViewsListInput) (ViewsListOutput, error) {
	page, err := deps.Views.ListViews(ctx, projectID, views.ViewFilter{
		Renderer: in.Renderer, Cursor: in.Cursor, Limit: in.Limit,
	})
	if err != nil {
		return ViewsListOutput{}, err
	}
	stale, err := deps.Views.StaleViews(ctx, projectID, page.Views)
	if err != nil {
		return ViewsListOutput{}, err
	}
	out := ViewsListOutput{Items: make([]ViewSummaryOutput, 0, len(page.Views))}
	for _, row := range page.Views {
		out.Items = append(out.Items, ViewSummaryOutput{
			Key: row.Key, Name: row.Name, Renderer: row.Renderer,
			Version: row.Version, Stale: stale[row.ID],
		})
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	return out, nil
}

// MCPViewsGet implements views.get.
func MCPViewsGet(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in ViewsGetInput) (ViewOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return ViewOutput{}, err
	}
	return viewsGet(ctx, deps, projectID, in)
}

func viewsGet(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in ViewsGetInput) (ViewOutput, error) {
	row, err := deps.Views.ViewByKey(ctx, projectID, in.Key)
	if err != nil {
		return ViewOutput{}, err
	}
	return viewOutput(row)
}

// MCPViewsUpsert implements views.upsert.
func MCPViewsUpsert(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in ViewsUpsertInput) (ViewOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return ViewOutput{}, err
	}
	return viewsUpsert(ctx, deps, caller, projectID, in)
}

func viewsUpsert(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in ViewsUpsertInput) (ViewOutput, error) {
	row, err := deps.Views.UpsertView(ctx, projectID, views.ViewInput{
		Key: in.Key, Name: in.Name, Description: in.Description,
		Query: []byte(in.Query), Renderer: in.Renderer, RendererParams: in.RendererParams,
		LayoutMode: in.LayoutMode, LayoutSeed: in.LayoutSeed,
		ExpectedVersion: in.ExpectedVersion, Actor: actorOf(caller),
	})
	if err != nil {
		return ViewOutput{}, err
	}
	return viewOutput(row)
}

// MCPViewsRemove implements views.remove.
func MCPViewsRemove(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in ViewsRemoveInput) (ViewsRemovedOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return ViewsRemovedOutput{}, err
	}
	return viewsRemove(ctx, deps, projectID, in)
}

func viewsRemove(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in ViewsRemoveInput) (ViewsRemovedOutput, error) {
	if err := deps.Views.RemoveView(ctx, projectID, in.Key); err != nil {
		return ViewsRemovedOutput{}, err
	}
	return ViewsRemovedOutput{Removed: true}, nil
}

// MCPViewsRun implements views.run.
func MCPViewsRun(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in ViewsRunInput) (map[string]any, error) {
	if err := requireScope(caller, projectID); err != nil {
		return nil, err
	}
	return viewsRun(ctx, deps, projectID, in)
}

// viewsRun is the one tool with two entry points into the domain, and the
// choice between them is the caller's alone.
//
// **Naming both a key and a query is refused, and so is naming
// neither.** A precedence rule would run a picture the caller did not ask
// for and say nothing, and this is a read whose whole value is that it
// answers the question that was posed.
func viewsRun(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in ViewsRunInput) (map[string]any, error) {
	req := views.RunRequest{
		Params: in.Params, IncludeFields: in.IncludeFields, OnStale: in.OnStale,
	}
	switch {
	case in.Key != "" && len(in.Query) > 0:
		return nil, invalidInput("key", "names a saved view and a query document was sent "+
			"as well: run one or the other, because guessing which you meant would draw a "+
			"picture you did not ask for")
	case in.Key != "":
		result, err := deps.Views.RunView(ctx, projectID, in.Key, req)
		if err != nil {
			return nil, err
		}
		return runOutput(result, true), nil
	case len(in.Query) > 0:
		query, err := views.ParseQuery([]byte(in.Query))
		if err != nil {
			return nil, err
		}
		req.Query = query
		result, err := deps.Views.Run(ctx, projectID, req)
		if err != nil {
			return nil, err
		}
		return runOutput(result, false), nil
	default:
		return nil, invalidInput("key", "is required unless a query document is sent: this "+
			"tool runs a saved view by key, or an inline query, and it cannot run neither")
	}
}

// MCPViewsValidate implements views.validate.
func MCPViewsValidate(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in ViewsValidateInput) (ViewsValidateOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return ViewsValidateOutput{}, err
	}
	return viewsValidate(ctx, deps, projectID, in)
}

func viewsValidate(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in ViewsValidateInput) (ViewsValidateOutput, error) {
	query, err := views.ParseQuery([]byte(in.Query))
	if err != nil {
		return ViewsValidateOutput{}, err
	}
	got, err := deps.Views.Validate(ctx, projectID, views.ValidateRequest{
		Query: query, Params: in.Params,
		Renderer: in.Renderer, RendererParams: in.RendererParams,
	})
	if err != nil {
		return ViewsValidateOutput{}, err
	}
	out := ViewsValidateOutput{
		Valid: true,
		Refs:  make([]ViewsRefOutput, 0, len(got.Refs)),
		Limits: ViewsLimitsOutput{MaxDepth: got.Limits.MaxDepth,
			MaxNodes: got.Limits.MaxNodes, MaxEdges: got.Limits.MaxEdges},
	}
	for _, ref := range got.Refs {
		out.Refs = append(out.Refs, ViewsRefOutput{
			Kind: ref.Kind, Key: ref.Key, Pointer: ref.Pointer,
		})
	}
	return out, nil
}

// MCPViewsSetPositions implements views.set_positions.
func MCPViewsSetPositions(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in ViewsSetPositionsInput) (ViewsPositionsWrittenOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return ViewsPositionsWrittenOutput{}, err
	}
	return viewsSetPositions(ctx, deps, projectID, in)
}

func viewsSetPositions(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in ViewsSetPositionsInput) (ViewsPositionsWrittenOutput, error) {
	positions := make([]views.PositionInput, 0, len(in.Positions))
	for _, p := range in.Positions {
		positions = append(positions, views.PositionInput{
			EntityType: p.EntityType, EntityKey: p.EntityKey, X: p.X, Y: p.Y, Pinned: p.Pinned,
		})
	}
	// nil rather than an empty slice when the caller sent none, so the
	// domain's own "a call that sets no position changed nothing" refusal
	// fires on the argument the caller actually sent.
	if len(in.Positions) == 0 {
		positions = nil
	}
	if err := deps.Views.SetPositions(ctx, projectID, in.Key, positions); err != nil {
		return ViewsPositionsWrittenOutput{}, err
	}
	return ViewsPositionsWrittenOutput{Written: len(positions)}, nil
}

// MCPViewsClearPositions implements views.clear_positions.
func MCPViewsClearPositions(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in ViewsClearPositionsInput) (ViewsPositionsRemovedOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return ViewsPositionsRemovedOutput{}, err
	}
	return viewsClearPositions(ctx, deps, projectID, in)
}

// viewsClearPositions carries the nil/empty distinction across the wire
// unchanged: a nil Entities means the whole view, an empty one is
// refused by the domain, and this function must not collapse the two by
// building a slice for a pointer that was never sent.
func viewsClearPositions(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in ViewsClearPositionsInput) (ViewsPositionsRemovedOutput, error) {
	var addresses []views.EntityAddress
	if in.Entities != nil {
		addresses = make([]views.EntityAddress, 0, len(*in.Entities))
		for _, a := range *in.Entities {
			addresses = append(addresses, views.EntityAddress{
				EntityType: a.EntityType, EntityKey: a.EntityKey,
			})
		}
	}
	removed, err := deps.Views.ClearPositions(ctx, projectID, in.Key, addresses)
	if err != nil {
		return ViewsPositionsRemovedOutput{}, err
	}
	return ViewsPositionsRemovedOutput{Removed: removed}, nil
}

// MCPViewsSetBackground implements views.set_background.
func MCPViewsSetBackground(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in ViewsSetBackgroundInput) (ViewOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return ViewOutput{}, err
	}
	return viewsSetBackground(ctx, deps, projectID, in)
}

func viewsSetBackground(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in ViewsSetBackgroundInput) (ViewOutput, error) {
	background := views.BackgroundInput{Scale: in.Scale}
	if in.AssetID != nil {
		id, err := parseID("asset_id", *in.AssetID)
		if err != nil {
			return ViewOutput{}, err
		}
		background.AssetID = &id
	}
	if in.Offset != nil {
		background.Offset = &views.Point{X: in.Offset.X, Y: in.Offset.Y}
	}
	if err := deps.Views.SetBackground(ctx, projectID, in.Key, background); err != nil {
		return ViewOutput{}, err
	}
	// The row back rather than an acknowledgement: the three columns this
	// call writes are the three a caller then wants to read, and a second
	// views.get to see what it just wrote would be a round trip for
	// nothing.
	row, err := deps.Views.ViewByKey(ctx, projectID, in.Key)
	if err != nil {
		return ViewOutput{}, err
	}
	return viewOutput(row)
}

// MCPViewsListAssets implements views.list_assets.
func MCPViewsListAssets(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in ViewsListAssetsInput) (ViewsAssetsOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return ViewsAssetsOutput{}, err
	}
	return viewsListAssets(ctx, deps, projectID, in, "")
}

// viewsListAssets takes the game slug the URL was asked through, so the
// URL it hands back is the URL the caller can fetch. An MCP caller has no
// slug in hand and gets the game's own; see viewAssetOutput, which draws
// the same distinction for the REST mirror.
func viewsListAssets(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in ViewsListAssetsInput, game string) (ViewsAssetsOutput, error) {
	if game == "" {
		project, err := deps.Projects.ByID(ctx, projectID)
		if err != nil {
			return ViewsAssetsOutput{}, err
		}
		game = project.Slug
	}
	page, err := deps.Views.ListAssets(ctx, projectID, views.AssetFilter{
		Cursor: in.Cursor, Limit: in.Limit,
	})
	if err != nil {
		return ViewsAssetsOutput{}, err
	}
	out := ViewsAssetsOutput{Items: make([]ViewAssetOutput, 0, len(page.Assets))}
	for _, asset := range page.Assets {
		out.Items = append(out.Items, viewAssetOutput(game, asset))
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	return out, nil
}

// --- Registration ---

// addViewsTools registers the ten saved-view tools.
//
// Every one goes through addScopedTool, so game isolation is enforced in
// one place regardless of how many tools this file grows —
// TestEveryMCPToolGoesThroughAddScopedTool walks the *served* tool list
// and fails on any tool that reached the server another way.
//
// **The two long tables in these descriptions are generated.**
// views.RendererDescription() and views.OperatorDescription() each read
// the data their own package enforces against, guarded in both directions
// by that package's own tests. A hand-written paragraph naming a renderer
// parameter or an operator would be a paragraph that goes false the first
// time either table moves, and an agent would find out by being refused.
func (s *Server) addViewsTools(srv *mcp.Server, deps MCPDeps) {
	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "views.list",
		Description: fmt.Sprintf(
			"List this game's saved views: key, name, renderer, version and whether the "+
				"view is stale. **No query documents** — views.get is where one comes from. "+
				"Filter by renderer to find every graph or every table. "+
				"`stale` true means this view names something the game no longer has or no "+
				"longer spells that way, so running it will report a rename or refuse; "+
				"views.run says which position broke and how. "+
				"Pass the previous answer's next_cursor for the next page; a cursor belongs "+
				"to the game and the filter it was issued for and is refused against any "+
				"other. %s",
			retryAdvice),
		OutputSchema: viewsListOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in ViewsListInput) (ViewsListOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPViewsList(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "views.get",
		Description: fmt.Sprintf(
			"Read one saved view by key: its whole query document, its renderer and that "+
				"renderer's parameters, its layout mode and seed, its background, and its "+
				"version. The version is what views.upsert's expected_version takes. "+
				"Keys are matched without regard to case. %s %s",
			viewsKeyDoc, retryAdvice),
		OutputSchema: viewOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in ViewsGetInput) (ViewOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPViewsGet(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "views.upsert",
		Description: fmt.Sprintf(
			"Create or replace one saved view, addressed by key. A view is a stored query "+
				"plus the renderer that draws it; it stores no picture, so running it "+
				"always draws the game as it stands now.\n\n"+
				"**expected_version is required. Pass 0 to create a view that must not exist "+
				"yet, or the version you read to replace the one that is there.** Omitting "+
				"it is invalid_input, not a guess: a mistyped key with no version would "+
				"silently become a second view. A mismatch is version_conflict carrying the "+
				"current version.\n\n"+
				"**Any other version, against a view this game does not have, is not_found "+
				"saying it was removed** — not a quiet re-creation. The view that would come "+
				"back carries a new id, and a view's saved node positions and its background "+
				"image hang off that id, so the picture a designer arranged would be "+
				"discarded without a word. Pass 0 to bring the view back deliberately, "+
				"knowing the arrangement starts over.\n\n"+
				"%s\n\n"+
				"The query is judged before anything is stored: it must resolve against this "+
				"game's types and fields, and the renderer must be able to draw what it "+
				"produces, or nothing is written. A document that does not resolve is "+
				"query_invalid at the pointer that broke; a renderer that cannot consume it "+
				"is renderer_requirements, and the answer is to pick another renderer or "+
				"change the query.\n\n"+
				"layout_mode is one of %s and is a contract with the client: the server "+
				"stores it, returns it and reads nothing else off it. layout_seed is for a "+
				"client's own deterministic layout, and 0 is a seed like any other.\n\n"+
				"**This call does not touch the background.** A view's background image, "+
				"scale and offset are views.set_background's, so an ordinary edit of a query "+
				"never silently detaches the world map a designer placed behind it. It does "+
				"refuse a renderer change that would strand one: change the renderer to a "+
				"renderer that draws a background, or clear the background first.\n\n"+
				"%s\n\n%s\n\n%s\n\n%s",
			viewsKeyDoc, strings.Join(views.LayoutModes(), ", "),
			views.RendererDescription(), views.OperatorDescription(), viewsQueryDoc, retryAdvice),
		InputSchema:  queryDocumentInput[ViewsUpsertInput]("views.upsert"),
		OutputSchema: viewOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in ViewsUpsertInput) (ViewOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPViewsUpsert(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "views.remove",
		Description: fmt.Sprintf(
			"Delete one saved view by key, with its stored positions and its dependency "+
				"index. **It takes no expected_version**, deliberately: a view is derived "+
				"content — the query is stored, the picture is not — so removing one "+
				"somebody had just edited costs a single views.upsert from a document the "+
				"caller already holds. What is lost is the arrangement designers dragged, "+
				"which nothing restores. A key that names no view is not_found. %s",
			retryAdvice),
		OutputSchema: viewsRemovedOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in ViewsRemoveInput) (ViewsRemovedOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPViewsRemove(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "views.run",
		Description: fmt.Sprintf(
			"Run a view and get the picture back: pass `key` for a saved view, or `query` "+
				"for an inline document. Exactly one of the two — both together is refused, "+
				"because guessing which you meant would draw a picture you did not ask for.\n\n"+
				"**An inline query leaves nothing behind.** \"Which quests can a Mage reach?\", "+
				"asked once in conversation, is not a saved artefact of the game: send the "+
				"document, read the answer, save nothing. Save a view when a designer will "+
				"want to open it again.\n\n"+
				"`params` binds values over the document's declared defaults. "+
				"`include_fields` adds every declared field of every node and edge to the "+
				"answer, which is off by default because it is the difference between a "+
				"diagram and a data dump.\n\n"+
				"%s\n\n%s\n\n%s\n\n"+
				"**Every run is bounded and the bounds are reported.** The query's own "+
				"`limits` cap the walk; a `retryable` error whose message names max_depth, "+
				"max_nodes and max_edges is a run that exceeded its time budget, and the "+
				"advice in it is the answer — lower a bound, narrow the selector, drop a "+
				"step. There is no cursor on this tool: a picture is a whole answer or it is "+
				"not one, and a half-drawn graph is worse than a bounded one.\n\n%s\n\n%s",
			viewsQueryDoc, viewsEnvelopeDoc, viewsStaleDoc,
			views.OperatorDescription(), retryAdvice),
		InputSchema:  queryDocumentInput[ViewsRunInput]("views.run"),
		OutputSchema: viewsRunOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in ViewsRunInput) (map[string]any, error) {
		caller, _ := CallerFrom(ctx)
		return MCPViewsRun(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "views.validate",
		Description: fmt.Sprintf(
			"Judge a query without saving it and without running it. **Composing a view is "+
				"a loop and this is what closes it**: a five-step traversal against a game "+
				"you have not seen will be wrong twice, and validating costs a catalogue "+
				"read rather than a graph walk — no timeout, no bounded transaction, and the "+
				"same structured errors views.run and views.upsert answer with, at the same "+
				"pointers. Validate until it passes, then save or run once.\n\n"+
				"Name a renderer to have the pair judged together, which is the only way a "+
				"renderer parameter *can* be judged: a renderer requirement is a statement "+
				"about what the query produces. Naming none judges the query alone.\n\n"+
				"A query that passes answers with `refs` — every type it depends on, with "+
				"the pointer that names it, which is exactly what deleting one of those "+
				"types would report as broken — and `limits`, the bounds a run would "+
				"actually use.\n\n"+
				"**Staleness has no meaning here.** A document handed over for validation "+
				"has no recorded past to have moved away from, so a misspelling in it is "+
				"query_invalid rather than query_stale. Ask views.run about a saved view to "+
				"learn what the game moved under it.\n\n%s\n\n%s\n\n%s\n\n%s",
			views.RendererDescription(), views.OperatorDescription(),
			viewsQueryDoc, retryAdvice),
		InputSchema:  queryDocumentInput[ViewsValidateInput]("views.validate"),
		OutputSchema: viewsValidateOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in ViewsValidateInput) (ViewsValidateOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPViewsValidate(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "views.set_positions",
		Description: fmt.Sprintf(
			"Write the coordinates of one or more nodes of one saved view. Each entry names "+
				"a node by its entity type's key and its own key, with x, y and an optional "+
				"`pinned` — true by default, meaning a client's layout may not move it.\n\n"+
				"**Positions are per view, not per entity.** The same quest sits in a "+
				"different place in every view that draws it, and a run of another view is "+
				"unaffected. They survive an edit to the query and they survive the game "+
				"growing: adding twelve more quests leaves the four that were placed exactly "+
				"where they were, and the new ones arrive unplaced.\n\n"+
				"Coordinates are the client's own space and mean nothing to the server: "+
				"nothing here scales, snaps or clamps them. x and y must be finite. "+
				"At most %d positions in one call, which is the most nodes a run can return "+
				"— a longer call is positioning something no run has shown you. An empty "+
				"list is refused rather than answered with success. Naming one node twice in "+
				"one call is refused too, because the two coordinates would be resolved by "+
				"array order and nobody meant that.\n\n"+
				"A node that was never dragged is **absent** from a run's `positions`, not "+
				"returned at the origin: (0, 0) is a place a designer may deliberately have "+
				"chosen. %s",
			views.MaxPositions, retryAdvice),
		OutputSchema: viewsPositionsWrittenOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in ViewsSetPositionsInput) (ViewsPositionsWrittenOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPViewsSetPositions(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "views.clear_positions",
		Description: fmt.Sprintf(
			"Drop stored positions for one saved view, and answer with how many rows went.\n\n"+
				"**Omitting `entities` clears the whole view's arrangement; sending an empty "+
				"array is refused.** Said nothing is not said none, and that distinction is "+
				"the only thing standing between a client whose list of dirty nodes came out "+
				"empty and a designer's afternoon of map work. `\"entities\": null` means the "+
				"same as omitting it, which is *clear everything* — do not send it meaning "+
				"\"clear nothing\".\n\n"+
				"An address that names no entity of this game is not_found. An entity that "+
				"exists and was never dragged is not an error: it has no row, and removing "+
				"nothing from it is the answer, which is why the count is what comes back. "+
				"At most %d addresses in one call. %s",
			views.MaxPositions, retryAdvice),
		OutputSchema: viewsPositionsRemovedOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in ViewsClearPositionsInput) (ViewsPositionsRemovedOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPViewsClearPositions(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "views.set_background",
		Description: fmt.Sprintf(
			"Put an uploaded image behind one saved view, with a scale and an offset, and "+
				"answer with the view. asset_id is the id of an image this game already "+
				"holds — views.list_assets is where one comes from, and there is no upload "+
				"on this surface: images arrive from a browser, because pushing megabytes of "+
				"base64 through a tool call to save a human from opening the UI is the wrong "+
				"trade.\n\n"+
				"**Omitting asset_id clears the background**, and that is the only spelling "+
				"for it. scale and offset are the defaults when omitted rather than whatever "+
				"the previous image used — a new image must not inherit the old one's "+
				"arithmetic — and offset is an object, `{\"x\": …, \"y\": …}`.\n\n"+
				"Two refusals worth knowing before you call: a scale or an offset with no "+
				"asset is refused, because they place an image that is not there; and only a "+
				"renderer that actually draws a background accepts one — a background stored "+
				"under a renderer that ignores it is a value nothing reads. An asset of "+
				"another game is not_found. %s",
			retryAdvice),
		OutputSchema: viewOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in ViewsSetBackgroundInput) (ViewOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPViewsSetBackground(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "views.list_assets",
		Description: fmt.Sprintf(
			"List the background images this game holds: id, filename, mime, pixel width "+
				"and height, and the URL the bytes are served from. **This is where an "+
				"asset_id for views.set_background comes from** — nothing else on this "+
				"surface hands one out.\n\n"+
				"mime, width and height are *decoded from the bytes*, never taken from the "+
				"upload's own claims, so the pixel size here is the real one and is what a "+
				"scale should be computed against. filename is prose a designer recognises "+
				"the image by and is read by nothing.\n\n"+
				"Uploading and deleting are browser-side, over REST. Pass the previous "+
				"answer's next_cursor for the next page. %s",
			retryAdvice),
		OutputSchema: viewsAssetsOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in ViewsListAssetsInput) (ViewsAssetsOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPViewsListAssets(ctx, deps, caller, projectID, in)
	})
}

// --- Hand-written output schemas ---
//
// Written by hand for the reason mcp.go's own schema block gives: the SDK
// validates a tool's output against its *marshalled JSON*, and its
// reflection-based inference gets that shape wrong for any type whose
// marshalling comes from a method rather than from its literal Go
// structure — a mismatch that surfaces only when the tool is called.

var viewSummarySchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"key", "name", "renderer", "version", "stale"},
	Properties: map[string]*jsonschema.Schema{
		"key": stringSchema(), "name": stringSchema(), "renderer": stringSchema(),
		"version": integerSchema(), "stale": boolSchema(),
	},
}

var viewsListOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"items"},
	Properties: map[string]*jsonschema.Schema{
		"items":       {Type: "array", Items: viewSummarySchema},
		"next_cursor": stringSchema(),
	},
}

var pointSchema = &jsonschema.Schema{
	Type:       "object",
	Required:   []string{"x", "y"},
	Properties: map[string]*jsonschema.Schema{"x": numberSchema(), "y": numberSchema()},
}

var viewOutputSchema = &jsonschema.Schema{
	Type: "object",
	Required: []string{"id", "key", "name", "query", "renderer", "renderer_params",
		"layout_mode", "background_scale", "background_offset", "version", "updated_at"},
	Properties: map[string]*jsonschema.Schema{
		"id": stringSchema(), "key": stringSchema(), "name": stringSchema(),
		"description": stringSchema(), "query": objectSchema(), "renderer": stringSchema(),
		"renderer_params": objectSchema(), "layout_mode": stringSchema(),
		"layout_seed": integerSchema(), "background_asset_id": stringSchema(),
		"background_scale": numberSchema(), "background_offset": pointSchema,
		"version": integerSchema(), "updated_at": stringSchema(),
	},
}

// queryDocumentInput is the input schema of a tool that takes a query
// document, and it exists because the SDK infers a schema from the Go
// type and json.RawMessage is a []byte.
//
// Inferred, `query` arrives on the wire as `{"type": ["null", "array"]}`
// — an array of bytes — so every call sending the document as the object
// it is was refused by input validation before any handler ran. The three
// tools that take one were therefore **uncallable over the real
// transport**, while every test in this package passed: they call the
// MCP* functions directly, which is where the isolation invariant is
// pinned and where nothing crosses a schema.
//
// The substitution is made at the one type that has the problem rather
// than by hand-writing three schemas, so a member added to any of these
// inputs still appears without anybody remembering to add it — the drift
// a hand-written schema invites. `{}` and not `{"type": "object"}`:
// ParseQuery is what judges a document, at the pointers an agent can act
// on, and a schema that refused a non-object first would answer the same
// mistake with a worse message.
func queryDocumentInput[T any](tool string) *jsonschema.Schema {
	schema, err := jsonschema.For[T](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[json.RawMessage](): {},
		},
	})
	if err != nil {
		// Registration time, and mcp.AddTool panics on a bad schema for
		// the same reason: a tool whose arguments cannot be described is
		// not a tool a server can serve.
		panic(fmt.Sprintf("input schema for %s: %v", tool, err))
	}
	return schema
}

var viewsRemovedOutputSchema = &jsonschema.Schema{
	Type:       "object",
	Required:   []string{"removed"},
	Properties: map[string]*jsonschema.Schema{"removed": boolSchema()},
}

// viewsRunOutputSchema leaves `nodes`, `edges` and `positions` as arrays
// of open objects rather than enumerating a node's members.
//
// That is a deliberate limit and not an omission. A node's `attrs` is
// keyed by the *projection slots this document declared*, and its
// `fields` by the *game's own field keys*, so the shape of one node is a
// property of the query that drew it — a schema pinning it here would be
// a schema that is wrong for every document but one. The three members
// that are the same for every run — `stats`, `truncated` and the
// distinction between an absent and an empty `positions` — are pinned.
var viewsRunOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"nodes", "edges", "stats", "truncated"},
	Properties: map[string]*jsonschema.Schema{
		"nodes": {Type: "array", Items: objectSchema()},
		"edges": {Type: "array", Items: objectSchema()},
		"stats": {
			Type: "object",
			// The four members views.Stats actually carries. They were
			// spelled node_count and edge_count here and nodes and edges
			// on the struct, which the SDK enforces on the way out: every
			// run over the real transport failed output validation, and
			// nothing in this package read the schema, so nothing said
			// so. TestEveryViewsToolIsCallableOverTheRealTransport is
			// what reads it now.
			Required: []string{"duration_ms", "nodes", "edges", "max_depth_reached"},
			Properties: map[string]*jsonschema.Schema{
				"duration_ms": integerSchema(), "nodes": integerSchema(),
				"edges": integerSchema(), "max_depth_reached": integerSchema(),
			},
		},
		"truncated": {
			Type:     "object",
			Required: []string{"nodes", "edges", "depth"},
			Properties: map[string]*jsonschema.Schema{
				"nodes": boolSchema(), "edges": boolSchema(), "depth": boolSchema(),
			},
		},
		"stale":     {Type: "array", Items: objectSchema()},
		"positions": {Type: "array", Items: objectSchema()},
	},
}

var viewsValidateOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"valid", "refs", "limits"},
	Properties: map[string]*jsonschema.Schema{
		"valid": boolSchema(),
		"refs": {Type: "array", Items: &jsonschema.Schema{
			Type:     "object",
			Required: []string{"kind", "key", "pointer"},
			Properties: map[string]*jsonschema.Schema{
				"kind": stringSchema(), "key": stringSchema(), "pointer": stringSchema(),
			},
		}},
		"limits": {
			Type:     "object",
			Required: []string{"max_depth", "max_nodes", "max_edges"},
			Properties: map[string]*jsonschema.Schema{
				"max_depth": integerSchema(), "max_nodes": integerSchema(),
				"max_edges": integerSchema(),
			},
		},
	},
}

var viewsPositionsWrittenOutputSchema = &jsonschema.Schema{
	Type:       "object",
	Required:   []string{"written"},
	Properties: map[string]*jsonschema.Schema{"written": integerSchema()},
}

var viewsPositionsRemovedOutputSchema = &jsonschema.Schema{
	Type:       "object",
	Required:   []string{"removed"},
	Properties: map[string]*jsonschema.Schema{"removed": integerSchema()},
}

var viewAssetSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"id", "filename", "mime", "width", "height", "created_at", "url"},
	Properties: map[string]*jsonschema.Schema{
		"id": stringSchema(), "filename": stringSchema(), "mime": stringSchema(),
		"width": integerSchema(), "height": integerSchema(),
		"created_at": stringSchema(), "url": stringSchema(),
	},
}

var viewsAssetsOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"items"},
	Properties: map[string]*jsonschema.Schema{
		"items":       {Type: "array", Items: viewAssetSchema},
		"next_cursor": stringSchema(),
	},
}
