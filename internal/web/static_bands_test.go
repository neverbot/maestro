package web_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

func homeShell(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("static", "game.html"))
	assert.NoErr(t, err, "read game.html")
	return string(raw)
}

func homeStyles(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("static", "styles.css"))
	assert.NoErr(t, err, "read styles.css")
	return string(raw)
}

// **A screen made of bands is a stack of peers.** docs/design.md's Band
// Rule says so, and the home shipped the connections band as a heading
// one step down *inside* the content band — a claim about the metamodel
// nobody meant to make, since entity types and relation types are two
// halves of one pair. A reader read the only legible cue, the face, and
// concluded the connections belonged to the content. They were reading
// correctly: the markup nested them.
func TestTheHomesBandsArePeers(t *testing.T) {
	t.Parallel()
	shell := homeShell(t)

	sections := regexp.MustCompile(`<section class="lane[^"]*"`).FindAllString(shell, -1)
	assert.Must(t, len(sections) == 4, "the home has %d bands and the Band Rule wants four peers: %v", len(sections), sections)

	// No heading under a heading. A rank carried by one step of the type
	// scale is 1.11 and cannot say "inside"; a rank carried by a face
	// says "the game's words", which is the Two Voices Rule's meaning
	// and not this one.
	lower := regexp.MustCompile(`(?s)<h[3-6][\s>]`).FindAllString(shell, -1)
	assert.Must(t, len(lower) == 0, "the home ranks a band under another with %v", lower)

	// The two halves of the metamodel are the pair, and they are marked
	// as such in the markup rather than found by position.
	paired := strings.Count(shell, `class="lane lane-paired"`)
	assert.Must(t, paired == 2, "the home marks %d bands as paired and the pair is two", paired)
}

// Every band says what the reader is looking at, standing, and says it
// through the catalogue like every other string on the screen. The
// sentence is in the shell because it never varies: not with the data,
// not with who is reading.
func TestEveryBandCarriesItsOwnSentence(t *testing.T) {
	t.Parallel()
	shell := homeShell(t)
	notes := regexp.MustCompile(`<p class="band-note" data-i18n="([^"]+)"></p>`).FindAllStringSubmatch(shell, -1)
	assert.Must(t, len(notes) == 4, "%d bands carry a sentence and the home has four", len(notes))

	source := catalogue(t, sourceLocale)
	for _, note := range notes {
		line, ok := source[note[1]]
		assert.Must(t, ok, "%s is on the page and in no catalogue", note[1])
		// Long enough to be a sentence and short enough not to be prose:
		// the measure the stylesheet caps it at is 62ch.
		assert.Must(t, len(line) > 40 && len(line) <= 140, "%s is %d characters: %q", note[1], len(line), line)
	}
}

// **A band is told from the band above it by a line, not by a size.**
// The separation was 40px of space and nothing else, and both
// docs/design.md and docs/product.md name rule lines as the instrument
// for exactly this.
func TestBandsAreSeparatedByARule(t *testing.T) {
	t.Parallel()
	styles := homeStyles(t)
	// The declaration has to be *in that block*: `border-top` appears a
	// dozen times in this stylesheet, so a guard that only greps the file
	// passes with the rule deleted — which is how this guard was first
	// written, and it stayed green through the mutation that proves it.
	band := regexp.MustCompile(`(?ms)^\.lane \+ \.lane \{(.*?)\}`).FindStringSubmatch(styles)
	assert.Must(t, band != nil, "the stylesheet has no rule between one band and the next")
	assert.Must(t, strings.Contains(band[1], "border-top: 1px solid var(--line)"),
		"a band is separated from the one above it by %q, which is air", strings.TrimSpace(band[1]))
	// The h3 role had one caller on one screen, and that caller is a band
	// of its own now. A type role nothing reads is a role the page does
	// not have.
	assert.Must(t, !strings.Contains(styles, ".lane h3"), "the stylesheet still declares .lane h3")
}

// **The pair shares one measurement, or it has lost the comparison it
// exists for.** Two sibling grids share nothing: side by side, the two
// catalogues had their counts 44px apart and their share bars 45px
// apart, so "47% here against 43% there" could not be read across the
// gap. The override declares every track after the name, and it declares
// as many tracks as a row appends cells.
func TestThePairedCataloguesShareOneMeasurement(t *testing.T) {
	t.Parallel()
	styles := homeStyles(t)
	paired := regexp.MustCompile(`(?s)\.lanes\.pair \.lane-paired ul\.catalogue \{.*?grid-template-columns:\s*([^;]+);`).FindStringSubmatch(styles)
	assert.Must(t, paired != nil, "the pair declares no shared grid, so its two halves size independently")

	tracks := strings.Fields(strings.Join(strings.Fields(paired[1]), " "))
	// The list's own template is the count this has to match: a row's
	// cells sit in the list's tracks through `subgrid`, so a template one
	// track short moves every column after it.
	base := regexp.MustCompile(`(?ms)^ul\.catalogue \{.*?grid-template-columns:\s*([^;]+);`).FindStringSubmatch(styles)
	assert.Must(t, base != nil, "the catalogue declares no grid at all; this guard is reading the wrong place")
	want := len(strings.Fields(regexp.MustCompile(`\(\s*[^)]*\s*\)`).ReplaceAllString(base[1], "()")))
	got := len(strings.Fields(regexp.MustCompile(`\(\s*[^)]*\s*\)`).ReplaceAllString(strings.Join(tracks, " "), "()")))
	assert.Must(t, got == want, "the pair declares %d tracks and a catalogue row sits in %d", got, want)
}
