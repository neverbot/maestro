package metamodel

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// This file is the one call that changes a type's key.
//
// **Why a rename exists at all, when every other key in this product is
// permanent.** Both type upserts are addressed by key and idempotent by
// it, so writing a different key creates a *second* type and leaves the
// first standing: fixing a misspelled handle meant declaring the new
// type, moving every entity onto it and deleting the old one — several
// calls, and the history and the version go with the row that is
// deleted. What decided it is that the machinery a rename needs was
// already built and idle. internal/views resolves a saved view's type
// references **by id** precisely so that a rename is transparent to the
// picture, and two of its eight staleness diagnostics —
// `entity_type_renamed` and `relation_type_renamed` — exist to report a
// document that still spells a type the old way. All of that shipped for
// a state no caller could produce. This call is its producer.
//
// **A rename moves the catalogue row and nothing else, and the "nothing
// else" is the load-bearing half.** In particular it does *not* tidy the
// recorded key in view_refs and does not rewrite a stored query
// document. The staleness design rests on the document and the index
// continuing to agree on the **old** spelling while only the catalogue
// moves: that agreement is what makes resolution fall to the by-id arm
// (view_refs.entity_type_id / .relation_type_id are untouched by a
// rename) and what makes the run report `*_renamed` with both spellings
// at the pointer that has to change. A rename that helpfully updated the
// index would break the agreement — the ref would carry the new key and
// the query the old one, resolution would fall through to the by-key
// lookup, find nothing, and every affected view would report its type
// *missing*: the feature causing the exact failure it exists to prevent.
// TestARenameLeavesTheViewReferenceIndexSpellingTheOldKey
// (internal/views) is what holds that hands-off.
//
// **Repair stays an explicit views.upsert**, for the reason
// internal/views/stale.go already gives about every other kind of
// staleness: rewriting a stored query behind its author would make the
// next expected_version check pass against a document nobody wrote.
//
// **No child row is touched because no child row carries the key.**
// Entities point at entity_types.id, relations at relation_types.id,
// endpoint lists hold entity type ids, view_refs hold ids, and the
// entity search vector indexes an entity's own key and never its type's.
// So a rename is an UPDATE of one column under the row's own version,
// plus an event.

// RenameInput is one change of a type's key.
//
// From and To are both required and both judged as row keys, in one
// pass, so a caller that mistyped each of them hears about both rather
// than fixing one, calling again and learning about the other — the rule
// every multi-argument write in this repository obeys.
//
// **From is an address and To is a value.** From is matched without
// regard to case, exactly as types.get and types.remove match theirs, so
// a caller that read `Quest` and renames `quest` reaches the same row; it
// is deliberately *not* refused as a respelling, because nothing about
// the spelling of the address is being stored. To is the handle that
// will be stored, verbatim.
//
// ExpectedVersion is required, for the reason internal/markdown requires
// it on a move: a rename advances the version, so an unguarded one would
// land on top of an edit the caller never read — and it would do it to
// the one column every other reader of this game spells out loud.
type RenameInput struct {
	From            string
	To              string
	ExpectedVersion *int32
	Actor           Actor
}

// typeRenameEvent is the payload of type.renamed and
// relation_type.renamed: the row's id and both spellings.
//
// It carries identity and nothing a client could mistake for current
// content, which is this package's standing rule for a payload — the id
// is the row, and the two keys are the two spellings of that same
// identity. `to` is what the row was renamed *to* by this call rather
// than a promise about what it is keyed now: publication order is not
// commit order, so a subscriber applying two renames out of order must
// re-read like it must for every other event here.
//
// **Both spellings, and not just the new one**, because the old one is
// what a subscriber is holding. A client that cached the type under
// `available_to` cannot act on an event that says only `usable_by`: it
// has no way to know which of its entries to drop.
type typeRenameEvent struct {
	ID   uuid.UUID `json:"id"`
	From string    `json:"from"`
	To   string    `json:"to"`
}

