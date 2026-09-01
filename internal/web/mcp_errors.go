package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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

// WithDetails returns a copy of e carrying details, rather than mutating
// e in place: several call sites (ErrScopeViolation, for one) are
// shared package-level values reused across many requests, and a copy
// keeps one caller's details from leaking onto another's error through
// the same shared pointer.
func (e *MCPError) WithDetails(details map[string]any) *MCPError {
	return &MCPError{Code: e.Code, Message: e.Message, Details: details}
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
	switch {
	case errors.Is(err, projects.ErrProjectNotFound):
		return mcpErrorResult(errCodeNotFound, "no such game", nil)
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
