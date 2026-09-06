package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/views"
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
		{"contention", &pgconn.PgError{Code: "40001", Message: "could not serialize access"}, http.StatusServiceUnavailable, errCodeRetryable},
		// A deadlock, wrapped the way every write path in
		// internal/metamodel wraps a database failure — `errors.As` is
		// what has to see through that, and a bare *pgconn.PgError would
		// pass this row without ever exercising it. 40P01 earns its own
		// row rather than riding on 40001's: it is the SQLSTATE two
		// writers taking two locks in opposite orders actually produce,
		// it was the one measured against RemoveEntityType(cascade), and
		// the MCP side has pinned it since Task 7
		// (TestMCPErrorForReportsContentionAsRetryable) while this side
		// pinned only its neighbour. Reported as internal_error it tells
		// a caller to give up on a call a retry would land.
		{"a deadlock", fmt.Errorf("delete entity type: %w",
			&pgconn.PgError{Code: "40P01", Message: "deadlock detected"}),
			http.StatusServiceUnavailable, errCodeRetryable},
		{"a refusal this layer made", NewMCPError(errCodeInvalidInput, "cursor is not a cursor"), http.StatusBadRequest, errCodeInvalidInput},
		// The markdown domain's two own error types. Neither is caught
		// by an arm above it: ConflictError is a different Go type from
		// VersionConflictError, and MissingError reaches the not_found
		// arm only because its Is method answers for that sentinel.
		{"a stale document version", &markdown.ConflictError{Current: 3}, http.StatusConflict, errCodeVersionConflict},
		{"no document there", &markdown.MissingError{Path: "path", Message: "no such document"}, http.StatusNotFound, errCodeNotFound},
		// The views domain's four, so the twin claim covers the codes
		// this task added rather than only the ones that were there when
		// it was written.
		{"a query that does not compile", views.ErrQueryInvalid, http.StatusBadRequest, errCodeQueryInvalid},
		{"a renderer that cannot draw it", views.ErrRendererRequirements, http.StatusUnprocessableEntity, errCodeRendererRequirements},
		{"a limit above its cap", views.ErrLimitExceeded, http.StatusBadRequest, errCodeLimitExceeded},
		{"a view the game moved under", views.ErrQueryStale, http.StatusConflict, errCodeQueryStale},
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

// codeOfResult pulls the wire code out of an MCP error result. Every
// failed tool call is one JSON object with a stable "error" member
// (mcpErrorResult), and this is the only thing an agent branches on.
func codeOfResult(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if !result.IsError {
		t.Fatal("the result is not an error result")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] = %T, want *mcp.TextContent", result.Content[0])
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(text.Text), &body); err != nil {
		t.Fatalf("decode %q: %v", text.Text, err)
	}
	return body.Error
}

// detailsOfResult pulls the details object out of an MCP error result,
// or nil when it carried none. "there were no field problems" and "this
// kind of error has no field problems" are different statements
// (fieldDetails says so), and a test that could not tell them apart
// would pass over a details object that had quietly stopped being sent.
func detailsOfResult(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] = %T, want *mcp.TextContent", result.Content[0])
	}
	var body struct {
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal([]byte(text.Text), &body); err != nil {
		t.Fatalf("decode %q: %v", text.Text, err)
	}
	return body.Details
}

// TestEveryViewsSentinelHasAWireCode iterates views.Sentinels() rather
// than repeating a list, so a fifth sentinel added there cannot be
// forgotten on this surface: a code this function does not map falls to
// mcpErrorFor's default arm and reaches an agent as internal_error,
// which means "give up" for a failure the caller could have fixed.
//
// It asserts the code each one maps to as well as that it is not
// internal_error, because "not internal_error" is satisfied by mapping
// all four onto one code, which would lose the distinction the four
// exist for.
func TestEveryViewsSentinelHasAWireCode(t *testing.T) {
	want := map[string]string{
		views.CodeQueryInvalid:         errCodeQueryInvalid,
		views.CodeRendererRequirements: errCodeRendererRequirements,
		views.CodeLimitExceeded:        errCodeLimitExceeded,
		views.CodeQueryStale:           errCodeQueryStale,
	}
	sentinels := views.Sentinels()
	if len(sentinels) == 0 {
		t.Fatal("views.Sentinels() is empty; this test would pass vacuously")
	}
	for _, sentinel := range sentinels {
		result := mcpErrorFor(context.Background(), "views.run", Caller{}, sentinel)
		code := codeOfResult(t, result)
		if code == errCodeInternal {
			t.Errorf("%v maps to internal_error: a caller-fixable failure must never do that", sentinel)
			continue
		}
		expected, named := want[sentinel.Error()]
		if !named {
			t.Errorf("%v is a sentinel this test does not name: add it here and to "+
				"mcpErrorFor and writeDomainError, or it reaches an agent as %q by luck",
				sentinel, code)
			continue
		}
		if code != expected {
			t.Errorf("%v maps to %q, want %q", sentinel, code, expected)
		}
	}
}

