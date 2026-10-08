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

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/comments"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/views"
	"github.com/neverbot/maestro/internal/web"
)

// restFixture is a game, its owner's session cookie, and the services
// behind them. Every test in this file drives the REST surface exactly
// as a browser does: a session cookie, a JSON body, and the URL the SPA
// would have built.
type restFixture struct {
	srv      *web.Server
	ids      *identity.Service
	proj     *projects.Service
	mm       *metamodel.Service
	md       *markdown.Service
	log      *comments.Service
	game     uuid.UUID
	gameSlug string
	ownerID  uuid.UUID
	cookie   *http.Cookie
	agent    web.Caller
	lib      *views.Service
}

func newRESTFixture(t *testing.T) restFixture {
	t.Helper()
	srv, ids, projSvc, mm, md, log, lib := newMetamodelTestServer(t)
	ctx := context.Background()

	owner, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "owner@example.test", DisplayName: "Owner", Password: "password12345",
	})
	assert.Must(t, err == nil, "CreateUser: %v", err)
	game, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	assert.Must(t, err == nil, "Create game: %v", err)
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: game.ID, UserID: owner.ID, Label: "agent",
	})
	assert.Must(t, err == nil, "CreateAPIToken: %v", err)
	agent, err := web.CallerForToken(ctx, ids, token)
	assert.Must(t, err == nil, "CallerForToken: %v", err)
	return restFixture{
		srv: srv, ids: ids, proj: projSvc, mm: mm, md: md, log: log, lib: lib,
		game: game.ID, gameSlug: game.Slug, ownerID: owner.ID,
		cookie: loginAs(t, srv, "owner@example.test"),
		agent:  agent,
	}
}

// deps and caller let a REST test reach a tool core directly, which is
// what the tests that compare the two surfaces' answers need: the page
// and the tool are one assembly, and proving they agree means calling
// both against one game. The caller is a token caller because
// requireScope refuses a session one, which is the whole of what
// separates the two surfaces at this layer.
func (f restFixture) deps() web.MCPDeps {
	return web.MCPDeps{Identity: f.ids, Projects: f.proj, Metamodel: f.mm, Markdown: f.md,
		Comments: f.log, Views: f.lib}
}

func (f restFixture) caller() web.Caller { return f.agent }

