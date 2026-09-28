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
const (
	searchKindEntity   = "entity"
	searchKindDocument = "document"
)

// SearchInput is the argument shape of the search tool.
type SearchInput struct {
	ScopedArgs
	Query   string `json:"query"`
	Kind    string `json:"kind,omitempty"`
	TypeKey string `json:"type_key,omitempty"`
	DocKind string `json:"doc_kind,omitempty"`
	// Cursor pages the **entity** half of a search, and is accepted only
	// with kind "entity". A merged answer interleaves two indexes with
	// two orders, and one position cannot name a place in both; rather
	// than invent a rule for that, a paged search is asked for by kind.
	// See searchContent for the refusal.
	Cursor  string `json:"cursor,omitempty"`
	Limit   int32  `json:"limit,omitempty"`
	Verbose bool   `json:"verbose,omitempty"`
}

// SearchHit is one result, labelled.
type SearchHit struct {
	Kind      string             `json:"kind"`
	NameMatch bool               `json:"name_match"`
	Rank      float32            `json:"rank"`
	Entity    *EntityOutput      `json:"entity,omitempty"`
	Document  *DocumentHitOutput `json:"document,omitempty"`
}

// DocumentHitOutput is the document half of a search result. It carries a
// summary and never a body: prose is the largest payload in the system and
// a search that returned bodies would blow a context window on its first
// answer. markdown.DocumentHit, which this is built from, carries no body
// either, and TestSearchArea's "a search hit carries no body at all" case
// pins that over every one of its fields.
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
type SearchOutput struct {
	Items     []SearchHit `json:"items"`
	Truncated bool        `json:"truncated"`
	// NextCursor is set only for a paged search — kind "entity" — and
	// only when that page came back full. Everywhere else it is absent,
	// which is the honest answer: the merged search is a top-N and says
	// so through Truncated.
	NextCursor string `json:"next_cursor,omitempty"`
}

// MCPSearch implements search over both of a game's indexes.
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

	// **A cursor is a position in one listing**, and the merged answer is
	// not one: two indexes, two orders, interleaved. Refused rather than
	// applied to the entity half of a merged answer, which would page
	// one half while the other restarted on every call.
	if in.Cursor != "" && in.Kind != searchKindEntity {
		return SearchOutput{}, invalidInput("cursor",
			`pages an entity search: pass kind "entity" with it, because a merged answer `+
				`interleaves two indexes and one position cannot name a place in both`)
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
		rows, next, err := searchEntities(ctx, deps, projectID, in, limit)
		if err != nil {
			return SearchOutput{}, err
		}
		out.NextCursor = next
		for _, row := range rows {
			entity, err := entityOf(row.Entity, names, in.Verbose)
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

// searchEntities runs the entity half of a search, paged or not, and
// returns the rows in one shape so the caller above builds a hit exactly
// once.
func searchEntities(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in SearchInput, limit int32) (
	[]searchedEntity, string, error,
) {
	if in.Kind != searchKindEntity {
		rows, err := deps.Metamodel.Search(ctx, projectID, in.Query, in.TypeKey, limit)
		if err != nil {
			return nil, "", err
		}
		out := make([]searchedEntity, 0, len(rows))
		for _, row := range rows {
			out = append(out, searchedEntity{
				Entity: dbq.Entity{
					ID: row.ID, ProjectID: row.ProjectID, EntityTypeID: row.EntityTypeID,
					Key: row.Key, Name: row.Name, Fields: row.Fields,
					Invalid: row.Invalid, Version: row.Version,
				},
				NameMatch: row.NameMatch, Rank: row.Rank,
			})
		}
		return out, "", nil
	}

	page, err := deps.Metamodel.SearchPage(ctx, projectID, metamodel.SearchFilter{
		Query: in.Query, TypeKey: in.TypeKey, Cursor: in.Cursor, Limit: limit,
	})
	if err != nil {
		return nil, "", err
	}
	out := make([]searchedEntity, 0, len(page.Entities))
	for _, row := range page.Entities {
		out = append(out, searchedEntity{
			Entity: dbq.Entity{
				ID: row.ID, ProjectID: row.ProjectID, EntityTypeID: row.EntityTypeID,
				Key: row.Key, Name: row.Name, Fields: row.Fields,
				Invalid: row.Invalid, Version: row.Version,
			},
			NameMatch: row.NameMatch, Rank: row.Rank,
		})
	}
	return out, page.NextCursor, nil
}

// searchedEntity is one matched row and the two numbers the order is
// built from, in the one shape both domain calls come back as.
type searchedEntity struct {
	Entity    dbq.Entity
	NameMatch bool
	Rank      float32
}
