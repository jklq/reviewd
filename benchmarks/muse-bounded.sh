#!/bin/bash
# Trusted harness adapter for Muse Code: bounded model steps at max reasoning,
# then validate the final JSON through the same reviewd reporting CLI as the
# other bounded adapters. Arguments: prompt, model, max model steps.
# A rejected submission is retried once with the rejection fed back to the model.
set -euo pipefail
umask 077
mkdir -p "$HOME/.config/muse"
printf %s "$MUSE_AUTH_JSON" > "$HOME/.config/muse/auth.json"
unset MUSE_AUTH_JSON
# This path is in the disposable workspace, never the host snapshot.
rm -rf /workspace/.muse
prompt="$1"
attempt=1
while :; do
  echo "bounded adapter: attempt $attempt" >&2
  err=""
  if muse exec --yolo --model "$2" --reasoning-effort max --max-model-steps "$3" --json --workspace /workspace "$prompt" | tee /tmp/muse-events.jsonl >&2; then
    if python3 - <<'PY' > /tmp/report.json
import json
texts = []
with open('/tmp/muse-events.jsonl') as f:
    for line in f:
        line = line.strip()
        if not line.startswith('{'):
            continue
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if event.get('payload_type') == 'run.output.delta':
            texts.append(event.get('payload', {}).get('text', ''))
print(json.dumps(json.loads(''.join(texts))))
PY
    then
      if err=$(reviewd agent submit --file /tmp/report.json 2>&1); then
        exit 0
      fi
    else
      err="final response was not a valid JSON report"
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
