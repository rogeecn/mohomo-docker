#!/bin/sh
set -eu

image=${1:-mohomo-docker:smoke}
suffix=$$
container="mohomo-smoke-${suffix}"
provider="mohomo-provider-${suffix}"
network="mohomo-network-${suffix}"
volume="mohomo-data-${suffix}"
cold_volume="mohomo-cold-${suffix}"
secret_file=$(mktemp "${TMPDIR:-/tmp}/mohomo-secret.XXXXXX")
secret="fake-container-token"

case "$container:$provider:$network:$volume:$cold_volume" in
mohomo-smoke-*':mohomo-provider-'*':mohomo-network-'*':mohomo-data-'*':mohomo-cold-'*) ;;
*) echo "refusing unsafe cleanup targets" >&2; exit 1 ;;
esac

cleanup() {
	docker container rm --force "$container" "$provider" >/dev/null 2>&1 || true
	docker volume rm "$volume" "$cold_volume" >/dev/null 2>&1 || true
	docker network rm "$network" >/dev/null 2>&1 || true
	rm -f "$secret_file"
}
trap cleanup EXIT INT TERM

wait_for_health() {
	attempt=0
	until [ "$(docker inspect --format '{{.State.Health.Status}}' "$container")" = healthy ]; do
		attempt=$((attempt + 1))
		if [ "$attempt" -ge 60 ]; then
			docker logs "$container" >&2
			exit 1
		fi
		sleep 1
	done
}

wait_for_last_good() {
	expected=$1
	attempt=0
	until docker exec "$container" grep -F "$expected" /data/last-good/subscription.yaml >/dev/null 2>&1; do
		attempt=$((attempt + 1))
		if [ "$attempt" -ge 30 ]; then
			docker logs "$container" >&2
			echo "last-good did not contain $expected" >&2
			exit 1
		fi
		sleep 1
	done
}

wait_for_log() {
	expected=$1
	attempt=0
	until docker logs "$container" 2>&1 | grep -F "$expected" >/dev/null; do
		attempt=$((attempt + 1))
		[ "$attempt" -lt 30 ] || { docker logs "$container" >&2; exit 1; }
		sleep 1
	done
}

printf 'http://%s:8080/provider.yaml?token=%s\n' "$provider" "$secret" > "$secret_file"
chmod 0444 "$secret_file"

compose=$(SUBSCRIPTION_FILE="$secret_file" docker compose config)
[ "$(printf '%s\n' "$compose" | grep -c 'target: 7890')" -eq 1 ]
[ "$(printf '%s\n' "$compose" | grep -c 'target: 9090')" -eq 1 ]
[ "$(printf '%s\n' "$compose" | grep -c 'host_ip: 127.0.0.1')" -eq 2 ]
printf '%s\n' "$compose" | grep -F 'read_only: true' >/dev/null
printf '%s\n' "$compose" | grep -F 'source: subscription' >/dev/null
[ "$(printf '%s\n' "$compose" | grep -c 'protocol: tcp')" -eq 2 ]
if [ "$(printf '%s\n' "$compose" | grep -c 'protocol:')" -ne 2 ] || printf '%s\n' "$compose" | grep -F 'target: 9091' >/dev/null; then
	echo "Compose publishes a forbidden port or protocol" >&2
	exit 1
fi

docker build --tag "$image" .
docker network create "$network" >/dev/null
docker volume create "$volume" >/dev/null
docker volume create "$cold_volume" >/dev/null

docker run --detach --rm \
	--name "$provider" \
	--network "$network" \
	--entrypoint /bin/sh \
	"$image" -c 'mkdir /tmp/web; printf "proxies:\n  - name: first-node\n    type: socks5\n    server: 127.0.0.1\n    port: 9\n" > /tmp/web/provider.yaml; printf "%s\n" "#!/bin/sh" "body=\$(cat /tmp/web/provider.yaml)" "length=\$(printf \"%s\" \"\$body\" | wc -c)" "printf \"HTTP/1.1 200 OK\\r\\nContent-Type: text/yaml\\r\\nContent-Length: %s\\r\\nConnection: close\\r\\n\\r\\n\" \"\$length\"" "printf \"%s\" \"\$body\"" > /tmp/handler; chmod +x /tmp/handler; exec nc -lk -p 8080 -e /tmp/handler' >/dev/null

