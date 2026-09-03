# Agents

## Running with Podman

Start the container in the background:

```bash
podman run -d --name agents quay.io/agentfox/agents:latest sleep infinity
```

Enter the running container with an interactive shell to run the agents (`nightshift`, `agent-fox`, `pi`, `opencode`, `claude`, ...):

```bash
podman exec -it agents bash
```

When you're done, stop and remove the container:

```bash
podman rm -f agents
```

## Providing an API key

Pass credentials such as the Anthropic API key via `-e` (or `--env-file` for
multiple secrets). Agents that read `ANTHROPIC_API_KEY` from the environment
(e.g. `claude`) will pick it up automatically:

```bash
podman run -d --name agents \
  -e ANTHROPIC_API_KEY="$ANTHROPIC_API_KEY" \
  quay.io/agentfox/agents:latest sleep infinity
```

## Mounting a local workspace

The container's working directory is `/opt/app-root/workspace` (`$WORKSPACE`),
separate from `$HOME` (`/opt/app-root/src`) where agent config, credentials,
and session state are stored. Bind-mount a local project directory onto
`$WORKSPACE` so the agents operate on your code without mixing agent state
into it:

```bash
podman run -d --name agents \
  -e ANTHROPIC_API_KEY="$ANTHROPIC_API_KEY" \
  -v "$(pwd)":/opt/app-root/workspace:Z \
  quay.io/agentfox/agents:latest sleep infinity
```

Then enter the container and run an agent against the mounted workspace:

```bash
podman exec -it agents bash
cd /opt/app-root/workspace
claude
```

Note: the `:Z` suffix relabels the volume for SELinux (needed on Fedora/RHEL
hosts); omit it on systems without SELinux.

