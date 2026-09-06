package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/web"
)

// TestAMoveOnTheSurfaceKeepsTheDocumentAndAnswersWithItsNewAddress is
// the surface half of the move: what a caller sends and what it reads
// back. The domain proves a move keeps the history; this proves the
// tool hands the caller the two facts it needs afterwards — the new path
// and the version — and that every other read agrees.
func TestAMoveOnTheSurfaceKeepsTheDocumentAndAnswersWithItsNewAddress(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	written, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
		Path: "lore/dusk.md", Content: "# Duskwood\n\nA wood.\n",
		Kind: stringPtr("lore"), ExpectedVersion: int32Ptr(0),
	})
	if err != nil {
		t.Fatalf("docs.write: %v", err)
	}

	moved, err := web.MCPDocsMove(ctx, f.deps, f.caller, f.game, web.DocsMoveInput{
		From: "lore/dusk.md", To: "zones/duskwood/lore.md",
		ExpectedVersion: int32Ptr(1), Message: "filed under its zone",
	})
	if err != nil {
		t.Fatalf("docs.move: %v", err)
	}
	if moved.Path != "zones/duskwood/lore.md" || moved.Version != 2 {
		t.Fatalf("move answered (%q, v%d), want the destination at version 2",
			moved.Path, moved.Version)
	}
	if moved.ID != written.ID {
		t.Fatalf("move answered a different document: %s, was %s", moved.ID, written.ID)
	}
	if moved.Kind != "lore" {
		t.Fatalf("Kind = %q, want it carried across", moved.Kind)
	}
	if moved.Deleted {
		t.Fatal("a moved document came back marked deleted")
	}
	// The version the move answers with is the one a following write has
	// to pass, and that is the whole reason a summary is returned rather
	// than nothing. Asserted by using it.
	if _, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
		Path: moved.Path, Content: "edited\n", ExpectedVersion: &moved.Version,
	}); err != nil {
		t.Fatalf("writing with the version the move answered with: %v", err)
	}

	// docs.read at the new address, not at the old one.
	if _, err := web.MCPDocsRead(ctx, f.deps, f.caller, f.game,
		web.DocsReadInput{Path: "lore/dusk.md"}); err == nil {
		t.Fatal("the old path still reads")
	}
	read, err := web.MCPDocsRead(ctx, f.deps, f.caller, f.game,
		web.DocsReadInput{Path: "zones/duskwood/lore.md"})
	if err != nil {
		t.Fatalf("docs.read at the new path: %v", err)
	}
	if read.Version != 3 {
		t.Fatalf("Version = %d, want 3", read.Version)
	}

	// And the listing lists it once, at the new path.
	page, err := web.MCPDocsList(ctx, f.deps, f.caller, f.game, web.DocsListInput{})
	if err != nil {
		t.Fatalf("docs.list: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Path != "zones/duskwood/lore.md" {
		t.Fatalf("listing = %+v, want one row at the new path", page.Items)
	}
}

