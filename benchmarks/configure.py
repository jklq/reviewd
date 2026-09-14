#!/usr/bin/env python3
"""Make a private replay config with OpenCode JSON events; preserve live config."""
import argparse
import json
import os
from pathlib import Path


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--source', type=Path, default=Path('/etc/reviewd/reviewd.json'))
    p.add_argument('--output', type=Path, default=Path('.benchmarks/config.json'))
    a = p.parse_args()
    source = a.source.resolve()
    config = json.loads(source.read_text())
    def absolute(value):
        return str((source.parent / value).resolve()) if value else value
    for key in ('data_dir', 'private_key_file', 'agent_binary'):
        if config.get(key):
            config[key] = absolute(config[key])
    for credential in config.get('credentials', {}).values():
        credential['state_file'] = absolute(credential['state_file'])
    for harness in config['harnesses'].values():
        harness['command'] = [arg.replace('opencode run --auto', 'opencode run --format json --auto') for arg in harness['command']]
    a.output.parent.mkdir(parents=True, exist_ok=True)
    descriptor = os.open(a.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, 'w') as output:
        json.dump(config, output, indent=2)
        output.write('\n')
    print(a.output)


if __name__ == '__main__':
    main()
