package web_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/web"
)

// This file is the sub-project's definition of done: a whole game,
// declared and seeded the way a real agent would do it — over the MCP
// tool surface, never through the Go domain API — and then read back
// through the same surface plus the REST one the page uses.
//
// **Why it drives the tools rather than metamodel.Service.** Every other
// test in this repository proves one call behaves; none of them proves
// the calls *compose*. An agent seeding a game has to get the ids of the
// entity types it just declared in order to state a relation type's
// endpoints, has to keep the versions a bulk write reported in order to
// edit those same rows a minute later, and has to page a traversal to
// see a hub's whole neighbourhood. Those are properties of the surface
// as a whole, and they are only visible from a test that uses it as a
// whole.
//
// **The genre is the racing career the plan names** (Task 9's own
// skeleton calls the game `le-mans`), and it was chosen over the MMORPG
// for one reason worth recording: a career game needs a self-referencing
// relation type that is not a toy — a licence tier superseding the one
// below it — and it needs an entity type that is genuinely outside the
// graph, which a sponsor is and a zone never is.
//
// The shape is in the constants below: 511 entities across seven types
// and 1,044 edges across eight relation types, with one deliberately
// dense hub (the circuit 120 of the 200 races are run at) so that a
// traversal has something to page.

const (
	seedCircuits      = 60
	seedCars          = 90
	seedDrivers       = 120
	seedRaces         = 200
	seedChampionships = 12
	seedLicences      = 4
	seedSponsors      = 25

	// seedHubRaces is how many of the races are run at circuit-000. It is
	// larger than the 50-row default page and than the 500-row cap is
	// small, which is what makes the traversal test a paging test.
	seedHubRaces = 120

	seedEntities = seedCircuits + seedCars + seedDrivers + seedRaces +
		seedChampionships + seedLicences + seedSponsors
)

// licenceTiers is the licence ladder, lowest first. The `supersedes`
// edges walk it, which is the game's self-referencing relation.
var licenceTiers = []string{"bronze", "silver", "gold", "platinum"}

// seeded is a whole game plus everything the caller that seeded it
// needs to keep talking to it.
type seeded struct {
	srv     *web.Server
	deps    web.MCPDeps
	caller  web.Caller
	game    uuid.UUID
	cookie  *http.Cookie
	typeIDs map[string]uuid.UUID
	// races is the bulk report of the 200-race batch, kept because the
	// read-then-write loop below is exactly the thing it exists for.
	races []metamodel.BulkWrite
}

func i32(v int32) *int32 { return &v }

// briefing builds a paragraph long enough that the word it hides in the
// middle is genuinely in the middle, so that "search finds a word in the
// middle of a long description" is not answered by a two-word string.
func briefing(word string) string {
	var b strings.Builder
	for i := 0; i < 60; i++ {
		b.WriteString("The stint plan holds through the opening hour and the pit window stays shut. ")
		if i == 30 {
			b.WriteString("Watch the " + word + " on the exit of the second chicane. ")
		}
	}
	return b.String()
}

// seedRacingGame declares the game's whole vocabulary and fills it,
// entirely through the MCP tools.
func seedRacingGame(t *testing.T) *seeded {
	t.Helper()
	ctx := context.Background()
	srv, ids, projSvc, mm := newMetamodelTestServer(t)

	user, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	game, err := projSvc.Create(ctx, "le-mans", "Le Mans", user.ID)
	if err != nil {
		t.Fatalf("Create game: %v", err)
	}
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: game.ID, UserID: user.ID, Label: "seed agent",
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	caller, err := web.CallerForToken(ctx, ids, token)
	if err != nil {
		t.Fatalf("CallerForToken: %v", err)
	}
	s := &seeded{
		srv:     srv,
		deps:    web.MCPDeps{Identity: ids, Projects: projSvc, Metamodel: mm},
		caller:  caller,
		game:    game.ID,
		cookie:  loginAs(t, srv, "designer@studio.com"),
		typeIDs: map[string]uuid.UUID{},
	}

	s.declareTypes(t)
	s.declareRelationTypes(t)
	s.seedEntities(t)
	s.seedRelations(t)
	return s
}

// declareTypes declares the seven entity types. Between them they use
// every field shape the metamodel has: a required field, an enum, a
// list<text>, a bounded number, a declared default (including one whose
// value is `false`, which is the case a value-based "is it set" rule
// gets wrong), a longtext and a bool.
func (s *seeded) declareTypes(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	specs := []web.TypesUpsertInput{
		{
			Key: "circuit", Label: "Circuit", LabelPlural: "Circuits",
			Description: "A track a race can be run at.",
			Schema: []web.FieldInput{
				{Key: "country", Label: "Country", Type: "text", Required: true},
				{Key: "length_km", Label: "Length (km)", Type: "number", Min: ptrFloat(3), Max: ptrFloat(30)},
				{Key: "surface", Label: "Surface", Type: "enum",
					Options: []string{"tarmac", "street", "mixed"}, Default: "tarmac"},
				{Key: "tags", Label: "Tags", Type: "list<text>"},
				{Key: "notes", Label: "Notes", Type: "longtext"},
			},
		},
		{
			Key: "car", Label: "Car", LabelPlural: "Cars",
			Schema: []web.FieldInput{
				{Key: "power_hp", Type: "number", Required: true, Min: ptrFloat(100), Max: ptrFloat(1600)},
				{Key: "class", Type: "enum", Required: true,
					Options: []string{"hypercar", "lmp2", "gte", "gt3"}},
				{Key: "manufacturer", Type: "text"},
				// A default of false: declared, not absent.
				{Key: "hybrid", Type: "bool", Default: false},
			},
		},
		{
			Key: "driver", Label: "Driver", LabelPlural: "Drivers",
			Schema: []web.FieldInput{
				{Key: "nationality", Type: "text", Required: true},
				{Key: "rating", Type: "number", Min: ptrFloat(0), Max: ptrFloat(100)},
				{Key: "traits", Type: "list<text>"},
				{Key: "bio", Type: "longtext"},
			},
		},
		{
			Key: "race", Label: "Race", LabelPlural: "Races",
			Schema: []web.FieldInput{
				{Key: "laps", Type: "number", Required: true, Min: ptrFloat(1), Max: ptrFloat(400)},
				{Key: "night", Type: "bool", Default: false},
				{Key: "briefing", Type: "longtext"},
			},
		},
		{
			Key: "championship", Label: "Championship", LabelPlural: "Championships",
			Schema: []web.FieldInput{{Key: "season", Type: "number", Required: true}},
		},
		{
			Key: "licence", Label: "Licence", LabelPlural: "Licences",
			Schema: []web.FieldInput{
				{Key: "tier", Type: "enum", Required: true, Options: licenceTiers},
			},
		},
		{
			// The type that is deliberately outside the graph: no relation
			// type names it at either endpoint, and no edge ever touches
			// one. A metamodel that only works for connected things would
			// fail here.
			Key: "sponsor", Label: "Sponsor", LabelPlural: "Sponsors",
			Schema: []web.FieldInput{{Key: "budget_musd", Type: "number", Min: ptrFloat(0)}},
		},
	}
	for _, spec := range specs {
		out, err := web.MCPTypesUpsert(ctx, s.deps, s.caller, s.game, spec)
		if err != nil {
			t.Fatalf("declare type %s: %v", spec.Key, err)
		}
		if out.Version != 1 {
			t.Fatalf("type %s landed at version %d, want 1", spec.Key, out.Version)
		}
		s.typeIDs[spec.Key] = out.ID
	}
}

