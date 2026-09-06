package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/analysis"
	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/views"
)

// MCPError is a typed domain error that describes its own wire shape: a
// stable, machine-readable code, a human message, and an optional
// structured Details object. mcpErrorFor unwraps one with errors.As
// instead of growing a switch over sentinel errors one code at a time —
// the metamodel plan's own error vocabulary (schema_violation with a
// field path, a version conflict carrying the current version,
// endpoint_type_mismatch naming both endpoint types — see this task's
// plan corrections for where that is written down) needs a details
// payload none of Task 13's own errors carry, and a caller-supplied
// value that grows one should never require a matching case added here.
//
// A plain sentinel error (projects.ErrProjectNotFound, say) is still
// supported — mcpErrorFor falls back to a short, explicit mapping for
// those — but any new MCP-specific error a metamodel tool introduces
// should be an *MCPError, not another package-level sentinel needing its
// own case in mcpErrorFor's switch.
type MCPError struct {
	Code    string
	Message string
	Details map[string]any
}

// Error satisfies the error interface. It is not the string this task's
// tools ever put in front of an agent — mcpErrorResult's JSON body is —
// this is only what shows up in a Go stack trace or a %w-wrapped log
// line.
func (e *MCPError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// NewMCPError builds an MCPError with no details.
func NewMCPError(code, message string) *MCPError {
	return &MCPError{Code: code, Message: message}
}

// mcpErrorResult builds the wire shape for a failed tool call: a single
// JSON object with a stable "error" code, a human "message", and — when
// the domain error carried one — a structured "details" object. This is
// the exact two-or-three-field shape writeError's REST error body uses
// (auth.go), so an agent parses one error vocabulary regardless of which
// surface answered it. Returning this as CallToolResult.Content with
// IsError set, rather than simply returning a Go error from the tool
// handler, is deliberate: ToolHandlerFor packs a bare error into
// unstructured prose text with no code at all (its own doc comment says
// so) — exactly the failure mode this task's brief warned about.
//
// The one place this vocabulary does NOT reach is the SDK's own
// argument-schema validation, which runs before any tool handler is
// ever called and rejects malformed input with plain prose, no code —
// see addScopedTool's own doc comment and
// TestMCPInputValidationFailuresAreProseNotACode (mcp_http_test.go) for
// why that is a documented exception, not an oversight.
func mcpErrorResult(code, message string, details map[string]any) *mcp.CallToolResult {
	body := map[string]any{"error": code, "message": message}
	if len(details) > 0 {
		body["details"] = details
	}
	text, err := json.Marshal(body)
	if err != nil {
		// Every value above is a plain string or a map of them;
		// json.Marshal cannot fail on this input. This branch exists only
		// so a future change to this function's inputs cannot ship a
		// silently empty error body.
		text = fmt.Appendf(nil, `{"error":%q,"message":"failed to encode the error"}`, errCodeInternal)
	}
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: string(text)}},
	}
}

