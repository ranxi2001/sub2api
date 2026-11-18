#!/usr/bin/env python3
"""Match unrelated imported tests to an entire historical production overlay.

No production code or selected recovery oracle is changed. Current-head GREEN
continues to run every imported fixture. Fail closed unless both gateway entry
points are byte-for-byte historical, and each touched fixture is candidate or
already historical. Record every change for the RED artifact.
"""
import pathlib
import subprocess
import sys

baseline, tree, candidate = sys.argv[1:]
root = pathlib.Path(tree).resolve()

def git(*args, optional=False):
    result = subprocess.run(['git', '-C', str(root), *args], capture_output=True)
    if result.returncode and not optional:
        raise RuntimeError(result.stderr.decode())
    return None if result.returncode else result.stdout

def blob(ref, name):
    return git('show', f'{ref}:{name}', optional=True)

contracts = ('backend/internal/handler/openai_gateway_handler.go',
             'backend/internal/service/openai_gateway_service.go')
for name in contracts:
    old = blob(baseline, name)
    current = root / name
    if old is None or not current.exists() or current.read_bytes() != old:
        sys.exit(0)  # Partial overlays have their own dependency adapters.
if all(blob(baseline, name) == blob(candidate, name) for name in contracts):
    sys.exit(0)  # Never alter a clean candidate.

# PR909's independent request-TTL tests require production APIs absent from
# these complete historical overlays. This is their sole overlay owner; keep
# the selected recovery oracles and every candidate GREEN fixture unchanged.
ttl_fixture = 'backend/internal/service/gateway_cache_ttl_request_billing_test.go'
ttl_source = 'backend/internal/service/gateway_cache_ttl_request.go'
ttl_candidate = blob(candidate, ttl_fixture)
if ttl_candidate is not None and blob(baseline, ttl_source) is None:
    if blob(baseline, ttl_fixture) is not None:
        raise RuntimeError(f'refusing to exclude baseline TTL fixture: {ttl_fixture}')
    account = 'backend/internal/service/account.go'
    old_account = blob(baseline, account)
    if (old_account is None or b'IsCacheTTLBillingFromRequestEnabled' in old_account
            or (root / account).read_bytes() != old_account):
        raise RuntimeError('request-TTL fixture requires the exact historical Account contract')
    if (root / ttl_source).exists() or (root / ttl_source).is_symlink():
        raise RuntimeError(f'request-TTL implementation unexpectedly present: {ttl_source}')
    path = root / ttl_fixture
    candidate_blob = git('rev-parse', f'{candidate}:{ttl_fixture}').decode().strip()
    if path.is_symlink():
        raise RuntimeError(f'refusing to exclude symlink TTL fixture: {ttl_fixture}')
    if path.exists():
        if not path.is_file() or path.read_bytes() != ttl_candidate:
            raise RuntimeError(f'refusing to exclude modified TTL fixture: {ttl_fixture}')
        actual_blob = git('hash-object', str(path)).decode().strip()
        if actual_blob != candidate_blob:
            raise RuntimeError(f'request-TTL fixture blob mismatch: {ttl_fixture}')
        path.unlink()
        action = 'excluded'
    else:
        # The HTTP2 and replay helper chains can invoke this same owner twice.
        # Accept only the tracked deletion of this exact candidate fixture.
        deleted = git('diff', '--name-only', '--diff-filter=D', candidate, '--', ttl_fixture).decode().strip()
        if deleted != ttl_fixture:
            raise RuntimeError(f'request-TTL fixture deletion mismatch: {ttl_fixture}')
        actual_blob = 'absent'
        action = 'already-excluded'
    account_blob = git('rev-parse', f'{baseline}:{account}').decode().strip()
    print(f'independent request-TTL fixture baseline={baseline} candidate={candidate} path={ttl_fixture} baseline_blob=absent candidate_blob={candidate_blob} actual_blob={actual_blob} account_blob={account_blob} action={action} restored=absent')

manifest = pathlib.Path(__file__).with_name('upstream-v0222-red-fixtures.txt')
for name in manifest.read_text().splitlines():
    if not name or name.startswith('#'):
        continue
    assert name.startswith('backend/') and name.endswith('_test.go'), name
    path = root / name
    old, new = blob(baseline, name), blob(candidate, name)
    current = path.read_bytes() if path.exists() else None
    if current == old:
        continue
    if current != new:
        raise RuntimeError(f'refusing to replace modified fixture: {name}')
    if old is None:
        path.unlink()
    else:
        path.write_bytes(old)
    print(f'upstream independent fixture baseline={baseline} candidate={candidate} path={name} restored={"baseline" if old else "absent"}')
