#!/usr/bin/env python3
"""Create scoring worksheets for pinned, source-inspected labels.
Inspect each finding against base/head, then fill verdict and matched bug IDs.
This command never assigns a true/false-positive verdict automatically.
"""
import argparse
import json
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('results', type=Path)
    parser.add_argument('--labels', type=Path, default=Path(__file__).with_name('labels.json'))
    args = parser.parse_args()
    labels = {v['head']: v for v in json.loads(args.labels.read_text()).values()
              if v['status'] == 'verified_by_source_inspection'}
    for metrics in args.results.glob('*/metrics.json'):
        run = metrics.parent
        output = run/'adjudication.json'
        if output.exists():
            continue
        meta = run/'reviewer-0/input/context.json'
        if not meta.exists():
            continue
        label = labels.get(json.loads(meta.read_text())['head'])
        if not label:
            continue
        report = json.loads((run/'report.json').read_text()) if (run/'report.json').exists() else {}
        packet = dict(control=label['control'], gold_bug_ids=[b['id'] for b in label['bugs']],
                      findings=[dict(index=i, verdict='pending', matches=[], rationale='')
                                for i, _ in enumerate(report.get('findings', []))])
        output.write_text(json.dumps(packet, indent=2)+'\n')
        print(output)


if __name__ == '__main__':
    main()
