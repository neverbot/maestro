// Package markdown is Maestro's prose domain: documents addressed by
// path inside one game, versioned in full snapshots, and attached to
// zero, one or several entities.
//
// It depends on internal/metamodel deliberately. A document links to
// entities, so the dependency is real, and it is acyclic — nothing in
// the metamodel mentions documents. Taking it means this package reuses
// that one's FieldError, ValidationError, sentinels and wire codes
// rather than growing a second, drifting copy of each.
package markdown

import (
	"encoding/json"
	"fmt"

	"github.com/neverbot/maestro/internal/metamodel"
)

// The sentinels this package answers with. They are aliases of the
// metamodel's, not new values, so internal/web's existing mcpErrorFor
// and writeDomainError arms catch them with no change: one vocabulary,
// one set of codes, two domains.
//
// ErrInUse is deliberately absent even though the spec lists it among
// the reused shapes. Nothing here is ever refused for being in use:
// deletion is soft, and a link disappears with its entity by cascade.
// Declaring a sentinel no path returns would be a claim the code does
// not make.
var (
	ErrNotFound        = metamodel.ErrNotFound
	ErrVersionConflict = metamodel.ErrVersionConflict
	ErrInvalidInput    = metamodel.ErrInvalidInput
	// ErrActorNotInGame is not in the wire vocabulary and that is
	// deliberate, exactly as it is in internal/views: the actor on a
	// write is resolved by the transport from the credential the call
	// arrived with and is never caller-supplied, so an agent can do
	// nothing about it and internal_error is the honest report. What it
	// buys is the *server's* side of the story — a token scoped to
	// another game, named as such, instead of SQLSTATE 23503 over
	// document_versions_author_token_id_project_id_fkey.
	ErrActorNotInGame = metamodel.ErrActorNotInGame
)

// invalidInput is the one shape every refusal of a caller's argument
// takes in this package: invalid_input at the argument's own path.
//
// Giving them one constructor is what keeps a future refusal from being
// reported as a server fault — the failure mode
// metamodel.searchQueryProblem's own comment records, where three
// untyped refusals reached an agent as internal_error over values the
// agent itself supplied.
func invalidInput(path, message string) error {
	return &metamodel.ValidationError{
		Code:   metamodel.CodeInvalidInput,
		Fields: []metamodel.FieldError{{Path: path, Message: message}},
	}
}

// invalidInputProblems is invalidInput for a call that found several
// problems at once, so a caller fixes all of them in one round trip
// instead of discovering them one call apart. Write is its first caller,
// and TestEveryProblemWithOneWriteIsReportedInOnePass is what pins that
// two problems arrive as two fields of one refusal.
func invalidInputProblems(problems []metamodel.FieldError) error {
	return &metamodel.ValidationError{Code: metamodel.CodeInvalidInput, Fields: problems}
}

// MissingError is a not_found that names *which address* was not found.
//
// The spec proposed a separate `entity_not_found` wire code for this,
// arguing that one flat not_found from docs.links.add cannot tell an
// agent which of the document path and the entity key it typo'd. The
// argument is right about a *flat* one. This is not flat: it carries the
// argument's own path, and an agent reads which address failed as data
// rather than parsing prose. So the discrimination ships and the ninth
// wire code does not — a code exists to name a recovery, and both of
// these have the same recovery, at two different arguments.
//
// internal/web's fieldDetails publishes it as details.fields on both
// surfaces (TestANamedMissPublishesItsPath, over there); what is pinned
// here is the type, its sentinel and its Fields accessor
// (TestAMissingErrorIsANotFoundThatNamesItsArgument).
type MissingError struct {
	Path    string
	Message string
}

func (e *MissingError) Error() string { return "not_found: " + e.Message }

// Is makes errors.Is(err, ErrNotFound) true, so every existing
// not_found arm in internal/web catches this without knowing the type.
func (e *MissingError) Is(target error) bool { return target == metamodel.ErrNotFound }

// Fields is what internal/web's fieldDetails reads to publish the path.
func (e *MissingError) Fields() []metamodel.FieldError {
	return []metamodel.FieldError{{Path: e.Path, Message: e.Message}}
}

// missingDocument is what every by-path miss says. It names the path
// back because that is the only part of the call a caller can compare
// against what it holds. Read and Write are its first callers
// (TestReadingAnotherGamesDocumentIsNotFound and
// TestExpectingAVersionOfADocumentThatDoesNotExistIsNotFound).
func missingDocument(path string) error {
	return &MissingError{
		Path:    "path",
		Message: fmt.Sprintf("this game has no document at %q", path),
	}
}

// ConflictError is a version_conflict that carries the current document
// as well as the current version.
//
// **This is the one wire shape this domain adds, and it is a superset
// rather than a new code** (spec §4). For an entity a conflicting caller
// can re-fetch cheaply and merge field by field; for prose it needs the
// text to merge at all, and forcing a second round trip on every
// conflict spends an agent's turn and its context window for nothing.
// Include is the docs.write `include_current` argument, defaulting to
// true, so a caller writing a 200 KB document can turn the echo off.
//
// It is a different Go type from metamodel.VersionConflictError, which
// means **internal/web needs an arm of its own for it in both
// mcpErrorFor and writeDomainError** — landed in Task 10, and the
// reason TestEveryMarkdownDomainErrorHasAWireCode exists there. Without
// that arm this lands on the default arm and an agent is told
// internal_error over a conflict it could have merged, which is
// precisely the class of defect this plan's header names. What is
// pinned here is the sentinel it satisfies and the payload Details
// builds (TestAConflictErrorCarriesTheCurrentDocument).
//
// **Deleted is a fourth thing a conflict can be about**, added by Task 4
// rather than inherited: the version a caller is told to merge onto may
// be a tombstone. The default message sends that caller to re-read the
// document, and a re-read of a deleted document answers not_found —
// two refusals with nothing connecting them, from one call. Saying so
// costs a bool and turns a dead end into an instruction, because
// resurrection is exactly the same call the caller was already making.
// TestAStaleVersionCannotSilentlyResurrectADocument pins it and
// TestAnOrdinaryConflictDoesNotClaimTheDocumentWasDeleted pins that a
// live conflict says nothing about deletion.
type ConflictError struct {
	Current     int32
	Include     bool
	Deleted     bool
	Title       string
	BodyMD      string
	Frontmatter json.RawMessage
}

func (e *ConflictError) Error() string {
	if e.Deleted {
		return fmt.Sprintf("version_conflict: this document was deleted at version %d; "+
			"write to the path with that expected_version to bring it back", e.Current)
	}
	return fmt.Sprintf("version_conflict: this document is at version %d; "+
		"merge onto it and write again", e.Current)
}

// Is makes errors.Is(err, ErrVersionConflict) true.
func (e *ConflictError) Is(target error) bool { return target == metamodel.ErrVersionConflict }

// Details is the structured payload internal/web publishes under
// "details". It is built here, in the domain, so the two surfaces cannot
// disagree about what a conflict carries.
func (e *ConflictError) Details() map[string]any {
	details := map[string]any{"current_version": e.Current}
	// Present only when true. An absent key and a false one read the
	// same to a caller that checks for truth, and the absent one does
	// not invite a reader of an ordinary conflict to wonder what
	// deletion has to do with it.
	if e.Deleted {
		details["deleted"] = true
	}
	if !e.Include {
		return details
	}
	details["current_title"] = e.Title
	details["current_body"] = e.BodyMD
	frontmatter := e.Frontmatter
	if len(frontmatter) == 0 {
		frontmatter = json.RawMessage(`{}`)
	}
	details["current_frontmatter"] = frontmatter
	return details
}
