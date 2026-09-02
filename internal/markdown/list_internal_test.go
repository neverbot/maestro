package markdown

import (
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/paging"
)

// TestTheDocumentListingFingerprintLeadsWithTheProjectId pins the
// composition paging.Fingerprint's comment mandates and that no
// behaviour test can be trusted to reach.
//
// TestACursorFromAnotherGamesListingIsRefused does go red today when the
// project id is dropped, because an unfiltered listing's other parts are
// the same string in every game — but that is a property of the filters
// this listing happens to have, not of the rule, and it is exactly how
// the history listing's equivalent test came to pass for the wrong
// reason (Task 6's correction 4: its document id already discriminated).
// The moment this listing grows a filter whose resolved form differs per
// game, the behavioural test goes green with the project id gone and
// this one does not.
func TestTheDocumentListingFingerprintLeadsWithTheProjectId(t *testing.T) {
	project := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	other := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	filter := ListFilter{
		PathPrefix:     "Lore/",
		Kind:           "Lore",
		IncludeDeleted: true,
	}
	entity := uuid.MustParse("22222222-2222-2222-2222-222222222222").String()

	got := documentListingFingerprint(project, filter, entity)
	want := paging.Fingerprint(project.String(), "documents",
		"lore/", "lore", entity, "true")
	if got != want {
		t.Fatalf("fingerprint = %q, want the project id first, then the listing, "+
			"then the prefix, the kind, the entity and the deleted flag, folded", got)
	}

	// Every part on its own must move the answer, or it is not part of
	// it — a part that does not divide two listings is a filter a cursor
	// can be carried across.
	for _, part := range []struct {
		name  string
		other string
	}{
		{"the game", documentListingFingerprint(other, filter, entity)},
		{"the path prefix", documentListingFingerprint(project,
			ListFilter{PathPrefix: "scripts/", Kind: filter.Kind, IncludeDeleted: true}, entity)},
		{"the kind", documentListingFingerprint(project,
			ListFilter{PathPrefix: filter.PathPrefix, Kind: "script", IncludeDeleted: true}, entity)},
		{"the entity", documentListingFingerprint(project, filter, other.String())},
		{"deleted documents", documentListingFingerprint(project,
			ListFilter{PathPrefix: filter.PathPrefix, Kind: filter.Kind}, entity)},
	} {
		if got == part.other {
			t.Fatalf("two listings differing in %s share one fingerprint", part.name)
		}
	}

	// "no opinion" is a third state, not a spelling of one of the two:
	// a listing with no entity filter is not a listing filtered on some
	// entity, and its part must be its own string.
	if documentListingFingerprint(project, filter, "") ==
		documentListingFingerprint(project, filter, entity) {
		t.Fatal("an unfiltered listing shares a fingerprint with an entity-filtered one")
	}

	// And the domain discriminator is really there: without it the parts
	// are the game and a pile of filter strings, which is what another
	// domain reaching for the same shape would spell.
	if got == paging.Fingerprint(project.String(),
		"lore/", "lore", entity, "true") {
		t.Fatal("the fingerprint carries no domain discriminator")
	}
}
