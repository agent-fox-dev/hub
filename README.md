# agent-fox hub

The hub is the server that AI coding agents, and the people who run them, work
through. It holds everything an agent run needs except the agent itself: the
repository, the credentials, the secrets, a sandbox to run in, and a record of
what happened and what it cost.

It ships as two programs: **`hub`**, the server, and **`afc`**, its
command-line client. Everything `afc` does is also available over a REST API.

## Why

Pointing coding agents at real repositories raises the same questions every
time:

- **Where does the agent's copy of the code live**, and how do several agents
  work on one repository without stepping on each other?
- **How do their changes get back** into the main branch without a person
  rebasing and merging every branch by hand?
- **Which credentials does an agent get?** Forge tokens and API keys copied
  into an agent's environment are hard to scope and harder to revoke.
- **Where does the agent run**, isolated from the machine it was started on?
- **What did it do, and what did it cost?**

Without a common answer, every team scripts its own: tokens end up in
environment files, branches are merged by whoever notices, and the trail of
what an agent did is scattered across terminals.

## How the hub solves it

The hub answers all of these in one service, behind one set of scoped
credentials:

| Capability | What it gives you |
|---|---|
| **Workspaces** | Register a repository once. The hub clones it, keeps it in sync with its remote, and serves it over git, so agents clone from and push to the hub instead of the forge. |
| **Merge queue** | Submit a branch and the hub rebases it onto the target and fast-forwards, one job at a time per workspace. Conflicts are reported, never half-applied. |
| **Carry-patch forks** | Keep a fork as an ordered list of patch branches on top of an upstream. When upstream moves, the hub rebuilds the integration branch, detects patches merged upstream and remembers conflict resolutions. |
| **Secrets and variables** | Store them per user, organization or workspace. Secret values are written, never read back. |
| **Scoped access** | Personal access tokens carry read/write scopes and can be bound to a single workspace. Members of an organization get read-only access to its workspaces. |
| **Forge access** | Read and write issues and pull requests on GitHub or GitLab through the hub, using the workspace's own credential, and receive the forge's webhooks as hub events. |
| **Sandboxes** | Lease an isolated container shaped by a profile the operator defines. The hub injects the secrets; the caller never sees them. |
| **Accounting and audit** | Agent sessions with token usage and cost, an audit log of everything that changed, a live event stream and Prometheus metrics. |

## Example

A workspace from creation to a merged, pushed branch:

```sh
# Log in (opens a browser for GitHub sign-in)
afc login --endpoint-url http://localhost:8080

# Register a repository; --wait returns once the hub has cloned it.
# The printed JSON includes hub_url, the workspace's address on the hub.
afc workspace create --slug demo --git-url https://github.com/acme/demo.git \
    --git-pat "$GITHUB_TOKEN" --wait

# Let git authenticate to the hub with your afc login, then clone from the hub
git config --global credential.http://localhost:8080.helper '!afc credential-helper'
git clone http://localhost:8080/git/acme/demo.git && cd demo

# An agent (or you) works on a branch and pushes it to the hub
git switch -c feature/greeting
# ... edit, commit ...
git push -u origin feature/greeting

# Merge it into main through the queue, then push main to GitHub
afc merge submit demo --source feature/greeting --target main --wait
afc workspace push demo main
```

The [CLI reference](docs/cli.md) covers every command; the
[carry-patch guide](docs/carry_patch_workflow.md) walks through maintaining a
fork.

## Documentation

| Document | Read it to |
|---|---|
| [CLI Reference](docs/cli.md) | Look up an `afc` command, its flags and its output |
| [API Reference](docs/api.md) | Call the REST API: endpoints, request and response bodies, errors |
| [OpenAPI description](docs/openapi.yaml) | Generate a client from the machine-readable API description |
| [Permissions](docs/permissions.md) | Choose credentials and scopes, and see who may do what |
| [Server Configuration](docs/configuration.md) | Configure and deploy the server: `config.toml`, environment, containers, Kubernetes |
| [Carry-Patch Workflow](docs/carry_patch_workflow.md) | Maintain a fork that carries patches on top of an upstream |
| [Architecture](docs/architecture.md) | Understand how the hub is built, before changing it |
| [Architecture Decision Records](docs/adr/) | Learn why the hub is built the way it is |
| [Product Requirements](docs/prd/) | See features that are proposed or being specified |
| [Go package docs](docs/generated/) | Browse the exported Go API of the public packages |
| [Agents container](containers/agents/README.md) | Run the bundled coding agents in a container |
| [carrypatch module](carrypatch/README.md) | Understand the Go module that implements carry-patch, and how a host embeds it |

## Install

### Prerequisites

- Go 1.27 or later, with cgo enabled (the audit store needs it)
- Checkouts of [apikit](https://github.com/txsvc/apikit),
  [agentkit-go](https://github.com/agent-fox-dev/agentkit-go) and
  [aflib](https://github.com/agent-fox-dev/aflib) next to this repository
  (`../apikit`, `../agentkit-go`, `../aflib`)
- A GitHub OAuth application, for signing users in
- Optional: Podman, to build the container images and to run sandboxes

### Build

```sh
make build
```

This builds the server into `bin/hub` and installs `afc` into `$GOBIN` (or
`$GOPATH/bin`).

### Run

Create a `config.toml` in the working directory:

```toml
[server]
port = 8080
external_url = "http://localhost:8080"

[database]
path = "afhub.db"

[[oauth.providers]]
name = "github"
client_id = "${GITHUB_CLIENT_ID}"
client_secret = "${GITHUB_CLIENT_SECRET}"
```

On first boot, create the admin account. The hub writes an admin token to the
file `admin_token` next to `config.toml` and exits. Store the token somewhere
safe and delete the file (the hub will not start while it exists), then start
the hub for real:

```sh
bin/hub --admin-email=admin@example.com
cat admin_token && rm admin_token   # keep this token: it is the admin credential
bin/hub
```

Check that it is up:

```sh
curl http://localhost:8080/healthz   # {"status": "ok"}
```

Then sign in with `afc login --endpoint-url http://localhost:8080`. [Server Configuration](docs/configuration.md)
covers every setting, sandboxes, the container image and the Kubernetes
manifests in [`deploy/`](deploy/).

## Develop

Read [`AGENTS.md`](AGENTS.md) (the same as [`CLAUDE.md`](CLAUDE.md)) first:
it is the working agreement for this repository, for people and coding agents
alike, and points to the project directives in
[`.specs/steering.md`](.specs/steering.md). Features are specified in
[`.specs/`](.specs/) before they are built.

```sh
make help       # list all targets
make check      # fmt, vet, lint and test: run before every commit
make test       # tests only
make doc        # regenerate docs/generated after changing an exported API
make build-containers   # build the hub, sandbox and agents images with Podman
```

Start with [Architecture](docs/architecture.md) for a map of the code.

## License

See [LICENSE](LICENSE).
