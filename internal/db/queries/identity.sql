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

-- name: CountAPITokensForProject :one
-- Every api_tokens row scoped to project_id, revoked or not — the same
-- "audit trail, not just what's live" scope ListAPITokens already uses.
-- Added in Task 18's Round 2 corrections purely so handleDeleteGame can
-- log how many tokens its cascade is about to take with it, since that
-- number cannot be recovered afterward — projects.Delete's own DELETE FROM
-- projects statement carries no rows-affected count for anything it
-- cascades into (see that method's own doc comment).
SELECT count(*) FROM api_tokens WHERE project_id = sqlc.arg('project_id')::uuid;

-- name: CountInvitesForProject :one
-- Every invites row scoped to project_id, redeemed or not — deliberately
-- wider than ListOutstandingProjectInvites' redeemed_at IS NULL filter,
-- since a game's cascade takes its already-redeemed (audit-trail) invite
-- rows with it too, not only its outstanding ones. Added alongside
-- CountAPITokensForProject above, for the same reason.
SELECT count(*) FROM invites WHERE project_id = sqlc.arg('project_id')::uuid;

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

-- name: ExtendSession :execrows
-- Sliding renewal, called from web.resolveSessionCaller on a session past
-- the halfway point of its SESSION_TTL. The target expiry is computed
-- here, in SQL, from the session's own row and Postgres' own now() — not
-- passed in from Go as an already-resolved timestamp. An earlier version
-- did the latter (now().Add(ttl) computed once per request in auth.go)
-- and its WHERE clause compared "is the new value later than the
-- current one" — which sounds like it should make concurrent renewals
-- collapse into one write, but does not: each request computes its own,
-- slightly later, wall-clock target, so a second writer blocking on the
-- row lock wakes up, re-evaluates against the now-committed row, finds
-- its own target still greater, and writes again. A quality review
-- proved this by holding the row lock open with pg_sleep and by firing
-- fifteen concurrent requests at the real binary — three to six writes
-- each time, not one.
--
-- Two things fix that, both here instead of trusted to the Go caller:
--
-- Concurrency: the target is now() + ttl computed by this statement, and
-- the WHERE clause requires the *current* expires_at to be more than a
-- minute short of that target before writing at all. A session already
-- renewed by another concurrent request seconds ago has an expires_at
-- within that one-minute slack of any request's now() + ttl, so it
-- matches nothing and writes nothing. This does not collapse N
-- concurrent requests into exactly one write in the general case — two
-- requests arriving more than a minute apart both legitimately renew,
-- which is the throttle working as intended, the same way
-- TouchAPIToken's five-minute window lets last_used_at move again after
-- it elapses — but it does mean requests that are actually concurrent
-- (the case this comment used to overclaim "a no-op" for) collapse to
-- one write, because they all fall inside the one-minute slack of each
-- other's target.
--
-- Absolute lifetime: capping the new value at created_at + 90 days (see
-- identity.maxSessionLifetime in sessions.go, which this literal must
-- match) means a session in continuous use is renewed right up to that
-- cap and never again past it.
--
-- This stays an UPDATE, never an upsert: a logout (DeleteSession) landing
-- between the SELECT in UserForSession and this statement leaves no row
-- to match, and an UPDATE that matches nothing creates nothing — a
-- revoked session cannot be resurrected by a renewal that started before
-- the revocation. Do not change this to INSERT ... ON CONFLICT to "handle"
-- a missing row; a missing row here means "let it stay gone". This is
-- :execrows, not :exec, purely so a test can count actual writes instead
-- of inferring them from the final value, which looks identical whether
-- one write happened or six.
UPDATE sessions
SET expires_at = LEAST(created_at + interval '90 days', now() + sqlc.arg('ttl')::interval)
WHERE token_hash = sqlc.arg('token_hash')::bytea
  AND expires_at < now() + sqlc.arg('ttl')::interval - interval '1 minute';

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
--
-- project_id IS NULL, added in Task 18: this is the instance-wide admin
-- surface (POST/GET/DELETE /api/invites, gated on Caller.IsAdmin, never on
-- project membership), so it must never return a project-bound invite — an
-- instance admin has no standing in a game they are not a member of
-- anywhere else in this codebase (requireProject's RoleOf lookup never
-- consults IsAdmin), and this query returning another game's pending
-- invite roster would be the one place that stopped being true. A
-- project-bound invite is listed through its own game's
-- ListOutstandingProjectInvites instead, gated on that game's own owner
-- role.
SELECT * FROM invites WHERE redeemed_at IS NULL AND project_id IS NULL ORDER BY created_at DESC, id DESC;

-- name: ListOutstandingProjectInvites :many
-- ListOutstandingInvites' project-scoped counterpart, added in Task 18 for
-- GET /api/games/{game}/invites: the same "outstanding" definition, the
-- same ordering, scoped to invites that name this one game instead of
-- account-only ones. See ListOutstandingInvites' own comment for why the
-- two never overlap.
SELECT * FROM invites WHERE redeemed_at IS NULL AND project_id = sqlc.arg('project_id')::uuid ORDER BY created_at DESC, id DESC;

-- name: RevokeInvite :exec
-- Setting expires_at to now(), rather than deleting the row, keeps the
-- audit trail (who created it, when, for what) intact instead of erasing
-- it — the same reasoning DeleteExpiredInvites documents for why it only
-- ever removes unredeemed rows. Restricted to redeemed_at IS NULL so
-- revoking an already-redeemed or already-expired invite is a no-op that
-- cannot rewrite a real redemption's or an earlier revocation's expires_at.
--
-- project_id IS NULL, added in Task 18, for the same reason
-- ListOutstandingInvites above is scoped to it: this backs the
-- instance-wide DELETE /api/invites/{id}, and a project-bound invite must
-- only ever be revocable through its own game's RevokeProjectInvite, gated
-- on that game's owner role — never through the instance admin surface,
-- which has no standing over a specific game's membership grants.
UPDATE invites SET expires_at = now()
WHERE id = sqlc.arg('id')::uuid AND project_id IS NULL AND redeemed_at IS NULL;

-- name: RevokeProjectInvite :exec
-- RevokeInvite's project-scoped counterpart, added in Task 18 for
-- DELETE /api/games/{game}/invites/{invite}. Scoped to project_id in
-- addition to id, the same way RevokeAPIToken is scoped to project_id: an
-- id that exists but names a different game's invite must be a silent
-- no-op, not a 404 or a 500, so a caller with standing in one game can
-- never use this to probe whether some other id belongs to a different
-- game.
UPDATE invites SET expires_at = now()
WHERE id = sqlc.arg('id')::uuid AND project_id = sqlc.arg('project_id')::uuid AND redeemed_at IS NULL;

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
-- Joins users for is_admin so web.resolveBearerCaller can build a Caller
-- from one query instead of two. This is a deliberate exception to the
-- rest of this file keeping api_tokens and users apart (ListAPITokens
-- below joins only for a display label, never a fact the middleware would
-- trust) — see identity.APITokenSummary.UserIsAdmin's own doc comment for
-- why this one earns the join: it replaces a second round trip on the
-- hottest authenticated path in the product, the same class of cost
-- TouchAPIToken's throttle and ExtendSession's guard above exist to
-- control, and Task 9's original design (a lean, no-join lookup here) is
-- the one being traded away to get it.
SELECT t.*, u.is_admin AS user_is_admin FROM api_tokens t
JOIN users u ON u.id = t.user_id
WHERE t.token_hash = sqlc.arg('token_hash')::bytea AND t.revoked_at IS NULL;

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
