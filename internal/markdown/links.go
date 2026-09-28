package markdown

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/paging"
)

// MaxRoleLen bounds a link's free-text role ("script", "lore",
// "backstory"). Maestro attaches no meaning to it and ships no
// vocabulary: the same rule that forbids a built-in Quest type forbids a
// built-in Script role. Whether a project should be able to *declare*
// its roles the way it declares relation types is open (spec §10.3) and
// is not settled here; what is settled is that the link's key is
// (document, entity), so free text cannot produce two attachments of one
// pair under two spellings.
const MaxRoleLen = 64

// MaxLinksPerWrite bounds the links array a single write may carry.
const MaxLinksPerWrite = 100

// LinkTarget is one end of an attachment: the entity, and the role the
// document plays for it.
type LinkTarget struct {
	EntityType string
	EntityKey  string
	Role       string
}

// LinkInput is one attachment operation, from the document's side.
type LinkInput struct {
	Path       string
	EntityType string
	EntityKey  string
	Role       string
}

// UnlinkInput is one detachment operation, from the document's side.
type UnlinkInput struct {
	Path       string
	EntityType string
	EntityKey  string
}

// EntityLink is one attachment as seen from the document: which entity,
// and in what role.
type EntityLink struct {
	EntityID      uuid.UUID
	EntityTypeKey string
	EntityKey     string
	EntityName    string
	Role          string
}

// DocumentLink is one attachment as seen from the entity: which
// document, and in what role. It carries no body — a listing never does.
type DocumentLink struct {
	DocumentID uuid.UUID
	Path       string
	Title      string
	Kind       string
	Role       string
}

