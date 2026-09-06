package markdown

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
)

// MoveInput is one change of address.
//
// From and To are both required and both judged as paths, in one pass,
// so a caller that mistyped each of them hears about both rather than
// fixing one, calling again and learning about the other — the rule
// checkWrite and Delete already obey.
//
// Message is recorded on the version the move appends, so "why did this
// end up here" is answerable from the history. ExpectedVersion is
// required for the reason it is required on a write and on a delete: a
// move advances the version, so an unguarded one would silently land on
// top of an edit the caller never read.
type MoveInput struct {
	From            string
	To              string
	Message         string
	ExpectedVersion *int32
	Actor           Actor
}

// Move changes a document's path, keeping the document.
//
// **This is the whole point of the call and it is worth stating as a
// contract rather than as an implementation note.** Before it existed, a
// document written to the wrong path could only be re-written at the
// right one and deleted at the old one, which forked its history in two:
// the new path started at version one holding none of what came before,
// and the old path kept everything under a tombstone nobody would think
// to look at. Everything in this domain that is not the path hangs off
// documents.id — every version row, every link row — so a move is one
// UPDATE and none of them has to be touched. What survives a move, and
// is pinned by name: the version numbering continues rather than
// restarting (TestAMovedDocumentKeepsItsHistoryAndItsNumbering), every
// attachment stays attached (TestAMovedDocumentKeepsItsLinks), and the
// document's id, kind and creator are unchanged.
//
// **A case-only move is refused, and that is a decision rather than an
// oversight.** documents_path_key is UNIQUE (project_id, lower(path)),
// so `lore/Duskwood` and `lore/duskwood` are not two addresses — they
// are one address spelled two ways, and every reader in this package
// already finds the document under either. A "move" between them would
// therefore change no address at all; it would rewrite a stored display
// string, and this repository has already decided what happens when a
// caller asks for that. metamodel.keyRespellingError refuses a case-only
// respelling of a row key rather than silently updating the stored one,
// and pathRespellingError refuses it for a document path on the write
// path, for the reason both comments give: the stored spelling is the
// handle other things refer to, and a typo'd capital must not be able to
// move it under them. A move is a *stronger* case for the same answer,
// not a weaker one — it is the call that would make the rewrite explicit
// and durable, appending a version row and an event announcing a change
// of address that no reader can observe. So the first spelling stored
// stands here too, and the refusal names both spellings and says what a
// caller who genuinely wants a different subtree should do instead.
// TestACaseOnlyMoveIsRefusedAsARespelling pins it.
//
// **Only a live document moves.** A deleted path keeps its tombstone and
// its history exactly where they are; the recovery is to write to the
// path and bring the document back, and then move it. Moving a tombstone
// would have to answer what happens to the resurrection rule at two
// paths at once, and there is no question anyone is asking that it
// answers. TestMovingADeletedDocumentSaysToBringItBackFirst pins the
// refusal and its wording.
//
// **An occupied destination is refused, live or tombstoned.** The unique
// index covers soft-deleted rows, so a path someone deleted is still
// taken; merging two histories onto one path is not something this call
// does quietly. Both refusals name the destination and its remedy
// (TestMovingOntoALiveDocumentIsRefusedAtTheDestination,
// TestMovingOntoADeletedPathSaysItsHistoryIsStillThere).
func (s *Service) Move(ctx context.Context, projectID uuid.UUID, in MoveInput) (dbq.Document, error) {
	// One pass over every argument, as Write and Delete do.
	problems := pathProblemsAt("from", in.From)
	problems = append(problems, pathProblemsAt("to", in.To)...)
	problems = append(problems, checkShortText("message", in.Message, MaxMessageLen)...)
	if in.ExpectedVersion == nil {
		problems = append(problems, metamodel.FieldError{
			Path:    "expected_version",
			Message: "is required: pass the version you read, so a move cannot race an edit",
		})
	}
	if len(problems) > 0 {
		return dbq.Document{}, invalidInputProblems(problems)
	}

	// After the path checks, never before: both values are known to be
	// ASCII by then (pathSegmentPattern), which is what makes
	// strings.EqualFold here mean exactly what lower() means in
	// documents_path_key. On arbitrary Unicode the two differ, and a
	// pre-flight check that disagreed with the index would refuse moves
	// the database would have allowed.
	if strings.EqualFold(in.From, in.To) {
		return dbq.Document{}, sameAddressError(in.From, in.To)
	}

	var moved dbq.Document
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		if err := s.lockBothEnds(ctx, q, projectID, in); err != nil {
			return err
		}
		row, err := q.MoveDocument(ctx, dbq.MoveDocumentParams{
			ProjectID:       projectID,
			FromPath:        in.From,
			ToPath:          in.To,
			ExpectedVersion: *in.ExpectedVersion,
			ActorUserID:     in.Actor.UserID,
			ActorTokenID:    in.Actor.TokenID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// The locked read above already told the three cases apart,
			// so reaching here means the row moved between that read and
			// this statement — which cannot happen while we hold its
			// lock. Kept for the reason writeWith keeps its own
			// unreachable arms: a bare pgx.ErrNoRows escaping this
			// function would reach an agent as internal_error.
			return s.moveRefusal(ctx, q, projectID, in)
		}
		if err != nil {
			// Two moves onto one free path: both locked reads found the
			// destination free, the loser meets documents_path_key here.
			// Nothing earlier can tell them apart, which is why this arm
			// is real and not defensive.
			if taken := destinationTaken(err, in.To); taken != nil {
				return taken
			}
			if mapped := metamodel.ActorConstraintViolation(err); errors.Is(mapped, ErrActorNotInGame) {
				return mapped
			}
			return fmt.Errorf("move document: %w", err)
		}
		moved = row
		// The snapshot the move appends carries the document exactly as
		// it stands — same title, summary, body and frontmatter as the
		// version before it — at the *new* path. That equality is the
		// point: "this version is a move" is what a reader concludes
		// from a snapshot whose content did not change and whose path
		// did, rather than from a flag it would have to trust.
		// TestTheVersionAMoveAppendsCarriesTheNewPathAndTheOldContent
		// pins both halves.
		if _, err := q.InsertDocumentVersion(ctx, dbq.InsertDocumentVersionParams{
			ProjectID:     projectID,
			DocumentID:    row.ID,
			Version:       row.CurrentVersion,
			Path:          row.Path,
			Title:         row.Title,
			Summary:       row.Summary,
			BodyMd:        row.BodyMd,
			Frontmatter:   row.Frontmatter,
			Message:       in.Message,
			Deleted:       false,
			AuthorUserID:  in.Actor.UserID,
			AuthorTokenID: in.Actor.TokenID,
		}); err != nil {
			if mapped := metamodel.ActorConstraintViolation(err); errors.Is(mapped, ErrActorNotInGame) {
				return mapped
			}
			return fmt.Errorf("insert move version: %w", err)
		}
		return nil
	})
	if err != nil {
		return dbq.Document{}, err
	}
	s.publish(projectID, eventDocumentMoved, documentEventMinRole, documentEventHumanOnly,
		MoveEvent{ID: moved.ID, From: in.From, To: moved.Path, Version: moved.CurrentVersion})
	return moved, nil
}

