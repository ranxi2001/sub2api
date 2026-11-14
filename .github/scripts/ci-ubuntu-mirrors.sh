#!/usr/bin/env bash
# Use Ubuntu's HTTPS archive directly: the mirror transport can repeatedly
# choose an unavailable mirror for individual indexes even after a fallback.
# Bound network waits and retain signed Release/package verification.
# Never apply this helper to a production host.
set -euo pipefail
[[ "${GITHUB_ACTIONS:-}" == "true" ]] || { echo "CI-only helper" >&2; exit 1; }
for mirror_list in /etc/apt/apt-mirrors.txt /etc/apt/apt-security-mirrors.txt; do
  if [[ -f "$mirror_list" ]]; then
    printf '%s\n' 'https://archive.ubuntu.com/ubuntu' | sudo tee "$mirror_list" >/dev/null
  fi
done
printf '%s\n' 'Acquire::http::Timeout "30";' 'Acquire::https::Timeout "30";' 'Acquire::Retries "2";' | sudo tee /etc/apt/apt.conf.d/99-sub2api-ci-timeouts >/dev/null
