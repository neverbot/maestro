package metamodel

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestAnEntityPageAsksForTooMuchAndGetsTheCap pins the shared page rule
// at the entity listing's own bounds. The argument is
// relationPageSize's: asking for nothing is no opinion and gets the
// default, asking for too much is an opinion and gets the cap, and
// folding both onto the default made `Limit: 501` return strictly fewer
// rows than `Limit: 500` without telling anyone.
func TestAnEntityPageAsksForTooMuchAndGetsTheCap(t *testing.T) {
	cases := []struct {
		name string
		in   int32
		want int32
	}{
		{"nothing asked for", 0, defaultEntityPage},
		{"a negative limit is still no opinion", -7, defaultEntityPage},
		{"an ordinary limit", 10, 10},
		{"the cap itself", maxEntityPage, maxEntityPage},
		{"one above the cap", maxEntityPage + 1, maxEntityPage},
		{"far above the cap", 1 << 20, maxEntityPage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pageSize(tc.in, defaultEntityPage, maxEntityPage); got != tc.want {
				t.Fatalf("pageSize(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestAFingerprintTellsFiltersApartWhereverTheirPartsDivide pins the
// length prefixing fingerprintOf inherits from foldedIdentity.
//
// Without it two different filters whose parts run together spell one
// string — ("ab", "c") and ("a", "bc") — and share a fingerprint, so a
// cursor issued for one pages the other and the check this whole
// mechanism exists for passes over exactly the case it was added for.
func TestAFingerprintTellsFiltersApartWhereverTheirPartsDivide(t *testing.T) {
	if fingerprintOf("ab", "c") == fingerprintOf("a", "bc") {
		t.Fatal("two filters dividing differently share a fingerprint")
	}
	if fingerprintOf("a", "") == fingerprintOf("", "a") {
		t.Fatal("an empty part is not distinguished from a missing one")
	}
	first, second := fingerprintOf("a", "b"), fingerprintOf("a", "b")
	if first != second {
		t.Fatalf("a fingerprint is not stable across two calls: %q then %q", first, second)
	}
}

// TestTheInvalidFilterKeepsItsThreeStatesApart pins that "no opinion" is
// a third fingerprint and not a spelling of one of the two opinions: a
// cursor issued for the unfiltered listing must not page the
// invalid-only one, whose rows are a different set in the same order.
func TestTheInvalidFilterKeepsItsThreeStatesApart(t *testing.T) {
	yes, no := true, false
	parts := []string{
		invalidFilterPart(nil),
		invalidFilterPart(&yes),
		invalidFilterPart(&no),
	}
	for i := range parts {
		for j := i + 1; j < len(parts); j++ {
			if parts[i] == parts[j] {
				t.Fatalf("invalid filter states %d and %d share the spelling %q", i, j, parts[i])
			}
		}
	}
}

// TestACursorCarriesNothingButAPositionAndAFingerprint pins what is on
// the wire, because cursor's doc comment claims it leaks nothing and a
// claim about an encoding is only worth what the encoding actually
// holds. A field added to the struct without a decision about it fails
// here.
func TestACursorCarriesNothingButAPositionAndAFingerprint(t *testing.T) {
	encoded := encodeCursor(cursor{
		Name: "Elwynn Forest", ID: uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		Fingerprint: "abc",
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

// TestAMalformedCursorIsRefusedBeforeItsFingerprintIsJudged pins the
// order of decodeCursor's two refusals. A truncated cursor reported as
// belonging to another listing sends a caller to inspect its filter,
// which is not where the problem is.
func TestAMalformedCursorIsRefusedBeforeItsFingerprintIsJudged(t *testing.T) {
	_, err := decodeCursor("!!not base64!!", "some-fingerprint")
	if err == nil {
		t.Fatal("a malformed cursor was accepted")
	}
	if !strings.Contains(err.Error(), "is malformed") {
		t.Fatalf("err = %v, want it reported as malformed", err)
	}
}
