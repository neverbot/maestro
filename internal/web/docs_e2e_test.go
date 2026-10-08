package web_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/roles"
	"github.com/neverbot/maestro/internal/web"
)

// This file is the prose sub-project's definition of done, and it is a
// different kind of test from every other file beside it: those prove
// one call behaves, this drives a whole game's worth of writing the way
// an agent and a designer actually would — over the MCP tools and the
// REST routes the reading view calls, never through markdown.Service —
// and then reads all of it back.

const (
	// The twelve quest scripts, and the one the script below is about.
	e2eScripts    = 12
	e2eLeadQuest  = "the-defias-brotherhood"
	e2eLeadScript = "scripts/the-defias-brotherhood.md"

	// e2eDialogueWord appears in the lead script's dialogue and nowhere
	// else in the game — not in an entity's name, key or field. Searching
	// it must produce a document hit and no entity hit at all, which is
	// how "entity search does not reach into attached prose" is asserted
	// as a guarantee rather than assumed.
	e2eDialogueWord = "cobblestones"
)

// e2eLeadV1 is the first version of the lead script: frontmatter
// carrying a key Maestro has no idea about (`era`), a title and a
// summary it does know, and a line of dialogue.
const e2eLeadV1 = "---\n" +
	"title: The Defias Brotherhood\n" +
	"summary: VanCleef receives the Stonemasons' last petition.\n" +
	"era: third\n" +
	"---\n" +
	"# Act one\n\n" +
	"VanCleef says: \"You laid every one of these " + e2eDialogueWord + " and were paid in promises.\"\n"

// e2eLeadV2 is the first agent's second version, written while the
// second agent is still holding version 1 — the state a conflict is
// about.
const e2eLeadV2 = "---\n" +
	"title: The Defias Brotherhood\n" +
	"summary: VanCleef receives the Stonemasons' last petition.\n" +
	"era: third\n" +
	"---\n" +
	"# Act one\n\n" +
	"VanCleef says: \"You laid every one of these " + e2eDialogueWord + " and were paid in promises.\"\n\n" +
	"# Act two\n\n" +
	"The petition burns.\n"

// e2eLeadMerged is what the second agent produces after reading the
// conflict's own echo of the current body and merging its line onto it.
const e2eLeadMerged = "---\n" +
	"title: The Defias Brotherhood\n" +
	"summary: VanCleef receives the Stonemasons' last petition.\n" +
	"era: third\n" +
	"---\n" +
	"# Act one\n\n" +
	"VanCleef says: \"You laid every one of these " + e2eDialogueWord + " and were paid in promises.\"\n\n" +
	"# Act two\n\n" +
	"The petition burns.\n\n" +
	"# Act three\n\n" +
	"Stormwind answers with a proclamation.\n"

// proseWorld is one game, everything that writes into it, and a second
// game whose token is used for nothing but the isolation sweep.
type proseWorld struct {
	srv      *web.Server
	deps     web.MCPDeps
	agent    web.Caller // the lore agent's token, bound to game
	rival    web.Caller // a second agent's token, same game
	guest    web.Caller // a token bound to otherGame and to nothing else
	game     uuid.UUID
	gameSlug string
	other    uuid.UUID
	token    string       // the lore agent's secret, for the HTTP session
	cookie   *http.Cookie // the designer: a session caller with the editor role
	typeID   map[string]uuid.UUID
}

// rest sends one request as the designer's session, at the game's own
// prefix. Every REST assertion below goes through it, so the routes are
// exercised exactly as the reading view calls them: the game in the
// path, the document's path in the query string, never a URL segment.
func (w proseWorld) rest(t *testing.T, method, suffix string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		assert.Must(t, err == nil, "marshal body: %v", err)
		reader = strings.NewReader(string(raw))
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, "/api/games/"+w.gameSlug+suffix, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(w.cookie)
	rec := httptest.NewRecorder()
	w.srv.ServeHTTP(rec, req)
	return rec
}

// newProseWorld builds the game and everything that writes into it. The
// entity half goes through the metamodel tools for the same reason the
// prose half goes through the prose ones: a seeding script is a caller.
func newProseWorld(t *testing.T) *proseWorld {
	t.Helper()
	ctx := context.Background()
	srv, ids, projSvc, mm, md, _, _ := newMetamodelTestServer(t)

	owner, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "lead@example.test", DisplayName: "Lead", Password: "password12345",
	})
	assert.Must(t, err == nil, "CreateUser: %v", err)
	// The designer is a *different* user with the editor role, not the
	// owner: "a human writes through REST as a session caller with the
	// editor role" is the case the spec's definition of done names, and
	// an owner would pass requireEditor for a reason that says nothing
	// about an editor.
	designer, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "ana@example.test", DisplayName: "Ana", Password: "password12345",
	})
	assert.Must(t, err == nil, "CreateUser designer: %v", err)
	game, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	assert.Must(t, err == nil, "Create game: %v", err)
	other, err := projSvc.Create(ctx, "le-mans", "Le Mans", owner.ID)
	assert.Must(t, err == nil, "Create other game: %v", err)
	if _, err := projSvc.SetRole(ctx, designer.ID, game.ID, string(roles.Editor)); err != nil {
		t.Fatalf("SetRole editor: %v", err)
	}

	mint := func(project uuid.UUID, label string) (web.Caller, string) {
		t.Helper()
		secret, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
			ProjectID: project, UserID: owner.ID, Label: label,
		})
		assert.Must(t, err == nil, "CreateAPIToken %s: %v", label, err)
		caller, err := web.CallerForToken(ctx, ids, secret)
		assert.Must(t, err == nil, "CallerForToken %s: %v", label, err)
		return caller, secret
	}
	agent, agentSecret := mint(game.ID, "lore agent")
	rival, _ := mint(game.ID, "continuity agent")
	guest, _ := mint(other.ID, "le mans agent")

	w := &proseWorld{
		srv:      srv,
		deps:     web.MCPDeps{Identity: ids, Projects: projSvc, Metamodel: mm, Markdown: md},
		agent:    agent,
		rival:    rival,
		guest:    guest,
		game:     game.ID,
		gameSlug: game.Slug,
		other:    other.ID,
		token:    agentSecret,
		cookie:   loginAs(t, srv, "ana@example.test"),
		typeID:   map[string]uuid.UUID{},
	}
	w.seedEntities(t)
	return w
}

