package web_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

// cssRules reads one stylesheet as (selector, body) pairs with the
// comments taken out, which is all these guards read it for.
func cssRules(t *testing.T, name string) [][2]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("static", name))
	assert.NoErr(t, err, "read "+name)
	src := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(string(raw), "")
	var out [][2]string
	for _, m := range regexp.MustCompile(`(?s)([^{}]+)\{([^{}]*)\}`).FindAllStringSubmatch(src, -1) {
		out = append(out, [2]string{strings.Join(strings.Fields(m[1]), " "), m[2]})
	}
	assert.Must(t, len(out) > 10, "%s parsed into %d rules; this guard is reading the wrong place", name, len(out))
	return out
}

// **A control that undresses the button has to undress it on hover too.**
// The vocabulary dresses a bare `<button>` as the primary button — ink
// fill, paper text — so every control that is something else resets it:
// the ghost, the chip, the tab, the link-button. A reset that stops at
// the resting state leaves `button:hover` to repaint the control in ink
// while its own rule keeps the text ink: a black box with an invisible
// word in it. That shipped on every sortable column heading in the
// product and was reported from a browser, which is the only place a
// hover state exists.
func TestEveryUndressedControlStaysUndressedUnderThePointer(t *testing.T) {
	t.Parallel()
	var rules [][2]string
	for _, sheet := range []string{"controls.css", "styles.css"} {
		rules = append(rules, cssRules(t, sheet)...)
	}

	undress := regexp.MustCompile(`background:\s*(none|transparent)`)
	paints := regexp.MustCompile(`background`)
	// The class a rule is about: the last one in the selector, which is
	// the control being dressed.
	lastClass := regexp.MustCompile(`\.([A-Za-z][\w-]*)[^.{]*$`)

	resting := map[string]string{}
	hovered := map[string]bool{}
	for _, rule := range rules {
		sel, body := rule[0], rule[1]
		for _, part := range strings.Split(sel, ",") {
			part = strings.TrimSpace(part)
			m := lastClass.FindStringSubmatch(part)
			if m == nil {
				continue
			}
			switch {
			case strings.Contains(part, ":hover") && paints.MatchString(body):
				hovered[m[1]] = true
			case !strings.Contains(part, ":hover") && !strings.Contains(part, ":active") && undress.MatchString(body):
				// Only controls: a `<div>` with no background is not a
				// button and has no hover to lose.
				// **The word list is why this guard did not fire on the
				// field editor's own control.** `.field-edit` and `.quiet`
				// undress the button and name none of these words, so the
				// one control in the product that wore two vocabularies at
				// once — `ghost field-edit`, a halo drawn around a word —
				// was outside the only guard written to catch exactly that.
				if strings.Contains(part, "button") || strings.HasSuffix(part, "-button") ||
					strings.Contains(part, "sort") || strings.Contains(part, "chip") ||
					strings.Contains(part, "tab") || strings.Contains(part, "ghost") ||
					strings.Contains(part, "sign-out") || strings.Contains(part, "btn") ||
					strings.Contains(part, "edit") || strings.Contains(part, "quiet") {
					resting[m[1]] = part
				}
			}
		}
	}
	assert.Must(t, len(resting) >= 3, "found %d controls that undress the button; this guard is reading the wrong place", len(resting))

	var bare []string
	for class, sel := range resting {
		if !hovered[class] {
			bare = append(bare, sel)
		}
	}
	sort.Strings(bare)
	assert.Must(t, len(bare) == 0,
		"%s reset the button's fill and say nothing about the pointer, so button:hover repaints them in ink",
		strings.Join(bare, "; "))
}

