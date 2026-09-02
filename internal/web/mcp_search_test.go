package web_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/web"
)

// seedSearchable declares a quest type, one quest named "Wanted: Hogger"
// and one script attached to it whose only distinctive word — "candle" —
// lives in its body and nowhere in the entity index. That asymmetry is
// the whole point of the surface: a designer searching a line of
// dialogue must find the document, and the document must name the quest
// it belongs to, because entity search deliberately does not reach into
// attached prose.
func seedSearchable(t *testing.T, f metamodelFixture) {
	t.Helper()
	ctx := context.Background()

	if _, err := web.MCPTypesUpsert(ctx, f.deps, f.caller, f.game, web.TypesUpsertInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	}); err != nil {
		t.Fatalf("MCPTypesUpsert: %v", err)
	}
	if _, err := web.MCPEntitiesUpsert(ctx, f.deps, f.caller, f.game, web.EntitiesUpsertInput{
		Items: []web.EntityItemInput{{TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger"}},
	}); err != nil {
		t.Fatalf("MCPEntitiesUpsert: %v", err)
	}
	if _, err := f.markdown.Write(ctx, f.game, markdown.WriteInput{
		Path:            "scripts/hogger",
		Content:         "---\ntitle: \"The Confrontation\"\n---\nHOGGER: You no take candle!\n",
		Kind:            ptrStr("script"),
		ExpectedVersion: ptrInt32Web(0),
		Links: &[]markdown.LinkTarget{
			{EntityType: "quest", EntityKey: "hogger", Role: "script"},
		},
	}); err != nil {
		t.Fatalf("seed the script: %v", err)
	}
}

func ptrStr(v string) *string { return &v }

func ptrInt32Web(v int32) *int32 { return &v }

// TestSearchReturnsBothKindsInOneLabelledList is the read-back this
// task's feature owes: two indexes, one ranked list, each hit labelled,
// and a document hit carrying the way back to the entity it is attached
// to.
func TestSearchReturnsBothKindsInOneLabelledList(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()
	seedSearchable(t, f)

	// A word from the quest's own name that the script does not carry: an
	// entity hit, and nothing else.
	byName, err := web.MCPSearch(ctx, f.deps, f.caller, f.game, web.SearchInput{Query: "wanted"})
	if err != nil {
		t.Fatalf("MCPSearch: %v", err)
	}
	if len(byName.Items) != 1 || byName.Items[0].Kind != "entity" ||
		byName.Items[0].Entity == nil || byName.Items[0].Entity.Key != "hogger" {
		t.Fatalf("searching the quest's name gave %+v, want one entity hit", byName.Items)
	}
	if byName.Items[0].Document != nil {
		t.Fatalf("an entity hit carries a document half: %+v", byName.Items[0])
	}

	// A line of dialogue: a document hit, and it names the quest.
	byLine, err := web.MCPSearch(ctx, f.deps, f.caller, f.game, web.SearchInput{Query: "candle"})
	if err != nil {
		t.Fatalf("MCPSearch: %v", err)
	}
	if len(byLine.Items) != 1 || byLine.Items[0].Kind != "document" {
		t.Fatalf("searching a line of dialogue gave %+v, want one document hit", byLine.Items)
	}
	hit := byLine.Items[0]
	if hit.Entity != nil {
		t.Fatalf("a document hit carries an entity half: %+v", hit)
	}
	doc := hit.Document
	if doc == nil || doc.Path != "scripts/hogger" || doc.Title != "The Confrontation" ||
		doc.DocKind != "script" || doc.Version != 1 || doc.ID == uuid.Nil {
		t.Fatalf("document half = %+v, want the script read back in full", doc)
	}
	if len(doc.LinkedTo) != 1 || doc.LinkedTo[0].EntityKey != "hogger" ||
		doc.LinkedTo[0].EntityTypeKey != "quest" || doc.LinkedTo[0].Name != "Wanted: Hogger" ||
		doc.LinkedTo[0].Role != "script" {
		t.Fatalf("linked_to = %+v, want the quest the script belongs to", doc.LinkedTo)
	}

	// One query that both indexes answer — the quest is *named* Hogger
	// and the script's dialogue says it — so: one list, both labels.
	both, err := web.MCPSearch(ctx, f.deps, f.caller, f.game,
		web.SearchInput{Query: "hogger"})
	if err != nil {
		t.Fatalf("MCPSearch: %v", err)
	}
	if len(both.Items) != 2 {
		t.Fatalf("%d hits for a query both indexes answer, want 2: %+v", len(both.Items), both.Items)
	}
	kinds := map[string]int{}
	for _, item := range both.Items {
		kinds[item.Kind]++
	}
	if kinds["entity"] != 1 || kinds["document"] != 1 {
		t.Fatalf("kinds = %v, want one of each in the one list", kinds)
	}
}

func TestSearchNarrowsToOneKind(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()
	seedSearchable(t, f)

	// The one word both indexes answer, so each narrowing has something
	// of the other kind to have dropped.
	const query = "hogger"
	for _, tc := range []struct{ kind, want string }{
		{"entity", "entity"},
		{"document", "document"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			out, err := web.MCPSearch(ctx, f.deps, f.caller, f.game,
				web.SearchInput{Query: query, Kind: tc.kind})
			if err != nil {
				t.Fatalf("MCPSearch: %v", err)
			}
			if len(out.Items) != 1 {
				t.Fatalf("%d hits, want the one of kind %q: %+v", len(out.Items), tc.want, out.Items)
			}
			if out.Items[0].Kind != tc.want {
				t.Fatalf("kind = %q, want %q", out.Items[0].Kind, tc.want)
			}
		})
	}
}

