package views

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/paging"
)

// Background assets: the images a `map` view is drawn over, and the one
// place in Maestro that accepts bytes from outside.
//
// **Upload is REST-only, from the browser.** There is no MCP tool that
// takes image bytes and there is not going to be one: pushing megabytes
// of base64 through a tool call to save a human from opening the UI is
// the wrong trade, and an agent that needs a background references an
// asset that already exists (views.list_assets, views.set_background).
// internal/web is where the HTTP surface lives; this file is the policy
// it enforces.
//
// **Everything about the bytes is read out of the bytes.** The mime is
// sniffed from the leading magic numbers, never taken from the caller's
// Content-Type or from the filename, and the width and height are
// decoded from the image header rather than accepted as arguments. A
// filename check is exactly what an SVG carrying a script walks past:
// TestAnSVGIsRefusedWhateverItCallsItself sends an SVG's bytes with a
// PNG's Content-Type and a .png name.
//
// **SVG is refused, and refused by an allowlist rather than by naming
// it.** An SVG served inline executes script, in a browser session
// holding this designer's cookie; a raster map is what a designer has
// anyway. The refusal is the allowlist of three magic numbers below —
// so a format nobody has thought of is refused for the same reason,
// which is the lesson internal/markdown's safeDestination records after
// a denylist let `javascript:` through an autolink. sniffedMime never
// returns a mime it was not asked to look for; svgLooking below changes
// the wording of a refusal and can never change the decision.
//
// **What was decided about the decoder as an attack surface**, since
// this is the one place hostile bytes meet a parser:
//
//   - **Nothing decodes pixels.** Dimensions come from
//     png.DecodeConfig and jpeg.DecodeConfig, which read the header and
//     return -- they never allocate the pixel buffer -- and from
//     webpConfig below, which reads the RIFF chunk header and no more.
//     A decompression bomb (a 20000x20000 flat-colour PNG compresses to
//     a few hundred kilobytes) therefore never expands inside this
//     process at all.
//   - **The decoder is chosen by the sniffed mime, not by a second
//     sniff.** image.DecodeConfig would re-sniff through the registry
//     of whatever formats some other package happened to blank-import,
//     which is a second judgement that can disagree with the stored one
//     -- and the stored mime is what the serving route later answers
//     with. Dispatching on the mime this file already decided makes
//     "the format we say it is" and "the format we parsed it as" one
//     decision.
//   - **WebP is parsed here rather than by adding a dependency.**
//     golang.org/x/image/webp is a full lossy/lossless decoder for a
//     job that needs fourteen bits of a chunk header; the parser below
//     reads the first RIFF chunk, bounds-checks every offset against
//     the buffer it already holds, allocates nothing and loops over
//     nothing.
//   - **A header the parsers believe is still bounded.** The three
//     parsers report whatever the file claims, and a claim is free:
//     MaxAssetDimension and MaxAssetPixels refuse an image whose header
//     says it is enormous, because the browser that draws it *will*
//     decode the pixels this process declined to, and 20000x20000 is
//     1.6 GB of RGBA in a designer's tab. The bound protects the
//     client, which is the only reader that expands these bytes.
//   - **The size bound is applied while reading, never after, and it is
//     applied here only.** readBounded stops at MaxAssetBytes+1, so a
//     caller streaming four gigabytes is refused having buffered eight
//     megabytes and one byte. internal/web hands the request body over
//     unwrapped: this comment used to say it wrapped it in
//     http.MaxBytesReader over the same constant, and that wrapper was
//     measured, found unable to fire -- the limited read below takes the
//     byte that would have tripped it -- and removed, which left this
//     sentence describing code that is not there.
//     api_view_assets.go's header carries the whole argument.
//     TestAnOversizeAssetIsRefusedBeforeItIsRead counts the bytes this
//     package pulls from the reader, because "refused" and "refused
//     before it was read" are different claims and only one of them is
//     worth making.
//   - **How many, as well as how big.** MaxAssetsPerGame is the
//     aggregate bound; every other number here is per asset, and for a
//     sub-project a game could hold as many eight-megabyte images as
//     anyone cared to upload.
//   - **The dimension bounds are per canvas, and an animated file has
//     more than one frame. Recorded, not bounded, and here is the
//     judgement.** Both PNG and WebP carry animation -- APNG's acTL
//     chunk, WebP's ANIM/ANMF under a VP8X whose flags say so -- and the
//     header decode above stops before either: png.DecodeConfig returns
//     at IHDR and webpConfig reads the first chunk and no more, so
//     nothing here ever sees a frame count. MaxAssetPixels therefore
//     bounds one canvas and a browser's cost is that canvas times
//     however many frames it decides to hold decoded.
//
//     No bound is added today, for two reasons. MaxAssetBytes already
//     caps the compressed whole at eight megabytes, and every frame's
//     data is inside it -- what animation buys an attacker is the same
//     bytes replayed, not more of them. And the only *cheap* check is
//     the WebP one, a flag bit webpConfig already has in its hand:
//     refusing animated WebP while APNG walks through would be a bound
//     that reads as protection and is not one, which is worse than the
//     gap stated plainly. If a designer's tab is ever measured falling
//     over on an animated background, the change is both formats at
//     once -- the VP8X flag, and a scan for acTL ahead of the first IDAT
//     -- and it belongs with a test per format.
//
// **This file is the second write path over a view's row, and it stays
// the narrow one.** UpsertView is the only place a query and its
// renderer parameters are written together, which is what makes
// CheckRenderer's single caller enough to guarantee that no stored view
// is undrawable. SetBackground writes the three background columns and
// nothing else -- no renderer parameter, no query -- so that guarantee
// is untouched. What it must not become is a general setter; views.go
// says the same where the invariant is stated.

