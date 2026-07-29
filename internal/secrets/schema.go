package secrets

import "database/sql"

// InitSchema creates the secrets and variables tables in the database.
// It is called during server boot to ensure the schema exists.
func InitSchema(db *sql.DB) error {
	const secretsSQL = `CREATE TABLE IF NOT EXISTS secrets (
		owner_type TEXT NOT NULL CHECK(owner_type IN ('user', 'org', 'workspace')),
		owner_id   TEXT NOT NULL,
		key        TEXT NOT NULL,
		value      TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (owner_type, owner_id, key)
	)`

	const variablesSQL = `CREATE TABLE IF NOT EXISTS variables (
		owner_type TEXT NOT NULL CHECK(owner_type IN ('user', 'org', 'workspace')),
		owner_id   TEXT NOT NULL,
		key        TEXT NOT NULL,
		value      TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (owner_type, owner_id, key)
	)`

	if _, err := db.Exec(secretsSQL); err != nil {
		return err
	}
	if _, err := db.Exec(variablesSQL); err != nil {
		return err
	}
	return nil
}