// RenameEntityType changes an entity type's key, addressed by the old
// key and guarded by the caller's version.
//
// **Addressed by the old key rather than by id**, and that is a decision
// rather than a convenience. Metamodel 14 moved every removal onto keys
// and left no by-id segment on the REST surface at all: keys *replace*
// ids as the address on this surface rather than sitting beside them.
// Addressing a rename by the old key keeps that decision intact, and it
// reads as what it is — `types.rename(from, to)` says the whole
// operation in its arguments.
//
// **What it does not do**, stated here because it is the part a caller
// will otherwise discover from a diagnostic: it does not repair saved
// views. A view that named this type keeps resolving — it recorded the
// id — and every run of it reports the rename, at the pointer that has
// to change, until somebody saves the view again with the new spelling.
// That is the designed behaviour and not a shortcoming: the alternative
// is editing an author's document underneath them without a version
// bump.
//
// The refusals, in the order they are made:
//
//   - Both keys are judged, together, as row keys.
//   - A case-only respelling is refused. See renameToSameAddressError:
//     the unique index folds case, so the two spellings are one address
//     written two ways and the "rename" would change no address while
//     announcing a change no reader can observe. The fold comparison
//     runs *after* validation, so both values are known to be ASCII and
//     Go's EqualFold means exactly what SQL's lower() means.
//   - A destination that is already taken is refused at `to`, live or
//     merely differently spelled: a rename never merges two types.
//   - A `from` that names no type is not_found.
//   - A version that does not match is a version conflict.
func (s *Service) RenameEntityType(ctx context.Context, projectID uuid.UUID, in RenameInput) (dbq.EntityType, error) {
	if err := checkRenameInput("entity type", in); err != nil {
		return dbq.EntityType{}, err
	}

	var row dbq.EntityType
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		read := func(ctx context.Context, key string) (renamedRow, error) {
			got, err := q.GetEntityTypeByKeyForUpdate(ctx, dbq.GetEntityTypeByKeyForUpdateParams{
				ProjectID: projectID, Key: key,
			})
			return renamedRow{Key: got.Key, Version: got.Version}, err
		}
		if err := judgeRename(ctx, "entity type", in, read); err != nil {
			return err
		}

		var err error
		row, err = q.RenameEntityType(ctx, dbq.RenameEntityTypeParams{
			ProjectID:        projectID,
			FromKey:          in.From,
			ToKey:            in.To,
			ExpectedVersion:  *in.ExpectedVersion,
			UpdatedByUserID:  in.Actor.UserID,
			UpdatedByTokenID: in.Actor.TokenID,
		})
		return renameFailure(err, "entity type", in)
	})
	if err != nil {
		return dbq.EntityType{}, err
	}

	s.publish(projectID, eventTypeRenamed, typeEventMinRole, typeEventHumanOnly,
		typeRenameEvent{ID: row.ID, From: in.From, To: row.Key})
	return row, nil
}

// RenameRelationType changes a relation type's key. It is
// RenameEntityType over the other table, in every respect including the
// two things a caller most needs to know — the rename is addressed by
// the old key, and it does not repair the saved views that name this
// type — so that comment carries the argument and this one does not
// restate it.
//
// The two are separate functions rather than one generic over a table
// because the two dbq row types are two Go types with no common
// interface; what *is* shared — the validation, the two locked reads and
// their judgement, and the failure mapping — is shared, so the halves
// that could drift cannot.
func (s *Service) RenameRelationType(ctx context.Context, projectID uuid.UUID, in RenameInput) (dbq.RelationType, error) {
	if err := checkRenameInput("relation type", in); err != nil {
		return dbq.RelationType{}, err
	}

	var row dbq.RelationType
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		read := func(ctx context.Context, key string) (renamedRow, error) {
			got, err := q.GetRelationTypeByKeyForUpdate(ctx, dbq.GetRelationTypeByKeyForUpdateParams{
				ProjectID: projectID, Key: key,
			})
			return renamedRow{Key: got.Key, Version: got.Version}, err
		}
		if err := judgeRename(ctx, "relation type", in, read); err != nil {
			return err
		}

		var err error
		row, err = q.RenameRelationType(ctx, dbq.RenameRelationTypeParams{
			ProjectID:        projectID,
			FromKey:          in.From,
			ToKey:            in.To,
			ExpectedVersion:  *in.ExpectedVersion,
			UpdatedByUserID:  in.Actor.UserID,
			UpdatedByTokenID: in.Actor.TokenID,
		})
		return renameFailure(err, "relation type", in)
	})
	if err != nil {
		return dbq.RelationType{}, err
	}

	s.publish(projectID, eventRelationTypeRenamed, relationTypeEventMinRole,
		relationTypeEventHumanOnly, typeRenameEvent{ID: row.ID, From: in.From, To: row.Key})
	return row, nil
}

