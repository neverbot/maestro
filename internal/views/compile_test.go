package views

import (
	"regexp"
	"strings"
	"testing"
)

func compileOf(t *testing.T, g *game, doc string) (string, []any) {
	t.Helper()
	r, err := g.views.Resolve(t.Context(), g.projectID, mustParse(t, doc))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	sql, args, err := Compile(r, g.projectID)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return sql, args
}

// TestNoCallerValueEverReachesTheStatementText is the injection question,
// answered by construction rather than by care. Every string the caller
// controls is a distinctive sentinel; none may appear in the SQL.
func TestNoCallerValueEverReachesTheStatementText(t *testing.T) {
	g, _ := newGame(t)
	// The sentinels have to be legal for the document to resolve, so they
	// are the seeded keys themselves — which is the strongest form of the
	// test: even a *valid* key must travel as a bind parameter, because
	// the compiler cannot tell a valid key from a crafted one.
	sql, args := compileOf(t, g, `{"v":1,"from":[{"type":"quest","keys":["hogger"],
		"where":{"field":"rank","op":"eq","value":"rare"}}]}`)
	for _, sentinel := range []string{"hogger", "rare", "quest", "min_level"} {
		if strings.Contains(sql, sentinel) {
			t.Errorf("the caller value %q reached the statement text:\n%s", sentinel, sql)
		}
	}
	if len(args) < 3 {
		t.Fatalf("the values must have gone somewhere: %d bind arguments", len(args))
	}
}

// TestEveryTableReferenceIsProjectFiltered walks the emitted SQL rather
// than one query's behaviour, so a clause added by a later task cannot
// quietly drop the filter.
func TestEveryTableReferenceIsProjectFiltered(t *testing.T) {
	g, _ := newGame(t)
	sql, _ := compileOf(t, g, `{"v":1,"from":[{"type":"quest","as":"q"}],
		"traverse":[{"from":"q","via":"available_to","direction":"out","to_type":"class","as":"c"}],
		"edges":[{"from_step":"c"}]}`)
	refs := regexp.MustCompile(`(?i)\b(FROM|JOIN)\s+(entities|relations)\b`).FindAllString(sql, -1)
	if len(refs) == 0 {
		t.Fatalf("no table references found; the assertion below would be vacuous:\n%s", sql)
	}
	for _, block := range strings.Split(sql, "SELECT") {
		if !strings.Contains(block, "entities") && !strings.Contains(block, "relations") {
			continue
		}
		if !strings.Contains(block, "project_id = $1") {
			t.Errorf("a select over entities or relations does not filter on the project:\n%s", block)
		}
	}
}

func TestATypedPredicateGuardsItsCastByJsonbType(t *testing.T) {
	g, _ := newGame(t)
	sql, _ := compileOf(t, g,
		`{"v":1,"from":[{"type":"quest","where":{"field":"min_level","op":"between","value":[20,30]}}]}`)
	if !strings.Contains(sql, "jsonb_typeof") {
		t.Fatalf("a number predicate must guard its cast with jsonb_typeof:\n%s", sql)
	}
	if !strings.Contains(sql, "::numeric") {
		t.Fatalf("a number predicate must compare numerically:\n%s", sql)
	}
}

func TestInvalidRowsAreExcludedUnlessAskedFor(t *testing.T) {
	g, _ := newGame(t)
	sql, _ := compileOf(t, g, `{"v":1,"from":[{"type":"quest"}]}`)
	if !strings.Contains(sql, "invalid = false") {
		t.Fatalf("invalid rows must be excluded by default:\n%s", sql)
	}
	sql, _ = compileOf(t, g, `{"v":1,"from":[{"type":"quest"}],"include_invalid":true}`)
	if strings.Contains(sql, "invalid = false") {
		t.Fatalf("include_invalid must lift the filter:\n%s", sql)
	}
}
