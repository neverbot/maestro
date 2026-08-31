-- api_tokens.token_hint stores a short, non-secret discriminator derived
-- from the clear token's random body (never from its hash, and never a
-- prefix long enough to matter for guessing): enough for an operator who
-- finds "mst_xK9…" in a leaked log or a committed .env to find the
-- matching row via ListAPITokens without being able to reconstruct the
-- token, or anything close to it, from the hint alone.
-- +goose Up
ALTER TABLE api_tokens ADD COLUMN token_hint text NOT NULL DEFAULT '';
ALTER TABLE api_tokens ALTER COLUMN token_hint DROP DEFAULT;

-- +goose Down
ALTER TABLE api_tokens DROP COLUMN token_hint;
