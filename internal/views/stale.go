package views

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
)

// This file is staleness: what happens to a saved view when the game it
// was written against moves on.
//
// **Stale views are the normal case, not the exception.** A game's
// vocabulary keeps moving for as long as the game is being designed, so a
// view written in one month references types renamed in another. Nothing
// here treats that as a fault; it is the working condition of the
// product, and the design question is only what a run does about it.
//
// Resolution at execution time goes, per reference:
//
//  1. **By id.** A renamed type still resolves, because a rename does not
//     change its id. The reference is live and the picture is right; what
//     is stale is the spelling in the document.
//  2. **By key**, when the id is null. ON DELETE SET NULL empties the id
//     column and leaves the key text standing, so a type deleted and
//     re-created under the same key resolves — the common shape of a
//     designer fixing a mistake, and one that would otherwise report a
//     working view as broken.
//  3. Neither: the reference is dead.
//
// That order is implemented **once**, in staleness.entityTypeAt and
// staleness.relationTypeAt, and every position in the query that names a
// type goes through it: the closures resolveInto hands to itself, the
// projection's own scope, and the relation types an edges[] entry
// inherits from the step it draws. A position that resolved by key alone
// would be a position where a rename silently loses the type.
//
// **A rename does not rewrite the stored query.** Resolution succeeds by
// id, the key text is left exactly as the author wrote it, and the run
// reports `*_renamed`. Rewriting the stored key would edit an author's
// document underneath them without a version bump, which makes optimistic
// concurrency lie: the next expected_version check would pass against a
// document nobody wrote. Repair is an explicit views.upsert.

// The eight diagnostic codes, which are eight promises. Each one is
// reachable, each one is read, and each one is produced by the same pass
// that makes the judgement it reports — a second walk over the document
// would be a second implementation of every rule, and the first thing it
// would drift on is which of them counts as staleness.
const (
	// DiagEntityTypeMissing: the query names an entity type that resolved
	// neither by id nor by key.
	DiagEntityTypeMissing = "entity_type_missing"
	// DiagRelationTypeMissing: the same, for a relation type.
	DiagRelationTypeMissing = "relation_type_missing"
	// DiagEntityTypeRenamed: the reference resolved by id and the game
	// spells it differently now. The view runs.
	DiagEntityTypeRenamed = "entity_type_renamed"
	// DiagRelationTypeRenamed: the same, for a relation type.
	DiagRelationTypeRenamed = "relation_type_renamed"
	// DiagFieldMissing: a field key the query compares or projects is no
	// longer declared where the query needs it.
	DiagFieldMissing = "field_missing"
	// DiagFieldTypeChanged: the field is declared, and no longer as the
	// query needs it — a different type, an operator that type does not
	// answer, or two types in scope that now declare it differently.
	DiagFieldTypeChanged = "field_type_changed"
	// DiagEnumOptionMissing: an enum field is compared against an option
	// its declaration no longer carries.
	DiagEnumOptionMissing = "enum_option_missing"
	// DiagParamUnbound: a declared parameter with no default was given no
	// value for this run, so the leaf that reads it has nothing to
	// compare against.
	DiagParamUnbound = "param_unbound"
)

// The two on_stale policies.
//
// **fail is the default**, and it is the decision to defend: a diagram
// that silently dropped its level filter looks exactly like a correct
// diagram, and a designer will believe it. A wrong picture is worse than
// no picture.
//
// best_effort exists because sometimes seeing most of the graph is what
// you need. It is the designer's explicit choice, and what it dropped
// comes back in Result.Stale for the UI to band across the top.
const (
	OnStaleFail       = "fail"
	OnStaleBestEffort = "best_effort"
)

// Diagnostic is one thing a query says that this game no longer has, or
// no longer spells that way.
//
// Pointer is the JSON pointer into the stored query document, the same
// address every refusal in this package carries: an agent told
// `/traverse/0/via/0` knows which of five steps to rewrite, and no amount
// of prose gets it there as reliably.
//
// Was and Now are read one way throughout: **Was is what the document
// says, Now is what the game says today**, and Now is empty when the game
// says nothing. So a rename carries the old key and the new one, a
// missing type carries the key and nothing, a changed field carries the
// key and the type it is declared as now, and a vanished enum option
// carries the option and nothing.
type Diagnostic struct {
	Code    string `json:"code"`
	Pointer string `json:"pointer"`
	Was     string `json:"was,omitempty"`
	Now     string `json:"now,omitempty"`
}

