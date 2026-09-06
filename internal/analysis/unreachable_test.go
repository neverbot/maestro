package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/metamodel"
)

// unreachable runs the report and fails the test on an error, which is
// what most of these want; the refusal tests call the service directly.
func (g game) unreachable(t *testing.T, in UnreachableInput) UnreachableResult {
	t.Helper()
	got, err := g.analysis.Unreachable(context.Background(), g.projectID, in)
	if err != nil {
		t.Fatalf("unreachable: %v", err)
	}
	return got
}

func (r UnreachableResult) keys() []string {
	out := make([]string, 0, len(r.Findings))
	for _, f := range r.Findings {
		out = append(out, f.Key)
	}
	return out
}

func (r UnreachableResult) find(t *testing.T, key string) Finding {
	t.Helper()
	for _, f := range r.Findings {
		if f.Key == key {
			return f
		}
	}
	t.Fatalf("no finding for %q; the report names %v", key, r.keys())
	return Finding{}
}

// TestAGameWhereEverythingIsReachableReportsZeroAndCountsThemAll is the
// negative half, and it is the load-bearing one.
//
// **An empty findings list is what a broken walk returns too**, so the
// assertion is not that the list is empty: it is that the list is empty
// *and* twelve entities were counted reachable *and* each type is named
// with its own count. A walk that started nowhere satisfies the first
// and fails the other two.
func TestAGameWhereEverythingIsReachableReportsZeroAndCountsThemAll(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	g.declareEntityType(t, "zone")
	// One ungated root, then a chain and a fan, twelve entities in all.
	g.entity(t, "zone", "start")
	for i := range 8 {
		g.entity(t, "quest", string(rune('a'+i)))
	}
	for _, key := range []string{"z1", "z2", "z3"} {
		g.entity(t, "zone", key)
	}
	g.edge(t, "unlocks", "quest", "a", "b")
	g.edge(t, "unlocks", "quest", "b", "c")
	g.edge(t, "unlocks", "quest", "c", "d")
	g.edge(t, "unlocks", "quest", "d", "e")
	g.edge(t, "unlocks", "quest", "e", "f")
	g.edge(t, "unlocks", "quest", "f", "g")
	g.edge(t, "unlocks", "quest", "g", "h")

	got := g.unreachable(t, UnreachableInput{})
	if len(got.Findings) != 0 {
		t.Errorf("a fully reachable game reported %v as unreachable", got.keys())
	}
	if got.ReachableTotal != 12 {
		t.Errorf("reachable_total = %d, want 12: an empty findings list over a walk that "+
			"reached nothing is the same JSON as a healthy game, and this count is what "+
			"separates them", got.ReachableTotal)
	}
	if got.UnreachableTotal != 0 {
		t.Errorf("unreachable_total = %d, want 0", got.UnreachableTotal)
	}
	perType := map[string]TypeCount{}
	for _, count := range got.PerType {
		perType[count.EntityType] = count
	}
	if perType["quest"].Reachable != 8 || perType["zone"].Reachable != 4 {
		t.Errorf("per_type = %v, want 8 quests and 4 zones, each with its own count", got.PerType)
	}
	if got.Seeds.Total == 0 {
		t.Error("the report names no seed at all, so the walk it rests on started nowhere")
	}
	if got.EdgesWalked != 7 {
		t.Errorf("edges_walked = %d, want the seven edges of the fixture", got.EdgesWalked)
	}
}

