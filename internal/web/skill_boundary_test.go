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

	"github.com/neverbot/maestro/internal/assert"
)

// B1: no genre vocabulary in the server's own code.
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
func TestNoGenreVocabularyInServerCode(t *testing.T) {
	t.Parallel()
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
	assert.Must(t, err == nil, "scanning the repository: %v", err)
	// The walk's own count, because every assertion below is "nothing
	// was found" and a walk that read no files finds nothing.
	assert.Must(t, files >= 50, "the scan read %d non-test Go files: it is not walking the repository, so "+
		"the assertion below passes by measuring nothing", files)
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
func TestTheGenreScannerReadsAWholeFile(t *testing.T) {
	t.Parallel()
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
	assert.Must(t, err == nil, "scanning the fixture tree: %v", err)
	assert.Must(t, files == 1, "the scan read %d files of the fixture tree, want 1: the _test.go exemption "+
		"is not doing what it says", files)
	found := map[string]string{}
	for _, hit := range hits {
		found[hit.Name] = hit.Word
	}
	assert.Must(t, found["questCount"] == "quest" && found["Zone"] == "zone", "the scan missed a declaration it was pointed at: %v", found)
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
