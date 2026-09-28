package markdown

import (
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/paging"
)

// TestTheTwoSidesOfTheJoinDoNotShareAFingerprint pins the composition no
// behaviour test can be trusted to reach.
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
	assert.Must(t, byDocument == paging.Fingerprint(project.String(),
		"document_links_by_document", row.String()), "fingerprint = %q, want the project id first, then the side, then the row",
		byDocument)
	assert.Must(t, byEntity == paging.Fingerprint(project.String(),
		"document_links_by_entity", row.String()), "fingerprint = %q, want the project id first, then the side, then the row",
		byEntity)
	for _, part := range []struct {
		name  string
		other string
	}{
		{"the game", documentLinksFingerprint(other, row)},
		{"the document", documentLinksFingerprint(project, other)},
	} {
		assert.Must(t, byDocument != part.other, "two attachment listings differing in %s share one fingerprint", part.name)
	}
	assert.Must(t, byEntity != entityLinksFingerprint(project, other), "two entities' document listings share one fingerprint")
}
