package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/web"
)

// TestTheCommentLogReachesBothSurfaces is the call-site half of
// internal/comments' own tests: those prove the behaviour, and this
// proves the three tools and the three routes actually reach it, with
// the argument names an agent and a browser really send.
func TestTheCommentLogReachesBothSurfaces(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	questType(t, f)
	rec := f.as(t, http.MethodPost, "/entities", map[string]any{
		"items": []any{map[string]any{"type_key": "quest", "key": "hogger", "name": "Wanted: Hogger"}},
	})
	assert.Must(t, rec.Code == http.StatusOK, "seed = %d: %s", rec.Code, rec.Body.String())

	// The browser's half: a POST with the target in the body.
	rec = f.as(t, http.MethodPost, "/comments", map[string]any{
		"target": map[string]any{"on": "entity", "type_key": "quest", "key": "hogger"},
		"body":   "Imported from the old engine; the damage formula is a guess.",
	})
	assert.Must(t, rec.Code == http.StatusOK, "add = %d: %s", rec.Code, rec.Body.String())
	var written struct {
		ID        uuid.UUID `json:"id"`
		On        string    `json:"on"`
		Body      string    `json:"body"`
		CreatedAt string    `json:"created_at"`
		Author    string    `json:"author"`
	}
	assert.NoErr(t, json.Unmarshal(rec.Body.Bytes(), &written), "decode the written comment")
	assert.Must(t, written.On == "entity", "on = %q, want entity", written.On)
	assert.Must(t, written.CreatedAt != "", "a log entry with no time on it is half an entry")
	assert.Must(t, written.Author != "", "a log entry with no author on it is half an entry")

	// And the read, with the target in the query string: a read is a read
	// and should not need a body.
	read := f.as(t, http.MethodGet, "/comments?on=entity&type_key=quest&key=hogger", nil)
	assert.Must(t, read.Code == http.StatusOK, "list = %d: %s", read.Code, read.Body.String())
	assert.Must(t, strings.Contains(read.Body.String(), "old engine"), "the log came back without the comment: %s", read.Body.String())

	// The game's whole log is the same route with no target at all.
	whole := f.as(t, http.MethodGet, "/comments", nil)
	assert.Must(t, whole.Code == http.StatusOK, "the game's log = %d: %s", whole.Code, whole.Body.String())
	assert.Must(t, strings.Contains(whole.Body.String(), "old engine"), "the game's log is empty: %s", whole.Body.String())

	// The agent's half, through the tool entry point.
	out, err := web.MCPCommentsList(context.Background(), f.deps(), f.caller(), f.game, web.CommentsListInput{
		Target: &web.CommentTargetInput{On: "entity", TypeKey: "quest", Key: "hogger"},
	})
	assert.NoErr(t, err, "MCPCommentsList")
	assert.Must(t, len(out.Items) == 1, "the tool reads %d entries, want 1", len(out.Items))

	// And the removal, by the id the write answered with.
	gone := f.as(t, http.MethodDelete, "/comments/"+written.ID.String(), nil)
	assert.Must(t, gone.Code == http.StatusOK, "remove = %d: %s", gone.Code, gone.Body.String())
	after := f.as(t, http.MethodGet, "/comments?on=entity&type_key=quest&key=hogger", nil)
	assert.Must(t, !strings.Contains(after.Body.String(), "old engine"), "the comment survived its removal: %s", after.Body.String())
}

// TestACommentOnAThingThisGameDoesNotHaveIsNotFound pins the refusal on
// the wire: without it the log fills with notes about rows nobody can
// reach.
func TestACommentOnAThingThisGameDoesNotHaveIsNotFound(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	questType(t, f)
	rec := f.as(t, http.MethodPost, "/comments", map[string]any{
		"target": map[string]any{"on": "entity", "type_key": "quest", "key": "nobody"},
		"body":   "about nothing",
	})
	assertError(t, rec, http.StatusNotFound, "not_found", "")

	// And an `on` this surface does not have is the caller's own argument.
	rec = f.as(t, http.MethodPost, "/comments", map[string]any{
		"target": map[string]any{"on": "quest", "type_key": "quest", "key": "hogger"},
		"body":   "about nothing",
	})
	assertError(t, rec, http.StatusBadRequest, "invalid_input", "target.on")
}
