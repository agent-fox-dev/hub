package sandboxes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// redactHolder holds the token the ways a struct that is printed might: in an
// exported field, in an unexported one, in a slice, in a map and behind a pointer.
type redactHolder struct {
	Token   RedactedString
	hidden  RedactedString
	List    []RedactedString
	ByName  map[string]RedactedString
	Pointer *RedactedString
}

func newRedactHolder(secret string) redactHolder {
	token := newRedacted(secret)
	return redactHolder{
		Token:   token,
		hidden:  token,
		List:    []RedactedString{token},
		ByName:  map[string]RedactedString{"token": token},
		Pointer: &token,
	}
}

// requireRedacted fails the test unless out shows the marker and not the secret
// or any part of it of eight bytes or more.
func requireRedacted(t *testing.T, what, out, secret string) {
	t.Helper()
	if !strings.Contains(out, "[redacted]") {
		t.Errorf("%s does not show [redacted]: %q", what, out)
	}
	for i := 0; i+8 <= len(secret); i++ {
		if part := secret[i : i+8]; strings.Contains(out, part) {
			t.Errorf("%s shows %q of the secret: %q", what, part, out)
			return
		}
	}
}

// TS-29-37: The redacting token renders [redacted] under every formatting and
// encoding, alone and as a field of a printed struct.
// Requirement: 29-REQ-8.4
func TestRedactedString_RendersRedacted_TS_29_37(t *testing.T) {
	const secret = "tok-SECRET-0123456789-abcdef"
	token := newRedacted(secret)
	if token.reveal() != secret {
		t.Fatalf("reveal() = %q; want the secret", token.reveal())
	}
	holder := newRedactHolder(secret)

	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
		requireRedacted(t, "Sprintf("+verb+", token)", fmt.Sprintf(verb, token), secret)
		requireRedacted(t, "Sprintf("+verb+", &token)", fmt.Sprintf(verb, &token), secret)
		requireRedacted(t, "Sprintf("+verb+", holder)", fmt.Sprintf(verb, holder), secret)
		requireRedacted(t, "Sprintf("+verb+", &holder)", fmt.Sprintf(verb, &holder), secret)
	}
	requireRedacted(t, "Sprint", fmt.Sprint(token), secret)
	requireRedacted(t, "String", token.String(), secret)
	requireRedacted(t, "GoString", token.GoString(), secret)

	for name, handler := range map[string]func(*bytes.Buffer) slog.Handler{
		"text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
		"json": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
	} {
		var buf bytes.Buffer
		slog.New(handler(&buf)).Info("start", "token", token, "group", slog.GroupValue(slog.Any("inner", token)))
		requireRedacted(t, "the slog "+name+" handler", buf.String(), secret)
	}

	direct, err := json.Marshal(token)
	if err != nil {
		t.Fatalf("json.Marshal(token): %v", err)
	}
	requireRedacted(t, "json.Marshal(token)", string(direct), secret)
	if string(direct) != `"[redacted]"` {
		t.Errorf("json.Marshal(token) = %s; want the string [redacted]", direct)
	}
	nested, err := json.Marshal(holder)
	if err != nil {
		t.Fatalf("json.Marshal(holder): %v", err)
	}
	requireRedacted(t, "json.Marshal(holder)", string(nested), secret)
	text, err := token.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	requireRedacted(t, "MarshalText", string(text), secret)
}

// The validated definition holds the token in a RedactedString, so printing it
// never shows the token.
func TestValidated_PrintsWithoutTheToken(t *testing.T) {
	v := requireValid(t, remoteConfig("https://gw.example"))
	if v.GatewayToken.reveal() != testGatewayToken {
		t.Fatalf("the validated token = %q; want the configured one", v.GatewayToken.reveal())
	}
	for _, verb := range []string{"%v", "%+v", "%#v"} {
		requireRedacted(t, "Sprintf("+verb+", Validated)", fmt.Sprintf(verb, *v), testGatewayToken)
		requireRedacted(t, "Sprintf("+verb+", &Validated)", fmt.Sprintf(verb, v), testGatewayToken)
	}
}
