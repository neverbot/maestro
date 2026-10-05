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

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/testutil"
	"github.com/neverbot/maestro/internal/web"
)

// newMetamodelTestServer wires a server with a metamodel service over the
// same ephemeral database, and hands back both.
func newMetamodelTestServer(t *testing.T) (*web.Server, *identity.Service, *projects.Service, *metamodel.Service, *markdown.Service) {
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
	// The markdown service shares the pool, because `search` unions the
	// two indexes and a fixture holding only one of them cannot see the
	// merge at all.
	md := markdown.New(pool, nil)
	srv := web.NewServer(web.Options{
		Version:   "test",
		Config:    cfg,
		Identity:  ids,
		Projects:  projSvc,
		Metamodel: mm,
		Markdown:  md,
	})
	return srv, ids, projSvc, mm, md
}

// metamodelFixture is everything a tool test needs: the deps struct the
// plain MCP* functions take, a caller holding a real token for `game`,
// and a second game the same user owns, to prove isolation is about the
// token's binding and not about ownership.
type metamodelFixture struct {
	deps      web.MCPDeps
	caller    web.Caller
	game      uuid.UUID
	gameSlug  string
	other     uuid.UUID
	otherSlug string
	token     string
	srv       *web.Server
	// markdown is the prose domain behind f.deps, handed out so a test
	// can seed documents the way the metamodel ones seed entities.
	markdown *markdown.Service
}

func newMetamodelFixture(t *testing.T) metamodelFixture {
	t.Helper()
	srv, ids, projSvc, mm, md := newMetamodelTestServer(t)
	ctx := context.Background()

	user, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "designer@example.test", DisplayName: "Designer", Password: "password12345",
	})
	assert.Must(t, err == nil, "CreateUser: %v", err)
	game, err := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	assert.Must(t, err == nil, "Create game: %v", err)
	other, err := projSvc.Create(ctx, "le-mans", "Le Mans", user.ID)
	assert.Must(t, err == nil, "Create other game: %v", err)
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: game.ID, UserID: user.ID, Label: "agent",
	})
	assert.Must(t, err == nil, "CreateAPIToken: %v", err)
	caller, err := web.CallerForToken(ctx, ids, token)
	assert.Must(t, err == nil, "CallerForToken: %v", err)
	return metamodelFixture{
		deps:      web.MCPDeps{Identity: ids, Projects: projSvc, Metamodel: mm, Markdown: md},
		caller:    caller,
		game:      game.ID,
		gameSlug:  game.Slug,
		other:     other.ID,
		otherSlug: other.Slug,
		token:     token,
		srv:       srv,

		markdown: md,
	}
}

func TestMCPTypesUpsertAndList(t *testing.T) {
	t.Parallel()
	f := newMetamodelFixture(t)
	ctx := context.Background()

	created, err := web.MCPTypesUpsert(ctx, f.deps, f.caller, f.game, web.TypesUpsertInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: []web.FieldInput{{Key: "min_level", Type: "number", Required: true}},
	})
	assert.Must(t, err == nil, "MCPTypesUpsert: %v", err)
	assert.Must(t, created.Key == "quest" && created.Version == 1, "created = %+v, want key quest at version 1", created)
	assert.Must(t, len(created.Schema) == 1 && created.Schema[0].Key == "min_level", "schema = %+v, want the declared field back", created.Schema)

	list, err := web.MCPTypesList(ctx, f.deps, f.caller, f.game, web.TypesListInput{})
	assert.Must(t, err == nil, "MCPTypesList: %v", err)
	assert.Must(t, len(list.Items) == 1 && list.Items[0].Key == "quest", "types = %+v", list.Items)
	if list.Items[0].ID != created.ID {
		t.Fatalf("the listing's id %s is not the created type's %s", list.Items[0].ID, created.ID)
	}

	got, err := web.MCPTypesGet(ctx, f.deps, f.caller, f.game, web.TypesGetInput{Key: "QUEST"})
	assert.Must(t, err == nil, "MCPTypesGet: %v", err)
	assert.Must(t, got.ID == created.ID, "a key matched without regard to case found %s, want %s", got.ID, created.ID)
}

// TestAVersionClaimAgainstAMissingRowIsNotFoundOnTheWire is the
// surface's half of Metamodel 17, and it is here because the two wire
// codes are the whole point of the domain's discrimination: `not_found`
// tells an agent to decide whether to re-create the row, and
// `version_conflict` tells it to merge — and merging is the one recovery
// that cannot work against a row that is gone.
func TestAVersionClaimAgainstAMissingRowIsNotFoundOnTheWire(t *testing.T) {
	t.Parallel()
	f := newMetamodelFixture(t)
	ctx := context.Background()

	_, err := web.MCPTypesUpsert(ctx, f.deps, f.caller, f.game, web.TypesUpsertInput{
		Key: "circuit", Label: "Circuit", LabelPlural: "Circuits",
		ExpectedVersion: ptrInt32Web(1),
	})
	assert.Must(t, errors.Is(err, metamodel.ErrNotFound), "err = %v, want not_found", err)
	assert.Must(t, !errors.Is(err, metamodel.ErrVersionConflict), "err = %v, want not_found and not version_conflict on the wire too", err)
	assert.Must(t, strings.Contains(err.Error(), "was removed"), "err = %v, want the message an agent acts on", err)
	// And the type was not quietly created on the way to the refusal.
	if _, err := web.MCPTypesGet(ctx, f.deps, f.caller, f.game,
		web.TypesGetInput{Key: "circuit"}); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("the refused upsert created the type anyway: %v", err)
	}
}

