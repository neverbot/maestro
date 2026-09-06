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
	"github.com/neverbot/maestro/internal/metamodel"
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

// DocsWriteManyInput is the argument shape of docs.write_many: a mode
// and a list of writes.
//
// **It is a second tool rather than a mode of docs.write**, which is the
// opposite of the choice entities.upsert made ("one entity is a batch of
// one, which is why there is no separate single-row tool"). The reason
// is what the two answer with. docs.write answers with the whole
// document — body, frontmatter, attachments — and a version conflict on
// it carries the current body to merge onto; neither survives being
// multiplied by four hundred, so a batch answers with a report of paths,
// ids and versions instead. Two answers that different are two tools,
// and collapsing them would have meant one tool whose answer shape
// depended on how many items it was handed.
type DocsWriteManyInput struct {
	ScopedArgs
	Mode  string               `json:"mode,omitempty"`
	Items []DocsWriteItemInput `json:"items"`
}

// DocsWriteItemInput is one write of a batch: DocsWriteInput without the
// two things that cannot mean anything in a batch.
//
// It carries no project_id, because the batch states the game once, and
// no include_current, because a batch failure is an index, a key, a code
// and a message with nowhere for a body to travel — see
// markdown.WriteMany. Every other argument, including expected_version,
// is per item and means exactly what it means on docs.write.
type DocsWriteItemInput struct {
	Path            string           `json:"path"`
	Content         string           `json:"content"`
	Kind            *string          `json:"kind,omitempty"`
	Message         string           `json:"message,omitempty"`
	ExpectedVersion *int32           `json:"expected_version"`
	Links           *[]DocsLinkInput `json:"links,omitempty"`
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
	Cursor     string `json:"cursor,omitempty"`
	Limit      int32  `json:"limit,omitempty"`
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

	// CreatedAt, UpdatedAt, CreatedBy and UpdatedBy say when this
	// document changed and who changed it. See DocumentSummaryOutput,
	// which carries the same four for the same reason: a fact that is on
	// a listing row and missing from the document itself is a fact a
	// caller has to go somewhere else for.
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	CreatedBy *AuthorOutput `json:"created_by,omitempty"`
	UpdatedBy *AuthorOutput `json:"updated_by,omitempty"`

	// LinksTruncated says the attachments above are one page and not the
	// whole set: ask docs.links.list, which pages.
	//
	// It is not omitempty, because false is a statement — "these are all
	// of them" is the fact a caller acts on, and an absent key would read
	// the same as a client that forgot to look. Reaching it takes more
	// than markdown.MaxLinkPage attachments on one document, which is a
	// taxonomy rather than a document (MaxLinksPerWrite says so at a
	// lower number); the field is here because a listing that silently
	// stops at a bound is the defect this pair exists to prevent, not
	// because the case is common.
	LinksTruncated bool `json:"links_truncated"`
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

	// CreatedAt, UpdatedAt, CreatedBy and UpdatedBy are what make "what
	// changed lately" answerable from one call.
	//
	// **They cost nothing new to publish and everything to withhold.**
	// The columns have been on documents since the schema landed and on
	// this listing's select list since it was written; withholding them
	// meant the first question a designer opens a game bible to ask —
	// what moved this week, and who moved it — took a docs.history call
	// per document, fifty for a page of fifty. That is the shape of an
	// answer nobody asks for twice.
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	CreatedBy *AuthorOutput `json:"created_by,omitempty"`
	UpdatedBy *AuthorOutput `json:"updated_by,omitempty"`
}

