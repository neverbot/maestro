package web_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/skill"
)

// The fourth closed vocabulary: the error codes a game-content tool can
// answer with.
//
// Its guard lives here rather than in internal/skill because its source
// of truth is a delimited region of internal/web/auth.go and
// internal/web imports internal/skill, so the dependency has exactly one
// direction. internal/skill/vocab_test.go names this test as the checker
// for vocab:error_codes and fails if the fence disappears, so neither
// half can go quiet on its own.

const (
	errorCodeRegionBegin = "// vocab:error_codes begin"
	errorCodeRegionEnd   = "// vocab:error_codes end"
)

// TestTheErrorCodeRegionIsFindable is the guard on the guard. Every
// comparison below reads the region by its markers, and a renamed or
// duplicated marker would make those comparisons run over an empty set
// and pass — the failure this repository has now hit under three
// different names.
func TestTheErrorCodeRegionIsFindable(t *testing.T) {
	body := readSource(t, "auth.go")
	if got := strings.Count(body, errorCodeRegionBegin); got != 1 {
		t.Fatalf("auth.go carries %d %q markers, want exactly 1", got, errorCodeRegionBegin)
	}
	if got := strings.Count(body, errorCodeRegionEnd); got != 1 {
		t.Fatalf("auth.go carries %d %q markers, want exactly 1", got, errorCodeRegionEnd)
	}
	if strings.Index(body, errorCodeRegionBegin) > strings.Index(body, errorCodeRegionEnd) {
		t.Fatal("the vocab:error_codes end marker comes before its begin marker")
	}
	codes := errorCodesInRegion(t)
	if len(codes) < 12 {
		t.Fatalf("the region holds %d codes (%v): too few to be the content surface's "+
			"vocabulary, so the comparisons below would be checking almost nothing",
			len(codes), codes)
	}
}

// TestBundleErrorCodesMatchTheSurface set-compares the bundle's
// vocab:error_codes fence against the delimited region, in both
// directions: a code the server can return and no page names (an agent
// meets an error the bundle never taught it to recover from), and a code
// the bundle names that no tool can return (a recovery for something
// that never arrives).
func TestBundleErrorCodesMatchTheSurface(t *testing.T) {
	fences, err := skill.VocabFences(skill.Files())
	if err != nil {
		t.Fatalf("reading the bundle's vocab fences: %v", err)
	}
	bundle, err := skill.VocabWords(fences, "error_codes")
	if err != nil {
		t.Fatalf("%v", err)
	}
	code := errorCodesInRegion(t)
	if len(bundle) == 0 || len(code) == 0 {
		t.Fatalf("the fence lists %d codes and the region holds %d: a comparison over an "+
			"empty set passes against anything", len(bundle), len(code))
	}

	missing, extra := skill.DiffVocabularies(bundle, code)
	if len(missing) > 0 {
		t.Errorf("the surface can return %v and the bundle's vocab:error_codes fence does "+
			"not list them: an agent would meet an error no page taught it to recover from",
			missing)
	}
	if len(extra) > 0 {
		t.Errorf("the bundle's vocab:error_codes fence lists %v and no code in auth.go's "+
			"delimited region carries that value: the page teaches a recovery from an error "+
			"that never arrives", extra)
	}
}

// TestTheErrorCodeRegionIsExactlyWhatAnMCPToolCanReturn takes the
// judgement out of the region's membership.
//
// "The codes a game-content tool can return" is a sentence somebody has
// to keep true, and nobody does. So it is derived instead: every error
// code this package puts on the MCP wire is parsed out of the source —
// the first argument of mcpErrorResult and of NewMCPError, and the Code
// field of every &MCPError literal — and that set must equal the region,
// in both directions.
//
// A code moved into the region that no tool produces fails here; so does
// a new MCP refusal whose code was declared outside it, which is the
// direction that would otherwise ship an agent an error the bundle never
// documents.
//
// Both spellings are collected because both occur: mcp.go names the
// constant, and mcp_metamodel.go's invalidInput writes the string
// "invalid_input" straight into the literal. Reading only the constants
// would have missed the second and reported invalid_input as a region
// member nothing produces.
func TestTheErrorCodeRegionIsExactlyWhatAnMCPToolCanReturn(t *testing.T) {
	region := errorCodesInRegion(t)
	emitted := mcpEmittedErrorCodes(t)

	if len(emitted) < 10 {
		t.Fatalf("only %d error codes were found reaching the MCP wire across this package "+
			"(%v): the parse is broken, and both comparisons below would be vacuous",
			len(emitted), emitted)
	}
	if len(region) == 0 {
		t.Fatal("no codes were parsed out of the delimited region")
	}

	missing, extra := skill.DiffVocabularies(emitted, region)
	if len(missing) > 0 {
		t.Errorf("%v are inside the vocab:error_codes region and no MCP path produces them: "+
			"the bundle would teach a recovery from an error that never arrives", missing)
	}
	if len(extra) > 0 {
		t.Errorf("%v reach the MCP wire and are declared outside the vocab:error_codes "+
			"region: an agent can receive them and the bundle never enumerates them", extra)
	}
}

