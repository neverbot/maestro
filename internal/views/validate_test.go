package views

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
)

// TestValidateAnswersWhatItLearnedRatherThanMerelyYes is the reason
// ValidateResult is not an empty struct: a validator whose success
// carries nothing tells a caller only that it may proceed.
//
// Both members are asserted against a document that states neither: the
// query names its types at three pointers and declares no `limits`
// block, so Refs is something only the resolve pass knows and Limits is
// the defaults a caller would otherwise have to look up.
func TestValidateAnswersWhatItLearnedRatherThanMerelyYes(t *testing.T) {
	g, _ := newGame(t)
	got, err := g.views.Validate(context.Background(), g.projectID, ValidateRequest{
		Query: mustParse(t, questsToZones),
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(got.Refs) != 3 {
		t.Fatalf("Refs = %+v, want one per type reference in the document", got.Refs)
	}
	for _, ref := range got.Refs {
		if ref.Pointer == "" || ref.Key == "" {
			t.Errorf("ref %+v carries no address: the pointer is what makes the list useful", ref)
		}
	}
	if got.Limits != (ResolvedLimits{MaxDepth: DefaultMaxDepth, MaxNodes: DefaultMaxNodes,
		MaxEdges: DefaultMaxEdges}) {
		t.Errorf("Limits = %+v, want the defaults a document that set none runs under", got.Limits)
	}
}

// TestValidateRefusesWhatARunWouldRefuse is the loop views.validate
// exists to close, and every case here is a refusal that reaches a
// different pass. A validator that stopped after one of them would tell
// an agent a document is fine and then have views.run refuse it, which
// is worse than no validator: the loop would close on the wrong answer.
//
// The last two are the ones that make compiling non-negotiable. A depth
// beyond the walk's shape and an `@type` operand bound to a *parameter*
// are refused by the compiler and by nothing before it — resolution
// rewrites an `@type` operand it can see, and it cannot see a value the
// run binds.
func TestValidateRefusesWhatARunWouldRefuse(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name    string
		req     ValidateRequest
		code    string
		point   string
		message string
	}{
		{
			name: "a misspelled entity type, caught by resolution",
			req:  ValidateRequest{Query: mustParse(t, `{"v":1,"from":[{"type":"qeust"}]}`)},
			code: CodeQueryInvalid, point: "/from/0/type",
		},
		{
			name: "a misspelled relation type, caught by resolution",
			req: ValidateRequest{Query: mustParse(t, `{"v":1,"from":[{"type":"quest","as":"q"}],
				"traverse":[{"from":"q","via":"avilable_to","as":"z"}]}`)},
			code: CodeQueryInvalid, point: "/traverse/0/via/0",
		},
		{
			name: "an operator the declared field does not answer",
			req: ValidateRequest{Query: mustParse(t, `{"v":1,"from":[{"type":"quest",
				"where":{"field":"min_level","op":"contains","value":"x"}}]}`)},
			code: CodeQueryInvalid, point: "/from/0/where/op",
		},
		{
			name: "a renderer that cannot draw this query",
			req: ValidateRequest{
				Query:          mustParse(t, questsOnly),
				Renderer:       RendererNested,
				RendererParams: map[string]any{"contain_via": "takes_place_in"},
			},
			code: CodeRendererRequirements, point: "/renderer_params/contain_via",
		},
		{
			// A parameter feeding an @type operand: resolution cannot
			// rewrite it, because the value arrives with the run.
			name: "an @type bound to a parameter naming no type",
			req: ValidateRequest{
				Query: mustParse(t, `{"v":1,"params":[{"key":"kind","type":"text","default":"qeust"}],
					"from":[{"type":"quest","where":{"field":"@type","op":"eq","value":{"param":"kind"}}}]}`),
			},
			code: CodeQueryInvalid,
		},
		{
			// The same leaf, with the bad spelling arriving as the
			// *call's* value over a default that is fine. This is what
			// makes the bound parameters load-bearing rather than
			// decorative: validating against the declared defaults would
			// accept this document and views.run would then refuse it,
			// which is the loop closing on the wrong answer.
			name: "an @type whose value this call binds names no type",
			req: ValidateRequest{
				Query: mustParse(t, `{"v":1,"params":[{"key":"kind","type":"text","default":"quest"}],
					"from":[{"type":"quest","where":{"field":"@type","op":"eq","value":{"param":"kind"}}}]}`),
				Params: map[string]any{"kind": "qeust"},
			},
			code: CodeQueryInvalid, message: `"qeust"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := g.views.Validate(ctx, g.projectID, tc.req)
			if err == nil {
				t.Fatal("the document was accepted; views.run would then refuse it")
			}
			var qe *QueryError
			if !errors.As(err, &qe) {
				t.Fatalf("err = %v (%T), want a *QueryError", err, err)
			}
			if qe.Code != tc.code {
				t.Fatalf("code = %q, want %q: %v", qe.Code, tc.code, err)
			}
			if tc.message != "" && !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("err = %v, want it to name %s", err, tc.message)
			}
			if tc.point == "" {
				return
			}
			found := false
			for _, f := range qe.Fields {
				if f.Path == tc.point {
					found = true
				}
			}
			if !found {
				t.Fatalf("no problem at %s: %+v", tc.point, qe.Fields)
			}
		})
	}
}

// TestValidateRefusesTheSameDocumentARunRefuses is the pair assertion
// the table above cannot make on its own: the two calls must agree, or
// the loop closes on an answer the run does not honour. It drives the
// parameter case in particular, which is the one that reaches only the
// compiler.
func TestValidateRefusesTheSameDocumentARunRefuses(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	doc := `{"v":1,"params":[{"key":"kind","type":"text","default":"qeust"}],
		"from":[{"type":"quest","where":{"field":"@type","op":"eq","value":{"param":"kind"}}}]}`

	_, validateErr := g.views.Validate(ctx, g.projectID, ValidateRequest{Query: mustParse(t, doc)})
	_, runErr := g.views.Run(ctx, g.projectID, RunRequest{Query: mustParse(t, doc)})
	if validateErr == nil || runErr == nil {
		t.Fatalf("validate = %v, run = %v: this test needs both to refuse", validateErr, runErr)
	}
	if validateErr.Error() != runErr.Error() {
		t.Fatalf("validate said %q and run said %q for one document", validateErr, runErr)
	}

	// And the control, so the pair is not "both always refuse": the same
	// query with a spelling this game declares is accepted by both.
	good := strings.ReplaceAll(doc, "qeust", "quest")
	if _, err := g.views.Validate(ctx, g.projectID,
		ValidateRequest{Query: mustParse(t, good)}); err != nil {
		t.Fatalf("the repaired document must validate: %v", err)
	}
	if _, err := g.views.Run(ctx, g.projectID,
		RunRequest{Query: mustParse(t, good)}); err != nil {
		t.Fatalf("the repaired document must run: %v", err)
	}
}

// TestValidateStoresNothingAndRunsNothing is the other half of what the
// tool promises. "Without saving" is the assertion that matters — an
// implementation that validated by saving and rolling back would leave a
// version number moved — and it is checked by asking for the view the
// document would have been saved under.
func TestValidateStoresNothingAndRunsNothing(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	if _, err := g.views.Validate(ctx, g.projectID, ValidateRequest{
		Query: mustParse(t, questsOnly), Renderer: RendererGraph,
	}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	page, err := g.views.ListViews(ctx, g.projectID, ViewFilter{})
	if err != nil {
		t.Fatalf("ListViews: %v", err)
	}
	if len(page.Views) != 0 {
		t.Fatalf("validation stored %d views", len(page.Views))
	}
}

// TestTheStaleFlagAgreesWithWhatARunReports is the whole claim
// StaleViews makes: the boolean is exact even though the pass never
// reads view_refs, because the dependency index changes *which*
// diagnostic a run gives and never whether there is one.
//
// The four cases are the four the index could plausibly have separated:
// a view nothing touched, a view whose type was renamed (which a run
// resolves by id and still calls stale), a view whose type was deleted
// and re-declared under the same key (which resolves by key and is not
// stale), and a view whose field was dropped from a schema — the case a
// flag computed from view_refs alone would have called clean, because a
// field is not a reference.
//
// The run is asked the same question in the same test, so the two cannot
// drift: the flag is compared against whether RunView refuses or reports
// a diagnostic, not against a second list of expectations.
func TestTheStaleFlagAgreesWithWhatARunReports(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()

	g.save(t, "untouched", questsOnly)
	g.save(t, "renamed", chainView)
	g.save(t, "filtered", `{"v":1,"from":[{"type":"quest",
		"where":{"field":"min_level","op":"gt","value":10}}]}`)

	g.renameRelationType(t, "requires", "depends_on")
	// The field the third view filters on, dropped from the schema: the
	// case a refs-only flag cannot see at all.
	if _, err := g.pool.Exec(ctx,
		`UPDATE entity_types SET field_schema = (
		   SELECT coalesce(jsonb_agg(f), '[]'::jsonb)
		   FROM jsonb_array_elements(field_schema) f
		   WHERE f ->> 'key' <> 'min_level')
		 WHERE project_id = $1 AND key = 'quest'`, g.projectID); err != nil {
		t.Fatalf("drop min_level: %v", err)
	}

	page, err := g.views.ListViews(ctx, g.projectID, ViewFilter{})
	if err != nil {
		t.Fatalf("ListViews: %v", err)
	}
	if len(page.Views) != 3 {
		t.Fatalf("%d views listed, want the three saved", len(page.Views))
	}
	flags, err := g.views.StaleViews(ctx, g.projectID, page.Views)
	if err != nil {
		t.Fatalf("StaleViews: %v", err)
	}

	flagged := 0
	for _, row := range page.Views {
		flag, ok := flags[row.ID]
		if !ok {
			t.Fatalf("%s has no flag at all", row.Key)
		}
		if flag {
			flagged++
		}
		res, runErr := g.views.RunView(ctx, g.projectID, row.Key, RunRequest{})
		reported := runErr != nil || len(res.Stale) > 0
		if flag != reported {
			t.Errorf("%s: the listing says stale=%v and a run says %v (err=%v, stale=%+v)",
				row.Key, flag, reported, runErr, res.Stale)
		}
	}
	// The controls, so the agreement is not "everything is stale" or
	// "nothing is": exactly the two that were moved under are flagged.
	if flagged != 2 {
		t.Fatalf("%d of 3 views are stale, want the renamed one and the filtered one", flagged)
	}
}

// TestADeletedAndRecreatedTypeIsNotStaleInTheListing is the flag's
// negative control against the resolution order's step 2. It is separate
// from the test above because it needs a game whose type it can delete
// and re-declare, which the shared fixture's entities forbid.
func TestADeletedAndRecreatedTypeIsNotStaleInTheListing(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	g.save(t, "chain", chainView)

	id := g.typeIDOf(t, KindRelationType, "requires")
	if _, err := g.pool.Exec(ctx, `DELETE FROM relations WHERE project_id = $1
		AND relation_type_id = $2`, g.projectID, id); err != nil {
		t.Fatalf("clear the relations: %v", err)
	}
	if _, err := g.pool.Exec(ctx, `DELETE FROM relation_types WHERE project_id = $1
		AND id = $2`, g.projectID, id); err != nil {
		t.Fatalf("delete the relation type: %v", err)
	}
	page, err := g.views.ListViews(ctx, g.projectID, ViewFilter{})
	if err != nil {
		t.Fatalf("ListViews: %v", err)
	}
	gone, err := g.views.StaleViews(ctx, g.projectID, page.Views)
	if err != nil {
		t.Fatalf("StaleViews: %v", err)
	}
	if !gone[page.Views[0].ID] {
		t.Fatal("a view whose relation type was deleted must be stale")
	}

	// Re-declared under the same key: a different id, no ref to follow,
	// and the by-key step of the resolution order answers.
	if _, err := g.meta.UpsertRelationType(ctx, g.projectID,
		metamodel.RelationTypeInput{Key: "requires", Label: "Requires"}); err != nil {
		t.Fatalf("re-declare requires: %v", err)
	}
	back, err := g.views.StaleViews(ctx, g.projectID, page.Views)
	if err != nil {
		t.Fatalf("StaleViews: %v", err)
	}
	if back[page.Views[0].ID] {
		t.Fatal("a type deleted and re-declared under the same key resolves by key: not stale")
	}
}

// TestAParameterisedViewIsNotStale is the flag's other negative control,
// and it is the one an implementation that reused runStored whole would
// fail. A view declaring a parameter with no default is refused by a run
// that omits the value — `param_unbound` — and nothing about the game
// moved: flagging it would light up every parameterised view in a game
// forever, which is a flag designers learn to ignore.
func TestAParameterisedViewIsNotStale(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	in := saveable("byLevel", `{"v":1,"params":[{"key":"floor","type":"number"}],
		"from":[{"type":"quest","where":{"field":"min_level","op":"gte","value":{"param":"floor"}}}]}`)
	if _, err := g.views.UpsertView(ctx, g.projectID, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	page, err := g.views.ListViews(ctx, g.projectID, ViewFilter{})
	if err != nil {
		t.Fatalf("ListViews: %v", err)
	}
	flags, err := g.views.StaleViews(ctx, g.projectID, page.Views)
	if err != nil {
		t.Fatalf("StaleViews: %v", err)
	}
	if flags[page.Views[0].ID] {
		t.Fatal("a view declaring an unbound parameter is not stale: the game did not move")
	}
	// The control that the parameter really is unbound, so this test is
	// not passing over a document that binds itself from a default.
	if _, err := g.views.RunView(ctx, g.projectID, "byLevel", RunRequest{}); err == nil {
		t.Fatal("the run was accepted; this fixture needs a parameter with no default")
	}
}

// TestStaleViewsIsEmptyRatherThanNilForAnEmptyPage keeps the caller from
// having to tell a missing flag from a false one on a page with no rows.
func TestStaleViewsIsEmptyRatherThanNilForAnEmptyPage(t *testing.T) {
	g, _ := newGame(t)
	flags, err := g.views.StaleViews(context.Background(), g.projectID, []dbq.View{})
	if err != nil {
		t.Fatalf("StaleViews: %v", err)
	}
	if flags == nil {
		t.Fatal("an empty page answered nil rather than an empty map")
	}
}
