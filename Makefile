BINARY  := envmagic

.PHONY: build install install-tools lint fmt test version tag release

build:
	go build -o $(BINARY) ./cmd/envmagic

install:
	go install ./cmd/envmagic

install-tools:
	go install mvdan.cc/gofumpt@v0.11.0
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2
	go install golang.org/x/vuln/cmd/govulncheck@latest
	go install github.com/goreleaser/goreleaser/v2@latest

lint:
	@out=$$(gofumpt -l $$(git ls-files '*.go')) || exit $$?; if [ -n "$$out" ]; then echo "$$out"; exit 1; fi
	golangci-lint run ./...
	govulncheck ./...

fmt:
	gofumpt -w $$(git ls-files '*.go')

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