// readSource reads one file of this package from disk. The tests here
// read source rather than values because the region is a *textual*
// delimitation: its whole purpose is that a human can see where it
// starts and stops.
func readSource(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Clean(name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(body)
}

// errorCodesInRegion returns the string values of the constants between
// the markers.
func errorCodesInRegion(t *testing.T) []string {
	t.Helper()
	out := make([]string, 0, 16)
	for _, line := range regionLines(t) {
		if _, value, ok := constantAssignment(line); ok {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func regionLines(t *testing.T) []string {
	t.Helper()
	body := readSource(t, "auth.go")
	begin := strings.Index(body, errorCodeRegionBegin)
	end := strings.Index(body, errorCodeRegionEnd)
	if begin < 0 || end < 0 || end < begin {
		t.Fatalf("the vocab:error_codes region is not delimited in auth.go: begin at %d, "+
			"end at %d", begin, end)
	}
	return strings.Split(body[begin:end], "\n")
}

// constantAssignment reads `errCodeSomething = "value"` off one line.
func constantAssignment(line string) (name, value string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "errCode") {
		return "", "", false
	}
	name, rest, found := strings.Cut(trimmed, "=")
	if !found {
		return "", "", false
	}
	unquoted, err := strconv.Unquote(strings.TrimSpace(rest))
	if err != nil {
		return "", "", false
	}
	return strings.TrimSpace(name), unquoted, true
}

// mcpEmittedErrorCodes parses every non-test Go file of this package and
// returns the error code *values* that reach the MCP wire.
//
// It reads the calls and the literals, not a list somebody maintains,
// which is what makes the region's membership derived rather than
// remembered. A code named by a constant is resolved through this
// package's own errCode* declarations; a code written as a bare string
// is taken as it is; a code that is neither — `domainErr.Code`, the
// pass-through in mcpErrorFor — carries no new value and is skipped,
// since whatever built that MCPError is itself in this scan.
func mcpEmittedErrorCodes(t *testing.T) []string {
	t.Helper()
	values := errorCodeConstantValues(t)
	seen := map[string]bool{}
	record := func(expr ast.Expr) {
		switch node := expr.(type) {
		case *ast.Ident:
			if value, ok := values[node.Name]; ok {
				seen[value] = true
			}
		case *ast.BasicLit:
			if node.Kind != token.STRING {
				return
			}
			if unquoted, err := strconv.Unquote(node.Value); err == nil {
				seen[unquoted] = true
			}
		}
	}
	forEachSourceFile(t, func(_ string, file *ast.File) {
		ast.Inspect(file, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.CallExpr:
				fn, ok := value.Fun.(*ast.Ident)
				if !ok || len(value.Args) == 0 {
					return true
				}
				if fn.Name == "mcpErrorResult" || fn.Name == "NewMCPError" {
					record(value.Args[0])
				}
			case *ast.CompositeLit:
				name, ok := value.Type.(*ast.Ident)
				if !ok || name.Name != "MCPError" {
					return true
				}
				for _, element := range value.Elts {
					pair, ok := element.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if key, ok := pair.Key.(*ast.Ident); ok && key.Name == "Code" {
						record(pair.Value)
					}
				}
			}
			return true
		})
	})
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// errorCodeConstantValues maps every errCode* constant in this package to
// its string value, across all of its files — the region is only part of
// them, and a code emitted from outside it is exactly what this file's
// comparison is looking for.
func errorCodeConstantValues(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	forEachSourceFile(t, func(_ string, file *ast.File) {
		for _, decl := range file.Decls {
			general, ok := decl.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, spec := range general.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || len(value.Names) != len(value.Values) {
					continue
				}
				for i, name := range value.Names {
					if !strings.HasPrefix(name.Name, "errCode") {
						continue
					}
					literal, ok := value.Values[i].(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					unquoted, err := strconv.Unquote(literal.Value)
					if err != nil {
						t.Fatalf("unquoting %s: %v", literal.Value, err)
					}
					out[name.Name] = unquoted
				}
			}
		}
	})
	if len(out) == 0 {
		t.Fatal("no errCode constants were parsed out of this package: every lookup below " +
			"would silently miss")
	}
	return out
}

// forEachSourceFile parses this package's non-test files.
func forEachSourceFile(t *testing.T, visit func(name string, file *ast.File)) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("listing the package directory: %v", err)
	}
	fset := token.NewFileSet()
	parsed := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Clean(name), nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		parsed++
		visit(name, file)
	}
	if parsed == 0 {
		t.Fatal("no source files were parsed: this package's own scan is reading nothing")
	}
}
