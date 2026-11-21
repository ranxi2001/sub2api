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

# The candidate's metadata resolver also fills an audio capability bit. The
# historical list implementation ignores it; retain storage for that caller.
descriptor = target.with_name('openai_codex_models_service.go')
source = descriptor.read_text()
anchor = 'type codexModelMetadataOverride struct {\n'
if source.count(anchor) != 1:
    raise SystemExit('historical descriptor declaration missing or ambiguous')
descriptor.write_text(source.replace(anchor, anchor + '\taudioInput bool\n', 1))
