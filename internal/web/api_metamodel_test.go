package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/web"
)

// restFixture is a game, its owner's session cookie, and the services
// behind them. Every test in this file drives the REST surface exactly
// as a browser does: a session cookie, a JSON body, and the URL the SPA
// would have built.
type restFixture struct {
	srv     *web.Server
	ids     *identity.Service
	proj    *projects.Service
	mm      *metamodel.Service
	game    uuid.UUID
	ownerID uuid.UUID
	cookie  *http.Cookie
}

func newRESTFixture(t *testing.T) restFixture {
	t.Helper()
	srv, ids, projSvc, mm := newMetamodelTestServer(t)
	ctx := context.Background()

	owner, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	game, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create game: %v", err)
	}
	return restFixture{
		srv: srv, ids: ids, proj: projSvc, mm: mm,
		game: game.ID, ownerID: owner.ID, cookie: loginAs(t, srv, "owner@studio.com"),
	}
}

// call sends one request as the given cookie holder and hands back the
// recorder. body is nil for a GET.
func (f restFixture) call(t *testing.T, cookie *http.Cookie, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

func (f restFixture) path(suffix string) string {
	return "/api/games/" + f.game.String() + suffix
}

// as is call with the fixture's own owner cookie.
func (f restFixture) as(t *testing.T, method, suffix string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return f.call(t, f.cookie, method, f.path(suffix), body)
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
}

// wireError is the error body every handler in this package writes:
// a stable code, a human message, and — for the errors that carry field
// paths — the same details object the MCP surface returns.
type wireError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Details struct {
		Fields []struct {
			Path    string `json:"path"`
			Message string `json:"message"`
		} `json:"fields"`
		CurrentVersion *int32 `json:"current_version"`
	} `json:"details"`
}

// assertError insists on the status, the code AND the field path a
// refusal names. Asserting the status alone would pass for a refusal
// that happened for an entirely different reason, which is the failure
// mode this project has spent several tasks removing from its tests.
func assertError(t *testing.T, rec *httptest.ResponseRecorder, status int, code, path string) wireError {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	var body wireError
	decodeBody(t, rec, &body)
	if body.Error != code {
		t.Fatalf("error = %q, want %q: %s", body.Error, code, rec.Body.String())
	}
	if body.Message == "" {
		t.Fatalf("no message on %s", rec.Body.String())
	}
	if path != "" {
		found := false
		for _, f := range body.Details.Fields {
			if f.Path == path {
				found = true
			}
		}
		if !found {
			t.Fatalf("no field problem at path %q: %s", path, rec.Body.String())
		}
	}
	return body
}

