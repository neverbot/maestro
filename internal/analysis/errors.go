// Package analysis is Maestro's analysis engine: the four answers a game
// cannot get by reading — where its prerequisites loop, what no player
// can reach, what nothing points at, and whether a named progression
// still holds.
//
// It depends on internal/metamodel, acyclically, so that it reuses that
// package's FieldError, ValidationError, sentinels, Actor and key
// grammar rather than growing a drifting copy of each.
//
// **It does not depend on internal/views, nor views on it.**
// internal/graph owns the SQL primitive both compile into; policy —
// normalised dependency direction, any/all gating, containment
// propagation — lives in reach.go and is not shared.
package analysis

import (
	"errors"
	"fmt"
	"strings"

	"github.com/neverbot/maestro/internal/metamodel"
)

// CodeSemanticsUndeclared is the one wire code this sub-project adds,
// because its recovery is one no existing code names: not "change your
// argument", not "resend", but **declare something about your game's
// relation types**. It also carries a payload no existing code has a
// place for — the project's relation types with their current roles and
// traits, which is the list a caller needs to perform that recovery.
//
// A timeout here is `retryable`, whose message names the elapsed budget
// and the arguments that narrow a run; a malformed argument is
// `invalid_input`; and `query_invalid` belongs to a query document,
// which this package's flat inputs are not.
const CodeSemanticsUndeclared = "semantics_undeclared"

// ErrSemanticsUndeclared is the sentinel CodeSemanticsUndeclared names.
var ErrSemanticsUndeclared = errors.New(CodeSemanticsUndeclared)

// Sentinels lists every sentinel this package answers with, so
// internal/web's TestEveryAnalysisSentinelHasAWireCode can iterate it
// rather than repeating a list a second sentinel would silently fall out
// of. The order is the declaration order and carries no meaning.
func Sentinels() []error { return []error{ErrSemanticsUndeclared} }

// The sentinels this package reuses rather than redeclaring. They are
// aliases of the metamodel's, not new values, so internal/web's existing
// mcpErrorFor and writeDomainError arms catch them with no change — and
// so that errors.Is against either package's name is the same question.
//
// ErrLimitExceeded is the one worth naming. internal/views declared it
// first and this package needs precisely that value: "a declared limit
// is above its hard cap; lower it, the cap is in the error" is word for
// word this package's cap refusal. Two domain packages may not import
// each other, and a second errors.New("limit_exceeded") would not match
// under errors.Is — internal/web's single arm would miss it and it would
// reach an agent as internal_error. So the value moved down into the
// package both already depend on, and both alias it.
// TestLimitExceededIsOneValueAcrossBothDomains asserts the identity.
var (
	ErrNotFound        = metamodel.ErrNotFound
	ErrVersionConflict = metamodel.ErrVersionConflict
	ErrInvalidInput    = metamodel.ErrInvalidInput
	ErrInvalidSchema   = metamodel.ErrInvalidSchema
	ErrInUse           = metamodel.ErrInUse
	ErrLimitExceeded   = metamodel.ErrLimitExceeded
	// ErrActorNotInGame is not in Sentinels() and that is deliberate: it
	// is not a wire code. The actor on a write is resolved by the
	// transport from the credential the call arrived with and is never
	// caller-supplied, so an agent can do nothing about it.
	ErrActorNotInGame = metamodel.ErrActorNotInGame
)

// CodeLimitExceeded and CodeInvalidInput are aliases too, for a caller
// building one of those errors without spelling the string.
const (
	CodeLimitExceeded = metamodel.CodeLimitExceeded
	CodeInvalidInput  = metamodel.CodeInvalidInput
)

// UndeclaredError is what an analysis answers when the game it was asked
// about has told it nothing.
//
// **It is emphatically not "no problems found".** A clean bill of health
// from an engine that had nothing to read is the worst output this
// package could produce: a designer would believe their game's
// prerequisites do not loop when the truth is that no relation type
// declares itself a prerequisite, so no walk had an edge to follow.
//
// It carries the catalogue rather than pointing at it — every relation
// type of the game with its current role and traits — because the
// recovery is "declare something about your game's relation types" and
// the caller cannot perform it without the list in front of it. That
// payload is the reason this error earns a code of its own: no existing
// code has a place to put it.
type UndeclaredError struct {
	// Types is every relation type the game has, in the order the
	// catalogue returned them, each with whatever it currently declares.
	Types []TypeReport
	// Advice is one line of what to declare, generated from the
	// vocabulary rather than written out, so it cannot promise a word
	// the column would refuse.
	Advice string
}

