package views

import (
	"errors"
	"strings"
	"testing"
)

// nodesByKey indexes a result the way every assertion in this file reads
// it: by the row key, because a node's position in the slice is the
// document's declaration order and not something a projection test is
// about.
func nodesByKey(nodes []Node) map[string]Node {
	out := make(map[string]Node, len(nodes))
	for _, n := range nodes {
		out[n.Key] = n
	}
	return out
}

// TestALabelDefaultsToTheEntityNameInTheResult pins the one projection
// every query has whether or not it asked for one. A document with no
// `project` at all still comes back with a label per node, because a
// renderer that has to fall back to `name` itself is a renderer that has
// to know what a node is. Its sibling in query_test.go pins the same
// default in the *document*; this is the half that says the default
// reaches the picture.
func TestALabelDefaultsToTheEntityNameInTheResult(t *testing.T) {
	g, _ := newGame(t)
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest","as":"quests"}]}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Nodes) != 3 {
		t.Fatalf("three quests are seeded, got %d", len(res.Nodes))
	}
	for _, n := range res.Nodes {
		if n.Attrs["label"] != n.Name {
			t.Errorf("the node %q must be labelled with its name %q, got %#v",
				n.Key, n.Name, n.Attrs["label"])
		}
	}
}

// TestAProjectedFieldAppearsInAttrsAndNotInFields is a read-back: it
// asserts the value came out of the database, not that the query ran.
//
// The second half is the token-discipline rule from the other side —
// projecting one field is not the same as including the payload, so
// Fields stays empty while attrs carries the one value the projection
// named.
func TestAProjectedFieldAppearsInAttrsAndNotInFields(t *testing.T) {
	g, _ := newGame(t)
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest","keys":["hogger"],"as":"q"}],
			"project":{"color_by":"min_level"}}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Nodes) != 1 {
		t.Fatalf("one quest was selected, got %d", len(res.Nodes))
	}
	node := res.Nodes[0]
	// A number stays a number: the attribute travels as jsonb, so 22 is
	// 22 and not "22". A renderer sizing or ordering by this value would
	// otherwise be comparing text.
	if got, ok := node.Attrs["color_by"].(float64); !ok || got != 22 {
		t.Fatalf("color_by must carry hogger's min_level as a number, got %#v",
			node.Attrs["color_by"])
	}
	if node.Fields != nil {
		t.Fatalf("include_fields was not asked for, so the payload must stay out: %#v",
			node.Fields)
	}
}

// TestAOneHopRelatedAttributeReadsTheFarEntity is the spec's own case:
// the colour is not a property of the quest, it is the name of the zone
// one hop away.
//
// The third quest is the load-bearing one. `cook` has no zone, and it
// gets **no attribute at all** rather than an empty string, so "this
// node has no zone" and "this node's zone is named the empty string"
// stay distinguishable to a renderer. It is also what says the join is a
// LEFT one: an inner join would drop the row entirely.
func TestAOneHopRelatedAttributeReadsTheFarEntity(t *testing.T) {
	g, _ := newGame(t)
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest","as":"q"}],
			"project":{"color_by":{"related":{"via":"takes_place_in","direction":"out",
			                                  "type":"zone","attr":"@name"}}}}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	byKey := nodesByKey(res.Nodes)
	if len(byKey) != 3 {
		t.Fatalf("three quests must come back, got %v", keysOf(res.Nodes))
	}
	for key, want := range map[string]string{
		"hogger": "Elwynn Forest",
		"defias": "Westfall",
	} {
		if got := byKey[key].Attrs["color_by"]; got != want {
			t.Errorf("%s is coloured by its zone %q, got %#v", key, want, got)
		}
	}
	cook, ok := byKey["cook"]
	if !ok {
		t.Fatalf("the quest with no zone must still be in the picture: %v", keysOf(res.Nodes))
	}
	if _, present := cook.Attrs["color_by"]; present {
		t.Fatalf("a quest with no zone must carry no color_by at all, not an empty one: %#v",
			cook.Attrs)
	}
	// And it still carries the attribute every node has, so the missing
	// one above is the hop and not a node whose attrs never arrived.
	if cook.Attrs["label"] != cook.Name {
		t.Fatalf("the zoneless quest must still be labelled: %#v", cook.Attrs)
	}
}

