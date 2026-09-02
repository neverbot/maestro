package web

import (
	"context"
	"sort"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
)

// This file is the one search surface the markdown spec's §8 asks for:
// two indexes, one ranked list, every hit labelled with what it is.
//
// **The union is here and not in SQL.** A single UNION ALL query would
// have to carry a second copy of SearchEntities' ranking expression —
// the most measured statement in this repository, with an EXPLAIN and
// 200 timed runs in its comment — and a copy is a thing that drifts.
// Two queries, each the authority for its own index, merged by the same
// (name_match, rank) key both of them order by.
//
// **The merge is complete for a top-N.** Each side returns its own top
// `limit` under the identical sort key, so a hit that would have placed
// in the overall top `limit` cannot have been cut from its own side's
// top `limit`. Nothing that would have been shown is lost.
//
// **The two ranks are comparable**, and this is the assumption that has
// to be stated rather than assumed: both are ts_rank over a `simple`
// configuration with the default weight array ({D:0.1, C:0.2, B:0.4,
// A:1.0}; neither query passes one), over a vector whose A half is the
// row's subject — an entity's name, a document's title — and whose lower
// weights are the rest. They would not be comparable if the two indexes
// used different configurations, which is one of the three reasons the
// markdown plan settled on `simple` for both.

// The two things a game holds, and the two values `kind` narrows to. The
// empty value is "both", which is the point of the surface.
const (
	searchKindEntity   = "entity"
	searchKindDocument = "document"
)

// SearchInput is the argument shape of the search tool.
//
// **Kind is the filter the spec asks for**, and an unrecognised value is
// refused rather than defaulted: reading "quest" as "both" would answer
// a question the caller did not ask with a full page, which is the
// failure mode this read surface is most exposed to
// (TestAnUnrecognisedSearchKindIsRefused). TypeKey narrows the entity
// half only and DocKind the document half, so passing either against
// the other kind is invalid_input rather than a silently ignored
// argument.
//
// DocKind's empty value is "no filter", and it has no spelling for
// "documents carrying no kind at all" — the same limitation
// markdown.ListFilter.Kind records, since a document's kind is optional
// and "" is also a real stored value.
type SearchInput struct {
	ScopedArgs
	Query   string `json:"query"`
	Kind    string `json:"kind,omitempty"`
	TypeKey string `json:"type_key,omitempty"`
	DocKind string `json:"doc_kind,omitempty"`
	Limit   int32  `json:"limit,omitempty"`
}

// SearchHit is one result, labelled.
//
// **This shape replaces the entity-only one that shipped**, and the
// replacement is deliberate rather than additive. The alternative was to
// keep the entity fields at the top level and add optional document
// ones, which would have meant a schema whose `Required` list was a lie
// for half the hits: `type_key`, `key`, `name`, `invalid` are not
// properties a document has. Nesting each kind under its own key keeps
// every field honest and keeps one ranked list.
//
// Kind, NameMatch and Rank are at the top level because they are the
// three things that are true of both, and because Kind is what a client
// branches on before it looks at anything else.
//
// NameMatch is on the wire, not only in the sort, for the reason it was
// put there for entities: the order is `(name_match, rank)`, and a
// caller that re-sorts by rank alone reconstructs the wrong order,
// because a hit with name_match false can carry a higher rank than one
// with it true and still sort after it.
// TestSearchRanksANamedHitAboveAMentionAcrossKinds asserts that
// inversion across the two indexes.
type SearchHit struct {
	Kind      string             `json:"kind"`
	NameMatch bool               `json:"name_match"`
	Rank      float32            `json:"rank"`
	Entity    *EntityOutput      `json:"entity,omitempty"`
	Document  *DocumentHitOutput `json:"document,omitempty"`
}

