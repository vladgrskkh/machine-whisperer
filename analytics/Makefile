.PHONY: run migrate build test test-integration lint tools

run:
	go run ./cmd/analytics

migrate:
	go run ./cmd/migrate

build:
	go build ./...

test:
	go test ./... -race -count=1

test-integration:
	go test -tags=integration ./tests/integration -race -count=1

lint:
	golangci-lint run --config ../api/.golangci.yml --build-tags=integration --timeout=5m

tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
