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
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/db/dbq"
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

// inviteRoles are the roles an invite may grant. This must stay identical
// to the membership CHECK constraint in migration 0001
// (role IN ('owner', 'editor', 'viewer')): granting an invite a role the
// memberships table would refuse is exactly the kind of mismatch that
// should fail loudly here, in Go, rather than as a raw constraint
// violation from UpsertMembership deep inside RedeemInvite's transaction.
var inviteRoles = map[string]bool{
	"owner":  true,
	"editor": true,
	"viewer": true,
}

// InviteRequest describes an invite to mint. Email is optional: an invite
// with no email binds to whoever redeems it first, but when given it must
// pass the same length bound and ALLOWED_EMAIL_DOMAINS check CreateUser
// itself applies — an invite for a domain this instance would refuse is a
// dead link the recipient has no power to fix. ProjectID and Role travel
// together — an invite with no project only grants an account, and an
// invite with a project always grants membership at a role from
// inviteRoles — matching the database's own
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
	if req.Role != "" && !inviteRoles[req.Role] {
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
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(ttl), Valid: true},
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
		return "", InviteSummary{}, fmt.Errorf("create invite: %w", err)
	}
	return token, inviteSummaryFrom(inv), nil
}

// RedeemInvite consumes an invite and creates the account it grants. When
// the invite names a project, the new user is added to it with the
// invite's role. Creating the user, marking the invite redeemed and
// granting the membership run in one transaction (via withTx, from Task
// 5): a failure partway through must not leave a replayable invite or a
// user with no membership.
//
// req is the same CreateUserRequest CreateUser takes, not four adjacent
// strings: a public, unauthenticated handler decoding this straight out of
// a JSON body is exactly the call site Task 5's Correction 11 wrote
// CreateUserRequest to protect — a transposed DisplayName/Password would
// otherwise compile, pass validation (both are plausible-length strings),
// and write a plaintext password into users.display_name where every
// other member can read it.
//
// Redemption is safe under concurrent use of the same token. The lookup
// below (GetLiveInvite) runs before the transaction purely to reject an
// unknown token cheaply and to compare the bound email without paying for
// a transaction; it does not by itself prevent two concurrent redemptions
// of the same live token from both reaching this point. What prevents a
// double redemption is MarkInviteRedeemed inside the transaction: it is a
// conditional UPDATE (`WHERE ... AND redeemed_at IS NULL AND expires_at >
// now()`) whose row lock serializes concurrent redeemers of the same
// invite — the first to commit wins, and the second, once unblocked, finds
// the row no longer matches its WHERE clause and updates zero rows. This
// method treats "zero rows updated" as ErrInviteInvalid, which rolls back
// the whole transaction, so the loser's user insert and membership grant
// are undone along with it rather than left as an orphaned account with no
// membership.
//
// The same mechanism also covers a project deleted out from under a live
// invite: invites.project_id is ON DELETE CASCADE, so deleting a project
// deletes every invite that named it in the same transaction as the
// delete. If that commits between the lookup below and this method's own
// transaction, the invite row is simply gone by the time
// MarkInviteRedeemed runs, its UPDATE affects zero rows, and redemption
// fails with ErrInviteInvalid instead of granting membership in a project
// that no longer exists.
//
// A pre-existing account for req.Email is also mapped to ErrInviteInvalid
// rather than left as ErrEmailTaken. Without that, an unbound invite (no
// email attached) would let anyone holding it probe an unlimited number of
// addresses for an existing account: redeem, read the error, learn whether
// that address is taken, and try again with the invite still unconsumed
// (a failed insert never reaches MarkInviteRedeemed). Bounding the number
// of attempts against one client is the HTTP layer's job (Task 11's
// invite-redemption rate limiter); this method's job is only to make sure
// the response itself carries no signal either way.
func (s *Service) RedeemInvite(ctx context.Context, token string, req CreateUserRequest) (User, error) {
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	sum := sha256.Sum256([]byte(token))

	invite, err := s.q.GetLiveInvite(ctx, sum[:])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, s.resolveInviteMiss(ctx, sum[:])
		}
		return User{}, fmt.Errorf("lookup invite: %w", err)
	}
	// invite.Email, like every other email this package stores, was
	// lower-cased and trimmed by CreateInvite before it was written;
	// req.Email above is normalised the same way, so this is a
	// like-for-like comparison rather than one that happens to work only
	// for already-lowercase input.
	if invite.Email != nil && *invite.Email != req.Email {
		return User{}, ErrInviteInvalid
	}

	// Validate and hash before opening the transaction: hashing is
	// argon2, CPU-bound and deliberately slow, and has no interaction
	// with the transaction's atomicity — only the insert that follows
	// does. Running it inside the transaction would pin a pool
	// connection for the full derivation and lengthen how long the
	// invite row's lock (see this method's own doc comment above) is
	// held, for every client racing to redeem a stale or shared link.
	prepared, err := s.prepareUser(req)
	if err != nil {
		return User{}, err
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
		return User{}, err
	}
	return user, nil
}

