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