// TestSearchNarrowsDocumentsByDocKind is the doc_kind arm, which nothing
// else on this surface reaches: kind:"document" alone would answer the
// same for a fixture with one document, so a second document of another
// kind is what makes the filter visible.
func TestSearchNarrowsDocumentsByDocKind(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()
	seedSearchable(t, f)
	if _, err := f.markdown.Write(ctx, f.game, markdown.WriteInput{
		Path: "lore/candles", Content: "Candle-making in Elwynn.\n",
		Kind: ptrStr("lore"), ExpectedVersion: ptrInt32Web(0),
	}); err != nil {
		t.Fatalf("write the lore page: %v", err)
	}

	all, err := web.MCPSearch(ctx, f.deps, f.caller, f.game, web.SearchInput{Query: "candle"})
	if err != nil {
		t.Fatalf("MCPSearch: %v", err)
	}
	if len(all.Items) != 2 {
		t.Fatalf("%d hits with no doc_kind, want 2: %+v", len(all.Items), all.Items)
	}
	narrowed, err := web.MCPSearch(ctx, f.deps, f.caller, f.game,
		web.SearchInput{Query: "candle", DocKind: "script"})
	if err != nil {
		t.Fatalf("MCPSearch: %v", err)
	}
	if len(narrowed.Items) != 1 || narrowed.Items[0].Document == nil ||
		narrowed.Items[0].Document.Path != "scripts/hogger" {
		t.Fatalf("doc_kind narrowed to %+v, want only the script", narrowed.Items)
	}
}

