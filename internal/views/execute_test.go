package views

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
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
// deduplication, and pins the ordering **as text**.
//
// The split matters, and it was found by mutation: deleting the ORDER BY
// leaves the behavioural half green, because a UNION of two arms happens
// to come back in arm order on this Postgres. So the behavioural half is
// only the positive control — it proves the node is deduplicated and that
// the set it reports is the first one declared — and the text assertion
// is what makes the order a contract rather than an executor detail. It
// is the half that is red without the ORDER BY.
func TestANodeInTwoSetsComesBackOnceUnderTheFirstSetThatClaimedIt(t *testing.T) {
	g, _ := newGame(t)
	sql, _ := compileOf(t, g, `{"v":1,"from":[{"type":"quest","as":"first"},
		{"type":"quest","as":"second"}]}`)
	if !strings.Contains(sql, "ORDER BY 1, 11, 2") {
		t.Fatalf("the rows must be ordered by kind, then by the declaration rank of the "+
			"entry that produced them, then by id:\n%s", sql)
	}
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

// TestALikePatternsMetacharactersAreEscaped is why escapeLike exists: a
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

// TestAGlobPatternDoesNotLeakThePerCentItWasGiven is the behavioural half
// of the same rule, and the test the operator had none of: `matches
// "50%*"` asks for names that start "50%", not for names that start "50".
// The control row is in the same fixture, so an empty answer cannot pass.
func TestAGlobPatternDoesNotLeakThePerCentItWasGiven(t *testing.T) {
	g, _ := newGame(t)
	g.entity(t, "quest", "half", "50% Off", nil)
	g.entity(t, "quest", "fifty", "50 Silver", nil)

	literal, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest",
			"where":{"field":"@name","op":"matches","value":"50%*"}}]}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := keysOf(literal.Nodes); len(got) != 1 || got[0] != "half" {
		t.Fatalf(`matches "50%%*" must match only the name that holds a per-cent, got %v`, got)
	}
	// The control: the same glob without the per-cent matches both, which
	// is what makes the assertion above about the escape and not about an
	// empty answer.
	both, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest",
			"where":{"field":"@name","op":"matches","value":"50*"}}]}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := keysOf(both.Nodes); len(got) != 2 {
		t.Fatalf(`matches "50*" is the control and matches both, got %v`, got)
	}
	// A `?` is one character, and the underscore a caller writes is not.
	single, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest",
			"where":{"field":"@name","op":"matches","value":"5?%*"}}]}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := keysOf(single.Nodes); len(got) != 1 || got[0] != "half" {
		t.Fatalf(`matches "5?%%*" must map ? and keep the per-cent literal, got %v`, got)
	}
}

