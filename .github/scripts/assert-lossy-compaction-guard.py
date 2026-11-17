"""Only the caught owner=nil assertion is this separate guard counterexample."""
import json
import re
import sys
from pathlib import Path

status, path = sys.argv[1:]
raw = Path(path).read_text()
rows = [json.loads(line) for line in raw.splitlines() if line.startswith("{")]
root = "TestOpenAILossyProtocolOwnerlessCompactionRefused"
package = "github.com/Wei-Shaw/sub2api/internal/service"
assert int(status) == 1
assert sum(r.get("Action") == "run" and r.get("Test") == root and r.get("Package") == package for r in rows) == 1
assert sum(r.get("Action") == "fail" and r.get("Test") == root and r.get("Package") == package for r in rows) == 1
assert sum(r.get("Action") == "fail" and r.get("Test") is None and r.get("Package") == package for r in rows) == 1
assert not any(r.get("Action") == "skip" or (r.get("Action") == "fail" and (r.get("Package") != package or r.get("Test") not in (root, None))) for r in rows)
output = "".join(r.get("Output", "") for r in rows if r.get("Test") == root)
assert all(s in output for s in ("LOSSY_OWNERLESS_COMPACTION_GUARD", "should not panic", "checkOpenAIAgentRecoveryCompaction", "invalid memory address"))
assert not re.search(r"build failed|setup failed|undefined|no tests to run|data race|test timed out|^panic:", raw + output, re.I | re.M)
print("RED guard only: caught ownerless compaction assertion; not tool-loop evidence")
