# Real PR review benchmarks

An unpublished replay suite for reviewd's production Docker sandbox. It measures
completion latency, stage latency, failures, tool/model steps, token usage and
adjudicated bug detection. The compared cells are OpenCode / DeepSeek V4.1
Flash / max reasoning, OpenCode / Inception Mercury 2.5 (via OpenRouter) /
max reasoning, and Muse Code / muse-spark-1.3 / max reasoning.
Nothing here installs a daemon, changes live configuration, or publishes reviews.
See [the pilot report](REPORT.md) for measurements and limitations.

## Corpus and labels

- `corpus.json`: 17 real upstream PR candidates, pinned to review comments'
  **original_commit_id**, with an immutable dataset revision and source URL.
- `inventory.json`: actual pinned-diff sizes and preparation availability. The
  final PR's size can differ substantially from the originally reviewed revision.
- `local-corpus.json`: three real local PR snapshots, including two controls.
- `labels.json`: source-inspected bug hypotheses and controls. Labels are not
  exhaustive. One upstream claim is explicitly excluded as pre-existing.
- `.benchmarks/cases/`: full head/merge-base snapshots and GitHub patches. It is
  gitignored, as are upstream comments, harness logs and raw reports.

The inspected set has five positive PR revisions and two controls. The positives
cover small, medium, large and very large changes; the controls cover a one-line
README edit and a 139-line code change. A source-inspected label is not an
executed regression test. No model-generated review is treated as ground truth.
An empty review or a merged PR is not sufficient to label a control.