// TestAnAmbiguousHopIsMarkedRatherThanSilentlyPicked is why the hop is
// detected rather than assumed. A quest in two zones has no one zone
// colour, and silently painting it with the first would produce a map
// that is wrong in a way nobody can see.
//
// The single-zone quest in the same result is the control: it is what
// stops a flag that is always true from passing this test.
func TestAnAmbiguousHopIsMarkedRatherThanSilentlyPicked(t *testing.T) {
	g, _ := newGame(t)
	// hogger is now in Elwynn Forest *and* in Westfall. Alphabetically
	// first is Elwynn Forest, which is the one the picture uses.
	g.relate(t, "takes_place_in", "quest", "hogger", "zone", "westfall")
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest","as":"q"}],
			"project":{"color_by":{"related":{"via":"takes_place_in","direction":"out",
			                                  "type":"zone","attr":"@name"}}}}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// Three quests, not four: a hop that came back as two rows would put
	// hogger in the picture twice, count twice against max_nodes, and let
	// capOf's dedupe pick an arbitrary one of its two zones.
	if len(res.Nodes) != 3 {
		t.Fatalf("a quest in two zones is still one node, got %d: %v",
			len(res.Nodes), keysOf(res.Nodes))
	}
	byKey := nodesByKey(res.Nodes)
	hogger := byKey["hogger"]
	if got := hogger.Attrs["color_by"]; got != "Elwynn Forest" {
		t.Errorf("the first zone by name must be used, got %#v", got)
	}
	if !hogger.Ambiguous {
		t.Error("a quest in two zones must be marked ambiguous: a colour picked from two " +
			"candidates and not said so is a map that is wrong invisibly")
	}
	defias := byKey["defias"]
	if got := defias.Attrs["color_by"]; got != "Westfall" {
		t.Errorf("the single-zone quest must keep its zone, got %#v", got)
	}
	if defias.Ambiguous {
		t.Error("the quest with exactly one zone must not be marked ambiguous; this is the " +
			"control that stops an always-true flag passing")
	}
	if byKey["cook"].Ambiguous {
		t.Error("a quest with no zone at all is not ambiguous either")
	}
}

// TestIncludeFieldsReturnsTheWholePayloadAndTheDefaultDoesNot asserts the
// token-discipline rule both ways, because only the pair says anything: a
// run that always returned the payload passes the first half alone, and
// one that never returned it passes the second.
func TestIncludeFieldsReturnsTheWholePayloadAndTheDefaultDoesNot(t *testing.T) {
	g, _ := newGame(t)
	doc := `{"v":1,"from":[{"type":"quest","keys":["hogger"],"as":"q"}]}`
	lean, err := g.views.Run(t.Context(), g.projectID, RunRequest{Query: mustParse(t, doc)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(lean.Nodes) != 1 || lean.Nodes[0].Fields != nil {
		t.Fatalf("the default must carry no payload, got %#v", lean.Nodes)
	}
	full, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, doc), IncludeFields: true})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(full.Nodes) != 1 {
		t.Fatalf("one quest was selected, got %d", len(full.Nodes))
	}
	fields := full.Nodes[0].Fields
	if got, ok := fields["min_level"].(float64); !ok || got != 22 {
		t.Fatalf("include_fields must return the whole payload, got %#v", fields)
	}
	if fields["rank"] != "rare" {
		t.Fatalf("include_fields must return every declared field, got %#v", fields)
	}
}

