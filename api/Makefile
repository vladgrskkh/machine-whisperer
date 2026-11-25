.PHONY: run build test test-integration lint tools migrate-up migrate-down migrate-status swagger swagger-check

SWAGGER_DIR := docs/openapi
SWAGGER_INIT := go run github.com/swaggo/swag/cmd/swag@v1.16.6 init \
	--dir cmd/api,internal/application,internal/handler,internal/domain/telemetry \
	--generalInfo main.go --parseInternal --outputTypes yaml
OPENAPI_CONVERT := go -C tools/openapi run -mod=readonly .

run:
	go run ./cmd/api

build:
	go build -o bin/api ./cmd/api

swagger:
	$(SWAGGER_INIT) --output "$(SWAGGER_DIR)"
	$(OPENAPI_CONVERT) "$(abspath $(SWAGGER_DIR))/swagger.yaml" "$(abspath $(SWAGGER_DIR))/openapi.yaml"

swagger-check:
	@set -eu; \
	tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT; \
	trap 'exit 1' HUP INT TERM; \
	$(SWAGGER_INIT) --output "$$tmp"; \
	$(OPENAPI_CONVERT) "$$tmp/swagger.yaml" "$$tmp/openapi.yaml"; \
	diff -u "$(SWAGGER_DIR)/swagger.yaml" "$$tmp/swagger.yaml"; \
	diff -u "$(SWAGGER_DIR)/openapi.yaml" "$$tmp/openapi.yaml"

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