// call sends one request as the given cookie holder and hands back the
// recorder. body is nil for a GET.
func (f restFixture) call(t *testing.T, cookie *http.Cookie, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		assert.Must(t, err == nil, "marshal body: %v", err)
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

// raw sends a body this fixture must not marshal: the point of several
// tests below is a body encoding/json will reject, or one carrying more
// than a single JSON value, neither of which survives a round trip
// through json.Marshal.
func (f restFixture) raw(t *testing.T, method, suffix, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, f.path(suffix), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

func (f restFixture) path(suffix string) string {
	return "/api/games/" + f.gameSlug + suffix
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
	assert.Must(t, rec.Code == status, "status = %d, want %d: %s", rec.Code, status, rec.Body.String())
	var body wireError
	decodeBody(t, rec, &body)
	assert.Must(t, body.Error == code, "error = %q, want %q: %s", body.Error, code, rec.Body.String())
	assert.Must(t, body.Message != "", "no message on %s", rec.Body.String())
	if path != "" {
		found := false
		for _, f := range body.Details.Fields {
			if f.Path == path {
				found = true
			}
		}
		assert.Must(t, found, "no field problem at path %q: %s", path, rec.Body.String())
	}
	return body
}

func questType(t *testing.T, f restFixture) {
	t.Helper()
	rec := f.as(t, http.MethodPost, "/types", map[string]any{
		"key": "quest", "label": "Quest", "label_plural": "Quests",
		"field_schema": []any{map[string]any{"key": "min_level", "type": "number"}},
	})
	assert.Must(t, rec.Code == http.StatusOK, "declare quest type = %d: %s", rec.Code, rec.Body.String())
}

func TestRESTTypesRequireMembership(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	if _, err := f.ids.CreateUser(context.Background(), identity.CreateUserRequest{
		Email: "stranger@example.test", DisplayName: "Stranger", Password: "password12345",
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	stranger := loginAs(t, f.srv, "stranger@example.test")

	// A stranger is told the game is not available to them, in the same
	// words a game that does not exist gets: a game is addressed by its
	// slug now and a slug is guessable, so the two must not be told
	// apart (resolveGameRef).
	rec := f.call(t, stranger, http.MethodGet, f.path("/types"), nil)
	assertError(t, rec, http.StatusNotFound, "not_found", "")
}

func TestRESTCreateAndListTypes(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	questType(t, f)

	rec := f.as(t, http.MethodGet, "/types", nil)
	assert.Must(t, rec.Code == http.StatusOK, "list = %d: %s", rec.Code, rec.Body.String())
	var payload struct {
		Items []struct {
			ID          string `json:"id"`
			Key         string `json:"key"`
			LabelPlural string `json:"label_plural"`
			Version     int32  `json:"version"`
		} `json:"items"`
	}
	decodeBody(t, rec, &payload)
	assert.Must(t, len(payload.Items) == 1 && payload.Items[0].Key == "quest", "items = %+v, want the one declared type", payload.Items)
	if payload.Items[0].Version != 1 || payload.Items[0].ID == "" {
		t.Fatalf("item = %+v, want an id and version 1", payload.Items[0])
	}

	// One type in full, addressed by key.
	one := f.as(t, http.MethodGet, "/types/by-key/quest", nil)
	assert.Must(t, one.Code == http.StatusOK, "get = %d: %s", one.Code, one.Body.String())
	var detail struct {
		Key    string `json:"key"`
		Schema []struct {
			Key string `json:"key"`
		} `json:"field_schema"`
	}
	decodeBody(t, one, &detail)
	assert.Must(t, detail.Key == "quest" && len(detail.Schema) == 1 && detail.Schema[0].Key == "min_level", "detail = %+v, want the declared field schema back", detail)
}

// TestRESTDeclaringABrokenSchemaIsInvalidSchema is where the plan's own
// snippet was wrong: it asserted a field schema declaring an enum with
// no options came back as `schema_violation`. It does not, and should
// not — Task 3's correction 25 split the three codes precisely so an
// agent (or a designer) knows which thing to fix. A broken *declaration*
// is `invalid_schema`; `schema_violation` is a stored value that no
// longer fits a schema. This pins the code the domain actually produces.
func TestRESTDeclaringABrokenSchemaIsInvalidSchema(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

// TestRESTAMergeReachesTheWriteThroughItsOwnName is the call-site half of
// the merge: the domain tests prove the behaviour, and this proves the
// argument an agent actually spells — fields_mode, on the body, over the
// REST mirror — arrives there. A JSON tag nobody posted would be a knob
// that works in Go and does nothing on the wire.
func TestRESTAMergeReachesTheWriteThroughItsOwnName(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	rec := f.as(t, http.MethodPost, "/types", map[string]any{
		"key": "quest", "label": "Quest", "label_plural": "Quests",
		"field_schema": []any{
			map[string]any{"key": "min_level", "type": "number"},
			map[string]any{"key": "summary", "type": "text"},
		},
	})
	assert.Must(t, rec.Code == http.StatusOK, "declare quest type = %d: %s", rec.Code, rec.Body.String())

	rec = f.as(t, http.MethodPost, "/entities", map[string]any{
		"items": []any{map[string]any{
			"type_key": "quest", "key": "hogger", "name": "Wanted: Hogger",
			"fields": map[string]any{"min_level": 10, "summary": "Kill Hogger."},
		}},
	})
	assert.Must(t, rec.Code == http.StatusOK, "create = %d: %s", rec.Code, rec.Body.String())

	rec = f.as(t, http.MethodPost, "/entities", map[string]any{
		"fields_mode": "merge",
		"items": []any{map[string]any{
			"type_key": "quest", "key": "hogger", "name": "Wanted: Hogger",
			"fields": map[string]any{"min_level": 12}, "expected_version": 1,
		}},
	})
	assert.Must(t, rec.Code == http.StatusOK, "merge = %d: %s", rec.Code, rec.Body.String())

	read := f.as(t, http.MethodGet, "/entities/by-key/quest/hogger", nil)
	assert.Must(t, read.Code == http.StatusOK, "read back = %d: %s", read.Code, read.Body.String())
	assert.Must(t, strings.Contains(read.Body.String(), "Kill Hogger."), "the row came back without the field the merge did not name: %s", read.Body.String())

	// And the misspelling, because the whole argument for refusing one is
	// that reading it as replace is silent.
	rec = f.as(t, http.MethodPost, "/entities", map[string]any{
		"fields_mode": "mrege",
		"items": []any{map[string]any{
			"type_key": "quest", "key": "hogger", "name": "Wanted: Hogger",
			"fields": map[string]any{"min_level": 13}, "expected_version": 2,
		}},
	})
	assertError(t, rec, http.StatusBadRequest, "invalid_input", "fields_mode")
}

// TestRESTAStaleVersionIsRefusedWithTheCurrentOne pins the conflict
// shape a browser needs to merge: the code, and the version the write
// would have met.
func TestRESTAStaleVersionIsRefusedWithTheCurrentOne(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	questType(t, f)

	rec := f.as(t, http.MethodPost, "/types", map[string]any{
		"key": "quest", "label": "Quest", "label_plural": "Quests",
		"expected_version": 7,
	})
	body := assertError(t, rec, http.StatusConflict, "version_conflict", "")
	assert.Must(t, body.Details.CurrentVersion != nil && *body.Details.CurrentVersion == 1, "details = %+v, want the current version 1: %s", body.Details, rec.Body.String())
}

// TestTheRenameRoutesMirrorTheirTools is what stops the REST rename from
// being a mirror that compiles and is never called: two surfaces, one
// core each, and the browser's route is the half a mirror most easily
// gets wrong.
func TestTheRenameRoutesMirrorTheirTools(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)

	declare := func(key string) string {
		rec := f.as(t, http.MethodPost, "/types", map[string]any{
			"key": key, "label": key, "label_plural": key + "s",
		})
		assert.Must(t, rec.Code == http.StatusOK, "declare %q = %d: %s", key, rec.Code, rec.Body.String())
		var out struct {
			ID string `json:"id"`
		}
		decodeBody(t, rec, &out)
		return out.ID
	}
	questID := declare("quest")
	declare("zone")

	rec := f.as(t, http.MethodPost, "/types/rename", map[string]any{
		"from": "quest", "to": "mission", "expected_version": 1,
	})
	assert.Must(t, rec.Code == http.StatusOK, "rename = %d: %s", rec.Code, rec.Body.String())
	var renamed struct {
		ID      string `json:"id"`
		Key     string `json:"key"`
		Version int32  `json:"version"`
	}
	decodeBody(t, rec, &renamed)
	assert.Must(t, renamed.ID == questID && renamed.Key == "mission" && renamed.Version == 2, "the rename answered %+v, want the same id under the new key at version 2",
		renamed)
	if got := f.as(t, http.MethodGet, "/types/by-key/quest", nil); got.Code != http.StatusNotFound {
		t.Fatalf("the old key = %d, want 404: the type moved, it was not copied", got.Code)
	}

	// A taken destination is the caller's own argument, so 400 rather
	// than the 409 a version conflict gets.
	taken := f.as(t, http.MethodPost, "/types/rename", map[string]any{
		"from": "mission", "to": "zone", "expected_version": 2,
	})
	assert.Must(t, taken.Code == http.StatusBadRequest, "renaming onto a taken key = %d, want 400: %s", taken.Code, taken.Body.String())
	var problem wireError
	decodeBody(t, taken, &problem)
	assert.Must(t, problem.Error == "invalid_input" && len(problem.Details.Fields) != 0 && problem.Details.Fields[0].Path == "to", "the refusal is %+v, want invalid_input reported at `to`", problem)

	stale := f.as(t, http.MethodPost, "/types/rename", map[string]any{
		"from": "mission", "to": "quest", "expected_version": 1,
	})
	assert.Must(t, stale.Code == http.StatusConflict, "a stale version = %d, want 409: %s", stale.Code, stale.Body.String())
	var conflict wireError
	decodeBody(t, stale, &conflict)
	assert.Must(t, conflict.Details.CurrentVersion != nil && *conflict.Details.CurrentVersion == 2, "the conflict is %+v, want the version to merge onto", conflict)

	// The relation-type twin, over its own route.
	if rec := f.as(t, http.MethodPost, "/relation-types", map[string]any{
		"key": "available_to", "label": "Available to",
	}); rec.Code != http.StatusOK {
		t.Fatalf("declare the relation type = %d: %s", rec.Code, rec.Body.String())
	}
	rec = f.as(t, http.MethodPost, "/relation-types/rename", map[string]any{
		"from": "available_to", "to": "usable_by", "expected_version": 1,
	})
	assert.Must(t, rec.Code == http.StatusOK, "rename the relation type = %d: %s", rec.Code, rec.Body.String())
	var movedEdgeType struct {
		Key     string `json:"key"`
		Version int32  `json:"version"`
	}
	decodeBody(t, rec, &movedEdgeType)
	assert.Must(t, movedEdgeType.Key == "usable_by" && movedEdgeType.Version == 2, "the relation type answered %+v, want usable_by at version 2", movedEdgeType)
}

// TestARouteShapedKeyIsStillAddressable is decision 1 of this task,
// proved rather than argued: row keys permit `new`, `index`, `id`,
// `null`, `games` and `types`, so the route shape must not put a key in
// a position where a literal segment could ever claim it. Every key
// here sits behind a fixed `by-key` discriminator, which is
// the segment no key can occupy, so none of these can collide with
// anything this router serves — including with the discriminators
// themselves, which are perfectly legal keys too.
func TestARouteShapedKeyIsStillAddressable(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	// `rename` joins the list with types.rename: the rename route is
	// POST /types/rename, a literal sibling of the collection route, so
	// a type keyed `rename` is exactly the collision the by-key
	// discriminator exists to make impossible. A route added without a
	// discriminator would make this key unreachable and nothing else
	// would fail.
	keys := []string{"new", "index", "id", "null", "games", "types", "by-key", "by-id",
		"search", "rename"}
	for _, key := range keys {
		rec := f.as(t, http.MethodPost, "/types", map[string]any{
			"key": key, "label": key, "label_plural": key + "s",
		})
		assert.Must(t, rec.Code == http.StatusOK, "declare %q = %d: %s", key, rec.Code, rec.Body.String())
	}
	for _, key := range keys {
		rec := f.as(t, http.MethodGet, "/types/by-key/"+key, nil)
		assert.Must(t, rec.Code == http.StatusOK, "get %q = %d: %s", key, rec.Code, rec.Body.String())
		var detail struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		}
		decodeBody(t, rec, &detail)
		assert.Must(t, detail.Key == key, "get %q answered with %q", key, detail.Key)
		// And the removal reaches the same row: since Metamodel 14 the
		// removal is addressed by key too, so a key shaped like a route
		// has to survive it as well as the read.
		removed := f.as(t, http.MethodDelete, "/types/by-key/"+key, nil)
		assert.Must(t, removed.Code == http.StatusOK, "remove %q = %d: %s", key, removed.Code, removed.Body.String())
	}
	rec := f.as(t, http.MethodGet, "/types", nil)
	var payload struct {
		Items []json.RawMessage `json:"items"`
	}
	decodeBody(t, rec, &payload)
	assert.Must(t, len(payload.Items) == 0, "items = %d, want every route-shaped key removed by id", len(payload.Items))
}

// TestAGameFieldNamedLikeARowColumnNeverShadowsIt is decision 2, proved
// the same way: nothing stops a game declaring fields called `key`,
// `name`, `id` or `version`, and this task is where a row is rendered
// for a page. The answer is that no surface here flattens — a game's
// values stay in their own `fields` object, exactly as they do in the
// database and on the MCP wire — so the two namespaces cannot collide
// and no key needs reserving.
func TestAGameFieldNamedLikeARowColumnNeverShadowsIt(t *testing.T) {
	t.Parallel()
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
	assert.Must(t, rec.Code == http.StatusOK, "declare = %d: %s", rec.Code, rec.Body.String())

	shadow := map[string]any{
		"id": "not-a-uuid", "key": "not-the-key", "name": "Not The Name",
		"version": "not-a-version", "invalid": "not-a-bool", "type_key": "not-the-type",
	}
	written := f.as(t, http.MethodPost, "/entities", map[string]any{
		"items": []any{map[string]any{
			"type_key": "quest", "key": "hogger", "name": "Wanted: Hogger", "fields": shadow,
		}},
	})
	assert.Must(t, written.Code == http.StatusOK, "write = %d: %s", written.Code, written.Body.String())

	got := f.as(t, http.MethodGet, "/entities/by-key/quest/hogger", nil)
	assert.Must(t, got.Code == http.StatusOK, "read = %d: %s", got.Code, got.Body.String())
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
	assert.Must(t, entity.Key == "hogger" && entity.Name == "Wanted: Hogger" && entity.TypeKey == "quest", "row = %+v, want the row's own identity, not the game's field values", entity)
	assert.Must(t, entity.Version == 1 && !entity.Invalid, "row = %+v, want version 1 and invalid false", entity)
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
func TestEveryContentWriteRouteRefusesAViewer(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	ctx := context.Background()
	questType(t, f)

	viewer, err := f.ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "viewer@example.test", DisplayName: "Viewer", Password: "password12345",
	})
	assert.Must(t, err == nil, "CreateUser: %v", err)
	if _, err := f.proj.SetRole(ctx, viewer.ID, f.game, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	cookie := loginAs(t, f.srv, "viewer@example.test")

	// A viewer still reads: the refusal below has to be about writing,
	// not about being shut out of the game.
	if rec := f.call(t, cookie, http.MethodGet, f.path("/types"), nil); rec.Code != http.StatusOK {
		t.Fatalf("viewer list = %d: %s", rec.Code, rec.Body.String())
	}

	writes := f.srv.ContentWritePatternsForTest()
	assert.Must(t, len(writes) != 0, "the server registered no content write routes — this test would pass vacuously")
	for _, pattern := range writes {
		method, path, ok := strings.Cut(pattern, " ")
		assert.Must(t, ok, "pattern %q names no method", pattern)
		// A body that would be perfectly acceptable from an editor, so
		// the refusal cannot be blamed on the request itself. Every
		// wildcard but {game} is filled with a value that resolves to
		// nothing: the check has to happen before any of it is read.
		path = strings.ReplaceAll(path, "{game}", f.gameSlug)
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
			assert.Should(t, strings.Contains(rec.Body.String(), "viewer"), "%s said %q, want it to name the role that cannot write", pattern, rec.Body.String())
		})
	}
}

// wildcards matches a ServeMux path wildcard, for the table above.
var wildcards = regexp.MustCompile(`\{[^}]+\}`)

// TestAStatedGameMustAgreeWithTheURL mirrors ScopedArgs's own rule
// onto this surface. A body naming a different game than the URL is a
// caller that has lost track of which game it is editing, and answering
// it by silently ignoring the field — which is what "the URL wins" would
// mean in practice — is how content lands in the wrong game.
func TestAStatedGameMustAgreeWithTheURL(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	other, err := f.proj.Create(context.Background(), "le-mans", "Le Mans", f.ownerID)
	assert.Must(t, err == nil, "Create other game: %v", err)

	rec := f.as(t, http.MethodPost, "/types", map[string]any{
		"game": other.Slug,
		"key":  "quest", "label": "Quest", "label_plural": "Quests",
	})
	assertError(t, rec, http.StatusForbidden, "scope_violation", "")

	// And the same body naming this game is accepted, so the check is a
	// disagreement check and not a blanket refusal of the field.
	ok := f.as(t, http.MethodPost, "/types", map[string]any{
		"game": f.gameSlug,
		"key":  "quest", "label": "Quest", "label_plural": "Quests",
	})
	assert.Must(t, ok.Code == http.StatusOK, "agreeing game = %d: %s", ok.Code, ok.Body.String())

	// The confirmation folds case, like the address beside it: a caller
	// that confirmed "Azeroth" while working in "azeroth" confirmed the
	// right game, and refusing it would make the field harder to satisfy
	// than the URL it is checked against.
	folded := f.as(t, http.MethodPost, "/types", map[string]any{
		"game": strings.ToUpper(f.gameSlug),
		"key":  "zone", "label": "Zone", "label_plural": "Zones",
	})
	assert.Must(t, folded.Code == http.StatusOK, "a differently-cased game confirmation = %d: %s", folded.Code, folded.Body.String())
}

// The order the catalogue's column headers set, read off a URL.
func TestTheOrderReachesTheListingThroughTheURL(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	questType(t, f)
	if rec := f.as(t, http.MethodPost, "/entities", map[string]any{"items": []any{
		map[string]any{"type_key": "quest", "key": "alpha", "name": "Alpha"},
		map[string]any{"type_key": "quest", "key": "bravo", "name": "Bravo"},
		map[string]any{"type_key": "quest", "key": "charlie", "name": "Charlie"},
	}}); rec.Code != http.StatusOK {
		t.Fatalf("seed = %d: %s", rec.Code, rec.Body.String())
	}

	ascending := entityKeys(t, f, "?type_key=quest&order=name")
	descending := entityKeys(t, f, "?type_key=quest&order=-name")
	assert.Must(t, len(ascending) == 3 && ascending[0] == "alpha", "order=name = %v", ascending)
	assert.Must(t, len(descending) == 3 && descending[0] == "charlie", "order=-name = %v", descending)
	// Not "the two are different": two wrong orders are different too.
	// The reverse of one is the other, row for row.
	for i, key := range ascending {
		assert.Must(t, descending[len(descending)-1-i] == key, "order=-name %v is not the reverse of order=name %v", descending, ascending)
	}
	if got := entityKeys(t, f, "?type_key=quest&order=-updated"); len(got) != 3 {
		t.Fatalf("order=-updated = %v, want three rows", got)
	}

	// An unrecognised order is the caller's own argument at its own
	// path, not a 500 and not a listing quietly ordered by name.
	assertError(t, f.as(t, http.MethodGet, "/entities?type_key=quest&order=level", nil),
		http.StatusBadRequest, "invalid_input", "order")
}

// TestRESTListingBoundsComeThroughUnchanged pins that the bounds the
// domain enforces reach this surface as the caller's own problem, at
// the caller's own path, rather than as a 500.
func TestRESTListingBoundsComeThroughUnchanged(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	questType(t, f)

	assertError(t, f.as(t, http.MethodGet, "/entities?cursor=not-a-cursor", nil),
		http.StatusBadRequest, "invalid_input", "cursor")
	assertError(t, f.as(t, http.MethodGet, "/entities?limit=lots", nil),
		http.StatusBadRequest, "invalid_input", "limit")
	assertError(t, f.as(t, http.MethodGet, "/search?query="+strings.Repeat("a", metamodel.MaxSearchQuery+1), nil),
		http.StatusBadRequest, "invalid_input", "query")
	// The endpoint filter takes a ref now, and a ref naming no entity is
	// not_found rather than a malformed uuid: the parameter is a key, and
	// a key that names nothing is a real question with a real answer.
	assertError(t, f.as(t, http.MethodGet, "/relations?source_type_key=quest&source_key=nope", nil),
		http.StatusNotFound, "not_found", "")
	assertError(t, f.as(t, http.MethodDelete, "/entities/by-key/quest/nope", nil),
		http.StatusNotFound, "not_found", "")
}

// TestRESTRelationsListNamesItsEndpointsByRef chases Task 7's finding 18
// into this surface too: the graph view faces the same question the MCP
// tool did, and both now answer it from the same code.
func TestRESTRelationsListNamesItsEndpointsByRef(t *testing.T) {
	t.Parallel()
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
		"source_type_keys": []string{"quest"}, "target_type_keys": []string{"zone"},
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
	assert.Must(t, rec.Code == http.StatusOK, "list = %d: %s", rec.Code, rec.Body.String())
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
	assert.Must(t, len(page.Items) == 1, "items = %+v, want the one edge", page.Items)
	edge := page.Items[0]
	assert.Must(t, edge.Source != nil && edge.Target != nil, "edge = %+v, want both endpoints resolved", edge)
	assert.Must(t, edge.Source.Key == "hogger" && edge.Source.TypeKey == "quest" && edge.Source.Name == "Wanted: Hogger", "source = %+v, want the quest the edge was written with", edge.Source)
	assert.Must(t, edge.Target.Key == "elwynn", "target = %+v, want the zone", edge.Target)
	if _, err := uuid.Parse(edge.SourceID); err != nil {
		t.Fatalf("source_id = %q, want the id a removal addresses", edge.SourceID)
	}
}

// TestRESTIsIsolatedByTheURLsGameAndNothingElse is the isolation guard
// for this surface: a member of one game reading another game's content
// route is refused, and the refusal is about membership rather than
// about the row.
func TestRESTIsIsolatedByTheURLsGameAndNothingElse(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	ctx := context.Background()
	questType(t, f)

	outsider, err := f.ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "outsider@example.test", DisplayName: "Outsider", Password: "password12345",
	})
	assert.Must(t, err == nil, "CreateUser: %v", err)
	theirs, err := f.proj.Create(ctx, "le-mans", "Le Mans", outsider.ID)
	assert.Must(t, err == nil, "Create: %v", err)
	cookie := loginAs(t, f.srv, "outsider@example.test")

	// Their own game answers, and holds none of this game's types.
	own := f.call(t, cookie, http.MethodGet, "/api/games/"+theirs.Slug+"/types", nil)
	assert.Must(t, own.Code == http.StatusOK, "own game = %d: %s", own.Code, own.Body.String())
	var payload struct {
		Items []json.RawMessage `json:"items"`
	}
	decodeBody(t, own, &payload)
	assert.Must(t, len(payload.Items) == 0, "items = %d, want another game's types to be invisible", len(payload.Items))

	// This game does not, and says so in the words a game that does not
	// exist gets: a slug is guessable, so the two are one answer
	// (resolveGameRef).
	assertError(t, f.call(t, cookie, http.MethodGet, f.path("/types/by-key/quest"), nil),
		http.StatusNotFound, "not_found", "")
}

