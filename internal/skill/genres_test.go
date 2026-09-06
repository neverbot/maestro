package skill_test

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/neverbot/maestro/internal/skill"
)

// The genre pages are the part of this bundle most likely to become the
// thing the whole product says it is not: a built-in vocabulary. A
// shipped "MMORPG template" is built-in vocabulary by another name
// unless four rules hold, and three of them are testable.
//
//  1. The page's product is the reasoning, not the type list. Untestable,
//     and it is what review is for.
//  2. The page and its transcript declare the same types, so no page
//     describes a type nothing builds — TestTheGenrePageAndItsTranscriptAgree.
//  3. No two genres declare the same vocabulary, because three genres
//     that agree on everything have collapsed into one domain —
//     TestNoTwoGenresDeclareTheSameVocabulary.
//  4. No genre word reaches the server's own code —
//     TestNoGenreVocabularyInServerCode, which lives in internal/web
//     because it walks the whole repository.
//
// Rule 2's fence is also what carries the vocabulary rule one step
// along: knownVocabularies in vocab_test.go names every
// `vocab:genre_types_*` fence and this file is the guard it names, so a
// fourth genre page whose fence nothing compares fails there.

// deliberateDifference is the heading every genre page carries exactly
// once.
const deliberateDifference = "## Where this differs deliberately"

// genrePages walks the bundle and pairs `genres/<name>.md` with
// `genres/<name>.json`.
//
// Both directions are errors: a page with no transcript is prose no test
// ever executes, and a transcript with no page is a worked example with
// its reasoning missing — which is the half this bundle says is the
// valuable one. Pairs are discovered by walking rather than listed, so a
// fourth genre is covered without editing any test here.
func genrePages(t *testing.T) map[string]genrePair {
	t.Helper()
	pairs := map[string]genrePair{}
	err := fs.WalkDir(skill.Files(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || path.Dir(name) != "genres" {
			return nil
		}
		genre := strings.TrimSuffix(path.Base(name), path.Ext(name))
		pair := pairs[genre]
		pair.Genre = genre
		switch path.Ext(name) {
		case ".md":
			pair.Page = name
		case ".json":
			pair.Transcript = name
		default:
			t.Errorf("%s is in genres/ and is neither a page nor a transcript", name)
		}
		pairs[genre] = pair
		return nil
	})
	if err != nil {
		t.Fatalf("walking the bundle's genres: %v", err)
	}
	if len(pairs) == 0 {
		t.Fatal("the bundle ships no genres at all: every assertion in this file would run " +
			"over an empty set and pass")
	}
	for genre, pair := range pairs {
		if pair.Page == "" {
			t.Errorf("genres/%s.json has no page: a transcript with no reasoning beside it "+
				"is the half of a worked example nobody needed", genre)
		}
		if pair.Transcript == "" {
			t.Errorf("genres/%s.md has no transcript: a page whose example is never executed "+
				"is a page that goes false quietly", genre)
		}
	}
	return pairs
}

type genrePair struct {
	Genre      string
	Page       string
	Transcript string
}

// transcriptTypeKeys reads the entity type keys a transcript declares,
// off its `types.upsert` calls.
//
// It reads the calls rather than a list beside them for the reason every
// guard in this package walks the tree: a second list is what goes
// stale, and a transcript that adds a type and forgets the list would be
// compared against a list that never mentioned it.
func transcriptTypeKeys(fsys fs.FS, name string) ([]string, error) {
	body, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, err
	}
	var script struct {
		Calls []struct {
			Tool string `json:"tool"`
			Args struct {
				Key string `json:"key"`
			} `json:"args"`
		} `json:"calls"`
	}
	if err := json.Unmarshal(body, &script); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	var out []string
	seen := map[string]bool{}
	for _, call := range script.Calls {
		if call.Tool != "types.upsert" || call.Args.Key == "" || seen[call.Args.Key] {
			continue
		}
		seen[call.Args.Key] = true
		out = append(out, call.Args.Key)
	}
	sort.Strings(out)
	return out, nil
}

