# mohomo-docker

Minimal Mihomo service with the ACL4SSR `Online Full MultiMode` routing model. The host exposes the SSClash Web UI on port `9091` and the mixed proxy on port `7890`; Mihomo's controller remains private to the container.

## Quick start

```sh
cp .env.example .env
# Generate a password, then set SUBSCRIPTION_URL and SSCLASH_PASSWORD in .env.
openssl rand -base64 24
docker compose up -d --build
docker compose logs -f ssclash
```

The subscription endpoint must return a Clash/Mihomo proxy-provider YAML document (`proxies:`). Use an HTTPS endpoint when its URL contains a credential. A fresh volume refuses to start without an `SSCLASH_PASSWORD` of at least 12 characters; bootstrap uses SSClash's own `setpass` command before the Web listener starts. Open `http://<server>:9091` and log in with that password. A valid existing authentication file is preserved, so later starts do not require or replace the password. Proxy clients connect to either endpoint:

```text
HTTP proxy:   http://<server>:7890
SOCKS5 proxy: socks5://<server>:7890
```

`WEB_BIND`, `WEB_PORT`, `PROXY_BIND`, and `PROXY_PORT` are optional deployment overrides; both services bind all host interfaces by default. Authentication prevents anonymous first-run setup, but the Web UI still serves plain HTTP: place it behind HTTPS and additional access control before exposing it to the Internet. Configure Mihomo proxy authentication before publishing port `7890` outside a trusted network.

## Update and secret handling

The bootstrap fetches the subscription once before startup and every hour thereafter. Each candidate is limited to 16 MiB and validated with the packaged Mihomo binary before an atomic replacement and hot reload. A failed reload restores and reloads the previous provider; if that recovery cannot be confirmed, both services stop instead of running with uncertain state.

The subscription URL and bootstrap administrator password are removed from child-process environments and never printed. The password is persisted only as SSClash's PBKDF2 authentication file. Before opening the Web listener, bootstrap requires that file to be a readable, process-owned regular file with exact mode `0600` and SSClash v6.1.0's expected PBKDF2 format; a missing or abnormal file fails closed. The generated configuration contains only a local provider path. Subscription data lives under `/dev/shm/mohomo`, so neither the image nor the `/opt/clash` volume stores its URL, response, or node credentials. Container restarts intentionally fetch a fresh subscription instead of persisting credentials.

Do not commit `.env`; it is ignored by Git. Docker still exposes bootstrap environment variables to principals allowed to inspect the container, so restrict Docker daemon access.

## ACL4SSR rules

The image packages ACL4SSR provider files from pinned commit `6e27259b8625e360699c014f98f978ee7408c644`. The archive checksum is pinned in the Dockerfile. Runtime routing uses only those local files—there is no online rule converter or rule-provider download.

The generated groups and rule order mirror `ACL4SSR_Online_Full_MultiMode.ini`: automatic selection, fallback, load balancing, regional selectors, service/media splits, ad rejection, China direct routing, GFW routing, and final fallback.

## Persistent data

`/opt/clash` stores only SSClash settings, the packaged Mihomo core, and the non-secret generated configuration. Bootstrap creates missing files, preserves existing regular non-empty files, enforces `OPERATING_MODE=server` and `PROXY_MODE=none`, and rejects corrupt or ambiguous persistent state. On container replacement it repairs only SSClash's exact `rule-providers` and `proxy-providers` links into `SSCLASH_TMP`; unexpected links are rejected without deleting their targets.

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

The unit suite checks strict fail-closed authentication-file validation, provider-link recovery, atomic rollback, URL redaction, server-only listeners, local ACL4SSR providers, and at least 65% bootstrap coverage. The container smoke test verifies that a fresh volume without an administrator password never starts the Web UI, logs in through published port `9091`, checks the exact `7890`/`9091` port set, confirms that plaintext credentials are neither persisted nor logged, and repeats health and login checks after recreating the container with the same volume.

## License boundary

Original packaging code is MIT licensed. SSClash-Go, Mihomo, and packaged ACL4SSR rule files retain their upstream licenses; the image includes ACL4SSR's CC BY-SA 4.0 text.