// seedEntities declares the three types and the fifteen entities: twelve
// quests, two places and one faction.
func (w *proseWorld) seedEntities(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, spec := range []web.TypesUpsertInput{
		{Key: "quest", Label: "Quest", LabelPlural: "Quests",
			Schema: []web.FieldInput{{Key: "min_level", Type: "number", Min: ptrFloat(1), Max: ptrFloat(60)}}},
		{Key: "zone", Label: "Zone", LabelPlural: "Zones"},
		{Key: "faction", Label: "Faction", LabelPlural: "Factions"},
	} {
		out, err := web.MCPTypesUpsert(ctx, w.deps, w.agent, w.game, spec)
		assert.Must(t, err == nil, "declare %s: %v", spec.Key, err)
		w.typeID[spec.Key] = out.ID
	}

	items := []web.EntityItemInput{
		{TypeKey: "zone", Key: "elwynn-forest", Name: "Elwynn Forest"},
		{TypeKey: "zone", Key: "stormwind-city", Name: "Stormwind City"},
		{TypeKey: "faction", Key: "defias-brotherhood", Name: "Defias Brotherhood"},
	}
	// The lead quest first, then eleven more, each of which gets a
	// script of its own.
	items = append(items, web.EntityItemInput{
		TypeKey: "quest", Key: e2eLeadQuest, Name: "The Defias Brotherhood",
		Fields: map[string]any{"min_level": 16},
	})
	for i := 1; i < e2eScripts; i++ {
		items = append(items, web.EntityItemInput{
			TypeKey: "quest",
			Key:     fmt.Sprintf("quest-%02d", i),
			Name:    fmt.Sprintf("Errand %02d", i),
			Fields:  map[string]any{"min_level": 10 + i},
		})
	}
	out, err := web.MCPEntitiesUpsert(ctx, w.deps, w.agent, w.game, web.EntitiesUpsertInput{Items: items})
	assert.Must(t, err == nil, "seed entities: %v", err)
	assert.Must(t, out.Count == len(items), "seeded %d entities, want %d", out.Count, len(items))
	assert.Must(t, len(items) == 15, "the fixture seeded %d entities; the shape this test is written against is 15", len(items))
}

// write is docs.write as the given agent, failing on anything but a
// clean answer. Named for what it is rather than `writeDoc`, which
// mcp_docs_test.go already defines in this same package.
func (w *proseWorld) write(t *testing.T, caller web.Caller, in web.DocsWriteInput) web.DocumentOutput {
	t.Helper()
	out, err := web.MCPDocsWrite(context.Background(), w.deps, caller, w.game, in)
	assert.Must(t, err == nil, "docs.write %s: %v", in.Path, err)
	return out
}

func (w *proseWorld) read(t *testing.T, path string) web.DocumentOutput {
	t.Helper()
	out, err := web.MCPDocsRead(context.Background(), w.deps, w.agent, w.game, web.DocsReadInput{Path: path})
	assert.Must(t, err == nil, "docs.read %s: %v", path, err)
	return out
}

func (w *proseWorld) history(t *testing.T, path string) web.DocsHistoryOutput {
	t.Helper()
	out, err := web.MCPDocsHistory(context.Background(), w.deps, w.agent, w.game,
		web.DocsHistoryInput{Path: path, Limit: 50})
	assert.Must(t, err == nil, "docs.history %s: %v", path, err)
	return out
}

func (w *proseWorld) search(t *testing.T, in web.SearchInput) web.SearchOutput {
	t.Helper()
	out, err := web.MCPSearch(context.Background(), w.deps, w.agent, w.game, in)
	assert.Must(t, err == nil, "search %q: %v", in.Query, err)
	return out
}