// TestEveryToolThatTakesAVersionSaysWhatAClaimMeans pins the sentence on
// the wire, for the reason every description guard in this package
// exists: the behaviour changed under agents that had learned the old
// one, and an agent that re-sends a seed with the versions it last read
// will now meet not_found on exactly the rows a designer removed. A rule
// an agent cannot read is a rule an agent trips over.
func TestEveryToolThatTakesAVersionSaysWhatAClaimMeans(t *testing.T) {
	t.Parallel()
	f := newMetamodelFixture(t)
	descriptions := f.srv.ToolDescriptionsForTest()
	for _, tool := range []string{
		"types.upsert", "relation_types.upsert", "entities.upsert", "relations.upsert",
	} {
		got, ok := descriptions[tool]
		if !ok {
			t.Errorf("%s is not served", tool)
			continue
		}
		assert.Should(t, strings.Contains(got, "A version claim is a claim about a row that exists"), "%s does not say what a version claim means", tool)
		assert.Should(t, strings.Contains(got, "no expected_version"), "%s does not name the recovery", tool)
	}
}

// TestEveryWholeReplacementWriteSaysSo is here because one of these four
// descriptions said it and the other three did not, and an agent that
// built an update from a slim listing emptied fifty-four rows under a
// correct version claim. The version guards against another writer, not
// against a half-built payload, so the only thing that can protect a
// caller is the sentence.
func TestEveryWholeReplacementWriteSaysSo(t *testing.T) {
	t.Parallel()
	f := newMetamodelFixture(t)
	descriptions := f.srv.ToolDescriptionsForTest()
	// Each tool, what it must say it replaces, and the read-first call it
	// must name: the listing that omits the thing is what the mistake was
	// built from, so naming the reader is half the rule.
	for tool, phrases := range map[string][]string{
		"entities.upsert":       {"replaces the row's whole field map", "entities.get", "fields_mode"},
		"relations.upsert":      {"replaces its fields whole", "relations.get", "fields_mode"},
		"types.upsert":          {"replaces the whole declaration", "types.get"},
		"relation_types.upsert": {"replaces the whole declaration", "relation_types.get"},
	} {
		got, ok := descriptions[tool]
		if !ok {
			t.Errorf("%s is not served", tool)
			continue
		}
		for _, phrase := range phrases {
			assert.Should(t, strings.Contains(got, phrase), "%s does not say %q", tool, phrase)
		}
	}
}

// TestTheRenameToolsSayWhatARenameDoesNotDo is the description guard for
// the one thing an agent will otherwise meet as a surprise.
func TestTheRenameToolsSayWhatARenameDoesNotDo(t *testing.T) {
	t.Parallel()
	f := newMetamodelFixture(t)
	descriptions := f.srv.ToolDescriptionsForTest()
	for _, tool := range []string{"types.rename", "relation_types.rename"} {
		got, ok := descriptions[tool]
		if !ok {
			t.Errorf("%s is not served", tool)
			continue
		}
		for phrase, why := range map[string]string{
			"does not repair the saved views": "the limit an agent meets first",
			"until somebody saves the view again": "the repair, named rather than " +
				"left to be guessed",
			"differing only in capitalisation": "a case-only rename is refused, and an " +
				"agent that does not know will read the refusal as a bug",
			"never merges two types": "a taken destination is refused",
		} {
			assert.Should(t, strings.Contains(got, phrase), "%s does not say %q — %s", tool, phrase, why)
		}
	}
}