// AuthorOutput is who wrote something, in the one shape every answer on
// this surface uses for that fact.
//
// **Kind and Label are both here and neither replaces the other.** Kind
// ("user" or "token") is what tells a designer's edit from an agent's,
// and it is machine-readable; Label is the name a page prints. A client
// that had only the kind would render "an agent" ten times for ten
// versions written by three agents, which is what the reading view did
// before this type existed, and one that had only the label could not
// tell a person from a token with the same name.
//
// **A revoked token still comes back with its label**, which is decided
// rather than incidental — ResolveAuthors' own comment carries the
// argument: revoking a token changes what it may do next, not who wrote
// the prose, and this surface's tokens listing already includes revoked
// rows for exactly that reason.
//
// The whole object is absent — a nil *AuthorOutput — when the row
// records nobody, which is what both audit columns being NULL means and
// what ON DELETE SET NULL leaves behind when a user or a token is really
// gone. A client says what it says about that; the reading view's
// existing answer is "a former member". An object with a kind naming
// nothing would be worse than no object.
//
// Label is omitempty because "present and nameless" is a third case: the
// row names somebody this server could not resolve. A client falls back
// to its own wording there rather than printing an empty name.
type AuthorOutput struct {
	Kind  string     `json:"kind"`
	ID    *uuid.UUID `json:"id,omitempty"`
	Label string     `json:"label,omitempty"`
}

// authorOutput publishes one resolved author, or nothing at all for a
// row that records nobody. One converter, so every answer on this
// surface spells the fact the same way.
func authorOutput(a markdown.Author) *AuthorOutput {
	if a.Kind == "" {
		return nil
	}
	return &AuthorOutput{Kind: a.Kind, ID: a.ID, Label: a.Label}
}

