package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/identity"
)

// The seed that is gone, asserted on both wires.
//
// `views.layout_seed` was a column with no reader on either side: the
// server validated it, stored it and handed it back, and the client's
// layout engine is deterministic by construction — it sorts by entity
// address before it lays anything out — so there was never a picture for
// a seed to change. Migration 0012 dropped it and carries the argument,
// including what a future stochastic engine would actually have to seed.
//
// What this file is for is the half a migration cannot state. A column
// can go and the *wire* can keep taking the argument: a Go struct field
// left behind, a property left in a hand-written schema, a sentence left
// in a tool description an agent reads to learn the vocabulary. Each of
// those is the same defect the removal was for — a knob that answers
// nothing — moved one layer up, and the last one is the worst, because a
// description is the only place an agent looks.

// TestNoSurfaceAcceptsALayoutSeed drives views.upsert with a
// `layout_seed` member on **both** surfaces and requires each to refuse
// it rather than drop it.
//
// Both, in one test, on purpose. The recurring defect of this
// sub-project is a rule carried down one path and not the other, and two
// tests in two files is how that happens: the MCP surface refuses this
// for free (the SDK infers `additionalProperties: false` from the Go
// struct, so deleting the field was the whole fix) while the REST
// surface had to be told, since encoding/json's default is to discard
// what it does not recognise. A silently discarded argument is the same
// lie one layer up — the caller is told its write succeeded *as sent*,
// and it was not.
func TestNoSurfaceAcceptsALayoutSeed(t *testing.T) {
	f := newViewsRESTFixture(t)
	ctx := context.Background()

	body := map[string]any{
		"key":              "seeded",
		"name":             "Seeded",
		"query":            json.RawMessage(questsQuery),
		"renderer":         "graph",
		"layout_mode":      "manual",
		"layout_seed":      7,
		"expected_version": 0,
	}

	t.Run("REST", func(t *testing.T) {
		rec := f.call(t, f.cookie, http.MethodPost, f.path("/views"), body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST with a layout_seed = %d: %s\nwant 400: an argument this "+
				"surface does not have must be answered, not dropped",
				rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "layout_seed") {
			t.Fatalf("the refusal does not name the member that caused it: %s",
				rec.Body.String())
		}
		// And nothing was written: a refusal that stored the row anyway
		// would be the same silence with a louder status code.
		if _, err := f.views.ViewByKey(ctx, f.game, "seeded"); err == nil {
			t.Fatal("the refused upsert stored a view anyway")
		}
	})

	t.Run("MCP", func(t *testing.T) {
		httpSrv := httptest.NewServer(f.srv)
		defer httpSrv.Close()
		token, _, err := f.ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
			ProjectID: f.game, UserID: f.ownerID, Label: "agent",
		})
		if err != nil {
			t.Fatalf("CreateAPIToken: %v", err)
		}
		session := connectMCP(t, httpSrv.URL, token)

		args := map[string]any{}
		for k, v := range body {
			args[k] = v
		}
		args["key"] = "seeded_mcp"
		// The query goes over as the decoded document it is: a
		// json.RawMessage would reach the SDK's argument encoder as a
		// []byte and arrive as an array of numbers.
		args["query"] = map[string]any{
			"v": 1, "from": []any{map[string]any{"type": "quest", "as": "q"}},
		}

		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "views.upsert", Arguments: args,
		})
		if err != nil {
			t.Fatalf("CallTool(views.upsert): %v", err)
		}
		if !result.IsError {
			t.Fatal("views.upsert accepted a layout_seed: the tool's input schema " +
				"must close the struct to members it does not have")
		}
		if _, err := f.views.ViewByKey(ctx, f.game, "seeded_mcp"); err == nil {
			t.Fatal("the refused tool call stored a view anyway")
		}
	})
}

// TestTheViewToolDescriptionNamesNoSeed reads views.upsert's description
// off the real transport, which is where an agent learns what arguments
// exist.
//
// A description is not documentation about the tool; for an agent it *is*
// the tool. A column dropped from the schema and a member dropped from
// the struct, with the prose still saying "layout_seed is for a client's
// own deterministic layout", leaves every agent sending an argument that
// is now refused — the removal made worse than not doing it, because the
// only reader that ever had a stake was told the opposite.
//
// The schema is asserted too, in the same place: `layout_seed` must not
// be a property of the input or of the output, so an agent inspecting
// the tool rather than reading its prose learns the same thing.
func TestTheViewToolDescriptionNamesNoSeed(t *testing.T) {
	f := newViewsRESTFixture(t)
	ctx := context.Background()
	httpSrv := httptest.NewServer(f.srv)
	defer httpSrv.Close()
	token, _, err := f.ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: f.game, UserID: f.ownerID, Label: "agent",
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	session := connectMCP(t, httpSrv.URL, token)

	var found int
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("list tools: %v", err)
		}
		if !strings.HasPrefix(tool.Name, "views.") {
			continue
		}
		found++
		if strings.Contains(tool.Description, "layout_seed") {
			t.Errorf("%s's description still names layout_seed", tool.Name)
		}
		if schemaHasProperty(t, tool.InputSchema, "layout_seed") {
			t.Errorf("%s's input schema still has a layout_seed property", tool.Name)
		}
		if schemaHasProperty(t, tool.OutputSchema, "layout_seed") {
			t.Errorf("%s's output schema still has a layout_seed property", tool.Name)
		}
	}
	// Without this the loop above passes on a server that registered no
	// views tools at all, which is the shape every "assert nothing
	// matches" test fails in.
	if found == 0 {
		t.Fatal("no views.* tool was listed: this test asserted nothing")
	}
}

