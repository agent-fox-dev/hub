# sandbox-lease: from a lease to a running sandbox

A small Go program that takes the lease `afc sandbox create` prints, creates the
sandbox the lease allows, runs commands in it and closes it again. Read it as the
missing half of `afc sandbox create`: `afc` gets you the lease, this program shows
what you do with it.

## Why

A hub's sandboxes are meant for code that should not run on the machine that
starts it: an agent, a test suite, a build. The operator decides what such a
sandbox may be (its image, network, memory, lifetime and the secrets it gets) in
a **profile**. A caller does not get a container; it gets a **lease**: the right
to one sandbox of one profile, for a limited time, with a token that works for
that sandbox alone.

The split keeps the dangerous parts on the hub's side:

- **The caller never holds a secret.** The hub puts the profile's secrets
  (an API key, a forge token) into the sandbox's environment itself. The caller
  holds only the lease token, which opens this one sandbox and stops working
  when the lease ends.
- **The caller cannot widen the sandbox.** Whatever image, network or
  environment the caller asks for, the hub builds the sandbox from the lease.
- **Nothing is left behind.** The sandbox ends when its holder closes it, when
  the lease is ended with `afc sandbox destroy`, or when its time runs out,
  whichever comes first.

Issuing a lease starts nothing. The holder dials the hub's **sandbox gateway**
(the lease's `gateway_url`) with the lease token, creates the sandbox there and
then works in it through AgentKit, the same way it would work in a sandbox on its
own machine. This program does exactly that, in five steps:

1. **Read the lease** and its token, and check that the lease can still create
   its sandbox.
2. **Dial the gateway** at the lease's `gateway_url` with the token. A token of a
   lease that has ended is refused here.
3. **Create the sandbox**, optionally copying a directory of yours into its
   workspace. The lease becomes `active`.
4. **Run commands** in the sandbox's workspace and show their output; optionally
   copy the workspace back out.
5. **Close the sandbox.** The hub destroys it and the lease ends as `destroyed`,
   with `end_reason` `client`. Ctrl-C closes it too.

## Example

Issue a lease, keeping the token out of the terminal, and hand both to the
program:

```sh
afc sandbox create demo --purpose "try the example" --token-file lease.token > lease.json
go run ./examples/sandbox-lease --lease lease.json --token-file lease.token
```

```
lease sbx_0f9381b1a8ac8cfcf60a968e8dbdeb3f: workspace demo, profile demo, image localhost/agentkit-sandbox:test, network none
  create the sandbox before 09:24:09; it lives until 09:39:09
dialled http://127.0.0.1:8443: gateway (devel), protocol 1.0, backend docker
sandbox 04189dc128cc58ba created: agent (devel), workspace /workspace
  labels example=sandbox-lease hub.lease=sbx_0f9381b1a8ac8cfcf60a968e8dbdeb3f hub.workspace=demo

$ id
uid=1001 gid=0(root) groups=0(root)

$ uname -srm
Linux 6.18.44-fc-v165 x86_64

$ ls -la
total 8
drwxrwxr-x 2 1001 root 4096 Oct 27 09:10 .
drwxr-xr-x 1 root root 4096 Oct 27 09:19 ..

# which of the lease's secrets are set (values are not shown)
DEMO_TOKEN: set
OPTIONAL_KEY: not set

sandbox closed; lease sbx_0f9381b1a8ac8cfcf60a968e8dbdeb3f has ended (afc sandbox get demo sbx_0f9381b1a8ac8cfcf60a968e8dbdeb3f)
```

The `hub.lease` and `hub.workspace` labels are set by the hub; the secret check
shows that the profile's secrets reached the sandbox without anyone on this side
seeing them. `afc sandbox get demo <id>` now shows the lease as `destroyed`.

Run your own work instead of the tour: copy a project in, run its tests, and
take the results back out:

```sh
afc sandbox create demo --token-file lease.token > lease.json
go run ./examples/sandbox-lease --lease lease.json --token-file lease.token \
    --dir . --run 'go test ./... > test.log 2>&1' --run 'tail -5 test.log' --out results
```

Without `--token-file`, `afc` prints the token inside the lease, and the program
reads it from there; `--lease -` reads the lease from stdin:

```sh
afc sandbox create demo | go run ./examples/sandbox-lease --lease -
```

| Flag | Meaning |
|---|---|
| `--lease` | **Required.** The lease JSON `afc sandbox create` printed; `-` reads it from stdin |
| `--token-file` | The file `afc sandbox create --token-file` wrote. Needed when the lease JSON has no token |
| `--dir` | Copy this directory into the sandbox's workspace before running anything |
| `--run` | A shell command to run in the sandbox's workspace; repeatable, run in order. Without any, the program runs a short tour |
| `--out` | Copy the sandbox's workspace into this directory before closing the sandbox |

The program exits 1 when the sandbox cannot be created or a command fails (after
running the remaining commands and closing the sandbox), and 2 on a usage error.

### When it fails

| Message | What to do |
|---|---|
| `lease ... is active` (or `destroyed`, `expired`, `failed`): `only a leased lease can create its sandbox` | A lease creates its sandbox once. Issue a new lease |
| `lease ... ran out of time` | The lease is past its `expires_at`. Issue a new lease |
| `the gateway refused the lease token` | The lease has ended or its time is up, or the token belongs to another lease. `afc sandbox get` shows the lease's state |
| `creating the sandbox: ... lease <id> has expired` | The sandbox was not created before the lease's `lease_expires_at` (5 minutes after issue by default). Issue a new lease and use it right away |
| `creating the sandbox: ... secret_missing: ...` | A secret the profile requires was deleted after the lease was issued; the lease is now `failed`. Store the secret again and issue a new lease |
| `http is accepted only for localhost or a loopback address` | The hub's gateway is on another host without TLS. The operator sets `[sandbox].tls_cert`, `tls_key` and `external_url` |
| `closing the sandbox: ...`, `the lease is still active` | The hub could not destroy the sandbox. Run the `afc sandbox destroy` command the message gives |

## Where to go next

| Document | Read it to |
|---|---|
| [CLI Reference: Sandbox Commands](../../docs/cli.md#sandbox-commands) | Issue, list, inspect and end leases with `afc` |
| [API Reference: Sandbox Lease Endpoints](../../docs/api.md#sandbox-lease-endpoints) | See the lease's fields and states, and issue leases over REST |
| [API Reference: The sandbox gateway](../../docs/api.md#the-sandbox-gateway) | Learn what the hub takes from the lease and what from the caller, and every error a create can give |
| [Server Configuration: `[sandbox]`](../../docs/configuration.md#sandbox) | Turn sandboxes on and define profiles |
| [Permissions](../../docs/permissions.md) | Choose the scopes a PAT needs (`sandboxes:write` to issue and end leases) |
| [AgentKit](https://github.com/agent-fox-dev/agentkit-go) | Go further in the sandbox: its tools, a model-driven agent, guards |

## Install and develop

You need:

- A hub with sandboxes turned on: a `[sandbox]` section and at least one
  profile in its `config.toml`, a container engine (Podman or Docker) on the hub's
  host, and the profile's image, which must contain AgentKit's sandbox agent
  (see [Server Configuration](../../docs/configuration.md#sandbox)).
  `make build-containers` in the agentkit-go checkout builds such an image with
  Podman, tagged `quay.io/agentfox/agentkit-sandbox:latest`. For a hub on your
  own machine, a minimal section is:

  ```toml
  [sandbox]
  provider = "podman"            # Podman; add engine_binary = "docker" for Docker

  [[sandbox.profiles]]
  name    = "demo"
  default = true
  image   = "quay.io/agentfox/agentkit-sandbox:latest"
  ttl     = "20m"
  ```

- A workspace you can write to, the profile's secrets stored for it (for
  example `afc secrets create GIT_PAT=... --workspace demo`), and an `afc` login
  or a PAT with `sandboxes:write`.
- The Go toolchain and the sibling checkouts the hub builds with (see the
  [README](../../README.md#install)). The example is part of the hub's Go module
  and needs nothing else.

The hub's gateway listens on loopback by default (`http://127.0.0.1:8443`), so
run the program on the hub's machine, or give the gateway TLS and an
`external_url` to reach it from elsewhere.

The tests run the program against an AgentKit gateway in the test process, with
no hub and no container engine:

```sh
go test ./examples/sandbox-lease/
```
