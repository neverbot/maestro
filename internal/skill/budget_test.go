package skill

import (
	"fmt"
	"io/fs"
	"path"
	"strings"
	"testing"
	"testing/fstest"
)

// The page budgets. They exist because the number an agent actually pays
// for this bundle is tokens, and a page nobody bounded grows until it is
// cheaper to skip than to read.
//
// The byte caps are there because the line caps alone are gameable by a
// page of very long lines. 9000 bytes is roughly 2,000 tokens at this
// repository's measured 4.4 bytes per token for English prose with code
// fences.
//
// The table is written before the prose, deliberately: a budget added
// after the page it bounds is a budget raised to fit what was already
// written.
var budgetTiers = []budgetTier{
	{
		Name:     "entry point",
		Match:    func(p string) bool { return p == "skill.md" },
		MaxLines: 170,
		MaxBytes: 9000,
	},
	{
		Name: "reference, modelling",
		Match: func(p string) bool {
			return matchesDir(p, "reference", ".md") || matchesDir(p, "modelling", ".md")
		},
		MaxLines: 250,
		MaxBytes: 14000,
	},
	{
		Name: "genres, recipes",
		Match: func(p string) bool {
			return matchesDir(p, "genres", ".md") || matchesDir(p, "recipes", ".md")
		},
		MaxLines: 300,
		MaxBytes: 17000,
	},
	{
		Name:     "transcripts",
		Match:    func(p string) bool { return matchesDir(p, "genres", ".json") },
		MaxLines: 0, // no line cap: a transcript is machine-shaped, and its bytes are the honest bound
		MaxBytes: 60000,
	},
}

type budgetTier struct {
	Name     string
	Match    func(path string) bool
	MaxLines int // 0 means no line cap
	MaxBytes int
}

// matchesDir is true for a file sitting directly in dir with the given
// extension: "reference/errors.md" and not "reference/deep/errors.md",
// which would be a tree shape nothing in this bundle has and which the
// untiered guard should report rather than silently admit.
func matchesDir(p, dir, ext string) bool {
	return path.Dir(p) == dir && path.Ext(p) == ext
}

type budgetProblem struct {
	Path   string
	Detail string
}

type budgetReport struct {
	// Files is how many files the walk actually saw. It is in the report
	// because every assertion below is a "no problems found" assertion,
	// and "no problems found" is what an empty tree also produces.
	Files int
	Tiers map[string]string // path -> tier name

	Overruns []budgetProblem
	Untiered []string
}

// auditBudgets walks a bundle tree and reports every page over its
// tier's caps and every file no tier claims.
//
// It walks fs.WalkDir over the whole tree rather than a list of pages,
// for the reason every guard in this package does: a rule that watches
// the files it was written for stops watching the moment somebody adds
// one.
//
// **It reports a problem for an empty tree and for a tree with no
// skill.md**, which is not decoration. Every caller of this function
// asserts that it found nothing wrong, and a walk that sees no files
// finds nothing wrong — this repository has shipped a guard whose scan
// pattern matched nothing and looked clean for exactly that reason.
// TestTheBudgetGuardFailsOnAnEmptyTree drives it.
func auditBudgets(fsys fs.FS) (budgetReport, error) {
	report := budgetReport{Tiers: map[string]string{}}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		report.Files++
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		var matched []budgetTier
		for _, tier := range budgetTiers {
			if tier.Match(p) {
				matched = append(matched, tier)
			}
		}
		switch len(matched) {
		case 0:
			report.Untiered = append(report.Untiered, p)
			return nil
		case 1:
		default:
			names := make([]string, 0, len(matched))
			for _, tier := range matched {
				names = append(names, tier.Name)
			}
			report.Overruns = append(report.Overruns, budgetProblem{
				Path:   p,
				Detail: fmt.Sprintf("matches more than one tier (%s): the tier table is ambiguous about which caps apply", strings.Join(names, ", ")),
			})
			return nil
		}
		tier := matched[0]
		report.Tiers[p] = tier.Name
		if lines := countLines(body); tier.MaxLines > 0 && lines > tier.MaxLines {
			report.Overruns = append(report.Overruns, budgetProblem{
				Path:   p,
				Detail: fmt.Sprintf("%d lines, over the %q tier's cap of %d", lines, tier.Name, tier.MaxLines),
			})
		}
		if len(body) > tier.MaxBytes {
			report.Overruns = append(report.Overruns, budgetProblem{
				Path:   p,
				Detail: fmt.Sprintf("%d bytes, over the %q tier's cap of %d", len(body), tier.Name, tier.MaxBytes),
			})
		}
		return nil
	})
	if err != nil {
		return report, err
	}
	if report.Files == 0 {
		report.Overruns = append(report.Overruns, budgetProblem{
			Path:   "(the whole bundle)",
			Detail: "the walk found no files at all, so every budget below passed by having nothing to measure",
		})
		return report, nil
	}
	if _, ok := report.Tiers["skill.md"]; !ok {
		report.Overruns = append(report.Overruns, budgetProblem{
			Path:   "skill.md",
			Detail: "the bundle's entry point is missing, so the tier that bounds it measured nothing",
		})
	}
	return report, nil
}

