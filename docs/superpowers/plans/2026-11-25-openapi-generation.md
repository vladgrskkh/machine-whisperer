# Swagger Generation Implementation Plan

Use Superpowers subagent driven development and an independent implementation review.

**Goal** Generate a reviewable Swagger 2.0 YAML contract from the Go API.

**Design** See [issue #9 and the approved design](../specs/2026-11-25-issue-9-openapi-design.md).

- [x] Start from a base containing the device scoped ingest route. Inventory the 18 operations, including the compatibility POST and the owner history GET.
- [x] Annotate handlers and the health route, with small documentation wire types where needed. Keep separate JWT and device credential schemes, exact response statuses and headers, and nullable output fields.
- [x] Add Make targets backed by Swaggo v1.16.6 for regeneration and drift checking in a temporary directory. Commit the deterministic YAML under api/docs/openapi.
- [x] Wire the drift check into the API build job. Document local generation and validate the resulting route and credential inventory against chi registration.
- [x] Independently review the generated contract.
- [ ] Open the issue #9 PR.
