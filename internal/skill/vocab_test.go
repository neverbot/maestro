package skill_test

import (
	"io/fs"
	"path"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/skill"
	"github.com/neverbot/maestro/internal/views"
)

// knownVocabularies is every vocabulary name a guard in this repository
// knows how to check, with where the check lives.
//
// It is the list the "one step along" guard below is built on: a fence
// named vocab:layout_modes that no guard checks is an unchecked
// enumeration wearing the costume of a checked one, and a checker with
// no fence is a comparison against nothing.
var knownVocabularies = map[string]string{
	"field_types":    "TestBundleVocabulariesMatchCode, here",
	"semantic_roles": "TestBundleVocabulariesMatchCode, here",
	"renderers":      "TestBundleVocabulariesMatchCode, here",
	// error_codes cannot be checked from this package: its source of
	// truth is a delimited region of internal/web/auth.go, and
	// internal/web imports this package, so the dependency has exactly
	// one direction. The check lives in
	// internal/web/skilldoc_vocab_test.go, which also asserts this fence
	// exists — so naming it here as "checked elsewhere" is a claim with
	// a test behind it rather than a promise.
	"error_codes": "TestBundleErrorCodesMatchTheSurface, in internal/web",
}

// TestBundleVocabulariesMatchCode set-compares every vocabulary this
// package can reach against its single Go declaration, in both
// directions: a word the code has and the bundle lacks (a seventh field
// type, a fifth renderer) and a word the bundle has and the code lacks
// (a value an agent would be told to send and the server would refuse).
func TestBundleVocabulariesMatchCode(t *testing.T) {
	fences, err := skill.VocabFences(skill.Files())
	if err != nil {
		t.Fatalf("reading the bundle's vocab fences: %v", err)
	}

	for _, tc := range []struct {
		name string
		code []string
	}{
		{"field_types", fieldTypeStrings()},
		{"semantic_roles", metamodel.SemanticRoles},
		{"renderers", views.RendererNames()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle, err := skill.VocabWords(fences, tc.name)
			if err != nil {
				t.Fatalf("%v", err)
			}
			// Neither side may be empty. Two empty sets compare equal, and a
			// comparison that runs over nothing is the failure this
			// repository has now shipped under three different names.
			if len(bundle) == 0 || len(tc.code) == 0 {
				t.Fatalf("the bundle lists %d words and the code declares %d: a comparison "+
					"over an empty set passes against anything", len(bundle), len(tc.code))
			}
			assertSameSet(t, tc.name, bundle, tc.code)
		})
	}
}

// TestEveryVocabFenceIsKnownAndEveryKnownVocabIsFenced carries the rule
// one step along, which is where every guard in this repository has
// died: the set of fence names in the bundle must equal the set of
// vocabularies a guard knows how to check.
//
// Without it, a contributor adds ```vocab:layout_modes```, no guard
// looks at it, and the suite is green over an enumeration nothing
// compares — the same defect as a budget tier table that does not name a
// new directory.
func TestEveryVocabFenceIsKnownAndEveryKnownVocabIsFenced(t *testing.T) {
	fences, err := skill.VocabFences(skill.Files())
	if err != nil {
		t.Fatalf("reading the bundle's vocab fences: %v", err)
	}
	if len(fences) == 0 {
		t.Fatal("the bundle carries no vocab fences at all: every comparison this file " +
			"makes would be over an empty set")
	}

	found := map[string]bool{}
	for _, fence := range fences {
		found[fence.Name] = true
		if _, known := knownVocabularies[fence.Name]; !known {
			t.Errorf("%s:%d carries a vocab:%s fence and no guard checks it: an enumeration "+
				"nothing compares is a second copy of a list, which is what the fences exist "+
				"to prevent", fence.Path, fence.Line, fence.Name)
		}
	}
	for name, where := range knownVocabularies {
		if !found[name] {
			t.Errorf("%s checks vocab:%s and the bundle carries no such fence: the "+
				"comparison runs over an empty set and passes", where, name)
		}
	}
}

