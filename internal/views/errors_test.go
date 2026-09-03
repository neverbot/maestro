package views

import (
	"errors"
	"testing"

	"github.com/neverbot/maestro/internal/metamodel"
)

// TestEachSentinelMatchesOnlyItsOwnCode pins that a QueryError answers
// errors.Is for its own sentinel and for no other. The metamodel's
// ValidationError has exactly this shape and exactly this trap: a code
// that is set but unrecognised matches no sentinel at all and surfaces as
// internal_error, which is the one thing a caller-fixable failure must
// never be.
func TestEachSentinelMatchesOnlyItsOwnCode(t *testing.T) {
	cases := map[string]error{
		CodeQueryInvalid:         ErrQueryInvalid,
		CodeRendererRequirements: ErrRendererRequirements,
		CodeLimitExceeded:        ErrLimitExceeded,
		CodeQueryStale:           ErrQueryStale,
	}
	for code, own := range cases {
		err := &QueryError{Code: code}
		if !errors.Is(err, own) {
			t.Fatalf("a QueryError with code %q must match its own sentinel", code)
		}
		for otherCode, other := range cases {
			if otherCode == code {
				continue
			}
			if errors.Is(err, other) {
				t.Fatalf("a QueryError with code %q must not match %q's sentinel", code, otherCode)
			}
		}
		if errors.Is(err, metamodel.ErrInvalidInput) {
			t.Fatalf("a QueryError with code %q must not match invalid_input", code)
		}
	}
}

// TestAnUnknownCodeMatchesNothingRatherThanDefaulting is the trap named
// above, asserted directly. Defaulting an unrecognised code onto the
// most-taught sentinel would tell a caller to fix the wrong thing.
func TestAnUnknownCodeMatchesNothingRatherThanDefaulting(t *testing.T) {
	err := &QueryError{Code: "query_slightly_off"}
	for _, sentinel := range []error{ErrQueryInvalid, ErrRendererRequirements,
		ErrLimitExceeded, ErrQueryStale, metamodel.ErrInvalidInput, metamodel.ErrNotFound} {
		if errors.Is(err, sentinel) {
			t.Fatalf("an unrecognised code must match no sentinel, matched %v", sentinel)
		}
	}
}

// TestAQueryErrorNamesItsPointerAndItsProblem pins the message shape a
// caller reads when it does not branch on the code.
func TestAQueryErrorNamesItsPointerAndItsProblem(t *testing.T) {
	err := &QueryError{Code: CodeQueryInvalid, Fields: []metamodel.FieldError{
		{Path: "/traverse/0/via", Message: `no relation type "requires" in this game`},
	}}
	want := `query_invalid: /traverse/0/via: no relation type "requires" in this game`
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestSentinelsListsEveryCodeThisPackageAnswersWith pins the list
// internal/web's TestEveryViewsSentinelHasAWireCode will iterate (Task
// 15): a fifth sentinel declared without being added here would be
// invisible to that convention test, which is the whole reason the list
// is a function rather than a comment.
func TestSentinelsListsEveryCodeThisPackageAnswersWith(t *testing.T) {
	want := []string{CodeQueryInvalid, CodeRendererRequirements, CodeLimitExceeded, CodeQueryStale}
	got := Sentinels()
	if len(got) != len(want) {
		t.Fatalf("Sentinels() has %d entries, want %d: %v", len(got), len(want), got)
	}
	for i, code := range want {
		if !errors.Is(&QueryError{Code: code}, got[i]) {
			t.Fatalf("Sentinels()[%d] does not match code %q", i, code)
		}
	}
}
