CREATE TABLE IF NOT EXISTS users (
    id            TEXT PRIMARY KEY,
    email         TEXT UNIQUE NOT NULL COLLATE NOCASE,
    password_hash TEXT NOT NULL,
    created_at    DATETIME NOT NULL,
    updated_at    DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS password_resets (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL,
    expires_at DATETIME NOT NULL,
    used_at    DATETIME,
    created_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_password_resets_user_id ON password_resets(user_id);
CREATE INDEX IF NOT EXISTS idx_password_resets_token_hash ON password_resets(token_hash);

CREATE TABLE IF NOT EXISTS projects (
    id          TEXT PRIMARY KEY,
    slug        TEXT UNIQUE NOT NULL,
    user_id     TEXT NOT NULL DEFAULT '',
    repo_owner  TEXT NOT NULL DEFAULT '',
    repo_name   TEXT NOT NULL DEFAULT '',
    branch      TEXT NOT NULL DEFAULT 'main',
    subdir      TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'cloning',
    last_commit TEXT NOT NULL DEFAULT '',
    last_error  TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL,
    updated_at  DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_projects_user_id ON projects(user_id);

CREATE TABLE IF NOT EXISTS user_settings (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key     TEXT NOT NULL,
    value   TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (user_id, key)
);

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);

