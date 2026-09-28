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
type ValidateRequest struct {
	Query          *Query
	Params         map[string]any
	Renderer       string
	RendererParams map[string]any
}

// ValidateResult is what a query that passed says about itself.
type ValidateResult struct {
	Refs   []TypeRef
	Limits ResolvedLimits
}

// Validate compiles and judges one query against one game without
// storing it and without executing it.
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
