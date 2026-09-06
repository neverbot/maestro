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
//
// **Over the wire, not through the Go functions.** A transcript is a
// list of JSON arguments an agent sends, so it is sent: a real MCP
// client session against a real HTTP server. Calling the exported
// MCP* handlers directly would skip every input schema, which is the
// layer that made three tools uncallable by any real client in the views
// sub-project while the whole repository was green.
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
//
// It is read off the calls rather than kept in a second list beside
// them, because a second list is the thing that goes stale: a transcript
// that adds a type and forgets the list would be a transcript nothing
// checked the new type against.
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
	if err != nil {
		t.Fatalf("walking the bundle for transcripts: %v", err)
	}
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
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	var parsed transcript
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if parsed.Genre == "" {
		t.Fatalf("%s declares no genre", name)
	}
	if want := strings.TrimSuffix(path.Base(name), ".json"); parsed.Genre != want {
		t.Fatalf("%s declares genre %q: the file name and the genre are one name written "+
			"twice and they have drifted", name, parsed.Genre)
	}
	if len(parsed.Calls) == 0 {
		t.Fatalf("%s carries no calls", name)
	}
	return parsed
}

// transcriptFixture is one server, one user, one game per transcript and
// one witness game that no transcript ever touches.
//
// **A game per transcript, not one shared game.** Three transcripts
// applied to one game would pass while a cross-game leak existed, and
// the witness is the other half of that: a game whose counts stay empty
// while three others are filled is the only evidence that scoping did
// the work rather than the assertions.
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
		Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	newToken := func(slug, name string) string {
		game, err := projSvc.Create(ctx, slug, name, owner.ID)
		if err != nil {
			t.Fatalf("Create game %s: %v", slug, err)
		}
		secret, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
			ProjectID: game.ID, UserID: owner.ID, Label: "agent",
		})
		if err != nil {
			t.Fatalf("CreateAPIToken for %s: %v", slug, err)
		}
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
//
// Every failure names the transcript, the index of the call, the tool
// and the server's own message: "call 12 of genres/racing.json:
// relation_types.upsert: invalid_input at source_type_keys[0]" is
// repairable and "transcript failed" is not.
func applyTranscript(t *testing.T, f transcriptFixture, name string, script transcript) web.GameCountsOutput {
	t.Helper()
	session := connectMCP(t, f.baseURL, f.tokens[name])
	ctx := context.Background()
	validated, saved := 0, 0
	for i, call := range script.Calls {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: call.Tool, Arguments: call.Args})
		if err != nil {
			t.Fatalf("call %d of %s: %s: transport: %v", i, name, call.Tool, err)
		}
		if result.IsError {
			t.Fatalf("call %d of %s: %s: %s", i, name, call.Tool, transcriptFailure(result))
		}
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
			if !answer.Valid {
				t.Fatalf("call %d of %s: views.validate refused the document it teaches: %+v",
					i, name, answer.Errors)
			}
		case "views.upsert":
			saved++
		}
	}
	// A transcript that ends without composing a view covers three
	// surfaces and claims to cover five. The plan's shape for every
	// genre is a validate and a save, and asserting the count here is
	// what stops a transcript quietly dropping them.
	if validated == 0 || saved == 0 {
		t.Fatalf("%s ran %d views.validate and %d views.upsert calls: a transcript that "+
			"never composes a view leaves the two view surfaces unexecuted while looking "+
			"like a passing example", name, validated, saved)
	}

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
//
// The call-by-call half only. The shape of the game each transcript
// produces is TestEveryDeclaredTypeInATranscriptIsPopulated's, and the
// two are separate tests because they catch different faults: every call
// can succeed while a declared type is never seeded, which is a worked
// example with a dead branch.
func TestGenreTranscriptsApply(t *testing.T) {
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
//
// A transcript that declares `faction` and never seeds one passes every
// call and teaches a vocabulary with a dead branch. A transcript that
// leaves flagged rows behind teaches a game shape the product itself
// considers broken.
func TestEveryDeclaredTypeInATranscriptIsPopulated(t *testing.T) {
	paths := transcriptPaths(t)
	f := newTranscriptFixture(t, paths)
	for _, name := range paths {
		t.Run(name, func(t *testing.T) {
			script := readTranscript(t, name)
			counts := applyTranscript(t, f, name, script)

			declaredTypes := script.declaredTypeKeys()
			declaredRelationTypes := script.declaredRelationTypeKeys()
			if len(declaredTypes) == 0 || len(declaredRelationTypes) == 0 {
				t.Fatalf("%s declares %d entity types and %d relation types: the comparisons "+
					"below would run over an empty set", name, len(declaredTypes), len(declaredRelationTypes))
			}

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
			if len(entities) != len(declaredTypes) || len(relations) != len(declaredRelationTypes) {
				t.Errorf("%s declares %d entity and %d relation types; games.counts reports "+
					"%d and %d", name, len(declaredTypes), len(declaredRelationTypes),
					len(entities), len(relations))
			}
			for _, key := range declaredTypes {
				row, ok := entities[key]
				if !ok {
					t.Errorf("%s declares the entity type %q and games.counts does not report it",
						name, key)
					continue
				}
				if row.EntityCount == 0 {
					t.Errorf("%s declares the entity type %q and seeds none of it: a worked "+
						"example with a branch nothing instances", name, key)
				}
				if row.InvalidCount != 0 {
					t.Errorf("%s leaves %d invalid rows of %q behind: the example teaches a "+
						"game shape the product itself flags as broken", name, row.InvalidCount, key)
				}
			}
			for _, key := range declaredRelationTypes {
				row, ok := relations[key]
				if !ok {
					t.Errorf("%s declares the relation type %q and games.counts does not report it",
						name, key)
					continue
				}
				if row.RelationCount == 0 {
					t.Errorf("%s declares the relation type %q and writes no edge of it: the "+
						"edge type is vocabulary the example never uses", name, key)
				}
				if row.InvalidCount != 0 {
					t.Errorf("%s leaves %d invalid edges of %q behind", name, row.InvalidCount, key)
				}
			}
			if counts.Totals.Invalid != 0 {
				t.Errorf("%s leaves %d invalid rows behind in total", name, counts.Totals.Invalid)
			}
			if counts.Totals.Entities == 0 || counts.Totals.Relations == 0 {
				t.Errorf("%s produced %d entities and %d relations", name,
					counts.Totals.Entities, counts.Totals.Relations)
			}
		})
	}
}

// TestATranscriptWritesOnlyToItsOwnGame is the isolation half.
//
// It applies every transcript and then reads a game no transcript was
// ever pointed at. Without it, three transcripts could be writing into
// one shared game — or a scope check could be broken — and every
// assertion above would still pass, because every assertion above only
// ever looks at games that were meant to be full.
func TestATranscriptWritesOnlyToItsOwnGame(t *testing.T) {
	paths := transcriptPaths(t)
	f := newTranscriptFixture(t, paths)

	witness := connectMCP(t, f.baseURL, f.witness)
	var before web.GameCountsOutput
	decodeStructured(t, callOK(t, witness, "games.counts", map[string]any{}), &before)
	if len(before.EntityTypes) != 0 || len(before.RelationTypes) != 0 {
		t.Fatalf("the witness game was not empty before anything ran: %+v", before)
	}

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
	if len(after.EntityTypes) != 0 || len(after.RelationTypes) != 0 ||
		after.Totals.Entities != 0 || after.Totals.Relations != 0 {
		t.Fatalf("a game no transcript was pointed at holds content after %d rows were "+
			"written elsewhere: %+v", written, after)
	}
}

// TestTheTranscriptRunnerReportsAFailedCall is the precision fixture for
// the runner itself.
//
// Every assertion in this file is of the form "nothing was refused", and
// a runner that quietly skipped calls — an empty Calls slice, a tool
// name it did not recognise, a result whose IsError it never read —
// would satisfy all of them. So a deliberately broken call is sent
// through the same path and its refusal is read, in a sub-test that is
// expected to fail and is run with its own recovery.
func TestTheTranscriptRunnerReportsAFailedCall(t *testing.T) {
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
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError {
		t.Fatal("a relation type between two undeclared entity types was accepted, so the " +
			"transcripts' own endpoint declarations are checked by nothing")
	}
	message := transcriptFailure(result)
	if message == "" || strings.Contains(message, "refused with no content") {
		t.Fatalf("the runner rendered a refusal as %q, which names neither the code nor the "+
			"argument: a failure this test can print is the whole difference between a "+
			"repairable transcript and a rerun", message)
	}
	t.Logf("a refused call renders as: %s", message)
}