// TestEveryGenrePageDeclaresADeliberateDifference holds every genre page
// to naming another genre page and a decision taken differently there.
//
// The section is the one thing that stops three pages reading as three
// copies of one template. A page that differs from `genres/rts.md` when
// no such page ships is a dead reference, so the named page has to exist
// in the bundle — and it has to be a *different* page, because a page
// that cites itself has cited nothing.
func TestEveryGenrePageDeclaresADeliberateDifference(t *testing.T) {
	pairs := genrePages(t)
	for _, pair := range pairs {
		if pair.Page == "" {
			continue
		}
		t.Run(pair.Page, func(t *testing.T) {
			body, err := fs.ReadFile(skill.Files(), pair.Page)
			if err != nil {
				t.Fatalf("reading %s: %v", pair.Page, err)
			}
			section, count := sectionAfter(string(body), deliberateDifference)
			if count != 1 {
				t.Fatalf("%s carries the %q heading %d times: exactly one, or the section a "+
					"reader is sent to is ambiguous", pair.Page, deliberateDifference, count)
			}
			named := genresNamedIn(section)
			var others []string
			for _, other := range named {
				if other == pair.Genre {
					continue
				}
				if _, ok := pairs[other]; !ok {
					t.Errorf("%s says it differs from genres/%s.md, which is not in the "+
						"bundle: a dead reference sends a reader to read nothing",
						pair.Page, other)
					continue
				}
				others = append(others, other)
			}
			if len(others) == 0 {
				t.Errorf("%s's %q section names no other genre page that ships: the section "+
					"exists to say what a second, shipped example decided differently",
					pair.Page, deliberateDifference)
			}
		})
	}
}

// TestTheGenrePageAndItsTranscriptAgree is set equality, both
// directions, between a page's `vocab:genre_types_<genre>` fence and the
// entity types its transcript declares.
//
// It is the guard that stops a page describing a `mount` type the
// transcript never builds, and it is the guard that stops a transcript
// growing a type the page never explains. The pairs come from the walk,
// so a fourth genre is checked without editing this test.
func TestTheGenrePageAndItsTranscriptAgree(t *testing.T) {
	fences, err := skill.VocabFences(skill.Files())
	if err != nil {
		t.Fatalf("reading the bundle's vocab fences: %v", err)
	}
	pairs := genrePages(t)
	checked := 0
	for _, pair := range pairs {
		if pair.Page == "" || pair.Transcript == "" {
			continue
		}
		t.Run(pair.Genre, func(t *testing.T) {
			name := genreVocabName(pair.Genre)
			page, err := skill.VocabWords(fences, name)
			if err != nil {
				t.Fatalf("%s: %v", pair.Page, err)
			}
			transcript, err := transcriptTypeKeys(skill.Files(), pair.Transcript)
			if err != nil {
				t.Fatalf("%v", err)
			}
			// Neither side may be empty: two empty sets compare equal,
			// and this comparison is the only thing standing between a
			// page and a vocabulary nothing builds.
			if len(page) == 0 || len(transcript) == 0 {
				t.Fatalf("%s lists %d types and %s declares %d: the comparison would run "+
					"over an empty set", pair.Page, len(page), pair.Transcript, len(transcript))
			}
			missing, extra := skill.DiffVocabularies(page, transcript)
			for _, key := range missing {
				t.Errorf("%s declares the type %q with types.upsert and %s's vocab:%s fence "+
					"does not list it", pair.Transcript, key, pair.Page, name)
			}
			for _, key := range extra {
				t.Errorf("%s's vocab:%s fence lists the type %q and %s never declares it: a "+
					"page describing a type nothing builds", pair.Page, name, key, pair.Transcript)
			}
			checked++
		})
	}
	if checked == 0 {
		t.Fatal("no genre page was compared against a transcript at all")
	}
}

// TestNoTwoGenresDeclareTheSameVocabulary is the convergence signal.
//
// Equality, not overlap. `requires` appearing in all three genres is
// expected and is fine — it is one idea three games happen to share. Two
// genres whose *whole* type vocabularies are equal have collapsed into
// one built-in domain, and the bundle would then be teaching that
// Maestro has a shape of its own, which is the one thing this product
// says it does not.
func TestNoTwoGenresDeclareTheSameVocabulary(t *testing.T) {
	pairs := genrePages(t)
	vocab := map[string]string{}
	compared := 0
	for _, pair := range pairs {
		if pair.Transcript == "" {
			continue
		}
		keys, err := transcriptTypeKeys(skill.Files(), pair.Transcript)
		if err != nil {
			t.Fatalf("%v", err)
		}
		if len(keys) == 0 {
			t.Fatalf("%s declares no entity types", pair.Transcript)
		}
		signature := strings.Join(keys, " ")
		if other, clash := vocab[signature]; clash {
			t.Errorf("%s and %s declare the same entity type vocabulary (%s): two genres that "+
				"agree on everything are one genre, and a bundle shipping it twice is "+
				"teaching a built-in domain", other, pair.Transcript, signature)
		}
		vocab[signature] = pair.Transcript
		compared++
	}
	if compared < 2 {
		t.Fatalf("only %d genre vocabularies were read: a uniqueness check over fewer than "+
			"two sets compares nothing", compared)
	}
}

