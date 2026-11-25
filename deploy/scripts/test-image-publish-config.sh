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
abort "Concurrent publishers must queue by service and SHA" unless publish["concurrency"] == {
  "group" => 'dockerhub-sha-${{ matrix.service }}-${{ github.sha }}', "queue" => "max"
}
abort "Incorrect registry namespace" unless publish.dig("env", "DOCKERHUB_NAMESPACE") ==
  '${{ vars.DOCKERHUB_NAMESPACE || secrets.DOCKERHUB_PUBLISH_USERNAME }}'

steps = publish.fetch("steps")
login = steps.find { |s| s["uses"].to_s.start_with?("docker/login-action@") }
expected_login = {
  "username" => '${{ secrets.DOCKERHUB_PUBLISH_USERNAME }}', "password" => '${{ secrets.DOCKERHUB_PUBLISH_TOKEN }}'
}
abort "Incorrect Docker Hub login" unless login && login["with"] == expected_login
abort "Unprotected publisher" unless publish["environment"] == "dockerhub-publish"
build = steps.find { |s| s["uses"].to_s.start_with?("docker/build-push-action@") }
abort "Missing publish step" unless build
args = build.fetch("with")
abort "Incorrect build context or push setting" unless args["context"] == './${{ matrix.service }}' && args["push"] == true
image = 'docker.io/${{ env.DOCKERHUB_NAMESPACE }}/machine-whisperer-${{ matrix.service }}'
abort "Incorrect release tags" unless args.fetch("tags").lines.map(&:strip).sort == ["#{image}:sha-${{ github.sha }}"]

probe = steps.find { |s| s["id"] == "release" }
abort "SHA lookup must gate the push" unless probe && steps.index(login) < steps.index(probe) &&
  steps.index(probe) < steps.index(build) && build["if"] == "steps.release.outputs.exists == 'false'"
abort "Wrong release tag for lookup" unless probe.dig("env", "RELEASE_IMAGE") == args["tags"].strip

promote = jobs.fetch("promote")
abort "Promotion must wait for the complete pair" unless promote["needs"] == "publish" &&
  promote["if"] == publish["if"] && !promote.key?("strategy")
abort "Unprotected promotion" unless promote["environment"] == "dockerhub-publish" &&
  promote["env"] == publish["env"] && promote["steps"].any? { |s| s["uses"] == login["uses"] && s["with"] == expected_login }
abort "Latest promotions must queue without cancellation" unless promote["concurrency"] == {
  "group" => "dockerhub-latest", "queue" => "max"
}

require "tmpdir"
require "open3"
Dir.mktmpdir do |dir|
  docker = File.join(dir, "docker")
  File.write(docker, <<~'SH')
    #!/bin/bash
    echo "$*" >> "$LOG"
    if [[ "$1 $2 $3" == "buildx imagetools inspect" ]]; then
      [[ "$4" == "$RELEASE_IMAGE" ]] || exit 3
      case "$MODE" in
        existing) echo "Digest: sha256:123";;
        missing) echo "ERROR: $4: not found" >&2; exit 1;;
        denied) echo "ERROR: unauthorized" >&2; exit 1;;
      esac
    fi
  SH
  gh = File.join(dir, "gh")
  File.write(gh, <<~'SH')
    #!/bin/bash
    [[ "$*" == 'api repos/org/repo/git/ref/heads/main --jq .object.sha' ]] || exit 9
    echo "$HEAD_SHA"
  SH
  File.chmod(0755, docker, gh)
  log = File.join(dir, "calls")
  output = File.join(dir, "out")
  env = {"PATH" => "#{dir}:#{ENV.fetch('PATH')}", "LOG" => log, "GITHUB_OUTPUT" => output,
         "RELEASE_IMAGE" => "docker.io/org/machine-whisperer-api:sha-abc", "GITHUB_SHA" => "abc",
         "GITHUB_REPOSITORY" => "org/repo", "DOCKERHUB_NAMESPACE" => "org"}
  {"existing" => "exists=true", "missing" => "exists=false", "denied" => nil}.each do |mode, expected|
    File.write(output, "")
    File.write(log, "")
    _, status = Open3.capture2e(env.merge("MODE" => mode), "bash", "-eu", "-o", "pipefail", "-c", probe.fetch("run"))
    abort "Wrong SHA lookup (#{mode})" unless status.success? == !expected.nil? &&
      File.read(output).strip == expected.to_s && File.read(log).strip == "buildx imagetools inspect #{env['RELEASE_IMAGE']}"
  end
  promotion = promote["steps"].last.fetch("run")
  ["abc", "older"].each do |head|
    File.write(log, "")
    _, status = Open3.capture2e(env.merge("HEAD_SHA" => head), "bash", "-eu", "-o", "pipefail", "-c", promotion)
    expected = head == "abc" ? %w[api analytics].map { |svc|
      ref = "docker.io/org/machine-whisperer-#{svc}"
      "buildx imagetools create --prefer-index=false --tag #{ref}:latest #{ref}:sha-abc"
    } : []
    abort "Incorrect latest promotion (#{head})" unless status.success? && File.readlines(log, chomp: true) == expected
  end
end

jobs.reject { |name, _| %w[publish promote].include?(name) }.each do |name, job|
  abort "Registry credentials or publication action in #{name}" if job.to_s.match?(/secrets\.DOCKERHUB_|docker\/(login|build-push)-action/)
end
RUBY