// TestEveryViewsSentinelHasARESTStatus is the REST twin, and it checks
// the one thing the MCP side has nothing to say about: the status. The
// codes are compared by TestWriteDomainErrorIsTheRESTTwinOfMCPErrorFor,
// whose table names all four.
func TestEveryViewsSentinelHasARESTStatus(t *testing.T) {
	srv := NewServer(stubOptions("test"))
	want := map[string]int{
		views.CodeQueryInvalid:         http.StatusBadRequest,
		views.CodeRendererRequirements: http.StatusUnprocessableEntity,
		views.CodeLimitExceeded:        http.StatusBadRequest,
		views.CodeQueryStale:           http.StatusConflict,
	}
	sentinels := views.Sentinels()
	if len(sentinels) == 0 {
		t.Fatal("views.Sentinels() is empty; this test would pass vacuously")
	}
	for _, sentinel := range sentinels {
		rec := httptest.NewRecorder()
		srv.writeDomainError(rec, httptest.NewRequest(http.MethodPost, "/api/games/x/views/run", nil), sentinel)
		expected, named := want[sentinel.Error()]
		if !named {
			t.Errorf("%v is a sentinel this test does not name; it answered %d", sentinel, rec.Code)
			continue
		}
		if rec.Code != expected {
			t.Errorf("%v answered %d, want %d: %s", sentinel, rec.Code, expected, rec.Body.String())
		}
		if rec.Code == http.StatusInternalServerError {
			t.Errorf("%v is a 500: a caller-fixable failure must never be one", sentinel)
		}
	}
}

// TestAQueryRefusalPublishesItsPointersOnBothSurfaces is what makes the
// pointer worth having. `/traverse/0/via/0` is the whole argument for
// addressing a query document by JSON pointer, and it reaches a caller
// only if fieldDetails reads *views.QueryError — which it did not until
// this task, because every other error it knew carried a Go field name.
func TestAQueryRefusalPublishesItsPointersOnBothSurfaces(t *testing.T) {
	srv := NewServer(stubOptions("test"))
	err := &views.QueryError{Code: views.CodeQueryInvalid, Fields: []metamodel.FieldError{
		{Path: "/traverse/0/via/0", Message: `no relation type "avilable_to" in this game`},
	}}

	result := mcpErrorFor(context.Background(), "views.validate", Caller{}, err)
	details := detailsOfResult(t, result)
	if details == nil {
		t.Fatal("the MCP refusal carried no details at all")
	}
	fields, ok := details["fields"].([]any)
	if !ok || len(fields) != 1 {
		t.Fatalf("details.fields = %v, want the one problem", details["fields"])
	}
	first, _ := fields[0].(map[string]any)
	if first["path"] != "/traverse/0/via/0" {
		t.Fatalf("path = %v, want the pointer unchanged", first["path"])
	}

	rec := httptest.NewRecorder()
	srv.writeDomainError(rec, httptest.NewRequest(http.MethodPost, "/api/games/x/views/run", nil), err)
	var body struct {
		Details struct {
			Fields []map[string]string `json:"fields"`
		} `json:"details"`
	}
	if jsonErr := json.Unmarshal(rec.Body.Bytes(), &body); jsonErr != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), jsonErr)
	}
	if len(body.Details.Fields) != 1 || body.Details.Fields[0]["path"] != "/traverse/0/via/0" {
		t.Fatalf("REST details.fields = %v, want the same pointer", body.Details.Fields)
	}
}