// TestMCPToolsRefuseAnotherGame is the isolation test for this whole
// surface: every tool that takes a project id is called with the id of a
// game the caller's own *user* owns but the caller's own *token* is not
// bound to, and every one must refuse before touching the database.
func TestMCPToolsRefuseAnotherGame(t *testing.T) {
	t.Parallel()
	f := newMetamodelFixture(t)
	ctx := context.Background()

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
			_, err := web.MCPTypesRemove(ctx, f.deps, f.caller, f.other, web.TypesRemoveInput{Key: "circuit"})
			return err
		},
		"types.rename": func() error {
			_, err := web.MCPTypesRename(ctx, f.deps, f.caller, f.other, web.TypesRenameInput{
				From: "circuit", To: "track", ExpectedVersion: ptrInt32Web(1),
			})
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
			_, err := web.MCPRelationTypesRemove(ctx, f.deps, f.caller, f.other, web.RelationTypesRemoveInput{Key: "races_on"})
			return err
		},
		"relation_types.rename": func() error {
			_, err := web.MCPRelationTypesRename(ctx, f.deps, f.caller, f.other,
				web.RelationTypesRenameInput{
					From: "races_on", To: "drives_on", ExpectedVersion: ptrInt32Web(1),
				})
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
			_, err := web.MCPEntitiesRemove(ctx, f.deps, f.caller, f.other, web.EntitiesRemoveInput{TypeKey: "circuit", Key: "spa"})
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
		"relations.get": func() error {
			_, err := web.MCPRelationsGet(ctx, f.deps, f.caller, f.other, web.RelationsGetInput{
				TypeKey: "races_on",
				Source:  web.RefInput{TypeKey: "circuit", Key: "spa"},
				Target:  web.RefInput{TypeKey: "circuit", Key: "monza"},
			})
			return err
		},
		"relations.remove": func() error {
			_, err := web.MCPRelationsRemove(ctx, f.deps, f.caller, f.other, web.RelationsRemoveInput{
				TypeKey: "races_on",
				Source:  web.RefInput{TypeKey: "circuit", Key: "spa"},
				Target:  web.RefInput{TypeKey: "circuit", Key: "monza"},
			})
			return err
		},
		"games.counts": func() error {
			_, err := web.MCPGameCounts(ctx, f.deps, f.caller, f.other, web.GameCountsInput{})
			return err
		},
		"entities.repair": func() error {
			_, err := web.MCPEntitiesRepair(ctx, f.deps, f.caller, f.other, web.EntitiesRepairInput{
				TypeKey: "circuit", DropUnknown: true,
			})
			return err
		},
		"relations.repair": func() error {
			_, err := web.MCPRelationsRepair(ctx, f.deps, f.caller, f.other, web.RelationsRepairInput{
				TypeKey: "races_on", DropUnknown: true,
			})
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
	assert.Must(t, err == nil, "MCPTypesList: %v", err)
	assert.Must(t, len(types.Items) == 0, "a refused call wrote into a game after all: %+v", types.Items)
}

func TestMCPEntitiesUpsertBulkReportsFailuresAndWhatLanded(t *testing.T) {
	t.Parallel()
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
	assert.Must(t, err == nil, "MCPEntitiesUpsert: %v", err)
	assert.Must(t, out.Count == 1 && len(out.Written) == 1, "count = %d, written = %+v, want exactly the one row that landed", out.Count, out.Written)
	if out.Written[0].Key != "a" || out.Written[0].Version != 1 {
		t.Fatalf("written[0] = %+v, want key a at version 1", out.Written[0])
	}
	assert.Must(t, len(out.Failed) == 1 && out.Failed[0].Code == "schema_violation" && out.Failed[0].Index == 1, "failed = %+v, want one schema_violation at index 1", out.Failed)
}

// TestMCPEntitiesUpsertRecordsTheCallersOwnToken pins the one thing the
// wire input types exist for: an agent cannot name the author of its own
// writes. metamodel.EntityInput carries an Actor; EntityItemInput does
// not, and actorOf builds one from the authenticated caller.
func TestMCPEntitiesUpsertRecordsTheCallersOwnToken(t *testing.T) {
	t.Parallel()
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
	assert.Must(t, err == nil, "EntityByKey: %v", err)
	assert.Must(t, row.UpdatedByTokenID != nil && *row.UpdatedByTokenID == *f.caller.TokenID, "updated_by_token_id = %v, want the calling token %v", row.UpdatedByTokenID, f.caller.TokenID)
	assert.Must(t, row.UpdatedByUserID != nil && *row.UpdatedByUserID == f.caller.UserID, "updated_by_user_id = %v, want the calling user %v", row.UpdatedByUserID, f.caller.UserID)
}

// TestMCPMalformedIDIsTheCallersOwnArgument pins that an id this layer
// parses is refused as invalid_input at that argument's own path, not as
// an internal error — the rule the metamodel applies to every other
// caller-supplied value.
func TestMCPMalformedIDIsTheCallersOwnArgument(t *testing.T) {
	t.Parallel()
	f := newMetamodelFixture(t)
	ctx := context.Background()

	// **This test used to drive three tools and now drives none of
	// them**, which is Metamodel 14's whole point: types.remove,
	// entities.remove and relations.list's endpoint filters all took a
	// uuid, and a malformed one had to be reported as the caller's own
	// argument rather than as a server fault. They take keys now, and a
	// key that names nothing is not_found — a different answer to a
	// different question.
	if _, err := web.MCPTypesUpsert(ctx, f.deps, f.caller, f.game, web.TypesUpsertInput{
		Key: "circuit", Label: "Circuit", LabelPlural: "Circuits",
	}); err != nil {
		t.Fatalf("types.upsert: %v", err)
	}
	_, err := web.MCPRelationTypesUpsert(ctx, f.deps, f.caller, f.game, web.RelationTypesUpsertInput{
		Key: "requires", Label: "requires",
		SourceTypeKeys: []string{"circuit", "nope"},
	})
	var invalid *metamodel.ValidationError
	assert.Must(t, errors.As(err, &invalid) && invalid.Code == "invalid_input", "err = %#v, want an invalid_input ValidationError", err)
	assert.Must(t, len(invalid.Fields) == 1, "problems = %+v, want only the bad element", invalid.Fields)
	if invalid.Fields[0].Path != "source_type_keys[1]" ||
		!strings.Contains(invalid.Fields[0].Message, "nope") {
		t.Fatalf("problem = %+v, want it to name the element at fault and the key",
			invalid.Fields[0])
	}
	if _, err := web.MCPRelationTypesGet(ctx, f.deps, f.caller, f.game,
		web.RelationTypesGetInput{Key: "requires"}); err == nil {
		t.Fatalf("the refused declaration stored something")
	}

	// And a list that names one type twice, which the database would
	// have stored happily: an endpoint list is a set.
	_, err = web.MCPRelationTypesUpsert(ctx, f.deps, f.caller, f.game, web.RelationTypesUpsertInput{
		Key: "requires", Label: "requires",
		SourceTypeKeys: []string{"circuit", "CIRCUIT"},
	})
	assert.Must(t, errors.As(err, &invalid) && len(invalid.Fields) == 1 && invalid.Fields[0].Path == "source_type_keys[1]", "err = %#v, want the repeated element named", err)
}

// TestMCPMetamodelToolsAreServedOverTheRealTransport is the end-to-end
// pass: a real token, a real MCP client, and the whole seeding round trip
// an agent actually performs — declare two types, declare an edge type
// between them, seed entities, join them, then find one by search.
func TestMCPMetamodelToolsAreServedOverTheRealTransport(t *testing.T) {
	t.Parallel()
	f := newMetamodelFixture(t)
	httpSrv := httptest.NewServer(f.srv)
	defer httpSrv.Close()
	ctx := context.Background()

	session := connectMCP(t, httpSrv.URL, f.token)

	tools, err := session.ListTools(ctx, nil)
	assert.Must(t, err == nil, "ListTools: %v", err)
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{
		"games.counts",
		"types.upsert", "types.list", "types.get", "types.remove", "types.rename",
		"relation_types.upsert", "relation_types.list", "relation_types.get",
		"relation_types.remove", "relation_types.rename",
		"entities.upsert", "entities.list", "entities.get", "entities.remove",
		"entities.repair",
		"relations.upsert", "relations.list", "relations.remove", "relations.repair",
		"search",
	} {
		assert.Must(t, names[want], "the served tool list is missing %q", want)
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
		"source_type_keys": []any{"quest"},
		"target_type_keys": []any{"zone"},
		"semantic_role":    "spatial",
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
	assert.Must(t, seeded.Count == 2 && len(seeded.Written) == 2 && len(seeded.Failed) == 0, "seed = %+v, want two rows written and none failed", seeded)
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
	assert.Must(t, len(related.Items) == 1 && related.Items[0].Key == "elwynn" && related.Items[0].TypeKey == "zone", "related = %+v, want the zone the quest takes place in", related.Items)

	// The search hit's shape on the actual wire, decoded from the JSON
	// the transport produced rather than from the Go struct: `kind` is
	// what a client branches on, and the entity's own fields live under
	// `entity` since a document hit has none of them.
	var found struct {
		Items []struct {
			Kind      string  `json:"kind"`
			NameMatch bool    `json:"name_match"`
			Rank      float64 `json:"rank"`
			Entity    *struct {
				Key    string `json:"key"`
				Fields map[string]any
			} `json:"entity"`
			Document *json.RawMessage `json:"document"`
		} `json:"items"`
		Truncated bool `json:"truncated"`
	}
	decodeStructured(t, callOK(t, session, "search", map[string]any{"query": "gnoll"}), &found)
	assert.Must(t, len(found.Items) == 1 && found.Items[0].Kind == "entity" && found.Items[0].Entity != nil && found.Items[0].Entity.Key == "hogger", "search = %+v, want one labelled entity hit for the quest whose summary "+
		"carries the word", found.Items)
	if found.Items[0].Document != nil {
		t.Fatalf("an entity hit carries a document half: %+v", found.Items[0])
	}
	assert.Must(t, !found.Truncated, "one hit under the default limit must not report the answer as truncated")

	// A listing without verbose carries no fields; with it, it does.
	var slim struct {
		Items []struct {
			Key    string         `json:"key"`
			Fields map[string]any `json:"fields"`
		} `json:"items"`
	}
	decodeStructured(t, callOK(t, session, "entities.list", map[string]any{"type_key": "quest"}), &slim)
	assert.Must(t, len(slim.Items) == 1 && slim.Items[0].Fields == nil, "a slim listing carried fields: %+v", slim.Items)
	decodeStructured(t, callOK(t, session, "entities.list",
		map[string]any{"type_key": "quest", "verbose": true}), &slim)
	assert.Must(t, len(slim.Items) == 1 && slim.Items[0].Fields["min_level"] != nil, "a verbose listing carried no fields: %+v", slim.Items)
}

// TestMCPMetamodelErrorsCarryTheirOwnCode pins that the domain's error
// vocabulary survives the transport: an agent reads a code, not prose,
// and each of these has a different recovery.
func TestMCPMetamodelErrorsCarryTheirOwnCode(t *testing.T) {
	t.Parallel()
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
		"target_type_keys": []any{"zone"},
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
		{"a type that is not there", "types.remove",
			map[string]any{"key": "nothing-here"}, "not_found"},
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
			assert.Must(t, err == nil, "CallTool(%s): %v", tc.tool, err)
			assert.Must(t, result.IsError, "%s must have failed, got %+v", tc.tool, result.StructuredContent)
			assert.Must(t, result.StructuredContent == nil, "an error result must carry no StructuredContent, got %#v", result.StructuredContent)
			var wireErr struct {
				Error string `json:"error"`
			}
			decodeToolText(t, result, &wireErr)
			assert.Must(t, wireErr.Error == tc.want, "error = %q, want %q", wireErr.Error, tc.want)
		})
	}

	// The one that is refused as in_use rather than not_found needs a
	// type that really has entities, so it is done separately.
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "types.remove", Arguments: map[string]any{"key": "quest"},
	})
	assert.Must(t, err == nil, "CallTool(types.remove): %v", err)
	var wireErr struct {
		Error string `json:"error"`
	}
	decodeToolText(t, result, &wireErr)
	assert.Must(t, wireErr.Error == "in_use", "removing a type that still has entities reported %q, want in_use", wireErr.Error)
}

