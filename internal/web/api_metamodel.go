package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/roles"
	"github.com/neverbot/maestro/internal/views"
)

// This file is the human half of the game-content surface: the REST
// routes a browser reads and writes a game's types, entities and
// relations through. It mirrors the seventeen MCP tools
// (mcp_metamodel.go), and "mirrors" is meant literally — every handler
// here decodes into that file's own input struct, calls that file's own
// unexported core, and answers with that file's own output struct. There
// is one implementation of each operation and one wire vocabulary; the
// two surfaces differ only in how a caller is admitted (see
// mcp_metamodel.go's header for that split) and in how a refusal is
// spelled: an MCP tool answers with a code inside a tool result, and
// this file answers with the same code and the same details object under
// an HTTP status.
//
// **Route shapes: a row key never shares a segment with a literal.**
// This is Task 8's first recorded decision. Row keys permit `new`,
// `index`, `id`, `null`, `games`, `types` and `search` (see
// internal/metamodel/keys.go, which recorded the collision and left the
// choice here), so /types/{key} would make a key's legality depend on
// which sibling routes happen to exist — and would silently change
// meaning the day a /types/new page is added, because Go's ServeMux
// prefers a literal segment over a wildcard without saying so. Every
// row here is therefore addressed through a fixed discriminator:
// /types/by-key/{key}, /entities/by-key/{type}/{key}. The discriminator
// sits where no key ever sits, so no key can collide with it —
// "by-key" and "by-id" are themselves perfectly legal keys, and
// TestARouteShapedKeyIsStillAddressable declares a type for each of the
// dangerous words, reads it and removes it.
//
// **Metamodel 14 removed the by-id half.** The four removals took uuids
// and now take keys, so `by-key` is the only discriminator this surface
// has; the shape it discriminates against is unchanged, and the segment
// stays because what makes it safe is that a key never sits where a
// literal could claim it. The two alternatives
// keys.go recorded — a reserved-word list in the domain, or resolving
// the ambiguity in the router — were both rejected for the same reason:
// they make a designer's vocabulary hostage to a routing table, and a
// key rule tightened after a game is seeded costs renames.
//
// **Nothing here flattens a game's fields onto a row.** That is the
// second decision. A game may declare fields named `key`, `name`, `id`,
// `version`, `invalid` or `type_key`; the database keeps them in a jsonb
// column, walled off from the row's own columns, and this surface keeps
// exactly that wall — an entity answers with its own identity at the top
// level and the game's values nested under `fields`, the same shape the
// MCP surface uses and the same shape the page renders from. So there is
// no reserved-key list, no rename forced on a game that legitimately
// calls a field `name`, and no place where lifting one namespace into
// the other could shadow the other. TestAGameFieldNamedLikeARowColumnNeverShadowsIt
// writes an entity whose every field is named after a row column and
// reads it back to prove it.

// maxContentRequestBodyBytes bounds a game-content request body.
//
// It is deliberately far larger than maxAuthRequestBodyBytes (16 KiB,
// api_auth.go), because the bodies are a different kind of thing: a
// login carries three short strings, while one entities.upsert batch is
// the unit a seed is written in — hundreds of rows, each of which may
// carry up to metamodel.MaxIndexedText of prose. 4 MiB holds a large
// batch comfortably and still bounds what a single request can make this
// process allocate. Over it, the caller is told the body was too large
// (413) rather than being handed a JSON parse error to puzzle over.
const maxContentRequestBodyBytes = 4 << 20

// deps builds the MCPDeps the shared cores take. Built per call rather
// than cached: it is four pointer copies, and a cached copy would be a
// second place Options' services are read from.
func (s *Server) deps() MCPDeps {
	return MCPDeps{Identity: s.opts.Identity, Projects: s.opts.Projects,
		Metamodel: s.opts.Metamodel, Markdown: s.opts.Markdown, Views: s.opts.Views}
}

// requireContentService refuses a game-content route on an instance
// built without a metamodel service. That shape is supported
// deliberately (Options.Metamodel), and every core below would panic on
// a nil service, so the guard is here rather than in each of them.
func (s *Server) requireContentService(w http.ResponseWriter) bool {
	if s.opts.Metamodel == nil {
		writeError(w, http.StatusNotFound, errCodeNotFound, "this instance serves no game content")
		return false
	}
	return true
}

// requireEditor gates every write on this surface. It is the one check
// the MCP surface never needed: a token is editor-equivalent by
// construction (see requireProject's own doc comment for why that is
// deliberate), but a session caller's role is real, and a viewer is
// someone who may read a game and not change it.
//
// No handler in this file calls it. registerContentRoute (server.go)
// applies it to every non-GET route on this surface, so a write is gated
// because it is a write rather than because its handler remembered — a
// review stripped this call from five of the eight handlers when they
// each made it themselves, and nothing failed.
//
// The message names the caller's actual role, because "forbidden" alone
// leaves a designer who was quietly demoted with nothing to act on.
func requireEditor(w http.ResponseWriter, scope ProjectScope) bool {
	if roles.AtLeast(roles.Role(scope.Role), roles.Editor) {
		return true
	}
	writeError(w, http.StatusForbidden, errCodeForbidden,
		"your role in this game is "+scope.Role+"; changing its content needs at least editor")
	return false
}

// decodeContentBody decodes a game-content request body under this
// file's own, larger bound. See maxContentRequestBodyBytes.
func decodeContentBody(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeJSONBodyLimit(w, r, v, maxContentRequestBodyBytes)
}