// mcpErrorFor maps a domain error from an MCP tool implementation to a
// stable wire code. An *MCPError (errors.As) describes its own code,
// message and details; anything else falls back to a short list of
// known sentinels, and anything not on that list becomes errCodeInternal
// — logged here, with the tool name and the caller's own identifiers,
// so "an agent got internal_error" is something an operator can actually
// go find in the logs rather than a report with nothing to search for.
// An MCP tool handler has no http.ResponseWriter-adjacent place to log
// from the way a REST handler does, so this is that path's one logging
// point; the agent itself still only ever sees the generic code, never
// the underlying error text.
func mcpErrorFor(ctx context.Context, toolName string, caller Caller, err error) *mcp.CallToolResult {
	var domainErr *MCPError
	if errors.As(err, &domainErr) {
		return mcpErrorResult(domainErr.Code, domainErr.Message, domainErr.Details)
	}
	var (
		conflict    *metamodel.VersionConflictError
		docConflict *markdown.ConflictError
		viewTimeout *views.TimeoutError
	)
	switch {
	case errors.Is(err, projects.ErrProjectNotFound):
		return mcpErrorResult(errCodeNotFound, "no such game", nil)

	// The markdown domain's conflict, above the metamodel's because it
	// is a different Go type and the arm below would not catch it: a
	// version conflict on a document would otherwise fall all the way
	// through to internal_error, which is the code meaning "give up"
	// for the one failure a re-read and a retry resolve.
	//
	// A document conflict carries the current body as well as the
	// current version, unless the caller turned the echo off, and says
	// so when the version to merge onto is a tombstone. The details
	// payload is built in the domain (ConflictError.Details) so this
	// surface and the REST one cannot disagree about what a conflict
	// carries. TestEveryMarkdownDomainErrorHasAWireCode and
	// TestAConflictCarriesTheBodyAsDataRatherThanProse pin it.
	case errors.As(err, &docConflict):
		return mcpErrorResult(errCodeVersionConflict, err.Error(), docConflict.Details())

	// The metamodel's vocabulary. Each arm is matched with errors.Is
	// against the sentinel and never by reading a code off the error
	// itself, which is the difference that matters for
	// *metamodel.ValidationError: it carries a Code field, its Is method
	// answers only for the two codes that are documented, and an
	// unrecognised one therefore matches no arm here and falls through to
	// internal_error — an honest report for a code no spec names, rather
	// than a typo quietly published as whichever code is most taught. See
	// ValidationError.Is's own doc comment, which argues that pairing
	// from the other side.
	case errors.As(err, &conflict):
		return mcpErrorResult(errCodeVersionConflict, err.Error(),
			map[string]any{"current_version": conflict.Current})
	case errors.Is(err, metamodel.ErrInvalidSchema):
		return mcpErrorResult(errCodeInvalidSchema, err.Error(), fieldDetails(err))
	case errors.Is(err, metamodel.ErrSchemaViolation):
		return mcpErrorResult(errCodeSchemaViolation, err.Error(), fieldDetails(err))
	case errors.Is(err, metamodel.ErrInvalidInput):
		return mcpErrorResult(errCodeInvalidInput, err.Error(), fieldDetails(err))
	case errors.Is(err, metamodel.ErrEndpointTypeMismatch):
		return mcpErrorResult(errCodeEndpointTypeMismatch, err.Error(), nil)
	case errors.Is(err, metamodel.ErrInUse):
		return mcpErrorResult(errCodeInUse, err.Error(), nil)
	// fieldDetails, not nil: a *markdown.MissingError names *which* of a
	// call's two addresses missed, and this arm is the only one it ever
	// reaches. Passing nil here is what made that discrimination dead
	// code — docs.links.add with a mistyped entity key answered a flat
	// not_found and an agent had to parse prose. The metamodel's plain
	// sentinels carry no field list and fieldDetails answers nil for
	// them, so nothing else on this arm changes.
	// TestANamedMissPublishesItsPath and TestAPlainNotFoundCarriesNoFieldList
	// pin both halves.
	case errors.Is(err, metamodel.ErrNotFound):
		return mcpErrorResult(errCodeNotFound, err.Error(), fieldDetails(err))

	// The views domain's four codes, each naming a different recovery —
	// internal/views/errors.go argues the set, and argues why there is no
	// fifth for a timeout. TestEveryViewsSentinelHasAWireCode drives
	// views.Sentinels() through this function rather than repeating the
	// list, so a fifth sentinel added there cannot be forgotten here.
	//
	// All four carry fieldDetails, which reads *views.QueryError's Fields
	// as JSON pointers into the query document: `/traverse/0/via/0` is
	// the whole reason this sub-project addresses itself by pointer, and
	// publishing it as details.fields[].path needs no translation — a
	// pointer is a path like any other.
	case errors.Is(err, views.ErrQueryInvalid):
		return mcpErrorResult(errCodeQueryInvalid, err.Error(), fieldDetails(err))
	case errors.Is(err, views.ErrRendererRequirements):
		return mcpErrorResult(errCodeRendererRequirements, err.Error(), fieldDetails(err))
	case errors.Is(err, views.ErrLimitExceeded):
		return mcpErrorResult(errCodeLimitExceeded, err.Error(), fieldDetails(err))
	// query_stale carries a second list beside the fields: the codes and
	// the was/now pairs a UI bands over a picture. internal/views fills
	// both from one list (QueryError.Stale) precisely so the two readers
	// do not have to share one shape.
	case errors.Is(err, views.ErrQueryStale):
		return mcpErrorResult(errCodeQueryStale, err.Error(), staleDetails(err))

	// The analysis domain's one code. TestEveryAnalysisSentinelHasAWireCode
	// drives analysis.Sentinels() through this function rather than
	// repeating the list, so a second sentinel added there cannot be
	// forgotten here — an unrecognised code matches no arm and surfaces
	// as internal_error, which tells an agent to give up on something it
	// could fix in one call.
	//
	// The details payload is the game's relation type catalogue, built
	// in the domain (analysis.UndeclaredError.Details) so this surface
	// and the REST one cannot disagree about what the refusal carries.
	// It is the whole reason this code exists: the recovery is "declare
	// something about your game's relation types" and no caller can
	// perform it without that list.
	case errors.Is(err, analysis.ErrSemanticsUndeclared):
		return mcpErrorResult(errCodeSemanticsUndeclared, err.Error(), undeclaredDetails(err))

	// **Before the retryable arm, and that ordering is the whole point.**
	// A *views.TimeoutError unwraps to the pgconn.PgError carrying 57014,
	// so metamodel.IsRetryable admits it and the arm below would catch it
	// — with the right code, and having thrown the message away. That
	// substitution is correct for a lock wait, whose recovery really is
	// "resend and change nothing", and it is exactly wrong here: this
	// error's own sentence names the budget that elapsed and the three
	// bounds to lower, which is the only thing that helps once resending
	// has stopped working. The code stays `retryable`, because the first
	// recovery is still to resend; what changes is that the second one
	// reaches the caller.
	// TestATimedOutViewKeepsItsAdviceOnBothSurfaces pins it, and is red
	// with this arm moved below the one after it.
	case errors.As(err, &viewTimeout):
		slog.WarnContext(ctx, "mcp view run exceeded its statement budget",
			"tool", toolName, "user_id", caller.UserID, "token_id", caller.TokenID, "error", err)
		return mcpErrorResult(errCodeRetryable, viewTimeout.Error(), nil)

	case metamodel.IsRetryable(err):
		// Checked before the default arm and after every mapping that
		// names something the caller sent, for the reason failureFor
		// (internal/metamodel/bulk.go) gives about the same ordering: it
		// only ever intercepts errors that were heading for
		// internal_error, which is the set it exists to rescue. The
		// database's own message is deliberately not carried through —
		// an agent acts on the code, and "canceling statement due to
		// lock timeout" describes the server's internals, not the
		// caller's next move — but it is logged, because an operator
		// seeing a run of these wants to know which lock.
		slog.WarnContext(ctx, "mcp tool call hit database contention",
			"tool", toolName, "user_id", caller.UserID, "token_id", caller.TokenID, "error", err)
		// The second sentence is review finding L3. Three of the four
		// SQLSTATEs IsRetryable admits are contention by construction;
		// 57014 is not — `query_canceled` is also what an operator's
		// statement_timeout raises on a query that is simply too
		// expensive, and that one fails every time it is run. The code
		// stays, because it still names the one recovery all four share
		// and dropping 57014 would report a lock wait that ran into
		// statement_timeout as a server fault. What was missing is the
		// next step when resending does not help, and on every tool
		// here there is one: ask for less. `entities.upsert`'s
		// description carries the write-side version (send fewer rows),
		// and each read tool's carries the read-side version.
		return mcpErrorResult(errCodeRetryable,
			"the database refused this over contention; send the same call again. "+
				"If it keeps failing, the call is too expensive as written rather than "+
				"unlucky: ask for less rather than resending it again", nil)
	default:
		slog.ErrorContext(ctx, "mcp tool call failed",
			"tool", toolName, "user_id", caller.UserID, "token_id", caller.TokenID, "error", err)
		return mcpErrorResult(errCodeInternal, "internal error", nil)
	}
}

