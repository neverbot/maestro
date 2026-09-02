package web

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/markdown"
)

// This file is the prose half of the game-content surface: the tools an
// agent uses to write, read, version and attach a game's documents.
//
// **Every exported MCPDocs* function starts with requireScope and
// delegates to an unexported core of the same name.** The cores exist
// for the REST mirror, and the split is exactly where the two surfaces
// differ and nowhere else: requireScope asks "is this token bound to
// this game", which a session caller cannot answer, and requireProject
// asks the equivalent question of a session. One implementation of every
// tool, two admission checks. TestEveryDocsToolRefusesAnotherGamesToken
// (mcp_docs_test.go) drives every one of them at a game the caller's own
// *user* owns and its *token* is not bound to, and it is driven from the
// registered tool list rather than from a hand-written one, so a twelfth
// tool added tomorrow is covered without editing it.
//
// **No input type here is a domain type.** markdown.WriteInput carries
// an Actor, and an Actor is the audit record of who wrote this row;
// accepting it off the wire would let an agent name any user or token it
// liked as the author of its writes. actorOf (mcp_metamodel.go) builds
// it from the authenticated caller and nothing else.
//
// **Documents are addressed by path and never by id.** There is no
// by-id tool and no id argument anywhere in this file. A path is the
// stable handle an agent re-seeds against and the thing a designer says
// out loud; an id would be a second address for one row, and the only
// thing it would buy is a way to reach a document whose path you do not
// know, which is what docs.list is for.
//
// **expected_version is a *int32 on every tool that takes one, and a
// nil one is refused as invalid_input at its own path.** The refusal
// itself lives in the domain (markdown.WriteInput, DeleteInput and
// RevertInput each check it and report at `expected_version`), not here;
// what this file owes is the pointer, because the SDK's own input-schema
// validation is the natural place to make a field required and it
// reports a missing required field as plain prose with no code
// (addScopedTool's doc comment records that exception). A pointer plus
// the domain's own check gives an agent a code it can branch on.
// TestADocsWriteWithoutAnExpectedVersionIsInvalidInputAtItsOwnPath pins
// it through this surface.

// --- Inputs ---

// DocsWriteInput is the argument shape of docs.write.
//
// **Kind is a pointer, not a plain string.** markdown.WriteInput.Kind
// used to be a plain string too, and every edit that omitted it silently
// erased the document's kind — fixed there by making it a *string where
// nil preserves and any value, including "", sets. `omitempty` on a
// plain string cannot carry that distinction across JSON: an absent
// field and an explicit `"kind":""` unmarshal to the same Go zero value.
// A pointer does — omitted decodes to nil, `"kind":""` decodes to a
// pointer at "" — so this field must stay a pointer for the same reason
// ExpectedVersion is one, and this handler passes it through to
// WriteInput.Kind unconverted.
//
// **Links is `*[]DocsLinkInput`, and the pointer is load-bearing for
// the same reason.** `omitempty` on a plain slice omits an empty one,
// which would collapse "detach everything" back into "say nothing" — the
// single most destructive thing this tool could do quietly, in the
// silent direction. markdown pins the distinction on a stand-in of this
// exact shape (TestOmittingLinksAndSendingAnEmptyArrayAreDifferentOnThe
// Wire, internal/markdown/links_test.go) and
// TestOmittingLinksAndSendingAnEmptyArrayAreDifferentOnThisType
// (mcp_docs_internal_test.go) re-pins the claim against this real type.
// `"links": null` decodes to nil and therefore *preserves*, the same as
// omitting the field; docs.write's description says so in words, because
// an agent sending null meaning "detach" otherwise gets the opposite
// with nothing to notice by.
type DocsWriteInput struct {
	ScopedArgs
	Path            string           `json:"path"`
	Content         string           `json:"content"`
	Kind            *string          `json:"kind,omitempty"`
	Message         string           `json:"message,omitempty"`
	ExpectedVersion *int32           `json:"expected_version"`
	IncludeCurrent  *bool            `json:"include_current,omitempty"`
	Links           *[]DocsLinkInput `json:"links,omitempty"`
}

// DocsLinkInput is one element of a write's links array.
type DocsLinkInput struct {
	EntityType string `json:"entity_type"`
	EntityKey  string `json:"entity_key"`
	Role       string `json:"role,omitempty"`
}

// DocsReadInput addresses one document. HeadOnly asks for the
// frontmatter and a short preview instead of the whole body, for a
// caller deciding whether it wants the document at all.
type DocsReadInput struct {
	ScopedArgs
	Path     string `json:"path"`
	HeadOnly bool   `json:"head_only,omitempty"`
}

// DocsListInput narrows a listing.
type DocsListInput struct {
	ScopedArgs
	PathPrefix     string `json:"path_prefix,omitempty"`
	Kind           string `json:"kind,omitempty"`
	EntityType     string `json:"entity_type,omitempty"`
	EntityKey      string `json:"entity_key,omitempty"`
	IncludeDeleted bool   `json:"include_deleted,omitempty"`
	Cursor         string `json:"cursor,omitempty"`
	Limit          int32  `json:"limit,omitempty"`
}

// DocsDeleteInput soft-deletes one document.
type DocsDeleteInput struct {
	ScopedArgs
	Path            string `json:"path"`
	Message         string `json:"message,omitempty"`
	ExpectedVersion *int32 `json:"expected_version"`
}

