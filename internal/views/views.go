package views

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/paging"
)

// The bounds on a saved view's own prose, and the vocabulary of its
// layout mode.
const (
	// MaxViewNameLen bounds `name`. A name is required: ListViewsPage
	// orders by it, so an unnamed view sorts to the front of every list a
	// designer sees and identifies itself by nothing — the argument
	// internal/metamodel/descriptors.go makes for a type's label, and the
	// same listing consequence.
	MaxViewNameLen = 200
	// MaxViewDescriptionLen bounds `description`, which is optional and
	// is the one string on a view that may hold a newline: it is prose a
	// designer writes about the picture, and refusing a paragraph break
	// in it would make the rule less useful than no rule.
	MaxViewDescriptionLen = 4000
)

// The three layout modes, which are a closed contract with the client
// and are checked here as well as by 0008_views.sql's CHECK.
const (
	LayoutAuto   = "auto"
	LayoutManual = "manual"
	LayoutMixed  = "mixed"
	// DefaultLayoutMode is the column default in 0008_views.sql, spelled
	// here because this package fills the value rather than letting the
	// column default it: UpsertView writes layout_mode on both arms, so
	// an update that said nothing would otherwise store the empty string
	// and fail the CHECK.
	DefaultLayoutMode = LayoutMixed
)

// layoutModes is the closed list, in the order a refusal names them.
var layoutModes = []string{LayoutAuto, LayoutManual, LayoutMixed}

// LayoutModes is the closed list, for the tool description that has to
// print it. It returns a copy, so a caller cannot edit the list this
// package validates against, and it is generated from the same slice
// layoutModeProblem reads — a fourth mode added there appears on the wire
// in the same commit rather than being a mode no agent knows to send.
func LayoutModes() []string { return append([]string(nil), layoutModes...) }

// noVersion is the expected_version an upsert passes when its caller has
// no version to expect. Versions start at 1 and only ever climb, so no
// stored row can equal it: the guarded DO UPDATE is then a no-op on the
// insert path and a guaranteed mismatch if a row turns out to exist
// after all. The same value and the same argument as
// internal/metamodel's; a view is addressed by (project, key) exactly as
// a type is, so the shapes are the same shape rather than two.
const noVersion int32 = -1

// createExpectedVersion is the `expected_version` that spells "this view
// must not exist yet".
const createExpectedVersion int32 = 0

// ViewInput is one saved-view upsert, addressed by its key.
type ViewInput struct {
	Key             string
	Name            string
	Description     string
	Query           []byte
	Renderer        string
	RendererParams  map[string]any
	LayoutMode      string
	ExpectedVersion *int32
	Actor           Actor
}

// ViewFilter narrows a view listing.
type ViewFilter struct {
	Renderer string
	Cursor   string
	Limit    int32
}

// ViewPage is one page of saved views plus the cursor for the next.
type ViewPage struct {
	Views      []dbq.View
	NextCursor string
}

// The bounds on one view listing: asking for nothing is no opinion and
// gets the default, asking for too much is an opinion and gets the cap.
// A game holds tens of views rather than thousands, so both are smaller
// than the metamodel's.
const (
	defaultViewPage int32 = 50
	maxViewPage     int32 = 200
)

// ViewDependency is one dependency of one type, as the deletion of that
// type would report it: which view holds it and where in that view's
// query.
type ViewDependency struct {
	ViewID  uuid.UUID
	ViewKey string
	Name    string
	Kind    string
	RefKey  string
	Pointer string
}

