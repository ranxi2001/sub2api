"""F1 must fail because an unissued matching pair actually reaches upstream."""
import importlib.util
import json
import sys
from collections import Counter
from pathlib import Path

spec = importlib.util.spec_from_file_location("clean", Path(__file__).with_name("assert-readable-continuation.py"))
clean = importlib.util.module_from_spec(spec)
spec.loader.exec_module(clean)
phase, status, path = sys.argv[1:]
raw = Path(path).read_text()
clean.check_clean(raw)
root = "TestOpenAIWSMixedOwnerToolProvenance"
leaves = {root + "/phantom_" + kind for kind in ("function", "custom")}
rows = [json.loads(line) for line in raw.splitlines() if line.startswith("{")]
expected = "fail" if phase == "RED" else "pass"
assert phase in ("RED", "GREEN") and int(status) == (1 if phase == "RED" else 0)
counts = Counter((r.get("Action"), r.get("Test")) for r in rows)
for name in leaves | {root}:
    assert counts["run", name] == 1 and counts[expected, name] == 1, name
    assert counts["pass", name] + counts["fail", name] == 1, name
assert counts[expected, None] == 1
for row in rows:
    if row.get("Action") in ("run", "pass", "fail"):
        assert row.get("Package") == "github.com/Wei-Shaw/sub2api/internal/handler"
        assert row.get("Test") in leaves | {root, None}
if phase == "RED":
    for name in leaves:
        output = "".join(r.get("Output", "") for r in rows if r.get("Test") == name)
        assert "F1_PHANTOM_PAIR_REACHED_UPSTREAM" in output, name
        assert "F1_UNTRUSTED_MATCHED_PAIR_DISPATCHED" in output, name
print(f"F1 {phase}: both actual authenticated handler phantom-pair scenarios verified")
