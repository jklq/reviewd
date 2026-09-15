# Deploy and operate

## Native Linux service

Build on the Docker host:

```sh
make build
make codex-image
```

Create an unprivileged service user with Docker socket access, which is privileged host access. Paths:
- Binaries: `/opt/reviewd/bin/reviewd` plus the credential helpers `reviewd-credential-codex` and `reviewd-credential-project`, built beside it
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

For Codex: install the Codex CLI on the service PATH; the shipped `reviewd-credential-codex` helper rotates the login through it. Add to config:
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

Choose a host directory for config, keys, binaries and data (e.g., `/opt/reviewd`). Copy the built binaries and the config there. In config set:
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

For Codex, keep the login at `/opt/reviewd/credentials/codex/auth.json` and point `state_file` there. It is covered by the deployment-directory mount. The server image contains the Codex CLI and the refresh helper, so ensure the service UID can write the login directory and adjacent cache. Only the server accesses this directory, not sibling review containers.

Use numeric UID/GID that owns the deployment directory and socket's GID (`stat -c %g /var/run/docker.sock`). Run `docker compose up -d`. Keep the bind-published listener on loopback and use the same TLS proxy setup as native deployment.

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

Shared credentials are refreshed by trusted commands on the server. Official providers retain exported access credentials on the server; custom command harnesses receive them in their containers. Login state, refresh tokens, locks and caches stay outside review workspaces. Static `env` credentials remain supported.

Each harness has: read-only image, no Linux capabilities, no privilege escalation, fixed PID/CPU/memory limits, bounded writable workspace and `/tmp`, read-only snapshots/context, and 8 MiB tmpfs output. No writable host paths exposed. After the harness exits, the reporting CLI exports its submitted JSON over stdout and the server validates and persists it. Only custom command harnesses receive their model-provider credentials. Official SDK providers connect to a per-run Unix socket; the server gateway adds credentials to allowlisted model requests. See [provider boundaries](harnesses.md#credential-boundary).

Processes run as the daemon's numeric UID/GID. Network `bridge` allows Internet access. Provider model requests also work with `network: "none"`: the Unix-socket gateway does not require container networking. Tests or dependency installation that need the Internet still require network access. Use dedicated host and restricted `reviewd-*` network where threat model requires.

Docker is not a security boundary against hostile kernel exploits. Prefer isolated host/VM for public fork reviews. Default leaves fork reviews disabled and restricts manual triggers to repository collaborators. Operator CLI is trusted and controlled by OS permissions.