// DocumentHitOutput is the document half of a search result. It carries
// a summary and never a body: prose is the largest payload in the system
// and a search that returned bodies would blow a context window on its
// first answer. markdown.DocumentHit, which this is built from, carries
// no body either, and TestASearchHitCarriesNoBodyAtAll pins that over
// every one of its fields.
type DocumentHitOutput struct {
	ID       uuid.UUID   `json:"id"`
	Path     string      `json:"path"`
	Title    string      `json:"title"`
	Summary  string      `json:"summary"`
	DocKind  string      `json:"doc_kind"`
	Version  int32       `json:"version"`
	LinkedTo []LinkedRef `json:"linked_to"`
}

// LinkedRef is one entity a document hit is attached to. It is what
// makes a document hit actionable: entity search deliberately does not
// reach into attached prose, so a word that lives only in a script
// produces a document hit, and this is the way back to the quest.
type LinkedRef struct {
	EntityTypeKey string `json:"entity_type_key"`
	EntityKey     string `json:"entity_key"`
	Name          string `json:"name"`
	Role          string `json:"role,omitempty"`
}

// SearchOutput is one ranked list of labelled hits.
//
// Truncated says the answer filled the limit, which a caller cannot
// otherwise tell: this is a top-N and not a page, and there is no cursor
// to be absent. It is the weaker thing this envelope can honestly check
// — an answer exactly as long as the limit allowed may or may not have
// had more behind it — and the recovery either way is a narrower query.
type SearchOutput struct {
	Items     []SearchHit `json:"items"`
	Truncated bool        `json:"truncated"`
}

// MCPSearch implements search over both of a game's indexes. The entity
// hits carry their fields: a search is a caller looking for content, and
// a hit it then has to fetch one by one is a round trip per row.
func MCPSearch(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID, in SearchInput) (SearchOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return SearchOutput{}, err
	}
	return searchContent(ctx, deps, caller, projectID, in)
}

