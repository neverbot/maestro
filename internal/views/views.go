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
//
// The two lengths are internal/metamodel's, deliberately: a view's name
// is the same kind of value as a type's label — one line in a picker,
// ordered by a listing — and its description is the same kind of value
// as a type's description. Matching them means a designer does not meet
// two different caps for the same shape of text in two places of one
// product.
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
//
// **Checked in Go rather than left to the CHECK**, for the reason every
// bound in this sub-project is checked before Postgres sees it: a
// constraint violation comes back as `new row for relation "views"
// violates check constraint (SQLSTATE 23514)`, untyped, over a value the
// caller itself supplied, and reaches an agent as internal_error. What
// the CHECK is for is a write path that does not come through here.
//
// What each mode means is a contract with the *client* and no server
// code reads the value beyond validating and returning it;
// 0008_views.sql states the three meanings where the column is declared.
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
	// DefaultLayoutSeed is 0008_views.sql's column default, spelled here
	// for the same reason. It is not 0: an unseeded force layout draws a
	// different diagram every load, and a seed of zero is a legal seed a
	// caller may deliberately choose, which is why LayoutSeed is a
	// pointer rather than an int32 whose zero value means "unset".
	DefaultLayoutSeed int32 = 1
)

// layoutModes is the closed list, in the order a refusal names them.
var layoutModes = []string{LayoutAuto, LayoutManual, LayoutMixed}

// noVersion is the expected_version an upsert passes when its caller has
// no version to expect. Versions start at 1 and only ever climb, so no
// stored row can equal it: the guarded DO UPDATE is then a no-op on the
// insert path and a guaranteed mismatch if a row turns out to exist
// after all. The same value and the same argument as
// internal/metamodel's; a view is addressed by (project, key) exactly as
// a type is, so the shapes are the same shape rather than two.
const noVersion int32 = -1

// ViewInput is one saved-view upsert, addressed by its key.
//
// Query is the document **as the caller wrote it**, bytes and all, and
// that is what gets stored: a rename never rewrites it (Task 12), so
// what is read back is what was written. It is parsed and resolved
// before anything is stored, and the refs written beside it are the ones
// that pass returned — see UpsertView, which is where the difference
// between a parsed query and a resolved one is spent.
//
// RendererParams is a free map judged entirely by CheckRenderer: an
// unknown name is refused, and every declared one is judged by its
// kind's checker. That is also what bounds it — a caller cannot store a
// megabyte under a name no renderer takes — so this package applies no
// second size rule of its own.
//
// ExpectedVersion must match the stored version when the view already
// exists; a nil ExpectedVersion against an existing view is a conflict,
// not an overwrite. On creation there is nothing to match, and the field
// is still passed as the guard on the DO UPDATE, because a caller that
// believes it is creating may be racing a creator — the whole argument
// is at metamodel.EntityTypeInput and is not restated here.
//
// LayoutMode and LayoutSeed are optional: an empty mode is
// DefaultLayoutMode and a nil seed is DefaultLayoutSeed. A pointer for
// the seed because 0 is a seed a caller may mean.
type ViewInput struct {
	Key             string
	Name            string
	Description     string
	Query           []byte
	Renderer        string
	RendererParams  map[string]any
	LayoutMode      string
	LayoutSeed      *int32
	ExpectedVersion *int32
	Actor           Actor
}

// ViewFilter narrows a view listing.
//
// Renderer is an exact match on the stored renderer name and is empty
// for "every renderer". It is deliberately not resolved or folded: a
// renderer name is one of a fixed catalogue of lower-case identifiers
// this package itself writes, not a game's own key, so there is no
// second spelling for a fold to reconcile.
//
// Cursor is the NextCursor of a previous call. It belongs to the game
// and the filter it was issued for and to no other, and it is a position
// rather than a snapshot; paging.Cursor carries the whole contract.
type ViewFilter struct {
	Renderer string
	Cursor   string
	Limit    int32
}