// TestAnInListComparesAsItsOwnType pins the typed-array cast, which
// nothing reached before: `in` binds its operand list as an array of the
// field's declared type and says so in the statement, because a list
// bound as `any` arrives as text and compares a number against its own
// spelling. Both halves are here — the emitted cast and the rows it
// returns — because the cast is what the behaviour rests on.
func TestAnInListComparesAsItsOwnType(t *testing.T) {
	g, _ := newGame(t)
	numeric := `{"v":1,"from":[{"type":"quest",
		"where":{"field":"min_level","op":"in","value":[22,28]}}]}`
	sql, _ := compileOf(t, g, numeric)
	if !strings.Contains(sql, "::numeric[]") {
		t.Fatalf("a number list binds as numeric[]:\n%s", sql)
	}
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{Query: mustParse(t, numeric)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := keysOf(res.Nodes); len(got) != 2 {
		t.Fatalf("22 and 28 are two of the three quests, got %v", got)
	}
	// The text arm is a different branch of typedList, and the fold is
	// applied to the list rather than to a single value.
	textual := `{"v":1,"from":[{"type":"quest",
		"where":{"field":"@key","op":"in","value":["HOGGER","cook"]}}]}`
	sql, _ = compileOf(t, g, textual)
	if !strings.Contains(sql, "::text[]") {
		t.Fatalf("a text list binds as text[]:\n%s", sql)
	}
	res, err = g.views.Run(t.Context(), g.projectID, RunRequest{Query: mustParse(t, textual)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := keysOf(res.Nodes); len(got) != 2 {
		t.Fatalf("a key list folds each element, got %v", got)
	}
}

// TestABetweenEdgeReadsItsDirection: the two sets of a `between` entry
// are ordered, and `in` draws the relations that run the other way. No
// test used a non-default direction before this one, so the arm shipped
// on a reading of the code rather than on an answer from the database.
func TestABetweenEdgeReadsItsDirection(t *testing.T) {
	g, _ := newGame(t)
	// takes_place_in runs quest -> zone, so "out" between quests and
	// zones draws it and "in" draws nothing.
	run := func(direction string) []Edge {
		t.Helper()
		res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, `{"v":1,"from":[{"type":"quest","as":"quests"},
				{"type":"zone","as":"zones"}],
				"edges":[{"via":"takes_place_in","between":["quests","zones"],
				          "direction":"`+direction+`"}]}`)})
		if err != nil {
			t.Fatalf("run %s: %v", direction, err)
		}
		return res.Edges
	}
	if got := len(run("out")); got != 2 {
		t.Fatalf("two quests take place in a zone, got %d edges out", got)
	}
	if got := len(run("in")); got != 0 {
		t.Fatalf("no zone takes place in a quest, got %d edges in", got)
	}
	if got := len(run("any")); got != 2 {
		t.Fatalf("any draws the same two, got %d", got)
	}
}

// TestAnEdgeDrawnTwiceComesBackOnce: the node dedupe was pinned and the
// edge dedupe was not. Two entries drawing the same relation cannot be
// deduplicated by the UNION, because each arm carries its own bound rank,
// so the edge arriving once is Go's doing and this is what says so.
func TestAnEdgeDrawnTwiceComesBackOnce(t *testing.T) {
	g, _ := newGame(t)
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"class","as":"cls"}],
			"traverse":[{"from":"cls","via":"available_to","direction":"in",
			             "to_type":"quest","as":"quests"}],
			"edges":[{"from_step":"quests"},
			         {"via":"available_to","between":["quests","cls"]}]}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Edges) != 3 {
		t.Fatalf("both entries draw the same three relations, got %d edges", len(res.Edges))
	}
	seen := map[uuid.UUID]bool{}
	for _, e := range res.Edges {
		if seen[e.ID] {
			t.Fatalf("the relation %s came back twice", e.ID)
		}
		seen[e.ID] = true
	}
	if res.Stats.Edges != len(res.Edges) {
		t.Fatalf("stats count the rows that came back: %d vs %d", res.Stats.Edges, len(res.Edges))
	}
}

// TestMaxDepthReachedCountsTheHopsThatContributedANode pins the
// arithmetic Task 8 is told to replace, which had no test at all: a
// selector is depth 0, a step is its source's depth plus its own, and a
// set that drew no node contributes nothing.
func TestMaxDepthReachedCountsTheHopsThatContributedANode(t *testing.T) {
	g, _ := newGame(t)
	depthOf := func(doc string) int {
		t.Helper()
		res, err := g.views.Run(t.Context(), g.projectID, RunRequest{Query: mustParse(t, doc)})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(res.Nodes) == 0 {
			t.Fatalf("a depth read off an empty result would say nothing: %s", doc)
		}
		return res.Stats.MaxDepthReached
	}
	if got := depthOf(`{"v":1,"from":[{"type":"quest"}]}`); got != 0 {
		t.Errorf("a selector is depth 0, got %d", got)
	}
	oneHop := `{"v":1,"from":[{"type":"class","as":"cls"}],
		"traverse":[{"from":"cls","via":"available_to","direction":"in",
		             "to_type":"quest","as":"quests"}]}`
	if got := depthOf(oneHop); got != 1 {
		t.Errorf("one step is depth 1, got %d", got)
	}
	twoHops := `{"v":1,"from":[{"type":"class","as":"cls"}],
		"traverse":[{"from":"cls","via":"available_to","direction":"in",
		             "to_type":"quest","as":"quests"},
		            {"from":"quests","via":"takes_place_in","direction":"out",
		             "to_type":"zone","as":"zones"}]}`
	if got := depthOf(twoHops); got != 2 {
		t.Errorf("a step from a step is depth 2, got %d", got)
	}
	// The second step is walked but drawn by nobody, so nothing it
	// reached contributes a depth.
	drawnShallow := `{"v":1,"from":[{"type":"class","as":"cls"}],
		"traverse":[{"from":"cls","via":"available_to","direction":"in",
		             "to_type":"quest","as":"quests"},
		            {"from":"quests","via":"takes_place_in","direction":"out",
		             "to_type":"zone","as":"zones"}],
		"nodes":[{"set":"cls"},{"set":"quests"}]}`
	if got := depthOf(drawnShallow); got != 1 {
		t.Errorf("a set that drew no node contributes no depth, got %d", got)
	}
}

