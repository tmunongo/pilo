-- name: CreateUser :one
INSERT INTO users (id, email, password_hash, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = ? LIMIT 1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = ? LIMIT 1;

-- name: UpdateUserPassword :exec
UPDATE users
SET password_hash = ?, updated_at = ?
WHERE id = ?;

-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: CreateSession :one
INSERT INTO sessions (id, user_id, expires_at, created_at)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: GetSessionWithUser :one
SELECT sessions.id AS session_id,
       sessions.user_id,
       sessions.expires_at,
       sessions.created_at AS session_created_at,
       users.email,
       users.created_at AS user_created_at,
       users.updated_at AS user_updated_at
FROM sessions
JOIN users ON users.id = sessions.user_id
WHERE sessions.id = ? AND sessions.expires_at > ?
LIMIT 1;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = ?;

-- name: DeleteSessionsByUserID :exec
DELETE FROM sessions WHERE user_id = ?;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at <= ?;

-- name: CreatePasswordReset :one
INSERT INTO password_resets (id, user_id, token_hash, expires_at, created_at)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: GetActivePasswordResetByTokenHash :one
SELECT password_resets.*, users.email
FROM password_resets
JOIN users ON users.id = password_resets.user_id
WHERE password_resets.token_hash = ?
  AND password_resets.used_at IS NULL
  AND password_resets.expires_at > ?
LIMIT 1;

-- name: MarkPasswordResetUsed :exec
UPDATE password_resets
SET used_at = ?
WHERE id = ?;

-- name: ListProjects :many
SELECT * FROM projects
ORDER BY created_at ASC;

-- name: ListProjectsByUserID :many
SELECT * FROM projects
WHERE user_id = ?
ORDER BY created_at ASC;

-- name: GetProjectBySlug :one
SELECT * FROM projects
WHERE slug = ?
LIMIT 1;

-- name: GetProjectBySlugAndUserID :one
SELECT * FROM projects
WHERE slug = ? AND user_id = ?
LIMIT 1;

-- name: UpsertProject :exec
INSERT INTO projects (
    id, slug, user_id, repo_owner, repo_name, branch, subdir,
    status, last_commit, last_error, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    slug        = excluded.slug,
    user_id     = excluded.user_id,
    repo_owner  = excluded.repo_owner,
    repo_name   = excluded.repo_name,
    branch      = excluded.branch,
    subdir      = excluded.subdir,
    status      = excluded.status,
    last_commit = excluded.last_commit,
    last_error  = excluded.last_error,
    updated_at  = excluded.updated_at;

-- name: DeleteProjectBySlug :exec
DELETE FROM projects WHERE slug = ?;

-- name: DeleteProjectBySlugAndUserID :exec
DELETE FROM projects WHERE slug = ? AND user_id = ?;

-- name: ListUserSettings :many
SELECT key, value FROM user_settings WHERE user_id = ?;

-- name: GetUserSetting :one
SELECT value FROM user_settings WHERE user_id = ? AND key = ? LIMIT 1;

-- name: UpsertUserSetting :exec
INSERT INTO user_settings (user_id, key, value)
VALUES (?, ?, ?)
ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value;

-- name: ListSettings :many
SELECT key, value FROM settings;

-- name: GetSetting :one
SELECT value FROM settings WHERE key = ? LIMIT 1;

-- name: UpsertSetting :exec
INSERT INTO settings (key, value)
VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value;

-- name: CreateWebhookLog :one
INSERT INTO webhook_logs (
    id, project_slug, user_id, event, delivery, signature, status, message, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListWebhookLogsByProjectSlug :many
SELECT * FROM webhook_logs
WHERE project_slug = ?
ORDER BY created_at DESC
LIMIT ?;

-- name: ListWebhookLogsByProjectSlugAndUserID :many
SELECT * FROM webhook_logs
WHERE project_slug = ? AND user_id = ?
ORDER BY created_at DESC
LIMIT ?;

-- name: ListWebhookLogsByUserID :many
SELECT * FROM webhook_logs
WHERE user_id = ?
ORDER BY created_at DESC
LIMIT ?;

-- name: GetLatestWebhookLogForUser :one
SELECT * FROM webhook_logs
WHERE user_id = ?
ORDER BY created_at DESC
LIMIT 1;

-- name: GetLatestWebhookLogForProject :one
SELECT * FROM webhook_logs
WHERE project_slug = ?
ORDER BY created_at DESC
LIMIT 1;


