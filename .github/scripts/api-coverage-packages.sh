#!/usr/bin/env bash
set -euo pipefail

go list "$@" ./cmd/... ./internal/... |
  awk -F/ '$0 !~ /\/internal\/testutil(\/|$)/ && $NF !~ /mock$/' |
  paste -sd , -