// TestAnEmptyResultSerialisesAsEmptyListsNotNull: a nil slice marshals as
// JSON null, and an envelope whose nodes are null is a different shape
// from one whose nodes are [] for every client that reads it — Task 15's
// REST mirror included.
func TestAnEmptyResultSerialisesAsEmptyListsNotNull(t *testing.T) {
	g, _ := newGame(t)
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest",
			"where":{"field":"@key","op":"eq","value":"no-such-quest"}}]}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Nodes) != 0 || len(res.Edges) != 0 {
		t.Fatalf("this query draws nothing: %d nodes, %d edges", len(res.Nodes), len(res.Edges))
	}
	encoded, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"nodes":[]`, `"edges":[]`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("an empty result carries %s, got %s", want, encoded)
		}
	}
}

// TestAStepDrawsOnlyItsDestinationTypeAndOnlyValidRows pins the two
// filters a step's entity join carries that only the golden files were
// red for: `to_type` and the invalid exclusion. A golden file is a diff a
// reviewer might regenerate; this is an answer from the database.
//
// The fixture needs a relation of the walked type reaching a row of
// another type, which the seed does not have, and an invalid row on the
// far side, which it does not either — so the test makes both.
func TestAStepDrawsOnlyItsDestinationTypeAndOnlyValidRows(t *testing.T) {
	g, _ := newGame(t)
	// A zone that is "available_to" the mage: nothing in the metamodel
	// constrains a relation type's endpoints, and a step's to_type is
	// what keeps a walk to one kind of thing.
	g.relate(t, "available_to", "zone", "elwynn", "class", "mage")
	if _, err := g.pool.Exec(t.Context(),
		`UPDATE entities SET invalid = true WHERE project_id = $1 AND key = 'cook'`,
		g.projectID); err != nil {
		t.Fatalf("flag the row: %v", err)
	}
	walk := func(extra string) []string {
		t.Helper()
		res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, `{"v":1,`+extra+`"from":[{"type":"class","as":"cls",
				"where":{"field":"@key","op":"eq","value":"mage"}}],
				"traverse":[{"from":"cls","via":"available_to","direction":"in",
				             "to_type":"quest","as":"reachable"}],
				"nodes":[{"set":"reachable"}]}`)})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		got := keysOf(res.Nodes)
		sort.Strings(got)
		return got
	}
	if got := strings.Join(walk(""), ","); got != "defias,hogger" {
		t.Fatalf("the walk draws valid quests only — not the zone, not the flagged row: %v", got)
	}
	// The control for the invalid filter: lifting it draws the third one,
	// and still not the zone.
	if got := strings.Join(walk(`"include_invalid":true,`), ","); got != "cook,defias,hogger" {
		t.Fatalf("include_invalid lifts the exclusion and to_type still holds: %v", got)
	}
	// The control for to_type: without it the zone is reachable, which is
	// what makes the assertions above about the filter and not about the
	// fixture.
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"class","as":"cls",
			"where":{"field":"@key","op":"eq","value":"mage"}}],
			"traverse":[{"from":"cls","via":"available_to","direction":"in","as":"reachable"}],
			"nodes":[{"set":"reachable"}]}`)})
	if err != nil {
		t.Fatalf("run without to_type: %v", err)
	}
	got := keysOf(res.Nodes)
	sort.Strings(got)
	if strings.Join(got, ",") != "defias,elwynn,hogger" {
		t.Fatalf("without to_type the zone is reached too, got %v", got)
	}
}

