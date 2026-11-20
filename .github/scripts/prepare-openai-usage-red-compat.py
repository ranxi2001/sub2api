#!/usr/bin/env python3
"""Add only missing zero-value usage storage to an exact historical RED blob."""
import argparse
import hashlib
import json
import re
import subprocess
from pathlib import Path

TARGET = 'backend/internal/service/openai_gateway_service.go'
FIELDS = (
    ('CacheCreation5mTokens', 'int `json:"cache_creation_5m_tokens,omitempty"`'),
    ('CacheCreation1hTokens', 'int `json:"cache_creation_1h_tokens,omitempty"`'),
    ('CacheCreation30mInputTokens', 'int `json:"cache_creation_30m_input_tokens,omitempty"`'),
    ('CacheCreation12hInputTokens', 'int `json:"cache_creation_12h_input_tokens,omitempty"`'),
    ('cacheCreationInputTokensPresent', 'bool'),
)


def blob(data):
    return hashlib.sha1(b'blob ' + str(len(data)).encode() + b'\0' + data).hexdigest()


def prepare(tree, baseline):
    tree = Path(tree)
    original = subprocess.check_output(['git', '-C', str(tree), 'show', baseline + ':' + TARGET])
    path = tree / TARGET
    if path.read_bytes() != original:
        raise ValueError('historical usage source is not the exact baseline blob')
    result = original
    layers = []
    for struct, fields in [('OpenAIUsage', FIELDS), ('OpenAIForwardResult', (
            ('WanBilledSeconds', 'float64'), ('WanBaseCost', '*float64')))]:
        match = re.search(rb'^type ' + struct.encode() + rb' struct \{\n(.*?)^\}', result, re.M | re.S)
        if not match:
            raise ValueError('historical layout not found: ' + struct)
        additions = b''.join(('\t' + name + ' ' + declaration + '\n').encode()
            for name, declaration in fields if not re.search(rb'^\s*' + name.encode() + rb'\s', match[1], re.M))
        position = match.start(1)
        result = result[:position] + additions + result[position:]
        layers.append(additions.decode())
    restored = result
    for layer in reversed(layers):
        if layer:
            restored = restored.replace(layer.encode(), b'', 1)
    if restored != original:
        raise ValueError('historical executable bytes changed')
    path.write_bytes(result)
    return {'baseline': baseline, 'path': TARGET, 'baseline_blob': blob(original),
            'overlay_blob': blob(result), 'added_storage': layers,
            'historical_bytes_preserved': True}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('tree')
    parser.add_argument('baseline')
    args = parser.parse_args()
    print(json.dumps(prepare(args.tree, args.baseline), indent=2))
