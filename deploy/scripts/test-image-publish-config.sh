#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
ruby -ryaml - .github/workflows/ci.yml <<'RUBY'
workflow = YAML.load_file(ARGV.fetch(0))
jobs = workflow.fetch("jobs")
abort "Registry credentials must be scoped to the publish job" if workflow.fetch("env", {}).to_s.include?("secrets.DOCKERHUB_")
publish = jobs.fetch("publish") { abort "Missing image publication job" }
abort "Publishing must require a push to main" unless publish.fetch("if") ==
  "github.event_name == 'push' && github.ref == 'refs/heads/main'"
abort "Publishing must wait for all checks" unless publish.fetch("needs").sort ==
  %w[analytics build deployment integration lint mobile test]
abort "Unexpected publication matrix" unless publish.dig("strategy", "matrix", "service").sort == %w[analytics api]
abort "Incorrect registry namespace" unless publish.dig("env", "DOCKERHUB_NAMESPACE") ==
  '${{ vars.DOCKERHUB_NAMESPACE || secrets.DOCKERHUB_USERNAME }}'

steps = publish.fetch("steps")
login = steps.find { |s| s["uses"].to_s.start_with?("docker/login-action@") }
abort "Incorrect Docker Hub login" unless login && login.fetch("with") == {
  "username" => '${{ secrets.DOCKERHUB_USERNAME }}', "password" => '${{ secrets.DOCKERHUB_TOKEN }}'
}
build = steps.find { |s| s["uses"].to_s.start_with?("docker/build-push-action@") }
abort "Missing publish step" unless build
args = build.fetch("with")
abort "Incorrect build context or push setting" unless args["context"] == './${{ matrix.service }}' && args["push"] == true
image = 'docker.io/${{ env.DOCKERHUB_NAMESPACE }}/machine-whisperer-${{ matrix.service }}'
abort "Incorrect release tags" unless args.fetch("tags").lines.map(&:strip).sort == ["#{image}:latest", "#{image}:sha-${{ github.sha }}"]

jobs.reject { |name, _| name == "publish" }.each do |name, job|
  abort "Registry credentials or publication action in #{name}" if job.to_s.match?(/secrets\.DOCKERHUB_|docker\/(login|build-push)-action/)
end
RUBY