// The three mimes an asset may have, matching 0008_views.sql's CHECK
// exactly. The CHECK is the backstop for a write path that does not come
// through this file; this list is what decides.
const (
	MimePNG  = "image/png"
	MimeJPEG = "image/jpeg"
	MimeWebP = "image/webp"
)

// AssetMimes is the closed list, in the order a refusal names them.
func AssetMimes() []string { return []string{MimePNG, MimeJPEG, MimeWebP} }

const (
	// MaxAssetBytes is the most one asset may weigh. Eight megabytes is
	// a generous world map and a bounded thing to hold in memory: an
	// upload is buffered whole, because both the sniff and the header
	// decode need the front of the file and the insert needs all of it.
	MaxAssetBytes = 8 << 20
	// MaxAssetDimension and MaxAssetPixels bound what the header may
	// claim, for the client's sake rather than for this process's -- see
	// this file's header. Twenty thousand a side and forty megapixels
	// leave room for any hand-drawn world map (8000x5000 is 40 MP) and
	// refuse the shapes a decompression bomb takes: the enormous square,
	// and the one-pixel-tall strip a square bound would let through.
	MaxAssetDimension = 20000
	MaxAssetPixels    = 40_000_000
	// MaxAssetFilenameLen bounds the filename, which is prose a designer
	// reads in a picker and is never interpreted -- not as a path, not as
	// a mime hint. It is capped in runes, through metamodel.LengthProblem,
	// for the reason MaxViewNameLen is.
	MaxAssetFilenameLen = 200
	// MaxAssetsPerGame is how many background images one game may hold,
	// and it is the bound every other bound in this file was missing.
	//
	// **Every other one is per asset.** Eight megabytes an image, forty
	// megapixels a canvas -- and nothing at all on how many. Any writer
	// with an editor role, which includes an agent's token, could push
	// unbounded bytes into Postgres eight megabytes at a time and no
	// statement in this package would notice. The file header argues at
	// length that MaxAssetBytes protects *memory*; nothing protected
	// disk.
	//
	// **A count rather than an aggregate byte total, and the reason is
	// the bytea.** bytes is a toasted, compressed column: summing
	// octet_length over it makes Postgres detoast every stored image on
	// every upload, so the cheaper-looking bound is the one that reads
	// the whole game's pixels to decide whether to accept eight
	// megabytes. A count is answered from view_assets_project_idx
	// without touching an image (CountViewAssets says the same where the
	// statement is), and it composes with MaxAssetBytes into a worst
	// case that can be stated rather than measured: a hundred assets of
	// eight megabytes is 800 MB a game, which is a number an operator
	// can multiply by their number of games and reason about.
	//
	// A hundred is generous for what this is: one background per zone of
	// a hand-drawn world, or one circuit map per track. A game that
	// really needs more is a conversation with whoever runs the
	// instance, which is what a refusal naming the cap starts.
	MaxAssetsPerGame = 100
)

// The bounds on one asset listing. A game holds tens of assets rather
// than thousands -- MaxAssetsPerGame is a hundred -- so the default page
// shows most games' whole library in one call and the cap is a little
// above the per-game limit, which means a caller that asks for
// everything gets everything and still gets a cursor if the cap ever
// rises.
const (
	defaultAssetPage int32 = 50
	maxAssetPage     int32 = 200
)

// Asset is one background image as it reads back, without its bytes.
//
// Width, Height and Mime are decoded and sniffed values, not arguments,
// which is why views.list_assets returns them and why
// TestWidthAndHeightAreDecodedAndReadBack reads them back through this
// struct: a column filled by a decoder nobody ever reads is a decoder
// nobody can prove ran.
type Asset struct {
	ID        uuid.UUID
	Filename  string
	Mime      string
	Width     int32
	Height    int32
	CreatedAt time.Time
}