// TestTheGameSummaryCountsContentWithoutListingIt is the home page's
// only server-side requirement, and the reason it is a separate
// endpoint rather than the SPA counting a listing itself: the answer
// must stay the same size whether a game holds four entities or four
// hundred. This seeds enough rows that a listing would be obvious in
// the payload, and asserts none of them is in it.
func TestTheGameSummaryCountsContentWithoutListingIt(t *testing.T) {
	t.Parallel()
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
	assert.Must(t, rec.Code == http.StatusOK, "summary = %d: %s", rec.Code, rec.Body.String())
	var summary gameSummary
	decodeBody(t, rec, &summary)

	counts := map[string]int64{}
	for _, typ := range summary.EntityTypes {
		counts[typ.Key] = typ.EntityCount
	}
	assert.Must(t, counts["quest"] == 100 && counts["zone"] == 20, "counts = %+v, want 100 quests and 20 zones", counts)
	assert.Must(t, summary.Totals.Entities == 120 && summary.Totals.Relations == 0 && summary.Totals.Invalid == 0, "totals = %+v, want 120 entities and nothing else", summary.Totals)
	// The payload is a catalogue, not a listing: no individual row of
	// the hundred and twenty appears in it.
	assert.Must(t, !strings.Contains(rec.Body.String(), "quest-42"), "the summary carries individual entities: %s", rec.Body.String())
}

