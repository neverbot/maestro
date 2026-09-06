package web_test

import (
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/analysis"
	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/skill"
	"github.com/neverbot/maestro/internal/views"
	"github.com/neverbot/maestro/internal/web"
)

// TestToolReferenceIsCurrent is the sqlc-diff pattern applied to the
// bundle: reference/tools.md is generated and committed, and this test
// regenerates it in memory and diffs it against what is on disk. A tool
// added, renamed or removed without running the generator fails here.
//
// It is **not** enough on its own, which is why the test below it
// exists. This one compares the file against the *generator*, so a
// generator that skipped a whole domain produces a file that agrees with
// it forever.
func TestToolReferenceIsCurrent(t *testing.T) {
	want := web.NewToolReferenceServer().ToolReference()
	got := readBundleFile(t, web.ToolReferencePath)
	if got == want {
		return
	}
	t.Fatalf("%s is stale.\n\nRegenerate it with:\n\n\tgo run ./cmd/maestro-skilldoc\n\n%s",
		web.ToolReferencePath, lineDiff(want, got))
}

// TestTheToolReferenceNamesEveryRegisteredTool compares the committed
// file against the **server**, in both directions: a registered tool
// missing from the file, and a name in the file no tool answers to.
//
// It builds its own server rather than calling
// web.NewToolReferenceServer, deliberately. That constructor is what the
// generator uses; a test driving it would be asking the generator
// whether the generator saw everything. The service list here is
// assembled from the domains this server has, and the fixture below
// refuses to run over a suspiciously small table, so a server that
// registered almost nothing cannot pass this by having little to
// compare.
//
// **Mutation, run and recorded in this task's plan corrections:** make
// ToolReference skip the docs group. TestToolReferenceIsCurrent stays
// green the moment the file is regenerated from the broken generator;
// this test goes red immediately and names all fourteen docs tools.
func TestTheToolReferenceNamesEveryRegisteredTool(t *testing.T) {
	srv := web.NewServer(web.Options{
		Version:   "skilldoc-test",
		Identity:  identity.New(nil, config.Config{}),
		Projects:  projects.New(nil),
		Metamodel: metamodel.New(nil, nil),
		Markdown:  markdown.New(nil, nil),
		Views:     views.New(nil, nil),
		Analysis:  analysis.New(nil, nil),
	})
	registered := srv.ToolDescriptionsForTest()
	if len(registered) < 40 {
		t.Fatalf("this server registered %d tools; the comparison below would be over "+
			"a table too small to be the real surface", len(registered))
	}
	// Every domain this bundle routes through, asserted by hand. A
	// service silently dropped from the Options above would otherwise
	// take its whole domain out of *both* sides of the comparison.
	for _, prefix := range []string{"analysis.", "docs.", "entities.", "games.",
		"relation_types.", "relations.", "routes.", "types.", "views."} {
		if !anyToolHasPrefix(registered, prefix) {
			t.Fatalf("no registered tool starts with %q: this test's own server is missing a domain, "+
				"so the comparison below cannot see whether the file is", prefix)
		}
	}

	named := toolNamesIn(t, readBundleFile(t, web.ToolReferencePath))
	if len(named) == 0 {
		t.Fatal("no tool names were parsed out of the committed file: the parse below matches nothing " +
			"and the comparison would be over an empty set")
	}

	for name := range registered {
		if !named[name] {
			t.Errorf("%s is registered and is not in %s: run `go run ./cmd/maestro-skilldoc`",
				name, web.ToolReferencePath)
		}
	}
	for name := range named {
		if _, ok := registered[name]; !ok {
			t.Errorf("%s is listed in %s and this server registers no such tool: the index sends an "+
				"agent at a call that does not exist", name, web.ToolReferencePath)
		}
	}
}

// TestTheToolReferenceCountsWhatItLists pins the one number on the page.
// It is written from len() by the generator; this asserts the number an
// agent reads equals the number of bullets under it, so a hand-edited
// header — or a generator that counted one table and listed another —
// is a failing build rather than a sentence that quietly went false.
func TestTheToolReferenceCountsWhatItLists(t *testing.T) {
	body := readBundleFile(t, web.ToolReferencePath)
	named := toolNamesIn(t, body)
	stated := regexp.MustCompile(`(?m)^(\d+) tools\.`).FindStringSubmatch(body)
	if stated == nil {
		t.Fatalf("no \"N tools.\" line in %s: the count this test checks is not on the page",
			web.ToolReferencePath)
	}
	if want := strings.TrimSpace(stated[1]); want != strconv.Itoa(len(named)) {
		t.Fatalf("%s says %s tools and lists %d", web.ToolReferencePath, want, len(named))
	}
}

