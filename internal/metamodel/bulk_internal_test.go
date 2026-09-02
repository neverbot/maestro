package metamodel

import "testing"

// TestFoldedIdentityKeepsThePartsOfARowApart pins both halves of what
// foldedIdentity claims: the case fold, and the length prefix.
//
// Only the fold was pinned before, by the batch tests that repeat a key
// in a different case. Changing the format string from "%d:%s" to ":%s"
// left the whole suite green — so the extraction's own headline
// justification, that entities and relations cannot drift apart because
// they fold identity through one function, was pinned for one kind of
// drift and not the other. It matters most for edges: Ref.TypeKey and
// Ref.Key are documented as never pattern-validated, so a part carrying
// the delimiter is reachable input, and under a delimiter-only join the
// pairs below are one identity — the second item of a batch refused as a
// repetition of the first, or, in atomic mode, the whole batch refused.
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
			if got, other := foldedIdentity(tc.a...), foldedIdentity(tc.b...); got == other {
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
			if got, other := foldedIdentity(tc.a...), foldedIdentity(tc.b...); got != other {
				t.Fatalf("%v folds to %q and %v to %q; they are one row", tc.a, got, tc.b, other)
			}
		})
	}
}