// message is the sentence this diagnostic reaches an agent as when a run
// refuses. It says what moved and what to do, because "entity_type_missing"
// on its own is a code an agent has to look up.
func (d Diagnostic) message() string {
	switch d.Code {
	case DiagEntityTypeMissing:
		return fmt.Sprintf("this view names the entity type %q and this game no longer has it: "+
			"declare it again with types.upsert, point this reference at another type, or run "+
			"with on_stale %q to draw what is left", d.Was, OnStaleBestEffort)
	case DiagRelationTypeMissing:
		return fmt.Sprintf("this view names the relation type %q and this game no longer has "+
			"it: declare it again with relation_types.upsert, point this reference at another "+
			"type, or run with on_stale %q to draw what is left", d.Was, OnStaleBestEffort)
	case DiagEntityTypeRenamed:
		return fmt.Sprintf("the entity type this view calls %q is now keyed %q: the view runs "+
			"unchanged, and views.upsert with the new spelling is how it stops being reported",
			d.Was, d.Now)
	case DiagRelationTypeRenamed:
		return fmt.Sprintf("the relation type this view calls %q is now keyed %q: the view runs "+
			"unchanged, and views.upsert with the new spelling is how it stops being reported",
			d.Was, d.Now)
	case DiagFieldMissing:
		return fmt.Sprintf("the field %q is no longer declared where this view reads it", d.Was)
	case DiagFieldTypeChanged:
		if d.Now == "" {
			return fmt.Sprintf("the field %q is no longer declared the same way everywhere this "+
				"view reads it", d.Was)
		}
		return fmt.Sprintf("the field %q is declared %s now, which is not what this view asks "+
			"of it", d.Was, d.Now)
	case DiagEnumOptionMissing:
		return fmt.Sprintf("%q is no longer one of the options the enum this view compares "+
			"declares", d.Was)
	case DiagParamUnbound:
		return fmt.Sprintf("the parameter %q has no value for this run: give it a default in "+
			"params, or supply it when running the view", d.Was)
	}
	// Unreachable while diagnosticCodes is the whole vocabulary, which
	// TestEveryDiagnosticCodeHasASentenceAndIsReachable holds both ways.
	return d.Code
}

// diagnosticCodes is the whole vocabulary, in the order this file
// declares it. It exists so a guard can iterate the codes rather than
// repeat a list a ninth code would silently fall out of.
func diagnosticCodes() []string {
	return []string{
		DiagEntityTypeMissing, DiagRelationTypeMissing,
		DiagEntityTypeRenamed, DiagRelationTypeRenamed,
		DiagFieldMissing, DiagFieldTypeChanged,
		DiagEnumOptionMissing, DiagParamUnbound,
	}
}

// **What makes a diagnostic fatal is not written here**, and that is
// deliberate. The obvious version is a predicate over the code — the two
// renames are survivable, the other six are not — and it would be a
// second statement of something resolution already says: a reference
// that resolved reports no problem, and a reference that did not reports
// one at its own pointer. runStored prunes and refuses on *those*
// pointers, so the two can never disagree; a code-based predicate could,
// the first time a code is produced beside a reference that still
// resolved.

// staleness is the stored dependency index of one saved view, plus the
// report the resolution pass builds as it walks that view's query.
//
// **A nil *staleness is the ad-hoc case and every method here is nil-safe
// for it.** An inline query has no recorded past: nothing to resolve by
// id, and nothing that can have moved. Making the zero case nil rather
// than an empty struct is what keeps resolveInto one pass instead of two,
// with the ordinary path taking exactly the branches it took before.
type staleness struct {
	// stored is view_refs keyed by pointer, which view_refs_key makes
	// unique per view — one reference per position, so a pointer is the
	// whole address.
	stored map[string]dbq.ViewRef
	diags  []Diagnostic
}

