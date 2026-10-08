package views

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/metamodel"
)

// TestAttachmentsArea is the images hanging on an entity: what attaching
// means, what detaching leaves behind, and what one game can reach of
// another's.
func TestAttachmentsArea(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// quest is the entity every claim here hangs an image on.
	quest := func(t *testing.T, g *game) uuid.UUID {
		t.Helper()
		row, err := g.meta.EntityByKey(ctx, g.projectID, "quest", "hogger")
		assert.Must(t, err == nil, "read the fixture entity: %v", err)
		return row.ID
	}

	t.Run("an image hangs on an entity and reads back with the asset's own metadata", func(t *testing.T) {
		g, _ := newGame(t)
		entity := quest(t, g)
		asset := g.upload(t, "map.png", pngBytes(t, 64, 48))

		assert.Must(t, g.views.Attach(ctx, g.projectID, entity, asset.ID, Actor{}) == nil, "attach")

		got, err := g.views.Attachments(ctx, g.projectID, entity)
		assert.Must(t, err == nil, "read attachments: %v", err)
		assert.Must(t, len(got) == 1, "%d attachments, want 1", len(got))
		// **The metadata comes from the asset and is not copied into the
		// link.** A filename read off the join row would be the filename
		// as it was when somebody attached it.
		assert.Should(t, got[0].ID == asset.ID, "attachment id %s, want the asset's %s", got[0].ID, asset.ID)
		assert.Should(t, got[0].Filename == "map.png", "filename %q", got[0].Filename)
		assert.Should(t, got[0].Width == 64 && got[0].Height == 48, "size %dx%d, want 64x48", got[0].Width, got[0].Height)
	})

	t.Run("attaching the same image twice is the attachment that is already there", func(t *testing.T) {
		g, _ := newGame(t)
		entity := quest(t, g)
		asset := g.upload(t, "map.png", pngBytes(t, 8, 8))

		for range 3 {
			assert.Must(t, g.views.Attach(ctx, g.projectID, entity, asset.ID, Actor{}) == nil, "attach")
		}
		got, err := g.views.Attachments(ctx, g.projectID, entity)
		assert.Must(t, err == nil, "read attachments: %v", err)
		assert.Should(t, len(got) == 1, "%d attachments after attaching one image three times, want 1", len(got))
	})

	t.Run("one image hangs on several entities at once", func(t *testing.T) {
		g, _ := newGame(t)
		hogger := quest(t, g)
		elwynn, err := g.meta.EntityByKey(ctx, g.projectID, "zone", "elwynn")
		assert.Must(t, err == nil, "read the second entity: %v", err)
		asset := g.upload(t, "shared.png", pngBytes(t, 8, 8))

		assert.Must(t, g.views.Attach(ctx, g.projectID, hogger, asset.ID, Actor{}) == nil, "attach to the first")
		assert.Must(t, g.views.Attach(ctx, g.projectID, elwynn.ID, asset.ID, Actor{}) == nil, "attach to the second")

		for _, entity := range []uuid.UUID{hogger, elwynn.ID} {
			got, err := g.views.Attachments(ctx, g.projectID, entity)
			assert.Must(t, err == nil, "read attachments: %v", err)
			assert.Should(t, len(got) == 1, "the image did not reach %s", entity)
		}
	})

	t.Run("detaching leaves the image in the library", func(t *testing.T) {
		g, _ := newGame(t)
		entity := quest(t, g)
		asset := g.upload(t, "map.png", pngBytes(t, 8, 8))
		assert.Must(t, g.views.Attach(ctx, g.projectID, entity, asset.ID, Actor{}) == nil, "attach")

		assert.Must(t, g.views.Detach(ctx, g.projectID, entity, asset.ID) == nil, "detach")
		got, err := g.views.Attachments(ctx, g.projectID, entity)
		assert.Must(t, err == nil, "read attachments: %v", err)
		assert.Should(t, len(got) == 0, "%d attachments survived the detach", len(got))

		// **The bytes are still there**, which is the asymmetry this
		// whole table exists to express: the library belongs to the game
		// and the attachment was only a statement that one entity
		// referred to it.
		_, err = g.views.ReadAsset(ctx, g.projectID, asset.ID)
		assert.Should(t, err == nil, "detaching took the image with it: %v", err)
	})

	t.Run("detaching something that is not attached is not_found", func(t *testing.T) {
		g, _ := newGame(t)
		entity := quest(t, g)
		asset := g.upload(t, "map.png", pngBytes(t, 8, 8))
		err := g.views.Detach(ctx, g.projectID, entity, asset.ID)
		assert.Should(t, errors.Is(err, metamodel.ErrNotFound), "detaching an unattached image reported %v, want not_found", err)
	})

	t.Run("removing the image takes its attachments with it", func(t *testing.T) {
		g, _ := newGame(t)
		entity := quest(t, g)
		asset := g.upload(t, "map.png", pngBytes(t, 8, 8))
		assert.Must(t, g.views.Attach(ctx, g.projectID, entity, asset.ID, Actor{}) == nil, "attach")

		assert.Must(t, g.views.RemoveAsset(ctx, g.projectID, asset.ID) == nil, "remove the image")
		got, err := g.views.Attachments(ctx, g.projectID, entity)
		assert.Must(t, err == nil, "read attachments: %v", err)
		assert.Should(t, len(got) == 0, "an attachment outlived the image it pointed at, and nothing can ever draw it")
	})

	t.Run("removing the entity takes its attachments and leaves the image", func(t *testing.T) {
		g, _ := newGame(t)
		entity := quest(t, g)
		asset := g.upload(t, "map.png", pngBytes(t, 8, 8))
		assert.Must(t, g.views.Attach(ctx, g.projectID, entity, asset.ID, Actor{}) == nil, "attach")

		_, err := g.pool.Exec(ctx, `DELETE FROM entities WHERE id = $1`, entity)
		assert.Must(t, err == nil, "remove the entity: %v", err)

		var left int
		assert.Must(t, g.pool.QueryRow(ctx,
			`SELECT count(*) FROM entity_assets WHERE asset_id = $1`, asset.ID).Scan(&left) == nil, "count")
		assert.Should(t, left == 0, "%d attachments outlived the entity they hung on", left)
		_, err = g.views.ReadAsset(ctx, g.projectID, asset.ID)
		assert.Should(t, err == nil, "removing an entity took a shared image with it: %v", err)
	})

	t.Run("one game cannot hang another game's image, or reach its attachments", func(t *testing.T) {
		azeroth, outland := newGame(t)
		theirs := outland.upload(t, "theirs.png", pngBytes(t, 8, 8))
		mine := quest(t, azeroth)

		err := azeroth.views.Attach(ctx, azeroth.projectID, mine, theirs.ID, Actor{})
		assert.Should(t, errors.Is(err, metamodel.ErrNotFound),
			"a game attached another game's image and got %v, want not_found", err)

		// And the read is scoped too: the other game's own attachment is
		// invisible from here even with the entity id in hand.
		their := outland.upload(t, "ours.png", pngBytes(t, 8, 8))
		row, err := outland.meta.EntityByKey(ctx, outland.projectID, "quest", "hogger")
		assert.Must(t, err == nil, "read their entity: %v", err)
		assert.Must(t, outland.views.Attach(ctx, outland.projectID, row.ID, their.ID, Actor{}) == nil, "attach theirs")

		got, err := azeroth.views.Attachments(ctx, azeroth.projectID, row.ID)
		assert.Must(t, err == nil, "read across games: %v", err)
		assert.Should(t, len(got) == 0, "one game read %d of another game's attachments", len(got))
	})

	t.Run("a page of entities is counted in one query", func(t *testing.T) {
		g, _ := newGame(t)
		hogger := quest(t, g)
		elwynn, err := g.meta.EntityByKey(ctx, g.projectID, "zone", "elwynn")
		assert.Must(t, err == nil, "read the second entity: %v", err)
		for i := range 2 {
			asset := g.upload(t, "map.png", pngBytes(t, 8+i, 8))
			assert.Must(t, g.views.Attach(ctx, g.projectID, hogger, asset.ID, Actor{}) == nil, "attach")
		}

		counts, err := g.views.AttachmentCounts(ctx, g.projectID, []uuid.UUID{hogger, elwynn.ID})
		assert.Must(t, err == nil, "count: %v", err)
		assert.Should(t, counts[hogger] == 2, "hogger counted %d, want 2", counts[hogger])
		// **Absent and not zero.** A row with no attachments has no row
		// in the group-by, and a caller reading a missing key gets the
		// zero value, which is the answer.
		_, present := counts[elwynn.ID]
		assert.Should(t, !present, "an entity with no images has an entry: the count is a group-by, not a left join")
	})
}
