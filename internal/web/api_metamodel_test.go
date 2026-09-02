package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
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

// TestEveryContentWriteRouteRefusesAViewer pins the one thing the REST
// surface has to decide that the MCP surface never faced: a token is
// editor-equivalent by construction, but a session caller's role is real
// and a viewer must not be able to write a game's content.
//
// It drives *every* write route the server actually registered, read
// back from the routing table rather than typed out here, because the
// hand-written version of this test covered three of the eight and a
// review stripped the check from five handlers without failing anything.
// A write route added tomorrow appears in this table on its own.
func TestEveryContentWriteRouteRefusesAViewer(t *testing.T) {
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

	// A viewer still reads: the refusal below has to be about writing,
	// not about being shut out of the game.
	if rec := f.call(t, cookie, http.MethodGet, f.path("/types"), nil); rec.Code != http.StatusOK {
		t.Fatalf("viewer list = %d: %s", rec.Code, rec.Body.String())
	}

	writes := f.srv.ContentWritePatternsForTest()
	if len(writes) == 0 {
		t.Fatal("the server registered no content write routes — this test would pass vacuously")
	}
	for _, pattern := range writes {
		method, path, ok := strings.Cut(pattern, " ")
		if !ok {
			t.Fatalf("pattern %q names no method", pattern)
		}
		// A body that would be perfectly acceptable from an editor, so
		// the refusal cannot be blamed on the request itself. Every
		// wildcard but {game} is filled with a value that resolves to
		// nothing: the check has to happen before any of it is read.
		path = strings.ReplaceAll(path, "{game}", f.game.String())
		path = wildcards.ReplaceAllString(path, uuid.NewString())
		var body any
		if method != http.MethodDelete {
			body = map[string]any{"key": "zone", "label": "Zone", "label_plural": "Zones",
				"items": []any{map[string]any{"type_key": "quest", "key": "hogger", "name": "Hogger"}}}
		}

		// A subtest per route, so a run reports every write a viewer
		// got through rather than stopping at the first.
		t.Run(pattern, func(t *testing.T) {
			rec := f.call(t, cookie, method, path, body)
			assertError(t, rec, http.StatusForbidden, "forbidden", "")
			if !strings.Contains(rec.Body.String(), "viewer") {
				t.Errorf("%s said %q, want it to name the role that cannot write", pattern, rec.Body.String())
			}
		})
	}
}

// wildcards matches a ServeMux path wildcard, for the table above.
var wildcards = regexp.MustCompile(`\{[^}]+\}`)

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

// TestTheGameSummaryCountsContentWithoutListingIt is the home page's
// only server-side requirement, and the reason it is a separate
// endpoint rather than the SPA counting a listing itself: the answer
// must stay the same size whether a game holds four entities or four
// hundred. This seeds enough rows that a listing would be obvious in
// the payload, and asserts none of them is in it.
func TestTheGameSummaryCountsContentWithoutListingIt(t *testing.T) {
	f := newRESTFixture(t)
	questType(t, f)
	if rec := f.as(t, http.MethodPost, "/types", map[string]any{
		"key": "zone", "label": "Zone", "label_plural": "Zones"}); rec.Code != http.StatusOK {
		t.Fatalf("zone type = %d: %s", rec.Code, rec.Body.String())
	}

	items := make([]any, 0, 120)
	for i := range 100 {
		items = append(items, map[string]any{
			"type_key": "quest", "key": "quest-" + strconv.Itoa(i), "name": "Quest " + strconv.Itoa(i)})
	}
	for i := range 20 {
		items = append(items, map[string]any{
			"type_key": "zone", "key": "zone-" + strconv.Itoa(i), "name": "Zone " + strconv.Itoa(i)})
	}
	if rec := f.as(t, http.MethodPost, "/entities", map[string]any{"items": items}); rec.Code != http.StatusOK {
		t.Fatalf("seed = %d: %s", rec.Code, rec.Body.String())
	}

	rec := f.as(t, http.MethodGet, "/summary", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("summary = %d: %s", rec.Code, rec.Body.String())
	}
	var summary gameSummary
	decodeBody(t, rec, &summary)

	counts := map[string]int64{}
	for _, typ := range summary.EntityTypes {
		counts[typ.Key] = typ.EntityCount
	}
	if counts["quest"] != 100 || counts["zone"] != 20 {
		t.Fatalf("counts = %+v, want 100 quests and 20 zones", counts)
	}
	if summary.Totals.Entities != 120 || summary.Totals.Relations != 0 || summary.Totals.Invalid != 0 {
		t.Fatalf("totals = %+v, want 120 entities and nothing else", summary.Totals)
	}
	// The payload is a catalogue, not a listing: no individual row of
	// the hundred and twenty appears in it.
	if strings.Contains(rec.Body.String(), "quest-42") {
		t.Fatalf("the summary carries individual entities: %s", rec.Body.String())
	}
}