// storedID is the id this view recorded at this position for this kind of
// type, or nil: no such reference, a reference of the other kind, a
// reference that does not describe this document, or a reference whose
// type has since been deleted (ON DELETE SET NULL).
//
// **The key comparison is what makes the id safe to follow.** RunView
// reads the document and the index in two separate statements, with no
// transaction and no version check pairing them; Task 11 writes them in
// one transaction, but a read does not. An upsert committing between the
// two hands a run document version N beside refs version N+1, and every
// ref whose pointer survived the edit would then redirect by id to the
// type the *new* document names — the wrong picture, returned green, and
// described as a rename. Requiring the ref's own key to be the key the
// document spells here closes that: the two are written from the same
// document, so a disagreement means the index is not this document's.
//
// It costs the rename path nothing, which is the reason it can be
// unconditional. **Neither the document nor the ref row is rewritten by a
// rename** — that is this file's header decision — so after one they still
// agree with each other on the old spelling and only the catalogue has
// moved. A mismatch is never a rename; it is an index describing a
// document this run is not holding, and falling through to the by-key
// lookup answers from the document alone.
//
// **It is therefore a constraint on the rename operation this package was
// built before.** Such an operation may move a type's key freely; what it
// may not do is tidy `view_refs.ref_key` to the new spelling while
// leaving the stored documents alone, because that is precisely the
// disagreement this check reads as a torn index — and every renamed view
// would fall to the by-key lookup and report its type missing. The two
// spellings move together or neither moves.
func (st *staleness) storedID(kind, ptr, key string) *uuid.UUID {
	if st == nil {
		return nil
	}
	ref, ok := st.stored[ptr]
	if !ok || ref.Kind != kind || !strings.EqualFold(ref.RefKey, key) {
		return nil
	}
	if kind == KindEntityType {
		return ref.EntityTypeID
	}
	return ref.RelationTypeID
}

// entityTypeAt resolves one entity type reference: by id, then by key.
//
// note says whether this position is the one that *reports* a rename. It
// is true exactly at the positions that also record a TypeRef, and false
// where the same pointer is resolved a second time to build a scope —
// a diagnostic reported twice is a designer told to repair one thing
// twice.
func (st *staleness) entityTypeAt(cat *Catalogue, ptr, key string, note bool) (dbq.EntityType, bool) {
	if id := st.storedID(KindEntityType, ptr, key); id != nil {
		if row, ok := cat.entityTypesByID[*id]; ok {
			if note && !strings.EqualFold(row.Key, key) {
				st.note(DiagEntityTypeRenamed, ptr, key, row.Key)
			}
			return row, true
		}
	}
	row, ok := cat.EntityTypes[strings.ToLower(key)]
	return row, ok
}

// relationTypeAt is entityTypeAt for a relation type.
func (st *staleness) relationTypeAt(cat *Catalogue, ptr, key string,
	note bool,
) (dbq.RelationType, bool) {
	if id := st.storedID(KindRelationType, ptr, key); id != nil {
		if row, ok := cat.relationTypesByID[*id]; ok {
			if note && !strings.EqualFold(row.Key, key) {
				st.note(DiagRelationTypeRenamed, ptr, key, row.Key)
			}
			return row, true
		}
	}
	row, ok := cat.RelationTypes[strings.ToLower(key)]
	return row, ok
}

func (st *staleness) note(code, ptr, was, now string) {
	if st == nil {
		return
	}
	st.diags = append(st.diags, Diagnostic{Code: code, Pointer: ptr, Was: was, Now: now})
}

// noteScope turns a scope's own refusal into the diagnostic it already
// knows itself to be. The judgement is made once, where it belongs — in
// fieldScope.field and projectionScope.declares — and this reads the code
// off it rather than deciding a second time from the message text.
func (st *staleness) noteScope(err error, ptr, key string) {
	if st == nil {
		return
	}
	var se *scopeError
	if errors.As(err, &se) && se.code != "" {
		st.note(se.code, ptr, key, se.now)
	}
}

// noteOperand classifies a literal this field can no longer be compared
// with. An enum's own refusal is a vanished option — the only way a
// string fails an enum's validation is by not being one of its options —
// and everything else is the field's declared type having moved.
func (st *staleness) noteOperand(typ metamodel.FieldType, raw any, ptr, key string) {
	if st == nil {
		return
	}
	if typ == metamodel.FieldEnum {
		if option, ok := raw.(string); ok {
			st.note(DiagEnumOptionMissing, ptr, option, "")
			return
		}
	}
	st.note(DiagFieldTypeChanged, ptr, key, string(typ))
}

