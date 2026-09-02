package web_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/testutil"
	"github.com/neverbot/maestro/internal/web"
)

// seedQuest declares a quest type and one quest in the fixture's own
// game, so a document has something to be attached to. Links are the one
// part of this surface that cannot be exercised without the metamodel.
func seedQuest(t *testing.T, f metamodelFixture, key, name string) {
	t.Helper()
	ctx := context.Background()
	if _, err := web.MCPTypesUpsert(ctx, f.deps, f.caller, f.game, web.TypesUpsertInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	}); err != nil {
		t.Fatalf("MCPTypesUpsert: %v", err)
	}
	if _, err := web.MCPEntitiesUpsert(ctx, f.deps, f.caller, f.game, web.EntitiesUpsertInput{
		Items: []web.EntityItemInput{{TypeKey: "quest", Key: key, Name: name}},
	}); err != nil {
		t.Fatalf("MCPEntitiesUpsert: %v", err)
	}
}

func int32Ptr(v int32) *int32    { return &v }
func stringPtr(v string) *string { return &v }
func boolPtr(v bool) *bool       { return &v }

const hoggerContent = "---\ntitle: The Fall of Hogger\n" +
	"summary: How the gnoll met his end.\nmood: grim\n---\n" +
	"# Act one\n\nHogger says: \"Rrrrr!\"\n"

