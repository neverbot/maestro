package views

import (
	"sort"
	"strings"
	"testing"
)

// TestASeedSelectorComesBackAsNodes is the read-back this whole
// sub-project's first feature owes. It asserts a named quest with its
// key, name, type and set — not a row count, which an unrelated bug can
// also satisfy.
func TestASeedSelectorComesBackAsNodes(t *testing.T) {
	g, _ := newGame(t)
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest","as":"quests"}]}`),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	byKey := map[string]Node{}
	for _, n := range res.Nodes {
		byKey[n.Key] = n
	}
	hogger, ok := byKey["hogger"]
	if !ok {
		t.Fatalf("the seeded quest must come back; got %v", keysOf(res.Nodes))
	}
	if hogger.Name != "Wanted: Hogger" || hogger.Type != "quest" || hogger.Set != "quests" {
		t.Fatalf("the node did not carry its identity: %+v", hogger)
	}
	if len(res.Nodes) != 3 {
		t.Fatalf("three quests are seeded, got %d: %v", len(res.Nodes), keysOf(res.Nodes))
	}
	if res.Stats.Nodes != 3 {
		t.Fatalf("stats must count what came back, got %+v", res.Stats)
	}
}

func TestAPredicateNarrowsTheResultAndTheControlProvesIt(t *testing.T) {
	g, _ := newGame(t)
	all, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest"}]}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(all.Nodes) != 3 {
		t.Fatalf("positive control: three quests, got %d", len(all.Nodes))
	}
	band, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest","where":{"all":[
			{"field":"min_level","op":"gte","value":20},
			{"field":"min_level","op":"lte","value":30}]}}]}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := keysOf(band.Nodes); len(got) != 2 {
		t.Fatalf("the 20-30 band holds hogger and defias, got %v", got)
	}
}

func TestAOneHopTraversalReturnsItsEdges(t *testing.T) {
	g, _ := newGame(t)
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"class","as":"cls",
			"where":{"field":"@key","op":"eq","value":"mage"}}],
			"traverse":[{"from":"cls","via":"available_to","direction":"in",
			             "to_type":"quest","as":"reachable"}],
			"edges":[{"from_step":"reachable"}]}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Edges) != 3 {
		t.Fatalf("three quests are available to the mage, got %d edges", len(res.Edges))
	}
	for _, e := range res.Edges {
		if e.Type != "available_to" || e.Source == e.Target {
			t.Fatalf("an edge must carry its type and two endpoints: %+v", e)
		}
	}
}

// TestARunFromAnotherGameSeesNothing is the isolation test at the
// execution boundary, with its positive control in the same test.
func TestARunFromAnotherGameSeesNothing(t *testing.T) {
	g, other := newGame(t)
	q := `{"v":1,"from":[{"type":"quest"}]}`
	mine, err := g.views.Run(t.Context(), g.projectID, RunRequest{Query: mustParse(t, q)})
	if err != nil || len(mine.Nodes) != 3 {
		t.Fatalf("positive control: %v, %d nodes", err, len(mine.Nodes))
	}
	theirs, err := other.views.Run(t.Context(), other.projectID, RunRequest{Query: mustParse(t, q)})
	if err != nil {
		t.Fatalf("run in the other game: %v", err)
	}
	for _, n := range theirs.Nodes {
		for _, m := range mine.Nodes {
			if n.ID == m.ID {
				t.Fatalf("a node of one game came back in the other: %s", n.Key)
			}
		}
	}
}

func keysOf(nodes []Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Key)
	}
	return out
}

// TestARunBindsItsParametersOverTheDeclaredDefaults is the read-back a
// parameter owes: the default draws one answer, the binding draws
// another, and both come back through Run rather than through Resolve.
func TestARunBindsItsParametersOverTheDeclaredDefaults(t *testing.T) {
	g, _ := newGame(t)
	g.entity(t, "class", "rogue", "Rogue", nil)
	doc := `{"v":1,"params":[{"key":"class_key","type":"text","default":"mage"}],
		"from":[{"type":"class","as":"cls",
		  "where":{"field":"@key","op":"eq","value":{"param":"class_key"}}}]}`

	byDefault, err := g.views.Run(t.Context(), g.projectID, RunRequest{Query: mustParse(t, doc)})
	if err != nil {
		t.Fatalf("run with the default: %v", err)
	}
	if got := keysOf(byDefault.Nodes); len(got) != 1 || got[0] != "mage" {
		t.Fatalf("the declared default selects the mage, got %v", got)
	}

	bound, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, doc), Params: map[string]any{"class_key": "rogue"}})
	if err != nil {
		t.Fatalf("run with a binding: %v", err)
	}
	if got := keysOf(bound.Nodes); len(got) != 1 || got[0] != "rogue" {
		t.Fatalf("the run's own value must win over the default, got %v", got)
	}
}

