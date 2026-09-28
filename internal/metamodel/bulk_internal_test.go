package metamodel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/neverbot/maestro/internal/assert"
)

// TestFoldedIdentityKeepsThePartsOfARowApart pins both halves of what
// FoldedIdentity claims: the case fold, and the length prefix.
func TestFoldedIdentityKeepsThePartsOfARowApart(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b []string
	}{
		{
			// Two entities, as entityBulkSpec folds them: (type key, key).
			name: "an entity's two parts cannot meet in the middle",
			a:    []string{"a:b", "c"},
			b:    []string{"a", "b:c"},
		},
		{
			// Two edges, as relationBulkSpec folds them: the relation type
			// key and both endpoints' (type key, key). This is the shape
			// the review reached for, and it is an ordinary seeding
			// mistake rather than an attack: nothing between an agent and
			// this function checks a Ref against a pattern.
			name: "an edge's five parts cannot meet in the middle",
			a:    []string{"requires", "quest", "a:quest", "b", ""},
			b:    []string{"requires", "quest", "a", "quest:b", ""},
		},
		{
			// An empty trailing part is a part, not an absence.
			name: "an empty part still occupies its position",
			a:    []string{"requires", "quest", "a", "", "b"},
			b:    []string{"requires", "quest", "a", "b", ""},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, other := FoldedIdentity(tc.a...), FoldedIdentity(tc.b...); got == other {
				t.Fatalf("%v and %v fold to the same identity %q", tc.a, tc.b, got)
			}
		})
	}

	// The other half: the fold is case-insensitive, because every one of
	// these parts is resolved through a lower(key) lookup and two items
	// spelling a key differently address one row.
	for _, tc := range []struct {
		name string
		a, b []string
	}{
		{"an entity", []string{"Quest", "Hogger"}, []string{"quest", "hogger"}},
		{
			"an edge",
			[]string{"Requires", "Quest", "Hogger", "Zone", "Elwynn"},
			[]string{"requires", "quest", "hogger", "zone", "elwynn"},
		},
	} {
		t.Run(tc.name+" is one row however it is spelled", func(t *testing.T) {
			if got, other := FoldedIdentity(tc.a...), FoldedIdentity(tc.b...); got != other {
				t.Fatalf("%v folds to %q and %v to %q; they are one row", tc.a, got, tc.b, other)
			}
		})
	}
}

// TestABulkFailureNeverCarriesTheDatabasesOwnWords closes review finding
// M2, and pins the ordering of failureFor's switch (finding L2).
func TestABulkFailureNeverCarriesTheDatabasesOwnWords(t *testing.T) {
	lockTimeout := fmt.Errorf("upsert relation: %w",
		&pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"})
	missingTable := fmt.Errorf("upsert relation: %w",
		&pgconn.PgError{Code: "42P01", Message: `relation "entities_secret" does not exist`})

	for _, tc := range []struct {
		name, wantCode, wantMessage string
		err                         error
	}{
		{"contention", "retryable", retryableFailureMessage, lockTimeout},
		{"anything nobody planned for", "internal_error", internalFailureMessage, missingTable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := failureFor(context.Background(), 3, "hogger", tc.err)
			assert.Must(t, got.Code == tc.wantCode, "code = %q, want %q", got.Code, tc.wantCode)
			assert.Must(t, got.Message == tc.wantMessage, "message = %q, want the fixed %q", got.Message, tc.wantMessage)
			assert.Must(t, !strings.Contains(got.Message, "SQLSTATE") && !strings.Contains(got.Message, "canceling statement") && !strings.Contains(got.Message, "entities_secret"), "message leaks the database's own text: %q", got.Message)
		})
	}

	// A caller-fixable fault keeps its own message: that half of the
	// report is the whole point of a batch failure.
	spoken := failureFor(context.Background(), 0, "hogger", fmt.Errorf("%w: no entity type %q in this game",
		ErrNotFound, "quest"))
	assert.Must(t, spoken.Code == "not_found" && strings.Contains(spoken.Message, "quest"), "failure = %+v, want a not_found naming the key", spoken)

	// Finding L2: IsRetryable is checked *last*, and the reason is stated
	// at length on failureFor. Nothing pinned it — moving the arm to the
	// front left the whole suite green. An error that is both a domain
	// refusal and a contention SQLSTATE must report the domain code: the
	// caller can act on that one, and "resend unchanged" is the worst
	// possible advice about a row that will be refused again.
	for _, tc := range []struct {
		name, want string
		sentinel   error
	}{
		{"a refusal", "not_found", ErrNotFound},
		{"a bad argument", "invalid_input", ErrInvalidInput},
		{"a stale version", "version_conflict", ErrVersionConflict},
	} {
		t.Run(tc.name+" outranks a contention SQLSTATE on the same error", func(t *testing.T) {
			both := errors.Join(tc.sentinel, &pgconn.PgError{Code: "40001"})
			if got := failureFor(context.Background(), 0, "hogger", both); got.Code != tc.want {
				t.Fatalf("code = %q, want %q", got.Code, tc.want)
			}
		})
	}
}