// TestTheDrawnBooleanWearsTokensAndSitsWhereItIsRead holds the two
// decisions a drawn boolean carries that no module test can see. The
// colours, because a hex here is a ninth data colour nobody argued for;
// and the two alignments, because the mark is centred under the heading
// that names it in a column and stays beside its label on the page where
// the label is the thing it belongs to.
func TestTheDrawnBooleanWearsTokensAndSitsWhereItIsRead(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		".mark-yes":                       "color: var(--",
		".mark-no":                        "color: var(--",
		".catalogue-cell.marked":          "justify-content: center",
		"dl.fields dd.field-value.marked": "justify-content: flex-start",
	}
	seen := map[string]bool{}
	for _, rule := range cssRules(t, "styles.css") {
		body, ok := want[rule[0]]
		if !ok {
			continue
		}
		seen[rule[0]] = true
		flat := strings.Join(strings.Fields(rule[1]), " ")
		assert.Should(t, strings.Contains(flat, body), "%s is %q, want it to carry %q", rule[0], flat, body)
	}
	for selector := range want {
		assert.Should(t, seen[selector], "no rule in styles.css selects %s: the guard reads a selector the "+
			"stylesheet no longer has", selector)
	}
}

// TestNoControlWearsTwoVocabularies is the markup half of the rule
// above, and the half that would have caught the defect first: a
// stylesheet guard compares a class with its own hover rule and cannot
// see that an element carries two classes at once. `ghost field-edit`
// did — one dressing the button, the other stripping it — so the pointer
// answered with the ghost's fill, border and 3px halo drawn around a
// 32-pixel box holding twelve pixels of text.
func TestNoControlWearsTwoVocabularies(t *testing.T) {
	t.Parallel()
	// A class that dresses a control, and the classes that undress one.
	// Either list growing is a decision; the pairing is never one.
	dressed := []string{"ghost", "sign-out", "chip", "tab"}
	undressed := []string{"field-edit", "quiet"}

	classList := regexp.MustCompile(`className\s*=\s*"([^"]*)"|class="([^"]*)"`)
	for _, module := range ownModules(t) {
		source, err := os.ReadFile(module)
		assert.Must(t, err == nil, "read %s: %v", module, err)
		for _, m := range classList.FindAllStringSubmatch(string(source), -1) {
			classes := strings.Fields(m[1] + " " + m[2])
			has := func(list []string) string {
				for _, want := range list {
					for _, got := range classes {
						if got == want {
							return want
						}
					}
				}
				return ""
			}
			a, b := has(dressed), has(undressed)
			assert.Should(t, a == "" || b == "", "%s writes class %q: %q dresses a control and %q strips it, so the "+
				"resting state is one vocabulary and the pointer gets the other", module,
				strings.Join(classes, " "), a, b)
		}
	}
}

// TestTheRowsControlIsReachableWithoutAPointer. A control revealed by
// hover is a control a keyboard and a touch screen never meet, which is
// the argument this stylesheet carried against hover reveals at all.
// Three rules answer it, and all three have to be there: the reveal, the
// focus that brings it back, and the fallback where there is no pointer.
func TestTheRowsControlIsReachableWithoutAPointer(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("static", "styles.css"))
	assert.Must(t, err == nil, "read styles.css: %v", err)
	sheet := string(raw)
	for phrase, why := range map[string]string{
		".field-row:hover .field-action .field-edit":        "the row reveals its own control",
		".field-row:focus-within .field-action .field-edit": "a keyboard reaching the control brings it back",
		"@media (hover: none)":                              "a screen with no pointer never hides it",
	} {
		assert.Should(t, strings.Contains(sheet, phrase), "styles.css carries no %q: %s", phrase, why)
	}
	// And it must not be display or visibility: both take the control out
	// of the tab order, which is the thing the focus rule exists to use.
	for _, rule := range cssRules(t, "styles.css") {
		if !strings.Contains(rule[0], "field-action") && !strings.Contains(rule[0], "field-edit") {
			continue
		}
		flat := strings.Join(strings.Fields(rule[1]), " ")
		assert.Should(t, !strings.Contains(flat, "display: none") && !strings.Contains(flat, "visibility: hidden"),
			"%s hides the control with %q, which takes it out of the tab order", rule[0], flat)
	}
}

