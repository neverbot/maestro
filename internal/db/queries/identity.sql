-- name: CreateUser :one
INSERT INTO users (email, display_name, password_hash, is_admin)
VALUES (sqlc.arg('email')::text, sqlc.arg('display_name')::text,
        sqlc.arg('password_hash')::text, sqlc.arg('is_admin')::boolean)
RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE lower(email) = lower(sqlc.arg('email')::text);

-- name: GetUserByID :one
SELECT * FROM users WHERE id = sqlc.arg('id')::uuid;

-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: UpdateUserPasswordHash :exec
UPDATE users SET password_hash = sqlc.arg('password_hash')::text
WHERE id = sqlc.arg('id')::uuid;

-- name: CreateSession :exec
INSERT INTO sessions (token_hash, user_id, expires_at)
VALUES (sqlc.arg('token_hash')::bytea, sqlc.arg('user_id')::uuid, sqlc.arg('expires_at')::timestamptz);

-- name: GetSessionUser :one
SELECT u.id, u.email, u.display_name, u.password_hash, u.is_admin, u.created_at, u.updated_at,
       s.expires_at AS session_expires_at
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token_hash = sqlc.arg('token_hash')::bytea
  AND s.expires_at > now();

-- name: ExtendSession :exec
UPDATE sessions SET expires_at = sqlc.arg('expires_at')::timestamptz
WHERE token_hash = sqlc.arg('token_hash')::bytea;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE token_hash = sqlc.arg('token_hash')::bytea;

-- name: DeleteSessionsForUser :exec
DELETE FROM sessions WHERE user_id = sqlc.arg('user_id')::uuid;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at <= now();

-- name: CreateInvite :one
INSERT INTO invites (token_hash, email, project_id, role, created_by, expires_at)
VALUES (sqlc.arg('token_hash')::bytea, sqlc.narg('email')::text,
        sqlc.narg('project_id')::uuid, sqlc.narg('role')::text,
        sqlc.narg('created_by')::uuid, sqlc.arg('expires_at')::timestamptz)
RETURNING *;

-- name: GetLiveInvite :one
SELECT * FROM invites
WHERE token_hash = sqlc.arg('token_hash')::bytea
  AND redeemed_at IS NULL
  AND expires_at > now();

-- name: GetInviteByTokenHash :one
-- Unlike GetLiveInvite, this ignores redeemed_at and expires_at: it exists
-- only for RedeemInvite's fallback path, reached after GetLiveInvite has
-- already found no row, to tell an expired invite apart from a truly
-- unknown or already-redeemed one. Reaching that fallback at all requires
-- the caller to already hold a token whose SHA-256 equals a stored
-- token_hash, which nobody can produce without either holding the real
-- token or having brute-forced 256 bits of entropy — so this query never
-- gives an attacker anything they could not already get by holding the
-- token itself.
SELECT * FROM invites WHERE token_hash = sqlc.arg('token_hash')::bytea;

-- name: MarkInviteRedeemed :execrows
UPDATE invites SET redeemed_at = now(), redeemed_by = sqlc.arg('redeemed_by')::uuid
WHERE id = sqlc.arg('id')::uuid
  AND redeemed_at IS NULL
  AND expires_at > now();

-- name: ListOutstandingInvites :many
-- "Outstanding" means not yet redeemed, regardless of whether it has since
-- expired: an admin looking for a mis-sent invite to revoke needs to find
-- it before it necessarily expires on its own, and a lapsed-but-unredeemed
-- row is also useful context ("this one needs reissuing"). Ordered
-- newest-first, with id as an explicit tiebreak (matching ListAPITokens
-- and every other listing in this file that learned this lesson first) so
-- two invites created in the same transaction don't reshuffle between
-- calls.
SELECT * FROM invites WHERE redeemed_at IS NULL ORDER BY created_at DESC, id DESC;

-- name: RevokeInvite :exec
-- Setting expires_at to now(), rather than deleting the row, keeps the
-- audit trail (who created it, when, for what) intact instead of erasing
-- it — the same reasoning DeleteExpiredInvites documents for why it only
-- ever removes unredeemed rows. Restricted to redeemed_at IS NULL so
-- revoking an already-redeemed or already-expired invite is a no-op that
-- cannot rewrite a real redemption's or an earlier revocation's expires_at.
UPDATE invites SET expires_at = now()
WHERE id = sqlc.arg('id')::uuid AND redeemed_at IS NULL;

