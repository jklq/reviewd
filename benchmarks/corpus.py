#!/usr/bin/env python3
"""Collect real PR candidates; pin the revision a finding actually refers to.

Labels are deliberately stored outside case workspaces. Upstream bot comments
are hypotheses until someone adjudicates source and (where available) a fix.
"""
import argparse
import hashlib
import io
import json
from pathlib import Path
import subprocess
import shutil
import tempfile
import time
import tarfile
import urllib.request

UPSTREAM = 'Entelligence-AI/benchmark-maxxing'
SOURCES = [('coderabbit', 'oven-sh-bun'), ('coderabbit', 'appwrite-appwrite'),
           ('coderabbit', 'pingcap-tidb'), ('coderabbit', 'unkeyed-unkey'),
           ('greptile', 'BerriAI-litellm')]
ROOT = Path(__file__).resolve().parents[1]
CACHE = ROOT / '.benchmarks'


def api(path):
    return json.loads(subprocess.check_output(['gh', 'api', path]))


def save(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2) + '\n')


def download(url):
    with urllib.request.urlopen(url, timeout=120) as response:
        data = response.read(256 * 1024 * 1024 + 1)
    if len(data) > 256 * 1024 * 1024:
        raise ValueError('download exceeds 256 MiB')
    return data


def bucket(lines):
    return 'small' if lines < 100 else 'medium' if lines < 500 else 'large' if lines < 2000 else 'very-large'


def collect():
    revision = api(f'repos/{UPSTREAM}/commits/main')['sha']
    cases = []
    for tool, name in SOURCES:
        docs = []
        for kind in ('data', 'ground_truth'):
            url = f'https://raw.githubusercontent.com/{UPSTREAM}/{revision}/{kind}/{tool}/{name}.json'
            raw = download(url)
            doc = json.loads(raw)
            save(CACHE / 'upstream' / f'{kind}-{name}.json', doc)
            docs.append(doc)
        data, truth = docs
        labels = {p['pr_number']: p['review_comments'] for p in truth['prs']}
        # Select one candidate in every available size stratum, favoring runtime issues.
        selected = {}
        for pr in sorted(data['prs'], key=lambda p: p['additions'] + p['deletions']):
            comments = [c for c in labels.get(pr['number'], [])
                        if c.get('commit_id') and ('Potential issue' in c['body'] or tool == 'greptile')
                        and not c.get('in_reply_to_id')]
            if not comments:
                continue
            size = bucket(pr['additions'] + pr['deletions'])
            if size in selected:
                continue
            first = comments[0]
            head = first.get('original_commit_id') or first['commit_id']
            case_id = name + '-' + str(pr['number'])
            evidence = [c for c in comments if (c.get('original_commit_id') or c['commit_id']) == head]
            save(CACHE / 'labels' / f'{case_id}.json', {
                'status': 'candidate', 'upstream': UPSTREAM, 'revision': revision,
                'comments': evidence, 'warning': 'Unverified bot claims; not ground truth.'})
            selected[size] = dict(id=case_id, repo=data['repo'], number=pr['number'],
                                 head=head, base_ref=pr['base_sha'], title=pr['title'],
                                 body=pr.get('body') or '', size=size,
                                 size_basis='upstream final PR; recomputed when prepared',
                                 changed_lines=pr['additions'] + pr['deletions'],
                                 changed_files=pr['changed_files'], label_status='candidate',
                                 source=f"https://github.com/{data['repo']}/pull/{pr['number']}",
                                 upstream_revision=revision)
        cases.extend(selected.values())
    save(ROOT / 'benchmarks' / 'corpus.json', cases)
    print(f'Collected {len(cases)} pinned real PR candidates; labels need adjudication.')


def snapshot(repo, sha, destination):
    raw = download(f'https://codeload.github.com/{repo}/tar.gz/{sha}')
    omitted = []
    with tarfile.open(fileobj=io.BytesIO(raw), mode='r:gz') as archive:
        members = archive.getmembers()
        if sum(m.size for m in members) > 1024**3:
            raise ValueError('snapshot exceeds 1 GiB expanded')
        # No links or devices: repository data must not escape the snapshot.
        for member in members:
            parts = Path(member.name).parts[1:]
            if not parts:
                continue
            if '..' in parts or any(p.startswith('/') for p in parts):
                raise ValueError('unsafe archive path')
            target = destination.joinpath(*parts)
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
            elif member.isfile():
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(archive.extractfile(member).read())
            else:
                omitted.append(str(Path(*parts)))
    return dict(sha256=hashlib.sha256(raw).hexdigest(), omitted_special_paths=omitted)


def prepare(case_id):
    case = next(c for c in json.loads((ROOT / 'benchmarks/corpus.json').read_text()) if c['id'] == case_id)
    destination = CACHE / 'cases' / (case_id + '-' + case['head'][:8])
    if destination.exists():
        raise ValueError(f'{destination} already exists; refusing to overwrite')
    started = time.monotonic()
    comparison = api(f"repos/{case['repo']}/compare/{case['base_ref']}...{case['head']}")
    files = comparison.get('files', [])
    if len(files) >= 300 or not files:
        raise ValueError('GitHub compare file cap reached or empty diff; use git-based import')
    allowed = {'filename', 'previous_filename', 'status', 'patch', 'additions', 'deletions'}
    files = [{k: v for k, v in f.items() if k in allowed} for f in files]
    base = comparison['merge_base_commit']['sha']
    final_destination = destination
    destination.parent.mkdir(parents=True, exist_ok=True)
    temporary = tempfile.TemporaryDirectory(prefix=".preparing-", dir=destination.parent)
    destination = Path(temporary.name)
    hashes = {}
    for name, sha in [('base', base), ('head', case['head'])]:
        path = destination / name
        path.mkdir()
        hashes[name] = snapshot(case['repo'], sha, path)
    save(destination / 'files.json', files)
    save(destination / 'context.json', dict(repo=case['repo'], number=case['number'],
         title=case['title'], body=case['body'], head=case['head'], merge_base=base,
         coverage_limitations=[f'{name}: {len(info["omitted_special_paths"])} symlinks/special paths omitted' for name,info in hashes.items() if info['omitted_special_paths']]))
    lines = sum(f['additions'] + f['deletions'] for f in files)
    save(destination / 'provenance.json', dict(case=case, merge_base=base,
         snapshots=hashes, preparation_seconds=time.monotonic()-started, changed_lines=lines, changed_files=len(files),
         size=bucket(lines), omitted_patches=[f['filename'] for f in files if not f.get('patch')]))
    destination.rename(final_destination)
    temporary.cleanup()
    print(final_destination)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['collect', 'prepare'])
    parser.add_argument('case', nargs='?')
    args = parser.parse_args()
    collect() if args.command == 'collect' else prepare(args.case)