// DocsHistoryInput pages one document's versions.
type DocsHistoryInput struct {
	ScopedArgs
	Path   string `json:"path"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int32  `json:"limit,omitempty"`
}

// DocsReadVersionInput reads one past version in full.
type DocsReadVersionInput struct {
	ScopedArgs
	Path    string `json:"path"`
	Version int32  `json:"version"`
}

// DocsRevertInput restores a past version as a new one.
type DocsRevertInput struct {
	ScopedArgs
	Path            string `json:"path"`
	ToVersion       int32  `json:"to_version"`
	ExpectedVersion *int32 `json:"expected_version"`
	Message         string `json:"message,omitempty"`
	IncludeCurrent  *bool  `json:"include_current,omitempty"`
}

// DocsDiffInput compares two versions.
type DocsDiffInput struct {
	ScopedArgs
	Path        string `json:"path"`
	FromVersion int32  `json:"from_version"`
	ToVersion   int32  `json:"to_version"`
}

// DocsLinksListInput reads the join from either side: by path, or by
// entity. Exactly one of the two addresses must be given, and giving
// neither or both is invalid_input — "the join, from either side" is two
// questions, and a call that names both is a caller that has not decided
// which it is asking.
// TestAskingTheJoinFromBothSidesAtOnceIsInvalidInput pins both refusals.
type DocsLinksListInput struct {
	ScopedArgs
	Path       string `json:"path,omitempty"`
	EntityType string `json:"entity_type,omitempty"`
	EntityKey  string `json:"entity_key,omitempty"`
}

// DocsLinkAddInput attaches one document to one entity, in a role.
//
// **This is a different type from DocsLinkRemoveInput below, not the
// same struct reused for both tools.** markdown.LinkRemove's argument
// (markdown.UnlinkInput) does not read a role at all — the link's key is
// (document, entity), so there is no second link under another role for
// one to choose between — and an input schema that still asked for it
// would tell a caller otherwise, with nothing to correct the belief.
type DocsLinkAddInput struct {
	ScopedArgs
	Path       string `json:"path"`
	EntityType string `json:"entity_type"`
	EntityKey  string `json:"entity_key"`
	Role       string `json:"role,omitempty"`
}

// DocsLinkRemoveInput detaches one document from one entity. No Role:
// see DocsLinkAddInput's doc comment.
// TestTheRemoveToolsInputCarriesNoRole pins the absence on the wire.
type DocsLinkRemoveInput struct {
	ScopedArgs
	Path       string `json:"path"`
	EntityType string `json:"entity_type"`
	EntityKey  string `json:"entity_key"`
}

// --- Outputs ---

// DocumentOutput is one document in full.
//
// Body is the raw markdown, exactly as it was written: MCP always gets
// the raw body, and the rendered HTML is a REST-only affordance for the
// browser.
//
// Truncated and BodyLength are set together from one condition (headOf)
// so they cannot disagree. A caller that ignores them and treats a
// truncated body as the document is the failure this pair exists to
// prevent, which is why Truncated is not omitempty: false is a
// statement.
//
// **Deleted is derived from the row and is false on every answer any
// tool in this file can give today.** docs.read does not find a
// soft-deleted document (markdown.Read refuses it), and docs.write and
// docs.revert both resurrect one. The field is here because it is the
// row's own state and a caller reading it should not have to infer it
// from which tool answered; no test claims it is ever true, because
// through this type nothing makes it true.
type DocumentOutput struct {
	ID          uuid.UUID       `json:"id"`
	Path        string          `json:"path"`
	Kind        string          `json:"kind,omitempty"`
	Title       string          `json:"title"`
	Summary     string          `json:"summary,omitempty"`
	Version     int32           `json:"version"`
	Frontmatter json.RawMessage `json:"frontmatter"`
	Body        string          `json:"body"`
	Truncated   bool            `json:"truncated"`
	BodyLength  int             `json:"body_length"`
	Deleted     bool            `json:"deleted"`
	Links       []LinkedRef     `json:"links"`
}

// DocumentSummaryOutput is one row of a listing: no body, no
// frontmatter. It is also what docs.delete answers with, for the reason
// docsDelete's own comment gives.
type DocumentSummaryOutput struct {
	ID      uuid.UUID `json:"id"`
	Path    string    `json:"path"`
	Kind    string    `json:"kind,omitempty"`
	Title   string    `json:"title"`
	Summary string    `json:"summary,omitempty"`
	Version int32     `json:"version"`
	Deleted bool      `json:"deleted"`
}

// DocsListOutput is one page of summaries. NextCursor and Truncated are
// set together, from one condition, so they cannot disagree.
type DocsListOutput struct {
	Items      []DocumentSummaryOutput `json:"items"`
	NextCursor *string                 `json:"next_cursor,omitempty"`
	Truncated  bool                    `json:"truncated"`
}

// VersionOutput is one row of a history: metadata, no body.
//
// AuthorKind is "user" or "token" and AuthorID is the corresponding id.
// The pair is what makes "rewritten by the lore agent" and "rewritten by
// Ana" distinguishable in a history view without a synthetic user per
// agent — and it is *why* the version table carries two nullable author
// columns rather than one. Both are empty on a version whose author is
// no longer on file.
type VersionOutput struct {
	Version    int32      `json:"version"`
	Title      string     `json:"title"`
	Summary    string     `json:"summary,omitempty"`
	Message    string     `json:"message,omitempty"`
	Deleted    bool       `json:"deleted"`
	AuthorKind string     `json:"author_kind,omitempty"`
	AuthorID   *uuid.UUID `json:"author_id,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// DocsHistoryOutput is one page of version metadata, newest first.
type DocsHistoryOutput struct {
	Items      []VersionOutput `json:"items"`
	NextCursor *string         `json:"next_cursor,omitempty"`
	Truncated  bool            `json:"truncated"`
}

// DocsVersionOutput is one past version in full, body included.
type DocsVersionOutput struct {
	Path        string          `json:"path"`
	Version     int32           `json:"version"`
	Title       string          `json:"title"`
	Summary     string          `json:"summary,omitempty"`
	Message     string          `json:"message,omitempty"`
	Deleted     bool            `json:"deleted"`
	Frontmatter json.RawMessage `json:"frontmatter"`
	Body        string          `json:"body"`
	CreatedAt   time.Time       `json:"created_at"`
}

// DocsDiffOutput is one unified diff, computed on read.
//
// Coarse says the two versions were too large to compare line by line
// and the answer is "the whole body was replaced". It is on the wire
// because a client cannot tell the difference from the diff itself, and
// would otherwise render "everything changed" for two versions that
// differ by a word.
type DocsDiffOutput struct {
	Path        string `json:"path"`
	FromVersion int32  `json:"from_version"`
	ToVersion   int32  `json:"to_version"`
	Unified     string `json:"unified"`
	Coarse      bool   `json:"coarse"`
}

// LinkedDocumentRef is one document an entity is attached to: the
// mirror image of LinkedRef (mcp_search.go), which is one entity a
// document is attached to.
//
// **It is not DocumentSummaryOutput**, and the difference is not
// cosmetic: markdown.DocumentLink carries a path, a title, a kind and a
// role and nothing else, so answering with a summary shape would put a
// zero Version and a false Deleted on the wire for every row — two
// fields that read as facts about the document and would be neither
// read from it nor true of it. Only the columns the join actually
// selects are published.
type LinkedDocumentRef struct {
	ID    uuid.UUID `json:"id"`
	Path  string    `json:"path"`
	Title string    `json:"title"`
	Kind  string    `json:"kind,omitempty"`
	Role  string    `json:"role,omitempty"`
}

// DocsLinksOutput answers the join from whichever side was asked, and is
// also what the two write tools answer with, so a caller reads the
// resulting set back on the same call that changed it.
//
// The two arrays are never both populated: Documents is set for an
// entity-side question and Entities for a document-side one. Both are
// non-nil, so an empty answer marshals as [] rather than null.
type DocsLinksOutput struct {
	Entities  []LinkedRef         `json:"entities"`
	Documents []LinkedDocumentRef `json:"documents"`
}

// --- Tools ---

// MCPDocsWrite implements docs.write.
func MCPDocsWrite(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in DocsWriteInput) (DocumentOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return DocumentOutput{}, err
	}
	return docsWrite(ctx, deps, caller, projectID, in)
}