// schemaHasProperty reports whether a schema as the client received it
// -- an untyped value decoded from the wire, which is the only shape a
// real agent ever sees -- declares the named property.
func schemaHasProperty(t *testing.T, schema any, name string) bool {
	t.Helper()
	if schema == nil {
		return false
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	var decoded struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode schema %s: %v", raw, err)
	}
	_, ok := decoded.Properties[name]
	return ok
}

// TestNoSourceFileNamesALayoutSeed is the grep the task asked for, run as
// a test rather than by hand.
//
// A removal is not finished when the code compiles; it is finished when
// the identifier cannot come back without something going red. The
// survivors are named here one by one, and each is history rather than a
// mechanism: migration 0008 declares the column because that is what it
// did on the day it ran, migration 0012 drops it and carries the
// argument, the design documents under docs/ record a decision that was
// taken and then reversed, and the two test names in this file and in
// internal/views spell the thing they are about.
func TestNoSourceFileNamesALayoutSeed(t *testing.T) {
	root := seedGrepRoot(t)
	var offenders []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch rel {
			case ".git", "docs", "bin", "develop", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		switch rel {
		case "internal/db/migrations/0008_views.sql",
			"internal/db/migrations/0012_drop_layout_seed.sql",
			// This file is the check itself: it has to spell what it
			// is looking for, and a check that could not name its own
			// subject would have to look for it obliquely.
			"internal/web/views_no_seed_test.go":
			return nil
		}
		switch filepath.Ext(rel) {
		case ".go", ".sql", ".js", ".mjs", ".html", ".css", ".json", ".md":
		default:
			return nil
		}
		content, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		for i, line := range strings.Split(string(content), "\n") {
			// The two mandated test names contain the identifier and are
			// the only permitted spelling outside history, so they come
			// out of the line before it is judged.
			stripped := strings.ReplaceAll(line, "NoSurfaceAcceptsALayoutSeed", "")
			stripped = strings.ReplaceAll(stripped, "NoSourceFileNamesALayoutSeed", "")
			if !strings.Contains(stripped, "layout_seed") &&
				!strings.Contains(stripped, "LayoutSeed") {
				continue
			}
			offenders = append(offenders,
				rel+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(offenders) > 0 {
		t.Fatalf("the seed came back in %d place(s):\n%s",
			len(offenders), strings.Join(offenders, "\n"))
	}
}

// seedGrepRoot walks up from this file to the directory holding go.mod,
// rather than trusting `go test`'s working directory.
func seedGrepRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above " + file)
		}
		dir = parent
	}
}

// TestARequestBodyWithAnUnknownMemberIsRefusedByName pins the refusal
// Task 17 had to add to make the sentence above true on the REST side.
//
// It lives in this file rather than beside the other REST view tests
// because it is only here that it has a reason: nothing had asked the
// browser surface to be strict until a member was removed from it, and
// the removal is what turned "encoding/json drops what it does not know"
// from a default nobody had looked at into a way of answering a caller
// with a success it did not get.
//
// The member is *named*. A 400 saying "malformed JSON body" over a body
// that parsed cleanly sends a caller looking for a syntax error it does
// not have, and the misspelling that caused this — one letter, in a
// field that then took its default — is exactly the mistake a name
// resolves and a code does not. The name comes out of a message
// encoding/json gives no type to, so this is also the test that would
// see that parse break.
func TestARequestBodyWithAnUnknownMemberIsRefusedByName(t *testing.T) {
	f := newViewsRESTFixture(t)

	rec := f.call(t, f.cookie, http.MethodPost, f.path("/views"), map[string]any{
		"key": "typo", "name": "Typo", "query": json.RawMessage(questsQuery),
		"renderer": "graph", "layotu_mode": "manual", "expected_version": 0,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST with a misspelled member = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error   string `json:"error"`
		Details struct {
			Fields []struct {
				Path    string `json:"path"`
				Message string `json:"message"`
			} `json:"fields"`
		} `json:"details"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode the refusal %s: %v", rec.Body.String(), err)
	}
	if body.Error != "bad_request" {
		t.Errorf("error = %q, want bad_request", body.Error)
	}
	if len(body.Details.Fields) != 1 || body.Details.Fields[0].Path != "layotu_mode" {
		t.Fatalf("the refusal does not name the misspelled member: %s", rec.Body.String())
	}
	// The nearby correct spelling is not in the answer, deliberately:
	// this surface says what it did not recognise, not what it guesses
	// was meant, because a guess that is wrong is worse than no guess.
	if _, err := f.views.ViewByKey(context.Background(), f.game, "typo"); err == nil {
		t.Fatal("the refused upsert stored a view anyway")
	}
}
