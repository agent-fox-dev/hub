// Package secrets provides the store layer and validation logic for secrets
// and variables. Both resource types share the same storage schema and
// validation rules; the main difference is that secret values are never
// returned to API clients while variable values are readable.
package secrets

// MaxKeyLength is the maximum allowed length for a key name.
const MaxKeyLength = 255

// MaxValueSize is the maximum allowed size for a raw value in bytes (256 KB).
const MaxValueSize = 262144

// MaxEntriesPerScope is the maximum number of entries allowed per
// (owner_type, owner_id) scope.
const MaxEntriesPerScope = 100

// EntryInput represents a key/value pair in a create request.
type EntryInput struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// SecretEntry represents a stored secret. Value is never exposed via the API.
type SecretEntry struct {
	Key       string `json:"key"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// VariableEntry represents a stored variable. Value is readable via the API.
type VariableEntry struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ResolvedVariableEntry represents a variable in the resolved endpoint response.
// It includes the Origin field indicating which tier (user, org, workspace)
// the value was resolved from.
type ResolvedVariableEntry struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Origin    string `json:"origin"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}