// declareRelationTypes declares the eight edge kinds. Six carry endpoint
// rules, two of those are self-referencing, and one (`mentions`)
// deliberately carries none at all.
func (s *seeded) declareRelationTypes(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	id := func(key string) string { return s.typeIDs[key].String() }
	specs := []web.RelationTypesUpsertInput{
		{
			Key: "takes_place_in", Label: "takes place in",
			SourceTypeIDs: []string{id("race")}, TargetTypeIDs: []string{id("circuit")},
			SemanticRole: "spatial",
		},
		{
			Key: "part_of", Label: "part of",
			SourceTypeIDs: []string{id("race")}, TargetTypeIDs: []string{id("championship")},
			SemanticRole: "containment",
		},
		{
			Key: "drives", Label: "drives",
			SourceTypeIDs: []string{id("driver")}, TargetTypeIDs: []string{id("car")},
			// An edge with a schema of its own.
			Schema: []web.FieldInput{
				{Key: "seat", Type: "enum", Options: []string{"race", "reserve"}, Default: "race"},
				{Key: "season", Type: "number", Min: ptrFloat(1990), Max: ptrFloat(2100)},
			},
		},
		{
			Key: "eligible_for", Label: "eligible for",
			SourceTypeIDs: []string{id("car")}, TargetTypeIDs: []string{id("race")},
			SemanticRole: "availability",
		},
		{
			Key: "requires_licence", Label: "requires licence",
			SourceTypeIDs: []string{id("race")}, TargetTypeIDs: []string{id("licence")},
			SemanticRole: "prerequisite",
		},
		{
			// Self-referencing: licence to licence.
			Key: "supersedes", Label: "supersedes",
			SourceTypeIDs: []string{id("licence")}, TargetTypeIDs: []string{id("licence")},
			SemanticRole: "unlock",
		},
		{
			// Self-referencing again, and dense: every driver has one.
			Key: "teammate_of", Label: "teammate of",
			SourceTypeIDs: []string{id("driver")}, TargetTypeIDs: []string{id("driver")},
		},
		{
			// No endpoint rules at all: anything may sit at either end.
			Key: "mentions", Label: "mentions",
		},
	}
	for _, spec := range specs {
		out, err := web.MCPRelationTypesUpsert(ctx, s.deps, s.caller, s.game, spec)
		if err != nil {
			t.Fatalf("declare relation type %s: %v", spec.Key, err)
		}
		if out.Version != 1 {
			t.Fatalf("relation type %s landed at version %d, want 1", spec.Key, out.Version)
		}
	}
}

func (s *seeded) upsertEntities(t *testing.T, what string, items []web.EntityItemInput) []metamodel.BulkWrite {
	t.Helper()
	out, err := web.MCPEntitiesUpsert(context.Background(), s.deps, s.caller, s.game,
		web.EntitiesUpsertInput{Mode: string(metamodel.BulkAtomic), Items: items})
	if err != nil {
		t.Fatalf("seed %s: %v", what, err)
	}
	if out.Count != len(items) || len(out.Written) != len(items) {
		t.Fatalf("seed %s: count = %d, written = %d, want %d", what, out.Count, len(out.Written), len(items))
	}
	if len(out.Failed) != 0 {
		t.Fatalf("seed %s: %d failures on an atomic batch that returned no error: %+v", what, len(out.Failed), out.Failed)
	}
	return out.Written
}