// TestTheFenceReaderIsPrecise drives the reader with pages the real
// bundle does not contain. A reader that silently returned nothing would
// make every set comparison above pass, so its own failures are asserted
// rather than assumed.
func TestTheFenceReaderIsPrecise(t *testing.T) {
	t.Run("a fence is read, wrapped across lines", func(t *testing.T) {
		fences := mustFences(t, page("```vocab:field_types\ntext longtext\nnumber\n```\n"))
		words, err := skill.VocabWords(fences, "field_types")
		if err != nil {
			t.Fatalf("%v", err)
		}
		if strings.Join(words, " ") != "text longtext number" {
			t.Fatalf("words = %q", words)
		}
	})
	t.Run("an ordinary code fence is not a vocabulary", func(t *testing.T) {
		fences := mustFences(t, page("```json\n{\"a\": 1}\n```\n"))
		if len(fences) != 0 {
			t.Fatalf("a json fence was read as a vocabulary: %+v", fences)
		}
	})
	t.Run("an unnamed fence is an error", func(t *testing.T) {
		if _, err := skill.VocabFences(page("```vocab\ntext\n```\n")); err == nil {
			t.Fatal("an unnamed vocab fence was accepted")
		}
	})
	t.Run("an unclosed fence is an error", func(t *testing.T) {
		if _, err := skill.VocabFences(page("```vocab:field_types\ntext\n")); err == nil {
			t.Fatal("an unterminated vocab fence was accepted")
		}
	})
	t.Run("a missing vocabulary is an error, not an empty set", func(t *testing.T) {
		fences := mustFences(t, page("# nothing here\n"))
		if _, err := skill.VocabWords(fences, "field_types"); err == nil {
			t.Fatal("a vocabulary the bundle does not carry came back as an empty word list, " +
				"which every comparison would then pass against")
		}
	})
	t.Run("an empty fence is an error", func(t *testing.T) {
		fences := mustFences(t, page("```vocab:field_types\n```\n"))
		if _, err := skill.VocabWords(fences, "field_types"); err == nil {
			t.Fatal("an empty fence came back as an empty word list")
		}
	})
	t.Run("two fences for one vocabulary is an error", func(t *testing.T) {
		fences := mustFences(t, fstest.MapFS{
			"a.md": &fstest.MapFile{Data: []byte("```vocab:field_types\ntext\n```\n")},
			"b.md": &fstest.MapFile{Data: []byte("```vocab:field_types\nbool\n```\n")},
		})
		if _, err := skill.VocabWords(fences, "field_types"); err == nil {
			t.Fatal("one vocabulary in two fences was accepted: the two copies drift apart " +
				"and the guard checks whichever it found")
		}
	})
	t.Run("the walk reaches a nested page", func(t *testing.T) {
		fences := mustFences(t, fstest.MapFS{
			"reference/fields.md": &fstest.MapFile{
				Data: []byte("```vocab:field_types\ntext\n```\n"),
			},
		})
		if len(fences) != 1 || fences[0].Path != "reference/fields.md" {
			t.Fatalf("the walk did not reach a page in a subdirectory: %+v", fences)
		}
	})
}

// TestTheSetComparisonCatchesBothDirections drives the comparison
// itself, because every assertion above is of the form "it found nothing
// wrong", which is also what a comparison that compares nothing reports.
func TestTheSetComparisonCatchesBothDirections(t *testing.T) {
	for _, tc := range []struct {
		name         string
		bundle, code []string
		missing      []string
		extra        []string
	}{
		{
			name:   "a word the code has and the bundle lacks",
			bundle: []string{"text"}, code: []string{"text", "bool"},
			missing: []string{"bool"},
		},
		{
			name:   "a word the bundle has and the code lacks",
			bundle: []string{"text", "colour"}, code: []string{"text"},
			extra: []string{"colour"},
		},
		{
			name:   "the same set in a different order is the same set",
			bundle: []string{"bool", "text"}, code: []string{"text", "bool"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			missing, extra := skill.DiffVocabularies(tc.bundle, tc.code)
			if strings.Join(missing, ",") != strings.Join(tc.missing, ",") {
				t.Errorf("missing = %v, want %v", missing, tc.missing)
			}
			if strings.Join(extra, ",") != strings.Join(tc.extra, ",") {
				t.Errorf("extra = %v, want %v", extra, tc.extra)
			}
		})
	}
}

// assertSameSet compares two vocabularies in both directions and says
// which side is missing what.
func assertSameSet(t *testing.T, name string, bundle, code []string) {
	t.Helper()
	missing, extra := skill.DiffVocabularies(bundle, code)
	if len(missing) > 0 {
		t.Errorf("vocab:%s — the code declares %v and the bundle's fence does not list them: "+
			"an agent reading this page would never know the value exists", name, missing)
	}
	if len(extra) > 0 {
		t.Errorf("vocab:%s — the bundle lists %v and no such value is declared in Go: an "+
			"agent would send a word the server refuses", name, extra)
	}
}

func fieldTypeStrings() []string {
	out := make([]string, 0, len(metamodel.FieldTypes))
	for _, fieldType := range metamodel.FieldTypes {
		out = append(out, string(fieldType))
	}
	return out
}