// TestAnUnrecognisedSearchKindIsRefused, and its two neighbours below,
// are the "answer a question the caller did not ask" cases: every one of
// them would otherwise be a full, plausible answer to something else.
func TestAnUnrecognisedSearchKindIsRefused(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()
	seedSearchable(t, f)

	for _, tc := range []struct {
		name    string
		in      web.SearchInput
		path    string
		message string
	}{
		{"an unrecognised kind", web.SearchInput{Query: "hogger", Kind: "quest"},
			"kind", `must be "entity", "document", or omitted for both`},
		{"a type_key against documents",
			web.SearchInput{Query: "hogger", Kind: "document", TypeKey: "quest"},
			"type_key", `cannot be combined with kind "document"`},
		{"a doc_kind against entities",
			web.SearchInput{Query: "hogger", Kind: "entity", DocKind: "script"},
			"doc_kind", `cannot be combined with kind "entity"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := web.MCPSearch(ctx, f.deps, f.caller, f.game, tc.in)
			requireMCPFieldError(t, err, tc.path, tc.message)
		})
	}
}

// requireMCPFieldError asserts a refusal carries invalid_input, the
// argument's own path and the exact message. Asserting only that an
// error happened would pass for the wrong reason on every one of the
// three cases above, which differ from each other only in which argument
// they name.
func requireMCPFieldError(t *testing.T, err error, wantPath, wantMessage string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want a refusal at %q, got nil", wantPath)
	}
	var mcpErr *web.MCPError
	if !errors.As(err, &mcpErr) {
		t.Fatalf("want a *web.MCPError, got %#v", err)
	}
	if mcpErr.Code != "invalid_input" {
		t.Fatalf("code = %q, want invalid_input: %v", mcpErr.Code, err)
	}
	fields, _ := mcpErr.Details["fields"].([]map[string]string)
	for _, field := range fields {
		if field["path"] == wantPath && strings.Contains(field["message"], wantMessage) {
			return
		}
	}
	t.Fatalf("no problem at %q containing %q; got %v", wantPath, wantMessage, mcpErr.Details)
}

// TestSearchRanksANamedHitAboveAMentionAcrossKinds is the cross-index
// case the merge exists for: a document *titled* with the query beside
// an entity that only mentions the words, repeated until ts_rank alone
// would put it first. The named document has to come first, and it has
// to be first because of name_match rather than because of rank — which
// is why the test also asserts the inversion, that the mentioning
// entity carries the higher rank of the two.
func TestSearchRanksANamedHitAboveAMentionAcrossKinds(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	if _, err := web.MCPTypesUpsert(ctx, f.deps, f.caller, f.game, web.TypesUpsertInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: []web.FieldInput{{Key: "summary", Type: "longtext"}},
	}); err != nil {
		t.Fatalf("MCPTypesUpsert: %v", err)
	}
	if _, err := web.MCPEntitiesUpsert(ctx, f.deps, f.caller, f.game, web.EntitiesUpsertInput{
		Items: []web.EntityItemInput{{TypeKey: "quest", Key: "mentioned", Name: "Wanted: Hogger",
			Fields: map[string]any{"summary": strings.TrimSpace(strings.Repeat(
				"A gnoll pack camp led by a gnoll pack chieftain. ", 500))}}},
	}); err != nil {
		t.Fatalf("MCPEntitiesUpsert: %v", err)
	}
	if _, err := f.markdown.Write(ctx, f.game, markdown.WriteInput{
		Path: "lore/the-pack", Content: "# The Gnoll Pack\n\nA short note.\n",
		ExpectedVersion: ptrInt32Web(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	out, err := web.MCPSearch(ctx, f.deps, f.caller, f.game, web.SearchInput{Query: "gnoll pack"})
	if err != nil {
		t.Fatalf("MCPSearch: %v", err)
	}
	if len(out.Items) != 2 {
		t.Fatalf("items = %+v, want both", out.Items)
	}
	if out.Items[0].Kind != "document" || out.Items[0].Document == nil ||
		out.Items[0].Document.Path != "lore/the-pack" || !out.Items[0].NameMatch {
		t.Fatalf("first hit = %+v, want the document the query names, flagged", out.Items[0])
	}
	if out.Items[1].Kind != "entity" || out.Items[1].NameMatch {
		t.Fatalf("second hit = %+v, want the entity that only mentions the words", out.Items[1])
	}
	// The half the position alone cannot show: rank does not explain this
	// order, so a merge sorting on rank alone would invert it.
	if out.Items[1].Rank <= out.Items[0].Rank {
		t.Fatalf("ranks = [%v %v]: this fixture no longer proves that name_match, and not "+
			"rank, is what puts the named hit first", out.Items[0].Rank, out.Items[1].Rank)
	}
	// And the field the order is built from agrees with the order, for
	// every adjacent pair — the assertion that generalises past this
	// fixture.
	for i := 1; i < len(out.Items); i++ {
		if !out.Items[i-1].NameMatch && out.Items[i].NameMatch {
			t.Fatalf("items[%d].NameMatch = false sorted before items[%d].NameMatch = true: "+
				"the wire order and the wire field disagree", i-1, i)
		}
	}
}

// TestTwoHitsOfEqualRankKeepOneOrderAcrossIdenticalCalls pins that a
// search answers identically twice when its ranking cannot break the
// tie: forty documents whose titles satisfy the query in exactly the
// same way, so name_match and rank are equal across all of them and
// nothing but the merge's own determinism decides the order. It would
// catch a merge ordered by anything with no order of its own — a map
// iteration, say.
//
// **What it does not catch, recorded rather than implied: replacing
// sort.SliceStable with sort.Slice in searchContent leaves it green**,
// and so does every other test in this file. Both were run, at
// -count=20 and -count=5. The reason is structural rather than a fixture
// weakness: each side of the merge arrives already ordered by the exact
// key the merge sorts on, so the concatenated slice is always
// non-decreasing, and Go's pdqsort does not move a run it never has to
// reorder — nor does it move anything for an array whose elements all
// compare equal. The only input that could tell the two apart is an
// entity hit and a document hit of *identical* (name_match, rank) with
// a differently-ranked hit between them, which depends on two ts_rank
// values from two differently-built vectors landing on the same float.
// sort.SliceStable stays because the stable order is the one the two
// queries' own tie-breaks (name then id; title then id) were written to
// produce, but this suite does not pin the choice.
func TestTwoHitsOfEqualRankKeepOneOrderAcrossIdenticalCalls(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	// Two documents whose titles satisfy the query identically: same
	// name_match, same ts_rank, ordered only by the query's own
	// `title, id` tie-break.
	// Forty of them, not four: Go's sort.Slice falls back to insertion
	// sort below a dozen elements, which *is* stable, so a small fixture
	// cannot tell an unstable sort from a stable one at all. Forty is
	// past that threshold and into pdqsort's partitioning.
	const equalRanked = 40
	for i := 0; i < equalRanked; i++ {
		if _, err := f.markdown.Write(ctx, f.game, markdown.WriteInput{
			Path:            fmt.Sprintf("lore/doc-%03d", i),
			Content:         fmt.Sprintf("# Note %03d on the gnoll pack\n\nA note.\n", i),
			ExpectedVersion: ptrInt32Web(0),
		}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	var first []string
	for call := 0; call < 20; call++ {
		out, err := web.MCPSearch(ctx, f.deps, f.caller, f.game, web.SearchInput{Query: "gnoll pack"})
		if err != nil {
			t.Fatalf("MCPSearch: %v", err)
		}
		order := make([]string, 0, len(out.Items))
		for _, item := range out.Items {
			if item.Document == nil {
				t.Fatalf("hit %+v is not a document hit", item)
			}
			order = append(order, item.Document.Path)
		}
		if len(order) != equalRanked {
			t.Fatalf("%d hits, want all %d documents", len(order), equalRanked)
		}
		if call == 0 {
			first = order
			continue
		}
		if strings.Join(order, ",") != strings.Join(first, ",") {
			t.Fatalf("call %d answered %v, call 0 answered %v: two identical calls must "+
				"answer identically", call, order, first)
		}
	}
}

// TestAnEmptySearchAnswerIsAnEmptyListAndNotNull is the other end of the
// same decision the listings make: "this game holds nothing matching"
// and "the server sent no list" are different statements, and only the
// marshalled JSON can tell them apart.
func TestAnEmptySearchAnswerIsAnEmptyListAndNotNull(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()
	seedSearchable(t, f)

	out, err := web.MCPSearch(ctx, f.deps, f.caller, f.game,
		web.SearchInput{Query: "murloc"})
	if err != nil {
		t.Fatalf("MCPSearch: %v", err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"items":[]`) {
		t.Fatalf("an empty answer marshals as %s, want an empty array", raw)
	}
}

// TestASearchNeverLeavesTheTokensGame is the isolation the whole surface
// rests on, asserted at the new half: the document index is filtered by
// the same project id the entity one is, and a token bound to one game
// finds neither the other game's entities nor its prose.
func TestASearchNeverLeavesTheTokensGame(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()
	seedSearchable(t, f)

	if _, err := web.MCPSearch(ctx, f.deps, f.caller, f.other,
		web.SearchInput{Query: "candle"}); err == nil {
		t.Fatal("a token searched another game and was answered")
	}
}

// TestTheSearchWireTypesCarryExactlyTheseKeys is
// TestTheDomainTypesOnTheWireCarryExactlyTheseKeys' mechanism applied to
// the three types this task put on the wire. `search` is a tool that
// already shipped, and this commit changed its answer's shape: what has
// to hold from here on is that a field added to any of the three is
// decided in the open rather than discovered by an agent.
//
// Every optional key is given a value, because the marshaller omits the
// empty ones and a zero value would pin half the contract.
func TestTheSearchWireTypesCarryExactlyTheseKeys(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  []string
	}{
		{"web.SearchHit", web.SearchHit{
			Kind: "entity", NameMatch: true, Rank: 1,
			Entity: &web.EntityOutput{}, Document: &web.DocumentHitOutput{},
		}, []string{"document", "entity", "kind", "name_match", "rank"}},
		{"web.DocumentHitOutput", web.DocumentHitOutput{},
			[]string{"doc_kind", "id", "linked_to", "path", "summary", "title", "version"}},
		{"web.LinkedRef", web.LinkedRef{Role: "script"},
			[]string{"entity_key", "entity_type_key", "name", "role"}},
		{"web.SearchOutput", web.SearchOutput{}, []string{"items", "truncated"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var keyed map[string]json.RawMessage
			if err := json.Unmarshal(raw, &keyed); err != nil {
				t.Fatalf("decode %s: %v", raw, err)
			}
			got := make([]string, 0, len(keyed))
			for k := range keyed {
				got = append(got, k)
			}
			slices.Sort(got)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("wire keys = %v, want %v — a search wire type grew a field; decide "+
					"whether an agent should see it, then update this list and the "+
					"hand-written output schema beside it", got, tc.want)
			}
		})
	}
}