// TestDocumentsEndToEnd is the spec's §9 definition of done, executed as
// one test in the order a real session would run it.
func TestDocumentsEndToEnd(t *testing.T) {
	t.Parallel()
	w := newProseWorld(t)
	ctx := context.Background()

	// --- Step 1: an agent writes the script with its links in one call.

	first := w.write(t, w.agent, web.DocsWriteInput{
		Path:            e2eLeadScript,
		Content:         e2eLeadV1,
		Kind:            stringPtr("script"),
		Message:         "the first draft",
		ExpectedVersion: int32Ptr(0),
		Links: &[]web.DocsLinkInput{
			{EntityType: "quest", EntityKey: e2eLeadQuest, Role: "script"},
		},
	})
	assert.Must(t, first.Version == 1, "the first write landed at version %d, want 1", first.Version)
	assert.Must(t, len(first.Links) == 1 && first.Links[0].EntityKey == e2eLeadQuest && first.Links[0].Role == "script", "links = %+v, want the quest in the script role", first.Links)
	assert.Must(t, first.Title == "The Defias Brotherhood", "title = %q, want the frontmatter's own", first.Title)

	// --- Step 2: a second agent, still holding version 1, rewrites it
	// after the first agent has already written version 2.

	second := w.write(t, w.agent, web.DocsWriteInput{
		Path: e2eLeadScript, Content: e2eLeadV2,
		Message: "act two", ExpectedVersion: int32Ptr(1),
	})
	assert.Must(t, second.Version == 2, "the second write landed at version %d, want 2", second.Version)

	_, err := web.MCPDocsWrite(ctx, w.deps, w.rival, w.game, web.DocsWriteInput{
		Path: e2eLeadScript, Content: e2eLeadV1 + "\nStormwind answers.\n",
		Message: "act three", ExpectedVersion: int32Ptr(1),
	})
	assert.Must(t, err != nil, "a write at a stale version succeeded; the compare-and-set did not happen")
	details := conflictDetails(t, err)
	if got := details["current_version"]; got != int32(2) {
		t.Fatalf("details.current_version = %#v, want 2", got)
	}
	if got, _ := details["current_body"].(string); got != strings.SplitN(e2eLeadV2, "---\n", 3)[2] {
		// The echoed body is the stored one: frontmatter split off, the
		// prose kept byte for byte. Anything else and the loser of the
		// race cannot merge onto what actually landed.
		t.Fatalf("details.current_body = %q, want the stored body of version 2", got)
	}
	if _, ok := details["deleted"]; ok {
		t.Fatalf("an ordinary conflict claimed something about deletion: %#v", details)
	}

	// --- Step 3: the second agent merges and succeeds.

	merged := w.write(t, w.rival, web.DocsWriteInput{
		Path: e2eLeadScript, Content: e2eLeadMerged,
		Message: "act three, merged", ExpectedVersion: int32Ptr(2),
	})
	assert.Must(t, merged.Version == 3, "the merge landed at version %d, want 3", merged.Version)
	wantBody := strings.SplitN(e2eLeadMerged, "---\n", 3)[2]
	if back := w.read(t, e2eLeadScript); back.Body != wantBody {
		t.Fatalf("the merged body did not read back byte for byte:\n got %q\nwant %q", back.Body, wantBody)
	}

	// --- Step 4: an ordinary edit with no links array leaves the
	// attachment alone. This is the case that would silently detach
	// everything if "omitted" meant "empty", and a seeding script is
	// where that would happen.

	edited := w.write(t, w.agent, web.DocsWriteInput{
		Path: e2eLeadScript, Content: e2eLeadMerged + "\nThe city gates close.\n",
		Message: "a closing line", ExpectedVersion: int32Ptr(3),
	})
	assert.Must(t, edited.Version == 4, "the edit landed at version %d, want 4", edited.Version)
	assert.Must(t, len(edited.Links) == 1 && edited.Links[0].EntityKey == e2eLeadQuest, "an edit with no links array changed the attachment: %+v", edited.Links)

	// --- Step 5: a human writes through REST, so the document has a
	// version by a user beside its versions by tokens.

	rec := w.rest(t, http.MethodPost, "/docs", map[string]any{
		"path":             e2eLeadScript,
		"content":          e2eLeadMerged + "\nThe city gates close behind him.\n",
		"message":          "tightened the closing line",
		"expected_version": 4,
	})
	assert.Must(t, rec.Code == http.StatusOK, "the designer's write = %d: %s", rec.Code, rec.Body.String())
	var humanWrite struct {
		Version int32 `json:"version"`
	}
	decodeBody(t, rec, &humanWrite)
	assert.Must(t, humanWrite.Version == 5, "the designer's write landed at version %d, want 5", humanWrite.Version)

	// --- Step 6: docs.history shows both kinds of author.

	hist := w.history(t, e2eLeadScript)
	assert.Must(t, len(hist.Items) == 5, "history has %d versions, want 5", len(hist.Items))
	assert.Must(t, hist.Items[0].Version == 5 && hist.Items[4].Version == 1, "history is not newest first: %+v", hist.Items)
	if hist.Items[0].AuthorKind != "user" {
		t.Fatalf("version 5's author_kind = %q, want user", hist.Items[0].AuthorKind)
	}
	if hist.Items[4].AuthorKind != "token" {
		t.Fatalf("version 1's author_kind = %q, want token", hist.Items[4].AuthorKind)
	}
	assert.Must(t, hist.Items[0].AuthorID != nil && hist.Items[4].AuthorID != nil, "a history row named an author kind and no author id: %+v", hist.Items)
	assert.Must(t, *hist.Items[0].AuthorID != *hist.Items[4].AuthorID, "the human and the token report the same author id; the two columns are not distinguished")
	if hist.Items[0].Message != "tightened the closing line" {
		t.Fatalf("version 5's message = %q", hist.Items[0].Message)
	}

	// --- Step 7: a diff between two versions names the line that
	// changed, and is not coarse.

	diff, err := web.MCPDocsDiff(ctx, w.deps, w.agent, w.game, web.DocsDiffInput{
		Path: e2eLeadScript, FromVersion: 4, ToVersion: 5,
	})
	assert.Must(t, err == nil, "docs.diff: %v", err)
	assert.Must(t, !diff.Coarse, "a one-line change came back coarse: %s", diff.Unified)
	assert.Must(t, strings.Contains(diff.Unified, "-The city gates close.") && strings.Contains(diff.Unified, "+The city gates close behind him."), "the diff does not name the line that changed:\n%s", diff.Unified)
	assert.Must(t, !diff.FromDeleted && !diff.ToDeleted, "a diff between two live versions claims a deletion: %+v", diff)

	// --- Step 8: a revert writes forward.

	v1, err := web.MCPDocsReadVersion(ctx, w.deps, w.agent, w.game,
		web.DocsReadVersionInput{Path: e2eLeadScript, Version: 1})
	assert.Must(t, err == nil, "docs.read_version 1: %v", err)
	reverted, err := web.MCPDocsRevert(ctx, w.deps, w.agent, w.game, web.DocsRevertInput{
		Path: e2eLeadScript, ToVersion: 1, ExpectedVersion: int32Ptr(5),
	})
	assert.Must(t, err == nil, "docs.revert: %v", err)
	assert.Must(t, reverted.Version == 6, "the revert landed at version %d, want 6 — a revert writes forward", reverted.Version)
	assert.Must(t, reverted.Body == v1.Body && reverted.Title == v1.Title && reverted.Summary == v1.Summary, "the revert did not restore version 1's stored fields:\n got %q / %q / %q\nwant %q / %q / %q",
		reverted.Body, reverted.Title, reverted.Summary, v1.Body, v1.Title, v1.Summary)
	hist = w.history(t, e2eLeadScript)
	assert.Must(t, len(hist.Items) == 6, "history has %d versions after the revert, want 6", len(hist.Items))
	for i, item := range hist.Items {
		if want := int32(6 - i); item.Version != want {
			t.Fatalf("history has a gap: item %d is version %d, want %d", i, item.Version, want)
		}
	}
	if hist.Items[0].Message != "reverted to version 1" {
		t.Fatalf("the revert's automatic message = %q", hist.Items[0].Message)
	}

	// --- The rest of the fixture, written now that the lead script's
	// own life is complete: the bible with no entity and no
	// frontmatter, the lore attached to two, the eleven other scripts,
	// the deleted-and-resurrected note and the over-long chronicle.

	w.seedTheRestOfTheProse(t)

	// --- Step 9: the designer's browser reads it.

	w.assertTheReadingViewRendersAndRefuses(t)

	// --- Step 10: search finds both kinds.

	w.assertSearchSpansBothIndexes(t)

	// --- Step 11: the listing pages and filters.

	w.assertTheListingPagesAndFilters(t)

	// --- Step 12: the isolation sweep, over every registered tool.

	w.assertAnotherGamesTokenIsRefusedEverywhere(t)

	// --- Step 13: the frontmatter survived all of it.

	final := w.read(t, e2eLeadScript)
	var front map[string]any
	if err := json.Unmarshal(final.Frontmatter, &front); err != nil {
		t.Fatalf("decode frontmatter %s: %v", final.Frontmatter, err)
	}
	assert.Must(t, front["era"] == "third", "frontmatter = %s; the key Maestro does not know did not survive "+
		"a conflict, a merge, an edit, a human's write and a revert", final.Frontmatter)
}