// docsWrite is MCPDocsWrite without the token-binding check, for the
// REST mirror, whose caller is a person whose standing requireProject
// already resolved. See this file's header.
func docsWrite(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in DocsWriteInput) (DocumentOutput, error) {
	row, err := deps.Markdown.Write(ctx, projectID, markdown.WriteInput{
		Path:            in.Path,
		Content:         in.Content,
		Kind:            in.Kind,
		Message:         in.Message,
		ExpectedVersion: in.ExpectedVersion,
		IncludeCurrent:  includeCurrent(in.IncludeCurrent),
		Links:           linkTargetsOf(in.Links),
		Actor:           actorOf(caller),
	})
	if err != nil {
		return DocumentOutput{}, err
	}
	return documentWithLinks(ctx, deps, projectID, row, false)
}

// MCPDocsRead implements docs.read.
func MCPDocsRead(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in DocsReadInput) (DocumentOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return DocumentOutput{}, err
	}
	return docsRead(ctx, deps, projectID, in)
}

func docsRead(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in DocsReadInput) (DocumentOutput, error) {
	row, err := deps.Markdown.Read(ctx, projectID, in.Path)
	if err != nil {
		return DocumentOutput{}, err
	}
	return documentWithLinks(ctx, deps, projectID, row, in.HeadOnly)
}

// MCPDocsList implements docs.list.
func MCPDocsList(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in DocsListInput) (DocsListOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return DocsListOutput{}, err
	}
	return docsList(ctx, deps, projectID, in)
}

func docsList(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in DocsListInput) (DocsListOutput, error) {
	page, err := deps.Markdown.List(ctx, projectID, markdown.ListFilter{
		PathPrefix:     in.PathPrefix,
		Kind:           in.Kind,
		EntityType:     in.EntityType,
		EntityKey:      in.EntityKey,
		IncludeDeleted: in.IncludeDeleted,
		Cursor:         in.Cursor,
		Limit:          in.Limit,
	})
	if err != nil {
		return DocsListOutput{}, err
	}
	items := make([]DocumentSummaryOutput, 0, len(page.Documents))
	for _, row := range page.Documents {
		items = append(items, DocumentSummaryOutput{
			ID: row.ID, Path: row.Path, Kind: row.Kind, Title: row.Title,
			Summary: row.Summary, Version: row.CurrentVersion, Deleted: row.Deleted,
		})
	}
	out := DocsListOutput{Items: items}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
		out.Truncated = true
	}
	return out, nil
}

// MCPDocsDelete implements docs.delete.
func MCPDocsDelete(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in DocsDeleteInput) (DocumentSummaryOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return DocumentSummaryOutput{}, err
	}
	return docsDelete(ctx, deps, caller, projectID, in)
}

