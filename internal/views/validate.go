package views

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// Validation and the listing's stale flag: the two things this package
// answers about a query it is not going to execute.
//
// Both exist because composing a view is a loop. An agent writing a
// five-step traversal against a game it has never seen will get it wrong
// twice, and the difference between a loop that closes in seconds and
// one that closes after a five-second graph walk is whether the
// judgement can be asked for on its own.

// ValidateRequest is one query judged without saving it and without
// running it.
//
// Renderer and RendererParams are optional and are judged together with
// the query when a renderer is named, which is the only way they *can*
// be judged: a renderer requirement is a statement about what the query
// produces (renderers.go), so "is this renderer parameter any good" has
// no answer on its own. Naming neither validates the query alone, which
// is what a caller composing a traversal before it has decided how to
// draw it is doing.
//
// Params are the values a run would bind over the document's declared
// defaults. They are judged here for the same reason the compiler judges
// them: a parameter with no value is refused at the leaf that reads it,
// so a document that validates with no parameters may still refuse the
// run that omits one.
type ValidateRequest struct {
	Query          *Query
	Params         map[string]any
	Renderer       string
	RendererParams map[string]any
}

// ValidateResult is what a query that passed says about itself.
//
// **It is not an empty struct, and that is deliberate.** A validator
// whose success carries nothing tells a caller only that it may proceed;
// these two members are what it learned on the way, and both are things
// an agent would otherwise have to derive from the document by hand.
// Refs is every type this query depends on, with the pointer that names
// it — the same list a save writes into the dependency index, so an
// agent can see before saving which types deleting would break this view.
// Limits are the bounds this query would actually run under, which is
// the document's own where it set them and the defaults where it did
// not: a caller that wrote no `limits` block learns what it got.
type ValidateResult struct {
	Refs   []TypeRef
	Limits ResolvedLimits
}

// Validate compiles and judges one query against one game without
// storing it and without executing it.
//
// **It compiles as well as resolving, and that is not thoroughness for
// its own sake.** Two refusals live in the compiler and nowhere else: a
// depth or a shape resolution has no opinion about, and an `@type`
// operand a run *binds to a parameter*, which resolution cannot rewrite
// because it does not know the value. A validator that stopped after
// resolution would answer "this is fine" to a document views.run then
// refuses, which is worse than no validator — the loop it exists to
// close would close on the wrong answer.
//
// **What it deliberately does not do is execute.** That is the whole
// trade: no statement timeout, no graph walk, no bounded transaction, and
// therefore an answer in the time a catalogue read takes rather than in
// the time a five-hop traversal over a large game takes.
//
// **Staleness has no meaning here**, for the reason Run refuses
// on_stale: staleness is what a *saved* view's dependency index answers,
// and a document handed over for validation has no recorded past to have
// moved away from. A misspelling in it is query_invalid, which is what it
// is.
func (s *Service) Validate(ctx context.Context, projectID uuid.UUID,
	req ValidateRequest,
) (ValidateResult, error) {
	if req.Query == nil {
		return ValidateResult{}, invalidQuery("", "no query document was given")
	}
	cat, err := s.LoadCatalogue(ctx, projectID)
	if err != nil {
		return ValidateResult{}, err
	}
	resolved, err := ResolveAgainst(projectID, cat, req.Query)
	if err != nil {
		return ValidateResult{}, err
	}
	params, err := bindParams(resolved, req.Params)
	if err != nil {
		return ValidateResult{}, err
	}
	// The renderer before the compiler, matching UpsertView's order: a
	// caller told "this renderer cannot draw this query" and "this step
	// is too deep" in one breath fixes the wrong one first, and the save
	// path already decided which of the two a caller hears about.
	if req.Renderer != "" {
		if err := CheckRenderer(req.Renderer, req.RendererParams, resolved); err != nil {
			return ValidateResult{}, err
		}
	}
	// The bound parameters go onto a copy, the way execute builds its
	// own: a `@type` operand a run binds to a parameter is refused by the
	// compiler and by nothing before it, so a validator compiling against
	// the *declared defaults* would answer for a document the caller did
	// not send. The copy is execute's rule as well — one run's bindings
	// must never leak into a *Resolved another run is holding.
	forRun := *resolved
	forRun.Params = params
	if _, _, err := Compile(&forRun, projectID); err != nil {
		return ValidateResult{}, err
	}
	return ValidateResult{Refs: resolved.Refs, Limits: resolved.Limits}, nil
}

