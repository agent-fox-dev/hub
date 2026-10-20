package sandboxes

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/agent-fox-dev/hub/internal/secrets"
)

// Defaults for the keys an operator may leave out.
const (
	defaultEngineBinary = "podman"
	defaultMaxTTL       = 4 * time.Hour
	defaultNetwork      = "none"
	defaultMaxSandboxes = 16
)

// defaultNetworkModes returns network_modes when the key is absent: a sandbox
// has no network unless the operator allows one.
func defaultNetworkModes() []string { return []string{defaultNetwork} }

// providerPodman is the only provider this build accepts; providerGateway is
// recognised only to refuse it with a clear message.
const (
	providerPodman  = "podman"
	providerGateway = "gateway"
)

// profileNameRE is the shape of a profile name.
var profileNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// Validated is the sandbox definition after validation: the effective
// deployment values, with defaults applied, and the validated profiles in
// file order.
type Validated struct {
	Provider     string
	EngineBinary string
	Runtime      string
	UserNS       string
	MaxTTL       time.Duration
	NetworkModes []string
	// MaxSandboxes is the effective limit on live leases.
	MaxSandboxes int
	Profiles     []ValidatedProfile
}

// Resources are a profile's resource bounds. Zero means the provider's
// default.
type Resources struct {
	CPUs     float64
	MemoryMB int64
	PIDs     int64
	DiskMB   int64
}

// ValidatedProfile is a profile that passed validation.
type ValidatedProfile struct {
	Name            string
	Default         bool
	Image           string
	Resources       Resources
	Network         string
	TTL             time.Duration
	Secrets         []string
	OptionalSecrets []string
	Env             map[string]string
	RequireLandlock bool
}

// collector gathers validation failures so that they can be reported
// together.
type collector struct{ errs []error }

func (c *collector) addf(format string, args ...any) {
	c.errs = append(c.errs, fmt.Errorf(format, args...))
}

// Validate checks cfg against every static rule and returns the effective
// definition. Every failure is collected and returned as one joined error, one
// failure per line, each naming the key or the profile and the field. The
// error never contains a value of an env key.
func Validate(cfg *Config) (*Validated, error) {
	if cfg == nil {
		return nil, errors.New("sandbox: there is no [sandbox] section to validate")
	}

	var c collector
	v := &Validated{
		Provider:     cfg.Provider,
		EngineBinary: cfg.EngineBinary,
		Runtime:      cfg.Runtime,
		UserNS:       cfg.UserNS,
		MaxTTL:       defaultMaxTTL,
		NetworkModes: defaultNetworkModes(),
		MaxSandboxes: defaultMaxSandboxes,
	}
	if v.EngineBinary == "" {
		v.EngineBinary = defaultEngineBinary
	}

	validateProvider(&c, cfg)
	maxTTLValid := validateMaxTTL(&c, cfg, v)
	validateNetworkModes(&c, cfg, v)
	validateMaxSandboxes(&c, cfg, v)
	validateProfiles(&c, cfg, v, maxTTLValid)

	if len(c.errs) > 0 {
		return nil, errors.Join(c.errs...)
	}
	return v, nil
}

// validateProvider checks provider.
func validateProvider(c *collector, cfg *Config) {
	switch cfg.Provider {
	case providerPodman:
	case "":
		c.addf("sandbox.provider is required (accepted values: %q)", providerPodman)
	case providerGateway:
		c.addf("sandbox.provider %q: the gateway provider is not supported by this build (accepted values: %q)", cfg.Provider, providerPodman)
	default:
		c.addf("sandbox.provider %q is not supported (accepted values: %q)", cfg.Provider, providerPodman)
	}
}

