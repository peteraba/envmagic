BINARY  := envmagic
GOBIN   := $(shell go env GOPATH)/bin

# Injected at link time; falls back to "dev" when not set (e.g. plain go run).
VERSION_LD := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GO_LD_FLAGS := -s -w -X main.version=$(VERSION_LD)

.PHONY: build install lint test version tag release

build:
	go build -ldflags "$(GO_LD_FLAGS)" -o $(BINARY) ./cmd/envmagic

install:
	go install -ldflags "$(GO_LD_FLAGS)" ./cmd/envmagic

lint:
	gofumpt -w .
	golangci-lint run ./...
	govulncheck ./...

test: lint
	go test ./...

version:
	@VERSION=$$(go run -ldflags "$(GO_LD_FLAGS)" ./cmd/envmagic --version | awk '{print $$NF}'); \
	if git tag | grep -qx "$$VERSION"; then \
		echo "Error: version $$VERSION already exists as a git tag — bump the version before committing"; \
		exit 1; \
	else \
		echo "OK: version $$VERSION is not yet tagged"; \
	fi

tag: version
	@VERSION=$$(go run -ldflags "$(GO_LD_FLAGS)" ./cmd/envmagic --version | awk '{print $$NF}'); \
	git tag "$$VERSION" && echo "Tagged $$VERSION"

release: tag
	git push
	git push --tags
	goreleaser release --clean