// AssetBytes is one asset with its bytes, for the serving route.
type AssetBytes struct {
	Asset
	Bytes []byte
}

// Point is the [x, y] a background's top-left corner sits at, stored as
// the object 0008_views.sql declares (`{"x":0,"y":0}`) rather than as a
// two-element array.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// DefaultBackgroundScale and DefaultBackgroundOffset are
// 0008_views.sql's column defaults, spelled here for the reason
// DefaultLayoutSeed is: SetBackground writes all three columns on every
// call, so a caller clearing a background must have something to write.
const DefaultBackgroundScale = 1.0

// DefaultBackgroundOffset is the origin.
func DefaultBackgroundOffset() Point { return Point{} }

// CreateAsset stores one background image, judging it entirely from its
// own bytes.
//
// filename is prose: it is stored so a designer can recognise the image
// in a picker, and it is read by nothing else. It is not a path, it is
// not consulted for the mime, and it is not echoed into any response
// header -- the serving route sends a Content-Type and no
// Content-Disposition, so there is no filename-injection surface to
// bound in the first place.
//
// The order of the four judgements is the order a caller can act on
// them: the filename, then the size, then the format, then the
// dimensions. A caller whose 40 MB SVG is also called `<script>` hears
// about the name first, which is the rule every other call in this
// package follows.
func (s *Service) CreateAsset(ctx context.Context, projectID uuid.UUID, actor Actor,
	filename string, body io.Reader,
) (Asset, error) {
	if problems := assetFilenameProblems(filename); len(problems) > 0 {
		return Asset{}, &metamodel.ValidationError{
			Code: metamodel.CodeInvalidInput, Fields: problems,
		}
	}
	raw, err := readBounded(body)
	if err != nil {
		return Asset{}, err
	}
	mime := sniffedMime(raw)
	if mime == "" {
		return Asset{}, unreadableAsset(raw)
	}
	width, height, err := assetDimensions(mime, raw)
	if err != nil {
		return Asset{}, err
	}

	var row dbq.InsertViewAssetRow
	err = s.withTx(ctx, func(q *dbq.Queries) error {
		// **The per-game cap, counted in the transaction that inserts.**
		// Not because that makes it exact -- two uploads racing each
		// other both count MaxAssetsPerGame-1 and both land, so the
		// stored total can overshoot by the number of concurrent
		// uploaders -- but because a count taken outside the transaction
		// can be stale by any amount at all. This is a quota rather than
		// a security boundary: what it exists to stop is unbounded
		// growth, and a bound that can be exceeded by the handful of
		// browsers a game has open at once still stops that. Locking the
		// game to make it exact would serialise every upload in it
		// against a number nobody reads.
		held, err := q.CountViewAssets(ctx, projectID)
		if err != nil {
			return fmt.Errorf("count view assets: %w", err)
		}
		if held >= MaxAssetsPerGame {
			return &metamodel.ValidationError{
				Code: metamodel.CodeInvalidInput,
				Fields: []metamodel.FieldError{{
					Path: pointer("bytes"),
					Message: fmt.Sprintf("would be background image %d and a game may "+
						"hold %d: delete an image this game no longer draws over, with "+
						"the asset route, before uploading another",
						held+1, MaxAssetsPerGame),
				}},
			}
		}

		row, err = q.InsertViewAsset(ctx, dbq.InsertViewAssetParams{
			ProjectID: projectID, Filename: filename, Mime: mime,
			Width: width, Height: height, Bytes: raw,
			CreatedByUserID: actor.UserID, CreatedByTokenID: actor.TokenID,
		})
		if err != nil {
			// The same mapping every write in this repository carries: a
			// token from another game trips a composite foreign key, and
			// SQLSTATE 23503 over a generated constraint name says nothing
			// about a credential scoped to the wrong project.
			if mapped := metamodel.ActorConstraintViolation(err); errors.Is(mapped, ErrActorNotInGame) {
				return mapped
			}
			return fmt.Errorf("insert view asset: %w", err)
		}
		return nil
	})
	if err != nil {
		return Asset{}, err
	}
	return Asset{
		ID: row.ID, Filename: row.Filename, Mime: row.Mime,
		Width: row.Width, Height: row.Height, CreatedAt: row.CreatedAt.Time,
	}, nil
}

// AssetFilter narrows an asset listing. There is nothing to filter on --
// an asset has no key, no kind and no owner -- so it carries a position
// and a size and nothing else.
//
// Cursor is the NextCursor of a previous call. It belongs to the game it
// was issued for and to no other, and it is a position rather than a
// snapshot; paging.Cursor carries the whole contract.
type AssetFilter struct {
	Cursor string
	Limit  int32
}

