package metamodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/roles"
)

// Actor records who performed a write, for the audit columns. Both fields
// are optional and both may be set at once: a human editing through the UI
// carries a UserID, an agent carries the TokenID of the credential it
// authenticated with, and a token always belongs to the member who minted
// it. A token id that belongs to another game is rejected by the database,
// not here (0004_metamodel.sql's composite keys).
type Actor struct {
	UserID  *uuid.UUID
	TokenID *uuid.UUID
}

// Service is the metamodel domain.
type Service struct {
	pool *pgxpool.Pool
	q    *dbq.Queries
	hub  *realtime.Hub
}

// New builds the service. The hub may be nil, in which case nothing is
// published; the package's own tests run that way.
func New(pool *pgxpool.Pool, hub *realtime.Hub) *Service {
	return &Service{pool: pool, q: dbq.New(pool), hub: hub}
}

// publish emits a change event, if a hub is attached.
//
// minRole and humanOnly are passed explicitly rather than inferred from
// kind, exactly as internal/web's own publish does, so each call site
// shows the gating it chose instead of inheriting one from a table three
// files away. The values themselves are named constants declared beside
// the kind they belong to, in events.go, which is where the reasoning
// for each lives; a helper that could not express these fields at all —
// the shape this package shipped with — silently made every event as
// open as the hub's zero value, whether or not that was the right answer.
//
// A payload carries only the identity of what changed — a key, an id —
// and never a value a client could then treat as current. Publication
// order is not commit order (internal/web/publish.go's package comment
// works through why), so a payload holding, say, a type's new label could
// stably tell a client the wrong label with nothing to signal it. The
// client re-reads instead.
//
// **Every caller must call this after withTx has returned, never from
// inside fn.** An event published inside the transaction announces a
// change that may still roll back, and a subscriber that re-reads on
// hearing it — which is the only thing this hub's payloads let it do —
// would read the state before the change and cache it as the state
// after. The hub itself cannot enforce that; the metamodel's own tests
// pin it, including the last placement — a publish as the final
// statement inside fn, which differs from the correct one only by the
// commit that follows: TestNoEventIsPublishedWhenTheCommitFails installs
// a deferred constraint in the test's own throwaway database so the
// commit, and only the commit, fails. The other two are
// TestNoEventIsPublishedWhenTheWriteIsRolledBack and
// TestNothingIsAnnouncedWhileTheTransactionIsStillOpen.
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

// withTx runs fn inside a transaction, rolling back unless it returns nil.
//
// Every mutation in this package needs one: a write and the re-validation
// sweep that follows it are one change, and half of it landing would leave
// a type declaring a schema its own entities were never re-checked against.
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

// decodeFields turns a stored jsonb blob back into a value map.
func decodeFields(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode fields: %w", err)
	}
	return out, nil
}