// mcpUnauthenticated is returned by addScopedTool when it finds no
// Caller on its context. mcpHandler already refuses any request reaching
// the transport without a token caller (see its own doc comment), so
// this is unreachable in production; it exists only so a tool handler
// never trusts an absent Caller into a nil-pointer panic if that
// invariant is ever loosened.
func mcpUnauthenticated() *mcp.CallToolResult {
	return mcpErrorResult(errCodeUnauthorized, "authentication required", nil)
}

// fieldDetails pulls the per-field problems out of a domain error, so an
// agent gets the paths as data rather than only inside a sentence it
// would have to parse. The shape is {"fields": [{"path", "message"}]},
// which is the same shape invalidInput (mcp_metamodel.go) builds for a
// problem this layer diagnosed itself.
//
// An error carrying no field list — an endpoint mismatch, a not_found —
// gets no details at all rather than an empty array: "there were no
// field problems" and "this kind of error has no field problems" are
// different statements, and only the second is true here.
func fieldDetails(err error) map[string]any {
	var (
		validation *metamodel.ValidationError
		schemaErr  *metamodel.SchemaError
		missing    *markdown.MissingError
		queryErr   *views.QueryError
	)
	var problems []metamodel.FieldError
	switch {
	case errors.As(err, &validation):
		problems = validation.Fields
	case errors.As(err, &schemaErr):
		problems = schemaErr.Fields
	// A named miss publishes the argument that missed. This is the
	// discrimination the markdown spec wanted a ninth wire code
	// (`entity_not_found`) for: one flat not_found from docs.links.add
	// cannot say whether the document path or the entity key was
	// typo'd, and this one is not flat — it carries the path as data.
	// It reaches here through the not_found arm of mcpErrorFor and of
	// writeDomainError, both of which pass fieldDetails(err) rather than
	// nil for exactly this type; TestANamedMissPublishesItsPath drives
	// both of those and would fail if either went back to nil.
	case errors.As(err, &missing):
		problems = missing.Fields()
	// A query refusal's paths are JSON pointers rather than field names,
	// which is a difference in what the string says and in nothing else:
	// both surfaces publish it as details.fields[].path unchanged, and an
	// agent that can act on `name` can act on `/traverse/0/via/0`. The
	// views domain also reports a *row's* own arguments (`/name`,
	// `/positions/0/x`) as a metamodel.ValidationError, which the first
	// arm above already reads — the two conventions meet in that package
	// deliberately, and this file publishes both without flattening them
	// into one.
	case errors.As(err, &queryErr):
		problems = queryErr.Fields
	default:
		return nil
	}
	if len(problems) == 0 {
		return nil
	}
	fields := make([]map[string]string, 0, len(problems))
	for _, problem := range problems {
		fields = append(fields, map[string]string{"path": problem.Path, "message": problem.Message})
	}
	return map[string]any{"fields": fields}
}