// TestAProjectedFieldOfAnUndeclaredKeyIsRefusedAtResolution belongs to
// Task 4's pass and is asserted here because this is the task that makes
// a projection reachable at all. A colour source nothing declares is a
// typo, and a typo answered with a picture in one flat colour is the
// silent-empty failure this language refuses everywhere else.
func TestAProjectedFieldOfAnUndeclaredKeyIsRefusedAtResolution(t *testing.T) {
	g, _ := newGame(t)
	_, err := g.views.Resolve(t.Context(), g.projectID, mustParse(t,
		`{"v":1,"from":[{"type":"quest","as":"q"}],"project":{"color_by":"no_such_field"}}`))
	if err == nil {
		t.Fatal("a projected field no type in the query declares must be refused")
	}
	if !errors.Is(err, ErrQueryInvalid) {
		t.Fatalf("must be query_invalid, got %v", err)
	}
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("must be a *QueryError, got %T", err)
	}
	if len(qe.Fields) != 1 || qe.Fields[0].Path != "/project/color_by" {
		t.Fatalf("must be reported at the reference the caller wrote, got %v", qe.Fields)
	}
	// The control: the same query with a key the quest type declares
	// resolves, so the refusal above is about the key and not about
	// projections being refused wholesale.
	if _, err := g.views.Resolve(t.Context(), g.projectID, mustParse(t,
		`{"v":1,"from":[{"type":"quest","as":"q"}],"project":{"color_by":"min_level"}}`)); err != nil {
		t.Fatalf("a declared key must resolve: %v", err)
	}
}

// TestAProjectedFieldDeclaredOnOneOfSeveralTypesIsAllowed states the rule
// that separates a projection from a predicate, because the two read the
// same document syntax and mean different things.
//
// A predicate naming a field only one of the types in scope declares is
// refused: it would silently match nothing on the other. A projection has
// no such failure — a node whose type does not declare the key simply
// carries no attribute, which is the same "unset" a node with no value
// carries, and colouring quests by min_level while zones stay uncoloured
// is a picture a designer legitimately wants.
func TestAProjectedFieldDeclaredOnOneOfSeveralTypesIsAllowed(t *testing.T) {
	g, _ := newGame(t)
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest","as":"q"},{"type":"zone","as":"z"}],
			"project":{"color_by":"min_level"}}`)})
	if err != nil {
		t.Fatalf("min_level is declared on quest and not on zone, which a projection allows: %v",
			err)
	}
	byKey := nodesByKey(res.Nodes)
	if got, ok := byKey["hogger"].Attrs["color_by"].(float64); !ok || got != 22 {
		t.Fatalf("the type that declares the key must carry the value, got %#v",
			byKey["hogger"].Attrs)
	}
	if _, present := byKey["elwynn"].Attrs["color_by"]; present {
		t.Fatalf("the type that does not declare it must carry no attribute, got %#v",
			byKey["elwynn"].Attrs)
	}
}

// TestARelatedHopWithoutATypeReadsEveryNeighbour pins the optional half
// of the hop: `type` narrows the far side, and leaving it out is a hop
// over every entity the relation reaches rather than a refusal.
//
// The hop below writes neither `direction` nor `attr` either, so it also
// pins the two defaults applyDefaults fills for it — `out`, the direction
// a step and an edge entry default to, and @name, which is what "coloured
// by zone" means. Both are refusals in the compiler when they are empty,
// so a default that stopped being filled is an error rather than a hop
// answered backwards or reading nothing.
func TestARelatedHopWithoutATypeReadsEveryNeighbour(t *testing.T) {
	g, _ := newGame(t)
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest","keys":["defias"],"as":"q"}],
			"project":{"group_by":{"related":{"via":"requires"}}}}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Nodes) != 1 {
		t.Fatalf("one quest was selected, got %d", len(res.Nodes))
	}
	// defias requires hogger, and the hop's attr defaults to @name.
	if got := res.Nodes[0].Attrs["group_by"]; got != "Wanted: Hogger" {
		t.Fatalf("a hop with no type must still read its neighbour, got %#v", got)
	}
}

// TestARelatedHopReadsItsDirection is the arm a same-direction fixture
// cannot tell apart. takes_place_in runs quest -> zone, so the hop is
// answered `out` from the quest and answered by nothing `in`.
func TestARelatedHopReadsItsDirection(t *testing.T) {
	g, _ := newGame(t)
	run := func(direction string) Node {
		t.Helper()
		res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, `{"v":1,"from":[{"type":"quest","keys":["hogger"],"as":"q"}],
				"project":{"color_by":{"related":{"via":"takes_place_in","direction":"`+
				direction+`","type":"zone","attr":"@name"}}}}`)})
		if err != nil {
			t.Fatalf("run %s: %v", direction, err)
		}
		if len(res.Nodes) != 1 {
			t.Fatalf("one quest was selected, got %d", len(res.Nodes))
		}
		return res.Nodes[0]
	}
	if got := run("out").Attrs["color_by"]; got != "Elwynn Forest" {
		t.Fatalf("out must find the zone, got %#v", got)
	}
	if got, present := run("in").Attrs["color_by"]; present {
		t.Fatalf("in must find nothing: no zone takes place in a quest, got %#v", got)
	}
	if got := run("any").Attrs["color_by"]; got != "Elwynn Forest" {
		t.Fatalf("any must find the zone, got %#v", got)
	}
}