// TestTheRESTMirrorAnswersWithTheSameLabelledHits reads the change back
// through the other public surface. The two are one core
// (searchContent), and a mirror that was never called is a mirror that
// compiles.
func TestTheRESTMirrorAnswersWithTheSameLabelledHits(t *testing.T) {
	f := newRESTFixture(t)
	ctx := context.Background()

	if _, err := f.md.Write(ctx, f.game, markdown.WriteInput{
		Path:            "scripts/hogger",
		Content:         "---\ntitle: \"The Confrontation\"\n---\nHOGGER: You no take candle!\n",
		Kind:            ptrStr("script"),
		ExpectedVersion: ptrInt32Web(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	rec := f.as(t, http.MethodGet, "/search?query=candle", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Items []struct {
			Kind     string `json:"kind"`
			Document *struct {
				Path    string `json:"path"`
				DocKind string `json:"doc_kind"`
			} `json:"document"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if len(out.Items) != 1 || out.Items[0].Kind != "document" ||
		out.Items[0].Document == nil || out.Items[0].Document.Path != "scripts/hogger" {
		t.Fatalf("REST search = %s, want the labelled document hit", rec.Body.String())
	}

	// The filters the mirror has to read for itself, since a parameter a
	// person cannot spell in a URL is a filter this surface does not
	// have.
	narrowed := f.as(t, http.MethodGet, "/search?query=candle&kind=entity", nil)
	if err := json.Unmarshal(narrowed.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", narrowed.Body.String(), err)
	}
	if len(out.Items) != 0 {
		t.Fatalf("kind=entity over the REST mirror returned %s", narrowed.Body.String())
	}
	refused := f.as(t, http.MethodGet, "/search?query=candle&kind=quest", nil)
	if refused.Code != http.StatusBadRequest ||
		!strings.Contains(refused.Body.String(), "invalid_input") {
		t.Fatalf("an unrecognised kind over REST = %d %s", refused.Code, refused.Body.String())
	}
}

// TestADocumentHitPassesTheToolsOwnOutputSchema is the half no
// direct-function test reaches: the SDK validates a tool's result
// against the hand-written OutputSchema, and a document hit had never
// been through it — the entity half is exercised by
// TestMCPMetamodelToolsAreServedOverTheRealTransport, which seeds no
// prose. A Required list naming a key the document half does not send
// fails here and nowhere else.
func TestADocumentHitPassesTheToolsOwnOutputSchema(t *testing.T) {
	f := newMetamodelFixture(t)
	httpSrv := httptest.NewServer(f.srv)
	defer httpSrv.Close()
	seedSearchable(t, f)

	session := connectMCP(t, httpSrv.URL, f.token)
	var out struct {
		Items []struct {
			Kind     string `json:"kind"`
			Document *struct {
				Path     string `json:"path"`
				LinkedTo []struct {
					EntityKey string `json:"entity_key"`
					Role      string `json:"role"`
				} `json:"linked_to"`
			} `json:"document"`
		} `json:"items"`
	}
	decodeStructured(t, callOK(t, session, "search", map[string]any{"query": "candle"}), &out)
	if len(out.Items) != 1 || out.Items[0].Kind != "document" || out.Items[0].Document == nil {
		t.Fatalf("search over the transport = %+v, want one document hit", out.Items)
	}
	doc := out.Items[0].Document
	if doc.Path != "scripts/hogger" || len(doc.LinkedTo) != 1 ||
		doc.LinkedTo[0].EntityKey != "hogger" || doc.LinkedTo[0].Role != "script" {
		t.Fatalf("document hit over the transport = %+v", doc)
	}
}
