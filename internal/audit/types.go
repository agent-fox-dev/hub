// Package audit provides DuckDB-backed audit event storage and ingestion.
package audit

// HubEvent represents a hub-internal audit event passed to the Emitter.
type HubEvent struct {
	EventType    string
	ActorID      string
	ActorType    string // one of: admin_token, api_key, pat, system
	ResourceType string
	ResourceID   string
	Action       string
	Workspace    string
	Metadata     map[string]any
}

// HubEventRow represents a fully-prepared row ready for insertion into
// the hub_audit_events table.
type HubEventRow struct {
	ID           string
	EventType    string
	ActorID      string
	ActorType    string
	ResourceType string
	ResourceID   string
	Action       string
	Workspace    string
	Metadata     string // JSON-encoded
	IngestedAt   string
}
