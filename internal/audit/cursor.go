package audit

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// encodeCursor creates a base64url-encoded (RFC 4648 §5, no padding) cursor
// from a timestamp string and an id.
func encodeCursor(ts, id string) string {
	c := PaginatedCursor{Ts: ts, ID: id}
	data, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(data)
}

// decodeCursor decodes a base64url-encoded cursor and returns ts, id, and
// any error. Returns an error if the cursor is not valid base64url or does
// not contain ts and id fields.
func decodeCursor(cursor string) (ts string, id string, err error) {
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", fmt.Errorf("invalid cursor encoding: %w", err)
	}
	var c PaginatedCursor
	if err := json.Unmarshal(data, &c); err != nil {
		return "", "", fmt.Errorf("invalid cursor JSON: %w", err)
	}
	if c.Ts == "" || c.ID == "" {
		return "", "", fmt.Errorf("cursor missing required fields ts and id")
	}
	return c.Ts, c.ID, nil
}