// TestTheToolReferenceCarriesFirstSentencesAndNotDescriptions is the
// line this sub-project holds, at the one place a generator could cross
// it by accident: the index must route, not restate. A description is
// six thousand characters of contract an agent already has on the wire;
// a bullet here is its opening sentence.
func TestTheToolReferenceCarriesFirstSentencesAndNotDescriptions(t *testing.T) {
	srv := web.NewToolReferenceServer()
	descriptions := srv.ToolDescriptionsForTest()
	body := readBundleFile(t, web.ToolReferencePath)

	multiSentence := 0
	for name, description := range descriptions {
		flat := strings.Join(strings.Fields(description), " ")
		first := web.FirstSentenceForTest(description)
		if first == flat {
			continue
		}
		multiSentence++
		if strings.Contains(body, flat) {
			t.Errorf("%s's whole description is in %s: the index restates a contract the wire "+
				"already carries", name, web.ToolReferencePath)
		}
		rest := strings.TrimSpace(strings.TrimPrefix(flat, first))
		if rest != "" && strings.Contains(body, rest) {
			t.Errorf("%s: text past its first sentence reached %s", name, web.ToolReferencePath)
		}
	}
	if multiSentence < 10 {
		t.Fatalf("only %d registered descriptions run past one sentence; this test would be "+
			"asserting almost nothing", multiSentence)
	}
}

