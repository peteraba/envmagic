BINARY  := envmagic
GOBIN   := $(shell go env GOPATH)/bin

.PHONY: build install lint test

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