// callOK calls a tool and fails the test unless it succeeded.
func callOK(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	assert.Must(t, err == nil, "CallTool(%s): %v", name, err)
	assert.Must(t, !result.IsError, "CallTool(%s) reported an error: %+v", name, result.Content)
	return result
}

// TestTheTraversalPagesAndItsDirectionIsRequiredOnTheWire closes review
// findings H1 and H2 at the layer where both were wrong: the wire.
func TestTheTraversalPagesAndItsDirectionIsRequiredOnTheWire(t *testing.T) {
	t.Parallel()
	f := newMetamodelFixture(t)
	httpSrv := httptest.NewServer(f.srv)
	defer httpSrv.Close()
	ctx := context.Background()

	session := connectMCP(t, httpSrv.URL, f.token)

	tools, err := session.ListTools(ctx, nil)
	assert.Must(t, err == nil, "ListTools: %v", err)
	var list *mcp.Tool
	for _, tool := range tools.Tools {
		if tool.Name == "entities.list" {
			list = tool
		}
	}
	assert.Must(t, list != nil, "entities.list is not served")
	// InputSchema crosses the wire as raw JSON, so the served schema is
	// read as JSON rather than as a Go type — which is also the shape an
	// agent's client sees.
	raw, err := json.Marshal(list.InputSchema)
	assert.Must(t, err == nil, "marshal input schema: %v", err)
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
	assert.Must(t, slices.Contains(served.Properties.RelatedTo.Required, "direction"), "related_to.required = %v, want direction among them: %s",
		served.Properties.RelatedTo.Required, raw)

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
	assert.Must(t, len(page.Items) == 2 && page.NextCursor != "", "first traversal page = %+v, want two neighbours and a cursor", page)

	seen := []string{page.Items[0].Key, page.Items[1].Key}
	decodeStructured(t, callOK(t, session, "entities.list",
		anchor(map[string]any{"cursor": page.NextCursor})), &page)
	assert.Must(t, len(page.Items) == 1, "second traversal page = %+v, want the remaining neighbour", page.Items)
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
	assert.Must(t, err != nil || result.IsError, "a traversal with no direction was answered: %+v", result.StructuredContent)
}

