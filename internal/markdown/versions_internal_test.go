package markdown

import (
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/paging"
)

// TestTheHistoryFingerprintLeadsWithTheProjectId pins the composition
// paging.Fingerprint's comment mandates and no behaviour test in this
// package can reach: a history cursor's other two parts — the domain
// discriminator and the document id — already tell two games apart,
// because two games' documents at one path are two different rows. So
// TestACursorFromAnotherGamesHistoryIsRefused stays green with the
// project id dropped, and this is what does not.
func TestTheHistoryFingerprintLeadsWithTheProjectId(t *testing.T) {
	project := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	document := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	if got, want := historyFingerprint(project, document),
		paging.Fingerprint(project.String(), "document_versions", document.String()); got != want {
		t.Fatalf("fingerprint = %q, want the project id first, then the listing, then the document", got)
	}
	// Each part on its own must move the answer, or it is not part of it.
	other := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	if historyFingerprint(project, document) == historyFingerprint(other, document) {
		t.Fatal("two games share one history fingerprint")
	}
	if historyFingerprint(project, document) == historyFingerprint(project, other) {
		t.Fatal("two documents share one history fingerprint")
	}
	// And the discriminator is really there: without it the parts are the
	// game and the document alone, which is what another domain reaching
	// for the same shape would spell.
	if historyFingerprint(project, document) ==
		paging.Fingerprint(project.String(), document.String()) {
		t.Fatal("the fingerprint carries no domain discriminator")
	}
}