// ActorConstraintViolation recognises a write refused because its Actor
// does not belong to the project being written to, and returns
// ErrActorNotInGame; every other error passes through unchanged.
//
// **Exported because internal/views needs the same judgement.**
// 0008_views.sql gives views the same two audit columns under the same
// two composite foreign keys as every table in 0004_metamodel.sql, so a
// saved view written with a foreign token is refused by the database in
// exactly the same way, and a second copy of this scan in that package
// would be a second place for the constraint-name list to fall out of
// step. The sentinel it returns is aliased there too.
//
// It matches on the constraint's column rather than its full generated
// name (entity_types_updated_by_token_id_project_id_fkey today) so that
// Tasks 4, 5 and 6 get the same mapping for entities, relation types and
// relations without four near-identical name lists to keep in step: every
// table in 0004_metamodel.sql carries the same two audit columns under
// the same two constraint shapes. A missing updated_by_user_id is folded
// in with it — a user id that resolves to no row is the same class of
// fault, an actor this instance cannot vouch for, arriving from the same
// place.
//
// **created_by_* is in the list too, and was not for two sub-projects.**
// The scan named the two `updated_by_` columns only, which was complete
// for 0004_metamodel.sql and stopped being complete at
// 0007_documents.sql; 0008_views.sql's view_assets is a table whose only
// audit columns are `created_by_`, because an asset's bytes never change
// and there is no update path to record. Its upload with a foreign token
// therefore came back as a raw SQLSTATE 23503 over a generated
// constraint name — the same defect this function exists to remove, one
// column prefix along. Both prefixes are named in actorColumns, in one
// place, so a third table cannot fall out of step by being written
// somewhere else.
// TestTheUploaderIsRecordedAndAForeignTokenIsRefused (internal/views) is
// what caught it.
//
// **author_* is in the list too, and completing the list one prefix at a
// time is itself the pattern this comment keeps recording.** Extending a
// shared helper rather than copying it is the right move and was still
// one column short: 0007_documents.sql's document_versions names its
// actor `author_user_id` / `author_token_id`, because a version is
// authored once and never edited, and it carries byte-identical
// FOREIGN KEY (author_token_id, project_id) REFERENCES api_tokens
// (id, project_id) — so the same foreign-token write was reaching an
// agent as internal_error there while it was mapped everywhere else.
// The sweep that found it is over the migrations rather than over Go: a
// prefix is added here when a migration declares a composite key into
// api_tokens under a new name, and there are now three such names.
//
// **The mapping is only worth anything where it is called**, and
// internal/markdown had no caller at all. The defence for leaving it —
// the actor on a write is resolved by the transport from the credential
// the call arrived with, is never caller-supplied, and so is
// internal_error's to report — is the argument errors.go makes for
// ErrActorNotInGame not being a wire code, and it is why the sentinel
// stays off the wire. It is not an argument for the *log* line being a
// generated constraint name instead of a sentence. documents.go's three
// write paths call it now, for the reason internal/views' two do.
// actorColumns are the audit columns whose composite foreign key means
// "this credential belongs to another game".
var actorColumns = []string{
	"updated_by_token_id", "updated_by_user_id",
	"created_by_token_id", "created_by_user_id",
	"author_token_id", "author_user_id",
}

func ActorConstraintViolation(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		return err
	}
	for _, column := range actorColumns {
		if strings.Contains(pgErr.ConstraintName, column) {
			return ErrActorNotInGame
		}
	}
	return err
}

// searchLimitExceeded recognises a write refused because a value was too
// large for something the database had to build from it, and reports it
// as the caller's own input rather than as a server fault; every other
// error passes through unchanged.
//
// The only path that reaches it today is the search vector, and
// searchTextLimit is what keeps that path from being reached at all: the
// text handed to to_tsvector is bounded well below the 1,048,575-byte
// cap, so no field a designer can write gets here. This is the backstop
// for the case where something else does — a future write path that
// builds an index from a value without going through searchTextOf, or a
// Postgres limit on a column nobody has hit yet. 54000 is
// program_limit_exceeded, and every instance of it is the same shape of
// fault: something the caller sent is too big, and shortening it is a
// fix the caller can make. Left unmapped it lands on failureFor's
// default arm as internal_error, which tells an agent to give up on a
// call it could have fixed.
//
// The database's own message is carried through rather than paraphrased:
// it names the limit and the size that broke it, which is the only part
// of this a caller can act on numerically.
func searchLimitExceeded(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "54000" {
		return err
	}
	return fmt.Errorf("%w: a value of this row is too large to index (%s): %w",
		ErrInvalidInput, pgErr.Message, err)
}

// notFound maps pgx's no-rows sentinel onto the domain's, leaving every
// other error wrapped with what was being looked up.
func notFound(err error, what string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("%s: %w", what, err)
}

// missingByID is what a removal says when the id it was given names no
// row of this game — review finding L4.
//
// The four removals used to answer with a bare ErrNotFound, whose
// message is the sentinel's own name, so an agent read
// `{"error":"not_found","message":"not_found"}`: the code repeated as
// prose, with nothing to distinguish "this id was never a thing" from
// "somebody removed it a moment ago", and nothing to check the id
// against. Every by-key accessor in this package has named its miss
// since Task 4 (`no entity type "quest" in this game`); this is the same
// rule applied to an address that is an id rather than a key.
//
// It names the id back because that is the only part of the call a
// caller can compare against what it holds: an agent that removed the
// row already and re-sent, and an agent that pasted the wrong id, need
// to tell those apart, and the id in the message is what does it.
func missingByID(what string, id uuid.UUID) error {
	return fmt.Errorf("%w: this game has no %s with id %s", ErrNotFound, what, id)
}

// notFoundByID is notFound for a lookup addressed by a caller-supplied
// id: no-rows becomes a named ErrNotFound rather than the bare
// sentinel, and anything else keeps its context.
func notFoundByID(err error, what string, id uuid.UUID, doing string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return missingByID(what, id)
	}
	return fmt.Errorf("%s: %w", doing, err)
}