// seedTheRestOfTheProse writes the fifteen documents that are not the
// lead script.
func (w *proseWorld) seedTheRestOfTheProse(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	// The game bible: attached to nothing, and carrying no frontmatter
	// at all, so the title has to come from the body's own heading.
	bible := w.write(t, w.agent, web.DocsWriteInput{
		Path:            "bible.md",
		Content:         "# The Shape of Azeroth\n\nA world of two continents and one grudge.\n",
		Kind:            stringPtr("bible"),
		Message:         "the bible",
		ExpectedVersion: int32Ptr(0),
	})
	assert.Must(t, len(bible.Links) == 0, "the bible is attached to %+v; it is the zero-entity case", bible.Links)
	assert.Must(t, bible.Title == "The Shape of Azeroth", "a document with no frontmatter took its title from %q", bible.Title)
	assert.Must(t, string(bible.Frontmatter) == "{}", "frontmatter = %s, want an empty object for a document that declared none", bible.Frontmatter)

	// The lore of the grudge: attached to two entities at once.
	lore := w.write(t, w.agent, web.DocsWriteInput{
		Path: "lore/defias-and-stormwind.md",
		Content: "---\ntitle: Debts of Stone\n---\n" +
			"The Stonemasons rebuilt Stormwind City and were never paid.\n",
		Kind: stringPtr("lore"), Message: "the grudge", ExpectedVersion: int32Ptr(0),
		Links: &[]web.DocsLinkInput{
			{EntityType: "faction", EntityKey: "defias-brotherhood", Role: "origin"},
			{EntityType: "zone", EntityKey: "stormwind-city", Role: "setting"},
		},
	})
	assert.Must(t, len(lore.Links) == 2, "the lore is attached to %d entities, want 2", len(lore.Links))

	// Eleven more scripts, one per remaining quest. Written in reverse
	// key order so a listing that came back in insertion order rather
	// than path order would be visible.
	for i := e2eScripts - 1; i >= 1; i-- {
		key := fmt.Sprintf("quest-%02d", i)
		w.write(t, w.agent, web.DocsWriteInput{
			Path:            "scripts/" + key + ".md",
			Content:         fmt.Sprintf("# Errand %02d\n\nA courier waits by the road.\n", i),
			Kind:            stringPtr("script"),
			Message:         "seeded",
			ExpectedVersion: int32Ptr(0),
			Links:           &[]web.DocsLinkInput{{EntityType: "quest", EntityKey: key, Role: "script"}},
		})
	}

	// A note deleted and brought back, so a version line carries a
	// tombstone in its middle and the numbering is seen to continue
	// across it.
	w.write(t, w.agent, web.DocsWriteInput{
		Path: "notes/scrapped.md", Content: "# Scrapped\n\nThe kobold arc.\n",
		Message: "an idea", ExpectedVersion: int32Ptr(0),
	})
	w.write(t, w.agent, web.DocsWriteInput{
		Path: "notes/scrapped.md", Content: "# Scrapped\n\nThe kobold arc, expanded.\n",
		Message: "more of it", ExpectedVersion: int32Ptr(1),
	})
	if _, err := web.MCPDocsDelete(ctx, w.deps, w.agent, w.game, web.DocsDeleteInput{
		Path: "notes/scrapped.md", Message: "cut", ExpectedVersion: int32Ptr(2),
	}); err != nil {
		t.Fatalf("docs.delete: %v", err)
	}
	// A deleted document is not readable, and its history is: that pair
	// is what "deleting keeps the history" means in practice.
	if _, err := web.MCPDocsRead(ctx, w.deps, w.agent, w.game,
		web.DocsReadInput{Path: "notes/scrapped.md"}); !errors.Is(err, markdown.ErrNotFound) {
		t.Fatalf("reading a deleted document answered %v, want not_found", err)
	}
	tomb, err := web.MCPDocsReadVersion(ctx, w.deps, w.agent, w.game,
		web.DocsReadVersionInput{Path: "notes/scrapped.md", Version: 3})
	assert.Must(t, err == nil, "read the tombstone: %v", err)
	assert.Must(t, tomb.Deleted, "version 3 of a deleted document does not read as deleted; the tombstone is not a readable version")
	resurrected := w.write(t, w.agent, web.DocsWriteInput{
		Path: "notes/scrapped.md", Content: "# Scrapped\n\nBack, with gnolls.\n",
		Message: "back", ExpectedVersion: int32Ptr(3),
	})
	assert.Must(t, resurrected.Version == 4, "the resurrection landed at version %d, want 4 — the numbering continues across a tombstone",
		resurrected.Version)
	scrappedHistory := w.history(t, "notes/scrapped.md")
	assert.Must(t, len(scrappedHistory.Items) == 4, "the resurrected note has %d versions, want 4", len(scrappedHistory.Items))
	assert.Must(t, scrappedHistory.Items[1].Deleted && !scrappedHistory.Items[0].Deleted, "the tombstone is not where the history says it is: %+v", scrappedHistory.Items)
	// A comparison that spans the deletion. The tombstone carries the
	// body the document had when it went, so the unified diff is empty
	// and the two flags are the only thing that says a deletion is what
	// this is — the defect this end-to-end run found, asserted here on
	// both surfaces because the page reads the REST one and an agent
	// reads the MCP one.
	acrossTomb, err := web.MCPDocsDiff(ctx, w.deps, w.agent, w.game, web.DocsDiffInput{
		Path: "notes/scrapped.md", FromVersion: 2, ToVersion: 3,
	})
	assert.Must(t, err == nil, "docs.diff across the tombstone: %v", err)
	assert.Must(t, !acrossTomb.FromDeleted && acrossTomb.ToDeleted, "docs.diff across a tombstone = (%v, %v), want (false, true)",
		acrossTomb.FromDeleted, acrossTomb.ToDeleted)
	rec := w.rest(t, http.MethodGet, docsPath("/docs/comparison", map[string]string{
		"path": "notes/scrapped.md", "from_version": "2", "to_version": "3",
	}), nil)
	assert.Must(t, rec.Code == http.StatusOK, "GET /docs/comparison across the tombstone = %d: %s", rec.Code, rec.Body.String())
	var spanning struct {
		FromDeleted bool `json:"from_deleted"`
		ToDeleted   bool `json:"to_deleted"`
	}
	decodeBody(t, rec, &spanning)
	assert.Must(t, !spanning.FromDeleted && spanning.ToDeleted, "the comparison view across a tombstone = %+v, want to_deleted alone", spanning)

	// The chronicle: long enough that a word past the search index bound
	// is stored and readable and not findable, and long enough that a
	// rewrite of it comes back as a coarse diff.
	w.write(t, w.agent, web.DocsWriteInput{
		Path: "lore/chronicle.md", Content: e2eChronicle("stormcrow", "aftbound"),
		Kind: stringPtr("lore"), Message: "the long version", ExpectedVersion: int32Ptr(0),
	})
	w.write(t, w.agent, web.DocsWriteInput{
		Path: "lore/chronicle.md", Content: e2eChronicle("stormcrow", "sternbound"),
		Kind: stringPtr("lore"), Message: "rewritten end to end", ExpectedVersion: int32Ptr(1),
	})
	coarse, err := web.MCPDocsDiff(ctx, w.deps, w.agent, w.game, web.DocsDiffInput{
		Path: "lore/chronicle.md", FromVersion: 1, ToVersion: 2,
	})
	assert.Must(t, err == nil, "docs.diff on the chronicle: %v", err)
	assert.Must(t, coarse.Coarse, "a body rewritten line by line past the diff bound did not come back coarse")
}

