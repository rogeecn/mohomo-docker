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

gitea_trigger_count=$(awk '/^on:/ { in_on = 1; next } in_on && /^[^ ]/ { exit } in_on && /^  [a-z_]+:/ { count++ } END { print count + 0 }' "$gitea_workflow")
if [ "$gitea_trigger_count" -ne 3 ] || [ "$(grep -Fc '      - main' "$gitea_workflow")" -ne 2 ]; then
	echo "Gitea workflow must only run for main pushes, main pull requests, and manual dispatches" >&2
	exit 1
fi

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
grep -F 'git.ipao.vip/rogee/mohomo-docker' "$gitea_workflow" >/dev/null
# Match Gitea expressions literally.
# shellcheck disable=SC2016
grep -F '${{ secrets.REGISTRY_TOKEN }}' "$gitea_workflow" >/dev/null
# shellcheck disable=SC2016
grep -F 'sha-${{ gitea.sha }}' "$gitea_workflow" >/dev/null
grep -F 'docker login git.ipao.vip' "$gitea_workflow" >/dev/null
grep -F 'docker push "$IMAGE_NAME:latest"' "$gitea_workflow" >/dev/null
grep -F 'api/v1/packages/rogee/container/mohomo-docker' "$gitea_workflow" >/dev/null
grep -F '/link/mohomo-docker' "$gitea_workflow" >/dev/null

publish_condition="if: gitea.event_name != 'pull_request'"
if [ "$(grep -Fc "$publish_condition" "$gitea_workflow")" -ne 3 ]; then
	echo "every Gitea publishing step must be disabled for pull requests" >&2
	exit 1
fi
if grep -Ei 'ghcr\.io|github\.|GITHUB_TOKEN|packages: write|docker/(login|build-push)-action' "$gitea_workflow" >/dev/null; then
	echo "Gitea workflow must not depend on GitHub publishing" >&2
	exit 1
fi

if grep -Ei 'gitea\.|REGISTRY_TOKEN|git\.ipao\.vip' "$github_workflow" >/dev/null; then
	echo "GitHub workflow must not depend on Gitea publishing" >&2
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
