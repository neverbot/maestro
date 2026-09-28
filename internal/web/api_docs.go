package web

import (
	"net/http"
	"time"

	"github.com/neverbot/maestro/internal/markdown"
)

// This file is the human half of the prose surface: the REST routes a
// browser reads and writes a game's documents through. It mirrors the
// twelve `docs.*` MCP tools (mcp_docs.go), and "mirrors" is meant
// literally — every handler here builds that file's own input struct,
// calls that file's own unexported core, and answers with that file's
// own output struct. There is one implementation of each operation and
// one wire vocabulary; the two surfaces differ only in how a caller is
// admitted (a token's binding on MCP, a session member's role here) and
// in how a refusal is spelled: an MCP tool answers a code inside a tool
// result, this file answers the same code and the same details object
// under an HTTP status, through writeDomainError.
func (s *Server) requireProseService(w http.ResponseWriter) bool {
	if s.opts.Markdown == nil {
		writeError(w, http.StatusNotFound, errCodeNotFound, "this instance serves no prose")
		return false
	}
	return true
}

// --- The two rendered views ---

// DocRenderedOutput is the reading view: one document's identity and its
// body rendered to HTML.
type DocRenderedOutput struct {
	Path    string      `json:"path"`
	Kind    string      `json:"kind,omitempty"`
	Title   string      `json:"title"`
	Summary string      `json:"summary,omitempty"`
	Version int32       `json:"version"`
	HTML    string      `json:"html"`
	Links   []LinkedRef `json:"links"`

	// LinksTruncated is DocumentOutput's field, carried here because
	// this view is built from that answer and shows the same list. A
	// reading page that quietly showed two hundred of a document's
	// attachments would be the same wrong answer through a second route.
	LinksTruncated bool `json:"links_truncated"`

	// UpdatedAt and UpdatedBy say when the document last changed and who
	// changed it, which is what the page's meta line prints beside the
	// version.
	UpdatedAt time.Time     `json:"updated_at"`
	UpdatedBy *AuthorOutput `json:"updated_by,omitempty"`
}

// DocComparisonOutput is the comparison view: the diff docs.diff would
// answer with, plus that diff rendered as classed lines.
type DocComparisonOutput struct {
	Path        string `json:"path"`
	FromVersion int32  `json:"from_version"`
	ToVersion   int32  `json:"to_version"`
	Unified     string `json:"unified"`
	Coarse      bool   `json:"coarse"`
	FromDeleted bool   `json:"from_deleted"`
	ToDeleted   bool   `json:"to_deleted"`
	HTML        string `json:"html"`
}

// --- Documents ---