// noteUnboundParams reports every leaf whose parameter this run has no
// value for.
//
// It is here rather than left to the compiler's own refusal because a
// stale run has to *report* what it cannot do before deciding whether to
// refuse: the compiler's version raises the first one it meets and stops,
// which is a whole-document pass giving a one-problem answer, and under
// best_effort it would abort a picture that could still be drawn without
// that leaf's set. The compiler's refusal stays where it is, unchanged,
// for every ad-hoc run — the two agree on which leaves are unbound
// because both ask the same question of the same bound map.
func (st *staleness) noteUnboundParams(r *Resolved, bound map[string]any) []string {
	if st == nil {
		return nil
	}
	var pointers []string
	var walk func(p *ResolvedPredicate)
	walk = func(p *ResolvedPredicate) {
		if p == nil {
			return
		}
		for i := range p.All {
			walk(&p.All[i])
		}
		for i := range p.Any {
			walk(&p.Any[i])
		}
		walk(p.Not)
		if p.Leaf == nil {
			return
		}
		ref, ok := p.Leaf.Value.(ParamRef)
		if !ok {
			return
		}
		if _, has := bound[ref.Key]; has {
			return
		}
		at := p.Leaf.Pointer + "/value"
		st.note(DiagParamUnbound, at, ref.Key, "")
		pointers = append(pointers, at)
	}
	for i := range r.Sets {
		walk(r.Sets[i].Where)
	}
	for i := range r.Steps {
		walk(r.Steps[i].Where)
		walk(r.Steps[i].EdgeWhere)
	}
	return pointers
}

// scopeError is a refusal from a field scope that also carries the
// staleness diagnostic it *is*.
//
// The code travels on the error rather than being inferred from the
// pointer or matched out of the message, because both of those are a
// second copy of a judgement this package already makes: fieldScope.field
// is the one place that knows the difference between "no type in scope
// declares this key" and "two of them declare it differently", and a
// reader of its sentence would have to work that out again. A code of ""
// is a refusal that is not staleness — an open scope, or a scope built
// from types that themselves failed to resolve, whose missing type is
// already reported at its own pointer.
type scopeError struct {
	code string
	now  string
	msg  string
}

func (e *scopeError) Error() string { return e.msg }

// staleScope builds one, with the message formatted exactly as the plain
// fmt.Errorf it replaced.
func staleScope(code, now, format string, args ...any) error {
	return &scopeError{code: code, now: now, msg: fmt.Sprintf(format, args...)}
}

// resolveCtx is what a predicate needs beyond its own scope: the
// staleness sink, and the two closures that turn a type key into a row
// and record the dependency.
//
// **It is what makes an @type operand a dependency**, which is the
// decision Task 4 deferred to the compiler and Task 6 deferred to here.
// `@type eq "quest"` holds the entity type `quest` up exactly as
// `from[0].type` does — the compiler refuses the view outright when that
// key names nothing — so leaving it out of view_refs meant deleting the
// type reported that nothing broke, and renaming it broke a view that
// every other reference in this file would have carried through. The
// operand is resolved by the same three steps as every other reference
// and is **rewritten in the resolved leaf to the type's current key**, so
// the compiler's own lookup finds it after a rename. The stored document
// is untouched: rewriting happens in the *Resolved a run holds, not in
// the jsonb column.
type resolveCtx struct {
	st           *staleness
	entityType   func(ptr, key string) *dbq.EntityType
	relationType func(ptr, key string) *dbq.RelationType
}

