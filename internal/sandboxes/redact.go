package sandboxes

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/agent-fox-dev/hub/internal/scrub"
)

// RedactedString holds a secret that must never be printed: hub's token for the
// remote gateway. Every way a value is turned into text (fmt in any verb, slog,
// JSON, text marshalling) gives [redacted], so a struct that holds one can be
// printed or logged whole.
//
// The secret is behind a pointer on purpose. fmt prints an unexported field of
// a struct by reflection, without calling its methods, so a string held in a
// field would be printed whatever methods the type has; a pointer is printed as
// an address.
type RedactedString struct{ value *string }

// newRedacted returns a RedactedString holding s.
func newRedacted(s string) RedactedString { return RedactedString{value: &s} }

// reveal returns the secret. It is for the one place that sends it to the
// remote gateway.
func (r RedactedString) reveal() string {
	if r.value == nil {
		return ""
	}
	return *r.value
}

// Format writes [redacted] for every verb and flag, so %v, %+v, %#v, %s, %q and
// the rest all agree.
func (RedactedString) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, scrub.Redacted) }

// String returns [redacted].
func (RedactedString) String() string { return scrub.Redacted }

// GoString returns [redacted].
func (RedactedString) GoString() string { return scrub.Redacted }

// LogValue makes slog log [redacted].
func (RedactedString) LogValue() slog.Value { return slog.StringValue(scrub.Redacted) }

// MarshalJSON encodes [redacted] as a JSON string.
func (RedactedString) MarshalJSON() ([]byte, error) { return []byte(`"` + scrub.Redacted + `"`), nil }

// MarshalText returns [redacted].
func (RedactedString) MarshalText() ([]byte, error) { return []byte(scrub.Redacted), nil }