// docsDelete answers with a summary and not a DocumentOutput, on
// purpose. The caller has just removed the document and does not need
// its body echoed back; what it does need is the version the deletion
// landed on, because that is the expected_version a later write must
// pass to bring the document back (markdown.Delete's own doc comment
// argues why the tombstone advances the version at all), and Deleted
// true, which is the one place in this file that field is ever true.
func docsDelete(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in DocsDeleteInput) (DocumentSummaryOutput, error) {
	row, err := deps.Markdown.Delete(ctx, projectID, markdown.DeleteInput{
		Path:            in.Path,
		Message:         in.Message,
		ExpectedVersion: in.ExpectedVersion,
		Actor:           actorOf(caller),
	})
	if err != nil {
		return DocumentSummaryOutput{}, err
	}
	return DocumentSummaryOutput{
		ID: row.ID, Path: row.Path, Kind: row.Kind, Title: row.Title,
		Summary: row.Summary, Version: row.CurrentVersion, Deleted: row.DeletedAt.Valid,
	}, nil
}

// MCPDocsHistory implements docs.history.
func MCPDocsHistory(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in DocsHistoryInput) (DocsHistoryOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return DocsHistoryOutput{}, err
	}
	return docsHistory(ctx, deps, projectID, in)
}

func docsHistory(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in DocsHistoryInput) (DocsHistoryOutput, error) {
	page, err := deps.Markdown.History(ctx, projectID, markdown.HistoryFilter{
		Path: in.Path, Cursor: in.Cursor, Limit: in.Limit,
	})
	if err != nil {
		return DocsHistoryOutput{}, err
	}
	items := make([]VersionOutput, 0, len(page.Versions))
	for _, row := range page.Versions {
		item := VersionOutput{
			Version: row.Version, Title: row.Title, Summary: row.Summary,
			Message: row.Message, Deleted: row.Deleted, CreatedAt: row.CreatedAt.Time,
		}
		item.AuthorKind, item.AuthorID = authorOf(row.AuthorUserID, row.AuthorTokenID)
		items = append(items, item)
	}
	out := DocsHistoryOutput{Items: items}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
		out.Truncated = true
	}
	return out, nil
}

// MCPDocsReadVersion implements docs.read_version.
func MCPDocsReadVersion(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in DocsReadVersionInput) (DocsVersionOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return DocsVersionOutput{}, err
	}
	return docsReadVersion(ctx, deps, projectID, in)
}

func docsReadVersion(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in DocsReadVersionInput) (DocsVersionOutput, error) {
	row, err := deps.Markdown.ReadVersion(ctx, projectID, in.Path, in.Version)
	if err != nil {
		return DocsVersionOutput{}, err
	}
	return DocsVersionOutput{
		Path:        in.Path,
		Version:     row.Version,
		Title:       row.Title,
		Summary:     row.Summary,
		Message:     row.Message,
		Deleted:     row.Deleted,
		Frontmatter: frontmatterOf(row.Frontmatter),
		Body:        row.BodyMd,
		CreatedAt:   row.CreatedAt.Time,
	}, nil
}

// MCPDocsRevert implements docs.revert.
func MCPDocsRevert(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in DocsRevertInput) (DocumentOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return DocumentOutput{}, err
	}
	return docsRevert(ctx, deps, caller, projectID, in)
}

func docsRevert(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in DocsRevertInput) (DocumentOutput, error) {
	row, err := deps.Markdown.Revert(ctx, projectID, markdown.RevertInput{
		Path:            in.Path,
		ToVersion:       in.ToVersion,
		ExpectedVersion: in.ExpectedVersion,
		Message:         in.Message,
		IncludeCurrent:  includeCurrent(in.IncludeCurrent),
		Actor:           actorOf(caller),
	})
	if err != nil {
		return DocumentOutput{}, err
	}
	return documentWithLinks(ctx, deps, projectID, row, false)
}

// MCPDocsDiff implements docs.diff.
func MCPDocsDiff(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in DocsDiffInput) (DocsDiffOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return DocsDiffOutput{}, err
	}
	return docsDiff(ctx, deps, projectID, in)
}

func docsDiff(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in DocsDiffInput) (DocsDiffOutput, error) {
	result, err := deps.Markdown.Diff(ctx, projectID, in.Path, in.FromVersion, in.ToVersion)
	if err != nil {
		return DocsDiffOutput{}, err
	}
	return DocsDiffOutput{
		Path:        result.Path,
		FromVersion: result.FromVersion,
		ToVersion:   result.ToVersion,
		Unified:     result.Unified,
		Coarse:      result.Coarse,
	}, nil
}

// MCPDocsLinksList implements docs.links.list.
func MCPDocsLinksList(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in DocsLinksListInput) (DocsLinksOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return DocsLinksOutput{}, err
	}
	return docsLinksList(ctx, deps, projectID, in)
}

func docsLinksList(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in DocsLinksListInput) (DocsLinksOutput, error) {
	byPath := in.Path != ""
	byEntity := in.EntityType != "" || in.EntityKey != ""
	switch {
	case byPath && byEntity:
		return DocsLinksOutput{}, invalidInput("path",
			"cannot be given together with entity_type and entity_key: "+
				"ask the join from one side or the other")
	case !byPath && !byEntity:
		return DocsLinksOutput{}, invalidInput("path",
			"is required unless entity_type and entity_key are given: "+
				"ask the join from one side or the other")
	case byPath:
		return documentSideLinks(ctx, deps, projectID, in.Path)
	default:
		links, err := deps.Markdown.LinksByEntity(ctx, projectID, in.EntityType, in.EntityKey)
		if err != nil {
			return DocsLinksOutput{}, err
		}
		return linksOutput(nil, links), nil
	}
}

