"""Validate behavioral RED, including per-subtest diagnostic ownership."""
import re
import sys
from pathlib import Path

POLLUTION = r'build failed|setup failed|undefined:|no tests to run|--- SKIP:|panic:|fatal error:|data race|race detected|\btimeout\b|timed out|deadline exceeded'
PARENT = 'TestTOTPRecoveryInsertNeverOverwritesExistingOrConcurrentConfig'


def validate(raw):
    if re.search(POLLUTION, raw, re.I):
        raise ValueError('RED contains infrastructure or execution contamination')
    expected = [PARENT + '/existing', PARENT + '/concurrent-insert']
    sections = {}
    current = None
    for line in raw.splitlines():
        started = re.fullmatch(r'=== RUN\s+(\S+)\s*', line)
        if started:
            current = started.group(1)
            sections.setdefault(current, [])
        elif current:
            sections[current].append(line)
    failures = re.findall(r'^\s*--- FAIL: (\S+) \(', raw, re.M)
    if sorted(failures) != sorted([PARENT, *expected]):
        raise ValueError('RED must fail exactly the parent and both named subtests')
    for name in expected:
        section = '\n'.join(sections.get(name, []))
        if not re.search(r'Test:\s+' + re.escape(name) + r'\s*$', section, re.M):
            raise ValueError('missing assertion ownership for ' + name)
        if not re.search(r'Messages:\s+recovery loser must conflict\s*$', section, re.M):
            raise ValueError('missing recovery conflict assertion for ' + name)
    return True


if __name__ == '__main__':
    validate(Path(sys.argv[1]).read_text())
    print('Both recovery subtests failed their own conflict assertion; no contaminated RED.')
