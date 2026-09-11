package web_test

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The arithmetic half of the interface's identity.
//
// The design spec (.superpowers/specs/2026-09-06-interface-design.md
// §2) commits to one mechanical rule — the chrome is achromatic and
// every hue on screen belongs to the game — and to a categorical palette
// of eight hues that has to survive being read at 11px, on two grounds,
// by a reader with the common form of colour blindness. Almost none of
// that is a matter of judgement:
//
//   - whether a token exists in both themes is a set comparison;
//   - whether a token that exists is ever read is a grep in two
//     directions, which is the bidirectional data-table guard the views
//     sub-project named as the thing that kept working;
//   - whether text is legible on its ground is WCAG's own ratio;
//   - whether eight hues stay apart under deuteranopia and protanopia is
//     a colour-space simulation and a distance.
//
// So all of it is a test, and a palette that fails is a palette that
// fails a build rather than a palette somebody squints at.
//
// **What this file cannot answer, said plainly.** Whether eight hues
// that are provably far apart in CIELAB actually *separate* for a human
// reading a five-pixel node label over a busy canvas, and whether the
// serif/sans/mono split reads as editorial rather than accidental. Those
// are the hand check named in the plan's Task 1 and they are not
// automatable; this file is the floor, not the ceiling.

const stylesheetPath = "static/styles.css"

// darkBlockMarker is the boundary between the two token sets. It is
// matched literally rather than by a CSS parser because there is exactly
// one dark block and this test would rather fail loudly the day that
// stops being true than silently guard half a stylesheet.
const darkBlockMarker = "@media (prefers-color-scheme: dark)"