// lockBothEnds takes the row lock on whichever of the two paths exist and
// judges them.
//
// **The two locks are taken in folded-path order, not source-then-
// destination order, and that is what keeps two opposite moves from
// deadlocking.** Moving `a` to `b` while another caller moves `b` to `a`
// would otherwise have each transaction holding the lock the other
// needs; Postgres would break it with SQLSTATE 40P01 and one caller
// would meet a deadlock over a pair of writes that have a perfectly good
// serial order. Ordering by a value both transactions compute the same
// way removes the cycle. This is the same rule the metamodel applies to
// an entity write, which locks the entity type before the entity row in
// both of its writers, and the reason it is stated again here is that
// the ordering key is different: there, a fixed order of two *tables*;
// here, a data-dependent order of two rows in one table.
// TestTwoOppositeMovesDoNotDeadlock pins it.
//
// Move has already refused From and To folding together, so the order is
// total: no two calls can disagree about which end goes first.
func (s *Service) lockBothEnds(ctx context.Context, q *dbq.Queries, projectID uuid.UUID,
	in MoveInput,
) error {
	first, second := in.From, in.To
	if strings.ToLower(second) < strings.ToLower(first) {
		first, second = second, first
	}
	rows := map[string]*dbq.Document{}
	for _, path := range []string{first, second} {
		row, err := q.GetDocumentByPathForUpdate(ctx, dbq.GetDocumentByPathForUpdateParams{
			ProjectID: projectID, Path: path,
		})
		switch {
		case err == nil:
			locked := row
			rows[strings.ToLower(path)] = &locked
		case errors.Is(err, pgx.ErrNoRows):
			// Absent is the ordinary case for the destination and the
			// refused case for the source; both are judged below, once
			// both locks are held, so the judgement does not depend on
			// which end sorted first.
		default:
			return fmt.Errorf("lock document for move: %w", err)
		}
	}

	if target := rows[strings.ToLower(in.To)]; target != nil {
		return destinationOccupiedError(in.To, target.Path, target.DeletedAt.Valid)
	}
	source := rows[strings.ToLower(in.From)]
	if source == nil {
		return &MissingError{
			Path:    "from",
			Message: fmt.Sprintf("this game has no document at %q", in.From),
		}
	}
	if source.DeletedAt.Valid {
		return &MissingError{
			Path: "from",
			Message: fmt.Sprintf("the document at %q was deleted; write to the path to bring "+
				"it back, then move it", in.From),
		}
	}
	// The destination is checked before the version, and the source's
	// existence before its version, for the ordering reason
	// writeWith states: a caller failing for two reasons hears the one
	// it can act on, and "that path is taken" is actionable where "merge
	// onto version 4 and try again" is not, when the retry will meet the
	// same occupied destination.
	if *in.ExpectedVersion != source.CurrentVersion {
		// IncludeCurrent is false, for the reason deleteRefusal gives:
		// a caller changing an address is not merging prose, and the
		// body is the largest payload in the system.
		return s.conflictOn(ctx, q, projectID, *source, false)
	}
	return nil
}

