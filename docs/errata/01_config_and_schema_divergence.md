# Errata: Spec 01 config format and schema divergence from architecture docs

## Configuration format divergence

**Spec 01** uses `config.toml` loaded from the current working directory as the
configuration source (TOML format, BurntSushi/toml library).

**docs/services-architecture.md §8.1** specifies `~/.af/settings.yaml` as the
configuration source with a data directory resolution chain:
`AF_DATA_DIR` env → `data_dir` in settings.yaml → default `~/.local/share/af/`.

This is a deliberate simplification for the foundation phase. The spec's
config.toml approach provides a straightforward, self-contained configuration
model suitable for initial development and single-instance deployment. Migration
to the architecture's settings.yaml model (or a hybrid approach) should be
addressed in a future spec when multi-user and multi-workspace concerns require
the XDG-style directory resolution chain.

## Database filename divergence

**Spec 01** uses `af-hub.db` as the default database filename
(`[database] path = "./data/af-hub.db"`).

**docs/services-architecture.md §8.1** specifies `af.db` under `<data_dir>/af.db`.

The spec's naming is intentional for this phase — `af-hub.db` clearly identifies
the file as belonging to the hub service, which is useful during development when
multiple components may coexist. Future specs should standardize on one name and
document the migration path.

## Schema subset divergence

**Spec 01** defines 5 tables: `users`, `workspaces`, `workspace_members`,
`api_keys`, `admin_tokens`.

**docs/services-architecture.md §8.2** defines 17+ tables with different column
schemas for shared table names (e.g., `workspaces` columns differ between spec
and architecture doc).

The spec's schema is a foundation subset. The 5 tables establish the auth and
access control layer. Future specs will extend or migrate these tables as the
full architecture is realized. Schema migrations should use `CREATE TABLE IF NOT
EXISTS` with `ALTER TABLE` for additive changes.

## Communication model divergence

**Spec 01** uses Echo HTTP on a single TCP port (default 8080) with REST
endpoints and health probes.

**docs/services-architecture.md §2.2** specifies two interfaces: a Unix domain
socket (`hub.sock`) for CLI and a TCP gRPC port (`localhost:7400`) for agent SDK.

The HTTP/REST model is intentional for the foundation phase, providing a simpler
development and testing surface. The gRPC and Unix socket interfaces should be
introduced in a future spec when the agent SDK communication layer is built.
