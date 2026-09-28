package views

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/assert"
)

func TestExecuteArea(t *testing.T) {
	t.Parallel()
	a := newArea(t)

	// TestExecuteArea's "a seed selector comes back as nodes" case is the
	// read-back this whole sub-project's first feature owes. It asserts a
	// named quest with its key, name, type and set — not a row count, which an
	// unrelated bug can also satisfy.
	t.Run("a seed selector comes back as nodes", func(t *testing.T) {
		g, _ := a.games(t)
		res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, `{"v":1,"from":[{"type":"quest","as":"quests"}]}`),
		})
		assert.Must(t, err == nil, "run: %v", err)
		byKey := map[string]Node{}
		for _, n := range res.Nodes {
			byKey[n.Key] = n
		}
		hogger, ok := byKey["hogger"]
		assert.Must(t, ok, "the seeded quest must come back; got %v", keysOf(res.Nodes))
		assert.Must(t, hogger.Name == "Wanted: Hogger" && hogger.Type == "quest" && hogger.Set == "quests", "the node did not carry its identity: %+v", hogger)
		assert.Must(t, len(res.Nodes) == 3, "three quests are seeded, got %d: %v", len(res.Nodes), keysOf(res.Nodes))
		assert.Must(t, res.Stats.Nodes == 3, "stats must count what came back, got %+v", res.Stats)
	})

	t.Run("a predicate narrows the result and the control proves it", func(t *testing.T) {
		g, _ := a.games(t)
		all, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, `{"v":1,"from":[{"type":"quest"}]}`)})
		assert.Must(t, err == nil, "run: %v", err)
		assert.Must(t, len(all.Nodes) == 3, "positive control: three quests, got %d", len(all.Nodes))
		band, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, `{"v":1,"from":[{"type":"quest","where":{"all":[
				{"field":"min_level","op":"gte","value":20},
				{"field":"min_level","op":"lte","value":30}]}}]}`)})
		assert.Must(t, err == nil, "run: %v", err)
		if got := keysOf(band.Nodes); len(got) != 2 {
			t.Fatalf("the 20-30 band holds hogger and defias, got %v", got)
		}
	})

	t.Run("a one hop traversal returns its edges", func(t *testing.T) {
		g, _ := a.games(t)
		res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, `{"v":1,"from":[{"type":"class","as":"cls",
				"where":{"field":"@key","op":"eq","value":"mage"}}],
				"traverse":[{"from":"cls","via":"available_to","direction":"in",
				             "to_type":"quest","as":"reachable"}],
				"edges":[{"from_step":"reachable"}]}`)})
		assert.Must(t, err == nil, "run: %v", err)
		assert.Must(t, len(res.Edges) == 3, "three quests are available to the mage, got %d edges", len(res.Edges))
		for _, e := range res.Edges {
			assert.Must(t, e.Type == "available_to" && e.Source != e.Target, "an edge must carry its type and two endpoints: %+v", e)
		}
	})

	// TestExecuteArea's "a run from another game sees nothing" case is the
	// isolation test at the execution boundary, with its positive control in
	// the same test.
	t.Run("a run from another game sees nothing", func(t *testing.T) {
		g, other := a.games(t)
		q := `{"v":1,"from":[{"type":"quest"}]}`
		mine, err := g.views.Run(t.Context(), g.projectID, RunRequest{Query: mustParse(t, q)})
		assert.Must(t, err == nil && len(mine.Nodes) == 3, "positive control: %v, %d nodes", err, len(mine.Nodes))
		theirs, err := other.views.Run(t.Context(), other.projectID, RunRequest{Query: mustParse(t, q)})
		assert.Must(t, err == nil, "run in the other game: %v", err)
		for _, n := range theirs.Nodes {
			for _, m := range mine.Nodes {
				assert.Must(t, n.ID != m.ID, "a node of one game came back in the other: %s", n.Key)
			}
		}
	})

	// TestExecuteArea's "a run binds its parameters over the declared
	// defaults" case is the read-back a parameter owes: the default draws one
	// answer, the binding draws another, and both come back through Run rather
	// than through Resolve.
	t.Run("a run binds its parameters over the declared defaults", func(t *testing.T) {
		g, _ := a.games(t)
		g.entity(t, "class", "rogue", "Rogue", nil)
		doc := `{"v":1,"params":[{"key":"class_key","type":"text","default":"mage"}],
			"from":[{"type":"class","as":"cls",
			  "where":{"field":"@key","op":"eq","value":{"param":"class_key"}}}]}`

		byDefault, err := g.views.Run(t.Context(), g.projectID, RunRequest{Query: mustParse(t, doc)})
		assert.Must(t, err == nil, "run with the default: %v", err)
		if got := keysOf(byDefault.Nodes); len(got) != 1 || got[0] != "mage" {
			t.Fatalf("the declared default selects the mage, got %v", got)
		}

		bound, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, doc), Params: map[string]any{"class_key": "rogue"}})
		assert.Must(t, err == nil, "run with a binding: %v", err)
		if got := keysOf(bound.Nodes); len(got) != 1 || got[0] != "rogue" {
			t.Fatalf("the run's own value must win over the default, got %v", got)
		}
	})

	t.Run("a parameter the query does not declare is refused", func(t *testing.T) {
		g, _ := a.games(t)
		doc := `{"v":1,"params":[{"key":"class_key","type":"text","default":"mage"}],
			"from":[{"type":"class","as":"cls",
			  "where":{"field":"@key","op":"eq","value":{"param":"class_key"}}}]}`
		_, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, doc), Params: map[string]any{"clsas_key": "rogue"}})
		oneProblem(t, err, "/params", `no parameter named "clsas_key"`)

		_, err = g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, doc), Params: map[string]any{"class_key": 3}})
		oneProblem(t, err, "/params/0", "expected text, got int")
	})

	// TestExecuteArea's "a parameter with no value is refused rather than
	// compiled as nothing" case: a parameter declared without a default and
	// left unbound has no value, and compiling it as NULL would draw an empty
	// picture and say nothing.
	t.Run("a parameter with no value is refused rather than compiled as nothing", func(t *testing.T) {
		g, _ := a.games(t)
		doc := `{"v":1,"params":[{"key":"class_key","type":"text"}],
			"from":[{"type":"class","as":"cls",
			  "where":{"field":"@key","op":"eq","value":{"param":"class_key"}}}]}`
		_, err := g.views.Run(t.Context(), g.projectID, RunRequest{Query: mustParse(t, doc)})
		oneProblem(t, err, "/from/0/where/value", "has no value for this run")

		// The control: supplying one runs.
		res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, doc), Params: map[string]any{"class_key": "mage"}})
		assert.Must(t, err == nil && len(res.Nodes) == 1, "the control must run: %v, %d nodes", err, len(res.Nodes))
	})

	// TestExecuteArea's "a node in two sets comes back once under the first
	// set that claimed it" case pins the deduplication, and pins the ordering
	// **as text**.
	t.Run("a node in two sets comes back once under the first set that claimed it", func(t *testing.T) {
		g, _ := a.games(t)
		sql, _ := compileOf(t, g, `{"v":1,"from":[{"type":"quest","as":"first"},
			{"type":"quest","as":"second"}]}`)
		assert.Must(t, strings.Contains(sql, "ORDER BY 1, 11, 2"), "the rows must be ordered by kind, then by the declaration rank of the "+
			"entry that produced them, then by id:\n%s", sql)
		res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, `{"v":1,"from":[{"type":"quest","as":"first"},
				{"type":"quest","as":"second"}]}`)})
		assert.Must(t, err == nil, "run: %v", err)
		assert.Must(t, len(res.Nodes) == 3, "three quests in two sets are three nodes, got %d: %v",
			len(res.Nodes), keysOf(res.Nodes))
		for _, n := range res.Nodes {
			assert.Must(t, n.Set == "first", "the first set declared must claim the node, got %q for %s", n.Set, n.Key)
		}
	})

	// TestExecuteArea's "include fields is what puts fields in the envelope"
	// case reads back the switch that decides whether a run carries its jsonb
	// payload, on both sides.
	t.Run("include fields is what puts fields in the envelope", func(t *testing.T) {
		g, _ := a.games(t)
		doc := `{"v":1,"from":[{"type":"quest","keys":["hogger"]}]}`
		without, err := g.views.Run(t.Context(), g.projectID, RunRequest{Query: mustParse(t, doc)})
		assert.Must(t, err == nil && len(without.Nodes) == 1, "run: %v, %d nodes", err, len(without.Nodes))
		if without.Nodes[0].Fields != nil {
			t.Fatalf("a run that did not ask for fields must not carry them: %v",
				without.Nodes[0].Fields)
		}
		with, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, doc), IncludeFields: true})
		assert.Must(t, err == nil && len(with.Nodes) == 1, "run: %v, %d nodes", err, len(with.Nodes))
		if got := with.Nodes[0].Fields["min_level"]; got != float64(22) {
			t.Fatalf("include_fields must carry the declared values, got %#v", with.Nodes[0].Fields)
		}
	})

	// TestExecuteArea's "a like patterns metacharacters are escaped" case is
	// why escapeLike exists: a designer's quest name may hold a per-cent sign,
	// and `starts_with: "50%"` must mean a name starting "50%" rather than a
	// name starting "50". The control is in the same assertion, so an empty
	// answer cannot pass it.
	t.Run("a like patterns metacharacters are escaped", func(t *testing.T) {
		g, _ := a.games(t)
		g.entity(t, "quest", "half", "50% Off", nil)
		g.entity(t, "quest", "fifty", "50 Silver", nil)

		escaped, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, `{"v":1,"from":[{"type":"quest",
				"where":{"field":"@name","op":"starts_with","value":"50%"}}]}`)})
		assert.Must(t, err == nil, "run: %v", err)
		if got := keysOf(escaped.Nodes); len(got) != 1 || got[0] != "half" {
			t.Fatalf(`"50%%" must match only the name that holds one, got %v`, got)
		}
		control, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, `{"v":1,"from":[{"type":"quest",
				"where":{"field":"@name","op":"starts_with","value":"50"}}]}`)})
		assert.Must(t, err == nil, "run: %v", err)
		if got := keysOf(control.Nodes); len(got) != 2 {
			t.Fatalf("the unescaped prefix matches both, got %v", got)
		}
	})

	// TestExecuteArea's "a key comparison folds case on both sides" case: a
	// row key is matched case-insensitively by the write path
	// (entities_key_key is UNIQUE over lower(key)), so folding one side only
	// would refuse a spelling the metamodel accepted.
	t.Run("a key comparison folds case on both sides", func(t *testing.T) {
		g, _ := a.games(t)
		g.entity(t, "quest", "Deadmines", "The Deadmines", nil)
		for _, spelling := range []string{"deadmines", "DEADMINES", "DeadMines"} {
			res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
				Query: mustParse(t, `{"v":1,"from":[{"type":"quest",
					"where":{"field":"@key","op":"eq","value":"`+spelling+`"}}]}`)})
			assert.Must(t, err == nil, "run for %q: %v", spelling, err)
			if got := keysOf(res.Nodes); len(got) != 1 || got[0] != "Deadmines" {
				t.Fatalf("%q must find the stored key, got %v", spelling, got)
			}
		}
		// The keys shortcut folds too, and it is a different clause.
		res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, `{"v":1,"from":[{"type":"quest","keys":["DEADMINES"]}]}`)})
		assert.Must(t, err == nil && len(res.Nodes) == 1, "the keys shortcut folds as well: %v, %v", err, keysOf(res.Nodes))
	})

	// TestExecuteArea's "a list field answers its own operators" case covers
	// the arms nothing else reaches: containment, the any/all set operators,
	// empty and the length family, each with the row that must not match in
	// the same fixture.
	t.Run("a list field answers its own operators", func(t *testing.T) {
		g, _ := a.games(t)
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
			assert.Must(t, err == nil, "run %s: %v", c.predicate, err)
			got := keysOf(res.Nodes)
			sort.Strings(got)
			assert.Should(t, strings.Join(got, ",") == strings.Join(c.want, ","), "%s: want %v, got %v", c.predicate, c.want, got)
		}
	})

	// TestExecuteArea's "a value of the wrong jsonb type is skipped rather
	// than raised" case is the failure the jsonb_typeof guard exists for,
	// seen. internal/metamodel flags an entity invalid on a schema change and
	// leaves its values in place, so a field declared number can hold a
	// string; without the guard the cast raises SQLSTATE 22P02 and the whole
	// view fails on one stale row.
	t.Run("a value of the wrong jsonb type is skipped rather than raised", func(t *testing.T) {
		g, _ := a.games(t)
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
		assert.Must(t, err == nil, "a stale value must not fail the view: %v", err)
		got := keysOf(res.Nodes)
		sort.Strings(got)
		assert.Must(t, strings.Join(got, ",") == "defias,hogger", "the two numeric rows answer and the stale one does not, got %v", got)
	})

	// TestExecuteArea's "a glob pattern does not leak the per cent it was
	// given" case is the behavioural half of the same rule, and the test the
	// operator had none of: `matches "50%*"` asks for names that start "50%",
	// not for names that start "50". The control row is in the same fixture,
	// so an empty answer cannot pass.
	t.Run("a glob pattern does not leak the per cent it was given", func(t *testing.T) {
		g, _ := a.games(t)
		g.entity(t, "quest", "half", "50% Off", nil)
		g.entity(t, "quest", "fifty", "50 Silver", nil)

		literal, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, `{"v":1,"from":[{"type":"quest",
				"where":{"field":"@name","op":"matches","value":"50%*"}}]}`)})
		assert.Must(t, err == nil, "run: %v", err)
		if got := keysOf(literal.Nodes); len(got) != 1 || got[0] != "half" {
			t.Fatalf(`matches "50%%*" must match only the name that holds a per-cent, got %v`, got)
		}
		// The control: the same glob without the per-cent matches both, which
		// is what makes the assertion above about the escape and not about an
		// empty answer.
		both, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, `{"v":1,"from":[{"type":"quest",
				"where":{"field":"@name","op":"matches","value":"50*"}}]}`)})
		assert.Must(t, err == nil, "run: %v", err)
		if got := keysOf(both.Nodes); len(got) != 2 {
			t.Fatalf(`matches "50*" is the control and matches both, got %v`, got)
		}
		// A `?` is one character, and the underscore a caller writes is not.
		single, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, `{"v":1,"from":[{"type":"quest",
				"where":{"field":"@name","op":"matches","value":"5?%*"}}]}`)})
		assert.Must(t, err == nil, "run: %v", err)
		if got := keysOf(single.Nodes); len(got) != 1 || got[0] != "half" {
			t.Fatalf(`matches "5?%%*" must map ? and keep the per-cent literal, got %v`, got)
		}
	})

	// TestExecuteArea's "an in list compares as its own type" case pins the
	// typed-array cast, which nothing reached before: `in` binds its operand
	// list as an array of the field's declared type and says so in the
	// statement, because a list bound as `any` arrives as text and compares a
	// number against its own spelling. Both halves are here — the emitted cast
	// and the rows it returns — because the cast is what the behaviour rests
	// on.
	t.Run("an in list compares as its own type", func(t *testing.T) {
		g, _ := a.games(t)
		numeric := `{"v":1,"from":[{"type":"quest",
			"where":{"field":"min_level","op":"in","value":[22,28]}}]}`
		sql, _ := compileOf(t, g, numeric)
		assert.Must(t, strings.Contains(sql, "::numeric[]"), "a number list binds as numeric[]:\n%s", sql)
		res, err := g.views.Run(t.Context(), g.projectID, RunRequest{Query: mustParse(t, numeric)})
		assert.Must(t, err == nil, "run: %v", err)
		if got := keysOf(res.Nodes); len(got) != 2 {
			t.Fatalf("22 and 28 are two of the three quests, got %v", got)
		}
		// The text arm is a different branch of typedList, and the fold is
		// applied to the list rather than to a single value.
		textual := `{"v":1,"from":[{"type":"quest",
			"where":{"field":"@key","op":"in","value":["HOGGER","cook"]}}]}`
		sql, _ = compileOf(t, g, textual)
		assert.Must(t, strings.Contains(sql, "::text[]"), "a text list binds as text[]:\n%s", sql)
		res, err = g.views.Run(t.Context(), g.projectID, RunRequest{Query: mustParse(t, textual)})
		assert.Must(t, err == nil, "run: %v", err)
		if got := keysOf(res.Nodes); len(got) != 2 {
			t.Fatalf("a key list folds each element, got %v", got)
		}
	})

	// TestExecuteArea's "a between edge reads its direction" case: the two
	// sets of a `between` entry are ordered, and `in` draws the relations that
	// run the other way. No test used a non-default direction before this one,
	// so the arm shipped on a reading of the code rather than on an answer
	// from the database.
	t.Run("a between edge reads its direction", func(t *testing.T) {
		g, _ := a.games(t)
		// takes_place_in runs quest -> zone, so "out" between quests and
		// zones draws it and "in" draws nothing.
		run := func(direction string) []Edge {
			t.Helper()
			res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
				Query: mustParse(t, `{"v":1,"from":[{"type":"quest","as":"quests"},
					{"type":"zone","as":"zones"}],
					"edges":[{"via":"takes_place_in","between":["quests","zones"],
					          "direction":"`+direction+`"}]}`)})
			assert.Must(t, err == nil, "run %s: %v", direction, err)
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
	})

	// TestExecuteArea's "an edge drawn twice comes back once" case: the node
	// dedupe was pinned and the edge dedupe was not. Two entries drawing the
	// same relation cannot be deduplicated by the UNION, because each arm
	// carries its own bound rank, so the edge arriving once is Go's doing and
	// this is what says so.
	t.Run("an edge drawn twice comes back once", func(t *testing.T) {
		g, _ := a.games(t)
		res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, `{"v":1,"from":[{"type":"class","as":"cls"}],
				"traverse":[{"from":"cls","via":"available_to","direction":"in",
				             "to_type":"quest","as":"quests"}],
				"edges":[{"from_step":"quests"},
				         {"via":"available_to","between":["quests","cls"]}]}`)})
		assert.Must(t, err == nil, "run: %v", err)
		assert.Must(t, len(res.Edges) == 3, "both entries draw the same three relations, got %d edges", len(res.Edges))
		seen := map[uuid.UUID]bool{}
		for _, e := range res.Edges {
			assert.Must(t, !(seen[e.ID]), "the relation %s came back twice", e.ID)
			seen[e.ID] = true
		}
		assert.Must(t, res.Stats.Edges == len(res.Edges), "stats count the rows that came back: %d vs %d", res.Stats.Edges, len(res.Edges))
	})

	// TestExecuteArea's "max depth reached counts the hops that contributed a
	// node" case pins the arithmetic Task 8 is told to replace, which had no
	// test at all: a selector is depth 0, a step is its source's depth plus
	// its own, and a set that drew no node contributes nothing.
	t.Run("max depth reached counts the hops that contributed a node", func(t *testing.T) {
		g, _ := a.games(t)
		depthOf := func(doc string) int {
			t.Helper()
			res, err := g.views.Run(t.Context(), g.projectID, RunRequest{Query: mustParse(t, doc)})
			assert.Must(t, err == nil, "run: %v", err)
			assert.Must(t, len(res.Nodes) != 0, "a depth read off an empty result would say nothing: %s", doc)
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
	})

	// TestExecuteArea's "an empty result serialises as empty lists not null"
	// case: a nil slice marshals as JSON null, and an envelope whose nodes are
	// null is a different shape from one whose nodes are [] for every client
	// that reads it — Task 15's REST mirror included.
	t.Run("an empty result serialises as empty lists not null", func(t *testing.T) {
		g, _ := a.games(t)
		res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, `{"v":1,"from":[{"type":"quest",
				"where":{"field":"@key","op":"eq","value":"no-such-quest"}}]}`)})
		assert.Must(t, err == nil, "run: %v", err)
		assert.Must(t, len(res.Nodes) == 0 && len(res.Edges) == 0, "this query draws nothing: %d nodes, %d edges", len(res.Nodes), len(res.Edges))
		encoded, err := json.Marshal(res)
		assert.Must(t, err == nil, "marshal: %v", err)
		for _, want := range []string{`"nodes":[]`, `"edges":[]`} {
			assert.Should(t, strings.Contains(string(encoded), want), "an empty result carries %s, got %s", want, encoded)
		}
	})

	// TestExecuteArea's "a step draws only its destination type and only valid
	// rows" case pins the two filters a step's entity join carries that only
	// the golden files were red for: `to_type` and the invalid exclusion. A
	// golden file is a diff a reviewer might regenerate; this is an answer
	// from the database.
	t.Run("a step draws only its destination type and only valid rows", func(t *testing.T) {
		g, _ := a.games(t)
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
			assert.Must(t, err == nil, "run: %v", err)
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
		assert.Must(t, err == nil, "run without to_type: %v", err)
		got := keysOf(res.Nodes)
		sort.Strings(got)
		assert.Must(t, strings.Join(got, ",") == "defias,elwynn,hogger", "without to_type the zone is reached too, got %v", got)
	})

	// TestExecuteArea's "no arm of a picture draws an edge that no longer
	// validates" case is the edge twin of TestExecuteArea's "a step draws only
	// its destination type and only valid rows" case, and it exists because
	// until 0009 an edge could not be flagged at all: a relation type carries
	// a field schema, an edge's values are validated against it, and nothing
	// re-judged them when the schema changed.
	t.Run("no arm of a picture draws an edge that no longer validates", func(t *testing.T) {
		g, _ := a.games(t)
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
			assert.Must(t, err == nil, "run %s: %v", doc, err)
			got := keysOf(res.Nodes)
			sort.Strings(got)
			return strings.Join(got, ",")
		}
		edges := func(doc string) int {
			t.Helper()
			res, err := g.views.Run(t.Context(), g.projectID, RunRequest{Query: mustParse(t, doc)})
			assert.Must(t, err == nil, "run %s: %v", doc, err)
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
			assert.Must(t, err == nil, "run the projection: %v", err)
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
	})
}

func keysOf(nodes []Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Key)
	}
	return out
}
