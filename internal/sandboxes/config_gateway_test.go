package sandboxes

import (
	"strings"
	"testing"
)

// gatewayConfig returns a configuration that passes validation, changed by edit.
func gatewayConfig(edit func(*Config)) *Config {
	cfg := validConfig()
	edit(cfg)
	return cfg
}

// TS-27-1: A [sandbox] section without the four gateway keys validates with the
// default listen address; the keys are known, are expanded like every other
// string, and their failures are reported with the failures of the rest of the
// section.
// Requirement: 27-REQ-1.1
func TestGatewayKeys_DefaultKnownExpandedAndJoined_TS_27_1(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		v := requireValid(t, validConfig())
		if v.Listen != "127.0.0.1:8443" {
			t.Errorf("Listen = %q; want the default 127.0.0.1:8443", v.Listen)
		}
		if v.TLSCert != "" || v.TLSKey != "" || v.ExternalURL != "" {
			t.Errorf("tls_cert, tls_key and external_url = %q, %q, %q; want all empty", v.TLSCert, v.TLSKey, v.ExternalURL)
		}
	})

	t.Run("the keys are known and ${NAME} is expanded", func(t *testing.T) {
		t.Setenv("GWHOST", "127.0.0.1")
		t.Setenv("GWDIR", "/etc/hub")
		t.Setenv("GWNAME", "gw.example.com")
		path := writeConfigFile(t, t.TempDir(), `[sandbox]
provider     = "podman"
listen       = "${GWHOST}:0"
tls_cert     = "${GWDIR}/gw.crt"
tls_key      = "${GWDIR}/gw.key"
external_url = "https://${GWNAME}"

[[sandbox.profiles]]
name  = "dev"
image = "registry.example/dev:1"
ttl   = "1h"
`)
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("LoadConfig reported %v; want the four keys to be known", err)
		}
		v := requireValid(t, cfg)
		if v.Listen != "127.0.0.1:0" || v.TLSCert != "/etc/hub/gw.crt" || v.TLSKey != "/etc/hub/gw.key" || v.ExternalURL != "https://gw.example.com" {
			t.Errorf("validated gateway keys = %q, %q, %q, %q; want them expanded", v.Listen, v.TLSCert, v.TLSKey, v.ExternalURL)
		}
	})

	t.Run("failures are joined with the other failures", func(t *testing.T) {
		cfg := gatewayConfig(func(c *Config) {
			c.Listen = "nonsense"
			c.Profiles[0].Image = ""
		})
		_, err := Validate(cfg)
		if err == nil {
			t.Fatal("Validate accepted an invalid listen and an invalid profile")
		}
		requireFailure(t, err, "sandbox.listen")
		requireFailure(t, err, `profile "dev"`, "image is required")
	})
}

// TS-27-2: An invalid listen address is refused naming sandbox.listen, and the
// ports 0 and 65535 are accepted.
// Requirement: 27-REQ-1.2
func TestGatewayListen_Address_TS_27_2(t *testing.T) {
	for _, listen := range []string{"127.0.0.1", "127.0.0.1:http", "127.0.0.1:65536", "127.0.0.1:", "127.0.0.1:-1", "127.0.0.1:+80", "127.0.0.1:8 0"} {
		t.Run("refused "+listen, func(t *testing.T) {
			_, err := Validate(gatewayConfig(func(c *Config) { c.Listen = listen }))
			if err == nil {
				t.Fatalf("Validate accepted listen %q", listen)
			}
			requireFailure(t, err, "sandbox.listen")
		})
	}
	for _, listen := range []string{"127.0.0.1:0", "127.0.0.1:65535", "localhost:8443", "[::1]:8443"} {
		t.Run("accepted "+listen, func(t *testing.T) {
			if v := requireValid(t, gatewayConfig(func(c *Config) { c.Listen = listen })); v.Listen != listen {
				t.Errorf("Listen = %q; want %q", v.Listen, listen)
			}
		})
	}
}

