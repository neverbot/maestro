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

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/identity"
)

// The seed that is gone, asserted on both wires.
func TestNoSurfaceAcceptsALayoutSeed(t *testing.T) {
	t.Parallel()
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
		assert.Must(t, rec.Code == http.StatusBadRequest, "POST with a layout_seed = %d: %s\nwant 400: an argument this "+
			"surface does not have must be answered, not dropped",
			rec.Code, rec.Body.String())
		assert.Must(t, strings.Contains(rec.Body.String(), "layout_seed"), "the refusal does not name the member that caused it: %s",
			rec.Body.String())
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
		assert.Must(t, err == nil, "CreateAPIToken: %v", err)
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
		assert.Must(t, err == nil, "CallTool(views.upsert): %v", err)
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
func TestTheViewToolDescriptionNamesNoSeed(t *testing.T) {
	t.Parallel()
	f := newViewsRESTFixture(t)
	ctx := context.Background()
	httpSrv := httptest.NewServer(f.srv)
	defer httpSrv.Close()
	token, _, err := f.ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: f.game, UserID: f.ownerID, Label: "agent",
	})
	assert.Must(t, err == nil, "CreateAPIToken: %v", err)
	session := connectMCP(t, httpSrv.URL, token)

	var found int
	for tool, err := range session.Tools(ctx, nil) {
		assert.Must(t, err == nil, "list tools: %v", err)
		if !strings.HasPrefix(tool.Name, "views.") {
			continue
		}
		found++
		assert.Should(t, !strings.Contains(tool.Description, "layout_seed"), "%s's description still names layout_seed", tool.Name)
		assert.Should(t, !schemaHasProperty(t, tool.InputSchema, "layout_seed"), "%s's input schema still has a layout_seed property", tool.Name)
		assert.Should(t, !schemaHasProperty(t, tool.OutputSchema, "layout_seed"), "%s's output schema still has a layout_seed property", tool.Name)
	}
	// Without this the loop above passes on a server that registered no
	// views tools at all, which is the shape every "assert nothing
	// matches" test fails in.
	assert.Must(t, found != 0, "no views.* tool was listed: this test asserted nothing")
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
	assert.Must(t, err == nil, "marshal schema: %v", err)
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
func TestNoSourceFileNamesALayoutSeed(t *testing.T) {
	t.Parallel()
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
			// `.superpowers` holds the specs and plans, which argue
			// about the dormant column by name and are not shipped;
			// `docs` is the published documentation; the rest is
			// build output and agent scratch.
			case ".git", ".superpowers", ".impeccable", ".scratch",
				"docs", "bin", "node_modules":
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
	assert.Must(t, err == nil, "walk %s: %v", root, err)
	assert.Must(t, len(offenders) <= 0, "the seed came back in %d place(s):\n%s",
		len(offenders), strings.Join(offenders, "\n"))
}

// seedGrepRoot walks up from this file to the directory holding go.mod,
// rather than trusting `go test`'s working directory.
func seedGrepRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	assert.Must(t, ok, "runtime.Caller failed")
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
func TestARequestBodyWithAnUnknownMemberIsRefusedByName(t *testing.T) {
	t.Parallel()
	f := newViewsRESTFixture(t)

	rec := f.call(t, f.cookie, http.MethodPost, f.path("/views"), map[string]any{
		"key": "typo", "name": "Typo", "query": json.RawMessage(questsQuery),
		"renderer": "graph", "layotu_mode": "manual", "expected_version": 0,
	})
	assert.Must(t, rec.Code == http.StatusBadRequest, "POST with a misspelled member = %d: %s", rec.Code, rec.Body.String())
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
	assert.Should(t, body.Error == "bad_request", "error = %q, want bad_request", body.Error)
	assert.Must(t, len(body.Details.Fields) == 1 && body.Details.Fields[0].Path == "layotu_mode", "the refusal does not name the misspelled member: %s", rec.Body.String())
	// The nearby correct spelling is not in the answer, deliberately:
	// this surface says what it did not recognise, not what it guesses
	// was meant, because a guess that is wrong is worse than no guess.
	if _, err := f.views.ViewByKey(context.Background(), f.game, "typo"); err == nil {
		t.Fatal("the refused upsert stored a view anyway")
	}
}
