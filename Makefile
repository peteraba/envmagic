BINARY  := envmagic
GOBIN   := $(shell go env GOPATH)/bin

.PHONY: build install lint test version tag release

build:
	go build -o $(BINARY) .

install:
	go install .

lint:
	gofumpt -w .
	golangci-lint run .
	govulncheck ./...

test: lint
	go test ./...

version:
	@VERSION=$$(go run . --version | awk '{print $$NF}'); \
	if git tag | grep -qx "$$VERSION"; then \
		echo "Error: version $$VERSION already exists as a git tag — bump the version before committing"; \
		exit 1; \
	else \
		echo "OK: version $$VERSION is not yet tagged"; \
	fi

tag: version
	@VERSION=$$(go run . --version | awk '{print $$NF}'); \
	git tag "$$VERSION" && echo "Tagged $$VERSION"

release: tag
	git push
	git push --tags
	goreleaser release --clean
