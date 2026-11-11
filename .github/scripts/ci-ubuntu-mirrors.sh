#!/usr/bin/env bash
# Ubuntu's authoritative DNS outage can block both Azure and primary mirrors.
# Use MIT's HTTPS Ubuntu mirror in disposable CI; apt retains Ubuntu's signed
# Release/package verification. Never apply this helper to a production host.
set -euo pipefail
[[ "${GITHUB_ACTIONS:-}" == "true" ]] || { echo "CI-only helper" >&2; exit 1; }
for mirror_list in /etc/apt/apt-mirrors.txt /etc/apt/apt-security-mirrors.txt; do
  if [[ -f "$mirror_list" ]]; then
    printf '%s\n' 'https://mirrors.mit.edu/ubuntu' | sudo tee "$mirror_list" >/dev/null
  fi
done
