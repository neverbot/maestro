package web_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/neverbot/maestro/internal/skill"
	"github.com/neverbot/maestro/internal/web"
)

// This file is the anti-restatement guard: the one-tool test, mechanised.
//
// A sentence that names exactly one registered tool and makes a claim
// about that tool's arguments, admitted values, defaults, bounds or
// refusals belongs in that tool's description. The bundle may route to
// it, and may quote it verbatim with attribution; it may not reword it.
//
// The guard catches that in **both** directions, which is the whole
// point: the bundle inventing a claim (no description says it), and a
// description being reworded underneath a quote that used to match
// (the quote stops being verbatim). Reword a shared description and
// every page quoting it goes red in the same commit as the reword.

// restatementFixtures are the hand-written inputs the guard is asserted
// against on every run. Half of them it must flag and half it must not,
// because a scanner that has stopped working and a bundle that is clean
// produce the same empty list — the failure this repository has shipped
// four times, each break a level below the last.
var restatementFixtures = []struct {
	name string
	// page is markdown, scanned exactly as a bundle page is.
	page string
	// want is how many violations the guard must report.
	want int
	// contains, when set, must appear in the first violation's text.
	contains string
}{
	{
		name: "a claim about one tool, unquoted",
		page: "entities.upsert never clamps a batch.",
		want: 1, contains: "not inside an attributed quote block",
	},
	{
		name: "a sequence over two tools is the bundle's own subject",
		page: "Declare types before relation types, then entities.upsert in batches, then relations.upsert.",
		want: 0,
	},
	{
		name: "a claim wrapped across two lines is still one claim",
		// The bundle is hard-wrapped, and the wrap falls between the tool's
		// name and the claim about it. A line-at-a-time splitter sees a
		// mention with no claim, then a claim about nothing, and reports
		// nothing at all — which is the exact shape of a guard that has
		// stopped working while the bundle looks clean.
		page: "A batch is a batch: entities.upsert\naccepts at most 500 items and refuses a larger one.",
		want: 1,
	},
	{
		name: "a claim in a bullet is still a claim",
		page: "- entities.upsert refuses a batch over its cap.",
		want: 1,
	},
	{
		name: "a claim in a table cell is still a claim",
		page: "| tool | rule |\n|---|---|\n| batching | entities.upsert refuses an oversized batch |",
		want: 1,
	},
	{
		name: "a claim in a heading is still a claim",
		page: "## entities.upsert always refuses an oversized batch",
		want: 1,
	},
	{
		name: "routing names a tool and claims nothing",
		page: "Read `entities.upsert`'s own description before your first batch.",
		want: 0,
	},
	{
		name: "a mention of a longer tool name is not a mention of its prefix",
		// views.list_assets contains views.list. A substring matcher reads
		// this as naming one tool and flags it; a whole-token matcher reads
		// it as naming one tool too — but the *right* one, which is what
		// the attribution below is checked against.
		page: "> **From `views.list`'s own description:**\n> views.list_assets returns nothing of the sort",
		want: 1, contains: "attributed to views.list and the claim is about views.list_assets",
	},
	{
		name: "an attribution to a tool the server does not register",
		page: "> **From `analysis.orphans`'s own description:**\n> entities.upsert accepts at most 500 items",
		want: 1, contains: "does not register",
	},
	{
		name: "an abbreviation does not end a sentence",
		// If "e.g." split the sentence, the tool name and the marker would
		// land in different sentences and the claim would go unreported.
		page: "One tool takes a batch, e.g. entities.upsert, and never clamps it.",
		want: 1,
	},
}

