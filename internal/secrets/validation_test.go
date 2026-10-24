package secrets

import (
	"strings"
	"testing"
)

func TestValidateKey(t *testing.T) {
	cases := []struct {
		key  string
		want string // a fragment of the error, "" for valid
	}{
		{"DATABASE_URL", ""},
		{"a", ""},
		{"_private", ""},
		{"mixed_Case_123", ""},
		{strings.Repeat("K", MaxKeyLength), ""},
		{"", "must not be empty"},
		{strings.Repeat("K", MaxKeyLength+1), "exceeds maximum length"},
		{"1KEY", "must not start with a digit"},
		{"MY-KEY", "invalid character"},
		{"MY.KEY", "invalid character"},
		{"MY KEY", "invalid character"},
		{"KEY$", "invalid character"},
	}
	for _, c := range cases {
		err := ValidateKey(c.key)
		if (err == nil) != (c.want == "") || (err != nil && !strings.Contains(err.Error(), c.want)) {
			t.Errorf("ValidateKey(%q) = %v, want %q", c.key, err, c.want)
		}
	}
}

func TestValidateValue_LimitsSize(t *testing.T) {
	if err := ValidateValue(""); err != nil {
		t.Errorf("empty value: %v", err)
	}
	if err := ValidateValue(strings.Repeat("x", MaxValueSize)); err != nil {
		t.Errorf("value of the maximum size: %v", err)
	}
	if err := ValidateValue(strings.Repeat("x", MaxValueSize+1)); err == nil || !strings.Contains(err.Error(), "exceeds maximum") {
		t.Errorf("oversized value: %v, want the size error", err)
	}
}

func TestValidateEntries(t *testing.T) {
	cases := []struct {
		name    string
		entries []EntryInput
		want    string
	}{
		{"one entry", []EntryInput{{Key: "A", Value: "1"}}, ""},
		{"nil", nil, "must not be empty"},
		{"empty", []EntryInput{}, "must not be empty"},
		{"duplicate", []EntryInput{{Key: "A"}, {Key: "A"}}, "duplicate key"},
		{"duplicate ignoring case", []EntryInput{{Key: "Key"}, {Key: "KEY"}}, "duplicate key"},
		{"invalid key", []EntryInput{{Key: "1A"}}, "must not start with a digit"},
		{"oversized value", []EntryInput{{Key: "A", Value: strings.Repeat("x", MaxValueSize+1)}}, "exceeds maximum"},
	}
	for _, c := range cases {
		err := ValidateEntries(c.entries)
		if (err == nil) != (c.want == "") || (err != nil && !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

// Values are stored base64-encoded and decode back to the raw bytes,
// whatever they contain; a stored value that is not base64 is an error.
func TestEncodeDecodeValue_RoundTrip(t *testing.T) {
	for _, raw := range []string{"", "plain", "line1\nline2\n", "key: value\n  nested: true", "émoji 🔑 \x00 \"quotes\""} {
		got, err := DecodeValue(EncodeValue(raw))
		if err != nil || got != raw {
			t.Errorf("DecodeValue(EncodeValue(%q)) = %q, %v", raw, got, err)
		}
	}
	if _, err := DecodeValue("not base64!"); err == nil {
		t.Error("DecodeValue of malformed input: nil error")
	}
}