// e2eChronicle builds a body far past markdown.MaxIndexedChars: `early`
// appears in the first line, `late` well past the bound. Every line is
// distinct, so rewriting it changes more lines than the diff will build
// a table for.
func e2eChronicle(early, late string) string {
	var b strings.Builder
	b.WriteString("---\ntitle: The Chronicle\n---\n")
	b.WriteString("The " + early + " was seen over Westfall in the first year.\n")
	for i := 0; b.Len() < markdown.MaxIndexedChars+2048; i++ {
		fmt.Fprintf(&b, "In year %d the harvest was counted and the tithe was paid in %s coin.\n", i, late[:4])
	}
	b.WriteString("The " + late + " ship was never launched.\n")
	return b.String()
}

// assertTheReadingViewRendersAndRefuses is step 9: the designer's half,
// over the two routes the reading view actually calls.
func (w *proseWorld) assertTheReadingViewRendersAndRefuses(t *testing.T) {
	t.Helper()

	rec := w.rest(t, http.MethodGet, docsPath("/docs/rendered", map[string]string{"path": e2eLeadScript}), nil)
	assert.Must(t, rec.Code == http.StatusOK, "GET /docs/rendered = %d: %s", rec.Code, rec.Body.String())
	var rendered struct {
		Title string `json:"title"`
		HTML  string `json:"html"`
		Body  string `json:"body"`
		Links []struct {
			EntityKey string `json:"entity_key"`
		} `json:"links"`
	}
	decodeBody(t, rec, &rendered)
	assert.Must(t, strings.Contains(rendered.HTML, "<h1") && strings.Contains(rendered.HTML, e2eDialogueWord), "the rendered view is not HTML carrying the dialogue: %q", rendered.HTML)
	assert.Must(t, rendered.Body == "", "the rendered view also returned the raw body (%d bytes); that is the payload it exists not to send twice",
		len(rendered.Body))
	assert.Must(t, len(rendered.Links) == 1 && rendered.Links[0].EntityKey == e2eLeadQuest, "the reading view does not name the entities: %+v", rendered.Links)

	rec = w.rest(t, http.MethodGet, docsPath("/docs/one", map[string]string{"path": e2eLeadScript}), nil)
	assert.Must(t, rec.Code == http.StatusOK, "GET /docs/one = %d: %s", rec.Code, rec.Body.String())
	var raw struct {
		Body string `json:"body"`
		HTML string `json:"html"`
	}
	decodeBody(t, rec, &raw)
	assert.Must(t, raw.HTML == "", "GET /docs/one returned HTML as well as markdown: %q", raw.HTML)
	assert.Must(t, strings.Contains(raw.Body, "# Act one"), "GET /docs/one did not return the raw markdown: %q", raw.Body)
	assert.Must(t, raw.Body != rendered.HTML, "the raw and the rendered views answered identically")
	if got := w.read(t, e2eLeadScript).Body; got != raw.Body {
		t.Fatalf("REST and MCP disagree about the same document's body:\n rest %q\n mcp  %q", raw.Body, got)
	}

	// What the reading view must refuse. A document that is not there is
	// a 404 naming the argument, not an empty page; and a comparison
	// against a version that never existed is refused rather than
	// rendered as "everything changed".
	rec = w.rest(t, http.MethodGet, docsPath("/docs/rendered", map[string]string{"path": "scripts/nothing.md"}), nil)
	assertError(t, rec, http.StatusNotFound, "not_found", "path")

	rec = w.rest(t, http.MethodGet, docsPath("/docs/comparison", map[string]string{
		"path": e2eLeadScript, "from_version": "1", "to_version": "99",
	}), nil)
	assertError(t, rec, http.StatusNotFound, "not_found", "to_version")

	// And what it must render: a comparison of two real versions, as
	// classed lines beside the unified text.
	rec = w.rest(t, http.MethodGet, docsPath("/docs/comparison", map[string]string{
		"path": e2eLeadScript, "from_version": "1", "to_version": "3",
	}), nil)
	assert.Must(t, rec.Code == http.StatusOK, "GET /docs/comparison = %d: %s", rec.Code, rec.Body.String())
	var comparison struct {
		Unified string `json:"unified"`
		Coarse  bool   `json:"coarse"`
		HTML    string `json:"html"`
	}
	decodeBody(t, rec, &comparison)
	assert.Must(t, !comparison.Coarse, "comparing two small versions came back coarse: %s", comparison.Unified)
	assert.Must(t, strings.Contains(comparison.Unified, "+# Act three"), "the comparison does not name the added act:\n%s", comparison.Unified)
	assert.Must(t, strings.Contains(comparison.HTML, "class="), "the comparison's html is not classed lines: %q", comparison.HTML)
}

