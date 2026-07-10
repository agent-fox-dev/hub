# Erratum: Server Configuration Package Location

**Spec:** 01 (server_foundation)
**Affected tasks:** 1.1, 8.1, 8.2

## Divergence

The tasks.json for spec 01 specifies creating server configuration types and
the `LoadConfig` function in `internal/config/config.go`. However, that
package already exists and contains CLI client configuration types for spec 05
(`Config` with `HubURL`, `UserID`, `APIKey`, `KeyID` fields; `Load`, `Save`,
`EnsureConfigDir`, `EnsureConfigFile` functions).

## Resolution

Server configuration lives in a separate package:

- **`internal/serverconfig/`** — server-side TOML configuration (spec 01)
- **`internal/config/`** — CLI client configuration (spec 05)

The `internal/serverconfig` package provides:

- `Config`, `ServerConfig`, `DatabaseConfig`, `LogConfig`, `OAuthConfig`,
  `OAuthProvider` struct types with TOML tags
- `LoadResult` struct with `Config`, `ConfigDir`, `UnrecognizedKeys`,
  `Warnings` fields
- `LoadConfig(path string) (*LoadResult, error)` — loads and validates
  config.toml
- `StartupLogFields(cfg *Config) map[string]any` — returns the structured
  fields for the "server starting" log entry

All spec 01 test references to "internal/config/config.go" or
"internal/config/config_test.go" should be read as
`internal/serverconfig/serverconfig.go` and
`internal/serverconfig/serverconfig_test.go` respectively.

## Rationale

Both packages define a type named `Config` with completely different fields.
Merging them into a single package would create a naming conflict and violate
separation of concerns — the CLI client and server have distinct configuration
schemas, lifecycles, and file formats (JSON vs TOML).
