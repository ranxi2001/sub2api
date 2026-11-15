"""Require the same named business failures before the fix and completion after."""
import importlib.util
import json
from pathlib import Path
import sys

phase, code, filename = sys.argv[1:]
spec = importlib.util.spec_from_file_location('clean', Path(__file__).with_name('assert-readable-continuation.py'))
clean = importlib.util.module_from_spec(spec)
spec.loader.exec_module(clean)
raw = Path(filename).read_text()
clean.check_clean(raw)
events = [json.loads(line) for line in raw.splitlines() if line.startswith('{')]
root = 'TestOpenAI409NativeControlsContinuation'
leaves = {f'{root}/{account}/stream={stream}/{shape}'
          for account in ('oauth', 'apikey') for stream in ('true', 'false')
          for shape in ('web_search', 'image_aliases', 'image_bridge', 'image_strip', 'combined')}
terminal = {e['Test']: e['Action'] for e in events
            if e.get('Test') and e.get('Action') in ('pass', 'fail', 'skip')}
assert leaves <= terminal.keys(), 'missing named scenarios'
if phase == 'GREEN':
    assert int(code) == 0 and all(value == 'pass' for value in terminal.values()), terminal
else:
    assert phase == 'RED' and int(code) == 1, (phase, code)
    for name in leaves:
        expected = 'pass' if '/apikey/' in name and name.endswith('/web_search') else 'fail'
        assert terminal[name] == expected, (name, terminal[name], expected)
        if expected == 'fail':
            output = ''.join(e.get('Output', '') for e in events if e.get('Test') == name)
            assert 'HTTP_REBUILD_B_DISPATCH_MISSING' in output and 'encrypted_agent_recovery_wire_changed' in output, name
print(f'{phase}: verified {len(leaves)} named native-control scenarios')