// renamedRow is the two columns judgeRename reads off either table's
// locked row. Neither dbq type implements an interface, and a rename
// needs nothing from a type but its stored spelling and its version.
type renamedRow struct {
	Key     string
	Version int32
}

// checkRenameInput judges a rename's arguments before any transaction is
// opened: both keys in one pass, the version claim, and only then the
// fold.
//
// **The fold comparison runs after validation and that ordering is
// load-bearing**, which is why it is here rather than at the top. Both
// values are known to be ASCII once rowKeyProblems has passed
// (rowKeyPattern), and that is exactly what makes strings.EqualFold mean
// what lower() means in entity_types_key_key. On arbitrary Unicode the
// two disagree, and a pre-flight check that disagreed with the index
// would refuse renames the database would have allowed. internal/markdown
// states the same rule for document paths at Move, and this is the third
// place in the repository to obey it — the other two being that move and
// keyRespellingError on the write path.
func checkRenameInput(what string, in RenameInput) error {
	problems := rowKeyProblems("from", in.From)
	problems = append(problems, rowKeyProblems("to", in.To)...)
	if in.ExpectedVersion == nil {
		problems = append(problems, FieldError{
			Path: "expected_version",
			Message: fmt.Sprintf(
				"is required: pass the version you read, so a rename cannot land on top of "+
					"an edit of this %s you never saw", what),
		})
	}
	if len(problems) > 0 {
		return &ValidationError{Code: codeInvalidInput, Fields: problems}
	}
	if strings.EqualFold(in.From, in.To) {
		return renameToSameAddressError(what, in.From, in.To)
	}
	return nil
}

// judgeRename takes the row lock on whichever of the two keys exist and
// decides what the rename is allowed to do.
//
// **The two locks are taken in folded-key order, not from-then-to
// order**, and that is what keeps two opposite renames from deadlocking:
// renaming `a` to `b` while another caller renames `b` to `a` would
// otherwise have each transaction holding the lock the other needs, and
// Postgres would break it with SQLSTATE 40P01 over a pair of writes with
// a perfectly good serial order. Ordering by a value both transactions
// compute the same way removes the cycle. It is the same rule
// internal/markdown's lockBothEnds applies to a document move, and the
// ordering key is the same kind of thing — a data-dependent order of two
// rows in one table. checkRenameInput has already refused a From and To
// that fold together, so the order is total.
//
// The judgement order — destination, then the source's existence, then
// its version — is the order every refusal in this repository is made
// in: a caller failing for two reasons hears the one it can act on, and
// "merge onto version 4" is not actionable when the retry will meet the
// same occupied destination.
func judgeRename(ctx context.Context, what string, in RenameInput,
	read func(context.Context, string) (renamedRow, error),
) error {
	first, second := in.From, in.To
	if strings.ToLower(second) < strings.ToLower(first) {
		first, second = second, first
	}
	rows := map[string]*renamedRow{}
	for _, key := range []string{first, second} {
		row, err := read(ctx, key)
		switch {
		case err == nil:
			locked := row
			rows[strings.ToLower(key)] = &locked
		case errors.Is(err, pgx.ErrNoRows):
			// Absent is the ordinary case for the destination and the
			// refused case for the source; both are judged below, once
			// both locks are held, so the judgement does not depend on
			// which key sorted first.
		default:
			return fmt.Errorf("lock %s for rename: %w", what, err)
		}
	}

	if taken := rows[strings.ToLower(in.To)]; taken != nil {
		return renameDestinationTakenError(what, in.To, taken.Key)
	}
	source := rows[strings.ToLower(in.From)]
	if source == nil {
		return fmt.Errorf("%w: no %s %q in this game", ErrNotFound, what, in.From)
	}
	if *in.ExpectedVersion != source.Version {
		return &VersionConflictError{Current: source.Version}
	}
	return nil
}

