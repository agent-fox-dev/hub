package sandboxes

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Defaults for the keys an operator may leave out.
const (
	defaultEngineBinary = "podman"
	defaultMaxTTL       = 4 * time.Hour
)

// defaultNetworkModes returns network_modes when the key is absent: a sandbox
// has no network unless the operator allows one.
func defaultNetworkModes() []string { return []string{"none"} }

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
	}
	if v.EngineBinary == "" {
		v.EngineBinary = defaultEngineBinary
	}

	validateProvider(&c, cfg)
	validateMaxTTL(&c, cfg, v)
	validateNetworkModes(&c, cfg, v)
	validateProfiles(&c, cfg, v)

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

// validateMaxTTL checks max_ttl and stores the effective value in v.
func validateMaxTTL(c *collector, cfg *Config, v *Validated) {
	if cfg.MaxTTL == nil {
		return
	}
	d, err := time.ParseDuration(*cfg.MaxTTL)
	switch {
	case err != nil:
		c.addf("sandbox.max_ttl: %v", err)
	case d <= 0:
		c.addf("sandbox.max_ttl %q must be greater than zero", *cfg.MaxTTL)
	default:
		v.MaxTTL = d
	}
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
func validateProfiles(c *collector, cfg *Config, v *Validated) {
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

		v.Profiles = append(v.Profiles, ValidatedProfile{
			Name:    p.Name,
			Default: p.Default,
			Image:   p.Image,
		})
	}

	if len(defaults) > 1 {
		c.addf("default: more than one profile sets default = true (%s); at most one may", strings.Join(defaults, ", "))
	}
}