The public candidate source is
[Entelligence-AI/benchmark-maxxing](https://github.com/Entelligence-AI/benchmark-maxxing).
It contains naturally occurring PRs and bot comments, including noisy comments,
outdated revisions and fixes. Raw comments remain private in the download cache;
the tracked labels are short independent descriptions with source evidence.
This vendor-selected set is biased and may overlap model training data. Treat
pilot cases as development data, and reserve new repositories/revisions for a
subsequent held-out evaluation. Do not compare these scores to vendor scores
collected on other cases or with other graders.

## Build and prepare

Requires the existing OpenCode harness image, Docker access, Go 1.25+, Python 3,
`gh` read access and the canonical OpenCode credential from the service config.
No provider keys are copied into repository files or passed on command lines.

```sh
make bench-build bench-test
python3 benchmarks/configure.py
python3 benchmarks/corpus.py collect
python3 benchmarks/corpus.py prepare appwrite-appwrite-11580
# Only needed for the bounded strategy; keeps the live image unchanged:
docker build -f benchmarks/Dockerfile -t reviewd-benchmark:local benchmarks
# Mercury runs reuse that image with a config that pins the model:
# (.benchmarks/mercury.json; gitignored, mode 0600)
python3 - <<'PY'
import json, os, pathlib
c = json.load(open('.benchmarks/config.json'))
for h in c['harnesses'].values():
    h['model'] = 'openrouter/inception/mercury-2.5'
    h['command'] = [v.replace('opencode-go/deepseek-v4.1-flash', 'openrouter/inception/mercury-2.5') for v in h['command']]
p = pathlib.Path('.benchmarks/mercury.json')
p.write_text(json.dumps(c, indent=2) + '\n')
os.chmod(p, 0o600)
PY
# Muse runs need their own image with the statically linked muse binary:
rm -rf /tmp/muse-image && mkdir -p /tmp/muse-image \
  && cp ~/.local/bin/muse-bin-* /tmp/muse-image/muse \
  && cp benchmarks/muse-bounded.sh /tmp/muse-image/ \
  && docker build -f benchmarks/Dockerfile.muse -t reviewd-muse:local /tmp/muse-image
# ... and a config with a muse harness plus a projected-auth credential
# (.benchmarks/muse.json and .benchmarks/credentials/muse-auth.json, 0600):
install -m 0600 ~/.config/muse/auth.json .benchmarks/credentials/muse-auth.json
# then copy .benchmarks/config.json to .benchmarks/muse.json with harnesses
# replaced by {"muse": {..., "model": "muse-spark-1.3"}}, reviewers/validator
# set to "muse", and a muse_account credential projecting the state file to
# MUSE_AUTH_JSON (same shape as the opencode_account projection).
```

`collect` downloads immutable upstream metadata and generates the manifest.
`prepare` resolves the merge base, rejects the GitHub 300-file comparison cap,
records missing patches, and downloads both pinned snapshots. It refuses to
overwrite an existing case. Snapshots cap downloads at 256 MiB and extraction at
1 GiB; symlinks/special files are omitted and recorded. The whole preparation is
separate from warm-cache review time. A failed download is not a review result.
Reviewers receive no labels, review comments, fix commits or later revisions.

Local cases in this machine's `.benchmarks/cases/local-*` were copied from the
job IDs recorded in `local-corpus.json`. They contain only head/base snapshots,
context and diff, not previous reports. Other hosts can retrieve the recorded
GitHub revisions or import equivalent saved snapshots.

## Experiments

| Strategy | Discovery | Final validation | Context |
| --- | --- | --- | --- |
| baseline | live configured fan-out (here 2) | full sequential validator | current lazy file reads |
| single | 1 reviewer | schema/diff only | lazy file reads |
| preload | 1 reviewer | schema/diff only | bounded patches and changed file contents |
| structural | 1 reviewer | schema/diff only | patches, Go AST declarations/call sites, neighboring paths |
| targeted | 1 reviewer, candidate-focused strategy | candidate-focused validator | structural pack |
| sharded | `sharded-N` pins N reviewers, `sharded-adaptive` grows 1/2/4/6 with changed lines | candidate-focused validator | per-slice structural pack |
| bounded | 1 read-only agent (OpenCode or Muse Code), enforced 12 model steps | trusted adapter + schema/diff only | structural pack |

A single-pass arm at high reasoning (no validator) is run with
`single`/`structural` and a config whose command uses `--variant high`; results
are in [REPORT-single-high.md](REPORT-single-high.md). It found every labeled
gold except the hard local-attribution case, with precision 1.0 and clean
controls on nine runs.

Sharded reviewers receive disjoint slices: `AssignShards` balances changed lines
across slices and prefers keeping a directory in one slice. Each reviewer's
`files.json` and structural context pack cover only its slice, so small PRs stay
on one reviewer while large ones are split into bounded amounts of work. The
validator still sees the full diff and pack, and `assignment.json` plus the
`shards`, `shard_changed_lines`, `shard_file_counts` and `shard_context_bytes`
metrics record the split. `sharded` keeps the historical cap of three reviewers.
Fixed `sharded-N` levels are the control for the adaptive arm; both limit each
reviewer's scope with the same prompt guidance. An interim sharding report with
the completed measurements and why the matrix stopped early is in
[REPORT-sharding.md](REPORT-sharding.md).

The structural pack uses Go's parser, not a type-resolved call graph. Same-name
calls are navigation hints; non-Go files get patches and neighboring paths,
not AST analysis. Packs are capped at 64 KB with explicit truncation markers.
Prompts do not prescribe a tool-call count; only the bounded adapter enforces a
step budget, through the harness configuration rather than prompt text.
The bounded strategy uses the agent's step budget (`steps` for OpenCode,
`--max-model-steps` for Muse Code) and a JSON-final-response adapter
(`benchmarks/opencode-bounded.sh` or `benchmarks/muse-bounded.sh`, selected by
harness name). This changes both tool permissions and report submission: it is
a combined exploratory strategy, not an isolated attribution of improvement to
one feature. It does not reduce reasoning effort. The Muse adapter runs
`muse exec --yolo` (unsandboxed, full tools, instructed read-only); the
OpenCode adapter enforces read-only permissions.

All strategies reuse credential locking, sandbox limits, schema validation,
changed-line validation and normalization. They use independent container owner
labels and new output directories. There are no automatic retries. A timeout or
invalid report is a failure, never a clean result. Repository code/tests are not
needed for these read-only experiments; bounded denies execution tools outright.

