# Project guidance

Machine Whisperer monitors vibration on steady-running machines. This monorepo
contains the API monolith and a separate analytics service.

## Architecture

- Use `../docs/machine-whisperer-project-design.md` for project context. Preserve
  agreed decisions; discuss changes with the user first. Open questions may be
  updated as they are investigated or resolved.
- Organize services around `domain` (models and rules), `usecase` (business
  logic), `infrastructure` (outbound integrations) and `handler` (inbound
  entry points, such as HTTP handlers or consumers).
- Dependencies point inward: domain does not depend on outer layers; use cases
  do not depend on concrete adapters. Wire implementations at the entry point.
- Define small interfaces at the point of use, not alongside implementations.
- Use cases define transaction boundaries when atomicity spans business
  operations. Infrastructure implements the transaction manager and ensures
  participating operations share the transaction.

## Go

- Write clear, idiomatic Go. Prefer cohesive packages, explicit dependencies
  and simple code over premature abstractions.
- Keep comments concise and explain why, not what. Omit redundant comments.
- Propagate context through I/O and cancellation boundaries; set appropriate
  timeouts for external calls.
- Handle errors explicitly. Wrap causes when intentionally exposing them to
  callers; avoid duplicate logging and leaking internals to clients.
- Use structured logs and never log credentials or sensitive request bodies.

## Tests and checks

- Prefer integration tests for business logic and real dependencies where
  relevant. Use unit tests for domain rules and other suitable logic.
- Test HTTP through `httptest.NewServer` and a real HTTP client. Use `testify`;
  prefer `assert` and use `require` when a failed prerequisite makes continuing
  invalid or unsafe. Generate mocks with `mockery`; do not hand-write them.
- Do not compare error strings. Use `errors.Is` for sentinel errors and
  `errors.As` or `errors.AsType` for typed errors; otherwise check observable
  behavior.
- Keep tests deterministic and isolated. Cover important failures, authorization,
  transactions, retries and concurrency where relevant.
- Use the golden golangci-lint configuration, with a pinned compatible version.
- Before a commit or PR, run the relevant tests and linter, including applicable
  integration tests. Fix failures and report checks that could not be run.
- Use the Superpowers workflow for every feature. If it is unavailable, tell
  the user before starting feature work.
