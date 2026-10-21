package forge

import "database/sql"

// deliverySchemaSQL creates the delivery log: one row for each verified delivery the
// webhook ingress has recorded, keyed by workspace and the forge's id of the delivery, so
// that an insert that conflicts is a redelivery. target is NULL for a delivery that
// concerned no repository of the workspace; relayed holds the JSON array of the event
// types handed to the emitter. No column holds the payload or the webhook secret.
const deliverySchemaSQL = `CREATE TABLE IF NOT EXISTS forge_deliveries (
	workspace_slug TEXT NOT NULL,
	delivery_id    TEXT NOT NULL,
	forge          TEXT NOT NULL,
	event          TEXT NOT NULL,
	action         TEXT NOT NULL,
	target         TEXT,
	outcome        TEXT NOT NULL,
	relayed        TEXT NOT NULL,
	received_at    TEXT NOT NULL,
	PRIMARY KEY (workspace_slug, delivery_id)
)`

// deliveryIndexSQL serves the newest-first list of a workspace's deliveries, the latest
// received_at of GET /forge and the pruning by age.
const deliveryIndexSQL = `CREATE INDEX IF NOT EXISTS idx_forge_deliveries_received
	ON forge_deliveries (workspace_slug, received_at)`

// InitSchema creates the forge_deliveries table and its index. It runs at every start, in
// the way secrets.InitSchema does, and running it again leaves the rows alone.
func InitSchema(db *sql.DB) error {
	if _, err := db.Exec(deliverySchemaSQL); err != nil {
		return err
	}
	_, err := db.Exec(deliveryIndexSQL)
	return err
}
