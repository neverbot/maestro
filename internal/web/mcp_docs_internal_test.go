package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/markdown"
)

// decodeMCPError pulls the JSON body out of a tool result, which is the
// only thing an agent ever sees of a failure.
func decodeMCPError(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] = %T, want *mcp.TextContent", result.Content[0])
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(text.Text), &body); err != nil {
		t.Fatalf("decode the error body %q: %v", text.Text, err)
	}
	return body
}

// TestEveryMarkdownDomainErrorHasAWireCode drives one of each error the
// markdown domain can produce through mcpErrorFor and fails on
// internal_error.
//
// It exists because nothing a caller can fix may report internal_error,
// and because the failure is silent: an unmapped error type falls
// through mcpErrorFor's default arm, logs, and hands an agent "the
// server broke" over a value the agent itself sent. markdown.
// ConflictError is a different Go type from metamodel.VersionConflictError,
// so the existing errors.As arm does not catch it, and a version
// conflict — the one failure a re-read and a retry resolve — would
// otherwise arrive as the code meaning "give up".
func TestEveryMarkdownDomainErrorHasAWireCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"a bad path", markdown.CheckPath(""), errCodeInvalidInput},
		{"a missing document", &markdown.MissingError{Path: "path", Message: "no such document"},
			errCodeNotFound},
		{"a missing link endpoint",
			&markdown.MissingError{Path: "entity_key", Message: "no such quest"}, errCodeNotFound},
		{"a version conflict",
			&markdown.ConflictError{Current: 3, Include: true, BodyMD: "body"},
			errCodeVersionConflict},
		{"a version conflict with the echo off",
			&markdown.ConflictError{Current: 3}, errCodeVersionConflict},
		{"a conflict on a deleted document",
			&markdown.ConflictError{Current: 4, Deleted: true}, errCodeVersionConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil {
				t.Fatal("the case builds no error, so it asserts nothing")
			}
			body := decodeMCPError(t, mcpErrorFor(context.Background(), "docs.write", Caller{}, tc.err))
			if body["error"] != tc.want {
				t.Fatalf("error = %v, want %v (body %v)", body["error"], tc.want, body)
			}
			if body["error"] == errCodeInternal {
				t.Fatal("nothing a caller can fix may report internal_error")
			}
		})
	}
}

// TestAMarkdownConflictIsAConflictOnBothSurfaces is the REST half of the
// arm above. writeDomainError claims arm for arm to be mcpErrorFor's
// twin, and a conflict this domain produces has to reach a designer in a
// browser as 409 version_conflict, not 500.
func TestAMarkdownConflictIsAConflictOnBothSurfaces(t *testing.T) {
	srv := NewServer(stubOptions("test"))
	rec := httptest.NewRecorder()
	srv.writeDomainError(rec, httptest.NewRequest(http.MethodPut, "/api/games/x/documents/lore", nil),
		&markdown.ConflictError{Current: 3, Include: true, Title: "T", BodyMD: "two\n"})

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error   string         `json:"error"`
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if body.Error != errCodeVersionConflict {
		t.Fatalf("error = %q, want %q", body.Error, errCodeVersionConflict)
	}
	if body.Details["current_body"] != "two\n" {
		t.Fatalf("details = %v, want the current body", body.Details)
	}
}

// TestAConflictCarriesTheBodyAsDataRatherThanProse asserts the details
// payload, because an agent merging prose needs the text as a field and
// not inside a sentence.
func TestAConflictCarriesTheBodyAsDataRatherThanProse(t *testing.T) {
	body := decodeMCPError(t, mcpErrorFor(context.Background(), "docs.write", Caller{},
		&markdown.ConflictError{Current: 3, Include: true, Title: "T", BodyMD: "two\n"}))
	details, ok := body["details"].(map[string]any)
	if !ok {
		t.Fatalf("no details in %v", body)
	}
	if details["current_body"] != "two\n" || details["current_version"] != float64(3) {
		t.Fatalf("details = %v, want the current version and body", details)
	}
	if _, present := details["deleted"]; present {
		t.Fatalf("details = %v, want no deleted key on a live conflict", details)
	}
}

// TestAConflictOnADeletedDocumentSaysSoOnTheWire pins the fourth thing a
// conflict can be about (markdown.ConflictError's own doc comment): the
// version to merge onto is a tombstone, and writing to the path with
// that expected_version brings the document back. An agent told only
// "version 4" would re-read, get not_found, and have two refusals with
// nothing connecting them.
func TestAConflictOnADeletedDocumentSaysSoOnTheWire(t *testing.T) {
	body := decodeMCPError(t, mcpErrorFor(context.Background(), "docs.write", Caller{},
		&markdown.ConflictError{Current: 4, Deleted: true}))
	details, ok := body["details"].(map[string]any)
	if !ok {
		t.Fatalf("no details in %v", body)
	}
	if details["deleted"] != true {
		t.Fatalf("details = %v, want deleted true", details)
	}
	message, _ := body["message"].(string)
	if !strings.Contains(message, "bring it back") {
		t.Fatalf("message = %q, want the resurrection instruction", message)
	}
}