// TestTheBundleRestatesNoToolDescription is the guard, run over the real
// bundle and over the fixtures above in the same test, so a scanner that
// has stopped scanning cannot present itself as a clean bundle.
func TestTheBundleRestatesNoToolDescription(t *testing.T) {
	registered := web.NewToolReferenceServer().ToolDescriptionsForTest()
	if len(registered) < 40 {
		t.Fatalf("this server registered %d tools; the scan below would be against a table "+
			"too small to be the real surface", len(registered))
	}

	// The fixtures first. If these do not behave, nothing the real bundle
	// says below is evidence of anything.
	for _, fixture := range restatementFixtures {
		t.Run(fixture.name, func(t *testing.T) {
			found := web.AuditRestatementForTest(
				web.ScanMarkdownForTest("fixture.md", fixture.page), registered)
			if len(found) != fixture.want {
				t.Fatalf("the guard reported %d violations, want %d, for:\n%s\n\n%s",
					len(found), fixture.want, fixture.page, strings.Join(found, "\n"))
			}
			if fixture.contains != "" && !strings.Contains(found[0], fixture.contains) {
				t.Fatalf("the violation does not explain itself with %q:\n%s",
					fixture.contains, found[0])
			}
		})
	}

	sentences, err := web.ScanBundleForTest(skill.Files())
	if err != nil {
		t.Fatalf("scanning the bundle: %v", err)
	}
	// The scan is asserted to have seen the bundle before its silence is
	// read as good news: some sentences at all, and some of them naming a
	// registered tool.
	if len(sentences) < 20 {
		t.Fatalf("the scan produced %d sentences over the whole bundle: it is not reading "+
			"the tree, and its silence below would mean nothing", len(sentences))
	}
	// Two independent ways a scanned sentence can be *about* a tool, both
	// asserted, because each is the whole input of one half of the guard:
	// the sentence names a tool in its own text (the unquoted half), and
	// the sentence sits under an attribution (the verbatim half, which is
	// where the generated index's every bullet lands).
	naming, attributed := 0, 0
	for _, sentence := range sentences {
		if len(web.ToolsNamedInForTest(sentence.Text, registered)) > 0 {
			naming++
		}
		if sentence.InQuote && sentence.QuoteFor != "" {
			attributed++
		}
	}
	if naming < 5 {
		t.Fatalf("only %d scanned sentences name a registered tool: the matcher is not "+
			"matching, so nothing below can be caught", naming)
	}
	if attributed < len(registered) {
		t.Fatalf("only %d scanned sentences are inside an attribution and the server "+
			"registers %d tools: the generated index's bullets are not being read as the "+
			"attributed quotes they are, so no reword of a description would be caught here",
			attributed, len(registered))
	}

	for _, violation := range web.AuditRestatementForTest(sentences, registered) {
		t.Error(violation)
	}
}

// TestEveryClaimMarkerIsLiveAndBounded runs the plan's step-2 mutation on
// every build instead of once by hand. Emptying the marker list, or
// dropping any single marker from it, makes one of these sub-tests fail.
//
// The second half is the precision half: a marker must match its own
// word and not a word that contains it. `mistakes` is not `takes`, and
// the first draft of a guard of this shape in this repository matched by
// substring and reported seven false positives.
func TestEveryClaimMarkerIsLiveAndBounded(t *testing.T) {
	registered := map[string]string{"entities.upsert": "Write entities in bulk."}
	markers := web.ClaimMarkersForTest()
	if len(markers) == 0 {
		t.Fatal("the claim-marker list is empty, so the guard flags nothing and every " +
			"assertion made against it is vacuous")
	}
	for _, marker := range markers {
		t.Run(marker, func(t *testing.T) {
			page := fmt.Sprintf("entities.upsert %s something.", marker)
			found := web.AuditRestatementForTest(web.ScanMarkdownForTest("fixture.md", page), registered)
			if len(found) != 1 {
				t.Fatalf("%q reported %d violations for %q, want 1", marker, len(found), page)
			}
		})
	}
	for _, page := range []string{
		"entities.upsert mistakes are common in a first seeding pass.",
		"entities.upsert and mustard have nothing in common.",
	} {
		found := web.AuditRestatementForTest(web.ScanMarkdownForTest("fixture.md", page), registered)
		if len(found) != 0 {
			t.Errorf("a word containing a marker was read as a marker in %q:\n%s",
				page, strings.Join(found, "\n"))
		}
	}
}