// typeOperand resolves an @type operand and hands back the key to
// compile against; every other leaf's value passes through untouched.
//
// Only eq, neq and in take this road. The pattern operators ask about the
// *spelling* of a key rather than about a type, compile to a subquery
// over the key text, and would be a dependency on a string rather than on
// a declared thing — Task 6's review recorded that split, and it stands.
//
// **The three operators are treated alike, and neq is the one where that
// costs something.** Deleting the type an `eq` or an `in` names really
// does change the picture: those narrow to it, and once it is gone they
// select nothing. A negation does not — nothing is of a type this game no
// longer has, so "type is not X" selects exactly what it selected before
// — and reporting the view broken there is a delete a designer is asked
// to reconsider for a picture that would not have moved. That asymmetry
// is real, it was measured, and the reference is still a hard dependency
// on purpose:
//
//   - **The no-op is an accident of today's rows, not a property of the
//     document.** Resolution's own step 2 exists because deleting a type
//     and re-declaring it under the same key is the common shape of a
//     designer fixing a mistake — and the moment that happens the
//     negation narrows again, with no version bump and nothing said. A
//     reference that is dead now and live again on Tuesday is exactly
//     what a stale report is for.
//   - **The alternative puts a second rule in this function.** What makes
//     an operand a reference would then depend on the operator twice, on
//     two different axes — spelling versus thing, and narrowing versus
//     widening — and the second axis is where the first drift would be.
//   - **The deletion report would have to promise something harder.**
//     Today it lists the views that *name* the type, which view_refs can
//     answer exactly; exempting negations makes it "the views whose
//     picture changes", which no index can answer.
//
// TestANegatedTypeComparisonIsADependencyLikeAnyOther pins it, so the
// asymmetry is a decision on the record rather than something nobody
// noticed.
func (rc *resolveCtx) typeOperand(scope fieldScope, leaf *ResolvedLeaf, ptr string,
	value any,
) (any, bool) {
	if !leaf.Field.Builtin || leaf.Field.Key != AttrType {
		return value, true
	}
	switch leaf.Op {
	case OpEq, OpNeq, OpIn:
	default:
		return value, true
	}
	key, ok := value.(string)
	if !ok {
		// The compiler refuses a non-string operand with its own message,
		// naming the same pointer. Reachable only from a hand-built
		// *Resolved, since coerceOperand has already made this text.
		return value, true
	}
	if scope.edge {
		row := rc.relationType(ptr, key)
		if row == nil {
			return nil, false
		}
		return row.Key, true
	}
	row := rc.entityType(ptr, key)
	if row == nil {
		return nil, false
	}
	return row.Key, true
}

// staleQuery is the refusal a stale view answers with under on_stale
// fail.
//
// It carries the diagnostics **and** the pointer-addressed sentences, not
// one or the other: internal/web publishes Fields as
// details.fields[].path and a client that reads only those still learns
// where to look, while a UI banding a warning across the top of a picture
// wants the codes. Any resolution problem not already addressed by a
// diagnostic is carried too, so a stale view that is also wrong for some
// other reason does not lose the other reason.
func staleQuery(diags []Diagnostic, problems []metamodel.FieldError) error {
	fields := make([]metamodel.FieldError, 0, len(diags)+len(problems))
	covered := make(map[string]bool, len(diags))
	for _, d := range diags {
		covered[d.Pointer] = true
		fields = append(fields, metamodel.FieldError{Path: d.Pointer, Message: d.message()})
	}
	for _, p := range problems {
		if !covered[p.Path] {
			fields = append(fields, p)
		}
	}
	return &QueryError{Code: CodeQueryStale, Fields: fields, Stale: diags}
}

