package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
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
