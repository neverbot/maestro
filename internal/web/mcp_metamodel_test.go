package web_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/testutil"
	"github.com/neverbot/maestro/internal/web"
)

// newMetamodelTestServer wires a server with a metamodel service over the
// same ephemeral database, and hands back both.
//
// It is a second helper rather than a change to newTestServer's return
// values: every one of newTestServer's several dozen callers would have
// had to grow an ignored fourth result to reach a service none of them
// use, and a Server built without a metamodel service is a shape this
// package supports deliberately (MCPDeps.Metamodel).
func newMetamodelTestServer(t *testing.T) (*web.Server, *identity.Service, *projects.Service, *metamodel.Service) {
	t.Helper()
	pool := testutil.NewPool(t)
	cfg := config.Config{
		SessionTTL: testConfig().SessionTTL,
		InviteTTL:  testConfig().InviteTTL,
		Argon2:     testConfig().Argon2,
	}
	ids := identity.New(pool, cfg)
	projSvc := projects.New(pool)
	mm := metamodel.New(pool, nil)
	srv := web.NewServer(web.Options{
		Version:   "test",
		Config:    cfg,
		Identity:  ids,
		Projects:  projSvc,
		Metamodel: mm,
	})
	return srv, ids, projSvc, mm
}

// metamodelFixture is everything a tool test needs: the deps struct the
// plain MCP* functions take, a caller holding a real token for `game`,
// and a second game the same user owns, to prove isolation is about the
// token's binding and not about ownership.
type metamodelFixture struct {
	deps   web.MCPDeps
	caller web.Caller
	game   uuid.UUID
	other  uuid.UUID
	token  string
	srv    *web.Server
}

func newMetamodelFixture(t *testing.T) metamodelFixture {
	t.Helper()
	srv, ids, projSvc, mm := newMetamodelTestServer(t)
	ctx := context.Background()

	user, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	game, err := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	if err != nil {
		t.Fatalf("Create game: %v", err)
	}
	other, err := projSvc.Create(ctx, "le-mans", "Le Mans", user.ID)
	if err != nil {
		t.Fatalf("Create other game: %v", err)
	}
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: game.ID, UserID: user.ID, Label: "agent",
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	caller, err := web.CallerForToken(ctx, ids, token)
	if err != nil {
		t.Fatalf("CallerForToken: %v", err)
	}
	return metamodelFixture{
		deps:   web.MCPDeps{Identity: ids, Projects: projSvc, Metamodel: mm},
		caller: caller,
		game:   game.ID,
		other:  other.ID,
		token:  token,
		srv:    srv,
	}
}

func TestMCPTypesUpsertAndList(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	created, err := web.MCPTypesUpsert(ctx, f.deps, f.caller, f.game, web.TypesUpsertInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: []web.FieldInput{{Key: "min_level", Type: "number", Required: true}},
	})
	if err != nil {
		t.Fatalf("MCPTypesUpsert: %v", err)
	}
	if created.Key != "quest" || created.Version != 1 {
		t.Fatalf("created = %+v, want key quest at version 1", created)
	}
	if len(created.Schema) != 1 || created.Schema[0].Key != "min_level" {
		t.Fatalf("schema = %+v, want the declared field back", created.Schema)
	}

	list, err := web.MCPTypesList(ctx, f.deps, f.caller, f.game, web.TypesListInput{})
	if err != nil {
		t.Fatalf("MCPTypesList: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].Key != "quest" {
		t.Fatalf("types = %+v", list.Items)
	}
	if list.Items[0].ID != created.ID {
		t.Fatalf("the listing's id %s is not the created type's %s", list.Items[0].ID, created.ID)
	}

	got, err := web.MCPTypesGet(ctx, f.deps, f.caller, f.game, web.TypesGetInput{Key: "QUEST"})
	if err != nil {
		t.Fatalf("MCPTypesGet: %v", err)
	}
	if got.ID != created.ID {
		t.Fatalf("a key matched without regard to case found %s, want %s", got.ID, created.ID)
	}
}