// assertSearchSpansBothIndexes is step 10.
func (w *proseWorld) assertSearchSpansBothIndexes(t *testing.T) {
	t.Helper()

	// A line of the dialogue: a document hit, naming the quest it is
	// attached to, and no entity hit at all — entity search does not
	// reach into attached prose, and that is a guarantee.
	hits := w.search(t, web.SearchInput{Query: e2eDialogueWord, Limit: 20})
	assert.Must(t, len(hits.Items) != 0, "searching a line of the dialogue found nothing")
	foundDoc := false
	for _, hit := range hits.Items {
		assert.Must(t, hit.Kind != "entity", "searching a word that lives only in prose produced an entity hit: %+v", hit.Entity)
		assert.Must(t, hit.Kind == "document" && hit.Document != nil, "a hit labelled %q carried no document: %+v", hit.Kind, hit)
		if hit.Document.Path != e2eLeadScript {
			continue
		}
		foundDoc = true
		assert.Must(t, len(hit.Document.LinkedTo) == 1 && hit.Document.LinkedTo[0].EntityKey == e2eLeadQuest, "the document hit does not name its quest: %+v", hit.Document.LinkedTo)
		assert.Must(t, hit.Document.Summary != "", "the document hit carries no summary; a search result a caller cannot read is a round trip")
	}
	assert.Must(t, foundDoc, "the lead script was not among the hits for %q: %+v", e2eDialogueWord, hits.Items)

	// The quest's name: an entity hit, and first. Both indexes carry
	// "Defias" — the faction and the quest by name, the lore document by
	// mention — so this is the cross-index ranking claim, not a
	// single-index one.
	named := w.search(t, web.SearchInput{Query: "Defias", Limit: 20})
	assert.Must(t, len(named.Items) >= 2, "searching the quest's name returned %d hits; the fixture has both kinds", len(named.Items))
	if named.Items[0].Kind != "entity" || !named.Items[0].NameMatch {
		t.Fatalf("a name match did not come first: %+v", named.Items[0])
	}
	sawDocument := false
	for _, hit := range named.Items {
		if hit.Kind == "document" {
			sawDocument = true
		}
	}
	assert.Must(t, sawDocument, "searching %q returned no document hit at all: %+v", "Defias", named.Items)

	// A document by its title, which is the other half of the document
	// index's own weighting.
	byTitle := w.search(t, web.SearchInput{Query: "Shape", Kind: "document", Limit: 20})
	assert.Must(t, len(byTitle.Items) == 1 && byTitle.Items[0].Document.Path == "bible.md", "searching a document's title found %+v", byTitle.Items)
	assert.Must(t, byTitle.Items[0].NameMatch, "a hit on a document's own title is not reported as a name match")

	// The index bound, stated as a fact rather than as a comment: a word
	// in the first line of the chronicle is findable, a word past
	// markdown.MaxIndexedChars is stored, readable, and not.
	assert.Must(t, len(w.search(t, web.SearchInput{Query: "stormcrow", Limit: 5}).Items) != 0, "a word at the head of a long document is not findable")
	if hits := w.search(t, web.SearchInput{Query: "sternbound", Limit: 5}).Items; len(hits) != 0 {
		t.Fatalf("a word past markdown.MaxIndexedChars was found: %+v", hits)
	}
	assert.Must(t, strings.Contains(w.read(t, "lore/chronicle.md").Body, "sternbound"), "the word past the index bound is not in the stored body either; the fixture proves nothing")
}

// assertTheListingPagesAndFilters is step 11.
func (w *proseWorld) assertTheListingPagesAndFilters(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	// Sixteen documents, paged five at a time. The limit is explicit:
	// see this test's own note about the default page being far larger
	// than the fixture the plan specifies.
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		page, err := web.MCPDocsList(ctx, w.deps, w.agent, w.game, web.DocsListInput{Limit: 5, Cursor: cursor})
		assert.Must(t, err == nil, "docs.list page %d: %v", pages, err)
		pages++
		for _, item := range page.Items {
			assert.Must(t, !(seen[item.Path]), "%s came back on two pages", item.Path)
			seen[item.Path] = true
		}
		if page.NextCursor == nil {
			break
		}
		assert.Must(t, pages <= 10, "the listing never reported an end")
		cursor = *page.NextCursor
	}
	assert.Must(t, pages >= 2, "the listing came back in %d page(s); a keyset cursor that never issues is not paging", pages)
	// Sixteen live documents: the lead script, eleven more scripts, the
	// bible, the lore, the chronicle and the resurrected note.
	assert.Must(t, len(seen) == 16, "the listing returned %d documents, want 16: %v", len(seen), seen)

	prefixed, err := web.MCPDocsList(ctx, w.deps, w.agent, w.game, web.DocsListInput{
		PathPrefix: "scripts/", Limit: 50,
	})
	assert.Must(t, err == nil, "docs.list by prefix: %v", err)
	assert.Must(t, len(prefixed.Items) == e2eScripts, "path_prefix scripts/ selected %d documents, want %d", len(prefixed.Items), e2eScripts)
	for i, item := range prefixed.Items {
		assert.Must(t, strings.HasPrefix(item.Path, "scripts/"), "path_prefix let %q through", item.Path)
		if i > 0 && prefixed.Items[i-1].Path >= item.Path {
			t.Fatalf("the listing is not in path order: %q then %q", prefixed.Items[i-1].Path, item.Path)
		}
	}

	byEntity, err := web.MCPDocsList(ctx, w.deps, w.agent, w.game, web.DocsListInput{
		EntityType: "quest", EntityKey: e2eLeadQuest, Limit: 50,
	})
	assert.Must(t, err == nil, "docs.list by entity: %v", err)
	assert.Must(t, len(byEntity.Items) == 1 && byEntity.Items[0].Path == e2eLeadScript, "filtering by the quest returned %+v, want only its script", byEntity.Items)

	// The join answers from both sides, and the same link is on both.
	fromDoc, err := web.MCPDocsLinksList(ctx, w.deps, w.agent, w.game,
		web.DocsLinksListInput{Path: "lore/defias-and-stormwind.md"})
	assert.Must(t, err == nil, "docs.links.list by path: %v", err)
	assert.Must(t, len(fromDoc.Entities) == 2 && len(fromDoc.Documents) == 0, "the document side answered %+v", fromDoc)
	fromEntity, err := web.MCPDocsLinksList(ctx, w.deps, w.agent, w.game,
		web.DocsLinksListInput{EntityType: "faction", EntityKey: "defias-brotherhood"})
	assert.Must(t, err == nil, "docs.links.list by entity: %v", err)
	assert.Must(t, len(fromEntity.Documents) == 1 && fromEntity.Documents[0].Path == "lore/defias-and-stormwind.md", "the entity side answered %+v", fromEntity.Documents)
	if fromEntity.Documents[0].Role != "origin" {
		t.Fatalf("the entity side lost the role: %+v", fromEntity.Documents[0])
	}

	// A link survives a delete and comes back with the document. The
	// note is attached here rather than in the seed so that the delete
	// and the resurrection it already went through are behind it and
	// this is a second, deliberate round.
	if _, err := web.MCPDocsLinkAdd(ctx, w.deps, w.agent, w.game, web.DocsLinkAddInput{
		Path: "notes/scrapped.md", EntityType: "zone", EntityKey: "elwynn-forest", Role: "setting",
	}); err != nil {
		t.Fatalf("docs.links.add: %v", err)
	}
	if _, err := web.MCPDocsDelete(ctx, w.deps, w.agent, w.game, web.DocsDeleteInput{
		Path: "notes/scrapped.md", ExpectedVersion: int32Ptr(4),
	}); err != nil {
		t.Fatalf("docs.delete the linked note: %v", err)
	}
	back := w.write(t, w.agent, web.DocsWriteInput{
		Path: "notes/scrapped.md", Content: "# Scrapped\n\nAgain.\n",
		Message: "again", ExpectedVersion: int32Ptr(5),
	})
	assert.Must(t, len(back.Links) == 1 && back.Links[0].EntityKey == "elwynn-forest", "the link did not survive a delete and a resurrection: %+v", back.Links)
}