// RunView runs a saved view, by key, against the game as it stands now.
//
// This is the entry point staleness exists for. It reads the stored
// document and the dependency index that was written beside it in the
// same transaction (Task 11), resolves the one against the other, and
// then does one of three things: runs the view, runs what is left of it,
// or refuses.
//
// It never writes. A repaired spelling is an explicit views.upsert, for
// the reason this file's header gives.
func (s *Service) RunView(ctx context.Context, projectID uuid.UUID, key string,
	req RunRequest,
) (Result, error) {
	view, err := s.ViewByKey(ctx, projectID, key)
	if err != nil {
		return Result{}, err
	}
	q, err := ParseQuery(view.Query)
	if err != nil {
		// A stored document that no longer parses is not staleness — it
		// is this package having stored something it would refuse today,
		// which is a bug here rather than a game that moved on.
		return Result{}, fmt.Errorf("the stored query of view %q does not parse: %w", key, err)
	}
	refs, err := s.ViewRefs(ctx, projectID, view.ID)
	if err != nil {
		return Result{}, err
	}
	stored := make(map[string]dbq.ViewRef, len(refs))
	for _, ref := range refs {
		stored[ref.Pointer] = ref
	}
	// The renderer and its parameters are stored beside the query, and
	// they name types and fields the game can move under exactly as the
	// query does. A run that resolved only the document would leave that
	// half unreported — and, worse, would prescribe a repair the save
	// path then refuses at a pointer nobody had been shown.
	var params map[string]any
	if len(view.RendererParams) > 0 {
		if err := json.Unmarshal(view.RendererParams, &params); err != nil {
			// Same judgement as a stored document that no longer parses:
			// this package wrote the column through CheckRenderer, so
			// anything it cannot read back is a bug here rather than a
			// game that moved on.
			return Result{}, fmt.Errorf("the stored renderer parameters of view %q "+
				"do not parse: %w", key, err)
		}
	}
	result, err := s.runStored(ctx, projectID, q, &staleness{stored: stored}, req,
		view.Renderer, params)
	if err != nil {
		return Result{}, err
	}
	// The arrangement, read here and nowhere in Run: positions belong to
	// a *saved* view, so an ad-hoc query has none for the same reason it
	// has no staleness. Read after the run rather than before it because
	// a run that refuses answers with no envelope at all, and a read that
	// only ever feeds a refused answer is a read nothing needs.
	//
	// It is read outside the run's own transaction, which is deliberate
	// rather than overlooked: that transaction is read-only and bounded
	// by a statement timeout the picture's cost is measured against, and
	// a drag committing between the two is a picture one drag old — which
	// is what view.positions exists to tell the reader about, and what a
	// client re-reads on. Nothing here is compared against the nodes, so
	// a position for a node this run did not draw comes back too: it is
	// the same arrangement the next run of a widened query will use, and
	// dropping it would make an edit to the query look like a lost
	// afternoon of map work.
	positions, err := s.positionsOf(ctx, projectID, view.ID)
	if err != nil {
		return Result{}, err
	}
	result.Positions = positions
	return result, nil
}

// runStored resolves a stored query against the game and decides what the
// run does about what has moved.
func (s *Service) runStored(ctx context.Context, projectID uuid.UUID, q *Query,
	st *staleness, req RunRequest, renderer string, rendererParams map[string]any,
) (Result, error) {
	cat, err := s.LoadCatalogue(ctx, projectID)
	if err != nil {
		return Result{}, err
	}
	r, problems := resolveInto(cat, q, st)
	params, err := bindParams(r, req.Params)
	if err != nil {
		// The run's own arguments are wrong, which is not the view's
		// fault and not staleness: an undeclared parameter name, or a
		// value of the wrong type. invalid, at its own pointer.
		return Result{}, err
	}
	unbound := st.noteUnboundParams(r, params)
	// The renderer's own references, judged against the document as
	// stored rather than against whatever best_effort is about to prune:
	// the pointers a diagnostic carries address the view a designer will
	// open to repair it, and that view is the whole one.
	staleParams := rendererStaleness(st, r, renderer, rendererParams)

	// Everything the run cannot do, addressed. The diagnostics are the
	// *report*; these pointers are what best_effort prunes on, and they
	// are the resolution problems rather than the diagnostics because a
	// stored query can also be unresolvable for a reason that is not
	// staleness — and running it half-resolved would silently widen it,
	// since a predicate whose leaf failed to resolve comes back as the
	// tree without that leaf.
	broken := make([]string, 0, len(problems)+len(unbound)+len(staleParams))
	for _, p := range problems {
		broken = append(broken, p.Path)
	}
	broken = append(broken, unbound...)
	broken = append(broken, staleParams...)

	if len(broken) == 0 {
		// Renames only, or nothing at all. The view runs as written.
		return s.execute(ctx, projectID, r, params, req, st.diags)
	}
	if !strings.EqualFold(req.OnStale, OnStaleBestEffort) {
		if len(st.diags) == 0 {
			return Result{}, invalidQueryProblems(problems)
		}
		return Result{}, staleQuery(st.diags, problems)
	}
	pruned, ok := pruneStale(r, broken)
	if !ok {
		// Best effort refuses for two reasons and answers the same way
		// for both, because they are the same answer: there is no picture
		// this run can honestly draw.
		//
		// **Every seed set is gone**, so best effort is no effort — and
		// an empty picture with a warning beside it reads as "this game
		// has nothing", which is the lie the whole switch exists to
		// avoid.
		//
		// That guard is about the *query*, not about the result, and the
		// difference is worth stating because the sentence above invites
		// the wrong reading. It cannot promise a non-empty picture and
		// does not try to: a run whose seed sets all survive can still
		// come back with nothing — a narrowing schema invalidates the
		// rows that held a value for the field it dropped, and pruning
		// cannot see that, so best effort answers zero nodes, no error
		// and a warning attached. Refusing empty results is not the fix
		// either; a legitimately empty query is a thing a designer asks
		// for. What this arm covers is the one case pruning *made* empty,
		// where the emptiness is this function's own doing.
		//
		// **Or a problem was raised at a position pruning cannot act
		// on**, in which case dropping nothing and running the document
		// whole would execute it with that problem standing, which is the
		// silently widened picture by another road.
		return Result{}, staleQuery(st.diags, problems)
	}
	return s.execute(ctx, projectID, pruned, params, req, st.diags)
}