// checkStatedProject enforces, on this surface, the rule ScopedArgs
// states for the MCP one: an optional `game` in the body is a
// confirmation, never a selector. Present and disagreeing with the game
// in the URL, the call is refused; present and agreeing, it is accepted;
// absent, nothing happens.
//
// Silently ignoring a disagreeing `game` would be the alternative,
// and it is the dangerous one: a client that has lost track of which
// game it is editing would be told its write succeeded, in the other
// game, which is exactly the mistake the field exists to catch.
//
// The judgement itself is statedProjectProblem's (mcp.go), which both
// surfaces call, so "the same rule" is a shared function and not two
// switches that agreed when they were written. Only the message is this
// surface's own: the caller here holds a session, not a token, and the
// URL is what it disagreed with.
func checkStatedProject(w http.ResponseWriter, scope ProjectScope, in scopedInput) bool {
	switch statedGameProblem(in.requestedGame(), scope.Slug) {
	case errCodeBadRequest:
		writeError(w, http.StatusBadRequest, errCodeBadRequest,
			"game must name a game: pass this game's slug, or leave it out")
		return false
	case errCodeScopeViolation:
		writeError(w, http.StatusForbidden, errCodeScopeViolation,
			"the game in this request names a different game than the URL")
		return false
	}
	return true
}

// writeDomainError maps a domain error onto an HTTP status and the same
// code the MCP surface returns for it, so both surfaces are one
// contract. It is the REST twin of mcpErrorFor (mcp_errors.go), and the
// arms are deliberately in the same order and matched the same way —
// errors.As for the two typed errors, errors.Is against a sentinel for
// the rest, never by reading a code off the error itself.
//
// The statuses are the only thing this adds:
//
//   - 400 for invalid_input: the caller's own argument is malformed —
//     a cursor, a uuid, a limit, an oversized query — which is a bad
//     request in the plainest sense.
//   - 422 for invalid_schema, schema_violation and endpoint_type_mismatch:
//     the request was well formed and the content it carried was refused
//     by a rule the game itself declared.
//   - 409 for version_conflict and in_use: the caller is not wrong, the
//     world moved (or is holding on to the row).
//   - 404 for not_found, 503 for retryable, 500 for anything unmapped.
//
// Nothing an agent or a designer can fix reports internal_error; that is
// this project's standing rule and the default arm is only reached by a
// fault neither of them caused, which is why it also logs.
func (s *Server) writeDomainError(w http.ResponseWriter, r *http.Request, err error) {
	var domainErr *MCPError
	if errors.As(err, &domainErr) {
		writeCodedError(w, statusForCode(domainErr.Code), domainErr.Code, domainErr.Message, domainErr.Details)
		return
	}
	var (
		conflict    *metamodel.VersionConflictError
		docConflict *markdown.ConflictError
		viewTimeout *views.TimeoutError
	)
	switch {
	// Unreachable through a route today — requireProject resolves the
	// game before any handler here runs, so a missing game is a 404 long
	// before a domain call is made. It is here because mcpErrorFor's
	// first arm is this one, and "arm for arm" is a claim this file
	// makes about itself: a shared core that grows a project lookup of
	// its own would otherwise report a missing game as internal_error on
	// one surface and not_found on the other.
	case errors.Is(err, projects.ErrProjectNotFound):
		writeCodedError(w, http.StatusNotFound, errCodeNotFound, "no such game", nil)
	// The markdown domain's conflict, in the same position mcpErrorFor
	// puts it and for the same reason: it is a different Go type from
	// metamodel.VersionConflictError and the arm below does not catch
	// it. "Arm for arm" is a claim this file makes about itself, and
	// TestAMarkdownConflictIsAConflictOnBothSurfaces is what enforces
	// it for this pair.
	case errors.As(err, &docConflict):
		writeCodedError(w, http.StatusConflict, errCodeVersionConflict, err.Error(),
			docConflict.Details())
	case errors.As(err, &conflict):
		writeCodedError(w, http.StatusConflict, errCodeVersionConflict, err.Error(),
			map[string]any{"current_version": conflict.Current})
	case errors.Is(err, metamodel.ErrInvalidSchema):
		writeCodedError(w, http.StatusUnprocessableEntity, errCodeInvalidSchema, err.Error(), fieldDetails(err))
	case errors.Is(err, metamodel.ErrSchemaViolation):
		writeCodedError(w, http.StatusUnprocessableEntity, errCodeSchemaViolation, err.Error(), fieldDetails(err))
	case errors.Is(err, metamodel.ErrInvalidInput):
		writeCodedError(w, http.StatusBadRequest, errCodeInvalidInput, err.Error(), fieldDetails(err))
	case errors.Is(err, metamodel.ErrEndpointTypeMismatch):
		writeCodedError(w, http.StatusUnprocessableEntity, errCodeEndpointTypeMismatch, err.Error(), nil)
	case errors.Is(err, metamodel.ErrInUse):
		writeCodedError(w, http.StatusConflict, errCodeInUse, err.Error(), nil)
	// fieldDetails, not nil, for the reason mcpErrorFor's twin of this
	// arm gives: a *markdown.MissingError names which address missed and
	// this is the only arm it reaches, while the metamodel's plain
	// sentinels answer nil from fieldDetails and are unaffected.
	case errors.Is(err, metamodel.ErrNotFound):
		writeCodedError(w, http.StatusNotFound, errCodeNotFound, err.Error(), fieldDetails(err))
	// The views domain's four, arm for arm with mcpErrorFor and in the
	// same order. The statuses are the only thing this side adds:
	//
	//   - 400 for query_invalid and limit_exceeded: the document, or a
	//     bound written into it, is the caller's own malformed argument.
	//   - 422 for renderer_requirements: the request was well formed and
	//     was refused by a rule the catalogue declares, which is exactly
	//     what invalid_schema and schema_violation are 422 for.
	//   - 409 for query_stale: the caller is not wrong, the game moved
	//     under a document it saved earlier — the same statement
	//     version_conflict and in_use make.
	case errors.Is(err, views.ErrQueryInvalid):
		writeCodedError(w, http.StatusBadRequest, errCodeQueryInvalid, err.Error(), fieldDetails(err))
	case errors.Is(err, views.ErrRendererRequirements):
		writeCodedError(w, http.StatusUnprocessableEntity, errCodeRendererRequirements, err.Error(), fieldDetails(err))
	case errors.Is(err, views.ErrLimitExceeded):
		writeCodedError(w, http.StatusBadRequest, errCodeLimitExceeded, err.Error(), fieldDetails(err))
	case errors.Is(err, views.ErrQueryStale):
		writeCodedError(w, http.StatusConflict, errCodeQueryStale, err.Error(), staleDetails(err))
	// Before the retryable arm, for the reason mcpErrorFor's twin of this
	// one gives at length: the arm below drops the error's own message,
	// which is right for a lock wait and would discard the one thing a
	// timed-out view adds. A designer in a browser gets the same advice
	// an agent does, which is the claim
	// TestWriteDomainErrorIsTheRESTTwinOfMCPErrorFor makes about this
	// whole function.
	case errors.As(err, &viewTimeout):
		slog.WarnContext(r.Context(), "a view run exceeded its statement budget",
			"path", r.URL.Path, "error", err)
		writeCodedError(w, http.StatusServiceUnavailable, errCodeRetryable, viewTimeout.Error(), nil)
	case metamodel.IsRetryable(err):
		// Logged, not carried: the database's own "canceling statement
		// due to lock timeout" describes this server's internals, not
		// the caller's next move, and an operator seeing a run of these
		// wants to know which lock. Same split mcpErrorFor makes.
		slog.WarnContext(r.Context(), "game-content request hit database contention",
			"path", r.URL.Path, "error", err)
		// The second sentence is Task 7's, and it is not decoration:
		// 57014 is also what an operator's statement_timeout raises on a
		// request that is simply too expensive, and that one fails every
		// time it is resent. A browser client gets the same advice an
		// agent does — mcpErrorFor's own comment argues the case.
		writeCodedError(w, http.StatusServiceUnavailable, errCodeRetryable,
			"the database refused this over contention; send the same request again. "+
				"If it keeps failing, the request is too expensive as written rather than "+
				"unlucky: ask for less rather than resending it again", nil)
	default:
		slog.ErrorContext(r.Context(), "game-content request failed",
			"path", r.URL.Path, "error", err)
		writeCodedError(w, http.StatusInternalServerError, errCodeInternal,
			"the server could not complete the request", nil)
	}
}

