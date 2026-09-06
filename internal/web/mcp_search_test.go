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
	"github.com/neverbot/maestro/internal/metamodel"
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
// sort.SliceStable with sort.Slice in searchContent leaves *this* test
// green**, at -count=20 and -count=5. All its ties are same-kind
// (document vs. document), so the input this fixture hands the merge is
// already in the order the merge produces, and pdqsort moves nothing. A
// fixture that ties *across* kinds does catch it —
// TestAFullReversalOfTheConcatenationStillKeepsBothTieBreaks, whose own
// comment carries the measurement and is red against sort.Slice, proved
// by hand. sort.SliceStable stays because the stable order is the one
// the two queries' own tie-breaks (name then id; title then id) were
// written to produce, and that other test is what actually pins the
// choice — this one only pins the narrower, same-kind case.
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

// TestAMergedAnswerIsCutAtTheLimitAndSaysSo is the merge's own bound.
// Each side returns up to `limit` on its own, so the concatenation can
// be twice that, and only this cut keeps the promise the limit makes.
// The hit that survives is the top of the merged ranking and not the
// top of either side's — the entity ranks below the document here, and
// a cut applied per side rather than after the merge would keep both.
func TestAMergedAnswerIsCutAtTheLimitAndSaysSo(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()
	seedSearchable(t, f)

	out, err := web.MCPSearch(ctx, f.deps, f.caller, f.game,
		web.SearchInput{Query: "hogger", Limit: 1})
	if err != nil {
		t.Fatalf("MCPSearch: %v", err)
	}
	if len(out.Items) != 1 {
		t.Fatalf("%d hits for limit 1, want the merged answer cut to one: %+v",
			len(out.Items), out.Items)
	}
	if !out.Truncated {
		t.Fatal("an answer that filled the limit must say so: there is no cursor to be absent")
	}
	// The quest is *named* Hogger and the script only says the word, so
	// the entity is what a merge-then-cut keeps.
	if out.Items[0].Kind != "entity" || !out.Items[0].NameMatch {
		t.Fatalf("the surviving hit is %+v, want the top of the merged ranking", out.Items[0])
	}
	// And the same query under a limit both hits fit inside is not
	// reported as truncated, so `truncated` is not simply always true.
	roomy, err := web.MCPSearch(ctx, f.deps, f.caller, f.game,
		web.SearchInput{Query: "hogger", Limit: 5})
	if err != nil {
		t.Fatalf("MCPSearch: %v", err)
	}
	if len(roomy.Items) != 2 || roomy.Truncated {
		t.Fatalf("two hits under a limit of five = %+v, truncated %v", roomy.Items, roomy.Truncated)
	}
}

