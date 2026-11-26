#!/usr/bin/env bash
set -euo pipefail

check_main() {
  local head
  head=$(git ls-remote --exit-code origin refs/heads/main)
  if [[ "${head%%$'\t'*}" != "$GITHUB_SHA" ]]; then
    echo "Main has moved. Keeping the existing coverage badges."
    exit 0
  fi
}

for percentage in "$API_COVERAGE" "$ANALYTICS_COVERAGE"; do
  if [[ ! "$percentage" =~ ^[0-9]+(\.[0-9]+)?%$ ]]; then
    echo "Expected a Go statement coverage percentage." >&2
    exit 1
  fi
done

check_main
if git ls-remote --exit-code origin refs/heads/coverage-badges >/dev/null; then
  git fetch --no-tags origin refs/heads/coverage-badges
  git switch --detach FETCH_HEAD
else
  status=$?
  if [[ "$status" != 2 ]]; then
    exit "$status"
  fi
  git switch --orphan coverage-badges
fi

jq -n --arg label "API unit coverage" --arg message "$API_COVERAGE" \
  '{schemaVersion: 1, label: $label, message: $message, color: "blue"}' > api.json
jq -n --arg label "Analytics unit coverage" --arg message "$ANALYTICS_COVERAGE" \
  '{schemaVersion: 1, label: $label, message: $message, color: "blue"}' > analytics.json

git add api.json analytics.json
if git diff --cached --quiet; then
  echo "Coverage badges are up to date."
  exit 0
fi
git -c user.name='github-actions[bot]' \
  -c user.email='41898282+github-actions[bot]@users.noreply.github.com' \
  commit -m 'chore: update coverage badges'
check_main
git push origin HEAD:refs/heads/coverage-badges
