#!/usr/bin/env bash
# GitHub Ubuntu images may prefer an unavailable Azure package mirror.
# Use Ubuntu's primary HTTPS mirrors before installing test dependencies.
set -euo pipefail
[[ "${GITHUB_ACTIONS:-}" == "true" ]] || { echo "CI-only helper" >&2; exit 1; }
if [[ -f /etc/apt/apt-mirrors.txt ]]; then
  printf '%s\n' 'https://archive.ubuntu.com/ubuntu' | sudo tee /etc/apt/apt-mirrors.txt >/dev/null
fi
if [[ -f /etc/apt/apt-security-mirrors.txt ]]; then
  printf '%s\n' 'https://security.ubuntu.com/ubuntu' | sudo tee /etc/apt/apt-security-mirrors.txt >/dev/null
fi

# Only repair these two package hosts in the disposable CI runner. DNSSEC stays
# enabled in Google's DoH API; apt still verifies TLS and repository signatures.
# API: https://developers.google.com/speed/public-dns/docs/doh/json
python3 - <<'PY'
import ipaddress
import json
import subprocess
import urllib.request

for host in ('archive.ubuntu.com', 'security.ubuntu.com'):
    try:
        resolved = subprocess.run(['getent', 'ahostsv4', host], capture_output=True, timeout=5, check=False)
        if resolved.returncode == 0 and resolved.stdout.strip():
            continue
    except subprocess.TimeoutExpired:
        pass
    with urllib.request.urlopen('https://dns.google/resolve?name=' + host + '&type=A', timeout=20) as response:
        answer = json.load(response)
    if answer.get('Status') != 0:
        raise SystemExit('DoH failed for ' + host)
    addresses = []
    for record in answer.get('Answer', []):
        if record.get('type') != 1:
            continue
        address = ipaddress.IPv4Address(record['data'])
        if address.is_global:
            addresses.append(str(address))
    if not addresses:
        raise SystemExit('No public IPv4 address for ' + host)
    entries = ''.join(address + ' ' + host + '\n' for address in sorted(set(addresses)))
    subprocess.run(['sudo', 'tee', '-a', '/etc/hosts'], input=entries, text=True, stdout=subprocess.DEVNULL, check=True)
    print('Applied HTTPS DNS fallback for ' + host)
PY