// TestAnEdgeLabelComesFromTheRelation fills the last field of the
// envelope that had nothing behind it. label_from names a field the
// relation type declares, or @type for the relation type's key.
func TestAnEdgeLabelComesFromTheRelation(t *testing.T) {
	g, _ := newGame(t)
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest","as":"q"}],
			"edges":[{"via":"requires","between":["q","q"],"label_from":"@type"}]}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Edges) != 1 {
		t.Fatalf("one requires relation is seeded, got %d", len(res.Edges))
	}
	if res.Edges[0].Label != "requires" {
		t.Fatalf("the edge must be labelled with its relation type, got %q", res.Edges[0].Label)
	}
	// The control: the same edge with no label_from carries no label, so
	// the assertion above is about label_from and not about a label that
	// is always there.
	plain, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest","as":"q"}],
			"edges":[{"via":"requires","between":["q","q"]}]}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(plain.Edges) != 1 || plain.Edges[0].Label != "" {
		t.Fatalf("an edge entry with no label_from must carry no label, got %#v", plain.Edges)
	}
}

// TestAnEdgeLabelOfAnUndeclaredKeyIsRefused is the same refusal the node
// side gets, at the position the caller wrote it.
func TestAnEdgeLabelOfAnUndeclaredKeyIsRefused(t *testing.T) {
	g, _ := newGame(t)
	_, err := g.views.Resolve(t.Context(), g.projectID, mustParse(t,
		`{"v":1,"from":[{"type":"quest","as":"q"}],
		 "edges":[{"via":"requires","between":["q","q"],"label_from":"no_such_field"}]}`))
	if err == nil {
		t.Fatal("an edge label naming a field the relation type does not declare must be refused")
	}
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("must be a *QueryError, got %T", err)
	}
	if len(qe.Fields) != 1 || qe.Fields[0].Path != "/edges/0/label_from" {
		t.Fatalf("must be reported at /edges/0/label_from, got %v", qe.Fields)
	}
	// A relation carries no name and no key, so those built-ins name no
	// column there and are refused as well — the same rule an edge
	// predicate follows.
	_, err = g.views.Resolve(t.Context(), g.projectID, mustParse(t,
		`{"v":1,"from":[{"type":"quest","as":"q"}],
		 "edges":[{"via":"requires","between":["q","q"],"label_from":"@name"}]}`))
	if err == nil {
		t.Fatal("@name must be refused on a relation")
	}
	if !strings.Contains(err.Error(), "cannot be compared on a relation") {
		t.Fatalf("must say why, got %v", err)
	}
}

