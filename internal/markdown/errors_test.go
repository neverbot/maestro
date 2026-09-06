package markdown_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
)

// The two typed errors this package adds are declared here and first
// used by later tasks. These tests are what keeps them from being
// write-only shapes nobody ever reads: the sentinel each satisfies and
// the payload each publishes are asserted through the exported surface,
// which is exactly what internal/web will do to them.

func TestAMissingErrorIsANotFoundThatNamesItsArgument(t *testing.T) {
	err := error(&markdown.MissingError{
		Path:    "entity_key",
		Message: `this game has no entity with key "hogger"`,
	})

	if !errors.Is(err, markdown.ErrNotFound) {
		t.Fatalf("a MissingError must match ErrNotFound, got %v", err)
	}
	if errors.Is(err, markdown.ErrInvalidInput) {
		t.Fatal("a MissingError must not match ErrInvalidInput: the recoveries differ")
	}
	if !strings.HasPrefix(err.Error(), "not_found: ") {
		t.Fatalf("Error() = %q, want it to lead with the wire code", err.Error())
	}

	var missing *markdown.MissingError
	if !errors.As(err, &missing) {
		t.Fatalf("errors.As did not recover the *MissingError from %v", err)
	}
	fields := missing.Fields()
	if len(fields) != 1 {
		t.Fatalf("Fields() = %v, want exactly one", fields)
	}
	if fields[0].Path != "entity_key" {
		t.Fatalf("Fields()[0].Path = %q, want %q", fields[0].Path, "entity_key")
	}
	if !strings.Contains(fields[0].Message, "hogger") {
		t.Fatalf("Fields()[0].Message = %q, want it to name the address that missed",
			fields[0].Message)
	}
}

func TestAConflictErrorCarriesTheCurrentDocument(t *testing.T) {
	conflict := &markdown.ConflictError{
		Current:     7,
		Include:     true,
		Title:       "Duskwood",
		BodyMD:      "The worgen came at dusk.\n",
		Frontmatter: json.RawMessage(`{"era":"third"}`),
	}

	if !errors.Is(error(conflict), markdown.ErrVersionConflict) {
		t.Fatalf("a ConflictError must match ErrVersionConflict, got %v", conflict)
	}
	if !strings.Contains(conflict.Error(), "version 7") {
		t.Fatalf("Error() = %q, want it to name the version to merge onto", conflict.Error())
	}

	details := conflict.Details()
	if details["current_version"] != int32(7) {
		t.Fatalf("details[current_version] = %#v, want int32(7)", details["current_version"])
	}
	if details["current_title"] != "Duskwood" {
		t.Fatalf("details[current_title] = %#v, want the title", details["current_title"])
	}
	if details["current_body"] != "The worgen came at dusk.\n" {
		t.Fatalf("details[current_body] = %#v, want the body byte for byte",
			details["current_body"])
	}
	if got := string(details["current_frontmatter"].(json.RawMessage)); got != `{"era":"third"}` {
		t.Fatalf("details[current_frontmatter] = %s, want the stored frontmatter", got)
	}
}

func TestAConflictErrorWithNoFrontmatterStillPublishesAnObject(t *testing.T) {
	details := (&markdown.ConflictError{Current: 1, Include: true}).Details()
	if got := string(details["current_frontmatter"].(json.RawMessage)); got != "{}" {
		t.Fatalf("details[current_frontmatter] = %s, want {}: never null, never absent", got)
	}
}

func TestAConflictErrorWithoutIncludeEchoesTheVersionAndItsAuthorAndNoProse(t *testing.T) {
	// include_current: false is what a caller writing a 200 KB document
	// sends to stop the echo, so the *prose* fields must be absent, not
	// merely empty.
	//
	// **The author and the timestamp survive it**, deliberately, and this
	// test is where that decision is pinned: they are two short strings
	// and they are the pair that decides whether the caller merges or
	// asks — a version an agent wrote two seconds ago is a retry and one
	// a designer wrote this morning is a conversation. See
	// ConflictError.Author.
	writer := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	when := time.Date(2026, 9, 2, 10, 30, 0, 0, time.UTC)
	details := (&markdown.ConflictError{
		Current:   3,
		Include:   false,
		Title:     "Duskwood",
		BodyMD:    "The worgen came at dusk.\n",
		UpdatedAt: when,
		Author: markdown.Author{
			Kind: "token", ID: &writer, Label: "the lore agent",
		},
	}).Details()

	for _, absent := range []string{"current_title", "current_body", "current_frontmatter"} {
		if _, ok := details[absent]; ok {
			t.Fatalf("details carries %s with include_current false", absent)
		}
	}
	if details["current_version"] != int32(3) {
		t.Fatalf("details[current_version] = %#v, want int32(3)", details["current_version"])
	}
	if details["current_updated_at"] != when {
		t.Fatalf("details[current_updated_at] = %#v, want %v", details["current_updated_at"], when)
	}
	if details["current_author_kind"] != "token" {
		t.Fatalf("details[current_author_kind] = %#v, want token", details["current_author_kind"])
	}
	if details["current_author_id"] != &writer {
		t.Fatalf("details[current_author_id] = %#v, want the writer", details["current_author_id"])
	}
	if details["current_author_label"] != "the lore agent" {
		t.Fatalf("details[current_author_label] = %#v, want the token's label",
			details["current_author_label"])
	}
}

// TestAConflictErrorNamesNoAuthorItCannotResolve is the other half:
// an empty label would read as somebody called nothing, and a version
// whose author is gone must carry no author keys at all rather than a
// kind naming nobody.
func TestAConflictErrorNamesNoAuthorItCannotResolve(t *testing.T) {
	details := (&markdown.ConflictError{Current: 1}).Details()
	for _, absent := range []string{
		"current_author_kind", "current_author_id", "current_author_label",
	} {
		if _, ok := details[absent]; ok {
			t.Fatalf("details carries %s for a document that records nobody", absent)
		}
	}
	nameless := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	details = (&markdown.ConflictError{
		Current: 1, Author: markdown.Author{Kind: "user", ID: &nameless},
	}).Details()
	if details["current_author_kind"] != "user" {
		t.Fatal("a document that records somebody must say so even when the name is gone")
	}
	if _, ok := details["current_author_label"]; ok {
		t.Fatal("an unresolvable author must carry no label rather than an empty one")
	}
}

// The wire codes internal/markdown builds its refusals with must be the
// metamodel's own, so a ValidationError this package makes is matched by
// the sentinel internal/web already maps.
func TestTheExportedWireCodesAreTheOnesValidationErrorMatchesOn(t *testing.T) {
	invalid := &metamodel.ValidationError{Code: metamodel.CodeInvalidInput}
	if !errors.Is(invalid, metamodel.ErrInvalidInput) {
		t.Fatalf("CodeInvalidInput = %q does not match ErrInvalidInput", metamodel.CodeInvalidInput)
	}
	violation := &metamodel.ValidationError{Code: metamodel.CodeSchemaViolation}
	if !errors.Is(violation, metamodel.ErrSchemaViolation) {
		t.Fatalf("CodeSchemaViolation = %q does not match ErrSchemaViolation",
			metamodel.CodeSchemaViolation)
	}
}
