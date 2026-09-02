package markdown

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/roles"
)

// Actor records who performed a write, for the audit columns. It is
// metamodel.Actor, not a second copy: the two domains record the same
// two kinds of actor into the same two shapes of column, and a token id
// belonging to another game is refused by the database in both
// (0007_documents.sql's composite keys), not here.
type Actor = metamodel.Actor

// Service is the markdown domain.
type Service struct {
	pool *pgxpool.Pool
	q    *dbq.Queries
	hub  *realtime.Hub
}

// New builds the service. The hub may be nil, in which case nothing is
// published.
func New(pool *pgxpool.Pool, hub *realtime.Hub) *Service {
	return &Service{pool: pool, q: dbq.New(pool), hub: hub}
}

// publish emits a change event, if a hub is attached.
//
// minRole and humanOnly are passed explicitly rather than inferred from
// kind, exactly as internal/metamodel's and internal/web's publish do,
// so each call site shows the gating it chose instead of inheriting one
// from a table three files away. The values are named constants declared
// beside the kind they belong to, in events.go, which is where the
// reasoning for each lives.
//
// **Every caller must call this after withTx has returned, never from
// inside fn.** An event published inside the transaction announces a
// change that may still roll back, and a subscriber that re-reads on
// hearing it would read the state before the change and cache it as the
// state after. TestAWriteIsAnnouncedOnlyAfterItCommits pins the refused
// case — the one this task can reach, since Write's only failures
// before the commit are refusals. The rolled-back and failed-commit
// placements are pinned in internal/metamodel
// (TestNoEventIsPublishedWhenTheCommitFails and its two neighbours) for
// the identical helper, and Task 12 exercises this package's own.
func (s *Service) publish(projectID uuid.UUID, kind string, minRole roles.Role, humanOnly bool, payload any) {
	if s.hub == nil {
		return
	}
	s.hub.Publish(realtime.Event{
		ProjectID: projectID,
		Kind:      kind,
		MinRole:   string(minRole),
		HumanOnly: humanOnly,
		Payload:   payload,
	})
}

