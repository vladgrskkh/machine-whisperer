# Machine Whisperer API

The API listens on `API_ADDRESS` (default `:8080`). No product routes are
registered yet. Go 1.26.5 and golangci-lint v2.12.2 are required.

```sh
make run
make build
make test
make lint
make tools
```

Tests use real HTTP connections and require permission to bind loopback ports.
