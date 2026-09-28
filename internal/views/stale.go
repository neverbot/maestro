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
const (
	OnStaleFail       = "fail"
	OnStaleBestEffort = "best_effort"
)

// Diagnostic is one thing a query says that this game no longer has, or
// no longer spells that way.
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
type resolveCtx struct {
	st           *staleness
	entityType   func(ptr, key string) *dbq.EntityType
	relationType func(ptr, key string) *dbq.RelationType
}

// typeOperand resolves an @type operand and hands back the key to
// compile against; every other leaf's value passes through untouched.
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
		return Result{}, staleQuery(st.diags, problems)
	}
	return s.execute(ctx, projectID, pruned, params, req, st.diags)
}

// pruneStale drops the parts of a resolved query that cannot run, and
// says whether best effort has a picture left to draw: false when every
// seed set went, and false when a problem was raised at a position this
// function cannot act on, since running the document whole would then
// execute it with that problem standing.
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
		// omission.** resolveProjection walks past a slot or a project.fields key
		// it cannot judge, so the resolved projection this function is handed
		// already lacks it: a colour source that broke costs the colour and
		// nothing else, and pruning it a second time here would be a second
		// implementation of a rule resolution already applies. The first version
		// of this function had one, and no mutation of it could be made to change
		// a picture — which is what a mechanism nothing reads looks like from the
		// inside. TestStaleArea's "best effort keeps the projected fields it can
		// still read" case is what observes the surviving keys.
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