// TestTheGameSummaryOfAnEmptyGameIsAnEmptyCatalogue pins what the home
// page shows a designer who has just created a game: a well-formed
// answer with nothing in it, never an error and never a 404. The page's
// own empty state is what it renders from this.
func TestTheGameSummaryOfAnEmptyGameIsAnEmptyCatalogue(t *testing.T) {
	f := newRESTFixture(t)
	rec := f.as(t, http.MethodGet, "/summary", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("summary = %d: %s", rec.Code, rec.Body.String())
	}
	var summary gameSummary
	decodeBody(t, rec, &summary)
	if summary.EntityTypes == nil || summary.RelationTypes == nil {
		t.Fatalf("summary = %+v, want empty arrays a page can iterate, not nulls: %s",
			summary, rec.Body.String())
	}
	if len(summary.EntityTypes) != 0 || len(summary.RelationTypes) != 0 {
		t.Fatalf("summary = %+v, want nothing declared yet", summary)
	}
	if summary.Totals.Entities != 0 || summary.Totals.Relations != 0 || summary.Totals.Invalid != 0 {
		t.Fatalf("totals = %+v, want zeros", summary.Totals)
	}
}

// TestTheGameSummaryCountsTheRowsAScemaEditInvalidated is the one number
// on the home page a designer has to act on: an entity whose values no
// longer fit its type is kept, marked, and counted here, per type and in
// the total.
func TestTheGameSummaryCountsTheRowsASchemaEditInvalidated(t *testing.T) {
	f := newRESTFixture(t)
	questType(t, f)
	if rec := f.as(t, http.MethodPost, "/entities", map[string]any{"items": []any{
		map[string]any{"type_key": "quest", "key": "hogger", "name": "Wanted: Hogger"},
		map[string]any{"type_key": "quest", "key": "kobolds", "name": "Kobolds",
			"fields": map[string]any{"min_level": 5}},
	}}); rec.Code != http.StatusOK {
		t.Fatalf("seed = %d: %s", rec.Code, rec.Body.String())
	}

	// Narrowing the schema: min_level becomes required, and the row
	// that never carried one stops fitting.
	if rec := f.as(t, http.MethodPost, "/types", map[string]any{
		"key": "quest", "label": "Quest", "label_plural": "Quests", "expected_version": 1,
		"field_schema": []any{map[string]any{"key": "min_level", "type": "number", "required": true}},
	}); rec.Code != http.StatusOK {
		t.Fatalf("narrow = %d: %s", rec.Code, rec.Body.String())
	}

	rec := f.as(t, http.MethodGet, "/summary", nil)
	var summary gameSummary
	decodeBody(t, rec, &summary)
	if len(summary.EntityTypes) != 1 {
		t.Fatalf("entity_types = %+v, want the one type", summary.EntityTypes)
	}
	quest := summary.EntityTypes[0]
	if quest.EntityCount != 2 || quest.InvalidCount != 1 {
		t.Fatalf("quest = %+v, want 2 entities of which 1 invalid", quest)
	}
	if summary.Totals.Invalid != 1 {
		t.Fatalf("totals = %+v, want one invalid row", summary.Totals)
	}
}

// gameSummary is GET /api/games/{game}/summary's answer, as a client
// reads it.
type gameSummary struct {
	EntityTypes []struct {
		ID           string `json:"id"`
		Key          string `json:"key"`
		Label        string `json:"label"`
		LabelPlural  string `json:"label_plural"`
		EntityCount  int64  `json:"entity_count"`
		InvalidCount int64  `json:"invalid_count"`
	} `json:"entity_types"`
	RelationTypes []struct {
		ID            string `json:"id"`
		Key           string `json:"key"`
		Label         string `json:"label"`
		RelationCount int64  `json:"relation_count"`
	} `json:"relation_types"`
	Totals struct {
		Entities  int64 `json:"entities"`
		Relations int64 `json:"relations"`
		Invalid   int64 `json:"invalid"`
	} `json:"totals"`
}