// TestANamedMissPublishesItsPath asserts the discrimination the spec
// wanted a separate `entity_not_found` code for arrives as data.
func TestANamedMissPublishesItsPath(t *testing.T) {
	details := fieldDetails(&markdown.MissingError{Path: "entity_key", Message: "no such quest"})
	fields, ok := details["fields"].([]map[string]string)
	if !ok || len(fields) != 1 || fields[0]["path"] != "entity_key" {
		t.Fatalf("details = %v, want one field problem at entity_key", details)
	}
}

// TestOmittingLinksAndSendingAnEmptyArrayAreDifferentOnThisType re-pins,
// against the real type, the claim internal/markdown could only pin
// against a stand-in: TestOmittingLinksAndSendingAnEmptyArrayAreDifferent
// OnTheWire (internal/markdown/links_test.go) decodes into a struct
// declared in its own file, because DocsWriteInput did not exist when it
// was written, and its own comment says its claim must be re-pinned
// here.
//
// The three states have to survive JSON in both directions: omitted and
// an explicit null preserve the document's attachments, an empty array
// detaches everything, and a populated one replaces the set. `omitempty`
// on a plain slice would collapse the empty case back into the omitted
// one, which is the silent, destructive direction — and it is exactly
// how `kind` broke once already, as a plain string with omitempty.
func TestOmittingLinksAndSendingAnEmptyArrayAreDifferentOnThisType(t *testing.T) {
	// The declaration itself, so a later edit cannot quietly drop the
	// pointer or the tag and leave the decode cases passing for a
	// different reason.
	field, ok := reflect.TypeOf(DocsWriteInput{}).FieldByName("Links")
	if !ok {
		t.Fatal("DocsWriteInput has no Links field at all")
	}
	if got := field.Type.String(); got != "*[]web.DocsLinkInput" {
		t.Fatalf("Links is %s, want *[]web.DocsLinkInput", got)
	}
	if got := field.Tag.Get("json"); got != "links,omitempty" {
		t.Fatalf(`Links is tagged %q, want "links,omitempty"`, got)
	}

	for _, tc := range []struct {
		name    string
		body    string
		nil     bool
		targets int
	}{
		{"omitted", `{"path":"a.md"}`, true, 0},
		{"null", `{"path":"a.md","links":null}`, true, 0},
		{"empty", `{"path":"a.md","links":[]}`, false, 0},
		{"populated", `{"path":"a.md","links":[{"entity_type":"quest","entity_key":"k"}]}`, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var in DocsWriteInput
			if err := json.Unmarshal([]byte(tc.body), &in); err != nil {
				t.Fatalf("decode %s: %v", tc.body, err)
			}
			if (in.Links == nil) != tc.nil {
				t.Fatalf("%s decoded to Links %v, want nil = %v", tc.body, in.Links, tc.nil)
			}
			if in.Links != nil && len(*in.Links) != tc.targets {
				t.Fatalf("%s decoded to %d targets, want %d", tc.body, len(*in.Links), tc.targets)
			}
			// And what the domain is handed for each: nil preserves,
			// non-nil replaces.
			targets := linkTargetsOf(in.Links)
			if (targets == nil) != tc.nil {
				t.Fatalf("linkTargetsOf gave %v for %s, want nil = %v", targets, tc.body, tc.nil)
			}

			// Out again, so an empty array does not re-encode as an
			// omitted field.
			raw, err := json.Marshal(in)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			hasKey := strings.Contains(string(raw), `"links"`)
			if hasKey == tc.nil {
				t.Fatalf("re-encoded as %s; a nil Links must omit the key and a "+
					"non-nil one must carry it", raw)
			}
		})
	}
}

// TestTheRemoveToolsInputCarriesNoRole pins the decision Task 7 made in
// the domain — markdown.LinkRemove takes an UnlinkInput, which cannot
// carry a role — on the wire, where a caller actually reads it. A link's
// key is (document, entity), so there is never a second link under
// another role to choose between, and an input schema still asking for a
// role would tell a caller otherwise with nothing to correct the belief.
// docs.links.add keeps its role, and the two are not one shared shape.
func TestTheRemoveToolsInputCarriesNoRole(t *testing.T) {
	if _, ok := reflect.TypeOf(DocsLinkRemoveInput{}).FieldByName("Role"); ok {
		t.Fatal("DocsLinkRemoveInput carries a Role that markdown.LinkRemove cannot read")
	}
	for _, name := range jsonKeysOf(reflect.TypeOf(DocsLinkRemoveInput{})) {
		if name == "role" {
			t.Fatal("docs.links.remove's input schema asks for a role it ignores")
		}
	}
	if _, ok := reflect.TypeOf(DocsLinkAddInput{}).FieldByName("Role"); !ok {
		t.Fatal("DocsLinkAddInput lost its Role, which markdown.LinkAdd does read")
	}
}

// jsonKeysOf lists the wire names of a struct's own fields.
func jsonKeysOf(t reflect.Type) []string {
	keys := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		tag := t.Field(i).Tag.Get("json")
		if name, _, _ := strings.Cut(tag, ","); name != "" {
			keys = append(keys, name)
		}
	}
	return keys
}