// TestTheGameSummaryOfAnEmptyGameIsAnEmptyCatalogue pins what the home
// page shows a designer who has just created a game: a well-formed
// answer with nothing in it, never an error and never a 404. The page's
// own empty state is what it renders from this.
func TestTheGameSummaryOfAnEmptyGameIsAnEmptyCatalogue(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	rec := f.as(t, http.MethodGet, "/summary", nil)
	assert.Must(t, rec.Code == http.StatusOK, "summary = %d: %s", rec.Code, rec.Body.String())
	var summary gameSummary
	decodeBody(t, rec, &summary)
	assert.Must(t, summary.EntityTypes != nil && summary.RelationTypes != nil, "summary = %+v, want empty arrays a page can iterate, not nulls: %s",
		summary, rec.Body.String())
	assert.Must(t, len(summary.EntityTypes) == 0 && len(summary.RelationTypes) == 0, "summary = %+v, want nothing declared yet", summary)
	assert.Must(t, summary.Totals.Entities == 0 && summary.Totals.Relations == 0 && summary.Totals.Invalid == 0, "totals = %+v, want zeros", summary.Totals)
}

// TestTheGameSummaryCountsTheRowsASchemaEditInvalidated is the one number
// on the home page a designer has to act on: an entity whose values no
// longer fit its type is kept, marked, and counted here, per type and in
// the total.
func TestTheGameSummaryCountsTheRowsASchemaEditInvalidated(t *testing.T) {
	t.Parallel()
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
	assert.Must(t, len(summary.EntityTypes) == 1, "entity_types = %+v, want the one type", summary.EntityTypes)
	quest := summary.EntityTypes[0]
	assert.Must(t, quest.EntityCount == 2 && quest.InvalidCount == 1, "quest = %+v, want 2 entities of which 1 invalid", quest)
	assert.Must(t, summary.Totals.Invalid == 1, "totals = %+v, want one invalid row", summary.Totals)
}

