package markdown

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// KindCount is one entry of a game's document-kind vocabulary: a kind
// its documents actually carry, and how many carry it.
//
// Kind is the *folded* spelling, and that is a decision rather than an
// accident of the query. Both filters that take a kind — ListFilter.Kind
// and SearchDocuments' — compare lower(d.kind) = lower(the argument), so
// "Lore" and "lore" select one and the same set of documents. A
// catalogue is read in order to be fed back into those filters, and the
// folded spelling is the one value that names exactly the set this row
// counted. Returning a stored spelling instead would hand a caller a
// value that works and a number that belonged to a different set the
// moment two spellings were in use.
// TestTwoSpellingsOfOneKindAreOneCatalogueRow pins it.
type KindCount struct {
	Kind          string
	DocumentCount int64
}

// KindTotals is what the catalogue's rows cannot say between them.
//
// Documents is every live document in the game, so a reader can tell
// "three kinds covering everything" from "three kinds covering a tenth
// of it" without summing anything. Unkinded is how many carry no kind at
// all — the one group the catalogue itself cannot list, because ” is
// not a filter value (ListFilter.Kind's own comment records that gap)
// and offering it as a kind would offer a filter that does nothing.
type KindTotals struct {
	Documents int64
	Unkinded  int64
}

// KindCatalogue is the answer Kinds gives.
//
// Kinds is never nil: a game whose documents all lack a kind answers
// with an empty list and not null, the same decision DocumentPage and
// the metamodel's own game summary make, and for the same reason — a
// caller that has to tell [] from null before it can loop has been
// handed two spellings of one fact.
type KindCatalogue struct {
	Kinds  []KindCount
	Totals KindTotals
}

// Kinds lists the document kinds one game actually uses, with counts.
//
// **This is the discovery half of a decision whose other half stands.**
// A document's kind is free text and Maestro ships no vocabulary of
// kinds — the same rule that forbids a built-in Quest entity type
// forbids a built-in Lore kind — so an unknown kind is not a knowable
// state, and both filters that take one answer an unrecognised value
// with an empty page rather than a refusal (List's own doc comment
// argues that at length, and it is unchanged). What was missing was not
// a refusal but a way to find out: both surfaces filter by kind and
// nothing told a caller which kinds exist, so an agent had to already
// know the vocabulary to use it and a designer had no way to see it at
// all.
//
// The shape is the game summary's, deliberately. That call answers the
// identical question for entity types — here is what this game declared,
// with how many rows each holds — and the two are read side by side by
// anyone trying to understand a game they did not build. A second shape
// for the same question would be a second thing to learn.
//
// **It describes live documents only.** A kind kept alive by nothing but
// a tombstone would be a filter value whose answer, under the default
// listing and under search, is an empty page.
// TestADeletedDocumentsKindLeavesTheCatalogue pins it, and pins that the
// kind comes back when the document is resurrected.
//
// The catalogue is a fixed-size answer in the sense that matters: it
// grows with the number of *kinds* a game uses, not with the number of
// documents it holds, which is what makes it safe to put on a page load
// and in an agent's context. TestTheKindCatalogueDoesNotGrowWithThe
// DocumentsItCounts pins that property the way the game summary's own
// test does.
func (s *Service) Kinds(ctx context.Context, projectID uuid.UUID) (KindCatalogue, error) {
	rows, err := s.q.CountDocumentsPerKind(ctx, projectID)
	if err != nil {
		return KindCatalogue{}, fmt.Errorf("count documents per kind: %w", err)
	}
	totals, err := s.q.CountDocuments(ctx, projectID)
	if err != nil {
		return KindCatalogue{}, fmt.Errorf("count documents: %w", err)
	}
	catalogue := KindCatalogue{
		Kinds: make([]KindCount, 0, len(rows)),
		Totals: KindTotals{
			Documents: totals.Total,
			Unkinded:  totals.Unkinded,
		},
	}
	for _, row := range rows {
		catalogue.Kinds = append(catalogue.Kinds, KindCount{Kind: row.Kind, DocumentCount: row.DocumentCount})
	}
	return catalogue, nil
}