// TestMCPToolsRefuseAnotherGame is the isolation test for this whole
// surface: every tool that takes a project id is called with the id of a
// game the caller's own *user* owns but the caller's own *token* is not
// bound to, and every one must refuse before touching the database.
//
// One table rather than one test per tool, because the invariant is the
// same one sixteen times and a per-tool test is sixteen chances to
// forget the seventeenth.
func TestMCPToolsRefuseAnotherGame(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()
	id := uuid.New().String()

	calls := map[string]func() error{
		"types.upsert": func() error {
			_, err := web.MCPTypesUpsert(ctx, f.deps, f.caller, f.other, web.TypesUpsertInput{
				Key: "circuit", Label: "Circuit", LabelPlural: "Circuits",
			})
			return err
		},
		"types.list": func() error {
			_, err := web.MCPTypesList(ctx, f.deps, f.caller, f.other, web.TypesListInput{})
			return err
		},
		"types.get": func() error {
			_, err := web.MCPTypesGet(ctx, f.deps, f.caller, f.other, web.TypesGetInput{Key: "circuit"})
			return err
		},
		"types.remove": func() error {
			_, err := web.MCPTypesRemove(ctx, f.deps, f.caller, f.other, web.TypesRemoveInput{ID: id})
			return err
		},
		"relation_types.upsert": func() error {
			_, err := web.MCPRelationTypesUpsert(ctx, f.deps, f.caller, f.other, web.RelationTypesUpsertInput{
				Key: "races_on", Label: "races on",
			})
			return err
		},
		"relation_types.list": func() error {
			_, err := web.MCPRelationTypesList(ctx, f.deps, f.caller, f.other, web.RelationTypesListInput{})
			return err
		},
		"relation_types.get": func() error {
			_, err := web.MCPRelationTypesGet(ctx, f.deps, f.caller, f.other, web.RelationTypesGetInput{Key: "races_on"})
			return err
		},
		"relation_types.remove": func() error {
			_, err := web.MCPRelationTypesRemove(ctx, f.deps, f.caller, f.other, web.RelationTypesRemoveInput{ID: id})
			return err
		},
		"entities.upsert": func() error {
			_, err := web.MCPEntitiesUpsert(ctx, f.deps, f.caller, f.other, web.EntitiesUpsertInput{
				Items: []web.EntityItemInput{{TypeKey: "circuit", Key: "spa", Name: "Spa"}},
			})
			return err
		},
		"entities.list": func() error {
			_, err := web.MCPEntitiesList(ctx, f.deps, f.caller, f.other, web.EntitiesListInput{})
			return err
		},
		"entities.get": func() error {
			_, err := web.MCPEntitiesGet(ctx, f.deps, f.caller, f.other, web.EntitiesGetInput{TypeKey: "circuit", Key: "spa"})
			return err
		},
		"entities.remove": func() error {
			_, err := web.MCPEntitiesRemove(ctx, f.deps, f.caller, f.other, web.EntitiesRemoveInput{ID: id})
			return err
		},
		"relations.upsert": func() error {
			_, err := web.MCPRelationsUpsert(ctx, f.deps, f.caller, f.other, web.RelationsUpsertInput{
				Items: []web.RelationItemInput{{TypeKey: "races_on"}},
			})
			return err
		},
		"relations.list": func() error {
			_, err := web.MCPRelationsList(ctx, f.deps, f.caller, f.other, web.RelationsListInput{})
			return err
		},
		"relations.remove": func() error {
			_, err := web.MCPRelationsRemove(ctx, f.deps, f.caller, f.other, web.RelationsRemoveInput{ID: id})
			return err
		},
		"search": func() error {
			_, err := web.MCPSearch(ctx, f.deps, f.caller, f.other, web.SearchInput{Query: "spa"})
			return err
		},
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, web.ErrScopeViolation) {
				t.Fatalf("err = %v, want ErrScopeViolation", err)
			}
		})
	}

	// Nothing may have landed in the other game, which is the assertion
	// a refusal that merely returned early would still pass.
	types, err := web.MCPTypesList(ctx, f.deps, f.caller, f.game, web.TypesListInput{})
	if err != nil {
		t.Fatalf("MCPTypesList: %v", err)
	}
	if len(types.Items) != 0 {
		t.Fatalf("a refused call wrote into a game after all: %+v", types.Items)
	}
}