// AssetPage is one page of a game's assets plus the cursor for the next.
//
// **NextCursor is set when the page came back full**, and empty
// otherwise, so a caller looping until it is empty is correct and must
// expect a final empty page rather than treating one as an error. The
// rest of the contract is paging.Cursor's, stated once for every domain
// that pages. Cursor.Sort, for this listing, is the row's created_at in
// RFC 3339 -- the same shape internal/metamodel's relation listing uses,
// and for the same reason: the sort key is a timestamp and ties on it
// are ordinary, so the id is what divides them.
type AssetPage struct {
	Assets     []Asset
	NextCursor string
}

// ListAssets is one page of a game's assets, oldest first, without bytes.
//
// **It used to be every asset, with no limit at all** -- the whole
// game's library in one answer, in a package whose query file contained
// exactly one LIMIT -- while view_assets_project_idx's own comment said
// its order existed so a page could be sought to rather than read whole
// and sorted. It is paged now, through the same mechanism every other
// listing in this product uses.
func (s *Service) ListAssets(ctx context.Context, projectID uuid.UUID,
	f AssetFilter,
) (AssetPage, error) {
	limit := paging.Size(f.Limit, defaultAssetPage, maxAssetPage)
	fingerprint := assetListingFingerprint(projectID)
	after, err := paging.Decode(f.Cursor, fingerprint, refuseViewCursor)
	if err != nil {
		return AssetPage{}, err
	}

	params := dbq.ListViewAssetsPageParams{ProjectID: projectID, Limit: limit}
	if after.ID != uuid.Nil {
		at, err := time.Parse(time.RFC3339Nano, after.Sort)
		if err != nil {
			// Only a hand-edited cursor reaches this: the encode below
			// writes the format this parses and the fingerprint has
			// already agreed. It is still the caller's own argument.
			return AssetPage{}, refuseViewCursor("is not a cursor this listing issued: " +
				"it carries no creation time")
		}
		params.AfterCreatedAt = pgtype.Timestamptz{Time: at, Valid: true}
		params.AfterID = &after.ID
	}

	rows, err := s.q.ListViewAssetsPage(ctx, params)
	if err != nil {
		return AssetPage{}, fmt.Errorf("list view assets: %w", err)
	}
	page := AssetPage{Assets: make([]Asset, 0, len(rows))}
	for _, row := range rows {
		page.Assets = append(page.Assets, Asset{
			ID: row.ID, Filename: row.Filename, Mime: row.Mime,
			Width: row.Width, Height: row.Height, CreatedAt: row.CreatedAt.Time,
		})
	}
	// paging.Size never returns a limit below one, so a full page is
	// never an empty one and there is no separate emptiness check.
	if len(rows) == int(limit) {
		last := rows[len(rows)-1]
		page.NextCursor = paging.Encode(paging.Cursor{
			Sort:        last.CreatedAt.Time.Format(time.RFC3339Nano),
			ID:          last.ID,
			Fingerprint: fingerprint,
		})
	}
	return page, nil
}

// assetListingFingerprint digests the listing a cursor was issued under.
//
// The project id first and the domain discriminator second, for the
// reasons viewListingFingerprint sets out at length: without the project
// id two games' listings share a fingerprint and one game's cursor pages
// the other's rows from a position that means nothing there, and without
// a discriminator this listing's cursor and the view listing's over the
// same game would be interchangeable. There is no filter part because
// this listing has no filter.
func assetListingFingerprint(projectID uuid.UUID) string {
	return paging.Fingerprint(projectID.String(), "view_assets")
}

