# Deploy and operate

## Native Linux service

Build on the Docker host:

```sh
make build
make codex-image
```

Create an unprivileged service user with Docker socket access, which is privileged host access. Paths:
- Binary: `/opt/reviewd/bin/reviewd`
- Config: `/etc/reviewd/reviewd.json`
- App key: `/etc/reviewd/github-app.pem`
- Data: `/var/lib/reviewd` (owned by service user, mode 0700)

Add to config:
```json
{
  "data_dir": "/var/lib/reviewd",
  "private_key_file": "/etc/reviewd/github-app.pem"
}
```

Store webhook secret in `/etc/reviewd/environment` (0600):
```text
REVIEWD_WEBHOOK_SECRET=<random>
```

For Codex: install Python 3 and CLI on service PATH. Add to config:
```json
"credentials": {
  "codex_account": {
    "state_file": "/var/lib/reviewd/credentials/codex/auth.json",
    ...
  }
}
```

Create login as service user:
```sh
sudo -u reviewd mkdir -p /var/lib/reviewd/credentials/codex
sudo -u reviewd CODEX_HOME=/var/lib/reviewd/credentials/codex codex -c 'cli_auth_credentials_store="file"' login --device-auth
```

An existing saved login can be referenced by absolute `state_file`. Use one managed state file per session, and do not independently refresh copies. The command, state file and directory must be service-accessible. The shipped systemd unit allows writes under `/var/lib/reviewd`, so paths under `/home` need `ProtectHome`/`ReadWritePaths` overrides. Other harnesses use the same [refresh contract](configuration.md#shared-credential-refresh) with their own command, or keep static credential `env`.

Install [reviewd.service](../deploy/reviewd.service) to `/etc/systemd/system/`, then:
```sh
sudo systemctl daemon-reload
sudo systemctl enable --now reviewd
sudo journalctl -u reviewd -f
```

Use a reverse proxy to terminate TLS and forward `/webhooks/github` to `http://127.0.0.1:8080`. Preserve original body and GitHub headers, and set the App's webhook URL to that public HTTPS address. `/healthz` confirms HTTP liveness only. Run `reviewd doctor` under the service environment to test dependencies.

Do not use remote Docker contexts. Only one daemon may hold a data directory lock. On startup it removes orphan containers for that directory and returns interrupted jobs to the queue. Graceful shutdown stops accepting requests, cancels harnesses, removes containers, and requeues interrupted jobs. Harness containers never receive the Docker socket.

## Docker Compose

The native binary is simpler, but an image and Compose template are provided. The server still orchestrates **sibling** containers through the host daemon.

Build:
```sh
make build && make image && make codex-image
```

Choose a host directory for config, keys, binary and data (e.g., `/opt/reviewd`). Copy binary and config there. In config set:
```json
{
  "listen": "0.0.0.0:8080",
  "data_dir": "/opt/reviewd/data",
  "private_key_file": "/opt/reviewd/github-app.pem",
  "agent_binary": "/opt/reviewd/bin/reviewd"
}
```

Host and container paths must be identical for `data_dir` and `agent_binary`. Sibling harnesses resolve bind sources on the host, and the server image's internal binary path need not exist on the host.

Copy [compose.yaml](../deploy/compose.yaml) to deployment directory. Create `.env` there:
```text
REVIEWD_ROOT=/opt/reviewd
REVIEWD_UID=1000
REVIEWD_GID=1000
DOCKER_GID=999
REVIEWD_WEBHOOK_SECRET=<random>
```

For Codex, keep the login at `/opt/reviewd/credentials/codex/auth.json` and point `state_file` there. It is covered by the deployment-directory mount. The server image contains Python and Codex, so ensure the service UID can write the login directory and adjacent cache. Only the server accesses this directory, not sibling review containers.

Use numeric UID/GID that owns the deployment directory and socket's GID (`stat -c %g /var/run/docker.sock`). Run `docker compose up -d`. Keep the bind-published listener on loopback and use the same TLS proxy setup as native deployment.

## Egress proxy deployment

The [egress](configuration.md#egress-proxy-and-credential-isolation) feature keeps provider credentials out of harness containers. Each egress run creates one internal Docker network, starts a per-job proxy container on it, and only then starts the harness with sentinels instead of credentials. The proxy is dual-homed: it joins the job's internal network (where it answers as `reviewd-egress`) and the configured upstream `egress.network` (where it reaches providers). The harness has no route except through the proxy, and the daemon removes the harness container, proxy container, and internal network on every exit path; a restart also clears orphans via the owner labels.

Generate a dedicated single-purpose CA for the proxy. The bundle holds the certificate and the private key the proxy needs to mint per-host certificates:

```sh
openssl req -x509 -newkey rsa:3072 -keyout egress-ca-key.pem -out egress-ca-cert.pem -days 825 -nodes -subj '/CN=reviewd-egress'
cat egress-ca-cert.pem egress-ca-key.pem > egress-ca.pem
install -m 0600 -o reviewd -g reviewd egress-ca.pem /etc/reviewd/egress-ca.pem
```

Protect this file like a credential: whoever holds the key can mint certificates your harnesses trust. Never reuse an organizational CA. Point `ca_file` at it and list provider hosts in the proxy `--allow` arguments. The same bundle is mounted read-only into the harness at `/review/egress-ca.pem` for TLS verification.

Build the proxy image on the Docker host:

```sh
make egress-image
```

To bake the CA into the shipped Codex harness image instead of trusting it at runtime, copy the bundle into the build context (`*.pem` is gitignored) and pass it as a build argument:

```sh
cp /etc/reviewd/egress-ca.pem ./egress-ca.pem
docker build --target codex --build-arg EGRESS_CA_FILE=./egress-ca.pem -t reviewd-codex:local .
rm ./egress-ca.pem
```

Without the argument the image builds unchanged. Custom harness images can use the runtime equivalent from the configuration reference.

The proxy owns OAuth refresh: when a credential nears expiry it re-runs `reviewd credential env` with the mounted config and credential state directories, so refresh commands execute inside the proxy container, not on the daemon host. Keep refresh commands dependency-free (`sh`, plus Python 3 which the image provides) or derive a custom proxy image that adds the needed CLIs:

```dockerfile
FROM reviewd-egress:local
RUN apt-get update && apt-get install -y --no-install-recommends nodejs && rm -rf /var/lib/apt/lists/*
COPY provider-cli /usr/local/bin/provider-cli
```

Native service notes: keep `ca_file`, the config, and credential state under `/etc/reviewd` and `/var/lib/reviewd` where the unit already allows access, and ensure the service user can read the CA bundle. Compose notes: keep them under `${REVIEWD_ROOT}` so the deployment-directory mount covers them, and use host-absolute paths that are identical inside the server container, following the same rule as `data_dir` and `agent_binary`. The daemon bind-mounts the config file, the CA, and state directories into sibling proxy containers, so those paths must resolve on the Docker host.

Verify after enabling `egress` on a harness:

```sh
./bin/reviewd config check --config /etc/reviewd/reviewd.json
./bin/reviewd doctor --config /etc/reviewd/reviewd.json
```

`doctor` checks the proxy image, its HEALTHCHECK, the CA file, a `reviewd-*` upstream network, and credential resolution. Then trigger a test review and confirm the job succeeds, `harness.log` contains only `reviewd-sentinel-*` values, and no `reviewd-job-*` networks or `reviewd-egress-*` containers remain (`docker network ls`, `docker ps -a`). As a leak drill, submit a test PR whose diff instructs the harness to print its credential: the job must fail with `egress sentinel found in harness output; report withheld`, no review may be published, and the real credential must appear nowhere in the run directory, logs, or GitHub.

Rollback is configuration-only: set `egress: false` (or remove the flag and restore the harness `network`), restart the daemon, and harnesses return to direct provider access with real credentials in their environment. Non-egress harnesses are behaviorally unchanged while others use the proxy.

## Observe and recover

Jobs have states: `queued`, `running`, `done`, `failed`, `superseded`. CLI needs same config path and identity:
```sh
reviewd jobs list --config /etc/reviewd/reviewd.json
reviewd jobs show JOB_ID --config /etc/reviewd/reviewd.json
reviewd jobs report JOB_ID --config /etc/reviewd/reviewd.json
reviewd jobs retry JOB_ID --config /etc/reviewd/reviewd.json
```

A failed harness never produces a clean review. All reviewers must succeed and submit valid reports, and then the validator must do the same (with parallelism 1 the single reviewer is the final report). Reviews retry up to three attempts. If saving completion fails, the worker retries the write until storage recovers or the service stops. GitHub errors retry on the durable job schedule, including throttling. Large deployments may need lower `workers` or `parallelism` for quota limits.

Each run stores:
- `context.json`, `files.json`: original PR context and diff
- `head/`, `base/`: pinned snapshots
- `*/input/`: exact instructions and context
- `*/output/harness.log`: stdout/stderr (capped at 8 MiB per stage)
- `*/output/report.json`: submitted stage reports
- `candidates.json`, `validated.json`, `report.json`: merged, validated, and final reports

Logs are not redacted. All artifacts are operator-private. Back up the data directory while the service is stopped, and back up configuration, environment, credential state and the App private key separately. Rotate provider credentials and restart. The App key can be replaced at its path before restart.

No automatic retention. Reclaim disk by removing `runs/JOB_ID` for terminal jobs you no longer need. Keep `jobs/JOB_ID.json` for webhook deduplication. Keep failed-job artifacts until the job is resolved, since removing them discards the final report checkpoint. Never delete queued/running artifacts.

## Security boundaries

Shared credentials are refreshed by trusted commands on the server. Containers receive only exported access credentials. Login state, refresh tokens, locks and caches stay outside review workspaces. Static `env` credentials remain supported.

Each harness has: read-only image, no Linux capabilities, no privilege escalation, fixed PID/CPU/memory limits, bounded writable workspace and `/tmp`, read-only snapshots/context, and 8 MiB tmpfs output. No writable host paths exposed. After the harness exits, the reporting CLI exports its submitted JSON over stdout and the server validates and persists it. The model-provider credential is available to that harness by design, unless the harness uses the [egress proxy](configuration.md#egress-proxy-and-credential-isolation), in which case the harness holds only single-use sentinels and the proxy container alone holds the real credential.

Processes run as the daemon's numeric UID/GID. Network `bridge` allows Internet access. Use dedicated host and restricted `reviewd-*` network where threat model requires.

Docker is not a security boundary against hostile kernel exploits. Prefer isolated host/VM for public fork reviews. Default leaves fork reviews disabled and restricts manual triggers to repository collaborators. Operator CLI is trusted and controlled by OS permissions.
