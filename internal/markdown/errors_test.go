package markdown_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

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

func TestAConflictErrorWithoutIncludeEchoesOnlyTheVersion(t *testing.T) {
	// include_current: false is what a caller writing a 200 KB document
	// sends to stop the echo, so the echoed fields must be absent, not
	// merely empty.
	details := (&markdown.ConflictError{
		Current: 3,
		Include: false,
		Title:   "Duskwood",
		BodyMD:  "The worgen came at dusk.\n",
	}).Details()

	if len(details) != 1 {
		t.Fatalf("details = %#v, want the version alone", details)
	}
	if details["current_version"] != int32(3) {
		t.Fatalf("details[current_version] = %#v, want int32(3)", details["current_version"])
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
