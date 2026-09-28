package analysis

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/paging"
)

// orphans runs the report and fails the test on an error; the refusal
// tests call the service directly.
func (g game) orphans(t *testing.T, in OrphansInput) OrphansResult {
	t.Helper()
	got, err := g.analysis.Orphans(context.Background(), g.projectID, in)
	assert.Must(t, err == nil, "orphans: %v", err)
	return got
}

func (r OrphansResult) keys() []string {
	out := make([]string, 0, len(r.Findings))
	for _, finding := range r.Findings {
		out = append(out, finding.Key)
	}
	return out
}

func (r OrphansResult) find(t *testing.T, key string) Orphan {
	t.Helper()
	for _, finding := range r.Findings {
		if finding.Key == key {
			return finding
		}
	}
	t.Fatalf("no orphan finding for %q; the report names %v", key, r.keys())
	return Orphan{}
}

func TestOrphans(t *testing.T) {
	t.Parallel()
	a := newArea(t)

	// TestAGameWithNoOrphansStillCountsEveryEntityItConsidered is the
	// negative half.
	t.Run("a game with no orphans still counts every entity it considered", func(t *testing.T) {
		g := a.gated(t)
		keys := []string{"a", "b", "c", "d", "e"}
		for _, key := range keys {
			g.entity(t, "quest", key)
		}
		for i := range keys[:len(keys)-1] {
			g.edge(t, "unlocks", "quest", keys[i], keys[i+1])
		}

		got := g.orphans(t, OrphansInput{})
		assert.Should(t, len(got.Findings) == 0, "a game where every entity has an edge reported %v as orphaned", got.keys())
		assert.Should(t, got.ConsideredTotal == int64(len(keys)), "considered_total = %d, want %d: an empty findings list over a run that "+
			"considered nothing is the same JSON as a game with no orphans, and this "+
			"count is what separates them", got.ConsideredTotal, len(keys))
		assert.Should(t, got.Mode == OrphanIsolated, "mode = %q, want the default %q", got.Mode, OrphanIsolated)
	})

	// TestAnEntityWithOnlyAnAnnotationEdgeIsAnOrphan and its other half, in
	// **one fixture**, so the only difference between the two entities is
	// the declaration on the type that joins them.
	t.Run("an entity with only an annotation edge is an orphan and one with a real edge is not", func(t *testing.T) {
		g := a.gated(t)
		g.declareRelationType(t, "is_illustrated_by", "", []string{"annotation"})
		for _, key := range []string{"decorated", "illustration", "connected", "target"} {
			g.entity(t, "quest", key)
		}
		g.edge(t, "is_illustrated_by", "quest", "decorated", "illustration")
		g.edge(t, "unlocks", "quest", "connected", "target")

		got := g.orphans(t, OrphansInput{})
		assert.Should(t, contains(got.keys(), "decorated"), "the report names %v; an entity whose only edge is of an annotation "+
			"type is an orphan, which is what the trait is for", got.keys())
		assert.Should(t, !contains(got.keys(), "connected") && !contains(got.keys(), "target"), "the report names %v; an entity with a real edge is not an orphan",
			got.keys())
		assert.Should(t, len(got.ExcludedRelationTypes) == 1 && got.ExcludedRelationTypes[0] == "is_illustrated_by", "excluded_relation_types = %v, want the one annotation type: a verdict "+
			"that rests on an exclusion has to name it", got.ExcludedRelationTypes)
		// The degrees are over the counted types, so the decorated entity
		// reports zero even though it has an edge in the database.
		if finding := got.find(t, "decorated"); finding.OutDegree != 0 {
			t.Errorf("out_degree = %d for an entity whose only edge is not counted",
				finding.OutDegree)
		}
	})

	// TestOrphansOverAGameWithNoTraitsAnswersRatherThanRefusing.
	t.Run("orphans over a game with no traits answers rather than refusing", func(t *testing.T) {
		g := a.game(t)
		g.declareEntityType(t, "quest")
		g.declareRelationType(t, "mentions", "", nil)
		g.entity(t, "quest", "lonely")

		got := g.orphans(t, OrphansInput{})
		assert.Must(t, len(got.Findings) == 1 && got.Findings[0].Key == "lonely", "the report names %v, want the one entity with no edges", got.keys())
		assert.Should(t, len(got.ExcludedRelationTypes) == 0, "excluded_relation_types = %v over a game that declared nothing: no "+
			"type was called decoration, so none was excluded", got.ExcludedRelationTypes)
		assert.Should(t, len(got.SemanticsSource) == 0, "semantics_source = %v over a game that declared nothing", got.SemanticsSource)

		// The control: the three analyses that walk edges do refuse it.
		if _, err := g.analysis.Cycles(context.Background(), g.projectID, CyclesInput{}); err == nil {
			t.Fatal("cycles answered over the same undeclared game, so the asymmetry this " +
				"test is about no longer exists and the first half proves nothing")
		}
	})

	// TestAnEntityWhoseOnlyEdgeIsInvalidIsNotAnOrphan is the invalid-edge
	// decision, at the site the plan named as where it is most likely to be
	// forgotten.
	t.Run("an entity whose only edge is invalid is not an orphan", func(t *testing.T) {
		g := a.gated(t)
		g.entity(t, "quest", "flagged-source")
		g.entity(t, "quest", "flagged-target")
		g.entity(t, "quest", "truly-alone")
		g.edge(t, "unlocks", "quest", "flagged-source", "flagged-target")
		// The product's own way of invalidating an edge: a required field
		// added to its type, so the stored row stops fitting its declaration.
		g.invalidate(t, "unlocks")

		got := g.orphans(t, OrphansInput{})
		assert.Should(t, !contains(got.keys(), "flagged-source") && !contains(got.keys(), "flagged-target"), "the report names %v; an entity whose only edge is flagged invalid has "+
			"an edge, and reporting it as an orphan sends a designer to delete content "+
			"that is connected", got.keys())
		// The control, so a report that found nothing at all cannot pass.
		assert.Should(t, contains(got.keys(), "truly-alone"), "the report names %v and not the entity with no edges at all", got.keys())
		if finding := got.find(t, "truly-alone"); finding.InDegree != 0 || finding.OutDegree != 0 {
			t.Errorf("degrees = %d/%d for an entity with no edges", finding.InDegree, finding.OutDegree)
		}
	})

	// TestSinkModeFindsOnlyIncomingAndSourceModeOnlyOutgoing runs one
	// fixture through both modes and asserts the two answers are **disjoint
	// and both non-empty**, which no single-mode test can establish.
	t.Run("sink mode finds only incoming and source mode only outgoing", func(t *testing.T) {
		g := a.gated(t)
		for _, key := range []string{"head", "tail", "middle-in", "middle-out", "alone"} {
			g.entity(t, "quest", key)
		}
		// head -> tail: head is a source, tail is a sink.
		g.edge(t, "unlocks", "quest", "head", "tail")
		// middle-out -> middle-in -> ... nothing, so middle-in is a sink too
		// and middle-out a source too.
		g.edge(t, "unlocks", "quest", "middle-out", "middle-in")

		sinks := g.orphans(t, OrphansInput{Mode: OrphanSink})
		sources := g.orphans(t, OrphansInput{Mode: OrphanSource})
		assert.Must(t, len(sinks.Findings) != 0 && len(sources.Findings) != 0, "sinks = %v, sources = %v; both halves must be non-empty or the "+
			"disjointness below is vacuous", sinks.keys(), sources.keys())
		for _, key := range sinks.keys() {
			assert.Should(t, !contains(sources.keys(), key), "%q is reported as both a sink and a source: the two modes are "+
				"defined as one direction each and cannot overlap", key)
		}
		for _, finding := range sinks.Findings {
			assert.Should(t, finding.InDegree != 0 && finding.OutDegree == 0, "sink %q has degrees %d/%d, want incoming only",
				finding.Key, finding.InDegree, finding.OutDegree)
		}
		for _, finding := range sources.Findings {
			assert.Should(t, finding.OutDegree != 0 && finding.InDegree == 0, "source %q has degrees %d/%d, want outgoing only",
				finding.Key, finding.InDegree, finding.OutDegree)
		}
		// And the entity with no edges at all is in neither, which is what
		// makes `isolated` a third mode rather than the union of these two.
		assert.Should(t, !contains(sinks.keys(), "alone") && !contains(sources.keys(), "alone"), "the isolated entity was reported by a directional mode")
		assert.Should(t, contains(g.orphans(t, OrphansInput{}).keys(), "alone"), "isolated mode did not report the entity with no edges at all")
	})

	// TestTheDegreesReportedMatchTheEdgesInTheDatabase reads the counts back
	// against a hand-counted fixture.
	t.Run("the degrees reported match the edges in the database", func(t *testing.T) {
		g := a.gated(t)
		for _, key := range []string{"hub", "in-1", "in-2", "in-3", "out-1"} {
			g.entity(t, "quest", key)
		}
		g.edge(t, "unlocks", "quest", "in-1", "hub")
		g.edge(t, "unlocks", "quest", "in-2", "hub")
		g.edge(t, "requires", "quest", "in-3", "hub")
		g.edge(t, "unlocks", "quest", "hub", "out-1")

		// hub has three in and one out, so it is neither a sink nor a
		// source; in-1 and in-2 are sources; out-1 is a sink.
		sinks := g.orphans(t, OrphansInput{Mode: OrphanSink})
		out1 := sinks.find(t, "out-1")
		assert.Should(t, out1.InDegree == 1 && out1.OutDegree == 0, "out-1 degrees = %d/%d, want 1/0", out1.InDegree, out1.OutDegree)
		sources := g.orphans(t, OrphansInput{Mode: OrphanSource})
		for _, key := range []string{"in-1", "in-2", "in-3"} {
			finding := sources.find(t, key)
			assert.Should(t, finding.OutDegree == 1 && finding.InDegree == 0, "%s degrees = %d/%d, want 0/1", key, finding.InDegree, finding.OutDegree)
		}
		assert.Should(t, !contains(sinks.keys(), "hub") && !contains(sources.keys(), "hub"), "the hub, with three incoming and one outgoing edge, was reported: "+
			"sinks %v, sources %v", sinks.keys(), sources.keys())
	})

	// TestOrphansAreNotReportedForIgnoredTypes, with the totals excluding
	// them too: a total that counts what the report excludes is a total that
	// disagrees with itself.
	t.Run("orphans are not reported for ignored types", func(t *testing.T) {
		g := a.gated(t)
		g.declareEntityType(t, "note")
		g.entity(t, "quest", "lonely-quest")
		g.entity(t, "note", "lonely-note-1")
		g.entity(t, "note", "lonely-note-2")

		all := g.orphans(t, OrphansInput{})
		assert.Must(t, len(all.Findings) == 3 && all.ConsideredTotal == 3, "the unfiltered report names %v over %d considered, want all three",
			all.keys(), all.ConsideredTotal)
		got := g.orphans(t, OrphansInput{IgnoreEntityTypes: []string{"note"}})
		assert.Should(t, len(got.Findings) == 1 && got.Findings[0].Key == "lonely-quest", "the report names %v, want only the quest", got.keys())
		assert.Should(t, got.ConsideredTotal == 1, "considered_total = %d, want 1: an ignored type is removed from the "+
			"totals as well as from the findings", got.ConsideredTotal)
		// And the same through the positive filter, which is the other way a
		// caller narrows the same report.
		narrowed := g.orphans(t, OrphansInput{EntityTypes: []string{"note"}})
		assert.Should(t, len(narrowed.Findings) == 2 && narrowed.ConsideredTotal == 2, "entity_types narrowed to note names %v over %d considered",
			narrowed.keys(), narrowed.ConsideredTotal)
	})

	// TestAnOrphanCursorFromAnotherGameIsRefused is the behavioural half of
	// the fingerprint contract: a position issued by one game's listing
	// means nothing in another's, and pages it perfectly if nothing refuses
	// it.
	t.Run("an orphan cursor from another game is refused", func(t *testing.T) {
		mine := a.gated(t)
		for _, key := range []string{"m1", "m2", "m3"} {
			mine.entity(t, "quest", key)
		}
		first := mine.orphans(t, OrphansInput{Limit: 1})
		assert.Must(t, first.NextCursor != "", "the first page issued no cursor, so there is nothing to carry")

		theirs := mine.sibling(t)
		theirs.declareEntityType(t, "quest")
		theirs.declareRelationType(t, "unlocks", "", []string{"unlocks"})
		for _, key := range []string{"t1", "t2", "t3"} {
			theirs.entity(t, "quest", key)
		}
		_, err := theirs.analysis.Orphans(context.Background(), theirs.projectID,
			OrphansInput{Limit: 1, Cursor: first.NextCursor})
		var invalid *metamodel.ValidationError
		assert.Must(t, errors.As(err, &invalid) && invalid.Code == CodeInvalidInput, "another game's cursor answered %v, want invalid_input", err)
		assert.Should(t, len(invalid.Fields) == 1 && invalid.Fields[0].Path == "cursor", "the refusal names %v, want the caller's own `cursor` argument",
			invalid.Fields)
	})

	// TestTheOrphanFingerprintIsProjectIdFirstAndCarriesItsMode is the
	// compositional half.
	t.Run("the orphan fingerprint is project id first and carries its mode", func(t *testing.T) {
		g := a.game(t)
		want := paging.Fingerprint(g.projectID.String(), "analysis.orphans", "sink", "quest")
		if got := orphanFingerprint(g.projectID, OrphanSink, []string{"quest"}); got != want {
			t.Fatalf("orphanFingerprint = %q, want %q: the project id is first and the mode "+
				"follows the domain, which is internal/paging's contract", got, want)
		}
		// The mode is load-bearing on its own: paging through the isolated
		// entities and having page 2 answer with the sinks is a wrong answer
		// with no error.
		assert.Must(t, orphanFingerprint(g.projectID, OrphanSink, nil) !=
			orphanFingerprint(g.projectID, OrphanSource, nil), "two modes share a fingerprint, so a cursor from one pages the other")
		assert.Must(t, orphanFingerprint(g.projectID, OrphanSink, nil) !=
			orphanFingerprint(g.sibling(t).projectID, OrphanSink, nil), "two games share a fingerprint over an unfiltered listing")
	})

	// TestAnOrphanPageWalksEveryEntityExactlyOnce -- the cursor is a
	// position, and a keyset that disagrees with its own sort order skips or
	// repeats rows at a page boundary and says nothing.
	t.Run("an orphan page walks every entity exactly once", func(t *testing.T) {
		g := a.gated(t)
		want := map[string]bool{}
		for i := range 7 {
			key := "lonely-" + string(rune('a'+i))
			g.entity(t, "quest", key)
			want[key] = true
		}

		seen := map[string]int{}
		cursor := ""
		for pages := 0; pages < 10; pages++ {
			page := g.orphans(t, OrphansInput{Limit: 3, Cursor: cursor})
			for _, finding := range page.Findings {
				seen[finding.Key]++
			}
			cursor = page.NextCursor
			if cursor == "" {
				break
			}
		}
		assert.Must(t, len(seen) == len(want), "the walk saw %v, want the seven entities", seen)
		for key, count := range seen {
			assert.Should(t, count == 1 && want[key], "%q was seen %d times", key, count)
		}
	})

	// TestAnUnknownOrphanModeIsRefusedRatherThanTreatedAsIsolated.
	t.Run("an unknown orphan mode is refused rather than treated as isolated", func(t *testing.T) {
		g := a.gated(t)
		g.entity(t, "quest", "alone")
		_, err := g.analysis.Orphans(context.Background(), g.projectID,
			OrphansInput{Mode: OrphanMode("dangling")})
		var invalid *metamodel.ValidationError
		assert.Must(t, errors.As(err, &invalid) && invalid.Code == CodeInvalidInput, "an unknown mode answered %v, want invalid_input", err)
		assert.Must(t, len(invalid.Fields) == 1 && invalid.Fields[0].Path == "mode", "the refusal names %v, want the caller's own `mode` argument", invalid.Fields)
		for _, known := range OrphanModes {
			if !strings.Contains(invalid.Fields[0].Message, string(known)) {
				t.Errorf("the refusal does not name %q, so it says something is wrong "+
					"without saying what would be right: %s", known, invalid.Fields[0].Message)
			}
		}
	})

	// TestOrphansOfAnotherGameAreNotReported -- the isolation half, over one
	// database holding both games, with a control so a report that found
	// nothing at all cannot pass.
	t.Run("orphans of another game are not reported", func(t *testing.T) {
		mine := a.gated(t)
		mine.entity(t, "quest", "my-lonely-quest")

		theirs := mine.sibling(t)
		theirs.declareEntityType(t, "quest")
		theirs.declareRelationType(t, "unlocks", "", []string{"unlocks"})
		theirs.entity(t, "quest", "their-lonely-quest")

		got := theirs.orphans(t, OrphansInput{})
		assert.Must(t, len(got.Findings) == 1 && got.Findings[0].Key == "their-lonely-quest", "the second game's report names %v", got.keys())
		assert.Should(t, got.ConsideredTotal == 1, "considered_total = %d, want the one entity of this game: a total that "+
			"counted the other game's rows would be the same defect one statement along",
			got.ConsideredTotal)
		if len(mine.orphans(t, OrphansInput{}).Findings) != 1 {
			t.Fatal("the first game stopped reporting its own orphan, so the assertion " +
				"above proves nothing")
		}
	})
}

func contains(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}
