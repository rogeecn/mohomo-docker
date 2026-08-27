#!/bin/sh
set -eu

github_workflow=.github/workflows/docker.yml
gitea_workflow=.gitea/workflows/docker.yml

test -f "$github_workflow" || {
	echo "missing GHCR workflow: $github_workflow" >&2
	exit 1
}
test -f "$gitea_workflow" || {
	echo "missing Gitea workflow: $gitea_workflow" >&2
	exit 1
}

for workflow in "$github_workflow" "$gitea_workflow"; do
	grep -F '  push:' "$workflow" >/dev/null
	grep -F '  pull_request:' "$workflow" >/dev/null
	grep -F '  workflow_dispatch:' "$workflow" >/dev/null
done

grep -F 'packages: write' "$github_workflow" >/dev/null
# Match the GitHub expression literally.
# shellcheck disable=SC2016
grep -F 'ghcr.io/${{ github.repository }}' "$github_workflow" >/dev/null
grep -F 'platforms: linux/amd64' "$github_workflow" >/dev/null
grep -F 'needs: test' "$github_workflow" >/dev/null
grep -F 'run: ./tests/container-smoke.sh' "$github_workflow" >/dev/null
grep -F 'cache-to: type=gha,mode=max,ignore-error=true' "$github_workflow" >/dev/null
# Match the GitHub expression literally.
# shellcheck disable=SC2016
grep -F 'push: ${{ github.event_name != '\''pull_request'\'' }}' "$github_workflow" >/dev/null

grep -F 'run: ./scripts/test.sh' "$gitea_workflow" >/dev/null
grep -F 'run: ./tests/container-smoke.sh' "$gitea_workflow" >/dev/null
if grep -Ei 'ghcr\.io|github\.|GITHUB_TOKEN|packages: write|docker/(login|build-push)-action' "$gitea_workflow" >/dev/null; then
	echo "Gitea workflow must not depend on GitHub publishing" >&2
	exit 1
fi

if grep -Ei 'arm64|setup-qemu' "$github_workflow" >/dev/null; then
	echo "workflow must build linux/amd64 only and must not configure QEMU" >&2
	exit 1
fi

uses_count=$(grep -Ehc '^[[:space:]]+uses:' "$github_workflow" "$gitea_workflow" | awk '{ total += $1 } END { print total }')
pinned_count=$(grep -Ehc '^[[:space:]]+uses: [^ ]+@[0-9a-f]{40}([[:space:]]|$)' "$github_workflow" "$gitea_workflow" | awk '{ total += $1 } END { print total }')
if [ "$uses_count" -eq 0 ] || [ "$uses_count" -ne "$pinned_count" ]; then
	echo "every action must be pinned to a full commit SHA" >&2
	exit 1
fi
