package skill_test

import (
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