// **The home page's columns, on the wire.** Twelve verbs listed their
// counts with nothing saying what any of them joined, and `hates 11` is
// right or wrong depending on whether it joins deities or zones. The
// endpoint keys are what the page draws there, and they cost no query:
// gameCounts already holds the id-to-key map they are rendered from.
func TestTheGameSummaryCarriesWhatTheHomeDraws(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)

	if rec := f.as(t, http.MethodPost, "/types", map[string]any{
		"key": "quest", "label": "Quest", "label_plural": "Quests"}); rec.Code != http.StatusOK {
		t.Fatalf("quest = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := f.as(t, http.MethodPost, "/types", map[string]any{
		"key": "zone", "label": "Zone", "label_plural": "Zones"}); rec.Code != http.StatusOK {
		t.Fatalf("zone = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := f.as(t, http.MethodPost, "/relation-types", map[string]any{
		"key": "takes_place_in", "label": "takes place in",
		"source_type_keys": []string{"quest"}, "target_type_keys": []string{"zone"},
		"field_schema": []any{map[string]any{"key": "note", "label": "Note", "type": "text"}},
	}); rec.Code != http.StatusOK {
		t.Fatalf("relation type = %d: %s", rec.Code, rec.Body.String())
	}
	// An undeclared endpoint rule means "anything", and it reads back as
	// an empty slice rather than as null — the same contract
	// endpointKeysOf documents for relation_types.get.
	if rec := f.as(t, http.MethodPost, "/relation-types", map[string]any{
		"key": "mentions", "label": "mentions"}); rec.Code != http.StatusOK {
		t.Fatalf("second relation type = %d: %s", rec.Code, rec.Body.String())
	}

	rec := f.as(t, http.MethodGet, "/summary", nil)
	assert.Must(t, rec.Code == http.StatusOK, "summary = %d: %s", rec.Code, rec.Body.String())
	var summary gameSummary
	decodeBody(t, rec, &summary)

	byKey := map[string]int{}
	for i, typ := range summary.RelationTypes {
		byKey[typ.Key] = i
	}
	joins := summary.RelationTypes[byKey["takes_place_in"]]
	assert.Must(t, strings.Join(joins.SourceTypeKeys, ",") == "quest", "source keys = %v", joins.SourceTypeKeys)
	assert.Must(t, strings.Join(joins.TargetTypeKeys, ",") == "zone", "target keys = %v", joins.TargetTypeKeys)

	any := summary.RelationTypes[byKey["mentions"]]
	assert.Must(t, any.SourceTypeKeys != nil && len(any.SourceTypeKeys) == 0,
		"an undeclared endpoint rule reads back as %#v, and a page must not have to tell null from \"anything\"", any.SourceTypeKeys)
	assert.Must(t, any.TargetTypeKeys != nil && len(any.TargetTypeKeys) == 0,
		"an undeclared endpoint rule reads back as %#v", any.TargetTypeKeys)
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
		ID             string   `json:"id"`
		Key            string   `json:"key"`
		Label          string   `json:"label"`
		RelationCount  int64    `json:"relation_count"`
		InvalidCount   int64    `json:"invalid_count"`
		SourceTypeKeys []string `json:"source_type_keys"`
		TargetTypeKeys []string `json:"target_type_keys"`
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
	t.Parallel()
	f := newRESTFixture(t)
	ctx := context.Background()
	questType(t, f)

	other, err := f.proj.Create(ctx, "le-mans", "Le Mans", f.ownerID)
	assert.Must(t, err == nil, "Create other game: %v", err)
	token, _, err := f.ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: f.game, UserID: f.ownerID, Label: "agent",
	})
	assert.Must(t, err == nil, "CreateAPIToken: %v", err)

	send := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		f.srv.ServeHTTP(rec, req)
		return rec
	}

	own := send(f.path("/types"))
	assert.Must(t, own.Code == http.StatusOK, "own game = %d: %s", own.Code, own.Body.String())
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
	assert.Must(t, rec.Code == http.StatusOK, "list %q = %d: %s", query, rec.Code, rec.Body.String())
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
	t.Parallel()
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

