# Configuration reference

Run `reviewd init` for a template, then verify with `reviewd config check` (static) and `reviewd doctor` (runtime). Unknown fields are errors. Omitted top-level fields use defaults. Harness and credential maps replace the default sets. A custom harness command must consume `{{.Prompt}}` or `{{.PromptFile}}`. Configuration is loaded at startup, so restart after changes.

| Field | Default | Meaning |
|---|---|---|
| `listen` | `127.0.0.1:8080` | Webhook and health endpoint |
| `data_dir` | `./reviewd-data` | Private queue and artifacts |
| `app_id` | `0` | GitHub App ID |
| `private_key_file` | `./github-app.pem` | PKCS#1 or PKCS#8 RSA key |
| `webhook_secret_env` | `REVIEWD_WEBHOOK_SECRET` | Env var holding webhook secret |
| `agent_binary` | running binary | Optional static Linux binary at identical host/container path |
| `harnesses` | Codex | Named driver or custom command entries; see [harnesses](harnesses.md) |
| `credentials` | Codex | Shared refresh definitions |
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

Harness `env` lists variable **names**, not values. For a provider driver these values stay on the server; for custom command harnesses they are forwarded to the container. Missing values fail execution, and no other process env vars are forwarded to the container. Credential `env` forwards only allowed service env vars to the refresh command. The webhook secret name, `GITHUB_*` and `REVIEWD_*` are rejected. Put provider credentials in the service environment, never GitHub or unrelated secrets.

Harness `model` optionally declares the model its command selects. The published review names the harness and model that produced it. An agent may report the model it is actually running through the overview, otherwise the declared value is used. The shipped Codex provider pins `gpt-5.6-luna` and `reasoning_effort: "max"` through its SDK so reviews are reproducible rather than following ambient defaults.

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

Return actual expiry, not the example. Export exactly the configured names with enough remaining validity. Keep refresh tokens in the state file and export only access credentials. A command may project still-valid credentials without a network request; the shipped `reviewd-credential-project NAME` helper does this for provider CLI login files that do not rotate, exporting the JSON state as `NAME`. Login refresh logic belongs in the command. Provider drivers consume the exported access credentials on the server.

reviewd locks the canonical state path across workers, checks cache after acquiring the lock, and atomically caches successful exports. Only refresh is serialized. Cache files and locks sit beside the state file. Replacing the login invalidates cache automatically.

Use one state file per login. Do not distribute refresh token copies. For interactive CLI use alongside reviewd, create a separate login session.

Commands have a 1-minute timeout and 1 MiB output limit. Failed refreshes stop the review. Rotated state survives a failed command if the command saved it first. Command stdout/stderr never enters review logs. Cache files contain access credentials and must be protected.

`reviewd doctor` resolves credentials and checks freshness without a review, using the service's identity, PATH and environment.

The shipped `reviewd-credential-codex` helper, built beside reviewd by `make build`, returns `CODEX_AUTH_JSON` with the refresh token removed and forwards `SSL_CERT_FILE`, `SSL_CERT_DIR`, `CODEX_CA_CERTIFICATE`, `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY` when present. It checks the access token's expiry and rotates the login through the installed Codex CLI's app server when needed, persisting the rotated refresh token in the state file. The Codex provider reads the exported value on the server and uses only its access token and account ID in the model gateway, and never writes a login into the container; a custom command may still write its export into its temporary home. Keep Codex CLI on the service `PATH`.

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
