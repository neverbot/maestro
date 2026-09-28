package web_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http/httptest"
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/skill"
	"github.com/neverbot/maestro/internal/testutil"
	"github.com/neverbot/maestro/internal/views"
	"github.com/neverbot/maestro/internal/web"
)

// A genre transcript is an ordered list of ordinary tool calls with
// their exact arguments — what an agent would have typed, written down.
// There is no importer and no server code reads one; the only thing that
// ever executes a transcript is this file.
type transcript struct {
	Genre string           `json:"genre"`
	Note  string           `json:"note"`
	Calls []transcriptCall `json:"calls"`
}

type transcriptCall struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
}

// declaredTypeKeys is every entity type key the transcript declares with
// types.upsert, in the order it declares them.
func (t transcript) declaredTypeKeys() []string {
	return t.declaredKeys("types.upsert")
}

// declaredRelationTypeKeys is the same for relation_types.upsert.
func (t transcript) declaredRelationTypeKeys() []string {
	return t.declaredKeys("relation_types.upsert")
}

func (t transcript) declaredKeys(tool string) []string {
	var out []string
	seen := map[string]bool{}
	for _, call := range t.Calls {
		if call.Tool != tool {
			continue
		}
		key, _ := call.Args["key"].(string)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

// transcriptPaths enumerates genres/*.json out of the shipped bundle
// rather than naming three files, so a fourth genre added tomorrow is
// executed without editing this test — the "rule not carried one step
// along" failure, which in a test that names its fixtures looks exactly
// like a passing suite.
func transcriptPaths(t *testing.T) []string {
	t.Helper()
	var out []string
	err := fs.WalkDir(skill.Files(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || path.Dir(name) != "genres" || path.Ext(name) != ".json" {
			return nil
		}
		out = append(out, name)
		return nil
	})
	assert.Must(t, err == nil, "walking the bundle for transcripts: %v", err)
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatal("the bundle ships no genre transcripts: every assertion below would run " +
			"over an empty set and pass")
	}
	return out
}

func readTranscript(t *testing.T, name string) transcript {
	t.Helper()
	body, err := fs.ReadFile(skill.Files(), name)
	assert.Must(t, err == nil, "reading %s: %v", name, err)
	var parsed transcript
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	assert.Must(t, parsed.Genre != "", "%s declares no genre", name)
	if want := strings.TrimSuffix(path.Base(name), ".json"); parsed.Genre != want {
		t.Fatalf("%s declares genre %q: the file name and the genre are one name written "+
			"twice and they have drifted", name, parsed.Genre)
	}
	assert.Must(t, len(parsed.Calls) != 0, "%s carries no calls", name)
	return parsed
}

// transcriptFixture is one server, one user, one game per transcript and
// one witness game that no transcript ever touches.
type transcriptFixture struct {
	srv     *web.Server
	baseURL string
	tokens  map[string]string // transcript path -> token
	witness string            // a token for a game no transcript writes to
}

func newTranscriptFixture(t *testing.T, paths []string) transcriptFixture {
	t.Helper()
	pool := testutil.NewPool(t)
	cfg := config.Config{
		SessionTTL: testConfig().SessionTTL,
		InviteTTL:  testConfig().InviteTTL,
		Argon2:     testConfig().Argon2,
	}
	ids := identity.New(pool, cfg)
	projSvc := projects.New(pool)
	mm := metamodel.New(pool, nil)
	vs := views.New(pool, nil)
	md := markdown.New(pool, nil)
	srv := web.NewServer(web.Options{
		Version: "test", Config: cfg, Identity: ids, Projects: projSvc,
		Metamodel: mm, Views: vs, Markdown: md,
	})
	ctx := context.Background()
	owner, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "designer@example.test", DisplayName: "Designer", Password: "password12345",
	})
	assert.Must(t, err == nil, "CreateUser: %v", err)
	newToken := func(slug, name string) string {
		game, err := projSvc.Create(ctx, slug, name, owner.ID)
		assert.Must(t, err == nil, "Create game %s: %v", slug, err)
		secret, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
			ProjectID: game.ID, UserID: owner.ID, Label: "agent",
		})
		assert.Must(t, err == nil, "CreateAPIToken for %s: %v", slug, err)
		return secret
	}
	f := transcriptFixture{srv: srv, tokens: map[string]string{}}
	for _, p := range paths {
		slug := strings.ReplaceAll(strings.TrimSuffix(path.Base(p), ".json"), "_", "-")
		f.tokens[p] = newToken(slug, slug)
	}
	f.witness = newToken("witness", "Witness")

	httpSrv := httptest.NewServer(srv)
	t.Cleanup(httpSrv.Close)
	f.baseURL = httpSrv.URL
	return f
}

