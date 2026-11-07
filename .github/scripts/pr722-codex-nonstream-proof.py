"""Validate exact behavioral RED/GREEN results from go test -json, never exit alone."""

import itertools
import json
from pathlib import Path
import sys


mode, log_path, exit_code = sys.argv[1:]
assert mode in {"red", "green"}, mode
assert int(exit_code) == (1 if mode == "red" else 0), exit_code
prefix = "TestOpenAIGatewayService_OAuthLike"
matrix = prefix + "NonStreamTerminalMatrix"
stream = prefix + "StreamTrueStillStreams"
literal = prefix + "NonStreamJSONLiteralFramingPreserved"
terminals = ("completed", "incomplete", "cancelled", "canceled")
switches = ("passthrough_on", "passthrough_off")
leaves = {
    "/".join((matrix, *parts))
    for parts in itertools.product(
        ("OAuth", "SetupToken"), switches, ("header_present", "header_missing"), terminals
    )
}
leaves.update(
    "/".join((stream, *parts))
    for parts in itertools.product(("oauth", "setup-token"), switches)
)
leaves.update(
    "/".join((literal, *parts))
    for parts in itertools.product(("oauth", "setup-token", "apikey"), switches)
)
expected_failures = {
    "/".join((matrix, "SetupToken", "passthrough_on", "header_missing", terminal))
    for terminal in terminals
} if mode == "red" else set()
events = []
for line in Path(log_path).read_text().splitlines():
    if line.startswith("go: downloading "):
        continue  # Dependency download notices on stderr are not test results.
    events.append(json.loads(line))
output = "".join(event.get("Output", "") for event in events)
for forbidden in ("[build failed]", "undefined:", "syntax error", "no tests to run", "panic:", "WARNING: DATA RACE"):
    assert forbidden not in output, forbidden
assert not any(event["Action"] == "skip" for event in events), "skipped test"
results = {}
for event in events:
    if "Test" in event and event["Action"] in {"pass", "fail"}:
        assert event["Test"] not in results, "duplicate test result"
        results[event["Test"]] = event["Action"]
assert set(results) == leaves | {matrix, stream, literal}, (set(results) - leaves, leaves - set(results))
for name in sorted(leaves):
    expected = "fail" if name in expected_failures else "pass"
    assert results[name] == expected, (name, results[name], expected)
    if expected == "fail":
        diagnostic = "".join(event.get("Output", "") for event in events if event.get("Test") == name)
        assert "Codex non-stream terminal must be a single JSON object; body=event: response." in diagnostic, diagnostic
        assert "data: {" in diagnostic and "Should be true" in diagnostic, diagnostic
    print(f"{mode.upper()} {expected.upper()} {name}")
assert results[matrix] == ("fail" if mode == "red" else "pass")
assert results[stream] == results[literal] == "pass"
package_results = [event["Action"] for event in events if "Test" not in event and event["Action"] in {"pass", "fail"}]
assert package_results == ["fail" if mode == "red" else "pass"], package_results
print(json.dumps({"mode": mode, "leaf_count": len(leaves), "behavioral_failures": len(expected_failures), "passed_controls": len(leaves) - len(expected_failures)}))
