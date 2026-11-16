"""Select affected CI jobs and reuse successful checks of identical inputs."""
import argparse
import fnmatch
import functools
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import urllib.parse
import urllib.request

import yaml

ROOT = Path(__file__).resolve().parents[2]


def git(*args):
    return subprocess.check_output(['git', *args], cwd=ROOT)


def commit(sha):
    if not re.fullmatch(r'[0-9a-f]{40}', sha):
        raise ValueError('expected an immutable commit SHA')
    try:
        git('cat-file', '-e', sha + '^{commit}')
    except subprocess.CalledProcessError:
        git('fetch', '--no-tags', '--depth=1', 'origin', sha)
    return sha


def matches(path, patterns):
    # Patterns intentionally use fnmatch semantics: * also matches slashes.
    return any(fnmatch.fnmatchcase(path, pattern) for pattern in patterns)


@functools.lru_cache(maxsize=32)
def tree(sha):
    return dict(entry.split(b'\t', 1)[::-1] for entry in
                git('ls-tree', '-rz', '--full-tree', sha).split(b'\0') if entry)


def source_snapshot(sha, patterns):
    return {path: blob for path, blob in tree(sha).items()
            if not path.decode().endswith(('.md', '.rst'))
            and matches(path.decode(), patterns)}


@functools.lru_cache(maxsize=32)
def workflow_at(sha, filename):
    return yaml.safe_load(git('show', sha + ':.github/workflows/' + filename))


def definition(sha, filename, job):
    workflow = workflow_at(sha, filename)
    # The selection gate does not change the commands being verified.
    spec = {k: v for k, v in workflow['jobs'][job].items() if k not in ('needs', 'if')}
    return {'job': spec, 'env': workflow.get('env'), 'defaults': workflow.get('defaults'),
            'permissions': workflow.get('permissions')}


def checks_candidate(sha, filename, job):
    # Default checkout on pull_request tests the synthetic merge tree, whose
    # base can change independently. Only reuse explicitly pinned head checks.
    steps = workflow_at(sha, filename)['jobs'][job].get('steps', [])
    checkouts = [step for step in steps if step.get('uses', '').startswith('actions/checkout@')]
    expected = '${{ github.event.pull_request.head.sha || github.sha }}'
    return bool(checkouts) and all(step.get('with', {}).get('ref') == expected for step in checkouts)


def fingerprint(sha, filename, job, patterns):
    spec = definition(sha, filename, job)
    # actions:read only permits evidence lookup, not test/runtime behavior.
    if isinstance(spec['permissions'], dict):
        spec['permissions'] = {k: v for k, v in spec['permissions'].items() if k != 'actions'}
    payload = {'files': {p.decode(): b.decode() for p, b in source_snapshot(sha, patterns).items()},
               'definition': spec, 'patterns': patterns}
    return hashlib.sha256(json.dumps(payload, sort_keys=True).encode()).hexdigest()


def affected(base, head, filename, job, patterns):
    try:
        return fingerprint(base, filename, job, patterns) != fingerprint(head, filename, job, patterns)
    except (KeyError, subprocess.CalledProcessError):
        return True  # New job/workflow or missing old source requires a check.


