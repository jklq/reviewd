# Muse review latency experiments (with a Codex GPT-5.6 Luna comparison)

## Setup

Eighteen successful replays and three retained failures: three pinned cases per
approach, one run per case, shuffled with seed `20260914`, executed at most two
runs concurrently per harness. The first three Muse cases ran serially before
that cap was adopted. This is a development pilot, not an exhaustive matrix or a
repeated-trial latency estimate.

Muse (nine runs):

- Harness: Muse Code, `muse-spark-1.3`, max reasoning, normal reporting CLI.
- Image: `reviewd-muse:local`; exact image and binary hashes are in each run's
  `metrics.json`.
- Limits: 2 CPUs and 3 GiB per container; 15-minute whole-review deadline.

Codex (nine successful runs plus three failures):

- Harness: `reviewd-codex:local`, Codex CLI 0.154.0, `gpt-5.6-luna`, normal text
  logging. Arms: high reasoning; high reasoning with `service_tier="fast"` (the
  Fast/priority tier, advertised as 1.5x speed with increased usage); and xhigh
  reasoning with the Fast tier.
- Strategy: `single` — one reviewer, no LLM validator; schema/diff checks remain.
- Limits: 2 CPUs and 2 GiB per container; 15-minute whole-review deadline.
- Three xhigh+Fast attempts on the first Codex account failed on its usage limit;
  `metrics.json.error` and `harness.log` are retained in
  `.benchmarks/luna-xhigh-fast/`. The successful xhigh+Fast runs used a second
  Codex account with identical model, command and tier.

Shared:

- Inputs: immutable prepared head/merge-base snapshots; no labels, prior reports
  or upstream review comments are provided to the agents.
- Cases: Appwrite #11580 (34 changed lines; audit attribution gold), Appwrite
  #11615 (1,503 lines; custom platform ID gold), and local attribution control
  (139 lines; includes the harness-name length fix).
- No repository programs/tests, dependency installation, external browsing or
  GitHub publication. Agents may use shell commands to inspect source and submit
  reports inside the disposable sandbox.
- No service deployment or live configuration changes. Existing local benchmark
  edits were preserved.

| Approach | Reviewers | Independent validator | Context |
| --- | --- | --- | --- |
| baseline | 2, full diff each, concurrent | always, full scope | lazy reads |
| single | 1 | none; schema/diff validation remains | lazy reads |
| conditional | 1, same discovery prompt as single | candidate-focused, only if findings are nonempty | lazy reads |

The conditional arm preserves an empty report's confidence and coverage gaps;
it does not claim that absence of findings proves correctness. Comparing single
with baseline changes both reviewer count and validation. Comparing conditional
with single tests the extra validation policy, but uses independent model runs;
it is not a replay of identical candidate reports.

The private config `.benchmarks/muse-speed.json` references the existing Muse
credential projection without embedding credentials. It replaces the old bounded
adapter with this command after materializing Muse authentication in its
container HOME:

```sh
muse exec --yolo --model muse-spark-1.3 --reasoning-effort max --json \
  --disable-web-tools --no-foreign-personal-context \
  --workspace /workspace --prompt-file /review/prompt.md
```

The Codex arms use `.benchmarks/codex-luna-high.json`,
`.benchmarks/codex-luna-fast.json` and `.benchmarks/codex-luna-xhigh-fast-2.json`
(copied from `reviewd.example.json` with the model pinned and only the reasoning
effort and service tier changed).

Reproduce with a fresh output directory:

```sh
make bench-build
python3 benchmarks/matrix.py --config .benchmarks/muse-speed.json \
  --cases .benchmarks/cases/appwrite-appwrite-11580-92b7760e \
          .benchmarks/cases/appwrite-appwrite-11615-d3c5a425 \
          .benchmarks/cases/local-attribution-control \
  --strategies baseline single conditional --repeats 1 --seed 20260914 \
  --timeout 15m --output .benchmarks/muse-speed-repeat
```

## Measurements

Host-clock spans distinguish credential resolution, container startup, workspace
copy, harness execution, export/container exit and cleanup, nested inside
reviewer/validator and engine spans. Concurrent spans overlap and are not summed.
Muse model steps count started model tasks and tool calls count result events.
The image's JSON timestamps are not useful wall-clock measurements and it emits
no token/cost totals; those are left unmeasured. Codex runs use normal text logs,
so no model-step or token totals are inferred for them either.

Replay time excludes queue wait, GitHub requests, cold snapshot downloads and
publication. The server now records these stages separately in
`runs/JOB_ID/timings-N.json`, including failed/superseded attempts. Missing replay
spans do not mean these real-world costs are zero.

## Results

Compact machine-readable numbers are in [results-muse-speed.json](results-muse-speed.json).
Seconds are end-to-end wall clock; "found" counts source-adjudicated findings
(all adjudications complete, none pending). Only two frozen golds exist: the
small case's `audit-key-type` and the large case's `ignored-platform-id`.

