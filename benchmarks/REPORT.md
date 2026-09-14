# Pilot report: bounded review quality and speed

Single-sample pilot comparing three model/harness cells on the same bounded
orchestration: one read-only review agent with an enforced 12 model-step
budget, a structural context pack (64 KB cap), and a trusted adapter that
validates the final JSON report through `reviewd agent submit`. Nothing here
installs a daemon, changes live configuration, or publishes reviews.

## Cells

| Cell | Harness | Model | Reasoning | Results dir |
| --- | --- | --- | --- | --- |
| deepseek | opencode (`reviewd-benchmark:local`) | `opencode-go/deepseek-v4.1-flash` | max | `.benchmarks/results/` |
| mercury | opencode (`reviewd-benchmark:local`) | `openrouter/inception/mercury-2.5` | max | `.benchmarks/results-mercury/` |
| muse | Muse Code (`reviewd-muse:local`, `--yolo`) | `muse-spark-1.3` | max | `.benchmarks/results-muse/` |

Configs: `.benchmarks/config.json` (deepseek), `.benchmarks/mercury.json`,
`.benchmarks/muse.json` (all gitignored, mode 0600). No Gemini model was used
in any run: every harness command pins `--model` explicitly, reports
self-report the pinned model, and Muse runs additionally attest
`model_id: muse-spark-1.3` in `run.model.configured` events. A repo-wide and
log-wide grep for gemini returns nothing.

## Prompt versions

- v1: bounded prompt as first written (`important_files` unconstrained).
- v2 (current): adds "every `important_files` path must be a file changed in
  this PR" and replaces the opencode-specific "read/grep/glob only" line with
  "investigate with read-only tools; do not modify files, execute repository
  programs, or install dependencies". The v1->v2 change rescued the deepseek
  audit cell (v1: validation failure at 4 steps, timeout at 12 steps;
  v2: pass at 12 steps).

All mercury and muse runs except `mercury-audit-bounded4-1` (v1, 4 steps) use
v2 with 12 steps.

## Results (bounded, adjudicated by source inspection)

| Cell | Runs | Success | Median | p95 | Precision | Gold recall | Controls noisy |
| --- | --- | --- | --- | --- | --- | --- | --- |
| mercury (v2) | 10 | 9 (90%) | 11.8 s | 21.7 s | 0/10 (0.0) | 0/7 (0.0) | 1/3 |
| mercury (v1 audit) | 1 | 1 | 14 s | — | 0/2 (0.0) | 0/1 (0.0) | — |
| muse (v2) | 9 | 8 (89%) | 91 s | 182 s | 16/16 (1.0) | 4/7 (0.57) | 0/2 |
| deepseek (mixed) | 6 | 4 (67%) | 104 s | — | 6/6 (1.0) | 2/5 (0.4) | 0/1 |

Per-case detail (seconds / findings TP+FP):

- appwrite-11580 audit: mercury v1 14 s 0+2, mercury v2 FAIL (empty
  sequence_diagram, 12 s), mercury adaptive 22 s 0+1 (rescued by correction
  retry after an invalid-JSON first attempt), muse 97 s 1+0 (gold),
  deepseek v2 197 s 2+0 (gold).
- appwrite-11615 platform: mercury 9 s 0+1, muse FAIL then 86 s 5+0 (gold)
  at 12 steps and 141 s 7+0 (gold) at adaptive 16 steps, deepseek v1 120 s
  4+0 (gold).
- unkey-5461: mercury 13 s 0+1, muse 182 s 2+0 (gold).
- oven-sh-bun-28486 (4,432 lines): mercury 19 s 0+2, muse 127 s 1+0 (new).
- local-attribution-bug: mercury 10 s, no findings (gold missed); muse 56 s,
  no findings (gold missed); deepseek v2 88 s, no findings (gold missed).
  The original 43-minute production review of this exact revision caught the
  gold bug via one of its two reviewers (the other reviewer and the
  validator added nothing), so this is the only direct old-vs-bounded
  comparison: 1/2 old reviewers caught it, 0/3 bounded cells did.
