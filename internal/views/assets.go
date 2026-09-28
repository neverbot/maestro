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
// DefaultPinned is: SetBackground writes all three columns on every
// call, so a caller clearing a background must have something to write.
const DefaultBackgroundScale = 1.0

// DefaultBackgroundOffset is the origin.
func DefaultBackgroundOffset() Point { return Point{} }

// CreateAsset stores one background image, judging it entirely from its
// own bytes.
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
type AssetFilter struct {
	Cursor string
	Limit  int32
}

// AssetPage is one page of a game's assets plus the cursor for the next.
type AssetPage struct {
	Assets     []Asset
	NextCursor string
}

// ListAssets is one page of a game's assets, oldest first, without bytes.
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
func assetListingFingerprint(projectID uuid.UUID) string {
	return paging.Fingerprint(projectID.String(), "view_assets")
}

// ReadAsset loads one asset with its bytes, for the serving route.
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
type BackgroundInput struct {
	AssetID *uuid.UUID
	Scale   *float64
	Offset  *Point
}

// SetBackground points one view at one asset, with a scale and an offset.
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
	// After the write and never before it, exactly as the position
	// writes publish: a background is the map renderer's ground and a
	// browser holding a picture has no other way to learn the ground
	// moved. events.go argues the kind, its payload and its gating; a
	// clear (a nil AssetID) publishes as well as a placement, because
	// removing the ground changes the picture as much as placing one.
	s.publish(projectID, eventViewBackground, viewEventMinRole, viewEventHumanOnly,
		viewPlacementEvent{ID: view.ID, Key: view.Key})
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
