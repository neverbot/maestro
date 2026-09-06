package web_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// B1: no genre vocabulary in the server's own code.
//
// The whole premise of this product is that Maestro ships no built-in
// game vocabulary — no Quest, no Zone, no Circuit — and that a game
// declares its own. The bundle now ships three worked examples full of
// exactly those words, which is the moment that premise is easiest to
// lose: one helper named questCount, one column named zone_id, and the
// product has a domain of its own with a skill bundle pretending it does
// not.
//
// **This is the guard in this sub-project most likely to be written
// wrong in a way that disables it silently.** `request` contains
// `quest`. `horizon` contains `zone`. A substring scan produces dozens
// of false positives on its first run and the contributor who meets them
// weakens it until it matches nothing, which looks exactly like a clean
// repository. So it matches whole words, splitting Go identifiers on
// case transitions first, and it asserts its own precision below —
// before it scans anything real.
var genreDenylist = []string{
	"quest", "zone", "dungeon", "talent", "circuit",
	"championship", "licence", "metroidvania", "mmorpg",
}

// identifierWord splits an identifier the way Go spells them: on case
// transitions and on the non-letters between them, so `QuestCount` and
// `quest_count` both yield `quest`, and `makeRequest` yields `make` and
// `request`.
var identifierWord = regexp.MustCompile(`[A-Z]?[a-z0-9]+|[A-Z]+`)
var identifierSeparator = regexp.MustCompile(`[^A-Za-z0-9]+`)

// genreWordIn returns the denied genre word an identifier carries, or ""
// for one that carries none.
func genreWordIn(identifier string) string {
	for _, chunk := range identifierSeparator.Split(identifier, -1) {
		for _, word := range identifierWord.FindAllString(chunk, -1) {
			lowered := strings.ToLower(word)
			for _, denied := range genreDenylist {
				if lowered == denied {
					return denied
				}
			}
		}
	}
	return ""
}

// genreHit is one identifier in the server's code that names a genre.
type genreHit struct {
	Path string
	Line int
	Name string
	Word string
}

// scanGoTreeForGenreWords walks every non-test .go file under root and
// reports every identifier carrying a denied word.
//
// **Identifiers, and neither comments nor string literals.** That is a
// narrower rule than "no genre word anywhere in the server", and it is
// narrower on purpose, because the wider rule is false today and was
// false before this bundle existed: `types.upsert`'s registered
// description says "a kind of thing this game contains (Quest, Zone,
// Class; Driver, Car, Circuit)", and that sentence is the *opposite* of
// a built-in vocabulary — it is two genres side by side, shipped to show
// an agent that neither is privileged. A scan including string literals
// would report it, a contributor would add an exception, and the next
// contributor would add another until the list of exceptions was the
// scan. What makes a genre word part of the server is a **declaration**:
// a type, a field, a function or a variable named after it. That is what
// this walks, and it walks every identifier rather than only declaring
// positions, so a reference to a genre-named symbol imported from
// anywhere is a hit too.
//
// **_test.go files are outside it**, for the reason the plan gives: a
// test naming a genre in a fixture is not server behaviour, and the
// metamodel's tests are necessarily written against some sample game.
// There are around two hundred such identifiers today and every one of
// them is a fixture.
//
// It is crude and it will produce a false positive one day on an
// unrelated word. That is accepted: the alternative is B1 as a promise,
// and this sub-project exists because promises are what fail. The remedy
// is a documented exception with a sentence, not a weaker scanner.
func scanGoTreeForGenreWords(root string) ([]genreHit, int, error) {
	var hits []genreHit
	files := 0
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if name := entry.Name(); name == ".git" || name == "node_modules" || name == "bin" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		files++
		parsed, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			identifier, ok := node.(*ast.Ident)
			if !ok {
				return true
			}
			if word := genreWordIn(identifier.Name); word != "" {
				hits = append(hits, genreHit{
					Path: p, Line: fset.Position(identifier.Pos()).Line,
					Name: identifier.Name, Word: word,
				})
			}
			return true
		})
		return nil
	})
	return hits, files, err
}

// repoRoot is this package's directory two levels up: internal/web ->
// internal -> the repository. It is derived rather than searched for so
// a failure here says "the tree moved" instead of scanning whatever
// directory the test happened to start in.
const repoRoot = "../.."

