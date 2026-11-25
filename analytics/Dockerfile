FROM golang:1.26.5-bookworm AS build

WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN go build -trimpath -o /out/analytics ./cmd/analytics \
    && go build -trimpath -o /out/migrate ./cmd/migrate

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates wget \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=build /out/analytics /out/migrate /usr/local/bin/

USER 10001:10001
ENTRYPOINT ["/usr/local/bin/analytics"]
