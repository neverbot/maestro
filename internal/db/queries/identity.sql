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

-- name: MarkInviteRedeemed :execrows
UPDATE invites SET redeemed_at = now(), redeemed_by = sqlc.arg('redeemed_by')::uuid
WHERE id = sqlc.arg('id')::uuid
  AND redeemed_at IS NULL
  AND expires_at > now();

-- name: UpsertMembership :exec
INSERT INTO memberships (user_id, project_id, role)
VALUES (sqlc.arg('user_id')::uuid, sqlc.arg('project_id')::uuid, sqlc.arg('role')::text)
ON CONFLICT (user_id, project_id) DO UPDATE SET role = excluded.role;

-- name: DeleteExpiredInvites :execrows
DELETE FROM invites WHERE redeemed_at IS NULL AND expires_at <= now();