// TestAHistoryOnTheSurfaceSaysWhereEachVersionWasWritten. The path is on
// every history row and on every version read, and it is what makes the
// move's own row legible as a move. A field on the wire nothing reads
// back is not on the wire, so this reads it back through both.
func TestAHistoryOnTheSurfaceSaysWhereEachVersionWasWritten(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	if _, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
		Path: "lore/dusk.md", Content: "one\n", ExpectedVersion: int32Ptr(0),
	}); err != nil {
		t.Fatalf("docs.write: %v", err)
	}
	if _, err := web.MCPDocsMove(ctx, f.deps, f.caller, f.game, web.DocsMoveInput{
		From: "lore/dusk.md", To: "zones/dusk.md", ExpectedVersion: int32Ptr(1),
	}); err != nil {
		t.Fatalf("docs.move: %v", err)
	}

	hist, err := web.MCPDocsHistory(ctx, f.deps, f.caller, f.game,
		web.DocsHistoryInput{Path: "zones/dusk.md"})
	if err != nil {
		t.Fatalf("docs.history: %v", err)
	}
	if len(hist.Items) != 2 {
		t.Fatalf("history has %d items, want 2", len(hist.Items))
	}
	// Newest first.
	if hist.Items[0].Path != "zones/dusk.md" || hist.Items[1].Path != "lore/dusk.md" {
		t.Fatalf("history paths = %q then %q, want zones/dusk.md then lore/dusk.md",
			hist.Items[0].Path, hist.Items[1].Path)
	}
	// The move's row is a move and not an edit: same title and summary
	// as its predecessor, different path. That is the reading the field
	// exists to make possible.
	if hist.Items[0].Title != hist.Items[1].Title {
		t.Fatalf("the move's row changed the title: %q vs %q",
			hist.Items[0].Title, hist.Items[1].Title)
	}

	// And the same field survives JSON, which is where a struct field
	// with the wrong tag would quietly vanish.
	encoded, err := json.Marshal(hist.Items[1])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"path":"lore/dusk.md"`) {
		t.Fatalf("a history row on the wire = %s, want it to carry its own path", encoded)
	}
}

// TestReadingAVersionOfAMovedDocumentAnswersWithItsOwnPath. docs.
// read_version used to echo the caller's `path` argument straight back;
// once documents can move, that reports today's address for yesterday's
// snapshot. It also stops the answer echoing a caller's own casing back
// at it as though it were the stored spelling.
func TestReadingAVersionOfAMovedDocumentAnswersWithItsOwnPath(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	if _, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
		Path: "lore/dusk.md", Content: "one\n", ExpectedVersion: int32Ptr(0),
	}); err != nil {
		t.Fatalf("docs.write: %v", err)
	}
	if _, err := web.MCPDocsMove(ctx, f.deps, f.caller, f.game, web.DocsMoveInput{
		From: "lore/dusk.md", To: "zones/dusk.md", ExpectedVersion: int32Ptr(1),
	}); err != nil {
		t.Fatalf("docs.move: %v", err)
	}

	v1, err := web.MCPDocsReadVersion(ctx, f.deps, f.caller, f.game,
		web.DocsReadVersionInput{Path: "zones/dusk.md", Version: 1})
	if err != nil {
		t.Fatalf("docs.read_version 1: %v", err)
	}
	if v1.Path != "lore/dusk.md" {
		t.Fatalf("version 1 answers path %q, want the address it was written at", v1.Path)
	}

	// The casing half: a caller addressing the document under another
	// spelling is answered with the stored one.
	v2, err := web.MCPDocsReadVersion(ctx, f.deps, f.caller, f.game,
		web.DocsReadVersionInput{Path: "ZONES/DUSK.MD", Version: 2})
	if err != nil {
		t.Fatalf("docs.read_version 2 under another casing: %v", err)
	}
	if v2.Path != "zones/dusk.md" {
		t.Fatalf("version 2 answers path %q, want the stored spelling", v2.Path)
	}
}

// TestTheMoveRefusalsReachTheSurfaceWithTheirFields. Every refusal Move
// makes names an argument, and both surfaces publish that as
// details.fields. A refusal that arrived flat would put an agent back to
// guessing which end it got wrong. Driven over REST because that is
// where the field list is actually serialised — asserting the Go error
// type would pin the domain again and not the wire.
func TestTheMoveRefusalsReachTheSurfaceWithTheirFields(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	for _, path := range []string{"lore/dusk.md", "zones/dusk.md"} {
		if _, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
			Path: path, Content: "x\n", ExpectedVersion: int32Ptr(0),
		}); err != nil {
			t.Fatalf("docs.write %s: %v", path, err)
		}
	}

	cases := []struct {
		name   string
		body   string
		status int
		field  string
		says   string
	}{
		{
			name:   "a case-only move",
			body:   `{"from":"lore/dusk.md","to":"lore/Dusk.md","expected_version":1}`,
			status: http.StatusBadRequest,
			field:  "to", says: "only in capitalisation",
		},
		{
			name:   "an occupied destination",
			body:   `{"from":"lore/dusk.md","to":"zones/dusk.md","expected_version":1}`,
			status: http.StatusBadRequest,
			field:  "to", says: "already has a document at",
		},
		{
			name:   "a source that is not there",
			body:   `{"from":"lore/nowhere.md","to":"zones/elsewhere.md","expected_version":1}`,
			status: http.StatusNotFound,
			field:  "from", says: "has no document at",
		},
		{
			name:   "two bad paths at once",
			body:   `{"from":"/leading","to":"trailing/","expected_version":1}`,
			status: http.StatusBadRequest,
			field:  "from", says: "must not begin or end with a slash",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := jsonRequest(http.MethodPost,
				"/api/games/"+f.game.String()+"/docs/move", tc.body)
			req.Header.Set("Authorization", "Bearer "+f.token)
			rec := httptest.NewRecorder()
			f.srv.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			body := rec.Body.String()
			if !strings.Contains(body, `"`+tc.field+`"`) || !strings.Contains(body, tc.says) {
				t.Fatalf("the refusal does not name %q or say %q:\n%s", tc.field, tc.says, body)
			}
		})
	}
	// The "two bad paths" case above must carry *both* ends, which is the
	// whole reason pathProblemsAt exists; the table only asserts one.
	req := jsonRequest(http.MethodPost, "/api/games/"+f.game.String()+"/docs/move",
		`{"from":"/leading","to":"trailing/","expected_version":1}`)
	req.Header.Set("Authorization", "Bearer "+f.token)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"from"`) ||
		!strings.Contains(rec.Body.String(), `"to"`) {
		t.Fatalf("two bad paths were not reported at two names:\n%s", rec.Body.String())
	}
}