func countLines(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	lines := strings.Count(string(body), "\n")
	if !strings.HasSuffix(string(body), "\n") {
		lines++
	}
	return lines
}

// TestEveryBundlePageFitsItsTier is the budget, over the bundle as it
// ships, one sub-test per file so a failure names the page.
func TestEveryBundlePageFitsItsTier(t *testing.T) {
	report, err := auditBudgets(Files())
	if err != nil {
		t.Fatalf("auditing the bundle: %v", err)
	}
	if report.Files == 0 {
		t.Fatal("the walk found no files: every assertion below would pass vacuously")
	}
	for _, problem := range report.Overruns {
		t.Errorf("%s: %s", problem.Path, problem.Detail)
	}
	for path, tier := range report.Tiers {
		t.Run(path, func(t *testing.T) {
			if tier == "" {
				t.Fatalf("%s is in no tier", path)
			}
		})
	}
}

// TestEveryBundleFileIsInATier is the guard on the guard. Without it a
// contributor adding advanced/analysis.md gets a 4,000-line page and a
// green suite, because the tier table never named their directory.
func TestEveryBundleFileIsInATier(t *testing.T) {
	report, err := auditBudgets(Files())
	if err != nil {
		t.Fatalf("auditing the bundle: %v", err)
	}
	if report.Files == 0 {
		t.Fatal("the walk found no files: an empty bundle has no untiered files either")
	}
	for _, p := range report.Untiered {
		t.Errorf("%s is in no budget tier: add it to budgetTiers, or put it where an existing tier already looks", p)
	}

	// The mutation, asserted here rather than run by hand once: a file
	// the table does not claim must be reported.
	scratch, err := auditBudgets(added(t, "notes.txt", "a scratch note somebody dropped in the bundle"))
	if err != nil {
		t.Fatalf("auditing the scratch overlay: %v", err)
	}
	if !contains(scratch.Untiered, "notes.txt") {
		t.Fatalf("a file in no tier was not reported: untiered = %v", scratch.Untiered)
	}
}

// TestTheBudgetGuardFailsOnAnEmptyTree is the precision fixture for the
// budget itself: every other assertion in this file is "the audit found
// nothing wrong", and an audit that reads nothing finds nothing wrong.
//
// Mutation that proves it bites: delete the report.Files == 0 branch in
// auditBudgets and this test fails.
func TestTheBudgetGuardFailsOnAnEmptyTree(t *testing.T) {
	empty, err := auditBudgets(fstest.MapFS{})
	if err != nil {
		t.Fatalf("auditing an empty tree: %v", err)
	}
	if len(empty.Overruns) == 0 {
		t.Fatal("an empty bundle passed the budget guard: the guard reads nothing and reports nothing")
	}
	if empty.Files != 0 {
		t.Fatalf("an empty tree reported %d files", empty.Files)
	}

	// And a tree that has files but not the entry point: the tier that
	// bounds skill.md would otherwise measure nothing and say so to
	// nobody.
	// It carries a page other than the entry point, so it is not the
	// empty tree wearing a different name: the emptiness branch above
	// must not be what catches this one.
	tree := mutated(t, "skill.md", nil)
	tree["reference/fields.md"] = &fstest.MapFile{Data: []byte("# Fields\n")}
	headless, err := auditBudgets(tree)
	if err != nil {
		t.Fatalf("auditing a bundle without its entry point: %v", err)
	}
	if !mentions(headless.Overruns, "skill.md") {
		t.Fatalf("a bundle with no skill.md passed the budget guard: overruns = %v", headless.Overruns)
	}
}

// TestTheBudgetGuardCatchesAnOversizedPage drives both caps, because
// they catch different things: the line cap catches a page that grew,
// and the byte cap catches the page that grew while staying inside the
// line cap by writing longer lines.
func TestTheBudgetGuardCatchesAnOversizedPage(t *testing.T) {
	tall, err := auditBudgets(mutated(t, "skill.md", func(body []byte) []byte {
		return append(body, []byte(strings.Repeat("a line of prose\n", 200))...)
	}))
	if err != nil {
		t.Fatalf("auditing the tall overlay: %v", err)
	}
	if !mentions(tall.Overruns, "lines") {
		t.Fatalf("a 200-line-longer skill.md passed the line cap: overruns = %v", tall.Overruns)
	}

	wide, err := auditBudgets(mutated(t, "skill.md", func(body []byte) []byte {
		return append(body, []byte(strings.Repeat("x", 20000)+"\n")...)
	}))
	if err != nil {
		t.Fatalf("auditing the wide overlay: %v", err)
	}
	if !mentions(wide.Overruns, "bytes") {
		t.Fatalf("a skill.md with one 20,000-byte line passed the byte cap: overruns = %v", wide.Overruns)
	}
}

func contains(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}

func mentions(problems []budgetProblem, substring string) bool {
	for _, problem := range problems {
		if strings.Contains(problem.Path, substring) || strings.Contains(problem.Detail, substring) {
			return true
		}
	}
	return false
}