// pruneStale drops the parts of a resolved query that cannot run, and
// says whether best effort has a picture left to draw: false when every
// seed set went, and false when a problem was raised at a position this
// function cannot act on, since running the document whole would then
// execute it with that problem standing.
//
// **The unit it drops is the smallest whole thing that can still be
// drawn, and it never weakens a condition.** That is the rule the whole
// file rests on, and it is the difference between best effort and a lie:
//
//   - a seed selector, a traversal step or an edges[] entry whose own
//     type reference died is dropped entire;
//   - a step whose `where` or `edge_where` cannot be resolved is dropped
//     entire, rather than run without that condition — dropping the
//     condition is exactly the silently-widened picture on_stale defaults
//     to fail over;
//   - a projection slot or a `project.fields` key is dropped, which costs
//     a colour and never adds a node;
//   - a step reading from a dropped set goes with it, transitively, and
//     an edges[] entry naming a dropped step or a dropped set goes too,
//     because an edge between things the picture no longer holds is not
//     an edge.
//
// It prunes the *Resolved and the shallow copy of the document beside it
// together, because the compiler reads both and reads them by index —
// `nodes` names sets by name and `edges` is walked over Query.Edges while
// indexing Resolved.Edges. Pruning one of the two is how those two lists
// come apart.
//
// The pointers in the diagnostics still address the **stored** document,
// which is the one a designer will open to repair it. Nothing renumbers.
func pruneStale(r *Resolved, broken []string) (*Resolved, bool) {
	dropSet := map[int]bool{}
	dropStep := map[int]bool{}
	dropEdge := map[int]bool{}
	for _, ptr := range broken {
		parts := strings.Split(ptr, "/")
		// **Every broken pointer has to be accounted for, and one this
		// switch cannot act on refuses the run.** Falling through was the
		// silent widening this whole file exists to prevent, arriving by
		// the one road nobody watches: a pointer outside the three
		// document positions prunes nothing, and best effort then
		// executes the document whole with a resolution problem standing
		// against it — a picture drawn as if the problem were not there,
		// with a warning beside it saying it is. A renderer parameter
		// naming a type this game deleted is exactly that pointer, and it
		// is why this is a live arm rather than defence in depth.
		handled := false
		if len(parts) >= 3 {
			index := func() (int, bool) {
				n, err := strconv.Atoi(parts[2])
				return n, err == nil
			}
			switch parts[1] {
			case "from":
				if i, ok := index(); ok {
					dropSet[i], handled = true, true
				}
			case "traverse":
				if i, ok := index(); ok {
					dropStep[i], handled = true, true
				}
			case "edges":
				if i, ok := index(); ok {
					dropEdge[i], handled = true, true
				}
			}
		}
		// **A /project pointer prunes nothing, and that is not an
		// omission.** resolveProjection walks past a slot or a
		// project.fields key it cannot judge, so the resolved projection
		// this function is handed already lacks it: a colour source that
		// broke costs the colour and nothing else, and pruning it a
		// second time here would be a second implementation of a rule
		// resolution already applies. The first version of this function
		// had one, and no mutation of it could be made to change a
		// picture — which is what a mechanism nothing reads looks like
		// from the inside. TestBestEffortKeepsTheProjectedFieldsItCanStillRead
		// is what observes the surviving keys.
		//
		// It counts as acted on for that reason and not by omission: the
		// pruning happened, one pass earlier, and the guard above is
		// asking whether anything acted on the problem rather than
		// whether this function did.
		if len(parts) >= 2 && parts[1] == "project" {
			handled = true
		}
		if !handled {
			return nil, false
		}
	}

	// The names that are going, and the steps that fall with them. A
	// fixed point rather than one pass: a step reading from a step
	// reading from a dropped set is dropped too.
	gone := map[string]bool{}
	for i := range r.Sets {
		if dropSet[i] && r.Sets[i].Name != "" {
			gone[r.Sets[i].Name] = true
		}
	}
	for {
		grew := false
		for i := range r.Steps {
			if dropStep[i] {
				if name := r.Steps[i].Name; name != "" && !gone[name] {
					gone[name] = true
					grew = true
				}
				continue
			}
			if gone[r.Steps[i].FromSet] {
				dropStep[i] = true
				grew = true
			}
		}
		if !grew {
			break
		}
	}
	for i := range r.Edges {
		spec := r.Edges[i].Spec
		if spec == nil {
			dropEdge[i] = true
			continue
		}
		if spec.FromStep != "" && gone[spec.FromStep] {
			dropEdge[i] = true
		}
		for _, name := range spec.Between {
			if gone[name] {
				dropEdge[i] = true
			}
		}
	}

	out := *r
	doc := *r.Query
	out.Query = &doc

	doc.From, out.Sets = nil, nil
	for i := range r.Sets {
		if dropSet[i] {
			continue
		}
		doc.From = append(doc.From, r.Query.From[i])
		out.Sets = append(out.Sets, r.Sets[i])
	}
	if len(out.Sets) == 0 {
		return nil, false
	}
	doc.Traverse, out.Steps = nil, nil
	for i := range r.Steps {
		if dropStep[i] {
			continue
		}
		doc.Traverse = append(doc.Traverse, r.Query.Traverse[i])
		out.Steps = append(out.Steps, r.Steps[i])
	}
	doc.Edges, out.Edges = nil, nil
	for i := range r.Edges {
		if dropEdge[i] {
			continue
		}
		doc.Edges = append(doc.Edges, r.Query.Edges[i])
		out.Edges = append(out.Edges, r.Edges[i])
	}
	doc.Nodes = nil
	for _, entry := range r.Query.Nodes {
		if gone[entry.Set] {
			continue
		}
		doc.Nodes = append(doc.Nodes, entry)
	}
	return &out, true
}

