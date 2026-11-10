# API initial setup

**Status:** Approved; module layout superseded by the later decision to use
separate Go modules and tooling per service.

## Goal

Establish a Go monorepo foundation for the API monolith and future analytics
service. This setup does not implement telemetry, authentication or detection.

## Approaches

- **One root Go module (recommended):** one dependency graph and simple
  repository-wide checks. The services remain separate deployable programs.
- **One module per service:** independent dependencies and release cycles,
  with more workspace and tooling configuration.

The module boundary is a proposed choice, not an agreed decision.

## Proposed setup

- Create a root Go module using the repository's GitHub path and place the API
  entry point in `services/api/cmd/api`. Add domain, use case, infrastructure
  and handler packages as real responsibilities are implemented.
- Provide a minimal API process with a configured HTTP server, graceful shutdown
  and no product endpoints. Keep runtime configuration and logging explicit.
- Add a root Makefile for build, test and lint, and the golden golangci-lint
  config with a compatible pinned tool version. Document commands and required
  tooling in a short README.
- Verify builds, tests and lint. No database, migrations or analytics service
  are needed until their first implementation; Goose and the agreed libraries
  will be introduced when used.

The current workspace cannot create or modify `.git`; setup remains local until
a writable checkout is available.