// renameFailure maps what the guarded UPDATE can answer with.
//
// The no-rows arm is unreachable while judgeRename holds the source's
// FOR UPDATE lock — nothing can move the row or its version between that
// read and this statement — and it is kept for the reason
// internal/markdown keeps the same unreachable arm in Move: a bare
// pgx.ErrNoRows escaping this function would reach an agent as
// internal_error, which is the one thing no refusal in this repository
// is allowed to be. It reports the version claim, because a guard that
// matched nothing with the row present can only mean the version moved.
//
// The unique-violation arm is *not* defensive. Two renames onto one free
// key both find the destination free under their own locks — they lock
// different rows, so neither waits for the other — and the loser meets
// entity_types_key_key here.
func renameFailure(err error, what string, in RenameInput) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return &VersionConflictError{Current: *in.ExpectedVersion}
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		// The winner's stored spelling is unavailable here — this
		// transaction never saw its row — so the message names what the
		// caller sent, which is the honest thing to name.
		return &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
			Path: "to",
			Message: fmt.Sprintf(
				"names a key another %s in this game already has (%q): another write took "+
					"it while this rename was in flight, so read it and pick a key that is free",
				what, in.To),
		}}}
	}
	if mapped := ActorConstraintViolation(err); errors.Is(mapped, ErrActorNotInGame) {
		return mapped
	}
	return fmt.Errorf("rename %s: %w", what, err)
}

// renameToSameAddressError refuses a rename whose two ends are one
// address.
//
// **A case-only respelling is refused, and this is not a fresh
// judgement.** entity_types_key_key and relation_types_key_key are
// UNIQUE (project_id, lower(key)), so `Quest` and `quest` are not two
// keys — they are one key spelled two ways, and every reader in this
// package already finds the row under either. A "rename" between them
// would therefore change no address at all; it would rewrite a stored
// display string, and this repository has already decided twice what
// happens when a caller asks for that. keyRespellingError refuses a
// case-only respelling on the type and entity write paths rather than
// silently updating the stored spelling, and internal/markdown's Move
// refuses it for a document path — a call whose whole purpose is to
// change an address, announcing a change of address no reader can
// observe. A rename is that same call for a key, so it gets that same
// answer, and the three agree.
//
// The identical case is separated out because "differs only in
// capitalisation" would be a confusing thing to tell a caller that sent
// the same string twice.
func renameToSameAddressError(what, from, to string) error {
	if from == to {
		return &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
			Path: "to",
			Message: fmt.Sprintf(
				"is the key this %s already has (%q): a rename changes a type's handle, "+
					"and this one changes nothing", what, to),
		}}}
	}
	return &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
		Path: "to",
		Message: fmt.Sprintf(
			"differs from %q only in capitalisation, and keys are matched without regard "+
				"to case, so %q and %q are one key rather than two: the spelling a %s was "+
				"first declared under is the handle its content and its saved views refer "+
				"to and is not rewritten, so pick a key that differs by more than "+
				"capitalisation", from, from, to, what),
	}}}
}

// renameDestinationTakenError refuses a rename onto a key that already
// names a type.
//
// **The shape a duplicate declaration is refused with**: invalid_input at
// the argument's own path, naming both spellings when they differ,
// exactly as keyRespellingError does — because from the caller's side
// this is the same fact. It is deliberately not a version conflict: a
// conflict tells a caller to merge onto a version and write again, and no
// amount of retrying frees an occupied key. What the caller has to do is
// pick a different key, or deal with the type that is already there.
func renameDestinationTakenError(what, requested, stored string) error {
	spelling := ""
	if stored != requested {
		spelling = fmt.Sprintf(" (spelled %q, and keys are matched without regard to case)", stored)
	}
	return &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
		Path: "to",
		Message: fmt.Sprintf(
			"names a key another %s in this game already has%s: a rename never merges two "+
				"types, so remove that one first or rename to a key that is free",
			what, spelling),
	}}}
}
