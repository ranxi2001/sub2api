"""Preserve additive APIs while restoring the original quality boundary behavior."""
import pathlib
import re
import sys

candidate = pathlib.Path(sys.argv[1]).read_text()
target = pathlib.Path('backend/internal/service/quality_supported_models.go')
original = target.read_text()
for name in ['qualityAccountAllowsModel', 'eligibleQualityModels']:
    match = re.search(r'^func (?:\([^\n]+\) )?' + name + r'\(', candidate, re.M)
    end = re.search(r'^}', candidate[match.start():], re.M)
    original += '\n' + candidate[match.start():match.start() + end.end()] + '\n'
if 'infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"' not in original:
    original = original.replace('import (', 'import (\n\tinfraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"', 1)
target.write_text(original)
