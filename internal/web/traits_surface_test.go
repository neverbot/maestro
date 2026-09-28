package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/metamodel"
)

// This file is the "both surfaces, one commit" half of the analysis
// sub-project's only change to an existing tool.
func TestATraitDeclaredOverTheRealToolComesBackOverTheRealTool(t *testing.T) {
	t.Parallel()
	f := newMetamodelFixture(t)
	httpSrv := httptest.NewServer(f.srv)
	defer httpSrv.Close()
	session := connectMCP(t, httpSrv.URL, f.token)

	callOK(t, session, "types.upsert", map[string]any{
		"key": "quest", "label": "Quest", "label_plural": "Quests",
	})
	callOK(t, session, "types.upsert", map[string]any{
		"key": "zone", "label": "Zone", "label_plural": "Zones",
	})

	var upserted struct {
		Key            string   `json:"key"`
		SemanticRole   string   `json:"semantic_role"`
		AnalysisTraits []string `json:"analysis_traits"`
	}
	decodeStructured(t, callOK(t, session, "relation_types.upsert", map[string]any{
		"key": "requires", "label": "requires",
		"source_type_keys": []any{"quest"},
		"target_type_keys": []any{"quest"},
		// Both questions asked of the same edge, on one call, because
		// the description promises they are two questions and a test
		// that sent only one could not tell them apart.
		"semantic_role":   "prerequisite",
		"analysis_traits": []any{"prerequisite_of", "acyclic"},
	}), &upserted)
	assert.Must(t, slices.Equal(upserted.AnalysisTraits, []string{"prerequisite_of", "acyclic"}), "relation_types.upsert answered analysis_traits = %v, want what was sent: "+
		"a field the write accepts and the answer drops is the write-only-field defect",
		upserted.AnalysisTraits)
	assert.Must(t, upserted.SemanticRole == "prerequisite", "semantic_role = %q, want it unaffected by the trait declaration",
		upserted.SemanticRole)

	// The read path, on a second call, because the upsert's own answer
	// could be echoing its input rather than the stored row.
	var fetched struct {
		Key            string   `json:"key"`
		AnalysisTraits []string `json:"analysis_traits"`
	}
	decodeStructured(t, callOK(t, session, "relation_types.get", map[string]any{
		"key": "requires",
	}), &fetched)
	assert.Must(t, fetched.Key == "requires" && slices.Equal(fetched.AnalysisTraits, []string{"prerequisite_of", "acyclic"}), "relation_types.get = %+v, want the stored traits under the documented "+
		"spelling analysis_traits", fetched)

	// A type that declared nothing answers with the key absent rather
	// than with `[]`: undeclared is not the same statement as
	// deliberately inert, and the answer must not flatten them.
	callOK(t, session, "relation_types.upsert", map[string]any{
		"key": "mentions", "label": "mentions",
	})
	raw := structuredJSON(t, callOK(t, session, "relation_types.get", map[string]any{
		"key": "mentions",
	}))
	assert.Must(t, !strings.Contains(raw, "analysis_traits"), "an undeclared type answered %s, want no analysis_traits key at all", raw)

	// And an incoherent combination is refused over the wire with the
	// code and the path a caller can act on, rather than as a server
	// fault carrying the constraint's own text.
	res := callErr(t, session, "relation_types.upsert", map[string]any{
		"key": "connects_to", "label": "connects to",
		"analysis_traits": []any{"symmetric", "prerequisite_of"},
	})
	assert.Must(t, res.Error == "invalid_schema", "error code = %q, want invalid_schema: an incoherent trait combination "+
		"is a type declaration that cannot stand", res.Error)
	assert.Must(t, strings.Contains(res.Message, "symmetric") && strings.Contains(res.Message, "prerequisite_of"), "message = %q, want it to name both traits it refused together",
		res.Message)
}

// TestATraitDeclaredOverTheRESTMirrorComesBackOverIt is the second
// surface, in the same commit as the first. The two share their input
// and output structs deliberately, and this is what pins that they still
// do — a divergence would show up here as a missing key, not as a
// compile error.
func TestATraitDeclaredOverTheRESTMirrorComesBackOverIt(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)

	if rec := f.as(t, http.MethodPost, "/types", map[string]any{
		"key": "quest", "label": "Quest", "label_plural": "Quests",
	}); rec.Code != http.StatusOK {
		t.Fatalf("declare the entity type = %d: %s", rec.Code, rec.Body.String())
	}

	rec := f.as(t, http.MethodPost, "/relation-types", map[string]any{
		"key": "requires", "label": "requires",
		"semantic_role":   "prerequisite",
		"analysis_traits": []any{"prerequisite_of", "acyclic"},
	})
	assert.Must(t, rec.Code == http.StatusOK, "upsert = %d: %s", rec.Code, rec.Body.String())
	var written struct {
		AnalysisTraits []string `json:"analysis_traits"`
	}
	decodeBody(t, rec, &written)
	assert.Must(t, slices.Equal(written.AnalysisTraits, []string{"prerequisite_of", "acyclic"}), "POST answered analysis_traits = %v, want what was sent",
		written.AnalysisTraits)

	got := f.as(t, http.MethodGet, "/relation-types/by-key/requires", nil)
	assert.Must(t, got.Code == http.StatusOK, "get = %d: %s", got.Code, got.Body.String())
	var read struct {
		Key            string   `json:"key"`
		AnalysisTraits []string `json:"analysis_traits"`
	}
	decodeBody(t, got, &read)
	assert.Must(t, read.Key == "requires" && slices.Equal(read.AnalysisTraits, []string{"prerequisite_of", "acyclic"}), "GET = %+v, want the stored traits back", read)

	// The refusal, on this surface too, with the same code and the same
	// path: closing one path and leaving the hole one step along is the
	// defect this whole commit is shaped around.
	bad := f.as(t, http.MethodPost, "/relation-types", map[string]any{
		"key": "notes", "label": "notes",
		"analysis_traits": []any{"annotation", "containment"},
	})
	body := assertError(t, bad, http.StatusUnprocessableEntity, "invalid_schema", "analysis_traits")
	assert.Must(t, strings.Contains(body.Message, "annotation") && strings.Contains(body.Message, "containment"), "message = %q, want it to name both traits it refused together",
		body.Message)
}