func (s *seeded) seedEntities(t *testing.T) {
	t.Helper()

	circuits := make([]web.EntityItemInput, 0, seedCircuits)
	for i := 0; i < seedCircuits; i++ {
		item := web.EntityItemInput{
			TypeKey: "circuit",
			Key:     fmt.Sprintf("circuit-%03d", i),
			Name:    fmt.Sprintf("Circuit %03d", i),
			Fields: map[string]any{
				"country":   []string{"France", "Italy", "Japan", "Brazil"}[i%4],
				"length_km": 3.5 + float64(i%20),
				"tags":      []any{"permanent", fmt.Sprintf("region-%d", i%5)},
			},
		}
		if i == 0 {
			// The hub, and the search fixture: a real name, a real tag,
			// and a surface it states rather than defaults into.
			item.Name = "Circuit de la Sarthe"
			item.Fields["surface"] = "mixed"
			item.Fields["tags"] = []any{"hunaudieres", "endurance", "permanent"}
			item.Fields["notes"] = "The Mulsanne straight was split by two chicanes in 1990."
		}
		circuits = append(circuits, item)
	}
	s.upsertEntities(t, "circuits", circuits)

	cars := make([]web.EntityItemInput, 0, seedCars)
	for i := 0; i < seedCars; i++ {
		cars = append(cars, web.EntityItemInput{
			TypeKey: "car",
			Key:     fmt.Sprintf("car-%03d", i),
			Name:    fmt.Sprintf("Car %03d", i),
			Fields: map[string]any{
				"power_hp":     500 + float64(i%600),
				"class":        []string{"hypercar", "lmp2", "gte", "gt3"}[i%4],
				"manufacturer": []string{"Aurel", "Bramante", "Corveau"}[i%3],
			},
		})
	}
	s.upsertEntities(t, "cars", cars)

	drivers := make([]web.EntityItemInput, 0, seedDrivers)
	for i := 0; i < seedDrivers; i++ {
		item := web.EntityItemInput{
			TypeKey: "driver",
			Key:     fmt.Sprintf("driver-%03d", i),
			Name:    fmt.Sprintf("Driver %03d", i),
			Fields: map[string]any{
				"nationality": []string{"FR", "IT", "JP", "BR", "DE"}[i%5],
				"rating":      float64(40 + i%60),
				"traits":      []any{"consistent", fmt.Sprintf("tier-%d", i%3)},
			},
		}
		if i == 7 {
			// The list<text> search fixture.
			item.Fields["traits"] = []any{"rainmaster", "qualifier", "consistent"}
			item.Fields["bio"] = "Came up through single seaters."
		}
		drivers = append(drivers, item)
	}
	s.upsertEntities(t, "drivers", drivers)

	races := make([]web.EntityItemInput, 0, seedRaces)
	for i := 0; i < seedRaces; i++ {
		item := web.EntityItemInput{
			TypeKey: "race",
			Key:     fmt.Sprintf("race-%03d", i),
			Name:    fmt.Sprintf("Race %03d", i),
			Fields:  map[string]any{"laps": float64(10 + i%40)},
		}
		switch i {
		case 42:
			// Carries the query word in its *name*.
			item.Name = "Mulsanne Endurance Classic"
		case 43:
			// Carries the same word only in its body, many times over:
			// the row a rank-only ordering would put first.
			item.Name = "Night Sprint"
			item.Fields["night"] = true
			item.Fields["briefing"] = strings.Repeat("Mulsanne. ", 40)
		case 100:
			// The word buried in the middle of a long description.
			item.Fields["briefing"] = briefing("kerbstone")
		}
		races = append(races, item)
	}
	s.races = s.upsertEntities(t, "races", races)

	championships := make([]web.EntityItemInput, 0, seedChampionships)
	for i := 0; i < seedChampionships; i++ {
		championships = append(championships, web.EntityItemInput{
			TypeKey: "championship",
			Key:     fmt.Sprintf("championship-%02d", i),
			Name:    fmt.Sprintf("Season %d Cup", 2014+i),
			Fields:  map[string]any{"season": float64(2014 + i)},
		})
	}
	s.upsertEntities(t, "championships", championships)

	licences := make([]web.EntityItemInput, 0, seedLicences)
	for _, tier := range licenceTiers {
		licences = append(licences, web.EntityItemInput{
			TypeKey: "licence", Key: "licence-" + tier,
			Name:   strings.ToUpper(tier[:1]) + tier[1:] + " licence",
			Fields: map[string]any{"tier": tier},
		})
	}
	s.upsertEntities(t, "licences", licences)

	sponsors := make([]web.EntityItemInput, 0, seedSponsors)
	for i := 0; i < seedSponsors; i++ {
		sponsors = append(sponsors, web.EntityItemInput{
			TypeKey: "sponsor",
			Key:     fmt.Sprintf("sponsor-%02d", i),
			Name:    fmt.Sprintf("Sponsor %02d", i),
			Fields:  map[string]any{"budget_musd": float64(i)},
		})
	}
	s.upsertEntities(t, "sponsors", sponsors)
}

func (s *seeded) upsertRelations(t *testing.T, what string, items []web.RelationItemInput) {
	t.Helper()
	out, err := web.MCPRelationsUpsert(context.Background(), s.deps, s.caller, s.game,
		web.RelationsUpsertInput{Mode: string(metamodel.BulkAtomic), Items: items})
	if err != nil {
		t.Fatalf("wire %s: %v", what, err)
	}
	if out.Count != len(items) {
		t.Fatalf("wire %s: count = %d, want %d", what, out.Count, len(items))
	}
	for _, w := range out.Written {
		if w.ID == uuid.Nil || w.SourceID == uuid.Nil || w.TargetID == uuid.Nil {
			t.Fatalf("wire %s: an edge came back without its ids: %+v", what, w)
		}
	}
}

func ref(typeKey, key string) web.RefInput { return web.RefInput{TypeKey: typeKey, Key: key} }

