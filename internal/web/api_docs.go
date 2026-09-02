package web

import (
	"net/http"

	"github.com/neverbot/maestro/internal/markdown"
)

// This file is the human half of the prose surface: the REST routes a
// browser reads and writes a game's documents through. It mirrors the
// eleven `docs.*` MCP tools (mcp_docs.go), and "mirrors" is meant
// literally — every handler here builds that file's own input struct,
// calls that file's own unexported core, and answers with that file's
// own output struct. There is one implementation of each operation and
// one wire vocabulary; the two surfaces differ only in how a caller is
// admitted (a token's binding on MCP, a session member's role here) and
// in how a refusal is spelled: an MCP tool answers a code inside a tool
// result, this file answers the same code and the same details object
// under an HTTP status, through writeDomainError.
//
// Two routes have no MCP twin, deliberately: /docs/rendered and
// /docs/comparison. **MCP always gets the raw body** (spec §7) — an
// agent asked to rewrite a script needs the markdown it will edit, and
// handing it HTML would mean it rewrote the rendering — so rendering is
// a REST-only affordance for the browser.
// TestTheReadingViewRendersAndTheRawBodyIsWhatMCPGets reads one document
// through both surfaces and pins that split.
//
// **A document path travels as a query parameter and never as a URL
// segment.** This breaks the metamodel's own convention
// (/types/by-key/{key}, api_metamodel.go's header) and the reason is
// Go's router, not taste. A document path contains slashes, so the
// equivalent shape would be /docs/by-path/{path...}, and ServeMux
// requires a {...} wildcard to be the *final* segment — which forecloses
// every sub-resource this domain has: /history, /version, /diff, /links.
// Percent-encoding the slashes into one segment does not work either:
// net/http decodes %2F before matching, so `lore%2Fduskwood` arrives as
// two segments and matches nothing. Moving the path into the query
// string keeps the convention's actual guarantee — a row's key never
// shares a namespace with a literal — in a *stronger* form than the
// metamodel has it: a document path occupies no URL segment at all, so
// it can collide with no literal ever, and /docs/history can never be
// shadowed by a document called `history`.
// TestADocumentPathIsNeverAURLSegment writes a document at each of the
// six sub-resource literals and reads every one of them back.
//
// **Every route here is registered through registerContentRoute**
// (server.go), which is what applies requireEditor to every non-GET. A
// viewer may read a game's prose and may not change it, and the check is
// decided by the pattern's own method rather than by each handler
// remembering — see registerContentRoute's doc comment for what that
// cost when it was each handler's job.

// requireProseService refuses a prose route on an instance built without
// a markdown service. That shape is supported deliberately
// (Options.Markdown), and every core below would panic on a nil service,
// so the guard is here rather than in each of them.
//
// **The routes themselves are registered unconditionally**, exactly as
// the game-content routes are, and that is load-bearing rather than
// incidental: TestEveryGameScopedRouteGoesThroughRequireProject and
// TestEveryContentRouteIsRegisteredAsContent both build their server
// from stubOptions, which passes no Markdown, so a registration gated on
// `opts.Markdown != nil` would make every route in this file invisible
// to both — which is precisely what happened to
// TestEveryMCPToolGoesThroughAddScopedTool, blind to all eleven docs
// tools until Task 10's review handed its server a markdown service.
// TestTheProseRoutesAreVisibleToTheConventionTests pins the visibility
// itself, from a stubOptions server, so re-introducing the gate fails a
// test that names this comment rather than silently blinding two others.
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
//
// It carries no `body`, on purpose. A caller that wants the markdown
// asks /docs/one (or docs.read), and answering both from one route would
// put a document's whole body on the wire twice for a page that renders
// only one of them.
//
// HTML is safe to insert as markup and that is the only reason this
// route exists: internal/markdown's renderer emits no raw HTML at all
// (goldmark without html.WithUnsafe) and rewrites every link and image
// destination whose scheme is not http, https or mailto. See
// internal/markdown/render.go's header for the full argument, and
// TestRawHTMLInABodyIsNotRendered, TestAnInlineHTMLSpanIsNotRendered,
// TestADangerousLinkSchemeIsNeutralised and
// TestAnEntityEncodedSchemeIsResolvedBeforeItIsJudged for what pins it.
type DocRenderedOutput struct {
	Path    string      `json:"path"`
	Kind    string      `json:"kind,omitempty"`
	Title   string      `json:"title"`
	Summary string      `json:"summary,omitempty"`
	Version int32       `json:"version"`
	HTML    string      `json:"html"`
	Links   []LinkedRef `json:"links"`
}

// DocComparisonOutput is the comparison view: the diff docs.diff would
// answer with, plus that diff rendered as classed lines.
//
// Unified travels beside HTML rather than being replaced by it: a
// designer copying a diff out of the page wants the text, and a client
// that wants to count changed lines should not have to parse the markup
// back apart. Coarse means the two versions were too large to compare
// line by line — see DocsDiffOutput.
type DocComparisonOutput struct {
	Path        string `json:"path"`
	FromVersion int32  `json:"from_version"`
	ToVersion   int32  `json:"to_version"`
	Unified     string `json:"unified"`
	Coarse      bool   `json:"coarse"`
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
	// Neither the both-sides refusal nor the neither-side one is decided
	// here: docsLinksList makes both, so the two surfaces cannot drift
	// into disagreeing about what "the join, from either side" means.
	out, err := docsLinksList(r.Context(), s.deps(), scope.ProjectID, DocsLinksListInput{
		Path: path, EntityType: entityType, EntityKey: entityKey,
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
//
// head_only is deliberately not accepted: a reading view of a preview is
// a page showing a designer two thirds of a scene with nothing saying
// so.
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
		Path:    doc.Path,
		Kind:    doc.Kind,
		Title:   doc.Title,
		Summary: doc.Summary,
		Version: doc.Version,
		HTML:    rendered,
		Links:   doc.Links,
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
//
// The refusal is this surface's own, and it has to be: DocsReadVersionInput.
// Version and DocsDiffInput's two are plain int32 with no omitempty, so
// the SDK's schema validator refuses an absent one on MCP before the core
// is ever called, naming the property. Reading absent as zero here
// instead would reach the domain and answer `not_found: the document at
// "x" has no version 0` — a different code, for a caller that gave no
// version at all rather than a wrong one. That is exactly the drift
// queryRelatedTo exists to prevent one surface along.
// TestTheRESTMirrorAnswersTheSameCodesAsTheTools drives all three.
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
