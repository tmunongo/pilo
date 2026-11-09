ALTER TABLE projects ADD COLUMN repo_host TEXT NOT NULL DEFAULT 'github.com';
ALTER TABLE projects ADD COLUMN access_token TEXT NOT NULL DEFAULT '';