// TestUnreachableFindsTheOneQuestNothingUnlocks is the positive case.
func TestUnreachableFindsTheOneQuestNothingUnlocks(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	for _, key := range []string{"open", "locked", "orphaned-gate"} {
		g.entity(t, "quest", key)
	}
	// orphaned-gate is itself gated by locked, so neither is reachable.
	g.edge(t, "unlocks", "quest", "orphaned-gate", "locked")
	g.edge(t, "unlocks", "quest", "locked", "orphaned-gate")

	got := g.unreachable(t, UnreachableInput{})
	if len(got.Findings) != 2 {
		t.Fatalf("the report names %v, want the two entities of the mutual lock", got.keys())
	}
	finding := got.find(t, "locked")
	if finding.EntityType != "quest" {
		t.Errorf("the finding names entity type %q, want quest", finding.EntityType)
	}
	if finding.Reason != ReasonNoPath {
		t.Errorf("reason = %q, want %q", finding.Reason, ReasonNoPath)
	}
	if len(finding.Blockers) != 1 || finding.Blockers[0].Key != "orphaned-gate" {
		t.Errorf("blockers = %v, want the specific gating entity", finding.Blockers)
	}
	if got.ReachableTotal != 1 {
		t.Errorf("reachable_total = %d, want the one open quest", got.ReachableTotal)
	}
}

// TestTheReasonsAreOrderedMostSpecificFirst builds an entity that
// qualifies for two reasons and asserts it gets the more specific.
func TestTheReasonsAreOrderedMostSpecificFirst(t *testing.T) {
	t.Parallel()
	g := newGame(t)
	g.declareEntityType(t, "zone")
	g.declareEntityType(t, "quest")
	g.declareRelationType(t, "requires", "", []string{"prerequisite_of"})
	g.declareRelationType(t, "contains", "", []string{"containment"})
	g.entity(t, "zone", "sealed")
	g.entity(t, "zone", "gate-zone")
	g.entity(t, "quest", "inside")

	ctx := context.Background()
	// sealed is gated by gate-zone, which is gated by sealed: neither is
	// reachable. `inside` is reachable only through sealed.
	for _, edge := range [][2]string{{"sealed", "gate-zone"}, {"gate-zone", "sealed"}} {
		if _, err := g.meta.UpsertRelation(ctx, g.projectID, metamodel.RelationInput{
			TypeKey: "requires",
			Source:  metamodel.Ref{TypeKey: "zone", Key: edge[0]},
			Target:  metamodel.Ref{TypeKey: "zone", Key: edge[1]},
		}); err != nil {
			t.Fatalf("write %v: %v", edge, err)
		}
	}
	if _, err := g.meta.UpsertRelation(ctx, g.projectID, metamodel.RelationInput{
		TypeKey: "contains",
		Source:  metamodel.Ref{TypeKey: "zone", Key: "sealed"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "inside"},
	}); err != nil {
		t.Fatalf("write the containment edge: %v", err)
	}

	got := g.unreachable(t, UnreachableInput{})
	inside := got.find(t, "inside")
	if inside.Reason != ReasonContainerUnreachable {
		t.Errorf("reason = %q, want %q: an entity whose only way in is a container nobody "+
			"can reach gets the reason whose fix is the container",
			inside.Reason, ReasonContainerUnreachable)
	}
	// The control: the two zones, which have gates and no container, get
	// the less specific reason, so the ordering is doing work.
	if sealed := got.find(t, "sealed"); sealed.Reason != ReasonNoPath {
		t.Errorf("the gated zone's reason is %q, want %q", sealed.Reason, ReasonNoPath)
	}
}

// TestAnIsolatedEntityIsNamedAsIsolatedAndOnlyWithUngatedOff pins the
// reason whose own doc comment says it cannot appear by default.
func TestAnIsolatedEntityIsNamedAsIsolatedAndOnlyWithUngatedOff(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	g.entity(t, "quest", "seed")
	g.entity(t, "quest", "floating")

	byDefault := g.unreachable(t, UnreachableInput{})
	if len(byDefault.Findings) != 0 {
		t.Fatalf("with include_ungated on, an entity with no gates is a start point, so "+
			"nothing should be unreachable; got %v", byDefault.keys())
	}
	off := g.unreachable(t, UnreachableInput{Reach: Params{
		SeedEntities:   []SeedRef{{EntityType: "quest", Key: "seed"}},
		IncludeUngated: boolPtr(false),
	}})
	if got := off.find(t, "floating").Reason; got != ReasonIsolatedFromStart {
		t.Errorf("reason = %q, want %q", got, ReasonIsolatedFromStart)
	}
}