// stillInUse is what a removal without cascade says when rows still
// point at the type — the other half of finding L4.
//
// `{"error":"in_use","message":"in_use"}` told an agent nothing it did
// not already have from the code. What it needs is which type, how much
// content stands behind the refusal, and that `cascade` is the way
// through — the last of which is the actual recovery and was nowhere in
// the answer.
//
// holders is spelled by the caller rather than counted here because the
// two removals count different things and one of them cannot count at
// all: the race arm catches a row written between the count and the
// delete, where the number is unknown and unstable by definition.
func stillInUse(what, key, holders string) error {
	return fmt.Errorf("%w: the %s %q still has %s; remove them first, "+
		"or pass cascade to remove them along with it", ErrInUse, what, key, holders)
}

// retryableSQLStates are the SQLSTATEs IsRetryable admits.
//
// They are listed rather than matched by class prefix. Class 40 also
// holds 40000 (transaction_rollback) and 40003 (statement_completion_
// unknown), and class 57 holds 57P01-57P05, every one of which is the
// server going away rather than two transactions meeting; telling an
// agent to resend into a shutting-down instance is not a recovery. A
// list is also readable next to the reason each entry earns its place:
//
//   - 40001 serialization_failure — two transactions could not be
//     ordered; one is asked to go again.
//   - 40P01 deadlock_detected — Postgres broke a cycle by cancelling
//     this transaction, and the survivor is about to release what this
//     one wanted.
//   - 55P03 lock_not_available — lock_timeout fired while waiting for a
//     row another transaction held. checkEndpointTypes (relation_types.go)
//     is the path that reaches this one, and its doc comment is where
//     this decision was left open.
//   - 57014 query_canceled — statement_timeout fired, or someone
//     cancelled the query.
//
// **57014 is the one entry that is not contention by construction, and
// it stays — review finding L3.** The first three can only arise from
// two transactions meeting; `query_canceled` also covers a query an
// operator's `statement_timeout` cut off for being too expensive, and
// that one fails identically every time it is run, so "change nothing
// and resend" is wrong advice for it. Dropping it was the alternative
// and it is worse: a lock wait that runs into `statement_timeout`
// rather than into `lock_timeout` is exactly the contention this code
// exists for, and it would report as `internal_error` — a caller told
// the server broke when the recovery was to wait a moment. So the code
// is kept and the *advice beside it* was completed instead: every tool
// description and `mcpErrorFor`'s own message now say what to do when
// resending stops helping, which is to ask for less.
var retryableSQLStates = map[string]bool{
	"40001": true,
	"40P01": true,
	"55P03": true,
	"57014": true,
}

// IsRetryable reports whether err is a database failure that the
// identical call, resent unchanged, may survive.
//
// **This is Task 7's third decision, and the wire code it feeds is
// `retryable`.** Until it existed, a lock timeout and a deadlock landed
// on internal_error — the code reserved for what nobody planned for, and
// the code a seeding agent reads as "stop": the recovery it teaches is
// to report the call broken, when the correct recovery was to send the
// same bytes again a moment later. Every other code in this package's
// vocabulary describes something the caller must *change* before
// resending (a value, a key, a version, a name that does not exist);
// this is the only one that says change nothing.
//
// **It is a predicate and not a sentinel, deliberately.** The other
// mappings in this file (actorConstraintViolation, searchLimitExceeded)
// convert a *pgconn.PgError into a domain error at the one call site
// that can produce it. Contention is not like that: it can surface from
// any statement in this package, so a sentinel would have to be wrapped
// in at dozens of call sites and would be silently missing from
// whichever one a later change forgets. A predicate over the SQLSTATE
// the error already carries is checked where the answer is needed — the
// two error-mapping boundaries, failureFor (bulk.go) and mcpErrorFor
// (internal/web/mcp_errors.go) — and cannot be forgotten by a write path
// that never mentions it. errors.As does the unwrapping, so it holds
// through however many layers of fmt.Errorf a path adds.
//
// It says nothing about whether retrying is *wise*: an agent that meets
// this repeatedly is contending with something, and the answer to that
// is a smaller batch or a pause, not a tighter loop. That advice belongs
// in the tool description, which is where a caller reads it.
func IsRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return retryableSQLStates[pgErr.Code]
}