// assertAnotherGamesTokenIsRefusedEverywhere is step 12: a token for a
// second game, driven at the first game's real content — its path, its
// versions, its history and its links — over **every** registered tool,
// discovered from the server's own registration table rather than from a
// list written here.
func (w *proseWorld) assertAnotherGamesTokenIsRefusedEverywhere(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	// Each call names something that really exists in w.game, so a
	// refusal cannot be a not_found wearing a scope violation's clothes.
	calls := map[string]func() error{
		"docs.write": func() error {
			_, err := web.MCPDocsWrite(ctx, w.deps, w.guest, w.game, web.DocsWriteInput{
				Path: e2eLeadScript, Content: "stolen", ExpectedVersion: int32Ptr(6),
			})
			return err
		},
		"docs.read": func() error {
			_, err := web.MCPDocsRead(ctx, w.deps, w.guest, w.game, web.DocsReadInput{Path: e2eLeadScript})
			return err
		},
		"docs.list": func() error {
			_, err := web.MCPDocsList(ctx, w.deps, w.guest, w.game, web.DocsListInput{PathPrefix: "scripts/"})
			return err
		},
		"docs.delete": func() error {
			_, err := web.MCPDocsDelete(ctx, w.deps, w.guest, w.game, web.DocsDeleteInput{
				Path: e2eLeadScript, ExpectedVersion: int32Ptr(6),
			})
			return err
		},
		"docs.write_many": func() error {
			_, err := web.MCPDocsWriteMany(ctx, w.deps, w.guest, w.game, web.DocsWriteManyInput{
				Items: []web.DocsWriteItemInput{{
					Path: e2eLeadScript, Content: "# no\n", ExpectedVersion: int32Ptr(6),
				}},
			})
			return err
		},
		"docs.history": func() error {
			_, err := web.MCPDocsHistory(ctx, w.deps, w.guest, w.game, web.DocsHistoryInput{Path: e2eLeadScript})
			return err
		},
		"docs.read_version": func() error {
			_, err := web.MCPDocsReadVersion(ctx, w.deps, w.guest, w.game, web.DocsReadVersionInput{
				Path: e2eLeadScript, Version: 1,
			})
			return err
		},
		"docs.revert": func() error {
			_, err := web.MCPDocsRevert(ctx, w.deps, w.guest, w.game, web.DocsRevertInput{
				Path: e2eLeadScript, ToVersion: 1, ExpectedVersion: int32Ptr(6),
			})
			return err
		},
		"docs.diff": func() error {
			_, err := web.MCPDocsDiff(ctx, w.deps, w.guest, w.game, web.DocsDiffInput{
				Path: e2eLeadScript, FromVersion: 1, ToVersion: 2,
			})
			return err
		},
		"docs.links.list": func() error {
			_, err := web.MCPDocsLinksList(ctx, w.deps, w.guest, w.game, web.DocsLinksListInput{
				Path: e2eLeadScript,
			})
			return err
		},
		"docs.links.add": func() error {
			_, err := web.MCPDocsLinkAdd(ctx, w.deps, w.guest, w.game, web.DocsLinkAddInput{
				Path: e2eLeadScript, EntityType: "quest", EntityKey: e2eLeadQuest,
			})
			return err
		},
		"docs.links.remove": func() error {
			_, err := web.MCPDocsLinkRemove(ctx, w.deps, w.guest, w.game, web.DocsLinkRemoveInput{
				Path: e2eLeadScript, EntityType: "quest", EntityKey: e2eLeadQuest,
			})
			return err
		},
		"docs.move": func() error {
			_, err := web.MCPDocsMove(ctx, w.deps, w.guest, w.game, web.DocsMoveInput{
				From: e2eLeadScript, To: "scripts/moved-by-a-guest.md",
				ExpectedVersion: int32Ptr(6),
			})
			return err
		},
		"docs.kinds": func() error {
			_, err := web.MCPDocsKinds(ctx, w.deps, w.guest, w.game, web.DocsKindsInput{})
			return err
		},
		"search": func() error {
			_, err := web.MCPSearch(ctx, w.deps, w.guest, w.game, web.SearchInput{Query: e2eDialogueWord})
			return err
		},
	}

	swept := 0
	for _, name := range w.srv.ScopedToolNamesForTest() {
		if !strings.HasPrefix(name, "docs.") && name != "search" {
			continue
		}
		call, ok := calls[name]
		assert.Must(t, ok, "%s is registered and this sweep does not drive it", name)
		swept++
		if err := call(); !errors.Is(err, web.ErrScopeViolation) {
			t.Fatalf("%s answered %v for another game's token, want a scope violation", name, err)
		}
	}
	assert.Must(t, swept == len(calls), "swept %d tools, the table has %d entries", swept, len(calls))
	assert.Must(t, swept == 15, "swept %d tools; the fourteen docs.* plus search is 15", swept)

	// The document is untouched by all of that: the sweep must not have
	// been a sequence of refusals that nevertheless changed something.
	if after := w.read(t, e2eLeadScript); after.Version != 6 {
		t.Fatalf("the lead script is at version %d after the sweep, want 6", after.Version)
	}
}