// statusForCode maps an *MCPError's own code — the codes this package's
// own parsing layer produces, never the domain's — onto a status. An
// unknown code is 422 rather than 500: an *MCPError is by construction a
// refusal this server chose to make about the caller's request, so
// reporting one as a server fault would be a lie, and 422 is the
// weakest true statement available.
func statusForCode(code string) int {
	switch code {
	case errCodeInvalidInput, errCodeQueryInvalid, errCodeLimitExceeded:
		return http.StatusBadRequest
	case errCodeScopeViolation:
		return http.StatusForbidden
	case errCodeNotFound:
		return http.StatusNotFound
	case errCodeUnauthorized:
		return http.StatusUnauthorized
	// query_stale is 409 here for the same reason writeDomainError gives
	// it 409: the caller is not wrong, the game moved. It reaches this
	// switch only through an *MCPError this layer built, which the views
	// surface does not do today — the domain's own sentinel takes the arm
	// above — and the entry is here because statusForCode's default is
	// 422, and answering "the game moved" as "your request was refused"
	// would be a lie the moment this layer does build one.
	case errCodeQueryStale:
		return http.StatusConflict
	default:
		return http.StatusUnprocessableEntity
	}
}

// writeCodedError is writeError plus the optional details object the
// domain's field-path errors carry. Split out rather than folded into
// writeError because every other call site in this package has no
// details to pass and should not have to say so.
func writeCodedError(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	body := map[string]any{"error": code, "message": message}
	if len(details) > 0 {
		body["details"] = details
	}
	writeJSON(w, status, body)
}

// --- Entity types ---

