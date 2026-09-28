// Package markdown is Maestro's prose domain: documents addressed by
// path inside one game, versioned in full snapshots, and attached to
// zero, one or several entities.
package markdown

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/neverbot/maestro/internal/metamodel"
)

// The sentinels this package answers with. They are aliases of the
// metamodel's, not new values, so internal/web's existing mcpErrorFor
// and writeDomainError arms catch them with no change: one vocabulary,
// one set of codes, two domains.
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
func invalidInput(path, message string) error {
	return &metamodel.ValidationError{
		Code:   metamodel.CodeInvalidInput,
		Fields: []metamodel.FieldError{{Path: path, Message: message}},
	}
}

// invalidInputProblems is invalidInput for a call that found several
// problems at once, so a caller fixes all of them in one round trip instead
// of discovering them one call apart. Write is its first caller, and
// TestDocumentsArea's "every problem with one write is reported in one
// pass" case is what pins that two problems arrive as two fields of one
// refusal.
func invalidInputProblems(problems []metamodel.FieldError) error {
	return &metamodel.ValidationError{Code: metamodel.CodeInvalidInput, Fields: problems}
}

// MissingError is a not_found that names *which address* was not found.
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

// missingDocument is what every by-path miss says. It names the path back
// because that is the only part of the call a caller can compare against
// what it holds. Read and Write are its first callers (TestDocumentsArea's
// "reading another games document is not found" case and
// TestDocumentsArea's "expecting a version of a document that does not
// exist is not found" case).
func missingDocument(path string) error {
	return &MissingError{
		Path:    "path",
		Message: fmt.Sprintf("this game has no document at %q", path),
	}
}

// ConflictError is a version_conflict that carries the current document
// as well as the current version.
type ConflictError struct {
	Current     int32
	Include     bool
	Deleted     bool
	Title       string
	BodyMD      string
	Frontmatter json.RawMessage

	// Author and UpdatedAt are who wrote the version being merged onto,
	// and when.
	Author    Author
	UpdatedAt time.Time
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
	// Before the Include gate, because these two are not what
	// include_current is about: see the field comments.
	details["current_updated_at"] = e.UpdatedAt
	if e.Author.Kind != "" {
		details["current_author_kind"] = e.Author.Kind
		details["current_author_id"] = e.Author.ID
		// Present only when it resolved. An empty name would read as a
		// person called nothing, where an absent key reads as "this
		// server cannot name the author", which is the true statement.
		if e.Author.Label != "" {
			details["current_author_label"] = e.Author.Label
		}
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