// TestEveryDocumentFieldSurvivesARoundTripThroughTheTools is the test
// this whole task is organised around.
//
// It writes one document carrying every field the surface accepts —
// path, content with frontmatter, kind, message, links with roles — and
// then reads every one of them back through docs.read, docs.list,
// docs.history, docs.read_version and docs.links.list. A field that goes
// in and does not come out fails here.
//
// It is not a smoke test and it is not redundant with the domain tests:
// the domain tests prove the service stores things, and this proves the
// *surface* hands them back. A relation's field values were stored
// correctly, and unreadable through either surface, for nine review
// rounds, because everything verified that writing worked and nothing
// asked whether it could be read back.
func TestEveryDocumentFieldSurvivesARoundTripThroughTheTools(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()
	seedQuest(t, f, "wanted-hogger", "Wanted: Hogger")

	written, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
		Path:            "lore/hogger.md",
		Content:         hoggerContent,
		Kind:            stringPtr("script"),
		Message:         "the first draft",
		ExpectedVersion: int32Ptr(0),
		Links: &[]web.DocsLinkInput{
			{EntityType: "quest", EntityKey: "wanted-hogger", Role: "script"},
		},
	})
	if err != nil {
		t.Fatalf("MCPDocsWrite: %v", err)
	}

	// Every field of the write's own answer.
	if written.Path != "lore/hogger.md" || written.Kind != "script" || written.Version != 1 {
		t.Fatalf("written = %+v, want the path, the kind and version 1", written)
	}
	if written.Title != "The Fall of Hogger" || written.Summary != "How the gnoll met his end." {
		t.Fatalf("title/summary = %q/%q, want the frontmatter's", written.Title, written.Summary)
	}
	wantBody := "# Act one\n\nHogger says: \"Rrrrr!\"\n"
	if written.Body != wantBody {
		t.Fatalf("body = %q, want the raw markdown %q", written.Body, wantBody)
	}
	if written.Truncated || written.BodyLength != len(wantBody) {
		t.Fatalf("truncated/body_length = %v/%d, want false and %d",
			written.Truncated, written.BodyLength, len(wantBody))
	}
	if written.Deleted {
		t.Fatal("a document that was just written is not deleted")
	}
	var frontmatter map[string]any
	if err := json.Unmarshal(written.Frontmatter, &frontmatter); err != nil {
		t.Fatalf("decode frontmatter %s: %v", written.Frontmatter, err)
	}
	if frontmatter["mood"] != "grim" {
		t.Fatalf("frontmatter = %v, want the uninterpreted keys echoed back", frontmatter)
	}
	if len(written.Links) != 1 || written.Links[0].EntityKey != "wanted-hogger" ||
		written.Links[0].Role != "script" || written.Links[0].Name != "Wanted: Hogger" ||
		written.Links[0].EntityTypeKey != "quest" {
		t.Fatalf("links = %+v, want the attachment with its role and label", written.Links)
	}

	// docs.read hands back exactly the same document.
	read, err := web.MCPDocsRead(ctx, f.deps, f.caller, f.game, web.DocsReadInput{Path: "lore/hogger.md"})
	if err != nil {
		t.Fatalf("MCPDocsRead: %v", err)
	}
	if read.ID != written.ID {
		t.Fatalf("read id %s, want the written %s", read.ID, written.ID)
	}
	if read.Body != wantBody || read.Kind != "script" || read.Title != written.Title ||
		read.Summary != written.Summary || read.Version != 1 || len(read.Links) != 1 {
		t.Fatalf("read = %+v, want the written document", read)
	}
	if string(read.Frontmatter) != string(written.Frontmatter) {
		t.Fatalf("frontmatter %s, want %s", read.Frontmatter, written.Frontmatter)
	}
	// A path matches without regard to case, and the answer is the
	// stored spelling and not the caller's.
	upper, err := web.MCPDocsRead(ctx, f.deps, f.caller, f.game, web.DocsReadInput{Path: "LORE/HOGGER.MD"})
	if err != nil {
		t.Fatalf("MCPDocsRead in another case: %v", err)
	}
	if upper.Path != "lore/hogger.md" {
		t.Fatalf("path = %q, want the stored spelling", upper.Path)
	}

	// docs.list carries the summary fields and no body.
	list, err := web.MCPDocsList(ctx, f.deps, f.caller, f.game, web.DocsListInput{})
	if err != nil {
		t.Fatalf("MCPDocsList: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("list = %+v, want one row", list.Items)
	}
	row := list.Items[0]
	if row.ID != written.ID || row.Path != written.Path || row.Kind != "script" ||
		row.Title != written.Title || row.Summary != written.Summary ||
		row.Version != 1 || row.Deleted {
		t.Fatalf("listing row = %+v, want every summary field of the written document", row)
	}
	if list.Truncated || list.NextCursor != nil {
		t.Fatalf("a one-row listing says truncated %v, cursor %v", list.Truncated, list.NextCursor)
	}
	// Filtering by the entity is the way to the script without knowing
	// the path.
	byEntity, err := web.MCPDocsList(ctx, f.deps, f.caller, f.game, web.DocsListInput{
		EntityType: "quest", EntityKey: "wanted-hogger",
	})
	if err != nil {
		t.Fatalf("MCPDocsList by entity: %v", err)
	}
	if len(byEntity.Items) != 1 || byEntity.Items[0].Path != "lore/hogger.md" {
		t.Fatalf("listing by entity = %+v, want the attached document", byEntity.Items)
	}

	// docs.history carries the message and the author.
	history, err := web.MCPDocsHistory(ctx, f.deps, f.caller, f.game, web.DocsHistoryInput{
		Path: "lore/hogger.md",
	})
	if err != nil {
		t.Fatalf("MCPDocsHistory: %v", err)
	}
	if len(history.Items) != 1 {
		t.Fatalf("history = %+v, want one version", history.Items)
	}
	version := history.Items[0]
	if version.Version != 1 || version.Message != "the first draft" ||
		version.Title != written.Title || version.Summary != written.Summary || version.Deleted {
		t.Fatalf("version row = %+v, want the write's own metadata", version)
	}
	if version.AuthorKind != "token" || version.AuthorID == nil {
		t.Fatalf("author = %q/%v, want the agent's own token", version.AuthorKind, version.AuthorID)
	}
	if version.CreatedAt.IsZero() {
		t.Fatal("a version with no timestamp is a version nobody can order")
	}

	// docs.read_version carries the body, byte for byte.
	past, err := web.MCPDocsReadVersion(ctx, f.deps, f.caller, f.game, web.DocsReadVersionInput{
		Path: "lore/hogger.md", Version: 1,
	})
	if err != nil {
		t.Fatalf("MCPDocsReadVersion: %v", err)
	}
	if past.Body != wantBody || past.Title != written.Title || past.Message != "the first draft" ||
		past.Version != 1 || past.Deleted || past.Path != "lore/hogger.md" {
		t.Fatalf("version = %+v, want the stored snapshot", past)
	}
	if string(past.Frontmatter) != string(written.Frontmatter) {
		t.Fatalf("version frontmatter %s, want %s", past.Frontmatter, written.Frontmatter)
	}

	// docs.links.list answers from both sides.
	fromDoc, err := web.MCPDocsLinksList(ctx, f.deps, f.caller, f.game, web.DocsLinksListInput{
		Path: "lore/hogger.md",
	})
	if err != nil {
		t.Fatalf("MCPDocsLinksList by path: %v", err)
	}
	if len(fromDoc.Entities) != 1 || fromDoc.Entities[0].EntityKey != "wanted-hogger" ||
		fromDoc.Entities[0].Role != "script" {
		t.Fatalf("entities = %+v, want the attachment", fromDoc.Entities)
	}
	if len(fromDoc.Documents) != 0 {
		t.Fatalf("documents = %+v, want the other side empty", fromDoc.Documents)
	}
	fromEntity, err := web.MCPDocsLinksList(ctx, f.deps, f.caller, f.game, web.DocsLinksListInput{
		EntityType: "quest", EntityKey: "wanted-hogger",
	})
	if err != nil {
		t.Fatalf("MCPDocsLinksList by entity: %v", err)
	}
	if len(fromEntity.Documents) != 1 || fromEntity.Documents[0].Path != "lore/hogger.md" ||
		fromEntity.Documents[0].Role != "script" || fromEntity.Documents[0].Kind != "script" ||
		fromEntity.Documents[0].Title != written.Title ||
		fromEntity.Documents[0].ID != written.ID {
		t.Fatalf("documents = %+v, want the attached document", fromEntity.Documents)
	}
	if len(fromEntity.Entities) != 0 {
		t.Fatalf("entities = %+v, want the other side empty", fromEntity.Entities)
	}
}

// TestAnEditThatOmitsKindLeavesItAloneThroughTheTool is the wire half of
// the domain's own TestAnEditThatOmitsKindLeavesItUnchanged: a plain
// string with omitempty could not tell an omitted kind from an explicit
// empty one, and every body-only edit would have erased the kind.
func TestAnEditThatOmitsKindLeavesItAloneThroughTheTool(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	if _, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
		Path: "lore/zone.md", Content: "first", Kind: stringPtr("lore"),
		ExpectedVersion: int32Ptr(0),
	}); err != nil {
		t.Fatalf("MCPDocsWrite: %v", err)
	}
	edited, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
		Path: "lore/zone.md", Content: "second", ExpectedVersion: int32Ptr(1),
	})
	if err != nil {
		t.Fatalf("MCPDocsWrite editing: %v", err)
	}
	if edited.Kind != "lore" {
		t.Fatalf("kind = %q after an edit that said nothing about it, want lore", edited.Kind)
	}
	cleared, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
		Path: "lore/zone.md", Content: "third", Kind: stringPtr(""),
		ExpectedVersion: int32Ptr(2),
	})
	if err != nil {
		t.Fatalf("MCPDocsWrite clearing: %v", err)
	}
	if cleared.Kind != "" {
		t.Fatalf("kind = %q after an explicit empty one, want it cleared", cleared.Kind)
	}
}