// TestTheRelationTypesUpsertDescriptionNamesEveryTraitAndEveryTraitIsNamed
// is the bidirectional guard over the generated tool description.
func TestTheRelationTypesUpsertDescriptionNamesEveryTraitAndEveryTraitIsNamed(t *testing.T) {
	t.Parallel()
	description := servedToolDescription(t, "relation_types.upsert")

	for _, trait := range metamodel.AnalysisTraits {
		assert.Must(t, strings.Contains(description, "`"+trait+"`") || strings.Contains(description, `"`+trait+`"`), "relation_types.upsert's description does not offer %q, so an agent "+
			"cannot know the trait exists: a vocabulary an agent is not told is a "+
			"vocabulary nobody declares", trait)
	}

	// Backwards. The description quotes plenty of things that are not
	// traits — semantic roles, field-schema words — so the assertion is
	// over the segment that lists the vocabulary, which is what
	// metamodel.QuotedList renders and what the description splices in.
	offered := metamodel.QuotedList(metamodel.AnalysisTraits)
	assert.Must(t, strings.Contains(description, offered), "the description does not carry the vocabulary as one generated list "+
		"(%q): if it stopped being generated, a trait added to "+
		"metamodel.AnalysisTraits will not reach an agent", offered)
}

// TestTheRelationTypesUpsertDescriptionNamesEveryRefusedCombination is
// the second half, over the coherence rules rather than the vocabulary.
func TestTheRelationTypesUpsertDescriptionNamesEveryRefusedCombination(t *testing.T) {
	t.Parallel()
	description := servedToolDescription(t, "relation_types.upsert")
	assert.Must(t, len(metamodel.AnalysisTraitConflicts) != 0, "the conflict table is empty, so every assertion below is vacuous")
	for _, line := range metamodel.TraitConflictLines() {
		assert.Must(t, strings.Contains(description, line), "the description does not name the refused combination %q: it is "+
			"generated from metamodel.AnalysisTraitConflicts, so a rule that is not "+
			"here means the generation stopped", line)
	}
	// And the distinction the whole column carries, which an agent gets
	// wrong by default: a role is not a behaviour, and an omitted list is
	// not an inert type.
	for _, phrase := range []string{"semantic_role", "undeclared", "annotation"} {
		assert.Must(t, strings.Contains(description, phrase), "the description never mentions %q", phrase)
	}
}

// servedToolDescription reads one tool's description off the served tool
// list — the text an agent is actually handed — rather than out of the
// Go literal that builds it.
func servedToolDescription(t *testing.T, name string) string {
	t.Helper()
	f := newMetamodelFixture(t)
	httpSrv := httptest.NewServer(f.srv)
	defer httpSrv.Close()
	session := connectMCP(t, httpSrv.URL, f.token)

	tools, err := session.ListTools(context.Background(), nil)
	assert.Must(t, err == nil, "ListTools: %v", err)
	for _, tool := range tools.Tools {
		if tool.Name == name {
			assert.Must(t, tool.Description != "", "%s is served with an empty description", name)
			return tool.Description
		}
	}
	t.Fatalf("%s is not served at all", name)
	return ""
}

// callErr calls a tool, insists it was refused, and decodes the error
// body every tool on this surface writes. Asserting only that a call
// failed would pass for a refusal that happened for an entirely
// different reason.
func callErr(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) wireError {
	t.Helper()
	result, err := session.CallTool(context.Background(),
		&mcp.CallToolParams{Name: name, Arguments: args})
	assert.Must(t, err == nil, "CallTool(%s): %v", name, err)
	assert.Must(t, result.IsError, "CallTool(%s) was expected to be refused and succeeded", name)
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("CallTool(%s) refusal is not text: %#v", name, result.Content[0])
	}
	var body wireError
	if err := json.Unmarshal([]byte(text.Text), &body); err != nil {
		t.Fatalf("decode the refusal %s: %v", text.Text, err)
	}
	assert.Must(t, body.Error != "" && body.Message != "", "a refusal with no code or no message: %s", text.Text)
	return body
}

// structuredJSON is one tool answer as the JSON an agent receives, for
// the assertions that are about a key being **absent** — which a decode
// into a Go struct cannot see.
func structuredJSON(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	assert.Must(t, result.StructuredContent != nil, "tool result has no StructuredContent")
	raw, err := json.Marshal(result.StructuredContent)
	assert.Must(t, err == nil, "marshal StructuredContent: %v", err)
	return string(raw)
}
