# Vault Snapshot Coordinator

Vault Snapshot Coordinator is a small Go service that runs as identical instances beside a Vault cluster and uses Consul leadership to ensure only one node performs a snapshot for each scheduled interval.

The service stays narrow on purpose. Envoy provides the trusted network path. Vault Agent provides short lived credentials. This service handles leader election, snapshot execution, artifact handling, retention, and operator visibility.

## Features

- Consul backed leader election with automatic failover
- Interval based backup scheduling aligned to wall clock boundaries
- Vault raft snapshot capture through `GET /v1/sys/storage/raft/snapshot`
- Secure local scratch handling with checksum and size metadata
- File destination uploads for an external encrypted mount or network share
- Count based and age based retention
- Structured JSON logs
- `/healthz`, `/readyz`, `/status`, and `/metrics` endpoints

## Runtime model

Every instance competes for the same Consul lock key. The leader keeps the lock alive and runs the backup loop. Non leaders stay passive.

Each backup run does this work.

1. Validates that the destination path is usable.
2. Streams a Vault snapshot into a scratch file.
3. Calculates the snapshot size and SHA256 checksum while streaming.
4. Uploads the artifact to the configured backup location.
5. Uploads a metadata sidecar file beside the artifact.
6. Applies retention after a successful upload.
7. Updates in memory status and metrics.

If leadership is lost then the active run is canceled through context propagation. If Vault, Consul, or the destination is unavailable then the node reports that state through `/status`, `/readyz`, and `/metrics`.
Permission or write failures in the destination surface during the backup run itself and are reported through backup failure status and logs.

## Configuration

All configuration is supplied through environment variables.

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `NODE_ID` | No | Hostname | Stable node identity written into leadership and metadata |
| `HTTP_BIND_ADDRESS` | No | `:8080` | Bind address for health, status, and metrics |
| `LOG_FORMAT` | No | `json` | Structured log format. Supported values: `json`, `text` |
| `LOG_LEVEL` | No | `info` | Structured log level |
| `VAULT_ADDR` | Yes | | Local Vault endpoint exposed through Envoy |
| `VAULT_TOKEN` | One of token or token file | | Static Vault token |
| `VAULT_TOKEN_FILE` | One of token or token file | | Vault Agent token sink file |
| `VAULT_CA_CERT_FILE` | No | | Absolute path to a PEM encoded CA certificate bundle used to trust the Vault server certificate |
| `VAULT_REQUEST_TIMEOUT` | No | `10m` | Timeout for snapshot and health requests |
| `CONSUL_ADDR` | Yes | | Local Consul endpoint exposed through Envoy |
| `CONSUL_HTTP_TOKEN` | No | | Static Consul ACL token |
| `CONSUL_HTTP_TOKEN_FILE` | No | | Consul ACL token file |
| `CONSUL_LOCK_KEY` | Yes | | Shared Consul coordination key |
| `CONSUL_SESSION_TTL` | No | `15s` | Session TTL used for lock ownership |
| `CONSUL_LOCK_WAIT` | No | `10s` | Long poll wait used while contending for the lock |
| `BACKUP_SCHEDULE` | Yes | | Go duration string such as `15m` or `1h` |
| `BACKUP_LOCATION` | Yes | | Absolute path to an external durable mount |
| `ARTIFACT_NAME_TEMPLATE` | No | `vault-snapshot-{{ .Timestamp }}-{{ .NodeID }}.snap` | Template for destination object names |
| `RETENTION_COUNT` | No | `7` | Number of snapshots to retain. `0` disables count pruning |
| `RETENTION_MAX_AGE` | No | | Maximum age to retain snapshots. Example `168h` |
| `SCRATCH_DIR` | No | `/tmp/vault-snapshot-coordinator` | Local temporary directory |
| `PROBE_INTERVAL` | No | `30s` | Dependency probe interval |

`BACKUP_LOCATION` is a filesystem path by design in this first implementation. Mount an encrypted network share or other off cluster durable path there.

When `VAULT_ADDR` uses HTTPS with a private CA, set `VAULT_CA_CERT_FILE` so the service can trust the Vault server certificate.

## HTTP endpoints

| Endpoint | Behavior |
| --- | --- |
| `/healthz` | Returns process health |
| `/readyz` | Returns `200` when Vault, Consul, and the backup destination path last probed successfully |
| `/status` | Returns machine readable operational state including leader status and last backup outcome |
| `/metrics` | Returns Prometheus compatible text metrics |

## Consul ACL requirements

When ACLs are enabled, the Consul token used by this service only needs the permissions required for the APIs it actually calls.

- `key:write` on the configured `CONSUL_LOCK_KEY`
- `session:write` for the Consul node where the service runs so it can create and renew the leadership session

The Consul readiness probe calls `/v1/status/leader`. HashiCorp documents that endpoint as requiring no ACL.

An example policy for a node named `node-a` and a lock key of `service/vault-backup/leader` looks like this:

```hcl
key "service/vault-backup/leader" {
  policy = "write"
}

session "node-a" {
  policy = "write"
}
```

If you scope the token more broadly, `key_prefix` and `session_prefix` rules can be used instead, but the application does not require any additional Consul ACL capabilities such as `service:write`, `node:write`, or `acl:write`.

## Build and test

```sh
make test
make build
```

## Local automation

```sh
make lint
make vulncheck
make licenses
make ci
```

## Container image

```sh
make docker-build
```

The image is built from `Dockerfile` and starts the single static binary.

## CI and release automation

- `.github/workflows/ci.yml` runs semantic-release on pushes to `main` and calls GoReleaser to publish binaries, checksums, and a multi-arch Docker release image for linux/amd64 and linux/arm64
- `.golangci.yml` defines the repository lint policy
- `.releaserc.yml` uses the `conventionalcommits` preset for semantic-release
- `.goreleaser.yml` publishes release artifacts and Docker images with GoReleaser
