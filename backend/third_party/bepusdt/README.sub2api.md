# Embedded collection engine

This directory contains the Go runtime packages used by Sub2API from
`github.com/v03413/bepusdt`, version
`v1.24.3-0.20260930070044-aa3bd5097f25` (commit `aa3bd5097f25`).
The upstream administrator web application and its unrelated assets are omitted.
The upstream GPL-3.0 license and copyright notices are preserved in `LICENSE`.

Sub2API maintains a local module replacement so scanner reliability fixes are
part of every source build; the Go module cache is never patched. Run its tests
using the parent module's dependency versions and SQLite adapter:

```sh
cd backend
go test github.com/v03413/bepusdt/app/conf github.com/v03413/bepusdt/app/task
go test -tags unit ./internal/bepusdt ./internal/payment/provider
```

Local changes keep failed scan ranges pending, find historical block boundaries
using each chain's native RPC, filter EVM logs by token and recipient, and verify
confirmation receipts. Retired public defaults are migrated without overwriting
custom nodes or credentials. HTTP RPC failover checks network identity and never
forwards a custom node's credentials to public alternatives. Solana version-one
transactions and loaded account ordering are supported; non-EVM scan failures
retry with bounded workers. Node health is checked before allocating a payment
address, and exposed to the administrator and checkout status APIs.

Order transitions are conditional so expiration, replay and duplicate detections
cannot overwrite a confirmed payment or dispatch its initial callback twice.
Embedded orders retain the merchant's absolute expiry time, including two-minute
and greater-than-one-hour deadlines, without changing external gateway timeout
defaults. Payment amount reservations use the normalized matching address so
mixed-case EVM receiving addresses cannot allocate the same amount to concurrent
orders; older records without a matching address are normalized on lookup.
The existing signed callback retry worker handles delivery failures. These tests
are explicitly included by `make test-collection` and the collection CI workflow,
because a parent module's `go test ./...` does not traverse nested modules.
