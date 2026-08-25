#!/bin/sh
set -eu

image=${1:-mohomo-docker:smoke}
suffix="$$"
container="mohomo-docker-smoke-${suffix}"
unconfigured="mohomo-docker-unconfigured-${suffix}"
legacy_container="mohomo-docker-legacy-${suffix}"
provider="mohomo-provider-smoke-${suffix}"
network="mohomo-network-smoke-${suffix}"
volume="mohomo-volume-smoke-${suffix}"
legacy_volume="mohomo-legacy-volume-smoke-${suffix}"
secret="container-smoke-secret"
admin_password="container-smoke-admin-password"

case "$container:$unconfigured:$legacy_container:$provider:$network:$volume:$legacy_volume" in
mohomo-docker-smoke-*':mohomo-docker-unconfigured-'*':mohomo-docker-legacy-'*':mohomo-provider-smoke-'*':mohomo-network-smoke-'*':mohomo-volume-smoke-'*':mohomo-legacy-volume-smoke-'*) ;;
*) echo "refusing unsafe cleanup targets" >&2; exit 1 ;;
esac

cleanup() {
	docker container rm --force "$container" "$unconfigured" "$legacy_container" "$provider" >/dev/null 2>&1 || true
	docker volume rm "$volume" "$legacy_volume" >/dev/null 2>&1 || true
	docker network rm "$network" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

wait_for_health() {
	attempt=0
	until [ "$(docker inspect --format '{{.State.Health.Status}}' "$container")" = healthy ]; do
		attempt=$((attempt + 1))
		if [ "$attempt" -ge 30 ]; then
			docker logs "$container" >&2
			echo "container did not become healthy" >&2
			exit 1
		fi
		sleep 1
	done
}

assert_published_ports() {
	published=$(docker port "$container")
	for port in 7890/tcp 7890/udp 9091/tcp; do
		printf '%s\n' "$published" | grep -F "$port ->" >/dev/null
	done
	if printf '%s\n' "$published" | grep -vE '^(7890/(tcp|udp)|9091/tcp)' >/dev/null; then
		echo "container published a port other than 7890 or 9091" >&2
		exit 1
	fi
}

assert_web_login_and_start() {
	web_port=$1
	docker run --rm --network host \
		--env "WEB_PORT=${web_port}" \
		--env "ADMIN_PASSWORD=${admin_password}" \
		--entrypoint /bin/sh \
		"$image" -c '
			set -eu
			cookie=$(mktemp)
			setup_redirect=$(curl --silent --show-error --output /dev/null \
				--write-out "%{http_code} %{redirect_url}" \
				"http://127.0.0.1:${WEB_PORT}/setup")
			if [ "$setup_redirect" != "303 http://127.0.0.1:${WEB_PORT}/login" ]; then
				echo "configured Web UI exposed setup: ${setup_redirect}" >&2
				exit 1
			fi
			login_html=$(curl --fail --silent --show-error --cookie-jar "$cookie" \
				"http://127.0.0.1:${WEB_PORT}/login")
			login_csrf=$(printf "%s" "$login_html" | sed -n "s/.*name=\"csrf\" value=\"\([^\"]*\)\".*/\1/p" | head -1)
			test -n "$login_csrf"
			curl --fail --silent --show-error \
				--cookie "$cookie" \
				--cookie-jar "$cookie" \
				--request POST \
				--data-urlencode "csrf=${login_csrf}" \
				--data-urlencode "password=${ADMIN_PASSWORD}" \
				"http://127.0.0.1:${WEB_PORT}/login" >/dev/null
			config_html=$(curl --fail --silent --show-error --cookie "$cookie" \
				"http://127.0.0.1:${WEB_PORT}/config")
			api_csrf=$(printf "%s" "$config_html" | sed -n "s/.*name=\"csrf-token\" content=\"\([^\"]*\)\".*/\1/p" | head -1)
			test -n "$api_csrf"
			curl --fail --silent --show-error \
				--cookie "$cookie" \
				--header "X-CSRF-Token: ${api_csrf}" \
				--header "Content-Type: application/json" \
				--data "{\"action\":\"start\"}" \
				"http://127.0.0.1:${WEB_PORT}/api/service" | grep -F "\"ok\":true" >/dev/null
			curl --fail --silent --show-error --cookie "$cookie" \
				"http://127.0.0.1:${WEB_PORT}/api/status" | grep -F "\"running\":true" >/dev/null
		'
}

default_compose=$(SUBSCRIPTION_URL=https://subscription.example.invalid/mihomo docker compose config)
loopback_bindings=$(printf '%s\n' "$default_compose" | awk '$1 == "host_ip:" && $2 == "127.0.0.1" { count++ } END { print count + 0 }')
if [ "$loopback_bindings" -ne 3 ]; then
	echo "Compose must bind 7890/tcp, 7890/udp, and 9091/tcp to host loopback by default" >&2
	exit 1
fi

public_proxy_compose=$(SUBSCRIPTION_URL=https://subscription.example.invalid/mihomo \
	PROXY_BIND=0.0.0.0 WEB_BIND=0.0.0.0 docker compose config)
public_bindings=$(printf '%s\n' "$public_proxy_compose" | awk '$1 == "host_ip:" && $2 == "0.0.0.0" { count++ } END { print count + 0 }')
loopback_bindings=$(printf '%s\n' "$public_proxy_compose" | awk '$1 == "host_ip:" && $2 == "127.0.0.1" { count++ } END { print count + 0 }')
if [ "$public_bindings" -ne 2 ] || [ "$loopback_bindings" -ne 1 ]; then
	echo "public opt-in must affect only 7890; 9091 must remain on host loopback" >&2
	exit 1
fi

docker build --tag "$image" .
docker run --rm --network none --entrypoint /bin/sh "$image" -c '
	set -eu
	runtime=$(mktemp -d)
	cp /usr/local/share/ssclash/config.yaml "$runtime/config.yaml"
	printf "proxies:\n  - name: smoke-node\n    type: socks5\n    server: 127.0.0.1\n    port: 9\n" > "$runtime/subscription.yaml"
	/usr/local/lib/ssclash/clash -t -d "$runtime" -f "$runtime/config.yaml"
' >/dev/null

docker volume create "$legacy_volume" >/dev/null
docker run --rm \
	--volume "$legacy_volume:/opt/clash" \
	--entrypoint /bin/sh \
	"$image" -c '
		set -eu
		grep -v -F "  ChinaIp: {type: file, behavior: ipcidr, format: yaml, path: /usr/local/share/ssclash/rules/ChinaIp.yaml}" \
			/usr/local/share/ssclash/config.yaml \
			| sed "s/^  - RULE-SET,ChinaIp,/  - GEOIP,CN,/" \
			> /opt/clash/config.yaml
		grep -F "  - GEOIP,CN,🎯 全球直连" /opt/clash/config.yaml >/dev/null
	'
if docker run \
	--name "$legacy_container" \
	--network none \
	--env "SUBSCRIPTION_URL=http://127.0.0.1:9/provider.yaml" \
	--env "SSCLASH_PASSWORD=${admin_password}" \
	--volume "$legacy_volume:/opt/clash" \
	"$image" >/dev/null 2>&1; then
	echo "legacy volume unexpectedly started without a subscription network" >&2
	exit 1
fi
docker logs "$legacy_container" 2>&1 | grep -F 'initial subscription update failed: subscription request failed' >/dev/null
if docker logs "$legacy_container" 2>&1 | grep -F 'geoip.metadb' >/dev/null; then
	echo "legacy managed config attempted a GeoIP download" >&2
	exit 1
fi
docker run --rm --network none \
	--volume "$legacy_volume:/opt/clash" \
	--entrypoint /bin/sh \
	"$image" -c '
		set -eu
		cmp /opt/clash/config.yaml /usr/local/share/ssclash/config.yaml
		test "$(cat /opt/clash/.mohomo-docker-config-version)" = 1
		runtime=$(mktemp -d)
		cp /opt/clash/config.yaml "$runtime/config.yaml"
		printf "proxies:\n  - name: smoke-node\n    type: socks5\n    server: 127.0.0.1\n    port: 9\n" > "$runtime/subscription.yaml"
		/usr/local/lib/ssclash/clash -t -d "$runtime" -f "$runtime/config.yaml"
	' >/dev/null

docker network create "$network" >/dev/null
docker volume create "$volume" >/dev/null
docker run --detach --rm \
	--name "$provider" \
	--network "$network" \
	--entrypoint /bin/sh \
	"$image" -c 'while :; do printf "HTTP/1.1 200 OK\r\nContent-Type: text/yaml\r\nConnection: close\r\n\r\nproxies:\n  - name: smoke-node\n    type: socks5\n    server: 127.0.0.1\n    port: 9\n" | nc -l -p 8080; done' >/dev/null
attempt=0
until docker exec "$provider" wget -qO- http://127.0.0.1:8080/provider.yaml | grep -F 'name: smoke-node' >/dev/null; do
	attempt=$((attempt + 1))
	if [ "$attempt" -ge 10 ]; then
		echo "subscription fixture did not become ready" >&2
		exit 1
	fi
	sleep 1
done

docker run --detach \
	--name "$unconfigured" \
	--network "$network" \
	--cap-drop ALL \
	--security-opt no-new-privileges:true \
	--env "SUBSCRIPTION_URL=http://${provider}:8080/provider.yaml" \
	--volume "$volume:/opt/clash" \
	--publish 127.0.0.1::9091/tcp \
	"$image" >/dev/null
unconfigured_port=$(docker port "$unconfigured" 9091/tcp | awk -F: 'NR == 1 { print $NF }')
attempt=0
while [ "$(docker inspect --format '{{.State.Running}}' "$unconfigured")" = true ]; do
	if curl --fail --silent --show-error --max-time 1 \
		"http://127.0.0.1:${unconfigured_port}/setup" >/dev/null 2>&1; then
		echo "fresh volume exposed anonymous setup" >&2
		exit 1
	fi
	attempt=$((attempt + 1))
	if [ "$attempt" -ge 10 ]; then
		echo "fresh volume did not fail closed without SSCLASH_PASSWORD" >&2
		exit 1
	fi
	sleep 1
done
if [ "$(docker inspect --format '{{.State.ExitCode}}' "$unconfigured")" -eq 0 ]; then
	echo "fresh volume exited successfully without SSCLASH_PASSWORD" >&2
	exit 1
fi
docker logs "$unconfigured" 2>&1 | grep -F 'SSCLASH_PASSWORD is required' >/dev/null
if docker logs "$unconfigured" 2>&1 | grep -F 'web UI listening' >/dev/null; then
	echo "fresh volume started the Web UI before authentication was configured" >&2
	exit 1
fi
docker container rm "$unconfigured" >/dev/null

docker run --detach \
	--name "$container" \
	--network "$network" \
	--cap-drop ALL \
	--security-opt no-new-privileges:true \
	--env "SUBSCRIPTION_URL=http://${provider}:8080/provider.yaml?token=${secret}" \
	--env "SSCLASH_PASSWORD=${admin_password}" \
	--volume "$volume:/opt/clash" \
	--publish 127.0.0.1::7890/tcp \
	--publish 127.0.0.1::7890/udp \
	--publish 127.0.0.1::9091/tcp \
	"$image" >/dev/null

wait_for_health
assert_published_ports
web_port=$(docker port "$container" 9091/tcp | awk -F: 'NR == 1 { print $NF }')
assert_web_login_and_start "$web_port"

host_gateway=$(docker network inspect "$network" --format '{{(index .IPAM.Config 0).Gateway}}')
container_ip=$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$container")
proxy_port=$(docker port "$container" 7890/tcp | awk -F: 'NR == 1 { print $NF }')
if curl --fail --silent --show-error --max-time 2 --noproxy "" \
	--proxy "http://${host_gateway}:${proxy_port}" \
	"http://${container_ip}:9090/version" >/dev/null 2>&1; then
	echo "default 7890 publish was reachable through a non-loopback host address" >&2
	exit 1
fi
if curl --fail --silent --show-error --max-time 2 \
	"http://${host_gateway}:${web_port}/login" >/dev/null 2>&1; then
	echo "default 9091 publish was reachable through a non-loopback host address" >&2
	exit 1
fi

docker exec "$container" grep -Fx 'OPERATING_MODE=server' /opt/clash/.ssclash/settings >/dev/null
docker exec "$container" grep -Fx 'PROXY_MODE=none' /opt/clash/.ssclash/settings >/dev/null
docker exec "$container" test -s /dev/shm/mohomo/subscription.yaml
docker exec "$container" /usr/local/lib/ssclash/clash \
	-t -d /dev/shm/mohomo -f /dev/shm/mohomo/config.yaml >/dev/null
docker exec "$container" curl --fail --silent --show-error \
	http://127.0.0.1:9090/version >/dev/null

if docker exec "$container" grep -R -F "$secret" /opt/clash /dev/shm/mohomo >/dev/null 2>&1; then
	echo "subscription URL credential was written to runtime files" >&2
	exit 1
fi
if docker logs "$container" 2>&1 | grep -F "$secret" >/dev/null; then
	echo "subscription URL credential was written to logs" >&2
	exit 1
fi
if docker exec "$container" grep -R -F "$admin_password" /opt/clash /dev/shm/mohomo >/dev/null 2>&1; then
	echo "administrator password was written to runtime files" >&2
	exit 1
fi
if docker logs "$container" 2>&1 | grep -F "$admin_password" >/dev/null; then
	echo "administrator password was written to logs" >&2
	exit 1
fi

docker container rm --force "$container" >/dev/null
docker run --rm \
	--volume "$volume:/opt/clash" \
	--entrypoint /bin/sh \
	"$image" -c 'test -L /opt/clash/rule-providers && test ! -e /opt/clash/rule-providers && test "$(readlink /opt/clash/rule-providers)" = /tmp/ssclash/rule-providers'

docker run --detach \
	--name "$container" \
	--network "$network" \
	--cap-drop ALL \
	--security-opt no-new-privileges:true \
	--env "SUBSCRIPTION_URL=http://${provider}:8080/provider.yaml?token=${secret}" \
	--volume "$volume:/opt/clash" \
	--publish 127.0.0.1::7890/tcp \
	--publish 127.0.0.1::7890/udp \
	--publish 127.0.0.1::9091/tcp \
	"$image" >/dev/null

wait_for_health
assert_published_ports
web_port=$(docker port "$container" 9091/tcp | awk -F: 'NR == 1 { print $NF }')
assert_web_login_and_start "$web_port"

echo "container smoke test passed: legacy config migrates offline; fresh volume fails closed; authenticated 9091 survives same-volume rebuild; only 7890/9091 are published"
