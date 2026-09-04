package views

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/metamodel"
)

// Node is one row of a result. It carries identity and the attributes the
// projection asked for, and never a full fields payload unless
// include_fields was set: a thousand-node result with every jsonb field
// inlined is a five-figure token bill for a picture the agent is not
// going to look at.
//
// Attrs is **declared and never filled** as of this task. The projection
// pass that fills it is Task 9's; nothing here reads project.label,
// color_by, group_by, size_by or sort_by, and Ambiguous is likewise
// Task 9's to set.
type Node struct {
	ID     uuid.UUID      `json:"id"`
	Key    string         `json:"key"`
	Type   string         `json:"type"`
	Name   string         `json:"name"`
	Set    string         `json:"set"`
	Role   string         `json:"role,omitempty"`
	Attrs  map[string]any `json:"attrs,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
	// Ambiguous is set when a one-hop related attribute found more than
	// one entity and the first by name was used. Silently picking one and
	// saying nothing would produce a map that is wrong in a way nobody
	// can see. Task 9 is what sets it.
	Ambiguous bool `json:"ambiguous,omitempty"`
}

// Edge is one relation in a result.
//
// **ID is a relations.id and is not stable across a re-seed.** relations
// carries no key and no version (0004_metamodel.sql), so an edge is
// addressable only as (relation_type_id, source_id, target_id) and a
// re-seed that deleted and recreated it produces a new id for the same
// edge. Nothing in this sub-project stores an edge id; a client that does
// is storing something that will change under it.
//
// Label is Task 9's, from an edge entry's label_from; nothing here fills
// it.
type Edge struct {
	ID     uuid.UUID      `json:"id"`
	Type   string         `json:"type"`
	Source uuid.UUID      `json:"source"`
	Target uuid.UUID      `json:"target"`
	Label  string         `json:"label,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
}

// Stats is always populated. The first question about a slow view is
// whether it is the query or the size of the answer, and without both
// numbers it cannot be answered. DurationMS is also the measurement this
// plan's open question O2 (indexed jsonb fields) is to be re-argued with.
//
// MaxDepthReached is derived from the *declared* depth of the sets that
// contributed a node, not from a depth column: every step this build
// compiles is one hop, so the two are the same number. Task 8's recursive
// steps have to read it off graph.WalkCTE's own depth column instead, and
// this comment is the note that says so rather than a silence.
type Stats struct {
	Nodes           int   `json:"nodes"`
	Edges           int   `json:"edges"`
	MaxDepthReached int   `json:"max_depth_reached"`
	DurationMS      int64 `json:"duration_ms"`
}

// Truncated says which caps were hit. Truncation is not an error: a
// designer asking about a huge graph should see a thousand nodes and a
// warning, not a stack trace.
//
// **Nothing sets any of these yet.** The statement this task emits
// carries no LIMIT at all; Task 7 is what adds the `cap + 1` mechanism
// that makes truncation detected rather than inferred, and until then a
// Truncated of all-false means "not measured", not "not truncated".
type Truncated struct {
	Nodes bool `json:"nodes"`
	Edges bool `json:"edges"`
	Depth bool `json:"depth"`
}

// Result is the one envelope every renderer consumes. One shape for every
// renderer is the point: swapping `graph` for `table` on a saved view
// must never require rewriting the query.
//
// The spec's envelope also carries a staleness report; it is not here,
// because Diagnostic is Task 12's type and a field declared against a
// type that does not exist yet is a field nothing can fill.
type Result struct {
	Nodes     []Node    `json:"nodes"`
	Edges     []Edge    `json:"edges"`
	Stats     Stats     `json:"stats"`
	Truncated Truncated `json:"truncated"`
}

// RunRequest is one execution: a parsed query and the parameter values
// that override its declared defaults.
//
// The spec's on_stale switch is not here for the same reason Result has
// no staleness report: Task 12 owns both, and a knob that is read by
// nothing is a knob that lies.
type RunRequest struct {
	Query  *Query
	Params map[string]any
	// IncludeFields selects each node's and each edge's declared fields
	// into the envelope. Off by default; see Node.
	IncludeFields bool
}