// TypeReport is one relation type as an UndeclaredError names it: what a
// caller would have to edit.
type TypeReport struct {
	Key          string   `json:"key"`
	SemanticRole string   `json:"semantic_role,omitempty"`
	Traits       []string `json:"analysis_traits,omitempty"`
}

func (e *UndeclaredError) Error() string {
	if len(e.Types) == 0 {
		return fmt.Sprintf("%s: this game has no relation types at all, so there are no "+
			"edges to analyse. %s", CodeSemanticsUndeclared, e.Advice)
	}
	keys := make([]string, 0, len(e.Types))
	for _, typ := range e.Types {
		keys = append(keys, typ.Key)
	}
	return fmt.Sprintf("%s: no relation type of this game declares analysis_traits, and "+
		"none carries a semantic_role an analysis can derive them from, so this engine "+
		"has nothing to walk and would answer that nothing is wrong. Its relation types "+
		"are %s. %s",
		CodeSemanticsUndeclared, metamodel.QuotedList(keys), e.Advice)
}

// Is makes errors.Is(err, ErrSemanticsUndeclared) true.
func (e *UndeclaredError) Is(target error) bool { return target == ErrSemanticsUndeclared }

// Details is the structured payload internal/web publishes beside the
// code, built in the domain so the MCP surface and the REST mirror
// cannot disagree about what this refusal carries — the shape
// markdown.ConflictError.Details established and this follows.
func (e *UndeclaredError) Details() map[string]any {
	types := make([]map[string]any, 0, len(e.Types))
	for _, typ := range e.Types {
		entry := map[string]any{"key": typ.Key}
		if typ.SemanticRole != "" {
			entry["semantic_role"] = typ.SemanticRole
		}
		if len(typ.Traits) > 0 {
			entry["analysis_traits"] = typ.Traits
		}
		types = append(types, entry)
	}
	return map[string]any{"relation_types": types, "advice": e.Advice}
}

// undeclaredAdvice is the one line of recovery every UndeclaredError
// carries, generated from the vocabulary and the derivation mapping so
// that a trait added to either reaches a caller without a second edit.
func undeclaredAdvice() string {
	roles := make([]string, 0, len(derivedTraits))
	for _, role := range metamodel.SemanticRoles {
		if _, ok := derivedTraits[role]; ok {
			roles = append(roles, role)
		}
	}
	return fmt.Sprintf(
		"Declare analysis_traits on the relation types that gate progression — "+
			"relation_types.upsert takes them, from %s — or set a semantic_role of %s, "+
			"which this engine translates into traits for a game seeded before traits "+
			"existed.",
		metamodel.QuotedList(metamodel.AnalysisTraits), metamodel.QuotedList(roles))
}

// limitExceeded refuses a caller's declared bound, naming the cap.
//
// **It refuses; it does not clamp.** bounds.go argues the split and this
// is the one constructor that enforces it, so a later task cannot quietly
// introduce a clamp for one argument: there is nowhere to put it.
func limitExceeded(path string, asked, cap int) error {
	return &metamodel.ValidationError{
		Code: CodeLimitExceeded,
		Fields: []metamodel.FieldError{{
			Path: path,
			Message: fmt.Sprintf(
				"%d is above the cap of %d; lower it. It is refused rather than trimmed "+
					"to the cap because an answer computed under a bound you did not ask "+
					"for reads exactly like a complete one", asked, cap),
		}},
	}
}

// invalidInput is the one constructor every caller-argument refusal in
// this package goes through, so a future refusal cannot reach a caller as
// a bare error and therefore as internal_error — the failure mode
// metamodel.searchQueryProblem's own comment records.
func invalidInput(path, message string) error {
	return &metamodel.ValidationError{
		Code:   CodeInvalidInput,
		Fields: []metamodel.FieldError{{Path: path, Message: message}},
	}
}

// unknownField turns encoding/json's DisallowUnknownFields error into a
// refusal a caller can act on: the member it did not recognise, at the
// argument object's own path.
//
// The message is parsed rather than reproduced because encoding/json
// gives the field name in no other way. If the parse fails the whole
// sentence is passed through, which is worse to read and still correct —
// silently answering as though the argument had been understood is the
// one behaviour this must never fall back to.
func unknownField(err error) error {
	const marker = `unknown field `
	message := err.Error()
	if i := strings.Index(message, marker); i >= 0 {
		name := strings.Trim(message[i+len(marker):], `"`)
		return invalidInput(name, fmt.Sprintf(
			"is not an argument of this call. It is refused rather than ignored: a "+
				"misspelled seed list read as no seed list at all would answer that your "+
				"whole game is unreachable"))
	}
	return invalidInput("", message)
}
