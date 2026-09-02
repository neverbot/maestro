package metamodel

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestAValueTooLargeToIndexIsCallerFixable pins the backstop behind
// searchTextLimit.
//
// The bound in searchTextOf is what actually keeps a long field
// writable, and with it in place no test can reach SQLSTATE 54000
// through the service. The mapping still has to exist and still has to
// be pinned: 54000 is program_limit_exceeded, always a value of the
// caller's that is too big for something, and the default arm of
// failureFor would file it as internal_error — the code reserved for
// what nobody planned for, which tells an agent to give up on a call it
// could fix by shortening what it sent. Anything else Postgres raises
// passes through untouched, so this cannot swallow a fault that is not
// the caller's.
func TestAValueTooLargeToIndexIsCallerFixable(t *testing.T) {
	raised := &pgconn.PgError{
		Code:    "54000",
		Message: "string is too long for tsvector (2197988 bytes, max 1048575 bytes)",
	}
	mapped := searchLimitExceeded(fmt.Errorf("upsert entity: %w", raised))
	if !errors.Is(mapped, ErrInvalidInput) {
		t.Fatalf("mapped = %v, want it to match ErrInvalidInput", mapped)
	}
	if code := failureFor(context.Background(), 0, "lore", mapped).Code; code != codeInvalidInput {
		t.Fatalf("bulk code = %q, want %q", code, codeInvalidInput)
	}
	if !errors.Is(mapped, raised) {
		t.Fatalf("mapped = %v, want the database's own error still reachable", mapped)
	}

	unrelated := error(&pgconn.PgError{Code: "23503", Message: "violates foreign key constraint"})
	if got := searchLimitExceeded(unrelated); got != unrelated {
		t.Fatalf("searchLimitExceeded(%v) = %v, want it unchanged", unrelated, got)
	}
	if got := searchLimitExceeded(errors.New("plain")); errors.Is(got, ErrInvalidInput) {
		t.Fatalf("a non-database error must pass through, got %v", got)
	}
}
