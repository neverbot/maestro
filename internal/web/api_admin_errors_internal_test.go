package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestWriteUnmappedErrorClassifiesContention pins the shared tail every
// REST handler in this package ends with.
//
// **This test constructs its errors rather than provoking them, and that
// is all it claims.** It proves the mapping — that each of the four
// SQLSTATEs metamodel.IsRetryable admits leaves here as 503 `retryable`
// and that anything else is still a 500 — and it proves nothing about
// whether a real request can reach it.
// TestAGameDeletionDeadlockedByAContentWriteIsRetryable
// (api_admin_contention_test.go) is the one that answers reachability:
// it stages a genuine deadlock between a cascading game deletion and a
// concurrent content write, and reads the status off the real handler.
func TestWriteUnmappedErrorClassifiesContention(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"a serialization failure", &pgconn.PgError{Code: "40001", Message: "could not serialize access"},
			http.StatusServiceUnavailable, errCodeRetryable},
		// Wrapped the way projects.Delete wraps its own failure
		// ("delete project: %w"), because errors.As seeing through that
		// wrap is the whole mechanism — a bare *pgconn.PgError would pass
		// this row without exercising it. 40P01 is the SQLSTATE a game
		// deletion cascading over concurrent content writes actually
		// produces; it was measured at 7 to 9 deadlocked deletions per
		// 15 seconds against eight writers.
		{"a deadlock, wrapped", fmt.Errorf("delete project: %w",
			&pgconn.PgError{Code: "40P01", Message: "deadlock detected"}),
			http.StatusServiceUnavailable, errCodeRetryable},
		{"a lock timeout", &pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"},
			http.StatusServiceUnavailable, errCodeRetryable},
		{"a cancelled statement", &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"},
			http.StatusServiceUnavailable, errCodeRetryable},
		// The default arm still exists and still means what it meant: a
		// fault nobody planned for, reported as one.
		{"anything else", errors.New("connection reset"), http.StatusInternalServerError, errCodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeUnmappedError(rec, httptest.NewRequest(http.MethodDelete, "/api/games/azeroth", nil),
				tc.err, "delete game failed", "could not delete the game", "project_id", "x")
			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			var body struct {
				Error   string `json:"error"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode %q: %v", rec.Body.String(), err)
			}
			if body.Error != tc.code {
				t.Errorf("code = %q, want %q", body.Error, tc.code)
			}
			// The caller-facing sentence is the surface's one sentence,
			// not a second copy of it: writeDomainError's tail and this
			// one answer identically because they are the same function.
			if tc.code == errCodeRetryable && body.Message != retryableAdvice {
				t.Errorf("message = %q, want the shared advice", body.Message)
			}
		})
	}
}

// gameAdministrationFiles are the six surfaces the defect was filed
// against, plus the admission path in front of them.
var gameAdministrationFiles = []string{
	"api_projects.go", "api_tokens.go", "api_invites.go",
	"api_auth.go", "api_admin.go", "api_password.go", "auth.go",
}

// TestNoGameAdministrationHandlerAnswersAnUnclassifiedServerFault is the
// guard against this fix being carried one step and not the next — the
// failure this repository repeats most often, and the exact shape of the
// original defect: the retryable arm existed, correctly, in one place,
// and half the REST surface never got it.
//
// Every 500 written by these files must either sit behind an
// IsRetryable classification (writeUnmappedError itself, and
// handleRoot's own HTML-shaped version of it) or be one of the two call
// sites that deliberately refuse the sweep, which say so in the words
// this test looks for: an IssueSession failure *after* the mutation the
// request asked for already committed, where "send the same request
// again" is advice that cannot work.
func TestNoGameAdministrationHandlerAnswersAnUnclassifiedServerFault(t *testing.T) {
	for _, name := range gameAdministrationFiles {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		lines := strings.Split(string(src), "\n")
		for i, line := range lines {
			if !strings.Contains(line, "http.StatusInternalServerError") {
				continue
			}
			from := max(i-15, 0)
			context := strings.Join(lines[from:i], "\n")
			// "IsRetryable(" with the parenthesis, not the bare word:
			// a nearby comment naming
			// TestAGameDeletionDeadlockedByAContentWriteIsRetryable
			// contains that word, and matching it let a mutation of
			// handleDeleteGame back to an unconditional 500 pass this
			// test unnoticed.
			if strings.Contains(context, "IsRetryable(") ||
				strings.Contains(context, "Deliberately not writeUnmappedError") {
				continue
			}
			t.Errorf("%s:%d answers a server fault without classifying contention first: %s\n"+
				"use writeUnmappedError (auth.go), or say why the sweep stops here",
				name, i+1, strings.TrimSpace(line))
		}
	}
}
