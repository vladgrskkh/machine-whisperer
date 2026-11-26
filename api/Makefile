.PHONY: run build test test-integration lint tools migrate-up migrate-down migrate-status swagger swagger-check

SWAGGER_DIR := docs/openapi
SWAGGER_SOURCES := --dir cmd/api,internal/application,internal/handler,internal/domain/telemetry,docs/openapi \
	--parseInternal --outputTypes yaml
SWAGGER_INIT := go run github.com/swaggo/swag/cmd/swag@v1.16.6 init $(SWAGGER_SOURCES) --generalInfo main.go
OPENAPI_INIT := go run github.com/swaggo/swag/v2/cmd/swag@v2.0.0-rc6 init $(SWAGGER_SOURCES) \
	--v3.1 --generalInfo openapi.go --instanceName openapi
# Swaggo emits the nullable extension and inclusive minimum from the shared field tags.
OPENAPI_SCHEMA := (.. | select(tag == "!!map" and .["x-nullable"] == true)) |= \
	(.type = [.type, "null"] | del(.["x-nullable"])) | \
	.components.schemas.BaselineInput.properties.stddev_rms_g |= (.exclusiveMinimum = .minimum | del(.minimum))

define generate_specs
	$(SWAGGER_INIT) --output "$(1)"; \
	$(OPENAPI_INIT) --output "$(1)"; \
	go run github.com/mikefarah/yq/v4@v4.54.3 '$(OPENAPI_SCHEMA)' "$(1)/openapi_swagger.yaml" > "$(1)/openapi.yaml"; \
	rm "$(1)/openapi_swagger.yaml"
endef

run:
	go run ./cmd/api

build:
	go build -o bin/api ./cmd/api

swagger:
	@set -eu; \
	tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT; \
	$(call generate_specs,$$tmp); \
	mkdir -p "$(SWAGGER_DIR)"; \
	mv "$$tmp/swagger.yaml" "$$tmp/openapi.yaml" "$(SWAGGER_DIR)/"

swagger-check:
	@set -eu; \
	tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT; \
	trap 'exit 1' HUP INT TERM; \
	$(call generate_specs,$$tmp); \
	diff -u "$(SWAGGER_DIR)/swagger.yaml" "$$tmp/swagger.yaml"; \
	diff -u "$(SWAGGER_DIR)/openapi.yaml" "$$tmp/openapi.yaml"

test:
	go test ./... -v -count=1

test-integration:
	go test -tags=integration ./... -v -count=1 -race $(GO_TEST_FLAGS)

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