// TestTheSameGameReportsFewerUnreachableUnderAnyThanUnderAll is O1's
// asymmetry, asserted as a **direction** rather than as two absolute
// numbers, on a fixture with one alternative route.
func TestTheSameGameReportsFewerUnreachableUnderAnyThanUnderAll(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	for _, key := range []string{"open", "blocked", "target"} {
		g.entity(t, "quest", key)
	}
	// target has two gates: `open`, which is reachable, and `blocked`,
	// which is not. Under `any` the alternative route is enough.
	g.edge(t, "unlocks", "quest", "open", "target")
	g.edge(t, "requires", "quest", "target", "blocked")
	g.edge(t, "requires", "quest", "blocked", "target")

	any := g.unreachable(t, UnreachableInput{})
	all := g.unreachable(t, UnreachableInput{Reach: Params{Gating: GatingAll}})
	if len(any.Findings) >= len(all.Findings) {
		t.Errorf("`any` reported %v and `all` reported %v; the stricter reading must not "+
			"report fewer", any.keys(), all.keys())
	}
	if len(any.Findings) == 0 && len(all.Findings) == 0 {
		t.Fatal("neither reading reported anything, so the comparison above is vacuous")
	}
}

// TestIgnoredEntityTypesAreNeitherReportedNorCounted holds the totals to
// the report: a total that counts what the report excludes is a total
// that disagrees with itself.
func TestIgnoredEntityTypesAreNeitherReportedNorCounted(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	g.declareEntityType(t, "lore")
	g.entity(t, "quest", "seed")
	g.entity(t, "lore", "codex")
	g.entity(t, "lore", "legend")

	full := g.unreachable(t, UnreachableInput{Reach: Params{
		SeedEntities:   []SeedRef{{EntityType: "quest", Key: "seed"}},
		IncludeUngated: boolPtr(false),
	}})
	if len(full.Findings) != 2 {
		t.Fatalf("without the filter the two lore rows must be unreachable; got %v", full.keys())
	}

	ignored := g.unreachable(t, UnreachableInput{
		Reach: Params{
			SeedEntities:   []SeedRef{{EntityType: "quest", Key: "seed"}},
			IncludeUngated: boolPtr(false),
		},
		IgnoreEntityTypes: []string{"lore"},
	})
	if len(ignored.Findings) != 0 {
		t.Errorf("an ignored type is still reported: %v", ignored.keys())
	}
	if ignored.UnreachableTotal != 0 {
		t.Errorf("unreachable_total = %d over an ignored type", ignored.UnreachableTotal)
	}
	for _, count := range ignored.PerType {
		if count.EntityType == "lore" {
			t.Errorf("per_type still names the ignored type: %v", ignored.PerType)
		}
	}
	if ignored.ReachableTotal != 1 {
		t.Errorf("reachable_total = %d, want the one seeded quest: the totals must count "+
			"exactly what the report considers", ignored.ReachableTotal)
	}
}

// blockedGame seeds five unreachable quests behind one lock, for the
// truncation tests.
func blockedGame(t *testing.T) game {
	t.Helper()
	g := gatedGame(t)
	g.entity(t, "quest", "seed")
	g.entity(t, "quest", "lock")
	g.edge(t, "requires", "quest", "lock", "lock")
	for _, key := range []string{"u1", "u2", "u3", "u4", "u5"} {
		g.entity(t, "quest", key)
		g.edge(t, "requires", "quest", key, "lock")
	}
	return g
}

