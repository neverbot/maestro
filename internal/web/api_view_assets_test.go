package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/testutil"
	"github.com/neverbot/maestro/internal/views"
	"github.com/neverbot/maestro/internal/web"
)

// The background-asset routes, over HTTP.
//
// Everything about the *bytes* is decided in internal/views and is
// tested there — the sniff, the size bound, the header decode, the SVG
// refusal. What is tested here is what only the transport can be wrong
// about: which headers a served image carries, that the body arrives
// byte for byte, that the transport's own size bound exists, and that
// one game cannot reach another's images through a leaked id.

type assetFixture struct {
	srv     *web.Server
	ids     *identity.Service
	proj    *projects.Service
	game    uuid.UUID
	other   uuid.UUID
	ownerID uuid.UUID
	cookie  *http.Cookie
}

func newAssetFixture(t *testing.T) assetFixture {
	t.Helper()
	pool := testutil.NewPool(t)
	cfg := config.Config{
		SessionTTL: testConfig().SessionTTL,
		InviteTTL:  testConfig().InviteTTL,
		Argon2:     testConfig().Argon2,
	}
	ids := identity.New(pool, cfg)
	projSvc := projects.New(pool)
	srv := web.NewServer(web.Options{
		Version:  "test",
		Config:   cfg,
		Identity: ids,
		Projects: projSvc,
		Views:    views.New(pool, nil),
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
	return assetFixture{
		srv: srv, ids: ids, proj: projSvc, game: game.ID, other: other.ID,
		ownerID: owner.ID, cookie: loginAs(t, srv, "owner@studio.com"),
	}
}

// send makes one request as the fixture's owner, with a raw body.
func (f assetFixture) send(t *testing.T, cookie *http.Cookie, method, path string,
	contentType string, body []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

func (f assetFixture) uploadPath(game uuid.UUID, filename string) string {
	return "/api/games/" + game.String() + "/view-assets?filename=" + filename
}

// upload posts one image and returns the decoded answer.
func (f assetFixture) upload(t *testing.T, game uuid.UUID, filename, contentType string,
	raw []byte,
) web.ViewAssetOutput {
	t.Helper()
	rec := f.send(t, f.cookie, http.MethodPost, f.uploadPath(game, filename),
		contentType, raw)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload %s = %d: %s", filename, rec.Code, rec.Body.String())
	}
	var out web.ViewAssetOutput
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode upload answer: %v", err)
	}
	return out
}

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func testJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

// TestAnAssetIsServedWithANoSniffHeaderAndItsOwnContentType is the
// serving route's whole contract.
//
// "Its own" is the load-bearing word: two formats are uploaded and each
// has to come back under the mime *its own bytes* carry. A handler that
// answered a constant image/png would pass a test that served one image,
// and a browser told the wrong type for the right bytes is exactly the
// confusion nosniff exists to stop mattering.
func TestAnAssetIsServedWithANoSniffHeaderAndItsOwnContentType(t *testing.T) {
	f := newAssetFixture(t)
	for _, tc := range []struct {
		filename string
		raw      []byte
		wantMime string
	}{
		// The names are deliberately misleading in both rows, because
		// nothing on this route may read them.
		{"map.jpg", testPNG(t, 37, 19), "image/png"},
		{"map.png", testJPEG(t, 48, 21), "image/jpeg"},
	} {
		asset := f.upload(t, f.game, tc.filename, "application/octet-stream", tc.raw)
		if asset.Mime != tc.wantMime {
			t.Fatalf("%s stored as %q, want %q", tc.filename, asset.Mime, tc.wantMime)
		}
		if asset.URL == "" {
			t.Fatalf("%s came back with no url to fetch it from", tc.filename)
		}

		rec := f.send(t, f.cookie, http.MethodGet, asset.URL, "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("serve %s = %d: %s", tc.filename, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); got != tc.wantMime {
			t.Errorf("Content-Type = %q, want %q: the served type is the stored one, "+
				"which is the one a decoder agreed with", got, tc.wantMime)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("X-Content-Type-Options = %q, want nosniff: a browser guessing "+
				"its own type out of bytes a designer uploaded is the whole of the "+
				"risk the closed mime list bounds", got)
		}
		if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") ||
			!strings.HasPrefix(got, "private") {
			t.Errorf("Cache-Control = %q, want a private immutable cache: an asset's "+
				"bytes never change, and they are a game's own content", got)
		}
		if !bytes.Equal(rec.Body.Bytes(), tc.raw) {
			t.Errorf("%s came back as %d bytes, want the %d that were uploaded",
				tc.filename, rec.Body.Len(), len(tc.raw))
		}
		// The rest of the policy every response carries is not lost on
		// the one route that writes bytes rather than JSON.
		if got := rec.Header().Get("Content-Security-Policy"); got == "" {
			t.Errorf("the served image lost the content security policy")
		}
	}
}

// TestAnSVGIsRefusedByTheRouteWhateverItSaysItIs is the plan's first
// case, driven over the wire: the bytes of an SVG, sent with a PNG's
// Content-Type and a PNG's filename.
//
// Both of those are the caller's own claims, and this route reads
// neither. The refusal is internal/views' — this test is what proves the
// transport does not slip a second, weaker judgement in front of it.
func TestAnSVGIsRefusedByTheRouteWhateverItSaysItIs(t *testing.T) {
	f := newAssetFixture(t)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	rec := f.send(t, f.cookie, http.MethodPost, f.uploadPath(f.game, "world-map.png"),
		"image/png", svg)
	assertError(t, rec, http.StatusBadRequest, "invalid_input", "/bytes")
	if !strings.Contains(rec.Body.String(), "SVG") {
		t.Errorf("the refusal said %q, want it to name the format a designer actually "+
			"tried to upload", rec.Body.String())
	}
	// Nothing was stored, so a second request cannot serve it.
	rec = f.send(t, f.cookie, http.MethodGet,
		"/api/games/"+f.game.String()+"/view-assets", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	var listing struct {
		Assets []web.ViewAssetOutput `json:"assets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	if len(listing.Assets) != 0 {
		t.Fatalf("a refused upload left %d assets behind", len(listing.Assets))
	}
}

// TestAnOversizeUploadIsRefusedOverTheWire asserts that the domain's
// size bound reaches a browser as something a designer can act on.
//
// **The plan asked for an http.MaxBytesReader here and there is none**,
// because it can never fire: the domain reads through a LimitReader of
// the same cap in the same read path, so the transport's limiter is
// never handed the byte that would trip it, and setting it a byte
// tighter only replaces a sentence about scaling the image down with
// `http: request body too large`. api_view_assets.go's header records
// the measurement. What is left to assert here is the mapping — a 400
// naming /bytes — which is the transport's actual job.
func TestAnOversizeUploadIsRefusedOverTheWire(t *testing.T) {
	f := newAssetFixture(t)
	oversize := make([]byte, views.MaxAssetBytes+1024)
	copy(oversize, testPNG(t, 8, 8))
	rec := f.send(t, f.cookie, http.MethodPost, f.uploadPath(f.game, "enormous.png"),
		"image/png", oversize)
	assertError(t, rec, http.StatusBadRequest, "invalid_input", "/bytes")
	// The advice, not merely the status: a designer whose world map is
	// too big needs to be told what to do about it, and this is the
	// sentence a transport-level limiter would have replaced.
	if !strings.Contains(rec.Body.String(), "scale the image down") {
		t.Fatalf("the refusal was %q, want the domain's own advice", rec.Body.String())
	}
	// The control, and it is not a formality: a transport bound set one
	// byte too tight would refuse every legitimate image and this test
	// would still pass without it.
	f.upload(t, f.game, "fine.png", "image/png", testPNG(t, 8, 8))
}

// TestAnAssetOfAnotherGameIsNotServedOverHTTP is the isolation sweep for
// these routes, with its positive control.
//
// The id is real and the caller owns both games, which is the case that
// matters: the refusal has to come from the URL's game and not from the
// caller's standing, or a leaked id would serve one game's map inside
// another's page.
func TestAnAssetOfAnotherGameIsNotServedOverHTTP(t *testing.T) {
	f := newAssetFixture(t)
	theirs := f.upload(t, f.other, "le-mans.png", "image/png", testPNG(t, 12, 8))

	crossed := "/api/games/" + f.game.String() + "/view-assets/" + theirs.ID
	if rec := f.send(t, f.cookie, http.MethodGet, crossed, "", nil); rec.Code !=
		http.StatusNotFound {
		t.Fatalf("serving across games = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if rec := f.send(t, f.cookie, http.MethodDelete, crossed, "", nil); rec.Code !=
		http.StatusNotFound {
		t.Fatalf("deleting across games = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	// The control: the same id under its own game is served and then
	// deleted, so the two refusals above are about the game.
	own := "/api/games/" + f.other.String() + "/view-assets/" + theirs.ID
	if rec := f.send(t, f.cookie, http.MethodGet, own, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("serving its own game = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := f.send(t, f.cookie, http.MethodDelete, own, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("deleting in its own game = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := f.send(t, f.cookie, http.MethodGet, own, "", nil); rec.Code !=
		http.StatusNotFound {
		t.Fatalf("a deleted asset is still served: %d", rec.Code)
	}
	// The listing is scoped the same way.
	rec := f.send(t, f.cookie, http.MethodGet,
		"/api/games/"+f.game.String()+"/view-assets", "", nil)
	var listing struct {
		Assets []web.ViewAssetOutput `json:"assets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	if len(listing.Assets) != 0 {
		t.Fatalf("azeroth listed the other game's assets: %v", listing.Assets)
	}
}

// TestAViewerMaySeeABackgroundAndMayNotUploadOne is the read/write split
// on this surface, stated here rather than left to
// TestEveryContentWriteRouteRefusesAViewer alone: that test proves the
// writes are gated, and this one proves the *read* is not — a viewer who
// cannot see the map cannot look at the game.
func TestAViewerMaySeeABackgroundAndMayNotUploadOne(t *testing.T) {
	f := newAssetFixture(t)
	ctx := context.Background()
	asset := f.upload(t, f.game, "azeroth.png", "image/png", testPNG(t, 20, 10))

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

	if rec := f.send(t, cookie, http.MethodGet, asset.URL, "", nil); rec.Code !=
		http.StatusOK {
		t.Fatalf("a viewer must see the background: %d %s", rec.Code, rec.Body.String())
	}
	rec := f.send(t, cookie, http.MethodPost, f.uploadPath(f.game, "sneaky.png"),
		"image/png", testPNG(t, 8, 8))
	assertError(t, rec, http.StatusForbidden, "forbidden", "")
}

// TestTheAssetRoutesAreVisibleToTheConventionTests is api_docs.go's own
// guard, applied to this surface for the same reason: the routes are
// registered unconditionally, so a registration later gated on
// Options.Views being present would make all four invisible to
// TestEveryContentRouteIsRegisteredAsContent and
// TestEveryContentWriteRouteRefusesAViewer without failing either.
func TestTheAssetRoutesAreVisibleToTheConventionTests(t *testing.T) {
	srv := web.NewServer(web.Options{
		Version:  "test",
		Identity: identity.New(nil, config.Config{}),
		Projects: projects.New(nil),
		// Deliberately no Views: this is stubOptions' own shape.
	})
	want := []string{
		"GET /api/games/{game}/view-assets",
		"POST /api/games/{game}/view-assets",
		"GET /api/games/{game}/view-assets/{id}",
		"DELETE /api/games/{game}/view-assets/{id}",
	}
	content := map[string]bool{}
	for _, pattern := range srv.ContentPatternsForTest() {
		content[pattern] = true
	}
	writes := map[string]bool{}
	for _, pattern := range srv.ContentWritePatternsForTest() {
		writes[pattern] = true
	}
	for _, pattern := range want {
		if !content[pattern] {
			t.Errorf("%s is not registered as a content route", pattern)
		}
		isWrite := !strings.HasPrefix(pattern, http.MethodGet)
		if writes[pattern] != isWrite {
			t.Errorf("%s: registered as a write = %v, want %v", pattern,
				writes[pattern], isWrite)
		}
	}
}

// TestAnAssetRouteOnAnInstanceWithNoViewsServiceIs404 pins the shape
// Options.Views' own doc comment promises, which the test above depends
// on being harmless.
func TestAnAssetRouteOnAnInstanceWithNoViewsServiceIs404(t *testing.T) {
	f := newAssetFixture(t)
	bare := web.NewServer(web.Options{
		Version:  "test",
		Config:   testConfig(),
		Identity: f.ids,
		Projects: f.proj,
	})
	req := httptest.NewRequest(http.MethodGet,
		"/api/games/"+f.game.String()+"/view-assets", nil)
	req.AddCookie(loginAs(t, bare, "owner@studio.com"))
	rec := httptest.NewRecorder()
	bare.ServeHTTP(rec, req)
	assertError(t, rec, http.StatusNotFound, "not_found", "")
	if !strings.Contains(rec.Body.String(), "no views") {
		t.Errorf("body = %q, want it to say the instance serves no views",
			rec.Body.String())
	}
}