func (s *seeded) seedRelations(t *testing.T) {
	t.Helper()

	// Races at circuits: the first seedHubRaces all run at circuit-000, so
	// that one entity has a neighbourhood no single page can hold. The
	// rest spread over circuits 1..59 — never 0, so the hub's degree is
	// exactly seedHubRaces.
	place := make([]web.RelationItemInput, 0, seedRaces)
	for i := 0; i < seedRaces; i++ {
		target := "circuit-000"
		if i >= seedHubRaces {
			target = fmt.Sprintf("circuit-%03d", 1+i%(seedCircuits-1))
		}
		place = append(place, web.RelationItemInput{
			TypeKey: "takes_place_in",
			Source:  ref("race", fmt.Sprintf("race-%03d", i)),
			Target:  ref("circuit", target),
		})
	}
	s.upsertRelations(t, "takes_place_in", place)

	partOf := make([]web.RelationItemInput, 0, seedRaces)
	for i := 0; i < seedRaces; i++ {
		partOf = append(partOf, web.RelationItemInput{
			TypeKey: "part_of",
			Source:  ref("race", fmt.Sprintf("race-%03d", i)),
			Target:  ref("championship", fmt.Sprintf("championship-%02d", i%seedChampionships)),
		})
	}
	s.upsertRelations(t, "part_of", partOf)

	drives := make([]web.RelationItemInput, 0, seedDrivers)
	for i := 0; i < seedDrivers; i++ {
		item := web.RelationItemInput{
			TypeKey: "drives",
			Source:  ref("driver", fmt.Sprintf("driver-%03d", i)),
			Target:  ref("car", fmt.Sprintf("car-%03d", i%seedCars)),
			Fields:  map[string]any{"season": float64(2014 + i%12)},
		}
		if i%10 == 0 {
			item.Fields["seat"] = "reserve"
		}
		drives = append(drives, item)
	}
	s.upsertRelations(t, "drives", drives)

	eligible := make([]web.RelationItemInput, 0, 2*seedCars)
	for i := 0; i < seedCars; i++ {
		for _, r := range []int{i, i + seedCars} {
			eligible = append(eligible, web.RelationItemInput{
				TypeKey: "eligible_for",
				Source:  ref("car", fmt.Sprintf("car-%03d", i)),
				Target:  ref("race", fmt.Sprintf("race-%03d", r)),
			})
		}
	}
	s.upsertRelations(t, "eligible_for", eligible)

	needs := make([]web.RelationItemInput, 0, seedRaces)
	for i := 0; i < seedRaces; i++ {
		needs = append(needs, web.RelationItemInput{
			TypeKey: "requires_licence",
			Source:  ref("race", fmt.Sprintf("race-%03d", i)),
			Target:  ref("licence", "licence-"+licenceTiers[i%seedLicences]),
		})
	}
	s.upsertRelations(t, "requires_licence", needs)

	ladder := make([]web.RelationItemInput, 0, seedLicences-1)
	for i := 1; i < seedLicences; i++ {
		ladder = append(ladder, web.RelationItemInput{
			TypeKey: "supersedes",
			Source:  ref("licence", "licence-"+licenceTiers[i]),
			Target:  ref("licence", "licence-"+licenceTiers[i-1]),
		})
	}
	s.upsertRelations(t, "supersedes", ladder)

	teammates := make([]web.RelationItemInput, 0, seedDrivers)
	for i := 0; i < seedDrivers; i++ {
		teammates = append(teammates, web.RelationItemInput{
			TypeKey: "teammate_of",
			Source:  ref("driver", fmt.Sprintf("driver-%03d", i)),
			Target:  ref("driver", fmt.Sprintf("driver-%03d", (i+1)%seedDrivers)),
		})
	}
	s.upsertRelations(t, "teammate_of", teammates)

	// `mentions` has no endpoint rules, so it carries two pairs no other
	// relation type in this game could hold.
	mentions := make([]web.RelationItemInput, 0, 21)
	for i := 0; i < 20; i++ {
		mentions = append(mentions, web.RelationItemInput{
			TypeKey: "mentions",
			Source:  ref("driver", fmt.Sprintf("driver-%03d", i)),
			Target:  ref("championship", fmt.Sprintf("championship-%02d", i%seedChampionships)),
		})
	}
	mentions = append(mentions, web.RelationItemInput{
		TypeKey: "mentions",
		Source:  ref("car", "car-000"),
		Target:  ref("circuit", "circuit-000"),
	})
	s.upsertRelations(t, "mentions", mentions)
}

// raceItems rebuilds the exact 200-race batch the seed sent, so a
// re-seed is a re-seed and not a different batch that happens to share
// its keys.
func (s *seeded) raceItems() []web.EntityItemInput {
	items := make([]web.EntityItemInput, 0, seedRaces)
	for i := 0; i < seedRaces; i++ {
		item := web.EntityItemInput{
			TypeKey: "race",
			Key:     fmt.Sprintf("race-%03d", i),
			Name:    fmt.Sprintf("Race %03d", i),
			Fields:  map[string]any{"laps": float64(10 + i%40)},
		}
		switch i {
		case 42:
			item.Name = "Mulsanne Endurance Classic"
		case 43:
			item.Name = "Night Sprint"
			item.Fields["night"] = true
			item.Fields["briefing"] = strings.Repeat("Mulsanne. ", 40)
		case 100:
			item.Fields["briefing"] = briefing("kerbstone")
		}
		items = append(items, item)
	}
	return items
}

// rewriteRaces re-sends every race at the version the batch report last
// gave for it, optionally with extra fields folded in, and keeps the new
// report. It is the read-then-write loop at seeding scale.
func (s *seeded) rewriteRaces(t *testing.T, extra map[string]any) {
	t.Helper()
	versions := map[string]int32{}
	for _, w := range s.races {
		versions[w.Key] = w.Version
	}
	items := s.raceItems()
	for i := range items {
		v := versions[items[i].Key]
		items[i].ExpectedVersion = &v
		for k, val := range extra {
			items[i].Fields[k] = val
		}
	}
	out, err := web.MCPEntitiesUpsert(context.Background(), s.deps, s.caller, s.game,
		web.EntitiesUpsertInput{Mode: string(metamodel.BulkPartial), Items: items})
	if err != nil {
		t.Fatalf("rewrite races: %v", err)
	}
	if out.Count != seedRaces || len(out.Failed) != 0 {
		t.Fatalf("rewrite races wrote %d and failed %+v", out.Count, out.Failed)
	}
	s.races = out.Written
}

// listAll walks a listing to exhaustion the way an agent must: pass the
// cursor back to the call that issued it, and stop when it is empty.
func (s *seeded) listAll(t *testing.T, in web.EntitiesListInput) []web.EntityOutput {
	t.Helper()
	ctx := context.Background()
	var all []web.EntityOutput
	for pages := 0; ; pages++ {
		if pages > 100 {
			t.Fatalf("a listing did not terminate after %d pages", pages)
		}
		page, err := web.MCPEntitiesList(ctx, s.deps, s.caller, s.game, in)
		if err != nil {
			t.Fatalf("entities.list: %v", err)
		}
		all = append(all, page.Items...)
		if page.NextCursor == nil {
			if page.Truncated {
				t.Fatalf("a page with no cursor still says it is truncated")
			}
			return all
		}
		in.Cursor = *page.NextCursor
	}
}