// TestAProjectionSlotIsALateralJoinAheadOfItsNestedSelect asserts the
// emitted shape rather than the answer, because the answer cannot tell a
// LEFT join from an inner one that happened to match every row of this
// fixture, and because the project-filter guard's one known blind spot is
// a filter written *after* a nested SELECT in the same block. The three
// filters this join carries are all ahead of its own subquery, and
// TestEveryTableReferenceIsProjectFiltered is what watches them.
func TestAProjectionSlotIsALateralJoinAheadOfItsNestedSelect(t *testing.T) {
	g, _ := newGame(t)
	sql, _ := compileOf(t, g, `{"v":1,"from":[{"type":"quest","as":"q"}],
		"project":{"color_by":{"related":{"via":"takes_place_in","direction":"out",
		                                  "type":"zone","attr":"@name"}}}}`)
	if !strings.Contains(sql, "LEFT JOIN LATERAL") {
		t.Fatalf("a related attribute is a lateral join, so a node with no far entity keeps "+
			"its row:\n%s", sql)
	}
	// LIMIT 1 with the count taken over the whole match set, which is
	// **not** what this task's plan prescribed: a LIMIT 2 lateral returns
	// two rows and duplicates the node row. A window count is computed
	// before ORDER BY and LIMIT, so this is one row whose matches is the
	// true number of candidates — detected, and not capped at two either.
	if !strings.Contains(sql, "count(*) OVER () AS matches") {
		t.Fatalf("the ambiguity flag must be counted rather than inferred:\n%s", sql)
	}
	if !strings.Contains(sql, "LIMIT 1") {
		t.Fatalf("the hop must contribute exactly one row, or a quest in two zones becomes "+
			"two rows of one node:\n%s", sql)
	}
	if !strings.Contains(sql, "ORDER BY far.name, far.id") {
		t.Fatalf("the hop must be ordered, or which of two zones a quest is painted with "+
			"changes between runs:\n%s", sql)
	}
	problems, _ := projectFilterProblems(sql)
	for _, problem := range problems {
		t.Error(problem)
	}
}

// TestProjectFieldsCarriesExactlyTheKeysItNames is the middle setting
// between "no payload" and "the whole payload", and the one the spec's
// §5.5 recommends: a picture that needs one field per node should pay for
// one field per node.
//
// The two controls are what make it mean something: the key the document
// named is there, and the key it did not name is *not*, so a run that
// quietly returned everything fails.
func TestProjectFieldsCarriesExactlyTheKeysItNames(t *testing.T) {
	g, _ := newGame(t)
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest","keys":["hogger"],"as":"q"}],
			"project":{"fields":["min_level"]}}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Nodes) != 1 {
		t.Fatalf("one quest was selected, got %d", len(res.Nodes))
	}
	fields := res.Nodes[0].Fields
	if got, ok := fields["min_level"].(float64); !ok || got != 22 {
		t.Fatalf("the named key must be in the payload, got %#v", fields)
	}
	if _, present := fields["rank"]; present {
		t.Fatalf("a key the document did not name must stay out of the payload, got %#v", fields)
	}
	// And it is the payload, not an attribute: `fields` asks for content
	// under the game's own keys, and a slot asks for a presentation
	// attribute under the slot's name. A field called "color_by" would
	// otherwise overwrite the colour.
	if _, present := res.Nodes[0].Attrs["min_level"]; present {
		t.Fatalf("project.fields belongs to the payload, not to attrs: %#v", res.Nodes[0].Attrs)
	}
}

// TestAProjectedFieldsKeyOfAnUndeclaredKeyIsRefused is the same refusal
// the attribute slots get, at the entry the caller wrote.
func TestAProjectedFieldsKeyOfAnUndeclaredKeyIsRefused(t *testing.T) {
	g, _ := newGame(t)
	_, err := g.views.Resolve(t.Context(), g.projectID, mustParse(t,
		`{"v":1,"from":[{"type":"quest","as":"q"}],"project":{"fields":["min_level","nope"]}}`))
	if err == nil {
		t.Fatal("a payload key no type in the query declares must be refused")
	}
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("must be a *QueryError, got %T", err)
	}
	if len(qe.Fields) != 1 || qe.Fields[0].Path != "/project/fields/1" {
		t.Fatalf("must be reported at the entry the caller wrote, got %v", qe.Fields)
	}
}

