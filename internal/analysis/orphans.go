package analysis

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/paging"
)

// **There is no recursion anywhere in this file, and that is a decision
// rather than an accident of the question.**
type OrphanMode string

const (
	// OrphanIsolated is the default: no edges at all, in either
	// direction, over every relation type a designer did not call
	// decoration.
	OrphanIsolated OrphanMode = "isolated"
	// OrphanSink has incoming edges and no outgoing ones: content
	// something points at that points nowhere itself.
	OrphanSink OrphanMode = "sink"
	// OrphanSource has outgoing edges and no incoming ones.
	OrphanSource OrphanMode = "source"
)

// OrphanModes is every value, so a surface can validate against the list
// rather than repeat it and a description can print it.
var OrphanModes = []OrphanMode{OrphanIsolated, OrphanSink, OrphanSource}

// OrphansInput is one orphan report's arguments.
type OrphansInput struct {
	// Mode is one of OrphanModes; empty is OrphanIsolated.
	Mode OrphanMode

	// EntityTypes narrows what is considered; empty means every type of
	// the game. IgnoreEntityTypes removes types from whatever that
	// leaves -- from the findings **and** from ConsideredTotal, because
	// a total that counts what the report excludes is a total that
	// disagrees with itself.
	EntityTypes       []string
	IgnoreEntityTypes []string

	// Limit and Cursor are the page. Limit is **clamped** rather than
	// refused, which is the existing split and not a new decision: a
	// page size is a transport detail whose remainder the cursor
	// promises, while a declared analysis bound is a statement about
	// what the answer means.
	Limit  int32
	Cursor string
}

// Orphan is one entity nothing points at, with the two degrees that make
// the three modes checkable.
type Orphan struct {
	EntityType string `json:"entity_type"`
	Key        string `json:"key"`
	Name       string `json:"name"`
	InDegree   int64  `json:"in_degree"`
	OutDegree  int64  `json:"out_degree"`
}

// OrphansResult is the whole report.
type OrphansResult struct {
	Findings   []Orphan `json:"orphans"`
	NextCursor string   `json:"next_cursor,omitempty"`

	Mode OrphanMode `json:"mode"`

	// ConsideredTotal is how many entities this run looked at, and it is
	// what makes the negative half real: an empty findings list over a
	// game of two hundred entities and one over a run that considered
	// none of them are the same JSON without it.
	ConsideredTotal int64 `json:"considered_total"`

	// ExcludedRelationTypes is the annotation types whose edges were not
	// counted, by key. **Empty means nothing was excluded**, which is
	// what a game that declared no traits gets, and it is reported
	// rather than left silent for the reason every count here is: a
	// verdict always arrives with the reading it rests on.
	ExcludedRelationTypes []string `json:"excluded_relation_types"`

	SemanticsSource []TypeSemantics `json:"semantics_source"`

	Stats Stats `json:"stats"`
}

const (
	defaultOrphanPage = int32(50)
	maxOrphanPage     = int32(200)
)

