package web_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/assert"
)

// **The whole round trip an agent performs on the log, over the real
// transport.** `comments.remove` shipped unusable: its id was declared
// as a `uuid.UUID`, the SDK reflected that `[16]byte` into a schema of
// sixteen numbers, and an agent could satisfy the schema or the server
// and never both. Every test of it called the Go function with a
// uuid.UUID already in hand, so the schema the SDK publishes was never
// in the picture — correct in the module, dead at the call site, which
// is the most repeated defect in this repository.
//
// The id comes from comments.add's own answer here, which is where an
// agent gets it too: a test that builds the id itself would pass on a
// surface no agent can drive.
func TestTheLogIsWrittenReadAndUnwrittenOverTheRealTransport(t *testing.T) {
	t.Parallel()
	f := newMetamodelFixture(t)
	httpSrv := httptest.NewServer(f.srv)
	defer httpSrv.Close()
	ctx := context.Background()
	session := connectMCP(t, httpSrv.URL, f.token)

	callOK(t, session, "types.upsert", map[string]any{
		"key": "quest", "label": "Quest", "label_plural": "Quests",
	})
	callOK(t, session, "entities.upsert", map[string]any{
		"items": []any{map[string]any{"type_key": "quest", "key": "first-steps", "name": "First Steps"}},
	})

	var written struct {
		ID string `json:"id"`
	}
	decodeStructured(t, callOK(t, session, "comments.add", map[string]any{
		"target": map[string]any{"on": "entity", "type_key": "quest", "key": "first-steps"},
		"body":   "Imported from the old engine; the damage formula is a guess.",
	}), &written)
	assert.Must(t, written.ID != "", "comments.add answered without an id: %+v", written)

	var listed struct {
		Items []struct {
			ID   string `json:"id"`
			Body string `json:"body"`
		} `json:"items"`
	}
	decodeStructured(t, callOK(t, session, "comments.list", map[string]any{
		"target": map[string]any{"on": "entity", "type_key": "quest", "key": "first-steps"},
	}), &listed)
	assert.Must(t, len(listed.Items) == 1, "the log holds %d comments, want one", len(listed.Items))
	assert.Must(t, listed.Items[0].ID == written.ID, "comments.list answers id %q, comments.add answered %q",
		listed.Items[0].ID, written.ID)

	// The id exactly as the surface just handed it over, which is the
	// call that could not be made at all.
	var removed struct {
		ID      string `json:"id"`
		Removed bool   `json:"removed"`
	}
	decodeStructured(t, callOK(t, session, "comments.remove", map[string]any{"id": written.ID}), &removed)
	assert.Must(t, removed.Removed && removed.ID == written.ID, "comments.remove answered %+v", removed)

	decodeStructured(t, callOK(t, session, "comments.list", map[string]any{
		"target": map[string]any{"on": "entity", "type_key": "quest", "key": "first-steps"},
	}), &listed)
	assert.Must(t, len(listed.Items) == 0, "the comment survived its removal: %+v", listed.Items)

	// And a malformed id is refused as an argument rather than as a
	// missing row, because the two send a caller to different places.
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "comments.remove", Arguments: map[string]any{"id": "not-a-uuid"},
	})
	assert.Must(t, err == nil, "CallTool(comments.remove): %v", err)
	assert.Must(t, result.IsError, "a malformed id was accepted")
	var wireErr struct {
		Error string `json:"error"`
	}
	decodeToolText(t, result, &wireErr)
	assert.Must(t, wireErr.Error == "invalid_input", "a malformed id reported %q, want invalid_input", wireErr.Error)
}