attempt=0
until docker exec "$provider" wget -qO- http://127.0.0.1:8080/provider.yaml | grep -F first-node >/dev/null; do
	attempt=$((attempt + 1))
	[ "$attempt" -lt 20 ] || { echo "subscription fixture did not start" >&2; exit 1; }
	sleep 1
done

if failure=$(docker run --rm \
	--network none \
	--volume "$cold_volume:/data" \
	--mount "type=bind,source=${secret_file},target=/run/secrets/subscription,readonly" \
	"$image" 2>&1); then
	echo "cold start succeeded without a reachable subscription" >&2
	exit 1
fi
if printf '%s\n' "$failure" | grep -F "$secret" >/dev/null; then
	echo "cold-start failure leaked the subscription secret" >&2
	exit 1
fi
docker run --rm --volume "$cold_volume:/data" --entrypoint /bin/sh "$image" -c 'test ! -e /data/last-good'

docker run --detach \
	--name "$container" \
	--network "$network" \
	--read-only \
	--tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m \
	--cap-drop ALL \
	--security-opt no-new-privileges:true \
	--volume "$volume:/data" \
	--mount "type=bind,source=${secret_file},target=/run/secrets/subscription,readonly" \
	--publish 127.0.0.1::7890/tcp \
	--publish 127.0.0.1::9090/tcp \
	"$image" >/dev/null

wait_for_health
wait_for_last_good first-node
published=$(docker port "$container")
[ "$(printf '%s\n' "$published" | wc -l)" -eq 2 ]
printf '%s\n' "$published" | grep -F '7890/tcp -> 127.0.0.1:' >/dev/null
printf '%s\n' "$published" | grep -F '9090/tcp -> 127.0.0.1:' >/dev/null
proxy_port=$(docker port "$container" 7890/tcp | awk -F: 'NR == 1 {print $NF}')
controller_port=$(docker port "$container" 9090/tcp | awk -F: 'NR == 1 {print $NF}')
docker run --rm --network host --entrypoint /bin/sh "$image" -c "nc -z 127.0.0.1 $proxy_port"
curl --fail --silent --show-error "http://127.0.0.1:${controller_port}/version" >/dev/null
curl --fail --silent --show-error "http://127.0.0.1:${controller_port}/ui/" | grep -Fi '<title>' >/dev/null
docker exec "$container" ps | grep -F '/usr/local/bin/mihomo' >/dev/null
if docker exec "$container" touch /read-only-root >/dev/null 2>&1; then
	echo "container root filesystem is writable" >&2
	exit 1
fi
docker logs "$container" 2>&1 | grep -F 'update_interval=1h0m0s' >/dev/null

first_target=$(docker exec "$container" readlink /data/last-good)
docker exec "$provider" /bin/sh -c 'printf "proxies: [" > /tmp/web/provider.yaml'
docker kill --signal HUP "$container" >/dev/null
sleep 2
[ "$(docker exec "$container" readlink /data/last-good)" = "$first_target" ]
wait_for_last_good first-node

docker exec "$provider" /bin/sh -c 'printf "proxies:\n  - name: second-node\n    type: socks5\n    server: 127.0.0.1\n    port: 9\n" > /tmp/web/provider.yaml'
docker kill --signal HUP "$container" >/dev/null
wait_for_last_good second-node
wait_for_log 'configuration updated and reloaded'
curl --fail --silent --show-error "http://127.0.0.1:${controller_port}/providers/proxies/subscription" >/dev/null

docker exec "$provider" /bin/sh -c 'printf "proxies: [" > /tmp/web/provider.yaml'
docker restart "$container" >/dev/null
wait_for_health
wait_for_last_good second-node
if docker logs "$container" 2>&1 | grep -F "$secret" >/dev/null; then
	echo "runtime logs leaked the subscription secret" >&2
	exit 1
fi
docker exec "$container" /usr/local/bin/mihomo -t -d /data/last-good -f /data/last-good/config.yaml >/dev/null

echo "container smoke test passed: cold fail-closed, warm recovery, HUP update, rollback, 7890, 9090, and ExternalUI"