// TestALinksArrayReplacesTheSetAndOmittingItPreservesIt is the wire half
// of the domain's own four-case test. The case that matters is the third
// one: an ordinary edit that says nothing about links must not detach
// them, and an explicit empty array must.
func TestALinksArrayReplacesTheSetAndOmittingItPreservesIt(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()
	seedQuest(t, f, "wanted-hogger", "Wanted: Hogger")

	if _, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
		Path: "lore/hogger.md", Content: "one", ExpectedVersion: int32Ptr(0),
		Links: &[]web.DocsLinkInput{{EntityType: "quest", EntityKey: "wanted-hogger"}},
	}); err != nil {
		t.Fatalf("MCPDocsWrite: %v", err)
	}
	kept, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
		Path: "lore/hogger.md", Content: "two", ExpectedVersion: int32Ptr(1),
	})
	if err != nil {
		t.Fatalf("MCPDocsWrite omitting links: %v", err)
	}
	if len(kept.Links) != 1 {
		t.Fatalf("links = %+v after an edit that omitted them, want them preserved", kept.Links)
	}
	detached, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
		Path: "lore/hogger.md", Content: "three", ExpectedVersion: int32Ptr(2),
		Links: &[]web.DocsLinkInput{},
	})
	if err != nil {
		t.Fatalf("MCPDocsWrite with an empty links array: %v", err)
	}
	if len(detached.Links) != 0 {
		t.Fatalf("links = %+v after an empty array, want everything detached", detached.Links)
	}
}

// TestADocsWriteWithoutAnExpectedVersionIsInvalidInputAtItsOwnPath
// asserts the code and the path, not that an error happened. This is the
// one argument the SDK's schema validation does not enforce for us:
// making it required there would report a missing one as prose with no
// code (addScopedTool's own doc comment records the exception), so it is
// a pointer and the domain refuses a nil one.
func TestADocsWriteWithoutAnExpectedVersionIsInvalidInputAtItsOwnPath(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	_, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
		Path: "lore/hogger.md", Content: "body",
	})
	assertInvalidInputAt(t, err, "expected_version")

	_, err = web.MCPDocsDelete(ctx, f.deps, f.caller, f.game, web.DocsDeleteInput{
		Path: "lore/hogger.md",
	})
	assertInvalidInputAt(t, err, "expected_version")

	_, err = web.MCPDocsRevert(ctx, f.deps, f.caller, f.game, web.DocsRevertInput{
		Path: "lore/hogger.md", ToVersion: 1,
	})
	assertInvalidInputAt(t, err, "expected_version")
}

// assertInvalidInputAt fails unless err is an invalid_input naming path
// among its field problems. Every negative test in this project asserts
// the specific code and the specific path, not merely that something
// went wrong.
//
// It handles both error shapes this surface produces, because the two
// are genuinely different and a caller cannot tell them apart: a refusal
// the *domain* made is a metamodel.ValidationError carrying the
// ErrInvalidInput sentinel, and a refusal internal/web made about an
// argument it parsed itself is an *web.MCPError carrying the same wire
// code and the same {"fields":[{"path","message"}]} details shape.
func assertInvalidInputAt(t *testing.T, err error, path string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error at all, want invalid_input naming %q", path)
	}
	var mcpErr *web.MCPError
	if errors.As(err, &mcpErr) {
		if mcpErr.Code != "invalid_input" {
			t.Fatalf("code = %q, want invalid_input", mcpErr.Code)
		}
		problems, ok := mcpErr.Details["fields"].([]map[string]string)
		if !ok {
			t.Fatalf("details = %v, carry no field problems", mcpErr.Details)
		}
		for _, problem := range problems {
			if problem["path"] == path {
				return
			}
		}
		t.Fatalf("fields = %v, want a problem at %q", problems, path)
	}
	if !errors.Is(err, metamodel.ErrInvalidInput) {
		t.Fatalf("err = %v, want invalid_input", err)
	}
	var validation *metamodel.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("err = %#v, carries no field problems at all", err)
	}
	for _, problem := range validation.Fields {
		if problem.Path == path {
			return
		}
	}
	t.Fatalf("fields = %+v, want a problem at %q", validation.Fields, path)
}

// TestAskingTheJoinFromBothSidesAtOnceIsInvalidInput pins that
// docs.links.list refuses a call that names both addresses and one that
// names neither. "The join, from either side" is two questions, and a
// caller naming both has not decided which it is asking.
func TestAskingTheJoinFromBothSidesAtOnceIsInvalidInput(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	_, err := web.MCPDocsLinksList(ctx, f.deps, f.caller, f.game, web.DocsLinksListInput{
		Path: "lore/hogger.md", EntityType: "quest", EntityKey: "wanted-hogger",
	})
	assertInvalidInputAt(t, err, "path")

	_, err = web.MCPDocsLinksList(ctx, f.deps, f.caller, f.game, web.DocsLinksListInput{})
	assertInvalidInputAt(t, err, "path")
}