// TestNoGenreVocabularyInServerCode is B1, mechanised.
//
// The precision fixtures run first and are the point: a scanner that
// matched nothing would satisfy this test's real assertion — no genre
// vocabulary found — for exactly the wrong reason, and a scanner that
// matched substrings would be switched off by the first contributor who
// hit `request`. Both failure modes are named here, with a case each.
func TestNoGenreVocabularyInServerCode(t *testing.T) {
	mustHit := map[string]string{
		"questCount":       "quest",
		"Zone":             "zone",
		"dungeonHandler":   "dungeon",
		"talent_id":        "talent",
		"CircuitList":      "circuit",
		"ChampionshipRow":  "championship",
		"licenceGrade":     "licence",
		"seedMMORPG":       "mmorpg",
		"metroidvaniaSeed": "metroidvania",
	}
	for identifier, want := range mustHit {
		if got := genreWordIn(identifier); got != want {
			t.Errorf("genreWordIn(%q) = %q, want %q: the scanner would not see a genre "+
				"concept declared in the server", identifier, got, want)
		}
	}
	mustMiss := []string{
		"makeRequest", "req", "requestedGame", "requests", "horizonScale",
		"Horizon", "zoned", "quests", "unquestionable", "recirculate",
	}
	for _, identifier := range mustMiss {
		if got := genreWordIn(identifier); got != "" {
			t.Errorf("genreWordIn(%q) = %q: a substring match is how this scanner gets "+
				"deleted by the first contributor who hits it", identifier, got)
		}
	}

	hits, files, err := scanGoTreeForGenreWords(repoRoot)
	if err != nil {
		t.Fatalf("scanning the repository: %v", err)
	}
	// The walk's own count, because every assertion below is "nothing
	// was found" and a walk that read no files finds nothing.
	if files < 50 {
		t.Fatalf("the scan read %d non-test Go files: it is not walking the repository, so "+
			"the assertion below passes by measuring nothing", files)
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Path < hits[j].Path })
	for _, hit := range hits {
		t.Errorf("%s:%d declares or uses %q, which carries the genre word %q: Maestro ships "+
			"no built-in game vocabulary, and a genre concept in the server's own "+
			"identifiers is that promise broken. Rename it, or add a documented exception "+
			"with a sentence saying why — not a weaker scanner",
			hit.Path, hit.Line, hit.Name, hit.Word)
	}
}

// TestTheGenreScannerReadsAWholeFile drives the scan end to end over a
// file written for it, because the word matcher being right and the walk
// being right are two different claims.
//
// Without it, `scanGoTreeForGenreWords` could visit no identifiers at
// all — a wrong node type in the Inspect, a walk that skipped every
// file — and TestNoGenreVocabularyInServerCode would still pass, having
// found nothing in a repository it never read.
func TestTheGenreScannerReadsAWholeFile(t *testing.T) {
	dir := t.TempDir()
	source := "package fixture\n\n" +
		"// the dungeon handler, in a comment: not a hit, and see the scanner's own note\n" +
		"const questCount = 3\n\n" +
		"type Zone struct{ Name string }\n\n" +
		"func makeRequest() string { return \"a quest in a string literal is not a hit\" }\n"
	if err := writeFixture(dir, "fixture.go", source); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	// A _test.go file in the same directory, carrying the same words, to
	// prove the exemption is the file name and not luck.
	if err := writeFixture(dir, "fixture_test.go", "package fixture\n\nvar dungeonSeed = 1\n"); err != nil {
		t.Fatalf("writing the test fixture: %v", err)
	}

	hits, files, err := scanGoTreeForGenreWords(dir)
	if err != nil {
		t.Fatalf("scanning the fixture tree: %v", err)
	}
	if files != 1 {
		t.Fatalf("the scan read %d files of the fixture tree, want 1: the _test.go exemption "+
			"is not doing what it says", files)
	}
	found := map[string]string{}
	for _, hit := range hits {
		found[hit.Name] = hit.Word
	}
	if found["questCount"] != "quest" || found["Zone"] != "zone" {
		t.Fatalf("the scan missed a declaration it was pointed at: %v", found)
	}
	for name := range found {
		if name == "questCount" || name == "Zone" {
			continue
		}
		t.Errorf("the scan reported %q: a comment, a string literal or a _test.go file was "+
			"read as server vocabulary", name)
	}
}

func writeFixture(dir, name, body string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
}