// moveRefusal names what stood in the way of a move whose guarded UPDATE
// matched no row. It is the same three-way re-read deleteRefusal makes,
// and it is reachable only if the row changed under a lock this
// transaction holds — see the call site.
func (s *Service) moveRefusal(ctx context.Context, q *dbq.Queries, projectID uuid.UUID,
	in MoveInput,
) error {
	row, err := q.GetDocumentByPath(ctx, dbq.GetDocumentByPathParams{
		ProjectID: projectID, Path: in.From, IncludeDeleted: true,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return &MissingError{
			Path:    "from",
			Message: fmt.Sprintf("this game has no document at %q", in.From),
		}
	}
	if err != nil {
		return fmt.Errorf("re-read document after a refused move: %w", err)
	}
	if row.DeletedAt.Valid {
		return &MissingError{
			Path: "from",
			Message: fmt.Sprintf("the document at %q was deleted; write to the path to bring "+
				"it back, then move it", in.From),
		}
	}
	return s.conflictOn(ctx, q, projectID, row, false)
}

// sameAddressError is what a caller sees when the two ends of its move
// fold to one address. Move's own doc comment carries the argument; this
// is only the wording, and it names both spellings the way
// pathRespellingError does, because the caller can see no difference
// between them from the outside.
func sameAddressError(from, to string) error {
	if from == to {
		return invalidInput("to", fmt.Sprintf(
			"is the path the document is already at (%q): a move changes a document's "+
				"address, and this one changes nothing", to))
	}
	return invalidInput("to", fmt.Sprintf(
		"differs from %q only in capitalisation, and paths are matched without regard to "+
			"case, so %q and %q are one address rather than two: the spelling a document "+
			"was first written under is the handle its links and its history refer to and "+
			"is not rewritten, so pick a path that differs by more than capitalisation",
		from, from, to))
}

// destinationOccupiedError is the refusal for a move onto a path that is
// already taken, with a different remedy for each of the two ways it can
// be taken.
//
// It reports at "to" and as invalid_input rather than as a conflict, and
// the distinction is the one ConflictError exists for: a conflict tells
// a caller to merge onto a version and write again, and no amount of
// retrying frees an occupied path. What the caller has to do is pick a
// different destination, or deal with what is already there.
func destinationOccupiedError(requested, stored string, deleted bool) error {
	spelling := ""
	if stored != requested {
		spelling = fmt.Sprintf(" (spelled %q, and paths are matched without regard to case)", stored)
	}
	if deleted {
		return invalidInput("to", fmt.Sprintf(
			"names a path a document was deleted at%s, and its history is still there: "+
				"write to %q to bring that document back, or move to a path that is free",
			spelling, stored))
	}
	return invalidInput("to", fmt.Sprintf(
		"names a path this game already has a document at%s: a move never merges two "+
			"documents, so delete that one first or move to a path that is free", spelling))
}

// destinationTaken recognises a move refused by documents_path_key and
// reports it as the caller's own destination rather than as a server
// fault. It returns nil for every other error, so the caller keeps its
// own arms.
//
// The stored spelling is unavailable here — the losing transaction never
// saw the winner's row — so the message names only what the caller sent,
// which is the honest thing to name.
func destinationTaken(err error, to string) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" ||
		pgErr.ConstraintName != "documents_path_key" {
		return nil
	}
	return invalidInput("to", fmt.Sprintf(
		"names a path this game already has a document at (%q): another write took it "+
			"while this move was in flight, so read it and pick a path that is free", to))
}