-- name: UpsertMembership :exec
INSERT INTO memberships (user_id, project_id, role)
VALUES (sqlc.arg('user_id')::uuid, sqlc.arg('project_id')::uuid, sqlc.arg('role')::text)
ON CONFLICT (user_id, project_id) DO UPDATE SET role = excluded.role;

-- name: DeleteExpiredInvites :execrows
-- Only ever removes unredeemed rows (redeemed_at IS NULL): a redeemed
-- invite past its original expires_at is not "expired" in any sense that
-- matters (it already did its job and MarkInviteRedeemed's own WHERE
-- clause makes it unreachable a second time regardless), and deleting it
-- would erase who created an account or a membership grant and when —
-- exactly the audit trail RevokeInvite above is careful to preserve too.
DELETE FROM invites WHERE redeemed_at IS NULL AND expires_at <= now();

-- name: CreateAPIToken :one
INSERT INTO api_tokens (token_hash, project_id, user_id, label, token_hint)
VALUES (sqlc.arg('token_hash')::bytea, sqlc.arg('project_id')::uuid,
        sqlc.arg('user_id')::uuid, sqlc.arg('label')::text, sqlc.arg('token_hint')::text)
RETURNING *;

-- name: GetLiveAPIToken :one
SELECT * FROM api_tokens
WHERE token_hash = sqlc.arg('token_hash')::bytea AND revoked_at IS NULL;

-- name: TouchAPIToken :exec
-- Throttled: this runs on every authenticated agent request, so an
-- unconditional UPDATE would take a row lock on the hot path for no
-- observable benefit. Only write when the existing timestamp is missing or
-- more than five minutes stale. An UPDATE whose WHERE clause matches no
-- row takes no row lock at all, so the common case (touched within the
-- last five minutes) costs a statement round trip but no lock contention.
-- The five-minute interval here must match identity.touchThrottle in
-- tokens.go, which pre-checks the same condition in Go before ever
-- issuing this statement (so the common case skips the round trip
-- entirely, not just the lock) — this WHERE clause stays authoritative
-- regardless, since it is what actually makes the write safe under two
-- concurrent resolves of the same token, which the Go pre-check alone
-- cannot guarantee. Change one, change both.
UPDATE api_tokens SET last_used_at = now()
WHERE id = sqlc.arg('id')::uuid
  AND (last_used_at IS NULL OR last_used_at < now() - interval '5 minutes');

-- name: RevokeAPIToken :exec
-- Scoped to project_id as well as id: revoking an id that exists but
-- belongs to a different project, like revoking an unknown or
-- already-revoked id, is a no-op rather than an error — the caller's goal
-- (no live token under this id in this project) is already satisfied, the
-- same convention RevokeSession and RevokeInvite already establish. This
-- also means the project scope of the caller is enforced by the query
-- itself, not by a separate ownership check the caller could forget.
UPDATE api_tokens SET revoked_at = now()
WHERE id = sqlc.arg('id')::uuid AND project_id = sqlc.arg('project_id')::uuid
  AND revoked_at IS NULL;

-- name: ListAPITokens :many
-- token_hash is deliberately not selected: this query backs an operator
-- listing (ListAPITokens in tokens.go), and the hash of a bearer
-- credential has no reason to leave the database even in a column nothing
-- currently renders. token_hint is selected instead — see its column
-- comment (migration 0003) for what it is safe to show. Revoked rows are
-- included on purpose: this is the audit trail for what happened to a
-- token, not only a list of what is still live. Joined to users so an
-- operator triaging a leaked token sees who minted it instead of a raw
-- id, and ordered by created_at then id so ties (two tokens minted in
-- the same transaction) list in a stable order across repeated calls,
-- the same tiebreak reasoning ListProjectsForUser and ListMembers
-- already apply to their own orderings.
SELECT t.id, t.project_id, t.user_id, u.display_name AS user_display_name,
       t.label, t.token_hint, t.created_at, t.last_used_at, t.revoked_at
FROM api_tokens t
JOIN users u ON u.id = t.user_id
WHERE t.project_id = sqlc.arg('project_id')::uuid
ORDER BY t.created_at DESC, t.id DESC;
