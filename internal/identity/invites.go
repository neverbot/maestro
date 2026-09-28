package identity

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/roles"
)

// ErrInviteInvalid covers unknown, already redeemed and mismatched
// invites: the caller learns nothing about which. Expired is deliberately
// not one of them — see ErrInviteExpired below.
var ErrInviteInvalid = errors.New("invite is not valid")

// ErrInviteExpired means the token hashed to a real invite row, but that
// row's expires_at has passed. It is split out from ErrInviteInvalid on
// purpose: reaching this branch already requires holding the actual clear
// token (see resolveInviteMiss's doc comment for why that means telling
// the holder is safe), and a designer who clicks a two-week-old link
// deserves "this expired, ask your admin for a new one" rather than a
// generic failure that reads like a bug and files a support request.
var ErrInviteExpired = errors.New("invite has expired")

// ErrInviteRequestInvalid means the invite an admin asked to create is
// malformed: an unrecognised role, a role given without a project (or a
// project given without a role), an email outside this instance's
// allowed domains, or an ExpiresIn outside the configured bounds. This is
// a distinct error from ErrInviteInvalid on purpose — that one is about
// redemption, where an attacker must learn nothing; this one is about
// creation, where the caller is a trusted admin who benefits from a clear
// message instead of a raw Postgres CHECK-constraint violation or a
// silently-unusable link mailed to someone who can't fix it themselves.
var ErrInviteRequestInvalid = errors.New("invite request is invalid")

// InviteRequest describes an invite to mint. Email is optional: an invite
// with no email binds to whoever redeems it first, but when given it must
// pass the same length bound and ALLOWED_EMAIL_DOMAINS check CreateUser
// itself applies — an invite for a domain this instance would refuse is a
// dead link the recipient has no power to fix. ProjectID and Role travel
// together — an invite with no project only grants an account, and an
// invite with a project always grants membership at a role from
// roles.All() — matching the database's own
// `CHECK ((project_id IS NULL) = (role IS NULL))`. ExpiresIn is optional:
// zero means Config.InviteTTL (the instance default), and any nonzero
// value is bounded above by config.MaxInviteTTL.
type InviteRequest struct {
	Email     string
	ProjectID *uuid.UUID
	Role      string
	CreatedBy *uuid.UUID
	ExpiresIn time.Duration
}

// InviteSummary is the identity domain's public view of an invite row. It
// deliberately omits dbq.Invite's TokenHash, for the same reason User
// omits PasswordHash: nothing outside this package needs it, and nothing
// should be one careless writeJSON away from serving a value an attacker
// could brute-force offline against. It carries only what an admin needs
// to recognise and act on an outstanding invite (ListOutstandingInvites,
// RevokeInvite) or to show a confirmation after minting one (CreateInvite).
type InviteSummary struct {
	ID        uuid.UUID
	Email     *string
	ProjectID *uuid.UUID
	Role      *string
	CreatedBy *uuid.UUID
	CreatedAt time.Time
	ExpiresAt time.Time

	// Revoked is "this invite's expiry has passed", **decided by the
	// database and not recomputed here or above this package.**
	Revoked bool
}

func inviteSummaryFrom(inv dbq.Invite) InviteSummary {
	return InviteSummary{
		ID:        inv.ID,
		Email:     inv.Email,
		ProjectID: inv.ProjectID,
		Role:      inv.Role,
		CreatedBy: inv.CreatedBy,
		CreatedAt: inv.CreatedAt.Time,
		ExpiresAt: inv.ExpiresAt.Time,
	}
}

// listedInvite is one row of either outstanding-invite listing. Both
// queries select the same columns plus the same computed `revoked`, and
// this is the one shape both are read through, so the two listings
// cannot drift about what an invite carries.
type listedInvite struct {
	ID         uuid.UUID
	TokenHash  []byte
	Email      *string
	ProjectID  *uuid.UUID
	Role       *string
	CreatedBy  *uuid.UUID
	CreatedAt  pgtype.Timestamptz
	ExpiresAt  pgtype.Timestamptz
	RedeemedAt pgtype.Timestamptz
	RedeemedBy *uuid.UUID
	Revoked    bool
}

func listedInviteSummary(row listedInvite) InviteSummary {
	return InviteSummary{
		ID:        row.ID,
		Email:     row.Email,
		ProjectID: row.ProjectID,
		Role:      row.Role,
		CreatedBy: row.CreatedBy,
		CreatedAt: row.CreatedAt.Time,
		ExpiresAt: row.ExpiresAt.Time,
		Revoked:   row.Revoked,
	}
}