// TS-27-3: Loopback is only localhost and a literal loopback IP address; nothing
// is resolved.
// Requirement: 27-REQ-1.3
func TestGatewayListen_Loopback_TS_27_3(t *testing.T) {
	for _, addr := range []string{"localhost:1", "127.0.0.1:1", "127.5.5.5:1", "[::1]:1"} {
		if !isLoopback(addr) {
			t.Errorf("isLoopback(%q) = false; want true", addr)
		}
	}
	for _, addr := range []string{":1", "0.0.0.0:1", "[::]:1", "10.0.0.1:1", "gw.example.com:1", "localhost.example.com:1", "127.0.0.1", ""} {
		if isLoopback(addr) {
			t.Errorf("isLoopback(%q) = true; want false", addr)
		}
	}
}

// TS-27-4: Setting only one of tls_cert and tls_key names the one that is
// missing.
// Requirement: 27-REQ-1.4
func TestGatewayTLS_OnlyOneKey_TS_27_4(t *testing.T) {
	_, err := Validate(gatewayConfig(func(c *Config) { c.TLSCert = "/etc/hub/gw.crt" }))
	if err == nil {
		t.Fatal("Validate accepted tls_cert without tls_key")
	}
	requireFailure(t, err, "sandbox.tls_key")

	_, err = Validate(gatewayConfig(func(c *Config) { c.TLSKey = "/etc/hub/gw.key" }))
	if err == nil {
		t.Fatal("Validate accepted tls_key without tls_cert")
	}
	requireFailure(t, err, "sandbox.tls_cert")
}

// TS-27-5: A listen address that is not on loopback needs TLS: a plaintext
// gateway is allowed only on loopback.
// Requirement: 27-REQ-1.5
func TestGatewayListen_NonLoopbackNeedsTLS_TS_27_5(t *testing.T) {
	_, err := Validate(gatewayConfig(func(c *Config) {
		c.Listen = "0.0.0.0:8443"
		c.ExternalURL = "https://gw.example.com"
	}))
	if err == nil {
		t.Fatal("Validate accepted a plaintext gateway off loopback")
	}
	requireFailure(t, err, "sandbox.listen", "loopback")
}

// TS-27-6: A listen address that is not on loopback needs external_url.
// Requirement: 27-REQ-1.6
func TestGatewayListen_NonLoopbackNeedsExternalURL_TS_27_6(t *testing.T) {
	withTLS := func(c *Config) {
		c.Listen = "0.0.0.0:8443"
		c.TLSCert = "/etc/hub/gw.crt"
		c.TLSKey = "/etc/hub/gw.key"
	}
	_, err := Validate(gatewayConfig(withTLS))
	if err == nil {
		t.Fatal("Validate accepted a public gateway without external_url")
	}
	requireFailure(t, err, "sandbox.external_url")

	v := requireValid(t, gatewayConfig(func(c *Config) {
		withTLS(c)
		c.ExternalURL = "https://gw.example.com"
	}))
	if v.Listen != "0.0.0.0:8443" || v.ExternalURL != "https://gw.example.com" {
		t.Errorf("validated listen and external_url = %q, %q", v.Listen, v.ExternalURL)
	}
}

// TS-27-7: A malformed external_url is refused naming sandbox.external_url, and
// a plain URL, with or without a root path, validates. The user information of
// a refused URL is not repeated in the failure.
// Requirement: 27-REQ-1.7
func TestGatewayExternalURL_Shape_TS_27_7(t *testing.T) {
	for _, url := range []string{
		"gw.example.com", "ftp://gw", "https://u:p@gw", "https://gw?x=1", "https://gw#f", "https://gw/api",
		"https://", "https:///x", "https://gw/?", "https://gw/#", "mailto:ops@example.com",
	} {
		t.Run("refused "+url, func(t *testing.T) {
			_, err := Validate(gatewayConfig(func(c *Config) { c.ExternalURL = url }))
			if err == nil {
				t.Fatalf("Validate accepted external_url %q", url)
			}
			requireFailure(t, err, "sandbox.external_url")
			if strings.Contains(err.Error(), "u:p") {
				t.Errorf("the failure repeats the user information of the URL:\n%v", err)
			}
		})
	}
	for _, url := range []string{"https://gw.example.com/", "https://gw.example.com", "http://gw.example.com", "https://gw.example.com:8443"} {
		t.Run("accepted "+url, func(t *testing.T) {
			if v := requireValid(t, gatewayConfig(func(c *Config) { c.ExternalURL = url })); v.ExternalURL != url {
				t.Errorf("ExternalURL = %q; want %q", v.ExternalURL, url)
			}
		})
	}
}
