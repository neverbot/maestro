package paging_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/paging"
)

// errRefused stands in for a domain's own error type. Asserting it comes
// back out of every refusal is what pins the package's central claim:
// paging builds no error of its own, so a bad cursor is always the
// caller's domain type at the caller's own argument path.
var errRefused = errors.New("refused")

func refuse(message string) error { return fmt.Errorf("%w: %s", errRefused, message) }

func TestARoundTrippedCursorComesBackUnchanged(t *testing.T) {
	want := paging.Cursor{Sort: "Duskwood", ID: uuid.New(), Fingerprint: "abc"}
	got, err := paging.Decode(paging.Encode(want), "abc", refuse)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != want {
		t.Fatalf("Decode(Encode(c)) = %+v, want %+v", got, want)
	}
}

func TestAnEmptyCursorIsTheStartOfTheListing(t *testing.T) {
	got, err := paging.Decode("", "abc", refuse)
	if err != nil || got.ID != uuid.Nil {
		t.Fatalf("Decode(\"\") = (%+v, %v), want the zero position and no error", got, err)
	}
}

func TestAnUnreadableCursorIsMalformedBeforeItIsJudgedAgainstAFingerprint(t *testing.T) {
	// The order matters: reported as belonging to another listing, a
	// truncated cursor sends a caller to inspect its filter, which is
	// not where the problem is.
	_, err := paging.Decode("!!!not base64!!!", "abc", refuse)
	if !errors.Is(err, errRefused) {
		t.Fatalf("err = %v, want the refusal the caller's own function built", err)
	}
	if !strings.Contains(err.Error(), "is malformed") {
		t.Fatalf("err = %v, want it to say the cursor is malformed", err)
	}
}

// TestEveryUnreadableCursorIsRefusedTheSameWay covers the three shapes
// Decode can find unreadable, since only the first has a test above and
// a copy of the message in any one of them is what this package exists
// to prevent.
func TestEveryUnreadableCursorIsRefusedTheSameWay(t *testing.T) {
	// A readable base64 payload that is not a page position, and one
	// that decodes but carries no row id.
	noPosition := paging.Encode(paging.Cursor{Sort: "a", Fingerprint: "abc"})
	for _, tc := range []struct{ name, in string }{
		{"not this encoding", "!!!not base64!!!"},
		{"not a page position", "bm90LWpzb24"},
		{"no row position", noPosition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := paging.Decode(tc.in, "abc", refuse)
			if err == nil || !strings.Contains(err.Error(), "is malformed") {
				t.Fatalf("err = %v, want it reported as malformed", err)
			}
			if strings.Contains(err.Error(), "different listing") {
				t.Fatalf("err = %v, want it not judged against the fingerprint", err)
			}
		})
	}
}

func TestACursorFromAnotherListingIsRefused(t *testing.T) {
	c := paging.Cursor{Sort: "a", ID: uuid.New(), Fingerprint: paging.Fingerprint("game-a", "documents")}
	_, err := paging.Decode(paging.Encode(c), paging.Fingerprint("game-b", "documents"), refuse)
	if err == nil || !strings.Contains(err.Error(), "different listing") {
		t.Fatalf("err = %v, want a refusal naming the listing", err)
	}
	if !errors.Is(err, errRefused) {
		t.Fatalf("err = %v, want the refusal the caller's own function built", err)
	}
}

// TestMalformedSaysTheSameThingDecodeDoes pins the exported helper the
// listings that parse a position further use (metamodel.ListRelations
// parses the sort half back into a timestamp) against the sentence
// Decode's own refusals carry, which is the whole reason the text lives
// in this package rather than in each domain.
func TestMalformedSaysTheSameThingDecodeDoes(t *testing.T) {
	direct := paging.Malformed("it is not the encoding this listing issues", refuse)
	viaDecode, err := "", error(nil)
	if _, err = paging.Decode("!!!not base64!!!", "abc", refuse); err != nil {
		viaDecode = err.Error()
	}
	if direct.Error() != viaDecode {
		t.Fatalf("Malformed says %q, Decode says %q", direct.Error(), viaDecode)
	}
}