// TestTheGenreGuardsArePrecise drives every reader in this file with
// fixtures the real bundle does not contain.
//
// Every assertion above is of the form "the two sides agree", and a
// reader that returned nothing would make all of them agree. So each
// reader is given a case it must find something in and a case it must
// find nothing in, here, rather than being trusted.
func TestTheGenreGuardsArePrecise(t *testing.T) {
	t.Run("the section reader finds one section and counts repeats", func(t *testing.T) {
		body := "# A genre\n\nIntro.\n\n" + deliberateDifference +
			"\n\nAgainst `genres/racing.md`, on gates.\n\n## Where to look next\n\nA link.\n"
		section, count := sectionAfter(body, deliberateDifference)
		if count != 1 {
			t.Fatalf("count = %d, want 1", count)
		}
		if !strings.Contains(section, "racing") {
			t.Fatalf("the section did not carry its own body: %q", section)
		}
		if strings.Contains(section, "Where to look next") || strings.Contains(section, "Intro") {
			t.Fatalf("the section ran past its own heading: %q", section)
		}
		if _, twice := sectionAfter(body+deliberateDifference+"\n\nAgain.\n", deliberateDifference); twice != 2 {
			t.Fatalf("a page with the heading twice counted %d", twice)
		}
	})

	t.Run("the genre reader reads a citation and not a tool or a self-citation", func(t *testing.T) {
		named := genresNamedIn("Against `genres/racing.md` and `genres/mmorpg.md`, and see " +
			"`reference/tools.md` and `types.upsert`.")
		if strings.Join(named, ",") != "mmorpg,racing" {
			t.Fatalf("named = %v, want the two genre pages and nothing else", named)
		}
		if len(genresNamedIn("No citation here at all.")) != 0 {
			t.Fatal("a section naming nothing produced a citation")
		}
	})

	t.Run("the transcript reader reads types.upsert keys and nothing else", func(t *testing.T) {
		tree := fstest.MapFS{"genres/x.json": &fstest.MapFile{Data: []byte(`{
			"genre": "x",
			"calls": [
				{"tool": "types.upsert", "args": {"key": "room"}},
				{"tool": "types.upsert", "args": {"key": "room"}},
				{"tool": "relation_types.upsert", "args": {"key": "connects_to"}},
				{"tool": "entities.upsert", "args": {"items": [{"type_key": "room", "key": "a"}]}},
				{"tool": "types.upsert", "args": {"key": "ability"}}
			]}`)}}
		keys, err := transcriptTypeKeys(tree, "genres/x.json")
		if err != nil {
			t.Fatalf("%v", err)
		}
		if strings.Join(keys, " ") != "ability room" {
			t.Fatalf("keys = %v: a relation type key or a duplicate was read as an entity "+
				"type, or a repeated declaration was counted twice", keys)
		}
	})

	t.Run("the fence name is the one vocab_test.go says it checks", func(t *testing.T) {
		if got := genreVocabName("racing"); got != "genre_types_racing" {
			t.Fatalf("genreVocabName(racing) = %q", got)
		}
	})
}

// sectionAfter returns the body between a heading and the next heading
// of any level, and how many times that heading appears.
func sectionAfter(body, heading string) (string, int) {
	lines := strings.Split(body, "\n")
	var section []string
	count, inside := 0, false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == heading {
			count++
			inside = true
			continue
		}
		if inside && strings.HasPrefix(trimmed, "#") {
			inside = false
			continue
		}
		if inside {
			section = append(section, line)
		}
	}
	return strings.Join(section, "\n"), count
}

// genrePagePattern matches a backticked `genres/<name>.md` citation.
// Anchored on the directory and the extension so a tool name in the same
// paragraph is not read as a page and a prose mention of a genre is not
// read as a citation: this guard's whole job is to insist the page named
// is one that ships.
var genrePagePattern = regexp.MustCompile("`genres/([a-z0-9_-]+)\\.md`")

func genresNamedIn(section string) []string {
	var out []string
	seen := map[string]bool{}
	for _, match := range genrePagePattern.FindAllStringSubmatch(section, -1) {
		if seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		out = append(out, match[1])
	}
	sort.Strings(out)
	return out
}

func genreVocabName(genre string) string { return "genre_types_" + genre }
