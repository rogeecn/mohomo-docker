#!/bin/sh
set -eu

image=${1:-mohomo-docker:smoke}
suffix="$$"
container="mohomo-docker-smoke-${suffix}"
volume="mohomo-docker-smoke-${suffix}"

case "$container:$volume" in
	mohomo-docker-smoke-*':mohomo-docker-smoke-'*) ;;
	*) echo "refusing unsafe cleanup targets" >&2; exit 1 ;;
esac

cleanup() {
	docker container rm --force "$container" >/dev/null 2>&1 || true
	docker volume rm "$volume" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

docker build --tag "$image" .
docker run --rm --entrypoint /usr/local/lib/ssclash/clash "$image" \
	-t -d /usr/local/share/ssclash

docker volume create "$volume" >/dev/null
docker run --detach \
	--name "$container" \
	--cap-drop ALL \
	--security-opt no-new-privileges:true \
	--volume "$volume:/opt/clash" \
	--publish 127.0.0.1::9091/tcp \
	--publish 127.0.0.1::7890/tcp \
	"$image" >/dev/null

web_port=$(docker port "$container" 9091/tcp | awk -F: 'NR == 1 { print $NF }')
proxy_port=$(docker port "$container" 7890/tcp | awk -F: 'NR == 1 { print $NF }')
test -n "$web_port"
test -n "$proxy_port"

attempt=0
until curl --fail --silent --show-error "http://127.0.0.1:${web_port}/" >/dev/null; do
	attempt=$((attempt + 1))
	if [ "$attempt" -ge 30 ]; then
		docker logs "$container" >&2
		echo "web UI did not become ready" >&2
		exit 1
	fi
	sleep 1
done

docker exec "$container" grep -Fx 'OPERATING_MODE=server' /opt/clash/.ssclash/settings >/dev/null
docker exec "$container" grep -Fx 'mixed-port: 7890' /opt/clash/config.yaml >/dev/null
docker exec --detach "$container" /opt/clash/bin/clash -d /opt/clash

attempt=0
until curl --fail --silent --show-error \
	--proxy "http://127.0.0.1:${proxy_port}" \
	--max-time 10 \
	https://example.com/ >/dev/null; do
	attempt=$((attempt + 1))
	if [ "$attempt" -ge 20 ]; then
		docker logs "$container" >&2
		echo "mixed proxy did not become ready" >&2
		exit 1
	fi
	sleep 1
done

echo "container smoke test passed: web_port=${web_port} proxy_port=${proxy_port}"
