package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/metamodel"
)

// **Every rule this surface enforces lives in a tool description**, and
// the skill bundle routes to those descriptions rather than copying them
// — deliberately, because a second copy of a vocabulary is a copy that
// goes quietly false. The cost was that an agent driving the REST mirror
// could not read them: it had to provoke a refusal to find out what it
// was allowed to say. One given the bundle with no other context
// recovered four of the seven analysis traits by grepping the genre
// templates for literals and never found `ordering`, `acyclic` or
// `annotation` at all.
//
// This route is what makes `reference/analysis.md`'s "read it before you
// need it" true on both surfaces, and this test is what keeps it true:
// the closed vocabulary is compared against `metamodel.AnalysisTraits`,
// which is the one place it is authoritative.
func TestTheToolDescriptionsAreReadableOverREST(t *testing.T) {
	t.Parallel()
	// The full server: a server built without the content services
	// registers none of their tools, and the vocabulary this test is
	// about lives on one of them.
	f := newRESTFixture(t)
	srv := f.srv

	req := httptest.NewRequest(http.MethodGet, "/api/mcp/tools", nil)
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding the tool reference: %v", err)
	}
	if len(body.Tools) == 0 {
		t.Fatal("no tool was listed, so this guard holds nothing")
	}

	descriptions := map[string]string{}
	for _, tool := range body.Tools {
		if tool.Description == "" {
			t.Errorf("%s is listed with no description, which is the whole of what this route is for", tool.Name)
		}
		descriptions[tool.Name] = tool.Description
	}

	upsert, ok := descriptions["relation_types.upsert"]
	if !ok {
		t.Fatal("relation_types.upsert is not listed, and it is the one that carries the trait vocabulary")
	}
	for _, trait := range metamodel.AnalysisTraits {
		if !strings.Contains(upsert, trait) {
			t.Errorf("the trait %q is in metamodel.AnalysisTraits and not in the description an agent "+
				"reads: a closed vocabulary nobody can read is a rule nobody can follow", trait)
		}
	}
}

// An anonymous caller gets nothing: the descriptions name every route
// this instance serves, and a signed-out reader has no business with the
// shape of a surface they cannot call.
func TestTheToolReferenceNeedsACaller(t *testing.T) {
	t.Parallel()
	srv := newRESTFixture(t).srv
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/mcp/tools", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}
