package web_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/web"
)

// The images attached to an entity, over the surfaces a person and an
// agent actually use: the browser's three routes, and the entity answer
// an agent reads.

// attachFixture seeds one entity to hang images on.
func attachFixture(t *testing.T) (restFixture, string) {
	t.Helper()
	f := newRESTFixture(t)
	questType(t, f)
	rec := f.as(t, http.MethodPost, "/entities", map[string]any{
		"items": []any{map[string]any{"type_key": "quest", "key": "first-steps", "name": "First Steps"}},
	})
	assert.Must(t, rec.Code == http.StatusOK, "seed the entity = %d: %s", rec.Code, rec.Body.String())
	return f, "/entities/by-key/quest/first-steps/images"
}

// rawRequest sends one request with a body this fixture must not
// marshal — image bytes, or none at all — against an absolute path,
// because a signed download URL is not under /api/games/{game}.
func rawRequest(t *testing.T, f restFixture, method, path, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

// unsigned sends a request carrying nothing at all: no cookie, no token.
func unsigned(t *testing.T, f restFixture, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func imageBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	assert.Must(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))) == nil, "encode png")
	return buf.Bytes()
}

// TestAnImageIsAttachedToAnEntityAndDetachedFromIt is the browser's
// whole round trip: upload into the game's library, hang it on the
// entity, read it back, take it off.
func TestAnImageIsAttachedToAnEntityAndDetachedFromIt(t *testing.T) {
	t.Parallel()
	f, path := attachFixture(t)

	// The upload is the library's own route, unchanged: a file becomes
	// an image in the game's library first and this entity's second.
	rec := rawRequest(t, f, http.MethodPost,
		f.path("/assets?filename=map.png"), "image/png", imageBytes(t, 64, 48))
	assert.Must(t, rec.Code == http.StatusOK, "upload = %d: %s", rec.Code, rec.Body.String())
	var uploaded struct {
		ID string `json:"id"`
	}
	assert.Must(t, json.Unmarshal(rec.Body.Bytes(), &uploaded) == nil, "decode the upload")

	rec = f.as(t, http.MethodPost, path, map[string]any{"asset_id": uploaded.ID})
	assert.Must(t, rec.Code == http.StatusOK, "attach = %d: %s", rec.Code, rec.Body.String())

	var listed web.EntityImagesOutput
	assert.Must(t, json.Unmarshal(rec.Body.Bytes(), &listed) == nil, "decode the attach answer")
	assert.Must(t, len(listed.Items) == 1, "attach answered %d images, want 1", len(listed.Items))
	// **The attach answers with the whole listing**, so a page that has
	// it does not need a second call to draw the result.
	got := listed.Items[0]
	assert.Should(t, got.Filename == "map.png", "filename %q", got.Filename)
	assert.Should(t, got.Width == 64 && got.Height == 48, "size %dx%d, want 64x48", got.Width, got.Height)
	assert.Should(t, got.URL == "/api/games/"+f.gameSlug+"/assets/"+got.ID.String(),
		"url %q does not point at this game's own asset route", got.URL)

	// And the URL in the answer really serves the picture, which is the
	// half a shape test cannot see.
	rec = rawRequest(t, f, http.MethodGet, got.URL, "", nil)
	assert.Must(t, rec.Code == http.StatusOK, "the url the listing gave answered %d", rec.Code)
	assert.Should(t, rec.Header().Get("Content-Type") == "image/png", "content type %q", rec.Header().Get("Content-Type"))

	rec = f.as(t, http.MethodGet, path, nil)
	assert.Must(t, rec.Code == http.StatusOK, "list = %d: %s", rec.Code, rec.Body.String())
	assert.Must(t, json.Unmarshal(rec.Body.Bytes(), &listed) == nil, "decode the listing")
	assert.Should(t, len(listed.Items) == 1, "the listing holds %d, want 1", len(listed.Items))

	rec = f.as(t, http.MethodDelete, path+"/"+got.ID.String(), nil)
	assert.Must(t, rec.Code == http.StatusOK, "detach = %d: %s", rec.Code, rec.Body.String())
	assert.Must(t, json.Unmarshal(rec.Body.Bytes(), &listed) == nil, "decode the detach answer")
	assert.Should(t, len(listed.Items) == 0, "%d images survived the detach", len(listed.Items))

	// **The image is still in the library.** Detaching says this entity
	// no longer refers to it, and nothing more.
	rec = rawRequest(t, f, http.MethodGet, f.path("/assets/"+got.ID.String()), "", nil)
	assert.Should(t, rec.Code == http.StatusOK, "detaching took the image out of the library: %d", rec.Code)
}

