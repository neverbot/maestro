package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
)

// TestWriteDomainErrorIsTheRESTTwinOfMCPErrorFor checks the claim
// writeDomainError's own doc comment makes, rather than trusting it: for
// every error either surface can be handed, the two must answer with the
// same code, and the REST side adds only a status.
//
// A review found the claim was not true — there was no
// projects.ErrProjectNotFound arm at all (unreachable through a route,
// since requireProject resolves the game first, but the doc comment said
// otherwise), and the retryable message quietly dropped the second
// sentence Task 7 added, so a designer in a browser got weaker advice
// than an agent got for the same failure. Comparing the two functions
// directly is what stops the next divergence being written down as a
// twin as well.
func TestWriteDomainErrorIsTheRESTTwinOfMCPErrorFor(t *testing.T) {
	srv := NewServer(stubOptions("test"))
	caller := newTokenCaller(uuid.New(), false, uuid.New(), uuid.New())

	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"no such game", projects.ErrProjectNotFound, http.StatusNotFound, errCodeNotFound},
		{"a stale version", &metamodel.VersionConflictError{Current: 7}, http.StatusConflict, errCodeVersionConflict},
		{"a broken declaration", metamodel.ErrInvalidSchema, http.StatusUnprocessableEntity, errCodeInvalidSchema},
		{"a value that does not fit", metamodel.ErrSchemaViolation, http.StatusUnprocessableEntity, errCodeSchemaViolation},
		{"a bad argument", metamodel.ErrInvalidInput, http.StatusBadRequest, errCodeInvalidInput},
		{"an endpoint of the wrong type", metamodel.ErrEndpointTypeMismatch, http.StatusUnprocessableEntity, errCodeEndpointTypeMismatch},
		{"a type still in use", metamodel.ErrInUse, http.StatusConflict, errCodeInUse},
		{"nothing there", metamodel.ErrNotFound, http.StatusNotFound, errCodeNotFound},
		{"contention", &pgconn.PgError{Code: "40001", Message: "deadlock detected"}, http.StatusServiceUnavailable, errCodeRetryable},
		{"a refusal this layer made", NewMCPError(errCodeInvalidInput, "cursor is not a cursor"), http.StatusBadRequest, errCodeInvalidInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.writeDomainError(rec, httptest.NewRequest(http.MethodGet, "/api/games/x/entities", nil), tc.err)

			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			var rest struct {
				Error   string `json:"error"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &rest); err != nil {
				t.Fatalf("decode %q: %v", rec.Body.String(), err)
			}
			if rest.Error != tc.code {
				t.Errorf("code = %q, want %q", rest.Error, tc.code)
			}

			// The same error through the MCP surface, which is what
			// "twin" has to mean: one vocabulary regardless of who asked.
			result := mcpErrorFor(context.Background(), "entities.list", caller, tc.err)
			text, ok := result.Content[0].(*mcp.TextContent)
			if !ok {
				t.Fatalf("content[0] = %T, want *mcp.TextContent", result.Content[0])
			}
			var agent struct {
				Error   string `json:"error"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal([]byte(text.Text), &agent); err != nil {
				t.Fatalf("decode %q: %v", text.Text, err)
			}
			if agent.Error != rest.Error {
				t.Errorf("MCP said %q and REST said %q for the same error", agent.Error, rest.Error)
			}
		})
	}
}

// TestTheRetryableAdviceIsTheSameOnBothSurfaces pins the half of the
// twin a code comparison cannot see. Task 7 added a second sentence to
// the retryable message deliberately — 57014 is also what a
// statement_timeout raises on a call that is simply too expensive, and
// that one fails every time it is resent — and the REST mirror carried
// only the first, telling a designer to keep resending something that
// will never succeed.
func TestTheRetryableAdviceIsTheSameOnBothSurfaces(t *testing.T) {
	srv := NewServer(stubOptions("test"))
	rec := httptest.NewRecorder()
	srv.writeDomainError(rec, httptest.NewRequest(http.MethodGet, "/api/games/x/entities", nil),
		&pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"})

	var body struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if !strings.Contains(body.Message, "send the same request again") {
		t.Errorf("message = %q, want the first sentence", body.Message)
	}
	if !strings.Contains(body.Message, "ask for less") {
		t.Errorf("message = %q, want the second sentence an agent already gets", body.Message)
	}
	// The database's own words stay in the log, on this surface too.
	if strings.Contains(body.Message, "canceling statement") {
		t.Errorf("message = %q leaks the database's own text", body.Message)
	}
}