func TestMCPEntitiesUpsertBulkReportsFailuresAndWhatLanded(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	if _, err := web.MCPTypesUpsert(ctx, f.deps, f.caller, f.game, web.TypesUpsertInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: []web.FieldInput{{Key: "min_level", Type: "number", Required: true}},
	}); err != nil {
		t.Fatalf("MCPTypesUpsert: %v", err)
	}

	out, err := web.MCPEntitiesUpsert(ctx, f.deps, f.caller, f.game, web.EntitiesUpsertInput{
		Mode: string(metamodel.BulkPartial),
		Items: []web.EntityItemInput{
			{TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)}},
			{TypeKey: "quest", Key: "b", Name: "B", Fields: map[string]any{"min_level": "nope"}},
		},
	})
	if err != nil {
		t.Fatalf("MCPEntitiesUpsert: %v", err)
	}
	if out.Count != 1 || len(out.Written) != 1 {
		t.Fatalf("count = %d, written = %+v, want exactly the one row that landed", out.Count, out.Written)
	}
	if out.Written[0].Key != "a" || out.Written[0].Version != 1 {
		t.Fatalf("written[0] = %+v, want key a at version 1", out.Written[0])
	}
	if len(out.Failed) != 1 || out.Failed[0].Code != "schema_violation" || out.Failed[0].Index != 1 {
		t.Fatalf("failed = %+v, want one schema_violation at index 1", out.Failed)
	}
}

// TestMCPEntitiesUpsertRecordsTheCallersOwnToken pins the one thing the
// wire input types exist for: an agent cannot name the author of its own
// writes. metamodel.EntityInput carries an Actor; EntityItemInput does
// not, and actorOf builds one from the authenticated caller.
func TestMCPEntitiesUpsertRecordsTheCallersOwnToken(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	if _, err := web.MCPTypesUpsert(ctx, f.deps, f.caller, f.game, web.TypesUpsertInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	}); err != nil {
		t.Fatalf("MCPTypesUpsert: %v", err)
	}
	if _, err := web.MCPEntitiesUpsert(ctx, f.deps, f.caller, f.game, web.EntitiesUpsertInput{
		Items: []web.EntityItemInput{{TypeKey: "quest", Key: "a", Name: "A"}},
	}); err != nil {
		t.Fatalf("MCPEntitiesUpsert: %v", err)
	}

	row, err := f.deps.Metamodel.EntityByKey(ctx, f.game, "quest", "a")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	if row.UpdatedByTokenID == nil || *row.UpdatedByTokenID != *f.caller.TokenID {
		t.Fatalf("updated_by_token_id = %v, want the calling token %v", row.UpdatedByTokenID, f.caller.TokenID)
	}
	if row.UpdatedByUserID == nil || *row.UpdatedByUserID != f.caller.UserID {
		t.Fatalf("updated_by_user_id = %v, want the calling user %v", row.UpdatedByUserID, f.caller.UserID)
	}
}

// TestMCPMalformedIDIsTheCallersOwnArgument pins that an id this layer
// parses is refused as invalid_input at that argument's own path, not as
// an internal error — the rule the metamodel applies to every other
// caller-supplied value.
func TestMCPMalformedIDIsTheCallersOwnArgument(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	for name, call := range map[string]func() error{
		"types.remove": func() error {
			_, err := web.MCPTypesRemove(ctx, f.deps, f.caller, f.game, web.TypesRemoveInput{ID: "not-a-uuid"})
			return err
		},
		"entities.remove": func() error {
			_, err := web.MCPEntitiesRemove(ctx, f.deps, f.caller, f.game, web.EntitiesRemoveInput{ID: "not-a-uuid"})
			return err
		},
		"relations.list source_id": func() error {
			_, err := web.MCPRelationsList(ctx, f.deps, f.caller, f.game, web.RelationsListInput{SourceID: "not-a-uuid"})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			var mcpErr *web.MCPError
			if !errors.As(err, &mcpErr) {
				t.Fatalf("err = %v, want an *MCPError", err)
			}
			if mcpErr.Code != "invalid_input" {
				t.Fatalf("code = %q, want invalid_input", mcpErr.Code)
			}
			if !strings.Contains(mcpErr.Message, "valid uuid") {
				t.Fatalf("message = %q, want it to say what is wrong", mcpErr.Message)
			}
		})
	}

	// An endpoint list names the element at fault, not just the list.
	_, err := web.MCPRelationTypesUpsert(ctx, f.deps, f.caller, f.game, web.RelationTypesUpsertInput{
		Key: "requires", Label: "requires",
		SourceTypeIDs: []string{uuid.New().String(), "nope"},
	})
	var mcpErr *web.MCPError
	if !errors.As(err, &mcpErr) || mcpErr.Code != "invalid_input" {
		t.Fatalf("err = %v, want an invalid_input MCPError", err)
	}
	if !strings.Contains(mcpErr.Message, "source_type_ids[1]") {
		t.Fatalf("message = %q, want it to name the element at fault", mcpErr.Message)
	}
}

