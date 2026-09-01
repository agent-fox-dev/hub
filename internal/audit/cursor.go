package audit

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// cursorPayload is the internal representation of a keyset pagination cursor.
// It encodes a (timestamp, id) tuple for deterministic ordering.
type cursorPayload struct {
	TS string `json:"ts"`
	ID string `json:"id"`
}

// encodeCursor encodes a (timestamp, id) tuple as a URL-safe base64 string
// per RFC 4648 section 5, without padding.
func encodeCursor(ts, id string) string {
	payload := cursorPayload{TS: ts, ID: id}
	data, _ := json.Marshal(payload)
	return base64.RawURLEncoding.EncodeToString(data)
}

// decodeCursor decodes an opaque cursor string into its (timestamp, id) tuple.
// Returns an error if the cursor is not valid URL-safe base64 or does not
// decode to a valid JSON payload with non-empty ts and id fields.
func decodeCursor(cursor string) (ts string, id string, err error) {
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", fmt.Errorf("invalid cursor: %w", err)
	}

	var payload cursorPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", "", fmt.Errorf("invalid cursor: %w", err)
	}

	if payload.TS == "" || payload.ID == "" {
		return "", "", fmt.Errorf("invalid cursor: missing ts or id")
	}

	return payload.TS, payload.ID, nil
}