// DocsWriteManyOutput is what a batch of writes answers with: what
// landed and what did not.
//
// Count is len(Written) and is built where both are assembled so the two
// cannot disagree, exactly as EntitiesUpsertOutput's is. Both slices are
// emitted as arrays even when empty — "the batch reported no failures"
// and "the batch reported nothing" must not look the same.
type DocsWriteManyOutput struct {
	Count   int                      `json:"count"`
	Written []markdown.DocumentWrite `json:"written"`
	Failed  []metamodel.BulkFailure  `json:"failed"`
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

	// AuthorLabel is the name that goes with the pair above: a user's
	// display name, a token's label.
	//
	// **Without it a history is a list of uuids.** The pair said which
	// *kind* of author wrote a version and gave an id that nothing on
	// this surface could resolve — there was no read path from a token id
	// to its label at all, though the label exists and the tokens listing
	// already returns it — so the reading view called every token "an
	// agent", and a designer looking at ten versions by three agents saw
	// "an agent" ten times.
	//
	// It is omitempty, and empty means two different things that a client
	// tells apart by the pair beside it: with no author_kind, the version
	// records nobody; with one, it records somebody this server could not
	// name.
	AuthorLabel string `json:"author_label,omitempty"`
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
//
// FromDeleted and ToDeleted say whether either endpoint is a tombstone,
// and they are on the wire for a reason the unified text cannot supply:
// a tombstone carries the body the document had when it was deleted, so
// a diff spanning a deletion is empty and reads as "nothing changed".
// markdown.DiffResult's own comment carries the argument, and
// TestADiffAcrossATombstoneSaysWhichSideIsDeleted pins it there;
// TestDocumentsEndToEnd asserts both of them on this type. Neither is
// omitempty: false is a statement about a live version.
type DocsDiffOutput struct {
	Path        string `json:"path"`
	FromVersion int32  `json:"from_version"`
	ToVersion   int32  `json:"to_version"`
	Unified     string `json:"unified"`
	Coarse      bool   `json:"coarse"`
	FromDeleted bool   `json:"from_deleted"`
	ToDeleted   bool   `json:"to_deleted"`
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
//
// NextCursor and Truncated are set together, from one condition, so they
// cannot disagree — the pair DocsListOutput carries, and here for the
// same reason: this answer is a page, and a page nobody knows is a page
// is a wrong answer that reads as a right one. The cursor belongs to the
// side it was issued for, and the two sides refuse each other's
// (markdown.documentLinksFingerprint).
type DocsLinksOutput struct {
	Entities   []LinkedRef         `json:"entities"`
	Documents  []LinkedDocumentRef `json:"documents"`
	NextCursor *string             `json:"next_cursor,omitempty"`
	Truncated  bool                `json:"truncated"`
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

// MCPDocsWriteMany implements docs.write_many.
//
// The mode string is passed through as the agent wrote it rather than
// folded onto the default when it is not recognised, for the reason
// MCPEntitiesUpsert gives: reading a typo as "partial" would silently
// land rows a caller asked to have rolled back.
func MCPDocsWriteMany(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in DocsWriteManyInput) (DocsWriteManyOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return DocsWriteManyOutput{}, err
	}
	return docsWriteMany(ctx, deps, caller, projectID, in)
}

func docsWriteMany(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in DocsWriteManyInput) (DocsWriteManyOutput, error) {
	actor := actorOf(caller)
	items := make([]markdown.WriteInput, 0, len(in.Items))
	for _, item := range in.Items {
		items = append(items, markdown.WriteInput{
			Path:            item.Path,
			Content:         item.Content,
			Kind:            item.Kind,
			Message:         item.Message,
			ExpectedVersion: item.ExpectedVersion,
			Links:           linkTargetsOf(item.Links),
			Actor:           actor,
		})
	}
	result, err := deps.Markdown.WriteMany(ctx, projectID, items, metamodel.BulkMode(in.Mode))
	if err != nil {
		return DocsWriteManyOutput{}, err
	}
	out := DocsWriteManyOutput{
		Count: len(result.Written), Written: result.Written, Failed: result.Failed,
	}
	if out.Written == nil {
		out.Written = []markdown.DocumentWrite{}
	}
	if out.Failed == nil {
		out.Failed = []metamodel.BulkFailure{}
	}
	return out, nil
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
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
			CreatedBy: authorOutput(row.CreatedBy), UpdatedBy: authorOutput(row.UpdatedBy),
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
	return documentSummaryOf(ctx, deps, projectID, row)
}

// documentSummaryOf builds a listing-shaped answer for one document row,
// audit columns included. docsDelete is its only caller today and the
// listing builds its own rows from markdown.DocumentSummary, which
// already carries resolved authors; this exists so the two shapes cannot
// disagree about what a summary carries.
func documentSummaryOf(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	row dbq.Document) (DocumentSummaryOutput, error) {
	created, updated, err := documentAudit(ctx, deps, projectID, row)
	if err != nil {
		return DocumentSummaryOutput{}, err
	}
	return DocumentSummaryOutput{
		ID: row.ID, Path: row.Path, Kind: row.Kind, Title: row.Title,
		Summary: row.Summary, Version: row.CurrentVersion, Deleted: row.DeletedAt.Valid,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
		CreatedBy: created, UpdatedBy: updated,
	}, nil
}

// documentAudit resolves one document row's two audit pairs to labels,
// in one round trip.
//
// **The order of the two actors is the whole contract here** — created
// first, updated second — and it is unpacked in the same order it is
// packed, three lines apart, so the pairing cannot drift. The listing
// does the same thing over fifty rows (markdown.Service.List) and states
// the same rule.
func documentAudit(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	row dbq.Document) (created, updated *AuthorOutput, err error) {
	authors, err := deps.Markdown.Authors(ctx, projectID, []markdown.Actor{
		{UserID: row.CreatedByUserID, TokenID: row.CreatedByTokenID},
		{UserID: row.UpdatedByUserID, TokenID: row.UpdatedByTokenID},
	})
	if err != nil {
		return nil, nil, err
	}
	return authorOutput(authors[0]), authorOutput(authors[1]), nil
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
		items = append(items, VersionOutput{
			Version: row.Version, Title: row.Title, Summary: row.Summary,
			Message: row.Message, Deleted: row.Deleted, CreatedAt: row.CreatedAt,
			AuthorKind: row.Author.Kind, AuthorID: row.Author.ID,
			AuthorLabel: row.Author.Label,
		})
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
		FromDeleted: result.FromDeleted,
		ToDeleted:   result.ToDeleted,
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
		page, err := deps.Markdown.LinksByDocument(ctx, projectID, in.Path,
			markdown.LinksFilter{Cursor: in.Cursor, Limit: in.Limit})
		if err != nil {
			return DocsLinksOutput{}, err
		}
		return linksOutput(page.Links, nil, page.NextCursor), nil
	default:
		page, err := deps.Markdown.LinksByEntity(ctx, projectID, in.EntityType, in.EntityKey,
			markdown.LinksFilter{Cursor: in.Cursor, Limit: in.Limit})
		if err != nil {
			return DocsLinksOutput{}, err
		}
		return linksOutput(nil, page.Links, page.NextCursor), nil
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
	// The cap rather than the default: a document's own answer carries
	// its attachments inline and has no cursor to hand out, so asking
	// for the largest page there is makes LinksTruncated the rarest
	// possible answer. It is still an answer this type has to be able to
	// give — see LinksTruncated.
	page, err := deps.Markdown.LinksByDocument(ctx, projectID, row.Path,
		markdown.LinksFilter{Limit: markdown.MaxLinkPage})
	if err != nil {
		return DocumentOutput{}, err
	}
	created, updated, err := documentAudit(ctx, deps, projectID, row)
	if err != nil {
		return DocumentOutput{}, err
	}
	body, truncated, length := row.BodyMd, false, len(row.BodyMd)
	if headOnly {
		body, truncated, length = headOf(row.BodyMd)
	}
	return DocumentOutput{
		ID:             row.ID,
		Path:           row.Path,
		Kind:           row.Kind,
		Title:          row.Title,
		Summary:        row.Summary,
		Version:        row.CurrentVersion,
		Frontmatter:    frontmatterOf(row.Frontmatter),
		Body:           body,
		Truncated:      truncated,
		BodyLength:     length,
		Deleted:        row.DeletedAt.Valid,
		CreatedAt:      row.CreatedAt.Time,
		UpdatedAt:      row.UpdatedAt.Time,
		CreatedBy:      created,
		UpdatedBy:      updated,
		Links:          linkedRefsOf(page.Links),
		LinksTruncated: page.NextCursor != "",
	}, nil
}

// documentSideLinks answers the join from the document's side, which is
// what the two write tools answer with as well as docs.links.list.
func documentSideLinks(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	path string) (DocsLinksOutput, error) {
	page, err := deps.Markdown.LinksByDocument(ctx, projectID, path, markdown.LinksFilter{})
	if err != nil {
		return DocsLinksOutput{}, err
	}
	return linksOutput(page.Links, nil, page.NextCursor), nil
}

// linksOutput builds the join answer with both arrays non-nil, so an
// empty side marshals as [] and never as null, and publishes the page's
// cursor whichever side produced it.
func linksOutput(entities []markdown.EntityLink, documents []markdown.DocumentLink,
	next string,
) DocsLinksOutput {
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
	if next != "" {
		cursor := next
		out.NextCursor = &cursor
		out.Truncated = true
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
//
// **The empty branch is unreachable through this server today** and is
// kept deliberately, which is why no test pins it: the column is
// jsonb NOT NULL DEFAULT '{}' and the domain writes `{}` for a document
// with no frontmatter, so every row reaching here is at least two bytes
// long. It exists because the alternative to a branch that costs
// nothing is a nil RawMessage marshalling as `null` the first time
// anything hands this function a zero value — a row read by a query
// that does not select the column, say.
func frontmatterOf(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
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
				"and it carries the current version, *who wrote it and when* "+
				"(current_author_kind, current_author_id, current_author_label, "+
				"current_updated_at — always, so you can tell a retry from a conversation) "+
				"*and the current body* so you can merge "+
				"without a second call — pass include_current false to turn that echo off "+
				"for a large document, which drops the body and keeps the author. If the "+
				"conflict's details say deleted, the version it "+
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
				"kind is free text of at most %d bytes; message is the \"why\" line this "+
				"version records, at most %d bytes, and it is what makes the history "+
				"readable a year later. "+
				"**links replaces the document's whole attachment set; omitting links — or "+
				"sending `\"links\": null`, which means the same thing — leaves it "+
				"untouched.** Pass an empty array to detach everything: that is the only "+
				"way to say it, and null is not it. At most %d attachments, each with an "+
				"optional role of at most %d bytes. Attachment is never inferred from the "+
				"content. %s",
			markdown.MaxBodyBytes, markdown.MaxIndexedChars, markdown.MaxPathLen,
			markdown.MaxPathSegments, markdown.MaxKindLen, markdown.MaxMessageLen,
			markdown.MaxLinksPerWrite, markdown.MaxRoleLen, retryAdvice),
		OutputSchema: documentOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in DocsWriteInput) (DocumentOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPDocsWrite(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "docs.write_many",
		Description: fmt.Sprintf(
			"Write several documents in one call. Seeding a game bible is a page per zone "+
				"and a script per quest, and one round trip per document is the difference "+
				"between one call and hundreds. Every item is a docs.write: the same path "+
				"rules, the same content, kind, message and links arguments, and the same "+
				"meaning for each. "+
				"**expected_version is required on every item, because a batch is a list of "+
				"claims about versions rather than a list of rows.** Pass 0 for a document "+
				"that must not exist yet and the version you read for one that does; there is "+
				"no batch-wide version, and an item that omits it fails alone at its own "+
				"index while the rest land. "+
				"mode is \"partial\" (the default: every item is its own transaction, the "+
				"good documents land and the rest come back in failed with their index, their "+
				"path and a code saying how to fix them) or \"atomic\" (one transaction; one "+
				"bad item rolls the whole batch back and nothing is reported as done). "+
				"Anything else is refused rather than read as partial. "+
				"**A stale expected_version is that item's failure, coded version_conflict, "+
				"and not the batch's** — re-read that document, merge, and send that item "+
				"again. It carries the version to merge onto and **not** the current body: "+
				"there is no include_current here, because four hundred conflicts would "+
				"answer with four hundred bodies. Use docs.write when you are merging prose. "+
				"Two items addressing one path are refused as such — paths are matched "+
				"without regard to case, so the second would otherwise overwrite the first "+
				"and both would be reported as landed. "+
				"written names every document that landed with its path, its id and its new "+
				"version, which is the expected_version of your next edit to it; count is how "+
				"many. A failure coded \"retryable\" means the database refused that item "+
				"over contention — send it again, and send fewer items at a time if a batch "+
				"keeps producing them. Bodies are at most %d bytes each, as on docs.write, and "+
				"a batch carries **at most %d items** — over that is invalid_input at path "+
				"`items` naming both numbers, the same ceiling entities.upsert and "+
				"relations.upsert answer to. %s",
			markdown.MaxBodyBytes, metamodel.MaxBulkItems, retryAdvice),
		OutputSchema: docsWriteManyOutputSchema,
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in DocsWriteManyInput) (DocsWriteManyOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPDocsWriteMany(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "docs.read",
		Description: fmt.Sprintf(
			"Read one document by path: its raw markdown body, its stored frontmatter, its "+
				"current version and the entities it is attached to. The body is the "+
				"markdown as written, never rendered HTML. "+
				"Pass head_only true for at most the first %d bytes of the body, with the "+
				"frontmatter, instead of the whole thing, when you are deciding whether you "+
				"want the document at all; the answer then carries truncated true and "+
				"body_length, the full size in bytes. The head is cut back off any character "+
				"the bound would split, so it can be a byte or two shorter than that number "+
				"and is always valid text. **A truncated body is not the document** — do not "+
				"write it back. "+
				"A soft-deleted document is not found here: list it with include_deleted, or "+
				"read one of its versions with docs.read_version. "+
				"The answer says when the document was created and last changed "+
				"(created_at, updated_at) and by whom (created_by, updated_by, each with a "+
				"kind of \"user\" or \"token\", an id and a label). %s",
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
				"filter. A kind is at most %d bytes here, as it is on a write. "+
				"A kind or a prefix that names nothing answers with an empty page; "+
				"an entity that names nothing is not_found, because that one is an address. "+
				"include_deleted brings back soft-deleted documents, which are otherwise "+
				"absent. Pass the previous answer's next_cursor for the next page; a cursor "+
				"belongs to the game and the filter it was issued for and is refused against "+
				"any other. limit defaults to %d and is capped at %d — asking for more gets "+
				"the cap, asking for less than one gets the default. "+
				"**Every row says when it changed and who changed it** — created_at, "+
				"updated_at, created_by and updated_by, each author carrying a kind of "+
				"\"user\" or \"token\", an id and a label — so \"what moved this week, and "+
				"who moved it\" is one call and not one call per document. %s",
			markdown.MaxKindLen, markdown.DefaultDocumentPage, markdown.MaxDocumentPage,
			retryAdvice),
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
				"include_deleted, in search, on the entity pages it was attached to, and as "+
				"an address for docs.links.list, docs.links.add and docs.links.remove, all "+
				"three of which answer not_found for its path — its links survive and come "+
				"back with it. %s",
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
				"agent's are told apart, and author_label is that person's display name or "+
				"that token's label, so you can say who without a second call. A revoked "+
				"token still comes back with its label: revoking it changes what it may do "+
				"next, not who wrote this. An entry with no author_kind at all records "+
				"nobody — the user or the token is gone. **Metadata only — no bodies.** "+
				"Read one version's body with docs.read_version, or compare two with "+
				"docs.diff. "+
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
		Description: fmt.Sprintf(
			"Restore a past version of a document as a **new** version. History is "+
				"append-only: reverting a three-version document leaves four versions, not "+
				"one, and the writing that was reverted is still there to read. "+
				"expected_version is required and is the version you read, so a revert "+
				"cannot race an edit; to_version is the one to restore. The restored content "+
				"is the content that was stored — title, summary and frontmatter come off "+
				"the version row verbatim. message is recorded on the new version, at most "+
				"%d bytes, as on a write. "+
				"**A revert does not touch the document's kind and does not touch its "+
				"links**: it restores the writing, and what a document is about is a "+
				"separate decision somebody made separately. Reverting a deleted document "+
				"brings it back; reverting *to* a tombstone version restores that version's "+
				"body and leaves the document alive, it does not re-delete it — use "+
				"docs.delete for that. "+
				"A mismatch is version_conflict and carries the current body, as on a write; "+
				"pass include_current false to turn that echo off. %s",
			markdown.MaxMessageLen, retryAdvice),
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
		Description: fmt.Sprintf(
			"Read the document-to-entity join from either side: give path for "+
				"everything one document is attached to, or entity_type and entity_key for "+
				"every document attached to one entity. **Exactly one of the two addresses** — "+
				"naming both, or neither, is invalid_input, because they are two different "+
				"questions. The answer's entities array is filled for a document-side question "+
				"and its documents array for an entity-side one; the other is empty. "+
				"**Both sides page.** limit defaults to %d and is capped at %d — asking for "+
				"more gets the cap, asking for less than one gets the default — and truncated "+
				"true means there is more: pass the answer's next_cursor for the next page. A "+
				"cursor belongs to the game, the side and the address it was issued for, and "+
				"is refused against any other, the document side and the entity side "+
				"included. "+
				"An entity lists no document that has been soft-deleted, and a soft-deleted "+
				"document is not an address either: asking by its path answers not_found rather "+
				"than an empty set. Its links are not gone — they come back with the document "+
				"when a write to the same path brings it back. %s",
			markdown.DefaultLinkPage, markdown.MaxLinkPage, retryAdvice),
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
		"truncated", "body_length", "deleted", "links", "links_truncated",
		"created_at", "updated_at"},
	Properties: map[string]*jsonschema.Schema{
		"id":              stringSchema(),
		"path":            stringSchema(),
		"kind":            stringSchema(),
		"title":           stringSchema(),
		"summary":         stringSchema(),
		"version":         integerSchema(),
		"frontmatter":     objectSchema(),
		"body":            stringSchema(),
		"truncated":       boolSchema(),
		"body_length":     integerSchema(),
		"deleted":         boolSchema(),
		"created_at":      stringSchema(),
		"updated_at":      stringSchema(),
		"created_by":      authorOutputSchema(),
		"updated_by":      authorOutputSchema(),
		"links":           arrayOf(linkedRefOutputSchema),
		"links_truncated": boolSchema(),
	},
}

// authorOutputSchema is AuthorOutput's wire shape, shared by every
// answer that names who wrote something.
//
// **A function and not a var**, like integerSchema and stringSchema
// beside it and unlike the row schemas above: the SDK requires a tool's
// output schema to form a tree, so one shared pointer used for both
// created_by and updated_by panics at registration. It is caught the
// moment a server is built (TestEveryMCPToolGoesThroughAddScopedTool),
// which is why this is a note rather than a hazard.
func authorOutputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:     "object",
		Required: []string{"kind"},
		Properties: map[string]*jsonschema.Schema{
			"kind":  stringSchema(),
			"id":    stringSchema(),
			"label": stringSchema(),
		},
	}
}

var documentSummaryOutputSchema = &jsonschema.Schema{
	Type: "object",
	Required: []string{"id", "path", "title", "version", "deleted",
		"created_at", "updated_at"},
	Properties: map[string]*jsonschema.Schema{
		"id":         stringSchema(),
		"path":       stringSchema(),
		"kind":       stringSchema(),
		"title":      stringSchema(),
		"summary":    stringSchema(),
		"version":    integerSchema(),
		"deleted":    boolSchema(),
		"created_at": stringSchema(),
		"updated_at": stringSchema(),
		"created_by": authorOutputSchema(),
		"updated_by": authorOutputSchema(),
	},
}

var docsListOutputSchema = listEnvelopeSchema(documentSummaryOutputSchema)

// documentWriteOutputSchema is markdown.DocumentWrite's wire shape: one
// document a batch landed. bulkFailureSchema (mcp_metamodel.go) is the
// other half and is shared with the two metamodel batches rather than
// copied, because a batch failure means the same thing whatever kind of
// row produced it.
var documentWriteOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"path", "id", "version"},
	Properties: map[string]*jsonschema.Schema{
		"path":    stringSchema(),
		"id":      stringSchema(),
		"version": integerSchema(),
	},
}

var docsWriteManyOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"count", "written", "failed"},
	Properties: map[string]*jsonschema.Schema{
		"count":   integerSchema(),
		"written": arrayOf(documentWriteOutputSchema),
		"failed":  arrayOf(bulkFailureSchema),
	},
}

var versionOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"version", "title", "deleted", "created_at"},
	Properties: map[string]*jsonschema.Schema{
		"version":      integerSchema(),
		"title":        stringSchema(),
		"summary":      stringSchema(),
		"message":      stringSchema(),
		"deleted":      boolSchema(),
		"author_kind":  stringSchema(),
		"author_id":    stringSchema(),
		"author_label": stringSchema(),
		"created_at":   stringSchema(),
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
	Required: []string{"entities", "documents", "truncated"},
	Properties: map[string]*jsonschema.Schema{
		"entities":    arrayOf(linkedRefOutputSchema),
		"documents":   arrayOf(linkedDocumentRefSchema),
		"next_cursor": stringSchema(),
		"truncated":   boolSchema(),
	},
}
