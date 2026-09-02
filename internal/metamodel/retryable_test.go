package metamodel_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/neverbot/maestro/internal/metamodel"
)

// TestIsRetryableNamesTheFourContentionStatesAndNothingElse pins Task
// 7's third decision: which database failures an agent is told to resend
// unchanged.
//
// The four admitted here share exactly one property — the identical
// call, resent unchanged, may succeed — and that property, not the
// cause, is what a caller has to act on. Everything else stays
// internal_error, which is the honest report for a fault nobody planned
// for and, deliberately, the report a *caller-fixable* fault must never
// get: those already have their own codes.
func TestIsRetryableNamesTheFourContentionStatesAndNothingElse(t *testing.T) {
	for _, tc := range []struct {
		code string
		what string
		want bool
	}{
		{"40001", "serialization_failure", true},
		{"40P01", "deadlock_detected", true},
		{"55P03", "lock_not_available (lock_timeout)", true},
		{"57014", "query_canceled (statement_timeout)", true},
		{"23503", "foreign_key_violation", false},
		{"23505", "unique_violation", false},
		{"22021", "character_not_in_repertoire", false},
		{"54000", "program_limit_exceeded", false},
		{"54001", "statement_too_complex", false},
		{"42601", "syntax_error", false},
	} {
		t.Run(tc.code+" "+tc.what, func(t *testing.T) {
			err := error(&pgconn.PgError{Code: tc.code, Message: tc.what})
			if got := metamodel.IsRetryable(err); got != tc.want {
				t.Fatalf("IsRetryable(%s) = %v, want %v", tc.code, got, tc.want)
			}
			// And it must survive wrapping: every path in this package
			// wraps a database error with what it was doing at the time.
			wrapped := fmt.Errorf("upsert entity: %w", err)
			if got := metamodel.IsRetryable(wrapped); got != tc.want {
				t.Fatalf("IsRetryable(wrapped %s) = %v, want %v", tc.code, got, tc.want)
			}
		})
	}

	if metamodel.IsRetryable(nil) {
		t.Fatal("IsRetryable(nil) must be false")
	}
	if metamodel.IsRetryable(errors.New("a plain error")) {
		t.Fatal("a non-database error must not be retryable")
	}
	// A domain refusal is never retryable, however it is wrapped: these
	// are the codes whose whole meaning is that resending unchanged is
	// pointless.
	for _, err := range []error{
		metamodel.ErrNotFound,
		metamodel.ErrInUse,
		metamodel.ErrSchemaViolation,
		metamodel.ErrInvalidInput,
		&metamodel.VersionConflictError{Current: 3},
	} {
		if metamodel.IsRetryable(fmt.Errorf("wrapped: %w", err)) {
			t.Fatalf("%v must not be retryable", err)
		}
	}
}