### Muse

| Approach | Small (34 lines) | Large (1,503 lines) | Control (139 lines) | Median (p95) | Precision | Gold recall | Control findings |
| --- | --- | --- | --- | --- | --- | --- | --- |
| baseline | 426.0 s; 1 | 584.0 s; 8 | 348.7 s; 1 | 426.0 (584.0) s | 0.9 | 2/2 | 1 false positive |
| single | 220.8 s; 1 | 260.2 s; 8 | 123.5 s; 0 | 220.8 (260.2) s | 1.0 | 2/2 | 0 |
| conditional | 259.0 s; 1 | 316.5 s; 7 | 135.5 s; 0 | 259.0 (316.5) s | 1.0 | 2/2 | 0 |

Both golds were found by every Muse approach. The baseline's control finding was
a false positive: it read JSON escape sequences in `internal/config/defaults.json`
as literal backslashes in the shell command.

Beyond the large case's gold, baseline and single each verified five extra roots:
the removed `MODEL_PLATFORM` constant still referenced at boot, `identifier`
updates against the `key` collection field, the union response model with empty
conditions, the doubly nested enum arrays, and the new react-native-web type
omitted from the hostname/scheme helpers. Conditional verified four extra roots,
missed the union-model and react-native-web gaps, but uniquely found that the app
and web update endpoints do not check the platform type and accept each other's
platforms. No Muse run found the small case's unpopulated `userType` response
field.

### Codex (GPT-5.6 Luna)

| Arm | Small | Large | Control | Median (p95) | Precision | Gold recall | Control findings |
| --- | --- | --- | --- | --- | --- | --- | --- |
| high | 175.7 s; 2 | 388.9 s; 7 | 243.3 s; 1 | 243.3 (388.9) s | 1.0 | 2/2 | 1 valid |
| Fast | 158.5 s; 2 | 200.3 s; 5 | 113.3 s; 0 | 158.5 (200.3) s | 1.0 | 2/2 | 0 |
| xhigh+Fast | 242.8 s; 2 | 384.9 s; 6 | 252.2 s; 1 | 252.2 (384.9) s | 1.0 | 2/2 | 1 valid |

Every successful Codex run found both golds and the small case's unpopulated
`userType` field. On the large case all arms found the boot constant, the
`identifier`/`key` mismatch, the empty union-model conditions and the nested enum
arrays; high and xhigh+Fast additionally found the react-native-web origin gap
and the query validator exposing `key` instead of the public `identifier`, while
Fast found neither. High and xhigh+Fast flagged the same control gap:
whitespace-only attribution values silently skip the configured-model fallback,
which source inspection confirms is real. Fast returned no control findings.

### Critical path

Host-side orchestration is hundreds of milliseconds per sandbox: credentials
0.00–0.11 s, container start 0.21–0.49 s, workspace copy 0.01–0.27 s, report
export 0.22–0.50 s, cleanup 0.03 s. Totals were 0.5–1.0 s for the single-run
reviews and 3.25 s for the Muse baseline's two reviewers plus validator. Harness
spans are 99.4–99.8% of single-run wall time. The Muse baseline critical path is
the slower reviewer (383.3 s) plus the sequential validator (200.6 s) out of
584.0 s; the two reviewers run concurrently. Live service reviews, queue wait,
GitHub requests, snapshot downloads and publication are excluded from all of it.

### Comparison

In this sample Luna Fast is the fastest arm: median 158.5 s, about 1.5x faster
than high on the same cases, while matching both golds with precision 1.0 and
clean controls — though it found fewer extra defects on the large case (four
roots versus high's six). Muse single is the fastest Muse arm (median 220.8 s)
with the same golds, precision 1.0 and clean controls. xhigh+Fast did not improve
latency or measured quality over high, and both noisy controls came from the
higher-effort Codex arms. Muse baseline was slowest and produced the only false
positive. These are single-run cells over three cases; nothing here establishes
equivalence, p95, or held-out recall.

## Limits

One run per case/approach, one model and one host; provider load, cache state and
Codex account quota are uncontrolled. Execution moved mid-pilot from serial to at
most two concurrent runs per harness, so some runs shared host CPU, disk and
provider load; latency comparisons here are not single-tenant. The xhigh+Fast arm
used a different Codex account than the high and Fast arms because the first
account hit its usage limit. Brief Go and Docker tests overlapped the first
baseline. Existing service reviews also ran on this 16-CPU host (observed around
1–2% CPU per service container during baseline validation); services were not
paused. The source-inspected labels are incomplete and not executed regression
tests; additional source-verified defects are counted as true positives with no
frozen label, and only the two frozen golds define recall. Failed attempts remain
failures and are never treated as clean reviews. This sample cannot establish
p95, statistical significance, quality equivalence or held-out recall. The
difficult local attribution bug and very large Bun case are outside this
three-case pilot. No review result was published to GitHub.