// MCPDocsLinkAdd implements docs.links.add.
func MCPDocsLinkAdd(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in DocsLinkAddInput) (DocsLinksOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return DocsLinksOutput{}, err
	}
	return docsLinkAdd(ctx, deps, projectID, in)
}

func docsLinkAdd(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in DocsLinkAddInput) (DocsLinksOutput, error) {
	err := deps.Markdown.LinkAdd(ctx, projectID, markdown.LinkInput{
		Path: in.Path, EntityType: in.EntityType, EntityKey: in.EntityKey, Role: in.Role,
	})
	if err != nil {
		return DocsLinksOutput{}, err
	}
	return documentSideLinks(ctx, deps, projectID, in.Path)
}

// MCPDocsLinkRemove implements docs.links.remove.
func MCPDocsLinkRemove(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in DocsLinkRemoveInput) (DocsLinksOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return DocsLinksOutput{}, err
	}
	return docsLinkRemove(ctx, deps, projectID, in)
}

// docsLinkRemove passes no role, because markdown.UnlinkInput carries
// none and DocsLinkRemoveInput carries none: see DocsLinkAddInput.
func docsLinkRemove(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in DocsLinkRemoveInput) (DocsLinksOutput, error) {
	err := deps.Markdown.LinkRemove(ctx, projectID, markdown.UnlinkInput{
		Path: in.Path, EntityType: in.EntityType, EntityKey: in.EntityKey,
	})
	if err != nil {
		return DocsLinksOutput{}, err
	}
	return documentSideLinks(ctx, deps, projectID, in.Path)
}

// --- Conversion helpers ---

// includeCurrent reads the docs.write / docs.revert argument, defaulting
// to true.
//
// **The default lives here, on the wire, and not in
// markdown.WriteInput.** An agent that does not know about the argument
// is exactly the agent that most needs the current body handed to it on
// a conflict — a second round trip costs it a turn and a slice of its
// context window — so the useful behaviour is the default. The Go zero
// value stays false so that a domain caller never gets a 200 KB body it
// did not ask for by forgetting a field.
// TestIncludeCurrentDefaultsToTrueOnTheWireAndIsHonouredWhenFalse pins
// both halves.
func includeCurrent(v *bool) bool {
	if v == nil {
		return true
	}
	return *v
}

// linkTargetsOf converts a write's links array, preserving the pointer's
// three states: nil (omitted, or an explicit JSON null) preserves the
// document's attachments, an empty array detaches everything, and a
// populated one replaces the set. See DocsWriteInput.
func linkTargetsOf(in *[]DocsLinkInput) *[]markdown.LinkTarget {
	if in == nil {
		return nil
	}
	targets := make([]markdown.LinkTarget, 0, len(*in))
	for _, link := range *in {
		targets = append(targets, markdown.LinkTarget{
			EntityType: link.EntityType, EntityKey: link.EntityKey, Role: link.Role,
		})
	}
	return &targets
}

// documentWithLinks builds the full answer for one document row,
// including the entities it is attached to.
//
// The links are read on every write and every read rather than left to
// docs.links.list, because an attachment set that can only be seen
// through a second tool is an attachment set nobody looks at — the
// write-only-field class of defect this project has already shipped
// once.
// TestEveryDocumentFieldSurvivesARoundTripThroughTheTools reads them
// back off a write.
func documentWithLinks(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	row dbq.Document, headOnly bool) (DocumentOutput, error) {
	links, err := deps.Markdown.LinksByDocument(ctx, projectID, row.Path)
	if err != nil {
		return DocumentOutput{}, err
	}
	body, truncated, length := row.BodyMd, false, len(row.BodyMd)
	if headOnly {
		body, truncated, length = headOf(row.BodyMd)
	}
	return DocumentOutput{
		ID:          row.ID,
		Path:        row.Path,
		Kind:        row.Kind,
		Title:       row.Title,
		Summary:     row.Summary,
		Version:     row.CurrentVersion,
		Frontmatter: frontmatterOf(row.Frontmatter),
		Body:        body,
		Truncated:   truncated,
		BodyLength:  length,
		Deleted:     row.DeletedAt.Valid,
		Links:       linkedRefsOf(links),
	}, nil
}

// documentSideLinks answers the join from the document's side, which is
// what the two write tools answer with as well as docs.links.list.
func documentSideLinks(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	path string) (DocsLinksOutput, error) {
	links, err := deps.Markdown.LinksByDocument(ctx, projectID, path)
	if err != nil {
		return DocsLinksOutput{}, err
	}
	return linksOutput(links, nil), nil
}

// linksOutput builds the join answer with both arrays non-nil, so an
// empty side marshals as [] and never as null.
func linksOutput(entities []markdown.EntityLink, documents []markdown.DocumentLink) DocsLinksOutput {
	out := DocsLinksOutput{
		Entities:  linkedRefsOf(entities),
		Documents: make([]LinkedDocumentRef, 0, len(documents)),
	}
	for _, link := range documents {
		out.Documents = append(out.Documents, LinkedDocumentRef{
			ID: link.DocumentID, Path: link.Path, Title: link.Title,
			Kind: link.Kind, Role: link.Role,
		})
	}
	return out
}

// linkedRefsOf converts the domain's attachment rows into the wire shape
// `search` already publishes for the same fact (LinkedRef,
// mcp_search.go), so a document's attachments read identically wherever
// they appear. Never nil.
func linkedRefsOf(links []markdown.EntityLink) []LinkedRef {
	refs := make([]LinkedRef, 0, len(links))
	for _, link := range links {
		refs = append(refs, LinkedRef{
			EntityTypeKey: link.EntityTypeKey, EntityKey: link.EntityKey,
			Name: link.EntityName, Role: link.Role,
		})
	}
	return refs
}