// LinkAdd attaches a document to an entity, or updates the role of an
// attachment that is already there.
func (s *Service) LinkAdd(ctx context.Context, projectID uuid.UUID, in LinkInput) error {
	// One pass over every argument, as Write and Delete do: a caller
	// whose path and whose role are both wrong hears about both.
	problems := pathProblems(in.Path)
	problems = append(problems, entityAddressProblems("", LinkTarget{
		EntityType: in.EntityType, EntityKey: in.EntityKey, Role: in.Role,
	})...)
	if len(problems) > 0 {
		return invalidInputProblems(problems)
	}

	var doc dbq.Document
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		var err error
		doc, err = s.documentForLinks(ctx, q, projectID, in.Path)
		if err != nil {
			return err
		}
		entityID, err := s.resolveEntity(ctx, q, projectID, "", LinkTarget{
			EntityType: in.EntityType, EntityKey: in.EntityKey,
		})
		if err != nil {
			return err
		}
		if _, err := q.UpsertDocumentLink(ctx, dbq.UpsertDocumentLinkParams{
			ProjectID: projectID, DocumentID: doc.ID, EntityID: entityID, Role: in.Role,
		}); err != nil {
			return fmt.Errorf("attach document: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.publish(projectID, eventDocumentLinked, documentEventMinRole, documentEventHumanOnly,
		DocumentEvent{ID: doc.ID, Path: doc.Path, Version: doc.CurrentVersion})
	return nil
}

// LinkRemove detaches a document from an entity.
func (s *Service) LinkRemove(ctx context.Context, projectID uuid.UUID, in UnlinkInput) error {
	problems := pathProblems(in.Path)
	problems = append(problems, entityAddressProblems("", LinkTarget{
		EntityType: in.EntityType, EntityKey: in.EntityKey,
	})...)
	if len(problems) > 0 {
		return invalidInputProblems(problems)
	}

	var doc dbq.Document
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		var err error
		doc, err = s.documentForLinks(ctx, q, projectID, in.Path)
		if err != nil {
			return err
		}
		entityID, err := s.resolveEntity(ctx, q, projectID, "", LinkTarget{
			EntityType: in.EntityType, EntityKey: in.EntityKey,
		})
		if err != nil {
			return err
		}
		rows, err := q.DeleteDocumentLink(ctx, dbq.DeleteDocumentLinkParams{
			ProjectID: projectID, DocumentID: doc.ID, EntityID: entityID,
		})
		if err != nil {
			return fmt.Errorf("detach document: %w", err)
		}
		if rows == 0 {
			return &MissingError{
				Path: "entity_key",
				Message: fmt.Sprintf("the document at %q is not attached to the %s %q",
					in.Path, in.EntityType, in.EntityKey),
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.publish(projectID, eventDocumentLinked, documentEventMinRole, documentEventHumanOnly,
		DocumentEvent{ID: doc.ID, Path: doc.Path, Version: doc.CurrentVersion})
	return nil
}

// The bounds on one page of attachments, on either side of the join.
const (
	// DefaultLinkPage and MaxLinkPage bound one page of LinksByDocument
	// and of LinksByEntity.
	DefaultLinkPage int32 = 50
	MaxLinkPage     int32 = 200
)

// LinksFilter is the paging half of a link listing: where to continue
// from, and how much to ask for. It is one type for both directions
// because the two answer the same question from two ends.
type LinksFilter struct {
	Cursor string
	Limit  int32
}

// EntityLinkPage is one page of a document's attachments plus the cursor
// for the next.
type EntityLinkPage struct {
	Links      []EntityLink
	NextCursor string
}

// DocumentLinkPage is one page of an entity's documents plus the cursor
// for the next. Cursor.Sort, for this listing, is the document's path,
// the same key the documents listing walks.
type DocumentLinkPage struct {
	Links      []DocumentLink
	NextCursor string
}

// LinksByDocument lists one page of everything a document is attached
// to.
func (s *Service) LinksByDocument(ctx context.Context, projectID uuid.UUID, path string,
	f LinksFilter,
) (EntityLinkPage, error) {
	if err := CheckPath(path); err != nil {
		return EntityLinkPage{}, err
	}
	doc, err := s.documentForLinks(ctx, s.q, projectID, path)
	if err != nil {
		return EntityLinkPage{}, err
	}
	limit := paging.Size(f.Limit, DefaultLinkPage, MaxLinkPage)
	fingerprint := documentLinksFingerprint(projectID, doc.ID)
	after, err := paging.Decode(f.Cursor, fingerprint, refuseCursor)
	if err != nil {
		return EntityLinkPage{}, err
	}
	params := dbq.ListDocumentLinksByDocumentParams{
		ProjectID: projectID, DocumentID: doc.ID, Limit: limit,
	}
	if after.ID != uuid.Nil {
		id := after.ID
		sort := after.Sort
		params.AfterID = &id
		params.AfterKey = &sort
	}
	rows, err := s.q.ListDocumentLinksByDocument(ctx, params)
	if err != nil {
		return EntityLinkPage{}, fmt.Errorf("list links by document: %w", err)
	}
	// Never nil: an empty slice marshals as [], and "this document is attached
	// to nothing" must not reach a client as null. TestLinksArea's "a document
	// with no attachments is an ordinary document" case pins it, through
	// encoding/json rather than by asserting non-nilness alone.
	page := EntityLinkPage{Links: make([]EntityLink, 0, len(rows))}
	for _, row := range rows {
		page.Links = append(page.Links, EntityLink{
			EntityID: row.EntityID, EntityTypeKey: row.EntityTypeKey,
			EntityKey: row.EntityKey, EntityName: row.EntityName, Role: row.Role,
		})
	}

	if len(rows) == int(limit) {
		last := rows[len(rows)-1]
		page.NextCursor = paging.Encode(paging.Cursor{
			Sort: last.EntityKey, ID: last.EntityID, Fingerprint: fingerprint,
		})
	}
	return page, nil
}

// documentLinksFingerprint is the resolved listing one attachment cursor
// belongs to: the game, this side of this domain's join, then the
// document.
func documentLinksFingerprint(projectID, documentID uuid.UUID) string {
	return paging.Fingerprint(projectID.String(), "document_links_by_document",
		documentID.String())
}

// LinksByEntity lists every document attached to one entity. This is how
// the UI builds an entity page, and how an agent asked to rewrite the
// Hogger dialogue finds the document from the quest.
func (s *Service) LinksByEntity(ctx context.Context, projectID uuid.UUID,
	entityType, entityKey string, f LinksFilter,
) (DocumentLinkPage, error) {
	if problems := entityAddressProblems("", LinkTarget{
		EntityType: entityType, EntityKey: entityKey,
	}); len(problems) > 0 {
		return DocumentLinkPage{}, invalidInputProblems(problems)
	}
	entityID, err := s.resolveEntity(ctx, s.q, projectID, "", LinkTarget{
		EntityType: entityType, EntityKey: entityKey,
	})
	if err != nil {
		return DocumentLinkPage{}, err
	}
	limit := paging.Size(f.Limit, DefaultLinkPage, MaxLinkPage)
	// The *resolved* entity id, not the caller's two keys, so two
	// spellings of one key give one fingerprint and a cursor survives a
	// respelling of its own address — the decision documentListing
	// Fingerprint makes for the entity filter, applied here.
	fingerprint := entityLinksFingerprint(projectID, entityID)
	after, err := paging.Decode(f.Cursor, fingerprint, refuseCursor)
	if err != nil {
		return DocumentLinkPage{}, err
	}
	params := dbq.ListDocumentLinksByEntityParams{
		ProjectID: projectID, EntityID: entityID, Limit: limit,
	}
	if after.ID != uuid.Nil {
		id := after.ID
		sort := after.Sort
		params.AfterID = &id
		params.AfterPath = &sort
	}
	rows, err := s.q.ListDocumentLinksByEntity(ctx, params)
	if err != nil {
		return DocumentLinkPage{}, fmt.Errorf("list links by entity: %w", err)
	}
	page := DocumentLinkPage{Links: make([]DocumentLink, 0, len(rows))}
	for _, row := range rows {
		page.Links = append(page.Links, DocumentLink{
			DocumentID: row.DocumentID, Path: row.Path,
			Title: row.Title, Kind: row.Kind, Role: row.Role,
		})
	}
	if len(rows) == int(limit) {
		last := rows[len(rows)-1]
		page.NextCursor = paging.Encode(paging.Cursor{
			Sort: last.Path, ID: last.DocumentID, Fingerprint: fingerprint,
		})
	}
	return page, nil
}

// entityLinksFingerprint is documentLinksFingerprint for the other
// direction, and the two names differ deliberately: see that function.
func entityLinksFingerprint(projectID, entityID uuid.UUID) string {
	return paging.Fingerprint(projectID.String(), "document_links_by_entity",
		entityID.String())
}

// documentForLinks resolves a path to the document links hang from. A
// soft-deleted document is not found: attaching prose nobody can read would
// put a dead entry on an entity page. TestLinksArea's "a deleted document
// is not an address for links" case pins both directions.
func (s *Service) documentForLinks(ctx context.Context, q *dbq.Queries, projectID uuid.UUID, path string) (dbq.Document, error) {
	row, err := q.GetDocumentByPath(ctx, dbq.GetDocumentByPathParams{
		ProjectID: projectID, Path: path, IncludeDeleted: false,
	})
	if err != nil {
		return dbq.Document{}, notFound(err, missingDocument(path), "resolve document")
	}
	return row, nil
}

// entityAddressKeyProblems bounds the two keys of an entity address —
// type and key, nothing else — before either lookup runs, at the
// argument's own path.
func entityAddressKeyProblems(prefix, entityType, entityKey string) []metamodel.FieldError {
	problems := metamodel.RowKeyProblems(prefix+"entity_type", entityType)
	return append(problems, metamodel.RowKeyProblems(prefix+"entity_key", entityKey)...)
}

// entityAddressProblems is entityAddressKeyProblems plus the role bound,
// for the calls that carry a role: LinkAdd and a write's own links
// array. List has no role argument and calls entityAddressKeyProblems
// directly instead; see that function's doc comment for why the
// difference matters.
func entityAddressProblems(prefix string, target LinkTarget) []metamodel.FieldError {
	problems := entityAddressKeyProblems(prefix, target.EntityType, target.EntityKey)
	return append(problems, checkShortText(prefix+"role", target.Role, MaxRoleLen)...)
}

// resolveEntity turns (entity type key, entity key) into an entity id,
// naming *which* of the two was not found.
func (s *Service) resolveEntity(ctx context.Context, q *dbq.Queries, projectID uuid.UUID,
	prefix string, target LinkTarget,
) (uuid.UUID, error) {
	typeID, err := q.GetEntityTypeIDByKey(ctx, dbq.GetEntityTypeIDByKeyParams{
		ProjectID: projectID, Key: target.EntityType,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, &MissingError{
			Path:    prefix + "entity_type",
			Message: fmt.Sprintf("this game has no entity type %q", target.EntityType),
		}
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve entity type: %w", err)
	}
	entityID, err := q.GetEntityIDByKey(ctx, dbq.GetEntityIDByKeyParams{
		ProjectID: projectID, EntityTypeID: typeID, Key: target.EntityKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, &MissingError{
			Path: prefix + "entity_key",
			Message: fmt.Sprintf("this game has no %s named %q",
				target.EntityType, target.EntityKey),
		}
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve entity: %w", err)
	}
	return entityID, nil
}

// duplicateLinkTargets finds the elements of a write's links array that
// address an entity an earlier element already addressed, folding case
// the way the entity key's own unique index does.
func duplicateLinkTargets(targets []LinkTarget) []metamodel.FieldError {
	first := make(map[string]int, len(targets))
	var problems []metamodel.FieldError
	for i, target := range targets {
		id := strings.ToLower(target.EntityType) + "\x00" + strings.ToLower(target.EntityKey)
		at, seen := first[id]
		if !seen {
			first[id] = i
			continue
		}
		problems = append(problems, metamodel.FieldError{
			Path: fmt.Sprintf("links[%d].entity_key", i),
			Message: fmt.Sprintf(
				"the %s %q is already addressed by links[%d], and entity keys are matched "+
					"without regard to case: merge the two elements into one, or drop the duplicate",
				target.EntityType, target.EntityKey, at),
		})
	}
	return problems
}

// replaceLinks is the links array a write carries. It runs inside the
// write's own transaction, so a bad link rolls the body back with it: a
// document and what it is about are one change, and half of it landing
// would attach a script to a quest it no longer describes. TestLinksArea's
// "a bad link in a write rolls the whole write back" case pins both halves.
func (s *Service) replaceLinks(ctx context.Context, q *dbq.Queries, projectID uuid.UUID,
	documentID uuid.UUID, targets []LinkTarget,
) error {
	// Checked whole, before anything is written, for the reason
	// duplicateLinkTargets' own comment argues: a repetition has no
	// per-element answer in a call that lands everything or nothing.
	if problems := duplicateLinkTargets(targets); len(problems) > 0 {
		return invalidInputProblems(problems)
	}
	keep := make([]uuid.UUID, 0, len(targets))
	for i, target := range targets {
		prefix := fmt.Sprintf("links[%d].", i)
		if problems := entityAddressProblems(prefix, target); len(problems) > 0 {
			return invalidInputProblems(problems)
		}
		entityID, err := s.resolveEntity(ctx, q, projectID, prefix, target)
		if err != nil {
			return err
		}
		if _, err := q.UpsertDocumentLink(ctx, dbq.UpsertDocumentLinkParams{
			ProjectID: projectID, DocumentID: documentID, EntityID: entityID, Role: target.Role,
		}); err != nil {
			return fmt.Errorf("attach document: %w", err)
		}
		keep = append(keep, entityID)
	}
	// pgx encodes a nil slice as SQL NULL, and `NOT (x = ANY(NULL))` is NULL,
	// which deletes nothing — so an empty links array would silently preserve
	// the set it was meant to clear. Measured, not assumed: with `keep`
	// declared as a nil slice instead of the make above, the empty-array case
	// of TestLinksArea's "a links array on a write replaces the set and
	// omitting it preserves it" case fails with the-defias still attached.
	if keep == nil {
		keep = []uuid.UUID{}
	}
	return q.DeleteDocumentLinksExcept(ctx, dbq.DeleteDocumentLinksExceptParams{
		ProjectID: projectID, DocumentID: documentID, Keep: keep,
	})
}
