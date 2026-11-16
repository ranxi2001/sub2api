"""Apply our reviewed MFA patch only to the pinned toSub2 source, fail on drift."""
import hashlib
from pathlib import Path
import subprocess
import sys

root = Path(sys.argv[1]).resolve()
source = root / 'src/protocol-login.mjs'
expected = 'b7e5ed0a4dcc2955607815f01ea291d1708c661af0796b19dc5dab23ff9c4752'
if hashlib.sha256(source.read_bytes()).hexdigest() != expected:
    raise SystemExit('unsupported TOTP protocol source; review the pin and patch')
subprocess.run(['patch', '--batch', '--fuzz=0', '-p1', '-i',
                str(Path(__file__).with_name('totp-factor-binding.patch').resolve())], cwd=root, check=True)
