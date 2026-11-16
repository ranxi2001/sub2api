"""Collect a relocatable runtime and glibc loader without archive symlinks."""
import json
from pathlib import Path
import platform
import shutil
import subprocess
import sys

out = Path(sys.argv[1])
out.mkdir(parents=True, exist_ok=True)
shutil.copytree("/usr/local/lib/python3.12", out / "python-root/lib/python3.12",
                symlinks=False, ignore=shutil.ignore_patterns("__pycache__", "*.pyc"))
(out / "python-root/bin").mkdir(parents=True)
shutil.copy2("/usr/local/bin/python3.12", out / "python-root/bin/python3")
shutil.copy2("/usr/local/bin/node", out / "node.bin")
shutil.copytree("/build/tosub2/src", out / "tosub2/src")
transport = out / "tosub2/src/tls-transport.mjs"
transport.write_text(transport.read_text().replace("nodeCommand: process.execPath,", "nodeCommand: process.env.NODE_EXECUTABLE || process.execPath,"))
shutil.copytree("/build/tosub2/node_modules", out / "tosub2/node_modules", symlinks=False)
shutil.copy2("/build/tosub2/LICENSE", out / "tosub2/LICENSE")
shutil.copy2("/build/tosub2/package-lock.json", out / "tosub2/package-lock.json")
shutil.copy2("/build/worker.py", out / "worker.py")
for name in ("openai_totp_rotation.py", "openai_totp_journal.mjs", "openai_excel_oauth_adapter.py", "openai_excel_2fa_login.py", "openai_excel_password_flow.mjs", "openai_siwc_oauth_adapter.py", "openai_siwc_password_flow.mjs", "openai_codex_oauth_adapter.py", "openai_codex_diagnostics.mjs"):
    shutil.copy2(Path("/build/tools") / name, out / name)
shutil.copy2("/etc/ssl/certs/ca-certificates.crt", out / "ca-certificates.crt")
shutil.copytree("/usr/share/doc", out / "licenses/debian", symlinks=False)
lib = out / "lib"
lib.mkdir()
queue = [out / "node.bin", out / "python-root/bin/python3"]
queue += list((out / "python-root").rglob("*.so"))
seen = set()
while queue:
    path = queue.pop()
    real = path.resolve()
    if real in seen:
        continue
    seen.add(real)
    result = subprocess.run(["ldd", str(real)], capture_output=True, text=True, check=False)
    for line in result.stdout.splitlines():
        words = line.replace("=>", " ").split()
        dependency = next((Path(w) for w in words if w.startswith("/") and Path(w).is_file()), None)
        if dependency is None:
            continue
        target = lib / dependency.name
        if not target.exists():
            shutil.copy2(dependency.resolve(), target)
            queue.append(dependency)
loader = next(lib.glob("ld-linux*.so.*"))
for name, binary in (("python", "python-root/bin/python3"), ("node", "node.bin")):
    # The bundled loader scopes library lookup to this executable. Exporting it
    # would also load the bundled libc into a host shell launched by Python.
    script = out / name
    script.write_text(
        '#!/bin/sh\nset -eu\nroot=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)\n'
        'export PYTHONHOME="$root/python-root"\n'
        'export SSL_CERT_FILE="$root/ca-certificates.crt"\n'
        'export CURL_CA_BUNDLE="$root/ca-certificates.crt"\n'
        f'exec "$root/lib/{loader.name}" --library-path "$root/lib" "$root/{binary}" "$@"\n'
    )
    script.chmod(0o755)
manifest = {"tosub2_commit": "8548397e89bf80e508eda64a87e0d556d43abc84",
            "totp_factor_binding_patch": "issue881-v1",
            "architecture": platform.machine(), "python": platform.python_version(),
            "node": subprocess.check_output(["node", "--version"], text=True).strip(),
            "python_packages": subprocess.check_output([sys.executable, "-m", "pip", "freeze"], text=True).splitlines()}
(out / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