// TestATokenMayReadItsOwnGamesContentAndNoOthers pins the one thing the
// REST content routes decide about token callers that the rest of the
// REST surface decides the other way. Creating games, membership, tokens
// and invites are closed to a token outright (requireHumanCaller); a
// game's content is not, because a token *is* a credential for exactly
// one game's content and refusing it here would deny over REST what the
// same token already does over MCP. What still holds is the binding:
// the game in the URL must be the game the token is bound to.
func TestATokenMayReadItsOwnGamesContentAndNoOthers(t *testing.T) {
	f := newRESTFixture(t)
	ctx := context.Background()
	questType(t, f)

	other, err := f.proj.Create(ctx, "le-mans", "Le Mans", f.ownerID)
	if err != nil {
		t.Fatalf("Create other game: %v", err)
	}
	token, _, err := f.ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: f.game, UserID: f.ownerID, Label: "agent",
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	send := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		f.srv.ServeHTTP(rec, req)
		return rec
	}

	own := send(f.path("/types"))
	if own.Code != http.StatusOK {
		t.Fatalf("own game = %d: %s", own.Code, own.Body.String())
	}
	assertError(t, send("/api/games/"+other.ID.String()+"/types"),
		http.StatusForbidden, "scope_violation", "")
}

// invalidAndValidQuests declares the quest type, writes two rows, then
// narrows the schema so exactly one of them stops fitting. It returns
// the key of the row that is now invalid and the key of the one that is
// still valid.
func invalidAndValidQuests(t *testing.T, f restFixture) (invalid, valid string) {
	t.Helper()
	questType(t, f)
	if rec := f.as(t, http.MethodPost, "/entities", map[string]any{"items": []any{
		map[string]any{"type_key": "quest", "key": "hogger", "name": "Wanted: Hogger"},
		map[string]any{"type_key": "quest", "key": "kobolds", "name": "Kobolds",
			"fields": map[string]any{"min_level": 5}},
	}}); rec.Code != http.StatusOK {
		t.Fatalf("seed = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := f.as(t, http.MethodPost, "/types", map[string]any{
		"key": "quest", "label": "Quest", "label_plural": "Quests", "expected_version": 1,
		"field_schema": []any{map[string]any{"key": "min_level", "type": "number", "required": true}},
	}); rec.Code != http.StatusOK {
		t.Fatalf("narrow = %d: %s", rec.Code, rec.Body.String())
	}
	return "hogger", "kobolds"
}

// entityKeys lists entities under the given query string and returns
// their keys.
func entityKeys(t *testing.T, f restFixture, query string) []string {
	t.Helper()
	rec := f.as(t, http.MethodGet, "/entities"+query, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list %q = %d: %s", query, rec.Code, rec.Body.String())
	}
	var page struct {
		Items []struct {
			Key string `json:"key"`
		} `json:"items"`
	}
	decodeBody(t, rec, &page)
	keys := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		keys = append(keys, item.Key)
	}
	return keys
}

// TestTheInvalidFilterIsTriStateAndRefusesAnythingElse pins the filter
// that decides whether a designer sees the rows the home page told them
// to fix. `invalid` has three states — absent, true, false — so the
// leniency the flag parameters get does not apply to it: an unrecognised
// spelling has no "other meaning" to fall back on, and reading it as
// false answers "show me the broken rows" with exactly the rows that are
// fine. It is refused at its own path instead.
func TestTheInvalidFilterIsTriStateAndRefusesAnythingElse(t *testing.T) {
	f := newRESTFixture(t)
	invalid, valid := invalidAndValidQuests(t, f)

	if got := entityKeys(t, f, "?type_key=quest"); len(got) != 2 {
		t.Fatalf("unfiltered = %v, want both rows", got)
	}
	if got := entityKeys(t, f, "?type_key=quest&invalid=true"); len(got) != 1 || got[0] != invalid {
		t.Fatalf("invalid=true = %v, want only %q", got, invalid)
	}
	if got := entityKeys(t, f, "?type_key=quest&invalid=false"); len(got) != 1 || got[0] != valid {
		t.Fatalf("invalid=false = %v, want only %q", got, valid)
	}
	// The other accepted spellings of each side, so the parsing is
	// pinned and not only the two canonical words.
	if got := entityKeys(t, f, "?type_key=quest&invalid=1"); len(got) != 1 || got[0] != invalid {
		t.Fatalf("invalid=1 = %v, want only %q", got, invalid)
	}
	if got := entityKeys(t, f, "?type_key=quest&invalid=0"); len(got) != 1 || got[0] != valid {
		t.Fatalf("invalid=0 = %v, want only %q", got, valid)
	}

	for _, raw := range []string{"maybe", "nope", "sometimes", "2"} {
		rec := f.as(t, http.MethodGet, "/entities?type_key=quest&invalid="+raw, nil)
		assertError(t, rec, http.StatusBadRequest, "invalid_input", "invalid")
	}
}