// TestAStatedGameIsJudgedTheWayTheMCPSurfaceJudgesIt closes the two
// divergences a review found between checkStatedProject and the rule
// ScopedArgs states, both of which this surface used to get wrong: a
// value naming no game at all is the caller's own argument rather than
// "a different game than the URL", so `scope_violation` was the wrong
// code; and an empty confirmation is not the same as no confirmation —
// the MCP surface refuses it, and accepting it here made the field's
// presence mean nothing.
func TestAStatedGameIsJudgedTheWayTheMCPSurfaceJudgesIt(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	declare := func(game any) *httptest.ResponseRecorder {
		return f.as(t, http.MethodPost, "/types", map[string]any{
			"game": game,
			"key":  "quest", "label": "Quest", "label_plural": "Quests",
		})
	}

	// Empty: present and naming nothing. The field is a confirmation,
	// and an empty confirmation confirms nothing.
	body := assertError(t, declare(""), http.StatusBadRequest, "bad_request", "")
	assert.Should(t, strings.Contains(body.Message, "slug"), "message = %q, want it to say what to pass", body.Message)

	// A name that is not a game at all is a scope violation and not a
	// bad_request: from this surface's point of view it is simply not
	// the game in the URL, and saying so does not require looking it up
	// (which would be the enumeration oracle resolveGameRef avoids).
	assertError(t, declare("no-such-game"), http.StatusForbidden, "scope_violation", "")

	// And the rule the field exists for still holds in both directions.
	other, err := f.proj.Create(context.Background(), "monza", "Monza", f.ownerID)
	assert.Must(t, err == nil, "Create other game: %v", err)
	assertError(t, declare(other.Slug), http.StatusForbidden, "scope_violation", "")
	if rec := declare(f.gameSlug); rec.Code != http.StatusOK {
		t.Fatalf("agreeing game = %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAWrongTypedFieldIsNamed pins what a caller is told when the body
// is valid JSON but one field is the wrong type. It used to be
// "malformed JSON body", which is false — the JSON parsed — and named
// neither the field nor what was wrong with it, on a surface where every
// other refusal carries the path it is about.
func TestAWrongTypedFieldIsNamed(t *testing.T) {
	t.Parallel()
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
		// encoding/json reports a number that does not fit as
		// Value "number <literal>", so the naive concatenation read
		// "must be a number, not number 999999999999" — false twice
		// over, since it is a number and the sentence says it is not.
		{"a version too large for the field", "/types",
			`{"key":"quest","label":"Quest","label_plural":"Quests","expected_version":999999999999}`,
			"expected_version"},
		{"a version that is not whole", "/types",
			`{"key":"quest","label":"Quest","label_plural":"Quests","expected_version":1.5}`,
			"expected_version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, f.path(tc.suffix), strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(f.cookie)
			rec := httptest.NewRecorder()
			f.srv.ServeHTTP(rec, req)

			got := assertError(t, rec, http.StatusBadRequest, "bad_request", tc.path)
			assert.Should(t, !strings.Contains(got.Message, "malformed"), "message = %q, but this body is well-formed JSON", got.Message)
			assert.Should(t, !strings.Contains(got.Message, "not number "), "message = %q leaks encoding/json's own wording and denies that a number is one", got.Message)
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
	t.Parallel()
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
	assert.Must(t, err == nil, "marshal: %v", err)
	assert.Must(t, len(body) > 16*1024, "the batch is %d bytes, too small to prove anything about a 16 KiB bound", len(body))

	req := httptest.NewRequest(http.MethodPost, f.path("/entities"), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	assert.Must(t, rec.Code == http.StatusOK, "seed of %d bytes = %d: %s", len(body), rec.Code, rec.Body.String())
	// A batch answers 200 with a report even when rows failed, so the
	// status alone would pass for a request that landed nothing.
	var report struct {
		Written []struct {
			Key string `json:"key"`
		} `json:"written"`
		Failed []any `json:"failed"`
	}
	decodeBody(t, rec, &report)
	assert.Must(t, len(report.Written) == len(items) && len(report.Failed) == 0, "wrote %d of %d rows, %d failed: %s",
		len(report.Written), len(items), len(report.Failed), rec.Body.String())
}

// TestAnIncompleteTraversalIsRefusedAndNeverAnsweredWithTheWholeGame
// pins hasRelatedTo's "any part, not all four". Read as "all four", a
// query naming one part of the traversal and forgetting the rest falls
// through to an ordinary listing — which answers a caller who asked for
// one entity's neighbours with every entity in the game, the exact
// failure that function's comment argues against.
func TestAnIncompleteTraversalIsRefusedAndNeverAnsweredWithTheWholeGame(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	invalidAndValidQuests(t, f)

	const (
		relKey  = "related_to.relation_type_key"
		typeKey = "related_to.entity_type_key"
		entKey  = "related_to.entity_key"
		dirKey  = "related_to.direction"
	)
	for _, tc := range []struct {
		query   string
		missing []string
	}{
		{"?" + entKey + "=hogger", []string{relKey, typeKey, dirKey}},
		{"?" + typeKey + "=quest", []string{relKey, entKey, dirKey}},
		{"?" + relKey + "=takes_place_in", []string{typeKey, entKey, dirKey}},
		{"?" + dirKey + "=outgoing", []string{relKey, typeKey, entKey}},
		// A part written with no value is a part that is present and
		// missing, not a part that was never asked for: the traversal is
		// still refused, and all four parts are named.
		{"?" + dirKey + "=", []string{relKey, typeKey, entKey, dirKey}},
	} {
		rec := f.as(t, http.MethodGet, "/entities"+tc.query, nil)
		assert.Must(t, rec.Code != http.StatusOK, "%s = 200 with %s, want a refusal rather than the whole game",
			tc.query, rec.Body.String())
		got := assertError(t, rec, http.StatusBadRequest, "invalid_input", "")
		paths := map[string]bool{}
		for _, field := range got.Details.Fields {
			paths[field.Path] = true
		}
		assert.Should(t, len(paths) == len(tc.missing), "%s named %d paths, want exactly the %d missing ones: %s",
			tc.query, len(paths), len(tc.missing), rec.Body.String())
		for _, want := range tc.missing {
			assert.Should(t, paths[want], "%s did not name the missing %s: %s", tc.query, want, rec.Body.String())
		}
	}

	// And a traversal with all four parts spelled still reaches the
	// domain, so the guard refuses incompleteness and nothing else.
	if rec := f.as(t, http.MethodGet, "/entities?"+relKey+"=takes_place_in&"+
		typeKey+"=quest&"+entKey+"=hogger&"+dirKey+"=outgoing", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("a complete traversal over an undeclared relation type = %d, want the domain's own 404: %s",
			rec.Code, rec.Body.String())
	}
}

// TestARepeatedOrEmptyQueryParameterIsRefused pins the rule this surface
// applies to its query string, which is the one queryTriState already
// applied to the value it reads: a parameter the caller wrote is a
// parameter the caller meant, so it must carry exactly one value and
// that value must say something.
func TestARepeatedOrEmptyQueryParameterIsRefused(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	invalidAndValidQuests(t, f)

	for _, tc := range []struct{ name, query, path string }{
		{"a filter given twice", "?invalid=true&invalid=false", "invalid"},
		{"a filter given twice, once unreadably", "?invalid=true&invalid=garbage", "invalid"},
		{"a limit given twice", "?limit=1&limit=2", "limit"},
		{"a filter with no value", "?invalid=", "invalid"},
		{"a limit with no value", "?limit=", "limit"},
		{"a cursor with no value", "?cursor=", "cursor"},
		{"a flag with no value", "?verbose=", "verbose"},
		{"a traversal part given twice", "?related_to.direction=outgoing&related_to.direction=incoming",
			"related_to.direction"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.as(t, http.MethodGet, "/entities"+tc.query, nil)
			assert.Must(t, rec.Code != http.StatusOK, "%s = 200: %s", tc.query, rec.Body.String())
			assertError(t, rec, http.StatusBadRequest, "invalid_input", tc.path)
		})
	}
}

// TestALimitTooLargeForTheFieldSaysSo pins the difference between a
// limit that is not a number and one that is. `?limit=999999999999` used
// to be told "limit is not a number", which is false — it is a number,
// it simply does not fit the int32 the field is — and unactionable,
// because a caller told their number is not one has nowhere to go.
func TestALimitTooLargeForTheFieldSaysSo(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	questType(t, f)

	got := assertError(t, f.as(t, http.MethodGet, "/entities?limit=999999999999", nil),
		http.StatusBadRequest, "invalid_input", "limit")
	assert.Should(t, !strings.Contains(got.Message, "not a number"), "message = %q, but 999999999999 is a number", got.Message)
	assert.Should(t, strings.Contains(got.Message, "999999999999"), "message = %q, want it to quote the value it refused", got.Message)
	// A genuine non-number still says what it is.
	got = assertError(t, f.as(t, http.MethodGet, "/entities?limit=lots", nil),
		http.StatusBadRequest, "invalid_input", "limit")
	assert.Should(t, strings.Contains(got.Message, "not a number"), "message = %q, want it to say \"lots\" is not a number", got.Message)
}

// TestABodyThatIsNotAnObjectSaysSo pins the other half of the wrong-type
// refusal. A body that is well-formed JSON but the wrong shape entirely
// — a list, a bare string — carries no field path for encoding/json to
// report, so the handler used to fall through to "malformed JSON body",
// which is false for the same reason it was false for a wrong-typed
// field: the JSON parsed. Naming no path is right; calling it malformed
// is not.
func TestABodyThatIsNotAnObjectSaysSo(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)

	// `null` is deliberately absent: unmarshalling it into a struct is a
	// no-op rather than an error, so it is not a wrong-shaped body at
	// all — it reaches the domain as an empty one and is refused there,
	// field by field, which is the right answer for it.
	for _, body := range []string{`[]`, `"just a string"`, `42`} {
		t.Run(body, func(t *testing.T) {
			got := assertError(t, f.raw(t, http.MethodPost, "/types", body),
				http.StatusBadRequest, "bad_request", "")
			assert.Should(t, !strings.Contains(got.Message, "malformed"), "message = %q, but %s is well-formed JSON", got.Message, body)
			assert.Should(t, strings.Contains(got.Message, "JSON object"), "message = %q, want it to say the body must be a JSON object", got.Message)
		})
	}
}

// TestDataAfterTheJSONBodyIsRefused pins the last silently ignored input
// on a surface whose stated rule is that nothing is silently ignored.
// json.Decoder.Decode reads one value and stops, so a body carrying two
// — `{"key":"a"}{"key":"b"}`, which is what a client concatenating
// payloads sends — used to answer 200 having written only the first, and
// the caller had no way to learn the second never happened.
func TestDataAfterTheJSONBodyIsRefused(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)

	body := `{"key":"quest","label":"Quest","label_plural":"Quests"}` +
		`{"key":"zone","label":"Zone","label_plural":"Zones"}`
	assertError(t, f.raw(t, http.MethodPost, "/types", body),
		http.StatusBadRequest, "bad_request", "")

	// And nothing was written: the first value must not land while the
	// second is refused.
	var list struct {
		Types []any `json:"types"`
	}
	decodeBody(t, f.as(t, http.MethodGet, "/types", nil), &list)
	assert.Must(t, len(list.Types) == 0, "a refused two-value body still wrote %d types", len(list.Types))
}

// TestTheSummaryNamesTheCallersRole pins the one field the game home
// page needs and could not get anywhere else. The page's empty state
// used to tell every reader that types are declared over MCP or the
// content routes; a viewer reading that sentence can do neither, and
// telling someone to take an action the server will refuse is worse than
// telling them nothing. The role is what lets the page say what its
// reader can actually do, and it is free here — requireProject has
// already resolved it for the request.
func TestTheSummaryNamesTheCallersRole(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	ctx := context.Background()

	var summary struct {
		Role string `json:"role"`
	}
	decodeBody(t, f.as(t, http.MethodGet, "/summary", nil), &summary)
	assert.Should(t, summary.Role == "owner", "the owner's summary names role %q", summary.Role)

	viewer, err := f.ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "onlooker@example.test", DisplayName: "Onlooker", Password: "password12345",
	})
	assert.Must(t, err == nil, "CreateUser: %v", err)
	if _, err := f.proj.SetRole(ctx, viewer.ID, f.game, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	rec := f.call(t, loginAs(t, f.srv, "onlooker@example.test"), http.MethodGet, f.path("/summary"), nil)
	assert.Must(t, rec.Code == http.StatusOK, "viewer summary = %d: %s", rec.Code, rec.Body.String())
	decodeBody(t, rec, &summary)
	assert.Should(t, summary.Role == "viewer", "the viewer's summary names role %q", summary.Role)
}

// TestRemovingATypeStillInUseIsAConflict pins the status the domain's
// own refusal gets. in_use is not the caller being wrong — the request
// was well formed and the type is real — it is the world holding on to
// the row, which is what 409 says and 400 does not.
func TestRemovingATypeStillInUseIsAConflict(t *testing.T) {
	t.Parallel()
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

	rec := f.as(t, http.MethodDelete, "/types/by-key/quest", nil)
	assertError(t, rec, http.StatusConflict, "in_use", "")

	// And with cascade the same removal succeeds, so the conflict is
	// about the entities and not about the route.
	if rec := f.as(t, http.MethodDelete, "/types/by-key/quest?cascade=true", nil); rec.Code != http.StatusOK {
		t.Fatalf("cascade remove = %d: %s", rec.Code, rec.Body.String())
	}
}

// TestRESTReadsAnEdgesOwnFields chases Metamodel 12 into the human half
// of the surface. The two surfaces share one core and one wire
// vocabulary, so a fix landing only on the MCP side would leave the
// graph view — the thing the views sub-project renders an edge's
// declared values from — still unable to see them.
func TestRESTReadsAnEdgesOwnFields(t *testing.T) {
	t.Parallel()
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
		"key": "connects_to", "label": "connects to",
		"source_type_keys": []string{"zone"}, "target_type_keys": []string{"zone"},
		"field_schema": []any{map[string]any{"key": "requires_ability", "type": "text"}},
	}); rec.Code != http.StatusOK {
		t.Fatalf("relation type = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := f.as(t, http.MethodPost, "/entities", map[string]any{"items": []any{
		map[string]any{"type_key": "zone", "key": "elwynn", "name": "Elwynn Forest"},
		map[string]any{"type_key": "zone", "key": "westfall", "name": "Westfall"},
	}}); rec.Code != http.StatusOK {
		t.Fatalf("entities = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := f.as(t, http.MethodPost, "/relations", map[string]any{"items": []any{
		map[string]any{"type_key": "connects_to",
			"source": map[string]any{"type_key": "zone", "key": "elwynn"},
			"target": map[string]any{"type_key": "zone", "key": "westfall"},
			"fields": map[string]any{"requires_ability": "mothwing_cloak"}},
	}}); rec.Code != http.StatusOK {
		t.Fatalf("relation = %d: %s", rec.Code, rec.Body.String())
	}

	type edgeBody struct {
		TypeKey string                `json:"type_key"`
		Fields  map[string]any        `json:"fields"`
		Source  *struct{ Key string } `json:"source"`
	}

	one := f.as(t, http.MethodGet,
		"/relations/one?type_key=connects_to&source_type_key=zone&source_key=elwynn"+
			"&target_type_key=zone&target_key=westfall", nil)
	assert.Must(t, one.Code == http.StatusOK, "read one edge = %d: %s", one.Code, one.Body.String())
	var got edgeBody
	decodeBody(t, one, &got)
	assert.Must(t, got.Fields["requires_ability"] == "mothwing_cloak", "edge = %+v, want the ability it was written with", got)
	assert.Must(t, got.TypeKey == "connects_to" && got.Source != nil && got.Source.Key == "elwynn", "edge = %+v, want the identity the listing gives too", got)

	// A fresh decode target per read: json.Unmarshal reuses the elements
	// of a slice it is given, so one shared page struct would let the
	// verbose answer be satisfied by what the plain one left behind.
	listing := func(path string) []edgeBody {
		t.Helper()
		rec := f.as(t, http.MethodGet, path, nil)
		assert.Must(t, rec.Code == http.StatusOK, "GET %s = %d: %s", path, rec.Code, rec.Body.String())
		var page struct {
			Items []edgeBody `json:"items"`
		}
		decodeBody(t, rec, &page)
		assert.Must(t, len(page.Items) == 1, "GET %s items = %+v, want the one edge", path, page.Items)
		return page.Items
	}
	if fields := listing("/relations")[0].Fields; fields != nil {
		t.Fatalf("a listing nobody asked to be verbose carried fields: %+v", fields)
	}
	if fields := listing("/relations?verbose=true")[0].Fields; fields["requires_ability"] != "mothwing_cloak" {
		t.Fatalf("verbose listing fields = %+v, want the edge's own values", fields)
	}

	// The refusals are this surface's own spelling of the domain's: a
	// bad address is 404 with the code and the message the tool gives,
	// and a key that could never have been stored is 400.
	missing := assertError(t, f.as(t, http.MethodGet,
		"/relations/one?type_key=connects_to&source_type_key=zone&source_key=westfall"+
			"&target_type_key=zone&target_key=elwynn", nil),
		http.StatusNotFound, "not_found", "")
	assert.Must(t, strings.Contains(missing.Message, `no "connects_to" edge`), "message = %q, want it to name the edge that is missing", missing.Message)
	assertError(t, f.as(t, http.MethodGet,
		"/relations/one?type_key=connects+to&source_type_key=zone&source_key=elwynn"+
			"&target_type_key=zone&target_key=westfall", nil),
		http.StatusBadRequest, "invalid_input", "type_key")
}