func (s *Server) handleListDocs(w http.ResponseWriter, r *http.Request, _ Caller, scope ProjectScope) {
	if !s.requireProseService(w) {
		return
	}
	pathPrefix, ok := queryString(w, r, "path_prefix")
	if !ok {
		return
	}
	kind, ok := queryString(w, r, "kind")
	if !ok {
		return
	}
	entityType, ok := queryString(w, r, "entity_type")
	if !ok {
		return
	}
	entityKey, ok := queryString(w, r, "entity_key")
	if !ok {
		return
	}
	includeDeleted, ok := queryBool(w, r, "include_deleted")
	if !ok {
		return
	}
	cursor, ok := queryString(w, r, "cursor")
	if !ok {
		return
	}
	limit, ok := queryLimit(w, r)
	if !ok {
		return
	}
	out, err := docsList(r.Context(), s.deps(), scope.ProjectID, DocsListInput{
		PathPrefix: pathPrefix, Kind: kind, EntityType: entityType, EntityKey: entityKey,
		IncludeDeleted: includeDeleted, Cursor: cursor, Limit: limit,
	})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleWriteDoc answers 200, not 201, for the reason handleUpsertType
// gives: the write is idempotent by path and the same request may create
// a document or edit one, so a status claiming "created" would be wrong
// half the time. The answer carries the version, which is what a client
// needs to know what happened and what to send next.
func (s *Server) handleWriteDoc(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireProseService(w) {
		return
	}
	var in DocsWriteInput
	if !decodeContentBody(w, r, &in) || !checkStatedProject(w, scope, in) {
		return
	}
	out, err := docsWrite(r.Context(), s.deps(), caller, scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleWriteDocs is the REST mirror of docs.write_many. It answers 200
// with a report even when some items failed, for the reason
// handleUpsertEntities gives: in partial mode a batch that lands
// nineteen of twenty documents is not a failed request, and the failures
// are in the body with their index, their path and their code. Only a
// refusal of the *call* — an unknown mode, an atomic batch rolled back,
// a path repeated inside the batch — comes back as a status.
func (s *Server) handleWriteDocs(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireProseService(w) {
		return
	}
	var in DocsWriteManyInput
	if !decodeContentBody(w, r, &in) || !checkStatedProject(w, scope, in) {
		return
	}
	out, err := docsWriteMany(r.Context(), s.deps(), caller, scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleMoveDoc is the REST mirror of docs.move. It is a POST with a
// body and not a PATCH on a path segment, for the reason this file's
// header gives: a document path never occupies a URL segment, and a move
// has two of them.
func (s *Server) handleMoveDoc(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireProseService(w) {
		return
	}
	var in DocsMoveInput
	if !decodeContentBody(w, r, &in) || !checkStatedProject(w, scope, in) {
		return
	}
	out, err := docsMove(r.Context(), s.deps(), caller, scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDocKinds is the REST mirror of docs.kinds, and it is what the
// game page reads to show a designer the vocabulary its own prose uses.
func (s *Server) handleDocKinds(w http.ResponseWriter, r *http.Request, _ Caller, scope ProjectScope) {
	if !s.requireProseService(w) {
		return
	}
	out, err := docsKinds(r.Context(), s.deps(), scope.ProjectID)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleReadDoc(w http.ResponseWriter, r *http.Request, _ Caller, scope ProjectScope) {
	if !s.requireProseService(w) {
		return
	}
	path, ok := queryString(w, r, "path")
	if !ok {
		return
	}
	headOnly, ok := queryBool(w, r, "head_only")
	if !ok {
		return
	}
	out, err := docsRead(r.Context(), s.deps(), scope.ProjectID, DocsReadInput{Path: path, HeadOnly: headOnly})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteDoc reads its arguments from the query string rather than
// from a body: this is the one write on this surface whose HTTP method
// has no body by convention, and a DELETE carrying one is refused or
// dropped by enough intermediaries that relying on it would be a bug
// waiting for a proxy. expected_version therefore arrives through
// queryVersion, which preserves the difference between absent and zero —
// the whole point of that argument, since zero means "create".
func (s *Server) handleDeleteDoc(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireProseService(w) {
		return
	}
	path, ok := queryString(w, r, "path")
	if !ok {
		return
	}
	message, ok := queryString(w, r, "message")
	if !ok {
		return
	}
	expected, ok := queryVersion(w, r, "expected_version")
	if !ok {
		return
	}
	out, err := docsDelete(r.Context(), s.deps(), caller, scope.ProjectID, DocsDeleteInput{
		Path: path, Message: message, ExpectedVersion: expected,
	})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- Versions ---

func (s *Server) handleDocHistory(w http.ResponseWriter, r *http.Request, _ Caller, scope ProjectScope) {
	if !s.requireProseService(w) {
		return
	}
	path, ok := queryString(w, r, "path")
	if !ok {
		return
	}
	cursor, ok := queryString(w, r, "cursor")
	if !ok {
		return
	}
	limit, ok := queryLimit(w, r)
	if !ok {
		return
	}
	out, err := docsHistory(r.Context(), s.deps(), scope.ProjectID, DocsHistoryInput{
		Path: path, Cursor: cursor, Limit: limit,
	})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleReadDocVersion(w http.ResponseWriter, r *http.Request, _ Caller, scope ProjectScope) {
	if !s.requireProseService(w) {
		return
	}
	path, ok := queryString(w, r, "path")
	if !ok {
		return
	}
	version, ok := s.requiredVersion(w, r, "version")
	if !ok {
		return
	}
	out, err := docsReadVersion(r.Context(), s.deps(), scope.ProjectID, DocsReadVersionInput{
		Path: path, Version: version,
	})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRevertDoc(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireProseService(w) {
		return
	}
	var in DocsRevertInput
	if !decodeContentBody(w, r, &in) || !checkStatedProject(w, scope, in) {
		return
	}
	out, err := docsRevert(r.Context(), s.deps(), caller, scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDocDiff(w http.ResponseWriter, r *http.Request, _ Caller, scope ProjectScope) {
	if !s.requireProseService(w) {
		return
	}
	in, ok := s.diffArguments(w, r)
	if !ok {
		return
	}
	out, err := docsDiff(r.Context(), s.deps(), scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- Links ---

func (s *Server) handleListDocLinks(w http.ResponseWriter, r *http.Request, _ Caller, scope ProjectScope) {
	if !s.requireProseService(w) {
		return
	}
	path, ok := queryString(w, r, "path")
	if !ok {
		return
	}
	entityType, ok := queryString(w, r, "entity_type")
	if !ok {
		return
	}
	entityKey, ok := queryString(w, r, "entity_key")
	if !ok {
		return
	}
	cursor, ok := queryString(w, r, "cursor")
	if !ok {
		return
	}
	limit, ok := queryLimit(w, r)
	if !ok {
		return
	}
	// Neither the both-sides refusal nor the neither-side one is decided
	// here: docsLinksList makes both, so the two surfaces cannot drift
	// into disagreeing about what "the join, from either side" means.
	// The paging arguments are read the same way the listing route reads
	// its own, so a browser walking an entity's hundred scripts spells it
	// exactly as it spells walking a game's documents.
	out, err := docsLinksList(r.Context(), s.deps(), scope.ProjectID, DocsLinksListInput{
		Path: path, EntityType: entityType, EntityKey: entityKey,
		Cursor: cursor, Limit: limit,
	})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAddDocLink(w http.ResponseWriter, r *http.Request, _ Caller, scope ProjectScope) {
	if !s.requireProseService(w) {
		return
	}
	var in DocsLinkAddInput
	if !decodeContentBody(w, r, &in) || !checkStatedProject(w, scope, in) {
		return
	}
	out, err := docsLinkAdd(r.Context(), s.deps(), scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRemoveDocLink reads its arguments from the query string, like
// handleDeleteDoc and for the same reason. It reads no `role`, because
// DocsLinkRemoveInput carries none — the link's key is (document,
// entity), so there is no second link under another role to choose
// between. See DocsLinkAddInput's doc comment.
func (s *Server) handleRemoveDocLink(w http.ResponseWriter, r *http.Request, _ Caller, scope ProjectScope) {
	if !s.requireProseService(w) {
		return
	}
	path, ok := queryString(w, r, "path")
	if !ok {
		return
	}
	entityType, ok := queryString(w, r, "entity_type")
	if !ok {
		return
	}
	entityKey, ok := queryString(w, r, "entity_key")
	if !ok {
		return
	}
	out, err := docsLinkRemove(r.Context(), s.deps(), scope.ProjectID, DocsLinkRemoveInput{
		Path: path, EntityType: entityType, EntityKey: entityKey,
	})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- The rendered views ---

// handleRenderDoc serves the reading view. It reads the document through
// the same core docs.read uses and renders the body it answers with, so
// there is no second read path and no second definition of "this
// document's current body".
func (s *Server) handleRenderDoc(w http.ResponseWriter, r *http.Request, _ Caller, scope ProjectScope) {
	if !s.requireProseService(w) {
		return
	}
	path, ok := queryString(w, r, "path")
	if !ok {
		return
	}
	doc, err := docsRead(r.Context(), s.deps(), scope.ProjectID, DocsReadInput{Path: path})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	rendered, err := markdown.Render(doc.Body)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, DocRenderedOutput{
		Path:           doc.Path,
		Kind:           doc.Kind,
		Title:          doc.Title,
		Summary:        doc.Summary,
		Version:        doc.Version,
		HTML:           rendered,
		Links:          doc.Links,
		LinksTruncated: doc.LinksTruncated,
		UpdatedAt:      doc.UpdatedAt,
		UpdatedBy:      doc.UpdatedBy,
	})
}

// handleCompareDoc serves the comparison view, from the same core
// docs.diff uses.
func (s *Server) handleCompareDoc(w http.ResponseWriter, r *http.Request, _ Caller, scope ProjectScope) {
	if !s.requireProseService(w) {
		return
	}
	in, ok := s.diffArguments(w, r)
	if !ok {
		return
	}
	diff, err := docsDiff(r.Context(), s.deps(), scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, DocComparisonOutput{
		Path:        diff.Path,
		FromVersion: diff.FromVersion,
		ToVersion:   diff.ToVersion,
		Unified:     diff.Unified,
		Coarse:      diff.Coarse,
		FromDeleted: diff.FromDeleted,
		ToDeleted:   diff.ToDeleted,
		HTML:        markdown.RenderDiff(diff.Unified),
	})
}

// diffArguments reads the three parameters /docs/diff and
// /docs/comparison share. One reader, not two: the two routes answer the
// same question and a caller that learned one's spelling has learned the
// other's.
func (s *Server) diffArguments(w http.ResponseWriter, r *http.Request) (DocsDiffInput, bool) {
	path, ok := queryString(w, r, "path")
	if !ok {
		return DocsDiffInput{}, false
	}
	from, ok := s.requiredVersion(w, r, "from_version")
	if !ok {
		return DocsDiffInput{}, false
	}
	to, ok := s.requiredVersion(w, r, "to_version")
	if !ok {
		return DocsDiffInput{}, false
	}
	return DocsDiffInput{Path: path, FromVersion: from, ToVersion: to}, true
}

// requiredVersion reads a version-shaped query parameter that the MCP
// twin's schema marks `required`, and refuses its absence at its own
// path.
func (s *Server) requiredVersion(w http.ResponseWriter, r *http.Request, name string) (int32, bool) {
	value, ok := queryVersion(w, r, name)
	if !ok {
		return 0, false
	}
	if value == nil {
		s.writeDomainError(w, r, invalidInput(name, "is required"))
		return 0, false
	}
	return *value, true
}
