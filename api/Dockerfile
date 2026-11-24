FROM golang:1.26.5-bookworm AS build

WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN go build -trimpath -o /out/api ./cmd/api
RUN GOBIN=/out go install -trimpath \
    -tags 'no_clickhouse no_libsql no_mssql no_mysql no_sqlite3 no_vertica no_ydb' \
    github.com/pressly/goose/v3/cmd/goose@v3.27.3

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=build /out/api /out/goose /usr/local/bin/
COPY migrations ./migrations
COPY --chmod=0555 scripts/migrate.sh /usr/local/bin/migrate

USER 10001:10001
ENTRYPOINT ["/usr/local/bin/api"]