// applyTranscript sends every call of one transcript over one MCP client
// session and answers with the game's shape afterwards.
func applyTranscript(t *testing.T, f transcriptFixture, name string, script transcript) web.GameCountsOutput {
	t.Helper()
	session := connectMCP(t, f.baseURL, f.tokens[name])
	ctx := context.Background()
	validated, saved := 0, 0
	for i, call := range script.Calls {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: call.Tool, Arguments: call.Args})
		assert.Must(t, err == nil, "call %d of %s: %s: transport: %v", i, name, call.Tool, err)
		assert.Must(t, !result.IsError, "call %d of %s: %s: %s", i, name, call.Tool, transcriptFailure(result))
		switch call.Tool {
		case "views.validate":
			validated++
			var answer struct {
				Valid  bool `json:"valid"`
				Errors []struct {
					Path    string `json:"path"`
					Message string `json:"message"`
				} `json:"errors"`
			}
			decodeStructured(t, result, &answer)
			assert.Must(t, answer.Valid, "call %d of %s: views.validate refused the document it teaches: %+v",
				i, name, answer.Errors)
		case "views.upsert":
			saved++
		}
	}
	// A transcript that ends without composing a view covers three
	// surfaces and claims to cover five. The plan's shape for every
	// genre is a validate and a save, and asserting the count here is
	// what stops a transcript quietly dropping them.
	assert.Must(t, validated != 0 && saved != 0, "%s ran %d views.validate and %d views.upsert calls: a transcript that "+
		"never composes a view leaves the two view surfaces unexecuted while looking "+
		"like a passing example", name, validated, saved)

	var counts web.GameCountsOutput
	decodeStructured(t, callOK(t, session, "games.counts", map[string]any{}), &counts)
	return counts
}

// transcriptFailure renders a refusal the way an agent receives it: the
// coded body if the tool wrote one, and the raw text if the refusal came
// from a layer below the tools — an input schema rejection, which is the
// failure this test exists to catch, has no coded body at all.
func transcriptFailure(result *mcp.CallToolResult) string {
	if len(result.Content) == 0 {
		return "refused with no content"
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		return fmt.Sprintf("refused with %T", result.Content[0])
	}
	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal([]byte(text.Text), &body); err == nil && body.Error != "" {
		if body.Path != "" {
			return fmt.Sprintf("%s at %s: %s", body.Error, body.Path, body.Message)
		}
		return fmt.Sprintf("%s: %s", body.Error, body.Message)
	}
	return text.Text
}

// TestGenreTranscriptsApply applies every genre transcript against a
// fresh game, over a real MCP client session, and asserts nothing was
// refused.
func TestGenreTranscriptsApply(t *testing.T) {
	t.Parallel()
	paths := transcriptPaths(t)
	f := newTranscriptFixture(t, paths)
	for _, name := range paths {
		t.Run(name, func(t *testing.T) {
			applyTranscript(t, f, name, readTranscript(t, name))
		})
	}
}

