.PHONY: build build-linux build-windows build-darwin generate test test-unit test-collection test-integration test-e2e

VERSION ?= $(shell ./scripts/resolve-version.sh)
LDFLAGS ?= -s -w -X main.Version=$(VERSION)

build:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -trimpath -o bin/server ./cmd/server

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags embed -ldflags="$(LDFLAGS)" -trimpath -o sub2api ./cmd/server

build-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -tags embed -ldflags="$(LDFLAGS)" -trimpath -o sub2api ./cmd/server

build-darwin:
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -tags embed -ldflags="$(LDFLAGS)" -trimpath -o sub2api ./cmd/server

generate:
	go generate ./ent
	go generate ./cmd/server

test: test-collection
	go test ./...
	golangci-lint run ./...

test-unit: test-collection
	go test -tags=unit ./...

# The embedded collection engine is a nested module, outside ./... .
test-collection:
	go test github.com/v03413/bepusdt/app/conf github.com/v03413/bepusdt/app/task github.com/v03413/bepusdt/app/task/notify

test-integration:
	go test -tags=integration ./...

test-e2e:
	./scripts/e2e-test.sh

test-e2e-local:
	go test -tags=e2e -v -timeout=300s ./internal/integration/...
