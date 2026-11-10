.PHONY: run build test lint tools

run:
	go run ./cmd/api

build:
	go build -o bin/api ./cmd/api

test:
	go test ./... -v -count=1

lint:
	golangci-lint run --timeout=5m

tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
	go install github.com/pressly/goose/v3/cmd/goose@v3.27.3
	go install github.com/vektra/mockery/v2@v2.53.6
