package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/roles"
)

// This file is the human half of the game-content surface: the REST
// routes a browser reads and writes a game's types, entities and
// relations through. It mirrors the sixteen MCP tools
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
// /types/by-key/{key}, /types/by-id/{id}, /entities/by-key/{type}/{key},
// /entities/by-id/{id}. The discriminator sits where no key ever sits,
// so no key can collide with it — "by-key" and "by-id" are themselves
// perfectly legal keys, and
// TestARouteShapedKeyIsStillAddressable declares a type for each of the
// dangerous words and addresses it both ways. The two alternatives
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
// than cached: it is three pointer copies, and a cached copy would be a
// second place Options' services are read from.
func (s *Server) deps() MCPDeps {
	return MCPDeps{Identity: s.opts.Identity, Projects: s.opts.Projects, Metamodel: s.opts.Metamodel}
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
// states for the MCP one: an optional project_id in the body is a
// confirmation, never a selector. Present and disagreeing with the game
// in the URL, the call is refused; present and agreeing, it is accepted;
// absent, nothing happens.
//
// Silently ignoring a disagreeing project_id would be the alternative,
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
	switch statedProjectProblem(in.requestedProjectID(), scope.ProjectID) {
	case errCodeBadRequest:
		writeError(w, http.StatusBadRequest, errCodeBadRequest,
			"project_id must be a valid uuid")
		return false
	case errCodeScopeViolation:
		writeError(w, http.StatusForbidden, errCodeScopeViolation,
			"the project_id in this request names a different game than the URL")
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
	var conflict *metamodel.VersionConflictError
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
	case errors.Is(err, metamodel.ErrNotFound):
		writeCodedError(w, http.StatusNotFound, errCodeNotFound, err.Error(), nil)
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
	case errCodeInvalidInput:
		return http.StatusBadRequest
	case errCodeScopeViolation:
		return http.StatusForbidden
	case errCodeNotFound:
		return http.StatusNotFound
	case errCodeUnauthorized:
		return http.StatusUnauthorized
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
	out, err := typesRemove(r.Context(), s.deps(), caller, scope.ProjectID, TypesRemoveInput{
		ID: r.PathValue("id"), Cascade: queryBool(r, "cascade"),
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
	out, err := relationTypesRemove(r.Context(), s.deps(), caller, scope.ProjectID, RelationTypesRemoveInput{
		ID: r.PathValue("id"), Cascade: queryBool(r, "cascade"),
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
	in := EntitiesListInput{
		TypeKey: r.URL.Query().Get("type_key"),
		Cursor:  r.URL.Query().Get("cursor"),
		Limit:   limit,
		Verbose: queryBool(r, "verbose"),
	}
	invalid, ok := queryTriState(w, r, "invalid")
	if !ok {
		return
	}
	in.Invalid = invalid
	// related_to is spelled with dotted parameter names so one query
	// string can carry a nested filter without inventing an encoding:
	// related_to.relation_type_key, .entity_type_key, .entity_key,
	// .direction. Any one of them present turns the listing into the
	// traversal, and the domain refuses an incomplete one at its own
	// path — including a missing direction, which it will not default.
	if hasRelatedTo(r) {
		in.RelatedTo = &RelatedToInput{
			RelationTypeKey: r.URL.Query().Get("related_to.relation_type_key"),
			EntityTypeKey:   r.URL.Query().Get("related_to.entity_type_key"),
			EntityKey:       r.URL.Query().Get("related_to.entity_key"),
			Direction:       r.URL.Query().Get("related_to.direction"),
		}
	}
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
		EntitiesRemoveInput{ID: r.PathValue("id")})
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
	out, err := relationsList(r.Context(), s.deps(), caller, scope.ProjectID, RelationsListInput{
		TypeKey:  r.URL.Query().Get("type_key"),
		SourceID: r.URL.Query().Get("source_id"),
		TargetID: r.URL.Query().Get("target_id"),
		Cursor:   r.URL.Query().Get("cursor"),
		Limit:    limit,
	})
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
	out, err := relationsRemove(r.Context(), s.deps(), caller, scope.ProjectID,
		RelationsRemoveInput{ID: r.PathValue("id")})
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
	out, err := searchEntities(r.Context(), s.deps(), caller, scope.ProjectID, SearchInput{
		Query:   r.URL.Query().Get("query"),
		TypeKey: r.URL.Query().Get("type_key"),
		Limit:   limit,
	})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- Query-string helpers ---

// queryBool reads a flag parameter. Present with any of the four true
// spellings is true; anything else, including absent, is false. It is
// deliberately lenient where queryLimit is strict: a flag has exactly
// two meanings and an unrecognised spelling of one of them can only mean
// the other, whereas a limit of "lots" has no defensible reading at all.
// That argument only holds for a genuine two-state flag — `cascade` and
// `verbose`, its only callers. A filter whose absence is a third state
// goes through queryTriState instead.
func queryBool(r *http.Request, name string) bool {
	switch strings.ToLower(r.URL.Query().Get(name)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
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
//
// Both spellings of both sides are accepted, and anything else is the
// caller's own argument at its own path.
func queryTriState(w http.ResponseWriter, r *http.Request, name string) (*bool, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil, true
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
func queryLimit(w http.ResponseWriter, r *http.Request) (int32, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 0, true
	}
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		writeCodedError(w, http.StatusBadRequest, errCodeInvalidInput, "limit is not a number",
			map[string]any{"fields": []map[string]string{{"path": "limit", "message": "is not a number"}}})
		return 0, false
	}
	return int32(value), true
}

// hasRelatedTo reports whether the query string carries any part of the
// traversal filter. Any part, not all four: an incomplete traversal must
// reach the domain and be refused there, at the missing part's own path,
// rather than being read here as an ordinary listing — which would
// answer a caller who asked for one entity's neighbours with the whole
// game.
func hasRelatedTo(r *http.Request) bool {
	query := r.URL.Query()
	for _, name := range []string{
		"related_to.relation_type_key", "related_to.entity_type_key",
		"related_to.entity_key", "related_to.direction",
	} {
		if query.Get(name) != "" {
			return true
		}
	}
	return false
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

// RelationTypeSummary is one declared relation type and how many edges
// instance it. There is no invalid count, because an edge cannot be
// invalid — see metamodel.RelationCountsByType.
type RelationTypeSummary struct {
	RelationTypeOutput
	RelationCount int64 `json:"relation_count"`
}

// GameTotals is the whole game in three numbers.
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
	ctx := r.Context()
	entityTypes, err := s.opts.Metamodel.ListEntityTypes(ctx, scope.ProjectID)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	relationTypes, err := s.opts.Metamodel.ListRelationTypes(ctx, scope.ProjectID)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	entityCounts, err := s.opts.Metamodel.EntityCountsByType(ctx, scope.ProjectID)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	relationCounts, err := s.opts.Metamodel.RelationCountsByType(ctx, scope.ProjectID)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}

	// Both slices are made, never left nil: a nil slice marshals to JSON
	// null, and a page iterating "the types this game has" must not have
	// to tell "none" from "the server said nothing".
	out := GameSummaryOutput{
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
		count := relationCounts[row.ID]
		out.RelationTypes = append(out.RelationTypes, RelationTypeSummary{
			RelationTypeOutput: relationTypeOf(row), RelationCount: count,
		})
		out.Totals.Relations += count
	}
	// The totals are summed from the same per-type numbers the rows
	// carry, rather than read from three separate count(*) queries, so
	// the header and the table on the page can never disagree — and a
	// type with no entities contributes a zero from the map's own zero
	// value, which is the honest answer for a type nobody has filled.
	writeJSON(w, http.StatusOK, out)
}