// withTx runs fn inside a transaction, rolling back unless it returns
// nil. Every mutation here needs one: a document row and the version row
// that records it are one change, and half of it landing would leave a
// document whose current_version names a snapshot that does not exist.
func (s *Service) withTx(ctx context.Context, fn func(*dbq.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(dbq.New(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// The bounds on the two short strings a caller may attach to a write.
//
// Exported for the rule the metamodel established for MaxSearchQuery: a
// bound a caller cannot read is a bound a caller trips over, and the
// tool descriptions are built with these values interpolated rather than
// typed out.
//
// Both are bounds on the caller's own bytes and relate to nothing the
// database applies: `kind` is not part of the generated search vector at
// all, and `message` lives on document_versions, which has no vector.
const (
	// MaxKindLen bounds `kind`, the free-text grouping label a project
	// puts on a document ("lore", "script", "pitch"). Maestro attaches
	// no meaning to it and ships no vocabulary — the same rule that
	// forbids a built-in Quest type forbids a built-in Lore kind.
	MaxKindLen = 64

	// MaxMessageLen bounds the "why this edit" line a version records.
	// It is a commit message, not a changelog entry: the change itself
	// is in the diff.
	MaxMessageLen = 500
)

// checkShortText is the one rule every single-line caller string in this
// package obeys, reported at the argument's own path.
//
// **The judgement is metamodel.CheckText and this function is only the
// wording**, which is Task 2's correction 14 applied where it said it
// would be: SplitContent had reimplemented that scan once already, and a
// third copy here — with its own ordering and its own allowance — is the
// drift errors.go's package comment takes the metamodel dependency to
// avoid. The allowance is the one thing this caller states for itself,
// and it is the empty string: newline, carriage return and tab are all
// refused here and all allowed in a body, because a body is prose whose
// line breaks are the writing while a kind or a message is one line
// rendered in a listing row. Refuse, never strip — deleting part of a
// caller's input answers a question it did not ask.
//
// The encoding is checked before the control scan, and that ordering is
// CheckText's own: ranging over a string turns an invalid byte into
// U+FFFD, which is not a control character, so a scan alone lets an
// invalid sequence through to Postgres and its SQLSTATE 22021.
// TestAKindAndAMessageAreBoundedAsTheCallersOwnArguments pins the length
// bound, both control refusals and the invalid-UTF-8 one.
func checkShortText(path, value string, max int) []metamodel.FieldError {
	problem := func(message string) []metamodel.FieldError {
		return []metamodel.FieldError{{Path: path, Message: message}}
	}
	if len(value) > max {
		return problem(fmt.Sprintf("must be at most %d bytes, and this one is %d", max, len(value)))
	}
	fault, bad := metamodel.CheckText(value, "")
	switch {
	case !bad:
		return nil
	case fault.InvalidUTF8:
		return problem("is not valid UTF-8: a byte in it does not decode as any character, " +
			"and Postgres refuses that outright")
	default:
		return problem(fmt.Sprintf(
			"holds a control character (%U at byte %d): this is one line of text",
			fault.Rune, fault.Offset))
	}
}

// notFound maps pgx's no-rows sentinel onto a named miss, leaving every
// other error wrapped with what was being looked up.
func notFound(err error, missing error, doing string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return missing
	}
	return fmt.Errorf("%s: %w", doing, err)
}

// oversizeForIndex recognises a write refused because a value was too
// large for the generated search vector, and reports it as the caller's
// own input rather than as a server fault; every other error passes
// through unchanged.
//
// This path is reachable, not merely a backstop, though not by the route
// the plan first described. left(title, 131072), left(summary, 131072)
// and left(body_md, 131072) in 0007_documents.sql bound every input to
// the generated expression, so a large *body* is truncated for the index
// rather than refused: it writes successfully and is indexed by its
// first 131072 characters only (Task 1's correction 2, and MaxBodyBytes'
// own comment). What reaches this handler is the case those bounds do
// not cover — a row whose three bounded pieces together still exceed
// to_tsvector's own 1,048,575-byte limit, which raises SQLSTATE 54000
// out of the INSERT or UPDATE itself. That is the shape
// metamodel.searchLimitExceeded takes for the same SQLSTATE, and it is
// here for the same reason: left unmapped, 54000 lands on the default
// arm as internal_error, which tells an agent to give up on a call it
// could have fixed by writing less.
//
// **No test in this package reaches it.** Building a row that trips it
// takes roughly a megabyte of multi-byte text past three `left()` calls,
// which is more than MaxBodyBytes admits for the body alone; it is kept
// because the alternative to an unreached mapping here is an
// internal_error for whoever does reach it in production. Every caller
// that reaches writeWith's guarded upsert is exposed to it alike,
// resurrection included: a write that brings a deleted path back is
// still a Write, and its content is still the caller's own, revalidated
// by SplitContent like any other. What is not exposed is Delete's own
// tombstone insert — it writes document_versions from the row's already-
// stored, already-validated values, and document_versions carries no
// generated tsvector of its own, so the failure this function maps
// cannot arise from that statement at all.
func oversizeForIndex(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "54000" {
		return err
	}
	return invalidInput("content", fmt.Sprintf(
		"is too large to index (%s): write less in one document", pgErr.Message))
}

// pathRespellingError is what a caller sees when its path matches an
// existing document's only case-insensitively.
//
// It is metamodel.keyRespellingError applied to a path, and the argument
// is the same one: silently updating the differently-spelled document
// would let a typo'd capital overwrite content, and letting the database
// raise it would surface as a version conflict — which says nothing
// about the actual problem — because the upsert carries an ON CONFLICT
// clause. Naming both spellings and both remedies is the whole answer,
// and a designer never has to know an index folds case.
// TestARespelledPathIsRefusedNamingBothSpellings pins the message and
// that the document it names is left untouched.
func pathRespellingError(requested, stored string) error {
	return invalidInput("path", fmt.Sprintf(
		"%q already exists here spelled %q, and paths are matched without regard to case: "+
			"use %q to update it, or pick a path that differs by more than capitalisation",
		requested, stored, stored))
}