// frontmatterOf publishes a stored frontmatter object, answering with an
// empty object rather than JSON null for a document that has none: a
// caller indexing into the answer should not have to test for null
// first, and "no frontmatter" and "an empty frontmatter" are the same
// document.
func frontmatterOf(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

// authorOf labels a version's author. Exactly one of the two columns is
// set on a version written through this surface; both nil is a version
// whose author is no longer on file, and it answers with neither a kind
// nor an id rather than with a kind naming nothing.
func authorOf(userID, tokenID *uuid.UUID) (string, *uuid.UUID) {
	switch {
	case tokenID != nil:
		return "token", tokenID
	case userID != nil:
		return "user", userID
	default:
		return "", nil
	}
}

// headOf builds the preview a head_only read answers with.
//
// A head is the first previewBytes of the body, cut back off any rune
// the bound splits — a partial rune is invalid UTF-8, which would make
// the whole answer unencodable — together with the true body length, so
// a caller can decide whether to fetch the rest.
// TestAHeadOnlyReadSaysHowMuchItLeftOut pins the cut, the flag and the
// length, over a body whose runes straddle the bound.
func headOf(body string) (preview string, truncated bool, length int) {
	length = len(body)
	if length <= previewBytes {
		return body, false, length
	}
	cut := previewBytes
	for cut > 0 && !utf8.RuneStart(body[cut]) {
		cut--
	}
	return body[:cut], true, length
}

// previewBytes bounds a head_only body.
//
// 2 KiB is a screenful: enough for an agent to tell whether this is the
// document it wanted, small enough that listing ten heads costs less
// than reading one script.
const previewBytes = 2 << 10

// --- Registration ---

// addDocsTools registers the prose tools on srv.
//
// Every one goes through addScopedTool, so the caller's game binding is
// the only scope any of them can act in — see that function's own doc
// comment and TestEveryMCPToolGoesThroughAddScopedTool, which fails if a
// tool ever reaches the served list any other way.
//
// **The descriptions interpolate the domain's own constants and never
// type the numbers out.** A bound an agent reads in a description and a
// bound the server enforces have to be the same number, and the only way
// to guarantee that is for there to be one of them.
func (s *Server) addDocsTools(srv *mcp.Server, deps MCPDeps) {
	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "docs.write",
		Description: fmt.Sprintf(
			"Create or rewrite one document: long prose attached to this game's content — "+
				"a quest's dialogue script, a zone's lore, the game bible. Every save is a "+
				"full snapshot with an author and a message; nothing is overwritten and "+
				"nothing is lost. "+
				"**expected_version is required. Pass 0 to create a document that must not "+
				"exist yet, or the version you read to update the one that is there.** "+
				"Omitting it is invalid_input, not a guess: a mistyped path with no version "+
				"would silently become a second document. A mismatch is version_conflict, "+
				"and it carries the current version *and the current body* so you can merge "+
				"without a second call — pass include_current false to turn that echo off "+
				"for a large document. If the conflict's details say deleted, the version it "+
				"names is a tombstone and writing with that expected_version brings the "+
				"document back. "+
				"content is the whole document: optional YAML frontmatter between --- "+
				"fences, then markdown. Frontmatter is stored and echoed back and is never "+
				"interpreted — it creates no links and no fields; `title` and `summary` are "+
				"read out of it for display and nothing else is. The body you read back is "+
				"the body you wrote, byte for byte. At most %d bytes, of which only the "+
				"first %d characters are indexed for search — a word past that is stored "+
				"and readable but not findable. "+
				"paths are matched without regard to case and are at most %d bytes over at "+
				"most %d slash-separated segments, each starting with an ASCII letter or "+
				"digit and continuing in letters, digits, underscores, hyphens or dots. "+
				"Slashes are a naming convention, not folders: nothing is inherited along "+
				"one. "+
				"**kind is a property of the document, not of this one edit: omitting it "+
				"leaves the document's current kind alone, and passing \"\" clears it.** "+
				"Fixing a typo in the body does not require knowing or restating the kind. "+
				"**links replaces the document's whole attachment set; omitting links — or "+
				"sending `\"links\": null`, which means the same thing — leaves it "+
				"untouched.** Pass an empty array to detach everything: that is the only "+
				"way to say it, and null is not it. At most %d attachments, each with an "+
				"optional role of at most %d bytes. Attachment is never inferred from the "+
				"content. %s",
			markdown.MaxBodyBytes, markdown.MaxIndexedChars, markdown.MaxPathLen,
			markdown.MaxPathSegments, markdown.MaxLinksPerWrite, markdown.MaxRoleLen,
			retryAdvice),
		OutputSchema: documentOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in DocsWriteInput) (DocumentOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPDocsWrite(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "docs.read",
		Description: fmt.Sprintf(
			"Read one document by path: its raw markdown body, its stored frontmatter, its "+
				"current version and the entities it is attached to. The body is the "+
				"markdown as written, never rendered HTML. "+
				"Pass head_only true for the frontmatter and the first %d bytes of the body "+
				"instead of the whole thing, when you are deciding whether you want the "+
				"document at all; the answer then carries truncated true and body_length, "+
				"the full size in bytes. **A truncated body is not the document** — do not "+
				"write it back. "+
				"A soft-deleted document is not found here: list it with include_deleted, or "+
				"read one of its versions with docs.read_version. %s",
			previewBytes, retryAdvice),
		OutputSchema: documentOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in DocsReadInput) (DocumentOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPDocsRead(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "docs.list",
		Description: fmt.Sprintf(
			"List this game's documents. **Summaries only — no bodies, ever.** Filter by "+
				"path_prefix, by kind, or by the entity a document is attached to "+
				"(entity_type and entity_key, given together; half an address is refused). "+
				"**Filtering by entity is how you find a quest's script without guessing "+
				"its path.** "+
				"**kind is free text and an omitted kind means \"no filter\": there is no "+
				"spelling for \"documents that have no kind\".** An empty string is the same "+
				"as saying nothing, so a kind-less document can only be found without a kind "+
				"filter. A kind or a prefix that names nothing answers with an empty page; "+
				"an entity that names nothing is not_found, because that one is an address. "+
				"include_deleted brings back soft-deleted documents, which are otherwise "+
				"absent. Pass the previous answer's next_cursor for the next page; a cursor "+
				"belongs to the game and the filter it was issued for and is refused against "+
				"any other. limit defaults to %d and is capped at %d — asking for more gets "+
				"the cap, asking for less than one gets the default. %s",
			markdown.DefaultDocumentPage, markdown.MaxDocumentPage, retryAdvice),
		OutputSchema: docsListOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in DocsListInput) (DocsListOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPDocsList(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "docs.delete",
		Description: fmt.Sprintf(
			"Remove one document from the game. **The deletion is soft and nothing is "+
				"lost:** the whole history survives, every past version stays readable "+
				"through docs.read_version, and **writing to the same path brings the "+
				"document back** — pass the version this call answers with as "+
				"expected_version and the document is live again, its numbering continuing "+
				"from the tombstone. There is no hard delete on this surface. "+
				"expected_version is required and must match the version you read, so a "+
				"deletion cannot race an edit. message is recorded on the tombstone, so "+
				"\"why was this cut\" is answerable from the history; at most %d bytes. "+
				"A deleted document stops appearing in docs.read, in docs.list without "+
				"include_deleted, in search, and on the entity pages it was attached to — "+
				"its links survive and come back with it. %s",
			markdown.MaxMessageLen, retryAdvice),
		OutputSchema: documentSummaryOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in DocsDeleteInput) (DocumentSummaryOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPDocsDelete(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "docs.history",
		Description: fmt.Sprintf(
			"List one document's versions, newest first: version number, title, summary, the "+
				"message its author left, whether it is the tombstone of a deletion, and who "+
				"wrote it — author_kind is \"user\" or \"token\", so a designer's edit and an "+
				"agent's are told apart. **Metadata only — no bodies.** Read one version's "+
				"body with docs.read_version, or compare two with docs.diff. "+
				"History is append-only: a revert writes a new version rather than removing "+
				"one, so there is no state in which version 9 exists and version 8 does not. "+
				"Pass the previous answer's next_cursor for the next page. limit defaults to "+
				"%d and is capped at %d. %s",
			markdown.DefaultHistoryPage, markdown.MaxHistoryPage, retryAdvice),
		OutputSchema: docsHistoryOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in DocsHistoryInput) (DocsHistoryOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPDocsHistory(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "docs.read_version",
		Description: "Read one past version of a document in full: the body, the frontmatter " +
			"and the title as they were stored that day, not re-derived from today's rules. " +
			"Versions are numbered from 1 and are never pruned. This is the one read that " +
			"answers for a deleted document: its tombstone version carries the document " +
			"exactly as it stood when it was cut. " + retryAdvice,
		OutputSchema: docsVersionOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in DocsReadVersionInput) (DocsVersionOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPDocsReadVersion(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "docs.revert",
		Description: "Restore a past version of a document as a **new** version. History is " +
			"append-only: reverting a three-version document leaves four versions, not one, " +
			"and the writing that was reverted is still there to read. " +
			"expected_version is required and is the version you read, so a revert cannot " +
			"race an edit; to_version is the one to restore. The restored content is the " +
			"content that was stored — title, summary and frontmatter come off the version " +
			"row verbatim. " +
			"**A revert does not touch the document's kind and does not touch its links**: " +
			"it restores the writing, and what a document is about is a separate decision " +
			"somebody made separately. Reverting a deleted document brings it back; " +
			"reverting *to* a tombstone version restores that version's body and leaves the " +
			"document alive, it does not re-delete it — use docs.delete for that. " +
			"A mismatch is version_conflict and carries the current body, as on a write; " +
			"pass include_current false to turn that echo off. " + retryAdvice,
		OutputSchema: documentOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in DocsRevertInput) (DocumentOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPDocsRevert(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "docs.diff",
		Description: fmt.Sprintf(
			"Compare two versions of one document and get a unified diff. The versions may "+
				"be given in either order and the diff reads from the first to the second, "+
				"so from 3 to 1 is the reverse of from 1 to 3 and both are legitimate "+
				"questions. "+
				"**There is a comparison bound and the answer says when it was hit.** The "+
				"common prefix and suffix are trimmed first, and if more than %d lines still "+
				"differ on either side the answer is one coarse hunk saying the whole body "+
				"was replaced, with coarse true. A one-line edit inside a 40,000-line script "+
				"is nowhere near the bound; only a document rewritten end to end reaches it. "+
				"**Read coarse before you render the diff** — a client that ignores it shows "+
				"\"everything changed\" and blames the author. %s",
			markdown.MaxDiffLines, retryAdvice),
		OutputSchema: docsDiffOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in DocsDiffInput) (DocsDiffOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPDocsDiff(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "docs.links.list",
		Description: "Read the document-to-entity join from either side: give path for " +
			"everything one document is attached to, or entity_type and entity_key for " +
			"every document attached to one entity. **Exactly one of the two addresses** — " +
			"naming both, or neither, is invalid_input, because they are two different " +
			"questions. The answer's entities array is filled for a document-side question " +
			"and its documents array for an entity-side one; the other is empty. " +
			"An entity lists no document that has been soft-deleted. " + retryAdvice,
		OutputSchema: docsLinksOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in DocsLinksListInput) (DocsLinksOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPDocsLinksList(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "docs.links.add",
		Description: fmt.Sprintf(
			"Attach one document to one entity, in an optional role (\"script\", \"lore\", "+
				"\"design notes\" — free text, at most %d bytes, and Maestro ships no "+
				"vocabulary of them). This is what makes a quest's dialogue findable from the "+
				"quest. "+
				"**Attachment is never inferred from a document's content** — not from its "+
				"frontmatter, not from a heading matching an entity's name — so it is only "+
				"ever what a caller asked for here or in a write's links array. "+
				"A link's key is (document, entity): attaching a document to an entity it is "+
				"already attached to updates the role rather than making a second link. It "+
				"takes no expected_version and does not move the document's version — a link "+
				"is not the document's content, and it is not part of the version history. "+
				"The answer is the document's whole attachment set after the change. %s",
			markdown.MaxRoleLen, retryAdvice),
		OutputSchema: docsLinksOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in DocsLinkAddInput) (DocsLinksOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPDocsLinkAdd(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "docs.links.remove",
		Description: "Detach one document from one entity. **There is no role argument**: a " +
			"link's key is (document, entity), so there is never a second link under " +
			"another role to choose between, and this removes the one link there is. " +
			"Removing a link that is not there is not_found. The document and the entity " +
			"are both untouched — this removes the attachment and nothing else — and the " +
			"answer is the document's whole attachment set after the change. " + retryAdvice,
		OutputSchema: docsLinksOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in DocsLinkRemoveInput) (DocsLinksOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPDocsLinkRemove(ctx, deps, caller, projectID, in)
	})
}

// --- Hand-written output schemas ---
//
// Written by hand for the reason mcp.go's own schema block gives: the
// SDK validates a tool's output against its marshalled JSON, and its
// reflection-based inference gets that JSON wrong for any type whose
// marshalling comes from a method — uuid.UUID and time.Time here.

func integerSchema() *jsonschema.Schema { return &jsonschema.Schema{Type: "integer"} }

var documentOutputSchema = &jsonschema.Schema{
	Type: "object",
	Required: []string{"id", "path", "title", "version", "frontmatter", "body",
		"truncated", "body_length", "deleted", "links"},
	Properties: map[string]*jsonschema.Schema{
		"id":          stringSchema(),
		"path":        stringSchema(),
		"kind":        stringSchema(),
		"title":       stringSchema(),
		"summary":     stringSchema(),
		"version":     integerSchema(),
		"frontmatter": objectSchema(),
		"body":        stringSchema(),
		"truncated":   boolSchema(),
		"body_length": integerSchema(),
		"deleted":     boolSchema(),
		"links":       arrayOf(linkedRefOutputSchema),
	},
}

var documentSummaryOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"id", "path", "title", "version", "deleted"},
	Properties: map[string]*jsonschema.Schema{
		"id":      stringSchema(),
		"path":    stringSchema(),
		"kind":    stringSchema(),
		"title":   stringSchema(),
		"summary": stringSchema(),
		"version": integerSchema(),
		"deleted": boolSchema(),
	},
}

var docsListOutputSchema = listEnvelopeSchema(documentSummaryOutputSchema)

var versionOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"version", "title", "deleted", "created_at"},
	Properties: map[string]*jsonschema.Schema{
		"version":     integerSchema(),
		"title":       stringSchema(),
		"summary":     stringSchema(),
		"message":     stringSchema(),
		"deleted":     boolSchema(),
		"author_kind": stringSchema(),
		"author_id":   stringSchema(),
		"created_at":  stringSchema(),
	},
}

var docsHistoryOutputSchema = listEnvelopeSchema(versionOutputSchema)

var docsVersionOutputSchema = &jsonschema.Schema{
	Type: "object",
	Required: []string{"path", "version", "title", "deleted", "frontmatter", "body",
		"created_at"},
	Properties: map[string]*jsonschema.Schema{
		"path":        stringSchema(),
		"version":     integerSchema(),
		"title":       stringSchema(),
		"summary":     stringSchema(),
		"message":     stringSchema(),
		"deleted":     boolSchema(),
		"frontmatter": objectSchema(),
		"body":        stringSchema(),
		"created_at":  stringSchema(),
	},
}

var docsDiffOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"path", "from_version", "to_version", "unified", "coarse"},
	Properties: map[string]*jsonschema.Schema{
		"path":         stringSchema(),
		"from_version": integerSchema(),
		"to_version":   integerSchema(),
		"unified":      stringSchema(),
		"coarse":       boolSchema(),
	},
}

// linkedDocumentRefSchema is one document an entity is attached to. It
// carries no version and no deleted flag because the join does not
// select them; see LinkedDocumentRef.
var linkedDocumentRefSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"id", "path", "title"},
	Properties: map[string]*jsonschema.Schema{
		"id":    stringSchema(),
		"path":  stringSchema(),
		"title": stringSchema(),
		"kind":  stringSchema(),
		"role":  stringSchema(),
	},
}

var docsLinksOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"entities", "documents"},
	Properties: map[string]*jsonschema.Schema{
		"entities":  arrayOf(linkedRefOutputSchema),
		"documents": arrayOf(linkedDocumentRefSchema),
	},
}