// StaleViews reports which of these saved views name something the game
// no longer has, or no longer spells that way.
//
// It is the flag views.list carries, and it costs one catalogue read for
// the whole page: the pass after that is the resolve pass, in Go, over
// documents this package itself stored.
//
// **It does not read view_refs, and the boolean is exact anyway.** The
// dependency index exists so a run can follow a type by id after a
// rename and report the rename instead of a missing type — and that
// changes *which* diagnostic a run gives, never whether there is one.
// The index can only turn "this key names nothing" into "this key was
// renamed", both of which are things the game did to this view; it can
// never rescue a reference that has no id recorded and no live key, and
// it can never break one whose key still resolves. So a per-view refs
// read — one statement per row of a page — would buy a distinction this
// answer does not carry. **What it costs is that a caller wanting to
// know what moved runs the view or opens it**, which is what the
// diagnostics are for and where their pointers are useful.
// TestTheStaleFlagAgreesWithWhatARunReports is what holds the two
// together.
//
// **The renderer half is observed by nothing today, and it stays.**
// StaleViews asks the same two questions runStored asks — the document's
// references and the renderer's — and mutating the second away leaves
// the package green. That is Task 12's own finding 2, one step along:
// `contain_via` is the only type-naming parameter and `nested`
// structurally requires the query to draw edges of that same relation,
// while every field-naming parameter must name a key `project.fields`
// carries, so a renderer parameter the game moved under is today always
// a query the game moved under as well. Dropping the call would make
// this flag correct by a coincidence of the current catalogue rather
// than by construction, and the next parameter kind that is not
// structurally mirrored in the query would stop being flagged with
// nothing to notice by. It is also what lets this function's own claim
// hold by construction: the flag and the run agree because they ask the
// same two questions, not because the answers happen to line up.
//
// **Run arguments are not staleness.** A view declaring a parameter with
// no default is refused by a run that omits the value, and it is not
// stale: nothing about the game moved, and flagging it would light up
// every parameterised view in a game forever. So this pass stops at the
// document's references and its renderer's, and never binds parameters.
func (s *Service) StaleViews(ctx context.Context, projectID uuid.UUID,
	rows []dbq.View,
) (map[uuid.UUID]bool, error) {
	flags := make(map[uuid.UUID]bool, len(rows))
	if len(rows) == 0 {
		return flags, nil
	}
	cat, err := s.LoadCatalogue(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		q, err := ParseQuery(row.Query)
		if err != nil {
			// The same judgement RunView makes: a stored document that no
			// longer parses is this package having stored something it
			// would refuse today, which is a bug here rather than a game
			// that moved on. A listing that answered "stale" for it would
			// file that bug as the designer's.
			return nil, fmt.Errorf("the stored query of view %q does not parse: %w", row.Key, err)
		}
		resolved, problems := resolveInto(cat, q, nil)
		stale := len(problems) > 0
		if !stale {
			var params map[string]any
			if len(row.RendererParams) > 0 {
				if err := json.Unmarshal(row.RendererParams, &params); err != nil {
					return nil, fmt.Errorf("the stored renderer parameters of view %q "+
						"do not parse: %w", row.Key, err)
				}
			}
			stale = len(rendererStaleness(nil, resolved, row.Renderer, params)) > 0
		}
		flags[row.ID] = stale
	}
	return flags, nil
}