// TestAQuoteIsCheckedAgainstTheDescriptionItClaimsToCopy is the
// direction that makes the bundle safe to detach from the server: a
// quote is a copy, and a copy drifts, so the build compares it against
// its original on every run.
//
// The reword mutation is run here rather than described: the same page
// is audited against a tool table holding the description it quotes, and
// then against one where that description has been reworded. The first
// must pass and the second must fail. That is the whole claim of this
// sub-project, asserted rather than promised.
func TestAQuoteIsCheckedAgainstTheDescriptionItClaimsToCopy(t *testing.T) {
	page := "> **From `entities.upsert`'s own description:**\n" +
		"> It accepts at most 500 items in one call and refuses a larger\n" +
		"> batch rather than clamping it."

	original := map[string]string{
		"entities.upsert": "Write entities in bulk. It accepts at most 500 items in one call " +
			"and refuses a larger batch rather than clamping it.",
	}
	if found := web.AuditRestatementForTest(
		web.ScanMarkdownForTest("fixture.md", page), original); len(found) != 0 {
		t.Fatalf("a verbatim, attributed quote was refused:\n%s", strings.Join(found, "\n"))
	}

	reworded := map[string]string{
		"entities.upsert": "Write entities in bulk. A call carries up to 500 items; a bigger " +
			"batch is refused outright and never trimmed.",
	}
	found := web.AuditRestatementForTest(web.ScanMarkdownForTest("fixture.md", page), reworded)
	if len(found) == 0 {
		t.Fatal("the description was reworded underneath the quote and the page stayed green: " +
			"the bundle would ship a sentence the server no longer says")
	}
	if !strings.Contains(found[0], "not a verbatim substring") {
		t.Fatalf("the failure does not say what went wrong:\n%s", found[0])
	}

	// And the exemption cannot be satisfied by emptiness. Every string
	// contains the empty string, so a quote of nothing, or a quote of a
	// tool registered with no description, must be refused rather than
	// admitted.
	empty := map[string]string{"entities.upsert": ""}
	emptyPage := "> **From `entities.upsert`'s own description:**\n> entities.upsert accepts anything."
	found = web.AuditRestatementForTest(web.ScanMarkdownForTest("fixture.md", emptyPage), empty)
	if len(found) == 0 {
		t.Error("a quote attributed to a tool with an empty description passed: the " +
			"substring check is satisfied by emptiness")
	}
}