// searchContent is MCPSearch without the token-binding check, for the
// REST mirror (api_metamodel.go), whose caller is a person whose
// standing requireProject already resolved. See mcp_metamodel.go's
// header.
//
// The caller is unused today and named `_` rather than dropped: every
// core on this surface takes one, and a signature that differs only
// where nothing needs it is a thing to re-derive at each call site.
func searchContent(ctx context.Context, deps MCPDeps, _ Caller, projectID uuid.UUID, in SearchInput) (SearchOutput, error) {
	switch in.Kind {
	case "", searchKindEntity, searchKindDocument:
	default:
		return SearchOutput{}, invalidInput("kind",
			`must be "entity", "document", or omitted for both`)
	}
	if in.TypeKey != "" && in.Kind == searchKindDocument {
		return SearchOutput{}, invalidInput("type_key",
			`narrows entity hits and cannot be combined with kind "document"; `+
				`use doc_kind to narrow documents`)
	}
	if in.DocKind != "" && in.Kind == searchKindEntity {
		return SearchOutput{}, invalidInput("doc_kind",
			`narrows document hits and cannot be combined with kind "entity"; `+
				`use type_key to narrow entities`)
	}
	// An explicit kind "document" against an instance with no markdown
	// service is refused rather than answered with an empty list. kind
	// "" (both) still degrades to entities alone — that arm's own
	// comment below explains why that half is deliberate — but a caller
	// who asked only for documents and got zero back cannot tell "this
	// game holds no matching prose" from "this instance serves no prose
	// at all" unless told which one happened.
	if in.Kind == searchKindDocument && deps.Markdown == nil {
		return SearchOutput{}, &MCPError{
			Code:    errCodeNotFound,
			Message: "this instance serves no document search; the markdown domain is not configured",
		}
	}

	limit := metamodel.SearchLimit(in.Limit)
	// Items is built with make so an empty answer marshals as [] rather
	// than null: "this game holds nothing matching" and "the server sent
	// no list" are different statements.
	out := SearchOutput{Items: make([]SearchHit, 0, limit)}

	if in.Kind != searchKindDocument {
		names, err := entityTypeKeys(ctx, deps, projectID)
		if err != nil {
			return SearchOutput{}, err
		}
		rows, err := deps.Metamodel.Search(ctx, projectID, in.Query, in.TypeKey, limit)
		if err != nil {
			return SearchOutput{}, err
		}
		for _, row := range rows {
			entity, err := entityOf(dbq.Entity{
				ID: row.ID, ProjectID: row.ProjectID, EntityTypeID: row.EntityTypeID,
				Key: row.Key, Name: row.Name, Fields: row.Fields,
				Invalid: row.Invalid, Version: row.Version,
			}, names, true)
			if err != nil {
				return SearchOutput{}, err
			}
			out.Items = append(out.Items, SearchHit{
				Kind: searchKindEntity, NameMatch: row.NameMatch, Rank: row.Rank, Entity: &entity,
			})
		}
	}

	// An instance built without the markdown domain answers kind "" with
	// entities alone rather than failing: MCPDeps.Markdown is optional
	// exactly as MCPDeps.Metamodel is, and cmd/maestro always builds
	// one. No test pins this arm — it is the shape of the dependency,
	// not a behaviour a caller can ask for. kind "document" against the
	// same nil service never reaches this arm at all — the guard above
	// refuses it before this point, rather than answering it with an
	// empty list that reads as "no matching prose" instead of "no prose
	// service".
	if in.Kind != searchKindEntity && deps.Markdown != nil {
		hits, err := deps.Markdown.SearchDocuments(ctx, projectID, in.Query, in.DocKind, limit)
		if err != nil {
			return SearchOutput{}, err
		}
		for _, hit := range hits {
			linked := make([]LinkedRef, 0, len(hit.LinkedEntities))
			for _, link := range hit.LinkedEntities {
				linked = append(linked, LinkedRef{
					EntityTypeKey: link.EntityTypeKey, EntityKey: link.EntityKey,
					Name: link.EntityName, Role: link.Role,
				})
			}
			doc := DocumentHitOutput{
				ID: hit.ID, Path: hit.Path, Title: hit.Title, Summary: hit.Summary,
				DocKind: hit.Kind, Version: hit.Version, LinkedTo: linked,
			}
			out.Items = append(out.Items, SearchHit{
				Kind: searchKindDocument, NameMatch: hit.NameMatch, Rank: hit.Rank, Document: &doc,
			})
		}
	}

	// The merge. sort.SliceStable, so the two sides' own tie-breaks —
	// name then id for entities, title then id for documents — survive
	// into the merged order and two identical calls answer identically.
	//
	// **This is load-bearing, not a defensive choice with nothing to
	// pin.** An earlier version of this comment claimed sort.Slice was
	// equally safe, on the theory that each side arrives already ordered
	// by the key this comparator uses, so the concatenation is always
	// non-decreasing and pdqsort has nothing to reorder. That holds only
	// when the entity block happens to rank above the document block. It
	// does not have to:
	// TestAFullReversalOfTheConcatenationStillKeepsBothTieBreaks builds
	// twenty entities and twenty documents that only *mention* the query
	// (name_match false on both sides), scored by the two differently
	// built vectors at 0.396 and 0.649 respectively — so the entity
	// block, appended first, ranks *below* the document block appended
	// after it. The concatenation is then descending-out-of-order across
	// its full length, sort.Slice really does partition, and it destroys
	// both queries' own title-then-id and name-then-id tie-breaks — the
	// same fixture is red against sort.Slice, proved by hand, and green
	// against sort.SliceStable, which is what ships.
	// TestTwoHitsOfEqualRankKeepOneOrderAcrossIdenticalCalls records the
	// narrower case — same-kind ties only — where sort.Slice does still
	// pass, and says why that fixture alone cannot tell the two sorts
	// apart.
	sort.SliceStable(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		if a.NameMatch != b.NameMatch {
			return a.NameMatch
		}
		return a.Rank > b.Rank
	})
	// Both comparisons are done in int rather than int32, so no
	// conversion is needed: SearchLimit's answer is bounded by
	// metamodel.MaxSearchLimit and cannot overflow either way.
	if len(out.Items) > int(limit) {
		out.Items = out.Items[:limit]
	}
	// The only thing this answer can honestly say about completeness:
	// the ranking was cut at the limit, so there may be more below it.
	// See SearchOutput.
	out.Truncated = len(out.Items) == int(limit)
	return out, nil
}