// ReadAsset loads one asset with its bytes, for the serving route.
//
// The bare sentinel rather than a named key, exactly as ViewByID: the
// caller already holds the id it sent and there is no second argument
// for it to tell apart.
func (s *Service) ReadAsset(ctx context.Context, projectID, id uuid.UUID) (AssetBytes, error) {
	row, err := s.q.GetViewAsset(ctx, dbq.GetViewAssetParams{ProjectID: projectID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return AssetBytes{}, ErrNotFound
	}
	if err != nil {
		return AssetBytes{}, fmt.Errorf("read view asset: %w", err)
	}
	return AssetBytes{
		Asset: Asset{
			ID: row.ID, Filename: row.Filename, Mime: row.Mime,
			Width: row.Width, Height: row.Height, CreatedAt: row.CreatedAt.Time,
		},
		Bytes: row.Bytes,
	}, nil
}

// RemoveAsset deletes one asset, and with it every background drawn from
// it.
//
// **The division of labour is deliberate and is what the test reads.**
// 0008_views.sql's composite FOREIGN KEY carries
// ON DELETE SET NULL (background_asset_id), so the constraint -- not
// this function -- is what detaches the image from every view using it;
// a view keeps its row and loses its picture. What this function does
// first, in the same transaction, is reset those views' scale and
// offset, because the constraint cannot: it would leave two knobs
// placing an image that is gone, which is the state SetBackground
// refuses to create and which nothing would ever read again.
//
// The order matters and is the reason there is a transaction at all: the
// reset has to run while background_asset_id still names the asset,
// since that column is how the views are found. Nulling the id here as
// well would make the constraint unobservable through this package,
// which is the opposite of what a constraint is for.
func (s *Service) RemoveAsset(ctx context.Context, projectID, id uuid.UUID) error {
	return s.withTx(ctx, func(q *dbq.Queries) error {
		if err := q.ClearBackgroundKnobsForAsset(ctx, dbq.ClearBackgroundKnobsForAssetParams{
			ProjectID: projectID, BackgroundAssetID: id,
		}); err != nil {
			return fmt.Errorf("clear background knobs: %w", err)
		}
		removed, err := q.DeleteViewAsset(ctx, dbq.DeleteViewAssetParams{
			ProjectID: projectID, ID: id,
		})
		if err != nil {
			return fmt.Errorf("delete view asset: %w", err)
		}
		if removed == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// BackgroundInput is what SetBackground writes.
//
// AssetID is a pointer because nil is a value a caller means: it clears
// the background, which is the only way to remove one. The same
// distinction PositionInput.Pinned and ViewInput.LayoutSeed draw, for
// the same reason -- a zero value that means "said nothing" cannot also
// mean "said none".
//
// Scale and Offset are pointers for the narrower half of that rule: a
// scale of 0 is refused anyway (the column CHECKs `> 0`), but an offset
// of {0,0} is the default and a legal thing to ask for, and a caller
// that says nothing about either should get the defaults rather than
// have the previous background's arithmetic apply to a new image.
type BackgroundInput struct {
	AssetID *uuid.UUID
	Scale   *float64
	Offset  *Point
}

// SetBackground points one view at one asset, with a scale and an offset.
//
// **It is the second write path over a view's row, and it stays narrow
// on purpose.** It writes the three background columns and nothing else.
// UpsertView is the only path that writes a query and its renderer
// parameters together, which is what makes CheckRenderer's single caller
// a guarantee that no stored view is undrawable; a setter here that
// grew into a renderer-parameter setter would lose that guarantee
// silently. views.go states the invariant where it is created.
//
// **Three rules, each carried from somewhere it was already decided
// rather than invented here.**
//
//  1. *A value nothing reads is refused.* A background belongs to a
//     renderer that draws one, and only `map` does: stored under any
//     other renderer it changes no picture, which is the lie
//     renderers.go's whole catalogue exists to refuse. The catalogue
//     says which renderers read one (Renderer.ReadsBackground), so this
//     rule has one source and cannot drift from the description an agent
//     reads.
//  2. *The same rule, one parameter along.* A scale or an offset with no
//     asset places an image that is not there. That was `map`'s own
//     Requires rule while the background lived in renderer_params; it
//     moved here with the columns rather than being restated.
//  3. *The asset is this game's.* Resolved by id inside the project for
//     the message, and refused by 0008_views.sql's composite foreign key
//     regardless -- the constraint is the guarantee, the lookup is the
//     sentence.
//
// **What this call does to the audit columns, decided: nothing.** It
// leaves version alone, for the reason above, and it leaves
// updated_by_user_id and updated_by_token_id alone as well -- while the
// timestamp trigger fires, so a view reads as modified with its
// attribution still pointing at whoever last edited the query. That
// asymmetry is the lesser of the two: `updated_by` on views answers
// "who wrote what this view says", which is the query document and the
// renderer parameters, and it is the pair a reader compares against a
// version they hold; repointing it at someone who changed no part of
// that document would make one row disagree with itself. Placement
// carries no attribution anywhere here -- view_positions has no audit
// columns either, and a dragged node is the same kind of act -- so
// "who moved the map" is a column on the placement, in the change that
// wants it for positions too. The statement says the same.
func (s *Service) SetBackground(ctx context.Context, projectID uuid.UUID, viewKey string,
	in BackgroundInput,
) error {
	view, err := s.ViewByKey(ctx, projectID, viewKey)
	if err != nil {
		return err
	}
	if problems := backgroundProblems(view.Renderer, in); len(problems) > 0 {
		return &metamodel.ValidationError{
			Code: metamodel.CodeInvalidInput, Fields: problems,
		}
	}
	if in.AssetID != nil {
		if _, err := s.q.GetViewAssetMeta(ctx, dbq.GetViewAssetMetaParams{
			ProjectID: projectID, ID: *in.AssetID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: no asset %s in this game: upload it through "+
					"the browser first, or pick one views.list_assets reports",
					ErrNotFound, *in.AssetID)
			}
			return fmt.Errorf("read view asset: %w", err)
		}
	}

	scale := DefaultBackgroundScale
	if in.Scale != nil {
		scale = *in.Scale
	}
	offset := DefaultBackgroundOffset()
	if in.Offset != nil {
		offset = *in.Offset
	}
	encoded, err := json.Marshal(offset)
	if err != nil {
		return fmt.Errorf("encode background offset: %w", err)
	}
	written, err := s.q.SetViewBackground(ctx, dbq.SetViewBackgroundParams{
		ProjectID: projectID, ID: view.ID,
		BackgroundAssetID: in.AssetID,
		BackgroundScale:   scale,
		BackgroundOffset:  encoded,
	})
	if err != nil {
		return fmt.Errorf("set view background: %w", err)
	}
	if written == 0 {
		// Unreachable from here -- the view was resolved inside this
		// game one statement ago -- and kept for the reason
		// SetPositions keeps its own row-count arm: discarding a count
		// that can only be zero when an invariant of this package is
		// broken is how a silent no-op reaches a green test run.
		return fmt.Errorf("%w: no view %q in this game", ErrNotFound, viewKey)
	}
	return nil
}

// backgroundProblems judges one SetBackground call whole, so a caller
// that is wrong twice hears both.
func backgroundProblems(renderer string, in BackgroundInput) []metamodel.FieldError {
	problems := make([]metamodel.FieldError, 0)
	if in.AssetID == nil {
		// Rule 2: a knob that places an image, with no image. Reported
		// per knob, at the knob, because the repair is per knob.
		if in.Scale != nil {
			problems = append(problems, metamodel.FieldError{
				Path: pointer("scale"),
				Message: "scales the background image and this call sets none: send an " +
					"asset_id, or leave the scale out",
			})
		}
		if in.Offset != nil {
			problems = append(problems, metamodel.FieldError{
				Path: pointer("offset"),
				Message: "places the background image and this call sets none: send an " +
					"asset_id, or leave the offset out",
			})
		}
		// Clearing a background is legal under every renderer: a view
		// whose renderer changed away from map must be able to drop the
		// image it can no longer draw.
		return problems
	}
	if !RendererReadsBackground(renderer) {
		problems = append(problems, metamodel.FieldError{
			Path: pointer("asset_id"),
			// The repair names the order the two calls have to be made
			// in, because the other one is refused as well: UpsertView
			// enforces the same rule from its own side, so "change the
			// renderer, then set the background" is the only sequence
			// that works and the previous wording — which named
			// views.upsert with no order — pointed at the call that
			// produced the forbidden state.
			Message: fmt.Sprintf("is a background image and this view's renderer is %q, "+
				"which draws none: only %q reads a background, so the image would be "+
				"stored and read by nothing. Change the renderer to %q through "+
				"views.upsert first, then set this background — or leave it unset",
				renderer, RendererMap, RendererMap),
		})
	}
	if in.Scale != nil {
		if problem := scaleProblem(*in.Scale); problem != "" {
			problems = append(problems, metamodel.FieldError{
				Path: pointer("scale"), Message: problem,
			})
		}
	}
	if in.Offset != nil {
		for _, part := range []struct {
			name  string
			value float64
		}{{"x", in.Offset.X}, {"y", in.Offset.Y}} {
			if problem := coordinateProblem(part.value); problem != "" {
				problems = append(problems, metamodel.FieldError{
					Path: pointer("offset", part.name), Message: problem,
				})
			}
		}
	}
	return problems
}

// scaleProblem refuses the scales 0008_views.sql's CHECK refuses, ahead
// of the CHECK, for the reason every bound in this sub-project is
// checked in Go first: a constraint violation arrives as an untyped
// SQLSTATE 23514 over a value the caller supplied and reaches an agent
// as internal_error.
func scaleProblem(scale float64) string {
	if math.IsNaN(scale) || math.IsInf(scale, 0) {
		return "is not a finite number: a scale says how many coordinate units one " +
			"pixel of the background is"
	}
	if scale <= 0 {
		return fmt.Sprintf("is %g: a scale must be greater than zero, because a "+
			"background at zero scale has no size", scale)
	}
	return ""
}

// assetFilenameProblems bounds the filename, which is prose and nothing
// else. Required: an asset has no key, so the name is the only thing a
// designer picking one out of a list has to go on.
func assetFilenameProblems(filename string) []metamodel.FieldError {
	if filename == "" {
		return []metamodel.FieldError{{
			Path: pointer("filename"),
			Message: "is required: an asset has no key, so this is the only thing a " +
				"designer picking one out of a list has to go on",
		}}
	}
	return oneValue(pointer("filename"),
		checkStorableText(filename, MaxAssetFilenameLen, false))
}

// readBounded reads at most MaxAssetBytes+1 bytes and refuses at the
// bound.
//
// **The +1 is the whole mechanism**: reading exactly the cap cannot tell
// a file of exactly eight megabytes from the first eight megabytes of a
// larger one, and the difference between those two is a stored truncated
// image. Reading one byte past is what makes "too big" observable, and
// it is the most this ever holds -- a caller streaming four gigabytes is
// refused having buffered 8 MB and one byte, not four gigabytes.
// TestAnOversizeAssetIsRefusedBeforeItIsRead counts what this pulls from
// the reader, because "refused" and "refused before it was read" are
// different claims.
func readBounded(body io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(body, MaxAssetBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: /bytes: could not be read: %v",
			ErrInvalidInput, err)
	}
	if len(raw) > MaxAssetBytes {
		return nil, &metamodel.ValidationError{
			Code: metamodel.CodeInvalidInput,
			Fields: []metamodel.FieldError{{
				Path: pointer("bytes"),
				Message: fmt.Sprintf("is larger than %d bytes, which is the most one "+
					"background image may weigh: scale the image down or save it at a "+
					"lower quality", MaxAssetBytes),
			}},
		}
	}
	if len(raw) == 0 {
		return nil, &metamodel.ValidationError{
			Code: metamodel.CodeInvalidInput,
			Fields: []metamodel.FieldError{{
				Path:    pointer("bytes"),
				Message: "is empty: an asset is an image, and there are no bytes here",
			}},
		}
	}
	return raw, nil
}

// The magic numbers of the three admitted formats.
//
// An allowlist, and the reason is internal/markdown's: a denylist of
// "svg" and whatever else looked dangerous at the time is one novel
// format away from being wrong, and the formats a map needs are three.
var (
	magicPNG = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	// JPEG: SOI followed by the first marker's 0xFF. Every JPEG a camera
	// or an editor writes starts this way, and three bytes is what tells
	// it from an arbitrary file beginning 0xFF 0xD8.
	magicJPEG = []byte{0xff, 0xd8, 0xff}
	// WebP is a RIFF container: "RIFF", a four-byte length this file
	// never trusts, then "WEBP".
	magicRIFF = []byte("RIFF")
	magicWEBP = []byte("WEBP")
)

// sniffedMime reads the format out of the bytes, or answers "" for
// anything that is not one of the three.
//
// **It is never told what to expect.** No Content-Type, no filename, no
// caller argument reaches it, which is why an SVG labelled image/png and
// named world-map.png is refused: the only thing consulted is the front
// of the file. TestTheMimeIsSniffedNotTrusted and
// TestAnSVGIsRefusedWhateverItCallsItself are the two halves of that.
func sniffedMime(raw []byte) string {
	switch {
	case bytes.HasPrefix(raw, magicPNG):
		return MimePNG
	case bytes.HasPrefix(raw, magicJPEG):
		return MimeJPEG
	case len(raw) >= 12 && bytes.HasPrefix(raw, magicRIFF) &&
		bytes.Equal(raw[8:12], magicWEBP):
		return MimeWebP
	}
	return ""
}

// unreadableAsset is the refusal for bytes that are none of the three
// formats.
//
// **svgLooking refines the wording and nothing else.** The decision was
// already made by sniffedMime's allowlist before this is called, so no
// bug in the recognition below can admit anything -- it can only make a
// designer who uploaded the file every designer has hear the sentence
// that helps. That ordering is deliberate: a denylist that decides is
// the defect internal/markdown recorded, and a denylist that only
// chooses a sentence is a better error message.
func unreadableAsset(raw []byte) error {
	message := fmt.Sprintf("are not a %s, a %s or a %s: the format is read from the "+
		"bytes themselves, never from the file's name or the Content-Type it was sent "+
		"with", MimePNG, MimeJPEG, MimeWebP)
	if svgLooking(raw) {
		message = "look like an SVG, which Maestro does not accept as a background: an " +
			"SVG served to a browser can carry script, and a map is a raster image " +
			"anyway. Export it as a PNG, a JPEG or a WebP and upload that"
	}
	return &metamodel.ValidationError{
		Code: metamodel.CodeInvalidInput,
		Fields: []metamodel.FieldError{{
			Path: pointer("bytes"), Message: message,
		}},
	}
}

// svgLooking answers whether the front of the file reads like SVG or
// XML. Wording only -- see unreadableAsset.
func svgLooking(raw []byte) bool {
	head := raw
	if len(head) > 1024 {
		head = head[:1024]
	}
	// The cutset carries a UTF-8 byte order mark as well as whitespace:
	// an editor that writes one puts it ahead of the "<?xml", and this is
	// wording rather than a decision either way.
	head = bytes.ToLower(bytes.TrimLeft(head, " \t\r\n\ufeff"))
	return bytes.Contains(head, []byte("<svg")) || bytes.HasPrefix(head, []byte("<?xml"))
}

// assetDimensions reads the width and the height out of the image
// header, with the parser chosen by the mime this file already sniffed.
//
// See the header for why nothing here decodes pixels and why the mime
// picks the parser rather than a second sniff picking it again.
func assetDimensions(mime string, raw []byte) (int32, int32, error) {
	var (
		width, height int
		err           error
	)
	switch mime {
	case MimePNG:
		var config image.Config
		config, err = png.DecodeConfig(bytes.NewReader(raw))
		width, height = config.Width, config.Height
	case MimeJPEG:
		var config image.Config
		config, err = jpeg.DecodeConfig(bytes.NewReader(raw))
		width, height = config.Width, config.Height
	case MimeWebP:
		width, height, err = webpConfig(raw)
	default:
		// Unreachable: the caller has already refused every other mime.
		// Kept because a silent zero would be a width nothing decoded.
		return 0, 0, fmt.Errorf("no dimension parser for %q", mime)
	}
	if err != nil {
		return 0, 0, brokenImage(mime, err.Error())
	}
	if problem := dimensionProblem(width, height); problem != "" {
		return 0, 0, brokenImage(mime, problem)
	}
	//nolint:gosec // G115: dimensionProblem above has just refused every
	// value outside 1..MaxAssetDimension (20000), on both sides, so
	// neither conversion can overflow — that bound is the reason the
	// check runs before the conversion rather than after it.
	return int32(width), int32(height), nil
}

// dimensionProblem bounds what the header claims. See this file's header
// for whose problem an enormous header actually is.
func dimensionProblem(width, height int) string {
	if width <= 0 || height <= 0 {
		return fmt.Sprintf("its header says it is %dx%d, which is no image at all",
			width, height)
	}
	if width > MaxAssetDimension || height > MaxAssetDimension {
		return fmt.Sprintf("its header says it is %dx%d and no side may be more than "+
			"%d pixels", width, height, MaxAssetDimension)
	}
	if width*height > MaxAssetPixels {
		return fmt.Sprintf("its header says it is %dx%d, which is %d pixels, and the "+
			"most a background may hold is %d: a browser expands those bytes into "+
			"memory even though Maestro does not",
			width, height, width*height, MaxAssetPixels)
	}
	return ""
}

// brokenImage is the refusal for bytes whose magic number matched and
// whose header did not.
func brokenImage(mime, why string) error {
	return &metamodel.ValidationError{
		Code: metamodel.CodeInvalidInput,
		Fields: []metamodel.FieldError{{
			Path:    pointer("bytes"),
			Message: fmt.Sprintf("are a %s whose size could not be read: %s", mime, why),
		}},
	}
}

// webpConfig reads a WebP canvas size out of the first RIFF chunk.
//
// Three shapes, all of them header arithmetic: VP8X carries the canvas
// size as two 24-bit values minus one; VP8 (lossy) carries a 14-bit
// width and height after the three-byte start code; VP8L (lossless)
// packs both into 28 bits after its signature byte. Nothing here loops,
// nothing allocates, and every offset is checked against the buffer
// before it is read -- the chunk length the file declares is deliberately
// never used to seek, because a length is the one number an attacker
// picks freely.
func webpConfig(raw []byte) (int, int, error) {
	// 12 bytes of RIFF header, 8 bytes of chunk header.
	if len(raw) < 20 {
		return 0, 0, errors.New("the file ends before its first chunk header")
	}
	fourcc := string(raw[12:16])
	body := raw[20:]
	switch fourcc {
	case "VP8X":
		if len(body) < 10 {
			return 0, 0, errors.New("its VP8X chunk ends before the canvas size")
		}
		width := int(body[4]) | int(body[5])<<8 | int(body[6])<<16
		height := int(body[7]) | int(body[8])<<8 | int(body[9])<<16
		return width + 1, height + 1, nil
	case "VP8 ":
		if len(body) < 10 {
			return 0, 0, errors.New("its VP8 chunk ends before the frame header")
		}
		if body[3] != 0x9d || body[4] != 0x01 || body[5] != 0x2a {
			return 0, 0, errors.New("its VP8 frame carries no start code")
		}
		width := int(binary.LittleEndian.Uint16(body[6:8]) & 0x3fff)
		height := int(binary.LittleEndian.Uint16(body[8:10]) & 0x3fff)
		return width, height, nil
	case "VP8L":
		if len(body) < 5 {
			return 0, 0, errors.New("its VP8L chunk ends before the image size")
		}
		if body[0] != 0x2f {
			return 0, 0, errors.New("its VP8L chunk carries no signature byte")
		}
		bits := binary.LittleEndian.Uint32(body[1:5])
		return int(bits&0x3fff) + 1, int((bits>>14)&0x3fff) + 1, nil
	}
	return 0, 0, fmt.Errorf("its first chunk is %q, which carries no image size", fourcc)
}