func questType(t *testing.T, f restFixture) {
	t.Helper()
	rec := f.as(t, http.MethodPost, "/types", map[string]any{
		"key": "quest", "label": "Quest", "label_plural": "Quests",
		"field_schema": []any{map[string]any{"key": "min_level", "type": "number"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("declare quest type = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRESTTypesRequireMembership(t *testing.T) {
	f := newRESTFixture(t)
	if _, err := f.ids.CreateUser(context.Background(), identity.CreateUserRequest{
		Email: "stranger@studio.com", DisplayName: "Stranger", Password: "password12345",
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	stranger := loginAs(t, f.srv, "stranger@studio.com")

	rec := f.call(t, stranger, http.MethodGet, f.path("/types"), nil)
	assertError(t, rec, http.StatusForbidden, "forbidden", "")
}

func TestRESTCreateAndListTypes(t *testing.T) {
	f := newRESTFixture(t)
	questType(t, f)

	rec := f.as(t, http.MethodGet, "/types", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []struct {
			ID          string `json:"id"`
			Key         string `json:"key"`
			LabelPlural string `json:"label_plural"`
			Version     int32  `json:"version"`
		} `json:"items"`
	}
	decodeBody(t, rec, &payload)
	if len(payload.Items) != 1 || payload.Items[0].Key != "quest" {
		t.Fatalf("items = %+v, want the one declared type", payload.Items)
	}
	if payload.Items[0].Version != 1 || payload.Items[0].ID == "" {
		t.Fatalf("item = %+v, want an id and version 1", payload.Items[0])
	}

	// One type in full, addressed by key.
	one := f.as(t, http.MethodGet, "/types/by-key/quest", nil)
	if one.Code != http.StatusOK {
		t.Fatalf("get = %d: %s", one.Code, one.Body.String())
	}
	var detail struct {
		Key    string `json:"key"`
		Schema []struct {
			Key string `json:"key"`
		} `json:"field_schema"`
	}
	decodeBody(t, one, &detail)
	if detail.Key != "quest" || len(detail.Schema) != 1 || detail.Schema[0].Key != "min_level" {
		t.Fatalf("detail = %+v, want the declared field schema back", detail)
	}
}

// TestRESTDeclaringABrokenSchemaIsInvalidSchema is where the plan's own
// snippet was wrong: it asserted a field schema declaring an enum with
// no options came back as `schema_violation`. It does not, and should
// not — Task 3's correction 25 split the three codes precisely so an
// agent (or a designer) knows which thing to fix. A broken *declaration*
// is `invalid_schema`; `schema_violation` is a stored value that no
// longer fits a schema. This pins the code the domain actually produces.
func TestRESTDeclaringABrokenSchemaIsInvalidSchema(t *testing.T) {
	f := newRESTFixture(t)
	rec := f.as(t, http.MethodPost, "/types", map[string]any{
		"key": "quest", "label": "Quest", "label_plural": "Quests",
		"field_schema": []any{map[string]any{"key": "difficulty", "type": "enum"}},
	})
	assertError(t, rec, http.StatusUnprocessableEntity, "invalid_schema", "field_schema[0]")
}

// TestRESTAValueThatDoesNotFitItsSchemaIsSchemaViolation is the other
// half of the same split, and the one the plan's snippet was reaching
// for.
func TestRESTAValueThatDoesNotFitItsSchemaIsSchemaViolation(t *testing.T) {
	f := newRESTFixture(t)
	questType(t, f)

	rec := f.as(t, http.MethodPost, "/entities", map[string]any{
		"items": []any{map[string]any{
			"type_key": "quest", "key": "hogger", "name": "Wanted: Hogger",
			"fields": map[string]any{"min_level": "ten"},
		}},
		"mode": "atomic",
	})
	assertError(t, rec, http.StatusUnprocessableEntity, "schema_violation", "fields.min_level")
}

// TestRESTAStaleVersionIsRefusedWithTheCurrentOne pins the conflict
// shape a browser needs to merge: the code, and the version the write
// would have met.
func TestRESTAStaleVersionIsRefusedWithTheCurrentOne(t *testing.T) {
	f := newRESTFixture(t)
	questType(t, f)

	rec := f.as(t, http.MethodPost, "/types", map[string]any{
		"key": "quest", "label": "Quest", "label_plural": "Quests",
		"expected_version": 7,
	})
	body := assertError(t, rec, http.StatusConflict, "version_conflict", "")
	if body.Details.CurrentVersion == nil || *body.Details.CurrentVersion != 1 {
		t.Fatalf("details = %+v, want the current version 1: %s", body.Details, rec.Body.String())
	}
}

// TestARouteShapedKeyIsStillAddressable is decision 1 of this task,
// proved rather than argued: row keys permit `new`, `index`, `id`,
// `null`, `games` and `types`, so the route shape must not put a key in
// a position where a literal segment could ever claim it. Every key
// here sits behind a fixed `by-key` (or `by-id`) discriminator, which is
// the segment no key can occupy, so none of these can collide with
// anything this router serves — including with the discriminators
// themselves, which are perfectly legal keys too.
func TestARouteShapedKeyIsStillAddressable(t *testing.T) {
	f := newRESTFixture(t)
	keys := []string{"new", "index", "id", "null", "games", "types", "by-key", "by-id", "search"}
	for _, key := range keys {
		rec := f.as(t, http.MethodPost, "/types", map[string]any{
			"key": key, "label": key, "label_plural": key + "s",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("declare %q = %d: %s", key, rec.Code, rec.Body.String())
		}
	}
	for _, key := range keys {
		rec := f.as(t, http.MethodGet, "/types/by-key/"+key, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("get %q = %d: %s", key, rec.Code, rec.Body.String())
		}
		var detail struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		}
		decodeBody(t, rec, &detail)
		if detail.Key != key {
			t.Fatalf("get %q answered with %q", key, detail.Key)
		}
		// And the id route reaches the same row: a removal addressed by
		// id must not be able to land on a different type than the one
		// the key route just showed.
		removed := f.as(t, http.MethodDelete, "/types/by-id/"+detail.ID, nil)
		if removed.Code != http.StatusOK {
			t.Fatalf("remove %q = %d: %s", key, removed.Code, removed.Body.String())
		}
	}
	rec := f.as(t, http.MethodGet, "/types", nil)
	var payload struct {
		Items []json.RawMessage `json:"items"`
	}
	decodeBody(t, rec, &payload)
	if len(payload.Items) != 0 {
		t.Fatalf("items = %d, want every route-shaped key removed by id", len(payload.Items))
	}
}

// TestAGameFieldNamedLikeARowColumnNeverShadowsIt is decision 2, proved
// the same way: nothing stops a game declaring fields called `key`,
// `name`, `id` or `version`, and this task is where a row is rendered
// for a page. The answer is that no surface here flattens — a game's
// values stay in their own `fields` object, exactly as they do in the
// database and on the MCP wire — so the two namespaces cannot collide
// and no key needs reserving.
func TestAGameFieldNamedLikeARowColumnNeverShadowsIt(t *testing.T) {
	f := newRESTFixture(t)
	rec := f.as(t, http.MethodPost, "/types", map[string]any{
		"key": "quest", "label": "Quest", "label_plural": "Quests",
		"field_schema": []any{
			map[string]any{"key": "id", "type": "text"},
			map[string]any{"key": "key", "type": "text"},
			map[string]any{"key": "name", "type": "text"},
			map[string]any{"key": "version", "type": "text"},
			map[string]any{"key": "invalid", "type": "text"},
			map[string]any{"key": "type_key", "type": "text"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("declare = %d: %s", rec.Code, rec.Body.String())
	}

	shadow := map[string]any{
		"id": "not-a-uuid", "key": "not-the-key", "name": "Not The Name",
		"version": "not-a-version", "invalid": "not-a-bool", "type_key": "not-the-type",
	}
	written := f.as(t, http.MethodPost, "/entities", map[string]any{
		"items": []any{map[string]any{
			"type_key": "quest", "key": "hogger", "name": "Wanted: Hogger", "fields": shadow,
		}},
	})
	if written.Code != http.StatusOK {
		t.Fatalf("write = %d: %s", written.Code, written.Body.String())
	}

	got := f.as(t, http.MethodGet, "/entities/by-key/quest/hogger", nil)
	if got.Code != http.StatusOK {
		t.Fatalf("read = %d: %s", got.Code, got.Body.String())
	}
	var entity struct {
		ID      string         `json:"id"`
		TypeKey string         `json:"type_key"`
		Key     string         `json:"key"`
		Name    string         `json:"name"`
		Version int32          `json:"version"`
		Invalid bool           `json:"invalid"`
		Fields  map[string]any `json:"fields"`
	}
	decodeBody(t, got, &entity)
	if _, err := uuid.Parse(entity.ID); err != nil {
		t.Fatalf("id = %q, want the row's own uuid: %s", entity.ID, got.Body.String())
	}
	if entity.Key != "hogger" || entity.Name != "Wanted: Hogger" || entity.TypeKey != "quest" {
		t.Fatalf("row = %+v, want the row's own identity, not the game's field values", entity)
	}
	if entity.Version != 1 || entity.Invalid {
		t.Fatalf("row = %+v, want version 1 and invalid false", entity)
	}
	for key, want := range shadow {
		if entity.Fields[key] != want {
			t.Fatalf("fields[%q] = %v, want %v", key, entity.Fields[key], want)
		}
	}
}

// TestRESTWritesAreRefusedToAViewer pins the one thing the REST surface
// has to decide that the MCP surface never faced: a token is
// editor-equivalent by construction, but a session caller's role is real
// and a viewer must not be able to write a game's content.
func TestRESTWritesAreRefusedToAViewer(t *testing.T) {
	f := newRESTFixture(t)
	ctx := context.Background()
	questType(t, f)

	viewer, err := f.ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "viewer@studio.com", DisplayName: "Viewer", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := f.proj.SetRole(ctx, viewer.ID, f.game, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	cookie := loginAs(t, f.srv, "viewer@studio.com")

	if rec := f.call(t, cookie, http.MethodGet, f.path("/types"), nil); rec.Code != http.StatusOK {
		t.Fatalf("viewer list = %d: %s", rec.Code, rec.Body.String())
	}
	for _, call := range []struct {
		method, suffix string
		body           any
	}{
		{http.MethodPost, "/types", map[string]any{"key": "zone", "label": "Zone", "label_plural": "Zones"}},
		{http.MethodPost, "/entities", map[string]any{"items": []any{
			map[string]any{"type_key": "quest", "key": "hogger", "name": "Hogger"}}}},
		{http.MethodDelete, "/types/by-id/" + uuid.NewString(), nil},
	} {
		rec := f.call(t, cookie, call.method, f.path(call.suffix), call.body)
		assertError(t, rec, http.StatusForbidden, "forbidden", "")
		if !strings.Contains(rec.Body.String(), "viewer") {
			t.Fatalf("%s %s said %q, want it to name the role that cannot write",
				call.method, call.suffix, rec.Body.String())
		}
	}
}

// TestAStatedProjectIDMustAgreeWithTheURL mirrors ScopedArgs's own rule
// onto this surface. A body naming a different game than the URL is a
// caller that has lost track of which game it is editing, and answering
// it by silently ignoring the field — which is what "the URL wins" would
// mean in practice — is how content lands in the wrong game.
func TestAStatedProjectIDMustAgreeWithTheURL(t *testing.T) {
	f := newRESTFixture(t)
	other, err := f.proj.Create(context.Background(), "le-mans", "Le Mans", f.ownerID)
	if err != nil {
		t.Fatalf("Create other game: %v", err)
	}

	rec := f.as(t, http.MethodPost, "/types", map[string]any{
		"project_id": other.ID.String(),
		"key":        "quest", "label": "Quest", "label_plural": "Quests",
	})
	assertError(t, rec, http.StatusForbidden, "scope_violation", "")

	// And the same body naming this game is accepted, so the check is a
	// disagreement check and not a blanket refusal of the field.
	ok := f.as(t, http.MethodPost, "/types", map[string]any{
		"project_id": f.game.String(),
		"key":        "quest", "label": "Quest", "label_plural": "Quests",
	})
	if ok.Code != http.StatusOK {
		t.Fatalf("agreeing project_id = %d: %s", ok.Code, ok.Body.String())
	}
}

// TestRESTListingBoundsComeThroughUnchanged pins that the bounds the
// domain enforces reach this surface as the caller's own problem, at
// the caller's own path, rather than as a 500.
func TestRESTListingBoundsComeThroughUnchanged(t *testing.T) {
	f := newRESTFixture(t)
	questType(t, f)

	assertError(t, f.as(t, http.MethodGet, "/entities?cursor=not-a-cursor", nil),
		http.StatusBadRequest, "invalid_input", "cursor")
	assertError(t, f.as(t, http.MethodGet, "/entities?limit=lots", nil),
		http.StatusBadRequest, "invalid_input", "limit")
	assertError(t, f.as(t, http.MethodGet, "/search?query="+strings.Repeat("a", metamodel.MaxSearchQuery+1), nil),
		http.StatusBadRequest, "invalid_input", "query")
	assertError(t, f.as(t, http.MethodGet, "/relations?source_id=nope", nil),
		http.StatusBadRequest, "invalid_input", "source_id")
	assertError(t, f.as(t, http.MethodDelete, "/entities/by-id/nope", nil),
		http.StatusBadRequest, "invalid_input", "id")
}

// TestRESTRelationsListNamesItsEndpointsByRef chases Task 7's finding 18
// into this surface too: the graph view faces the same question the MCP
// tool did, and both now answer it from the same code.
func TestRESTRelationsListNamesItsEndpointsByRef(t *testing.T) {
	f := newRESTFixture(t)

	quest := f.as(t, http.MethodPost, "/types", map[string]any{
		"key": "quest", "label": "Quest", "label_plural": "Quests"})
	zone := f.as(t, http.MethodPost, "/types", map[string]any{
		"key": "zone", "label": "Zone", "label_plural": "Zones"})
	var questID, zoneID struct {
		ID string `json:"id"`
	}
	decodeBody(t, quest, &questID)
	decodeBody(t, zone, &zoneID)

	if rec := f.as(t, http.MethodPost, "/relation-types", map[string]any{
		"key": "takes_place_in", "label": "takes place in",
		"source_type_ids": []string{questID.ID}, "target_type_ids": []string{zoneID.ID},
	}); rec.Code != http.StatusOK {
		t.Fatalf("relation type = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := f.as(t, http.MethodPost, "/entities", map[string]any{"items": []any{
		map[string]any{"type_key": "quest", "key": "hogger", "name": "Wanted: Hogger"},
		map[string]any{"type_key": "zone", "key": "elwynn", "name": "Elwynn Forest"},
	}}); rec.Code != http.StatusOK {
		t.Fatalf("entities = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := f.as(t, http.MethodPost, "/relations", map[string]any{"items": []any{
		map[string]any{"type_key": "takes_place_in",
			"source": map[string]any{"type_key": "quest", "key": "hogger"},
			"target": map[string]any{"type_key": "zone", "key": "elwynn"}},
	}}); rec.Code != http.StatusOK {
		t.Fatalf("relation = %d: %s", rec.Code, rec.Body.String())
	}

	rec := f.as(t, http.MethodGet, "/relations", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	var page struct {
		Items []struct {
			SourceID string `json:"source_id"`
			Source   *struct {
				TypeKey string `json:"type_key"`
				Key     string `json:"key"`
				Name    string `json:"name"`
			} `json:"source"`
			Target *struct {
				Key string `json:"key"`
			} `json:"target"`
		} `json:"items"`
	}
	decodeBody(t, rec, &page)
	if len(page.Items) != 1 {
		t.Fatalf("items = %+v, want the one edge", page.Items)
	}
	edge := page.Items[0]
	if edge.Source == nil || edge.Target == nil {
		t.Fatalf("edge = %+v, want both endpoints resolved", edge)
	}
	if edge.Source.Key != "hogger" || edge.Source.TypeKey != "quest" || edge.Source.Name != "Wanted: Hogger" {
		t.Fatalf("source = %+v, want the quest the edge was written with", edge.Source)
	}
	if edge.Target.Key != "elwynn" {
		t.Fatalf("target = %+v, want the zone", edge.Target)
	}
	if _, err := uuid.Parse(edge.SourceID); err != nil {
		t.Fatalf("source_id = %q, want the id a removal addresses", edge.SourceID)
	}
}

// TestRESTIsIsolatedByTheURLsGameAndNothingElse is the isolation guard
// for this surface: a member of one game reading another game's content
// route is refused, and the refusal is about membership rather than
// about the row.
func TestRESTIsIsolatedByTheURLsGameAndNothingElse(t *testing.T) {
	f := newRESTFixture(t)
	ctx := context.Background()
	questType(t, f)

	outsider, err := f.ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "outsider@studio.com", DisplayName: "Outsider", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	theirs, err := f.proj.Create(ctx, "le-mans", "Le Mans", outsider.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cookie := loginAs(t, f.srv, "outsider@studio.com")

	// Their own game answers, and holds none of this game's types.
	own := f.call(t, cookie, http.MethodGet, "/api/games/"+theirs.ID.String()+"/types", nil)
	if own.Code != http.StatusOK {
		t.Fatalf("own game = %d: %s", own.Code, own.Body.String())
	}
	var payload struct {
		Items []json.RawMessage `json:"items"`
	}
	decodeBody(t, own, &payload)
	if len(payload.Items) != 0 {
		t.Fatalf("items = %d, want another game's types to be invisible", len(payload.Items))
	}

	// This game does not.
	assertError(t, f.call(t, cookie, http.MethodGet, f.path("/types/by-key/quest"), nil),
		http.StatusForbidden, "forbidden", "")
}