// TestATruncatedUnreachableReportSaysItIsTruncated has both halves in
// one test, because exactly-at-cap and truncated-at-cap are the same
// answer without them.
func TestATruncatedUnreachableReportSaysItIsTruncated(t *testing.T) {
	t.Parallel()
	g := blockedGame(t)

	capped := g.unreachable(t, UnreachableInput{MaxResults: 2})
	if len(capped.Findings) != 2 {
		t.Fatalf("max_results 2 returned %d findings", len(capped.Findings))
	}
	if !capped.Truncated {
		t.Error("a report capped below the number of findings does not say it is truncated")
	}
	if capped.UnreachableTotal != 6 {
		t.Errorf("unreachable_total = %d, want all six even though only two are named: a "+
			"truncated report that also truncated its totals says nothing about what it "+
			"left out", capped.UnreachableTotal)
	}

	whole := g.unreachable(t, UnreachableInput{MaxResults: 6})
	if len(whole.Findings) != 6 {
		t.Fatalf("max_results 6 returned %d findings, want all six", len(whole.Findings))
	}
	if whole.Truncated {
		t.Error("a report that named every finding says it is truncated")
	}
}

// TestADepthLimitedReportSaysSo pins that a walk which stopped looking
// does not claim the entities past its bound are unreachable by design.
func TestADepthLimitedReportSaysSo(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	for _, key := range []string{"a", "b", "c", "d"} {
		g.entity(t, "quest", key)
	}
	g.edge(t, "unlocks", "quest", "a", "b")
	g.edge(t, "unlocks", "quest", "b", "c")
	g.edge(t, "unlocks", "quest", "c", "d")

	got := g.unreachable(t, UnreachableInput{Reach: Params{MaxDepth: 2}})
	if !got.DepthLimited {
		t.Error("the report does not say the walk stopped at its depth bound")
	}
	finding := got.find(t, "d")
	if finding.Reason != ReasonDepthLimited {
		t.Errorf("reason = %q, want %q: the walk stopped looking, which is not the same "+
			"statement as `no player can reach this`", finding.Reason, ReasonDepthLimited)
	}
	// The control: at a bound the chain fits under, nothing is
	// unreachable and nothing claims to be depth-limited.
	whole := g.unreachable(t, UnreachableInput{Reach: Params{MaxDepth: 10}})
	if whole.DepthLimited || len(whole.Findings) != 0 {
		t.Errorf("with room to finish, the report says depth_limited=%t and names %v",
			whole.DepthLimited, whole.keys())
	}
}

// TestAnAnalysisOverAGameWithNoTraitsRefusesRatherThanReportingHealth is
// the whole sub-project's most important negative test, and it runs
// through Unreachable rather than through the resolver, because the
// resolver being right is worth nothing if the analysis does not call it.
func TestAnAnalysisOverAGameWithNoTraitsRefusesRatherThanReportingHealth(t *testing.T) {
	t.Parallel()
	g := newGame(t)
	g.declareEntityType(t, "quest")
	g.declareRelationType(t, "relates_to", "", nil)
	g.entity(t, "quest", "a")
	g.entity(t, "quest", "b")
	g.edge(t, "relates_to", "quest", "a", "b")

	_, err := g.analysis.Unreachable(context.Background(), g.projectID, UnreachableInput{})
	if !errors.Is(err, ErrSemanticsUndeclared) {
		t.Fatalf("err = %v, want semantics_undeclared: a game that declared nothing must "+
			"be refused, never reported healthy", err)
	}
	var undeclared *UndeclaredError
	if !errors.As(err, &undeclared) {
		t.Fatalf("the refusal does not carry the catalogue: %v", err)
	}
	if len(undeclared.Types) != 1 || undeclared.Types[0].Key != "relates_to" {
		t.Errorf("the refusal names %v, want the game's one relation type", undeclared.Types)
	}
	// It names what is missing rather than that something is: the
	// recovery is in the message and the payload, not left to guesswork.
	for _, word := range []string{"analysis_traits", "semantic_role", "relates_to"} {
		if !strings.Contains(err.Error(), word) {
			t.Errorf("the refusal does not name %q: %v", word, err)
		}
	}
}

