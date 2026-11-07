CREATE TABLE IF NOT EXISTS projects (
    id          TEXT PRIMARY KEY,
    slug        TEXT UNIQUE NOT NULL,
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

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);