func TestAParameterTheQueryDoesNotDeclareIsRefused(t *testing.T) {
	g, _ := newGame(t)
	doc := `{"v":1,"params":[{"key":"class_key","type":"text","default":"mage"}],
		"from":[{"type":"class","as":"cls",
		  "where":{"field":"@key","op":"eq","value":{"param":"class_key"}}}]}`
	_, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, doc), Params: map[string]any{"clsas_key": "rogue"}})
	oneProblem(t, err, "/params", `no parameter named "clsas_key"`)

	_, err = g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, doc), Params: map[string]any{"class_key": 3}})
	oneProblem(t, err, "/params/0", "expected text, got int")
}

// TestAParameterWithNoValueIsRefusedRatherThanCompiledAsNothing: a
// parameter declared without a default and left unbound has no value, and
// compiling it as NULL would draw an empty picture and say nothing.
func TestAParameterWithNoValueIsRefusedRatherThanCompiledAsNothing(t *testing.T) {
	g, _ := newGame(t)
	doc := `{"v":1,"params":[{"key":"class_key","type":"text"}],
		"from":[{"type":"class","as":"cls",
		  "where":{"field":"@key","op":"eq","value":{"param":"class_key"}}}]}`
	_, err := g.views.Run(t.Context(), g.projectID, RunRequest{Query: mustParse(t, doc)})
	oneProblem(t, err, "/from/0/where/value", "has no value for this run")

	// The control: supplying one runs.
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, doc), Params: map[string]any{"class_key": "mage"}})
	if err != nil || len(res.Nodes) != 1 {
		t.Fatalf("the control must run: %v, %d nodes", err, len(res.Nodes))
	}
}

// TestANodeInTwoSetsComesBackOnceUnderTheFirstSetThatClaimedIt pins the
// deduplication and the order that makes it deterministic: without the
// rank column and the ORDER BY over it, which of two sets a shared node
// reports would be whatever Postgres returned first.
func TestANodeInTwoSetsComesBackOnceUnderTheFirstSetThatClaimedIt(t *testing.T) {
	g, _ := newGame(t)
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest","as":"first"},
			{"type":"quest","as":"second"}]}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Nodes) != 3 {
		t.Fatalf("three quests in two sets are three nodes, got %d: %v",
			len(res.Nodes), keysOf(res.Nodes))
	}
	for _, n := range res.Nodes {
		if n.Set != "first" {
			t.Fatalf("the first set declared must claim the node, got %q for %s", n.Set, n.Key)
		}
	}
}

// TestIncludeFieldsIsWhatPutsFieldsInTheEnvelope reads back the switch
// that decides whether a run carries its jsonb payload, on both sides.
func TestIncludeFieldsIsWhatPutsFieldsInTheEnvelope(t *testing.T) {
	g, _ := newGame(t)
	doc := `{"v":1,"from":[{"type":"quest","keys":["hogger"]}]}`
	without, err := g.views.Run(t.Context(), g.projectID, RunRequest{Query: mustParse(t, doc)})
	if err != nil || len(without.Nodes) != 1 {
		t.Fatalf("run: %v, %d nodes", err, len(without.Nodes))
	}
	if without.Nodes[0].Fields != nil {
		t.Fatalf("a run that did not ask for fields must not carry them: %v",
			without.Nodes[0].Fields)
	}
	with, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, doc), IncludeFields: true})
	if err != nil || len(with.Nodes) != 1 {
		t.Fatalf("run: %v, %d nodes", err, len(with.Nodes))
	}
	if got := with.Nodes[0].Fields["min_level"]; got != float64(22) {
		t.Fatalf("include_fields must carry the declared values, got %#v", with.Nodes[0].Fields)
	}
}

