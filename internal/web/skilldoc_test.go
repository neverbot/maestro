package web_test

import (
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

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
	})
	registered := srv.ToolDescriptionsForTest()
	if len(registered) < 40 {
		t.Fatalf("this server registered %d tools; the comparison below would be over "+
			"a table too small to be the real surface", len(registered))
	}
	// Every domain this bundle routes through, asserted by hand. A
	// service silently dropped from the Options above would otherwise
	// take its whole domain out of *both* sides of the comparison.
	for _, prefix := range []string{"docs.", "entities.", "games.", "relation_types.", "relations.", "types.", "views."} {
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