// TestAnAgentDrivesTheProseToolsOverHTTP is the other half of the
// definition of done, and it is a separate test because it asks a
// different question: everything above calls the tool functions in
// process, and this drives them the way an agent's client actually does
// — over the mounted MCP transport, with a bearer token, reading the
// conflict's `details` off the wire rather than off a Go error.
func TestAnAgentDrivesTheProseToolsOverHTTP(t *testing.T) {
	t.Parallel()
	w := newProseWorld(t)
	ctx := context.Background()

	httpSrv := httptest.NewServer(w.srv)
	defer httpSrv.Close()
	session := connectMCP(t, httpSrv.URL, w.token)

	tools, err := session.ListTools(ctx, nil)
	assert.Must(t, err == nil, "tools/list: %v", err)
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{
		"docs.write", "docs.read", "docs.list", "docs.delete", "docs.history",
		"docs.read_version", "docs.revert", "docs.diff",
		"docs.links.list", "docs.links.add", "docs.links.remove", "search",
	} {
		assert.Must(t, names[want], "tools/list does not carry %s: %v", want, names)
	}

	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		assert.Must(t, err == nil, "%s: %v", name, err)
		return result
	}

	written := call("docs.write", map[string]any{
		"game": w.gameSlug, "path": e2eLeadScript, "content": e2eLeadV1,
		"kind": "script", "message": "over the wire", "expected_version": 0,
		"links": []map[string]any{
			{"entity_type": "quest", "entity_key": e2eLeadQuest, "role": "script"},
		},
	})
	if written.IsError {
		var why map[string]any
		decodeToolText(t, written, &why)
		t.Fatalf("docs.write over the wire failed: %v", why)
	}
	var doc web.DocumentOutput
	decodeStructured(t, written, &doc)
	assert.Must(t, doc.Version == 1 && len(doc.Links) == 1, "docs.write answered %+v", doc)

	// A conflict, read as an agent reads it: the wire's own details
	// object, carrying the version to merge onto and the body to merge.
	call("docs.write", map[string]any{
		"game": w.gameSlug, "path": e2eLeadScript, "content": e2eLeadV2,
		"message": "act two", "expected_version": 1,
	})
	conflict := call("docs.write", map[string]any{
		"game": w.gameSlug, "path": e2eLeadScript, "content": e2eLeadV1,
		"message": "stale", "expected_version": 1,
	})
	assert.Must(t, conflict.IsError, "a stale write over the wire succeeded")
	var wire struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Details struct {
			CurrentVersion int32  `json:"current_version"`
			CurrentBody    string `json:"current_body"`
			CurrentTitle   string `json:"current_title"`
		} `json:"details"`
	}
	decodeToolText(t, conflict, &wire)
	assert.Must(t, wire.Error == "version_conflict", "the wire called a conflict %q: %s", wire.Error, wire.Message)
	assert.Must(t, wire.Details.CurrentVersion == 2, "details.current_version on the wire = %d, want 2", wire.Details.CurrentVersion)
	assert.Must(t, strings.Contains(wire.Details.CurrentBody, "# Act two"), "details.current_body on the wire = %q", wire.Details.CurrentBody)
	assert.Must(t, wire.Details.CurrentTitle == "The Defias Brotherhood", "details.current_title on the wire = %q", wire.Details.CurrentTitle)

	// And the reads an agent makes next, over the same transport.
	var history web.DocsHistoryOutput
	decodeStructured(t, call("docs.history", map[string]any{
		"game": w.gameSlug, "path": e2eLeadScript,
	}), &history)
	assert.Must(t, len(history.Items) == 2 && history.Items[0].AuthorKind == "token", "docs.history over the wire = %+v", history.Items)

	var diff web.DocsDiffOutput
	decodeStructured(t, call("docs.diff", map[string]any{
		"game": w.gameSlug, "path": e2eLeadScript, "from_version": 1, "to_version": 2,
	}), &diff)
	assert.Must(t, !diff.Coarse && strings.Contains(diff.Unified, "+# Act two"), "docs.diff over the wire = %+v", diff)

	var reverted web.DocumentOutput
	decodeStructured(t, call("docs.revert", map[string]any{
		"game": w.gameSlug, "path": e2eLeadScript, "to_version": 1, "expected_version": 2,
	}), &reverted)
	assert.Must(t, reverted.Version == 3, "docs.revert over the wire landed at %d, want 3", reverted.Version)

	var fromDoc web.DocsLinksOutput
	decodeStructured(t, call("docs.links.list", map[string]any{
		"game": w.gameSlug, "path": e2eLeadScript,
	}), &fromDoc)
	assert.Must(t, len(fromDoc.Entities) == 1 && fromDoc.Entities[0].EntityKey == e2eLeadQuest, "docs.links.list by path over the wire = %+v", fromDoc)
	var fromEntity web.DocsLinksOutput
	decodeStructured(t, call("docs.links.list", map[string]any{
		"game": w.gameSlug, "entity_type": "quest", "entity_key": e2eLeadQuest,
	}), &fromEntity)
	assert.Must(t, len(fromEntity.Documents) == 1 && fromEntity.Documents[0].Path == e2eLeadScript, "docs.links.list by entity over the wire = %+v", fromEntity)

	var found web.SearchOutput
	decodeStructured(t, call("search", map[string]any{
		"game": w.gameSlug, "query": "Defias",
	}), &found)
	kinds := map[string]int{}
	for _, hit := range found.Items {
		kinds[hit.Kind]++
	}
	assert.Must(t, kinds["entity"] != 0 && kinds["document"] != 0, "search over the wire returned %v, want both kinds", kinds)
	if found.Items[0].Kind != "entity" || !found.Items[0].NameMatch {
		t.Fatalf("search over the wire did not rank the named entity first: %+v", found.Items[0])
	}
}
