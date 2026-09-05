package web_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/testutil"
	"github.com/neverbot/maestro/internal/views"
	"github.com/neverbot/maestro/internal/web"
)

// The saved-view routes, over HTTP.
//
// Everything about the query language and about what a view stores is
// decided in internal/views and tested there. What is tested here is
// what only the transport can be wrong about: that both surfaces answer
// with the same codes and the same envelope, that a viewer may run a
// view and may not save one, and that the view.* events reach a
// subscriber over the endpoint a browser actually subscribes to.

type viewsRESTFixture struct {
	srv     *web.Server
	ids     *identity.Service
	proj    *projects.Service
	views   *views.Service
	meta    *metamodel.Service
	game    uuid.UUID
	other   uuid.UUID
	ownerID uuid.UUID
	cookie  *http.Cookie
	hub     *realtime.Hub
}

// newViewsRESTFixture wires a server the way cmd/maestro does: one hub,
// handed to every domain service and to Options.Hub alike, so a
// published event actually reaches /events. A fixture whose services
// take a nil hub cannot see any of that and would leave the wiring
// proved only by reading main.go.
func newViewsRESTFixture(t *testing.T) viewsRESTFixture {
	t.Helper()
	pool := testutil.NewPool(t)
	cfg := config.Config{
		SessionTTL: testConfig().SessionTTL,
		InviteTTL:  testConfig().InviteTTL,
		Argon2:     testConfig().Argon2,
	}
	ids := identity.New(pool, cfg)
	projSvc := projects.New(pool)
	hub := realtime.NewHub()
	mm := metamodel.New(pool, hub)
	vs := views.New(pool, hub)
	srv := web.NewServer(web.Options{
		Version: "test", Config: cfg, Identity: ids, Projects: projSvc,
		Metamodel: mm, Views: vs, Hub: hub,
		SSEMaxLifetime: time.Minute, SSEHeartbeatInterval: time.Minute,
	})
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
	other, err := projSvc.Create(ctx, "le-mans", "Le Mans", owner.ID)
	if err != nil {
		t.Fatalf("Create the second game: %v", err)
	}
	f := viewsRESTFixture{
		srv: srv, ids: ids, proj: projSvc, views: vs, meta: mm,
		game: game.ID, other: other.ID, ownerID: owner.ID, hub: hub,
		cookie: loginAs(t, srv, "owner@studio.com"),
	}
	f.seed(t)
	return f
}

func (f viewsRESTFixture) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.meta.UpsertEntityType(ctx, f.game, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: []metamodel.Field{{Key: "min_level", Type: metamodel.FieldNumber}},
	}); err != nil {
		t.Fatalf("seed the quest type: %v", err)
	}
	for _, in := range []metamodel.EntityInput{
		{TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
			Fields: map[string]any{"min_level": 22}},
		{TypeKey: "quest", Key: "defias", Name: "The Defias Brotherhood",
			Fields: map[string]any{"min_level": 28}},
	} {
		if _, err := f.meta.UpsertEntity(ctx, f.game, in); err != nil {
			t.Fatalf("seed %s: %v", in.Key, err)
		}
	}
}

func (f viewsRESTFixture) path(suffix string) string {
	return "/api/games/" + f.game.String() + suffix
}

// call makes one request as the given cookie, with a JSON body when one
// is given.
func (f viewsRESTFixture) call(t *testing.T, cookie *http.Cookie, method, path string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
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

// uploadAsset puts one PNG in the game through the upload route and
// answers its id. It goes through the route rather than the service
// because everything in this file that needs an asset needs one a
// browser could have produced, and the id is what views.set_background
// takes.
func (f viewsRESTFixture) uploadAsset(t *testing.T, filename string) string {
	t.Helper()
	body := bytes.NewReader(testPNG(t, 24, 16))
	req := httptest.NewRequest(http.MethodPost,
		"/api/games/"+f.game.String()+"/view-assets?filename="+filename, body)
	req.Header.Set("Content-Type", "image/png")
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("upload %s = %d: %s", filename, rec.Code, rec.Body.String())
	}
	var asset web.ViewAssetOutput
	decodeInto(t, rec, &asset)
	if asset.ID == "" {
		t.Fatalf("upload %s answered no id: %s", filename, rec.Body.String())
	}
	return asset.ID
}

