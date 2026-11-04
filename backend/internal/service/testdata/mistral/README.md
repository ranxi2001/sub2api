`openai_deltas.py` is the unchanged source from openai/openai-python commit
`f6e8480f9a5b2a4d02b17a1da54a1e57964bc312`, path
`src/openai/lib/streaming/_deltas.py`.

Source: https://raw.githubusercontent.com/openai/openai-python/f6e8480f9a5b2a4d02b17a1da54a1e57964bc312/src/openai/lib/streaming/_deltas.py

SHA-256: `e296d090732c61b57f910c08f352711e70f189932530fd6197516787c4e75f52`.
The upstream Apache-2.0 license is included as `OPENAI_LICENSE`.

`accumulate.py` changes only the relative import to equivalent local `dict` and
`list` type guards. It executes the pinned accumulation function against actual
normalized gateway deltas supplied by the Go test over stdin. It requires
Python 3 and no installed SDK, network, credentials, database or paid upstream.
