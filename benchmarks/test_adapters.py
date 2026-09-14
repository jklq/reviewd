#!/usr/bin/env python3
"""Hermetic retry tests for the bounded harness adapters.

PATH shims stand in for the model CLI and reviewd, so no network,
credentials, or containers are needed. The model shim emits the bad
payload on its first invocation and the good payload from the second on;
the reviewd shim rejects any report containing the INVALID marker.
"""
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent

REVIEWD_SHIM = """#!/bin/bash
printf 'reviewd %s\\n' "$*" >> "$SHIM_CALLS"
if grep -q INVALID "${@: -1}"; then
  echo "reviewd: simulated rejection" >&2
  exit 1
fi
exit 0
"""

MUSE_SHIM = """#!/bin/bash
printf 'muse %s\\n' "$*" >> "$SHIM_CALLS"
n=$(grep -c '^muse ' "$SHIM_CALLS")
body="$SHIM_BAD"
if [ "$n" -ge 2 ]; then body="$SHIM_GOOD"; fi
printf '{"payload_type":"run.output.delta","payload":{"text":%s}}\\n' "$body"
"""

OPENCODE_SHIM = """#!/bin/bash
printf 'opencode %s\\n' "$*" >> "$SHIM_CALLS"
n=$(grep -c '^opencode ' "$SHIM_CALLS")
body="$SHIM_BAD"
if [ "$n" -ge 2 ]; then body="$SHIM_GOOD"; fi
printf '{"type":"text","part":{"text":%s}}\\n' "$body"
"""

BAD_REPORT = json.dumps({"summary": "INVALID"})
GOOD_REPORT = json.dumps({"summary": "ok"})


def run_adapter(script, model_shim, auth_var, bad, good):
    tmp = Path(tempfile.mkdtemp(prefix="adapter-test-"))
    calls = tmp / "calls.log"
    calls.write_text("")
    env = dict(os.environ)
    env["HOME"] = str(tmp)
    env["PATH"] = f"{tmp}{os.pathsep}{env['PATH']}"
    env["SHIM_CALLS"] = str(calls)
    env["SHIM_BAD"] = json.dumps(bad)
    env["SHIM_GOOD"] = json.dumps(good)
    env[auth_var] = "{}"
    (tmp / "reviewd").write_text(REVIEWD_SHIM)
    (tmp / model_shim).write_text(MUSE_SHIM if model_shim == "muse" else OPENCODE_SHIM)
    for name in ("reviewd", model_shim):
        (tmp / name).chmod(0o755)
    proc = subprocess.run(
        ["bash", str(HERE / script), "review this", "test-model", "2"],
        env=env, capture_output=True, text=True, timeout=120)
    invocations = [line for line in calls.read_text().splitlines()
                   if line.startswith(model_shim + " ")]
    return proc, invocations


class AdapterRetryTest(unittest.TestCase):
    def test_muse_recovers_on_retry(self):
        proc, calls = run_adapter("muse-bounded.sh", "muse", "MUSE_AUTH_JSON", BAD_REPORT, GOOD_REPORT)
        self.assertEqual(proc.returncode, 0, proc.stderr[-2000:])
        self.assertEqual(len(calls), 2)
        self.assertIn("rejected", calls[1])

    def test_muse_gives_up_after_two_attempts(self):
        proc, calls = run_adapter("muse-bounded.sh", "muse", "MUSE_AUTH_JSON", BAD_REPORT, BAD_REPORT)
        self.assertEqual(proc.returncode, 1)
        self.assertEqual(len(calls), 2)

    def test_muse_single_shot_success_runs_once(self):
        proc, calls = run_adapter("muse-bounded.sh", "muse", "MUSE_AUTH_JSON", GOOD_REPORT, GOOD_REPORT)
        self.assertEqual(proc.returncode, 0, proc.stderr[-2000:])
        self.assertEqual(len(calls), 1)

    def test_opencode_recovers_on_retry(self):
        proc, calls = run_adapter("opencode-bounded.sh", "opencode", "OPENCODE_AUTH_JSON", BAD_REPORT, GOOD_REPORT)
        self.assertEqual(proc.returncode, 0, proc.stderr[-2000:])
        self.assertEqual(len(calls), 2)
        self.assertIn("rejected", calls[1])

    def test_opencode_gives_up_after_two_attempts(self):
        proc, calls = run_adapter("opencode-bounded.sh", "opencode", "OPENCODE_AUTH_JSON", BAD_REPORT, BAD_REPORT)
        self.assertEqual(proc.returncode, 1)
        self.assertEqual(len(calls), 2)

    def test_opencode_single_shot_success_runs_once(self):
        proc, calls = run_adapter("opencode-bounded.sh", "opencode", "OPENCODE_AUTH_JSON", GOOD_REPORT, GOOD_REPORT)
        self.assertEqual(proc.returncode, 0, proc.stderr[-2000:])
        self.assertEqual(len(calls), 1)


if __name__ == "__main__":
    unittest.main()
