# Errata: Spec 25 — Sandbox Profiles Divergences

## Spec Expectation

Spec 25 (`25_sandbox_profiles`) described AgentKit's sandbox API, `apikit`'s
configuration and CLI helpers, and the process exit of a bootstrap run from the
source PRD (`docs/prd/01-create-and-lease-sandboxes.md`), and marked every
AgentKit symbol as unverified. The following entries record where the delivered
implementation diverges from the spec, with the code and the test of the
delivered behaviour.

## Implementation Reality

1. **The start-up probe is one `Reap` call, not one `List` call.**
   25-REQ-5.1 and TS-25-28, 29, 30, 35 and 44 probe the provider with one
   `List` call under a 10-second timeout. AgentKit's `sandbox.Provider` has only
   `Capabilities`, `Create` and `Reap`; `List` and `Destroy` exist on the
   concrete `gateway.Provider` and not on the interface `podman.New` returns.
   `Reap` is the only provider method that reaches the container engine
   without creating a sandbox; it removes only leftovers whose own expiry
   label has already passed. The probe is one `Reap` call under the same
   timeout, and the tests count `Reap` calls and read the deadline of the
   context they receive. See `probe` in `internal/sandboxes/service.go`
   (the `p.Reap(ctx)` call) and `TestService_ProbesOnceWithinTenSeconds_TS_25_28`
   in `internal/sandboxes/service_test.go`. The later specs
   `sandbox_leases`, `sandbox_expiry` and `sandbox_gateway_provider` assume
   `Provider.List` and `Provider.Destroy` in the same way and need the same
   correction.

2. **A fractional `cpus` is accepted.** 25-REQ-5.7 refuses a fractional `cpus`
   when AgentKit's CPU field is a whole number. `sandbox.Resources.CPUs` is a
   `float64`, so there is nothing to refuse and the fractional value reaches
   `sandbox.CheckSpec` unchanged. See `profileSpec` in
   `internal/sandboxes/service.go` and
   `TestService_FractionalCPUs_TS_25_34` in `internal/sandboxes/service_test.go`,
   which also fails to compile if the field stops being a float.

3. **No package-level `ConfigPath`.** The task steps and TS-25-1 call a
   `ConfigPath()` of `internal/sandboxes`. `apikit` exports `ConfigPath()` with
   the same rule (`$XDG_CONFIG_HOME/config.toml`, otherwise `config.toml` in the
   working directory), and the steering forbids re-deriving what apikit
   provides, so `cmd/af-hub` passes `apikit.ConfigPath()` to `sandboxes.Init`.
   `config.toml` and `./config.toml` are the same path. See `cmd/af-hub/main.go`
   and `TestConfigPath_TS_25_1` in `internal/sandboxes/config_test.go`, which
   exercises `apikit.ConfigPath()` with `LoadConfig`.

4. **`afc sandbox profiles` reports errors on stdout.** TS-25-51 expects the
   error message on stderr and nothing on stdout. 25-REQ-9.3 requires
   `apikit.CLIHandleError`, which writes the JSON error envelope to stdout and
   nothing to stderr, as every `afc` command does and as `docs/cli.md`
   documents. The test asserts the envelope with the hub's code and message on
   stdout, an empty stderr and apikit's exit code. apikit's client keeps the
   code and the message of an error response, not its `error_type`, so the
   envelope of the disabled `404` carries the message
   `sandboxes are not enabled on this hub` and no `error_type`. See
   `internal/cli/sandbox_cmd.go` and
   `TestSandboxProfilesCmd_ReportsHubErrors_TS_25_51` in
   `internal/cli/sandbox_cmd_test.go`.

5. **A bootstrap run does not exit 0.** 25-REQ-1.7 and TS-25-8 expect
   `af-hub --admin-email` to write the admin token and exit 0. `cmd/af-hub`
   passes the `ErrBootstrapComplete` that `apikit.Bootstrap` returns after
   writing the token to `log.Fatal`, so the process exits with status 1 and
   prints `admin token generated; save the token securely and delete the file
   before restarting`, as `docs/configuration.md` documents ("the process
   terminates with `log.Fatal`"). Changing that exit status is outside this
   spec. The delivered behaviour is the one the requirement is after: the
   sandbox section is validated after the bootstrap, so a run with an invalid
   `[sandbox]` section writes the token, ends with the same status and message
   as a run without a `[sandbox]` section, and reports no sandbox error. The
   next start, once the operator has removed `admin_token`, refuses the invalid
   section before the workspace root, the audit database or a listener exists.
   See the `sandboxes.Init` call in `cmd/af-hub/main.go` and
   `TestAfHubBinary_SandboxValidatedAfterBootstrap_TS_25_8` in
   `internal/sandboxes/main_binary_test.go`.

6. **Tests that assert absence passed before the implementation.** TS-25-54
   (afc does not import `internal/sandboxes` or AgentKit), TS-25-46 and TS-25-48
   (nothing leaks) pass against an unchanged tree or a stub because they assert
   that something is absent. TS-25-46 and TS-25-48 were shown able to fail by
   temporarily making the handler return env values and the provider name.
   See `internal/sandboxes/build_test.go` and
   `internal/sandboxes/handlers_test.go`.