// TestNoArmOfAPictureDrawsAnEdgeThatNoLongerValidates is the edge twin of
// TestAStepDrawsOnlyItsDestinationTypeAndOnlyValidRows, and it exists
// because until 0009 an edge could not be flagged at all: a relation type
// carries a field schema, an edge's values are validated against it, and
// nothing re-judged them when the schema changed.
//
// **It exercises every arm of the compiler that names the `relations`
// table**, in one fixture, because that is the failure this repository
// keeps repeating: a rule established in one arm and not carried to the
// others. The arms are
//
//   - `hop`, a one-hop step's own JOIN;
//   - `walk`, a multi-hop step, where the exclusion rides in the edge
//     predicate and prunes the recursion rather than filtering its output
//     — an edge dropped on the way out would leave the node behind it
//     drawn with nothing joining it to the picture;
//   - `edges[].from_step`, the edges a step walked;
//   - `edges[].between`, relations drawn between two sets;
//   - `project`'s one-hop related attribute, where an edge nobody drew
//     still colours a node.
//
// Each is asserted with `include_invalid` as its own control, so a green
// assertion cannot be a fixture that was empty either way.
func TestNoArmOfAPictureDrawsAnEdgeThatNoLongerValidates(t *testing.T) {
	g, _ := newGame(t)
	// hogger -> elwynn is the edge that stops validating. defias ->
	// westfall is the same relation type, left alone, and it is what
	// makes every assertion below about the flag rather than about the
	// relation type.
	if _, err := g.pool.Exec(t.Context(),
		`UPDATE relations SET invalid = true
		   WHERE project_id = $1
		     AND source_id = (SELECT id FROM entities WHERE project_id = $1 AND key = 'hogger')
		     AND target_id = (SELECT id FROM entities WHERE project_id = $1 AND key = 'elwynn')`,
		g.projectID); err != nil {
		t.Fatalf("flag the edge: %v", err)
	}
	// A second hop for the walk arm to cross: elwynn takes_place_in
	// westfall is nonsense as game content and is exactly what a walk
	// needs — a valid edge on the far side of the flagged one.
	g.relate(t, "takes_place_in", "zone", "elwynn", "zone", "westfall")

	nodes := func(doc string) string {
		t.Helper()
		res, err := g.views.Run(t.Context(), g.projectID, RunRequest{Query: mustParse(t, doc)})
		if err != nil {
			t.Fatalf("run %s: %v", doc, err)
		}
		got := keysOf(res.Nodes)
		sort.Strings(got)
		return strings.Join(got, ",")
	}
	edges := func(doc string) int {
		t.Helper()
		res, err := g.views.Run(t.Context(), g.projectID, RunRequest{Query: mustParse(t, doc)})
		if err != nil {
			t.Fatalf("run %s: %v", doc, err)
		}
		return len(res.Edges)
	}

	// --- hop: a one-hop step must not follow the flagged edge. ---
	const hopDoc = `{"v":1,%s"from":[{"type":"quest","as":"q"}],
		"traverse":[{"from":"q","via":"takes_place_in","as":"where"}],
		"nodes":[{"set":"where"}]}`
	if got := nodes(fmt.Sprintf(hopDoc, "")); got != "westfall" {
		t.Fatalf("a step followed an edge that no longer validates: %v", got)
	}
	if got := nodes(fmt.Sprintf(hopDoc, `"include_invalid":true,`)); got != "elwynn,westfall" {
		t.Fatalf("include_invalid must lift the exclusion on a step's edge, got %v", got)
	}

	// --- walk: the exclusion prunes the recursion, so nothing behind the
	// flagged edge is reached either. Without pruning, westfall would be
	// reached twice over — once directly, once through elwynn — and the
	// difference would be invisible; the control is what makes it visible,
	// because with the exclusion lifted elwynn appears.
	const walkDoc = `{"v":1,%s"from":[{"type":"quest","as":"q",
		  "where":{"field":"@key","op":"eq","value":"hogger"}}],
		"traverse":[{"from":"q","via":"takes_place_in","as":"chain","depth":{"max":3}}],
		"nodes":[{"set":"chain"}]}`
	if got := nodes(fmt.Sprintf(walkDoc, "")); got != "" {
		t.Fatalf("a walk crossed an edge that no longer validates and drew %v", got)
	}
	if got := nodes(fmt.Sprintf(walkDoc, `"include_invalid":true,`)); got != "elwynn,westfall" {
		t.Fatalf("include_invalid must let the walk cross, got %v", got)
	}

	// --- edges[].from_step and edges[].between: the two collection
	// points that draw relations. from_step reads the step, which has
	// already pruned; between reads the table itself.
	const betweenDoc = `{"v":1,%s"from":[{"type":"quest","as":"q"},{"type":"zone","as":"z"}],
		"nodes":[{"set":"q"},{"set":"z"}],
		"edges":[{"via":"takes_place_in","between":["q","z"]}]}`
	if got := edges(fmt.Sprintf(betweenDoc, "")); got != 1 {
		t.Fatalf("a between entry drew %d edges, want only the one that still validates", got)
	}
	if got := edges(fmt.Sprintf(betweenDoc, `"include_invalid":true,`)); got != 2 {
		t.Fatalf("include_invalid must draw both, got %d", got)
	}

	const fromStepDoc = `{"v":1,%s"from":[{"type":"quest","as":"q"}],
		"traverse":[{"from":"q","via":"takes_place_in","as":"where"}],
		"nodes":[{"set":"where"}],"edges":[{"from_step":"where"}]}`
	if got := edges(fmt.Sprintf(fromStepDoc, "")); got != 1 {
		t.Fatalf("a from_step entry drew %d edges, want only the one that still validates", got)
	}
	if got := edges(fmt.Sprintf(fromStepDoc, `"include_invalid":true,`)); got != 2 {
		t.Fatalf("include_invalid must draw both, got %d", got)
	}

	// --- project: a one-hop related attribute must not colour a node
	// across an edge that no longer validates. hogger's zone is reached
	// only through the flagged edge, so its slot goes empty; defias's is
	// reached through the intact one and stays filled, which is what
	// makes this about the flag.
	colours := func(extra string) map[string]any {
		t.Helper()
		res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, `{"v":1,`+extra+`"from":[{"type":"quest","as":"q"}],
				"nodes":[{"set":"q"}],
				"project":{"color_by":{"related":{"via":"takes_place_in",
				  "direction":"out","attr":"@key"}}}}`)})
		if err != nil {
			t.Fatalf("run the projection: %v", err)
		}
		out := map[string]any{}
		for _, node := range res.Nodes {
			out[node.Key] = node.Attrs["color_by"]
		}
		return out
	}
	got := colours("")
	if got["hogger"] != nil {
		t.Fatalf("a node was coloured across an edge that no longer validates: %v", got["hogger"])
	}
	if got["defias"] != "westfall" {
		t.Fatalf("the intact edge stopped colouring its node: %v — the assertion above "+
			"would pass on an empty fixture", got["defias"])
	}
	if got := colours(`"include_invalid":true,`); got["hogger"] != "elwynn" {
		t.Fatalf("include_invalid must lift the exclusion on a related attribute, got %v",
			got["hogger"])
	}
}
