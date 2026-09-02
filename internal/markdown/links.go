package markdown

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
)

// MaxRoleLen bounds a link's free-text role ("script", "lore",
// "backstory"). Maestro attaches no meaning to it and ships no
// vocabulary: the same rule that forbids a built-in Quest type forbids a
// built-in Script role. Whether a project should be able to *declare*
// its roles the way it declares relation types is open (spec §10.3) and
// is not settled here; what is settled is that the link's key is
// (document, entity), so free text cannot produce two attachments of one
// pair under two spellings.
//
// TestALinkRoleIsBoundedAsTheCallersOwnArgument pins the bound, its
// boundary, the control-character refusal, and that the role is stored
// exactly as written.
const MaxRoleLen = 64

// MaxLinksPerWrite bounds the links array a single write may carry.
//
// A document about a hundred entities is not a document, it is a
// taxonomy that should be entities and relations. The bound is here so
// that a caller that has confused the two is told, at its own argument,
// rather than discovering it as a slow write.
// TestAWriteCarryingTooManyAttachmentsIsRefusedAtLinks pins it, and
// pins that the refusal lands before anything is written.
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

// EntityLink is one attachment as seen from the document: which entity,
// and in what role.
//
// EntityName is carried so a UI listing a document's attachments does
// not have to fetch each entity to render a label. It is a *display*
// value on a read, not an event payload, so the "carry identity only"
// rule does not apply: a read is answered from the same transaction's
// snapshot the caller asked for, and there is no ordering hazard.
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
//
// It does not take an expected_version and does not move the document's
// own version, deliberately. A link is not the document's content: two
// people attaching one script to two different quests are not editing
// the same thing, and making them serialise on the document's version
// would turn an independent operation into a conflict. What that costs
// is that a link change is not in the version history, which is the
// right trade — the history is the history of the *writing*.
// TestLinkingIsAnnounced reads the unmoved version back off the event.
//
// **Attachment is never inferred from content.** Not from frontmatter,
// not from a heading that happens to match an entity's name. A wrong
// attachment is invisible — the document simply shows up on the wrong
// quest and nobody notices — so it is only ever what a caller asked for
// here or in a write's links array.
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
//
// Removing an attachment that is not there is not_found rather than
// silence: an agent that detached the wrong pair, or that detached the
// right pair twice, has made a mistake it can act on, and "it worked"
// tells it nothing. TestRemovingALinkLeavesTheDocumentAndTheEntity pins
// both the refusal and that neither endpoint is touched by a removal.
//
// Role is not read here, and the reason is the key: an attachment is
// identified by (document, entity), so there is no second link under
// another role for a role argument to choose between.
func (s *Service) LinkRemove(ctx context.Context, projectID uuid.UUID, in LinkInput) error {
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

// LinksByDocument lists everything one document is attached to.
//
// TestALinkAttachesADocumentToAnEntityAndReadsBackFromBothSides reads
// every column back through it, and
// TestADocumentIsAttachedToSeveralEntitiesAndListedInAStableOrder pins
// the order.
func (s *Service) LinksByDocument(ctx context.Context, projectID uuid.UUID, path string) ([]EntityLink, error) {
	if err := CheckPath(path); err != nil {
		return nil, err
	}
	doc, err := s.documentForLinks(ctx, s.q, projectID, path)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListDocumentLinksByDocument(ctx, dbq.ListDocumentLinksByDocumentParams{
		ProjectID: projectID, DocumentID: doc.ID,
	})
	if err != nil {
		return nil, fmt.Errorf("list links by document: %w", err)
	}
	// Never nil: an empty slice marshals as [], and "this document is
	// attached to nothing" must not reach a client as null.
	// TestADocumentWithNoAttachmentsIsAnOrdinaryDocument pins it,
	// through encoding/json rather than by asserting non-nilness alone.
	links := make([]EntityLink, 0, len(rows))
	for _, row := range rows {
		links = append(links, EntityLink{
			EntityID: row.EntityID, EntityTypeKey: row.EntityTypeKey,
			EntityKey: row.EntityKey, EntityName: row.EntityName, Role: row.Role,
		})
	}
	return links, nil
}

// LinksByEntity lists every document attached to one entity. This is how
// the UI builds an entity page, and how an agent asked to rewrite the
// Hogger dialogue finds the document from the quest.
//
// Soft-deleted documents are not listed: an entity page naming prose
// nobody can read is a dead link on every quest it was attached to. The
// link row itself survives the deletion and comes back with the document
// (TestAnEntityStopsListingADocumentThatWasDeleted, which pins both
// halves).
func (s *Service) LinksByEntity(ctx context.Context, projectID uuid.UUID, entityType, entityKey string) ([]DocumentLink, error) {
	if problems := entityAddressProblems("", LinkTarget{
		EntityType: entityType, EntityKey: entityKey,
	}); len(problems) > 0 {
		return nil, invalidInputProblems(problems)
	}
	entityID, err := s.resolveEntity(ctx, s.q, projectID, "", LinkTarget{
		EntityType: entityType, EntityKey: entityKey,
	})
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListDocumentLinksByEntity(ctx, dbq.ListDocumentLinksByEntityParams{
		ProjectID: projectID, EntityID: entityID,
	})
	if err != nil {
		return nil, fmt.Errorf("list links by entity: %w", err)
	}
	links := make([]DocumentLink, 0, len(rows))
	for _, row := range rows {
		links = append(links, DocumentLink{
			DocumentID: row.DocumentID, Path: row.Path,
			Title: row.Title, Kind: row.Kind, Role: row.Role,
		})
	}
	return links, nil
}

// documentForLinks resolves a path to the document links hang from. A
// soft-deleted document is not found: attaching prose nobody can read
// would put a dead entry on an entity page.
// TestADeletedDocumentIsNotAnAddressForLinks pins both directions.
func (s *Service) documentForLinks(ctx context.Context, q *dbq.Queries, projectID uuid.UUID, path string) (dbq.Document, error) {
	row, err := q.GetDocumentByPath(ctx, dbq.GetDocumentByPathParams{
		ProjectID: projectID, Path: path, IncludeDeleted: false,
	})
	if err != nil {
		return dbq.Document{}, notFound(err, missingDocument(path), "resolve document")
	}
	return row, nil
}

// entityAddressProblems bounds the caller's own strings before either
// lookup runs, at the argument's own path.
//
// The two keys go through metamodel.RowKeyProblems rather than through a
// rule written here, because they are the metamodel's keys: the rule
// that decides which spellings can exist is the one that must decide
// which spellings can be asked for, and a second copy of it in this
// package would drift the day either one is loosened. The concrete thing
// it closes is an address Postgres itself refuses — an entity key
// holding an invalid UTF-8 byte raises SQLSTATE 22021, which would reach
// an agent as internal_error over a value the agent supplied, and this
// package's standing rule is that nothing a caller can fix reports one.
// TestAnEntityAddressIsBoundedBeforePostgresSeesIt pins it, at the
// single-link path and inside a write's array.
//
// prefix is "" for the single-link calls and "links[i]." for an element
// of a write's array.
func entityAddressProblems(prefix string, target LinkTarget) []metamodel.FieldError {
	problems := metamodel.RowKeyProblems(prefix+"entity_type", target.EntityType)
	problems = append(problems, metamodel.RowKeyProblems(prefix+"entity_key", target.EntityKey)...)
	return append(problems, checkShortText(prefix+"role", target.Role, MaxRoleLen)...)
}

// resolveEntity turns (entity type key, entity key) into an entity id,
// naming *which* of the two was not found.
//
// prefix is "" for the single-link tools and "links[i]." for an element
// of a write's array, so a caller sending twenty links is told which one
// is wrong — the rule metamodel.parseIDs applies to a list of ids.
//
// **This is what the spec's proposed `entity_not_found` code was for**,
// and it is why that code does not ship: the discrimination an agent
// needs is *which argument*, and a path is that, as data. See
// MissingError's own doc comment.
// TestAMistypedEntityTypeAndAMistypedEntityKeyAreToldApart pins the two
// misses apart, and TestABadLinkInAWriteRollsTheWholeWriteBack pins the
// prefix.
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

// replaceLinks is the links array a write carries. It runs inside the
// write's own transaction, so a bad link rolls the body back with it: a
// document and what it is about are one change, and half of it landing
// would attach a script to a quest it no longer describes.
// TestABadLinkInAWriteRollsTheWholeWriteBack pins both halves.
func (s *Service) replaceLinks(ctx context.Context, q *dbq.Queries, projectID uuid.UUID,
	documentID uuid.UUID, targets []LinkTarget,
) error {
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
	// pgx encodes a nil slice as SQL NULL, and `NOT (x = ANY(NULL))` is
	// NULL, which deletes nothing — so an empty links array would
	// silently preserve the set it was meant to clear. Measured, not
	// assumed: with `keep` declared as a nil slice instead of the make
	// above, the empty-array case of
	// TestALinksArrayOnAWriteReplacesTheSetAndOmittingItPreservesIt
	// fails with the-defias still attached.
	//
	// The make above is what actually avoids it, so **this branch cannot
	// fire as the function stands** and it is not claimed to be doing
	// work today. It is kept for the one edit it does catch — a later
	// `var keep []uuid.UUID`, which reads as an equivalent
	// "optimisation" and is not one — and it is stated as unreachable
	// rather than left to look load-bearing, because a guard whose
	// argument nobody can check is how Task 3's dead post-write path
	// check survived a review round.
	if keep == nil {
		keep = []uuid.UUID{}
	}
	return q.DeleteDocumentLinksExcept(ctx, dbq.DeleteDocumentLinksExceptParams{
		ProjectID: projectID, DocumentID: documentID, Keep: keep,
	})
}