// staleDetails is fieldDetails plus the diagnostics a stale view carries,
// under details.stale.
//
// The two lists answer different readers and are deliberately not one.
// details.fields is what an agent acts on: an addressed sentence per
// broken position, in the same shape every other refusal on this wire
// uses, so a caller with one error handler needs no second one. Under
// details.stale is the machine-readable half — a code, a pointer, and the
// was/now pair — which is what a UI bands over a picture it has decided
// to draw anyway, and what an agent uses to tell a rename it can repair
// from a type that is simply gone. internal/views fills both from one
// list, so they cannot disagree about what moved.
func staleDetails(err error) map[string]any {
	var queryErr *views.QueryError
	if !errors.As(err, &queryErr) || len(queryErr.Stale) == 0 {
		return fieldDetails(err)
	}
	details := fieldDetails(err)
	if details == nil {
		details = map[string]any{}
	}
	stale := make([]map[string]string, 0, len(queryErr.Stale))
	for _, d := range queryErr.Stale {
		entry := map[string]string{"code": d.Code, "pointer": d.Pointer}
		// was and now are omitted rather than sent empty, matching the
		// domain's own json tags: "the game says nothing" and "the game
		// says the empty string" are different statements and only the
		// first is ever true here.
		if d.Was != "" {
			entry["was"] = d.Was
		}
		if d.Now != "" {
			entry["now"] = d.Now
		}
		stale = append(stale, entry)
	}
	details["stale"] = stale
	return details
}

// undeclaredDetails is the relation type catalogue an undeclared game's
// refusal carries, or nil when the error is the bare sentinel.
//
// It is a helper rather than an `errors.As` in the arm itself for the
// reason every other arm in these two functions matches with errors.Is:
// the arm's job is to name the *code*, which the sentinel decides, and
// the payload is whatever the concrete error underneath happens to
// carry. Matching on the concrete type instead would send a bare
// analysis.ErrSemanticsUndeclared -- which is what
// TestEveryAnalysisSentinelHasAWireCode drives, and what a future
// wrapper could produce -- all the way to internal_error.
func undeclaredDetails(err error) map[string]any {
	var undeclared *analysis.UndeclaredError
	if !errors.As(err, &undeclared) {
		return nil
	}
	return undeclared.Details()
}
