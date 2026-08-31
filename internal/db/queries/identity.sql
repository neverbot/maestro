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
