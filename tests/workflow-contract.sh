#!/bin/sh
set -eu

workflow=.github/workflows/docker.yml
test -f "$workflow" || {
	echo "missing GHCR workflow: $workflow" >&2
	exit 1
}

grep -F 'packages: write' "$workflow" >/dev/null
# Match the GitHub expression literally.
# shellcheck disable=SC2016
grep -F 'ghcr.io/${{ github.repository }}' "$workflow" >/dev/null
grep -F 'platforms: linux/amd64' "$workflow" >/dev/null
grep -F 'needs: test' "$workflow" >/dev/null
grep -F 'run: ./tests/container-smoke.sh' "$workflow" >/dev/null
grep -F 'cache-to: type=gha,mode=max,ignore-error=true' "$workflow" >/dev/null
# Match the GitHub expression literally.
# shellcheck disable=SC2016
grep -F 'push: ${{ github.event_name != '\''pull_request'\'' }}' "$workflow" >/dev/null

if grep -Ei 'arm64|setup-qemu' "$workflow" >/dev/null; then
	echo "workflow must build linux/amd64 only and must not configure QEMU" >&2
	exit 1
fi

uses_count=$(grep -Ec '^[[:space:]]+uses:' "$workflow")
pinned_count=$(grep -Ec '^[[:space:]]+uses: [^ ]+@[0-9a-f]{40}([[:space:]]|$)' "$workflow")
if [ "$uses_count" -eq 0 ] || [ "$uses_count" -ne "$pinned_count" ]; then
	echo "every GitHub Action must be pinned to a full commit SHA" >&2
	exit 1
fi
