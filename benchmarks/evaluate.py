#!/usr/bin/env python3
"""Summarize latency and explicitly adjudicated findings. Never infer false positives
from absence in an incomplete label set. Failures remain in success/recall denominators.
"""
import argparse
from collections import defaultdict
import json
import math
from pathlib import Path
import statistics


def quantile(values, q):
    return sorted(values)[max(0, math.ceil(len(values) * q) - 1)] if values else None


def event_metrics(run):
    counts = defaultdict(float)
    seen = set()
    for log in run.glob('*/output/harness.log'):
        muse_tasks = {}
        for line in log.read_text(errors='replace').splitlines():
            try:
                event = json.loads(line)
            except ValueError:
                continue
            if not isinstance(event, dict):
                continue
            if 'payload_type' in event:
                payload = event.get('payload', {})
                if not isinstance(payload, dict):
                    continue
                stream = event.get('stream', {})
                if not isinstance(stream, dict):
                    stream = {}
                identity = (str(log), stream.get('id'), event.get('id'))
                if event.get('id') and identity in seen:
                    continue
                if event.get('id'):
                    seen.add(identity)
                kind = event['payload_type']
                task = payload.get('event', {})
                if not isinstance(task, dict):
                    task = {}
                task_id = task.get('task_id')
                if kind == 'task.lifecycle.proposed':
                    muse_tasks[task_id] = task.get('task_kind', '')
                elif kind == 'task.lifecycle.started' and muse_tasks.get(task_id, '').startswith('model.'):
                    counts['model_steps'] += 1
                elif kind == 'tool.result':
                    counts['tool_calls'] += 1
                # Muse's recorded_at values in this image are not wall-clock
                # timestamps. Use the host spans for latency; do not infer token
                # totals or costs when the event stream does not expose them.
                continue
            part = event.get('part', {})
            if not isinstance(part, dict):
                continue
            identity = (str(log), part.get('id'))
            if part.get('id') and identity in seen:
                continue
            seen.add(identity)
            if event.get('type') == 'tool_use':
                counts['tool_calls'] += 1
            if event.get('type') == 'step_finish':
                counts['model_steps'] += 1
                counts['reported_cost'] += part.get('cost', 0)
                tokens = part.get('tokens', {})
                for key in ('input', 'output', 'reasoning'):
                    counts[key + '_tokens'] += tokens.get(key, 0)
                for key, value in tokens.get('cache', {}).items():
                    counts['cache_' + key + '_tokens'] += value
    return dict(counts) if counts else None


def summarize(root):
    groups = defaultdict(list)
    for metrics in sorted(root.glob('*/metrics.json')):
        run = metrics.parent
        m = json.loads(metrics.read_text())
        report = json.loads((run / 'report.json').read_text()) if (run / 'report.json').exists() else None
        adjudication = json.loads((run / 'adjudication.json').read_text()) if (run / 'adjudication.json').exists() else None
        groups[m['strategy']].append((m, report, adjudication, event_metrics(run)))
    results = {}
    for strategy, rows in groups.items():
        successful = [m['seconds'] for m, _, _, _ in rows if not m.get('error')]
        quality = [a for _, _, a, _ in rows if a]
        tp = fp = matched = gold = controls = noisy_controls = pending = 0
        for m, report, a, _ in rows:
            if not a:
                pending += 1
                continue
            findings = (report or {}).get('findings', [])
            verdicts = a.get('findings', [])
            indices = [v['index'] for v in verdicts]
            if len(indices) != len(set(indices)) or any(i < 0 or i >= len(findings) for i in indices):
                raise ValueError('invalid or duplicate adjudication finding index')
            gold_ids = set(a.get('gold_bug_ids', []))
            detected = set()
            for v in verdicts:
                if v['verdict'] not in ('true_positive', 'false_positive', 'out_of_scope', 'pending'):
                    raise ValueError('unknown finding verdict')
                matches = set(v.get('matches', []))
                if not matches <= gold_ids:
                    raise ValueError('match references an unknown gold bug')
                if v['verdict'] == 'true_positive':
                    tp += 1
                    detected |= matches
                elif v['verdict'] == 'false_positive':
                    fp += 1
            matched += len(detected)
            gold += len(gold_ids)
            if a.get('control') and not m.get('error'):
                controls += 1
                noisy_controls += bool(findings)
            pending += len(indices) != len(findings) or any(v['verdict'] == 'pending' for v in verdicts)
        precision = tp / (tp + fp) if tp + fp else None
        recall = matched / gold if gold else None
        results[strategy] = dict(runs=len(rows), successes=len(successful), failures=len(rows)-len(successful),
            success_rate=len(successful)/len(rows), median_success_seconds=statistics.median(successful) if successful else None,
            p95_success_seconds=quantile(successful, .95),
            under_180_seconds=sum(not m.get('error') and m['seconds'] <= 180 for m, _, _, _ in rows)/len(rows),
            adjudicated_runs=len(quality), runs_with_pending_adjudication=pending,
            adjudicated_precision=precision, known_bug_recall=recall,
            gold_bug_opportunities=gold, matched_bug_opportunities=matched,
            successful_controls=controls, control_finding_rate=noisy_controls/controls if controls else None,
            events=[e for _, _, _, e in rows if e is not None])
    return results


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('results', type=Path)
    args = parser.parse_args()
    print(json.dumps(summarize(args.results), indent=2))