// UpsertView creates or replaces a saved view, addressed by its key.
func (s *Service) UpsertView(ctx context.Context, projectID uuid.UUID, in ViewInput) (dbq.View, error) {
	problems := metamodel.RowKeyProblems(pointer("key"), in.Key)
	problems = append(problems, viewNameProblems(in.Name)...)
	problems = append(problems, oneValue(pointer("description"),
		checkStorableText(in.Description, MaxViewDescriptionLen, true))...)
	problems = append(problems, oneValue(pointer("layout_mode"),
		layoutModeProblem(in.LayoutMode))...)
	if len(problems) > 0 {
		return dbq.View{}, &metamodel.ValidationError{
			Code: metamodel.CodeInvalidInput, Fields: problems,
		}
	}

	query, err := ParseQuery(in.Query)
	if err != nil {
		return dbq.View{}, err
	}
	// One catalogue read serves both the resolution and the renderer
	// check, and it is this package's one scoped path to the game's
	// vocabulary: reading through it is what makes "every lookup is
	// filtered by the caller's project" one implementation rather than a
	// fourth copy.
	resolved, err := s.Resolve(ctx, projectID, query)
	if err != nil {
		return dbq.View{}, err
	}
	if err := CheckRenderer(in.Renderer, in.RendererParams, resolved); err != nil {
		return dbq.View{}, err
	}

	params, err := json.Marshal(paramsOrEmpty(in.RendererParams))
	if err != nil {
		// Unreachable through CheckRenderer, which admits only values
		// that came out of a JSON decoder or out of a Go caller building
		// the same shapes. Reported rather than ignored: a renderer_params
		// column silently written as `{}` would be Task 10's own defect —
		// a parameter stored and never read — arriving by another route.
		return dbq.View{}, fmt.Errorf("encode renderer_params: %w", err)
	}

	expected := noVersion
	if in.ExpectedVersion != nil {
		expected = *in.ExpectedVersion
	}
	mode := in.LayoutMode
	if mode == "" {
		mode = DefaultLayoutMode
	}

	var row dbq.View
	err = s.withTx(ctx, func(q *dbq.Queries) error {
		// Read under the row lock, so the spelling and the version this
		// caller is told about are the ones its own write will meet.
		existing, err := q.GetViewByKeyForUpdate(ctx, dbq.GetViewByKeyForUpdateParams{
			ProjectID: projectID, Key: in.Key,
		})
		switch {
		case err == nil:
			// Spelling before version, the order internal/metamodel and
			// internal/markdown both settled: a caller failing for two reasons at
			// once hears the one it can act on, rather than "current version is N"
			// over a key that would be refused again at the same version. The order
			// is what this check is *for*, and it is the only thing that makes it
			// load-bearing: the post-write check below catches a respelling whose
			// version matched, and the re-read after a failed guard catches one
			// whose version did not, so with a correct version in hand this branch
			// is redundant. Neither of those can choose which fault to report when a
			// caller has both, and being told "current version is N" over a key that
			// would be refused again at that version is a loop a caller cannot leave
			// by doing what the error said. TestViewsArea's "a respelling is named
			// even when the version is also stale" case pins it.
			if existing.Key != in.Key {
				return viewKeyRespellingError(in.Key, existing.Key)
			}
			// **Behaviourally redundant with the SQL guard, and kept**,
			// which is the same position internal/markdown reached for
			// the identical check: deleting these three lines leaves the
			// whole package green, because the guarded DO UPDATE refuses
			// on its own and conflictOnViewKey's re-read reports the same
			// current version this branch would have. What it earns is
			// that a caller already known to be wrong is turned away
			// before its query document, its refs and a version row are
			// built, sent and rolled back.
			if in.ExpectedVersion == nil || *in.ExpectedVersion != existing.Version {
				return &metamodel.VersionConflictError{Current: existing.Version}
			}
			// **The rule SetBackground enforces, on the other write path
			// over the same row.** Task 14's own correction established
			// that a background stored under a renderer that draws none
			// is a value nothing reads, and put Renderer.ReadsBackground
			// in the catalogue as the one statement of which renderers
			// read one — and then consulted it from exactly one of the
			// two writers of background_asset_id. This upsert never
			// touches that column, so changing the renderer from `map`
			// to anything else left the image attached and produced,
			// through the front door, the state the setter refuses.
			// The refusal there even named this call as the way to
			// change the renderer.
			if existing.BackgroundAssetID != nil && !RendererReadsBackground(in.Renderer) {
				return &metamodel.ValidationError{
					Code: metamodel.CodeInvalidInput,
					Fields: []metamodel.FieldError{{
						Path: pointer("renderer"),
						Message: fmt.Sprintf("is %q, which draws no background, and this "+
							"view has one attached: it would be stored and read by "+
							"nothing. Clear it first with views.set_background and a null "+
							"asset_id, then change the renderer — only %q reads a "+
							"background", in.Renderer, RendererMap),
					}},
				}
			}
		case errors.Is(err, pgx.ErrNoRows):
			// **No row, and a version claimed: the view was removed.**
			// This branch used to carry that defect as a recorded
			// observation: an update blocked behind a committed removal
			// found no row, took the creation path, and its
			// ExpectedVersion — asserted about a row that no longer
			// exists — was accepted by an insert with no version to
			// guard, storing the view again under a new id and undoing
			// the designer's deletion. The note said internal/metamodel's
			// type upsert had byte-identical structure and filed it as a
			// backlog item.
			if in.ExpectedVersion != nil && *in.ExpectedVersion != createExpectedVersion {
				return &metamodel.RemovedError{
					Subject: "view",
					Address: fmt.Sprintf("%q", in.Key),
					Claimed: *in.ExpectedVersion,
				}
			}
			// Creation: no version to match, nothing to lock.
		default:
			return fmt.Errorf("lock view: %w", err)
		}

		row, err = q.UpsertView(ctx, dbq.UpsertViewParams{
			ProjectID:        projectID,
			Key:              in.Key,
			Name:             in.Name,
			Description:      in.Description,
			Query:            in.Query,
			Renderer:         in.Renderer,
			RendererParams:   params,
			LayoutMode:       mode,
			ExpectedVersion:  expected,
			UpdatedByUserID:  in.Actor.UserID,
			UpdatedByTokenID: in.Actor.TokenID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// The guarded DO UPDATE matched nothing: between the locked
			// read above and this statement another writer created or
			// advanced the row.
			return conflictOnViewKey(ctx, q, projectID, in.Key)
		}
		if err != nil {
			// 0008_views.sql gives views the same composite
			// FOREIGN KEY (updated_by_token_id, project_id) as every table
			// in 0004_metamodel.sql, so a token scoped to another game
			// cannot be recorded as the editor of this one's view. Without
			// this arm that refusal reaches a log as the raw SQLSTATE
			// 23503 over a constraint name, which says nothing about a
			// token being scoped to the wrong game; the judgement is
			// metamodel's, shared rather than copied.
			if mapped := metamodel.ActorConstraintViolation(err); errors.Is(mapped, ErrActorNotInGame) {
				return mapped
			}
			return fmt.Errorf("upsert view: %w", err)
		}
		// **There is deliberately no `row.Key != in.Key` check here any
		// more.** It was live, and it was staged rather than argued:
		// TestViewsArea's "a creation racing a creator under another spelling is
		// told the spelling" case held an ExpectedVersion equal to the version
		// the winning creator landed on, passed both the locked read and the
		// guard, and updated a row it never saw under a spelling it never sent.
		refs := make([]TypeRef, 0, len(resolved.Refs))
		refs = append(refs, resolved.Refs...)
		refs = append(refs, rendererTypeRefs(in.Renderer, in.RendererParams, resolved.Cat)...)
		return writeRefs(ctx, q, projectID, row.ID, refs)
	})
	if err != nil {
		return dbq.View{}, err
	}

	s.publish(projectID, eventViewUpserted, viewEventMinRole, viewEventHumanOnly,
		viewEvent{ID: row.ID, Key: row.Key, Version: row.Version})
	return row, nil
}

// writeRefs replaces one view's dependency index with the list the
// resolve pass produced.
func writeRefs(ctx context.Context, q *dbq.Queries, projectID, viewID uuid.UUID,
	refs []TypeRef,
) error {
	if err := q.DeleteViewRefs(ctx, dbq.DeleteViewRefsParams{
		ProjectID: projectID, ViewID: viewID,
	}); err != nil {
		return fmt.Errorf("clear view refs: %w", err)
	}
	for _, ref := range refs {
		params := dbq.InsertViewRefParams{
			ViewID: viewID, ProjectID: projectID, Kind: ref.Kind,
			RefKey: ref.Key, Pointer: ref.Pointer,
		}
		// kind says which of the two id columns the row uses, and the
		// table's CHECK refuses a row that claims one kind and carries the
		// other's id. Written as a switch over the two constants rather
		// than as an if: a third kind added to resolve.go fails here
		// loudly instead of storing a row with neither id set, which would
		// read back as a dependency on a type that has been deleted.
		switch ref.Kind {
		case KindEntityType:
			params.EntityTypeID = ref.ID
		case KindRelationType:
			params.RelationTypeID = ref.ID
		default:
			return fmt.Errorf("views: a type reference of kind %q has no column in "+
				"view_refs", ref.Kind)
		}
		if err := q.InsertViewRef(ctx, params); err != nil {
			return fmt.Errorf("write view ref at %s: %w", ref.Pointer, err)
		}
	}
	return nil
}

// conflictOnViewKey re-reads a key whose guarded upsert matched no row
// and names what actually stands in the way. Both outcomes are real: the
// winning writer may have created the key under a different spelling, or
// advanced a version this caller was holding.
func conflictOnViewKey(ctx context.Context, q *dbq.Queries, projectID uuid.UUID, key string) error {
	row, err := q.GetViewByKey(ctx, dbq.GetViewByKeyParams{ProjectID: projectID, Key: key})
	if err != nil {
		return fmt.Errorf("re-read view after a failed upsert: %w", err)
	}
	if row.Key != key {
		return viewKeyRespellingError(key, row.Key)
	}
	return &metamodel.VersionConflictError{Current: row.Version}
}

// ViewByKey loads one view by its key, matched without regard to case,
// as every key in Maestro is.
func (s *Service) ViewByKey(ctx context.Context, projectID uuid.UUID, key string) (dbq.View, error) {
	row, err := s.q.GetViewByKey(ctx, dbq.GetViewByKeyParams{ProjectID: projectID, Key: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbq.View{}, fmt.Errorf("%w: no view %q in this game", ErrNotFound, key)
	}
	if err != nil {
		return dbq.View{}, fmt.Errorf("read view: %w", err)
	}
	return row, nil
}

// ViewByID loads one view by its id, inside this game and no other.
func (s *Service) ViewByID(ctx context.Context, projectID, id uuid.UUID) (dbq.View, error) {
	row, err := s.q.GetViewByID(ctx, dbq.GetViewByIDParams{ProjectID: projectID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbq.View{}, ErrNotFound
	}
	if err != nil {
		return dbq.View{}, fmt.Errorf("read view: %w", err)
	}
	return row, nil
}

// ViewRefs is one view's dependency list as stored, in document order.
func (s *Service) ViewRefs(ctx context.Context, projectID, viewID uuid.UUID) ([]dbq.ViewRef, error) {
	rows, err := s.q.ListViewRefs(ctx, dbq.ListViewRefsParams{
		ProjectID: projectID, ViewID: viewID,
	})
	if err != nil {
		return nil, fmt.Errorf("list view refs: %w", err)
	}
	return rows, nil
}

// ViewsDependingOn answers "which views does this type hold up, and
// where in each query", for one entity type or one relation type.
func (s *Service) ViewsDependingOn(ctx context.Context, projectID uuid.UUID,
	kind string, typeID uuid.UUID,
) ([]ViewDependency, error) {
	params := dbq.ListViewsBrokenByTypeParams{ProjectID: projectID}
	switch kind {
	case KindEntityType:
		params.EntityTypeID = &typeID
	case KindRelationType:
		params.RelationTypeID = &typeID
	default:
		return nil, fmt.Errorf("views: %q is not a kind of type a view can reference", kind)
	}
	rows, err := s.q.ListViewsBrokenByType(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list views depending on a type: %w", err)
	}
	out := make([]ViewDependency, 0, len(rows))
	for _, row := range rows {
		out = append(out, ViewDependency{
			ViewID: row.ID, ViewKey: row.Key, Name: row.Name,
			Kind: row.Kind, RefKey: row.RefKey, Pointer: row.Pointer,
		})
	}
	return out, nil
}

// ListViews returns one page of a game's saved views.
func (s *Service) ListViews(ctx context.Context, projectID uuid.UUID, f ViewFilter) (ViewPage, error) {
	limit := paging.Size(f.Limit, defaultViewPage, maxViewPage)
	fingerprint := viewListingFingerprint(projectID, f)
	after, err := paging.Decode(f.Cursor, fingerprint, refuseViewCursor)
	if err != nil {
		return ViewPage{}, err
	}

	params := dbq.ListViewsPageParams{ProjectID: projectID, Limit: limit}
	if f.Renderer != "" {
		renderer := f.Renderer
		params.Renderer = &renderer
	}
	if after.ID != uuid.Nil {
		params.AfterID = &after.ID
		params.AfterName = &after.Sort
	}

	rows, err := s.q.ListViewsPage(ctx, params)
	if err != nil {
		return ViewPage{}, fmt.Errorf("list views: %w", err)
	}
	page := ViewPage{Views: rows}
	// paging.Size never returns a limit below one, so a full page is
	// never an empty one and there is no separate emptiness check.
	if len(rows) == int(limit) {
		last := rows[len(rows)-1]
		page.NextCursor = paging.Encode(paging.Cursor{
			Sort: last.Name, ID: last.ID, Fingerprint: fingerprint,
		})
	}
	return page, nil
}

// viewListingFingerprint digests the listing a cursor was issued under.
func viewListingFingerprint(projectID uuid.UUID, f ViewFilter) string {
	return paging.Fingerprint(projectID.String(), "views", f.Renderer)
}

// refuseViewCursor turns paging's message into this package's own error,
// at the argument's own path.
func refuseViewCursor(message string) error {
	return &metamodel.ValidationError{
		Code:   metamodel.CodeInvalidInput,
		Fields: []metamodel.FieldError{{Path: pointer("cursor"), Message: message}},
	}
}

// RemoveView deletes a view, and with it the positions a human dragged
// and the refs the upsert wrote: both key into views with ON DELETE
// CASCADE, so this is one statement rather than three.
func (s *Service) RemoveView(ctx context.Context, projectID uuid.UUID, key string) error {
	row, err := s.ViewByKey(ctx, projectID, key)
	if err != nil {
		return err
	}
	// Deleted by id rather than by key, so the row removed is the row
	// that was read: a key is folded and a second writer could have
	// replaced it between the two statements. Both are filtered on the
	// project regardless.
	affected, err := s.q.DeleteView(ctx, dbq.DeleteViewParams{ProjectID: projectID, ID: row.ID})
	if err != nil {
		return fmt.Errorf("delete view: %w", err)
	}
	if affected == 0 {
		// Another remover won the race between the read and the delete.
		// not_found rather than a silent success: nothing was removed by
		// this call, so announcing a removal would put an event on the
		// hub for a change this caller did not make.
		return fmt.Errorf("%w: no view %q in this game", ErrNotFound, key)
	}
	s.publish(projectID, eventViewRemoved, viewEventMinRole, viewEventHumanOnly,
		viewEvent{ID: row.ID, Key: row.Key, Version: row.Version})
	return nil
}

// paramsOrEmpty keeps a nil map out of the column.
func paramsOrEmpty(params map[string]any) map[string]any {
	if params == nil {
		return map[string]any{}
	}
	return params
}

// viewNameProblems bounds the one string on a view that a listing
// depends on.
func viewNameProblems(name string) []metamodel.FieldError {
	if name == "" {
		return []metamodel.FieldError{{
			Path: pointer("name"),
			Message: "is required: it is what a designer reads in the view list, and the " +
				"listing is ordered by it",
		}}
	}
	return oneValue(pointer("name"), checkStorableText(name, MaxViewNameLen, false))
}

// layoutModeProblem judges the layout mode, empty being "use the
// default" rather than a value.
func layoutModeProblem(mode string) string {
	if mode == "" {
		return ""
	}
	for _, admitted := range layoutModes {
		if mode == admitted {
			return ""
		}
	}
	return fmt.Sprintf("must be one of %q, %q or %q, or left out for %q",
		LayoutAuto, LayoutManual, LayoutMixed, DefaultLayoutMode)
}

// checkStorableText is this package's wording over metamodel.CheckText's
// judgement, for the two pieces of prose a view carries. The judgement
// is shared so that Maestro does not grow a seventh copy of the scan;
// only the wording and the allowance are decided here.
func checkStorableText(value string, max int, allowParagraphs bool) string {
	if problem := metamodel.LengthProblem(value, max); problem != "" {
		return problem
	}
	allowed := ""
	if allowParagraphs {
		allowed = "\n\t"
	}
	fault, bad := metamodel.CheckText(value, allowed)
	switch {
	case !bad:
		return ""
	case fault.InvalidUTF8:
		return "is not valid UTF-8: a byte in it does not decode as any character, " +
			"and Postgres refuses that outright"
	default:
		line := "this is one line of text"
		if allowParagraphs {
			line = "only a newline or a tab is allowed here"
		}
		return fmt.Sprintf("holds a control character (%U at byte %d): %s",
			fault.Rune, fault.Offset, line)
	}
}

// oneValue turns a "" means fine" judgement into the problem list every
// pass in this package collects into, so a caller with three bad
// arguments hears about all three.
func oneValue(path, message string) []metamodel.FieldError {
	if message == "" {
		return nil
	}
	return []metamodel.FieldError{{Path: path, Message: message}}
}

// viewKeyRespellingError is the metamodel's respelling refusal in this
// package's own address style: keys are matched without regard to case,
// so a second spelling of a stored key is a rewrite of a handle rather
// than a new view, and it is refused rather than silently ignored.
func viewKeyRespellingError(requested, stored string) error {
	return &metamodel.ValidationError{
		Code: metamodel.CodeInvalidInput,
		Fields: []metamodel.FieldError{{
			Path: pointer("key"),
			Message: fmt.Sprintf(
				"%q already exists here spelled %q, and keys are matched without regard "+
					"to case: use %q to update it, or pick a key that differs by more "+
					"than capitalisation", requested, stored, stored),
		}},
	}
}
