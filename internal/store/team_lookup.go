// team_lookup.go — lightweight team existence and membership queries
// used by the spec 07 workspace handler for access control checks.
//
// These methods query the teams and team_members tables introduced by
// spec 06 (team_rename). They are deliberately minimal — returning
// booleans rather than full structs — because the workspace handler
// only needs existence and membership checks.
package store

import "database/sql"

// TeamExists reports whether a team with the given ID exists in the
// teams table. It returns (false, nil) when no matching row is found
// and (false, err) on database errors.
func (s *sqliteStore) TeamExists(id string) (bool, error) {
	var one int
	err := s.db.QueryRow("SELECT 1 FROM teams WHERE id = ?", id).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// IsTeamMember reports whether the user identified by userID is a member
// of the team identified by teamID. Any role (reader, editor, admin) is
// sufficient — the caller decides what to do with the result.
func (s *sqliteStore) IsTeamMember(userID, teamID string) (bool, error) {
	var one int
	err := s.db.QueryRow(
		"SELECT 1 FROM team_members WHERE user_id = ? AND team_id = ?",
		userID, teamID,
	).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