// TestALikePatternsMetacharactersAreEscaped is why likeOperand exists: a
// designer's quest name may hold a per-cent sign, and `starts_with:
// "50%"` must mean a name starting "50%" rather than a name starting
// "50". The control is in the same assertion, so an empty answer cannot
// pass it.
func TestALikePatternsMetacharactersAreEscaped(t *testing.T) {
	g, _ := newGame(t)
	g.entity(t, "quest", "half", "50% Off", nil)
	g.entity(t, "quest", "fifty", "50 Silver", nil)

	escaped, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest",
			"where":{"field":"@name","op":"starts_with","value":"50%"}}]}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := keysOf(escaped.Nodes); len(got) != 1 || got[0] != "half" {
		t.Fatalf(`"50%%" must match only the name that holds one, got %v`, got)
	}
	control, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest",
			"where":{"field":"@name","op":"starts_with","value":"50"}}]}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := keysOf(control.Nodes); len(got) != 2 {
		t.Fatalf("the unescaped prefix matches both, got %v", got)
	}
}

// TestAKeyComparisonFoldsCaseOnBothSides: a row key is matched
// case-insensitively by the write path (entities_key_key is UNIQUE over
// lower(key)), so folding one side only would refuse a spelling the
// metamodel accepted.
func TestAKeyComparisonFoldsCaseOnBothSides(t *testing.T) {
	g, _ := newGame(t)
	g.entity(t, "quest", "Deadmines", "The Deadmines", nil)
	for _, spelling := range []string{"deadmines", "DEADMINES", "DeadMines"} {
		res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, `{"v":1,"from":[{"type":"quest",
				"where":{"field":"@key","op":"eq","value":"`+spelling+`"}}]}`)})
		if err != nil {
			t.Fatalf("run for %q: %v", spelling, err)
		}
		if got := keysOf(res.Nodes); len(got) != 1 || got[0] != "Deadmines" {
			t.Fatalf("%q must find the stored key, got %v", spelling, got)
		}
	}
	// The keys shortcut folds too, and it is a different clause.
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest","keys":["DEADMINES"]}]}`)})
	if err != nil || len(res.Nodes) != 1 {
		t.Fatalf("the keys shortcut folds as well: %v, %v", err, keysOf(res.Nodes))
	}
}

// TestAListFieldAnswersItsOwnOperators covers the arms nothing else
// reaches: containment, the any/all set operators, empty and the length
// family, each with the row that must not match in the same fixture.
func TestAListFieldAnswersItsOwnOperators(t *testing.T) {
	g, _ := newGame(t)
	for _, c := range []struct {
		predicate string
		want      []string
	}{
		{`{"field":"tags","op":"contains","value":"kill"}`, []string{"hogger"}},
		{`{"field":"tags","op":"contains_any","value":["chain","elite"]}`,
			[]string{"defias", "hogger"}},
		{`{"field":"tags","op":"contains_all","value":["kill","elite"]}`, []string{"hogger"}},
		{`{"field":"tags","op":"empty","value":true}`, []string{"cook"}},
		{`{"field":"tags","op":"length_gte","value":2}`, []string{"hogger"}},
		{`{"field":"tags","op":"length_eq","value":1}`, []string{"defias"}},
	} {
		res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, `{"v":1,"from":[{"type":"quest","where":`+c.predicate+`}]}`)})
		if err != nil {
			t.Fatalf("run %s: %v", c.predicate, err)
		}
		got := keysOf(res.Nodes)
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: want %v, got %v", c.predicate, c.want, got)
		}
	}
}

// TestAValueOfTheWrongJsonbTypeIsSkippedRatherThanRaised is the failure
// the jsonb_typeof guard exists for, seen. internal/metamodel flags an
// entity invalid on a schema change and leaves its values in place, so a
// field declared number can hold a string; without the guard the cast
// raises SQLSTATE 22P02 and the whole view fails on one stale row.
func TestAValueOfTheWrongJsonbTypeIsSkippedRatherThanRaised(t *testing.T) {
	g, _ := newGame(t)
	// Written past the schema the way MarkEntitiesOfTypeInvalid leaves a
	// row behind: the value stays, the row is flagged.
	if _, err := g.pool.Exec(t.Context(),
		`UPDATE entities SET fields = jsonb_set(fields, '{min_level}', '"veinte"'), invalid = true
		 WHERE project_id = $1 AND key = 'cook'`, g.projectID); err != nil {
		t.Fatalf("age the row: %v", err)
	}
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"include_invalid":true,"from":[{"type":"quest",
			"where":{"field":"min_level","op":"gte","value":10}}]}`)})
	if err != nil {
		t.Fatalf("a stale value must not fail the view: %v", err)
	}
	got := keysOf(res.Nodes)
	sort.Strings(got)
	if strings.Join(got, ",") != "defias,hogger" {
		t.Fatalf("the two numeric rows answer and the stale one does not, got %v", got)
	}
}
