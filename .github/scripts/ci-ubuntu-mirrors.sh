#!/usr/bin/env bash
# GitHub Ubuntu images may prefer an unavailable Azure package mirror.
# Use Ubuntu's primary HTTPS mirrors before installing test dependencies.
set -euo pipefail
if [[ -f /etc/apt/apt-mirrors.txt ]]; then
  printf '%s\n' 'https://archive.ubuntu.com/ubuntu' | sudo tee /etc/apt/apt-mirrors.txt >/dev/null
fi
if [[ -f /etc/apt/apt-security-mirrors.txt ]]; then
  printf '%s\n' 'https://security.ubuntu.com/ubuntu' | sudo tee /etc/apt/apt-security-mirrors.txt >/dev/null
fi