// TestARelatedHopDoesNotColourWithAnInvalidEntity is the hop's half of
// the rule the rest of the compiler already follows: a picture that
// excludes rows the metamodel flagged as no longer fitting their schema
// should not be coloured by one either. The control is the same query
// with include_invalid, where the document has asked for them.
func TestARelatedHopDoesNotColourWithAnInvalidEntity(t *testing.T) {
	g, _ := newGame(t)
	if _, err := g.pool.Exec(t.Context(),
		`UPDATE entities SET invalid = true WHERE project_id = $1 AND key = 'elwynn'`,
		g.projectID); err != nil {
		t.Fatalf("flag the zone invalid: %v", err)
	}
	colour := func(doc string) any {
		t.Helper()
		res, err := g.views.Run(t.Context(), g.projectID, RunRequest{Query: mustParse(t, doc)})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(res.Nodes) != 1 {
			t.Fatalf("one quest was selected, got %d", len(res.Nodes))
		}
		return res.Nodes[0].Attrs["color_by"]
	}
	hop := `"project":{"color_by":{"related":{"via":"takes_place_in","direction":"out",
	                                          "type":"zone","attr":"@name"}}}`
	if got := colour(`{"v":1,"from":[{"type":"quest","keys":["hogger"],"as":"q"}],` +
		hop + `}`); got != nil {
		t.Fatalf("an invalid zone must not colour a quest, got %#v", got)
	}
	if got := colour(`{"v":1,"include_invalid":true,
		"from":[{"type":"quest","keys":["hogger"],"as":"q"}],` + hop + `}`); got != "Elwynn Forest" {
		t.Fatalf("include_invalid must lift the exclusion here too, got %#v", got)
	}
}

// TestAReciprocalPairIsOneFarEntityNotTwo is the shape the ambiguity flag
// shipped wrong: `direction: "any"` anchors on
// `(rel.source_id = e.id OR rel.target_id = e.id)`, so **a relation type
// declared in both directions between the same two entities matches
// twice** — two rows, one far entity. Counting rows, the node was flagged
// ambiguous with a single candidate to choose from.
//
// That is the flag lying, not being conservative: Node.Ambiguous's doc,
// this file's header and the plan all say the flag means *more than one
// entity was found*, and `any` is the natural spelling for a symmetric
// relation type — `connects_to` is one this project names itself. A flag
// that fires where there is nothing to resolve is one designers learn to
// ignore, which costs exactly what a flag that never fires costs.
//
// The two controls are in the test above: two distinct zones still report
// true, and one zone still reports false.
func TestAReciprocalPairIsOneFarEntityNotTwo(t *testing.T) {
	g, _ := newGame(t)
	// The fixture already carries `defias requires hogger`. The reverse
	// edge makes the pair reciprocal, which is legal — the unique index is
	// on (type, source, target) — and is one far entity seen twice.
	g.relate(t, "requires", "quest", "hogger", "quest", "defias")
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest","as":"q"}],
			"project":{"color_by":{"related":{"via":"requires","direction":"any",
			                                  "type":"quest","attr":"@name"}}}}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	byKey := nodesByKey(res.Nodes)
	hogger := byKey["hogger"]
	if got := hogger.Attrs["color_by"]; got != "The Defias Brotherhood" {
		t.Errorf("the one far quest must colour the node, got %#v", got)
	}
	if hogger.Ambiguous {
		t.Error("a reciprocal pair is two edges to one entity, and one entity is not a " +
			"choice: the flag says an entity was picked out of several, so it must be false")
	}
	if byKey["defias"].Ambiguous {
		t.Error("the other end of the same reciprocal pair is not ambiguous either")
	}
}