func page(body string) fstest.MapFS {
	return fstest.MapFS{"skill.md": &fstest.MapFile{Data: []byte(body)}}
}

func mustFences(t *testing.T, fsys fstest.MapFS) []skill.VocabFence {
	t.Helper()
	fences, err := skill.VocabFences(fsys)
	if err != nil {
		t.Fatalf("reading fences: %v", err)
	}
	return fences
}

// The modelling pages are the three in this bundle that no generator
// could produce and no test can judge. Whether the advice on them is
// *good* is a question for a reader; these three guards pin the three
// properties of them that are not a matter of taste.
//
// Each reads the shipped pages out of skill.Files(), not a fixture. A
// guard pointed at a fixture is a guard that stays green while the
// shipped page says anything at all — and it walks every modelling/*.md
// rather than the three that exist today, so a fourth page added
// tomorrow is judged without editing this file.

// modellingPages reads every modelling/*.md out of a bundle tree.
func modellingPages(t *testing.T, fsys fs.FS) map[string]string {
	t.Helper()
	pages := map[string]string{}
	err := fs.WalkDir(fsys, "modelling", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Ext(p) != ".md" {
			return nil
		}
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		pages[p] = string(body)
		return nil
	})
	if err != nil {
		t.Fatalf("walking the modelling pages: %v", err)
	}
	if len(pages) == 0 {
		t.Fatal("no modelling page was found: every assertion below would pass over an " +
			"empty set, which is how a guard of this shape goes quiet")
	}
	return pages
}

// costPhrases is the closed list of ways these pages state a
// consequence. Closed rather than a heuristic, for the reason every
// closed list in this sub-project is closed: a heuristic drifts, and one
// that drifted towards matching everything would pass a style guide.
var costPhrases = []string{"calls become", "one write per", "costs", "cost"}

func statesACost(body string) bool {
	lowered := strings.ToLower(body)
	for _, phrase := range costPhrases {
		if strings.Contains(lowered, phrase) {
			return true
		}
	}
	return false
}

// TestEveryModellingPageStatesACost. A modelling page with no
// consequence in it is a style guide: it tells an agent what somebody
// prefers rather than what the alternative will cost, and an agent given
// a preference has no way to weigh it against the thing it is trading
// away.
func TestEveryModellingPageStatesACost(t *testing.T) {
	for name, body := range modellingPages(t, skill.Files()) {
		if !statesACost(body) {
			t.Errorf("%s states no cost: a modelling page that names no consequence is a "+
				"style guide, and the pages here exist to say what the wrong shape is paid "+
				"for in", name)
		}
	}

	// The precision fixture. Every assertion above is "the check found
	// nothing wrong", and a check whose phrase list matched every input
	// reports nothing wrong too.
	if statesACost("keys") {
		t.Error("a page consisting of the word \"keys\" passed the cost check: the phrase " +
			"list matches text that states no consequence at all")
	}
	if !statesACost("Six calls become six hundred.") {
		t.Error("a page stating \"six calls become six hundred\" failed the cost check: the " +
			"phrase list does not match the shape it was written for")
	}
}

// renameSentences splits a page into sentences, cheaply. It is a
// different splitter from internal/web's, deliberately: this package may
// not import that one, and a sentence rule good enough to hold a page to
// "do not say a type cannot be renamed" needs no more than this.
func renameSentences(body string) []string {
	flat := strings.Join(strings.Fields(body), " ")
	var out []string
	start := 0
	for i, r := range flat {
		if r == '.' || r == '!' || r == '?' || r == ':' {
			if piece := strings.TrimSpace(flat[start : i+1]); piece != "" {
				out = append(out, piece)
			}
			start = i + 1
		}
	}
	if piece := strings.TrimSpace(flat[start:]); piece != "" {
		out = append(out, piece)
	}
	return out
}

// deniedRename reports the sentences of a page that assert something
// cannot be renamed without saying it is an entity or a row.
//
// The asymmetry is the point: a *type* can be renamed and an *entity*
// cannot, and a page that states only the first half sends an agent at a
// tool that does not exist, while a page that states only the second
// half sends it at the delete-and-recreate workaround the rename
// replaced — which loses every edge.
func deniedRename(body string) []string {
	var out []string
	for _, sentence := range renameSentences(body) {
		lowered := strings.ToLower(sentence)
		if !strings.Contains(lowered, "rename") {
			continue
		}
		denied := false
		for _, word := range []string{"cannot", "can not", "never", "no way", "impossible"} {
			if strings.Contains(lowered, word) {
				denied = true
			}
		}
		if !denied {
			continue
		}
		if strings.Contains(lowered, "entity") || strings.Contains(lowered, "row") {
			continue
		}
		out = append(out, sentence)
	}
	return out
}

