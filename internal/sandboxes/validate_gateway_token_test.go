package sandboxes

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// tokenConfig returns a valid configuration for provider "gateway" with the given
// token.
func tokenConfig(token string) *Config {
	cfg := remoteConfig("https://gw.example")
	cfg.GatewayToken = token
	return cfg
}

// allPrintable is every byte from 0x21 to 0x7E, the whole alphabet a token may use.
func allPrintable() string {
	var b strings.Builder
	for c := byte(0x21); c <= 0x7E; c++ {
		b.WriteByte(c)
	}
	return b.String()
}

// TS-29-11: A missing or empty gateway_token under provider gateway is refused
// as required.
// Requirement: 29-REQ-3.1
func TestGatewayToken_Required_TS_29_11(t *testing.T) {
	unsetEnv(t, "AF_TEST_GATEWAY_TOKEN_UNSET")
	url := `gateway_url = "https://gw.example"`
	cases := map[string]string{
		"absent":       url,
		"empty":        url + "\ngateway_token = \"\"",
		"unset ${...}": url + "\ngateway_token = \"${AF_TEST_GATEWAY_TOKEN_UNSET}\"",
	}
	for name, lines := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeConfigFile(t, t.TempDir(), gatewayTOML(lines+"\n"))
			_, err := Validate(mustLoad(t, path))
			requireFailure(t, err, "sandbox.gateway_token", "required")
		})
	}
}

// TS-29-12: A 15-character token fails naming the minimum of 16, and a
// 16-character token passes.
// Requirement: 29-REQ-3.2
func TestGatewayToken_MinimumLength_TS_29_12(t *testing.T) {
	_, err := Validate(tokenConfig(strings.Repeat("a", 15)))
	requireFailure(t, err, "sandbox.gateway_token", "16")
	if _, err := Validate(tokenConfig(strings.Repeat("a", 16))); err != nil {
		t.Errorf("a token of 16 characters returned error: %v", err)
	}
	if _, err := Validate(tokenConfig("a")); err == nil {
		t.Error("a token of 1 character was accepted")
	}
}

// TS-29-13: A token with a byte outside 0x21 to 0x7E (a space, a newline, a tab,
// DEL, a non-ASCII byte or NUL) fails naming the rule, and the whole range passes.
// Requirement: 29-REQ-3.3
func TestGatewayToken_PrintableASCIIWithoutSpaces_TS_29_13(t *testing.T) {
	const sixteen = "abcdefghijklmnop"
	bad := map[string]string{
		"trailing newline": sixteen + "\n",
		"embedded space":   "abcdefgh ijklmnop",
		"leading space":    " " + sixteen,
		"tab":              "abcdefgh\tijklmnop",
		"carriage return":  sixteen + "\r",
		"DEL":              "abcdefgh\x7Fijklmnop",
		"high byte":        "abcdefgh\x80ijklmnop",
		"non-ASCII rune":   "abcdefgh\u00e9ijklmnop",
		"NUL":              "abcdefgh\x00ijklmnop",
	}
	for name, token := range bad {
		t.Run(name, func(t *testing.T) {
			_, err := Validate(tokenConfig(token))
			requireFailure(t, err, "sandbox.gateway_token", "printable ASCII without spaces")
		})
	}
	if all := allPrintable(); len(all) < 16 {
		t.Fatalf("the printable range has %d bytes", len(all))
	} else if _, err := Validate(tokenConfig(all)); err != nil {
		t.Errorf("a token of every byte from 0x21 to 0x7E returned error: %v", err)
	}
	for _, edge := range []string{strings.Repeat("!", 16), strings.Repeat("~", 16)} {
		if _, err := Validate(tokenConfig(edge)); err != nil {
			t.Errorf("a token of %q returned error: %v", edge[:1], err)
		}
	}
}

// genToken returns a token from one of the four families the property covers:
// valid, too short, with an invalid byte, and empty.
func genToken(r *rand.Rand) string {
	pick := func(alphabet string, n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = alphabet[r.IntN(len(alphabet))]
		}
		return string(b)
	}
	printable := allPrintable()
	switch r.IntN(5) {
	case 0:
		return pick(printable, 16+r.IntN(25))
	case 1:
		return pick(printable, 1+r.IntN(15))
	case 2:
		bad := " \t\n\r\x00\x7F\x80\xC3\xA9\xFF"
		token := []byte(pick(printable, 16+r.IntN(25)))
		token[r.IntN(len(token))] = bad[r.IntN(len(bad))]
		return string(token)
	case 3:
		return pick(printable+" \n", 8+r.IntN(30))
	}
	return ""
}

// TS-29-14: Validation error text never contains the token or any substring of
// it of 8 or more characters, whatever else is wrong with the configuration, and
// a token that breaks a rule always fails.
// Requirement: 29-REQ-3.4
func TestGatewayToken_ErrorNeverHoldsTheToken_TS_29_14(t *testing.T) {
	r := rand.New(rand.NewPCG(29, 14))
	for range 400 {
		token := genToken(r)
		cfg := tokenConfig(token)
		switch r.IntN(4) {
		case 1:
			cfg.GatewayURL = "http://gw.example:9000"
		case 2:
			cfg.GatewayURL = "https://user:" + token + "@gw.example"
		case 3:
			cfg.Provider = "podman"
		}
		cfg.Runtime = []string{"", "krun"}[r.IntN(2)]
		_, err := Validate(cfg)

		valid := len(token) >= 16 && !strings.ContainsFunc(token, func(c rune) bool { return c < 0x21 || c > 0x7E })
		if cfg.Provider == "gateway" && !valid && (err == nil || !strings.Contains(err.Error(), "sandbox.gateway_token")) {
			t.Fatalf("the token %q was accepted or not named: %v", token, err)
		}
		if err == nil {
			continue
		}
		text := err.Error()
		for i := 0; i+8 <= len(token); i++ {
			for j := i + 8; j <= len(token); j++ {
				if strings.Contains(text, token[i:j]) {
					t.Fatalf("the error holds %q of the token %q:\n%v", token[i:j], token, err)
				}
			}
		}
	}
}