// TestAStatedProjectIDIsJudgedTheWayTheMCPSurfaceJudgesIt closes the two
// divergences a review found between checkStatedProject and the rule
// ScopedArgs states, both of which this surface used to get wrong: a
// project_id that is not a uuid names no game at all, so calling it "a
// different game than the URL" is false and `scope_violation` is the
// wrong code; and an empty project_id is not the same as no project_id —
// the MCP surface refuses it, and accepting it here made the field's
// presence mean nothing.
func TestAStatedProjectIDIsJudgedTheWayTheMCPSurfaceJudgesIt(t *testing.T) {
	f := newRESTFixture(t)
	declare := func(projectID any) *httptest.ResponseRecorder {
		return f.as(t, http.MethodPost, "/types", map[string]any{
			"project_id": projectID,
			"key":        "quest", "label": "Quest", "label_plural": "Quests",
		})
	}

	// Not a uuid: the caller's own argument, not a scope violation.
	body := assertError(t, declare("not-a-uuid"), http.StatusBadRequest, "bad_request", "")
	if !strings.Contains(body.Message, "uuid") {
		t.Errorf("message = %q, want it to say the value is not a uuid", body.Message)
	}

	// Empty: present and unparseable, which is the same refusal. The
	// field is a confirmation, and an empty confirmation confirms
	// nothing.
	assertError(t, declare(""), http.StatusBadRequest, "bad_request", "")

	// And the rule the field exists for still holds in both directions.
	other, err := f.proj.Create(context.Background(), "monza", "Monza", f.ownerID)
	if err != nil {
		t.Fatalf("Create other game: %v", err)
	}
	assertError(t, declare(other.ID.String()), http.StatusForbidden, "scope_violation", "")
	if rec := declare(f.game.String()); rec.Code != http.StatusOK {
		t.Fatalf("agreeing project_id = %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAWrongTypedFieldIsNamed pins what a caller is told when the body
// is valid JSON but one field is the wrong type. It used to be
// "malformed JSON body", which is false — the JSON parsed — and named
// neither the field nor what was wrong with it, on a surface where every
// other refusal carries the path it is about.
func TestAWrongTypedFieldIsNamed(t *testing.T) {
	f := newRESTFixture(t)
	questType(t, f)

	for _, tc := range []struct {
		name, suffix string
		body         string
		path         string
	}{
		{"a string where a string is not expected", "/types",
			`{"key":123,"label":"Quest","label_plural":"Quests"}`, "key"},
		{"a version that is not a number", "/types",
			`{"key":"quest","label":"Quest","label_plural":"Quests","expected_version":"one"}`, "expected_version"},
		{"a batch that is not a list", "/entities", `{"items":"not-an-array"}`, "items"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, f.path(tc.suffix), strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(f.cookie)
			rec := httptest.NewRecorder()
			f.srv.ServeHTTP(rec, req)

			got := assertError(t, rec, http.StatusBadRequest, "bad_request", tc.path)
			if strings.Contains(got.Message, "malformed") {
				t.Errorf("message = %q, but this body is well-formed JSON", got.Message)
			}
		})
	}

	// A body that really is malformed still says so, and names no path
	// it cannot know.
	req := httptest.NewRequest(http.MethodPost, f.path("/types"), strings.NewReader(`{"key":`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	if got := assertError(t, rec, http.StatusBadRequest, "bad_request", ""); !strings.Contains(got.Message, "malformed") {
		t.Errorf("message = %q, want it to say the body is malformed", got.Message)
	}
}

// TestASeedSizedBatchIsAccepted pins maxContentRequestBodyBytes' whole
// justification: this surface's bound exists because the 16 KiB that is
// right for a login would refuse an ordinary seed as malformed. A batch
// far over that limit and far under this one has to land.
func TestASeedSizedBatchIsAccepted(t *testing.T) {
	f := newRESTFixture(t)
	questType(t, f)

	items := make([]any, 0, 300)
	for i := 0; i < 300; i++ {
		items = append(items, map[string]any{
			"type_key": "quest",
			"key":      "quest-" + strconv.Itoa(i),
			"name":     "Quest " + strconv.Itoa(i) + ": " + strings.Repeat("a long designed name ", 6),
		})
	}
	body, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(body) <= 16*1024 {
		t.Fatalf("the batch is %d bytes, too small to prove anything about a 16 KiB bound", len(body))
	}

	req := httptest.NewRequest(http.MethodPost, f.path("/entities"), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("seed of %d bytes = %d: %s", len(body), rec.Code, rec.Body.String())
	}
	// A batch answers 200 with a report even when rows failed, so the
	// status alone would pass for a request that landed nothing.
	var report struct {
		Written []struct {
			Key string `json:"key"`
		} `json:"written"`
		Failed []any `json:"failed"`
	}
	decodeBody(t, rec, &report)
	if len(report.Written) != len(items) || len(report.Failed) != 0 {
		t.Fatalf("wrote %d of %d rows, %d failed: %s",
			len(report.Written), len(items), len(report.Failed), rec.Body.String())
	}
}

// TestAnIncompleteTraversalIsRefusedAndNeverAnsweredWithTheWholeGame
// pins hasRelatedTo's "any part, not all four". Read as "all four", a
// query naming one part of the traversal and forgetting the rest falls
// through to an ordinary listing — which answers a caller who asked for
// one entity's neighbours with every entity in the game, the exact
// failure that function's comment argues against.
func TestAnIncompleteTraversalIsRefusedAndNeverAnsweredWithTheWholeGame(t *testing.T) {
	f := newRESTFixture(t)
	invalidAndValidQuests(t, f)

	// Three of the four permutations come back at the missing part's own
	// field path. The fourth is recorded here rather than fixed: with
	// only a direction, the domain resolves the relation type first and
	// answers 404 for the empty key, so the refusal names the type it
	// could not find instead of the parts that were missing. It is the
	// same answer the MCP surface gives — the two share the core — so
	// parity holds and the honest statement is "refused, at its own path
	// in three cases out of four".
	for _, tc := range []struct {
		query  string
		status int
		code   string
		path   string
	}{
		{"?related_to.entity_key=hogger", http.StatusBadRequest, "invalid_input", "related_to.direction"},
		{"?related_to.entity_type_key=quest", http.StatusBadRequest, "invalid_input", "related_to.direction"},
		{"?related_to.relation_type_key=takes_place_in", http.StatusBadRequest, "invalid_input", "related_to.direction"},
		{"?related_to.direction=outgoing", http.StatusNotFound, "not_found", ""},
	} {
		rec := f.as(t, http.MethodGet, "/entities"+tc.query, nil)
		if rec.Code == http.StatusOK {
			t.Fatalf("%s = 200 with %s, want a refusal rather than the whole game",
				tc.query, rec.Body.String())
		}
		assertError(t, rec, tc.status, tc.code, tc.path)
	}
}

// TestRemovingATypeStillInUseIsAConflict pins the status the domain's
// own refusal gets. in_use is not the caller being wrong — the request
// was well formed and the type is real — it is the world holding on to
// the row, which is what 409 says and 400 does not.
func TestRemovingATypeStillInUseIsAConflict(t *testing.T) {
	f := newRESTFixture(t)
	questType(t, f)
	if rec := f.as(t, http.MethodPost, "/entities", map[string]any{"items": []any{
		map[string]any{"type_key": "quest", "key": "hogger", "name": "Wanted: Hogger"},
	}}); rec.Code != http.StatusOK {
		t.Fatalf("seed = %d: %s", rec.Code, rec.Body.String())
	}
	var declared struct {
		ID string `json:"id"`
	}
	decodeBody(t, f.as(t, http.MethodGet, "/types/by-key/quest", nil), &declared)

	rec := f.as(t, http.MethodDelete, "/types/by-id/"+declared.ID, nil)
	assertError(t, rec, http.StatusConflict, "in_use", "")

	// And with cascade the same removal succeeds, so the conflict is
	// about the entities and not about the route.
	if rec := f.as(t, http.MethodDelete, "/types/by-id/"+declared.ID+"?cascade=true", nil); rec.Code != http.StatusOK {
		t.Fatalf("cascade remove = %d: %s", rec.Code, rec.Body.String())
	}
}