// TestTheRenameRuleIsTaughtBothWays exists because the spec this bundle
// was planned from was written before the rename shipped, and it says in
// as many words that there is no rename operation. The most likely
// defect in this whole sub-project is a page repeating that.
func TestTheRenameRuleIsTaughtBothWays(t *testing.T) {
	pages := modellingPages(t, skill.Files())
	naming, ok := pages["modelling/naming.md"]
	if !ok {
		t.Fatal("modelling/naming.md is missing: the page that carries the rename rule is " +
			"the page this guard reads")
	}
	for _, tool := range []string{"types.rename", "relation_types.rename"} {
		if !strings.Contains(naming, "`"+tool+"`") {
			t.Errorf("modelling/naming.md does not name %s: half the rename rule is "+
				"missing, and an agent that does not know the call exists is left with the "+
				"delete-and-recreate workaround, which loses every edge", tool)
		}
	}
	if !strings.Contains(strings.ToLower(naming), "permanent") {
		t.Error("modelling/naming.md never says an entity's own key is permanent: the other " +
			"half of the asymmetry, and the one an agent gets wrong in the expensive direction")
	}
	for name, body := range pages {
		for _, sentence := range deniedRename(body) {
			t.Errorf("%s says %q. A type's key can be renamed, addressed by the key it has "+
				"now; only an entity's own key is permanent. Say which of the two the "+
				"sentence is about", name, sentence)
		}
	}

	// The plan's mutation, run on every build: the false half pasted into
	// a page, and the true half beside it, which must not be reported.
	if len(deniedRename("A type cannot be renamed.")) != 1 {
		t.Error("the guard did not catch \"a type cannot be renamed\": the sentence the " +
			"overtaken spec would have put on this page passes")
	}
	if got := deniedRename("An entity's own key can never be renamed."); len(got) != 0 {
		t.Errorf("the guard reported the true half of the rule as a defect: %v", got)
	}
}

// TestTheAnalyticalAxisIsTaughtAsShippedNotAsComing replaces the plan's
// TestNoModellingPageTeachesAnUnshippedColumn, and the replacement is
// itself the correction it guards.
//
// The plan's ship-order section states that analysis_traits is not a
// column, so its guard forbade the identifier on these pages. It is a
// column today (0013_analysis.sql), an accepted input on
// relation_types.upsert, and that tool's own description says declaring
// a semantic_role does not declare behaviour. The old guard would now
// forbid teaching a shipped argument an agent has to send; this one
// requires it to be taught, and forbids the sentence the plan itself
// would have produced — an axis described as still on its way.
func TestTheAnalyticalAxisIsTaughtAsShippedNotAsComing(t *testing.T) {
	pages := modellingPages(t, skill.Files())
	deciding, ok := pages["modelling/deciding.md"]
	if !ok {
		t.Fatal("modelling/deciding.md is missing: the page that argues one relation type " +
			"against two is where the traits decision lives")
	}
	if !strings.Contains(deciding, "analysis_traits") {
		t.Error("modelling/deciding.md never names analysis_traits: the decision between one " +
			"relation type and two is settled by it, and a page arguing that decision " +
			"without it is arguing from taste")
	}
	for name, body := range pages {
		for _, sentence := range unshippedAxis(body) {
			t.Errorf("%s says %q. The analytical axis has shipped: it is a declared argument "+
				"today, not something an agent should wait for", name, sentence)
		}
	}

	// Precision, both ways: the sentence the plan would have written must
	// be caught, and the shipped page's own wording must not be.
	if len(unshippedAxis("A second, analytical axis is coming and will be read off this.")) != 1 {
		t.Error("the guard did not catch an axis described as coming")
	}
	if got := unshippedAxis("A relation type declares analysis_traits, which a walk reads."); len(got) != 0 {
		t.Errorf("the guard reported the shipped wording as a defect: %v", got)
	}
}

// unshippedAxis reports the sentences that present the analytical axis
// as something that has not arrived.
func unshippedAxis(body string) []string {
	var out []string
	for _, sentence := range renameSentences(body) {
		lowered := strings.ToLower(sentence)
		if !strings.Contains(lowered, "analytical axis") && !strings.Contains(lowered, "analysis_traits") {
			continue
		}
		for _, phrase := range []string{"is coming", "will arrive", "not yet", "does not exist", "when it lands"} {
			if strings.Contains(lowered, phrase) {
				out = append(out, sentence)
				break
			}
		}
	}
	return out
}