// resolveInviteMiss is called once GetLiveInvite has found no row for a
// given hash — meaning the token is unknown, its invite has already been
// redeemed, or it has expired. It distinguishes only the expired case
// (ErrInviteExpired), via a second lookup keyed on the hash alone.
//
// This is safe against an attacker with no token in hand: reaching this
// function at all already requires holding the clear token whose SHA-256
// equals tokenHash, and nobody can produce that without either holding the
// real token or having brute-forced 256 bits of entropy — the same
// property GetInviteByTokenHash's own doc comment relies on. Telling the
// holder "this expired" therefore leaks nothing an attacker without the
// token could use; it only ever reaches someone who could otherwise have
// learned the same thing by successfully redeeming a still-live version of
// the same link.
//
// An unknown token and an already-redeemed one both still return the
// generic ErrInviteInvalid: there is no user-facing action for either
// beyond "ask for a new invite", the same as expired, so nothing is
// gained by telling them apart — and collapsing "redeemed" into the
// generic error keeps this from becoming an oracle for whether some
// other, unrelated holder of the same link already used it.
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

// ListOutstandingInvites returns every invite not yet redeemed, newest
// first, for an admin surface to review and revoke by (see RevokeInvite).
// See the query's own doc comment for why "outstanding" includes invites
// that have since expired.
func (s *Service) ListOutstandingInvites(ctx context.Context) ([]InviteSummary, error) {
	rows, err := s.q.ListOutstandingInvites(ctx)
	if err != nil {
		return nil, fmt.Errorf("list outstanding invites: %w", err)
	}
	out := make([]InviteSummary, len(rows))
	for i, row := range rows {
		out[i] = inviteSummaryFrom(row)
	}
	return out, nil
}

// RevokeInvite makes one invite permanently unredeemable, by expiring it
// immediately, so an invite pasted into the wrong channel — a live bearer
// credential for as long as it has left to live — can actually be taken
// back rather than left to run out the clock. Revoking an unknown,
// already-redeemed or already-expired id is not an error: the caller's
// goal (no live invite under this id) is already satisfied, the same
// convention RevokeSession already established in sessions.go.
func (s *Service) RevokeInvite(ctx context.Context, id uuid.UUID) error {
	if err := s.q.RevokeInvite(ctx, id); err != nil {
		return fmt.Errorf("revoke invite: %w", err)
	}
	return nil
}

// PruneExpiredInvites deletes every unredeemed invite past its expires_at
// and reports how many rows it removed. As with PruneExpiredSessions in
// sessions.go, nothing in this task calls it: invites_expires_idx exists
// for exactly this query, but wiring a periodic sweep is a
// process-lifecycle concern that belongs with the rest of main's start-up
// wiring, not with the identity service itself. Leaving it unwired here is
// deliberate, not an oversight: an unpruned expired invite is dead weight
// (RedeemInvite already treats it as invalid via GetLiveInvite's own
// expires_at > now() filter), not a live security exposure. See the plan's
// Task 16 note: this and PruneExpiredSessions both still want an owner.
func (s *Service) PruneExpiredInvites(ctx context.Context) (int64, error) {
	n, err := s.q.DeleteExpiredInvites(ctx)
	if err != nil {
		return 0, fmt.Errorf("prune expired invites: %w", err)
	}
	return n, nil
}