// TestAnAgentIsToldWhatIsAttachedAndCanFetchItWithoutCredentials is the
// agent's half, and the reason the feature is shaped the way it is: an
// agent never uploads, is told what is there when it reads the entity,
// and is handed a URL a shell can fetch.
func TestAnAgentIsToldWhatIsAttachedAndCanFetchItWithoutCredentials(t *testing.T) {
	t.Parallel()
	f, path := attachFixture(t)

	raw := imageBytes(t, 32, 16)
	rec := rawRequest(t, f, http.MethodPost,
		f.path("/assets?filename=reference.png"), "image/png", raw)
	assert.Must(t, rec.Code == http.StatusOK, "upload = %d: %s", rec.Code, rec.Body.String())
	var uploaded struct {
		ID string `json:"id"`
	}
	assert.Must(t, json.Unmarshal(rec.Body.Bytes(), &uploaded) == nil, "decode the upload")
	rec = f.as(t, http.MethodPost, path, map[string]any{"asset_id": uploaded.ID})
	assert.Must(t, rec.Code == http.StatusOK, "attach = %d: %s", rec.Code, rec.Body.String())

	// **Through the server, not through a hand-built MCPDeps.** The
	// first draft of this test called the core with deps it assembled
	// itself, which carried no signing key: every download_url came out
	// well formed and answered 401, and the test found a defect that was
	// only in the test. The entity read is the same core on both
	// surfaces, and this is the one path that proves the server wired
	// the key into it.
	rec = f.as(t, http.MethodGet, "/entities/by-key/quest/first-steps", nil)
	assert.Must(t, rec.Code == http.StatusOK, "read the entity = %d: %s", rec.Code, rec.Body.String())
	var entity struct {
		Images []web.EntityImageRef `json:"images"`
	}
	assert.Must(t, json.Unmarshal(rec.Body.Bytes(), &entity) == nil, "decode the entity")
	assert.Must(t, len(entity.Images) == 1, "the entity answer names %d images, want 1", len(entity.Images))
	img := entity.Images[0]
	assert.Should(t, img.Filename == "reference.png", "filename %q", img.Filename)
	assert.Should(t, img.Mime == "image/png", "mime %q", img.Mime)
	assert.Should(t, img.Width == 32 && img.Height == 16, "size %dx%d", img.Width, img.Height)
	assert.Must(t, img.DownloadURL != "", "the answer carries no download_url: the agent is told the picture "+
		"exists and given no way to it")

	// **The URL works with no credentials at all**, which is the whole
	// point of signing it: the request below carries no cookie and no
	// bearer token.
	parsed, err := url.Parse(img.DownloadURL)
	assert.Must(t, err == nil, "download_url is not a URL: %v", err)
	rec = unsigned(t, f, parsed.RequestURI())
	assert.Must(t, rec.Code == http.StatusOK, "the signed url answered %d: %s", rec.Code, rec.Body.String())
	assert.Should(t, bytes.Equal(rec.Body.Bytes(), raw), "the signed url served %d bytes, the upload was %d",
		rec.Body.Len(), len(raw))

	// The signature is over the whole path, so it does not admit a
	// different image.
	other := strings.Replace(parsed.RequestURI(), img.ID.String(),
		"00000000-0000-0000-0000-000000000000", 1)
	rec = unsigned(t, f, other)
	assert.Should(t, rec.Code == http.StatusUnauthorized,
		"a signature minted for one image admitted another: %d", rec.Code)

	// And a listing does not pay for this: it would be one query per row,
	// for a question no listing asked.
	rec = f.as(t, http.MethodGet, "/entities?type_key=quest", nil)
	assert.Must(t, rec.Code == http.StatusOK, "list = %d: %s", rec.Code, rec.Body.String())
	var page struct {
		Items []struct {
			Images []web.EntityImageRef `json:"images"`
		} `json:"items"`
	}
	assert.Must(t, json.Unmarshal(rec.Body.Bytes(), &page) == nil, "decode the listing")
	assert.Must(t, len(page.Items) == 1, "the listing holds %d rows", len(page.Items))
	assert.Should(t, len(page.Items[0].Images) == 0, "a listing carried images")
}
