package sandboxes

import (
	"errors"
	"fmt"
	"log/slog"
)

// Init loads the sandbox section of the config file at path, which is the file
// apikit loaded (apikit.ConfigPath), and starts the service from it.
//
// It returns (nil, nil) when the feature is off, that is when the file is
// missing or has no [sandbox] table, after logging one info line that says so.
// The routes then answer sandboxes_disabled. Otherwise it validates the
// section, builds the provider through factory, probes it and checks every
// profile against it, and on success logs one info line with the provider and
// the profile names, and nothing else of the section. A section that cannot
// start is an error.
//
// hub calls Init after apikit.Bootstrap and before it sets up anything else,
// so that the first-boot admin token never depends on the sandbox
// configuration and a bad section stops the start before a queue, a worker or
// a route exists.
func Init(logger *slog.Logger, path string, factory ProviderFactory, opts ...StartOption) (*Service, error) {
	cfg, err := LoadConfig(path)
	var unknown *unknownKeysError
	switch {
	case errors.As(err, &unknown):
		// An unknown key does not hide the other static failures: report them
		// all, without building or probing the provider.
		_, invalid := Validate(cfg)
		return nil, fmt.Errorf("sandbox: cannot enable sandboxes with the configuration in %s:\n%w", path, errors.Join(err, invalid))
	case err != nil:
		return nil, err
	case cfg == nil:
		logger.Info("sandboxes are disabled: the configuration has no [sandbox] section")
		return nil, nil
	}

	svc, err := Start(cfg, factory, opts...)
	if err != nil {
		return nil, fmt.Errorf("sandbox: cannot enable sandboxes with the configuration in %s:\n%w", path, err)
	}

	names := make([]string, 0, len(svc.profiles))
	for _, p := range svc.profiles {
		names = append(names, p.Name)
	}
	logger.Info("sandboxes are enabled", "provider", cfg.Provider, "profiles", names)
	return svc, nil
}
