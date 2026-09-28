package markdown

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// KindCount is one entry of a game's document-kind vocabulary: a kind
// its documents actually carry, and how many carry it.
type KindCount struct {
	Kind          string
	DocumentCount int64
}

// KindTotals is what the catalogue's rows cannot say between them.
type KindTotals struct {
	Documents int64
	Unkinded  int64
}

// KindCatalogue is the answer Kinds gives.
type KindCatalogue struct {
	Kinds  []KindCount
	Totals KindTotals
}

// Kinds lists the document kinds one game actually uses, with counts.
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