// TestSeedARacingGameEndToEnd is the acceptance test for the whole
// metamodel sub-project. The subtests run in order against one seeded
// game and several of them depend on the state the one before left, so
// none of them may be made parallel.
func TestSeedARacingGameEndToEnd(t *testing.T) {
	s := seedRacingGame(t)
	ctx := context.Background()

	t.Run("the seed landed the whole game", func(t *testing.T) {
		all := s.listAll(t, web.EntitiesListInput{Limit: 500})
		if len(all) != seedEntities {
			t.Fatalf("the game holds %d entities, want %d", len(all), seedEntities)
		}
		byType := map[string]int{}
		for _, e := range all {
			byType[e.TypeKey]++
			if e.Invalid {
				t.Fatalf("%s/%s came out of the seed already invalid", e.TypeKey, e.Key)
			}
		}
		for key, want := range map[string]int{
			"circuit": seedCircuits, "car": seedCars, "driver": seedDrivers,
			"race": seedRaces, "championship": seedChampionships,
			"licence": seedLicences, "sponsor": seedSponsors,
		} {
			if byType[key] != want {
				t.Fatalf("%s: %d entities, want %d", key, byType[key], want)
			}
		}
	})

	t.Run("a bulk batch names every row it landed, and the names are addressable", func(t *testing.T) {
		// The claim under test: an agent can edit what it just wrote,
		// using nothing but the batch's own answer. So build the index
		// from the report, and check every entry against a fresh read.
		if len(s.races) != seedRaces {
			t.Fatalf("the race batch reported %d rows, want %d", len(s.races), seedRaces)
		}
		stored := map[string]web.EntityOutput{}
		for _, e := range s.listAll(t, web.EntitiesListInput{TypeKey: "race", Limit: 500}) {
			stored[e.Key] = e
		}
		for _, w := range s.races {
			e, ok := stored[w.Key]
			if !ok {
				t.Fatalf("the batch reported %q, which no listing returns", w.Key)
			}
			if w.ID != e.ID || w.Version != e.Version || w.TypeKey != e.TypeKey {
				t.Fatalf("the batch reported %+v, the row reads %+v", w, e)
			}
			if w.ID == uuid.Nil || w.Version != 1 {
				t.Fatalf("the batch reported %+v, want a real id at version 1", w)
			}
		}
	})

	t.Run("a re-seed keeps every row and conflicts on every one of them", func(t *testing.T) {
		out, err := web.MCPEntitiesUpsert(ctx, s.deps, s.caller, s.game, web.EntitiesUpsertInput{
			Mode: string(metamodel.BulkPartial), Items: s.raceItems(),
		})
		if err != nil {
			t.Fatalf("re-seed: %v", err)
		}
		if len(out.Failed) != seedRaces || out.Count != 0 {
			t.Fatalf("a re-seed with no expected_version wrote %d and failed %d, want 0 and %d",
				out.Count, len(out.Failed), seedRaces)
		}
		for _, f := range out.Failed {
			if f.Code != "version_conflict" {
				t.Fatalf("re-seed failure %+v, want version_conflict", f)
			}
		}
		// Idempotent in row identity: same count, same ids, same versions.
		stored := map[string]web.EntityOutput{}
		for _, e := range s.listAll(t, web.EntitiesListInput{TypeKey: "race", Limit: 500}) {
			stored[e.Key] = e
		}
		if len(stored) != seedRaces {
			t.Fatalf("a refused re-seed changed the row count to %d", len(stored))
		}
		for _, w := range s.races {
			if e := stored[w.Key]; e.ID != w.ID || e.Version != w.Version {
				t.Fatalf("a refused re-seed moved %q: %+v, was %+v", w.Key, e, w)
			}
		}
	})

	t.Run("the read-then-write loop the skill bundle has to teach", func(t *testing.T) {
		// Recovery from the conflict above, at full batch scale: an agent
		// holds the versions its first write reported, sends them back as
		// expected_version, and every row lands one version higher.
		before := map[string]int32{}
		for _, w := range s.races {
			before[w.Key] = w.Version
		}
		s.rewriteRaces(t, nil)
		for _, w := range s.races {
			if w.Version != before[w.Key]+1 {
				t.Fatalf("%q came back at version %d, want %d", w.Key, w.Version, before[w.Key]+1)
			}
		}
	})

	t.Run("a schema change flags rows and never back-fills or rejects them", func(t *testing.T) {
		raceSchema := []web.FieldInput{
			{Key: "laps", Type: "number", Required: true, Min: ptrFloat(1), Max: ptrFloat(400)},
			{Key: "night", Type: "bool", Default: false},
			{Key: "briefing", Type: "longtext"},
		}
		withRequired := append(append([]web.FieldInput{}, raceSchema...),
			web.FieldInput{Key: "tyre_rules", Type: "text", Required: true})

		// 1. A required field added under 200 rows: accepted, not refused.
		edited, err := web.MCPTypesUpsert(ctx, s.deps, s.caller, s.game, web.TypesUpsertInput{
			Key: "race", Label: "Race", LabelPlural: "Races",
			Schema: withRequired, ExpectedVersion: i32(1),
		})
		if err != nil {
			t.Fatalf("a schema change under 200 rows was refused: %v", err)
		}
		if edited.Version != 2 {
			t.Fatalf("the edited type is at version %d, want 2", edited.Version)
		}

		flagged := s.listAll(t, web.EntitiesListInput{TypeKey: "race", Invalid: boolp(true), Limit: 500})
		if len(flagged) != seedRaces {
			t.Fatalf("%d races were flagged, want all %d", len(flagged), seedRaces)
		}
		// Not back-filled: the rows still hold exactly what was written.
		one, err := web.MCPEntitiesGet(ctx, s.deps, s.caller, s.game,
			web.EntitiesGetInput{TypeKey: "race", Key: "race-000"})
		if err != nil {
			t.Fatalf("entities.get: %v", err)
		}
		if !one.Invalid {
			t.Fatalf("race-000 reads valid after the schema change")
		}
		if _, present := one.Fields["tyre_rules"]; present {
			t.Fatalf("the schema change back-filled a value: %+v", one.Fields)
		}
		if one.Fields["laps"] == nil || one.Fields["night"] != false {
			t.Fatalf("the schema change disturbed the stored values: %+v", one.Fields)
		}
		// And the flag is not an edit: the row's version did not move.
		if want := versionOf(t, s.races, "race-000"); one.Version != want {
			t.Fatalf("flagging moved race-000 to version %d, want %d", one.Version, want)
		}

		// 2. There is no bulk repair. A required field cannot also
		// declare a default — the schema checker refuses the pair as a
		// self-contradiction — so nothing a designer can write into the
		// *type* makes those two hundred rows fit again.
		withDefault := append(append([]web.FieldInput{}, raceSchema...),
			web.FieldInput{Key: "tyre_rules", Type: "text", Required: true, Default: "open"})
		if _, err := web.MCPTypesUpsert(ctx, s.deps, s.caller, s.game, web.TypesUpsertInput{
			Key: "race", Label: "Race", LabelPlural: "Races",
			Schema: withDefault, ExpectedVersion: i32(2),
		}); !errors.Is(err, metamodel.ErrInvalidSchema) {
			t.Fatalf("a required field with a default was accepted or misreported: %v", err)
		}

		// 3. So the repair is a rewrite of every flagged row, each
		// carrying the version the flagging did not move. This is the
		// loop an agent actually has to run after a schema edit, and it
		// is why the sweep leaving versions alone matters.
		s.rewriteRaces(t, map[string]any{"tyre_rules": "open"})
		if flagged := s.listAll(t, web.EntitiesListInput{
			TypeKey: "race", Invalid: boolp(true), Limit: 500,
		}); len(flagged) != 0 {
			t.Fatalf("%d races are still flagged after every one was rewritten", len(flagged))
		}

		// 4. Taking the field back out flags all two hundred again, for
		// the mirror-image reason: the value they now carry is an
		// unknown field. A schema edit is never one-way.
		if _, err := web.MCPTypesUpsert(ctx, s.deps, s.caller, s.game, web.TypesUpsertInput{
			Key: "race", Label: "Race", LabelPlural: "Races",
			Schema: raceSchema, ExpectedVersion: i32(2),
		}); err != nil {
			t.Fatalf("reverting the schema: %v", err)
		}
		if flagged := s.listAll(t, web.EntitiesListInput{
			TypeKey: "race", Invalid: boolp(true), Limit: 500,
		}); len(flagged) != seedRaces {
			t.Fatalf("removing a field flagged %d races, want all %d", len(flagged), seedRaces)
		}

		// 5. And the same rewrite, without the field, puts the game back.
		s.rewriteRaces(t, nil)
		if flagged := s.listAll(t, web.EntitiesListInput{
			TypeKey: "race", Invalid: boolp(true), Limit: 500,
		}); len(flagged) != 0 {
			t.Fatalf("%d races stayed flagged after the second rewrite", len(flagged))
		}
	})

	t.Run("search finds what a designer would search for", func(t *testing.T) {
		// A name.
		hits := s.search(t, "Sarthe", "")
		if len(hits) == 0 || hits[0].Key != "circuit-000" || !hits[0].NameMatch {
			t.Fatalf("searching a name did not find the circuit: %+v", hits)
		}
		// A tag in a list<text>.
		hits = s.search(t, "rainmaster", "")
		if len(hits) != 1 || hits[0].Key != "driver-007" {
			t.Fatalf("searching a list<text> tag found %+v", hits)
		}
		if hits[0].NameMatch {
			t.Fatalf("a tag match is reported as a name match: %+v", hits[0])
		}
		// A word in the middle of a long description.
		hits = s.search(t, "kerbstone", "")
		if len(hits) != 1 || hits[0].Key != "race-100" {
			t.Fatalf("searching inside a long description found %+v", hits)
		}
		// A name match outranks a body match, even one that repeats the
		// word forty times.
		hits = s.search(t, "Mulsanne", "")
		if len(hits) < 2 {
			t.Fatalf("Mulsanne found %d rows, want the named one and the ones that mention it", len(hits))
		}
		if hits[0].Key != "race-042" || !hits[0].NameMatch {
			t.Fatalf("the row named Mulsanne is not first: %+v", hits)
		}
		var sawBody bool
		for _, h := range hits[1:] {
			if h.NameMatch {
				t.Fatalf("a name match sorted below a body match: %+v", hits)
			}
			if h.Key == "race-043" {
				sawBody = true
			}
		}
		if !sawBody {
			t.Fatalf("the row that mentions Mulsanne forty times was not found at all: %+v", hits)
		}
		// Narrowing by type is the same query over one catalogue.
		if hits := s.search(t, "Mulsanne", "circuit"); len(hits) != 1 || hits[0].Key != "circuit-000" {
			t.Fatalf("a type-narrowed search found %+v", hits)
		}
	})

	t.Run("a dense neighbourhood is reachable in full, one page at a time", func(t *testing.T) {
		// Everything that races at the hub, incoming over takes_place_in,
		// at a page size well below its degree.
		in := web.EntitiesListInput{
			Limit: 50,
			RelatedTo: &web.RelatedToInput{
				RelationTypeKey: "takes_place_in",
				EntityTypeKey:   "circuit",
				EntityKey:       "circuit-000",
				Direction:       "incoming",
			},
		}
		seen := map[string]bool{}
		for _, e := range s.listAll(t, in) {
			if seen[e.Key] {
				t.Fatalf("the traversal returned %q twice", e.Key)
			}
			seen[e.Key] = true
			if e.TypeKey != "race" {
				t.Fatalf("the traversal returned a %s", e.TypeKey)
			}
		}
		if len(seen) != seedHubRaces {
			t.Fatalf("the hub's neighbourhood came back as %d rows, want %d", len(seen), seedHubRaces)
		}
		// The other direction of the same edge from one of those races.
		in = web.EntitiesListInput{RelatedTo: &web.RelatedToInput{
			RelationTypeKey: "takes_place_in", EntityTypeKey: "race",
			EntityKey: "race-000", Direction: "outgoing",
		}}
		if out := s.listAll(t, in); len(out) != 1 || out[0].Key != "circuit-000" {
			t.Fatalf("outgoing from race-000 gave %+v", out)
		}
		// A self-referencing relation type walks both ways over one type.
		in = web.EntitiesListInput{RelatedTo: &web.RelatedToInput{
			RelationTypeKey: "supersedes", EntityTypeKey: "licence",
			EntityKey: "licence-gold", Direction: "outgoing",
		}}
		if out := s.listAll(t, in); len(out) != 1 || out[0].Key != "licence-silver" {
			t.Fatalf("gold supersedes %+v, want silver", out)
		}
		in.RelatedTo.Direction = "incoming"
		if out := s.listAll(t, in); len(out) != 1 || out[0].Key != "licence-platinum" {
			t.Fatalf("gold is superseded by %+v, want platinum", out)
		}
		// The type that is outside the graph stays outside it.
		in = web.EntitiesListInput{RelatedTo: &web.RelatedToInput{
			RelationTypeKey: "mentions", EntityTypeKey: "sponsor",
			EntityKey: "sponsor-00", Direction: "outgoing",
		}}
		if out := s.listAll(t, in); len(out) != 0 {
			t.Fatalf("a sponsor has edges: %+v", out)
		}
	})

	t.Run("every field shape the metamodel supports behaves as declared", func(t *testing.T) {
		// A declared default lands in the row, including a default of
		// false, which is the case a value-based "is it set" rule loses.
		car, err := web.MCPEntitiesGet(ctx, s.deps, s.caller, s.game,
			web.EntitiesGetInput{TypeKey: "car", Key: "car-000"})
		if err != nil {
			t.Fatalf("entities.get car-000: %v", err)
		}
		if car.Fields["hybrid"] != false {
			t.Fatalf("the declared default of false did not land: %+v", car.Fields)
		}
		circuit, err := web.MCPEntitiesGet(ctx, s.deps, s.caller, s.game,
			web.EntitiesGetInput{TypeKey: "circuit", Key: "circuit-001"})
		if err != nil {
			t.Fatalf("entities.get circuit-001: %v", err)
		}
		if circuit.Fields["surface"] != "tarmac" {
			t.Fatalf("the declared enum default did not land: %+v", circuit.Fields)
		}
		if _, present := circuit.Fields["notes"]; present {
			t.Fatalf("an absent optional field was zero-filled: %+v", circuit.Fields)
		}
		tags, ok := circuit.Fields["tags"].([]any)
		if !ok || len(tags) != 2 {
			t.Fatalf("the list<text> did not round trip: %#v", circuit.Fields["tags"])
		}

		// The four ways one row can be wrong, all in one partial batch, so
		// that a seeding agent's first pass is a single call with a
		// per-row report rather than four round trips.
		out, err := web.MCPEntitiesUpsert(ctx, s.deps, s.caller, s.game, web.EntitiesUpsertInput{
			Mode: string(metamodel.BulkPartial),
			Items: []web.EntityItemInput{
				{TypeKey: "car", Key: "car-bad-required", Name: "No power",
					Fields: map[string]any{"class": "gte"}},
				{TypeKey: "car", Key: "car-bad-enum", Name: "Wrong class",
					Fields: map[string]any{"power_hp": float64(500), "class": "kart"}},
				{TypeKey: "circuit", Key: "circuit-bad-bound", Name: "Too long",
					Fields: map[string]any{"country": "FR", "length_km": float64(99)}},
				{TypeKey: "driver", Key: "driver-bad-unknown", Name: "Typo",
					Fields: map[string]any{"nationality": "FR", "ratng": float64(50)}},
				{TypeKey: "circuit", Key: "circuit-good", Name: "Fine",
					Fields: map[string]any{"country": "FR"}},
			},
		})
		if err != nil {
			t.Fatalf("partial batch: %v", err)
		}
		if out.Count != 1 || len(out.Failed) != 4 {
			t.Fatalf("the partial batch wrote %d and failed %d, want 1 and 4", out.Count, len(out.Failed))
		}
		for _, f := range out.Failed {
			if f.Code != "schema_violation" {
				t.Fatalf("%+v, want schema_violation", f)
			}
		}
		// Clean up the one row that landed so the counts below stand.
		if _, err := web.MCPEntitiesRemove(ctx, s.deps, s.caller, s.game,
			web.EntitiesRemoveInput{ID: out.Written[0].ID.String()}); err != nil {
			t.Fatalf("entities.remove: %v", err)
		}

		// Endpoint rules refuse the pair they were declared to refuse,
		// and the relation type that declared none accepts anything.
		bad, err := web.MCPRelationsUpsert(ctx, s.deps, s.caller, s.game, web.RelationsUpsertInput{
			Mode: string(metamodel.BulkPartial),
			Items: []web.RelationItemInput{
				{TypeKey: "takes_place_in", Source: ref("driver", "driver-000"), Target: ref("circuit", "circuit-000")},
				{TypeKey: "drives", Source: ref("driver", "driver-000"), Target: ref("car", "car-001"),
					Fields: map[string]any{"seat": "spectator"}},
			},
		})
		if err != nil {
			t.Fatalf("bad-edge batch: %v", err)
		}
		if len(bad.Failed) != 2 {
			t.Fatalf("the bad-edge batch failed %d items, want 2: %+v", len(bad.Failed), bad.Failed)
		}
		if bad.Failed[0].Code != "endpoint_type_mismatch" {
			t.Fatalf("a driver at a race endpoint gave %+v", bad.Failed[0])
		}
		if bad.Failed[1].Code != "schema_violation" {
			t.Fatalf("an edge field outside its enum gave %+v", bad.Failed[1])
		}

		// An edge's own default landed on every edge that did not state
		// one, and the listing names both endpoints by ref.
		edges, err := web.MCPRelationsList(ctx, s.deps, s.caller, s.game,
			web.RelationsListInput{TypeKey: "mentions", Limit: 50})
		if err != nil {
			t.Fatalf("relations.list: %v", err)
		}
		if len(edges.Items) != 21 {
			t.Fatalf("mentions holds %d edges, want 21", len(edges.Items))
		}
		for _, e := range edges.Items {
			if e.Source == nil || e.Target == nil || e.Source.Name == "" {
				t.Fatalf("an edge came back without resolved endpoints: %+v", e)
			}
		}
	})

	// TestSeedARacingGameEndToEnd's job is to find out what seeding a real
	// game costs, so the gaps it found are pinned here rather than only
	// written up. Each of these passes today and describes something an
	// agent has to work around; a change that closes one of them should
	// fail here and be deleted from this list.
	t.Run("what the surface makes an agent do the long way", func(t *testing.T) {
		// 1. **An edge's field values cannot be read back.** `drives`
		// declares a schema, the seed wrote `seat` and `season` onto 120
		// edges, and every one of them was validated against that schema
		// — but RelationOutput carries no fields, there is no
		// relations.get, and relations.list is the only way to see an
		// edge. The values are stored and unreachable.
		edges, err := web.MCPRelationsList(ctx, s.deps, s.caller, s.game,
			web.RelationsListInput{TypeKey: "drives", Limit: 5})
		if err != nil {
			t.Fatalf("relations.list: %v", err)
		}
		if len(edges.Items) == 0 {
			t.Fatalf("no drives edges")
		}
		raw, err := json.Marshal(edges.Items[0])
		if err != nil {
			t.Fatalf("marshal edge: %v", err)
		}
		if strings.Contains(string(raw), "season") || strings.Contains(string(raw), "seat") {
			t.Fatalf("an edge now reports its fields; delete this case and the note above it: %s", raw)
		}

		// 2. **Nothing on this surface counts.** There is no tool that
		// answers "how many races are there"; the REST home page has
		// EntityCountsByType and an agent has only the listing, so a
		// count is a full walk of every page.
		pages, total := 0, 0
		in := web.EntitiesListInput{TypeKey: "race", Limit: 50}
		for {
			page, err := web.MCPEntitiesList(ctx, s.deps, s.caller, s.game, in)
			if err != nil {
				t.Fatalf("entities.list: %v", err)
			}
			pages, total = pages+1, total+len(page.Items)
			if page.NextCursor == nil {
				break
			}
			in.Cursor = *page.NextCursor
		}
		if total != seedRaces || pages != seedRaces/50+1 {
			t.Fatalf("counting %d races took %d pages", total, pages)
		}

		// 3. **Search does not index keys.** A designer who types the
		// handle they see everywhere else gets nothing, and has to know
		// to use entities.get instead.
		if hits := s.search(t, "circuit-000", ""); len(hits) != 0 {
			t.Fatalf("search now finds a row by its key; delete this case: %+v", hits)
		}

		// 4. **Two of the sixteen tools address rows by uuid only.**
		// entities.remove and relations.remove take an id, and
		// relations.list filters endpoints by id, so an agent holding the
		// (type key, key) every other tool speaks has to resolve it
		// first. Here is that extra round trip.
		race, err := web.MCPEntitiesGet(ctx, s.deps, s.caller, s.game,
			web.EntitiesGetInput{TypeKey: "race", Key: "race-000"})
		if err != nil {
			t.Fatalf("entities.get: %v", err)
		}
		out, err := web.MCPRelationsList(ctx, s.deps, s.caller, s.game,
			web.RelationsListInput{SourceID: race.ID.String(), Limit: 50})
		if err != nil {
			t.Fatalf("relations.list by source: %v", err)
		}
		if len(out.Items) != 3 {
			t.Fatalf("race-000 has %d outgoing edges, want 3", len(out.Items))
		}

		// 5. **A relation type states its endpoints as entity type ids**,
		// so a second seeding session — one that did not itself declare
		// the types and so never saw the ids — has to call types.list and
		// build the key-to-id map by hand before it can declare or edit a
		// single relation type.
		listed, err := web.MCPTypesList(ctx, s.deps, s.caller, s.game, web.TypesListInput{})
		if err != nil {
			t.Fatalf("types.list: %v", err)
		}
		byKey := map[string]uuid.UUID{}
		for _, row := range listed.Items {
			byKey[row.Key] = row.ID
		}
		detail, err := web.MCPRelationTypesGet(ctx, s.deps, s.caller, s.game,
			web.RelationTypesGetInput{Key: "takes_place_in"})
		if err != nil {
			t.Fatalf("relation_types.get: %v", err)
		}
		if len(detail.SourceTypeIDs) != 1 || detail.SourceTypeIDs[0] != byKey["race"] {
			t.Fatalf("a relation type's endpoints are stated as ids an agent must translate: %+v", detail)
		}
	})

	t.Run("the game home page renders this game", func(t *testing.T) {
		rec := s.get(t, "/api/games/"+s.game.String()+"/summary")
		if rec.Code != http.StatusOK {
			t.Fatalf("summary = %d: %s", rec.Code, rec.Body.String())
		}
		var summary web.GameSummaryOutput
		decodeBody(t, rec, &summary)

		if len(summary.EntityTypes) != 7 || len(summary.RelationTypes) != 8 {
			t.Fatalf("the summary lists %d entity types and %d relation types, want 7 and 8",
				len(summary.EntityTypes), len(summary.RelationTypes))
		}
		if summary.Totals.Entities != seedEntities {
			t.Fatalf("the summary counts %d entities, want %d", summary.Totals.Entities, seedEntities)
		}
		if summary.Totals.Invalid != 0 {
			t.Fatalf("the summary counts %d invalid rows on a game with none", summary.Totals.Invalid)
		}
		wantEdges := int64(seedRaces + seedRaces + seedDrivers + 2*seedCars + seedRaces +
			(seedLicences - 1) + seedDrivers + 21)
		if summary.Totals.Relations != wantEdges {
			t.Fatalf("the summary counts %d relations, want %d", summary.Totals.Relations, wantEdges)
		}
		counts := map[string]int64{}
		for _, row := range summary.EntityTypes {
			counts[row.Key] = row.EntityCount
			if row.InvalidCount != 0 {
				t.Fatalf("%s carries %d invalid rows", row.Key, row.InvalidCount)
			}
		}
		if counts["sponsor"] != seedSponsors || counts["race"] != seedRaces {
			t.Fatalf("the per-type counts read %+v", counts)
		}

		// The page itself: the real app.js, rendering this game's real
		// summary rather than a hand-written fixture.
		renderSummaryInBrowserStub(t, rec.Body.Bytes())
	})
}