```sh
./bin/reviewbench --config .benchmarks/config.json \
  --case .benchmarks/cases/appwrite-appwrite-11580-92b7760e \
  --strategy bounded \
  --output "$PWD/.benchmarks/example-run"

python3 benchmarks/matrix.py \
  --config .benchmarks/config.json \
  --cases .benchmarks/cases/local-docs-control .benchmarks/cases/local-attribution-bug \
  --strategies baseline single preload structural targeted sharded-1 sharded-2 sharded-3 sharded-adaptive bounded \
  --repeats 3 --seed 20260914 --timeout 6m \
  --output .benchmarks/matrix

python3 benchmarks/adjudicate.py .benchmarks/matrix
# Inspect base/head and reports; fill each adjudication.json verdict and matches.
python3 benchmarks/evaluate.py .benchmarks/matrix
```

The matrix shuffles order deterministically. Benchmark runs execute at most two
concurrent runs per harness (codex, muse, opencode); reviewers within a baseline
run may still run concurrently. Compare paired cases with identical deadlines and
repeated trials, and keep cache conditions/provider load visible. Stratify by actual changed lines: small <100, medium <500, large <2000,
very large >=2000. Match bugs by trigger and consequence, not wording or line
number alone. Count a root cause once even if multiple findings describe it.

Bounded runs scale effort by PR size by default (`--adaptive-budget`, capped
by `--timeout`): small 8 steps/3 min, medium 12 steps/6 min, large 16 steps/
10 min, very large 24 steps/15 min. Metrics record the selected `budget_tier`,
`changed_lines`, and effective step/timeout values. Both bounded adapters
retry once inside the run when a submission is rejected, feeding the error
back to the model; attempts are logged as `bounded adapter: attempt N` in
`harness.log`. Pass `--adaptive-budget=false` with explicit `--max-steps` /
`--timeout` to reproduce the fixed-budget cells from the pilot report.

Metrics include successful-review median/p95, failure rate, all-attempt fraction
under 180 seconds, known-bug recall including failed attempts, precision on
adjudicated findings, and control finding rate. Unmatched findings remain pending
until reviewed: they may be valid bugs missing from the labels. Precision over a
partially adjudicated set is explicitly partial. Provider-reported token/cost
fields are retained; these are not subscription invoices. Default-format logs
have no reliable token totals. Small pilot samples cannot establish p95 or
quality equivalence.

Artifacts are private and operator-retained; snapshot downloads dominate disk
use. Delete only completed benchmark output/cache directories when no replays
use their snapshots. Leave `/var/lib/reviewd` and its live jobs alone.

## Muse speed experiments

`conditional` uses one ordinary reviewer with lazy reads. Empty findings return
that report unchanged (including its confidence and coverage limitations);
nonempty findings trigger a candidate-focused validator. This is an experimental
precision/latency tradeoff: an empty report can still have missed a bug. The
production engine's unconditional independent validator is unchanged.

Use an ordinary Muse CLI harness for `baseline`, `single`, and `conditional`;
the older `.benchmarks/muse.json` may instead invoke the bounded JSON adapter.
For a controlled comparison, use the same Muse model, reasoning effort and
command in all three approaches. The speed experiment uses `muse-spark-1.3`,
max reasoning, JSON event logs, and the normal `reviewd agent submit` protocol.
See [the Muse speed report](REPORT-muse-speed.md) for the exact setup and results.

`metrics.json.timings` records the engine and sandbox spans described in
[deployment timings](../docs/deployment.md#stage-timings). Replay inputs are
already prepared; queue wait, GitHub publication and cold snapshot downloads are
**not measured** by these replay runs. Missing spans do not mean zero latency.
Muse model steps count started model tasks, tools count result events, and
replayed event IDs are deduplicated per session. Provider tokens/costs are not
inferred when absent from the JSON events.