// TestTheViewRoutesAreTheSameContractAsTheTools walks the whole surface
// over HTTP: save, read back, list, run, validate, arrange, background,
// remove. It is not a re-test of the domain — it is the claim that the
// REST mirror reaches the same cores, in the same order, and hands back
// the same shapes.
func TestTheViewRoutesAreTheSameContractAsTheTools(t *testing.T) {
	f := newViewsRESTFixture(t)

	rec := f.call(t, f.cookie, http.MethodPost, f.path("/views"), map[string]any{
		"key": "route", "name": "The route", "renderer": "graph",
		"query": json.RawMessage(questsQuery), "expected_version": 0,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /views = %d: %s", rec.Code, rec.Body.String())
	}
	var saved web.ViewOutput
	decodeInto(t, rec, &saved)
	if saved.Version != 1 || saved.Key != "route" {
		t.Fatalf("saved = %+v, want route at version 1", saved)
	}

	rec = f.call(t, f.cookie, http.MethodGet, f.path("/views/by-key/route"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET one = %d: %s", rec.Code, rec.Body.String())
	}
	var got web.ViewOutput
	decodeInto(t, rec, &got)
	if got.ID != saved.ID {
		t.Fatalf("read back %s, want %s", got.ID, saved.ID)
	}

	rec = f.call(t, f.cookie, http.MethodGet, f.path("/views"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET listing = %d: %s", rec.Code, rec.Body.String())
	}
	var listing web.ViewsListOutput
	decodeInto(t, rec, &listing)
	if len(listing.Items) != 1 || listing.Items[0].Key != "route" || listing.Items[0].Stale {
		t.Fatalf("listing = %+v, want one row, not stale", listing.Items)
	}

	rec = f.call(t, f.cookie, http.MethodPost, f.path("/views/run"), map[string]any{"key": "route"})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST run = %d: %s", rec.Code, rec.Body.String())
	}
	var run map[string]any
	decodeInto(t, rec, &run)
	nodes, _ := run["nodes"].([]any)
	if len(nodes) != 2 {
		t.Fatalf("run drew %d nodes, want the two quests: %s", len(nodes), rec.Body.String())
	}
	// The envelope's empty lists are lists and never null, which is the
	// promise the domain makes and this is where a client reads it.
	if _, ok := run["positions"].([]any); !ok {
		t.Fatalf("positions = %v, want an array on a saved run", run["positions"])
	}

	rec = f.call(t, f.cookie, http.MethodPost, f.path("/views/validate"), map[string]any{
		"query": json.RawMessage(questsQuery),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST validate = %d: %s", rec.Code, rec.Body.String())
	}
	var validated web.ViewsValidateOutput
	decodeInto(t, rec, &validated)
	if !validated.Valid || len(validated.Refs) != 1 || validated.Limits.MaxNodes == 0 {
		t.Fatalf("validate = %+v", validated)
	}

	rec = f.call(t, f.cookie, http.MethodPost, f.path("/views/by-key/route/positions"),
		map[string]any{"positions": []any{
			map[string]any{"entity_type": "quest", "entity_key": "hogger", "x": 3, "y": 4},
		}})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST positions = %d: %s", rec.Code, rec.Body.String())
	}
	rec = f.call(t, f.cookie, http.MethodPost, f.path("/views/by-key/route/positions/clear"), map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST positions/clear = %d: %s", rec.Code, rec.Body.String())
	}
	var cleared web.ViewsPositionsRemovedOutput
	decodeInto(t, rec, &cleared)
	if cleared.Removed != 1 {
		t.Fatalf("cleared %d, want the one that was written", cleared.Removed)
	}

	// The background, which the doc comment above has always promised
	// and the body did not drive. It is its own step and not a
	// re-test of the domain: this route extracts the view key from the
	// path itself — the tool takes it in the body — so the key
	// extraction is the one thing the REST mirror can be uniquely wrong
	// about here, and deleting it left the whole suite green.
	//
	// A background needs a renderer that draws one, so the placement
	// runs against a second view; the clear runs against it too, since
	// clearing is the arm that reaches a different statement.
	asset := f.uploadAsset(t, "azeroth.png")
	rec = f.call(t, f.cookie, http.MethodPost, f.path("/views"), map[string]any{
		"key": "atlas", "name": "The atlas", "renderer": "map",
		"query": json.RawMessage(questsQuery), "expected_version": 0,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST a map view = %d: %s", rec.Code, rec.Body.String())
	}
	rec = f.call(t, f.cookie, http.MethodPost, f.path("/views/by-key/atlas/background"),
		map[string]any{"asset_id": asset, "scale": 2.0, "offset": map[string]any{"x": 5, "y": -7}})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST background = %d: %s", rec.Code, rec.Body.String())
	}
	rec = f.call(t, f.cookie, http.MethodGet, f.path("/views/by-key/atlas"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET after background = %d: %s", rec.Code, rec.Body.String())
	}
	var placed web.ViewOutput
	decodeInto(t, rec, &placed)
	if placed.BackgroundAssetID == nil || *placed.BackgroundAssetID != asset {
		t.Fatalf("background asset = %v, want %s", placed.BackgroundAssetID, asset)
	}
	if placed.BackgroundScale != 2 || placed.BackgroundOffset.X != 5 ||
		placed.BackgroundOffset.Y != -7 {
		t.Fatalf("background placement = %+v, want scale 2 at (5,-7)", placed)
	}
	rec = f.call(t, f.cookie, http.MethodPost, f.path("/views/by-key/atlas/background"),
		map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST background clear = %d: %s", rec.Code, rec.Body.String())
	}
	rec = f.call(t, f.cookie, http.MethodGet, f.path("/views/by-key/atlas"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET after the clear = %d: %s", rec.Code, rec.Body.String())
	}
	var clearedBackground web.ViewOutput
	decodeInto(t, rec, &clearedBackground)
	if clearedBackground.BackgroundAssetID != nil {
		t.Fatalf("background survived the clear: %v", clearedBackground.BackgroundAssetID)
	}

	rec = f.call(t, f.cookie, http.MethodDelete, f.path("/views/by-key/route"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body.String())
	}
	// The answer's one member, which is the whole of what this route
	// says: a `removed` that was always false would leave a client
	// unable to tell a deletion from a refusal it did not read.
	var removed web.ViewsRemovedOutput
	decodeInto(t, rec, &removed)
	if !removed.Removed {
		t.Errorf("DELETE answered %+v, want removed true", removed)
	}
	rec = f.call(t, f.cookie, http.MethodGet, f.path("/views/by-key/route"), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET after DELETE = %d, want 404", rec.Code)
	}
}

// TestAQueryRefusalOverRESTCarriesItsPointerAndItsCode is the wire codes
// arriving at a browser: the same code an agent gets, the status this
// surface adds, and the pointer as data rather than inside prose a
// client would have to parse.
func TestAQueryRefusalOverRESTCarriesItsPointerAndItsCode(t *testing.T) {
	f := newViewsRESTFixture(t)
	rec := f.call(t, f.cookie, http.MethodPost, f.path("/views/validate"), map[string]any{
		"query": json.RawMessage(`{"v":1,"from":[{"type":"qeust"}]}`),
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error   string `json:"error"`
		Details struct {
			Fields []map[string]string `json:"fields"`
		} `json:"details"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if body.Error != "query_invalid" {
		t.Errorf("error = %q, want query_invalid", body.Error)
	}
	if len(body.Details.Fields) != 1 || body.Details.Fields[0]["path"] != "/from/0/type" {
		t.Fatalf("details.fields = %v, want the pointer into the document", body.Details.Fields)
	}
}

// TestAViewerRunsAViewAndCannotSaveOne is open question O3, made true
// rather than intended — for the half a convention test cannot make.
// TestEveryContentWriteRouteRefusesAViewer already drives every write at
// a viewer; what it cannot say is that a viewer can still *read*, and a
// surface that refused a viewer everything would satisfy it.
//
// Running is the interesting case, because it is a read spelled as a
// POST: over REST it is therefore gated as a write, which is what
// api_views.go's header records as the cost of that spelling. So the
// read a viewer must keep is the listing and the view itself.
func TestAViewerRunsAViewAndCannotSaveOne(t *testing.T) {
	f := newViewsRESTFixture(t)
	ctx := context.Background()
	viewer, err := f.ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "viewer@studio.com", DisplayName: "Viewer", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := f.proj.SetRole(ctx, viewer.ID, f.game, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	if _, err := f.views.UpsertView(ctx, f.game, views.ViewInput{
		Key: "route", Name: "Route", Query: []byte(questsQuery), Renderer: "graph",
	}); err != nil {
		t.Fatalf("seed a view: %v", err)
	}
	cookie := loginAs(t, f.srv, "viewer@studio.com")

	if rec := f.call(t, cookie, http.MethodGet, f.path("/views"), nil); rec.Code != http.StatusOK {
		t.Fatalf("a viewer listing views = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := f.call(t, cookie, http.MethodGet, f.path("/views/by-key/route"), nil); rec.Code != http.StatusOK {
		t.Fatalf("a viewer opening a view = %d: %s", rec.Code, rec.Body.String())
	}
	rec := f.call(t, cookie, http.MethodPost, f.path("/views"), map[string]any{
		"key": "another", "name": "Another", "renderer": "graph",
		"query": json.RawMessage(questsQuery), "expected_version": 0,
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a viewer saving a view = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	// Nothing was stored by the refused write.
	if _, err := f.views.ViewByKey(ctx, f.game, "another"); err == nil {
		t.Fatal("the refused write stored a view anyway")
	}
}

// TestAViewEventReachesAViewerAndATokenAlike is the read-back for the
// four view.* kinds, and it is a different claim from internal/views'
// own event tests: those prove the hub was published to, this proves a
// subscriber actually receives it, over the endpoint it subscribes to,
// on a server wired the way main.go wires it.
//
// **Two subscribers, and both are load-bearing.** A viewer is the one a
// MinRole above viewer would cut out, and a token caller is the one
// HumanOnly true would cut out — and internal/web's own member, token
// and invite events all set HumanOnly true, so a test that only ever
// subscribed with a cookie would stay green if somebody published a
// view event that way and agents had silently stopped being told.
func TestAViewEventReachesAViewerAndATokenAlike(t *testing.T) {
	f := newViewsRESTFixture(t)
	ctx := context.Background()

	viewer, err := f.ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "viewer@studio.com", DisplayName: "Viewer", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := f.proj.SetRole(ctx, viewer.ID, f.game, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	secret, _, err := f.ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: f.game, UserID: f.ownerID, Label: "view agent",
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	viewerCookie := loginAs(t, f.srv, "viewer@studio.com")

	ts := httptest.NewServer(f.srv)
	// t.Cleanup rather than defer, and registered *before* the streams
	// are opened: cleanups run last-registered-first, so each stream's
	// own body close runs ahead of this, and httptest.Server.Close blocks
	// until every in-flight request has finished. With a defer here the
	// two open SSE handlers were still running, and the test sat out the
	// whole SSEMaxLifetime — a minute of waiting for a test that has
	// already passed, which is how a suite stops being run.
	t.Cleanup(ts.Close)
	// Uploaded before the streams open, so the upload's own traffic
	// cannot arrive as a frame this test then reads as a view event.
	background := f.uploadAsset(t, "ground.png")
	viewerStream := openStream(t, ts, f.game.String(), viewerCookie.Value)
	agentStream := openTokenStream(t, ts, f.game.String(), secret)

	// Every write goes through the *route*, not through the service:
	// this test is about the wiring, and a direct service call would
	// prove the domain publishes and nothing about whether the server
	// this process serves is holding the same hub.
	post := func(suffix string, body any) {
		t.Helper()
		if rec := f.call(t, f.cookie, http.MethodPost, f.path(suffix), body); rec.Code != http.StatusOK {
			t.Fatalf("POST %s = %d: %s", suffix, rec.Code, rec.Body.String())
		}
	}

	for _, step := range []struct {
		what string
		kind string
		do   func()
	}{
		// The renderer is map because one of the four kinds below is a
		// background, and only that renderer draws one. Nothing else in
		// this test reads the renderer.
		{"a save", "view.upserted", func() {
			post("/views", map[string]any{"key": "route", "name": "Route", "renderer": "map",
				"query": json.RawMessage(questsQuery), "expected_version": 0})
		}},
		{"a drag", "view.positions", func() {
			post("/views/by-key/route/positions", map[string]any{"positions": []any{
				map[string]any{"entity_type": "quest", "entity_key": "hogger", "x": 1, "y": 2},
			}})
		}},
		{"a clear", "view.positions", func() {
			post("/views/by-key/route/positions/clear", map[string]any{})
		}},
		// A background is the map renderer's ground, and a browser
		// holding a picture has no other way to learn the ground moved
		// — the argument view.positions is published on, which
		// views.sql states from the storage side as "a dragged node is
		// the same kind of act as a placed background". Both arms are
		// driven, because a clear removes the picture's ground as
		// surely as a placement changes it.
		{"a background", "view.background", func() {
			post("/views/by-key/route/background", map[string]any{"asset_id": background})
		}},
		{"a background clear", "view.background", func() {
			post("/views/by-key/route/background", map[string]any{})
		}},
		{"a removal", "view.removed", func() {
			if rec := f.call(t, f.cookie, http.MethodDelete,
				f.path("/views/by-key/route"), nil); rec.Code != http.StatusOK {
				t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body.String())
			}
		}},
	} {
		step.do()
		for who, reader := range map[string]*bufio.Reader{
			"the viewer": viewerStream, "the agent": agentStream,
		} {
			kind, _, data := readOneSSEFrame(t, reader)
			if kind != step.kind {
				t.Fatalf("%s got %q after %s, want %s", who, kind, step.what, step.kind)
			}
			if !strings.Contains(data, `"key":"route"`) {
				t.Errorf("%s got payload %s, want the view's key in it", who, data)
			}
			// A position event carries the identity and nothing else —
			// no coordinates, because publication order is not commit
			// order and a client rendering a payload would eventually
			// render the older of two drags.
			// The same holds for a background: identity only, no
			// asset id and no version, since the write advances none.
			if step.kind != "view.upserted" && step.kind != "view.removed" &&
				(strings.Contains(data, `"x"`) || strings.Contains(data, `"version"`) ||
					strings.Contains(data, `"asset`)) {
				t.Errorf("%s got a value on a %s payload, want identity only: %s",
					who, step.kind, data)
			}
		}
	}
}

// decodeInto reads a successful JSON response body.
func decodeInto(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

// TestTheViewListingCarriesItsRendererAndCursorOverREST is the REST half
// of the claim mcp_views_test.go makes for the tool: these two arguments
// travel as query parameters here rather than in a body, which is a
// second place for them to be dropped, and removing them from the
// listing input left the whole suite green.
func TestTheViewListingCarriesItsRendererAndCursorOverREST(t *testing.T) {
	f := newViewsRESTFixture(t)
	for _, view := range []struct{ key, renderer string }{
		{"one", "graph"}, {"two", "graph"}, {"atlas", "map"},
	} {
		if rec := f.call(t, f.cookie, http.MethodPost, f.path("/views"), map[string]any{
			"key": view.key, "name": view.key, "renderer": view.renderer,
			"query": json.RawMessage(questsQuery), "expected_version": 0,
		}); rec.Code != http.StatusOK {
			t.Fatalf("save %s = %d: %s", view.key, rec.Code, rec.Body.String())
		}
	}

	list := func(query string) web.ViewsListOutput {
		t.Helper()
		rec := f.call(t, f.cookie, http.MethodGet, f.path("/views"+query), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /views%s = %d: %s", query, rec.Code, rec.Body.String())
		}
		var out web.ViewsListOutput
		decodeInto(t, rec, &out)
		return out
	}

	only := list("?renderer=map")
	if len(only.Items) != 1 || only.Items[0].Key != "atlas" {
		t.Fatalf("?renderer=map answered %+v, want only the map view", only.Items)
	}
	// The control: all three are really there, so the filter is doing
	// the narrowing rather than an empty listing satisfying the check.
	if whole := list(""); len(whole.Items) != 3 {
		t.Fatalf("the unfiltered listing answered %+v, want all three", whole.Items)
	}

	page := list("?renderer=graph&limit=1")
	if len(page.Items) != 1 || page.NextCursor == nil {
		t.Fatalf("page one = %+v with cursor %v", page.Items, page.NextCursor)
	}
	next := list("?renderer=graph&limit=1&cursor=" + url.QueryEscape(*page.NextCursor))
	if len(next.Items) != 1 || next.Items[0].Key == page.Items[0].Key {
		t.Fatalf("page two = %+v, want the other graph view", next.Items)
	}
	// A cursor belongs to the filter it was issued for, on this surface
	// too: replayed against another renderer it is refused as the
	// caller's own argument rather than answered.
	rec := f.call(t, f.cookie, http.MethodGet,
		f.path("/views?renderer=map&cursor="+url.QueryEscape(*page.NextCursor)), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a cursor from another filter = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// TestAStatedProjectDisagreeingWithTheURLIsRefusedOnEveryViewsWriteRoute
// makes the check on these routes real rather than decorative. It is not
// a security hole — the scope always wins, and a session caller reaching
// another game is refused by the admission check before any of this —
// but the refusal the field exists to make was unasserted on every one
// of the routes that call it, and removing the check from the run route
// left the whole suite green.
//
// The field is a confirmation and never a selector: a client that has
// lost track of which game it is editing is told so, rather than told
// its write succeeded in the other one.
func TestAStatedProjectDisagreeingWithTheURLIsRefusedOnEveryViewsWriteRoute(t *testing.T) {
	f := newViewsRESTFixture(t)
	if rec := f.call(t, f.cookie, http.MethodPost, f.path("/views"), map[string]any{
		"key": "route", "name": "Route", "renderer": "graph",
		"query": json.RawMessage(questsQuery), "expected_version": 0,
	}); rec.Code != http.StatusOK {
		t.Fatalf("seed a view: %d %s", rec.Code, rec.Body.String())
	}

	for _, route := range []struct {
		suffix string
		body   map[string]any
	}{
		{"/views", map[string]any{"key": "route", "name": "Route", "renderer": "graph",
			"query": json.RawMessage(questsQuery), "expected_version": 1}},
		{"/views/run", map[string]any{"key": "route"}},
		{"/views/validate", map[string]any{"query": json.RawMessage(questsQuery)}},
		{"/views/by-key/route/positions", map[string]any{"positions": []any{
			map[string]any{"entity_type": "quest", "entity_key": "hogger", "x": 1, "y": 2},
		}}},
		{"/views/by-key/route/positions/clear", map[string]any{}},
		{"/views/by-key/route/background", map[string]any{}},
	} {
		t.Run(route.suffix, func(t *testing.T) {
			// The control first: the same body with no project_id at all
			// is accepted, so the refusal below is about the field and
			// not about the request being malformed.
			if rec := f.call(t, f.cookie, http.MethodPost, f.path(route.suffix),
				route.body); rec.Code != http.StatusOK {
				t.Fatalf("without a project_id = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			body := map[string]any{"project_id": f.other.String()}
			for k, v := range route.body {
				body[k] = v
			}
			rec := f.call(t, f.cookie, http.MethodPost, f.path(route.suffix), body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("a project_id naming another game = %d, want 403: %s",
					rec.Code, rec.Body.String())
			}
			var answer struct {
				Error string `json:"error"`
			}
			decodeInto(t, rec, &answer)
			if answer.Error != "scope_violation" {
				t.Errorf("error = %q, want scope_violation", answer.Error)
			}
		})
	}
}
