#!/usr/bin/env python3
"""Select one CJK package from the runner's signature-checked APT indexes."""
import os
from pathlib import Path
import re
import subprocess


def main():
    if os.environ.get('GITHUB_ACTIONS') != 'true':
        raise RuntimeError('CI-only APT metadata reader')
    evidence = Path(os.environ['EVIDENCE_DIR']).resolve()
    evidence.relative_to(Path(os.environ['RUNNER_TEMP']).resolve())
    package = 'fonts-noto-cjk'

    def apt_cache(kind, argument):
        result = subprocess.run(['apt-cache', kind, argument], check=True, text=True,
                                capture_output=True, timeout=30, env={**os.environ, 'LC_ALL': 'C'})
        (evidence / f'font-package-{kind}.txt').write_text(result.stdout)
        return result.stdout

    candidates = re.findall(r'^\s*Candidate:\s*(\S+)\s*$', apt_cache('policy', package), re.M)
    if len(candidates) != 1 or not re.fullmatch(r'(?:\d+:)?[0-9][A-Za-z0-9.+~\-]*', candidates[0]):
        raise RuntimeError('Missing or invalid APT candidate')
    version = candidates[0]
    records = []
    for stanza in apt_cache('show', f'{package}={version}').strip().split('\n\n'):
        fields = dict(line.split(': ', 1) for line in stanza.splitlines()
                      if line and not line[0].isspace() and ': ' in line)
        # Installed-status records have no archive integrity metadata.
        if fields.get('Package') == package and fields.get('Version') == version and 'Filename' in fields:
            records.append(tuple(fields.get(key, '') for key in ('Filename', 'SHA256', 'Version', 'Architecture', 'Size')))
    if len(set(records)) != 1:
        raise RuntimeError('APT candidate archive metadata missing or inconsistent')
    filename, sha256, version, architecture, size = records[0]
    expected = f'pool/main/f/{package}/{package}_{version.split(":")[-1]}_all.deb'
    if filename != expected or architecture != 'all' or not re.fullmatch(r'[0-9a-f]{64}', sha256):
        raise RuntimeError('Unexpected CJK archive path, architecture or SHA256')
    if not re.fullmatch(r'[1-9][0-9]*', size):
        raise RuntimeError('Missing archive size')
    # Every value is restricted above: no whitespace, shell expansion or quoting.
    values = {'FONT_FILENAME': filename, 'FONT_SHA256': sha256, 'FONT_VERSION': version,
              'FONT_ARCHITECTURE': architecture, 'FONT_SIZE': size}
    (evidence / 'font-package.env').write_text(''.join(f'{key}={value}\n' for key, value in values.items()))


if __name__ == '__main__':
    main()