var (
	rootBlockRE   = regexp.MustCompile(`(?s):root\s*\{(.*?)\}`)
	declarationRE = regexp.MustCompile(`(?m)^\s*(--[a-z0-9-]+)\s*:\s*([^;]+);`)
	referenceRE   = regexp.MustCompile(`var\(\s*(--[a-z0-9-]+)`)
	hexRE         = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

// themeTokens reads the two token sets out of styles.css: everything a
// `:root` block before the dark media query declares is the light set,
// everything a `:root` block after it declares is the dark set.
func themeTokens(t *testing.T) (light, dark map[string]string) {
	t.Helper()
	raw, err := os.ReadFile(stylesheetPath)
	if err != nil {
		t.Fatalf("read %s: %v", stylesheetPath, err)
	}
	src := string(raw)

	boundary := strings.Index(src, darkBlockMarker)
	if boundary < 0 {
		t.Fatalf("%s declares no %q block: the dark set is half the token layer", stylesheetPath, darkBlockMarker)
	}

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
	if len(light) == 0 || len(dark) == 0 {
		t.Fatalf("parsed %d light and %d dark tokens: the parser found nothing to guard", len(light), len(dark))
	}
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
		if !isColour(value) {
			t.Errorf("%s is redeclared in the dark block with a non-colour value %q: only colours have a theme", name, value)
		}
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
	if err != nil {
		t.Fatalf("walk static: %v", err)
	}
	if len(assets) == 0 {
		t.Fatal("scanned no assets under internal/web/static: the walk found nothing to guard")
	}
	return assets
}

// stripComments removes what a browser never reads, so that neither
// half of the bidirectional guard below can be satisfied — or broken —
// by prose. Both failure modes are real and this repository has met the
// first one: a token mentioned only in a doc comment would count as used
// and keep an orphan alive forever, and a comment that spells a token
// name in an example would fail the "declared" half for a name no
// stylesheet was ever asked to have.
//
// Block comments (/* … */, both CSS and JS) and HTML comments are
// removed wherever they occur. A // comment is removed to end of line
// only when it is not preceded by a colon, which is the one common
// spelling — a URL's scheme — that would otherwise eat the rest of a
// line of real code.
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
//
// There is no exception list, deliberately. A token whose consumer lands
// in a later task lands with that task; an exemption here would be the
// hiding place every unread token in the future would use.
func TestEveryDeclaredTokenIsUsed(t *testing.T) {
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
	if !hexRE.MatchString(value) {
		t.Fatalf("%s = %q is not a six-digit hex colour", name, value)
	}
	component := func(i int) float64 {
		n, err := strconv.ParseInt(value[i:i+2], 16, 32)
		if err != nil {
			t.Fatalf("%s = %q: %v", name, value, err)
		}
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
//
// This is a simulation of the dichromacies and not of the far commoner
// anomalous trichromacies, which see less separation than a trichromat
// and more than this. Passing here is therefore the harder half of the
// claim, not a weaker one.
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
	if !ok {
		t.Fatalf("%s theme declares no %s", th.name, name)
	}
	return parseHex(t, name, value)
}

// TestTextContrastMeetsWCAG holds the five text-on-ground pairs the
// interface actually puts on screen to WCAG AA for body text, in both
// themes. 4.5:1 and not 3:1: none of these five is large text.
func TestTextContrastMeetsWCAG(t *testing.T) {
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

// TestMeaningfulOutlinesMeetThreeToOne holds WCAG 1.4.11's threshold for
// non-text content over the things whose *stroke or fill is the whole of
// the signal*: the eight data hues a node wears, and --line-strong, which
// draws the dashed outline that is the only thing distinguishing a node
// whose colour_by slot found nothing.
//
// --line is deliberately not in this list. It is the decorative hairline
// — a table rule, a panel edge — and WCAG exempts decoration for the
// same reason the identity wants it: a rule at 3:1 against its own
// surface is not a hairline, it is a box, and the design spec's
// "hairline rules instead of boxes and shadows" is the sentence that
// would be lost. Any stroke that carries meaning uses --line-strong and
// is guarded here.
func TestMeaningfulOutlinesMeetThreeToOne(t *testing.T) {
	for _, th := range themes(t) {
		names := []string{"--line-strong"}
		for i := 1; i <= dataSlots; i++ {
			names = append(names, fmt.Sprintf("--data-%d", i))
		}
		ground := th.colour(t, "--ground")
		for _, name := range names {
			got := contrastRatio(th.colour(t, name), ground)
			if got < 3 {
				t.Errorf("%s theme: %s on --ground is %.2f:1, want >= 3:1", th.name, name, got)
			}
		}
	}
}

// dataSlots is palette.js's DATA_SLOTS, and the next test proves the two
// numbers are the same number rather than trusting this line.
const dataSlots = 8

// minSeparation is the stated threshold: 20 units of CIE 1976 ΔE*ab,
// pairwise, in normal vision and in both simulated dichromacies.
//
// Where the number comes from: ~2.3 is the just-noticeable difference
// for two large patches side by side, and the marks here are small,
// scattered across a canvas, never adjacent, and read from memory
// against a legend. An order of magnitude above the JND is the margin
// that buys; the palette that ships clears it by four in the light set
// and by six in the dark. It is a floor, not a target — the palette was
// chosen by maximising this quantity, not by satisfying it.
const minSeparation = 20.0

// TestTheDataHuesSeparateUnderDeuteranopiaAndProtanopia is the reason
// the palette was searched for rather than picked. Eight hues that
// separate for a trichromat routinely collapse to three under
// deuteranopia; this asserts all 28 pairs, in three vision models, in
// two themes — 168 distances.
func TestTheDataHuesSeparateUnderDeuteranopiaAndProtanopia(t *testing.T) {
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
					if got < minSeparation {
						t.Errorf("%s theme, %s: --data-%d and --data-%d are %.1f apart, want >= %.1f",
							th.name, mode.name, i+1, j+1, got, minSeparation)
					}
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
	light, _ := themeTokens(t)
	declared := 0
	for name := range light {
		if strings.HasPrefix(name, "--data-") {
			declared++
		}
	}
	if declared != dataSlots {
		t.Errorf("styles.css declares %d --data-n tokens, want %d", declared, dataSlots)
	}

	raw, err := os.ReadFile("static/palette.js")
	if err != nil {
		t.Fatalf("read palette.js: %v", err)
	}
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
	if named != dataSlots {
		t.Errorf("palette.js names %d data tokens, want %d", named, dataSlots)
	}
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
//
// The token layer's whole claim is that every colour on screen is
// declared twice, contrast-checked against both grounds and separated
// under both dichromacies. A rule that writes `#14532d` instead of
// naming a token opts out of all three silently: nothing fails, nothing
// warns, and the value is simply never looked at again — which is
// exactly what happened to the diff's two greens, chosen for a light
// ground and left at 1.91:1 on the dark one for as long as the dark
// theme has existed.
//
// So: outside the two token blocks, no rule in this stylesheet spells a
// colour. The declarations themselves are where literals belong and are
// skipped by name; every other rule paints with `var(--…)` or does not
// paint. `transparent` and `currentColor` are keywords rather than
// values and are not colours this test has anything to check.
func TestNoRuleSpellsAColourLiterally(t *testing.T) {
	raw, err := os.ReadFile(stylesheetPath)
	if err != nil {
		t.Fatalf("read %s: %v", stylesheetPath, err)
	}
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
	if len(offenders) > 0 {
		t.Fatalf("%d rule(s) spell a colour literally instead of naming a token:\n  %s\n"+
			"a literal is a colour that is never contrast-checked, never checked for colour-blind "+
			"separation and has no dark value at all",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// TestTheDiffReadsNoChromaticToken is the second half, and it is the one
// that holds the *decision* rather than the spelling.
//
// A diff is the strongest case this product has for a third chromatic
// exception — added and removed are opposites a reader must separate at
// a glance — and it was argued and refused (styles.css says why, at
// length, beside the rules). The refusal is only worth anything if it is
// held: this asserts the comparison paints with the achromatic tokens
// and never with --danger, --focus or one of the eight data hues, so
// re-admitting a hue there means re-arguing it and not editing a line.
func TestTheDiffReadsNoChromaticToken(t *testing.T) {
	raw, err := os.ReadFile(stylesheetPath)
	if err != nil {
		t.Fatalf("read %s: %v", stylesheetPath, err)
	}
	src := stripComments(string(raw))

	chromatic := map[string]bool{"--danger": true, "--focus": true, "--focus-ink": true}
	for i := 1; i <= dataSlots; i++ {
		chromatic[fmt.Sprintf("--data-%d", i)] = true
	}

	rules := diffRuleRE.FindAllStringSubmatch(src, -1)
	if len(rules) < 5 {
		t.Fatalf("found %d .diff rules; RenderDiff writes five classes and the container, so this test has stopped reading the thing it names", len(rules))
	}
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
//
// Added and removed are told apart four times over — the `+`/`-` the
// unified format writes into the line's text, a solid rule against a
// dashed one, a filled ground against an unfilled one, and full ink
// against muted — and only the last of those is a colour. This asserts
// the colour half to the same floor and in the same three vision models
// as the eight data hues, which is what makes "this spends no hue" a
// measurement rather than a claim: an achromatic pair separates by the
// same distance whichever cone is missing, and the numbers here move by
// less than a tenth of a unit between the three models.
//
// What the two greens scored, for the record and for whoever proposes a
// hue here next: 81.4 in normal vision, 23.0 under deuteranopia and 7.4
// under protanopia, against this floor of 20.
func TestTheDiffsTwoSpellingsSeparateUnderBothDichromacies(t *testing.T) {
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
			if got < minSeparation {
				t.Errorf("%s theme, %s: an added line's --ink and a removed line's --muted are %.1f apart, want >= %.1f",
					th.name, mode.name, got, minSeparation)
			}
		}
	}
}

// TestTheDiffsGroundsAndItsGutterAreLegible closes the arithmetic the
// two greens actually failed: not their separation from each other but
// their contrast against the ground they were painted on. #14532d was
// 8.59:1 on the light paper and 1.91:1 on the dark one — the same rule,
// legible in one theme and not in the other, because only one theme was
// ever looked at.
//
// The five pairs below are every text-on-ground the comparison puts on
// screen plus the gutter rule, which is a stroke that carries meaning
// and is therefore held to WCAG 1.4.11's 3:1 rather than exempted as
// decoration: it is one of the four things telling added from removed.
func TestTheDiffsGroundsAndItsGutterAreLegible(t *testing.T) {
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
//
// The comparison draws a transparent 3px rule on **every** line, so that
// the monospace columns of lines that show a gutter and lines that do
// not stay aligned. That default is `.diff > div`, which outranks a bare
// `.diff-added`. Written the obvious way, `.diff-added`'s
// `border-left-color` therefore lost to it: both gutters computed to
// `rgba(0, 0, 0, 0)`, added and removed differed only in their text, and
// **nothing here failed** — the tokens above were all correct. It was
// found by opening the page and reading the computed style, which is
// this sub-project's own recurring lesson arriving one more time.
//
// So the two overriding rules must carry the container in their
// selector. It is a weak check over a property with no runtime
// signature, and that is exactly when this repository writes one.
func TestTheDiffsGutterOutranksItsOwnDefault(t *testing.T) {
	raw, err := os.ReadFile(stylesheetPath)
	if err != nil {
		t.Fatalf("read %s: %v", stylesheetPath, err)
	}
	src := stripComments(string(raw))

	if !strings.Contains(src, ".diff > div {") {
		t.Fatal("the diff no longer draws a gutter on every line: this guard is about the rule that one outranks, and there is no longer one")
	}
	for _, class := range []string{".diff-added", ".diff-removed"} {
		if !strings.Contains(src, ".diff > "+class+" {") {
			t.Errorf("%s is not written as `.diff > %s`: `.diff > div` outranks a bare class, so its gutter silently computes to transparent and added and removed look the same",
				class, class)
		}
	}
}