// TestAFullReversalOfTheConcatenationStillKeepsBothTieBreaks is the
// fixture the "unpinnable" claim on sort.SliceStable's call site did not
// have, and does not survive: that comment said the concatenation the
// merge sorts is always non-decreasing because "each side arrives
// already ordered by the exact key the merge sorts on", which only
// holds when the entity block happens to rank above the document block.
//
// It does not here. Twenty entities that only mention the query in a
// field (name_match false) rank at 0.39641288; twenty documents that
// only mention it in their body (name_match false too) rank at
// 0.648798 — a single identical mention sentence, scored differently by
// the two vectors (see the tool description's own note on that). Search
// appends the entity block first and the document block second
// (searchContent's own order), so the concatenation this hands to the
// merge is [rank 0.396 x20][rank 0.649 x20]: the exact reverse of the
// order the merge produces, and about as unsorted as a same-length
// slice can be — every element has to cross every element of the other
// block.
//
// That is real work for the sort, and it is where the claim breaks:
// with sort.Slice in place of sort.SliceStable, this is red — proved by
// hand, not asserted here, because the break is nondeterministic across
// runs and pdqsort's internals are not this suite's to pin. What this
// test pins is the promise that survives *because* the sort is stable:
// documents.sql promises document ties break by title then id, and
// metamodel.sql promises entity ties break by name then id, and both
// tie-break orders — set by each query's own ORDER BY — must still hold
// after the merge despite the full reversal above.
func TestAFullReversalOfTheConcatenationStillKeepsBothTieBreaks(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	if _, err := web.MCPTypesUpsert(ctx, f.deps, f.caller, f.game, web.TypesUpsertInput{
		Key: "camp", Label: "Camp", LabelPlural: "Camps",
		Schema: []web.FieldInput{{Key: "summary", Type: "longtext"}},
	}); err != nil {
		t.Fatalf("MCPTypesUpsert: %v", err)
	}

	const mention = "A gnoll pack raider was seen nearby."
	const groupSize = 20

	items := make([]web.EntityItemInput, 0, groupSize)
	for i := 0; i < groupSize; i++ {
		items = append(items, web.EntityItemInput{
			TypeKey: "camp", Key: fmt.Sprintf("camp-%02d", i), Name: fmt.Sprintf("Camp %02d", i),
			Fields: map[string]any{"summary": mention},
		})
	}
	if _, err := web.MCPEntitiesUpsert(ctx, f.deps, f.caller, f.game, web.EntitiesUpsertInput{
		Items: items,
	}); err != nil {
		t.Fatalf("MCPEntitiesUpsert: %v", err)
	}
	for i := 0; i < groupSize; i++ {
		if _, err := f.markdown.Write(ctx, f.game, markdown.WriteInput{
			Path:            fmt.Sprintf("lore/note-%02d", i),
			Content:         fmt.Sprintf("# Note %02d\n\n%s\n", i, mention),
			ExpectedVersion: ptrInt32Web(0),
		}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	out, err := web.MCPSearch(ctx, f.deps, f.caller, f.game,
		web.SearchInput{Query: "gnoll pack", Limit: 2 * groupSize})
	if err != nil {
		t.Fatalf("MCPSearch: %v", err)
	}
	if len(out.Items) != 2*groupSize {
		t.Fatalf("%d hits, want both groups in full: %+v", len(out.Items), out.Items)
	}

	// The higher-ranked block (documents, 0.649) must lead the
	// lower-ranked one (entities, 0.396): the reversal is real, not just
	// a same-order coincidence.
	for i, item := range out.Items[:groupSize] {
		if item.Kind != "document" || item.Document == nil {
			t.Fatalf("item %d = %+v, want a document (the higher-ranked block)", i, item)
		}
		wantTitle := fmt.Sprintf("Note %02d", i)
		if item.Document.Title != wantTitle {
			t.Fatalf("document block[%d].Title = %q, want %q: title-then-id tie-break not "+
				"preserved across the merge", i, item.Document.Title, wantTitle)
		}
	}
	for i, item := range out.Items[groupSize:] {
		if item.Kind != "entity" || item.Entity == nil {
			t.Fatalf("item %d = %+v, want an entity (the lower-ranked block)", groupSize+i, item)
		}
		wantName := fmt.Sprintf("Camp %02d", i)
		if item.Entity.Name != wantName {
			t.Fatalf("entity block[%d].Name = %q, want %q: name-then-id tie-break not "+
				"preserved across the merge", i, item.Entity.Name, wantName)
		}
	}
}

// TestAnExplicitDocumentSearchIsRefusedWithoutTheMarkdownService is
// review finding L4 on Task 9: kind "document" against an instance
// built with a metamodel service and no markdown one used to answer 200
// with an empty items list — "this game holds no matching prose" — when
// the true statement is "this instance serves no prose at all". kind ""
// (both) still degrades to entities alone, which is deliberate and
// unchanged; only an explicit request for documents that cannot be
// served is refused. cmd/maestro always builds both services, so this
// shape is unreachable in the shipped binary today — it is pinned ahead
// of Task 10, which registers twelve more tools behind the same
// optional field, and Task 11, which mirrors them over REST.
func TestAnExplicitDocumentSearchIsRefusedWithoutTheMarkdownService(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	noMarkdown := f.deps
	noMarkdown.Markdown = nil

	_, err := web.MCPSearch(ctx, noMarkdown, f.caller, f.game,
		web.SearchInput{Query: "gnoll", Kind: "document"})
	var domainErr *web.MCPError
	if !errors.As(err, &domainErr) || domainErr.Code != "not_found" {
		t.Fatalf("MCPSearch(kind=document, no markdown service) = %v, want a not_found MCPError", err)
	}

	// kind "" is unaffected: it still degrades to entities alone rather
	// than failing.
	both, err := web.MCPSearch(ctx, noMarkdown, f.caller, f.game, web.SearchInput{Query: "gnoll"})
	if err != nil {
		t.Fatalf("MCPSearch(kind=\"\", no markdown service): %v", err)
	}
	if len(both.Items) != 0 {
		t.Fatalf("no entities matched \"gnoll\", got %+v", both.Items)
	}
}

// TestASearchOmitsFieldsUnlessAskedToBeVerbose pins the flag Task 9's
// seeding run found missing, on both surfaces and over the wire.
//
// The measurement that made it necessary: against a game whose rows carry
// 25 KB of lore each, one sixty-hit search answered with 1.6 MB of JSON,
// because there was no argument that turned the payload off. The fixture
// here is that shape in miniature — one row carrying a long briefing —
// and it asserts the two things that matter: without the flag the hit
// carries no `fields` key *at all* on the wire (not an empty object,
// which a client would have to tell apart from a row with no values),
// and with it the whole payload comes back.
func TestASearchOmitsFieldsUnlessAskedToBeVerbose(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	if _, err := web.MCPTypesUpsert(ctx, f.deps, f.caller, f.game, web.TypesUpsertInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: []web.FieldInput{{Key: "briefing", Type: "longtext"}},
	}); err != nil {
		t.Fatalf("MCPTypesUpsert: %v", err)
	}
	briefing := strings.Repeat("The kerbstone at the exit is the whole lap. ", 200)
	if _, err := web.MCPEntitiesUpsert(ctx, f.deps, f.caller, f.game, web.EntitiesUpsertInput{
		Items: []web.EntityItemInput{{
			TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
			Fields: map[string]any{"briefing": briefing},
		}},
	}); err != nil {
		t.Fatalf("MCPEntitiesUpsert: %v", err)
	}

	plain, err := web.MCPSearch(ctx, f.deps, f.caller, f.game, web.SearchInput{Query: "wanted"})
	if err != nil {
		t.Fatalf("MCPSearch: %v", err)
	}
	if len(plain.Items) != 1 || plain.Items[0].Entity == nil {
		t.Fatalf("search = %+v, want one entity hit", plain.Items)
	}
	if plain.Items[0].Entity.Fields != nil {
		t.Fatalf("a search nobody asked to be verbose carried fields: %+v", plain.Items[0].Entity)
	}
	// Identity is still whole: what a caller needs to choose which hit to
	// read is exactly what a non-verbose hit keeps.
	if got := plain.Items[0].Entity; got.Key != "hogger" || got.TypeKey != "quest" ||
		got.Name != "Wanted: Hogger" || got.Version != 1 {
		t.Fatalf("a non-verbose hit lost part of its identity: %+v", got)
	}
	raw, err := json.Marshal(plain.Items[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "fields") {
		t.Fatalf("a non-verbose hit carries a fields key on the wire: %s", raw)
	}

	verbose, err := web.MCPSearch(ctx, f.deps, f.caller, f.game,
		web.SearchInput{Query: "wanted", Verbose: true})
	if err != nil {
		t.Fatalf("MCPSearch verbose: %v", err)
	}
	if len(verbose.Items) != 1 || verbose.Items[0].Entity == nil ||
		verbose.Items[0].Entity.Fields["briefing"] != briefing {
		t.Fatalf("a verbose search left the row's values out: %+v", verbose.Items)
	}
}

// TestTheRESTMirrorTakesTheSameVerboseFlag is the other half of the flag:
// a search that could only be asked for fields over MCP would be a mirror
// answering a different question from the surface it mirrors. It goes
// through queryBool, so it takes the same four true spellings the two
// listings take and refuses the same value-less parameter.
func TestTheRESTMirrorTakesTheSameVerboseFlag(t *testing.T) {
	f := newRESTFixture(t)
	ctx := context.Background()

	if _, err := f.mm.UpsertEntityType(ctx, f.game, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: metamodel.Schema{{Key: "briefing", Type: metamodel.FieldText}},
	}); err != nil {
		t.Fatalf("UpsertEntityType: %v", err)
	}
	if _, err := f.mm.UpsertEntity(ctx, f.game, metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
		Fields: map[string]any{"briefing": "Defeat the gnoll chieftain."},
	}); err != nil {
		t.Fatalf("UpsertEntity: %v", err)
	}

	read := func(t *testing.T, path string) map[string]json.RawMessage {
		t.Helper()
		rec := f.as(t, http.MethodGet, path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
		}
		var out struct {
			Items []struct {
				Entity map[string]json.RawMessage `json:"entity"`
			} `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
		if len(out.Items) != 1 {
			t.Fatalf("GET %s answered with %s, want one hit", path, rec.Body.String())
		}
		return out.Items[0].Entity
	}

	if _, present := read(t, "/search?query=wanted")["fields"]; present {
		t.Fatalf("the REST mirror is verbose by default")
	}
	if fields, present := read(t, "/search?query=wanted&verbose=true")["fields"]; !present ||
		!strings.Contains(string(fields), "gnoll chieftain") {
		t.Fatalf("verbose=true over REST returned %s", fields)
	}
	refused := f.as(t, http.MethodGet, "/search?query=wanted&verbose=", nil)
	if refused.Code != http.StatusBadRequest ||
		!strings.Contains(refused.Body.String(), "invalid_input") {
		t.Fatalf("?verbose= over REST = %d %s", refused.Code, refused.Body.String())
	}
}

// TestSearchFindsAnEntityByItsKeyOverTheToolSurface carries 0010's change
// one step along: the domain test proves the vector holds the key, and
// this proves an agent calling the tool gets the row.
//
// It also pins where the key went. The row is found by its handle and
// comes back with name_match false, because 0010 put the key at label C
// and left `name_match` asking about the name alone — the property that
// keeps "a row the query names outranks a row that mentions the words" a
// guarantee, which a keyed-by-counter catalogue would otherwise flood.
func TestSearchFindsAnEntityByItsKeyOverTheToolSurface(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()
	seedSearchable(t, f)

	hits, err := web.MCPSearch(ctx, f.deps, f.caller, f.game, web.SearchInput{Query: "hogger"})
	if err != nil {
		t.Fatalf("MCPSearch: %v", err)
	}
	// "hogger" is both the entity's key and a word of its name, and it is
	// also a word of the attached script's body, so this call proves the
	// key does not break the merge either.
	var entity *web.SearchHit
	for i, hit := range hits.Items {
		if hit.Kind == "entity" {
			entity = &hits.Items[i]
			break
		}
	}
	if entity == nil || entity.Entity.Key != "hogger" || !entity.NameMatch {
		t.Fatalf("searching a word of the name gave %+v, want the named entity first", hits.Items)
	}

	// And a handle that is nothing but a handle. The script's body does
	// not carry it and neither does any name.
	if _, err := web.MCPEntitiesUpsert(ctx, f.deps, f.caller, f.game, web.EntitiesUpsertInput{
		Items: []web.EntityItemInput{{TypeKey: "quest", Key: "circuit-000", Name: "Silverpine Straight"}},
	}); err != nil {
		t.Fatalf("MCPEntitiesUpsert: %v", err)
	}
	byKey, err := web.MCPSearch(ctx, f.deps, f.caller, f.game, web.SearchInput{Query: "circuit-000"})
	if err != nil {
		t.Fatalf("MCPSearch: %v", err)
	}
	if len(byKey.Items) != 1 || byKey.Items[0].Entity == nil ||
		byKey.Items[0].Entity.Key != "circuit-000" {
		t.Fatalf("searching a row's key gave %+v, want that row", byKey.Items)
	}
	if byKey.Items[0].NameMatch {
		t.Fatalf("a hit found by its key alone claims name_match: %+v", byKey.Items[0])
	}
}
