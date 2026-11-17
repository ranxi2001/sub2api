#!/usr/bin/env python3
"""Drop only the new cache-parent argument from callers of the pinned RED API.

The causal identity/session implementation and the network oracle stay exact.
GREEN does not invoke this adapter. Print caller blob pairs for evidence.
"""
import pathlib
import subprocess

baseline = '77455cf755fa0f119367b911704f11df88e7917e'
contract = 'backend/internal/service/openai_codex_account_identity.go'
def git(*args):
    return subprocess.check_output(['git', *args])
assert pathlib.Path(contract).read_bytes() == git('show', f'{baseline}:{contract}')
for name in ('openai_codex_ticket_egress.go', 'openai_gateway_chat_completions.go',
             'openai_gateway_forward.go', 'openai_gateway_messages.go',
             'openai_gateway_passthrough.go'):
    path = pathlib.Path('backend/internal/service') / name
    original = path.read_bytes()
    assert original == git('show', f'HEAD:{path}'), path
    updated = original.replace(b', codexClientSessionID(c)', b'')
    if name == 'openai_codex_ticket_egress.go':
        updated = updated.replace(b', clientSessionIDs...)', b')')
    path.write_bytes(updated)
    print(f'identity RED caller={path} candidate={git("rev-parse", f"HEAD:{path}").decode().strip()} adapted={git("hash-object", str(path)).decode().strip()}')
assert pathlib.Path(contract).read_bytes() == git('show', f'{baseline}:{contract}')