// CreateInvite mints an invite and returns the clear token, which is shown
// to the human once and never stored, alongside the row's own id and
// expiry: the caller (eventually, an admin-facing endpoint) needs the id
// to revoke the invite later and the expiry to show "expires 14 Sep"
// without recomputing time.Now().Add(ttl) itself — the same argument
// IssueSession's doc comment makes about not letting a caller recompute
// policy that could drift from what was actually written.
func (s *Service) CreateInvite(ctx context.Context, req InviteRequest) (string, InviteSummary, error) {
	if (req.ProjectID == nil) != (req.Role == "") {
		return "", InviteSummary{}, fmt.Errorf("%w: project and role must be given together", ErrInviteRequestInvalid)
	}
	if req.Role != "" && !roles.Valid(req.Role) {
		return "", InviteSummary{}, fmt.Errorf("%w: role %q is not recognised", ErrInviteRequestInvalid, req.Role)
	}

	ttl := s.cfg.InviteTTL
	if req.ExpiresIn != 0 {
		if req.ExpiresIn < 0 || req.ExpiresIn > config.MaxInviteTTL {
			return "", InviteSummary{}, fmt.Errorf("%w: expires_in must be positive and at most %s", ErrInviteRequestInvalid, config.MaxInviteTTL)
		}
		ttl = req.ExpiresIn
	}

	token, err := randomToken()
	if err != nil {
		return "", InviteSummary{}, err
	}
	sum := sha256.Sum256([]byte(token))

	params := dbq.CreateInviteParams{
		TokenHash: sum[:],
		ProjectID: req.ProjectID,
		CreatedBy: req.CreatedBy,
		// The TTL travels as an interval; the database turns it into a
		// timestamp, because the database is what judges it (GetLiveInvite
		// and MarkInviteRedeemed both compare against its own now()).
		Ttl: pgtype.Interval{Microseconds: ttl.Microseconds(), Valid: true},
	}
	if email := strings.ToLower(strings.TrimSpace(req.Email)); email != "" {
		// Same bound CreateUser applies to a real account's email, and for
		// the same reason (Task 5's Correction 8): an unbounded text
		// column fed straight from a request. EmailAllowed catches the
		// case a length bound cannot: a syntactically fine address on a
		// domain this instance would refuse at registration time anyway,
		// which would otherwise only surface as a silently-dead link once
		// the recipient tried to redeem it.
		if n := utf8.RuneCountInString(email); n < minEmailRunes || n > maxEmailRunes {
			return "", InviteSummary{}, fmt.Errorf("%w: email must be between %d and %d characters", ErrInviteRequestInvalid, minEmailRunes, maxEmailRunes)
		}
		if !s.cfg.EmailAllowed(email) {
			return "", InviteSummary{}, fmt.Errorf("%w: email domain not allowed on this instance", ErrInviteRequestInvalid)
		}
		params.Email = &email
	}
	if req.Role != "" {
		role := req.Role
		params.Role = &role
	}

	inv, err := s.q.CreateInvite(ctx, params)
	if err != nil {
		if merr := mapInviteInsertError(err); merr != nil {
			return "", InviteSummary{}, merr
		}
		return "", InviteSummary{}, fmt.Errorf("create invite: %w", err)
	}
	return token, inviteSummaryFrom(inv), nil
}

// mapInviteInsertError narrows a foreign-key violation from
// CreateInvite's insert to ErrInviteRequestInvalid, exactly the way
// mapAPITokenInsertError (tokens.go) and projects.mapMembershipInsertError
// already narrow their own: by constraint name, not by SQLSTATE 23503
// alone, so a future unrelated foreign key on this table cannot be
// misreported as "bad request". Returns nil when err is neither, so the
// caller falls through to its own generic wrap.
func mapInviteInsertError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		return nil
	}
	switch pgErr.ConstraintName {
	case "invites_project_id_fkey":
		return fmt.Errorf("%w: that game no longer exists", ErrInviteRequestInvalid)
	case "invites_created_by_fkey":
		return fmt.Errorf("%w: the account creating this invite no longer exists", ErrInviteRequestInvalid)
	default:
		return nil
	}
}

