# mohomo-docker

Minimal Mihomo service with the ACL4SSR `Online Full MultiMode` routing model. The host publishes the SSClash Web UI on loopback port `9091` and the mixed proxy on loopback port `7890` by default; Mihomo's controller remains private to the container.

## Quick start

```sh
cp .env.example .env
# Generate a password, then set SUBSCRIPTION_URL and SSCLASH_PASSWORD in .env.
openssl rand -base64 24
docker compose up -d --build
docker compose logs -f ssclash
```

The subscription endpoint must return a Clash/Mihomo proxy-provider YAML document (`proxies:`). Use an HTTPS endpoint when its URL contains a credential. A fresh volume refuses to start without an `SSCLASH_PASSWORD` of at least 12 characters; bootstrap uses SSClash's own `setpass` command before the Web listener starts. On the Docker host, open `http://127.0.0.1:9091`, log in with that password, and press **Start**. SSClash then owns the Mihomo process and the Web UI Start/Stop/status controls stay authoritative. A valid existing authentication file is preserved, so later starts do not require or replace the password. Local proxy clients connect to either endpoint:

```text
HTTP proxy:   http://127.0.0.1:7890
SOCKS5 proxy: socks5://127.0.0.1:7890
```

The Compose boundary fixes plaintext `9091` to host loopback. To provide the required external Web access, configure a host HTTPS reverse proxy to `127.0.0.1:${WEB_PORT:-9091}`; for example, a host-native Caddy configuration is:

```caddyfile
ssclash.example.com {
    reverse_proxy 127.0.0.1:9091
}
```

Replace the domain and ensure its DNS reaches the host; Caddy then obtains and serves the TLS certificate. Do not publish 9091 directly as public HTTP.

`WEB_PORT`, `PROXY_BIND`, and `PROXY_PORT` are optional deployment overrides. Port 7890 also defaults to `127.0.0.1`; setting `PROXY_BIND=0.0.0.0` is the explicit public opt-in. The packaged Mihomo proxy has no client authentication, so use that opt-in only when a host firewall or network ACL restricts clients to a trusted range. Prefer binding `PROXY_BIND` to a specific trusted host address.

## Update and secret handling

The bootstrap fetches the subscription once before startup and every hour thereafter. Each candidate is limited to 16 MiB and validated with the active Mihomo binary before an atomic replacement. When Mihomo is running, bootstrap hot-reloads it; when it is stopped, the next Web-managed Start reads the latest provider. A failed reload restores and reloads the previous provider; if that recovery cannot be confirmed, SSClash stops instead of leaving Mihomo in an uncertain state.

The subscription URL and bootstrap administrator password are removed from child-process environments and never printed. The password is persisted only as SSClash's PBKDF2 authentication file. Before opening the Web listener, bootstrap requires that file to be a readable, process-owned regular file with exact mode `0600` and SSClash v6.1.0's expected PBKDF2 format; a missing or abnormal file fails closed. The generated configuration contains only a local provider path. Subscription data lives under `/dev/shm/mohomo`; `/opt/clash/subscription.yaml` is only a managed link to that in-memory file, so neither the image nor the volume stores its URL, response, or node credentials. Container restarts intentionally fetch a fresh subscription instead of persisting credentials.

Do not commit `.env`; it is ignored by Git. Docker still exposes bootstrap environment variables to principals allowed to inspect the container, so restrict Docker daemon access.

## ACL4SSR rules

The image packages ACL4SSR provider files from pinned commit `6e27259b8625e360699c014f98f978ee7408c644`. The archive checksum is pinned in the Dockerfile. Runtime routing uses only those local files—there is no online rule converter or rule-provider download.

The generated groups and rule order mirror `ACL4SSR_Online_Full_MultiMode.ini`: automatic selection, fallback, load balancing, regional selectors, service/media splits, ad rejection, China direct routing, GFW routing, and final fallback.

## Persistent data

`/opt/clash` stores only SSClash settings, the packaged Mihomo core, and the non-secret generated configuration. Bootstrap creates missing files, preserves existing regular non-empty files, enforces `OPERATING_MODE=server` and `PROXY_MODE=none`, and rejects corrupt or ambiguous persistent state. It atomically migrates only the exact legacy packaged `GEOIP,CN` configuration to version 1's local `ChinaIp` rule and records `.mohomo-docker-config-version`; a customized legacy `GEOIP,CN` configuration is preserved and startup fails with an explicit remediation message. On container replacement it repairs only SSClash's exact `rule-providers` and `proxy-providers` links into `SSCLASH_TMP`; unexpected links are rejected without deleting their targets.

## Reproducible inputs

The Dockerfile pins:

- SSClash-Go `v6.1.0`, verified with its release checksum file;
- Mihomo `v1.19.30`, verified with repository-pinned SHA-256 values;
- ACL4SSR rules by commit and archive SHA-256.

The GitHub Actions workflow builds `linux/amd64`, runs tests first, publishes only to private GHCR, and attaches SBOM and provenance. Do not make an image containing SSClash-Go public without permission from its copyright holder.

## Verification

```sh
./scripts/test.sh
./tests/container-smoke.sh
```

The unit suite checks strict fail-closed authentication-file validation, managed-config migration, provider-link recovery, atomic rollback, URL redaction, server-only listeners, local ACL4SSR providers, and at least 65% bootstrap coverage. The container smoke test validates a legacy volume with networking disabled, verifies loopback-only Compose defaults and proxy-only public opt-in, proves 7890/9091 are unreachable through a non-loopback host address, checks fresh-volume authentication and credential isolation, and repeats health and login checks after recreating the container with the same volume.

## License boundary

Original packaging code is MIT licensed. SSClash-Go, Mihomo, and packaged ACL4SSR rule files retain their upstream licenses; the image includes ACL4SSR's CC BY-SA 4.0 text.
