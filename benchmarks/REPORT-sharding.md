# Sharding experiment (interim, stopped early)

The sharded-parallelism matrix was stopped after 5 of 48 runs: the single-pass
high-reasoning architecture looked more promising, so the remaining budget moved
there. This note records what the completed shard runs measured. Raw runs are in
`.benchmarks/results-shards/` (gitignored) and the interrupted matrix plan plus
launch records are in `.benchmarks/matrix-shards/`.

## Setup

Production OpenCode harness (`reviewd-opencode:local`, `opencode-go/deepseek-v4.1-flash`,
`--variant max`), one PR at a time. Reviewers received disjoint file slices with
per-slice structural packs; the validator saw the full diff and all candidate
reports. Shard counts: `sharded-1`, `sharded-3` (pilots), `sharded-adaptive`
(2/4/6 by changed lines), `sharded-2`. Timeouts 15m (pilots) and 20m (matrix).
Labels are the source-inspected set in `labels.json`; unmatched findings are not
counted as false positives here.

## Completed runs

| Run | Case | Changed lines | Shards | Reviewer stage (s) | Validator (s) | Total (s) | Findings |
| --- | --- | --- | --- | --- | --- | --- | --- |
| pilot-3 | appwrite-11615 | 1503 | 1 | 200 | 94 | 294 | 5 (gold) |
| pilot-2 | appwrite-11615 | 1503 | 3 | 184/221/282 | 194 | 476 | 6 (gold) |
| 001 | appwrite-11615 | 1503 | 4 | 126/252/262/276 | 134 | 410 | 7 (gold) |
| pilot-4 | oven-sh-bun-28486 | 4432 | 6 | 151–333 | 312 | 645 | 8 |
| 003 | local-attribution-bug | 127 | 1 | 270 | 112 | 383 | 0 (gold missed) |
| 004 | local-attribution-bug | 127 | 2 | 96/221 | 94 | 315 | 0 (gold missed) |
| 000 | local-attribution-bug | 127 | 2 | 154/234 | 103 | 337 | 0 (gold missed) |
| pilot-1 | local-attribution-control | 139 | 2 | 113/142 | 120 | 262 | 0 (control clean) |
| 002 | local-attribution-control | 139 | 2 | 105/163 | 174 | 337 | 0 (control clean) |

## Observations on this sample

- Discovery did not speed up with slicing: on the 1503-line PR the slowest
  reviewer went 200s (1 shard) → 282s (3) → 276s (4). Each agent still pays a
  large repository-navigation cost, so max-shard wall time is nearly flat.
- The validator cost grows with candidate count: 94s (1 candidate), 134s (4),
  194s (3), 312s (6), and it spins 100–170s on zero-finding controls.
- End to end, 1 reviewer was fastest on the 1503-line PR (294s vs 410–476s).
  On the 127-line PR, 2 shards beat 1 shard in one pairing (315s vs 383s) but
  the same config took 337s in another run, inside observed variance.
- Quality was retained but not improved: the 11615 gold appeared at all shard
  levels, controls stayed clean in every arm, and the hard `harness-name-limit`
  gold in local-attribution-bug was missed at 1 and 2 shards alike.
- Sharding therefore needs enforced per-agent budgets or pipelined validation
  before it can pay for itself; slice size alone changes little.

## Limitations

Five matrix runs plus four pilots, one model, max reasoning, high latency
variance. The matrix was interrupted, so no p95, equivalence or quality claim is
supported. Findings are not adjudicated here beyond gold/control status.
