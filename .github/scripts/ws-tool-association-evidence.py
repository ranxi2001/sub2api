"""Verify the same named actual-wire association cases before/after repair."""
import json,sys,importlib.util
from pathlib import Path
from collections import Counter
spec=importlib.util.spec_from_file_location('clean',Path(__file__).with_name('assert-readable-continuation.py'))
clean=importlib.util.module_from_spec(spec);spec.loader.exec_module(clean)
phase,status,path=sys.argv[1:];raw=Path(path).read_text();clean.check_clean(raw)
rows=[json.loads(line) for line in raw.splitlines() if line.startswith('{')]
expected='fail' if phase=='RED' else 'pass'
assert phase in ('RED','GREEN') and int(status)==(1 if phase=='RED' else 0)
root='TestOpenAIWSMixedOwnerToolAssociation'
leaves={f'{root}/{mode}/{kind}/{variant}' for mode in ('ctx_pool','http_bridge','passthrough') for kind in ('function','custom') for variant in ('normal','swapped')}
counts=Counter((r.get('Action'),r.get('Test')) for r in rows)
for name in leaves|{root}:
 assert counts['run',name]==1 and counts[expected,name]==1,name
 assert counts['pass',name]+counts['fail',name]==1,name
assert counts[expected,None]==1
for r in rows:
 if r.get('Action') in ('run','pass','fail'):
  assert r.get('Package')=='github.com/Wei-Shaw/sub2api/internal/handler'
  assert r.get('Test') in leaves|{root,None}
if phase=='RED':
 for name in leaves:
  out=''.join(r.get('Output','') for r in rows if r.get('Test')==name)
  assert 'ASSOCIATION_LOST' in out,name
print(phase, len(leaves), 'actual-wire association cases verified')