// Run compiles and executes one query against one game.
//
// It resolves against a catalogue read for that game, binds the run's
// parameters over the query's declared defaults, compiles, and reads the
// single statement's rows into one envelope. Every value the caller
// controls — a key, a field name, a comparison operand, a set name —
// travels as a bind parameter; see compile.go's builder.
func (s *Service) Run(ctx context.Context, projectID uuid.UUID, req RunRequest) (Result, error) {
	if req.Query == nil {
		return Result{}, invalidQuery("", "no query document was given")
	}
	cat, err := s.LoadCatalogue(ctx, projectID)
	if err != nil {
		return Result{}, err
	}
	resolved, err := ResolveAgainst(projectID, cat, req.Query)
	if err != nil {
		return Result{}, err
	}
	params, err := bindParams(resolved, req.Params)
	if err != nil {
		return Result{}, err
	}
	// A copy, so one run's bindings cannot leak into a *Resolved another
	// run — or Task 12's staleness report — is still holding.
	forRun := *resolved
	forRun.Params = params

	statement, args, err := compileWith(&forRun, projectID, compileOptions{
		IncludeFields: req.IncludeFields,
	})
	if err != nil {
		return Result{}, err
	}

	started := time.Now()
	// Task 7 replaces this with the bounded read-only transaction: a
	// statement timeout, default_transaction_read_only, and the LIMIT
	// cap+1 that makes truncation detectable. Until then a run is neither
	// time-bounded nor row-bounded, which is stated here rather than left
	// for a reviewer to notice.
	rows, err := s.pool.Query(ctx, statement, args...)
	if err != nil {
		return Result{}, err
	}
	defer rows.Close()

	// Empty rather than nil, because a nil slice serialises as JSON null
	// and an empty result is `{"nodes": [], "edges": []}` — a picture
	// with nothing in it, not an absent picture. Task 15's REST mirror
	// hands this envelope to clients that would otherwise have to handle
	// both spellings of "no nodes".
	result := Result{Nodes: []Node{}, Edges: []Edge{}}
	seenNode := map[uuid.UUID]bool{}
	seenEdge := map[uuid.UUID]bool{}
	for rows.Next() {
		var (
			kind                        string
			id                          uuid.UUID
			key, name, typeKey, setName *string
			role                        *string
			source, target              *uuid.UUID
			fields                      []byte
			rank                        *int32
		)
		if err := rows.Scan(&kind, &id, &key, &name, &typeKey, &setName, &role,
			&source, &target, &fields, &rank); err != nil {
			return Result{}, fmt.Errorf("scan a view row: %w", err)
		}
		payload, err := decodeFields(fields)
		if err != nil {
			return Result{}, err
		}
		switch kind {
		case "node":
			// One node per id, keeping the first `nodes` entry that
			// claimed it: a node in two sets is one thing on the picture,
			// and the ORDER BY on the entry's rank is what makes "first"
			// mean the order the document declared rather than whatever
			// Postgres happened to return.
			// TestANodeInTwoSetsComesBackOnceUnderTheFirstSetThatClaimedIt
			// pins both halves, and says which of the two the ordering is.
			if seenNode[id] {
				continue
			}
			seenNode[id] = true
			result.Nodes = append(result.Nodes, Node{
				ID:     id,
				Key:    text(key),
				Type:   text(typeKey),
				Name:   text(name),
				Set:    text(setName),
				Role:   text(role),
				Fields: payload,
			})
		case "edge":
			if seenEdge[id] {
				continue
			}
			seenEdge[id] = true
			edge := Edge{ID: id, Type: text(typeKey), Fields: payload}
			if source != nil {
				edge.Source = *source
			}
			if target != nil {
				edge.Target = *target
			}
			result.Edges = append(result.Edges, edge)
		default:
			return Result{}, fmt.Errorf("views: a result row is a node or an edge, got %q", kind)
		}
	}
	if err := rows.Err(); err != nil {
		return Result{}, err
	}

	result.Stats = Stats{
		Nodes:           len(result.Nodes),
		Edges:           len(result.Edges),
		MaxDepthReached: maxDepthReached(&forRun, result.Nodes),
		DurationMS:      time.Since(started).Milliseconds(),
	}
	return result, nil
}

func text(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func decodeFields(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode a row's fields: %w", err)
	}
	return out, nil
}

// bindParams overlays the values one run supplies onto the defaults the
// document declared, checking each against the parameter's declared type.
//
// Every problem is reported in one pass, like every other refusal in this
// package: an agent that mistyped two parameter names should learn both
// at once. A supplied value is addressed at the *declaration* it belongs
// to, /params/N, because that is the position in the document the caller
// can go and read; a name nothing declares is addressed at /params,
// because it has no position of its own.
func bindParams(r *Resolved, supplied map[string]any) (map[string]any, error) {
	bound := make(map[string]any, len(r.Params)+len(supplied))
	for key, value := range r.Params {
		bound[key] = value
	}
	declared := make(map[string]int, len(r.Query.Params))
	for i, decl := range r.Query.Params {
		declared[decl.Key] = i
	}
	var problems []metamodel.FieldError
	for _, key := range sortedKeys(supplied) {
		i, ok := declared[key]
		if !ok {
			problems = append(problems, metamodel.FieldError{
				Path: pointer("params"),
				Message: fmt.Sprintf("no parameter named %q is declared by this query: "+
					"declare it in params, or drop it from the run", key),
			})
			continue
		}
		value, err := coerceParam(metamodel.FieldType(r.Query.Params[i].Type), supplied[key])
		if err != nil {
			problems = append(problems, metamodel.FieldError{
				Path:    pointer("params", i),
				Message: err.Error(),
			})
			continue
		}
		bound[key] = value
	}
	if len(problems) > 0 {
		return nil, invalidQueryProblems(problems)
	}
	return bound, nil
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	// Sorted so that two runs of the same wrong call report their
	// problems in the same order; a map's iteration order is not one an
	// agent can learn from.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// maxDepthReached is how many hops from a seed the furthest node that
// came back sits at.
//
// It is computed from the query's own shape rather than from the rows,
// which is exact only because every step this build compiles is one hop:
// a set's depth is its source set's depth plus that step's own, and a set
// that contributed no node contributes no depth. Task 8's multi-hop steps
// break that arithmetic and have to read the walk's depth column instead.
func maxDepthReached(r *Resolved, nodes []Node) int {
	depth := map[string]int{}
	for _, set := range r.Sets {
		depth[set.Name] = 0
	}
	for _, step := range r.Steps {
		hops := 1
		if step.Step.Depth != nil {
			hops = step.Step.Depth.Max
		}
		depth[step.Name] = depth[step.FromSet] + hops
	}
	deepest := 0
	for _, node := range nodes {
		if d, ok := depth[node.Set]; ok && d > deepest {
			deepest = d
		}
	}
	return deepest
}
