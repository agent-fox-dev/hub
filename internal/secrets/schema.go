package secrets

import "database/sql"

// InitSchema creates the secrets and variables tables in the database.
// It is called during server boot to ensure the schema exists.
func InitSchema(db *sql.DB) error {
	// TODO: implement in task group 4
	return nil
}
