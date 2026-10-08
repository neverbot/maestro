package views

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
)

// Images attached to an entity (0021_entity_assets.sql).
//
// **An attachment is a reference, not ownership.** The library belongs to
// the game: one image can hang on several entities and be a map's ground
// at the same time. Detaching drops the reference and leaves the bytes,
// and removing the image takes its attachments with it.
//
// **Nothing here uploads.** An image reaches the library through
// CreateAsset, which a person drives from the browser; this file only
// says which entity refers to which. That split is what makes "only a
// person uploads" a fact about one function rather than a rule spread
// over two.

// Attachment is one image hanging on one entity, without its bytes. It
// is the asset's own metadata: the link row holds nothing a reader wants,
// so a renamed file reads renamed everywhere it hangs.
type Attachment struct {
	Asset
}

// Attach hangs one image on one entity. Attaching an image that is
// already there is the attachment that is already there rather than an
// error: it is what a designer who picks the same picture twice means.
func (s *Service) Attach(ctx context.Context, projectID, entityID, assetID uuid.UUID, actor Actor) error {
	_, err := s.q.AttachAssetToEntity(ctx, dbq.AttachAssetToEntityParams{
		ProjectID: projectID, EntityID: entityID, AssetID: assetID,
		CreatedByUserID: actor.UserID,
	})
	if err != nil {
		// A foreign key is the only way to learn that one of the two ends
		// is not in this game, and it cannot say which: both are
		// composite keys carrying project_id, so an id from another game
		// and an id from nowhere trip the same constraint. The message
		// names both rather than guessing.
		if isForeignKeyViolation(err) {
			return fmt.Errorf("%w: this game has no such entity or no such image", metamodel.ErrNotFound)
		}
		return fmt.Errorf("attach image: %w", err)
	}
	return nil
}

// Detach takes one image off one entity and leaves the image.
func (s *Service) Detach(ctx context.Context, projectID, entityID, assetID uuid.UUID) error {
	removed, err := s.q.DetachAssetFromEntity(ctx, dbq.DetachAssetFromEntityParams{
		ProjectID: projectID, EntityID: entityID, AssetID: assetID,
	})
	if err != nil {
		return fmt.Errorf("detach image: %w", err)
	}
	if removed == 0 {
		return fmt.Errorf("%w: that image is not attached to this", metamodel.ErrNotFound)
	}
	return nil
}

// Attachments reads one entity's images, oldest first, which is the order
// they were attached in and the only order that means anything when none
// of them is the main one.
func (s *Service) Attachments(ctx context.Context, projectID, entityID uuid.UUID) ([]Attachment, error) {
	rows, err := s.q.ListAssetsForEntity(ctx, dbq.ListAssetsForEntityParams{
		ProjectID: projectID, EntityID: entityID,
	})
	if err != nil {
		return nil, fmt.Errorf("read attached images: %w", err)
	}
	out := make([]Attachment, 0, len(rows))
	for _, row := range rows {
		out = append(out, Attachment{Asset{
			ID: row.ID, Filename: row.Filename, Mime: row.Mime,
			Width: row.Width, Height: row.Height, CreatedAt: row.CreatedAt.Time,
		}})
	}
	return out, nil
}

// AttachmentCounts is how many images each of these entities carries. One
// query for a page of rows rather than one per row.
func (s *Service) AttachmentCounts(ctx context.Context, projectID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]int64, error) {
	out := map[uuid.UUID]int64{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.q.CountAssetsForEntities(ctx, dbq.CountAssetsForEntitiesParams{
		ProjectID: projectID, EntityIds: ids,
	})
	if err != nil {
		return nil, fmt.Errorf("count attached images: %w", err)
	}
	for _, row := range rows {
		out[row.EntityID] = row.Attached
	}
	return out, nil
}

// isForeignKeyViolation reports whether this is Postgres refusing a row
// whose parent is not there.
func isForeignKeyViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23503"
	}
	return errors.Is(err, pgx.ErrNoRows)
}