// ViewPage is one page of saved views plus the cursor for the next.
//
// **NextCursor is set when the page came back full**, and empty
// otherwise, so a caller looping until it is empty is correct and must
// expect a final empty page rather than treating one as an error. The
// rest of the contract — what a position buys under concurrent editing,
// why a cursor cannot be carried to another listing or another game, and
// why nothing signs it — is paging.Cursor's, stated once for every
// domain that pages. Cursor.Sort, for this listing, is the view's name.
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
//
// **Four passes, each answering with its own code, in the order a caller
// can act on them**, which is the order this package already refuses a
// query in (ParseQuery's structural pass, then its limits, then
// resolution): the row's own arguments, then the query document, then
// its resolution against this game's vocabulary, then the renderer
// against the resolved query. A caller whose name is empty and whose
// query names a type this game does not have hears about the name
// first — every pass but the first needs a document that parsed, and a
// FieldError list carries one code.
//
// **What is stored is what resolution returned, never what ParseQuery
// alone returned**, and this is the task where the difference is spent.
// A parsed query is structurally bounded and semantically unjudged: no
// key names anything, no operator is known to suit its field, and a
// parameter default is not yet known to be a scalar of its declared
// type. Storing one would be storing a document with no guarantee that
// it answers any question — and view_refs, which is computed from the
// *resolved* form, would have nothing to be computed from. So Resolve
// runs before the write and its Refs are what the transaction writes.
//
// **CheckRenderer is called rather than its rules restated.** A view
// that passes save-time checking and cannot then be drawn is the failure
// the renderer catalogue exists to prevent, and Task 10 found that
// failure three times in one file because a rule had been written once
// and copied. There is one caller of that check and this is it.
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
	seed := DefaultLayoutSeed
	if in.LayoutSeed != nil {
		seed = *in.LayoutSeed
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
			// internal/markdown both settled: a caller failing for two
			// reasons at once hears the one it can act on, rather than
			// "current version is N" over a key that would be refused
			// again at the same version.
			// The order is what this check is *for*, and it is the only
			// thing that makes it load-bearing: the post-write check
			// below catches a respelling whose version matched, and the
			// re-read after a failed guard catches one whose version did
			// not, so with a correct version in hand this branch is
			// redundant. Neither of those can choose which fault to
			// report when a caller has both, and being told "current
			// version is N" over a key that would be refused again at
			// that version is a loop a caller cannot leave by doing what
			// the error said.
			// TestARespellingIsNamedEvenWhenTheVersionIsAlsoStale pins it.
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
		case errors.Is(err, pgx.ErrNoRows):
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
			LayoutSeed:       seed,
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
		// The locked read cannot be the only place the spelling is
		// checked, and the hole it leaves is the creation path, where
		// there was nothing to lock: a writer racing a creator, holding
		// an ExpectedVersion that happens to equal the version the winner
		// landed on, passes both the read and the guard and updates a row
		// it never saw, stored under a different spelling. The statement
		// keeps the stored key — `key` is not in its SET list — so
		// comparing the returned spelling to the submitted one closes it,
		// and withTx rolls the write back.
		//
		// **That race is staged rather than argued**, because the same
		// claim was made in internal/markdown for a check that turned out
		// to be unreachable there: with expected_version 0 spelling a
		// create, the loser always falls to the failed-guard arm instead.
		// Here a version is a claim about a row rather than a claim to be
		// creating one, so the arm is live —
		// TestACreationRacingACreatorUnderAnotherSpellingIsToldTheSpelling
		// stages it, and deleting these three lines makes that test
		// report a nil error over a silently overwritten row.
		if row.Key != in.Key {
			return viewKeyRespellingError(in.Key, row.Key)
		}
		// **The refs are rewritten inside this transaction**, against the
		// row this statement just wrote. Outside it they would drift from
		// the query they index, which is the one thing they exist not to
		// do: a view whose query landed and whose refs did not would
		// report, on the next type deletion, that nothing broke.
		return writeRefs(ctx, q, projectID, row.ID, resolved.Refs)
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
//
// Delete-then-insert rather than a diff: the list is small (one entry
// per type reference in one query), the pointers move whenever the
// document's shape moves, and a diff would be a second place that
// decides what a reference is. The unique index on (view_id, pointer) is
// what makes a duplicate a refusal rather than a silent second row, so
// nothing here swallows one.
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
//
// A missing key is named rather than reported as a bare sentinel,
// because the caller supplied this key and it is the one thing it can
// act on. **ViewByID deliberately keeps the bare sentinel**: a caller
// addressing a row by id already holds the id it sent, and there is no
// second argument for it to tell apart — the same split
// internal/metamodel draws between EntityTypeByKey and EntityTypeByID.
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
//
// It reads by view id, and the id is scoped by the same project filter
// every other statement in this domain carries: a ref list is a
// description of a game's own vocabulary and a leaked view id is not
// authority to read it.
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
//
// **It must be asked before the type is deleted.** ON DELETE SET NULL is
// what makes a ref row survive its type — with its key text intact, so
// Task 12 can say what the query used to name — and the same SET NULL
// empties the column this lookup matches on. Asked afterwards it finds
// nothing and reports that nothing broke, which is a wrong answer rather
// than an error; the deletion that wants the list asks inside its own
// transaction, before the delete.
//
// kind is KindEntityType or KindRelationType. An unknown one is a
// programming error rather than a caller's, and is refused rather than
// answered with an empty list, which would read as "nothing depends on
// it".
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
//
// **A page is a position, not a snapshot**; paging.Cursor records what
// that means while the game is being edited underneath the caller, and
// ViewPage records what a cursor is set for.
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
//
// **The project id is first and the domain discriminator is second**,
// and neither is decoration. Without the project id two games'
// unfiltered listings share a fingerprint, and one game's cursor pages
// the other's rows from a position that means nothing there — the defect
// internal/paging's package comment records, which was fixed in one
// listing and left standing in another. Without "views", a cursor from
// this listing and one from the metamodel's entity listing over the same
// game and an empty filter would be interchangeable, and each would page
// the other perfectly and answer a different question.
//
// It is asserted compositionally as well as behaviourally, by
// TestTheViewListingFingerprintIsProjectIdFirstAndCarriesItsDomain,
// because the behavioural test can pass without the project id whenever
// another part discriminates — here the renderer filter does — and that
// is exactly how the original defect survived its first test.
func viewListingFingerprint(projectID uuid.UUID, f ViewFilter) string {
	return paging.Fingerprint(projectID.String(), "views", f.Renderer)
}

// refuseViewCursor turns paging's message into this package's own error,
// at the argument's own path.
//
// invalid_input rather than a bare error: the cursor is the caller's own
// argument and the recovery is the caller's — page from a cursor a
// previous call returned, or omit it. Left untyped it would reach an
// agent as internal_error over a value the agent itself supplied. The
// *sentence* lives in internal/paging so that this domain and the two
// that already page cannot tell a caller three different things about
// one bad cursor.
func refuseViewCursor(message string) error {
	return &metamodel.ValidationError{
		Code:   metamodel.CodeInvalidInput,
		Fields: []metamodel.FieldError{{Path: pointer("cursor"), Message: message}},
	}
}

// RemoveView deletes a view, and with it the positions a human dragged
// and the refs the upsert wrote: both key into views with ON DELETE
// CASCADE, so this is one statement rather than three.
//
// It takes no expected version, and that is a decision. A view is
// derived content — the query is stored, the picture is not — so the
// cost of removing one somebody else had just edited is one re-upsert
// from a document the caller already holds, while requiring a version
// would make every removal a read-then-write a designer has to retry
// against churn they do not care about. The metamodel makes the same
// choice for its own removals.
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
//
// encoding/json renders a nil map as `null`, and `null::jsonb` is a
// legal jsonb value that is not an object: every later reader — Task
// 15's tool, a renderer reading its parameters back — would then have to
// handle two spellings of "no parameters". The same rule Task 6 applied
// to an empty result envelope, applied to the one column of this domain
// that can be empty.
func paramsOrEmpty(params map[string]any) map[string]any {
	if params == nil {
		return map[string]any{}
	}
	return params
}

// viewNameProblems bounds the one string on a view that a listing
// depends on.
//
// Empty is refused, for the reason internal/metamodel refuses an empty
// label: ListViewsPage orders by name, so an unnamed view sorts to the
// front of every list a designer sees and identifies itself by nothing.
// A key is not a substitute — the key is the handle, the name is what a
// human reads.
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
//
// allowParagraphs is the one asymmetry: a description is free-form prose
// a designer writes about a picture and a newline in it is their own
// paragraph break, while a name is rendered as one line in a picker and
// a newline there is refused exactly like any other control character.
func checkStorableText(value string, max int, allowParagraphs bool) string {
	if len(value) > max {
		return fmt.Sprintf("must be at most %d bytes, and this one is %d", max, len(value))
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
