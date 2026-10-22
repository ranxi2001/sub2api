# BEpusdt collection engine

The built-in cryptocurrency collection feature links BEpusdt into the Sub2API
server. BEpusdt is copyright its authors and distributed under GPL-3.0:
https://github.com/v03413/BEpusdt.

Pinned source: `v1.24.3-0.20260930070044-aa3bd5097f25`, commit
`aa3bd5097f25` (the pinned version is declared in `backend/go.mod`;
the maintained source is tracked in this repository). Its license is included at `LICENSES/BEpusdt-GPL-3.0.txt`.
No upstream administrator login or HTTP router is exposed by Sub2API.

Distributions of the combined server must comply with GPL-3.0, including keeping
copyright/license notices and providing the complete corresponding source and
build instructions for the combined work. The LGPL-3.0 license of the original
Sub2API sources is compatible with that combined distribution; it does not remove
the GPL obligations introduced by the linked BEpusdt engine.

Source builds use the maintained runtime copy in `backend/third_party/bepusdt`
through a local Go module replacement. It derives from the pinned version above;
scanner reliability changes and test commands are documented in its
`README.sub2api.md`. Include that source and its LICENSE in distributions. Build the
frontend with the existing frontend build commands, then build the server
from `backend` with `go build -tags embed ./cmd/server`. Keep the local
`backend/third_party/glebarez-go-sqlite` adapter in source distributions: it reuses
Sub2API's modernc SQLite driver to avoid duplicate SQL driver registration.
