#!/bin/bash
# Trusted harness adapter: read-only investigation, bounded model steps, then
# validate final JSON through the same reviewd reporting CLI as other strategies.
# A rejected submission is retried once with the rejection fed back to the model.
set -euo pipefail
umask 077
mkdir -p "$HOME/.local/share/opencode"
printf %s "$OPENCODE_AUTH_JSON" > "$HOME/.local/share/opencode/auth.json"
unset OPENCODE_AUTH_JSON
# These paths are in the disposable workspace, never the host snapshot.
rm -rf /workspace/.opencode /workspace/opencode.json /workspace/opencode.jsonc
export OPENCODE_CONFIG_CONTENT='{"agent":{"reviewbench":{"description":"Bounded read-only PR review","mode":"primary","steps":12,"permission":{"*":"deny","read":"allow","grep":"allow","glob":"allow","external_directory":{"/review/**":"allow"}},"prompt":"Review the supplied change for actionable newly introduced defects. Follow /review/AGENTS.md. Your FINAL response must be exactly one complete JSON report, without Markdown fences or commentary. Even when your step limit is reached, output that JSON report. Do not submit via tools: a trusted adapter validates your final JSON."}}}'
export OPENCODE_CONFIG_CONTENT="$(printf %s "$OPENCODE_CONFIG_CONTENT" | jq --argjson steps "$3" '.agent.reviewbench.steps = $steps')"
prompt="$1"
attempt=1
while :; do
  echo "bounded adapter: attempt $attempt" >&2
  err=""
  if opencode run --pure --auto --agent reviewbench --format json --variant max --model "$2" "$prompt" | tee /tmp/reviewbench-events.jsonl >&2; then
    if jq -ers '[.[] | select(.type == "text") | .part.text] | last | fromjson' /tmp/reviewbench-events.jsonl > /tmp/report.json 2>/tmp/reviewbench-extract.err; then
      if err=$(reviewd agent submit --file /tmp/report.json 2>&1); then
        exit 0
      fi
    else
      err="final response was not a valid JSON report: $(head -c 300 /tmp/reviewbench-extract.err)"
    fi
  else
    err="model run failed"
  fi
  if [ "$attempt" -ge 2 ]; then
    echo "bounded adapter: giving up: $err" >&2
    exit 1
  fi
  echo "bounded adapter: attempt $attempt rejected, retrying: $err" >&2
  attempt=$((attempt + 1))
  prompt="$1 Previous submission was rejected: $err Output the complete corrected JSON report as your final response, with no other text."
done
