"""CI-only unsafe source mutants; never modify the clean candidate checkout."""

import hashlib
import json
import os
import sys
from pathlib import Path


def main():
    root, mode = Path(sys.argv[1]).resolve(), sys.argv[2]
    temporary = os.environ.get("RUNNER_TEMP")
    if os.environ.get("GITHUB_ACTIONS") != "true" or not temporary or not root.is_relative_to(Path(temporary).resolve()):
        raise SystemExit("source mutations require a disposable Actions RUNNER_TEMP checkout")
    target = root / "backend/internal/service/openai_agent_message_recovery.go"
    original = target.read_text()
    begin = original.index("func (s *OpenAIGatewayService) WithOpenAIAgentMessageRecovery(")
    end = original.index("func openAIAgentRecoveryFromContext(")
    function = original[begin:end]
    if mode == "newest-only":
        anchor = "\tstate := &s.agentMessageRecovery\n"
        if function.count(anchor) != 1:
            raise SystemExit("newest-only mutation anchor changed")
        function = function.replace(anchor, "\tif len(request.keys) > 0 {\n\t\trequest.keys = request.keys[len(request.keys)-1:]\n\t}\n" + anchor)
    elif mode == "break-on-owner-mismatch":
        loop = "for _, key := range request.keys {"
        anchor = "request.mixedOwners = true"
        if function.count(loop) != 1 or function.count(anchor) != 1:
            raise SystemExit("owner-mismatch mutation anchor changed")
        function = function.replace(loop, "for index, key := range request.keys {")
        # Model the unsafe shortcut retaining only the inspected prefix for
        # both rebuild and final revalidation. The later fenced item is lost.
        function = function.replace(anchor, anchor + "\n\t\t\trequest.keys = request.keys[:index+1]\n\t\t\tbreak")
    else:
        raise SystemExit("unknown source mutation")
    mutated = original[:begin] + function + original[end:]
    if mutated == original:
        raise SystemExit("source mutant did not change the candidate")
    target.write_text(mutated)
    print(json.dumps({"mutation": mode, "target": str(target.relative_to(root)),
                      "candidate_sha256": hashlib.sha256(original.encode()).hexdigest(),
                      "mutant_sha256": hashlib.sha256(mutated.encode()).hexdigest()}))


if __name__ == "__main__":
    main()
