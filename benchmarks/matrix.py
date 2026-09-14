#!/usr/bin/env python3
"""Run a reproducible randomized matrix, one PR at a time. No automatic retries.
Each launch is recorded first; interruption is visible and resume will not silently
rerun it. Use a new output directory for a separate trial.
"""
import argparse
import json
from pathlib import Path
import random
import subprocess


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--cases', nargs='+', required=True, type=Path)
    p.add_argument('--strategies', nargs='+', default=['baseline','single','preload','structural','targeted','sharded'])
    p.add_argument('--repeats', type=int, default=3)
    p.add_argument('--seed', type=int, default=20260914)
    p.add_argument('--timeout', default='6m')
    p.add_argument('--config', default='/etc/reviewd/reviewd.json')
    p.add_argument('--output', required=True, type=Path)
    a = p.parse_args()
    if a.repeats < 1:
        p.error('repeats must be positive')
    a.output.mkdir(parents=True, exist_ok=False)
    plan = [(str(c.resolve()),s,r) for c in a.cases for s in a.strategies for r in range(1,a.repeats+1)]
    random.Random(a.seed).shuffle(plan)
    (a.output/'plan.json').write_text(json.dumps(dict(seed=a.seed, timeout=a.timeout,plan=plan),indent=2)+'\n')
    for i,(case,strategy,repeat) in enumerate(plan):
        run = a.output/f'{i:03d}-{Path(case).name}-{strategy}-{repeat}'
        command = ['./bin/reviewbench','--config',a.config,'--case',case,'--strategy',strategy,'--timeout',a.timeout,'--output',str(run.resolve())]
        launch = a.output/(run.name+'.launch.json')
        launch.write_text(json.dumps(dict(command=command,status='started'),indent=2)+'\n')
        print(f'{i+1}/{len(plan)} {run.name}',flush=True)
        result = subprocess.run(command, check=False)
        launch.write_text(json.dumps(dict(command=command,status='finished',exit_code=result.returncode),indent=2)+'\n')


if __name__ == '__main__':
    main()