// TestAnUnreachableReportOfAnotherGameSeesNoneOfThisGamesContent is the
// isolation case. The token half -- an admin's token scoped to another
// game -- belongs to the surface and is Task 11's; this is the half the
// domain can assert, and every statement above filters in SQL.
func TestAnUnreachableReportOfAnotherGameSeesNoneOfThisGamesContent(t *testing.T) {
	t.Parallel()
	mine := blockedGame(t)
	theirs := mine.sibling(t)
	theirs.declareEntityType(t, "quest")
	theirs.declareRelationType(t, "requires", "", []string{"prerequisite_of"})
	theirs.entity(t, "quest", "theirs-open")

	got, err := theirs.analysis.Unreachable(context.Background(), theirs.projectID,
		UnreachableInput{})
	if err != nil {
		t.Fatalf("unreachable over the second game: %v", err)
	}
	if got.ReachableTotal != 1 || got.UnreachableTotal != 0 {
		t.Errorf("the second game's report counted %d reachable and %d unreachable; it holds "+
			"one entity and must see none of the first game's seven",
			got.ReachableTotal, got.UnreachableTotal)
	}
	// The control: the first game's own report still finds its six, so
	// the emptiness above is isolation and not a broken fixture.
	if mineReport := mine.unreachable(t, UnreachableInput{}); mineReport.UnreachableTotal != 6 {
		t.Fatalf("the first game reports %d unreachable, want 6", mineReport.UnreachableTotal)
	}
}

// TestADeclaredBoundAboveItsCapIsRefusedAndAPageSizeIsClamped holds the
// two halves of the refuse/clamp split in one test, so a later tidy
// cannot collapse them into one policy.
func TestADeclaredBoundAboveItsCapIsRefusedAndAPageSizeIsClamped(t *testing.T) {
	t.Parallel()
	g := blockedGame(t)
	ctx := context.Background()

	_, err := g.analysis.Unreachable(ctx, g.projectID,
		UnreachableInput{MaxResults: MaxMaxResults + 1})
	if !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("err = %v, want limit_exceeded for a declared bound above its cap", err)
	}
	if !strings.Contains(err.Error(), "max_results") {
		t.Errorf("the refusal does not name the argument at fault: %v", err)
	}

	// A page size above its cap is clamped and answers, because a page
	// is explicitly one slice of an answer whose remainder the cursor
	// promises.
	clamped := g.unreachable(t, UnreachableInput{Limit: maxUnreachablePage + 1000})
	if len(clamped.Findings) != 6 {
		t.Errorf("the clamped page returned %d findings, want the six the game has",
			len(clamped.Findings))
	}
}

// TestACursorFromAnotherGameIsRefusedByTheUnreachableListing is the
// behavioural half of the fingerprint guard.
func TestACursorFromAnotherGameIsRefusedByTheUnreachableListing(t *testing.T) {
	t.Parallel()
	mine := blockedGame(t)
	theirs := mine.sibling(t)
	theirs.declareEntityType(t, "quest")
	theirs.declareRelationType(t, "requires", "", []string{"prerequisite_of"})
	theirs.entity(t, "quest", "a")
	theirs.entity(t, "quest", "b")
	theirs.entity(t, "quest", "c")
	theirs.edge(t, "requires", "quest", "b", "b")
	theirs.edge(t, "requires", "quest", "c", "b")

	page := theirs.unreachable(t, UnreachableInput{Limit: 1})
	if page.NextCursor == "" {
		t.Fatal("the second game's first page issued no cursor, so nothing below is tested")
	}
	_, err := mine.analysis.Unreachable(context.Background(), mine.projectID,
		UnreachableInput{Limit: 1, Cursor: page.NextCursor})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want invalid_input for a cursor from another game", err)
	}
}