// TestMCPMetamodelToolsAreServedOverTheRealTransport is the end-to-end
// pass: a real token, a real MCP client, and the whole seeding round trip
// an agent actually performs — declare two types, declare an edge type
// between them, seed entities, join them, then find one by search.
//
// It is here rather than in mcp_http_test.go because it needs a server
// built with a metamodel service; what it adds over the direct-function
// tests above is everything the SDK does on the way — input schema
// validation against the reflected Go types, output validation against
// the hand-written schemas, and JSON marshalling of every uuid.
func TestMCPMetamodelToolsAreServedOverTheRealTransport(t *testing.T) {
	f := newMetamodelFixture(t)
	httpSrv := httptest.NewServer(f.srv)
	defer httpSrv.Close()
	ctx := context.Background()

	session := connectMCP(t, httpSrv.URL, f.token)

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{
		"types.upsert", "types.list", "types.get", "types.remove",
		"relation_types.upsert", "relation_types.list", "relation_types.get", "relation_types.remove",
		"entities.upsert", "entities.list", "entities.get", "entities.remove",
		"relations.upsert", "relations.list", "relations.remove", "search",
	} {
		if !names[want] {
			t.Fatalf("the served tool list is missing %q", want)
		}
	}

	var questType struct {
		ID string `json:"id"`
	}
	decodeStructured(t, callOK(t, session, "types.upsert", map[string]any{
		"key": "quest", "label": "Quest", "label_plural": "Quests",
		"field_schema": []any{
			map[string]any{"key": "min_level", "type": "number", "required": true},
			map[string]any{"key": "summary", "type": "longtext"},
			// A default of false is the case a bare `any` cannot tell
			// from "no default declared"; it round-trips through
			// metamodel's own decoder.
			map[string]any{"key": "repeatable", "type": "bool", "default": false},
		},
	}), &questType)

	var zoneType struct {
		ID string `json:"id"`
	}
	decodeStructured(t, callOK(t, session, "types.upsert", map[string]any{
		"key": "zone", "label": "Zone", "label_plural": "Zones",
	}), &zoneType)

	callOK(t, session, "relation_types.upsert", map[string]any{
		"key": "takes_place_in", "label": "takes place in",
		"source_type_ids": []any{questType.ID},
		"target_type_ids": []any{zoneType.ID},
		"semantic_role":   "spatial",
	})

	var seeded struct {
		Count   int `json:"count"`
		Written []struct {
			Key     string `json:"key"`
			ID      string `json:"id"`
			Version int32  `json:"version"`
		} `json:"written"`
		Failed []map[string]any `json:"failed"`
	}
	decodeStructured(t, callOK(t, session, "entities.upsert", map[string]any{
		"items": []any{
			map[string]any{"type_key": "quest", "key": "hogger", "name": "Wanted: Hogger",
				"fields": map[string]any{"min_level": 10, "summary": "Defeat the gnoll chieftain."}},
			map[string]any{"type_key": "zone", "key": "elwynn", "name": "Elwynn Forest"},
		},
	}), &seeded)
	if seeded.Count != 2 || len(seeded.Written) != 2 || len(seeded.Failed) != 0 {
		t.Fatalf("seed = %+v, want two rows written and none failed", seeded)
	}
	if seeded.Written[0].Version != 1 || seeded.Written[0].ID == "" {
		t.Fatalf("written[0] = %+v, want an id and version 1", seeded.Written[0])
	}

	callOK(t, session, "relations.upsert", map[string]any{
		"items": []any{map[string]any{
			"type_key": "takes_place_in",
			"source":   map[string]any{"type_key": "quest", "key": "hogger"},
			"target":   map[string]any{"type_key": "zone", "key": "elwynn"},
		}},
	})

	// The traversal answers in the terms the caller wrote, which is the
	// half relations.list cannot do.
	var related struct {
		Items []struct {
			TypeKey string `json:"type_key"`
			Key     string `json:"key"`
		} `json:"items"`
	}
	decodeStructured(t, callOK(t, session, "entities.list", map[string]any{
		"related_to": map[string]any{
			"relation_type_key": "takes_place_in",
			"entity_type_key":   "quest",
			"entity_key":        "hogger",
			"direction":         "outgoing",
		},
	}), &related)
	if len(related.Items) != 1 || related.Items[0].Key != "elwynn" || related.Items[0].TypeKey != "zone" {
		t.Fatalf("related = %+v, want the zone the quest takes place in", related.Items)
	}

	var found struct {
		Items []struct {
			Key    string  `json:"key"`
			Rank   float64 `json:"rank"`
			Fields map[string]any
		} `json:"items"`
		Truncated bool `json:"truncated"`
	}
	decodeStructured(t, callOK(t, session, "search", map[string]any{"query": "gnoll"}), &found)
	if len(found.Items) != 1 || found.Items[0].Key != "hogger" {
		t.Fatalf("search = %+v, want the quest whose summary carries the word", found.Items)
	}
	if found.Truncated {
		t.Fatal("one hit under the default limit must not report the answer as truncated")
	}

	// A listing without verbose carries no fields; with it, it does.
	var slim struct {
		Items []struct {
			Key    string         `json:"key"`
			Fields map[string]any `json:"fields"`
		} `json:"items"`
	}
	decodeStructured(t, callOK(t, session, "entities.list", map[string]any{"type_key": "quest"}), &slim)
	if len(slim.Items) != 1 || slim.Items[0].Fields != nil {
		t.Fatalf("a slim listing carried fields: %+v", slim.Items)
	}
	decodeStructured(t, callOK(t, session, "entities.list",
		map[string]any{"type_key": "quest", "verbose": true}), &slim)
	if len(slim.Items) != 1 || slim.Items[0].Fields["min_level"] == nil {
		t.Fatalf("a verbose listing carried no fields: %+v", slim.Items)
	}
}

