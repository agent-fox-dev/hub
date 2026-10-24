package sandboxes

import (
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

// edited returns validConfig changed by edit.
func edited(edit func(*Config)) *Config {
	cfg := validConfig()
	edit(cfg)
	return cfg
}

func mustFail(t *testing.T, cfg *Config) error {
	t.Helper()
	v, err := Validate(cfg)
	if err == nil || v != nil {
		t.Fatalf("Validate accepted an invalid configuration: %+v", v)
	}
	return err
}

func TestValidate_NilSectionIsAnError(t *testing.T) {
	if _, err := Validate(nil); err == nil {
		t.Error("Validate(nil) returned no error")
	}
}

// The defaults of the deployment keys, and the configured values kept as written.
func TestValidate_DeploymentDefaults(t *testing.T) {
	v := requireValid(t, validConfig())
	if v.EngineBinary != "podman" || v.Runtime != "" || v.UserNS != "" || v.MaxTTL != 4*time.Hour || !slices.Equal(v.NetworkModes, []string{"none"}) ||
		v.MaxSandboxes != 16 || v.Listen != "127.0.0.1:8443" || v.TLSCert != "" || v.ExternalURL != "" || v.LeaseTimeout != 5*time.Minute || v.ReapInterval != 10*time.Minute {
		t.Errorf("defaults = %+v", v)
	}
	// The default lease_timeout is not held to a short max_ttl.
	if v := requireValid(t, edited(func(c *Config) { c.MaxTTL = strPtr("2m"); c.Profiles[0].TTL = "1m" })); v.LeaseTimeout != 5*time.Minute {
		t.Errorf("LeaseTimeout under max_ttl 2m = %v; want the default", v.LeaseTimeout)
	}
	limit := 3.0
	v = requireValid(t, edited(func(c *Config) {
		c.EngineBinary, c.Runtime, c.UserNS = "docker", "krun", "keep-id"
		c.MaxTTL, c.NetworkModes, c.MaxSandboxes = strPtr("90m"), []string{"none", "unrestricted"}, &limit
		c.LeaseTimeout, c.ReapInterval = strPtr("1h"), strPtr("1h")
		c.Listen, c.TLSCert, c.TLSKey, c.ExternalURL = "0.0.0.0:8443", "/c", "/k", "https://gw.example.com/"
	}))
	if v.EngineBinary != "docker" || v.Runtime != "krun" || v.UserNS != "keep-id" || v.MaxTTL != 90*time.Minute || !slices.Equal(v.NetworkModes, []string{"none", "unrestricted"}) ||
		v.MaxSandboxes != 3 || v.LeaseTimeout != time.Hour || v.ReapInterval != time.Hour || v.Listen != "0.0.0.0:8443" || v.ExternalURL != "https://gw.example.com/" {
		t.Errorf("configured = %+v; want the values as written", v)
	}
	// Under gateway there is no engine binary to default, and the token is redacted.
	if v := requireValid(t, remoteConfig("https://gw.example")); v.EngineBinary != "" || v.GatewayToken.reveal() != testGatewayToken {
		t.Errorf("gateway: engine_binary %q, token of %d bytes", v.EngineBinary, len(v.GatewayToken.reveal()))
	}
}

// Every deployment key refuses the values outside its rule, naming the key.
func TestValidate_DeploymentKeys(t *testing.T) {
	limit := func(f float64) *float64 { return &f }
	for _, tc := range []struct {
		name string
		edit func(*Config)
		want []string
	}{
		{"provider required", func(c *Config) { c.Provider = "" }, []string{"sandbox.provider", "required", "podman", "gateway"}},
		{"provider unsupported", func(c *Config) { c.Provider = "kubernetes" }, []string{"sandbox.provider", "kubernetes", "podman", "gateway"}},
		{"max_ttl garbage", func(c *Config) { c.MaxTTL = strPtr("soon") }, []string{"sandbox.max_ttl"}},
		{"max_ttl zero", func(c *Config) { c.MaxTTL = strPtr("0s") }, []string{"sandbox.max_ttl", "greater than zero"}},
		{"max_ttl empty", func(c *Config) { c.MaxTTL = strPtr("") }, []string{"sandbox.max_ttl"}},
		{"network_modes empty entry", func(c *Config) { c.NetworkModes = []string{"none", ""} }, []string{"sandbox.network_modes[1]", "empty"}},
		{"network_modes repeat", func(c *Config) { c.NetworkModes = []string{"none", "unrestricted", "none"} }, []string{"sandbox.network_modes[2]", "repeats", "sandbox.network_modes[0]"}},
		{"max_sandboxes zero", func(c *Config) { c.MaxSandboxes = limit(0) }, []string{"sandbox.max_sandboxes", "whole number"}},
		{"max_sandboxes fraction", func(c *Config) { c.MaxSandboxes = limit(2.5) }, []string{"sandbox.max_sandboxes"}},
		{"max_sandboxes huge", func(c *Config) { c.MaxSandboxes = limit(1e300) }, []string{"sandbox.max_sandboxes"}},
		{"max_sandboxes NaN", func(c *Config) { c.MaxSandboxes = limit(math.NaN()) }, []string{"sandbox.max_sandboxes"}},
		{"lease_timeout garbage", func(c *Config) { c.LeaseTimeout = strPtr("abc") }, []string{"sandbox.lease_timeout"}},
		{"lease_timeout short", func(c *Config) { c.LeaseTimeout = strPtr("999ms") }, []string{"sandbox.lease_timeout", "at least 1s"}},
		{"lease_timeout over max_ttl", func(c *Config) { c.MaxTTL = strPtr("1h"); c.LeaseTimeout = strPtr("2h") }, []string{"sandbox.lease_timeout", "at most sandbox.max_ttl"}},
		{"lease_timeout empty", func(c *Config) { c.LeaseTimeout = strPtr("") }, []string{"sandbox.lease_timeout"}},
		{"reap_interval garbage", func(c *Config) { c.ReapInterval = strPtr("xyz") }, []string{"sandbox.reap_interval"}},
		{"reap_interval short", func(c *Config) { c.ReapInterval = strPtr("9s") }, []string{"sandbox.reap_interval", "between 10s and 1h0m0s"}},
		{"reap_interval long", func(c *Config) { c.ReapInterval = strPtr("61m") }, []string{"sandbox.reap_interval"}},
		{"listen no port", func(c *Config) { c.Listen = "127.0.0.1" }, []string{"sandbox.listen", "host:port"}},
		{"listen named port", func(c *Config) { c.Listen = "127.0.0.1:http" }, []string{"sandbox.listen", "0 to 65535"}},
		{"listen port too big", func(c *Config) { c.Listen = "127.0.0.1:65536" }, []string{"sandbox.listen"}},
		{"tls_cert alone", func(c *Config) { c.TLSCert = "/c" }, []string{"sandbox.tls_key is required"}},
		{"tls_key alone", func(c *Config) { c.TLSKey = "/k" }, []string{"sandbox.tls_cert is required"}},
		{"plaintext off loopback", func(c *Config) { c.Listen = "0.0.0.0:8443"; c.ExternalURL = "https://gw.example.com" }, []string{"sandbox.listen", "loopback"}},
		{"off loopback without external_url", func(c *Config) { c.Listen = "0.0.0.0:8443"; c.TLSCert, c.TLSKey = "/c", "/k" }, []string{"sandbox.external_url is required"}},
		{"external_url relative", func(c *Config) { c.ExternalURL = "gw.example.com" }, []string{"sandbox.external_url", "absolute"}},
		{"external_url no host", func(c *Config) { c.ExternalURL = "https://" }, []string{"sandbox.external_url", "host"}},
		{"external_url userinfo", func(c *Config) { c.ExternalURL = "https://u:p@gw" }, []string{"sandbox.external_url", "user information"}},
		{"external_url query", func(c *Config) { c.ExternalURL = "https://gw?x=1" }, []string{"sandbox.external_url", "query"}},
		{"external_url path", func(c *Config) { c.ExternalURL = "https://gw/api" }, []string{"sandbox.external_url", "path"}},
		{"external_url invalid", func(c *Config) { c.ExternalURL = "://nohost" }, []string{"sandbox.external_url", "valid URL"}},
		{"podman keys under gateway", func(c *Config) {
			*c = *remoteConfig("https://gw.example")
			c.EngineBinary, c.Runtime, c.UserNS = "/p", "krun", "keep-id"
		},
			[]string{"sandbox.runtime", `applies only to sandbox.provider "podman"`}},
		{"gateway keys under podman", func(c *Config) { c.GatewayURL, c.GatewayToken = "https://gw.example", "tok-SECRET-0123456789" },
			[]string{"sandbox.gateway_token", `applies only to sandbox.provider "gateway"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := mustFail(t, edited(tc.edit))
			requireFailure(t, err, tc.want...)
			if strings.Contains(err.Error(), "u:p") || strings.Contains(err.Error(), "SECRET") {
				t.Errorf("the error repeats a credential: %v", err)
			}
		})
	}
	// The ports 0 and 65535 and the loopback names are accepted.
	for _, listen := range []string{"127.0.0.1:0", "127.0.0.1:65535", "localhost:8443", "[::1]:8443"} {
		requireValid(t, edited(func(c *Config) { c.Listen = listen }))
	}
	for _, addr := range []string{"localhost:1", "127.5.5.5:1", "[::1]:1"} {
		if !isLoopback(addr) {
			t.Errorf("isLoopback(%q) = false", addr)
		}
	}
	for _, addr := range []string{":1", "0.0.0.0:1", "[::]:1", "gw.example.com:1", "localhost.example.com:1", "127.0.0.1"} {
		if isLoopback(addr) {
			t.Errorf("isLoopback(%q) = true", addr)
		}
	}
	// The three podman keys are each one failure; a max_ttl that does not parse
	// does not compare, so a lease_timeout above the default is left alone.
	err := mustFail(t, edited(func(c *Config) {
		*c = *remoteConfig("https://gw.example")
		c.EngineBinary, c.Runtime, c.UserNS = "/p", "krun", "keep-id"
	}))
	if n := len(failures(err)); n != 3 {
		t.Errorf("the three podman keys gave %d failures; want 3:\n%v", n, err)
	}
	err = mustFail(t, edited(func(c *Config) { c.MaxTTL = strPtr("bogus"); c.LeaseTimeout = strPtr("5h") }))
	if strings.Contains(err.Error(), "sandbox.lease_timeout") {
		t.Errorf("lease_timeout was compared with an unusable max_ttl: %v", err)
	}
}

// gateway_url is an https URL, or http to loopback, with a host, a valid port,
// no user information, query, fragment or path; no failure repeats credentials.
func TestValidate_GatewayURL(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:9000", "http://localhost:9000", "https://gw.example:443", "https://gw.example", "http://[::1]:9000", "https://gw.example/", "https://gw.example:1", "https://gw.example:65535"} {
		if _, err := Validate(remoteConfig(u)); err != nil {
			t.Errorf("Validate(%q) returned error: %v", u, err)
		}
	}
	for u, rule := range map[string]string{
		"":                               "required",
		"http://gw.example:9000":         "plaintext",
		"ftp://127.0.0.1":                "scheme",
		"gw.example:9000":                "scheme",
		"/just/a/path":                   "scheme",
		"https:gw.example":               "scheme://host",
		"http://0.0.0.0:9000":            "plaintext",
		"://nohost":                      "not a valid URL",
		"https://gw.example:0":           "port",
		"https://gw.example:65536":       "port",
		"https://gw.example:abc":         "port",
		"https://gw.example:":            "port",
		"https://:443":                   "name a host",
		"https://gw.example/api":         "path",
		"https://gw.example?x=1":         "query",
		"https://gw.example#f":           "fragment",
		"https://u:p@gw.example":         "user information",
		"http://u:p@gw.example:9000":     "plaintext",
		"https://u:p@gw.example/api?x=1": "user information",
		"https://u:p@gw.example:99999":   "port",
	} {
		err := mustFail(t, remoteConfig(u))
		requireFailure(t, err, "sandbox.gateway_url", rule)
		if strings.Contains(err.Error(), "u:p") || strings.Contains(err.Error(), "p@") {
			t.Errorf("Validate(%q) repeats the user information: %v", u, err)
		}
	}
	// The plaintext failure quotes the scheme and the host and no more.
	requireFailure(t, mustFail(t, remoteConfig("http://u:p@gw.example:9000")), `"http://gw.example:9000"`)
}

// gateway_token is required, at least 16 printable ASCII characters without
// spaces, and no failure repeats any part of it.
func TestValidate_GatewayToken(t *testing.T) {
	tokenConfig := func(token string) *Config {
		cfg := remoteConfig("https://gw.example")
		cfg.GatewayToken = token
		return cfg
	}
	var all strings.Builder
	for c := byte(0x21); c <= 0x7E; c++ {
		all.WriteByte(c)
	}
	for _, ok := range []string{strings.Repeat("a", 16), all.String(), strings.Repeat("!", 16), strings.Repeat("~", 16)} {
		if _, err := Validate(tokenConfig(ok)); err != nil {
			t.Errorf("the token %q was refused: %v", ok, err)
		}
	}
	const sixteen = "abcdefghijklmnop"
	for name, tc := range map[string]struct{ token, rule string }{
		"empty":     {"", "required"},
		"15 chars":  {strings.Repeat("a", 15), "16"},
		"newline":   {sixteen + "\n", "printable ASCII without spaces"},
		"space":     {"abcdefgh ijklmnop", "printable ASCII"},
		"tab":       {"abcdefgh\tijklmnop", "printable ASCII"},
		"DEL":       {"abcdefgh\x7Fijklmnop", "printable ASCII"},
		"non-ASCII": {"abcdefgh\u00e9ijklmnop", "printable ASCII"},
		"NUL":       {"abcdefgh\x00ijklmnop", "printable ASCII"},
	} {
		cfg := tokenConfig(tc.token)
		cfg.GatewayURL = "https://user:" + tc.token + "@gw.example" // whatever else is wrong, the token stays out
		err := mustFail(t, cfg)
		requireFailure(t, err, "sandbox.gateway_token", tc.rule)
		for i := 0; i+8 <= len(tc.token); i++ {
			if strings.Contains(err.Error(), tc.token[i:i+8]) {
				t.Errorf("%s: the error holds part of the token: %v", name, err)
			}
		}
	}
}

// Profiles: at least one, valid unique names, one default, an image, whole
// non-negative resources, a network of network_modes, a ttl under max_ttl,
// valid non-repeating secret names and env keys that collide with no secret.
func TestValidate_Profiles(t *testing.T) {
	requireFailure(t, mustFail(t, configWith()), "sandbox.profiles", "at least one profile")

	long64 := strings.Repeat("a", 64)
	err := mustFail(t, edited(func(c *Config) {
		c.MaxTTL = strPtr("1h")
		c.NetworkModes = []string{"none", "unrestricted"}
		c.Profiles = []ProfileConfig{
			profileWith("", func(p *ProfileConfig) { p.Image = "registry.example/unnamed:1" }),
			validProfile("Upper"), validProfile("-lead"), validProfile(long64),
			profileWith("dev", func(p *ProfileConfig) { p.Default = true }),
			profileWith("dev", func(p *ProfileConfig) { p.Default = true; p.Image = "" }),
			profileWith("neg-cpus", func(p *ProfileConfig) { p.CPUs = -1 }),
			profileWith("frac-pids", func(p *ProfileConfig) { p.PIDs = 1.5 }),
			profileWith("nan-cpus", func(p *ProfileConfig) { p.CPUs = math.NaN() }),
			profileWith("inf-memory", func(p *ProfileConfig) { p.MemoryMB = math.Inf(1) }),
			profileWith("neg-disk", func(p *ProfileConfig) { p.DiskMB = -1 }),
			profileWith("web", func(p *ProfileConfig) { p.Network = strPtr("bridge") }),
			profileWith("blank", func(p *ProfileConfig) { p.Network = strPtr("") }),
			profileWith("nottl", func(p *ProfileConfig) { p.TTL = "" }),
			profileWith("badttl", func(p *ProfileConfig) { p.TTL = "abc" }),
			profileWith("zerottl", func(p *ProfileConfig) { p.TTL = "0s" }),
			profileWith("over", func(p *ProfileConfig) { p.TTL = "61m" }),
			profileWith("badsecret", func(p *ProfileConfig) { p.Secrets = []string{"OK", "1BAD"}; p.OptionalSecrets = []string{"has-dash"} }),
			profileWith("within", func(p *ProfileConfig) { p.Secrets = []string{"API_KEY", "api_key"} }),
			profileWith("across", func(p *ProfileConfig) { p.Secrets = []string{"TOKEN"}; p.OptionalSecrets = []string{"token"} }),
			profileWith("badenv", func(p *ProfileConfig) {
				p.Env = map[string]string{"BAD-KEY": "supersecretvalue", "GOOD_KEY": "goodsecretvalue"}
			}),
			profileWith("clash", func(p *ProfileConfig) {
				p.Secrets = []string{"GIT_PAT"}
				p.Env = map[string]string{"git_pat": "leakme"}
			}),
			profileWith("optclash", func(p *ProfileConfig) { p.OptionalSecrets = []string{"X"}; p.Env = map[string]string{"x": "leakme"} }),
			profileWith("fine", func(p *ProfileConfig) {
				p.Network, p.TTL, p.CPUs = strPtr("unrestricted"), "1h", 0.5
				p.Secrets, p.OptionalSecrets, p.Env = []string{"GIT_PAT"}, []string{"GH_TOKEN"}, map[string]string{"LANG": "C", "_PRIVATE": "x"}
			}),
		}
	}))
	for _, want := range [][]string{
		{"sandbox.profiles[0]", "name is required"}, {`"Upper"`, "name must match"}, {`"-lead"`, "name"}, {long64, "name"},
		{`"dev"`, "name is already used by sandbox.profiles[4]"}, {"default", `"dev", "dev"`, "at most one"}, {`"dev"`, "image is required"},
		{`"neg-cpus"`, "cpus"}, {`"frac-pids"`, "pids"}, {`"nan-cpus"`, "cpus"}, {`"inf-memory"`, "memory_mb"}, {`"neg-disk"`, "disk_mb"},
		{`"web"`, "network", `"bridge"`}, {`"blank"`, "network"},
		{`"nottl"`, "ttl is required"}, {`"badttl"`, "ttl"}, {`"zerottl"`, "ttl", "greater than zero"}, {`"over"`, "ttl", "sandbox.max_ttl"},
		{`"badsecret"`, "secrets[1]", "1BAD"}, {`"badsecret"`, "optional_secrets[0]", "has-dash"},
		{`"within"`, `secrets[1] "api_key"`, "repeats"}, {`"across"`, `optional_secrets[0] "token"`, "repeats"},
		{`"badenv"`, "env", "BAD-KEY"}, {`"clash"`, "env key", `"git_pat"`}, {`"optclash"`, "env key", `"x"`},
	} {
		requireFailure(t, err, want...)
	}
	for _, leak := range []string{`"fine"`, "supersecretvalue", "goodsecretvalue", "leakme", "GOOD_KEY", "sandbox.profiles[5]: name is already", ": secrets[0]"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the error holds %q:\n%v", leak, err)
		}
	}

	v := requireValid(t, edited(func(c *Config) {
		c.NetworkModes = []string{"none", "unrestricted"}
		c.Profiles = []ProfileConfig{
			validProfile(strings.Repeat("a", 63)), validProfile("a_b-c1"), validProfile("0lead"),
			profileWith("sized", func(p *ProfileConfig) {
				p.Default, p.RequireLandlock = true, true
				p.CPUs, p.MemoryMB, p.PIDs, p.DiskMB = 1.5, 512, 100, 2048
				p.Network, p.TTL = strPtr("unrestricted"), "90s"
				p.Secrets, p.OptionalSecrets, p.Env = []string{"GIT_PAT"}, []string{"GH_TOKEN"}, map[string]string{"LANG": "C"}
			}),
		}
	}))
	plain, sized := v.Profiles[0], v.Profiles[3]
	if plain.Resources != (Resources{}) || plain.Network != "none" || plain.TTL != time.Hour || plain.Default || plain.RequireLandlock {
		t.Errorf("plain profile = %+v; want zero resources, network none, 1h, no default, no landlock", plain)
	}
	if sized.Resources != (Resources{CPUs: 1.5, MemoryMB: 512, PIDs: 100, DiskMB: 2048}) || sized.Network != "unrestricted" || sized.TTL != 90*time.Second ||
		!sized.Default || !sized.RequireLandlock || !slices.Equal(sized.Secrets, []string{"GIT_PAT"}) || !slices.Equal(sized.OptionalSecrets, []string{"GH_TOKEN"}) || sized.Env["LANG"] != "C" {
		t.Errorf("sized profile = %+v; want the configured values", sized)
	}
	// A ttl equal to max_ttl passes; with an invalid max_ttl it is not compared.
	requireValid(t, edited(func(c *Config) { c.MaxTTL = strPtr("1h"); c.Profiles[0].TTL = "1h" }))
	err = mustFail(t, edited(func(c *Config) { c.MaxTTL = strPtr("soon"); c.Profiles[0].TTL = "24h" }))
	if strings.Contains(err.Error(), `"dev"`) {
		t.Errorf("a ttl was compared with an invalid max_ttl: %v", err)
	}
}

// Every static fault of a section is reported in one joined error.
func TestValidate_JoinsEveryFailure(t *testing.T) {
	err := mustFail(t, edited(func(c *Config) {
		c.Provider, c.MaxTTL, c.NetworkModes, c.Listen = "kubernetes", strPtr("soon"), []string{"none", ""}, "nonsense"
		c.Profiles = append(c.Profiles, validProfile("Bad_Name"), profileWith("noimage", func(p *ProfileConfig) { p.Image = "" }))
	}))
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok || len(joined.Unwrap()) != 6 {
		t.Fatalf("error %T joins %d failures; want 6:\n%v", err, len(failures(err)), err)
	}
	for _, want := range []string{"sandbox.provider", "sandbox.max_ttl", "sandbox.network_modes[1]", "sandbox.listen", `"Bad_Name"`, `"noimage"`} {
		requireFailure(t, err, want)
	}
}
