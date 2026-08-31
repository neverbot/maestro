package identity

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// ErrInviteInvalid covers unknown, expired, already redeemed and mismatched
// invites: the caller learns nothing about which.
var ErrInviteInvalid = errors.New("invite is not valid")

// ErrInviteRequestInvalid means the invite an admin asked to create is
// malformed: an unrecognised role, or a role given without a project (or a
// project given without a role). This is a distinct error from
// ErrInviteInvalid on purpose — that one is about redemption, where an
// attacker must learn nothing; this one is about creation, where the
// caller is a trusted admin who benefits from a clear message instead of a
// raw Postgres CHECK-constraint violation.
var ErrInviteRequestInvalid = errors.New("invite request is invalid")

// InviteTTL is how long an invite link stays usable.
const InviteTTL = 14 * 24 * time.Hour

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
// with no email binds to whoever redeems it first. ProjectID and Role
// travel together — an invite with no project only grants an account, and
// an invite with a project always grants membership at a role from
// inviteRoles — matching the database's own
// `CHECK ((project_id IS NULL) = (role IS NULL))`.
type InviteRequest struct {
	Email     string
	ProjectID *uuid.UUID
	Role      string
	CreatedBy *uuid.UUID
}

// CreateInvite mints an invite and returns the clear token, which is shown to
// the human once and never stored.
func (s *Service) CreateInvite(ctx context.Context, req InviteRequest) (string, error) {
	if (req.ProjectID == nil) != (req.Role == "") {
		return "", fmt.Errorf("%w: project and role must be given together", ErrInviteRequestInvalid)
	}
	if req.Role != "" && !inviteRoles[req.Role] {
		return "", fmt.Errorf("%w: role %q is not recognised", ErrInviteRequestInvalid, req.Role)
	}

	token, err := randomToken()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(token))

	params := dbq.CreateInviteParams{
		TokenHash: sum[:],
		ProjectID: req.ProjectID,
		CreatedBy: req.CreatedBy,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(InviteTTL), Valid: true},
	}
	if email := strings.ToLower(strings.TrimSpace(req.Email)); email != "" {
		params.Email = &email
	}
	if req.Role != "" {
		role := req.Role
		params.Role = &role
	}

	if _, err := s.q.CreateInvite(ctx, params); err != nil {
		return "", fmt.Errorf("create invite: %w", err)
	}
	return token, nil
}

// RedeemInvite consumes an invite and creates the account it grants. When the
// invite names a project, the new user is added to it with the invite's
// role. Creating the user, marking the invite redeemed and granting the
// membership run in one transaction (via withTx, from Task 5): a failure
// partway through must not leave a replayable invite or a user with no
// membership.
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
func (s *Service) RedeemInvite(ctx context.Context, token, email, displayName, password string) (User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	sum := sha256.Sum256([]byte(token))

	invite, err := s.q.GetLiveInvite(ctx, sum[:])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrInviteInvalid
		}
		return User{}, fmt.Errorf("lookup invite: %w", err)
	}
	// invite.Email, like every other email this package stores, was
	// lower-cased and trimmed by CreateInvite before it was written; email
	// above is normalised the same way, so this is a like-for-like
	// comparison rather than one that happens to work only for
	// already-lowercase input.
	if invite.Email != nil && *invite.Email != email {
		return User{}, ErrInviteInvalid
	}

	var user User
	err = s.withTx(ctx, func(q *dbq.Queries) error {
		var cerr error
		user, cerr = s.createUserWith(ctx, q, CreateUserRequest{
			Email: email, DisplayName: displayName, Password: password,
		}, false)
		if cerr != nil {
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

// PruneExpiredInvites deletes every unredeemed invite past its expires_at
// and reports how many rows it removed. As with PruneExpiredSessions in
// sessions.go, nothing in this task calls it: invites_expires_idx exists
// for exactly this query, but wiring a periodic sweep is a
// process-lifecycle concern that belongs with the rest of main's start-up
// wiring, not with the identity service itself. Leaving it unwired here is
// deliberate, not an oversight: an unpruned expired invite is dead weight
// (RedeemInvite already treats it as invalid via GetLiveInvite's own
// expires_at > now() filter), not a live security exposure.
func (s *Service) PruneExpiredInvites(ctx context.Context) (int64, error) {
	n, err := s.q.DeleteExpiredInvites(ctx)
	if err != nil {
		return 0, fmt.Errorf("prune expired invites: %w", err)
	}
	return n, nil
}