// seedOneRESTEdge declares a quest type, a relation type carrying `note` and
// `difficulty`, two quests and one edge between them holding both
// values. It returns nothing: every test below addresses the edge by the
// keys it was written with.
func seedOneRESTEdge(t *testing.T, f restFixture) {
	t.Helper()
	if rec := f.as(t, http.MethodPost, "/types", map[string]any{
		"key": "quest", "label": "Quest", "label_plural": "Quests",
	}); rec.Code != http.StatusOK {
		t.Fatalf("quest type = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := f.as(t, http.MethodPost, "/relation-types", map[string]any{
		"key": "requires", "label": "requires",
		"field_schema": []any{
			map[string]any{"key": "note", "type": "text"},
			map[string]any{"key": "difficulty", "type": "number"},
		},
	}); rec.Code != http.StatusOK {
		t.Fatalf("relation type = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := f.as(t, http.MethodPost, "/entities", map[string]any{"items": []any{
		map[string]any{"type_key": "quest", "key": "hogger", "name": "Wanted: Hogger"},
		map[string]any{"type_key": "quest", "key": "kobolds", "name": "Kobolds"},
	}}); rec.Code != http.StatusOK {
		t.Fatalf("entities = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := f.as(t, http.MethodPost, "/relations", map[string]any{"items": []any{
		map[string]any{"type_key": "requires",
			"source": map[string]any{"type_key": "quest", "key": "kobolds"},
			"target": map[string]any{"type_key": "quest", "key": "hogger"},
			"fields": map[string]any{"note": "chain", "difficulty": 3}},
	}}); rec.Code != http.StatusOK {
		t.Fatalf("relation = %d: %s", rec.Code, rec.Body.String())
	}
}

// narrowRequiresOverREST re-declares the "requires" relation type without
// `difficulty`, which is what leaves the seeded edge no longer fitting.
func narrowRequiresOverREST(t *testing.T, f restFixture) {
	t.Helper()
	if rec := f.as(t, http.MethodPost, "/relation-types", map[string]any{
		"key": "requires", "label": "requires", "expected_version": 1,
		"field_schema": []any{map[string]any{"key": "note", "type": "text"}},
	}); rec.Code != http.StatusOK {
		t.Fatalf("narrow = %d: %s", rec.Code, rec.Body.String())
	}
}

// relationsPage is GET /relations' answer, as a client reads it.
type relationsPage struct {
	Items []struct {
		ID      string `json:"id"`
		TypeKey string `json:"type_key"`
		Version int32  `json:"version"`
		Invalid bool   `json:"invalid"`
	} `json:"items"`
}