// validateMaxTTL checks max_ttl and stores the effective value in v. It
// reports whether the effective value is one the operator can be held to:
// false when the configured value is invalid.
func validateMaxTTL(c *collector, cfg *Config, v *Validated) bool {
	if cfg.MaxTTL == nil {
		return true
	}
	d, err := time.ParseDuration(*cfg.MaxTTL)
	switch {
	case err != nil:
		c.addf("sandbox.max_ttl: %v", err)
		return false
	case d <= 0:
		c.addf("sandbox.max_ttl %q must be greater than zero", *cfg.MaxTTL)
		return false
	}
	v.MaxTTL = d
	return true
}

// validateMaxSandboxes checks max_sandboxes, a whole number of at least 1, and
// stores the effective limit in v.
func validateMaxSandboxes(c *collector, cfg *Config, v *Validated) {
	if cfg.MaxSandboxes == nil {
		return
	}
	n, ok := wholeNumber(*cfg.MaxSandboxes)
	if !ok || n < 1 {
		c.addf("sandbox.max_sandboxes must be a whole number of 1 or more (got %v)", *cfg.MaxSandboxes)
		return
	}
	v.MaxSandboxes = int(n)
}

// validateNetworkModes checks network_modes and stores the effective list in
// v. Absent means the default; an empty list is kept as it is.
func validateNetworkModes(c *collector, cfg *Config, v *Validated) {
	if cfg.NetworkModes == nil {
		return
	}
	first := map[string]int{}
	for i, mode := range cfg.NetworkModes {
		if mode == "" {
			c.addf("sandbox.network_modes[%d] must not be empty", i)
			continue
		}
		if j, seen := first[mode]; seen {
			c.addf("sandbox.network_modes[%d] %q repeats sandbox.network_modes[%d]", i, mode, j)
			continue
		}
		first[mode] = i
	}
	v.NetworkModes = slices.Clone(cfg.NetworkModes)
}

// profileLabel returns how a failure names profile i: by its name, or by its
// index when it has none. The short form is the bare name or index.
func profileLabel(i int, p ProfileConfig) (label, short string) {
	if p.Name == "" {
		short = fmt.Sprintf("sandbox.profiles[%d]", i)
		return short, short
	}
	short = fmt.Sprintf("%q", p.Name)
	return "profile " + short, short
}

// validateProfiles checks that there is a profile and checks each of them.
// maxTTLValid says whether a ttl can be compared with v.MaxTTL.
func validateProfiles(c *collector, cfg *Config, v *Validated, maxTTLValid bool) {
	if len(cfg.Profiles) == 0 {
		c.addf("sandbox.profiles: at least one profile is required")
		return
	}

	firstByName := map[string]int{}
	var defaults []string
	for i, p := range cfg.Profiles {
		label, short := profileLabel(i, p)

		switch {
		case p.Name == "":
			c.addf("%s: name is required", label)
		case !profileNameRE.MatchString(p.Name):
			c.addf("%s: name must match %s", label, profileNameRE)
		}
		if p.Name != "" {
			if j, seen := firstByName[p.Name]; seen {
				c.addf("%s: name is already used by sandbox.profiles[%d]", label, j)
			} else {
				firstByName[p.Name] = i
			}
		}

		if p.Default {
			defaults = append(defaults, short)
		}

		if p.Image == "" {
			c.addf("%s: image is required and must not be empty", label)
		}

		vp := ValidatedProfile{
			Name:            p.Name,
			Default:         p.Default,
			Image:           p.Image,
			Resources:       validateResources(c, label, p),
			Network:         validateNetwork(c, label, p, v),
			TTL:             validateTTL(c, label, p, v, maxTTLValid),
			Secrets:         slices.Clone(p.Secrets),
			OptionalSecrets: slices.Clone(p.OptionalSecrets),
			Env:             maps.Clone(p.Env),
			RequireLandlock: p.RequireLandlock,
		}
		validateNames(c, label, p)
		v.Profiles = append(v.Profiles, vp)
	}

	if len(defaults) > 1 {
		c.addf("default: more than one profile sets default = true (%s); at most one may", strings.Join(defaults, ", "))
	}
}

// maxWholeNumber is the largest integer a float64 holds exactly.
const maxWholeNumber = 1 << 53