// TestMCPMetamodelErrorsCarryTheirOwnCode pins that the domain's error
// vocabulary survives the transport: an agent reads a code, not prose,
// and each of these has a different recovery.
func TestMCPMetamodelErrorsCarryTheirOwnCode(t *testing.T) {
	f := newMetamodelFixture(t)
	httpSrv := httptest.NewServer(f.srv)
	defer httpSrv.Close()
	ctx := context.Background()
	session := connectMCP(t, httpSrv.URL, f.token)

	callOK(t, session, "types.upsert", map[string]any{
		"key": "quest", "label": "Quest", "label_plural": "Quests",
		"field_schema": []any{map[string]any{"key": "min_level", "type": "number", "required": true}},
	})
	callOK(t, session, "entities.upsert", map[string]any{
		"items": []any{map[string]any{"type_key": "quest", "key": "hogger", "name": "Hogger",
			"fields": map[string]any{"min_level": 10}}},
	})
	var zone struct {
		ID string `json:"id"`
	}
	decodeStructured(t, callOK(t, session, "types.upsert", map[string]any{
		"key": "zone", "label": "Zone", "label_plural": "Zones",
	}), &zone)
	// leads_to may only end at a zone, so an edge ending at a quest is
	// the endpoint rule being enforced and not a missing row.
	callOK(t, session, "relation_types.upsert", map[string]any{
		"key": "leads_to", "label": "leads to",
		"target_type_ids": []any{zone.ID},
	})

	for _, tc := range []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"an unknown type", "types.get", map[string]any{"key": "nope"}, "not_found"},
		{"an unknown entity", "entities.get",
			map[string]any{"type_key": "quest", "key": "nope"}, "not_found"},
		{"a schema declaration that cannot stand", "types.upsert",
			map[string]any{"key": "broken", "label": "B", "label_plural": "Bs",
				"field_schema": []any{map[string]any{"key": "f", "type": "enum"}}}, "invalid_schema"},
		{"a malformed key", "types.upsert",
			map[string]any{"key": "not a key", "label": "B", "label_plural": "Bs"}, "invalid_input"},
		{"an update with no version claim", "types.upsert",
			map[string]any{"key": "quest", "label": "Quest", "label_plural": "Quests"}, "version_conflict"},
		{"a type that is still in use", "types.remove",
			map[string]any{"id": uuid.Nil.String()}, "not_found"},
		{"a query with no word in it", "search", map[string]any{"query": "..."}, "invalid_input"},
		// An atomic batch reports its one bad row as an error rather
		// than as a failed entry, which is the only path that puts a
		// schema_violation or an endpoint_type_mismatch through
		// mcpErrorFor rather than through failureFor.
		{"a value that does not fit the schema", "entities.upsert",
			map[string]any{"mode": "atomic", "items": []any{
				map[string]any{"type_key": "quest", "key": "bad", "name": "Bad",
					"fields": map[string]any{"min_level": "nope"}},
			}}, "schema_violation"},
		{"an edge whose end is the wrong type", "relations.upsert",
			map[string]any{"mode": "atomic", "items": []any{
				map[string]any{"type_key": "leads_to",
					"source": map[string]any{"type_key": "quest", "key": "hogger"},
					"target": map[string]any{"type_key": "quest", "key": "hogger"}},
			}}, "endpoint_type_mismatch"},
		{"a mode that is neither", "entities.upsert",
			map[string]any{"mode": "all-or-nothing", "items": []any{}}, "invalid_input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
			if err != nil {
				t.Fatalf("CallTool(%s): %v", tc.tool, err)
			}
			if !result.IsError {
				t.Fatalf("%s must have failed, got %+v", tc.tool, result.StructuredContent)
			}
			if result.StructuredContent != nil {
				t.Fatalf("an error result must carry no StructuredContent, got %#v", result.StructuredContent)
			}
			var wireErr struct {
				Error string `json:"error"`
			}
			decodeToolText(t, result, &wireErr)
			if wireErr.Error != tc.want {
				t.Fatalf("error = %q, want %q", wireErr.Error, tc.want)
			}
		})
	}

	// The one that is refused as in_use rather than not_found needs a
	// real type id, so it is done separately.
	var quest struct {
		ID string `json:"id"`
	}
	decodeStructured(t, callOK(t, session, "types.get", map[string]any{"key": "quest"}), &quest)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "types.remove", Arguments: map[string]any{"id": quest.ID},
	})
	if err != nil {
		t.Fatalf("CallTool(types.remove): %v", err)
	}
	var wireErr struct {
		Error string `json:"error"`
	}
	decodeToolText(t, result, &wireErr)
	if wireErr.Error != "in_use" {
		t.Fatalf("removing a type that still has entities reported %q, want in_use", wireErr.Error)
	}
}

