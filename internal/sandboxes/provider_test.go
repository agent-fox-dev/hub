package sandboxes

import (
	"slices"
	"strings"
	"testing"
)

// engineEnv carries the rest of the process environment through to the
// engine binary, but drops XDG_CONFIG_HOME and XDG_DATA_HOME: hub's own
// config/data isolation (e.g. `make hub-run`) must not redirect podman away
// from its real machine registry.
func TestEngineEnv_DropsXDGConfigAndDataHomeOnly(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/isolated/config")
	t.Setenv("XDG_DATA_HOME", "/isolated/data")
	t.Setenv("ENGINE_ENV_DISTINCTIVE", "kept")

	env := engineEnv()

	for _, kv := range env {
		if strings.HasPrefix(kv, "XDG_CONFIG_HOME=") || strings.HasPrefix(kv, "XDG_DATA_HOME=") {
			t.Errorf("engineEnv() = %v; want XDG_CONFIG_HOME and XDG_DATA_HOME dropped", env)
		}
	}
	if !slices.Contains(env, "ENGINE_ENV_DISTINCTIVE=kept") {
		t.Errorf("engineEnv() = %v; want other variables kept", env)
	}
}
