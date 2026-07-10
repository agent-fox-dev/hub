-- Teams table: stores team records with lifecycle status.
-- Uses partial UNIQUE indexes to scope uniqueness to non-deleted teams,
-- allowing reuse of names and slugs after deletion (03-REQ-2.E2).
CREATE TABLE IF NOT EXISTS teams (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    slug       TEXT NOT NULL,
    url        TEXT,
    status     TEXT NOT NULL DEFAULT 'active',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_teams_name_active
    ON teams (name) WHERE status != 'deleted';

CREATE UNIQUE INDEX IF NOT EXISTS idx_teams_slug_active
    ON teams (slug) WHERE status != 'deleted';
