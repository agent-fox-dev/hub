package sandboxes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// A RedactedString renders [redacted] under every verb, in slog, in JSON and
// as text, alone and inside a printed struct, and the zero value reveals "".
func TestRedactedString_RendersRedactedEverywhere(t *testing.T) {
	const secret = "tok-SECRET-0123456789-abcdef"
	token := newRedacted(secret)
	holder := struct {
		Token  RedactedString
		hidden RedactedString
		List   []RedactedString
		ByName map[string]RedactedString
		Ptr    *RedactedString
	}{token, token, []RedactedString{token}, map[string]RedactedString{"t": token}, &token}
	v := requireValid(t, remoteConfig("https://gw.example"))
	check := func(what, out string) {
		t.Helper()
		if !strings.Contains(out, "[redacted]") || strings.Contains(out, secret[4:12]) || strings.Contains(out, testGatewayToken[:8]) {
			t.Errorf("%s = %q; want [redacted] and no part of the secret", what, out)
		}
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
		check("Sprintf("+verb+", token)", fmt.Sprintf(verb, token))
		check("Sprintf("+verb+", holder)", fmt.Sprintf(verb, holder))
		check("Sprintf("+verb+", &holder)", fmt.Sprintf(verb, &holder))
		check("Sprintf("+verb+", Validated)", fmt.Sprintf(verb, *v))
	}
	check("String", token.String())
	check("GoString", token.GoString())
	for name, mk := range map[string]func(*bytes.Buffer) slog.Handler{
		"text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
		"json": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
	} {
		var buf bytes.Buffer
		slog.New(mk(&buf)).Info("start", "token", token, "group", slog.GroupValue(slog.Any("inner", token)))
		check("slog "+name, buf.String())
	}
	direct, _ := json.Marshal(token)
	if string(direct) != `"[redacted]"` {
		t.Errorf("json.Marshal(token) = %s", direct)
	}
	nested, _ := json.Marshal(holder)
	check("json.Marshal(holder)", string(nested))
	text, _ := token.MarshalText()
	check("MarshalText", string(text))
	if token.reveal() != secret || (RedactedString{}).reveal() != "" {
		t.Error("reveal does not return the secret, or the zero value reveals something")
	}
}
