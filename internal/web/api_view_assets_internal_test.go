package web

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/testutil"
	"github.com/neverbot/maestro/internal/views"
)

// TestTheServingRoutesNoSniffHeaderIsItsOwn asserts the header below the
// middleware that masks it.
func TestTheServingRoutesNoSniffHeaderIsItsOwn(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	var project uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (slug, name) VALUES ('azeroth', 'Azeroth') RETURNING id`,
	).Scan(&project); err != nil {
		t.Fatalf("insert the game: %v", err)
	}
	svc := views.New(pool, nil)

	var raw bytes.Buffer
	if err := png.Encode(&raw, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	asset, err := svc.CreateAsset(ctx, project, views.Actor{}, "azeroth.png",
		bytes.NewReader(raw.Bytes()))
	assert.Must(t, err == nil, "upload: %v", err)

	s := &Server{opts: Options{Views: svc}}
	req := httptest.NewRequest(http.MethodGet, "/api/games/azeroth/view-assets/"+asset.ID.String(), nil)
	req.SetPathValue("id", asset.ID.String())
	rec := httptest.NewRecorder()
	s.handleServeViewAsset(rec, req, Caller{}, ProjectScope{ProjectID: project})

	assert.Must(t, rec.Code == http.StatusOK, "serve = %d: %s", rec.Code, rec.Body.String())
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q with no middleware in front, want "+
			"nosniff: this route sets it itself precisely so that a global "+
			"middleware narrowed later cannot take it away from here", got)
	}
	// The other two headers this handler owns, asserted in the same
	// place for the same reason: nothing else sets either of them, but a
	// reader arriving here should see the whole of what the route
	// promises rather than one third of it.
	if got := rec.Header().Get("Content-Type"); got != views.MimePNG {
		t.Fatalf("Content-Type = %q, want the stored mime %q", got, views.MimePNG)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q, want the long private one this route overrides "+
			"requireCaller's no-store with", got)
	}
}
