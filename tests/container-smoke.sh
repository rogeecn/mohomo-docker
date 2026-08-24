#!/bin/sh
set -eu

image=${1:-mohomo-docker:smoke}
suffix="$$"
container="mohomo-docker-smoke-${suffix}"
provider="mohomo-provider-smoke-${suffix}"
network="mohomo-network-smoke-${suffix}"
volume="mohomo-volume-smoke-${suffix}"
provider_dir=""
secret="container-smoke-secret"

case "$container:$provider:$network:$volume" in
mohomo-docker-smoke-*':mohomo-provider-smoke-'*':mohomo-network-smoke-'*':mohomo-volume-smoke-'*) ;;
*) echo "refusing unsafe cleanup targets" >&2; exit 1 ;;
esac

cleanup() {
	docker container rm --force "$container" "$provider" >/dev/null 2>&1 || true
	docker volume rm "$volume" >/dev/null 2>&1 || true
	docker network rm "$network" >/dev/null 2>&1 || true
	[ -z "$provider_dir" ] || rm -rf "$provider_dir"
}
trap cleanup EXIT INT TERM

docker build --tag "$image" .
docker run --rm --entrypoint /usr/local/lib/ssclash/clash "$image" \
	-t -d /usr/local/share/ssclash -f /usr/local/share/ssclash/config.yaml >/dev/null 2>&1 && {
	echo "config validation unexpectedly passed without a subscription provider" >&2
	exit 1
}

provider_dir=$(mktemp -d)
printf '%s\n' \
	'proxies:' \
	'  - name: smoke-node' \
	'    type: socks5' \
	'    server: 127.0.0.1' \
	'    port: 9' \
	> "$provider_dir/provider.yaml"
chmod 0755 "$provider_dir"
chmod 0644 "$provider_dir/provider.yaml"

docker network create "$network" >/dev/null
docker volume create "$volume" >/dev/null
docker run --detach --rm \
	--name "$provider" \
	--network "$network" \
	--volume "$provider_dir:/srv:ro" \
	--entrypoint /bin/sh \
	"$image" -c 'while :; do { printf "HTTP/1.1 200 OK\r\nContent-Type: text/yaml\r\nConnection: close\r\n\r\n"; cat /srv/provider.yaml; } | nc -l -p 8080; done' >/dev/null
attempt=0
until docker exec "$provider" wget -qO- http://127.0.0.1:8080/provider.yaml >/dev/null; do
	attempt=$((attempt + 1))
	if [ "$attempt" -ge 10 ]; then
		echo "subscription fixture did not become ready" >&2
		exit 1
	fi
	sleep 1
done
docker run --detach \
	--name "$container" \
	--network "$network" \
	--cap-drop ALL \
	--security-opt no-new-privileges:true \
	--env "SUBSCRIPTION_URL=http://${provider}:8080/provider.yaml?token=${secret}" \
	--volume "$volume:/opt/clash" \
	--publish 127.0.0.1::7890/tcp \
	--publish 127.0.0.1::7890/udp \
	"$image" >/dev/null

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

published=$(docker port "$container")
printf '%s\n' "$published" | grep -E '^7890/(tcp|udp)' >/dev/null
if printf '%s\n' "$published" | grep -vE '^7890/(tcp|udp)' >/dev/null; then
	echo "container published a port other than 7890" >&2
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

echo "container smoke test passed: only port 7890 published; subscription credential not persisted or logged"
