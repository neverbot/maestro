package web_test

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

// The arithmetic half of the interface's identity.

const stylesheetPath = "static/styles.css"

// darkBlockMarker is the boundary between the two token sets. It is
// matched literally rather than by a CSS parser because there is exactly
// one dark block and this test would rather fail loudly the day that
// stops being true than silently guard half a stylesheet.
const darkBlockMarker = "@media (prefers-color-scheme: dark)"

var (
	// The three shapes the root selector now has. The dark set is stated
	// twice — once for a system that asks for it and once for a person
	// who did — and a parser that only knew `:root {` read the second
	// theme as empty and failed every guard in this file with the same
	// line. TestTheTwoDarkBlocksAgree holds the two statements identical.
	rootBlockRE = regexp.MustCompile(
		`(?s):root(?::not\(\[data-theme="light"\]\)|\[data-theme="(?:dark|light)"\])?\s*\{(.*?)\}`)
	declarationRE = regexp.MustCompile(`(?m)^\s*(--[a-z0-9-]+)\s*:\s*([^;]+);`)
	referenceRE   = regexp.MustCompile(`var\(\s*(--[a-z0-9-]+)`)
	// Six digits, or eight: `#rrggbbaa` is a colour with an alpha
	// channel, which is what a halo drawn on paper is, and the rule this
	// matcher serves — only colours have a theme — is as true of one as
	// of the other. It admitted six only, so the first token with an
	// alpha read as "not a colour" and its dark-theme twin was reported
	// as noise.
	hexRE = regexp.MustCompile(`^#(?:[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)
)

// themeTokens reads the two token sets out of styles.css: everything a
// `:root` block before the dark media query declares is the light set,
// everything a `:root` block after it declares is the dark set.
func themeTokens(t *testing.T) (light, dark map[string]string) {
	t.Helper()
	raw, err := os.ReadFile(stylesheetPath)
	assert.Must(t, err == nil, "read %s: %v", stylesheetPath, err)
	src := string(raw)

	boundary := strings.Index(src, darkBlockMarker)
	assert.Must(t, boundary >= 0, "%s declares no %q block: the dark set is half the token layer", stylesheetPath, darkBlockMarker)

	light = map[string]string{}
	dark = map[string]string{}
	for _, loc := range rootBlockRE.FindAllStringSubmatchIndex(src, -1) {
		body := src[loc[2]:loc[3]]
		target := light
		if loc[0] > boundary {
			target = dark
		}
		for _, decl := range declarationRE.FindAllStringSubmatch(body, -1) {
			target[decl[1]] = strings.TrimSpace(decl[2])
		}
	}
	assert.Must(t, len(light) != 0 && len(dark) != 0, "parsed %d light and %d dark tokens: the parser found nothing to guard", len(light), len(dark))
	return light, dark
}

// isColour separates the tokens a theme owns from the tokens it does
// not. A font stack is the same in both themes and redeclaring it in the
// dark block would be noise; a colour is theme-dependent by definition,
// and one declared in only one set is the exact shape that ships as an
// invisible label on a dark ground.
func isColour(value string) bool {
	return hexRE.MatchString(value) || value == "transparent"
}

// TestEveryTokenIsDeclaredInBothThemes compares the two sets in both
// directions: a colour added to :root and forgotten in the dark block
// fails, and so does the reverse.
func TestEveryTokenIsDeclaredInBothThemes(t *testing.T) {
	t.Parallel()
	light, dark := themeTokens(t)

	var missing []string
	for name, value := range light {
		if !isColour(value) {
			continue
		}
		if _, ok := dark[name]; !ok {
			missing = append(missing, name+": declared for the light ground, absent from the dark block")
		}
	}
	for name := range dark {
		if _, ok := light[name]; !ok {
			missing = append(missing, name+": declared in the dark block, absent from :root")
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("%d token(s) exist in one theme only:\n%s", len(missing), strings.Join(missing, "\n"))
	}

	// A non-colour declared in the dark block would pass the loops above
	// (it is in both sets) while meaning nothing; say so rather than let
	// the exemption above become a hiding place.
	for name, value := range dark {
		assert.Should(t, isColour(value), "%s is redeclared in the dark block with a non-colour value %q: only colours have a theme", name, value)
	}
}

// staticAssets returns every stylesheet, script and shell this server
// ships, discovered rather than named, so a file added tomorrow is
// scanned on the day it lands.
func staticAssets(t *testing.T) map[string]string {
	t.Helper()
	assets := map[string]string{}
	err := filepath.WalkDir("static", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch filepath.Ext(path) {
		case ".css", ".js", ".mjs", ".html":
		default:
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		assets[path] = string(raw)
		return nil
	})
	assert.Must(t, err == nil, "walk static: %v", err)
	assert.Must(t, len(assets) != 0, "scanned no assets under internal/web/static: the walk found nothing to guard")
	return assets
}

// stripComments removes what a browser never reads, so that neither
// half of the bidirectional guard below can be satisfied — or broken —
// by prose. Both failure modes are real and this repository has met the
// first one: a token mentioned only in a doc comment would count as used
// and keep an orphan alive forever, and a comment that spells a token
// name in an example would fail the "declared" half for a name no
// stylesheet was ever asked to have.
func stripComments(src string) string {
	var out strings.Builder
	for i := 0; i < len(src); {
		switch {
		case strings.HasPrefix(src[i:], "/*"):
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return out.String()
			}
			i += 2 + end + 2
		case strings.HasPrefix(src[i:], "<!--"):
			end := strings.Index(src[i+4:], "-->")
			if end < 0 {
				return out.String()
			}
			i += 4 + end + 3
		case strings.HasPrefix(src[i:], "//") && (i == 0 || src[i-1] != ':'):
			end := strings.IndexByte(src[i:], '\n')
			if end < 0 {
				return out.String()
			}
			i += end
		default:
			out.WriteByte(src[i])
			i++
		}
	}
	return out.String()
}

// references returns every token any shipped asset reads.
func references(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for path, src := range staticAssets(t) {
		for _, hit := range referenceRE.FindAllStringSubmatch(stripComments(src), -1) {
			out[hit[1]] = append(out[hit[1]], path)
		}
	}
	return out
}

// TestEveryDeclaredTokenIsUsed is the half of the bidirectional guard
// that catches a token layer drifting ahead of the interface: a token
// nothing reads is a lie, and this repository has said so twice in two
// sub-projects.
func TestEveryDeclaredTokenIsUsed(t *testing.T) {
	t.Parallel()
	light, _ := themeTokens(t)
	used := references(t)

	var orphans []string
	for name := range light {
		if len(used[name]) == 0 {
			orphans = append(orphans, name)
		}
	}
	if len(orphans) > 0 {
		sort.Strings(orphans)
		t.Fatalf("%d declared token(s) that no shipped asset reads:\n  %s\n"+
			"a token nothing reads is a lie: delete it, or land it with the code that reads it",
			len(orphans), strings.Join(orphans, "\n  "))
	}
}

// TestEveryUsedTokenIsDeclared is the other half: a var(--…) whose name
// nothing declares resolves to nothing at all, silently, in every
// browser.
func TestEveryUsedTokenIsDeclared(t *testing.T) {
	t.Parallel()
	light, _ := themeTokens(t)
	var undeclared []string
	for name, paths := range references(t) {
		if _, ok := light[name]; !ok {
			sort.Strings(paths)
			undeclared = append(undeclared, fmt.Sprintf("%s (read by %s)", name, strings.Join(uniq(paths), ", ")))
		}
	}
	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		t.Fatalf("%d token(s) read but never declared:\n  %s", len(undeclared), strings.Join(undeclared, "\n  "))
	}
}

func uniq(in []string) []string {
	out := in[:0:0]
	seen := map[string]bool{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// TestATokenNamedOnlyInACommentIsNotAUse guards the guard. Without the
// comment strip both directions of the bidirectional check pass for the
// wrong reason: prose keeps an orphan token alive, and an example in a
// doc comment invents a token nothing declares.
func TestATokenNamedOnlyInACommentIsNotAUse(t *testing.T) {
	t.Parallel()
	src := "/* var(--only-in-a-block-comment) */\n" +
		"// var(--only-in-a-line-comment)\n" +
		"<!-- var(--only-in-an-html-comment) -->\n" +
		"a { color: var(--really-used); background: url(https://example.test/x.png); }\n" +
		"b { border-color: var(--after-a-url); }\n"

	var found []string
	for _, hit := range referenceRE.FindAllStringSubmatch(stripComments(src), -1) {
		found = append(found, hit[1])
	}
	sort.Strings(found)
	if got, want := strings.Join(found, ","), "--after-a-url,--really-used"; got != want {
		t.Fatalf("references found %q, want %q", got, want)
	}
}

// --- the colour arithmetic ------------------------------------------

type rgb struct{ r, g, b float64 } // gamma-encoded sRGB, 0..1

func parseHex(t *testing.T, name, value string) rgb {
	t.Helper()
	assert.Must(t, hexRE.MatchString(value), "%s = %q is not a six-digit hex colour", name, value)
	component := func(i int) float64 {
		n, err := strconv.ParseInt(value[i:i+2], 16, 32)
		assert.Must(t, err == nil, "%s = %q: %v", name, value, err)
		return float64(n) / 255
	}
	return rgb{component(1), component(3), component(5)}
}

func toLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func fromLinear(c float64) float64 {
	if c <= 0.0031308 {
		return 12.92 * c
	}
	return 1.055*math.Pow(c, 1/2.4) - 0.055
}

func clamp01(x float64) float64 { return math.Min(1, math.Max(0, x)) }

func (c rgb) linear() (float64, float64, float64) {
	return toLinear(c.r), toLinear(c.g), toLinear(c.b)
}

// luminance is WCAG 2's relative luminance, which is the ITU-R BT.709
// weighting over linearised sRGB.
func (c rgb) luminance() float64 {
	r, g, b := c.linear()
	return 0.2126*r + 0.7152*g + 0.0722*b
}

func contrastRatio(a, b rgb) float64 {
	hi, lo := a.luminance(), b.luminance()
	if hi < lo {
		hi, lo = lo, hi
	}
	return (hi + 0.05) / (lo + 0.05)
}

// lab is CIELAB under D65, which is where the distances below are taken.
func (c rgb) lab() (float64, float64, float64) {
	r, g, b := c.linear()
	x := 0.4124564*r + 0.3575761*g + 0.1804375*b
	y := 0.2126729*r + 0.7151522*g + 0.0721750*b
	z := 0.0193339*r + 0.1191920*g + 0.9503041*b
	f := func(t float64) float64 {
		if t > 216.0/24389.0 {
			return math.Cbrt(t)
		}
		return (24389.0/27.0*t + 16) / 116
	}
	fx, fy, fz := f(x/0.95047), f(y/1.0), f(z/1.08883)
	return 116*fy - 16, 500 * (fx - fy), 200 * (fy - fz)
}

// deltaE76 is the CIE 1976 distance in CIELAB: a plain Euclidean
// distance, chosen over CIEDE2000 because the threshold below is far
// above the region where the two disagree and because a reader of this
// file can check thirty lines of arithmetic by eye.
func deltaE76(a, b rgb) float64 {
	l1, a1, b1 := a.lab()
	l2, a2, b2 := b.lab()
	return math.Sqrt((l1-l2)*(l1-l2) + (a1-a2)*(a1-a2) + (b1-b2)*(b1-b2))
}

// simulate returns what a dichromat sees, by the Viénot, Brettel & Mollon
// (1999) method: linear sRGB into the Hunt-Pointer-Estévez LMS space,
// then replace the missing cone's response with the plane the remaining
// two span, then back. `protan` picks which cone is missing —
// protanopia (long) or deuteranopia (medium), the common pair.
func simulate(c rgb, protan bool) rgb {
	r, g, b := c.linear()
	l := 17.8824*r + 43.5161*g + 4.11935*b
	m := 3.45565*r + 27.1554*g + 3.86714*b
	s := 0.0299566*r + 0.184309*g + 1.46709*b
	if protan {
		l = 2.02344*m - 2.52581*s
	} else {
		m = 0.494207*l + 1.24827*s
	}
	nr := 0.0809444479*l - 0.130504409*m + 0.116721066*s
	ng := -0.0102485335*l + 0.0540193266*m - 0.113614708*s
	nb := -0.000365296938*l - 0.00412161469*m + 0.693511405*s
	return rgb{fromLinear(clamp01(nr)), fromLinear(clamp01(ng)), fromLinear(clamp01(nb))}
}

type theme struct {
	name   string
	tokens map[string]string
}

func themes(t *testing.T) []theme {
	t.Helper()
	light, dark := themeTokens(t)
	// The dark block redeclares only the colours; anything it leaves
	// alone is inherited from :root, so the effective dark theme is the
	// light one with the dark block laid over it.
	effective := map[string]string{}
	for name, value := range light {
		effective[name] = value
	}
	for name, value := range dark {
		effective[name] = value
	}
	return []theme{{"light", light}, {"dark", effective}}
}

func (th theme) colour(t *testing.T, name string) rgb {
	t.Helper()
	value, ok := th.tokens[name]
	assert.Must(t, ok, "%s theme declares no %s", th.name, name)
	return parseHex(t, name, value)
}

// TestTextContrastMeetsWCAG holds the five text-on-ground pairs the
// interface actually puts on screen to WCAG AA for body text, in both
// themes. 4.5:1 and not 3:1: none of these five is large text.
func TestTextContrastMeetsWCAG(t *testing.T) {
	t.Parallel()
	pairs := [][2]string{
		{"--ink", "--paper"},
		{"--ink", "--ground"},
		{"--muted", "--paper"},
		{"--danger", "--paper"},
		{"--focus-ink", "--focus"},
	}
	for _, th := range themes(t) {
		for _, pair := range pairs {
			got := contrastRatio(th.colour(t, pair[0]), th.colour(t, pair[1]))
			if got < 4.5 {
				t.Errorf("%s theme: %s on %s is %.2f:1, want >= 4.5:1", th.name, pair[0], pair[1], got)
			}
		}
	}
}

// **The label is printed on the node, not on the ground.**
func TestANodesNameIsLegibleOnItsOwnFill(t *testing.T) {
	t.Parallel()
	for _, th := range themes(t) {
		label := th.colour(t, "--paper")
		for i := 1; i <= dataSlots; i++ {
			name := fmt.Sprintf("--data-%d", i)
			got := contrastRatio(label, th.colour(t, name))
			assert.Should(t, got >= 4.5, "%s theme: a label in --paper on %s is %.2f:1, want >= 4.5:1 — "+
				"a node whose name cannot be read is a node with no name", th.name, name, got)
		}
	}
}

// TestTheLabelOnAHueIsTheTokenTheGuardMeasures pins the other half: the
// test above measures --paper against the hues, and it is only worth
// anything while that is the token the renderer prints. A change of mind
// in palette.js with this file left alone would leave a green guard over
// an unreadable diagram.
func TestTheLabelOnAHueIsTheTokenTheGuardMeasures(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("static/palette.js")
	assert.Must(t, err == nil, "read palette.js: %v", err)
	if !strings.Contains(string(source), `const HUE_LABEL_FILL = "var(--paper)"`) {
		t.Error("palette.js no longer sets a label on a hue in --paper, which is the pair " +
			"TestANodesNameIsLegibleOnItsOwnFill measures")
	}
}

// TestMeaningfulOutlinesMeetThreeToOne holds WCAG 1.4.11's threshold for
// non-text content over the things whose *stroke or fill is the whole of
// the signal*: the eight data hues a node wears, and --line-strong, which
// draws the dashed outline that is the only thing distinguishing a node
// whose colour_by slot found nothing.
func TestMeaningfulOutlinesMeetThreeToOne(t *testing.T) {
	t.Parallel()
	for _, th := range themes(t) {
		names := []string{"--line-strong"}
		for i := 1; i <= dataSlots; i++ {
			names = append(names, fmt.Sprintf("--data-%d", i))
		}
		ground := th.colour(t, "--ground")
		for _, name := range names {
			got := contrastRatio(th.colour(t, name), ground)
			assert.Should(t, got >= 3, "%s theme: %s on --ground is %.2f:1, want >= 3:1", th.name, name, got)
		}
	}
}

// dataSlots is palette.js's DATA_SLOTS, and the next test proves the two
// numbers are the same number rather than trusting this line.
const dataSlots = 8

// minSeparation is the stated threshold: 20 units of CIE 1976 ΔE*ab,
// pairwise, in normal vision and in both simulated dichromacies.
const minSeparation = 20.0

// TestTheDataHuesSeparateUnderDeuteranopiaAndProtanopia is the reason
// the palette was searched for rather than picked. Eight hues that
// separate for a trichromat routinely collapse to three under
// deuteranopia; this asserts all 28 pairs, in three vision models, in
// two themes — 168 distances.
func TestTheDataHuesSeparateUnderDeuteranopiaAndProtanopia(t *testing.T) {
	t.Parallel()
	vision := []struct {
		name string
		of   func(rgb) rgb
	}{
		{"normal vision", func(c rgb) rgb { return c }},
		{"deuteranopia", func(c rgb) rgb { return simulate(c, false) }},
		{"protanopia", func(c rgb) rgb { return simulate(c, true) }},
	}

	for _, th := range themes(t) {
		hues := make([]rgb, dataSlots)
		for i := range hues {
			hues[i] = th.colour(t, fmt.Sprintf("--data-%d", i+1))
		}
		for _, mode := range vision {
			for i := 0; i < dataSlots; i++ {
				for j := i + 1; j < dataSlots; j++ {
					got := deltaE76(mode.of(hues[i]), mode.of(hues[j]))
					assert.Should(t, got >= minSeparation, "%s theme, %s: --data-%d and --data-%d are %.1f apart, want >= %.1f",
						th.name, mode.name, i+1, j+1, got, minSeparation)
				}
			}
		}
	}
}

// TestThePaletteModuleAndTheStylesheetAgreeOnEight pins the two halves
// of the palette to one number. The stylesheet declares the hues and
// palette.js names them one by one; if either grew a ninth alone, a node
// would either paint with an undeclared token or a declared hue would
// never be reachable — and the two guards above would each still pass,
// because each only sees its own half.
func TestThePaletteModuleAndTheStylesheetAgreeOnEight(t *testing.T) {
	t.Parallel()
	light, _ := themeTokens(t)
	declared := 0
	for name := range light {
		if strings.HasPrefix(name, "--data-") {
			declared++
		}
	}
	assert.Should(t, declared == dataSlots, "styles.css declares %d --data-n tokens, want %d", declared, dataSlots)

	raw, err := os.ReadFile("static/palette.js")
	assert.Must(t, err == nil, "read palette.js: %v", err)
	src := string(raw)
	if want := fmt.Sprintf("export const DATA_SLOTS = %d;", dataSlots); !strings.Contains(src, want) {
		t.Errorf("palette.js does not declare %q", want)
	}
	named := 0
	for i := 1; i <= dataSlots+1; i++ {
		if strings.Contains(src, fmt.Sprintf(`"var(--data-%d)"`, i)) {
			named++
		}
	}
	assert.Should(t, named == dataSlots, "palette.js names %d data tokens, want %d", named, dataSlots)
}

// --- The chrome spends no hue, and that is arithmetic too -------------

// literalColourRE finds a colour written out rather than named. It is
// matched against the stylesheet with its comments stripped, so the hex
// values quoted in the prose above a rule — which is where this file's
// arguments keep their numbers — are not mistaken for paint.
var literalColourRE = regexp.MustCompile(`(?i)(#[0-9a-f]{3,8}\b|\b(?:rgba?|hsla?|color-mix|oklch|lab|lch)\s*\()`)

// diffRuleRE isolates the rules that draw a document comparison.
var diffRuleRE = regexp.MustCompile(`(?s)(\.diff[a-z-]*)\s*\{([^}]*)\}`)

// TestNoRuleSpellsAColourLiterally is the guard that would have caught
// the diff and now catches whatever comes next.
func TestNoRuleSpellsAColourLiterally(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(stylesheetPath)
	assert.Must(t, err == nil, "read %s: %v", stylesheetPath, err)
	src := stripComments(string(raw))

	// The token declarations are the one place a literal is the point.
	// They are removed wholesale rather than exempted line by line, so a
	// literal that appears anywhere else is found however it is spelled.
	withoutTokens := declarationRE.ReplaceAllString(src, "")

	var offenders []string
	for _, line := range strings.Split(withoutTokens, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if hit := literalColourRE.FindString(trimmed); hit != "" {
			offenders = append(offenders, fmt.Sprintf("%q spells %s", trimmed, hit))
		}
	}
	assert.Must(t, len(offenders) <= 0, "%d rule(s) spell a colour literally instead of naming a token:\n  %s\n"+
		"a literal is a colour that is never contrast-checked, never checked for colour-blind "+
		"separation and has no dark value at all",
		len(offenders), strings.Join(offenders, "\n  "))
}

// TestTheDiffReadsNoChromaticToken is the second half, and it is the one
// that holds the *decision* rather than the spelling.
func TestTheDiffReadsNoChromaticToken(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(stylesheetPath)
	assert.Must(t, err == nil, "read %s: %v", stylesheetPath, err)
	src := stripComments(string(raw))

	chromatic := map[string]bool{"--danger": true, "--focus": true, "--focus-ink": true}
	for i := 1; i <= dataSlots; i++ {
		chromatic[fmt.Sprintf("--data-%d", i)] = true
	}

	rules := diffRuleRE.FindAllStringSubmatch(src, -1)
	assert.Must(t, len(rules) >= 5, "found %d .diff rules; RenderDiff writes five classes and the container, so this test has stopped reading the thing it names", len(rules))
	for _, rule := range rules {
		for _, hit := range referenceRE.FindAllStringSubmatch(rule[2], -1) {
			if chromatic[hit[1]] {
				t.Errorf("%s reads %s: the diff was argued for a hue of its own and refused, and this is that hue arriving by the side door",
					rule[1], hit[1])
			}
		}
	}
}

// TestTheDiffsTwoSpellingsSeparateUnderBothDichromacies is the palette's
// own arithmetic, turned on the chrome.
func TestTheDiffsTwoSpellingsSeparateUnderBothDichromacies(t *testing.T) {
	t.Parallel()
	vision := []struct {
		name string
		of   func(rgb) rgb
	}{
		{"normal vision", func(c rgb) rgb { return c }},
		{"deuteranopia", func(c rgb) rgb { return simulate(c, false) }},
		{"protanopia", func(c rgb) rgb { return simulate(c, true) }},
	}
	for _, th := range themes(t) {
		added := th.colour(t, "--ink")
		removed := th.colour(t, "--muted")
		for _, mode := range vision {
			got := deltaE76(mode.of(added), mode.of(removed))
			assert.Should(t, got >= minSeparation, "%s theme, %s: an added line's --ink and a removed line's --muted are %.1f apart, want >= %.1f",
				th.name, mode.name, got, minSeparation)
		}
	}
}

// TestTheDiffsGroundsAndItsGutterAreLegible closes the arithmetic the
// two greens actually failed: not their separation from each other but
// their contrast against the ground they were painted on. #14532d was
// 8.59:1 on the light paper and 1.91:1 on the dark one — the same rule,
// legible in one theme and not in the other, because only one theme was
// ever looked at.
func TestTheDiffsGroundsAndItsGutterAreLegible(t *testing.T) {
	t.Parallel()
	text := [][2]string{
		{"--ink", "--ground"},   // an added line, on its fill
		{"--muted", "--paper"},  // a removed line, on the page
		{"--muted", "--ground"}, // a hunk header, on its fill
		{"--ink", "--paper"},    // a context line
	}
	for _, th := range themes(t) {
		for _, pair := range text {
			if got := contrastRatio(th.colour(t, pair[0]), th.colour(t, pair[1])); got < 4.5 {
				t.Errorf("%s theme: a diff line's %s on %s is %.2f:1, want >= 4.5:1", th.name, pair[0], pair[1], got)
			}
		}
		for _, ground := range []string{"--paper", "--ground"} {
			if got := contrastRatio(th.colour(t, "--line-strong"), th.colour(t, ground)); got < 3 {
				t.Errorf("%s theme: the diff's gutter rule --line-strong on %s is %.2f:1, want >= 3:1", th.name, ground, got)
			}
		}
	}
}

// TestTheDiffsGutterOutranksItsOwnDefault is a source-shape guard over a
// cascade, which is the one thing every other check in this file is
// blind to: a token can be right, declared in both themes, contrast-
// checked and colour-blind-checked, and still never reach the pixel.
func TestTheDiffsGutterOutranksItsOwnDefault(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(stylesheetPath)
	assert.Must(t, err == nil, "read %s: %v", stylesheetPath, err)
	src := stripComments(string(raw))

	assert.Must(t, strings.Contains(src, ".diff > div {"), "the diff no longer draws a gutter on every line: this guard is about the rule that one outranks, and there is no longer one")
	for _, class := range []string{".diff-added", ".diff-removed"} {
		assert.Should(t, strings.Contains(src, ".diff > "+class+" {"), "%s is not written as `.diff > %s`: `.diff > div` outranks a bare class, so its gutter silently computes to transparent and added and removed look the same",
			class, class)
	}
}

// --- The design document and the stylesheet ---------------------------

// designDocPath and designTokensPath are the two files claude.md names
// as owning the design system. They are read here, not restated.
const (
	designDocPath    = "../../docs/design.md"
	designTokensPath = "../../docs/design-tokens.json"
)

// colourLineRE is one entry of design.md's frontmatter `colors:` map.
var colourLineRE = regexp.MustCompile(`(?m)^  ([a-z0-9-]+):\s*"(#[0-9a-fA-F]{6})"`)

// statedColours reads the light set out of docs/design.md's frontmatter.
func statedColours(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(designDocPath)
	assert.Must(t, err == nil, "read %s: %v", designDocPath, err)
	src := string(raw)
	start := strings.Index(src, "\ncolors:\n")
	assert.Must(t, start >= 0, "%s has no `colors:` map in its frontmatter: this guard reads it", designDocPath)
	rest := src[start+len("\ncolors:\n"):]
	// The map ends at the next key in column zero.
	if end := regexp.MustCompile(`(?m)^[a-z]`).FindStringIndex(rest); end != nil {
		rest = rest[:end[0]]
	}
	out := map[string]string{}
	for _, m := range colourLineRE.FindAllStringSubmatch(rest, -1) {
		out[m[1]] = strings.ToLower(m[2])
	}
	assert.Must(t, len(out) != 0, "parsed no colours out of %s: the guard found nothing to hold", designDocPath)
	return out
}

// statedDarkColours reads the dark set out of docs/design-tokens.json,
// which is where each colour's dark hex lives.
func statedDarkColours(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(designTokensPath)
	assert.Must(t, err == nil, "read %s: %v", designTokensPath, err)
	var doc struct {
		Extensions struct {
			ColorMeta map[string]struct {
				DarkHex string `json:"darkHex"`
			} `json:"colorMeta"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", designTokensPath, err)
	}
	out := map[string]string{}
	for name, meta := range doc.Extensions.ColorMeta {
		if hexRE.MatchString(meta.DarkHex) {
			out[name] = strings.ToLower(meta.DarkHex)
		}
	}
	assert.Must(t, len(out) != 0, "parsed no dark colours out of %s: the guard found nothing to hold", designTokensPath)
	return out
}

// TestTheDesignDocumentAndTheStylesheetAgreeOnEveryColour is the guard
// that was missing, and it was missing for the whole build.
func TestTheDesignDocumentAndTheStylesheetAgreeOnEveryColour(t *testing.T) {
	t.Parallel()
	light, dark := themeTokens(t)

	var wrong []string
	for name, stated := range statedColours(t) {
		got, ok := light["--"+name]
		if !ok {
			wrong = append(wrong, "--"+name+": stated in docs/design.md, declared nowhere in the stylesheet")
			continue
		}
		if !strings.EqualFold(got, stated) {
			wrong = append(wrong, "--"+name+": docs/design.md says "+stated+", the stylesheet says "+got)
		}
	}
	for name, stated := range statedDarkColours(t) {
		got, ok := dark["--"+name]
		if !ok {
			wrong = append(wrong, "--"+name+": a dark hex in docs/design-tokens.json, no dark declaration in the stylesheet")
			continue
		}
		if !strings.EqualFold(got, stated) {
			wrong = append(wrong, "--"+name+" (dark): docs/design-tokens.json says "+stated+", the stylesheet says "+got)
		}
	}
	if len(wrong) > 0 {
		sort.Strings(wrong)
		t.Fatalf("%d colour(s) where the design system and the product disagree:\n  %s\n\n"+
			"docs/design.md and docs/design-tokens.json are the normative statement of this palette and "+
			"internal/web/static/styles.css is what a reader actually sees. When they differ one of them is "+
			"lying, and the generated design-system page renders the document's version.",
			len(wrong), strings.Join(wrong, "\n  "))
	}
}
