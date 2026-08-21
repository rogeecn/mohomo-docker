#!/bin/sh
set -eu

image=${1:-mohomo-docker:smoke}
suffix="$$"
container="mohomo-docker-smoke-${suffix}"
volume="mohomo-docker-smoke-${suffix}"
cookie=""
login_html=""
config_html=""

case "$container:$volume" in
	mohomo-docker-smoke-*':mohomo-docker-smoke-'*) ;;
	*) echo "refusing unsafe cleanup targets" >&2; exit 1 ;;
esac

cleanup() {
	docker container rm --force "$container" >/dev/null 2>&1 || true
	docker volume rm "$volume" >/dev/null 2>&1 || true
	[ -z "$cookie" ] || rm -f "$cookie"
	[ -z "$login_html" ] || rm -f "$login_html"
	[ -z "$config_html" ] || rm -f "$config_html"
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
docker exec "$container" grep -Fx 'PROXY_MODE=none' /opt/clash/.ssclash/settings >/dev/null
docker exec "$container" grep -Fx 'mixed-port: 7890' /opt/clash/config.yaml >/dev/null

docker exec "$container" /usr/local/bin/ssclash setpass container-smoke-only >/dev/null
cookie=$(mktemp)
login_html=$(mktemp)
config_html=$(mktemp)
curl --fail --silent --show-error --cookie-jar "$cookie" \
	"http://127.0.0.1:${web_port}/login" > "$login_html"
login_csrf=$(sed -n 's/.*name="csrf" value="\([^"]*\)".*/\1/p' "$login_html" | head -1)
test -n "$login_csrf"
curl --fail --silent --show-error \
	--cookie "$cookie" \
	--cookie-jar "$cookie" \
	--request POST \
	--data-urlencode "csrf=${login_csrf}" \
	--data-urlencode 'password=container-smoke-only' \
	"http://127.0.0.1:${web_port}/login" >/dev/null
curl --fail --silent --show-error \
	--cookie "$cookie" \
	"http://127.0.0.1:${web_port}/config" > "$config_html"
api_csrf=$(sed -n 's/.*name="csrf-token" content="\([^"]*\)".*/\1/p' "$config_html" | head -1)
test -n "$api_csrf"

start_response=$(curl --fail --silent --show-error \
	--cookie "$cookie" \
	--header "X-CSRF-Token: ${api_csrf}" \
	--header 'Content-Type: application/json' \
	--data '{"action":"start"}' \
	"http://127.0.0.1:${web_port}/api/service")
printf '%s' "$start_response" | grep -F '"ok":true' >/dev/null

status_response=$(curl --fail --silent --show-error \
	--cookie "$cookie" \
	--header "X-CSRF-Token: ${api_csrf}" \
	"http://127.0.0.1:${web_port}/api/status")
printf '%s' "$status_response" | grep -F '"running":true' >/dev/null
printf '%s' "$status_response" | grep -F '"operatingMode":"server"' >/dev/null

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

if docker exec "$container" grep -Eq '^(tproxy-port|redir-port|tun):' /opt/clash/config.yaml; then
	docker logs "$container" >&2
	echo "gateway listener leaked into server-only config" >&2
	exit 1
fi
if docker logs "$container" 2>&1 | grep -Ei '\[(error|fatal)\]|operation not permitted' >/dev/null; then
	docker logs "$container" >&2
	echo "container emitted an error during Web-managed startup" >&2
	exit 1
fi

echo "container smoke test passed: web_port=${web_port} proxy_port=${proxy_port}"
