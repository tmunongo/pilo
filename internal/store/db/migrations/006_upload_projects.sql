ALTER TABLE projects ADD COLUMN source_type TEXT NOT NULL DEFAULT 'git' CHECK (source_type IN ('git', 'upload'));
