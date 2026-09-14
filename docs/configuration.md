# Configuration reference

Run `reviewd init` for a template, then verify with `reviewd config check` (static) and `reviewd doctor` (runtime). Unknown fields are errors. Omitted top-level fields use defaults. Harness and credential maps replace the default sets. A command must consume `{{.Prompt}}` or `{{.PromptFile}}`. Configuration is loaded at startup, so restart after changes.

| Field | Default | Meaning |
|---|---|---|
| `listen` | `127.0.0.1:8080` | Webhook and health endpoint |
| `data_dir` | `./reviewd-data` | Private queue and artifacts |
| `app_id` | `0` | GitHub App ID |
| `private_key_file` | `./github-app.pem` | PKCS#1 or PKCS#8 RSA key |
| `webhook_secret_env` | `REVIEWD_WEBHOOK_SECRET` | Env var holding webhook secret |
| `agent_binary` | running binary | Optional static Linux binary at identical host/container path |
| `harnesses` | Codex | Named image/command/env/network/credential/egress entries |
| `credentials` | Codex | Shared refresh definitions |
| `egress` | none | Optional egress-proxy block shared by isolated harnesses |
| `reviewers` | `["codex"]` | Round-robin harness selection |
| `parallelism` | `1` | 1–16 concurrent reviewers per PR |
| `validator` | `codex` | Final independent pass harness (unused when `parallelism` is 1) |
| `size_tiers` | none | Optional PR-size routing to harness sets |
| `workers` | `2` | 1–32 simultaneously active PR jobs |
| `timeout` | `45m` | Whole job deadline, all stages (1s–2h) |
| `memory` | `2g` | Per-container memory limit (integer `m` or `g`) |
| `cpus` | `2` | Per-container CPU limit (≥1) |
| `max_findings` | `20` | Max published inline findings (1–50) |
| `min_finding_confidence` | `0.85` | Min evidence confidence (0.5–1) |
| `policy` | empty | Trusted review policy text |
| `allow_forks` | `false` | Review fork PRs |

Maximum **concurrent** reviewer containers = `workers × parallelism`. Validation uses one container per job, except parallelism 1 runs its single reviewer as the final pass with no separate validator. Defaults run two PR jobs at a time, each a single reviewer pass. Independent reviews of the same PR are serialized. Each attempt can invoke `parallelism + 1` harnesses (one total when `parallelism` is 1). Retries multiply model usage.

Harness `env` lists variable **names**, not values. Missing values fail execution, and no other process env vars are forwarded to the container. Credential `env` forwards only allowed service env vars to the refresh command. The webhook secret name, `GITHUB_*` and `REVIEWD_*` are rejected. Put provider credentials in the service environment, never GitHub or unrelated secrets.

Harness `model` optionally declares the model its command selects. The published review names the harness and model that produced it. An agent may report the model it is actually running through the overview, otherwise the declared value is used. The shipped Codex harness pins `gpt-5.6-luna` and `model_reasoning_effort="max"` in its command so reviews are reproducible rather than following ambient defaults.

## PR-size harness tiers

`size_tiers` routes PRs of different sizes to different harnesses, so small
fixes can use a fast reviewer while large changes get the full panel. Tiers
are evaluated in order and the first match wins; a PR matching no tier falls
back to the top-level `reviewers`, `validator` and `parallelism`. Size is the
PR's changed lines (additions plus deletions) and its changed-file count. Each
reviewer input records the selected tier as `size_tier` in its `context.json`.

```json
{
  "reviewers": ["codex"],
  "validator": "codex",
  "size_tiers": [
    {"name": "small", "max_lines": 50, "max_files": 5, "reviewers": ["fast"], "validator": "fast", "parallelism": 1},
    {"name": "medium", "max_lines": 500, "reviewers": ["codex"]},
    {"name": "large", "reviewers": ["codex", "thorough"], "parallelism": 2}
  ]
}
```

A tier matches when the PR is within both bounds; a zero or omitted bound is
unlimited, so the last tier above is a catch-all. `validator` and `parallelism`
default to the top-level values when omitted. Tier names are optional labels
and must be unique. `config check` rejects unknown harnesses, out-of-range
parallelism, and tiers an earlier tier already covers (list tiers smallest
first). Referenced harnesses must exist in `harnesses`, so `doctor` checks
their images like any other harness.

## Shared credential refresh

Declare once, reference from every harness using that login:

