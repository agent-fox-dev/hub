-- Team members table: stores membership relationships between teams and users.
-- Composite primary key prevents duplicate memberships.
-- CASCADE DELETE on team_id ensures members are removed when a team is deleted (03-REQ-1.2).
CREATE TABLE IF NOT EXISTS team_members (
    team_id    TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL REFERENCES users(id),
    created_at DATETIME NOT NULL,
    PRIMARY KEY (team_id, user_id)
);