// wholeNumber returns x as an integer, and false unless x is a whole number
// from 0 up to maxWholeNumber.
func wholeNumber(x float64) (int64, bool) {
	if math.IsNaN(x) || x < 0 || x > maxWholeNumber || x != math.Trunc(x) {
		return 0, false
	}
	return int64(x), true
}

// validateResources checks cpus, memory_mb, pids and disk_mb. Zero is valid
// and means the provider's default.
func validateResources(c *collector, label string, p ProfileConfig) Resources {
	var r Resources
	if math.IsNaN(p.CPUs) || math.IsInf(p.CPUs, 0) || p.CPUs < 0 {
		c.addf("%s: cpus must be a number of 0 or more (got %v)", label, p.CPUs)
	} else {
		r.CPUs = p.CPUs
	}
	for _, f := range []struct {
		field string
		value float64
		out   *int64
	}{
		{"memory_mb", p.MemoryMB, &r.MemoryMB},
		{"pids", p.PIDs, &r.PIDs},
		{"disk_mb", p.DiskMB, &r.DiskMB},
	} {
		n, ok := wholeNumber(f.value)
		if !ok {
			c.addf("%s: %s must be a whole number of 0 or more (got %v)", label, f.field, f.value)
			continue
		}
		*f.out = n
	}
	return r
}

// validateNetwork checks network, which is "none" when absent, against the
// effective network_modes, and returns the effective network.
func validateNetwork(c *collector, label string, p ProfileConfig, v *Validated) string {
	network := defaultNetwork
	if p.Network != nil {
		network = *p.Network
	}
	if !slices.Contains(v.NetworkModes, network) {
		c.addf("%s: network %q is not one of sandbox.network_modes %q", label, network, v.NetworkModes)
	}
	return network
}

// validateTTL checks that ttl is present, a positive Go duration and, when
// max_ttl is valid, at most max_ttl. It returns the parsed value, or zero.
func validateTTL(c *collector, label string, p ProfileConfig, v *Validated, maxTTLValid bool) time.Duration {
	if p.TTL == "" {
		c.addf("%s: ttl is required", label)
		return 0
	}
	d, err := time.ParseDuration(p.TTL)
	switch {
	case err != nil:
		c.addf("%s: ttl: %v", label, err)
	case d <= 0:
		c.addf("%s: ttl %q must be greater than zero", label, p.TTL)
	case maxTTLValid && d > v.MaxTTL:
		c.addf("%s: ttl %q is greater than sandbox.max_ttl (%s)", label, p.TTL, v.MaxTTL)
	default:
		return d
	}
	return 0
}

// validateNames checks the secret names and the env keys of a profile with
// secrets.ValidateKey, and that no secret name repeats and no env key equals
// a secret name, all ignoring case. Only names and keys appear in a failure,
// never an env value.
func validateNames(c *collector, label string, p ProfileConfig) {
	// first maps an upper-cased secret name to where it was first listed.
	first := map[string]string{}
	for _, list := range []struct {
		field string
		names []string
	}{
		{"secrets", p.Secrets},
		{"optional_secrets", p.OptionalSecrets},
	} {
		for i, name := range list.names {
			if err := secrets.ValidateKey(name); err != nil {
				c.addf("%s: %s[%d]: %v", label, list.field, i, err)
				continue
			}
			where := fmt.Sprintf("%s[%d] %q", list.field, i, name)
			if prior, repeated := first[strings.ToUpper(name)]; repeated {
				c.addf("%s: %s repeats %s (names are compared ignoring case)", label, where, prior)
				continue
			}
			first[strings.ToUpper(name)] = where
		}
	}

	for _, key := range slices.Sorted(maps.Keys(p.Env)) {
		if err := secrets.ValidateKey(key); err != nil {
			c.addf("%s: env: %v", label, err)
			continue
		}
		if prior, clash := first[strings.ToUpper(key)]; clash {
			c.addf("%s: env key %q has the same name as %s (names are compared ignoring case)", label, key, prior)
		}
	}
}