- local-docs-control: mercury 7 s clean; muse 56 s clean; deepseek v1 18 s
  clean.
- local-attribution-control: mercury 12 s 0+1; muse 78 s clean.

Failures: mercury audit v2 submitted an empty `sequence_diagram`; muse
platform-1 added a top-level `evidence` key (strict decoding rejects unknown
fields); deepseek v1 failed once on `important_files` validation (fixed by
the v2 prompt) and once on a 4-minute timeout. The rejected muse report
still contained the gold bug and three further plausible findings; the
manual retry passed. All failures are kept as recorded runs.

## Reliability: correction retry and adaptive budgets

4 of the first 22 bounded runs (18%) failed without producing a report: 3
schema slips and 1 timeout. Both adapters now retry once inside the run,
feeding the rejection back to the model ("Previous submission was rejected:
...; output the complete corrected JSON report"), and `reviewbench` scales
bounded effort by changed lines (`--adaptive-budget`, on by default):
small <100 lines 8 steps/3 min, medium <500 12 steps/6 min, large <2000
16 steps/10 min, very large 24 steps/15 min, capped by `--timeout`. Metrics
record `budget_tier`, `changed_lines`, and the effective step/timeout
values.

Observed since the change: the mercury audit rerun slipped invalid JSON on
attempt 1 and was rescued by the correction retry (22 s total, still 0
recall — retry fixes submission, not quality). Adaptive tiers selected
small/8/3 m for the 1-line control and very-large/24/15 m for the 4,432-line
Bun case. The 16-step muse platform rerun found 7 verified findings versus 5
at 12 steps (adding the MODEL_PLATFORM boot fatal and the PlatformList union
defect), a first hint that budget scales with recall on large diffs.
Production already retries failed jobs 3 times with backoff; the correction
retry and adaptive budgets are the missing pieces worth adopting there.

## Behavior notes

- Mercury used zero tools in all 8 runs (one model step each, ~$0.001/run).
  It answers single-shot from the preloaded context and hallucinates freely
  from truncated patches: a truncated dependency array, an unregistered
  action that is registered, a missing write-back that exists, cleanup logic
  that exists. Verdict: unusable as a review agent in this setup; speed is
  real (7–19 s) but there are no good results to get. Its failure mode is
  silent confidence, not abstention.
- Muse at max reasoning used 5–18 tool calls per run, found 3 of 5 unique
  gold bugs (missing only the bun output-shape contract and the attribution
  length-limit interplay), verified 7 additional real bugs missing from the
  labels, and kept both controls clean. No token/cost events are emitted by
  `muse exec --json`, so cost is unmeasured.
- Deepseek at max reasoning is the slowest cell (120–197 s on real bugs,
  12 steps / 22–31 tools / ~$0.02–0.03 per run) with perfect adjudicated
  precision on the runs that submit, but it timed out once and needed the v2
  prompt to submit on audit at all.
- Verified bugs missing from the labels (kept out of `labels.json` to avoid
  mid-stream label mutation; recorded as true positives with empty matches):
  MODEL_PLATFORM boot fatal, app-update identifier/key mismatch, nested enum
  in PlatformApp/PlatformWeb, audit userType coverage gap in users/messaging
  endpoints, unkey top-level redirect mangling (minor), bun bundle-config
  key leak (minor).

## Limitations

Single samples per cell; no p95 or equivalence claim is supported. Labels
are not exhaustive and were frozen before these runs. Prompt versions and
per-run timeouts (4–10 m) vary across cells as noted. Provider-reported
token/cost fields are retained where emitted; they are not invoices, and
Muse emits none. The vendor-selected corpus may overlap training data; treat
these as development data and reserve held-out repos/revisions for a later
evaluation.