// search is one search.* call, unwrapped.
func (s *seeded) search(t *testing.T, query, typeKey string) []web.SearchHit {
	t.Helper()
	out, err := web.MCPSearch(context.Background(), s.deps, s.caller, s.game,
		web.SearchInput{Query: query, TypeKey: typeKey, Limit: 50})
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	return out.Items
}

func boolp(v bool) *bool { return &v }

func versionOf(t *testing.T, rows []metamodel.BulkWrite, key string) int32 {
	t.Helper()
	for _, r := range rows {
		if r.Key == key {
			return r.Version
		}
	}
	t.Fatalf("no row %q in the batch report", key)
	return 0
}

// renderSummaryInBrowserStub drives internal/web/static/app.js over the
// summary this game actually produced.
//
// The existing harness (jstest/game_summary_test.mjs) proves the page
// renders *a* summary; it invents one. This proves it renders *this*
// one, which is the only way the two halves of the claim — the server
// counted the game correctly and the page shows what the server counted
// — are ever checked against the same numbers.
func renderSummaryInBrowserStub(t *testing.T, summary []byte) {
	t.Helper()
	nodeOrSkip(t)
	path := filepath.Join(t.TempDir(), "summary.json")
	if err := os.WriteFile(path, summary, 0o600); err != nil {
		t.Fatalf("write summary: %v", err)
	}
	cmd := exec.Command("node", "jstest/seeded_game_page_test.mjs", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("seeded_game_page_test.mjs failed: %v\n%s", err, out)
	}
	t.Log(string(out))
}

// get sends one authenticated GET. The REST fixture next door owns a
// game of its own, so it cannot be reused to read this one.
func (s *seeded) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(s.cookie)
	rec := httptest.NewRecorder()
	s.srv.ServeHTTP(rec, req)
	return rec
}