// TestTheToolMatcherReadsWholeTokens pins the shape of four broken
// guards in this repository's history: a name matched by substring,
// where one alias contains another.
func TestTheToolMatcherReadsWholeTokens(t *testing.T) {
	registered := map[string]string{
		"views.list": "…", "views.list_assets": "…", "entities.get": "…",
	}
	for _, tc := range []struct {
		text string
		want []string
	}{
		{"views.list_assets returns the images", []string{"views.list_assets"}},
		{"views.list returns the saved views", []string{"views.list"}},
		{"both views.list and views.list_assets", []string{"views.list", "views.list_assets"}},
		{"my_views.list is not a tool", nil},
		{"entities.getter is not a tool", nil},
		{"`entities.get`'s own description", []string{"entities.get"}},
	} {
		got := web.ToolsNamedInForTest(tc.text, registered)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("toolsNamedIn(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

// TestTheSentenceSplitterIsPrecise drives the split with the shapes that
// would make the guard blind: a dot inside a tool name, an
// abbreviation, and a sentence wrapped across lines.
func TestTheSentenceSplitterIsPrecise(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want []string
	}{
		{"a dot inside a tool name does not split", "Call entities.get first. Then read it.",
			[]string{"Call entities.get first.", "Then read it."}},
		{"an abbreviation does not split", "Declare a type, e.g. quest. Then seed it.",
			[]string{"Declare a type, e.g. quest.", "Then seed it."}},
		{"a question is a sentence", "Which tool? This one.",
			[]string{"Which tool?", "This one."}},
		{"no terminator at all", "Read one entity", []string{"Read one entity"}},
		{"nothing at all", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := web.SplitSentencesForTest(tc.in)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("split(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// The wrapped-sentence case, through the markdown scanner rather than
	// the splitter, because it is the join that does the work.
	scanned := web.ScanMarkdownForTest("fixture.md",
		"A batch is a batch: entities.upsert accepts at\nmost 500 items.")
	if len(scanned) != 1 {
		t.Fatalf("a wrapped sentence produced %d sentences: %+v", len(scanned), scanned)
	}
	if !strings.Contains(scanned[0].Text, "accepts at most 500") {
		t.Fatalf("the wrap was not joined: %q", scanned[0].Text)
	}
}

// TestTheScannerReadsTranscriptStrings carries the rule one step along.
// A genre transcript is authored text: a claim restated in a `_comment`
// is a claim, and a guard that only reads markdown would never see it.
func TestTheScannerReadsTranscriptStrings(t *testing.T) {
	registered := map[string]string{"entities.upsert": "Write entities in bulk."}
	body := []byte(`{"_comment": "entities.upsert accepts at most 500 items.", "steps": []}`)
	sentences, err := web.ScanJSONForTest("genres/fixture.json", body)
	if err != nil {
		t.Fatalf("scanning the fixture transcript: %v", err)
	}
	if found := web.AuditRestatementForTest(sentences, registered); len(found) != 1 {
		t.Fatalf("a restatement inside a transcript string reported %d violations, want 1: %v",
			len(found), found)
	}

	// And through the whole-tree walk, so the JSON branch is reached by
	// scanBundle and not only by a test calling it directly — the "correct
	// in the module, dead at the call site" shape.
	tree := fstest.MapFS{
		"skill.md":            &fstest.MapFile{Data: []byte("# Fixture\n")},
		"genres/fixture.json": &fstest.MapFile{Data: body},
	}
	all, err := web.ScanBundleForTest(tree)
	if err != nil {
		t.Fatalf("scanning the fixture tree: %v", err)
	}
	if found := web.AuditRestatementForTest(all, registered); len(found) != 1 {
		t.Fatalf("scanBundle reported %d violations over a tree whose transcript restates "+
			"a bound, want 1: %v", len(found), found)
	}
}

// TestNoBundlePageNamesAnUnregisteredTool is what makes the ship-order
// rule mechanical. A page that mentions an analysis tool before any
// analysis tool is registered is a failing build, not a review comment.
func TestNoBundlePageNamesAnUnregisteredTool(t *testing.T) {
	registered := web.NewToolReferenceServer().ToolDescriptionsForTest()

	tokens, err := web.BundleToolTokensForTest(skill.Files())
	if err != nil {
		t.Fatalf("reading the bundle's tool tokens: %v", err)
	}
	if len(tokens) < 40 {
		t.Fatalf("only %d backticked tool tokens were found in the whole bundle: the scan "+
			"is not reading it, and its silence would mean nothing", len(tokens))
	}
	for _, token := range tokens {
		if _, ok := registered[token.Name]; !ok {
			t.Errorf("%s:%d names `%s`, which this server does not register: a page that "+
				"describes an unimplemented surface sends an agent at a call that does not exist",
				token.Path, token.Line, token.Name)
		}
	}

	// The plan's step-4 mutation, run on every build rather than once by
	// hand: a scratch tree carrying a tool that does not exist.
	tree := fstest.MapFS{
		"skill.md": &fstest.MapFile{Data: []byte("Run `analysis.orphans` after seeding.\n")},
	}
	scratch, err := web.BundleToolTokensForTest(tree)
	if err != nil {
		t.Fatalf("reading the scratch tree: %v", err)
	}
	if len(scratch) != 1 || scratch[0].Name != "analysis.orphans" || scratch[0].Line != 1 {
		t.Fatalf("the scan did not report `analysis.orphans` at skill.md:1, got %+v", scratch)
	}

	// And the false positive that would get this scanner deleted: the
	// bundle names its own pages constantly, and `skill.md` is not a tool.
	names := fstest.MapFS{
		"skill.md": &fstest.MapFile{Data: []byte("See `reference/tools.md` and `skill.md`.\n")},
	}
	filenames, err := web.BundleToolTokensForTest(names)
	if err != nil {
		t.Fatalf("reading the filename tree: %v", err)
	}
	if len(filenames) != 0 {
		t.Errorf("a filename was read as a tool name: %+v", filenames)
	}
}

// notYetTaughtOutsideTheIndex is the ratchet this task leaves behind,
// and it is a deviation from the plan recorded in the plan's own
// corrections.
//
// The plan's step 3 asks that every tool outside a small administrative
// set appear in some page other than the generated index, so no tool is
// reachable only from a list. That rule is a property of the *finished*
// bundle: today the bundle is `skill.md` and the generated index, and
// the prose pages that would teach these tools are Tasks 5 to 10. Writing
// the rule as an aspiration for those tasks to remember is exactly the
// failure this plan's own preamble names, so it is written as a set
// equality instead: this list is exactly the tools no page but the index
// mentions. A task that teaches a tool and does not shorten this list
// fails, and a tool that quietly stops being taught fails too. Task 12
// asserts the list is empty.
var notYetTaughtOutsideTheIndex = []string{
	"docs.delete", "docs.diff", "docs.history",
	"docs.kinds", "docs.links.add", "docs.links.list",
	"docs.links.remove", "docs.list", "docs.move",
	"docs.read", "docs.read_version", "docs.revert",
	"docs.write", "docs.write_many", "entities.get",
	"entities.list", "entities.remove", "entities.repair",
	"games.counts", "games.get", "games.list",
	"relation_types.get", "relation_types.list", "relation_types.remove",
	"relation_types.rename", "relation_types.upsert", "relations.get",
	"relations.list", "relations.remove", "relations.repair",
	"relations.upsert", "search", "types.get",
	"types.list", "types.remove", "types.rename",
	"types.upsert", "views.clear_positions", "views.get",
	"views.list", "views.list_assets", "views.remove",
	"views.run", "views.set_background", "views.set_positions",
	"views.upsert", "views.validate", "whoami",
}

// TestEveryRegisteredToolIsRoutedFromTheBundle asserts every registered
// tool is reachable from the bundle at all (through the index), and pins
// exactly which ones are reachable *only* from the index.
func TestEveryRegisteredToolIsRoutedFromTheBundle(t *testing.T) {
	registered := web.NewToolReferenceServer().ToolDescriptionsForTest()
	// Mentions, not dotted tokens. `whoami` and `search` are registered
	// tools with no dot in their names, and a routing guard built on the
	// dotted scan would report those two as unrouted forever — a guard
	// failing for a reason that has nothing to do with the bundle is a
	// guard somebody switches off.
	tokens, err := web.BundleToolMentionsForTest(skill.Files(), registered)
	if err != nil {
		t.Fatalf("reading the bundle's tool mentions: %v", err)
	}
	if len(tokens) < len(registered) {
		t.Fatalf("only %d tool mentions were found across the whole bundle and %d tools are "+
			"registered: the scan is not reading the tree", len(tokens), len(registered))
	}

	inIndex := map[string]bool{}
	elsewhere := map[string]bool{}
	for _, token := range tokens {
		if token.Path == web.ToolReferencePath {
			inIndex[token.Name] = true
			continue
		}
		elsewhere[token.Name] = true
	}
	if len(inIndex) == 0 {
		t.Fatal("no tool names were found in the generated index: the comparison below " +
			"would be over an empty set")
	}

	var onlyInIndex []string
	for name := range registered {
		if !inIndex[name] {
			t.Errorf("%s is registered and is not routed from %s", name, web.ToolReferencePath)
		}
		if !elsewhere[name] {
			onlyInIndex = append(onlyInIndex, name)
		}
	}
	sort.Strings(onlyInIndex)

	want := append([]string(nil), notYetTaughtOutsideTheIndex...)
	sort.Strings(want)
	if strings.Join(onlyInIndex, "\n") != strings.Join(want, "\n") {
		t.Errorf("the tools reachable only from the index have moved.\n\nfound:\n%s\n\nrecorded "+
			"in notYetTaughtOutsideTheIndex:\n%s\n\nA page that teaches a tool shortens that "+
			"list in the same commit; a tool that stops being taught lengthens it.",
			strings.Join(onlyInIndex, "\n"), strings.Join(want, "\n"))
	}
}