```json
{
  "credentials": {
    "account": {
      "state_file": "/var/lib/reviewd/credentials/account.json",
      "command": ["/usr/local/bin/refresh-account"],
      "exports": ["HARNESS_AUTH"]
    }
  },
  "harnesses": {
    "my_custom_harness": {
      "image": "my-harness:1",
      "network": "bridge",
      "credentials": ["account"],
      "command": ["my-agent", "--prompt", "{{.Prompt}}"]
    }
  }
}
```

Reference `my_custom_harness` in `reviewers` or `validator`. The command runs on the service host (inside the server container for Compose), without PR inputs. It receives:
- `REVIEWD_CREDENTIAL_STATE`: absolute path to the saved login file
- `REVIEWD_CREDENTIAL_MIN_VALIDITY`: required remaining validity in seconds (timeout + 1m)

The working directory is the state file's directory. Commands are argument arrays, so use an explicit shell for shell syntax. Relative `state_file` is resolved against the config file.

The state file must exist and be service-writable. The command renews when necessary, atomically saves rotated refresh credentials to the state file, then emits this JSON on stdout:

```json
{
  "expires_at": "2030-01-01T12:00:00Z",
  "env": {"HARNESS_AUTH": "access-credentials-for-the-harness"}
}
```

Return actual expiry, not the example. Export exactly the configured names with enough remaining validity. Keep refresh tokens in the state file and export only access credentials. A command may project still-valid credentials without a network request. Provider-specific logic belongs in the command, not in reviewd.

reviewd locks the canonical state path across workers, checks cache after acquiring the lock, and atomically caches successful exports. Only refresh is serialized. Cache files and locks sit beside the state file. Replacing the login invalidates cache automatically.

Use one state file per login. Do not distribute refresh token copies. For interactive CLI use alongside reviewd, create a separate login session.

Commands have a 1-minute timeout and 1 MiB output limit. Failed refreshes stop the review. Rotated state survives a failed command if the command saved it first. Command stdout/stderr never enters review logs. Cache files contain access credentials and must be protected.

`reviewd doctor` resolves credentials and checks freshness without a review, using the service's identity, PATH and environment.

The shipped Codex refresh returns `CODEX_AUTH_JSON` with the refresh token removed and forwards `SSL_CERT_FILE`, `SSL_CERT_DIR`, `CODEX_CA_CERTIFICATE`, `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY` when present. The harness writes the access-only login into a temporary home. The server image includes Python 3 and Codex CLI.

## Egress proxy and credential isolation

By default a harness container holds its model-provider credential by design, so a prompt-injected harness can exfiltrate it. Setting `egress: true` on a harness removes the credential from the harness entirely: each run mints a random single-use sentinel per credential export (`reviewd-sentinel-` plus 32 hex characters), the harness receives only sentinels in its environment, and a trusted per-job proxy container swaps sentinel↔real on the network path. Sentinels live in daemon memory only and are never persisted.

```json
{
  "egress": {
    "image": "reviewd-egress:local",
    "command": ["/usr/local/bin/egress", "serve", "--listen", ":8080", "--allow", "api.openai.com", "--config", "/etc/reviewd/reviewd.json"],
    "network": "bridge",
    "ca_file": "/etc/reviewd/egress-ca.pem"
  },
  "harnesses": {
    "codex": {
      "image": "reviewd-codex:local",
      "command": ["codex", "exec", "{{.Prompt}}"],
      "credentials": ["codex_account"],
      "egress": true
    }
  }
}
```

A harness with `egress: true` must omit `network`; reviewd attaches it to a per-job internal network instead. The global `egress` block is required when any harness opts in. `image` and `command` select the proxy; `network` follows the same `bridge`/`none`/`reviewd-*` rule as harnesses and must reach the provider; `ca_file` is the operator-generated TLS CA bundle (certificate plus private key) the proxy uses to intercept TLS, resolved against the config file like `private_key_file`. `config check` enforces all of this statically, and `doctor` additionally verifies the proxy image exists, carries a HEALTHCHECK, the CA file holds a certificate plus the private key, a `reviewd-*` upstream network exists, and credential resolution succeeds.

The harness keeps its usual mounts and limits, plus a daemon-derived certificate-only CA file at `/review/egress-ca.pem`, `REVIEWD_EGRESS_CA` pointing at it, and `HTTPS_PROXY`/`HTTP_PROXY` set to `http://reviewd-egress:8080` with an empty `NO_PROXY`. Harness tools verify intercepted TLS against the mounted CA; tools that only read lowercase variables may need `http_proxy`/`https_proxy` exported from the uppercase values in the harness command. Custom images should combine the system bundle with the mounted CA at runtime (for example `cat /etc/ssl/certs/ca-certificates.crt "$REVIEWD_EGRESS_CA" > /tmp/bundle.pem` with `SSL_CERT_FILE` and `NODE_EXTRA_CA_CERTS` pointed at the bundle) or bake the certificate-only CA into the image like the shipped Codex stage does with its `EGRESS_CA_FILE` build argument. The CA private key stays with the proxy and never enters a harness container or image.

