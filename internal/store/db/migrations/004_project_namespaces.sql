ALTER TABLE users ADD COLUMN username TEXT NOT NULL DEFAULT '' COLLATE NOCASE;
CREATE UNIQUE INDEX idx_users_username ON users(username) WHERE username != '';

CREATE TABLE projects_new (
    id          TEXT PRIMARY KEY,
    slug        TEXT NOT NULL,
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

INSERT INTO projects_new SELECT id, slug, user_id, repo_owner, repo_name, branch, subdir, status, last_commit, last_error, created_at, updated_at FROM projects;
DROP TABLE projects;
ALTER TABLE projects_new RENAME TO projects;
CREATE UNIQUE INDEX idx_projects_owner_slug ON projects(user_id, slug);
CREATE INDEX idx_projects_user_id ON projects(user_id);