// TestMCPSearchNameMatchAgreesWithTheOrderItExplains closes the Task 7
// re-review's finding that the wire carried only `rank` after the
// ranking fix changed the sort to `(name_match, rank)`. SearchHit,
// SearchOutput and Search's own doc comment all still said the order
// was "by rank", which stopped being true the moment name_match became
// the leading key: proved live, `Gnoll Pack` (a lower rank) sorted
// before `Wanted: Hogger` (a higher one) because only the first is
// *named* by the query.
func TestMCPSearchNameMatchAgreesWithTheOrderItExplains(t *testing.T) {
	t.Parallel()
	f := newMetamodelFixture(t)
	ctx := context.Background()

	if _, err := web.MCPTypesUpsert(ctx, f.deps, f.caller, f.game, web.TypesUpsertInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: []web.FieldInput{
			{Key: "min_level", Type: "number", Required: true},
			{Key: "summary", Type: "longtext"},
		},
	}); err != nil {
		t.Fatalf("MCPTypesUpsert: %v", err)
	}

	// "named" is the row the query actually names. "mentioned" only
	// carries the words in a field, repeated until ts_rank alone would
	// have ranked it first — the exact shape review finding M1 proved
	// live, reproduced here at the wire rather than the domain layer.
	if _, err := web.MCPEntitiesUpsert(ctx, f.deps, f.caller, f.game, web.EntitiesUpsertInput{
		Items: []web.EntityItemInput{
			{TypeKey: "quest", Key: "named", Name: "Gnoll Pack",
				Fields: map[string]any{"min_level": float64(1)}},
			{TypeKey: "quest", Key: "mentioned", Name: "Wanted: Hogger",
				Fields: map[string]any{
					"min_level": float64(1),
					"summary": strings.TrimSpace(strings.Repeat(
						"A Gnoll Pack camp led by a Gnoll Pack chieftain. ", 500)),
				}},
		},
	}); err != nil {
		t.Fatalf("MCPEntitiesUpsert: %v", err)
	}

	out, err := web.MCPSearch(ctx, f.deps, f.caller, f.game, web.SearchInput{Query: "gnoll pack"})
	assert.Must(t, err == nil, "MCPSearch: %v", err)
	assert.Must(t, len(out.Items) == 2, "items = %+v, want both rows", out.Items)
	// The order the ranking fix exists to guarantee: the named row
	// first, regardless of how the un-weighted rank alone would compare.
	if out.Items[0].Entity.Key != "named" || out.Items[1].Entity.Key != "mentioned" {
		t.Fatalf("order = [%s %s], want [named mentioned]",
			out.Items[0].Entity.Key, out.Items[1].Entity.Key)
	}
	// The field the order is actually built from must say the same
	// thing the position does, for every adjacent pair — the assertion
	// that generalises past this one fixture.
	for i := 1; i < len(out.Items); i++ {
		assert.Must(t, out.Items[i-1].NameMatch || !out.Items[i].NameMatch, "items[%d].NameMatch = false sorted before items[%d].NameMatch = true: "+
			"the wire order and the wire field disagree", i-1, i)
	}
	if !out.Items[0].NameMatch {
		t.Fatalf("items[0] (%s) NameMatch = false, want true", out.Items[0].Entity.Key)
	}
	if out.Items[1].NameMatch {
		t.Fatalf("items[1] (%s) NameMatch = true, want false", out.Items[1].Entity.Key)
	}
}

// TestTheDomainTypesOnTheWireCarryExactlyTheseKeys closes review finding
// L1.
func TestTheDomainTypesOnTheWireCarryExactlyTheseKeys(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		value any
		want  []string
	}{
		{"metamodel.BulkWrite", metamodel.BulkWrite{},
			[]string{"id", "key", "type_key", "version"}},
		{"metamodel.RelationWrite", metamodel.RelationWrite{},
			[]string{"id", "source_id", "target_id", "type_key", "version"}},
		{"metamodel.BulkFailure", metamodel.BulkFailure{},
			[]string{"code", "index", "key", "message"}},
		// A Schema is a list of Fields, so the keys that matter are one
		// Field's. Every optional key is given a value, because the
		// marshaller omits the empty ones and a zero value would pin
		// half the contract. `default` comes from MarshalJSON rather
		// than from a struct tag, which is the reason these schemas are
		// hand-written at all.
		{"metamodel.Schema's Field", metamodel.Field{
			Key: "difficulty", Label: "Difficulty", Type: metamodel.FieldEnum,
			Required: true, Options: []string{"easy"},
			Min: ptrFloat(1), Max: ptrFloat(10),
			HasDefault: true, Default: "easy",
		}, []string{"default", "key", "label", "max", "min", "options", "required", "type"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.value)
			assert.Must(t, err == nil, "marshal: %v", err)
			var keyed map[string]json.RawMessage
			if err := json.Unmarshal(raw, &keyed); err != nil {
				t.Fatalf("decode %s: %v", raw, err)
			}
			got := make([]string, 0, len(keyed))
			for k := range keyed {
				got = append(got, k)
			}
			slices.Sort(got)
			assert.Must(t, slices.Equal(got, tc.want), "wire keys = %v, want %v — a domain type on this surface grew a "+
				"field; decide whether an agent should see it, then update this list",
				got, tc.want)
		})
	}
}

func ptrFloat(v float64) *float64 { return &v }