// TestTheUnreachableFingerprintIsProjectIdFirstAndCarriesItsParameters
// is the compositional half, and it is a separate test because the
// behavioural one can pass without the project id whenever another part
// discriminates -- which is exactly how the original paging defect
// survived its first test.
func TestTheUnreachableFingerprintIsProjectIdFirstAndCarriesItsParameters(t *testing.T) {
	t.Parallel()
	source := readSourceFile(t, "unreachable.go")
	if !strings.Contains(source, `parts := []string{projectID.String(), "analysis.unreachable"}`) {
		t.Error("the fingerprint no longer begins with the project id and this listing's " +
			"own discriminator; internal/paging's contract is that the project id is first, " +
			"always, and nothing enforces it but this")
	}

	g := blockedGame(t)
	base := UnreachableInput{}
	other := UnreachableInput{Reach: Params{Gating: GatingAll}}
	if unreachableFingerprint(g.projectID, base, nil) ==
		unreachableFingerprint(g.projectID, other, nil) {
		t.Error("two parameter sets share a fingerprint, so paging through one would " +
			"answer from the other")
	}
	sibling := g.sibling(t)
	if unreachableFingerprint(g.projectID, base, nil) ==
		unreachableFingerprint(sibling.projectID, base, nil) {
		t.Error("two games share a fingerprint for the same parameters")
	}
}

// TestACursorIssuedForOneParameterSetIsRefusedAgainstAnother is the
// wrong answer with no error this fingerprint exists to prevent.
func TestACursorIssuedForOneParameterSetIsRefusedAgainstAnother(t *testing.T) {
	t.Parallel()
	g := blockedGame(t)
	page := g.unreachable(t, UnreachableInput{Limit: 1})
	if page.NextCursor == "" {
		t.Fatal("the first page issued no cursor")
	}
	// The control: the same cursor against the same parameters pages on.
	second := g.unreachable(t, UnreachableInput{Limit: 1, Cursor: page.NextCursor})
	if len(second.Findings) != 1 || second.Findings[0].Key == page.Findings[0].Key {
		t.Fatalf("the cursor did not advance: page 1 %v, page 2 %v",
			page.keys(), second.keys())
	}
	_, err := g.analysis.Unreachable(context.Background(), g.projectID, UnreachableInput{
		Limit: 1, Cursor: page.NextCursor, Reach: Params{Gating: GatingAll},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want invalid_input: paging through one question and having "+
			"page 2 answer another is a wrong answer with no error", err)
	}
}

// TestAnUnreachableReportSerialisesWithItsDocumentedWireNames is the
// read-back this task can perform. The MCP tool and the REST mirror are
// Task 11's, and it owes the end-to-end half; what is asserted here is
// that the document those surfaces will publish carries the names the
// spec documents, because a field renamed in Go is a field renamed on
// the wire.
func TestAnUnreachableReportSerialisesWithItsDocumentedWireNames(t *testing.T) {
	t.Parallel()
	g := blockedGame(t)
	raw, err := json.Marshal(g.unreachable(t, UnreachableInput{}))
	if err != nil {
		t.Fatalf("marshal the report: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode the report: %v", err)
	}
	for _, name := range []string{
		"unreachable", "reachable_total", "unreachable_total", "per_type", "seeds",
		"semantics_source", "gating", "invalid_edges_followed", "edges_walked",
		"truncated", "depth_limited", "stats",
	} {
		if _, ok := document[name]; !ok {
			t.Errorf("the report has no member %q; the keys are %v", name, keysOf(document))
		}
	}
	findings, _ := document["unreachable"].([]any)
	if len(findings) == 0 {
		t.Fatal("the fixture produced no finding, so the finding's own names are unasserted")
	}
	first, _ := findings[0].(map[string]any)
	for _, name := range []string{"entity_type", "key", "name", "reason", "blockers"} {
		if _, ok := first[name]; !ok {
			t.Errorf("a finding has no member %q; the keys are %v", name, keysOf(first))
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
