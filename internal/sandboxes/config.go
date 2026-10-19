// Package sandboxes owns hub's sandbox definition: the [sandbox] section of
// config.toml, its validation, the provider hub builds from it, the permission
// scopes and the read-only profile listing.
package sandboxes

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the decoded [sandbox] table of config.toml, before validation.
// Keys whose absence must be told apart from a zero value are pointers or
// slices that stay nil when the key is absent.
type Config struct {
	// Provider selects the provider; "podman" is the only accepted value.
	Provider string `toml:"provider"`
	// EngineBinary, Runtime and UserNS are passed to the Podman provider.
	EngineBinary string `toml:"engine_binary"`
	Runtime      string `toml:"runtime"`
	UserNS       string `toml:"userns"`
	// MaxTTL is a Go duration string; nil when absent.
	MaxTTL *string `toml:"max_ttl"`
	// NetworkModes lists the network modes profiles may use; nil when absent.
	NetworkModes []string `toml:"network_modes"`
	// Profiles are the [[sandbox.profiles]] tables in file order.
	Profiles []ProfileConfig `toml:"profiles"`
}

// ProfileConfig is one decoded [[sandbox.profiles]] table, before validation.
// The resource values are floats so that a non-integer input reaches the
// validator, which names the profile and the field, instead of failing the
// decode.
type ProfileConfig struct {
	Name            string            `toml:"name"`
	Default         bool              `toml:"default"`
	Image           string            `toml:"image"`
	CPUs            float64           `toml:"cpus"`
	MemoryMB        float64           `toml:"memory_mb"`
	PIDs            float64           `toml:"pids"`
	DiskMB          float64           `toml:"disk_mb"`
	Network         *string           `toml:"network"`
	TTL             string            `toml:"ttl"`
	Secrets         []string          `toml:"secrets"`
	OptionalSecrets []string          `toml:"optional_secrets"`
	Env             map[string]string `toml:"env"`
	RequireLandlock bool              `toml:"require_landlock"`
}

// LoadConfig reads the [sandbox] section and its [[sandbox.profiles]] tables
// from the config file at path, which is the file apikit loads (see
// apikit.ConfigPath).
//
// It returns (nil, nil) when the file does not exist or has no [sandbox]
// table: the feature is off. A file that cannot be read or decoded, and any
// key under [sandbox] that hub does not know, is an error. Keys outside the
// sandbox section belong to apikit and are ignored.
//
// Unknown keys are the one error that comes with the decoded configuration, so
// that a caller can report them together with the validation failures of the
// rest of the section (see Init); every other error comes with a nil one.
//
// ${NAME} is replaced in every string value of the section after decoding, by
// the environment variable NAME or by an empty string when it is unset. The
// rest of the file is never expanded. No error produced here contains a
// value from the file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("sandbox: reading %s: %w", path, err)
	}

	var doc struct {
		Sandbox *Config `toml:"sandbox"`
	}
	md, err := toml.Decode(string(data), &doc)
	if err != nil {
		return nil, fmt.Errorf("sandbox: decoding %s: %w", path, err)
	}
	if doc.Sandbox == nil {
		return nil, nil
	}

	expandStrings(reflect.ValueOf(doc.Sandbox).Elem())
	if err := findUnknownKeys(md, data); err != nil {
		return doc.Sandbox, err
	}
	return doc.Sandbox, nil
}

// unknownKeysError is the failure of one or more unknown keys under [sandbox],
// one wrapped error per key.
type unknownKeysError struct{ errs []error }

func (e *unknownKeysError) Error() string   { return errors.Join(e.errs...).Error() }
func (e *unknownKeysError) Unwrap() []error { return e.errs }

// findUnknownKeys returns an *unknownKeysError naming the full path of every
// key under [sandbox] that the decoder left undecoded, or nil when there is
// none.
//
// The decoder's own list has no array index, so the path of a key in a
// profile is completed from a second, untyped decode: sandbox.profiles[1].cpu
// names every profile that holds the key.
func findUnknownKeys(md toml.MetaData, data []byte) error {
	// The decoder lists a key once for every table that holds it.
	var unknown []toml.Key
	seen := map[string]bool{}
	for _, key := range md.Undecoded() {
		if len(key) > 0 && key[0] == "sandbox" && !seen[key.String()] {
			seen[key.String()] = true
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}

	var raw struct {
		Sandbox struct {
			Profiles []map[string]any `toml:"profiles"`
		} `toml:"sandbox"`
	}
	_, _ = toml.Decode(string(data), &raw) // the typed decode of the same text succeeded

	var errs []error
	for _, key := range unknown {
		if hasUnknownParent(key, unknown) {
			continue
		}
		if len(key) > 2 && key[1] == "profiles" {
			tail := key[2:]
			var found bool
			for i, profile := range raw.Sandbox.Profiles {
				if hasPath(profile, tail) {
					errs = append(errs, fmt.Errorf("sandbox: unknown key sandbox.profiles[%d].%s", i, tail))
					found = true
				}
			}
			if found {
				continue
			}
		}
		errs = append(errs, fmt.Errorf("sandbox: unknown key %s", key))
	}
	return &unknownKeysError{errs: errs}
}

// hasUnknownParent reports whether another unknown key is a proper prefix of
// key. The decoder lists the keys of an unknown table next to the table.
func hasUnknownParent(key toml.Key, unknown []toml.Key) bool {
	for _, other := range unknown {
		if len(other) < len(key) && slices.Equal(other, key[:len(other)]) {
			return true
		}
	}
	return false
}

// hasPath reports whether the nested tables in m hold the key path.
func hasPath(m map[string]any, path toml.Key) bool {
	for i, part := range path {
		v, ok := m[part]
		if !ok {
			return false
		}
		if i == len(path)-1 {
			return true
		}
		if m, ok = v.(map[string]any); !ok {
			return false
		}
	}
	return false
}

// envRef matches a ${NAME} reference.
var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnv replaces every ${NAME} in s by the value of the environment
// variable NAME, or by an empty string when it is unset. A bare $ and a
// malformed reference stay as they are, and a replacement is not expanded
// again.
func expandEnv(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return envRef.ReplaceAllStringFunc(s, func(ref string) string {
		return os.Getenv(ref[2 : len(ref)-1])
	})
}

// expandStrings expands ${NAME} in every string reachable from v: scalars,
// list entries, and the values (not the keys) of maps. Walking the value
// keeps the expansion complete for keys later specs add to Config.
func expandStrings(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			v.SetString(expandEnv(v.String()))
		}
	case reflect.Pointer:
		if !v.IsNil() {
			expandStrings(v.Elem())
		}
	case reflect.Struct:
		for i := range v.NumField() {
			expandStrings(v.Field(i))
		}
	case reflect.Slice:
		for i := range v.Len() {
			expandStrings(v.Index(i))
		}
	case reflect.Map:
		for _, key := range v.MapKeys() {
			// Map elements are not addressable: expand a copy and store it.
			elem := reflect.New(v.Type().Elem()).Elem()
			elem.Set(v.MapIndex(key))
			expandStrings(elem)
			v.SetMapIndex(key, elem)
		}
	}
}