// RedeemInvite consumes an invite and creates the account it grants. When
// the invite names a project, the new user is added to it with the
// invite's role. Creating the user, marking the invite redeemed and
// granting the membership run in one transaction (via withTx, from Task
// 5): a failure partway through must not leave a replayable invite or a
// user with no membership.
func (s *Service) RedeemInvite(ctx context.Context, token string, req CreateUserRequest) (RedeemInviteResult, error) {
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	invite, err := s.lookupLiveInviteForRedemption(ctx, token)
	if err != nil {
		return RedeemInviteResult{}, err
	}
	// invite.Email, like every other email this package stores, was
	// lower-cased and trimmed by CreateInvite before it was written;
	// req.Email above is normalised the same way, so this is a
	// like-for-like comparison rather than one that happens to work only
	// for already-lowercase input.
	if invite.Email != nil && *invite.Email != req.Email {
		return RedeemInviteResult{}, ErrInviteInvalid
	}

	// Validate and hash before opening the transaction: hashing is
	// argon2, CPU-bound and deliberately slow, and has no interaction
	// with the transaction's atomicity — only the insert that follows
	// does. Running it inside the transaction would pin a pool
	// connection for the full derivation and lengthen how long the
	// invite row's lock (see this method's own doc comment above) is
	// held, for every client racing to redeem a stale or shared link.
	prepareFn := s.prepareUser
	if invite.Email != nil {
		prepareFn = s.prepareUserForInvite
	}
	prepared, err := prepareFn(req)
	if err != nil {
		return RedeemInviteResult{}, err
	}

	var user User
	err = s.withTx(ctx, func(q *dbq.Queries) error {
		var cerr error
		user, cerr = s.insertUser(ctx, q, prepared, false)
		if cerr != nil {
			if errors.Is(cerr, ErrEmailTaken) {
				// See the email-probing paragraph in this method's doc
				// comment: the caller must not learn that req.Email
				// belongs to an existing account.
				return ErrInviteInvalid
			}
			return cerr
		}

		n, merr := q.MarkInviteRedeemed(ctx, dbq.MarkInviteRedeemedParams{
			ID:         invite.ID,
			RedeemedBy: user.ID,
		})
		if merr != nil {
			return fmt.Errorf("mark invite redeemed: %w", merr)
		}
		if n == 0 {
			// Lost the race to another redeemer, expired between the
			// lookup above and here, or the invite's project was deleted
			// out from under it (see the method doc comment). Either way
			// this redemption must fail exactly as an unknown token would.
			return ErrInviteInvalid
		}

		if invite.ProjectID != nil {
			role := *invite.Role // CreateInvite's own check guarantees Role is set whenever ProjectID is.
			if err := q.UpsertMembership(ctx, dbq.UpsertMembershipParams{
				UserID:    user.ID,
				ProjectID: *invite.ProjectID,
				Role:      role,
			}); err != nil {
				return fmt.Errorf("grant membership: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return RedeemInviteResult{}, err
	}
	return RedeemInviteResult{User: user, InviteID: invite.ID, ProjectID: invite.ProjectID}, nil
}

// RedeemInviteResult is what RedeemInvite and RedeemInviteForExistingUser
// both report on success: the account involved — created, for
// RedeemInvite's anonymous path; reused, for RedeemInviteForExistingUser's
// already-logged-in one — the id of the invite row consumed, and, when
// the invite was project-bound, that project's id. ProjectID is nil for
// an account-only invite — the same nil-means-unbound convention
// InviteRequest.ProjectID and InviteSummary.ProjectID already use —
// which is exactly what handleRegister (api_auth.go) checks before
// publishing anything project-scoped: no project id, nothing to publish
// beyond what each path does on its own (starting a new session, for
// RedeemInvite; nothing further, for RedeemInviteForExistingUser, whose
// caller already has one).
type RedeemInviteResult struct {
	User // embedded: every existing call site that only ever read the
	// created account (user.ID, user.DisplayName, ...) keeps compiling
	// unchanged against this wider result, promoted through embedding
	// rather than a named User field forcing every one of them to add
	// ".User".
	InviteID  uuid.UUID
	ProjectID *uuid.UUID
}

// lookupLiveInviteForRedemption hashes token and resolves it to a live
// invite row, or the appropriate error — ErrInviteInvalid or
// ErrInviteExpired via resolveInviteMiss for a miss, a wrapped error for
// a genuine lookup failure. Shared by RedeemInvite and
// RedeemInviteForExistingUser, which differ only in what they do once
// they have a live invite in hand (create-and-grant vs. grant-only), not
// in how they find one.
func (s *Service) lookupLiveInviteForRedemption(ctx context.Context, token string) (dbq.Invite, error) {
	sum := sha256.Sum256([]byte(token))
	invite, err := s.q.GetLiveInvite(ctx, sum[:])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return dbq.Invite{}, s.resolveInviteMiss(ctx, sum[:])
		}
		return dbq.Invite{}, fmt.Errorf("lookup invite: %w", err)
	}
	return invite, nil
}

// RedeemInviteForExistingUser grants existingUserID membership via a
// project-bound invite, without creating a new account. This is the path
// RedeemInvite itself could never take: that method's insertUser call
// always attempts to create a new user, so a request naming an email
// that already has an account fails with ErrEmailTaken, mapped inside
// RedeemInvite to the generic ErrInviteInvalid — the same response a
// stale or unknown token gets. Before this method existed, that meant a
// designer with an existing account could never be granted a second
// game through the invite surface at all: clicking a project invite
// while already signed in was indistinguishable, from this package's own
// behaviour, from clicking a dead link. handleRegister (api_auth.go)
// calls this instead of RedeemInvite specifically when the request
// arrives from a live, human, session-authenticated caller — see that
// handler's own doc comment for the routing decision.
func (s *Service) RedeemInviteForExistingUser(ctx context.Context, token string, existingUserID uuid.UUID) (RedeemInviteResult, error) {
	invite, err := s.lookupLiveInviteForRedemption(ctx, token)
	if err != nil {
		return RedeemInviteResult{}, err
	}
	if invite.ProjectID == nil {
		return RedeemInviteResult{}, ErrInviteInvalid
	}

	existingUser, err := s.UserByID(ctx, existingUserID)
	if err != nil {
		return RedeemInviteResult{}, fmt.Errorf("lookup existing user: %w", err)
	}
	if invite.Email != nil && !strings.EqualFold(*invite.Email, existingUser.Email) {
		return RedeemInviteResult{}, ErrInviteInvalid
	}

	err = s.withTx(ctx, func(q *dbq.Queries) error {
		n, merr := q.MarkInviteRedeemed(ctx, dbq.MarkInviteRedeemedParams{
			ID:         invite.ID,
			RedeemedBy: existingUserID,
		})
		if merr != nil {
			return fmt.Errorf("mark invite redeemed: %w", merr)
		}
		if n == 0 {
			// Lost the race to another redeemer, expired since the
			// lookup above, or its project was deleted out from under
			// it — see RedeemInvite's own doc comment for the identical
			// mechanism and why every one of those collapses to the
			// same ErrInviteInvalid.
			return ErrInviteInvalid
		}

		role := *invite.Role // CreateInvite's own check guarantees Role is set whenever ProjectID is.
		if err := q.UpsertMembership(ctx, dbq.UpsertMembershipParams{
			UserID:    existingUserID,
			ProjectID: *invite.ProjectID,
			Role:      role,
		}); err != nil {
			return fmt.Errorf("grant membership: %w", err)
		}
		return nil
	})
	if err != nil {
		return RedeemInviteResult{}, err
	}
	return RedeemInviteResult{User: existingUser, InviteID: invite.ID, ProjectID: invite.ProjectID}, nil
}

// resolveInviteMiss is called once GetLiveInvite has found no row for a
// given hash — meaning the token is unknown, its invite has already been
// redeemed, or it has expired. It distinguishes only the expired case
// (ErrInviteExpired), via a second lookup keyed on the hash alone.
func (s *Service) resolveInviteMiss(ctx context.Context, tokenHash []byte) error {
	stale, err := s.q.GetInviteByTokenHash(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInviteInvalid
		}
		return fmt.Errorf("lookup stale invite: %w", err)
	}
	if stale.RedeemedAt.Valid {
		return ErrInviteInvalid
	}
	return fmt.Errorf("%w (expired %s)", ErrInviteExpired, stale.ExpiresAt.Time.Format("2006-01-02"))
}

// ListOutstandingInvites returns every account-only invite (no game
// attached) not yet redeemed, newest first, for the instance-wide admin
// surface (Task 18's POST/GET/DELETE /api/invites, gated on
// Caller.IsAdmin) to review and revoke by (see RevokeInvite). See the
// query's own doc comment for why "outstanding" includes invites that
// have since expired, and for why this is scoped to project_id IS NULL —
// a project-bound invite is this method's project_id IS NULL clause away
// from leaking another game's pending roster to an admin with no
// membership in it. ListOutstandingInvitesForProject is the project-bound
// counterpart.
func (s *Service) ListOutstandingInvites(ctx context.Context) ([]InviteSummary, error) {
	rows, err := s.q.ListOutstandingInvites(ctx)
	if err != nil {
		return nil, fmt.Errorf("list outstanding invites: %w", err)
	}
	out := make([]InviteSummary, len(rows))
	for i, row := range rows {
		out[i] = listedInviteSummary(listedInvite(row))
	}
	return out, nil
}

// ListOutstandingInvitesForProject is ListOutstandingInvites' project-
// scoped counterpart, added in Task 18 for GET /api/games/{game}/invites:
// every invite naming projectID that is not yet redeemed, newest first.
func (s *Service) ListOutstandingInvitesForProject(ctx context.Context, projectID uuid.UUID) ([]InviteSummary, error) {
	rows, err := s.q.ListOutstandingProjectInvites(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list outstanding project invites: %w", err)
	}
	out := make([]InviteSummary, len(rows))
	for i, row := range rows {
		out[i] = listedInviteSummary(listedInvite(row))
	}
	return out, nil
}

// CountInvitesForProject counts every invites row scoped to projectID,
// redeemed or not — deliberately wider than
// ListOutstandingInvitesForProject's redeemed_at IS NULL filter, since a
// game's cascade takes its already-redeemed (audit-trail) invite rows
// with it too. Added in Task 18's Round 2 corrections alongside
// CountAPITokensForProject (tokens.go), for the same reason: see that
// method's own doc comment.
func (s *Service) CountInvitesForProject(ctx context.Context, projectID uuid.UUID) (int64, error) {
	n, err := s.q.CountInvitesForProject(ctx, projectID)
	if err != nil {
		return 0, fmt.Errorf("count invites for project: %w", err)
	}
	return n, nil
}

// RevokeInvite makes one account-only invite (no game attached)
// permanently unredeemable, by expiring it immediately, so an invite
// pasted into the wrong channel — a live bearer credential for as long as
// it has left to live — can actually be taken back rather than left to
// run out the clock. Revoking an unknown, already-redeemed,
// already-expired or project-bound id is not an error: the caller's goal
// (no live account-only invite under this id) is already satisfied
// either way, the same convention RevokeSession already established in
// sessions.go — and see the query's own doc comment for why a
// project-bound id must fall into that same silent-no-op bucket rather
// than being revoked here. RevokeProjectInvite is the project-bound
// counterpart.
func (s *Service) RevokeInvite(ctx context.Context, id uuid.UUID) error {
	if err := s.q.RevokeInvite(ctx, id); err != nil {
		return fmt.Errorf("revoke invite: %w", err)
	}
	return nil
}

// RevokeProjectInvite is RevokeInvite's project-scoped counterpart, added
// in Task 18 for DELETE /api/games/{game}/invites/{invite}. Revoking an
// id that does not exist, or belongs to a different game, is the same
// silent no-op RevokeAPIToken's own doc comment establishes for tokens,
// and for the same reason: telling the two apart would let a caller with
// standing in one game probe whether some other id belongs to a
// different one.
func (s *Service) RevokeProjectInvite(ctx context.Context, projectID, id uuid.UUID) error {
	if err := s.q.RevokeProjectInvite(ctx, dbq.RevokeProjectInviteParams{ID: id, ProjectID: projectID}); err != nil {
		return fmt.Errorf("revoke project invite: %w", err)
	}
	return nil
}

// PruneExpiredInvites deletes every unredeemed invite past its expires_at
// and reports how many rows it removed. Called by cmd/maestro's
// startPruneLoop, once at start-up and then once every pruneInterval for
// the life of the process, the same way and for the same reason as its
// sibling PruneExpiredSessions (sessions.go) — see that method's own doc
// comment. invites_expires_idx exists for exactly this query. An
// unpruned expired invite is dead weight, not a live security exposure:
// RedeemInvite already treats it as invalid via GetLiveInvite's own
// expires_at > now() filter regardless of whether this has run recently.
func (s *Service) PruneExpiredInvites(ctx context.Context) (int64, error) {
	n, err := s.q.DeleteExpiredInvites(ctx)
	if err != nil {
		return 0, fmt.Errorf("prune expired invites: %w", err)
	}
	return n, nil
}
