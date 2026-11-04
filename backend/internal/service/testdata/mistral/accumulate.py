"""Run the pinned SDK function, replacing only its relative type guard import."""
import json
import pathlib
import sys

source = pathlib.Path(__file__).with_name('openai_deltas.py').read_text()
source = source.replace(
    'from ..._utils import is_dict, is_list',
    'def is_dict(x): return isinstance(x, dict)\ndef is_list(x): return isinstance(x, list)',
)
namespace = {}
exec(compile(source, 'openai_deltas.py', 'exec'), namespace)
acc = {}
for delta in json.load(sys.stdin):
    acc = namespace['accumulate_delta'](acc, delta)
json.dump(acc, sys.stdout)