// callOK calls a tool and fails the test unless it succeeded.
func callOK(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if result.IsError {
		t.Fatalf("CallTool(%s) reported an error: %+v", name, result.Content)
	}
	return result
}

// TestTheTraversalPagesAndItsDirectionIsRequiredOnTheWire closes review
// findings H1 and H2 at the layer where both were wrong: the wire.
//
// H1: `entities.list`'s description claimed the `related_to` traversal
// was not paged, never set `next_cursor`, and silently dropped every
// neighbour past `MaxEntityPage`. All three clauses were false —
// `listRelated` has always used the same `pageSize`, cursor and `pageOf`
// as the plain listing — and the description sent an agent to
// `relations.list` as the escape hatch for a case that does not exist.
// An agent believing it would have stopped at the first page holding a
// whole neighbourhood it had only part of. The domain side was already
// pinned by TestATraversalPagesLikeEveryOtherListing (internal/metamodel);
// what was missing was a guard at the wire, where the sentence lives.
//
// H2: `direction` was `omitempty`, so the served input schema left it
// out of `required`, and the description called "outgoing" its default —
// while `listRelated` refused an absent one outright. The domain's
// refusal is the right call; this test pins that the schema now says so
// too, and that omitting the field cannot produce a listing.
func TestTheTraversalPagesAndItsDirectionIsRequiredOnTheWire(t *testing.T) {
	f := newMetamodelFixture(t)
	httpSrv := httptest.NewServer(f.srv)
	defer httpSrv.Close()
	ctx := context.Background()

	session := connectMCP(t, httpSrv.URL, f.token)

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var list *mcp.Tool
	for _, tool := range tools.Tools {
		if tool.Name == "entities.list" {
			list = tool
		}
	}
	if list == nil {
		t.Fatal("entities.list is not served")
	}
	// InputSchema crosses the wire as raw JSON, so the served schema is
	// read as JSON rather than as a Go type — which is also the shape an
	// agent's client sees.
	raw, err := json.Marshal(list.InputSchema)
	if err != nil {
		t.Fatalf("marshal input schema: %v", err)
	}
	var served struct {
		Properties struct {
			RelatedTo struct {
				Required []string `json:"required"`
			} `json:"related_to"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &served); err != nil {
		t.Fatalf("decode input schema: %v", err)
	}
	if !slices.Contains(served.Properties.RelatedTo.Required, "direction") {
		t.Fatalf("related_to.required = %v, want direction among them: %s",
			served.Properties.RelatedTo.Required, raw)
	}

	var questType, zoneType struct {
		ID string `json:"id"`
	}
	decodeStructured(t, callOK(t, session, "types.upsert", map[string]any{
		"key": "quest", "label": "Quest", "label_plural": "Quests",
	}), &questType)
	decodeStructured(t, callOK(t, session, "types.upsert", map[string]any{
		"key": "zone", "label": "Zone", "label_plural": "Zones",
	}), &zoneType)
	callOK(t, session, "relation_types.upsert", map[string]any{
		"key": "takes_place_in", "label": "takes place in",
	})

	items := []any{map[string]any{"type_key": "zone", "key": "elwynn", "name": "Elwynn Forest"}}
	edges := []any{}
	for i := range 3 {
		key := fmt.Sprintf("quest-%d", i)
		items = append(items, map[string]any{
			"type_key": "quest", "key": key, "name": fmt.Sprintf("Quest %d", i),
		})
		edges = append(edges, map[string]any{
			"type_key": "takes_place_in",
			"source":   map[string]any{"type_key": "quest", "key": key},
			"target":   map[string]any{"type_key": "zone", "key": "elwynn"},
		})
	}
	callOK(t, session, "entities.upsert", map[string]any{"items": items})
	callOK(t, session, "relations.upsert", map[string]any{"items": edges})

	anchor := func(extra map[string]any) map[string]any {
		rel := map[string]any{
			"relation_type_key": "takes_place_in",
			"entity_type_key":   "zone",
			"entity_key":        "elwynn",
			"direction":         "incoming",
		}
		args := map[string]any{"related_to": rel, "limit": 2}
		for k, v := range extra {
			args[k] = v
		}
		return args
	}

	var page struct {
		Items []struct {
			Key string `json:"key"`
		} `json:"items"`
		NextCursor string `json:"next_cursor"`
	}
	decodeStructured(t, callOK(t, session, "entities.list", anchor(nil)), &page)
	if len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("first traversal page = %+v, want two neighbours and a cursor", page)
	}

	seen := []string{page.Items[0].Key, page.Items[1].Key}
	decodeStructured(t, callOK(t, session, "entities.list",
		anchor(map[string]any{"cursor": page.NextCursor})), &page)
	if len(page.Items) != 1 {
		t.Fatalf("second traversal page = %+v, want the remaining neighbour", page.Items)
	}
	seen = append(seen, page.Items[0].Key)
	if want := []string{"quest-0", "quest-1", "quest-2"}; !slices.Equal(seen, want) {
		t.Fatalf("paged over %v, want %v", seen, want)
	}

	// Omitting direction cannot answer with a listing. Whether it is the
	// SDK's own schema validation or the domain's refusal that stops it
	// is not this test's business — that it is stopped, is.
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "entities.list",
		Arguments: map[string]any{"related_to": map[string]any{
			"relation_type_key": "takes_place_in",
			"entity_type_key":   "zone",
			"entity_key":        "elwynn",
		}},
	})
	if err == nil && !result.IsError {
		t.Fatalf("a traversal with no direction was answered: %+v", result.StructuredContent)
	}
}