// cssSpecificity is (ids, classes, elements) for one compound selector,
// which is all these guards compare. `:not()`, `:is()` and `:has()`
// contribute the specificity of their argument and nothing of their own;
// a pseudo-element counts as an element and a pseudo-class as a class.
func cssSpecificity(selector string) [3]int {
	var out [3]int
	rest := selector
	for {
		open := strings.IndexAny(rest, "(")
		if open < 0 {
			break
		}
		// The functional pseudo-class this parenthesis belongs to.
		head := rest[:open]
		name := head[strings.LastIndexAny(head, " >+~,:")+1:]
		depth, end := 0, -1
		for i := open; i < len(rest); i++ {
			switch rest[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					end = i
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			break
		}
		inner := cssSpecificity(rest[open+1 : end])
		switch name {
		case "not", "is", "has":
			for i := range out {
				out[i] += inner[i]
			}
			// The pseudo-class itself counts for nothing; drop it and its
			// argument from what is left to scan.
			rest = rest[:open-len(name)-1] + rest[end+1:]
			continue
		default:
			// nth-child and friends: the pseudo-class counts, its
			// argument does not.
			rest = rest[:open] + rest[end+1:]
		}
	}
	out[0] += len(regexp.MustCompile(`#[\w-]+`).FindAllString(rest, -1))
	out[2] += len(regexp.MustCompile(`::[\w-]+`).FindAllString(rest, -1))
	withoutElements := regexp.MustCompile(`::[\w-]+`).ReplaceAllString(rest, " ")
	out[1] += len(regexp.MustCompile(`\.[\w-]+`).FindAllString(withoutElements, -1))
	out[1] += len(regexp.MustCompile(`:[\w-]+`).FindAllString(withoutElements, -1))
	out[1] += len(regexp.MustCompile(`\[[^\]]+\]`).FindAllString(withoutElements, -1))
	bare := regexp.MustCompile(`\.[\w-]+|#[\w-]+|:{1,2}[\w-]+|\[[^\]]+\]`).ReplaceAllString(withoutElements, " ")
	out[2] += len(regexp.MustCompile(`[A-Za-z][\w-]*`).FindAllString(bare, -1))
	return out
}

func cssBeats(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// TestEveryUndressedControlOutWeighsTheButtonItUndresses is the half the
// guard above could not see. That one asks whether a reset exists; the
// cascade asks which rule wins, and `.field-edit:hover` is (0,2,0)
// against `button:hover:not(:disabled)` at (0,2,1) — so a control that
// said "no background" was repainted the primary's ink the moment a
// pointer touched it, with its own rule keeping the text ink too. A reset
// that merely exists is not a reset.
func TestEveryUndressedControlOutWeighsTheButtonItUndresses(t *testing.T) {
	t.Parallel()
	// The calculator first, against selectors this file does not contain:
	// a wrong one would make every comparison below pass.
	for selector, want := range map[string][3]int{
		"button:hover:not(:disabled)":      {0, 2, 1},
		".field-edit:hover":                {0, 2, 0},
		".field-edit:hover:not(:disabled)": {0, 3, 0},
		"#id .a b::before":                 {1, 1, 2},
		"a":                                {0, 0, 1},
		".a.b.c":                           {0, 3, 0},
	} {
		assert.Must(t, cssSpecificity(selector) == want, "specificity(%q) = %v, want %v", selector, cssSpecificity(selector), want)
	}

	var rules [][2]string
	for _, sheet := range []string{"controls.css", "styles.css"} {
		rules = append(rules, cssRules(t, sheet)...)
	}
	paints := regexp.MustCompile(`background`)

	// The rules a bare `button` carries under the pointer, which every
	// undressing control has to beat.
	base := map[string][3]int{}
	for _, rule := range rules {
		for _, part := range strings.Split(rule[0], ",") {
			part = strings.TrimSpace(part)
			if !strings.HasPrefix(part, "button:") || !paints.MatchString(rule[1]) {
				continue
			}
			for _, state := range []string{":hover", ":active"} {
				if strings.Contains(part, state) {
					base[state] = cssSpecificity(part)
				}
			}
		}
	}
	assert.Must(t, len(base) == 2, "found %d bare-button pointer rules; this guard is reading the wrong place", len(base))

	for _, class := range []string{"field-edit", "quiet", "ghost", "catalogue-sort"} {
		for state, weight := range base {
			best := [3]int{}
			found := false
			for _, rule := range rules {
				for _, part := range strings.Split(rule[0], ",") {
					part = strings.TrimSpace(part)
					if !strings.Contains(part, "."+class) || !strings.Contains(part, state) || !paints.MatchString(rule[1]) {
						continue
					}
					found = true
					if got := cssSpecificity(part); cssBeats(got, best) {
						best = got
					}
				}
			}
			if !found {
				continue
			}
			assert.Should(t, cssBeats(best, weight), ".%s's %s rule is %v against button%s at %v, so the button's fill wins "+
				"and the control is repainted under the pointer", class, state, best, state, weight)
		}
	}
}

// TestTheEdgeTablesHeaderIsSeparatedFromItsRows holds the two rules the
// browser showed and the stylesheet did not: the line under a header is
// the one line in that table that is signal, and "the table ends here"
// is a statement about the body. Written as `tr:last-child` the second
// one also matched the header row, which is the last child of its own
// `thead` — so the rule that says where the table ends took away the one
// that says where it starts, and the header read as another row.
func TestTheEdgeTablesHeaderIsSeparatedFromItsRows(t *testing.T) {
	t.Parallel()
	rules := cssRules(t, "styles.css")
	var header, last, caption, captionLine string
	for _, rule := range rules {
		switch strings.Join(strings.Fields(rule[0]), " ") {
		case "table.edges th":
			header = strings.Join(strings.Fields(rule[1]), " ")
		case "table.edges tbody tr:last-child > *":
			last = strings.Join(strings.Fields(rule[1]), " ")
		case "caption.edge-head":
			caption = strings.Join(strings.Fields(rule[1]), " ")
		case "caption.edge-head > div":
			captionLine = strings.Join(strings.Fields(rule[1]), " ")
		}
		// The shape the defect had: a last-child rule that does not say
		// which section of the table it is about.
		assert.Should(t, !strings.Contains(rule[0], "table.edges tr:last-child"),
			"%s matches the header row too: it is the last child of its own thead", rule[0])
	}
	assert.Must(t, header != "" && last != "" && caption != "", "the three rules this guard reads are not all in styles.css")
	assert.Should(t, strings.Contains(header, "border-bottom: 1px solid var(--line-strong)"),
		"the header's rule is %q, and a hairline there is another row", header)
	assert.Should(t, strings.Contains(last, "border-bottom: 0"), "the last row of the body keeps a rule under it: %q", last)
	// A caption laid out as a flex box is no longer a caption: it loses
	// the table-caption role and renders inside the table's own flow.
	assert.Should(t, !strings.Contains(caption, "display: flex"),
		"caption.edge-head is %q, which takes away its table-caption role and drops it under the column headings", caption)

	// **The two rows that name the table share a fill, and it is neither
	// surface around them.** `var(--ground)` was the first answer and it
	// is the page's own colour, so the band read as a hole in the panel.
	for what, body := range map[string]string{"caption.edge-head": caption, "table.edges th": header} {
		assert.Should(t, strings.Contains(body, "background: color-mix("),
			"%s does not carry the header band's own fill: %q", what, body)
		assert.Should(t, !strings.Contains(body, "background: var(--ground)"),
			"%s is filled with the page's own colour, which is what a reader sees through the panel: %q", what, body)
	}

	// **Both of the obvious alignments are wrong on their own**, and this
	// is the pair. `align-items: center` centres each word by its own box,
	// so a serif, a mono and a sans at three sizes each sit at a different
	// height; `align-items: baseline` alone puts the shared line at the
	// top of a row-height box, which `ul.catalogue li` already records.
	assert.Must(t, captionLine != "", "no rule lays out the caption's own line")
	assert.Should(t, strings.Contains(captionLine, "align-items: baseline"),
		"the caption's line is %q, and centring each word by its own box staggers three faces", captionLine)
	assert.Should(t, strings.Contains(captionLine, "align-content: center"),
		"the caption's line is %q, so the words share a baseline and the line sits at the top of the row", captionLine)
}

// **A control that draws itself is not a box you type in.** The
// vocabulary gives every `input` a 32-pixel box, nine pixels of side
// padding, a border and a paper fill, and all four are wrong for a
// checkbox or a radio: one came out thirteen wide and thirty-two tall,
// in the operating system's blue, beside the Save button of the bool
// field editor. The reset existed twice before this guard — `.choices`
// for the account page's radios and `.tools-check` for the catalogue's
// one checkbox — which is why the other three toggles in the product
// never got it, and why this is one rule in controls.css now and a
// second copy nowhere.
func TestTheControlsThatDrawThemselvesAreNotDressedAsBoxes(t *testing.T) {
	t.Parallel()
	const reset = `input[type="checkbox"], input[type="radio"]`
	found := ""
	for _, rule := range cssRules(t, "controls.css") {
		if rule[0] == reset {
			found = strings.Join(strings.Fields(rule[1]), " ")
		}
	}
	assert.Must(t, found != "", "no rule in controls.css selects %s: a checkbox wears the box the generic "+
		"input rule gives it", reset)
	for _, want := range []string{"width: auto", "height: auto", "padding: 0", "accent-color: var(--"} {
		assert.Should(t, strings.Contains(found, want), "%s is %q, want it to carry %q", reset, found, want)
	}
	// One statement, not three. A page that resets a toggle of its own
	// is the divergence this file exists to end.
	for _, rule := range cssRules(t, "styles.css") {
		if !strings.Contains(rule[0], `[type="checkbox"]`) && !strings.Contains(rule[0], `[type="radio"]`) {
			continue
		}
		flat := strings.Join(strings.Fields(rule[1]), " ")
		assert.Should(t, !strings.Contains(flat, "accent-color") && !strings.Contains(flat, "height: auto"),
			"%s in styles.css restates the toggle reset controls.css already makes: %q", rule[0], flat)
	}
}

// **A gesture a reader learns on one screen works on the next.** The
// entity page drew a thumbnail and a preview under the pointer; the
// images page, which lists the same files from the same library, drew
// the thumbnail and nothing. Both were written by hand, a fortnight
// apart, and the second one was never given the half the first had
// grown — the rule established and not carried one step along, which
// this repository names as its second most repeated defect and which
// landed here inside the correction that established the rule.
//
// There is one builder now, in rows.js, and this is what holds the
// lists to it: a third list of images is a decision somebody makes by
// adding a line here, not a fourth hand-rolled row.
func TestEveryListOfImagesIsBuiltByTheOneThatDrawsThem(t *testing.T) {
	t.Parallel()
	const builder = "static/rows.js"

	source := func(name string) string {
		raw, err := os.ReadFile(filepath.Join("static", name))
		assert.NoErr(t, err, "read "+name)
		return string(raw)
	}

	// The builder really is the builder, so a guard reading it is not
	// asserting over an empty file.
	rows := source("rows.js")
	for _, want := range []string{"export function imageRow(", "asset-thumb", "image-preview", "image-row"} {
		assert.Must(t, strings.Contains(rows, want), "%s does not carry %q: the image row moved and this guard did not", builder, want)
	}

	// Every list of images, named. Each one hands its row to the builder
	// rather than making the picture itself.
	for _, page := range []string{"pages/assets.js", "pages/entity.js"} {
		body := source(page)
		assert.Should(t, strings.Contains(body, "imageRow("),
			"%s lists images and does not use rows.js's imageRow: a list that builds its own row is a list "+
				"the next gesture will not reach", page)
	}

	// And nobody else draws one. A module naming these classes is a
	// module building a picture of its own, which is how the two lists
	// came to disagree.
	walked := 0
	err := filepath.WalkDir("static", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".js") {
			return err
		}
		if filepath.ToSlash(path) == builder || strings.Contains(path, "vendor") {
			return nil
		}
		walked++
		body := string(mustRead(t, path))
		for _, class := range []string{`"asset-thumb"`, `"image-preview"`} {
			assert.Should(t, !strings.Contains(body, class),
				"%s builds %s itself; rows.js's imageRow is the one place an image row is made", path, class)
		}
		return nil
	})
	assert.NoErr(t, err, "walk static")
	assert.Must(t, walked > 10, "walked %d modules: this guard is reading the wrong tree", walked)
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	assert.NoErr(t, err, "read "+path)
	return raw
}