// Orphans answers "what does nothing point at".
func (s *Service) Orphans(ctx context.Context, projectID uuid.UUID, in OrphansInput) (
	OrphansResult, error,
) {
	started := time.Now()
	if len(in.EntityTypes) > MaxTypeKeys {
		return OrphansResult{}, limitExceeded("entity_types", len(in.EntityTypes), MaxTypeKeys)
	}
	if len(in.IgnoreEntityTypes) > MaxTypeKeys {
		return OrphansResult{}, limitExceeded(
			"ignore_entity_types", len(in.IgnoreEntityTypes), MaxTypeKeys)
	}
	mode := in.Mode
	if mode == "" {
		mode = OrphanIsolated
	}
	if !validOrphanMode(mode) {
		names := make([]string, 0, len(OrphanModes))
		for _, known := range OrphanModes {
			names = append(names, string(known))
		}
		return OrphansResult{}, invalidInput("mode", fmt.Sprintf(
			"is %q; the three are %s", mode, metamodel.QuotedList(names)))
	}

	considered, consideredKeys, err := s.consideredTypes(
		ctx, projectID, in.EntityTypes, in.IgnoreEntityTypes)
	if err != nil {
		return OrphansResult{}, err
	}

	// The permissive reading, and the only analysis that takes it. See
	// the doc comment above and traits.go's own note on the asymmetry.
	semantics, err := s.ResolveWithoutRefusing(ctx, projectID)
	if err != nil {
		return OrphansResult{}, err
	}
	ignored := dedupeIDs(semantics.WithTrait("annotation"))
	excluded := make([]string, 0, len(ignored))
	for _, id := range ignored {
		excluded = append(excluded, semantics.ByType[id].Key)
	}
	sort.Strings(excluded)

	out := OrphansResult{
		Mode:                  mode,
		ExcludedRelationTypes: excluded,
		SemanticsSource:       semantics.Types(),
	}
	total, err := s.q.CountConsideredEntities(ctx, dbq.CountConsideredEntitiesParams{
		ProjectID: projectID, EntityTypes: considered,
	})
	if err != nil {
		return OrphansResult{}, fmt.Errorf("count the entities considered: %w", err)
	}
	out.ConsideredTotal = total

	limit := paging.Size(in.Limit, defaultOrphanPage, maxOrphanPage)
	fingerprint := orphanFingerprint(projectID, mode, consideredKeys)
	after, err := paging.Decode(in.Cursor, fingerprint, refuseOrphanCursor)
	if err != nil {
		return OrphansResult{}, err
	}
	params := dbq.ListOrphansPageParams{
		ProjectID:            projectID,
		EntityTypes:          considered,
		IgnoredRelationTypes: ignored,
		Mode:                 string(mode),
		Limit:                limit,
	}
	if after.ID != uuid.Nil {
		entityType, key, ok := strings.Cut(after.Sort, "\x00")
		if !ok {
			return OrphansResult{}, refuseOrphanCursor(
				"is not a cursor this listing issued: it carries no entity type and key")
		}
		params.AfterID = &after.ID
		params.AfterEntityType = &entityType
		params.AfterKey = &key
	}

	rows, err := s.q.ListOrphansPage(ctx, params)
	if err != nil {
		return OrphansResult{}, fmt.Errorf("list orphans: %w", err)
	}
	out.Findings = make([]Orphan, 0, len(rows))
	for _, row := range rows {
		out.Findings = append(out.Findings, Orphan{
			EntityType: row.EntityTypeKey, Key: row.Key, Name: row.Name,
			InDegree: row.InDegree, OutDegree: row.OutDegree,
		})
	}
	// paging.Size never returns a limit below one, so a full page is
	// never an empty one and there is no separate emptiness check.
	if len(rows) == int(limit) {
		last := out.Findings[len(out.Findings)-1]
		out.NextCursor = paging.Encode(paging.Cursor{
			Sort:        last.EntityType + "\x00" + strings.ToLower(last.Key),
			ID:          rows[len(rows)-1].ID,
			Fingerprint: fingerprint,
		})
	}
	out.Stats.DurationMS = time.Since(started).Milliseconds()
	return out, nil
}

// validOrphanMode holds a caller's mode against the list rather than
// against three literals, so a fourth mode is accepted here the moment
// it is declared and not one edit later.
func validOrphanMode(mode OrphanMode) bool {
	for _, known := range OrphanModes {
		if mode == known {
			return true
		}
	}
	return false
}

// orphanFingerprint digests the listing a cursor was issued under.
func orphanFingerprint(projectID uuid.UUID, mode OrphanMode, considered []string) string {
	parts := []string{projectID.String(), "analysis.orphans", string(mode)}
	parts = append(parts, considered...)
	return paging.Fingerprint(parts...)
}

// refuseOrphanCursor turns paging's message into this package's own
// refusal, at the caller's own argument path. Left untyped a bad cursor
// would reach an agent as internal_error over a value the agent itself
// supplied.
func refuseOrphanCursor(message string) error { return invalidInput("cursor", message) }
