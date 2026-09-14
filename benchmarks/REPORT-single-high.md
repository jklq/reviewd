# Verification-less single-pass review at high reasoning

Quick experiment: one reviewer, no validator, OpenCode/DeepSeek V4.1 Flash
(`opencode-go/deepseek-v4.1-flash`) with `--variant high`. The run matrix was
stopped by request after 7 of 24 runs; two targeted extra runs filled the hard
gold and the non-trivial control. Prompts no longer prescribe a tool-call count;
only the bounded adapter keeps an enforced step budget.

## Setup

`single` reviews with lazy file reads; `structural` preloads the 64 KB
structural pack. Both return the reviewer's report directly (no second agent).
Production harness command and sandbox limits, one PR at a time, 20m deadline.
Config: `.benchmarks/config-high.json` (gitignored). Raw runs:
`.benchmarks/matrix-high/`; earlier high-reasoning pilots: `.benchmarks/results-high/`.

## Results (all runs succeeded, no timeouts)

| Case | Lines | Labeled gold | Strategy | Seconds | Findings | Gold | FP |
| --- | --- | --- | --- | --- | --- | --- | --- |
| docs control | 1 | — | single | 18 | 0 | — | 0 |
| docs control | 1 | — | structural | 21 | 0 | — | 0 |
| appwrite-11580 | 34 | audit-key-type | single | 204 | 1 | hit | 0 |
| appwrite-11580 | 34 | audit-key-type | structural | 338 | 1 | hit | 0 |
| appwrite-11615 | 1503 | ignored-platform-id | single | 282 | 6 | hit | 0 |
| appwrite-11615 | 1503 | ignored-platform-id | structural | 426 | 5 | hit | 0 |
| bun-28486 | 4432 | watch-output-shape | structural | 616 | 4 | miss | 0 |
| local-attribution-bug | 127 | harness-name-limit | single | 448 | 0 | miss | 0 |
| local-attribution-control | 139 | — | single | 322 | 0 | — | 0 |

Aggregates (`benchmarks/evaluate.py`): `single` 5/5 success, median 282s,
precision 1.0, gold recall 2/3, 2/2 controls clean. `structural` 4/4 success,
median 382s, precision 1.0 (one bun finding left pending), gold recall 2/3, 1/1
control clean.

## Observations

- Quality held without a validator: every labeled gold was found except the hard
  `harness-name-limit` case, all adjudicated findings were true positives, and
  no control produced a finding.
- Lazy reads beat the preloaded pack in this sample: `single` matched
  `structural`'s golds and was 1.4–1.7x faster on the 34- and 1503-line cases.
  The structural pack did not reduce tool use enough to pay for its preparation.
- `high` is not automatically faster than `max`: on the paired 11615 pilots it
  used 47 steps/83 tools versus 17/22 at max and took 333s versus 195s. Prior
  max-reasoning cells finished the same case in 294s with a validator.
- The local-attribution gold is missed by every architecture tried so far:
  bounded/max, sharded 1–2/max, and single/high all returned zero findings.
- Bun's labeled gold was missed, but three other reported bun defects were
  source-verified (dangling HMR module-key slices, double standalone build,
  `import.meta.hot.accept` callbacks never invoked); one watch entry-point
  finding remains unverified.

## Limitations

Single samples per cell, one model, one host; no max-reasoning single-pass arm
was run in this batch, so the variant comparison relies on the paired 11615
pilots. Findings were adjudicated by source inspection, not execution.