// seedOneEdge declares a quest and a zone, one takes_place_in relation
// type between them, two entities and the single edge joining them, and
// hands back the ids of both endpoints. It exists for the endpoint-ref
// tests below, which need a real edge and care about nothing else.
func seedOneEdge(t *testing.T, f metamodelFixture) (source, target uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	for _, spec := range []struct{ key, label, plural string }{
		{"quest", "Quest", "Quests"}, {"zone", "Zone", "Zones"},
	} {
		if _, err := web.MCPTypesUpsert(ctx, f.deps, f.caller, f.game, web.TypesUpsertInput{
			Key: spec.key, Label: spec.label, LabelPlural: spec.plural,
		}); err != nil {
			t.Fatalf("MCPTypesUpsert %s: %v", spec.key, err)
		}
	}
	if _, err := web.MCPRelationTypesUpsert(ctx, f.deps, f.caller, f.game, web.RelationTypesUpsertInput{
		Key: "takes_place_in", Label: "takes place in",
		SourceTypeKeys: []string{"quest"},
		TargetTypeKeys: []string{"zone"},
		// The edge carries a declared field, because the tests below are
		// about whether an edge's own values can be read back at all and
		// a schemaless edge cannot tell a working reader from a broken
		// one. Every relation-level test in this file goes through this
		// helper, so the value is written once and read by each of them.
		Schema: []web.FieldInput{{Key: "act", Type: "text"}},
	}); err != nil {
		t.Fatalf("MCPRelationTypesUpsert: %v", err)
	}
	written, err := web.MCPEntitiesUpsert(ctx, f.deps, f.caller, f.game, web.EntitiesUpsertInput{
		Items: []web.EntityItemInput{
			{TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger"},
			{TypeKey: "zone", Key: "elwynn", Name: "Elwynn Forest"},
		},
	})
	assert.Must(t, err == nil, "MCPEntitiesUpsert: %v", err)
	assert.Must(t, written.Count == 2, "seeded %d entities, want 2: %+v", written.Count, written.Failed)
	if _, err := web.MCPRelationsUpsert(ctx, f.deps, f.caller, f.game, web.RelationsUpsertInput{
		Items: []web.RelationItemInput{{
			TypeKey: "takes_place_in",
			Source:  web.RefInput{TypeKey: "quest", Key: "hogger"},
			Target:  web.RefInput{TypeKey: "zone", Key: "elwynn"},
			Fields:  map[string]any{"act": "one"},
		}},
	}); err != nil {
		t.Fatalf("MCPRelationsUpsert: %v", err)
	}
	return written.Written[0].ID, written.Written[1].ID
}

// TestRelationsListNamesBothEndpointsByRef closes Task 7's own finding
// 18: `relations.list` answered with endpoint ids because the row holds
// ids and no bulk entity-by-ids query existed, and it named this task as
// where the fix was cheapest. It is here: every edge now carries the
// (type_key, key, name) ref each endpoint was written with, alongside
// the ids a removal still addresses them by.
func TestRelationsListNamesBothEndpointsByRef(t *testing.T) {
	t.Parallel()
	f := newMetamodelFixture(t)
	ctx := context.Background()
	sourceID, targetID := seedOneEdge(t, f)

	page, err := web.MCPRelationsList(ctx, f.deps, f.caller, f.game, web.RelationsListInput{})
	assert.Must(t, err == nil, "MCPRelationsList: %v", err)
	assert.Must(t, len(page.Items) == 1, "items = %+v, want the one edge", page.Items)
	edge := page.Items[0]
	assert.Must(t, edge.SourceID == sourceID && edge.TargetID == targetID, "edge ids = (%s, %s), want (%s, %s)", edge.SourceID, edge.TargetID, sourceID, targetID)
	assert.Must(t, edge.Source != nil && edge.Target != nil, "edge = %+v, want both endpoints resolved to refs", edge)
	assert.Must(t, edge.Source.TypeKey == "quest" && edge.Source.Key == "hogger" && edge.Source.Name == "Wanted: Hogger", "source = %+v, want the quest it was written with", edge.Source)
	assert.Must(t, edge.Target.TypeKey == "zone" && edge.Target.Key == "elwynn" && edge.Target.Name == "Elwynn Forest", "target = %+v, want the zone it was written with", edge.Target)
}

// TestTheServedRelationsListSchemaAdvertisesTheEndpointRefs is the
// paired guard against the fault this project has found in nine
// consecutive tasks: an answer growing a field its published contract
// does not mention. The output schemas here are hand-written, so nothing
// but a test connects them to the struct they describe.
func TestTheServedRelationsListSchemaAdvertisesTheEndpointRefs(t *testing.T) {
	t.Parallel()
	f := newMetamodelFixture(t)
	httpSrv := httptest.NewServer(f.srv)
	defer httpSrv.Close()

	session := connectMCP(t, httpSrv.URL, f.token)
	tools, err := session.ListTools(context.Background(), nil)
	assert.Must(t, err == nil, "ListTools: %v", err)
	var list *mcp.Tool
	for _, tool := range tools.Tools {
		if tool.Name == "relations.list" {
			list = tool
		}
	}
	assert.Must(t, list != nil, "relations.list is not served")
	raw, err := json.Marshal(list.OutputSchema)
	assert.Must(t, err == nil, "marshal output schema: %v", err)
	var served struct {
		Properties struct {
			Items struct {
				Items struct {
					Properties map[string]struct {
						Properties map[string]json.RawMessage `json:"properties"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &served); err != nil {
		t.Fatalf("decode output schema: %v", err)
	}
	for _, end := range []string{"source", "target"} {
		ref, ok := served.Properties.Items.Items.Properties[end]
		assert.Must(t, ok, "output schema has no %q property: %s", end, raw)
		for _, key := range []string{"type_key", "key", "name"} {
			if _, ok := ref.Properties[key]; !ok {
				t.Fatalf("%s ref schema has no %q: %s", end, key, raw)
			}
		}
	}

	// **version and invalid are on the same contract**, and they are the
	// half a write-only column ships without: an agent cannot send an
	// expected_version it was never told, and cannot act on a flag whose
	// existence its published schema denies. The output schema here is
	// hand-written, so nothing but this connects it to RelationOutput.
	for _, key := range []string{"version", "invalid"} {
		if _, ok := served.Properties.Items.Items.Properties[key]; !ok {
			t.Fatalf("output schema has no %q property: %s", key, raw)
		}
	}

	// The description is the other half of the same contract, and Task
	// 7 shipped it saying the opposite of what the tool now does — twice
	// over: it also told an agent an edge had no version and no
	// expected_version to guard it, which 0009 made false.
	assert.Must(t, !strings.Contains(list.Description, "not as the (type_key, key) refs"), "relations.list still tells an agent its endpoints are ids only: %s", list.Description)
	assert.Must(t, strings.Contains(list.Description, "invalid"), "relations.list does not mention its invalid filter: %s", list.Description)
	var upsert *mcp.Tool
	for _, tool := range tools.Tools {
		if tool.Name == "relations.upsert" {
			upsert = tool
		}
	}
	assert.Must(t, upsert != nil, "relations.upsert is not served")
	assert.Must(t, !strings.Contains(upsert.Description, "has no version") && !strings.Contains(upsert.Description, "last writer wins"), "relations.upsert still promises last-writer-wins, which 0009 ended: %s",
		upsert.Description)
	assert.Must(t, strings.Contains(upsert.Description, "expected_version"), "relations.upsert does not tell an agent to send expected_version: %s",
		upsert.Description)
	// The input schema is the third copy of the same claim: the SDK
	// infers it from RelationItemInput, so a field the struct lacks is a
	// field an agent cannot send however the prose reads.
	rawIn, err := json.Marshal(upsert.InputSchema)
	assert.Must(t, err == nil, "marshal input schema: %v", err)
	assert.Must(t, strings.Contains(string(rawIn), "expected_version"), "relations.upsert's input schema has no expected_version: %s", rawIn)
}

// TestRelationsListFindsTheEdgesASchemaEditBroke is the MCP half of the
// read surface 0009's flag needs.
func TestRelationsListFindsTheEdgesASchemaEditBroke(t *testing.T) {
	t.Parallel()
	f := newMetamodelFixture(t)
	ctx := context.Background()
	seedOneEdge(t, f)

	// Narrowing the type: `act` is dropped, so the seeded edge carrying
	// it stops fitting.
	if _, err := web.MCPRelationTypesUpsert(ctx, f.deps, f.caller, f.game,
		web.RelationTypesUpsertInput{
			Key: "takes_place_in", Label: "takes place in",
			ExpectedVersion: ptrInt32Web(1),
		}); err != nil {
		t.Fatalf("narrow the relation type: %v", err)
	}

	yes, no := true, false
	for _, tc := range []struct {
		name   string
		filter *bool
		want   int
	}{
		{"no opinion", nil, 1},
		{"only the broken ones", &yes, 1},
		{"only the intact ones", &no, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := web.MCPRelationsList(ctx, f.deps, f.caller, f.game,
				web.RelationsListInput{Invalid: tc.filter})
			assert.Must(t, err == nil, "MCPRelationsList: %v", err)
			assert.Must(t, len(page.Items) == tc.want, "items = %d, want %d: %+v", len(page.Items), tc.want, page.Items)
			for _, item := range page.Items {
				assert.Must(t, item.Invalid, "the edge a schema edit broke is reported as valid: %+v", item)
			}
		})
	}

	// relations.get answers with the flag too: an agent that walked to one
	// edge by its address must not have to page a listing to learn that
	// the values it is reading no longer fit.
	got, err := web.MCPRelationsGet(ctx, f.deps, f.caller, f.game, web.RelationsGetInput{
		TypeKey: "takes_place_in",
		Source:  web.RefInput{TypeKey: "quest", Key: "hogger"},
		Target:  web.RefInput{TypeKey: "zone", Key: "elwynn"},
	})
	assert.Must(t, err == nil, "MCPRelationsGet: %v", err)
	assert.Must(t, got.Invalid, "relations.get = %+v, want the flag the listing reports", got)
	assert.Must(t, got.Version == 1, "version = %d, want 1: a sweep is not an edit and must not move it",
		got.Version)
}

// TestAnEdgesFieldsAreReadableOnBothToolsThatReturnAnEdge is Metamodel
// 12's own test. A relation type may declare a field schema, the values
// were validated on write and stored, and until this task no tool on
// either surface returned them: RelationOutput carried no fields, there
// was no relations.get, and relations.list was the only way to see an
// edge at all. Nine review rounds verified the write and none asked
// whether anything could read it.
func TestAnEdgesFieldsAreReadableOnBothToolsThatReturnAnEdge(t *testing.T) {
	t.Parallel()
	f := newMetamodelFixture(t)
	ctx := context.Background()
	sourceID, targetID := seedOneEdge(t, f)

	got, err := web.MCPRelationsGet(ctx, f.deps, f.caller, f.game, web.RelationsGetInput{
		TypeKey: "takes_place_in",
		Source:  web.RefInput{TypeKey: "quest", Key: "hogger"},
		Target:  web.RefInput{TypeKey: "zone", Key: "elwynn"},
	})
	assert.Must(t, err == nil, "MCPRelationsGet: %v", err)
	assert.Must(t, got.Fields["act"] == "one", "relations.get fields = %v, want act one", got.Fields)
	// One edge, addressed two ways: what relations.get answers is the
	// same row relations.list pages over, endpoint refs included.
	assert.Must(t, got.SourceID == sourceID && got.TargetID == targetID, "relations.get ids = (%s, %s), want (%s, %s)",
		got.SourceID, got.TargetID, sourceID, targetID)
	assert.Must(t, got.TypeKey == "takes_place_in", "relations.get type key = %q, want takes_place_in", got.TypeKey)
	assert.Must(t, got.Source != nil && got.Source.Name == "Wanted: Hogger" && got.Target != nil && got.Target.Name == "Elwynn Forest", "relations.get endpoints = %+v / %+v, want both resolved", got.Source, got.Target)

	verbose, err := web.MCPRelationsList(ctx, f.deps, f.caller, f.game,
		web.RelationsListInput{Verbose: true})
	assert.Must(t, err == nil, "MCPRelationsList verbose: %v", err)
	assert.Must(t, len(verbose.Items) == 1 && verbose.Items[0].Fields["act"] == "one", "verbose listing = %+v, want the edge with act one", verbose.Items)

	// Off by default, for the reason entities.list defaults it off and
	// then some: a game has more edges than entities, so a page of five
	// hundred of them carrying their fields is more of the game back in
	// one answer than the listing this rule was written for.
	plain, err := web.MCPRelationsList(ctx, f.deps, f.caller, f.game, web.RelationsListInput{})
	assert.Must(t, err == nil, "MCPRelationsList: %v", err)
	assert.Must(t, len(plain.Items) == 1, "items = %+v, want the one edge", plain.Items)
	if plain.Items[0].Fields != nil {
		t.Fatalf("a listing that was not asked to be verbose carried fields: %+v", plain.Items[0])
	}
	// Absent from the JSON too, not merely nil in Go: `fields` is
	// omitempty on the wire and a client must be able to tell "not
	// asked for" from "asked for and empty".
	raw, err := json.Marshal(plain.Items[0])
	assert.Must(t, err == nil, "marshal edge: %v", err)
	assert.Must(t, !strings.Contains(string(raw), "fields"), "non-verbose edge carries a fields key on the wire: %s", raw)
}

// TestRelationsGetNamesWhichPieceOfAnAddressIsWrong: an edge is
// addressed by three things, so there are three ways to miss, and the
// tool passes the domain's own distinction through rather than
// flattening all of them onto "not found".
func TestRelationsGetNamesWhichPieceOfAnAddressIsWrong(t *testing.T) {
	t.Parallel()
	f := newMetamodelFixture(t)
	ctx := context.Background()
	seedOneEdge(t, f)

	hogger := web.RefInput{TypeKey: "quest", Key: "hogger"}
	elwynn := web.RefInput{TypeKey: "zone", Key: "elwynn"}
	for _, tc := range []struct {
		name        string
		in          web.RelationsGetInput
		want        string
		wantInvalid bool
	}{
		{
			name: "unknown relation type",
			in:   web.RelationsGetInput{TypeKey: "unlocks", Source: hogger, Target: elwynn},
			want: `no relation type "unlocks"`,
		},
		{
			name: "unknown endpoint",
			in: web.RelationsGetInput{TypeKey: "takes_place_in", Source: hogger,
				Target: web.RefInput{TypeKey: "zone", Key: "westfall"}},
			want: `no entity "westfall"`,
		},
		{
			name: "the address is real and holds no edge",
			in:   web.RelationsGetInput{TypeKey: "takes_place_in", Source: elwynn, Target: hogger},
			want: `no "takes_place_in" edge`,
		},
		{
			name:        "a key that could never have been stored",
			in:          web.RelationsGetInput{TypeKey: "takes place in", Source: hogger, Target: elwynn},
			want:        "type_key",
			wantInvalid: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := web.MCPRelationsGet(ctx, f.deps, f.caller, f.game, tc.in)
			assert.Must(t, err != nil, "err = nil, want a refusal")
			want := metamodel.ErrNotFound
			if tc.wantInvalid {
				want = metamodel.ErrInvalidInput
			}
			assert.Must(t, errors.Is(err, want), "err = %v, want %v", err, want)
			assert.Must(t, strings.Contains(err.Error(), tc.want), "err = %q, want it to contain %q", err, tc.want)
		})
	}
}

// TestTheServedEdgeSchemasAdvertiseAnEdgesFields is the paired guard
// against this project's most repeated fault: an answer growing a field
// its published contract does not mention. The output schemas are
// hand-written, so nothing but a test connects them to the structs they
// describe — and relations.list's description claimed an agent could
// read an edge's own fields through it a whole task before it could.
func TestTheServedEdgeSchemasAdvertiseAnEdgesFields(t *testing.T) {
	t.Parallel()
	f := newMetamodelFixture(t)
	httpSrv := httptest.NewServer(f.srv)
	defer httpSrv.Close()

	session := connectMCP(t, httpSrv.URL, f.token)
	tools, err := session.ListTools(context.Background(), nil)
	assert.Must(t, err == nil, "ListTools: %v", err)
	served := map[string]*mcp.Tool{}
	for _, tool := range tools.Tools {
		served[tool.Name] = tool
	}
	get, ok := served["relations.get"]
	assert.Must(t, ok, "relations.get is not served")
	rawGet, err := json.Marshal(get.OutputSchema)
	assert.Must(t, err == nil, "marshal relations.get output schema: %v", err)
	var edge struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(rawGet, &edge); err != nil {
		t.Fatalf("decode relations.get output schema: %v", err)
	}
	if _, ok := edge.Properties["fields"]; !ok {
		t.Fatalf("relations.get advertises no fields: %s", rawGet)
	}

	list, ok := served["relations.list"]
	assert.Must(t, ok, "relations.list is not served")
	rawList, err := json.Marshal(list.OutputSchema)
	assert.Must(t, err == nil, "marshal relations.list output schema: %v", err)
	var page struct {
		Properties struct {
			Items struct {
				Items struct {
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"items"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(rawList, &page); err != nil {
		t.Fatalf("decode relations.list output schema: %v", err)
	}
	if _, ok := page.Properties.Items.Items.Properties["fields"]; !ok {
		t.Fatalf("relations.list advertises no fields: %s", rawList)
	}
	// The input half: a caller cannot ask for the fields unless the
	// input schema says the flag exists.
	rawIn, err := json.Marshal(list.InputSchema)
	assert.Must(t, err == nil, "marshal relations.list input schema: %v", err)
	assert.Must(t, strings.Contains(string(rawIn), "verbose"), "relations.list takes no verbose flag: %s", rawIn)
}
