#!/bin/sh
set -eu

project_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$project_root"

./tests/workflow-contract.sh

unformatted=$(gofmt -l cmd internal)
if [ -n "$unformatted" ]; then
	echo "Go files require formatting:" >&2
	echo "$unformatted" >&2
	exit 1
fi

go test -race -coverprofile=coverage.out ./internal/...
coverage=$(go tool cover -func=coverage.out | awk '/^total:/ { gsub(/%/, "", $3); print $3 }')
awk -v coverage="$coverage" 'BEGIN { if (coverage + 0 < 65) exit 1 }' || {
	echo "unit test coverage ${coverage}% is below required 65%" >&2
	exit 1
}
echo "unit test coverage: ${coverage}%"

go test ./...

if command -v docker >/dev/null 2>&1; then
	docker compose config --quiet
fi
