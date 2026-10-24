# Agents container

A container image with coding agents preinstalled: [Claude Code](https://claude.ai/code)
(`claude`), [opencode](https://opencode.ai) (`opencode`) and [pi](https://pi.dev)
(`pi`). It builds on the hub's **sandbox** image, a RHEL 10 development
environment with Go, Rust, Node.js, Python 3.14, git and common build tools.

Use it to run an agent against a project without installing the agent, its
toolchain or its configuration on your own machine. The same image can serve as
the image of a hub [sandbox profile](../../docs/configuration.md), so agents
leased through the hub run in the environment you tried locally.

## Example

Run an agent against the project in the current directory:

```bash
podman run -d --name agents \
  -e ANTHROPIC_API_KEY="$ANTHROPIC_API_KEY" \
  -v "$(pwd)":/opt/app-root/workspace:Z \
  quay.io/agentfox/agents:latest sleep infinity

podman exec -it agents bash     # opens a shell in /opt/app-root/workspace
claude                          # or opencode, pi

podman rm -f agents             # when you are done
```

- **Credentials** go in with `-e` (or `--env-file` for several). Agents that
  read their key from the environment, such as `claude` with
  `ANTHROPIC_API_KEY`, pick it up automatically.
- **Your project** is mounted at `/opt/app-root/workspace` (`$WORKSPACE`), the
  container's working directory. Agent configuration, credentials and session
  state live in `$HOME` (`/opt/app-root/src`), so they never mix with your
  code.
- **`:Z`** relabels the mount for SELinux (Fedora, RHEL). Leave it out on
  systems without SELinux.

## Build

From the repository root:

```bash
make build-agents-container   # builds the sandbox image first, then this one
```

Images are tagged `quay.io/agentfox/agents:<version>` and `:latest`; set
`CONTAINER_REGISTRY` to use another registry. The Python packages installed into
the image's virtual environment are listed in
[`requirements.txt`](requirements.txt).