// TestAStaleViewCarriesItsDiagnosticsBesideItsFields pins the second
// list. details.fields is what an agent acts on; details.stale is the
// machine-readable half a UI bands over a picture — a code, a pointer
// and the was/now pair — and the two are different readers rather than
// two spellings of one thing (staleDetails argues it).
func TestAStaleViewCarriesItsDiagnosticsBesideItsFields(t *testing.T) {
	srv := NewServer(stubOptions("test"))
	err := &views.QueryError{
		Code: views.CodeQueryStale,
		Fields: []metamodel.FieldError{
			{Path: "/traverse/0/via/0", Message: `"available_to" is now spelled "usable_by"`},
		},
		Stale: []views.Diagnostic{
			{Code: "relation_type_renamed", Pointer: "/traverse/0/via/0",
				Was: "available_to", Now: "usable_by"},
			{Code: "entity_type_missing", Pointer: "/from/0/type", Was: "zone"},
		},
	}

	details := detailsOfResult(t, mcpErrorFor(context.Background(), "views.run", Caller{}, err))
	if details == nil {
		t.Fatal("a stale refusal carried no details at all")
	}
	if _, ok := details["fields"]; !ok {
		t.Error("details carried no fields: the addressed half is what an agent acts on")
	}
	stale, ok := details["stale"].([]any)
	if !ok || len(stale) != 2 {
		t.Fatalf("details.stale = %v, want the two diagnostics", details["stale"])
	}
	renamed, _ := stale[0].(map[string]any)
	if renamed["code"] != "relation_type_renamed" || renamed["pointer"] != "/traverse/0/via/0" ||
		renamed["was"] != "available_to" || renamed["now"] != "usable_by" {
		t.Errorf("the rename diagnostic = %v, want its code, pointer and both spellings", renamed)
	}
	// A type that is gone carries `was` and no `now`, because the game
	// says nothing. Absent rather than empty: the two are different
	// statements and only the first is true.
	missing, _ := stale[1].(map[string]any)
	if _, present := missing["now"]; present {
		t.Errorf("a missing type carried now = %v; the game says nothing", missing["now"])
	}

	rec := httptest.NewRecorder()
	srv.writeDomainError(rec, httptest.NewRequest(http.MethodPost, "/api/games/x/views/run", nil), err)
	if rec.Code != http.StatusConflict {
		t.Errorf("REST status = %d, want 409", rec.Code)
	}
	var body struct {
		Details struct {
			Stale []map[string]string `json:"stale"`
		} `json:"details"`
	}
	if jsonErr := json.Unmarshal(rec.Body.Bytes(), &body); jsonErr != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), jsonErr)
	}
	if len(body.Details.Stale) != 2 {
		t.Fatalf("REST details.stale = %v, want the same two", body.Details.Stale)
	}
}

// TestATimedOutViewKeepsItsAdviceOnBothSurfaces is the arm ordering,
// asserted rather than commented.
//
// The error is built the way the domain builds it — wrapping the
// *pgconn.PgError carrying 57014 — because that is the whole difficulty:
// metamodel.IsRetryable admits it, so the retryable arm would catch it
// too, with the right code and with the message replaced by a generic
// contention sentence. An arm placed after that one is unreachable, and
// a test that built a TimeoutError wrapping nothing would pass under
// either order.
//
// The code stays `retryable` on purpose (internal/views/errors.go argues
// why there is no fifth wire code); what this asserts is that the
// message survives, because the elapsed budget and the three bounds to
// lower are the only thing that helps once resending has stopped
// working.
func TestATimedOutViewKeepsItsAdviceOnBothSurfaces(t *testing.T) {
	srv := NewServer(stubOptions("test"))
	timeout := &views.TimeoutError{
		Budget: 5 * time.Second,
		Limits: views.ResolvedLimits{MaxDepth: 4, MaxNodes: 2000, MaxEdges: 4000},
		Err:    &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"},
	}
	// The control that makes the ordering the finding: this error really
	// does reach the arm that would swallow it.
	if !metamodel.IsRetryable(timeout) {
		t.Fatal("the fixture is not retryable, so the arm below it could not have caught it " +
			"and this test proves nothing about the order")
	}

	result := mcpErrorFor(context.Background(), "views.run", Caller{}, timeout)
	if code := codeOfResult(t, result); code != errCodeRetryable {
		t.Errorf("MCP code = %q, want %q", code, errCodeRetryable)
	}
	text, _ := result.Content[0].(*mcp.TextContent)
	var agent struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(text.Text), &agent); err != nil {
		t.Fatalf("decode %q: %v", text.Text, err)
	}
	for _, want := range []string{"max_depth", "max_nodes", "max_edges", "5s"} {
		if !strings.Contains(agent.Message, want) {
			t.Errorf("the MCP message does not name %s: %q", want, agent.Message)
		}
	}

	rec := httptest.NewRecorder()
	srv.writeDomainError(rec, httptest.NewRequest(http.MethodPost, "/api/games/x/views/run", nil), timeout)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("REST status = %d, want 503", rec.Code)
	}
	var browser struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &browser); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if browser.Error != errCodeRetryable {
		t.Errorf("REST code = %q, want %q", browser.Error, errCodeRetryable)
	}
	if browser.Message != agent.Message {
		t.Errorf("a designer was told %q and an agent %q for the same failure",
			browser.Message, agent.Message)
	}
}