// RemoveTypeReportingViews removes an entity type or a relation type and
// answers with the saved views it broke.
//
// **Deleting a type that views depend on is allowed.** Refusing with
// in_use — the way the metamodel refuses deleting a type that still has
// entities — is rejected here because the cases are not alike: an entity
// is content and losing it loses work, while a view is derived and can be
// rewritten in one call. Making a type undeletable because a six-month-old
// diagram mentions it would push designers into deleting views in order
// to delete types, which is worse than either.
//
// The report is a courtesy and not a lock. It is read before the removal,
// in its own statement, because the removal is what empties the column it
// matches on; a view saved in the gap between the two is missing from the
// list and loses nothing by it — its ref row still survives the deletion
// with the key text intact, so the next run of that view reports the
// reference dead at its own pointer. The list saves a designer a search,
// and view_refs is what makes the answer true.
//
// It is composed here rather than inside internal/metamodel because the
// dependency runs one way: views knows about types and nothing in the
// metamodel mentions views. A removal that reported its views from in
// there would be that dependency pointing both ways.
func (s *Service) RemoveTypeReportingViews(ctx context.Context, projectID uuid.UUID,
	kind string, typeID uuid.UUID, cascade bool,
) ([]ViewDependency, error) {
	broke, err := s.ViewsDependingOn(ctx, projectID, kind, typeID)
	if err != nil {
		return nil, err
	}
	switch kind {
	case KindEntityType:
		err = s.meta.RemoveEntityType(ctx, projectID, typeID, cascade)
	case KindRelationType:
		err = s.meta.RemoveRelationType(ctx, projectID, typeID, cascade)
	default:
		// Unreachable: ViewsDependingOn has already refused any third kind.
		return nil, fmt.Errorf("views: %q is not a kind of type a view can reference", kind)
	}
	if err != nil {
		return nil, err
	}
	return broke, nil
}
