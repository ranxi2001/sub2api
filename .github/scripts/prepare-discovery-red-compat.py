"""Compile-only closure for selective historical recovery overlays.

Keep every byte of the pinned historical implementation. Add only the layout
field and inert context declarations required by unchanged candidate callers;
old managers never mint a discovery handoff. One old manager also lacks the
pure opaque-field scanner used by the candidate discovery fingerprint.
No test, historical executable statement, or behavior validator is replaced.
"""
import argparse
import hashlib
import json
import re
import subprocess
from pathlib import Path

SERVICE = "backend/internal/service/"
PROVENANCE = SERVICE + "openai_tool_provenance.go"
DELIVERY = SERVICE + "openai_agent_message_delivery.go"
MANAGER = SERVICE + "openai_ws_agent_recovery.go"
LEGACY_ANCHOR_BASELINE = "e7f54240d83765c90a98e961ad5e33b0fa35ab56"
LEGACY_ANCHOR_BLOB = "41fc424e64fdf029addeaeff9213f55e150a7a21"


def git(tree, *args, data=None):
    return subprocess.check_output(["git", "-C", str(tree), *args], input=data)


def blob(data):
    return hashlib.sha1(b"blob " + str(len(data)).encode() + b"\0" + data).hexdigest()


def unique(source, pattern):
    matches = re.findall(pattern, source, re.M | re.S)
    if len(matches) != 1:
        raise ValueError("missing or ambiguous candidate compatibility declaration")
    return matches[0]


def overlay(path, original, candidate):
    if path == DELIVERY:
        declaration = b"type openAIAgentRecoveryDelivery struct {\n"
        if b"discoveryBindings" in original:
            return original, []
        field = unique(candidate, rb"^\tdiscoveryBindings[ \t]+map\[string\]openAIToolProofSnapshot\n")
        if original.count(declaration) != 1:
            raise ValueError("historical delivery layout is ambiguous")
        return original.replace(declaration, declaration + field, 1), [field]
    additions = []
    if b"type openAIWSDiscoveryHandoffKey " not in original:
        additions.append(unique(candidate, rb"^type openAIWSDiscoveryHandoffKey struct\{\}\n"))
    if b"type openAIWSDiscoveryHandoff " not in original:
        declaration = unique(candidate, rb"^type openAIWSDiscoveryHandoff struct \{\n[^}]*^\}\n")
        # This is a data declaration only, never the candidate recovery methods.
        if b"func" in declaration:
            raise ValueError("executable declaration in compatibility layout")
        additions.append(declaration)
    if b"func openAIWSHasOpaqueField(" not in original:
        if b'"github.com/tidwall/gjson"' not in original:
            raise ValueError("historical manager lacks scanner's existing import")
        scanner = unique(candidate, rb"^func openAIWSHasOpaqueField\(value gjson.Result, depth int\) bool \{\n.*?^\}\n")
        additions.append(scanner)
    return original + b"".join(b"\n" + value for value in additions), additions


def prepare(baseline, tree, candidate, targets, fixtures=()):
    tree = Path(tree).resolve()
    baseline = git(tree, "rev-parse", baseline + "^{commit}").decode().strip()
    candidate = git(tree, "rev-parse", candidate + "^{commit}").decode().strip()
    if git(tree, "rev-parse", "HEAD").decode().strip() != candidate:
        raise ValueError("compatibility requires a disposable candidate checkout")
    caller = git(tree, "show", candidate + ":" + PROVENANCE)
    if (tree / PROVENANCE).read_bytes() != caller:
        raise ValueError("candidate provenance caller was changed; use a coherent full baseline instead")
    fixture_blobs = {}
    for path in fixtures:
        expected = git(tree, "show", candidate + ":" + path)
        if (tree / path).read_bytes() != expected:
            raise ValueError("selected candidate oracle changed: " + path)
        fixture_blobs[path] = blob(expected)
    rows, writes = [], []
    for path in targets:
        old = git(tree, "show", baseline + ":" + path)
        current = git(tree, "show", candidate + ":" + path)
        actual = (tree / path).read_bytes()
        if actual == current:
            rows.append({"path": path, "status": "unchanged candidate", "candidate_blob": blob(current)})
            continue
        expected = old
        if path == MANAGER and baseline == LEGACY_ANCHOR_BASELINE and blob(actual) == LEGACY_ANCHOR_BLOB:
            expected = old.replace(b"type openAIWSAgentRecovery struct {\n", b"type openAIWSAgentRecovery struct {\n\tcompletedBridge openAIWSBridgeResponseAnchor\n", 1)
        if actual != expected:
            raise ValueError("dirty/unidentified historical source: " + path)
        result, additions = overlay(path, actual, current)
        # Removing only the declared additions must recover the exact preimage.
        restored = result
        for addition in additions:
            restored = restored.replace(addition if path == DELIVERY else b"\n" + addition, b"", 1)
        if restored != actual:
            raise ValueError("compatibility changed historical executable bytes")
        rows.append({"path": path, "baseline_blob": blob(old), "candidate_blob": blob(current),
                     "preimage_blob": blob(actual), "overlay_blob": blob(result),
                     "added_candidate_fragments_sha256": [hashlib.sha256(a).hexdigest() for a in additions],
                     "historical_bytes_preserved": True})
        writes.append((tree / path, result))
    # Validate the entire overlay before writing anything.
    for path, data in writes:
        path.write_bytes(data)
    return {"baseline": baseline, "candidate": candidate,
            "candidate_tree": git(tree, "rev-parse", candidate + "^{tree}").decode().strip(),
            "baseline_tree": git(tree, "rev-parse", baseline + "^{tree}").decode().strip(),
            "candidate_caller": {"path": PROVENANCE, "blob": blob(caller)},
            "selected_candidate_oracles": fixture_blobs, "compatibility": rows}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("baseline")
    parser.add_argument("tree")
    parser.add_argument("candidate")
    parser.add_argument("--target", choices=("delivery", "ws", "all"), required=True)
    parser.add_argument("--fixture", action="append", default=[])
    parser.add_argument("--manifest")
    args = parser.parse_args()
    selected = [DELIVERY] if args.target == "delivery" else [MANAGER] if args.target == "ws" else [DELIVERY, MANAGER]
    result = prepare(args.baseline, args.tree, args.candidate, selected, args.fixture)
    text = json.dumps(result, indent=2) + "\n"
    if args.manifest:
        Path(args.manifest).write_text(text)
    print(text, end="")
