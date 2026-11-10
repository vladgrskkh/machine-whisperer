.PHONY: run build test test-integration lint tools migrate-up migrate-down migrate-status

run:
	go run ./cmd/api

build:
	go build -o bin/api ./cmd/api

test:
	go test ./... -v -count=1

test-integration:
	go test -tags=integration ./... -v -count=1 -race

lint:
	golangci-lint run --build-tags=integration --timeout=5m

migrate-up:
	GOOSE_DRIVER=postgres GOOSE_DBSTRING="$$DATABASE_URL" goose -dir migrations up

migrate-down:
	GOOSE_DRIVER=postgres GOOSE_DBSTRING="$$DATABASE_URL" goose -dir migrations down

migrate-status:
	GOOSE_DRIVER=postgres GOOSE_DBSTRING="$$DATABASE_URL" goose -dir migrations status

tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
	go install github.com/pressly/goose/v3/cmd/goose@v3.27.3
	go install github.com/vektra/mockery/v2@v2.53.6