// TestRESTRelationsListFiltersByTheInvalidFlag is the REST half of the
// read surface 0009's flag needs. A designer looking at a game summary
// that says "3 no longer fit" has to be able to click through to them,
// and the entity listing has taken `?invalid=` since it shipped.
func TestRESTRelationsListFiltersByTheInvalidFlag(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	seedOneRESTEdge(t, f)

	rec := f.as(t, http.MethodGet, "/relations", nil)
	var before relationsPage
	decodeBody(t, rec, &before)
	assert.Must(t, len(before.Items) == 1 && !before.Items[0].Invalid, "items = %+v, want one edge that still fits", before.Items)
	if before.Items[0].Version != 1 {
		t.Fatalf("version = %d, want 1 — an agent cannot send an expected_version "+
			"the listing never told it", before.Items[0].Version)
	}

	narrowRequiresOverREST(t, f)

	for _, tc := range []struct {
		query string
		want  int
	}{
		{"", 1},
		{"?invalid=true", 1},
		{"?invalid=false", 0},
	} {
		t.Run("relations"+tc.query, func(t *testing.T) {
			rec := f.as(t, http.MethodGet, "/relations"+tc.query, nil)
			assert.Must(t, rec.Code == http.StatusOK, "list = %d: %s", rec.Code, rec.Body.String())
			var page relationsPage
			decodeBody(t, rec, &page)
			assert.Must(t, len(page.Items) == tc.want, "items = %d, want %d: %s", len(page.Items), tc.want, rec.Body.String())
			for _, item := range page.Items {
				assert.Must(t, tc.query != "?invalid=true" || item.Invalid, "the invalid listing returned an edge with invalid=false: %+v", item)
			}
		})
	}

	// A value that is neither is the caller's own mistake, not "no
	// opinion" — the same refusal the entity listing gives.
	assertError(t, f.as(t, http.MethodGet, "/relations?invalid=maybe", nil),
		http.StatusBadRequest, "invalid_input", "")
}

// TestTheGameSummaryCountsTheEdgesASchemaEditInvalidated is the edge twin
// of TestTheGameSummaryCountsTheRowsASchemaEditInvalidated.
func TestTheGameSummaryCountsTheEdgesASchemaEditInvalidated(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	seedOneRESTEdge(t, f)

	rec := f.as(t, http.MethodGet, "/summary", nil)
	var before gameSummary
	decodeBody(t, rec, &before)
	assert.Must(t, len(before.RelationTypes) == 1, "relation_types = %+v, want the one type", before.RelationTypes)
	if before.RelationTypes[0].RelationCount != 1 || before.RelationTypes[0].InvalidCount != 0 {
		t.Fatalf("requires = %+v, want 1 edge of which 0 invalid", before.RelationTypes[0])
	}
	assert.Must(t, before.Totals.Invalid == 0, "totals = %+v, want nothing to fix yet", before.Totals)

	narrowRequiresOverREST(t, f)

	rec = f.as(t, http.MethodGet, "/summary", nil)
	var after gameSummary
	decodeBody(t, rec, &after)
	if after.RelationTypes[0].RelationCount != 1 || after.RelationTypes[0].InvalidCount != 1 {
		t.Fatalf("requires = %+v, want 1 edge of which 1 invalid", after.RelationTypes[0])
	}
	assert.Must(t, after.Totals.Invalid == 1, "totals = %+v, want the broken edge counted", after.Totals)
	// The entity half is untouched: this game's two quests still fit.
	if after.EntityTypes[0].InvalidCount != 0 {
		t.Fatalf("quest = %+v, want no invalid entities — the two counts are being "+
			"read off each other", after.EntityTypes[0])
	}
}

// TestRESTRelationsUpsertTakesAnExpectedVersion pins the write half over
// REST: an edge is now a compare-and-set, and the number to send comes
// back in the same answer that wrote it.
func TestRESTRelationsUpsertTakesAnExpectedVersion(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	seedOneRESTEdge(t, f)

	item := func(extra map[string]any) map[string]any {
		out := map[string]any{"type_key": "requires",
			"source": map[string]any{"type_key": "quest", "key": "kobolds"},
			"target": map[string]any{"type_key": "quest", "key": "hogger"},
			"fields": map[string]any{"note": "rewritten", "difficulty": 4}}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	// No expected_version: refused per item, and the batch reports it
	// rather than overwriting.
	rec := f.as(t, http.MethodPost, "/relations", map[string]any{"items": []any{item(nil)}})
	assert.Must(t, rec.Code == http.StatusOK, "blind rewrite = %d: %s", rec.Code, rec.Body.String())
	var report struct {
		Count   int `json:"count"`
		Written []struct {
			Version int32 `json:"version"`
		} `json:"written"`
		Failed []struct {
			Code string `json:"code"`
		} `json:"failed"`
	}
	decodeBody(t, rec, &report)
	assert.Must(t, report.Count == 0 && len(report.Failed) == 1 && report.Failed[0].Code == "version_conflict", "report = %+v, want one version_conflict", report)

	rec = f.as(t, http.MethodPost, "/relations",
		map[string]any{"items": []any{item(map[string]any{"expected_version": 1})}})
	assert.Must(t, rec.Code == http.StatusOK, "versioned rewrite = %d: %s", rec.Code, rec.Body.String())
	decodeBody(t, rec, &report)
	assert.Must(t, report.Count == 1, "report = %+v, want the write to land", report)
	if report.Written[0].Version != 2 {
		t.Fatalf("written version = %d, want 2 — the number the next edit has to send",
			report.Written[0].Version)
	}
}

// TestRESTAnEntitysProseArrivesRenderedAndMCPsDoesNot is the split
// internal/markdown's header states, asserted at both ends of it: the
// browser's route carries the longtext rendered, and the tool answers
// with the markdown an agent would edit.
func TestRESTAnEntitysProseArrivesRenderedAndMCPsDoesNot(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	rec := f.as(t, http.MethodPost, "/types", map[string]any{
		"key": "quest", "label": "Quest", "label_plural": "Quests",
		"field_schema": []any{
			map[string]any{"key": "summary", "type": "longtext"},
			map[string]any{"key": "hint", "type": "text"},
		},
	})
	assert.Must(t, rec.Code == http.StatusOK, "declare quest type = %d: %s", rec.Code, rec.Body.String())

	rec = f.as(t, http.MethodPost, "/entities", map[string]any{
		"items": []any{map[string]any{
			"type_key": "quest", "key": "hogger", "name": "Wanted: Hogger",
			"fields": map[string]any{
				"summary": "First line\nsecond line\n\nA new paragraph with **weight**.",
				"hint":    "Not prose, and **not rendered**.",
			},
		}},
	})
	assert.Must(t, rec.Code == http.StatusOK, "create = %d: %s", rec.Code, rec.Body.String())

	read := f.as(t, http.MethodGet, "/entities/by-key/quest/hogger", nil)
	assert.Must(t, read.Code == http.StatusOK, "read = %d: %s", read.Code, read.Body.String())
	var browser struct {
		Fields     map[string]any    `json:"fields"`
		FieldsHTML map[string]string `json:"fields_html"`
	}
	assert.NoErr(t, json.Unmarshal(read.Body.Bytes(), &browser), "decode the browser's answer")
	assert.Must(t, strings.Contains(browser.FieldsHTML["summary"], "<strong>weight</strong>"), "summary came back unrendered: %q", browser.FieldsHTML["summary"])
	assert.Must(t, strings.Contains(browser.FieldsHTML["summary"], "<br"), "the single newline was collapsed: %q", browser.FieldsHTML["summary"])
	_, renderedHint := browser.FieldsHTML["hint"]
	assert.Must(t, !renderedHint, "a text field was rendered: only a longtext is prose")
	assert.Must(t, browser.Fields["summary"] == "First line\nsecond line\n\nA new paragraph with **weight**.", "the raw value did not survive beside the rendering: %v", browser.Fields["summary"])

	// And the tool, which is the half that must not change.
	out, err := web.MCPEntitiesGet(context.Background(), f.deps(), f.caller(), f.game, web.EntitiesGetInput{
		TypeKey: "quest", Key: "hogger",
	})
	assert.NoErr(t, err, "MCPEntitiesGet")
	encoded, err := json.Marshal(out)
	assert.NoErr(t, err, "marshal the tool's answer")
	assert.Must(t, !strings.Contains(string(encoded), "fields_html"), "the agent's answer carries a rendering: %s", encoded)
	assert.Must(t, !strings.Contains(string(encoded), "<strong>"), "the agent's answer carries markup: %s", encoded)
}