func TestPartsThatDivideDifferentlyDoNotShareAFingerprint(t *testing.T) {
	// Without the length prefix, ("ab", "c") and ("a", "bc") spell one
	// string and two different listings share one fingerprint.
	if paging.Fingerprint("ab", "c") == paging.Fingerprint("a", "bc") {
		t.Fatal("two filters that divide differently must not share a fingerprint")
	}
	if paging.Fingerprint("a", "") == paging.Fingerprint("", "a") {
		t.Fatal("an empty part is not distinguished from a missing one")
	}
	first, second := paging.Fingerprint("a", "b"), paging.Fingerprint("a", "b")
	if first != second {
		t.Fatalf("a fingerprint is not stable across two calls: %q then %q", first, second)
	}
}

func TestSizeClampsRatherThanFoldingOntoTheDefault(t *testing.T) {
	for _, tc := range []struct{ limit, want int32 }{
		{-7, 50}, {0, 50}, {1, 1}, {37, 37}, {499, 499}, {500, 500}, {501, 500}, {1 << 20, 500},
	} {
		if got := paging.Size(tc.limit, 50, 500); got != tc.want {
			t.Fatalf("Size(%d, 50, 500) = %d, want %d", tc.limit, got, tc.want)
		}
	}
}

func TestTriStateKeepsNoOpinionDistinctFromBothOpinions(t *testing.T) {
	yes, no := true, false
	got := []string{paging.TriState(nil), paging.TriState(&yes), paging.TriState(&no)}
	if got[0] == got[1] || got[0] == got[2] || got[1] == got[2] {
		t.Fatalf("TriState produced %v, want three distinct spellings", got)
	}
}

// TestTheWireNamesArePinnedHereToo guards Cursor's JSON tags from inside
// this package. internal/metamodel's
// TestACursorCarriesNothingButAPositionAndAFingerprint pins the same
// three names, but a package whose wire format is pinned exclusively
// from another package is one deletion away from being unpinned — the
// comment on Cursor said so honestly, which was the right instinct but
// not a substitute for this test.
func TestTheWireNamesArePinnedHereToo(t *testing.T) {
	encoded := paging.Encode(paging.Cursor{
		Sort: "Elwynn Forest", ID: uuid.New(), Fingerprint: "abc",
	})
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, want := range []string{"n", "i", "f"} {
		if _, ok := fields[want]; !ok {
			t.Fatalf("cursor is missing %q: %s", want, raw)
		}
	}
	if len(fields) != 3 {
		t.Fatalf("cursor carries %d fields, want 3: %s", len(fields), raw)
	}
}

// TestTheRecoveryAdviceIsPinnedTooNotJustTheMalformedPrefix guards the
// half of the malformed sentence an agent actually acts on. Only the
// "is malformed" prefix and the Malformed-equals-Decode equivalence were
// pinned before this test, so the recovery clause — "page from the
// cursor a previous call returned, or omit it to start" — could be
// reworded into something useless and nothing here would notice.
func TestTheRecoveryAdviceIsPinnedTooNotJustTheMalformedPrefix(t *testing.T) {
	err := paging.Malformed("some reason", refuse)
	const advice = "page from the cursor a previous call returned, or omit it to start"
	if !strings.Contains(err.Error(), advice) {
		t.Fatalf("err = %q, want it to contain the recovery advice %q", err.Error(), advice)
	}
}

// TestMalformedPanicsOnANilRefuse pins the nil-refuse contract Malformed
// documents: a refuse function that returns nil must not be mistaken for
// success, because that success would be Decode handing back the zero
// Cursor — the start of the listing — for what should have been a
// refusal.
func TestMalformedPanicsOnANilRefuse(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Malformed with a nil refuse did not panic")
		}
	}()
	nilRefuse := func(string) error { return nil }
	_ = paging.Malformed("why", nilRefuse)
}

// TestANilRefusePanicsRatherThanReturningTheZeroPosition pins the same
// contract from Decode's malformed arms.
func TestANilRefusePanicsRatherThanReturningTheZeroPosition(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Decode with a nil refuse did not panic on a malformed cursor")
		}
	}()
	nilRefuse := func(string) error { return nil }
	_, _ = paging.Decode("!!!not base64!!!", "abc", nilRefuse)
}

// TestAFingerprintMismatchWithANilRefusePanics pins the contract on
// Decode's other refusal, the fingerprint mismatch, which does not go
// through Malformed and so needed its own enforcement.
func TestAFingerprintMismatchWithANilRefusePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Decode with a nil refuse did not panic on a fingerprint mismatch")
		}
	}()
	c := paging.Cursor{Sort: "a", ID: uuid.New(), Fingerprint: "abc"}
	nilRefuse := func(string) error { return nil }
	_, _ = paging.Decode(paging.Encode(c), "a-different-fingerprint", nilRefuse)
}
