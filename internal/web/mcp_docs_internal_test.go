package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
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
func TestEveryMarkdownDomainErrorHasAWireCode(t *testing.T) {
	t.Parallel()
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
			assert.Must(t, tc.err != nil, "the case builds no error, so it asserts nothing")
			body := decodeMCPError(t, mcpErrorFor(context.Background(), "docs.write", Caller{}, tc.err))
			if body["error"] != tc.want {
				t.Fatalf("error = %v, want %v (body %v)", body["error"], tc.want, body)
			}
			assert.Must(t, body["error"] != errCodeInternal, "nothing a caller can fix may report internal_error")
		})
	}
}

// TestAMarkdownConflictIsAConflictOnBothSurfaces is the REST half of the
// arm above. writeDomainError claims arm for arm to be mcpErrorFor's
// twin, and a conflict this domain produces has to reach a designer in a
// browser as 409 version_conflict, not 500.
func TestAMarkdownConflictIsAConflictOnBothSurfaces(t *testing.T) {
	t.Parallel()
	srv := NewServer(stubOptions("test"))
	rec := httptest.NewRecorder()
	srv.writeDomainError(rec, httptest.NewRequest(http.MethodPut, "/api/games/x/documents/lore", nil),
		&markdown.ConflictError{Current: 3, Include: true, Title: "T", BodyMD: "two\n"})

	assert.Must(t, rec.Code == http.StatusConflict, "status = %d, want 409: %s", rec.Code, rec.Body.String())
	var body struct {
		Error   string         `json:"error"`
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	assert.Must(t, body.Error == errCodeVersionConflict, "error = %q, want %q", body.Error, errCodeVersionConflict)
	assert.Must(t, body.Details["current_body"] == "two\n", "details = %v, want the current body", body.Details)
}

// TestAConflictCarriesTheBodyAsDataRatherThanProse asserts the details
// payload, because an agent merging prose needs the text as a field and
// not inside a sentence.
func TestAConflictCarriesTheBodyAsDataRatherThanProse(t *testing.T) {
	t.Parallel()
	body := decodeMCPError(t, mcpErrorFor(context.Background(), "docs.write", Caller{},
		&markdown.ConflictError{Current: 3, Include: true, Title: "T", BodyMD: "two\n"}))
	details, ok := body["details"].(map[string]any)
	assert.Must(t, ok, "no details in %v", body)
	assert.Must(t, details["current_body"] == "two\n" && details["current_version"] == float64(3), "details = %v, want the current version and body", details)
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
	t.Parallel()
	body := decodeMCPError(t, mcpErrorFor(context.Background(), "docs.write", Caller{},
		&markdown.ConflictError{Current: 4, Deleted: true}))
	details, ok := body["details"].(map[string]any)
	assert.Must(t, ok, "no details in %v", body)
	assert.Must(t, details["deleted"] == true, "details = %v, want deleted true", details)
	message, _ := body["message"].(string)
	assert.Must(t, strings.Contains(message, "bring it back"), "message = %q, want the resurrection instruction", message)
}

// TestANamedMissPublishesItsPath asserts the discrimination the spec
// wanted a separate `entity_not_found` code for arrives as data, **on
// both surfaces and through the mappers a real call goes through**.
func TestANamedMissPublishesItsPath(t *testing.T) {
	t.Parallel()
	missing := &markdown.MissingError{Path: "entity_key", Message: "no such quest"}

	body := decodeMCPError(t, mcpErrorFor(context.Background(), "docs.links.add", Caller{}, missing))
	if body["error"] != errCodeNotFound {
		t.Fatalf("error = %v, want %v", body["error"], errCodeNotFound)
	}
	details, ok := body["details"].(map[string]any)
	assert.Must(t, ok, "no details in %v: a named miss must publish which address missed", body)
	fields, ok := details["fields"].([]any)
	assert.Must(t, ok && len(fields) == 1, "details = %v, want one field problem", details)
	field, _ := fields[0].(map[string]any)
	assert.Must(t, field["path"] == "entity_key", "field = %v, want the path entity_key", field)

	srv := NewServer(stubOptions("test"))
	rec := httptest.NewRecorder()
	srv.writeDomainError(rec,
		httptest.NewRequest(http.MethodPost, "/api/games/x/documents/lore/links", nil), missing)
	assert.Must(t, rec.Code == http.StatusNotFound, "status = %d, want 404: %s", rec.Code, rec.Body.String())
	var rest struct {
		Error   string         `json:"error"`
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rest); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	restFields, ok := rest.Details["fields"].([]any)
	assert.Must(t, ok && len(restFields) == 1, "details = %v, want one field problem on the REST surface", rest.Details)
	restField, _ := restFields[0].(map[string]any)
	assert.Must(t, restField["path"] == "entity_key", "field = %v, want the path entity_key", restField)
}

// TestAPlainNotFoundCarriesNoFieldList is the other half of the arm
// above: passing fieldDetails into the not_found arms must not invent an
// empty "fields" on the metamodel's plain sentinels, which name no
// argument at all. "There were no field problems" and "this kind of
// error has no field problems" are different statements.
func TestAPlainNotFoundCarriesNoFieldList(t *testing.T) {
	t.Parallel()
	body := decodeMCPError(t, mcpErrorFor(context.Background(), "entities.get", Caller{},
		fmt.Errorf("no such entity: %w", metamodel.ErrNotFound)))
	if body["error"] != errCodeNotFound {
		t.Fatalf("error = %v, want %v", body["error"], errCodeNotFound)
	}
	if _, present := body["details"]; present {
		t.Fatalf("body = %v, want no details on a sentinel that names no argument", body)
	}
}

// TestOmittingLinksAndSendingAnEmptyArrayAreDifferentOnThisType re-pins,
// against the real type, the claim internal/markdown could only pin against
// a stand-in: TestLinksArea's "omitting links and sending an empty array
// are different on the wire" case (internal/markdown/links_test.go) decodes
// into a struct declared in its own file, because DocsWriteInput did not
// exist when it was written, and its own comment says its claim must be
// re-pinned here.
func TestOmittingLinksAndSendingAnEmptyArrayAreDifferentOnThisType(t *testing.T) {
	t.Parallel()
	// The declaration itself, so a later edit cannot quietly drop the
	// pointer or the tag and leave the decode cases passing for a
	// different reason.
	field, ok := reflect.TypeOf(DocsWriteInput{}).FieldByName("Links")
	assert.Must(t, ok, "DocsWriteInput has no Links field at all")
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
			assert.Must(t, (in.Links == nil) == tc.nil, "%s decoded to Links %v, want nil = %v", tc.body, in.Links, tc.nil)
			if in.Links != nil && len(*in.Links) != tc.targets {
				t.Fatalf("%s decoded to %d targets, want %d", tc.body, len(*in.Links), tc.targets)
			}
			// And what the domain is handed for each: nil preserves,
			// non-nil replaces.
			targets := linkTargetsOf(in.Links)
			assert.Must(t, (targets == nil) == tc.nil, "linkTargetsOf gave %v for %s, want nil = %v", targets, tc.body, tc.nil)

			// Out again, so an empty array does not re-encode as an
			// omitted field.
			raw, err := json.Marshal(in)
			assert.Must(t, err == nil, "encode: %v", err)
			hasKey := strings.Contains(string(raw), `"links"`)
			assert.Must(t, hasKey != tc.nil, "re-encoded as %s; a nil Links must omit the key and a "+
				"non-nil one must carry it", raw)
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
	t.Parallel()
	if _, ok := reflect.TypeOf(DocsLinkRemoveInput{}).FieldByName("Role"); ok {
		t.Fatal("DocsLinkRemoveInput carries a Role that markdown.LinkRemove cannot read")
	}
	for _, name := range jsonKeysOf(reflect.TypeOf(DocsLinkRemoveInput{})) {
		assert.Must(t, name != "role", "docs.links.remove's input schema asks for a role it ignores")
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