// TestTheFirstSentenceRuleIsPrecise drives the split with inputs the
// real surface does not contain, which is what stops it from being a
// rule asserted only by its own output.
func TestTheFirstSentenceRuleIsPrecise(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"a plain sentence", "Read one entity. Then read another.", "Read one entity."},
		{
			"a dot inside a tool name",
			"Create or update entities. See entities.get for reading them back.",
			"Create or update entities.",
		},
		{
			"an abbreviation is not the end",
			"Declare a type, e.g. quest. Then seed it.",
			"Declare a type, e.g. quest.",
		},
		{"newlines are flattened", "Read one\nentity.\n\nThen more.", "Read one entity."},
		{"no full stop at all", "Read one entity", "Read one entity"},
		{"nothing at all", "", "(this tool was registered with no description)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := web.FirstSentenceForTest(tc.in); got != tc.want {
				t.Fatalf("firstSentence(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestTheIndexGroupsAnUnprefixedToolUnderSession drives the grouping
// with a hand-built table: whoami and search have no dot in their names,
// and a group named after the empty string would be a heading an agent
// cannot scan for.
func TestTheIndexGroupsAnUnprefixedToolUnderSession(t *testing.T) {
	page := web.ToolReferenceFromForTest(map[string]string{
		"whoami":         "Report the calling identity. And more.",
		"entities.get":   "Read one entity. And more.",
		"entities.list":  "List a game's entities. And more.",
		"views.validate": "Judge a query. And more.",
	})
	for _, want := range []string{"## session", "## entities", "## views", "- `whoami` — Report the calling identity."} {
		if !strings.Contains(page, want) {
			t.Errorf("the rendered index does not contain %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "## \n") {
		t.Error("an unprefixed tool produced an empty group heading")
	}
	if got := strings.Index(page, "## entities"); got > strings.Index(page, "## session") {
		t.Error("the groups are not in a stable, sorted order")
	}
}

func readBundleFile(t *testing.T, path string) string {
	t.Helper()
	body, err := fs.ReadFile(skill.Files(), path)
	if err != nil {
		t.Fatalf("reading %s out of the bundle: %v", path, err)
	}
	return string(body)
}

// toolNamesIn parses the tool names back out of the rendered index: the
// name in backticks that opens each bullet.
func toolNamesIn(t *testing.T, body string) map[string]bool {
	t.Helper()
	pattern := regexp.MustCompile("(?m)^- `([a-z_]+(?:\\.[a-z_]+)*)` — ")
	out := map[string]bool{}
	for _, match := range pattern.FindAllStringSubmatch(body, -1) {
		if out[match[1]] {
			t.Errorf("%s is listed twice", match[1])
		}
		out[match[1]] = true
	}
	return out
}

func anyToolHasPrefix(descriptions map[string]string, prefix string) bool {
	for name := range descriptions {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// lineDiff is a small unified-ish diff, enough for a failure message to
// show which lines moved without pulling in a dependency for it.
func lineDiff(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	wantSet := map[string]bool{}
	gotSet := map[string]bool{}
	for _, line := range wantLines {
		wantSet[line] = true
	}
	for _, line := range gotLines {
		gotSet[line] = true
	}
	var missing, extra []string
	for _, line := range wantLines {
		if !gotSet[line] {
			missing = append(missing, "+ "+line)
		}
	}
	for _, line := range gotLines {
		if !wantSet[line] {
			extra = append(extra, "- "+line)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	var b strings.Builder
	b.WriteString("lines the generator produces and the file does not have:\n")
	b.WriteString(strings.Join(missing, "\n"))
	b.WriteString("\n\nlines the file has and the generator does not produce:\n")
	b.WriteString(strings.Join(extra, "\n"))
	return b.String()
}

// conflictModeSpelling is the argument name the batch upserts will grow
// when the metamodel plan's Task 10 lands. It is one string because two
// copies of it would be the defect this whole sub-project is about.
const conflictModeSpelling = "on_conflict"

// conflictModeMention matches the argument as a whole token, with `_`
// counted as part of a word.
//
// **A plain substring search is wrong here and was wrong on its first
// run**, which is why this is a pattern: `version_conflict` — the code
// both batch upserts already answer a stale write with, and a word in
// both of their descriptions today — ends in `on_conflict`. A tripwire
// built on strings.Contains fired on the shipped surface, reported that
// a conflict mode had landed, and would have had this page rewritten
// around an argument that does not exist. It is the `request`/`quest`
// mistake in a second place, and the fixture below pins it.
var conflictModeMention = regexp.MustCompile(`(^|[^A-Za-z0-9_])` + conflictModeSpelling + `([^A-Za-z0-9_]|$)`)

// batchUpsertsTaughtByTheSeedingRecipe are the tools whose read-then-write
// loop `recipes/seeding-a-game.md` teaches.
//
// Both of them, not one. The recipe's re-seed section ends with "the same
// three steps work for edges", so a conflict mode arriving on the entity
// tool alone would still make half that page bad advice, and a tripwire
// watching only the tool the plan happened to name is the rule not
// carried one step along.
var batchUpsertsTaughtByTheSeedingRecipe = []string{"entities.upsert", "relations.upsert"}

// conflictModeOnTheBatchUpserts reports every batch upsert whose
// description has grown a conflict mode.
//
// It is a function taking the table rather than a test body reading the
// server, so its own precision can be asserted against a fixture in the
// same test — a scanner that looked at nothing would otherwise report
// nothing and read exactly like a surface that has not changed.
func conflictModeOnTheBatchUpserts(descriptions map[string]string) []string {
	var grown []string
	for _, name := range batchUpsertsTaughtByTheSeedingRecipe {
		description, ok := descriptions[name]
		if !ok {
			// A tool the recipe teaches that this server does not
			// register is a louder failure than the one this tripwire
			// watches for, and reporting it here is better than
			// silently checking nothing.
			grown = append(grown, name+" is not registered at all")
			continue
		}
		if conflictModeMention.MatchString(description) {
			grown = append(grown, name)
		}
	}
	return grown
}

// TestTheSeedingRecipeIsStillNeeded fails when a batch upsert grows an
// on_conflict argument.
//
// **This is not a drift guard. It is its mirror image.** The four guards
// around it catch a bundle that has become *false*: a page restating a
// contract, a vocabulary that lost a word, an example that stopped
// running. This one catches a bundle that has become *bad advice*.
//
// `recipes/seeding-a-game.md` teaches a three-call re-seed — list the
// type, read each row's version, send the payload back with each version
// claimed — and that loop is correct today only because there is no
// conflict mode on the surface. The day one lands, that page starts
// teaching three calls where one would do, while remaining true in every
// sentence and while every other test in this package stays green.
//
// When this goes red: rewrite the recipe's "Re-running a seed" section
// around the new argument, delete the loop and its "what this recipe
// will look like when the surface changes" section, and delete this
// test.
func TestTheSeedingRecipeIsStillNeeded(t *testing.T) {
	// The precision fixture first. Without it, a matcher that read the
	// wrong key — or a tool list that had gone empty — would report
	// nothing and be indistinguishable from a surface that has not
	// changed, which is exactly the state this test claims to detect.
	fixture := map[string]string{
		"entities.upsert": "Create or update entities. on_conflict is \"skip\" or \"replace\".",
		// The false positive that fired on the real surface the first
		// time this test ran, kept as a fixture so it cannot come back.
		"relations.upsert": "Sending the wrong version is version_conflict reporting the " +
			"version to merge onto.",
	}
	if got := conflictModeOnTheBatchUpserts(fixture); len(got) != 1 || got[0] != "entities.upsert" {
		t.Fatalf("the tripwire read %v over a fixture where exactly entities.upsert has "+
			"grown a conflict mode: it is watching the wrong thing", got)
	}
	if got := conflictModeOnTheBatchUpserts(map[string]string{}); len(got) != 2 {
		t.Fatalf("the tripwire reported %v over an empty tool table: a tripwire that "+
			"passes when it can see nothing is a tripwire that has been switched off", got)
	}

	descriptions := web.NewToolReferenceServer().ToolDescriptionsForTest()
	if grown := conflictModeOnTheBatchUpserts(descriptions); len(grown) != 0 {
		t.Fatalf("%s now documents %s: recipes/seeding-a-game.md teaches a read-then-write "+
			"re-seed loop that exists only because there was no conflict mode. Rewrite that "+
			"page around the new argument and delete this test.",
			strings.Join(grown, " and "), conflictModeSpelling)
	}
}
