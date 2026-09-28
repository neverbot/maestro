package metamodel

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/neverbot/maestro/internal/assert"
)

// TestAValueTooLargeToIndexIsCallerFixable pins the backstop behind
// searchTextLimit.
func TestAValueTooLargeToIndexIsCallerFixable(t *testing.T) {
	raised := &pgconn.PgError{
		Code:    "54000",
		Message: "string is too long for tsvector (2197988 bytes, max 1048575 bytes)",
	}
	mapped := searchLimitExceeded(fmt.Errorf("upsert entity: %w", raised))
	assert.Must(t, errors.Is(mapped, ErrInvalidInput), "mapped = %v, want it to match ErrInvalidInput", mapped)
	if code := failureFor(context.Background(), 0, "lore", mapped).Code; code != codeInvalidInput {
		t.Fatalf("bulk code = %q, want %q", code, codeInvalidInput)
	}
	assert.Must(t, errors.Is(mapped, raised), "mapped = %v, want the database's own error still reachable", mapped)

	unrelated := error(&pgconn.PgError{Code: "23503", Message: "violates foreign key constraint"})
	if got := searchLimitExceeded(unrelated); got != unrelated {
		t.Fatalf("searchLimitExceeded(%v) = %v, want it unchanged", unrelated, got)
	}
	if got := searchLimitExceeded(errors.New("plain")); errors.Is(got, ErrInvalidInput) {
		t.Fatalf("a non-database error must pass through, got %v", got)
	}
}