// TestTheLinkToolsReadTheirOwnResultBack pins that docs.links.add and
// docs.links.remove answer with the document's whole attachment set
// after the change, so a caller sees what it did without a second call.
func TestTheLinkToolsReadTheirOwnResultBack(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()
	seedQuest(t, f, "wanted-hogger", "Wanted: Hogger")

	if _, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
		Path: "lore/hogger.md", Content: "body", ExpectedVersion: int32Ptr(0),
	}); err != nil {
		t.Fatalf("MCPDocsWrite: %v", err)
	}

	added, err := web.MCPDocsLinkAdd(ctx, f.deps, f.caller, f.game, web.DocsLinkAddInput{
		Path: "lore/hogger.md", EntityType: "quest", EntityKey: "wanted-hogger", Role: "script",
	})
	if err != nil {
		t.Fatalf("MCPDocsLinkAdd: %v", err)
	}
	if len(added.Entities) != 1 || added.Entities[0].Role != "script" {
		t.Fatalf("entities = %+v, want the attachment it just made", added.Entities)
	}

	removed, err := web.MCPDocsLinkRemove(ctx, f.deps, f.caller, f.game, web.DocsLinkRemoveInput{
		Path: "lore/hogger.md", EntityType: "quest", EntityKey: "wanted-hogger",
	})
	if err != nil {
		t.Fatalf("MCPDocsLinkRemove: %v", err)
	}
	if len(removed.Entities) != 0 {
		t.Fatalf("entities = %+v, want the set empty after the detachment", removed.Entities)
	}
	// Empty, never nil: an empty attachment set marshals as [] and not
	// as null, on both sides.
	raw, err := json.Marshal(removed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"entities":[]`) ||
		!strings.Contains(string(raw), `"documents":[]`) {
		t.Fatalf("marshalled %s, want both arrays as []", raw)
	}
}

// TestIncludeCurrentDefaultsToTrueOnTheWireAndIsHonouredWhenFalse pins
// the one default this file owns. markdown.WriteInput.IncludeCurrent is
// false by its Go zero value so a domain caller never gets a 200 KB body
// it did not ask for; the *tool* defaults it to true, because an agent
// that does not know about the argument is exactly the agent that most
// needs the current body handed to it on a conflict.
func TestIncludeCurrentDefaultsToTrueOnTheWireAndIsHonouredWhenFalse(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	if _, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
		Path: "lore/hogger.md", Content: "the stored body\n", ExpectedVersion: int32Ptr(0),
	}); err != nil {
		t.Fatalf("MCPDocsWrite: %v", err)
	}

	_, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
		Path: "lore/hogger.md", Content: "mine", ExpectedVersion: int32Ptr(0),
	})
	details := conflictDetails(t, err)
	if details["current_body"] != "the stored body\n" {
		t.Fatalf("details = %v, want the body echoed by default", details)
	}

	_, err = web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
		Path: "lore/hogger.md", Content: "mine", ExpectedVersion: int32Ptr(0),
		IncludeCurrent: boolPtr(false),
	})
	quiet := conflictDetails(t, err)
	if _, present := quiet["current_body"]; present {
		t.Fatalf("details = %v, want no body when the echo is turned off", quiet)
	}
	if quiet["current_version"] != int32(1) {
		t.Fatalf("details = %v, want the current version even with the echo off", quiet)
	}
}

// conflictDetails pulls the structured payload off a version conflict,
// which is what an agent merges from.
func conflictDetails(t *testing.T, err error) map[string]any {
	t.Helper()
	var conflict *markdown.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want a version conflict", err)
	}
	return conflict.Details()
}

// TestAHeadOnlyReadSaysHowMuchItLeftOut pins the three things a head
// read has to get right: the flag, the true length, and a preview that
// is still valid UTF-8 when the byte bound lands in the middle of a
// rune. The body is built out of three-byte runes so that the cut at
// 2048 bytes falls mid-rune — 2048 is not a multiple of 3.
func TestAHeadOnlyReadSaysHowMuchItLeftOut(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	body := strings.Repeat("あ", 2000) // 6,000 bytes, none of them ASCII.
	if _, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
		Path: "lore/long.md", Content: body, ExpectedVersion: int32Ptr(0),
	}); err != nil {
		t.Fatalf("MCPDocsWrite: %v", err)
	}

	head, err := web.MCPDocsRead(ctx, f.deps, f.caller, f.game, web.DocsReadInput{
		Path: "lore/long.md", HeadOnly: true,
	})
	if err != nil {
		t.Fatalf("MCPDocsRead head_only: %v", err)
	}
	if !head.Truncated {
		t.Fatal("a body cut short says so")
	}
	if head.BodyLength != len(body) {
		t.Fatalf("body_length = %d, want the whole body's %d", head.BodyLength, len(body))
	}
	if len(head.Body) >= len(body) {
		t.Fatalf("the preview is %d bytes of a %d-byte body", len(head.Body), len(body))
	}
	if !utf8.ValidString(head.Body) {
		t.Fatalf("the preview is not valid UTF-8: %q", head.Body[len(head.Body)-8:])
	}
	if !strings.HasPrefix(body, head.Body) {
		t.Fatal("the preview is not a prefix of the body")
	}

	// The same read without head_only is the whole document, and says
	// so: false is a statement.
	whole, err := web.MCPDocsRead(ctx, f.deps, f.caller, f.game, web.DocsReadInput{Path: "lore/long.md"})
	if err != nil {
		t.Fatalf("MCPDocsRead: %v", err)
	}
	if whole.Truncated || whole.Body != body || whole.BodyLength != len(body) {
		t.Fatalf("a full read says truncated %v over %d bytes", whole.Truncated, len(whole.Body))
	}
}

// TestEveryDocsToolRefusesAnotherGamesToken is the isolation test for
// this whole surface, driven from the *registered* tool list rather than
// a hand-written one: a docs tool with no entry in the table below fails
// this test, so a twelfth tool added tomorrow is covered without anybody
// remembering to edit it.
//
// The game it calls at is one the caller's own *user* owns and the
// caller's own *token* is not bound to, which is the case requireScope
// exists for — an instance admin gets no exemption either.
func TestEveryDocsToolRefusesAnotherGamesToken(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	calls := map[string]func() error{
		"docs.write": func() error {
			_, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.other, web.DocsWriteInput{
				Path: "lore/x.md", Content: "body", ExpectedVersion: int32Ptr(0),
			})
			return err
		},
		"docs.read": func() error {
			_, err := web.MCPDocsRead(ctx, f.deps, f.caller, f.other, web.DocsReadInput{Path: "lore/x.md"})
			return err
		},
		"docs.list": func() error {
			_, err := web.MCPDocsList(ctx, f.deps, f.caller, f.other, web.DocsListInput{})
			return err
		},
		"docs.delete": func() error {
			_, err := web.MCPDocsDelete(ctx, f.deps, f.caller, f.other, web.DocsDeleteInput{
				Path: "lore/x.md", ExpectedVersion: int32Ptr(1),
			})
			return err
		},
		"docs.history": func() error {
			_, err := web.MCPDocsHistory(ctx, f.deps, f.caller, f.other, web.DocsHistoryInput{Path: "lore/x.md"})
			return err
		},
		"docs.read_version": func() error {
			_, err := web.MCPDocsReadVersion(ctx, f.deps, f.caller, f.other, web.DocsReadVersionInput{
				Path: "lore/x.md", Version: 1,
			})
			return err
		},
		"docs.revert": func() error {
			_, err := web.MCPDocsRevert(ctx, f.deps, f.caller, f.other, web.DocsRevertInput{
				Path: "lore/x.md", ToVersion: 1, ExpectedVersion: int32Ptr(1),
			})
			return err
		},
		"docs.diff": func() error {
			_, err := web.MCPDocsDiff(ctx, f.deps, f.caller, f.other, web.DocsDiffInput{
				Path: "lore/x.md", FromVersion: 1, ToVersion: 2,
			})
			return err
		},
		"docs.links.list": func() error {
			_, err := web.MCPDocsLinksList(ctx, f.deps, f.caller, f.other, web.DocsLinksListInput{
				Path: "lore/x.md",
			})
			return err
		},
		"docs.links.add": func() error {
			_, err := web.MCPDocsLinkAdd(ctx, f.deps, f.caller, f.other, web.DocsLinkAddInput{
				Path: "lore/x.md", EntityType: "quest", EntityKey: "wanted-hogger",
			})
			return err
		},
		"docs.links.remove": func() error {
			_, err := web.MCPDocsLinkRemove(ctx, f.deps, f.caller, f.other, web.DocsLinkRemoveInput{
				Path: "lore/x.md", EntityType: "quest", EntityKey: "wanted-hogger",
			})
			return err
		},
	}

	registered := 0
	for _, name := range f.srv.ScopedToolNamesForTest() {
		if !strings.HasPrefix(name, "docs.") {
			continue
		}
		registered++
		call, ok := calls[name]
		if !ok {
			t.Fatalf("%s is registered and this test does not drive it: add it to the table, "+
				"or one game's agent reaches another game's prose unnoticed", name)
		}
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, web.ErrScopeViolation) {
				t.Fatalf("err = %v, want a scope violation", err)
			}
		})
	}
	if registered != len(calls) {
		t.Fatalf("%d docs tools are registered and the table has %d entries", registered, len(calls))
	}
	if registered == 0 {
		t.Fatal("no docs tool is registered at all; this test would pass vacuously")
	}
}

// TestTheDocsToolsAreAbsentWithoutAMarkdownService pins the optional
// half of MCPDeps.Markdown: a server built without one still starts, and
// tools/list simply does not carry the eleven.
func TestTheDocsToolsAreAbsentWithoutAMarkdownService(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := config.Config{
		SessionTTL: testConfig().SessionTTL,
		InviteTTL:  testConfig().InviteTTL,
		Argon2:     testConfig().Argon2,
	}
	ids := identity.New(pool, cfg)
	projSvc := projects.New(pool)
	srv := web.NewServer(web.Options{
		Version: "test", Config: cfg, Identity: ids, Projects: projSvc,
		Metamodel: metamodel.New(pool, nil),
	})

	ctx := context.Background()
	user, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "no-prose@studio.com", DisplayName: "Designer", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	game, err := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: game.ID, UserID: user.ID, Label: "agent",
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	httpSrv := httptest.NewServer(srv)
	defer httpSrv.Close()
	session := connectMCP(t, httpSrv.URL, token)

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range tools.Tools {
		if strings.HasPrefix(tool.Name, "docs.") {
			t.Fatalf("%s is served by an instance with no markdown service behind it", tool.Name)
		}
	}
	if len(tools.Tools) == 0 {
		t.Fatal("the server served no tools at all; this test would pass vacuously")
	}
}

// TestTheDocsToolsAreServedOverTheRealTransport is the read-back this
// task owes through the wire rather than through the Go functions:
// eleven tool names in tools/list, and a write, a conflict, a revert, a
// diff and a delete driven as an agent drives them, with every answer
// read out of the JSON a client actually parses.
func TestTheDocsToolsAreServedOverTheRealTransport(t *testing.T) {
	f := newMetamodelFixture(t)
	httpSrv := httptest.NewServer(f.srv)
	defer httpSrv.Close()
	ctx := context.Background()
	session := connectMCP(t, httpSrv.URL, f.token)

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{
		"docs.write", "docs.read", "docs.list", "docs.delete", "docs.history",
		"docs.read_version", "docs.revert", "docs.diff", "docs.links.list",
		"docs.links.add", "docs.links.remove",
	} {
		if !names[want] {
			t.Fatalf("the served tool list is missing %q", want)
		}
	}
	// A tool description is documentation an agent acts on, so an empty
	// one is a tool nobody can use correctly; this is the only place the
	// *served* text is looked at at all.
	//
	// The %! check below is a backstop and not the primary guard: every
	// format string here is a constant, so `go vet` (which `make check`
	// runs) already rejects a mismatched argument list at build time —
	// the fmt.Sprintf-arity mutation for this file was refused by the
	// compiler's vet pass, not by this assertion. It is kept for the
	// case vet cannot see, a format string that stops being constant,
	// and it is honest to say it has never been the thing that caught
	// one.
	for _, tool := range tools.Tools {
		if !strings.HasPrefix(tool.Name, "docs.") {
			continue
		}
		if tool.Description == "" {
			t.Fatalf("%s is served with no description at all", tool.Name)
		}
		if strings.Contains(tool.Description, "%!") {
			t.Fatalf("%s's description has a formatting fault in it: %s",
				tool.Name, tool.Description)
		}
	}

	var written struct {
		Version int32  `json:"version"`
		Body    string `json:"body"`
		Title   string `json:"title"`
	}
	decodeStructured(t, callOK(t, session, "docs.write", map[string]any{
		"path": "lore/hogger.md", "content": hoggerContent,
		"kind": "script", "message": "the first draft", "expected_version": 0,
	}), &written)
	if written.Version != 1 || written.Title != "The Fall of Hogger" {
		t.Fatalf("written = %+v, want version 1 titled from the frontmatter", written)
	}

	decodeStructured(t, callOK(t, session, "docs.write", map[string]any{
		"path": "lore/hogger.md", "content": "second draft\n",
		"message": "rewrite", "expected_version": 1,
	}), &written)
	if written.Version != 2 || written.Body != "second draft\n" {
		t.Fatalf("written = %+v, want version 2 carrying the new body", written)
	}

	// A stale write is a version_conflict carrying the current body, and
	// that is what an agent parses out of the error result.
	stale, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "docs.write",
		Arguments: map[string]any{
			"path": "lore/hogger.md", "content": "third", "expected_version": 1,
		},
	})
	if err != nil {
		t.Fatalf("CallTool(docs.write): %v", err)
	}
	if !stale.IsError {
		t.Fatal("writing onto a version that moved must fail")
	}
	var conflict struct {
		Error   string `json:"error"`
		Details struct {
			CurrentVersion int32  `json:"current_version"`
			CurrentBody    string `json:"current_body"`
		} `json:"details"`
	}
	decodeToolText(t, stale, &conflict)
	if conflict.Error != "version_conflict" {
		t.Fatalf("error = %q, want version_conflict", conflict.Error)
	}
	if conflict.Details.CurrentVersion != 2 || conflict.Details.CurrentBody != "second draft\n" {
		t.Fatalf("details = %+v, want the current version and body to merge onto", conflict.Details)
	}

	// docs.diff over the two versions, and the flag that says whether it
	// was computed line by line.
	var diff struct {
		Unified string `json:"unified"`
		Coarse  bool   `json:"coarse"`
	}
	decodeStructured(t, callOK(t, session, "docs.diff", map[string]any{
		"path": "lore/hogger.md", "from_version": 1, "to_version": 2,
	}), &diff)
	if diff.Coarse {
		t.Fatalf("a two-line change came back coarse: %q", diff.Unified)
	}
	if !strings.Contains(diff.Unified, "+second draft") {
		t.Fatalf("unified = %q, want the added line", diff.Unified)
	}

	// docs.revert writes forward.
	var reverted struct {
		Version int32  `json:"version"`
		Body    string `json:"body"`
	}
	decodeStructured(t, callOK(t, session, "docs.revert", map[string]any{
		"path": "lore/hogger.md", "to_version": 1, "expected_version": 2,
	}), &reverted)
	if reverted.Version != 3 || !strings.Contains(reverted.Body, "Act one") {
		t.Fatalf("reverted = %+v, want version 3 carrying version 1's body", reverted)
	}

	// docs.delete is soft, and the version it answers with is the one
	// that brings the document back.
	var deleted struct {
		Version int32 `json:"version"`
		Deleted bool  `json:"deleted"`
	}
	decodeStructured(t, callOK(t, session, "docs.delete", map[string]any{
		"path": "lore/hogger.md", "message": "cut", "expected_version": 3,
	}), &deleted)
	if !deleted.Deleted || deleted.Version != 4 {
		t.Fatalf("deleted = %+v, want the tombstone's version and deleted true", deleted)
	}

	gone, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "docs.read", Arguments: map[string]any{"path": "lore/hogger.md"},
	})
	if err != nil {
		t.Fatalf("CallTool(docs.read): %v", err)
	}
	if !gone.IsError {
		t.Fatal("a deleted document is not found")
	}

	var resurrected struct {
		Version int32 `json:"version"`
		Deleted bool  `json:"deleted"`
	}
	decodeStructured(t, callOK(t, session, "docs.write", map[string]any{
		"path": "lore/hogger.md", "content": "back", "expected_version": 4,
	}), &resurrected)
	if resurrected.Deleted || resurrected.Version != 5 {
		t.Fatalf("resurrected = %+v, want a live document at version 5", resurrected)
	}

	// The history is all five, newest first, with the tombstone marked.
	var history struct {
		Items []struct {
			Version    int32  `json:"version"`
			Deleted    bool   `json:"deleted"`
			AuthorKind string `json:"author_kind"`
			Message    string `json:"message"`
		} `json:"items"`
	}
	decodeStructured(t, callOK(t, session, "docs.history", map[string]any{
		"path": "lore/hogger.md",
	}), &history)
	if len(history.Items) != 5 || history.Items[0].Version != 5 {
		t.Fatalf("history = %+v, want five versions newest first", history.Items)
	}
	if !history.Items[1].Deleted || history.Items[1].Message != "cut" {
		t.Fatalf("version 4 = %+v, want the tombstone with its message", history.Items[1])
	}
	for _, item := range history.Items {
		if item.AuthorKind != "token" {
			t.Fatalf("version %d was written by %q, want the agent's token",
				item.Version, item.AuthorKind)
		}
	}
}

// TestADocsToolCallIsRefusedForAnotherGamesIDOverTheWire pins that the
// optional project_id confirmation is checked for these tools too — the
// check addScopedTool makes, exercised at a docs tool so it is not only
// the metamodel's that is covered.
func TestADocsToolCallIsRefusedForAnotherGamesIDOverTheWire(t *testing.T) {
	f := newMetamodelFixture(t)
	httpSrv := httptest.NewServer(f.srv)
	defer httpSrv.Close()
	session := connectMCP(t, httpSrv.URL, f.token)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "docs.list", Arguments: map[string]any{"project_id": f.other.String()},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError {
		t.Fatal("a docs tool stating another game's id must be refused")
	}
	var body struct {
		Error string `json:"error"`
	}
	decodeToolText(t, result, &body)
	if body.Error != "scope_violation" {
		t.Fatalf("error = %q, want scope_violation", body.Error)
	}
}

// writeDoc is a one-line document write for the tests below, which care
// about which rows come back from a listing rather than about the prose
// in them.
func writeDoc(t *testing.T, f metamodelFixture, path, body string, links ...web.DocsLinkInput) {
	t.Helper()
	in := web.DocsWriteInput{
		Path: path, Content: body, ExpectedVersion: int32Ptr(0), Message: "seed",
	}
	if len(links) > 0 {
		in.Links = &links
	}
	if _, err := web.MCPDocsWrite(context.Background(), f.deps, f.caller, f.game, in); err != nil {
		t.Fatalf("MCPDocsWrite %s: %v", path, err)
	}
}

// TestTheListingTellsTombstonesApartFiltersAndPages pins the four claims
// docs.list's own description makes and that nothing was asserting.
//
// It exists because four separate mutations of docsList survived the
// whole package: publishing Deleted false for every row, ignoring
// IncludeDeleted, ignoring the entity filter, and never setting
// NextCursor or Truncated. The round-trip test above touches three of
// those, and could not fail on any of them: its fixture holds exactly
// one document, so a filter that does nothing returns the same one row
// as a filter that works, a page that never fills cannot be truncated,
// and nothing in it is ever deleted. **A one-row fixture proves nothing
// about a filter.** This one holds three documents across two entities,
// one of them a tombstone, which is the smallest fixture in which each
// of those four behaviours has an observably different wrong answer.
func TestTheListingTellsTombstonesApartFiltersAndPages(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()
	seedQuest(t, f, "wanted-hogger", "Wanted: Hogger")
	if _, err := web.MCPEntitiesUpsert(ctx, f.deps, f.caller, f.game, web.EntitiesUpsertInput{
		Items: []web.EntityItemInput{{TypeKey: "quest", Key: "kobold-candles", Name: "Kobold Candles"}},
	}); err != nil {
		t.Fatalf("MCPEntitiesUpsert: %v", err)
	}

	writeDoc(t, f, "lore/hogger.md", "# Hogger\n",
		web.DocsLinkInput{EntityType: "quest", EntityKey: "wanted-hogger", Role: "script"})
	writeDoc(t, f, "lore/kobolds.md", "# Kobolds\n",
		web.DocsLinkInput{EntityType: "quest", EntityKey: "kobold-candles"})
	writeDoc(t, f, "lore/zzz-cut.md", "# Cut content\n")

	if _, err := web.MCPDocsDelete(ctx, f.deps, f.caller, f.game, web.DocsDeleteInput{
		Path: "lore/zzz-cut.md", Message: "cut from the release", ExpectedVersion: int32Ptr(1),
	}); err != nil {
		t.Fatalf("MCPDocsDelete: %v", err)
	}

	paths := func(out web.DocsListOutput) []string {
		got := make([]string, 0, len(out.Items))
		for _, item := range out.Items {
			got = append(got, item.Path)
		}
		return got
	}

	// 1. A soft-deleted document is absent by default.
	live, err := web.MCPDocsList(ctx, f.deps, f.caller, f.game, web.DocsListInput{})
	if err != nil {
		t.Fatalf("MCPDocsList: %v", err)
	}
	if got := paths(live); len(got) != 2 || got[0] != "lore/hogger.md" || got[1] != "lore/kobolds.md" {
		t.Fatalf("listing = %v, want the two live documents", got)
	}

	// 2. include_deleted brings it back, and the row says which one it
	// is. A listing that answers deleted false for a tombstone is a
	// listing a client cannot use include_deleted with at all.
	all, err := web.MCPDocsList(ctx, f.deps, f.caller, f.game, web.DocsListInput{IncludeDeleted: true})
	if err != nil {
		t.Fatalf("MCPDocsList include_deleted: %v", err)
	}
	if got := paths(all); len(got) != 3 {
		t.Fatalf("listing with include_deleted = %v, want all three documents", got)
	}
	for _, item := range all.Items {
		want := item.Path == "lore/zzz-cut.md"
		if item.Deleted != want {
			t.Fatalf("row %s says deleted %v, want %v", item.Path, item.Deleted, want)
		}
	}

	// 3. The entity filter narrows to the documents attached to that
	// entity, which is docs.list's bolded claim: it is how a quest's
	// script is found without guessing its path.
	byEntity, err := web.MCPDocsList(ctx, f.deps, f.caller, f.game, web.DocsListInput{
		EntityType: "quest", EntityKey: "kobold-candles",
	})
	if err != nil {
		t.Fatalf("MCPDocsList by entity: %v", err)
	}
	if got := paths(byEntity); len(got) != 1 || got[0] != "lore/kobolds.md" {
		t.Fatalf("listing by entity = %v, want only the document attached to that quest", got)
	}

	// 4. A page that does not hold everything says so and carries the
	// cursor to the rest.
	first, err := web.MCPDocsList(ctx, f.deps, f.caller, f.game, web.DocsListInput{Limit: 1})
	if err != nil {
		t.Fatalf("MCPDocsList limit 1: %v", err)
	}
	if len(first.Items) != 1 || !first.Truncated || first.NextCursor == nil {
		t.Fatalf("first page = %+v, want one row, truncated and a cursor", first)
	}
	second, err := web.MCPDocsList(ctx, f.deps, f.caller, f.game, web.DocsListInput{
		Limit: 1, Cursor: *first.NextCursor,
	})
	if err != nil {
		t.Fatalf("MCPDocsList second page: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].Path == first.Items[0].Path {
		t.Fatalf("second page = %+v, want the next row and not the first again", second.Items)
	}
	// The cursor is issued whenever a page came back full, so the page
	// after the last row is the empty one, and that is the page that
	// says the listing is over.
	if second.NextCursor == nil {
		t.Fatalf("second page = %+v, want a cursor while the page is still full", second)
	}
	last, err := web.MCPDocsList(ctx, f.deps, f.caller, f.game, web.DocsListInput{
		Limit: 1, Cursor: *second.NextCursor,
	})
	if err != nil {
		t.Fatalf("MCPDocsList past the last row: %v", err)
	}
	if len(last.Items) != 0 || last.Truncated || last.NextCursor != nil {
		t.Fatalf("page past the end = %+v, want empty and untruncated", last)
	}
}

// TestAHistoryPageSaysWhenThereIsMore is the history half of the paging
// claim. docs.history's description promises next_cursor and nothing
// asserted it: every existing history assertion reads a document with
// one or two versions and no limit, where a page can never fill.
func TestAHistoryPageSaysWhenThereIsMore(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()
	writeDoc(t, f, "lore/hogger.md", "# One\n")
	for version, body := range []string{"# Two\n", "# Three\n"} {
		if _, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
			Path: "lore/hogger.md", Content: body, ExpectedVersion: int32Ptr(int32(version) + 1),
			Message: "another draft",
		}); err != nil {
			t.Fatalf("MCPDocsWrite onto version %d: %v", version+1, err)
		}
	}

	first, err := web.MCPDocsHistory(ctx, f.deps, f.caller, f.game, web.DocsHistoryInput{
		Path: "lore/hogger.md", Limit: 1,
	})
	if err != nil {
		t.Fatalf("MCPDocsHistory: %v", err)
	}
	if len(first.Items) != 1 || !first.Truncated || first.NextCursor == nil {
		t.Fatalf("first page = %+v, want one version, truncated and a cursor", first)
	}
	if first.Items[0].Version != 3 {
		t.Fatalf("first row is version %d, want the newest", first.Items[0].Version)
	}
	second, err := web.MCPDocsHistory(ctx, f.deps, f.caller, f.game, web.DocsHistoryInput{
		Path: "lore/hogger.md", Limit: 1, Cursor: *first.NextCursor,
	})
	if err != nil {
		t.Fatalf("MCPDocsHistory second page: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].Version != 2 {
		t.Fatalf("second page = %+v, want version 2", second.Items)
	}
}

// TestADiffTooLargeToCompareSaysCoarseOnTheWire pins the one field of
// docs.diff a client cannot reconstruct from the answer.
//
// docs.diff's description tells a client to read coarse before it
// renders, because a coarse answer says "the whole body was replaced"
// about two versions that may differ by a word. Nothing asserted the
// field: every other diff assertion compares small bodies, where coarse
// is false either way.
func TestADiffTooLargeToCompareSaysCoarseOnTheWire(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	lines := func(word string) string {
		var b strings.Builder
		for i := 0; i <= markdown.MaxDiffLines; i++ {
			b.WriteString(word)
			b.WriteString(" ")
			b.WriteString(strconv.Itoa(i))
			b.WriteString("\n")
		}
		return b.String()
	}
	writeDoc(t, f, "lore/epic.md", lines("alpha"))
	if _, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
		Path: "lore/epic.md", Content: lines("beta"), ExpectedVersion: int32Ptr(1),
	}); err != nil {
		t.Fatalf("MCPDocsWrite the rewrite: %v", err)
	}
	if _, err := web.MCPDocsWrite(ctx, f.deps, f.caller, f.game, web.DocsWriteInput{
		Path: "lore/epic.md", Content: lines("beta") + "one more line\n", ExpectedVersion: int32Ptr(2),
	}); err != nil {
		t.Fatalf("MCPDocsWrite the small edit: %v", err)
	}

	whole, err := web.MCPDocsDiff(ctx, f.deps, f.caller, f.game, web.DocsDiffInput{
		Path: "lore/epic.md", FromVersion: 1, ToVersion: 2,
	})
	if err != nil {
		t.Fatalf("MCPDocsDiff: %v", err)
	}
	if !whole.Coarse {
		t.Fatalf("diff = %+v, want coarse true past the comparison bound", whole)
	}

	// And the same tool answers coarse false for an edit inside a body
	// of the same size, which is the half that makes the field worth
	// reading: a client that saw coarse true on everything would learn
	// to ignore it.
	small, err := web.MCPDocsDiff(ctx, f.deps, f.caller, f.game, web.DocsDiffInput{
		Path: "lore/epic.md", FromVersion: 2, ToVersion: 3,
	})
	if err != nil {
		t.Fatalf("MCPDocsDiff the small edit: %v", err)
	}
	if small.Coarse {
		t.Fatalf("diff = %+v, want coarse false for a one-line edit", small)
	}
}
