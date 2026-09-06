package markdown

import (
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/paging"
)

// TestTheTwoSidesOfTheJoinDoNotShareAFingerprint pins the composition no
// behaviour test can be trusted to reach.
//
// TestADocumentsAttachmentsPageAndTheCursorBelongsToItsOwnSide does
// refuse a document-side cursor offered to the entity side — but it does
// so today because a document id and an entity id are different uuids,
// not because the two listings are named apart, and it stays green with
// both names collapsed into one. That is the shape Task 6's correction 4
// found in the history fingerprint and it is recorded here rather than
// papered over: the two sides sort on different columns, an entity key
// one way and a document path the other, so a cursor crossing between
// them would compare a path against a key. The discriminator is what
// makes that impossible on purpose rather than by arithmetic.
func TestTheTwoSidesOfTheJoinDoNotShareAFingerprint(t *testing.T) {
	project := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	other := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	// One id standing for both a document and an entity, which is the
	// only way to ask whether the *names* differ.
	row := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	byDocument := documentLinksFingerprint(project, row)
	byEntity := entityLinksFingerprint(project, row)
	if byDocument == byEntity {
		t.Fatal("the two sides of the join share one fingerprint, so a cursor issued " +
			"for one would be accepted by the other")
	}
	if byDocument != paging.Fingerprint(project.String(),
		"document_links_by_document", row.String()) {
		t.Fatalf("fingerprint = %q, want the project id first, then the side, then the row",
			byDocument)
	}
	if byEntity != paging.Fingerprint(project.String(),
		"document_links_by_entity", row.String()) {
		t.Fatalf("fingerprint = %q, want the project id first, then the side, then the row",
			byEntity)
	}
	for _, part := range []struct {
		name  string
		other string
	}{
		{"the game", documentLinksFingerprint(other, row)},
		{"the document", documentLinksFingerprint(project, other)},
	} {
		if byDocument == part.other {
			t.Fatalf("two attachment listings differing in %s share one fingerprint", part.name)
		}
	}
	if byEntity == entityLinksFingerprint(project, other) {
		t.Fatal("two entities' document listings share one fingerprint")
	}
}
