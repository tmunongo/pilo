CREATE TABLE IF NOT EXISTS webhook_logs (
    id           TEXT PRIMARY KEY,
    project_slug TEXT NOT NULL DEFAULT '',
    user_id      TEXT NOT NULL DEFAULT '',
    event        TEXT NOT NULL DEFAULT 'push',
    delivery     TEXT NOT NULL DEFAULT '',
    signature    TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'success',
    message      TEXT NOT NULL DEFAULT '',
    created_at   DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_webhook_logs_project_slug ON webhook_logs(project_slug);
CREATE INDEX IF NOT EXISTS idx_webhook_logs_user_id ON webhook_logs(user_id);
CREATE INDEX IF NOT EXISTS idx_webhook_logs_created_at ON webhook_logs(created_at);