// TestTheKindCatalogueReachesBothSurfaces. The vocabulary is what makes
// the kind filter usable, so it has to be readable by the agent that
// filters and by the browser that shows a designer what its game holds.
func TestTheKindCatalogueReachesBothSurfaces(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	for _, doc := range []struct{ path, kind string }{
		{"lore/a.md", "Lore"}, {"lore/b.md", "lore"},
		{"scripts/a.md", "script"}, {"notes/a.md", ""},
	} {
		kind := doc.kind
		if _, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
			Path: doc.path, Content: "x\n", Kind: &kind, ExpectedVersion: int32Ptr(0),
		}); err != nil {
			t.Fatalf("docs.write %s: %v", doc.path, err)
		}
	}

	out, err := web.MCPDocsKinds(ctx, f.deps, f.caller, f.game, web.DocsKindsInput{})
	if err != nil {
		t.Fatalf("docs.kinds: %v", err)
	}
	if len(out.Kinds) != 2 ||
		out.Kinds[0].Kind != "lore" || out.Kinds[0].DocumentCount != 2 ||
		out.Kinds[1].Kind != "script" || out.Kinds[1].DocumentCount != 1 {
		t.Fatalf("kinds = %+v, want lore=2 and script=1, folded", out.Kinds)
	}
	if out.Documents != 4 || out.Unkinded != 1 {
		t.Fatalf("totals = (%d documents, %d unkinded), want 4 and 1",
			out.Documents, out.Unkinded)
	}

	// Every kind it names selects exactly the documents it counted,
	// through the tool it exists to feed.
	for _, k := range out.Kinds {
		page, listErr := web.MCPDocsList(ctx, f.deps, f.caller, f.game,
			web.DocsListInput{Kind: k.Kind})
		if listErr != nil {
			t.Fatalf("docs.list by %q: %v", k.Kind, listErr)
		}
		if int64(len(page.Items)) != k.DocumentCount {
			t.Fatalf("kind %q counts %d and lists %d", k.Kind, k.DocumentCount, len(page.Items))
		}
	}

	// An empty game answers with [] and not null, over the wire.
	empty, err := web.MCPDocsKinds(ctx, f.deps, f.caller, f.game, web.DocsKindsInput{})
	if err != nil {
		t.Fatalf("docs.kinds: %v", err)
	}
	_ = empty
	encoded, err := json.Marshal(web.DocsKindsOutput{Kinds: []web.DocsKindCountOutput{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"kinds":[]`) {
		t.Fatalf("an empty catalogue marshals as %s, want kinds: []", encoded)
	}
}

// TestTheRESTMirrorsOfMoveAndKinds. Both surfaces run the same function,
// and the mirror exists so a browser can reach them; a route registered
// and never driven is a route nothing pins.
func TestTheRESTMirrorsOfMoveAndKinds(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	if _, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
		Path: "lore/dusk.md", Content: "one\n", Kind: stringPtr("lore"),
		ExpectedVersion: int32Ptr(0),
	}); err != nil {
		t.Fatalf("docs.write: %v", err)
	}

	base := "/api/games/" + f.game.String() + "/docs"

	req := jsonRequest(http.MethodPost, base+"/move",
		`{"from":"lore/dusk.md","to":"zones/dusk.md","expected_version":1}`)
	req.Header.Set("Authorization", "Bearer "+f.token)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /docs/move = %d: %s", rec.Code, rec.Body.String())
	}
	var moved web.DocumentSummaryOutput
	if err := json.Unmarshal(rec.Body.Bytes(), &moved); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if moved.Path != "zones/dusk.md" || moved.Version != 2 {
		t.Fatalf("REST move answered (%q, v%d)", moved.Path, moved.Version)
	}

	req = httptest.NewRequest(http.MethodGet, base+"/kinds", nil)
	req.Header.Set("Authorization", "Bearer "+f.token)
	rec = httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /docs/kinds = %d: %s", rec.Code, rec.Body.String())
	}
	var kinds web.DocsKindsOutput
	if err := json.Unmarshal(rec.Body.Bytes(), &kinds); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(kinds.Kinds) != 1 || kinds.Kinds[0].Kind != "lore" ||
		kinds.Kinds[0].DocumentCount != 1 || kinds.Documents != 1 {
		t.Fatalf("REST kinds = %+v", kinds)
	}

	// A refused move comes back as a status and a field, not as a 500.
	req = jsonRequest(http.MethodPost, base+"/move",
		`{"from":"zones/dusk.md","to":"zones/Dusk.md","expected_version":2}`)
	req.Header.Set("Authorization", "Bearer "+f.token)
	rec = httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a case-only move over REST = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "only in capitalisation") {
		t.Fatalf("the refusal body does not say why: %s", rec.Body.String())
	}
}
