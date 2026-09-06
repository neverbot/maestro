package web_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/web"
)

// seedAddressable declares two entity types, one relation type between
// them with declared endpoint rules, two entities and one edge. It is
// the smallest game in which every address on this surface is
// exercisable.
func seedAddressable(t *testing.T, deps web.MCPDeps, caller web.Caller, game gameRef) {
	t.Helper()
	ctx := context.Background()
	for _, spec := range []web.TypesUpsertInput{
		{Key: "quest", Label: "Quest", LabelPlural: "Quests"},
		{Key: "zone", Label: "Zone", LabelPlural: "Zones"},
	} {
		if _, err := web.MCPTypesUpsert(ctx, deps, caller, game.ID, spec); err != nil {
			t.Fatalf("types.upsert %s: %v", spec.Key, err)
		}
	}
	if _, err := web.MCPRelationTypesUpsert(ctx, deps, caller, game.ID, web.RelationTypesUpsertInput{
		Key: "takes_place_in", Label: "takes place in",
		SourceTypeKeys: []string{"quest"}, TargetTypeKeys: []string{"zone"},
		SemanticRole: "spatial",
	}); err != nil {
		t.Fatalf("relation_types.upsert: %v", err)
	}
	if _, err := web.MCPEntitiesUpsert(ctx, deps, caller, game.ID, web.EntitiesUpsertInput{
		Mode: string(metamodel.BulkAtomic),
		Items: []web.EntityItemInput{
			{TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger"},
			{TypeKey: "zone", Key: "elwynn", Name: "Elwynn Forest"},
		},
	}); err != nil {
		t.Fatalf("entities.upsert: %v", err)
	}
	if _, err := web.MCPRelationsUpsert(ctx, deps, caller, game.ID, web.RelationsUpsertInput{
		Mode: string(metamodel.BulkAtomic),
		Items: []web.RelationItemInput{{
			TypeKey: "takes_place_in",
			Source:  web.RefInput{TypeKey: "quest", Key: "hogger"},
			Target:  web.RefInput{TypeKey: "zone", Key: "elwynn"},
		}},
	}); err != nil {
		t.Fatalf("relations.upsert: %v", err)
	}
}

// gameRef is the one field seedAddressable needs of either fixture.
type gameRef struct{ ID uuid.UUID }

// TestEveryAddressOnThisSurfaceIsAKey is Metamodel 14's addressing
// decision, asserted as one property rather than tool by tool.
//
// **Keys replace ids; they are not accepted beside them.** The evidence
// that the replacement is total is here: every tool that used to take a
// uuid is driven by the address the row was written under, and the rows
// really go. The evidence that it is a *replacement* is the second half:
// the ids are still on the wire everywhere they were, so nothing that
// had one has lost it — they are simply no longer how you address a row.
func TestEveryAddressOnThisSurfaceIsAKey(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()
	seedAddressable(t, f.deps, f.caller, gameRef{f.game})

	// The ids are still answered. A caller that wants one still gets one
	// from every reader that carried it before.
	entity, err := web.MCPEntitiesGet(ctx, f.deps, f.caller, f.game,
		web.EntitiesGetInput{TypeKey: "quest", Key: "hogger"})
	if err != nil {
		t.Fatalf("entities.get: %v", err)
	}
	if entity.ID == uuid.Nil {
		t.Fatalf("an entity came back without its id: %+v", entity)
	}
	edges, err := web.MCPRelationsList(ctx, f.deps, f.caller, f.game,
		web.RelationsListInput{
			Source: &web.RefInput{TypeKey: "quest", Key: "hogger"},
		})
	if err != nil {
		t.Fatalf("relations.list by source ref: %v", err)
	}
	if len(edges.Items) != 1 || edges.Items[0].SourceID != entity.ID {
		t.Fatalf("the endpoint filter answered with %+v", edges.Items)
	}

	// The filter is case-folded exactly as every other key lookup is, and
	// two spellings are one listing rather than two.
	upper, err := web.MCPRelationsList(ctx, f.deps, f.caller, f.game,
		web.RelationsListInput{Source: &web.RefInput{TypeKey: "QUEST", Key: "HOGGER"}})
	if err != nil {
		t.Fatalf("relations.list with a differently cased ref: %v", err)
	}
	if len(upper.Items) != 1 {
		t.Fatalf("a differently cased ref found %d edges, want 1", len(upper.Items))
	}

	// A ref that names no entity is not_found, not an empty page: an
	// empty page is a wrong answer that looks like a right one.
	if _, err := web.MCPRelationsList(ctx, f.deps, f.caller, f.game,
		web.RelationsListInput{
			Source: &web.RefInput{TypeKey: "quest", Key: "nope"},
		}); err == nil {
		t.Fatalf("an endpoint filter naming no entity was answered with a page")
	}

	// A ref carrying a byte no key may hold is the caller's own argument,
	// refused before Postgres sees it: EntityByKey passes a key straight
	// through, so an unbounded one comes back as SQLSTATE 22021 — an
	// internal_error over something the caller typed. Both halves of both
	// refs are checked, and every problem is reported at once.
	_, err = web.MCPRelationsList(ctx, f.deps, f.caller, f.game, web.RelationsListInput{
		Source: &web.RefInput{TypeKey: "quest", Key: "hog\x00ger"},
		Target: &web.RefInput{TypeKey: "not a key", Key: "elwynn"},
	})
	var invalid *metamodel.ValidationError
	if !errors.As(err, &invalid) || invalid.Code != "invalid_input" {
		t.Fatalf("err = %#v, want an invalid_input ValidationError", err)
	}
	paths := make([]string, 0, len(invalid.Fields))
	for _, p := range invalid.Fields {
		paths = append(paths, p.Path)
	}
	if strings.Join(paths, ",") != "source.key,target.type_key" {
		t.Fatalf("problems = %v, want both bad halves named at once", paths)
	}

	// Then the four removals, each by the address the row was written
	// under, innermost first.
	if _, err := web.MCPRelationsRemove(ctx, f.deps, f.caller, f.game, web.RelationsRemoveInput{
		TypeKey: "takes_place_in",
		Source:  web.RefInput{TypeKey: "quest", Key: "hogger"},
		Target:  web.RefInput{TypeKey: "zone", Key: "elwynn"},
	}); err != nil {
		t.Fatalf("relations.remove: %v", err)
	}
	if _, err := web.MCPRelationsGet(ctx, f.deps, f.caller, f.game, web.RelationsGetInput{
		TypeKey: "takes_place_in",
		Source:  web.RefInput{TypeKey: "quest", Key: "hogger"},
		Target:  web.RefInput{TypeKey: "zone", Key: "elwynn"},
	}); err == nil {
		t.Fatalf("the removed edge is still readable")
	}

	if _, err := web.MCPEntitiesRemove(ctx, f.deps, f.caller, f.game,
		web.EntitiesRemoveInput{TypeKey: "quest", Key: "hogger"}); err != nil {
		t.Fatalf("entities.remove: %v", err)
	}
	if _, err := web.MCPRelationTypesRemove(ctx, f.deps, f.caller, f.game,
		web.RelationTypesRemoveInput{Key: "takes_place_in"}); err != nil {
		t.Fatalf("relation_types.remove: %v", err)
	}
	if _, err := web.MCPTypesRemove(ctx, f.deps, f.caller, f.game,
		web.TypesRemoveInput{Key: "quest"}); err != nil {
		t.Fatalf("types.remove: %v", err)
	}
	// zone still holds an entity, so it needs cascade — which proves the
	// key route reaches the in_use check rather than short-circuiting.
	if _, err := web.MCPTypesRemove(ctx, f.deps, f.caller, f.game,
		web.TypesRemoveInput{Key: "zone"}); err == nil {
		t.Fatalf("removing a type that still has entities was accepted")
	}
	if _, err := web.MCPTypesRemove(ctx, f.deps, f.caller, f.game,
		web.TypesRemoveInput{Key: "zone", Cascade: true}); err != nil {
		t.Fatalf("types.remove with cascade: %v", err)
	}

	counts, err := web.MCPGameCounts(ctx, f.deps, f.caller, f.game, web.GameCountsInput{})
	if err != nil {
		t.Fatalf("games.counts: %v", err)
	}
	if len(counts.EntityTypes) != 0 || len(counts.RelationTypes) != 0 ||
		counts.Totals.Entities != 0 || counts.Totals.Relations != 0 {
		t.Fatalf("the game is not empty after four removals: %+v", counts)
	}
}

// TestARelationTypeReadsBackTheEndpointKeysItWasDeclaredWith is the
// property the endpoint change is worth having for: what comes out of
// the read is what can go back into the write, with no map to build in
// between.
func TestARelationTypeReadsBackTheEndpointKeysItWasDeclaredWith(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()
	seedAddressable(t, f.deps, f.caller, gameRef{f.game})

	detail, err := web.MCPRelationTypesGet(ctx, f.deps, f.caller, f.game,
		web.RelationTypesGetInput{Key: "takes_place_in"})
	if err != nil {
		t.Fatalf("relation_types.get: %v", err)
	}
	if len(detail.SourceTypeKeys) != 1 || detail.SourceTypeKeys[0] != "quest" ||
		len(detail.TargetTypeKeys) != 1 || detail.TargetTypeKeys[0] != "zone" {
		t.Fatalf("endpoint rules read back as %+v", detail)
	}
	// The stored spelling, not the caller's: this type was declared with
	// lower-case keys, and a differently cased declaration must not
	// change what the answer says.
	if _, err := web.MCPRelationTypesUpsert(ctx, f.deps, f.caller, f.game,
		web.RelationTypesUpsertInput{
			Key: "takes_place_in", Label: "takes place in",
			SourceTypeKeys: []string{"QUEST"}, TargetTypeKeys: []string{"Zone"},
			SemanticRole: "spatial", ExpectedVersion: &detail.Version,
		}); err != nil {
		t.Fatalf("re-declaring with differently cased keys: %v", err)
	}
	again, err := web.MCPRelationTypesGet(ctx, f.deps, f.caller, f.game,
		web.RelationTypesGetInput{Key: "takes_place_in"})
	if err != nil {
		t.Fatalf("relation_types.get: %v", err)
	}
	if again.SourceTypeKeys[0] != "quest" || again.TargetTypeKeys[0] != "zone" {
		t.Fatalf("endpoint rules echoed the caller's casing: %+v", again)
	}

	// An undeclared endpoint list is `[]` and never null: "this type
	// accepts any" and "the server said nothing" must not look alike.
	if _, err := web.MCPRelationTypesUpsert(ctx, f.deps, f.caller, f.game,
		web.RelationTypesUpsertInput{Key: "mentions", Label: "mentions"}); err != nil {
		t.Fatalf("relation_types.upsert mentions: %v", err)
	}
	open, err := web.MCPRelationTypesGet(ctx, f.deps, f.caller, f.game,
		web.RelationTypesGetInput{Key: "mentions"})
	if err != nil {
		t.Fatalf("relation_types.get mentions: %v", err)
	}
	raw, err := json.Marshal(open)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"source_type_keys":[]`) ||
		!strings.Contains(string(raw), `"target_type_keys":[]`) {
		t.Fatalf("an undeclared endpoint rule marshalled as %s", raw)
	}

	// An endpoint key carrying a byte no key may hold is refused before
	// the lookup runs, at its own indexed path. These keys reach Postgres
	// as a text[], so an unbounded one fails the whole statement with
	// SQLSTATE 22021 — an internal_error over the caller's own argument,
	// and over the list rather than the element.
	_, err = web.MCPRelationTypesUpsert(ctx, f.deps, f.caller, f.game,
		web.RelationTypesUpsertInput{
			Key: "eats", Label: "eats",
			SourceTypeKeys: []string{"quest", "zo\x00ne"},
		})
	var badKey *metamodel.ValidationError
	if !errors.As(err, &badKey) || badKey.Code != "invalid_input" {
		t.Fatalf("err = %#v, want an invalid_input ValidationError", err)
	}
	if len(badKey.Fields) != 1 || badKey.Fields[0].Path != "source_type_keys[1]" {
		t.Fatalf("problems = %+v, want the malformed element named", badKey.Fields)
	}

	// And the endpoint rule is still enforced, which is what would be
	// lost by a translation that resolved a key to the wrong id.
	_, err = web.MCPRelationsUpsert(ctx, f.deps, f.caller, f.game, web.RelationsUpsertInput{
		Mode: string(metamodel.BulkAtomic),
		Items: []web.RelationItemInput{{
			TypeKey: "takes_place_in",
			Source:  web.RefInput{TypeKey: "zone", Key: "elwynn"},
			Target:  web.RefInput{TypeKey: "zone", Key: "elwynn"},
		}},
	})
	if !errors.Is(err, metamodel.ErrEndpointTypeMismatch) {
		t.Fatalf("err = %v, want ErrEndpointTypeMismatch", err)
	}
}

// TestGamesCountsAnswersTheQuestionAPagedWalkUsedTo is the counting half
// of Metamodel 14, and it asserts the property that makes one shared
// assembly worth having: the tool and the page report the same numbers,
// because they are the same numbers.
func TestGamesCountsAnswersTheQuestionAPagedWalkUsedTo(t *testing.T) {
	f := newRESTFixture(t)
	ctx := context.Background()
	seedAddressable(t, f.deps(), f.caller(), gameRef{f.game})

	counts, err := web.MCPGameCounts(ctx, f.deps(), f.caller(), f.game, web.GameCountsInput{})
	if err != nil {
		t.Fatalf("games.counts: %v", err)
	}
	byType := map[string]int64{}
	for _, row := range counts.EntityTypes {
		byType[row.Key] = row.EntityCount
	}
	if byType["quest"] != 1 || byType["zone"] != 1 || counts.Totals.Entities != 2 {
		t.Fatalf("games.counts read %+v", counts)
	}
	if len(counts.RelationTypes) != 1 || counts.RelationTypes[0].RelationCount != 1 ||
		counts.Totals.Relations != 1 || counts.Totals.Invalid != 0 {
		t.Fatalf("games.counts read %+v for edges", counts)
	}

	// The invalid count is the number a designer acts on, so it has to
	// move when a schema edit flags rows — on both tables, which is the
	// half a count of entities alone would get wrong.
	if _, err := web.MCPTypesUpsert(ctx, f.deps(), f.caller(), f.game, web.TypesUpsertInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema:          []web.FieldInput{{Key: "laps", Type: "number", Required: true}},
		ExpectedVersion: i32p(1),
	}); err != nil {
		t.Fatalf("narrow the entity schema: %v", err)
	}
	if _, err := web.MCPRelationTypesUpsert(ctx, f.deps(), f.caller(), f.game,
		web.RelationTypesUpsertInput{
			Key: "takes_place_in", Label: "takes place in",
			SourceTypeKeys:  []string{"quest"},
			TargetTypeKeys:  []string{"zone"},
			Schema:          []web.FieldInput{{Key: "act", Type: "number", Required: true}},
			ExpectedVersion: i32p(1),
		}); err != nil {
		t.Fatalf("narrow the edge schema: %v", err)
	}
	flagged, err := web.MCPGameCounts(ctx, f.deps(), f.caller(), f.game, web.GameCountsInput{})
	if err != nil {
		t.Fatalf("games.counts: %v", err)
	}
	if flagged.Totals.Invalid != 2 {
		t.Fatalf("the totals count %d invalid rows, want the quest and the edge: %+v",
			flagged.Totals.Invalid, flagged)
	}

	// And the page. Same numbers, plus the one field only it has.
	rec := f.as(t, http.MethodGet, "/summary", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("summary = %d: %s", rec.Code, rec.Body.String())
	}
	var page web.GameSummaryOutput
	decodeBody(t, rec, &page)
	if page.Totals != flagged.Totals || len(page.EntityTypes) != len(flagged.EntityTypes) {
		t.Fatalf("the page and the tool disagree: %+v against %+v", page.Totals, flagged.Totals)
	}
	if page.Role == "" {
		t.Fatalf("the page lost the one field the tool does not carry")
	}
	// The tool does not carry it, and must not: an agent has a token,
	// not a membership row, and a `"role": ""` would say nothing.
	raw, err := json.Marshal(flagged)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "role") {
		t.Fatalf("the tool's answer carries a role: %s", raw)
	}
}

// TestTheRESTMirrorAddressesRowsByKeyToo is the human half of the
// addressing change: two surfaces, one core each, and a mirror that was
// never called is a mirror that compiles.
func TestTheRESTMirrorAddressesRowsByKeyToo(t *testing.T) {
	f := newRESTFixture(t)
	seedAddressable(t, f.deps(), f.caller(), gameRef{f.game})

	// The endpoint filter, as a ref in the query string.
	rec := f.as(t, http.MethodGet,
		"/relations?source_type_key=quest&source_key=hogger", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered listing = %d: %s", rec.Code, rec.Body.String())
	}
	var listing struct {
		Items []json.RawMessage `json:"items"`
	}
	decodeBody(t, rec, &listing)
	if len(listing.Items) != 1 {
		t.Fatalf("the filtered listing returned %s", rec.Body.String())
	}

	// **Half a ref is refused, at the half that is missing.** Reading it
	// as "no filter" would answer a narrowed question with the whole
	// listing, which is this surface's own wrong answer that looks like a
	// right one.
	half := f.as(t, http.MethodGet, "/relations?source_type_key=quest", nil)
	if half.Code != http.StatusBadRequest ||
		!strings.Contains(half.Body.String(), "source.key") {
		t.Fatalf("half an endpoint ref = %d %s", half.Code, half.Body.String())
	}

	// The four removals, by key, innermost first — and the edge one
	// through /relations/one, the same five parameters its read takes.
	edge := "/relations/one?type_key=takes_place_in" +
		"&source_type_key=quest&source_key=hogger" +
		"&target_type_key=zone&target_key=elwynn"
	if rec := f.as(t, http.MethodDelete, edge, nil); rec.Code != http.StatusOK {
		t.Fatalf("DELETE %s = %d: %s", edge, rec.Code, rec.Body.String())
	}
	if rec := f.as(t, http.MethodGet, edge, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("the removed edge is still readable: %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{
		"/entities/by-key/quest/hogger",
		"/relation-types/by-key/takes_place_in",
		"/types/by-key/quest",
		"/types/by-key/zone?cascade=true",
	} {
		if rec := f.as(t, http.MethodDelete, path, nil); rec.Code != http.StatusOK {
			t.Fatalf("DELETE %s = %d: %s", path, rec.Code, rec.Body.String())
		}
	}
	summary := f.as(t, http.MethodGet, "/summary", nil)
	var page web.GameSummaryOutput
	decodeBody(t, summary, &page)
	if len(page.EntityTypes) != 0 || page.Totals.Entities != 0 {
		t.Fatalf("the game is not empty after four removals: %+v", page)
	}
}

func i32p(v int32) *int32 { return &v }
