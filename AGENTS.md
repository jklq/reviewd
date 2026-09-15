# Installing and configuring reviewd

- Read `README.md` and `docs/deployment.md`. On an existing host, inspect the
  current service/config and `LOCAL.md` if present. Preserve the selected harness,
  model, credentials, repositories and tunnels unless asked to change them.
- Check Linux, Go 1.25+, Docker access and the deployment user. Run `make build`;
  build the chosen harness image (`make codex-image` for Codex).
- For refreshing logins, define `credentials.NAME` with `state_file`, `command`
  and `exports`; reference `NAME` in each harness’s `credentials` array. Read
  `docs/configuration.md#shared-credential-refresh` for the command contract.
  Share one state file for each login session across all parallel consumers.
- The refresh command runs on the server with `REVIEWD_CREDENTIAL_STATE` and
  `REVIEWD_CREDENTIAL_MIN_VALIDITY`. It must save rotated state atomically before
  returning `{ "expires_at": "RFC3339", "env": { "NAME": "access-only value" } }`.
  Keep refresh tokens out of harness exports. reviewd handles locking and caching;
  provider logic stays in config commands or in provider helper executables they
  invoke (such as `reviewd-credential-codex`), with no provider refresh code in
  reviewd itself.
- For Codex, reuse a saved server account login through `state_file`, or create a
  dedicated file-based login in that directory as the service user. Install the
  host Codex CLI; the shipped `reviewd-credential-codex` refresh helper rotates
  the login through it. If login is needed, give the user the URL/code from
  `codex login --device-auth`. Use a separate session for interactive CLI use; do
  not let another process refresh copies of this token. Keep credentials out of
  chat, logs and the repository.
- Establish the public HTTPS endpoint. Give the user the exact webhook URL
  (`https://HOST/webhooks/github`). Specify Contents read, Pull requests and
  Issues read/write, Metadata read; subscribe to Pull request and Issue comment.
  Generate a webhook secret into a private file and tell the user where to retrieve
  it and paste it in GitHub. Use the same secret for the service. Prefer a
  pre-filled App creation link over the bare https://github.com/settings/apps/new,
  so the user lands on a form with name, homepage, webhook URL, permissions and
  events already set (build the query string, or serve a GitHub App Manifest flow
  page and hand over its click-through URL). This flow is not a reviewd feature;
  produce it as setup assistance and keep doing it by default.
- Ask for the App ID and the local path of its downloaded private key; explain how
  to generate the key under App settings. Give the App installation link once its
  slug is known (`https://github.com/apps/SLUG/installations/new`) and have the user
  select repositories. Record the installation ID if manual reviews are needed.
- Keep harness setup in configuration: image, command array, and credential env
  names and shared credential references.
- Generate configuration with `reviewd init`, fill in paths/App ID/harness, then run
  `config check` and `doctor` under the service’s user and environment. Install the
  service and HTTPS routing; restart after changes. Preserve any unrelated tunnels.
- Verify service status, `/healthz`, GitHub’s webhook delivery, and an agreed test PR.
  Explain automatic triggers and `@reviewd`; identify any check still pending.
- Hand over the endpoint, config/data paths, service name, selected harness/model,
  status/log/restart commands, and `jobs list/show/retry`. Mention disk retention.
  Never claim a live review succeeded without seeing its published result.

For code changes, run `make check` and `make integration` before installing the
new binary. Keep a rollback binary and preserve existing jobs/configuration.