Any sentinel found in the harness transcript or `harness.log` fails the job before any report is decoded, validated, or written. GitHub sees only the generic failure comment; the operator-side error is the generic `egress sentinel found in harness output; report withheld`. The run directory and `harness.log` are left in place for inspection, and the real credential never appears in job artifacts.

The proxy contract: reviewd starts the configured image with the real credential values in its environment, `REVIEWD_SENTINELS` holding the export→sentinel JSON map, the config file mounted read-only, and each referenced credential state directory mounted read-write at the same absolute paths. The shipped proxy serves HTTP CONNECT and forwarding on `:8080`, mints per-host TLS certificates from the CA, substitutes sentinel→real in request headers and bodies and real→sentinel in responses while streaming, allowlists destinations from `--allow`/`REVIEWD_EGRESS_ALLOW` (exact hosts, `*.example.com` for subdomains) with 403 plus a log line for anything else, and answers `GET /healthz` for its HEALTHCHECK. It never logs credential values, headers, or bodies. The proxy caps concurrent connections at 32, runs under the same memory and PID limits as harnesses, and scrubs each response with the credential snapshot its request used, so a rotation mid-request cannot leak the old secret. Provider-specific behavior belongs in the proxy image and operator configuration, never in reviewd itself: reviewd only plans the proxy argv, mounts, and environment. The proxy owns token refresh by re-running `reviewd credential env --config <mounted config> <name>` when a credential nears expiry, so the harness never needs a refresh token; refresh commands therefore run inside the proxy container and must find their interpreters and CLIs there (the shipped image carries `sh` and Python 3, not provider CLIs). See [deployment](deployment.md#egress-proxy-deployment) for CA generation, the network model, and verification.

When the CLI reads its credential from a structured file such as a JSON auth file, export each secret leaf as its own credential export and rebuild the file in the harness command with the sentinels in the secret positions. A single export holding the whole file would hand the harness one opaque sentinel string that the CLI cannot parse. The refresh command below extracts one API key from an opencode login, and the harness command writes a minimal auth file around the sentinel; the CLI parses it normally, sends the sentinel, and the proxy swaps in the real key:

```json
"credentials": {
  "opencode_go": {
    "state_file": "/var/lib/reviewd/credentials/opencode/auth.json",
    "exports": ["OPENCODE_KEY"],
    "command": ["python3", "-c", "import json,os;print(json.dumps({'expires_at':'2035-01-01T00:00:00Z','env':{'OPENCODE_KEY':json.load(open(os.environ['REVIEWD_CREDENTIAL_STATE']))['opencode-go']['key']}}))"]
  }
}
```

```sh
printf '{"opencode-go":{"type":"api","key":"%s"}}' "$OPENCODE_KEY" > "$HOME/.local/share/opencode/auth.json"
```

Opaque bearer values sent as-is (API keys, OAuth access tokens) isolate cleanly. Tokens the CLI cryptographically verifies client-side do not: the Codex CLI verifies its ChatGPT identity token signature locally, so ChatGPT-account Codex harnesses cannot use egress and should stay on direct networking or switch to API-key auth. See the harness notes in [deployment](deployment.md#egress-proxy-deployment).

## Review policy

Example:

```json
{
  "policy": "Use established error types. Report violations only when behavior changes. Authentication must preserve tenant boundaries. Do not report formatting, speculative abstractions, or missing tests without concrete defects."
}
```

Agents read base-branch `AGENTS.md` and nested policy files. PR changes to those files are review material, not trusted operator policy. Agents see complete source snapshots and may run tests in the disposable workspace. Network-enabled images can install dependencies, but prebuilding is more reproducible.

The validator independently checks each candidate. The server validates metadata, evidence, scores, paths, side-specific line ranges, hunk boundaries and changed-file membership. It sorts by priority, then confidence, path and line, removes duplicates and findings below threshold, and enforces the cap. Exceeding the cap lowers merge confidence to ≤3 and adds an explanation. The uncapped validator report is retained.

Scores below 5 require reasons. P1/P2 findings cannot coexist with scores 4–5. Missing material coverage is uncertainty, not an invented inline finding.
