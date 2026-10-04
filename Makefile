BINARY  := envmagic

.PHONY: build install install-tools lint test version tag release

build:
	go build -o $(BINARY) ./cmd/envmagic

install:
	go install ./cmd/envmagic

install-tools:
	go install mvdan.cc/gofumpt@latest
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	go install golang.org/x/vuln/cmd/govulncheck@latest
	go install github.com/goreleaser/goreleaser/v2@latest

lint:
	gofumpt -w .
	golangci-lint run ./...
	govulncheck ./...

test: lint
	go test ./...

version:
	@VERSION=$$(go run ./cmd/envmagic --version | awk '{print $$NF}'); \
	if git tag | grep -qx "$$VERSION"; then \
		echo "Error: version $$VERSION already exists as a git tag — bump the version before committing"; \
		exit 1; \
	else \
		echo "OK: version $$VERSION is not yet tagged"; \
	fi

tag: version
	@VERSION=$$(go run ./cmd/envmagic --version | awk '{print $$NF}'); \
	git tag "$$VERSION" && echo "Tagged $$VERSION"

release: tag
	git push
	git push --tags
	goreleaser release --clean