func (s *Server) handleListTypes(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireContentService(w) {
		return
	}
	out, err := typesList(r.Context(), s.deps(), caller, scope.ProjectID, TypesListInput{})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUpsertType answers 200, not 201, and the reason is the domain's
// rather than a preference: this route is an upsert idempotent by key
// (metamodel.UpsertEntityType), so the same request may create a type or
// edit one, and a status claiming "created" would be wrong half the
// time. The answer carries the row's version, which is what a client
// actually needs to know what happened and what to send next.
func (s *Server) handleUpsertType(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireContentService(w) {
		return
	}
	var in TypesUpsertInput
	if !decodeContentBody(w, r, &in) || !checkStatedProject(w, scope, in) {
		return
	}
	out, err := typesUpsert(r.Context(), s.deps(), caller, scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetType(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireContentService(w) {
		return
	}
	out, err := typesGet(r.Context(), s.deps(), caller, scope.ProjectID, TypesGetInput{Key: r.PathValue("key")})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRemoveType(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireContentService(w) {
		return
	}
	cascade, ok := queryBool(w, r, "cascade")
	if !ok {
		return
	}
	out, err := typesRemove(r.Context(), s.deps(), caller, scope.ProjectID, TypesRemoveInput{
		Key: r.PathValue("key"), Cascade: cascade,
	})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- Relation types ---

func (s *Server) handleListRelationTypes(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireContentService(w) {
		return
	}
	out, err := relationTypesList(r.Context(), s.deps(), caller, scope.ProjectID, RelationTypesListInput{})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUpsertRelationType(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireContentService(w) {
		return
	}
	var in RelationTypesUpsertInput
	if !decodeContentBody(w, r, &in) || !checkStatedProject(w, scope, in) {
		return
	}
	out, err := relationTypesUpsert(r.Context(), s.deps(), caller, scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetRelationType(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireContentService(w) {
		return
	}
	out, err := relationTypesGet(r.Context(), s.deps(), caller, scope.ProjectID,
		RelationTypesGetInput{Key: r.PathValue("key")})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRemoveRelationType(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireContentService(w) {
		return
	}
	cascade, ok := queryBool(w, r, "cascade")
	if !ok {
		return
	}
	out, err := relationTypesRemove(r.Context(), s.deps(), caller, scope.ProjectID, RelationTypesRemoveInput{
		Key: r.PathValue("key"), Cascade: cascade,
	})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- Entities ---

// handleListEntities is the one handler whose input comes from the query
// string rather than a body, so it is also the one that has to decide
// what a malformed parameter means. Every one of them is the caller's
// own argument at its own path — never a 500, and never silently
// ignored: a page rendered from a filter the server quietly dropped is
// a wrong answer that looks like a right one.
func (s *Server) handleListEntities(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireContentService(w) {
		return
	}
	limit, ok := queryLimit(w, r)
	if !ok {
		return
	}
	typeKey, ok := queryString(w, r, "type_key")
	if !ok {
		return
	}
	cursor, ok := queryString(w, r, "cursor")
	if !ok {
		return
	}
	verbose, ok := queryBool(w, r, "verbose")
	if !ok {
		return
	}
	in := EntitiesListInput{TypeKey: typeKey, Cursor: cursor, Limit: limit, Verbose: verbose}
	invalid, ok := queryTriState(w, r, "invalid")
	if !ok {
		return
	}
	in.Invalid = invalid
	// The traversal filter and the completeness it is refused for both
	// live in queryRelatedTo; see its doc comment.
	relatedTo, ok := queryRelatedTo(w, r)
	if !ok {
		return
	}
	in.RelatedTo = relatedTo
	out, err := entitiesList(r.Context(), s.deps(), caller, scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUpsertEntities(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireContentService(w) {
		return
	}
	var in EntitiesUpsertInput
	if !decodeContentBody(w, r, &in) || !checkStatedProject(w, scope, in) {
		return
	}
	out, err := entitiesUpsert(r.Context(), s.deps(), caller, scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	// 200 with a report, even when some items failed: in partial mode a
	// batch that lands nineteen of twenty rows is not a failed request,
	// and the failures are in the body with their index, key and code.
	// Only a refusal of the *call* — an unknown mode, an atomic batch
	// rolled back — comes back as a status.
	writeJSON(w, http.StatusOK, out)
}

// handleRepairEntities and handleRepairRelations mirror the two repair
// tools.
//
// **POST, not PATCH**, and the route is a verb — `/entities/repair` —
// rather than a resource. Both are deliberate: a pass names no row, so
// there is no resource to PATCH, and it is not idempotent in the sense
// PUT would promise — running it twice moves a second batch of flagged
// rows, which is the point of the loop the tool description teaches. It
// takes a body rather than query parameters because `set` is a map of
// arbitrary declared values and a query string is the wrong shape for
// one.
//
// The answer is 200 with the report even when rows failed, for the
// reason handleUpsertEntities gives: a pass that repairs nineteen of
// twenty rows is not a failed request, and the twentieth is in the body
// with its key and its code. Only a refusal of the *call* — an unknown
// type, a `set` key the type does not declare, a pass stating no
// operation — is a status.
func (s *Server) handleRepairEntities(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireContentService(w) {
		return
	}
	var in EntitiesRepairInput
	if !decodeContentBody(w, r, &in) || !checkStatedProject(w, scope, in) {
		return
	}
	out, err := entitiesRepair(r.Context(), s.deps(), caller, scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRepairRelations(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireContentService(w) {
		return
	}
	var in RelationsRepairInput
	if !decodeContentBody(w, r, &in) || !checkStatedProject(w, scope, in) {
		return
	}
	out, err := relationsRepair(r.Context(), s.deps(), caller, scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetEntity(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireContentService(w) {
		return
	}
	out, err := entitiesGet(r.Context(), s.deps(), caller, scope.ProjectID, EntitiesGetInput{
		TypeKey: r.PathValue("type"), Key: r.PathValue("key"),
	})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRemoveEntity(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireContentService(w) {
		return
	}
	out, err := entitiesRemove(r.Context(), s.deps(), caller, scope.ProjectID,
		EntitiesRemoveInput{TypeKey: r.PathValue("type"), Key: r.PathValue("key")})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- Relations ---

func (s *Server) handleListRelations(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireContentService(w) {
		return
	}
	limit, ok := queryLimit(w, r)
	if !ok {
		return
	}
	verbose, ok := queryBool(w, r, "verbose")
	if !ok {
		return
	}
	in := RelationsListInput{Limit: limit, Verbose: verbose}
	var source, target RefInput
	for _, part := range []struct {
		name  string
		field *string
	}{
		{"type_key", &in.TypeKey}, {"cursor", &in.Cursor},
		{"source_type_key", &source.TypeKey}, {"source_key", &source.Key},
		{"target_type_key", &target.TypeKey}, {"target_key", &target.Key},
	} {
		value, ok := queryString(w, r, part.name)
		if !ok {
			return
		}
		*part.field = value
	}
	// An endpoint filter is present when either half of its ref is
	// written, not when both are: half a ref is a caller's mistake and
	// the domain answers it as one, at the path that is wrong. Dropping
	// it silently would answer a narrowed question with the whole
	// listing, which is this surface's own "wrong answer that looks like
	// a right one".
	if source != (RefInput{}) {
		in.Source = &source
	}
	if target != (RefInput{}) {
		in.Target = &target
	}
	// The same tri-state parse the entity listing's own invalid filter
	// takes, so `?invalid=true` means the same thing on both routes and a
	// value that is neither is refused rather than read as "no opinion".
	invalid, ok := queryTriState(w, r, "invalid")
	if !ok {
		return
	}
	in.Invalid = invalid
	out, err := relationsList(r.Context(), s.deps(), caller, scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetRelation reads one edge by its triple.
//
// The address is in the query string rather than in the path, and that
// is this file's route rule doing its job: an edge is named by five
// keys, and /relations/{type}/{source_type}/{source_key}/... would put
// five row keys in five path segments where the header's own argument
// says a key never shares a segment with anything. `/docs/one` already
// addresses a row by query string on this surface for the same reason.
//
// Every part is required, and an absent one is refused by the domain as
// invalid_input naming the part, not read as an empty key: an edge with
// four fifths of an address is not a request anyone meant.
func (s *Server) handleGetRelation(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireContentService(w) {
		return
	}
	in := RelationsGetInput{}
	for _, part := range []struct {
		name  string
		field *string
	}{
		{"type_key", &in.TypeKey},
		{"source_type_key", &in.Source.TypeKey}, {"source_key", &in.Source.Key},
		{"target_type_key", &in.Target.TypeKey}, {"target_key", &in.Target.Key},
	} {
		value, ok := queryString(w, r, part.name)
		if !ok {
			return
		}
		*part.field = value
	}
	out, err := relationsGet(r.Context(), s.deps(), caller, scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUpsertRelations(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireContentService(w) {
		return
	}
	var in RelationsUpsertInput
	if !decodeContentBody(w, r, &in) || !checkStatedProject(w, scope, in) {
		return
	}
	out, err := relationsUpsert(r.Context(), s.deps(), caller, scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRemoveRelation(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireContentService(w) {
		return
	}
	in := RelationsRemoveInput{}
	for _, part := range []struct {
		name  string
		field *string
	}{
		{"type_key", &in.TypeKey},
		{"source_type_key", &in.Source.TypeKey}, {"source_key", &in.Source.Key},
		{"target_type_key", &in.Target.TypeKey}, {"target_key", &in.Target.Key},
	} {
		value, ok := queryString(w, r, part.name)
		if !ok {
			return
		}
		*part.field = value
	}
	out, err := relationsRemove(r.Context(), s.deps(), caller, scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- Search ---

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireContentService(w) {
		return
	}
	limit, ok := queryLimit(w, r)
	if !ok {
		return
	}
	query, ok := queryString(w, r, "query")
	if !ok {
		return
	}
	typeKey, ok := queryString(w, r, "type_key")
	if !ok {
		return
	}
	// kind and doc_kind are read here and not only on the MCP side: this
	// surface mirrors that one, and a filter a person cannot spell in a
	// URL is a filter this mirror does not have.
	kind, ok := queryString(w, r, "kind")
	if !ok {
		return
	}
	docKind, ok := queryString(w, r, "doc_kind")
	if !ok {
		return
	}
	// verbose, through the same helper the two listings read it with, and
	// defaulting off exactly as they do. A search that could only be
	// asked for fields over MCP would be a mirror that answers a
	// different question from the surface it mirrors.
	verbose, ok := queryBool(w, r, "verbose")
	if !ok {
		return
	}
	out, err := searchContent(r.Context(), s.deps(), caller, scope.ProjectID, SearchInput{
		Query: query, Kind: kind, TypeKey: typeKey, DocKind: docKind,
		Limit: limit, Verbose: verbose,
	})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- Query-string helpers ---

// querySingle reads the one value of a query parameter, and is the
// single door every parameter on this surface comes in through.
//
// It enforces the half of that surface's stated rule the individual
// readers below could not see: a parameter appears at most once. Go's
// url.Values keeps every repetition and Get returns the first, so
// `?invalid=true&invalid=false` used to be answered from the first and
// the second dropped without a word — and `?invalid=true&invalid=garbage`
// answered 200 having never looked at the garbage at all, which is the
// one path on which an unrecognised spelling of `invalid` still got
// through after refusing it everywhere else.
//
// present is not the same as raw != "": `?invalid=` is a parameter the
// caller wrote and left empty, and the readers below all treat that as
// something to refuse rather than as absence. Absent is absent; written
// is written.
func querySingle(w http.ResponseWriter, r *http.Request, name string) (raw string, present, ok bool) {
	values, present := r.URL.Query()[name]
	if !present {
		return "", false, true
	}
	if len(values) > 1 {
		writeCodedError(w, http.StatusBadRequest, errCodeInvalidInput, name+" was given more than once",
			map[string]any{"fields": []map[string]string{{"path": name, "message": "was given more than once"}}})
		return "", true, false
	}
	return values[0], true, true
}

// queryPresentValue is querySingle plus the other half of the rule: a
// parameter written with no value is refused rather than read as absent.
//
// That is the same judgement checkStatedProject makes about an empty
// `game` — an empty confirmation confirms nothing — applied to the
// query string, and it holds here for the same reason. `?invalid=` used
// to answer with the whole listing, so a designer whose client dropped
// the value of the filter naming the rows that no longer fit their type
// was handed every row instead, which is this surface's own "wrong
// answer that looks like a right one". The one deliberate cost is the
// bare-flag idiom: `?verbose` and `?cascade` (which url.Values also
// present as written-and-empty) are refused rather than read as true.
// Refusing is the safe direction for a parameter one of whose callers is
// a cascading delete, and the caller is told exactly what to write.
func queryPresentValue(w http.ResponseWriter, r *http.Request, name string) (raw string, present, ok bool) {
	raw, present, ok = querySingle(w, r, name)
	if !ok || !present {
		return raw, present, ok
	}
	if raw == "" {
		writeCodedError(w, http.StatusBadRequest, errCodeInvalidInput, name+" was given with no value",
			map[string]any{"fields": []map[string]string{{"path": name, "message": "was given with no value"}}})
		return "", true, false
	}
	return raw, true, true
}

// queryString reads a plain string parameter — a key, a cursor, a search
// query. Absent is the empty string, which every listing in the domain
// reads as "do not filter on this"; written-and-empty is refused.
func queryString(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	raw, _, ok := queryPresentValue(w, r, name)
	return raw, ok
}

// queryBool reads a flag parameter. Present with any of the four true
// spellings is true; anything else is false. It is deliberately lenient
// where queryLimit is strict: a flag has exactly two meanings and an
// unrecognised spelling of one of them can only mean the other, whereas
// a limit of "lots" has no defensible reading at all. That argument only
// holds for a genuine two-state flag — `cascade` and `verbose`, its only
// callers. A filter whose absence is a third state goes through
// queryTriState instead.
//
// The leniency is about spelling and nothing else: a repeated flag and a
// flag written with no value are both refused, by queryPresentValue.
func queryBool(w http.ResponseWriter, r *http.Request, name string) (bool, bool) {
	raw, present, ok := queryPresentValue(w, r, name)
	if !ok || !present {
		return false, ok
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true, true
	default:
		return false, true
	}
}

// queryTriState reads a filter that has three states rather than two:
// absent (nil — do not filter), true, and false. It is deliberately
// strict where queryBool is lenient, and the difference is the third
// state: queryBool's justification is that a flag has exactly two
// meanings, so an unrecognised spelling of one can only mean the other,
// and that argument does not survive a filter whose absence is itself a
// meaning. `?invalid=maybe` read as false is not a near miss — it
// answers a designer asking for the rows that no longer fit their type
// with exactly the rows that do, which is the "wrong answer that looks
// like a right one" handleListEntities refuses everywhere else.
// handleListRelations takes the same filter, over the same column on the
// edge table (0009), and reads it through this same function rather than
// through a second parse — one spelling of `?invalid=` on both routes.
//
// Both spellings of both sides are accepted, and anything else is the
// caller's own argument at its own path.
func queryTriState(w http.ResponseWriter, r *http.Request, name string) (*bool, bool) {
	raw, present, ok := queryPresentValue(w, r, name)
	if !ok || !present {
		return nil, ok
	}
	var value bool
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		value = true
	case "0", "false", "no", "off":
		value = false
	default:
		writeCodedError(w, http.StatusBadRequest, errCodeInvalidInput, name+" is not true or false",
			map[string]any{"fields": []map[string]string{{"path": name, "message": "is not true or false"}}})
		return nil, false
	}
	return &value, true
}

// queryLimit reads the page limit. Absent is zero, which every listing
// in the domain reads as "the default" (metamodel.pageSize); a value
// that is not a number is the caller's own problem at path `limit`,
// never a silently ignored parameter. A number outside the domain's cap
// is *not* refused here — the domain clamps it, deliberately, and
// re-deciding that here would be a second bound to keep in step with the
// first.
//
// A number too wide for the int32 the field is gets its own answer.
// strconv reports range and syntax through the same error, so both used
// to be reported as "limit is not a number" — false for `999999999999`,
// which is a number, and unactionable, because a caller told their
// number is not one has nowhere to go from there. The clamp above is
// about the domain's page size and cannot help here: the value never
// reaches an int32 to be clamped.
func queryLimit(w http.ResponseWriter, r *http.Request) (int32, bool) {
	raw, present, ok := queryPresentValue(w, r, "limit")
	if !ok || !present {
		return 0, ok
	}
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		problem := "is not a number"
		if errors.Is(err, strconv.ErrRange) {
			problem = fmt.Sprintf("must be a whole number between %d and %d, not %s",
				math.MinInt32, math.MaxInt32, raw)
		}
		writeCodedError(w, http.StatusBadRequest, errCodeInvalidInput, "limit "+problem,
			map[string]any{"fields": []map[string]string{{"path": "limit", "message": problem}}})
		return 0, false
	}
	return int32(value), true
}

// queryVersion reads a version-shaped query parameter: absent is
// (nil, true), present and well-formed is (&v, true), present and
// malformed is a 400 at that parameter's own path.
//
// It exists rather than reusing queryLimit because the two report
// different paths and because a version is a *int32 — absent and zero
// are different things for expected_version, where zero means "create".
// Sharing queryLimit and mapping its zero would be exactly the guess the
// whole expected_version design refuses.
//
// Its only callers are the prose surface's (api_docs.go): DELETE
// /docs/one, whose method has no body to carry expected_version in, and
// requiredVersion, which adds the "absent is refused" half the three
// version arguments MCP marks `required` need.
func queryVersion(w http.ResponseWriter, r *http.Request, name string) (*int32, bool) {
	raw, present, ok := queryPresentValue(w, r, name)
	if !ok || !present {
		return nil, ok
	}
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		problem := "is not a number"
		if errors.Is(err, strconv.ErrRange) {
			problem = fmt.Sprintf("must be a whole number between %d and %d, not %s",
				math.MinInt32, math.MaxInt32, raw)
		}
		writeCodedError(w, http.StatusBadRequest, errCodeInvalidInput, name+" "+problem,
			map[string]any{"fields": []map[string]string{{"path": name, "message": problem}}})
		return nil, false
	}
	v := int32(value)
	return &v, true
}

// relatedToParts names the four query parameters that spell the one-hop
// traversal, in the order a refusal lists them. They are dotted so one
// query string can carry a nested filter without inventing an encoding.
var relatedToParts = []string{
	"related_to.relation_type_key",
	"related_to.entity_type_key",
	"related_to.entity_key",
	"related_to.direction",
}

// queryRelatedTo reads the traversal filter, or refuses an incomplete
// one naming every part that is missing.
//
// Any part present turns the listing into a traversal — any, not all
// four. Read as "all four", a query naming one part and forgetting the
// rest falls through to an ordinary listing, which answers a caller who
// asked for one entity's neighbours with every entity in the game.
//
// Completeness is decided here rather than left to the domain, and that
// is what makes this surface's parity with MCP true rather than
// asserted. RelatedToInput (mcp_metamodel.go) carries no `omitempty` on
// any of its four fields, so all four are `required` in the tool's
// served schema and the SDK's validator refuses an incomplete traversal,
// naming the absent properties, before the core is ever called. REST
// used to reach the domain instead and answer whatever its resolution
// order produced: three of the four one-part permutations at
// related_to.direction, and the fourth as `not_found: no relation type
// ""`, naming a lookup the caller never asked to make. The core is
// shared; the two surfaces were not answering the same question to it.
// They refuse the same four requests now, at the same paths.
//
// A part written with no value is present and missing both, so it is
// listed among the missing rather than refused on its own by
// queryPresentValue — one answer naming everything absent beats four
// answers naming one thing each.
func queryRelatedTo(w http.ResponseWriter, r *http.Request) (*RelatedToInput, bool) {
	values := make([]string, len(relatedToParts))
	present := false
	var missing []map[string]string
	for i, name := range relatedToParts {
		raw, given, ok := querySingle(w, r, name)
		if !ok {
			return nil, false
		}
		if given {
			present = true
		}
		if raw == "" {
			missing = append(missing, map[string]string{
				"path":    name,
				"message": "is required for a related_to traversal",
			})
			continue
		}
		values[i] = raw
	}
	if !present {
		return nil, true
	}
	if len(missing) > 0 {
		paths := make([]string, 0, len(missing))
		for _, field := range missing {
			paths = append(paths, field["path"])
		}
		writeCodedError(w, http.StatusBadRequest, errCodeInvalidInput,
			"a related_to traversal needs all four of its parts; missing "+strings.Join(paths, ", "),
			map[string]any{"fields": missing})
		return nil, false
	}
	return &RelatedToInput{
		RelationTypeKey: values[0],
		EntityTypeKey:   values[1],
		EntityKey:       values[2],
		Direction:       values[3],
	}, true
}

// --- The game home page's summary ---

// GameSummaryOutput is what a game's home page renders: the game's
// declared vocabulary with a count against each entry, and the three
// totals.
//
// It is deliberately a *catalogue and not a listing*. The one property
// this endpoint has to hold is that its answer is the same size for a
// game with four entities and a game with four hundred thousand: it
// carries one row per declared type — a handful of rows a designer wrote
// by hand — and never a row of content. A page that wants content asks
// for a page of it (GET /entities), with a cursor, like everything else
// here. TestTheGameSummaryCountsContentWithoutListingIt pins that.
//
// It has no MCP counterpart, and none is implied: an agent seeding a
// game already knows what it wrote, and every number here is one
// entities.list or relations.list away. This exists because a person
// opening a game needs to see what is in it before they can decide
// anything, which is not a need an agent has.
type GameSummaryOutput struct {
	GameCountsOutput

	// Role is the caller's own role in this game, and it is here for the
	// page's words rather than for its data. The empty state has to tell
	// a reader what to do next, and what to do next differs: an editor
	// declares the game's first type over MCP or the content routes,
	// while a viewer asking the same routes is refused. A page that tells
	// a viewer to do the one thing the server will not let them do is
	// worse than one that says nothing. requireProject has already
	// resolved the role for this request, so carrying it costs a field
	// and no query. It is never a permission — every refusal is still
	// the server's, made again on the next request.
	Role string `json:"role"`
}

// GameCountsOutput is the counted half of a game summary: one row per
// declared type with what the game holds of it, plus three totals.
//
// **It is a type of its own because it is what both surfaces answer
// with, and `role` is what only one of them has.** The REST page needs
// the caller's own role to word its empty state; an agent over MCP has a
// token, not a membership row, and a `"role": ""` would be a field that
// says nothing. GameSummaryOutput embeds this, so the page's JSON is
// byte-for-byte what it was.
//
// **Metamodel 14 put it on MCP, and nothing else changed.** Task 9's
// seeding run found that nothing on the agent surface counted: answering
// "how many races are there" was a full paged walk — five calls in the
// seeded game — while this page had the number the whole time from two
// grouped queries. The queries existed; only the exposure was missing.
type GameCountsOutput struct {
	EntityTypes   []EntityTypeSummary   `json:"entity_types"`
	RelationTypes []RelationTypeSummary `json:"relation_types"`
	Totals        GameTotals            `json:"totals"`
}

// EntityTypeSummary is one declared entity type and what the game holds
// of it. InvalidCount is the number a designer has to act on: rows a
// schema edit stopped fitting, kept and marked rather than deleted.
type EntityTypeSummary struct {
	TypeOutput
	EntityCount  int64 `json:"entity_count"`
	InvalidCount int64 `json:"invalid_count"`
}

// RelationTypeSummary is one declared relation type, how many edges
// instance it, and how many of those a schema edit stopped fitting.
//
// InvalidCount is spelled the same as EntityTypeSummary's and means the
// same thing, because since 0009 an edge is judged against its relation
// type's field schema by the same sweep an entity is judged by. This
// comment used to say an edge could not be invalid; a page that showed
// the flag on half a game's content was the read half of that gap.
type RelationTypeSummary struct {
	RelationTypeOutput
	RelationCount int64 `json:"relation_count"`
	InvalidCount  int64 `json:"invalid_count"`
}

// GameTotals is the whole game in three numbers.
//
// **Invalid counts both tables**, entities and edges together, and it did
// not before 0009 gave edges the flag. It is the "what do I have to go
// and fix" number, and the designer fixing it does not care which table a
// row lives in — a total that silently omitted every broken edge would
// send them away from a game that still had work in it.
type GameTotals struct {
	Entities  int64 `json:"entities"`
	Relations int64 `json:"relations"`
	Invalid   int64 `json:"invalid"`
}

// handleGameSummary answers the game home page. Four queries, none of
// which grows with the game's content: the two type listings and the two
// grouped counts.
func (s *Server) handleGameSummary(w http.ResponseWriter, r *http.Request, _ Caller, scope ProjectScope) {
	if !s.requireContentService(w) {
		return
	}
	counts, err := gameCounts(r.Context(), s.deps(), scope.ProjectID)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, GameSummaryOutput{GameCountsOutput: counts, Role: scope.Role})
}

// gameCounts is the four queries and the assembly both surfaces share.
//
// Four queries, none of which grows with the game's content: the two
// type listings and the two grouped counts. So a game holding four
// hundred entities answers exactly as fast, and as small, as one holding
// four, which is why this needs no page and no cursor.
//
// **Prose is deliberately not counted here.** The markdown domain is
// optional (MCPDeps.Markdown may be nil) and a document has no declared
// type to group by, so there is no row this shape could carry; more to
// the point, adding a count to one of the two surfaces this function
// serves and not the other is exactly the drift one shared assembly
// exists to prevent. docs.list answers "how much prose is under this
// path" by paging it.
func gameCounts(ctx context.Context, deps MCPDeps, projectID uuid.UUID) (GameCountsOutput, error) {
	entityTypes, err := deps.Metamodel.ListEntityTypes(ctx, projectID)
	if err != nil {
		return GameCountsOutput{}, err
	}
	relationTypes, err := deps.Metamodel.ListRelationTypes(ctx, projectID)
	if err != nil {
		return GameCountsOutput{}, err
	}
	entityCounts, err := deps.Metamodel.EntityCountsByType(ctx, projectID)
	if err != nil {
		return GameCountsOutput{}, err
	}
	relationCounts, err := deps.Metamodel.RelationCountsByType(ctx, projectID)
	if err != nil {
		return GameCountsOutput{}, err
	}
	names := make(map[uuid.UUID]string, len(entityTypes))
	for _, row := range entityTypes {
		names[row.ID] = row.Key
	}

	// Both slices are made, never left nil: a nil slice marshals to JSON
	// null, and a client iterating "the types this game has" must not
	// have to tell "none" from "the server said nothing".
	out := GameCountsOutput{
		EntityTypes:   make([]EntityTypeSummary, 0, len(entityTypes)),
		RelationTypes: make([]RelationTypeSummary, 0, len(relationTypes)),
	}
	for _, row := range entityTypes {
		counts := entityCounts[row.ID]
		out.EntityTypes = append(out.EntityTypes, EntityTypeSummary{
			TypeOutput: typeOf(row), EntityCount: counts.Total, InvalidCount: counts.Invalid,
		})
		out.Totals.Entities += counts.Total
		out.Totals.Invalid += counts.Invalid
	}
	for _, row := range relationTypes {
		counts := relationCounts[row.ID]
		out.RelationTypes = append(out.RelationTypes, RelationTypeSummary{
			RelationTypeOutput: relationTypeOf(row),
			RelationCount:      counts.Total, InvalidCount: counts.Invalid,
		})
		out.Totals.Relations += counts.Total
		out.Totals.Invalid += counts.Invalid
	}
	// The totals are summed from the same per-type numbers the rows
	// carry, rather than read from three separate count(*) queries, so
	// the header and the table on the page can never disagree — and a
	// type with no entities contributes a zero from the map's own zero
	// value, which is the honest answer for a type nobody has filled.
	return out, nil
}