// TestEveryDeclaredTypeInATranscriptIsPopulated is the half that makes
// the transcripts more than a smoke test: every declared entity type
// holds at least one row, every declared relation type at least one
// edge, and nothing anywhere is flagged invalid.
func TestEveryDeclaredTypeInATranscriptIsPopulated(t *testing.T) {
	t.Parallel()
	paths := transcriptPaths(t)
	f := newTranscriptFixture(t, paths)
	for _, name := range paths {
		t.Run(name, func(t *testing.T) {
			script := readTranscript(t, name)
			counts := applyTranscript(t, f, name, script)

			declaredTypes := script.declaredTypeKeys()
			declaredRelationTypes := script.declaredRelationTypeKeys()
			assert.Must(t, len(declaredTypes) != 0 && len(declaredRelationTypes) != 0, "%s declares %d entity types and %d relation types: the comparisons "+
				"below would run over an empty set", name, len(declaredTypes), len(declaredRelationTypes))

			entities := map[string]web.EntityTypeSummary{}
			for _, row := range counts.EntityTypes {
				entities[row.Key] = row
			}
			relations := map[string]web.RelationTypeSummary{}
			for _, row := range counts.RelationTypes {
				relations[row.Key] = row
			}
			// Both directions. A type the transcript declared and the
			// game does not report is a call that did nothing; a type the
			// game reports and the transcript never declared is a game
			// that was not built by this transcript alone.
			assert.Should(t, len(entities) == len(declaredTypes) && len(relations) == len(declaredRelationTypes), "%s declares %d entity and %d relation types; games.counts reports "+
				"%d and %d", name, len(declaredTypes), len(declaredRelationTypes),
				len(entities), len(relations))
			for _, key := range declaredTypes {
				row, ok := entities[key]
				if !ok {
					t.Errorf("%s declares the entity type %q and games.counts does not report it",
						name, key)
					continue
				}
				assert.Should(t, row.EntityCount != 0, "%s declares the entity type %q and seeds none of it: a worked "+
					"example with a branch nothing instances", name, key)
				assert.Should(t, row.InvalidCount == 0, "%s leaves %d invalid rows of %q behind: the example teaches a "+
					"game shape the product itself flags as broken", name, row.InvalidCount, key)
			}
			for _, key := range declaredRelationTypes {
				row, ok := relations[key]
				if !ok {
					t.Errorf("%s declares the relation type %q and games.counts does not report it",
						name, key)
					continue
				}
				assert.Should(t, row.RelationCount != 0, "%s declares the relation type %q and writes no edge of it: the "+
					"edge type is vocabulary the example never uses", name, key)
				assert.Should(t, row.InvalidCount == 0, "%s leaves %d invalid edges of %q behind", name, row.InvalidCount, key)
			}
			assert.Should(t, counts.Totals.Invalid == 0, "%s leaves %d invalid rows behind in total", name, counts.Totals.Invalid)
			assert.Should(t, counts.Totals.Entities != 0 && counts.Totals.Relations != 0, "%s produced %d entities and %d relations", name,
				counts.Totals.Entities, counts.Totals.Relations)
		})
	}
}

// TestATranscriptWritesOnlyToItsOwnGame is the isolation half.
func TestATranscriptWritesOnlyToItsOwnGame(t *testing.T) {
	t.Parallel()
	paths := transcriptPaths(t)
	f := newTranscriptFixture(t, paths)

	witness := connectMCP(t, f.baseURL, f.witness)
	var before web.GameCountsOutput
	decodeStructured(t, callOK(t, witness, "games.counts", map[string]any{}), &before)
	assert.Must(t, len(before.EntityTypes) == 0 && len(before.RelationTypes) == 0, "the witness game was not empty before anything ran: %+v", before)

	written := int64(0)
	for _, name := range paths {
		counts := applyTranscript(t, f, name, readTranscript(t, name))
		written += counts.Totals.Entities + counts.Totals.Relations
	}
	if written == 0 {
		t.Fatal("the transcripts wrote nothing at all, so an untouched witness game proves " +
			"nothing about isolation")
	}

	var after web.GameCountsOutput
	decodeStructured(t, callOK(t, witness, "games.counts", map[string]any{}), &after)
	assert.Must(t, len(after.EntityTypes) == 0 && len(after.RelationTypes) == 0 && after.Totals.Entities == 0 && after.Totals.Relations == 0, "a game no transcript was pointed at holds content after %d rows were "+
		"written elsewhere: %+v", written, after)
}

// TestTheTranscriptRunnerReportsAFailedCall is the precision fixture for
// the runner itself.
func TestTheTranscriptRunnerReportsAFailedCall(t *testing.T) {
	t.Parallel()
	paths := transcriptPaths(t)
	f := newTranscriptFixture(t, paths)
	session := connectMCP(t, f.baseURL, f.witness)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "relation_types.upsert",
		Arguments: map[string]any{
			"key": "requires", "label": "Requires",
			"source_type_keys": []any{"a_type_this_game_never_declared"},
			"target_type_keys": []any{"another_one"},
		},
	})
	assert.Must(t, err == nil, "CallTool: %v", err)
	if !result.IsError {
		t.Fatal("a relation type between two undeclared entity types was accepted, so the " +
			"transcripts' own endpoint declarations are checked by nothing")
	}
	message := transcriptFailure(result)
	assert.Must(t, message != "" && !strings.Contains(message, "refused with no content"), "the runner rendered a refusal as %q, which names neither the code nor the "+
		"argument: a failure this test can print is the whole difference between a "+
		"repairable transcript and a rerun", message)
	t.Logf("a refused call renders as: %s", message)
}