class Evidence:
    def __init__(self, repo, token):
        self.repo = repo
        self.token = token

    def get(self, path, query=None):
        url = 'https://api.github.com/repos/' + self.repo + '/' + path
        if query:
            url += '?' + urllib.parse.urlencode(query)
        request = urllib.request.Request(url, headers={
            'Authorization': 'Bearer ' + self.token,
            'Accept': 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28',
        })
        with urllib.request.urlopen(request, timeout=15) as response:
            return json.load(response)

    def candidates(self, filename, event, event_name, head):
        # Same PR only. For a production merge, inspect its actual parents.
        if event_name == 'pull_request':
            pr = event['pull_request']
            rows = self.get('actions/workflows/' + filename + '/runs', {
                'event': 'pull_request', 'branch': pr['head']['ref'], 'per_page': 30,
            })['workflow_runs']
            return [r for r in rows if r['head_sha'] != head
                    and r.get('head_repository', {}).get('full_name') == pr['head']['repo']['full_name']
                    and any(p['number'] == pr['number'] for p in r.get('pull_requests', []))]
        parents = git('show', '-s', '--format=%P', head).decode().split()
        rows = []
        for parent in parents:
            rows += self.get('actions/workflows/' + filename + '/runs', {
                'head_sha': parent, 'per_page': 30,
            })['workflow_runs']
        return [r for r in rows if r['event'] in ('pull_request', 'push')]

    @functools.lru_cache(maxsize=30)
    def successful_jobs(self, run_id):
        jobs = []
        for page in range(1, 6):
            batch = self.get(f'actions/runs/{run_id}/jobs', {'per_page': 100, 'page': page})['jobs']
            jobs += batch
            if len(batch) < 100:
                break
        return {j['name'] for j in jobs if j['status'] == 'completed' and j['conclusion'] == 'success'}


def select(filename, scopes, event_name, event, head, evidence=None, force=False):
    if force or event_name in ('workflow_dispatch', 'schedule') or os.environ.get('GITHUB_REF', '').startswith('refs/tags/'):
        return {job: (True, 'explicit full verification') for job in scopes}
    if event_name == 'pull_request':
        base = commit(event['pull_request']['base']['sha'])
        base = git('merge-base', base, head).decode().strip()
    else:
        base = event.get('before', '')
        if not base or set(base) == {'0'}:
            return {job: (True, 'no comparison base') for job in scopes}
        base = commit(base)
    selected = {job: (affected(base, head, filename, job, patterns), 'inputs changed')
                for job, patterns in scopes.items()}
    selected = {job: value if value[0] else (False, 'inputs unchanged') for job, value in selected.items()}
    if evidence is None or not any(value[0] for value in selected.values()):
        return selected
    try:
        candidates = evidence.candidates(filename, event, event_name, head)
        for run in candidates:
            if not any(v[0] for v in selected.values()):
                break
            old = commit(run['head_sha'])
            for job, patterns in scopes.items():
                if not selected[job][0]:
                    continue
                try:
                    same = fingerprint(old, filename, job, patterns) == fingerprint(head, filename, job, patterns)
                except (KeyError, subprocess.CalledProcessError):
                    continue
                if (same and checks_candidate(old, filename, job)
                        and checks_candidate(head, filename, job)
                        and job in evidence.successful_jobs(run['id'])):
                    selected[job] = (False, f"reused successful run {run['id']} at {old}")
    except Exception as error:
        # Missing token, API limits or unavailable commits must never skip tests.
        print(f'Evidence lookup unavailable ({type(error).__name__}); affected checks will run')
    return selected


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('workflow')
    args = parser.parse_args()
    config = json.loads((ROOT / '.github/ci/scopes.json').read_text())
    scopes = config[args.workflow]
    event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text())
    name = os.environ['GITHUB_EVENT_NAME']
    head = commit(event['pull_request']['head']['sha'] if name == 'pull_request' else os.environ['GITHUB_SHA'])
    token = os.environ.get('GH_TOKEN', '')
    evidence = Evidence(os.environ['GITHUB_REPOSITORY'], token) if token else None
    selected = select(args.workflow, scopes, name, event, head, evidence)
    with open(os.environ['GITHUB_OUTPUT'], 'a') as output:
        for job, (run, reason) in selected.items():
            output.write(f'{job}={str(run).lower()}\n')
            print(f'{job}: {"run" if run else "skip"} ({reason})')
    with open(os.environ['GITHUB_STEP_SUMMARY'], 'a') as summary:
        summary.write('| Check | Decision | Reason |\n| --- | --- | --- |\n')
        for job, (run, reason) in selected.items():
            summary.write(f'| {job} | {"run" if run else "skip"} | {reason} |\n')


if __name__ == '__main__':
    main()
