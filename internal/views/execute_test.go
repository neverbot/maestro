package views

import (
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
